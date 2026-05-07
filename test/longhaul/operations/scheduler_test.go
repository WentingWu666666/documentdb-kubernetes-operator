// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
)

// fakeOp is a minimal Operation for scheduler tests.
type fakeOp struct {
	name      string
	weight    int
	available bool
	executed  int
	err       error
}

func (f *fakeOp) Name() string  { return f.name }
func (f *fakeOp) Weight() int   { return f.weight }
func (f *fakeOp) Precondition(_ context.Context) (bool, string) {
	if f.available {
		return true, ""
	}
	return false, "precondition not met"
}
func (f *fakeOp) Execute(_ context.Context) error {
	f.executed++
	return f.err
}
func (f *fakeOp) OutagePolicy() journal.OutagePolicy { return journal.DefaultOutagePolicy() }

func newSchedulerForTest(ops ...Operation) *Scheduler {
	return &Scheduler{
		operations: ops,
		journal:    journal.New(),
		cooldown:   time.Hour,
	}
}

func TestScheduler_SelectOperation_NoCandidates(t *testing.T) {
	s := newSchedulerForTest(&fakeOp{name: "a", weight: 1, available: false})
	if got := s.selectOperation(context.Background()); got != nil {
		t.Errorf("selectOperation returned %v, want nil when no candidates pass precondition", got)
	}
}

func TestScheduler_SelectOperation_ZeroWeightSkipped(t *testing.T) {
	a := &fakeOp{name: "a", weight: 0, available: true}
	s := newSchedulerForTest(a)
	if got := s.selectOperation(context.Background()); got != nil {
		t.Errorf("selectOperation returned %v, want nil when total weight is zero", got)
	}
}

func TestScheduler_SelectOperation_OnlyEligibleReturned(t *testing.T) {
	a := &fakeOp{name: "a", weight: 1, available: false}
	b := &fakeOp{name: "b", weight: 1, available: true}
	s := newSchedulerForTest(a, b)
	for i := 0; i < 50; i++ {
		got := s.selectOperation(context.Background())
		if got == nil || got.Name() != "b" {
			t.Fatalf("expected only b to be selected, got %v on iter %d", got, i)
		}
	}
}

func TestScheduler_SelectOperation_WeightedDistribution(t *testing.T) {
	// a:weight=1, b:weight=9 -> b should be selected ~90% of the time.
	a := &fakeOp{name: "a", weight: 1, available: true}
	b := &fakeOp{name: "b", weight: 9, available: true}
	s := newSchedulerForTest(a, b)

	const trials = 2000
	bCount := 0
	for i := 0; i < trials; i++ {
		if s.selectOperation(context.Background()).Name() == "b" {
			bCount++
		}
	}
	// Expected 0.9 * 2000 = 1800; allow generous ±10% (180) for randomness.
	if bCount < 1620 || bCount > 1980 {
		t.Errorf("weighted selection skewed: b=%d/%d (want ~1800)", bCount, trials)
	}
}

func TestScheduler_ExecuteOpOpensAndClosesWindow(t *testing.T) {
	op := &fakeOp{name: "op", weight: 1, available: true}
	s := newSchedulerForTest(op)
	s.executeOp(context.Background(), op)
	if op.executed != 1 {
		t.Errorf("Execute called %d times, want 1", op.executed)
	}
	if s.journal.ActiveWindow() != nil {
		t.Error("expected window to be closed after executeOp")
	}
	if got := s.journal.DisruptionWindows(); len(got) != 1 || got[0].OperationName != "op" {
		t.Errorf("expected one closed window for 'op', got %+v", got)
	}
}

func TestScheduler_ExecuteOpRecordsErrorEvent(t *testing.T) {
	op := &fakeOp{name: "boom", weight: 1, available: true, err: errors.New("kaboom")}
	s := newSchedulerForTest(op)
	s.executeOp(context.Background(), op)

	var sawError bool
	for _, e := range s.journal.Events() {
		if e.Level == journal.LevelError && e.Component == "scheduler" {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected scheduler ERROR event on Execute failure")
	}
}

func TestScheduler_OpsExecutedCounter(t *testing.T) {
	s := newSchedulerForTest()
	if got := s.OpsExecuted(); got != 0 {
		t.Errorf("OpsExecuted() = %d on new scheduler, want 0", got)
	}
	s.opsExecuted = 7
	if got := s.OpsExecuted(); got != 7 {
		t.Errorf("OpsExecuted() = %d, want 7", got)
	}
}
