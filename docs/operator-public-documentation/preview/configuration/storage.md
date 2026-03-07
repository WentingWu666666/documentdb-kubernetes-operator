---
title: Storage Configuration
description: Configure persistent storage for DocumentDB including storage classes, PVC sizing, volume expansion, reclaim policies, and disk encryption across AKS, EKS, and GKE.
tags:
  - configuration
  - storage
  - encryption
---

# Storage Configuration

Configure persistent storage for DocumentDB clusters.

## Overview

DocumentDB uses Kubernetes PersistentVolumeClaims (PVCs) for database storage. The operator manages volumes with security-hardened settings.

```yaml
spec:
  resource:
    storage:
      pvcSize: 100Gi                           # Required: storage size
      storageClass: managed-csi-premium         # Optional: StorageClass name
      persistentVolumeReclaimPolicy: Retain     # Optional: Retain or Delete
```

For the full field reference, see [StorageConfiguration](../api-reference.md#storageconfiguration) in the API Reference.

## Storage Classes

### Recommended Storage Classes by Provider

| Provider | StorageClass | Notes |
|----------|-------------|-------|
| **AKS** | `managed-csi-premium` | Azure Premium SSD v2. Recommended for production. |
| **EKS** | `gp3` | AWS GP3 EBS. Good balance of price and performance. |
| **GKE** | `premium-rwo` | Google SSD persistent disk. |
| **Kind / Minikube** | `standard` (default) | Development only. |

### Example

```yaml
spec:
  resource:
    storage:
      pvcSize: 100Gi
      storageClass: managed-csi-premium   # Replace with your provider's class
```

## PVC Sizing

| Workload | Recommended Size |
|----------|-----------------|
| Development / Testing | 10–20 Gi |
| Small production | 50–100 Gi |
| Medium production | 100–500 Gi |
| Large production | 500 Gi–2 Ti |

!!! tip
    Provision at least **2x** your expected data size to allow for WAL files, temporary files, and growth.

## Volume Expansion

You can increase PVC size without downtime if the StorageClass supports volume expansion (`allowVolumeExpansion: true`).

```bash
kubectl patch documentdb my-documentdb -n default --type='json' \
  -p='[{"op": "replace", "path": "/spec/resource/storage/pvcSize", "value": "200Gi"}]'
```

!!! warning
    Volume expansion is a one-way operation. You cannot shrink a PVC after expanding it.

## Reclaim Policy

| Policy | Behavior |
|--------|----------|
| `Retain` (default) | PV is preserved after PVC deletion. **Recommended for production.** |
| `Delete` | PV and underlying storage are deleted with the PVC. Suitable for development. |

## Disk Encryption

| Provider | Default Encryption | Customer-Managed Keys |
|----------|-------------------|----------------------|
| **AKS** | ✅ Enabled (platform-managed keys) | Optional via [DiskEncryptionSet](https://learn.microsoft.com/azure/aks/azure-disk-customer-managed-keys) |
| **GKE** | ✅ Enabled (Google-managed keys) | Optional via [CMEK](https://cloud.google.com/kubernetes-engine/docs/how-to/using-cmek) |
| **EKS** | ❌ **Not enabled by default** | Set `encrypted: "true"` in [StorageClass](https://docs.aws.amazon.com/eks/latest/userguide/ebs-csi.html) |

!!! warning
    For production on EKS, always create a StorageClass with `encrypted: "true"` to ensure data at rest is protected.
