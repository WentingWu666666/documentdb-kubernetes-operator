// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package operations implements operation runners and individual disruptive
// operations for long haul tests.
package operations

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/config"
	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
	"github.com/documentdb/documentdb-operator/test/longhaul/monitor"
)

// Operation defines the interface for a disruptive operation.
type Operation interface {
	// Name returns a human-readable identifier for this operation.
	Name() string

	// Weight returns the relative probability of selection (higher = more likely).
	Weight() int

	// Precondition checks if the operation can be executed in the current state.
	Precondition(ctx context.Context) (bool, string)

	// Execute performs the operation and returns when complete.
	Execute(ctx context.Context) error

	// OutagePolicy returns the disruption budget for this operation.
	OutagePolicy() journal.OutagePolicy
}

// Scheduler selects and executes operations based on weighted random selection,
// preconditions, cooldowns, and steady-state gates.
type Scheduler struct {
	operations    []Operation
	healthMonitor *monitor.HealthMonitor
	journal       *journal.Journal
	cooldown      time.Duration

	// rng, when non-nil, pins weighted-random selection for reproducibility.
	// When nil the process-global generator is used (production behavior).
	rng *rand.Rand
	// coverage draws each operation without replacement and completes the run
	// once every operation has run at least once.
	coverage bool

	mu          sync.Mutex
	lastOpTime  time.Time
	opsExecuted int
	inProgress  bool

	state          runnerState
	aggregateIndex map[string]int
}

// SchedulerOption configures optional Scheduler behavior.
type SchedulerOption func(*Scheduler)

// WithSeed pins weighted-random selection to a fixed seed so the run is
// reproducible. Without it the scheduler uses the process-global generator.
func WithSeed(seed int64) SchedulerOption {
	return func(s *Scheduler) {
		s.rng = rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
	}
}

// WithCoverage enables coverage mode: the scheduler draws each operation
// without replacement and completes once every operation has run at least once,
// rather than running until context cancellation.
func WithCoverage() SchedulerOption {
	return func(s *Scheduler) { s.coverage = true }
}

// NewScheduler creates an operation scheduler.
func NewScheduler(
	ops []Operation,
	health *monitor.HealthMonitor,
	j *journal.Journal,
	cooldown time.Duration,
	opts ...SchedulerOption,
) *Scheduler {
	aggregates := make([]OperationAggregate, 0, len(ops))
	aggregateIndex := make(map[string]int, len(ops))
	for _, op := range ops {
		if _, exists := aggregateIndex[op.Name()]; exists {
			continue
		}
		aggregateIndex[op.Name()] = len(aggregates)
		aggregates = append(aggregates, OperationAggregate{Name: op.Name()})
	}
	s := &Scheduler{
		operations:    ops,
		healthMonitor: health,
		journal:       j,
		cooldown:      cooldown,
		state: newRunnerState(RunSnapshot{
			Mode:       config.OperationModeRandom,
			Status:     RunStatusPending,
			Aggregates: aggregates,
		}),
		aggregateIndex: aggregateIndex,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// intn returns a non-negative pseudo-random int in [0,n) from the scheduler's
// seeded generator when present, otherwise the process-global generator.
func (s *Scheduler) intn(n int) int {
	if s.rng != nil {
		return s.rng.IntN(n)
	}
	return rand.IntN(n)
}

// Run starts the scheduler loop. It blocks until context is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.journal.Info("scheduler", "operation scheduler started")
	s.state.mu.Lock()
	s.state.snapshot.Status = RunStatusRunning
	s.state.mu.Unlock()
	defer func() {
		s.state.mu.Lock()
		if s.state.snapshot.Status == RunStatusRunning {
			// Coverage runs that stop before covering every operation (watchdog
			// or shutdown) are terminally incomplete, not complete.
			if s.coverage && !s.allCoveredLocked() {
				s.state.snapshot.Status = RunStatusIncomplete
				if s.state.snapshot.FailureReason == "" {
					s.state.snapshot.FailureReason = "operation coverage incomplete: run stopped before every operation ran"
				}
			} else {
				s.state.snapshot.Status = RunStatusComplete
			}
		}
		s.state.mu.Unlock()
		s.state.closeDone()
		s.journal.Info("scheduler", "operation scheduler stopped")
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tryExecute(ctx)
			// Coverage mode is completion-driven: stop as soon as the run
			// reaches a terminal state (all operations covered, or a failure).
			if s.coverage && s.coverageTerminalReached() {
				return
			}
		}
	}
}

// coverageTerminalReached reports whether a coverage run has reached a terminal
// state and its loop should return.
func (s *Scheduler) coverageTerminalReached() bool {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return s.state.snapshot.Status == RunStatusComplete ||
		s.state.snapshot.Status == RunStatusFailed
}

