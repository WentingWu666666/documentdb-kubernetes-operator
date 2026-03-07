---
title: Networking Configuration
description: Configure service types, external access, DNS, load balancer annotations, and Network Policies for DocumentDB on AKS, EKS, and GKE.
tags:
  - configuration
  - networking
  - load-balancer
---

# Networking Configuration

Configure how clients connect to your DocumentDB cluster.

## Overview

DocumentDB exposes connectivity through a Kubernetes Service named `documentdb-service-<cluster-name>`. The gateway listens on port **10260** (MongoDB-compatible wire protocol).

For the full field reference, see [ExposeViaService](../api-reference.md#exposeviaservice) in the API Reference.

## Service Types

=== "ClusterIP (Internal)"

    Exposes the service only within the Kubernetes cluster.

    ```yaml
    spec:
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

    === "AKS (Azure)"

        ```yaml
        spec:
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

        For an internal load balancer (private VNet only):

        ```bash
        kubectl annotate svc documentdb-service-my-documentdb -n default \
          service.beta.kubernetes.io/azure-load-balancer-internal="true"
        ```

    === "EKS (AWS)"

        ```yaml
        spec:
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

    === "GKE (Google Cloud)"

        ```yaml
        spec:
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
| Operator → Database | `documentdb-operator` namespace | DocumentDB pods | 8000, 5432 |
| Application → Gateway | Application namespace | DocumentDB pods | 10260 |
| Database replication | DocumentDB pods | DocumentDB pods | 5432 |

See the [Kubernetes NetworkPolicy documentation](https://kubernetes.io/docs/concepts/services-networking/network-policies/) for examples.

## Troubleshooting

| Problem | Common Causes |
|---------|---------------|
| **LoadBalancer stuck in Pending** | Cloud provider quota exceeded; missing cloud controller permissions; subnet/security group misconfiguration |
| **Connection timeout to external IP** | Firewall blocking port 10260; pod not ready; service selector mismatch |
| **In-cluster DNS not resolving** | CoreDNS not running; wrong namespace in DNS name; service does not exist |
