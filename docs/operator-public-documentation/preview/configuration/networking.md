---
title: Networking Configuration
description: Configure service types, external access, DNS, load balancer annotations, and Network Policies for DocumentDB on AKS, EKS, and GKE.
tags:
  - configuration
  - networking
  - load-balancer
---

# Networking Configuration

## Overview

DocumentDB exposes connectivity through a Kubernetes Service named `documentdb-service-<cluster-name>`. The gateway listens on port **10260** (MongoDB-compatible wire protocol).

```yaml
apiVersion: documentdb.io/preview
kind: DocumentDB
metadata:
  name: my-documentdb
spec:
  environment: aks               # Optional: aks | eks | gke
  exposeViaService:
    serviceType: LoadBalancer     # ClusterIP (default) | LoadBalancer
```

For the full field reference, see [ExposeViaService](../api-reference.md#exposeviaservice) in the API Reference.

## Service Types

=== "ClusterIP (Internal)"

    Exposes the service only within the Kubernetes cluster.

    ```yaml
    apiVersion: documentdb.io/preview
    kind: DocumentDB
    metadata:
      name: my-documentdb
      namespace: default
    spec:
      nodeCount: 1
      instancesPerNode: 1
      resource:
        storage:
          pvcSize: 10Gi
      exposeViaService:
        serviceType: ClusterIP
    ```

    Connect from within the cluster:

    ```bash
    mongosh "mongodb://<username>:<password>@documentdb-service-my-documentdb.default.svc.cluster.local:10260/?directConnection=true"
    ```

    For local development, use port-forwarding:

    ```bash
    kubectl port-forward svc/documentdb-service-my-documentdb -n default 10260:10260
    mongosh "mongodb://<username>:<password>@localhost:10260/?directConnection=true"
    ```

=== "LoadBalancer (External)"

    Provisions a cloud load balancer for external access. Set the `environment` field to get cloud-optimized annotations.

    === "AKS"

        ```yaml
        apiVersion: documentdb.io/preview
        kind: DocumentDB
        metadata:
          name: my-documentdb
          namespace: default
        spec:
          nodeCount: 1
          instancesPerNode: 1
          resource:
            storage:
              pvcSize: 10Gi
          environment: aks
          exposeViaService:
            serviceType: LoadBalancer
        ```

        Get the external IP and connect:

        ```bash
        EXTERNAL_IP=$(kubectl get svc documentdb-service-my-documentdb -n default \
          -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
        mongosh "mongodb://<username>:<password>@$EXTERNAL_IP:10260/?directConnection=true"
        ```

    === "EKS"

        ```yaml
        apiVersion: documentdb.io/preview
        kind: DocumentDB
        metadata:
          name: my-documentdb
          namespace: default
        spec:
          nodeCount: 1
          instancesPerNode: 1
          resource:
            storage:
              pvcSize: 10Gi
          environment: eks
          exposeViaService:
            serviceType: LoadBalancer
        ```

        !!! note
            On EKS, the external endpoint is a hostname rather than an IP. Use it directly in your connection string.

        ```bash
        HOSTNAME=$(kubectl get svc documentdb-service-my-documentdb -n default \
          -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
        mongosh "mongodb://<username>:<password>@$HOSTNAME:10260/?directConnection=true"
        ```

    === "GKE"

        ```yaml
        apiVersion: documentdb.io/preview
        kind: DocumentDB
        metadata:
          name: my-documentdb
          namespace: default
        spec:
          nodeCount: 1
          instancesPerNode: 1
          resource:
            storage:
              pvcSize: 10Gi
          environment: gke
          exposeViaService:
            serviceType: LoadBalancer
        ```

        Get the external IP and connect:

        ```bash
        EXTERNAL_IP=$(kubectl get svc documentdb-service-my-documentdb -n default \
          -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
        mongosh "mongodb://<username>:<password>@$EXTERNAL_IP:10260/?directConnection=true"
        ```

## Network Policies

If your cluster uses restrictive [NetworkPolicies](https://kubernetes.io/docs/concepts/services-networking/network-policies/), ensure the following traffic is allowed:

| Traffic | From | To | Port |
|---------|------|----|------|
| Application → Gateway | Application namespace | DocumentDB pods | 10260 |
| CNPG instance manager | CNPG operator / DocumentDB pods | DocumentDB pods | 8000 |
| Database replication | DocumentDB pods | DocumentDB pods | 5432 |

!!! note
    The replication rule (port 5432) is only needed when `instancesPerNode > 1`.

### Example NetworkPolicy Configuration

If your cluster enforces a default-deny ingress policy, apply the following to allow DocumentDB traffic.

=== "Gateway Access (port 10260)"

    Allow application traffic to the DocumentDB gateway:

    ```yaml
    apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: allow-documentdb-gateway
      namespace: <documentdb-namespace>
    spec:
      podSelector:
        matchLabels:
          app: <documentdb-name>          # matches your DocumentDB CR name
      policyTypes:
      - Ingress
      ingress:
      - ports:
        - protocol: TCP
          port: 10260
    ```

=== "CNPG Instance Manager (port 8000)"

    Allow CNPG operator health checks. **Required** — without this, CNPG cannot manage pod lifecycle.

    ```yaml
    apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: allow-cnpg-status
      namespace: <documentdb-namespace>
    spec:
      podSelector:
        matchLabels:
          app: <documentdb-name>
      policyTypes:
      - Ingress
      ingress:
      - ports:
        - protocol: TCP
          port: 8000
    ```

=== "Replication (port 5432)"

    Allow pod-to-pod replication traffic. Only needed when `instancesPerNode > 1`.

    ```yaml
    apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: allow-documentdb-replication
      namespace: <documentdb-namespace>
    spec:
      podSelector:
        matchLabels:
          app: <documentdb-name>
      policyTypes:
      - Ingress
      ingress:
      - from:
        - podSelector:
            matchLabels:
              app: <documentdb-name>
        ports:
        - protocol: TCP
          port: 5432
    ```

See the [Kubernetes NetworkPolicy documentation](https://kubernetes.io/docs/concepts/services-networking/network-policies/) for more details.
