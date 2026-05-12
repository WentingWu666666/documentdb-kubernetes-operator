// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package report

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckpointReporter_NilClientsetIsSafe(t *testing.T) {
	// In tests/dev runs the reporter may be constructed without a clientset.
	// emit() should print to stdout but not panic.
	r := NewCheckpointReporter(nil, "ns", time.Second, func() Summary {
		return Summary{Result: ResultPass, Duration: time.Minute}
	})
	r.emit(context.Background())
}

func TestCheckpointReporter_CreatesConfigMapOnFirstEmit(t *testing.T) {
	cs := fake.NewSimpleClientset()
	r := NewCheckpointReporter(cs, "ns", time.Second, func() Summary {
		return Summary{Result: ResultPass, Duration: 2 * time.Hour, OpsExecuted: 5}
	})

	r.emit(context.Background())

	cm, err := cs.CoreV1().ConfigMaps("ns").Get(context.Background(), ConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected ConfigMap created, got err: %v", err)
	}
	for _, key := range []string{"latest-report", "last-updated", "result"} {
		if _, ok := cm.Data[key]; !ok {
			t.Errorf("ConfigMap missing expected key %q; data=%v", key, cm.Data)
		}
	}
	// PASS results are persisted as RUNNING for intermediate checkpoints
	// (so consumers can distinguish in-flight from final state).
	if cm.Data["result"] != "RUNNING" {
		t.Errorf("result=%q, want RUNNING for intermediate PASS checkpoint", cm.Data["result"])
	}
	if cm.Labels["app.kubernetes.io/name"] != "longhaul-test" {
		t.Errorf("missing/incorrect identifying label: %v", cm.Labels)
	}
}

func TestCheckpointReporter_FailResultPersistedAsFail(t *testing.T) {
	cs := fake.NewSimpleClientset()
	r := NewCheckpointReporter(cs, "ns", time.Second, func() Summary {
		return Summary{Result: ResultFail, FailReason: "data loss"}
	})

	r.emit(context.Background())

	cm, err := cs.CoreV1().ConfigMaps("ns").Get(context.Background(), ConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get cm: %v", err)
	}
	if cm.Data["result"] != "FAIL" {
		t.Errorf("result=%q, want FAIL", cm.Data["result"])
	}
}

func TestCheckpointReporter_UpdatesExistingConfigMap(t *testing.T) {
	cs := fake.NewSimpleClientset()

	calls := 0
	r := NewCheckpointReporter(cs, "ns", time.Second, func() Summary {
		calls++
		return Summary{Result: ResultPass, Duration: time.Duration(calls) * time.Hour, OpsExecuted: calls * 10}
	})

	// First emit creates.
	r.emit(context.Background())
	cm1, err := cs.CoreV1().ConfigMaps("ns").Get(context.Background(), ConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("first get cm: %v", err)
	}
	rv1 := cm1.ResourceVersion
	report1 := cm1.Data["latest-report"]

	// Second emit must Update (not error). Fake clientset does not bump
	// ResourceVersion automatically, so assert on content change instead.
	r.emit(context.Background())
	cm2, err := cs.CoreV1().ConfigMaps("ns").Get(context.Background(), ConfigMapName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("second get cm: %v", err)
	}
	_ = rv1
	// The report content should have changed (different OpsExecuted).
	if cm2.Data["latest-report"] == report1 {
		t.Errorf("latest-report did not update across emits")
	}
	if calls != 2 {
		t.Errorf("summaryFunc called %d times, want 2", calls)
	}
}
