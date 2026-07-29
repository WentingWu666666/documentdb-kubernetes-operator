// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package webhook

import (
	"context"
	"fmt"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// imageVolumeProbeImage is a syntactically valid image reference used only for
// the dry-run capability probe. It is never pulled or scheduled because every
// probe Pod is submitted with client.DryRunAll.
const imageVolumeProbeImage = "registry.k8s.io/pause:3.10"

// imageVolumeProbeVolumeName is the volume/mount name shared by both probe Pods.
const imageVolumeProbeVolumeName = "probe"

// imageVolumeConfirmed caches a positive detection of ImageVolume support.
//
// Only positive results are cached: once the cluster is known to support
// ImageVolume, subsequent creates skip the probe entirely (steady-state
// latency ~0). A negative or inconclusive result is never cached, so if an
// administrator enables the feature gate later the next create re-probes and
// succeeds without restarting the operator.
var imageVolumeConfirmed atomic.Bool

// ensureImageVolumeSupported blocks DocumentDB creation when the cluster does
// not support the Kubernetes ImageVolume feature (feature gate off, or a
// runtime/version that lacks it). The operator mounts the DocumentDB extension
// into the PostgreSQL pod via a corev1.ImageVolumeSource, so without this
// feature the cluster can never become ready.
//
// The check is intentionally fail-open: it blocks only when it can positively
// prove the feature is unavailable. If the cluster environment prevents a
// conclusive answer (e.g. Pod Security Admission or a ResourceQuota rejects the
// baseline probe Pod), creation is allowed and the reconcile loop surfaces any
// real failure later.
func (v *DocumentDBValidator) ensureImageVolumeSupported(ctx context.Context, namespace string) error {
	if imageVolumeConfirmed.Load() {
		return nil
	}

	supported, conclusive := v.probeImageVolume(ctx, namespace)
	if !conclusive {
		// Could not determine support; do not block on an inconclusive probe.
		return nil
	}
	if supported {
		imageVolumeConfirmed.Store(true)
		return nil
	}

	return fmt.Errorf(
		"the Kubernetes ImageVolume feature is not enabled on this cluster, but it is required by the DocumentDB " +
			"operator to mount the DocumentDB extension into PostgreSQL pods. ImageVolume is GA (on by default) in " +
			"Kubernetes 1.35+. On Kubernetes 1.33/1.34 you may enable the ImageVolume feature gate on both the " +
			"kube-apiserver and every kubelet, and ensure the container runtime supports it (containerd >= 2.1 or " +
			"CRI-O >= 1.33). Managed offerings (AKS/EKS/GKE) generally cannot enable this before it ships on by " +
			"default. See https://kubernetes.io/docs/concepts/storage/volumes/#image",
	)
}

// probeImageVolume performs a differential server-side dry-run to detect
// ImageVolume support without relying on error-string matching.
//
// It submits two dry-run Pods that are identical except for a single volume:
//   - baseline: an emptyDir volume (always a valid volume source), and
//   - image:    an ImageVolumeSource volume.
//
// Interpretation:
//   - baseline rejected           -> the environment blocks pod creation
//     entirely (PSA, quota, other webhooks); inconclusive.
//   - baseline ok, image ok       -> ImageVolume is supported.
//   - baseline ok, image rejected -> the image volume is the sole difference,
//     so ImageVolume is unsupported.
//
// The second return value reports whether the result is conclusive.
func (v *DocumentDBValidator) probeImageVolume(ctx context.Context, namespace string) (supported bool, conclusive bool) {
	if err := v.dryRunProbePod(ctx, namespace, baselineProbeVolume()); err != nil {
		documentdbLog.Info("ImageVolume preflight inconclusive: baseline probe pod rejected", "error", err.Error())
		return false, false
	}

	if err := v.dryRunProbePod(ctx, namespace, imageProbeVolume()); err != nil {
		documentdbLog.Info("ImageVolume preflight: image-volume probe pod rejected while baseline passed; feature unavailable", "error", err.Error())
		return false, true
	}

	return true, true
}

// dryRunProbePod submits a minimal Pod carrying the given probe volume as a
// server-side dry-run (never persisted) and returns the API server's verdict.
func (v *DocumentDBValidator) dryRunProbePod(ctx context.Context, namespace string, volume corev1.Volume) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "documentdb-imagevolume-preflight-",
			Namespace:    namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "probe",
				Image: imageVolumeProbeImage,
				VolumeMounts: []corev1.VolumeMount{{
					Name:      imageVolumeProbeVolumeName,
					MountPath: "/probe",
				}},
			}},
			Volumes: []corev1.Volume{volume},
		},
	}
	return v.Create(ctx, pod, client.DryRunAll)
}

// baselineProbeVolume returns an emptyDir volume, which every Kubernetes
// version accepts. It establishes whether the environment permits creating the
// probe Pod at all.
func baselineProbeVolume() corev1.Volume {
	return corev1.Volume{
		Name: imageVolumeProbeVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
}

// imageProbeVolume returns an ImageVolumeSource volume, the feature under test.
func imageProbeVolume() corev1.Volume {
	return corev1.Volume{
		Name: imageVolumeProbeVolumeName,
		VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{
				Reference:  imageVolumeProbeImage,
				PullPolicy: corev1.PullIfNotPresent,
			},
		},
	}
}
