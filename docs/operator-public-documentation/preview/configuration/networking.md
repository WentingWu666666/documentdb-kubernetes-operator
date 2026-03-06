---
title: Networking Configuration
description: Configure service types, external access, DNS, load balancer annotations, and Network Policies for DocumentDB on AKS, EKS, and GKE.
tags:
  - configuration
  - networking
  - load-balancer
---

# Networking Configuration

This guide covers networking configuration for the DocumentDB Kubernetes Operator, including service types, external access, DNS configuration, and cloud-specific load balancer annotations.

## Overview

DocumentDB exposes connectivity through Kubernetes Services. The operator creates and manages a service named `documentdb-service-<cluster-name>` that routes traffic to the primary database instance's gateway.

### Key Networking Fields

```yaml
spec:
  environment: aks                  # Cloud environment: aks, eks, gke
  exposeViaService:
    serviceType: LoadBalancer       # LoadBalancer or ClusterIP
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `environment` | string | No | — | Cloud environment identifier. Determines load balancer annotations. Options: `aks`, `eks`, `gke`. |
| `exposeViaService.serviceType` | string | No | — | Kubernetes Service type. Options: `LoadBalancer`, `ClusterIP`. |

For the full auto-generated type reference, see [ExposeViaService](../api-reference.md#exposeviaservice) in the API Reference.

### Default Port

DocumentDB gateway listens on port **10260** (MongoDB-compatible wire protocol over this port).

## Service Types

### ClusterIP (Internal Access)

ClusterIP exposes the service only within the Kubernetes cluster. Use this for applications running in the same cluster.

```yaml
apiVersion: documentdb.io/preview
kind: DocumentDB
metadata:
  name: my-documentdb
  namespace: default
spec:
  nodeCount: 1
  instancesPerNode: 3
  resource:
    storage:
      pvcSize: 100Gi
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

# In another terminal
mongosh "mongodb://<username>:<password>@localhost:10260/?directConnection=true"
```

### LoadBalancer (External Access)

LoadBalancer provisions a cloud load balancer for external access. The operator automatically applies cloud-specific annotations based on the `environment` field.

```yaml
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
  exposeViaService:
    serviceType: LoadBalancer
```

Get the external IP:

```bash
kubectl get svc documentdb-service-my-documentdb -n default \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

Connect externally:

```bash
EXTERNAL_IP=$(kubectl get svc documentdb-service-my-documentdb -n default \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
mongosh "mongodb://<username>:<password>@$EXTERNAL_IP:10260/?directConnection=true"
```

## Cloud-Specific Configuration

The operator automatically applies cloud-optimized annotations based on the `environment` field. Use content tabs below to see configuration for your cloud provider.

=== "AKS (Azure)"

    Set `environment: aks` to apply Azure-specific load balancer annotations.

    ```yaml
    spec:
      environment: aks
      exposeViaService:
        serviceType: LoadBalancer
    ```

    **Internal Load Balancer (private VNet only):**

    ```bash
    kubectl annotate svc documentdb-service-my-documentdb -n default \
      service.beta.kubernetes.io/azure-load-balancer-internal="true"
    ```

=== "EKS (AWS)"

    Set `environment: eks` to apply AWS-specific load balancer annotations. The operator configures an AWS Network Load Balancer (NLB).

    ```yaml
    spec:
      environment: eks
      exposeViaService:
        serviceType: LoadBalancer
    ```

    !!! note
        On EKS, the external endpoint may be a hostname (DNS name) rather than an IP address. Use the hostname directly in your connection string.

    ```bash
    # Get the external hostname
    kubectl get svc documentdb-service-my-documentdb -n default \
      -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
    ```

=== "GKE (Google Cloud)"

    Set `environment: gke` to apply GCP-specific load balancer annotations.

    ```yaml
    spec:
      environment: gke
      exposeViaService:
        serviceType: LoadBalancer
    ```

## DNS Configuration

### In-Cluster DNS

Kubernetes automatically creates DNS records for services. The format is:

```
<service-name>.<namespace>.svc.cluster.local
```

For DocumentDB:

