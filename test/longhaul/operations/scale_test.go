// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package operations

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/monitor"
)

// fakeClient is a minimal monitor.ClusterClient stub for unit tests.
type fakeClient struct {
	mu               sync.Mutex
	instancesPerNode int
	ipnErr           error
	imageTag         string
	scaleCalls       []int
	upgradeCalls     []string
}

func (f *fakeClient) GetClusterHealth(_ context.Context) (monitor.ClusterHealth, error) {
	return monitor.ClusterHealth{}, nil
}
func (f *fakeClient) GetCurrentDocumentDBImageTag(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.imageTag, nil
}
func (f *fakeClient) GetInstancesPerNode(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instancesPerNode, f.ipnErr
}
func (f *fakeClient) ScaleCluster(_ context.Context, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scaleCalls = append(f.scaleCalls, n)
	f.instancesPerNode = n
	return nil
}
func (f *fakeClient) UpgradeDocumentDB(_ context.Context, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upgradeCalls = append(f.upgradeCalls, v)
	return nil
}

func TestNewScaleUp_ClampsMaxInstances(t *testing.T) {
	cases := []struct{ in, want int }{
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 3}, // CRD upper bound
		{99, 3},
	}
	for _, tc := range cases {
		s := NewScaleUp(&fakeClient{}, nil, tc.in, time.Second)
		if s.maxInstances != tc.want {
			t.Errorf("NewScaleUp(maxInstances=%d).maxInstances=%d, want %d", tc.in, s.maxInstances, tc.want)
		}
	}
}

func TestNewScaleDown_ClampsMinInstances(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1}, // CRD lower bound
		{-5, 1},
		{1, 1},
		{2, 2},
		{3, 3},
	}
	for _, tc := range cases {
		s := NewScaleDown(&fakeClient{}, nil, tc.in, time.Second)
		if s.minInstances != tc.want {
			t.Errorf("NewScaleDown(minInstances=%d).minInstances=%d, want %d", tc.in, s.minInstances, tc.want)
		}
	}
}

func TestScaleUp_NameAndWeight(t *testing.T) {
	s := NewScaleUp(&fakeClient{}, nil, 3, time.Second)
	if s.Name() != "scale-up" {
		t.Errorf("Name=%q, want scale-up", s.Name())
	}
	if s.Weight() != 3 {
		t.Errorf("Weight=%d, want 3", s.Weight())
	}
}

func TestScaleDown_NameAndWeight(t *testing.T) {
	s := NewScaleDown(&fakeClient{}, nil, 1, time.Second)
	if s.Name() != "scale-down" {
		t.Errorf("Name=%q, want scale-down", s.Name())
	}
	if s.Weight() != 2 {
		t.Errorf("Weight=%d, want 2", s.Weight())
	}
}

func TestScaleUp_Precondition(t *testing.T) {
	cases := []struct {
		name             string
		current          int
		ipnErr           error
		max              int
		wantOK           bool
		wantReasonHas    string
	}{
		{"eligible: under max", 1, nil, 3, true, ""},
		{"eligible: just under max", 2, nil, 3, true, ""},
		{"blocked: at max", 3, nil, 3, false, "already at max"},
		{"blocked: ipn read error", 0, errors.New("apiserver down"), 3, false, "cannot get instancesPerNode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{instancesPerNode: tc.current, ipnErr: tc.ipnErr}
			s := NewScaleUp(c, nil, tc.max, time.Second)
			ok, reason := s.Precondition(context.Background())
			if ok != tc.wantOK {
				t.Errorf("ok=%v, want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if tc.wantReasonHas != "" && !strings.Contains(reason, tc.wantReasonHas) {
				t.Errorf("reason=%q does not contain %q", reason, tc.wantReasonHas)
			}
		})
	}
}

func TestScaleDown_Precondition(t *testing.T) {
	cases := []struct {
		name             string
		current          int
		ipnErr           error
		min              int
		wantOK           bool
		wantReasonHas    string
	}{
		{"eligible: above min", 3, nil, 1, true, ""},
		{"eligible: just above min", 2, nil, 1, true, ""},
		{"blocked: at min", 1, nil, 1, false, "already at min"},
		{"blocked: ipn read error", 0, errors.New("apiserver down"), 1, false, "cannot get instancesPerNode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{instancesPerNode: tc.current, ipnErr: tc.ipnErr}
			s := NewScaleDown(c, nil, tc.min, time.Second)
			ok, reason := s.Precondition(context.Background())
			if ok != tc.wantOK {
				t.Errorf("ok=%v, want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if tc.wantReasonHas != "" && !strings.Contains(reason, tc.wantReasonHas) {
				t.Errorf("reason=%q does not contain %q", reason, tc.wantReasonHas)
			}
		})
	}
}

func TestScaleUp_OutagePolicy(t *testing.T) {
	s := NewScaleUp(&fakeClient{}, nil, 3, 5*time.Minute)
	p := s.OutagePolicy()
	if p.AllowedDowntime != 30*time.Second {
		t.Errorf("AllowedDowntime=%v, want 30s", p.AllowedDowntime)
	}
	if p.AllowedWriteFailures != 20 {
		t.Errorf("AllowedWriteFailures=%d, want 20", p.AllowedWriteFailures)
	}
	if p.MustRecoverWithin != 5*time.Minute {
		t.Errorf("MustRecoverWithin=%v, want 5m (echoed from constructor)", p.MustRecoverWithin)
	}
}

func TestScaleDown_OutagePolicy(t *testing.T) {
	s := NewScaleDown(&fakeClient{}, nil, 1, 5*time.Minute)
	p := s.OutagePolicy()
	// Scale-down has more lenient policy than scale-up because cluster
	// stabilization (replica removal + WAL replay catch-up) takes longer.
	if p.AllowedDowntime != 60*time.Second {
		t.Errorf("AllowedDowntime=%v, want 60s", p.AllowedDowntime)
	}
	if p.AllowedWriteFailures != 50 {
		t.Errorf("AllowedWriteFailures=%d, want 50", p.AllowedWriteFailures)
	}
}


