// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package monitor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeClient is an in-memory ClusterClient for driving HealthMonitor.check()
// from unit tests. The test thread programs the next response via SetHealth /
// SetErr; concurrent reads from check() are protected by mu.
type fakeClient struct {
	mu     sync.Mutex
	health ClusterHealth
	err    error
}

func (f *fakeClient) SetHealth(h ClusterHealth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health = h
	f.err = nil
}

func (f *fakeClient) SetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeClient) GetClusterHealth(_ context.Context) (ClusterHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return ClusterHealth{}, f.err
	}
	return f.health, nil
}

// Other ClusterClient methods are unused by HealthMonitor, but the interface
// requires them. Return zero values.
func (f *fakeClient) GetCurrentReplicas(_ context.Context) (int, error) { return 0, nil }
func (f *fakeClient) ScaleCluster(_ context.Context, _ int) error       { return nil }
func (f *fakeClient) GetCurrentDocumentDBImageTag(_ context.Context) (string, error) {
	return "", nil
}
func (f *fakeClient) UpgradeDocumentDB(_ context.Context, _ string) error { return nil }

func healthy() ClusterHealth {
	return ClusterHealth{
		Timestamp:    time.Now(),
		AllPodsReady: true,
		ReadyPods:    3,
		TotalPods:    3,
		CRReady:      true,
	}
}

func unhealthy() ClusterHealth {
	return ClusterHealth{
		Timestamp:    time.Now(),
		AllPodsReady: false,
		ReadyPods:    2,
		TotalPods:    3,
		CRReady:      true,
	}
}

var _ = Describe("HealthMonitor", func() {
	var (
		fc *fakeClient
		j  *journal.Journal
		hm *HealthMonitor
	)

	BeforeEach(func() {
		fc = &fakeClient{health: healthy()}
		j = journal.New()
		// Use a tiny steadyStateWait so tests don't hang on real clocks.
		hm = NewHealthMonitor(fc, j, 30*time.Millisecond)
	})

	Describe("IsSteadyState", func() {
		It("is false before any health observation", func() {
			Expect(hm.IsSteadyState()).To(BeFalse())
		})

		It("is false immediately after first healthy sample (steadyStateWait not yet elapsed)", func() {
			hm.check(context.Background())
			Expect(hm.IsSteadyState()).To(BeFalse())
		})

		It("becomes true after steadyStateWait elapses with continuous healthy samples", func() {
			fc.SetHealth(healthy())
			hm.check(context.Background())
			Eventually(hm.IsSteadyState, 200*time.Millisecond, 5*time.Millisecond).Should(BeTrue())
		})

		It("resets to false on a single unhealthy sample", func() {
			hm.check(context.Background())
			Eventually(hm.IsSteadyState, 200*time.Millisecond, 5*time.Millisecond).Should(BeTrue())

			fc.SetHealth(unhealthy())
			hm.check(context.Background())
			Expect(hm.IsSteadyState()).To(BeFalse())
		})

		It("resets to false when the cluster client returns an error", func() {
			hm.check(context.Background())
			Eventually(hm.IsSteadyState, 200*time.Millisecond, 5*time.Millisecond).Should(BeTrue())

			fc.SetErr(errors.New("api unreachable"))
			hm.check(context.Background())
			Expect(hm.IsSteadyState()).To(BeFalse())
		})

		It("requires CRReady AND AllPodsReady (CRReady=false counts as unhealthy)", func() {
			h := healthy()
			h.CRReady = false
			fc.SetHealth(h)
			hm.check(context.Background())
			time.Sleep(50 * time.Millisecond)
			Expect(hm.IsSteadyState()).To(BeFalse())
		})
	})

	Describe("LastHealth", func() {
		It("reflects the most recent observation", func() {
			fc.SetHealth(unhealthy())
			hm.check(context.Background())
			Expect(hm.LastHealth().AllPodsReady).To(BeFalse())
			Expect(hm.LastHealth().ReadyPods).To(Equal(2))
		})
	})

	Describe("WaitForSteadyState", func() {
		It("returns nil once steady state is achieved", func() {
			fc.SetHealth(healthy())
			// WaitForSteadyState's internal ticker is 1s, so we have to budget
			// at least 2 ticks plus the steadyStateWait (30ms) to be reliable.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						hm.check(ctx)
					}
				}
			}()
			go func() {
				defer close(done)
				_ = hm.WaitForSteadyState(ctx)
			}()
			Eventually(done, 3*time.Second).Should(BeClosed())
			Expect(hm.IsSteadyState()).To(BeTrue())
		})

		It("returns an error when context expires before steady state", func() {
			fc.SetHealth(unhealthy())
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := hm.WaitForSteadyState(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("timed out"))
		})
	})
})
