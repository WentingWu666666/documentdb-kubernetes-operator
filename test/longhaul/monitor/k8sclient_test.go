// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package monitor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

var testCRGVR = schema.GroupVersionResource{
	Group:    "documentdb.io",
	Version:  "preview",
	Resource: "dbs",
}

func newTestCR(ns, name string, modify func(obj map[string]interface{})) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "documentdb.io/preview",
		"kind":       "DocumentDB",
		"metadata": map[string]interface{}{
			"namespace": ns,
			"name":      name,
		},
		"spec": map[string]interface{}{},
		"status": map[string]interface{}{},
	}
	if modify != nil {
		modify(obj)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "documentdb.io", Version: "preview", Kind: "DocumentDB"})
	return u
}

func newTestK8sClient(t *testing.T, ns, cluster string, cs *fake.Clientset, objs ...runtime.Object) *K8sClusterClient {
	t.Helper()
	scheme := runtime.NewScheme()
	gvk := schema.GroupVersionKind{Group: "documentdb.io", Version: "preview", Kind: "DocumentDB"}
	listGVK := schema.GroupVersionKind{Group: "documentdb.io", Version: "preview", Kind: "DocumentDBList"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	gvrToListKind := map[schema.GroupVersionResource]string{testCRGVR: "DocumentDBList"}
	// Construct without seeded objects, then add via tracker so we can pin the
	// exact GVR (the scheme cannot infer "dbs" plural from "DocumentDB" kind).
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("newTestK8sClient: object is not *unstructured.Unstructured: %T", o)
		}
		if err := dyn.Tracker().Create(testCRGVR, u, u.GetNamespace()); err != nil {
			t.Fatalf("tracker.Create: %v", err)
		}
	}
	return &K8sClusterClient{
		clientset:     cs,
		dynamicClient: dyn,
		namespace:     ns,
		clusterName:   cluster,
		crGVR:         testCRGVR,
	}
}

func TestIsPodReady(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			"no conditions",
			&corev1.Pod{},
			false,
		},
		{
			"PodReady=True",
			&corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			}}},
			true,
		},
		{
			"PodReady=False",
			&corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			}}},
			false,
		},
		{
			"only PodScheduled=True (no Ready condition)",
			&corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			}}},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPodReady(tc.pod); got != tc.want {
				t.Errorf("isPodReady=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetClusterHealth(t *testing.T) {
	const ns, cluster = "default", "documentdb-cluster"
	pods := []runtime.Object{
		// Two ready pods labeled with the cluster.
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns, Name: "pod1",
				Labels: map[string]string{"cnpg.io/cluster": cluster},
			},
			Status: corev1.PodStatus{
				Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 1}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns, Name: "pod2",
				Labels: map[string]string{"cnpg.io/cluster": cluster},
			},
			Status: corev1.PodStatus{
				Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 2}},
			},
		},
		// One unrelated pod (different cluster) — must be excluded by selector.
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns, Name: "other",
				Labels: map[string]string{"cnpg.io/cluster": "other-cluster"},
			},
			Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		},
	}
	cs := fake.NewSimpleClientset(pods...)

	cr := newTestCR(ns, cluster, func(o map[string]interface{}) {
		o["status"] = map[string]interface{}{"status": "Cluster in healthy state"}
	})
	k := newTestK8sClient(t, ns, cluster, cs, cr)

	got, err := k.GetClusterHealth(context.Background())
	if err != nil {
		t.Fatalf("GetClusterHealth: %v", err)
	}
	if got.TotalPods != 2 {
		t.Errorf("TotalPods=%d, want 2 (other-cluster pod must be filtered out)", got.TotalPods)
	}
	if got.ReadyPods != 2 || !got.AllPodsReady {
		t.Errorf("ReadyPods=%d AllPodsReady=%v, want 2 true", got.ReadyPods, got.AllPodsReady)
	}
	if got.RestartCount != 3 {
		t.Errorf("RestartCount=%d, want 3 (1+2)", got.RestartCount)
	}
	if !got.CRReady {
		t.Errorf("CRReady=false, want true (status was healthy)")
	}
}

func TestGetClusterHealth_CRStatusNotHealthy(t *testing.T) {
	const ns, cluster = "default", "documentdb-cluster"
	cs := fake.NewSimpleClientset()
	cr := newTestCR(ns, cluster, func(o map[string]interface{}) {
		o["status"] = map[string]interface{}{"status": "Reconciling"}
	})
	k := newTestK8sClient(t, ns, cluster, cs, cr)

	got, err := k.GetClusterHealth(context.Background())
	if err != nil {
		t.Fatalf("GetClusterHealth: %v", err)
	}
	if got.CRReady {
		t.Errorf("CRReady=true, want false (status was %q)", "Reconciling")
	}
	// Zero pods => AllPodsReady must be false (special-case in source).
	if got.AllPodsReady {
		t.Errorf("AllPodsReady=true with TotalPods=0, want false")
	}
}

