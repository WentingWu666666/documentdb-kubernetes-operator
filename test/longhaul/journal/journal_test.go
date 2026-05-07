// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package journal

import (
	"sync"
	"testing"
	"time"
)

func TestJournal_RecordAndEvents(t *testing.T) {
	j := New()
	j.Info("test", "first")
	j.Warn("test", "second")
	j.Error("test", "third")

	events := j.Events()
	if len(events) != 3 {
		t.Fatalf("Events() len = %d, want 3", len(events))
	}
	if events[0].Level != LevelInfo || events[1].Level != LevelWarn || events[2].Level != LevelError {
		t.Errorf("levels not preserved: got %v %v %v", events[0].Level, events[1].Level, events[2].Level)
	}
	if j.Len() != 3 {
		t.Errorf("Len() = %d, want 3", j.Len())
	}
}

func TestJournal_EventsSince(t *testing.T) {
	j := New()
	j.Info("test", "before")
	cutoff := time.Now()
	time.Sleep(2 * time.Millisecond)
	j.Info("test", "after1")
	j.Info("test", "after2")

	got := j.EventsSince(cutoff)
	if len(got) != 2 {
		t.Errorf("EventsSince got %d events, want 2", len(got))
	}
}

func TestJournal_DisruptionWindowLifecycle(t *testing.T) {
	j := New()
	policy := OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 10}

	if j.ActiveWindow() != nil {
		t.Fatal("expected no active window before Open")
	}

	j.OpenDisruptionWindow("scale-up", policy)
	w := j.ActiveWindow()
	if w == nil || w.OperationName != "scale-up" || !w.IsActive() {
		t.Fatalf("active window not set correctly: %+v", w)
	}

	j.RecordWriteFailure()
	j.RecordWriteFailure()
	j.RecordWriteFailure()
	if w := j.ActiveWindow(); w.WriteFailures != 3 {
		t.Errorf("WriteFailures = %d, want 3", w.WriteFailures)
	}

	j.CloseDisruptionWindow()
	if j.ActiveWindow() != nil {
		t.Fatal("active window should be nil after Close")
	}
	closed := j.DisruptionWindows()
	if len(closed) != 1 || closed[0].WriteFailures != 3 || closed[0].IsActive() {
		t.Errorf("closed windows wrong: %+v", closed)
	}
}

func TestJournal_OpenWhileActiveClosesPrevious(t *testing.T) {
	j := New()
	j.OpenDisruptionWindow("op1", DefaultOutagePolicy())
	j.OpenDisruptionWindow("op2", DefaultOutagePolicy())
	if w := j.ActiveWindow(); w == nil || w.OperationName != "op2" {
		t.Errorf("active window = %+v, want op2", w)
	}
	closed := j.DisruptionWindows()
	if len(closed) != 1 || closed[0].OperationName != "op1" {
		t.Errorf("closed windows = %+v, want [op1]", closed)
	}
}

func TestJournal_RecordWriteFailureWithoutWindowIsNoop(t *testing.T) {
	j := New()
	j.RecordWriteFailure() // no panic
}

func TestJournal_HasPolicyViolation(t *testing.T) {
	t.Run("no windows", func(t *testing.T) {
		j := New()
		if j.HasPolicyViolation() {
			t.Error("HasPolicyViolation() = true, want false on empty journal")
		}
	})
	t.Run("closed window within budget", func(t *testing.T) {
		j := New()
		j.OpenDisruptionWindow("op", OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 10})
		j.CloseDisruptionWindow()
		if j.HasPolicyViolation() {
			t.Error("HasPolicyViolation() = true on within-budget window")
		}
	})
	t.Run("closed window over write-failure budget", func(t *testing.T) {
		j := New()
		j.OpenDisruptionWindow("op", OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 1})
		j.RecordWriteFailure()
		j.RecordWriteFailure()
		j.CloseDisruptionWindow()
		if !j.HasPolicyViolation() {
			t.Error("HasPolicyViolation() = false on over-budget window")
		}
	})
	t.Run("active window over time budget", func(t *testing.T) {
		j := New()
		j.OpenDisruptionWindow("op", OutagePolicy{MustRecoverWithin: time.Nanosecond, AllowedWriteFailures: 10})
		time.Sleep(1 * time.Millisecond)
		if !j.HasPolicyViolation() {
			t.Error("HasPolicyViolation() = false on active over-time window")
		}
	})
}

func TestJournal_ConcurrentAppendIsRaceFree(t *testing.T) {
	// Run with: go test -race
	j := New()
	var wg sync.WaitGroup
	const writers = 8
	const perWriter = 100
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < perWriter; k++ {
				j.Info("c", "x")
			}
		}()
	}
	wg.Wait()
	if got := j.Len(); got != writers*perWriter {
		t.Errorf("Len() = %d, want %d", got, writers*perWriter)
	}
}
