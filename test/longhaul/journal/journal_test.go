// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package journal

import (
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Journal", func() {
	var j *Journal

	BeforeEach(func() {
		j = New()
	})

	Describe("Record/Info/Warn/Error", func() {
		It("appends events in order with the right level", func() {
			j.Info("comp", "first")
			j.Warn("comp", "second")
			j.Error("comp", "third")
			Expect(j.Len()).To(Equal(3))
			ev := j.Events()
			Expect(ev[0].Level).To(Equal(LevelInfo))
			Expect(ev[1].Level).To(Equal(LevelWarn))
			Expect(ev[2].Level).To(Equal(LevelError))
			Expect(ev[0].Message).To(Equal("first"))
		})

		It("returns a defensive copy from Events", func() {
			j.Info("c", "m")
			ev := j.Events()
			ev[0].Message = "MUTATED"
			Expect(j.Events()[0].Message).To(Equal("m"))
		})
	})

	Describe("EventsSince", func() {
		It("filters events by timestamp", func() {
			j.Info("c", "before")
			cut := time.Now()
			time.Sleep(2 * time.Millisecond)
			j.Info("c", "after")
			ev := j.EventsSince(cut)
			Expect(ev).To(HaveLen(1))
			Expect(ev[0].Message).To(Equal("after"))
		})
	})

	Describe("OpenDisruptionWindow / CloseDisruptionWindow", func() {
		var policy OutagePolicy

		BeforeEach(func() {
			policy = OutagePolicy{
				AllowedDowntime:      time.Second,
				AllowedWriteFailures: 5,
				MustRecoverWithin:    time.Minute,
			}
		})

		It("tracks an active window then archives it on close", func() {
			j.OpenDisruptionWindow("scale-up", policy)
			Expect(j.ActiveWindow()).NotTo(BeNil())
			Expect(j.ActiveWindow().OperationName).To(Equal("scale-up"))

			j.CloseDisruptionWindow()
			Expect(j.ActiveWindow()).To(BeNil())
			Expect(j.DisruptionWindows()).To(HaveLen(1))
			Expect(j.DisruptionWindows()[0].OperationName).To(Equal("scale-up"))
		})

		It("auto-closes a previous active window when opening a new one", func() {
			j.OpenDisruptionWindow("first", policy)
			j.OpenDisruptionWindow("second", policy)
			// First should now be in closedWindows; second should be active.
			Expect(j.DisruptionWindows()).To(HaveLen(1))
			Expect(j.DisruptionWindows()[0].OperationName).To(Equal("first"))
			Expect(j.ActiveWindow().OperationName).To(Equal("second"))
		})

		It("close is a no-op when no window is active", func() {
			Expect(func() { j.CloseDisruptionWindow() }).NotTo(Panic())
			Expect(j.DisruptionWindows()).To(BeEmpty())
		})

		It("ActiveWindow returns a copy (mutating the result does not affect state)", func() {
			j.OpenDisruptionWindow("op", policy)
			w := j.ActiveWindow()
			w.WriteFailures = 999
			Expect(j.ActiveWindow().WriteFailures).To(Equal(int64(0)))
		})
	})

	Describe("RecordWriteFailure", func() {
		It("attributes failures to the active window only", func() {
			policy := DefaultOutagePolicy()
			j.RecordWriteFailure() // no active window — must be a no-op
			j.OpenDisruptionWindow("op", policy)
			j.RecordWriteFailure()
			j.RecordWriteFailure()
			Expect(j.ActiveWindow().WriteFailures).To(Equal(int64(2)))

			j.CloseDisruptionWindow()
			j.RecordWriteFailure() // should not crash, must not affect closed window
			Expect(j.DisruptionWindows()[0].WriteFailures).To(Equal(int64(2)))
		})
	})

	Describe("HasPolicyViolation", func() {
		It("returns false with no windows", func() {
			Expect(j.HasPolicyViolation()).To(BeFalse())
		})

		It("returns true if a closed window exceeded budget", func() {
			policy := OutagePolicy{
				AllowedWriteFailures: 1,
				MustRecoverWithin:    time.Hour,
			}
			j.OpenDisruptionWindow("op", policy)
			j.RecordWriteFailure()
			j.RecordWriteFailure() // 2 > 1 -> exceeds
			j.CloseDisruptionWindow()
			Expect(j.HasPolicyViolation()).To(BeTrue())
		})

		It("returns true if the active window exceeds budget", func() {
			policy := OutagePolicy{
				AllowedWriteFailures: 0,
				MustRecoverWithin:    time.Hour,
			}
			j.OpenDisruptionWindow("op", policy)
			j.RecordWriteFailure()
			Expect(j.HasPolicyViolation()).To(BeTrue())
		})
	})

	Describe("concurrency", func() {
		It("is safe under concurrent writers", func() {
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for k := 0; k < 50; k++ {
						j.Info("c", "msg")
					}
				}()
			}
			wg.Wait()
			Expect(j.Len()).To(Equal(500))
		})
	})
})
