---
title: Storage Configuration
description: Configure persistent storage for DocumentDB including storage classes, PVC sizing, volume expansion, reclaim policies, and disk encryption across AKS, EKS, and GKE.
tags:
  - configuration
  - storage
  - encryption
---

# Storage Configuration

This guide covers storage configuration for the DocumentDB Kubernetes Operator, including storage classes, PVC sizing, volume expansion, reclaim policies, security hardening, and disk encryption across cloud providers.

## Overview

DocumentDB uses Kubernetes PersistentVolumeClaims (PVCs) for database storage. The operator manages PersistentVolumes with security-hardened settings and provides flexible configuration for different environments.

### Storage Fields

```yaml
spec:
  resource:
    storage:
      pvcSize: 100Gi                           # Required: storage size
      storageClass: managed-csi-premium         # Optional: StorageClass name
      persistentVolumeReclaimPolicy: Retain     # Optional: Retain or Delete
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `pvcSize` | string | Yes | — | Size of the PersistentVolumeClaim (for example, `10Gi`, `100Gi`, `1Ti`). |
| `storageClass` | string | No | Cluster default | Kubernetes StorageClass name. If omitted, the cluster's default StorageClass is used. |
| `persistentVolumeReclaimPolicy` | string | No | `Retain` | What happens to the PersistentVolume when the PVC is deleted. Options: `Retain`, `Delete`. |

For the full auto-generated type reference, see [StorageConfiguration](../api-reference.md#storageconfiguration) in the API Reference.

## Storage Classes

### Recommended Storage Classes by Provider

| Provider | StorageClass | Provisioner | Notes |
|----------|-------------|-------------|-------|
| **AKS** | `managed-csi-premium` | `disk.csi.azure.com` | Azure Premium SSD v2. Recommended for production. |
| **EKS** | `gp3` | `ebs.csi.aws.com` | AWS GP3 EBS volumes. Good balance of price and performance. |
| **GKE** | `premium-rwo` | `pd.csi.storage.gke.io` | Google SSD persistent disk. |
| **Kind** | `standard` (default) | `rancher.io/local-path` | Local path provisioner. Development only. |
| **Minikube** | `standard` (default) | `k8s.io/minikube-hostpath` | Host path provisioner. Development only. |

### Using a Specific Storage Class

=== "AKS"

    ```yaml title="documentdb-aks-storage.yaml"
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: my-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 3
      environment: aks
      resource:
        storage:
          pvcSize: 100Gi
          storageClass: managed-csi-premium # (1)!
    ```

    1. Azure Premium SSD v2 via `disk.csi.azure.com`. Recommended for production workloads.

=== "EKS"

    ```yaml title="documentdb-eks-storage.yaml"
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: my-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 3
      environment: eks
      resource:
        storage:
          pvcSize: 100Gi
          storageClass: gp3 # (1)!
    ```

    1. AWS GP3 EBS volumes. Good balance of price and performance.

=== "GKE"

    ```yaml title="documentdb-gke-storage.yaml"
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: my-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 3
      environment: gke
      resource:
        storage:
          pvcSize: 100Gi
          storageClass: premium-rwo # (1)!
    ```

    1. Google SSD persistent disk via `pd.csi.storage.gke.io`.

### Verifying Available Storage Classes

```bash
# List all storage classes
kubectl get storageclass

# Check default storage class
kubectl get storageclass -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}'

# Check if a storage class supports volume expansion
kubectl get storageclass <name> -o jsonpath='{.allowVolumeExpansion}'
```

## Benchmarking Storage

!!! important
    Before deploying DocumentDB to production, benchmark your storage to set clear performance expectations. Database workloads are highly sensitive to I/O latency and throughput.

We recommend a two-level benchmarking approach:

1. **Storage-level**: Use [fio](https://fio.readthedocs.io/en/latest/fio_doc.html) to measure raw throughput for sequential reads, sequential writes, random reads, and random writes.
2. **Database-level**: Run representative workloads against your DocumentDB cluster to measure end-to-end performance.

Know your storage characteristics — these baselines are invaluable during capacity planning and incident response.

## Block Storage Considerations (Ceph/Longhorn)

Most block storage solutions in Kubernetes, such as Longhorn and Ceph, recommend having multiple replicas of a volume to enhance resiliency. This works well for workloads without built-in replication.

However, DocumentDB (via CloudNative-PG) provides its own replication through multiple instances. Combining storage-level replication with database-level replication can result in:

- **Unnecessary I/O amplification** — Every write is replicated at both the storage and database layers
- **Increased latency** — Additional replication hops add latency to write operations
- **Higher cost** — Storage capacity is multiplied by the storage replica factor

!!! tip
    For DocumentDB clusters with `instancesPerNode: 3`, consider using storage with a single replica (or no replication) since the database already handles data redundancy. Consult your storage provider's documentation for configuration details.

## PVC Sizing

### Sizing Guidelines

| Workload | Recommended Size | Notes |
|----------|-----------------|-------|
| Development / Testing | 10–20 Gi | Minimal data, local clusters |
| Small production | 50–100 Gi | Light workloads, < 50 GB data |
| Medium production | 100–500 Gi | Moderate workloads, 50–200 GB data |
| Large production | 500 Gi–2 Ti | Heavy workloads, > 200 GB data |

!!! tip
    Provision at least **2x** your expected data size to allow for WAL files, temporary files, and growth. Monitor disk usage and expand before reaching 80% capacity.

## Volume Expansion

You can increase PVC size without downtime if the StorageClass supports volume expansion.

### Prerequisites

Verify that your StorageClass has `allowVolumeExpansion: true`:

```bash
kubectl get storageclass <name> -o yaml | grep allowVolumeExpansion
```

### Expanding Storage

Update the `pvcSize` field in your DocumentDB spec:

```bash
kubectl patch documentdb my-documentdb -n default --type='json' \
  -p='[{"op": "replace", "path": "/spec/resource/storage/pvcSize", "value": "200Gi"}]'
