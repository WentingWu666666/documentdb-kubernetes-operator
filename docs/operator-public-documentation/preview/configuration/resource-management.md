---
title: Resource Management
description: CPU and memory sizing guidelines for DocumentDB deployments including Kubernetes QoS classes, workload profiles, and monitoring recommendations.
tags:
  - configuration
  - resources
  - performance
---

# Resource Management

CPU and memory sizing guidelines for DocumentDB deployments.

## Overview

Proper resource allocation ensures stable performance and prevents pod eviction. For production, always configure explicit resource requests and limits.

!!! tip "Guaranteed QoS"
    Set resource requests equal to limits to achieve [Guaranteed QoS](https://kubernetes.io/docs/concepts/workloads/pods/pod-qos/#guaranteed). This gives your database pods the highest eviction priority — they are the last to be evicted under memory pressure.

## Sizing Guidelines

### Workload Profiles

=== "Development"

    | Setting | Value |
    |---------|-------|
    | Instances | 1 |
    | CPU | 1 (default) |
    | Memory | 2 Gi (default) |
    | Storage | 10–20 Gi |

    ```yaml title="documentdb-dev.yaml"
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: dev-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 1
      resource:
        storage:
          pvcSize: 10Gi
    ```

=== "Production"

    | Setting | Value |
    |---------|-------|
    | Instances | 3 |
    | CPU | 2–4 CPUs |
    | Memory | 4–8 Gi |
    | Storage | 100–500 Gi |

    ```yaml title="documentdb-prod.yaml"
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: prod-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 3
      resource:
        storage:
          pvcSize: 100Gi
          storageClass: managed-csi-premium
          persistentVolumeReclaimPolicy: Retain
    ```

=== "High-Load"

    | Setting | Value |
    |---------|-------|
    | Instances | 3 |
    | CPU | 4–8 CPUs |
    | Memory | 8–16 Gi |
    | Storage | 500 Gi–2 Ti |

    ```yaml title="documentdb-highload.yaml"
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: highload-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 3
      resource:
        storage:
          pvcSize: 1Ti
          storageClass: managed-csi-premium
          persistentVolumeReclaimPolicy: Retain
    ```

## Best Practices

1. **Use 3 instances for production** — `instancesPerNode: 3` enables automatic failover with one primary and two replicas.
2. **Use Guaranteed QoS** — Set resource requests equal to limits to prevent pod eviction under memory pressure.
3. **Use premium storage** — SSDs provide significantly better I/O for database workloads.
4. **Set `Retain` reclaim policy** — Prevents accidental data loss. See [Storage Configuration](storage.md).
5. **Monitor disk usage** — Expand storage proactively before reaching 80% capacity.
