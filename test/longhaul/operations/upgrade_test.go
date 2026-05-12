// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package operations

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestUpgradeDocumentDB_NameAndWeight(t *testing.T) {
	u := NewUpgradeDocumentDB(&fakeClient{}, fake.NewSimpleClientset(), nil, nil, "ns", time.Minute)
	if u.Name() != "upgrade-documentdb" {
		t.Errorf("Name=%q, want upgrade-documentdb", u.Name())
	}
	// Weight is intentionally low (1) so upgrades don't crowd out
	// scale/failover ops in the random scheduler.
	if u.Weight() != 1 {
		t.Errorf("Weight=%d, want 1", u.Weight())
	}
}

func TestUpgradeDocumentDB_OutagePolicy(t *testing.T) {
	u := NewUpgradeDocumentDB(&fakeClient{}, fake.NewSimpleClientset(), nil, nil, "ns", 10*time.Minute)
	p := u.OutagePolicy()
	// Upgrades touch every pod sequentially -> longer downtime budget.
	if p.AllowedDowntime != 120*time.Second {
		t.Errorf("AllowedDowntime=%v, want 120s", p.AllowedDowntime)
	}
	if p.AllowedWriteFailures != 200 {
		t.Errorf("AllowedWriteFailures=%d, want 200", p.AllowedWriteFailures)
	}
	if p.MustRecoverWithin != 10*time.Minute {
		t.Errorf("MustRecoverWithin=%v, want 10m", p.MustRecoverWithin)
	}
}

func TestReadDesiredVersion_NotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	u := NewUpgradeDocumentDB(&fakeClient{}, cs, nil, nil, "ns", time.Minute)

	got, err := u.readDesiredVersion(context.Background())
	if err != nil {
		t.Fatalf("expected nil err on NotFound, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string on NotFound, got %q", got)
	}
}

func TestReadDesiredVersion_FoundReturnsValue(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: VersionConfigMapName, Namespace: "ns"},
		Data:       map[string]string{VersionConfigMapKey: "0.110.0"},
	}
	cs := fake.NewSimpleClientset(cm)
	u := NewUpgradeDocumentDB(&fakeClient{}, cs, nil, nil, "ns", time.Minute)

	got, err := u.readDesiredVersion(context.Background())
	if err != nil {
		t.Fatalf("readDesiredVersion: %v", err)
	}
	if got != "0.110.0" {
		t.Errorf("got %q, want 0.110.0", got)
	}
}

func TestReadDesiredVersion_FoundButKeyMissing(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: VersionConfigMapName, Namespace: "ns"},
		Data:       map[string]string{"unrelated": "value"},
	}
	cs := fake.NewSimpleClientset(cm)
	u := NewUpgradeDocumentDB(&fakeClient{}, cs, nil, nil, "ns", time.Minute)

	got, err := u.readDesiredVersion(context.Background())
	if err != nil {
		t.Fatalf("readDesiredVersion: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (key missing in Data map)", got)
	}
}

func TestUpgradePrecondition(t *testing.T) {
	const ns = "ns"
	cases := []struct {
		name           string
		desired        string // value put in CM (empty => no CM)
		runningTag     string
		ipn            int
		wantOK         bool
		wantReasonHas  string
	}{
		{"no desired version published", "", "0.109.0", 2, false, "no desired version"},
		{"already at desired", "0.110.0", "0.110.0", 2, false, "already at desired"},
		{"single-instance: ipn=1 -> skip", "0.110.0", "0.109.0", 1, false, "no HA standby"},
		{"eligible: HA + version differs", "0.110.0", "0.109.0", 2, true, ""},
		{"eligible: max HA", "0.110.0", "0.109.0", 3, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cs = fake.NewSimpleClientset()
			if tc.desired != "" {
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: VersionConfigMapName, Namespace: ns},
					Data:       map[string]string{VersionConfigMapKey: tc.desired},
				}
				cs = fake.NewSimpleClientset(cm)
			}
			c := &fakeClient{instancesPerNode: tc.ipn, imageTag: tc.runningTag}
			u := NewUpgradeDocumentDB(c, cs, nil, nil, ns, time.Minute)

			ok, reason := u.Precondition(context.Background())
			if ok != tc.wantOK {
				t.Errorf("ok=%v, want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if tc.wantReasonHas != "" && !strings.Contains(reason, tc.wantReasonHas) {
				t.Errorf("reason=%q does not contain %q", reason, tc.wantReasonHas)
			}
		})
	}
}
