// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package monitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
)

// fakeClusterClient is a minimal ClusterClient stub for tests.
type fakeClusterClient struct {
	mu     sync.Mutex
	health ClusterHealth
	err    error
}

func (f *fakeClusterClient) setHealth(h ClusterHealth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health = h
	f.err = nil
}
func (f *fakeClusterClient) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}
func (f *fakeClusterClient) GetClusterHealth(_ context.Context) (ClusterHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health, f.err
}
func (f *fakeClusterClient) GetCurrentDocumentDBImageTag(_ context.Context) (string, error) {
	return "", nil
}
func (f *fakeClusterClient) GetInstancesPerNode(_ context.Context) (int, error)        { return 1, nil }
func (f *fakeClusterClient) ScaleCluster(_ context.Context, _ int) error               { return nil }
func (f *fakeClusterClient) UpgradeDocumentDB(_ context.Context, _ string) error       { return nil }

func TestHealthMonitor_IsSteadyState_NotHealthyYet(t *testing.T) {
	c := &fakeClusterClient{}
	h := NewHealthMonitor(c, journal.New(), 100*time.Millisecond)
	if h.IsSteadyState() {
		t.Error("IsSteadyState() = true before any check")
	}
}

func TestHealthMonitor_IsSteadyState_BecomesHealthy(t *testing.T) {
	c := &fakeClusterClient{}
	c.setHealth(ClusterHealth{AllPodsReady: true, CRReady: true, ReadyPods: 2, TotalPods: 2})
	h := NewHealthMonitor(c, journal.New(), 50*time.Millisecond)

	// First check sets steadySince but duration is 0.
	h.check(context.Background())
	if h.IsSteadyState() {
		t.Error("IsSteadyState() = true immediately after first healthy check")
	}

	time.Sleep(60 * time.Millisecond)
	if !h.IsSteadyState() {
		t.Error("IsSteadyState() = false after waiting > steadyStateWait")
	}
}

func TestHealthMonitor_LossOfHealthResetsSteady(t *testing.T) {
	c := &fakeClusterClient{}
	c.setHealth(ClusterHealth{AllPodsReady: true, CRReady: true})
	h := NewHealthMonitor(c, journal.New(), 1*time.Millisecond)
	h.check(context.Background())
	time.Sleep(2 * time.Millisecond)
	if !h.IsSteadyState() {
		t.Fatal("expected steady state to be reached")
	}

	c.setHealth(ClusterHealth{AllPodsReady: false, CRReady: false})
	h.check(context.Background())
	if h.IsSteadyState() {
		t.Error("IsSteadyState() = true after losing health")
	}
}

func TestHealthMonitor_PollErrorResetsSteady(t *testing.T) {
	c := &fakeClusterClient{}
	c.setHealth(ClusterHealth{AllPodsReady: true, CRReady: true})
	h := NewHealthMonitor(c, journal.New(), 1*time.Millisecond)
	h.check(context.Background())
	time.Sleep(2 * time.Millisecond)
	if !h.IsSteadyState() {
		t.Fatal("expected steady state to be reached")
	}

	c.setErr(errors.New("apiserver unreachable"))
	h.check(context.Background())
	if h.IsSteadyState() {
		t.Error("IsSteadyState() = true after poll error")
	}
}

func TestHealthMonitor_WaitForSteadyState_Cancelled(t *testing.T) {
	c := &fakeClusterClient{}
	h := NewHealthMonitor(c, journal.New(), 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := h.WaitForSteadyState(ctx)
	if err == nil {
		t.Error("WaitForSteadyState should return an error on context expiry")
	}
}

func TestHealthMonitor_WaitForSteadyState_Reached(t *testing.T) {
	c := &fakeClusterClient{}
	c.setHealth(ClusterHealth{AllPodsReady: true, CRReady: true})
	h := NewHealthMonitor(c, journal.New(), 1*time.Millisecond)

	// Get steady-state before WaitForSteadyState's first tick.
	h.check(context.Background())
	time.Sleep(5 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.WaitForSteadyState(ctx); err != nil {
		t.Errorf("WaitForSteadyState returned error: %v", err)
	}
}
