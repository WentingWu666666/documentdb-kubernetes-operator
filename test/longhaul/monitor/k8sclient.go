// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// PodMetrics holds resource usage for a single pod.
type PodMetrics struct {
	Name     string
	MemoryMB float64
	CPUCores float64
}

// K8sClusterClient implements ClusterClient using real Kubernetes API calls.
type K8sClusterClient struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
	metricsClient metricsv.Interface
	namespace     string
	clusterName   string
	crGVR         schema.GroupVersionResource
	metricsAvail  bool
}

// K8sClientConfig holds configuration for creating a K8sClusterClient.
type K8sClientConfig struct {
	Namespace   string
	ClusterName string
	Kubeconfig  string // optional, empty uses in-cluster
}

// NewK8sClusterClient creates a real Kubernetes cluster client.
// It first attempts in-cluster config, then falls back to KUBECONFIG.
func NewK8sClusterClient(cfg K8sClientConfig) (*K8sClusterClient, error) {
	restConfig, err := buildRestConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build rest config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	// Try to create metrics client (graceful fallback).
	metricsClient, metricsAvail := tryMetricsClient(restConfig)

	return &K8sClusterClient{
		clientset:     clientset,
		dynamicClient: dynClient,
		metricsClient: metricsClient,
		namespace:     cfg.Namespace,
		clusterName:   cfg.ClusterName,
		crGVR: schema.GroupVersionResource{
			Group:    "documentdb.io",
			Version:  "preview",
			Resource: "dbs",
		},
		metricsAvail: metricsAvail,
	}, nil
}

func buildRestConfig(kubeconfig string) (*rest.Config, error) {
	// Try in-cluster first.
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fall back to kubeconfig.
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func tryMetricsClient(config *rest.Config) (metricsv.Interface, bool) {
	mc, err := metricsv.NewForConfig(config)
	if err != nil {
		log.Printf("[k8sclient] metrics client creation failed (leak detection disabled): %v", err)
		return nil, false
	}
	return mc, true
}

// GetClusterHealth queries pod status and CR status to determine cluster health.
func (k *K8sClusterClient) GetClusterHealth(ctx context.Context) (ClusterHealth, error) {
	health := ClusterHealth{Timestamp: time.Now()}

	// List pods with the CNPG cluster label.
	labelSelector := fmt.Sprintf("cnpg.io/cluster=%s", k.clusterName)
	pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return health, fmt.Errorf("failed to list pods: %w", err)
	}

	health.TotalPods = len(pods.Items)
	var totalRestarts int32
	readyCount := 0

	for i := range pods.Items {
		pod := &pods.Items[i]
		if isPodReady(pod) {
			readyCount++
		}
		for _, cs := range pod.Status.ContainerStatuses {
			totalRestarts += cs.RestartCount
		}
	}

	health.ReadyPods = readyCount
	health.AllPodsReady = readyCount == health.TotalPods && health.TotalPods > 0
	health.RestartCount = totalRestarts

	// Get the DocumentDB CR status.
	cr, err := k.dynamicClient.Resource(k.crGVR).Namespace(k.namespace).Get(ctx, k.clusterName, metav1.GetOptions{})
	if err != nil {
		return health, fmt.Errorf("failed to get DocumentDB CR: %w", err)
	}

	status, _, _ := unstructured.NestedString(cr.Object, "status", "status")
	health.CRReady = status == "Cluster in healthy state"

	return health, nil
}

// GetInstancesPerNode reads spec.instancesPerNode from the DocumentDB CR.
// Range is 1-3 per the CRD; 1 means no HA, >=2 means at least one standby.
func (k *K8sClusterClient) GetInstancesPerNode(ctx context.Context) (int, error) {
	cr, err := k.dynamicClient.Resource(k.crGVR).Namespace(k.namespace).Get(ctx, k.clusterName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to get DocumentDB CR: %w", err)
	}

	ipn, found, err := unstructured.NestedInt64(cr.Object, "spec", "instancesPerNode")
	if err != nil {
		return 0, fmt.Errorf("spec.instancesPerNode read error: %w", err)
	}
	if !found {
		// Field omitted on CR — operator default is 1 (single instance).
		return 1, nil
	}
	return int(ipn), nil
}

// ScaleCluster patches spec.instancesPerNode on the DocumentDB CR.
//
// Note: spec.nodeCount is hard-capped at 1 by the CRD (minimum=maximum=1),
// so the only scale dimension exposed today is instancesPerNode (range 1-3).
// Each instance is a CNPG replica (1 primary + N-1 standbys); growing this
// dimension is what gives the cluster HA.
func (k *K8sClusterClient) ScaleCluster(ctx context.Context, instancesPerNode int) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"instancesPerNode": instancesPerNode,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	_, err = k.dynamicClient.Resource(k.crGVR).Namespace(k.namespace).Patch(
		ctx, k.clusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch DocumentDB CR: %w", err)
	}

	return nil
}

// GetCurrentDocumentDBImageTag reads status.documentDBImage from the CR
// and returns the tag portion (after the last colon).
func (k *K8sClusterClient) GetCurrentDocumentDBImageTag(ctx context.Context) (string, error) {
	cr, err := k.dynamicClient.Resource(k.crGVR).Namespace(k.namespace).Get(ctx, k.clusterName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get DocumentDB CR: %w", err)
	}

	image, found, err := unstructured.NestedString(cr.Object, "status", "documentDBImage")
	if err != nil || !found || image == "" {
		return "", nil
	}

	idx := strings.LastIndex(image, ":")
	if idx < 0 || idx == len(image)-1 {
		return "", nil
	}
	return image[idx+1:], nil
}

// UpgradeDocumentDB patches the DocumentDB CR to set documentDBVersion
// and schemaVersion="auto" so the operator performs a rolling upgrade.
// NOTE: the CRD field is documentDBVersion (capital DB), not documentDbVersion.
func (k *K8sClusterClient) UpgradeDocumentDB(ctx context.Context, version string) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"documentDBVersion": version,
			"schemaVersion":     "auto",
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	_, err = k.dynamicClient.Resource(k.crGVR).Namespace(k.namespace).Patch(
		ctx, k.clusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to patch DocumentDB CR: %w", err)
	}
	return nil
}

// GetPodMetrics queries metrics-server for pod resource usage.
// Returns nil, nil if metrics-server is not available.
func (k *K8sClusterClient) GetPodMetrics(ctx context.Context) ([]PodMetrics, error) {
	if !k.metricsAvail || k.metricsClient == nil {
		return nil, nil
	}

	labelSelector := fmt.Sprintf("cnpg.io/cluster=%s", k.clusterName)
	podMetricsList, err := k.metricsClient.MetricsV1beta1().PodMetricses(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		// Metrics API might have become unavailable.
		log.Printf("[k8sclient] metrics query failed (disabling): %v", err)
		k.metricsAvail = false
		return nil, nil
	}

	var result []PodMetrics
	for _, pm := range podMetricsList.Items {
		var totalMemBytes int64
		var totalCPUMillis int64
		for _, c := range pm.Containers {
			totalMemBytes += c.Usage.Memory().Value()
			totalCPUMillis += c.Usage.Cpu().MilliValue()
		}
		result = append(result, PodMetrics{
			Name:     pm.Name,
			MemoryMB: float64(totalMemBytes) / (1024 * 1024),
			CPUCores: float64(totalCPUMillis) / 1000.0,
		})
	}

	return result, nil
}

// MetricsAvailable returns whether metrics-server is usable.
func (k *K8sClusterClient) MetricsAvailable() bool {
	return k.metricsAvail
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