// allCoveredLocked reports whether every registered operation has run at least
// once. Callers must hold s.state.mu.
func (s *Scheduler) allCoveredLocked() bool {
	if len(s.state.snapshot.Aggregates) == 0 {
		return false
	}
	for _, a := range s.state.snapshot.Aggregates {
		if a.Passed+a.Failed == 0 {
			return false
		}
	}
	return true
}

// coveredSet returns the set of operation names that have run at least once.
func (s *Scheduler) coveredSet() map[string]bool {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	covered := make(map[string]bool, len(s.state.snapshot.Aggregates))
	for _, a := range s.state.snapshot.Aggregates {
		if a.Passed+a.Failed > 0 {
			covered[a.Name] = true
		}
	}
	return covered
}

func (s *Scheduler) tryExecute(ctx context.Context) {
	s.mu.Lock()
	if s.inProgress {
		s.mu.Unlock()
		return
	}

	// Check cooldown.
	if !s.lastOpTime.IsZero() && time.Since(s.lastOpTime) < s.cooldown {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Check steady-state gate.
	if !s.healthMonitor.IsSteadyState() {
		return
	}

	// Select an operation.
	op := s.selectOperation(ctx)
	if op == nil {
		return
	}

	// Execute.
	s.mu.Lock()
	s.inProgress = true
	s.mu.Unlock()

	err := s.executeOp(ctx, op)

	s.mu.Lock()
	s.inProgress = false
	s.lastOpTime = time.Now()
	s.opsExecuted++
	s.mu.Unlock()

	s.recordExecution(op.Name(), err)
}

func (s *Scheduler) selectOperation(ctx context.Context) Operation {
	// In coverage mode, exclude operations that have already run so each is
	// drawn without replacement until every operation has been covered.
	var covered map[string]bool
	if s.coverage {
		covered = s.coveredSet()
	}

	// Filter by preconditions and build weighted list.
	type candidate struct {
		op     Operation
		weight int
	}
	var candidates []candidate
	totalWeight := 0

	for _, op := range s.operations {
		if s.coverage && covered[op.Name()] {
			continue
		}
		ok, _ := op.Precondition(ctx)
		if ok {
			w := op.Weight()
			candidates = append(candidates, candidate{op: op, weight: w})
			totalWeight += w
		}
	}

	if len(candidates) == 0 || totalWeight == 0 {
		return nil
	}

	// Weighted random selection.
	r := s.intn(totalWeight)
	for _, c := range candidates {
		r -= c.weight
		if r < 0 {
			return c.op
		}
	}
	return candidates[len(candidates)-1].op
}

func (s *Scheduler) executeOp(ctx context.Context, op Operation) error {
	s.journal.Info("scheduler", fmt.Sprintf("executing operation: %s", op.Name()))
	s.journal.OpenDisruptionWindow(op.Name(), op.OutagePolicy())

	err := op.Execute(ctx)
	window := s.journal.CloseDisruptionWindow()

	if err != nil {
		s.journal.Error("scheduler", fmt.Sprintf("operation %s failed: %v", op.Name(), err))
		return fmt.Errorf("operation %s execute failed: %w", op.Name(), err)
	}
	if window == nil {
		err = fmt.Errorf("operation %s closed without a disruption window", op.Name())
		s.journal.Error("scheduler", err.Error())
		return err
	}
	if window.ExceededPolicy() {
		err = fmt.Errorf("operation %s exceeded its outage policy", op.Name())
		s.journal.Error("scheduler", err.Error())
		return err
	}

	s.journal.Info("scheduler", fmt.Sprintf("operation %s completed successfully", op.Name()))
	return nil
}

func (s *Scheduler) recordExecution(name string, err error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	index, ok := s.aggregateIndex[name]
	if !ok {
		return
	}
	if err != nil {
		s.state.snapshot.Aggregates[index].Failed++
		s.state.snapshot.Status = RunStatusFailed
		if s.state.snapshot.FailureReason == "" {
			s.state.snapshot.FailureReason = err.Error()
		}
		return
	}
	s.state.snapshot.Aggregates[index].Passed++
	// Coverage mode completes once every operation has run at least once.
	if s.coverage && s.state.snapshot.Status == RunStatusRunning && s.allCoveredLocked() {
		s.state.snapshot.Status = RunStatusComplete
	}
}

// OpsExecuted returns the number of operations completed.
func (s *Scheduler) OpsExecuted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opsExecuted
}

// Snapshot returns bounded aggregate counters in registration order.
func (s *Scheduler) Snapshot() RunSnapshot {
	return s.state.Snapshot()
}

// Done closes when the scheduler stops after context cancellation.
func (s *Scheduler) Done() <-chan struct{} {
	return s.state.Done()
}
