// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package journal

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These tests pin the pass/fail oracle for every long-haul disruption window.
// A regression here would silently pass a failed run or fail a passing run, so
// boundary conditions are exercised explicitly.
//
// NOTE: OutagePolicy.AllowedDowntime is declared on the struct but is NOT
// evaluated by DisruptionWindow.ExceededPolicy. Only MustRecoverWithin (against
// the window's wall-clock duration) and AllowedWriteFailures are checked. If
// the oracle is later extended to honor AllowedDowntime, add tests here.
var _ = Describe("DisruptionWindow", func() {
	var policy OutagePolicy

	BeforeEach(func() {
		policy = OutagePolicy{
			AllowedDowntime:      30 * time.Second,
			AllowedWriteFailures: 10,
			MustRecoverWithin:    2 * time.Minute,
		}
	})

	Describe("IsActive", func() {
		It("is active when EndTime is zero", func() {
			w := &DisruptionWindow{StartTime: time.Now()}
			Expect(w.IsActive()).To(BeTrue())
		})

		It("is not active once EndTime is set", func() {
			now := time.Now()
			w := &DisruptionWindow{StartTime: now, EndTime: now.Add(time.Second)}
			Expect(w.IsActive()).To(BeFalse())
		})
	})

	Describe("Duration", func() {
		It("returns EndTime-StartTime when closed", func() {
			start := time.Now()
			w := &DisruptionWindow{StartTime: start, EndTime: start.Add(45 * time.Second)}
			Expect(w.Duration()).To(Equal(45 * time.Second))
		})

		It("returns time-since-start when active", func() {
			start := time.Now().Add(-2 * time.Second)
			w := &DisruptionWindow{StartTime: start}
			d := w.Duration()
			Expect(d).To(BeNumerically(">=", 2*time.Second))
			Expect(d).To(BeNumerically("<", 5*time.Second))
		})
	})

	Describe("ExceededPolicy", func() {
		It("does not exceed when both within budget", func() {
			start := time.Now()
			w := &DisruptionWindow{
				StartTime:     start,
				EndTime:       start.Add(time.Minute),
				Policy:        policy,
				WriteFailures: 5,
			}
			Expect(w.ExceededPolicy()).To(BeFalse())
		})

		It("does not exceed when WriteFailures equals AllowedWriteFailures", func() {
			// Boundary: equal is allowed (predicate is strictly greater than).
			start := time.Now()
			w := &DisruptionWindow{
				StartTime:     start,
				EndTime:       start.Add(time.Minute),
				Policy:        policy,
				WriteFailures: policy.AllowedWriteFailures,
			}
			Expect(w.ExceededPolicy()).To(BeFalse())
		})

		It("exceeds when WriteFailures > AllowedWriteFailures", func() {
			start := time.Now()
			w := &DisruptionWindow{
				StartTime:     start,
				EndTime:       start.Add(time.Minute),
				Policy:        policy,
				WriteFailures: policy.AllowedWriteFailures + 1,
			}
			Expect(w.ExceededPolicy()).To(BeTrue())
		})

		It("does not exceed when Duration equals MustRecoverWithin", func() {
			start := time.Now()
			w := &DisruptionWindow{
				StartTime: start,
				EndTime:   start.Add(policy.MustRecoverWithin),
				Policy:    policy,
			}
			Expect(w.ExceededPolicy()).To(BeFalse())
		})

		It("exceeds when Duration > MustRecoverWithin", func() {
			start := time.Now()
			w := &DisruptionWindow{
				StartTime: start,
				EndTime:   start.Add(policy.MustRecoverWithin + time.Millisecond),
				Policy:    policy,
			}
			Expect(w.ExceededPolicy()).To(BeTrue())
		})

		It("exceeds for an active window once it overruns MustRecoverWithin", func() {
			policy.MustRecoverWithin = 10 * time.Millisecond
			w := &DisruptionWindow{
				StartTime: time.Now().Add(-50 * time.Millisecond),
				Policy:    policy,
			}
			Expect(w.ExceededPolicy()).To(BeTrue())
		})
	})
})

var _ = Describe("DefaultOutagePolicy", func() {
	It("returns conservative non-zero defaults", func() {
		p := DefaultOutagePolicy()
		Expect(p.AllowedDowntime).To(BeNumerically(">", time.Duration(0)))
		Expect(p.AllowedWriteFailures).To(BeNumerically(">", int64(0)))
		Expect(p.MustRecoverWithin).To(BeNumerically(">", p.AllowedDowntime),
			"MustRecoverWithin should be larger than AllowedDowntime")
	})
})