func TestGetInstancesPerNode(t *testing.T) {
	const ns, cluster = "default", "documentdb-cluster"
	cases := []struct {
		name      string
		modify    func(map[string]interface{})
		want      int
		expectErr bool
	}{
		{
			"explicit ipn=2",
			func(o map[string]interface{}) {
				o["spec"] = map[string]interface{}{"instancesPerNode": int64(2)}
			},
			2,
			false,
		},
		{
			"explicit ipn=3",
			func(o map[string]interface{}) {
				o["spec"] = map[string]interface{}{"instancesPerNode": int64(3)}
			},
			3,
			false,
		},
		{
			"unset ipn defaults to 1",
			func(o map[string]interface{}) { o["spec"] = map[string]interface{}{} },
			1,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := newTestCR(ns, cluster, tc.modify)
			k := newTestK8sClient(t, ns, cluster, fake.NewSimpleClientset(), cr)
			got, err := k.GetInstancesPerNode(context.Background())
			if (err != nil) != tc.expectErr {
				t.Fatalf("err=%v, expectErr=%v", err, tc.expectErr)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGetInstancesPerNode_CRMissing(t *testing.T) {
	k := newTestK8sClient(t, "default", "missing", fake.NewSimpleClientset())
	_, err := k.GetInstancesPerNode(context.Background())
	if err == nil {
		t.Errorf("expected error when CR is missing, got nil")
	}
}

func TestScaleCluster_PatchesIPN(t *testing.T) {
	const ns, cluster = "default", "documentdb-cluster"
	cr := newTestCR(ns, cluster, func(o map[string]interface{}) {
		o["spec"] = map[string]interface{}{"instancesPerNode": int64(1)}
	})
	k := newTestK8sClient(t, ns, cluster, fake.NewSimpleClientset(), cr)

	if err := k.ScaleCluster(context.Background(), 3); err != nil {
		t.Fatalf("ScaleCluster: %v", err)
	}
	got, err := k.GetInstancesPerNode(context.Background())
	if err != nil {
		t.Fatalf("read-back: %v", err)
	}
	if got != 3 {
		t.Errorf("after Scale, ipn=%d, want 3", got)
	}
}

func TestGetCurrentDocumentDBImageTag(t *testing.T) {
	const ns, cluster = "default", "documentdb-cluster"
	cases := []struct {
		name        string
		image       string // empty => field not set
		setStatus   bool
		want        string
	}{
		{"unset", "", false, ""},
		{"empty string", "", true, ""},
		{"image with tag", "ghcr.io/foo/documentdb:0.109.0", true, "0.109.0"},
		{"registry without tag", "ghcr.io/foo/documentdb", true, ""},
		{"trailing colon (malformed)", "ghcr.io/foo/documentdb:", true, ""},
		{"semver tag with port-like host", "host:5000/foo/documentdb:0.110.0-rc1", true, "0.110.0-rc1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := newTestCR(ns, cluster, func(o map[string]interface{}) {
				if tc.setStatus {
					o["status"] = map[string]interface{}{"documentDBImage": tc.image}
				}
			})
			k := newTestK8sClient(t, ns, cluster, fake.NewSimpleClientset(), cr)
			got, err := k.GetCurrentDocumentDBImageTag(context.Background())
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q (image=%q)", got, tc.want, tc.image)
			}
		})
	}
}

func TestUpgradeDocumentDB_PatchesVersionFields(t *testing.T) {
	const ns, cluster = "default", "documentdb-cluster"
	cr := newTestCR(ns, cluster, nil)
	k := newTestK8sClient(t, ns, cluster, fake.NewSimpleClientset(), cr)

	if err := k.UpgradeDocumentDB(context.Background(), "0.110.0"); err != nil {
		t.Fatalf("UpgradeDocumentDB: %v", err)
	}

	// Read back via dynamic client.
	got, err := k.dynamicClient.Resource(testCRGVR).Namespace(ns).Get(context.Background(), cluster, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	ver, _, _ := unstructured.NestedString(got.Object, "spec", "documentDBVersion")
	if ver != "0.110.0" {
		t.Errorf("spec.documentDBVersion=%q, want 0.110.0 (note CRD field uses capital DB)", ver)
	}
	schemaVer, _, _ := unstructured.NestedString(got.Object, "spec", "schemaVersion")
	if schemaVer != "auto" {
		t.Errorf("spec.schemaVersion=%q, want auto (operator picks compatible schema)", schemaVer)
	}
}

func TestMetricsAvailable(t *testing.T) {
	k := &K8sClusterClient{metricsAvail: true}
	if !k.MetricsAvailable() {
		t.Error("MetricsAvailable=false, want true")
	}
	k.metricsAvail = false
	if k.MetricsAvailable() {
		t.Error("MetricsAvailable=true, want false")
	}
}

func TestGetPodMetrics_DisabledWhenUnavailable(t *testing.T) {
	k := &K8sClusterClient{metricsAvail: false}
	got, err := k.GetPodMetrics(context.Background())
	if err != nil {
		t.Errorf("err=%v, want nil when metrics unavailable", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil when metrics unavailable", got)
	}
}
