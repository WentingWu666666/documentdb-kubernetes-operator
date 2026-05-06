// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package operations

import (
	"context"
	"math/rand"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubOp is a test-only Operation. Execute is a no-op; Precondition is
// programmable per-test, Weight is fixed at construction.
type stubOp struct {
	name    string
	weight  int
	allowed bool
	calls   int
}

func (s *stubOp) Name() string { return s.name }
func (s *stubOp) Weight() int  { return s.weight }
func (s *stubOp) Precondition(_ context.Context) (bool, string) {
	if s.allowed {
		return true, ""
	}
	return false, "blocked by test"
}
func (s *stubOp) Execute(_ context.Context) error {
	s.calls++
	return nil
}
func (s *stubOp) OutagePolicy() journal.OutagePolicy { return journal.DefaultOutagePolicy() }

var _ = Describe("Scheduler.selectOperation", func() {
	var (
		j  *journal.Journal
		s  *Scheduler
		a  *stubOp
		b  *stubOp
		c  *stubOp
	)

	BeforeEach(func() {
		j = journal.New()
		a = &stubOp{name: "a", weight: 1, allowed: true}
		b = &stubOp{name: "b", weight: 3, allowed: true}
		c = &stubOp{name: "c", weight: 2, allowed: true}
		// healthMonitor is not consulted by selectOperation, so a nil pointer
		// here is fine — tests that exercise tryExecute construct one explicitly.
		s = NewScheduler([]Operation{a, b, c}, nil, j, time.Second)
		// Deterministic seed so distribution checks are reproducible.
		rand.Seed(1)
	})

	It("returns nil when no operations pass their precondition", func() {
		a.allowed, b.allowed, c.allowed = false, false, false
		Expect(s.selectOperation(context.Background())).To(BeNil())
	})

	It("returns nil when no operations are configured", func() {
		empty := NewScheduler(nil, nil, j, time.Second)
		Expect(empty.selectOperation(context.Background())).To(BeNil())
	})

	It("only ever returns operations whose precondition is satisfied", func() {
		a.allowed = false
		c.allowed = false // only b is eligible
		for i := 0; i < 50; i++ {
			Expect(s.selectOperation(context.Background())).To(BeIdenticalTo(Operation(b)))
		}
	})

	It("distributes selections roughly proportional to weight", func() {
		// Weights a=1, b=3, c=2 (total 6). Over 6000 trials, expect each op to
		// land within ~10% of its theoretical share.
		counts := map[string]int{}
		const trials = 6000
		for i := 0; i < trials; i++ {
			op := s.selectOperation(context.Background())
			counts[op.Name()]++
		}
		// theoretical: a=1000, b=3000, c=2000
		Expect(counts["a"]).To(BeNumerically("~", 1000, 200))
		Expect(counts["b"]).To(BeNumerically("~", 3000, 300))
		Expect(counts["c"]).To(BeNumerically("~", 2000, 250))
	})

	It("never picks an op with weight zero in a mixed-weight pool", func() {
		zero := &stubOp{name: "z", weight: 0, allowed: true}
		s = NewScheduler([]Operation{a, b, zero}, nil, j, time.Second)
		for i := 0; i < 200; i++ {
			op := s.selectOperation(context.Background())
			Expect(op.Name()).NotTo(Equal("z"))
		}
	})
})

var _ = Describe("Scheduler.tryExecute (cooldown gate)", func() {
	var (
		j *journal.Journal
		s *Scheduler
	)

	BeforeEach(func() {
		j = journal.New()
		s = NewScheduler([]Operation{&stubOp{name: "x", weight: 1, allowed: true}}, nil, j, time.Hour)
		// Pretend an operation just ran. Steady-state gate would normally fire
		// next, but the cooldown check happens first and short-circuits.
		s.lastOpTime = time.Now()
	})

	It("short-circuits before consulting the steady-state gate when in cooldown", func() {
		// Note: healthMonitor is nil here. If the cooldown gate did NOT
		// short-circuit, IsSteadyState would dereference nil and panic. So
		// "no panic" is the assertion that the gate is honored.
		Expect(func() { s.tryExecute(context.Background()) }).NotTo(Panic())
		Expect(s.OpsExecuted()).To(Equal(0))
	})
})