```

Or edit the resource directly:

```bash
kubectl edit documentdb my-documentdb -n default
```

!!! warning
    Volume expansion is a one-way operation. You cannot shrink a PVC after expanding it.

## Reclaim Policy

The reclaim policy determines what happens to PersistentVolumes when the associated PVC is deleted.

| Policy | Behavior | Use Case |
|--------|----------|----------|
| `Retain` (default) | PV is preserved after PVC deletion. Data remains on disk. | Production. Protects against accidental data loss. |
| `Delete` | PV and underlying storage are deleted with the PVC. | Development and testing. Automatic cleanup. |

```yaml
spec:
  resource:
    storage:
      pvcSize: 100Gi
      persistentVolumeReclaimPolicy: Retain   # Recommended for production
```

!!! note
    The operator defaults to `Retain` to prevent accidental data loss. For production workloads, always use `Retain` and manually clean up PVs after confirming the data is no longer needed.

## PersistentVolume Security

The operator automatically applies security-hardening mount options to all PersistentVolumes associated with DocumentDB clusters:

| Mount Option | Description |
|-------------|-------------|
| `nodev` | Prevents device files from being interpreted on the filesystem |
| `nosuid` | Prevents setuid/setgid bits from taking effect |
| `noexec` | Prevents execution of binaries on the filesystem |

These options are applied automatically by the PV controller and require no configuration. They are compatible with major cloud storage provisioners (Azure Disk, AWS EBS, GCE PD).

!!! note
    Local-path provisioners used by Kind (`rancher.io/local-path`) and Minikube (`k8s.io/minikube-hostpath`) do not support mount options. The operator auto-detects these provisioners and skips applying mount options.

## Disk Encryption

Encryption at rest protects sensitive database data stored on disk. Configuration varies by cloud provider.

=== "AKS (Azure)"

    AKS encrypts all managed disks by default using Azure Storage Service Encryption (SSE) with platform-managed keys. **No additional configuration is required.**

    For customer-managed keys (CMK), create a StorageClass with a Disk Encryption Set:

    ```yaml title="storageclass-aks-encrypted.yaml"
    apiVersion: storage.k8s.io/v1
    kind: StorageClass
    metadata:
      name: managed-csi-encrypted
    provisioner: disk.csi.azure.com
    parameters:
      skuName: Premium_LRS
      diskEncryptionSetID: /subscriptions/<sub-id>/resourceGroups/<rg>/providers/Microsoft.Compute/diskEncryptionSets/<des-name>
    reclaimPolicy: Delete
    volumeBindingMode: WaitForFirstConsumer
    allowVolumeExpansion: true
    ```

=== "GKE (Google Cloud)"

    GKE encrypts all persistent disks by default using Google-managed encryption keys. **No additional configuration is required.**

    For customer-managed encryption keys (CMEK):

    ```yaml title="storageclass-gke-encrypted.yaml"
    apiVersion: storage.k8s.io/v1
    kind: StorageClass
    metadata:
      name: pd-ssd-encrypted
    provisioner: pd.csi.storage.gke.io
    parameters:
      type: pd-ssd
      disk-encryption-kms-key: projects/<project>/locations/<region>/keyRings/<keyring>/cryptoKeys/<key>
    reclaimPolicy: Delete
    volumeBindingMode: WaitForFirstConsumer
    allowVolumeExpansion: true
    ```

=== "EKS (AWS)"

    !!! warning
        Unlike AKS and GKE, EBS volumes on EKS are **not encrypted by default**. You must explicitly enable encryption.

    ```yaml title="storageclass-eks-encrypted.yaml"
    apiVersion: storage.k8s.io/v1
    kind: StorageClass
    metadata:
      name: ebs-sc-encrypted
    provisioner: ebs.csi.aws.com
    parameters:
      type: gp3
      encrypted: "true"
      # Optional: specify a KMS key for customer-managed encryption
      # kmsKeyId: arn:aws:kms:<region>:<account-id>:key/<key-id>
    reclaimPolicy: Delete
    volumeBindingMode: WaitForFirstConsumer
    allowVolumeExpansion: true
    ```

    Then reference the encrypted StorageClass in your DocumentDB spec:

    ```yaml
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: my-documentdb
      namespace: default
    spec:
      environment: eks
      nodeCount: 1
      instancesPerNode: 3
      resource:
        storage:
          pvcSize: 100Gi
          storageClass: ebs-sc-encrypted
    ```

### Encryption Summary

| Provider | Default Encryption | Customer-Managed Keys |
|----------|-------------------|----------------------|
| **AKS** | ✅ Enabled (SSE with platform keys) | Optional via DiskEncryptionSet |
| **GKE** | ✅ Enabled (Google-managed keys) | Optional via CMEK |
| **EKS** | ❌ **Not enabled** | Required: set `encrypted: "true"` in StorageClass |

!!! tip
    For production deployments on EKS, always create a StorageClass with `encrypted: "true"` to ensure data at rest is protected.