```
documentdb-service-<cluster-name>.<namespace>.svc.cluster.local
```

Example connection string using DNS:

```
mongodb://<username>:<password>@documentdb-service-my-documentdb.default.svc.cluster.local:10260/?directConnection=true
```

### External DNS

For LoadBalancer services, you can set up external DNS records pointing to the load balancer's external IP or hostname.

#### Manual DNS Setup

1. Get the external IP/hostname:

    ```bash
    kubectl get svc documentdb-service-my-documentdb -n default
    ```

2. Create a DNS A record (for IP) or CNAME record (for hostname) pointing to the external address.

#### Using ExternalDNS

[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) can automatically manage DNS records. Annotate the service:

```bash
kubectl annotate svc documentdb-service-my-documentdb -n default \
  external-dns.alpha.kubernetes.io/hostname="documentdb.example.com"
```

## Service Routing

The operator configures the service selector to route traffic to the CNPG primary instance:

- **When endpoints are enabled**: The service selector targets pods with the label `cnpg.io/instanceRole: primary`, ensuring traffic always reaches the current primary.
- **During failover**: CNPG promotes a replica to primary and updates the pod labels. The service automatically routes to the new primary.

## Connection Strings

### Standard Connection String Format

```
mongodb://<username>:<password>@<host>:<port>/?directConnection=true
```

### With TLS

```
mongodb://<username>:<password>@<host>:<port>/?tls=true&directConnection=true
```

### Retrieving Credentials

```bash
# Get username
kubectl get secret documentdb-credentials -n default \
  -o jsonpath='{.data.username}' | base64 -d

# Get password
kubectl get secret documentdb-credentials -n default \
  -o jsonpath='{.data.password}' | base64 -d
```

### Connection String from Status

The operator populates the connection string in the DocumentDB status:

```bash
kubectl get documentdb my-documentdb -n default \
  -o jsonpath='{.status.connectionString}'
```

## Troubleshooting

### Network Policies

If your cluster has restrictive [NetworkPolicies](https://kubernetes.io/docs/concepts/services-networking/network-policies/), you must ensure the DocumentDB operator can reach the database pods, and that pods can communicate with each other.

#### Allow Operator-to-Cluster Communication

If you have a default-deny ingress policy, create an explicit policy to allow the operator:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-documentdb-operator
  namespace: default                       # Namespace where DocumentDB runs
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: documentdb
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: documentdb-operator
      ports:
        - protocol: TCP
          port: 8000
        - protocol: TCP
          port: 5432
```

#### Allow Application Access to DocumentDB

Restrict database access to specific application namespaces:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-app-to-documentdb
  namespace: default
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: documentdb
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              name: app-namespace
      ports:
        - protocol: TCP
          port: 10260
```

#### Allow Inter-Pod Communication

DocumentDB instances must communicate with each other for replication:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-documentdb-internal
  namespace: default
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: documentdb
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: documentdb
      ports:
        - protocol: TCP
          port: 5432
```

### LoadBalancer Pending

**Symptoms**: Service stays in `Pending` state, no external IP assigned.

```bash
kubectl describe svc documentdb-service-my-documentdb -n default
```

**Common causes**:

- Cloud provider quota exceeded for load balancers
- Missing permissions for the cloud controller manager
- Network configuration issues (subnet, security groups)

### Connection Timeout

**Symptoms**: Cannot connect to the external IP.

```bash
# Verify the service has an endpoint
kubectl get endpoints documentdb-service-my-documentdb -n default

# Check if the pod is running and ready
kubectl get pods -n default -l cnpg.io/instanceRole=primary
```

**Common causes**:

- Firewall or network security group blocking port 10260
- Pod is not ready (check pod events and logs)
- Service selector does not match any pods

### DNS Resolution Failure

**Symptoms**: In-cluster DNS name does not resolve.

```bash
# Test DNS from within the cluster
kubectl run dns-test --image=busybox --rm -it -- nslookup \
  documentdb-service-my-documentdb.default.svc.cluster.local
```

**Common causes**:

- CoreDNS is not running or misconfigured
- Incorrect namespace in the DNS name
- Service does not exist
