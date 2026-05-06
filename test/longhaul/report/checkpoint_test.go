// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package report

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testNS = "documentdb-test-ns"

func passSummary() Summary {
	return Summary{Result: ResultPass, Duration: 10 * time.Second}
}

func failSummary() Summary {
	return Summary{Result: ResultFail, Duration: 10 * time.Second, FailReason: "downtime budget exceeded"}
}

var _ = Describe("CheckpointReporter.emit", func() {
	It("creates the ConfigMap on first emit when it does not exist", func() {
		cs := fake.NewSimpleClientset()
		r := NewCheckpointReporter(cs, testNS, time.Minute, passSummary)

		r.emit(context.Background())

		cm, err := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ConfigMapName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(cm.Data).To(HaveKey("latest-report"))
		Expect(cm.Data).To(HaveKey("last-updated"))
		Expect(cm.Data).To(HaveKey("result"))
	})

	It("surfaces RUNNING in the result field for an in-flight PASS summary", func() {
		// Intermediate checkpoints should never read PASS, since the run is not
		// over yet. The reporter rewrites PASS->RUNNING for ConfigMap consumers
		// (alerting workflow, dashboards). FAIL stays FAIL so it surfaces fast.
		cs := fake.NewSimpleClientset()
		r := NewCheckpointReporter(cs, testNS, time.Minute, passSummary)
		r.emit(context.Background())

		cm, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ConfigMapName, metav1.GetOptions{})
		Expect(cm.Data["result"]).To(Equal("RUNNING"))
	})

	It("surfaces FAIL in the result field as-is", func() {
		cs := fake.NewSimpleClientset()
		r := NewCheckpointReporter(cs, testNS, time.Minute, failSummary)
		r.emit(context.Background())

		cm, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ConfigMapName, metav1.GetOptions{})
		Expect(cm.Data["result"]).To(Equal("FAIL"))
	})

	It("updates the ConfigMap on subsequent emits without erroring", func() {
		cs := fake.NewSimpleClientset()
		state := ResultPass
		summary := func() Summary {
			return Summary{Result: state, Duration: 1 * time.Second}
		}
		r := NewCheckpointReporter(cs, testNS, time.Minute, summary)
		r.emit(context.Background())

		// Flip to FAIL and emit again — the ConfigMap should reflect the new state.
		state = ResultFail
		r.emit(context.Background())

		cm, err := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ConfigMapName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(cm.Data["result"]).To(Equal("FAIL"))
	})

	It("is a no-op when the clientset is nil", func() {
		// Verifies the in-process path (stdout-only) does not panic. The driver
		// uses this when LONGHAUL_DRY_RUN-style configurations omit a real client.
		r := NewCheckpointReporter(nil, testNS, time.Minute, passSummary)
		Expect(func() { r.emit(context.Background()) }).NotTo(Panic())
	})

	It("labels the ConfigMap so dashboards / kubectl selectors can find it", func() {
		cs := fake.NewSimpleClientset()
		r := NewCheckpointReporter(cs, testNS, time.Minute, passSummary)
		r.emit(context.Background())

		cm, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ConfigMapName, metav1.GetOptions{})
		Expect(cm.Labels).To(HaveKeyWithValue("app.kubernetes.io/name", "longhaul-test"))
		Expect(cm.Labels).To(HaveKeyWithValue("app.kubernetes.io/part-of", "documentdb-operator"))
	})
})
