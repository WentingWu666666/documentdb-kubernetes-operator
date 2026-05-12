// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package workload

import (
	"sync"
	"testing"
	"time"
)

func TestNewMetrics_StartTimeRecent(t *testing.T) {
	before := time.Now()
	m := NewMetrics()
	after := time.Now()
	if m.StartTime.Before(before) || m.StartTime.After(after) {
		t.Errorf("StartTime=%v not within [%v, %v]", m.StartTime, before, after)
	}
}

func TestSnapshot_ReadsAllCounters(t *testing.T) {
	m := NewMetrics()
	m.WriteAttempted.Store(10)
	m.WriteAcknowledged.Store(8)
	m.WriteFailed.Store(2)
	m.VerifyPasses.Store(3)
	m.VerifyGapsDetected.Store(1)
	m.ChecksumErrors.Store(0)

	time.Sleep(2 * time.Millisecond)
	s := m.Snapshot()
	if s.WriteAttempted != 10 || s.WriteAcknowledged != 8 || s.WriteFailed != 2 {
		t.Errorf("write counters wrong: %+v", s)
	}
	if s.VerifyPasses != 3 || s.GapsDetected != 1 || s.ChecksumErrors != 0 {
		t.Errorf("verify counters wrong: %+v", s)
	}
	if s.Elapsed <= 0 {
		t.Errorf("Elapsed=%v, want positive", s.Elapsed)
	}
}

func TestWriteSuccessRate(t *testing.T) {
	cases := []struct {
		name      string
		attempted int64
		acked     int64
		want      float64
	}{
		{"no attempts returns 1.0", 0, 0, 1.0},
		{"all succeeded", 100, 100, 1.0},
		{"none succeeded", 100, 0, 0.0},
		{"half succeeded", 100, 50, 0.5},
		{"acked exceeds attempted (sanity)", 10, 12, 1.2}, // not clamped; document behavior
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := MetricsSnapshot{WriteAttempted: tc.attempted, WriteAcknowledged: tc.acked}
			if got := s.WriteSuccessRate(); got != tc.want {
				t.Errorf("WriteSuccessRate=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasDataLoss(t *testing.T) {
	cases := []struct {
		name      string
		gaps      int64
		checksums int64
		want      bool
	}{
		{"clean", 0, 0, false},
		{"one gap", 1, 0, true},
		{"one checksum mismatch", 0, 1, true},
		{"both", 5, 7, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := MetricsSnapshot{GapsDetected: tc.gaps, ChecksumErrors: tc.checksums}
			if got := s.HasDataLoss(); got != tc.want {
				t.Errorf("HasDataLoss=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestMetrics_ConcurrentAtomicSafety(t *testing.T) {
	// All counter mutations happen across goroutines in production
	// (writer + verifier + scheduler). Verify the atomic.Int64 fields
	// don't race under concurrent increment + Snapshot reads.
	m := NewMetrics()
	const writers = 8
	const perWriter = 1000

	var wg sync.WaitGroup
	wg.Add(writers + 1)

	// concurrent writers
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				m.WriteAttempted.Add(1)
				m.WriteAcknowledged.Add(1)
			}
		}()
	}
	// concurrent reader
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = m.Snapshot()
		}
	}()
	wg.Wait()

	s := m.Snapshot()
	if want := int64(writers * perWriter); s.WriteAttempted != want || s.WriteAcknowledged != want {
		t.Errorf("counters lost increments: attempted=%d acked=%d want=%d", s.WriteAttempted, s.WriteAcknowledged, want)
	}
}
