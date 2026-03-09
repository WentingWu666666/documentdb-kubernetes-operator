---
title: Storage Configuration
description: Configure persistent storage for DocumentDB including storage classes, PVC sizing, volume expansion, reclaim policies, and disk encryption across AKS, EKS, and GKE.
tags:
  - configuration
  - storage
  - encryption
---

# Storage Configuration

## Overview

DocumentDB uses Kubernetes [PersistentVolumeClaims (PVCs)](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#persistentvolumeclaims) to request storage, which are backed by [PersistentVolumes (PVs)](https://kubernetes.io/docs/concepts/storage/persistent-volumes/) provisioned by your cloud provider.

```yaml
apiVersion: documentdb.io/preview
kind: DocumentDB
metadata:
  name: my-documentdb
spec:
  resource:
    storage:
      pvcSize: 100Gi                           # Required: storage size
      storageClass: managed-csi-premium         # Optional: defaults to Kubernetes default StorageClass
      persistentVolumeReclaimPolicy: Retain     # Optional: Retain or Delete
```

For the full field reference, see [StorageConfiguration](../api-reference.md#storageconfiguration) in the API Reference.

## PVC Sizing

!!! tip
    Provision at least **2x** your expected data size to allow for WAL files, temporary files, and growth.

!!! warning
    PVC size is set at cluster creation time. Resizing an existing PVC by updating `pvcSize` is **not yet supported** — the change will be accepted but not applied. See [#298](https://github.com/documentdb/documentdb-kubernetes-operator/issues/298) for tracking.

## Reclaim Policy

| Policy | Behavior |
|--------|----------|
| `Retain` (default) | PV is preserved after PVC deletion. **Recommended for production.** |
| `Delete` | PV and underlying storage are deleted with the PVC. Suitable for development. |

With `Retain`, you can recover data from a retained PV after cluster deletion. See [PersistentVolume Retention and Recovery](../backup-and-restore.md#persistentvolume-retention-and-recovery) for restore steps.

## Storage Classes

A [StorageClass](https://kubernetes.io/docs/concepts/storage/storage-classes/) defines the type of underlying disk (e.g., SSD vs HDD) and provisioner used for persistent volumes. If you don't specify one, Kubernetes uses the default StorageClass in your cluster.

To see available StorageClasses and which one is the default:

```bash
kubectl get storageclass
```

The default is marked with `(default)` in the output.

### Recommended Storage Classes by Provider

For production database workloads, use an SSD-backed StorageClass:

| Provider | StorageClass | Notes |
|----------|-------------|-------|
| **AKS** | `managed-csi-premium` | Azure Premium SSD v2. |
| **EKS** | `gp3` | AWS GP3 EBS. |
| **GKE** | `premium-rwo` | Google SSD persistent disk. |

## Disk Encryption

| Provider | Default Encryption | Customer-Managed Keys |
|----------|-------------------|----------------------|
| **AKS** | ✅ Enabled (platform-managed keys) | Optional via [DiskEncryptionSet](https://learn.microsoft.com/azure/aks/azure-disk-customer-managed-keys) |
| **GKE** | ✅ Enabled (Google-managed keys) | Optional via [CMEK](https://cloud.google.com/kubernetes-engine/docs/how-to/using-cmek) |
| **EKS** | ❌ **Not enabled by default** | Set `encrypted: "true"` in [StorageClass](https://docs.aws.amazon.com/eks/latest/userguide/ebs-csi.html) |

!!! warning
    For production on EKS, always create a StorageClass with `encrypted: "true"` to ensure data at rest is protected.
