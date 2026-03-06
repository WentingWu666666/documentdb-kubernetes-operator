---
title: Resource Management
description: CPU and memory sizing guidelines for DocumentDB deployments including Kubernetes QoS classes, workload profiles, and monitoring recommendations.
tags:
  - configuration
  - resources
  - performance
---

# Resource Management

This guide covers CPU and memory sizing guidelines for DocumentDB deployments, including recommendations for different workload profiles, Kubernetes Quality of Service classes, and internal operator resource allocations.

## Overview

DocumentDB runs on Kubernetes and leverages the underlying CloudNative-PG operator for resource management. Proper resource allocation ensures stable performance, prevents out-of-memory kills, and optimizes cost.

!!! important
    For production database workloads, always configure explicit resource requests and limits. Running without resource constraints risks pod eviction, OOM kills, and CPU throttling during peak load.

## Kubernetes Quality of Service (QoS)

Kubernetes assigns a QoS class to each pod based on its resource configuration. For database workloads, we recommend **Guaranteed** QoS:

| QoS Class | Condition | Priority | Recommendation |
|-----------|-----------|----------|----------------|
| **Guaranteed** | Requests = Limits for all containers | Highest | **Recommended for production** |
| Burstable | Requests < Limits | Medium | Acceptable for development |
| Best-Effort | No requests or limits set | Lowest (evicted first) | Not recommended |

To achieve **Guaranteed** QoS, set requests and limits to the same value for both CPU and memory. This ensures that your DocumentDB pods are the last to be evicted under memory pressure.

!!! note
    When QoS is set to Guaranteed, CloudNative-PG configures the PostgreSQL `postmaster` process with an OOM adjustment value of `0`, keeping its low OOM score of `-997`. If the OOM killer is triggered, child processes are terminated before the `postmaster`, allowing for a clean shutdown. This behavior helps keep the database instance alive as long as possible.

## Sizing Guidelines

### Workload Profiles

=== "Development"

    A minimal cluster for development and testing:

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

    A production-ready cluster with high availability:

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
      instancesPerNode: 3 # (1)!
      environment: aks
      resource:
        storage:
          pvcSize: 100Gi
          storageClass: managed-csi-premium
          persistentVolumeReclaimPolicy: Retain
      tls:
        gateway:
          mode: SelfSigned
      exposeViaService:
        serviceType: LoadBalancer
      backup:
        retentionDays: 30
    ```

    1. Three instances provide Guaranteed QoS with one primary and two replicas for automatic failover.

=== "High-Load"

    For workloads requiring maximum throughput:

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
      environment: aks
      resource:
        storage:
          pvcSize: 1Ti
          storageClass: managed-csi-premium
          persistentVolumeReclaimPolicy: Retain
      tls:
        gateway:
          mode: SelfSigned
      exposeViaService:
        serviceType: LoadBalancer
      backup:
        retentionDays: 30
    ```

## Internal Operator Resources

The operator allocates resources for internal processes such as SQL jobs (schema migrations, extension upgrades). These are pre-configured and not user-configurable.

### SQL Job Resources

| Resource | Request | Limit |
|----------|---------|-------|
| Memory | 32 Mi | 64 Mi |
| CPU | 10m | 50m |

SQL jobs run as non-root (UID 1000) with privilege escalation disabled.

## Monitoring Resource Usage

### Pod-Level Metrics

```bash
# View resource usage for DocumentDB pods
kubectl top pods -n default -l app.kubernetes.io/name=documentdb

# View detailed resource requests and limits
kubectl describe pod <pod-name> -n default | grep -A 5 "Requests\|Limits"
```

### Cluster-Level Monitoring

For comprehensive monitoring, consider setting up:

- **Prometheus + Grafana**: See the [Telemetry Guide](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/telemetry/README.md) for setup instructions
- **Kubernetes Metrics Server**: Required for `kubectl top` commands
- **Cloud provider monitoring**: Azure Monitor, CloudWatch, or Google Cloud Monitoring

## Best Practices

1. **Always use 3 instances for production** — Set `instancesPerNode: 3` for automatic failover and read scalability.

2. **Use Guaranteed QoS for production** — Set resource requests equal to limits to prevent pod eviction under memory pressure.

3. **Use premium storage** — SSDs provide significantly better I/O performance for database workloads. Benchmark storage before going to production.

4. **Set the `Retain` reclaim policy** — Prevents accidental data loss. Clean up PVs manually after confirming data is safely backed up.

5. **Monitor disk usage** — Expand storage proactively before reaching 80% capacity. See [Storage Configuration](storage.md) for volume expansion instructions.

6. **Configure backups** — Set `spec.backup.retentionDays` and create a `ScheduledBackup` resource. See [API Reference](../api-reference.md) for details.

7. **Enable TLS in production** — Use `SelfSigned` mode at minimum. See [TLS Configuration](tls.md) for options.

8. **Set the environment field** — Specify `environment: aks`, `eks`, or `gke` to get cloud-optimized service annotations. See [Networking Configuration](networking.md).

9. **Use dedicated nodes for databases** — Use `spec.affinity` with `nodeSelector` to schedule database pods on dedicated nodes, isolating them from other workloads. See the [CloudNative-PG scheduling documentation](https://cloudnative-pg.io/docs/1.28/scheduling/) for details.
