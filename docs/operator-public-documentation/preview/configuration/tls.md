---
title: TLS Configuration
description: Configure TLS encryption for DocumentDB gateway connections with SelfSigned, Provided, and CertManager modes, certificate rotation, and troubleshooting.
tags:
  - configuration
  - tls
  - security
---

# TLS Configuration

This guide covers TLS configuration for the DocumentDB Kubernetes Operator, including all supported modes, certificate management, rotation, monitoring, and troubleshooting.

## Overview

The DocumentDB operator supports TLS encryption for gateway connections via the `spec.tls` configuration. TLS protects data in transit between clients and the DocumentDB gateway.

## Configuration

Select your TLS mode below. Each tab shows prerequisites, the complete YAML configuration, and connection instructions.

=== "Disabled (default)"

    **Best for:** Development and testing only

    !!! danger "Not recommended for production"

    **Prerequisites:** None

    Disabled mode runs the gateway without TLS encryption. All traffic between clients and the gateway is unencrypted.

    ```yaml title="documentdb-tls-disabled.yaml"
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
      tls:
        gateway:
          mode: Disabled
    ```

    Connect without TLS:

    ```bash
    mongosh "mongodb://<username>:<password>@<host>:10260/?directConnection=true"
    ```

=== "SelfSigned"

    **Best for:** Development, testing, and environments without external PKI (Public Key Infrastructure)

    !!! note "Prerequisites"
        [cert-manager](https://cert-manager.io/) must be installed in the cluster. See [Install cert-manager](../index.md#install-cert-manager) for setup instructions.

    SelfSigned mode uses cert-manager to automatically generate and manage a self-signed CA and server certificate. No additional configuration is needed beyond setting the mode.

    ```yaml title="documentdb-tls-selfsigned.yaml"
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
          pvcSize: 10Gi
      tls:
        gateway:
          mode: SelfSigned
    ```

    The operator automatically creates a self-signed CA, generates a server certificate, and mounts it in the gateway pod.

    Connect with TLS using the CA certificate:

    ```bash
    # Extract the CA certificate
    kubectl get secret documentdb-gateway-cert-tls -n default \
      -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt

    # Connect with mongosh
    mongosh "mongodb://<username>:<password>@<host>:10260/?tls=true&directConnection=true" \
      --tls --tlsCAFile ca.crt
    ```

=== "CertManager"

    **Best for:** Production with existing cert-manager infrastructure

    !!! note "Prerequisites"
        [cert-manager](https://cert-manager.io/) must be installed (see [Install cert-manager](../index.md#install-cert-manager)), plus a configured [Issuer or ClusterIssuer](https://cert-manager.io/docs/concepts/issuer/).

    CertManager mode lets you use your own cert-manager [Issuer](https://cert-manager.io/docs/concepts/issuer/#namespaces) (namespace-scoped) or [ClusterIssuer](https://cert-manager.io/docs/concepts/issuer/) (cluster-scoped) to issue TLS certificates for the DocumentDB gateway. This is ideal for production environments that already have PKI infrastructure (for example, a corporate CA).

    Set `issuerRef.name` and `issuerRef.kind` to match your Issuer or ClusterIssuer. The operator will automatically request a certificate and mount it in the gateway.

    ```yaml title="documentdb-tls-certmanager.yaml"
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
      tls:
        gateway:
          mode: CertManager
          certManager:
            issuerRef:
              name: letsencrypt-prod # (1)!
              kind: ClusterIssuer # (2)!
            dnsNames: # (3)!
              - documentdb.example.com
              - "*.documentdb.example.com"
            secretName: my-documentdb-tls # (4)!
    ```

    1. Must match the `metadata.name` of your Issuer or ClusterIssuer.
    2. Use [`ClusterIssuer`](https://cert-manager.io/docs/concepts/issuer/#cluster-resource) for cluster-scoped issuers, or [`Issuer`](https://cert-manager.io/docs/concepts/issuer/#namespaces) for namespace-scoped.
    3. [Subject Alternative Names](https://en.wikipedia.org/wiki/Subject_Alternative_Name) — add all DNS names clients will use to connect.
    4. The Kubernetes Secret where cert-manager will store the issued certificate.

    For a complete list of CertManager fields, see the [API Reference — TLS Types](../api-reference.md#tlsconfiguration).

=== "Provided"

    **Best for:** Production with centralized certificate management

    !!! note "Prerequisites"
        A Kubernetes [TLS Secret](https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets) containing `tls.crt` and `tls.key` (and optionally `ca.crt`).

    Provided mode lets you supply your own TLS certificates. This is ideal when certificates are managed externally (for example, from Azure Key Vault, HashiCorp Vault, or a corporate CA).

    First, create a Kubernetes TLS Secret with your certificates:

    ```bash title="Create TLS secret"
    kubectl create secret generic my-documentdb-tls -n default \
      --from-file=tls.crt=server.crt \
      --from-file=tls.key=server.key \
      --from-file=ca.crt=ca.crt  # optional: include if clients need CA verification
    ```

    Then reference the secret in your DocumentDB configuration:

    ```yaml title="documentdb-tls-provided.yaml"
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
      tls:
        gateway:
          mode: Provided
          provided:
            secretName: my-documentdb-tls # (1)!
    ```

    1. The Secret must contain `tls.crt` (server certificate) and `tls.key` (private key). Optionally include `ca.crt` (CA certificate) if clients need to verify the server.

    For Azure Key Vault integration, see the [Manual Provided Mode Setup Guide](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/MANUAL-PROVIDED-MODE-SETUP.md).

## Certificate Rotation

### Automatic Rotation

- **SelfSigned and CertManager modes**: cert-manager automatically rotates certificates before expiration. The operator detects the updated Secret and reloads the gateway.
- **Provided mode**: Update the external Secret (or trigger a CSI driver sync). The operator picks up changes automatically.

### Monitoring Certificate Expiration

```bash
# Check certificate status via cert-manager
kubectl get certificate -n <namespace>

# Check expiration date
kubectl get secret <tls-secret> -n <namespace> \
  -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -dates

# Check DocumentDB TLS status
kubectl get documentdb <name> -n <namespace> \
  -o jsonpath='{.status.tls}' | jq
```

Example TLS status output:

```json
{
  "ready": true,
  "secretName": "documentdb-gateway-cert-tls",
  "message": ""
}
```

## Troubleshooting

### Certificate Not Ready

**Symptoms**: `tls.ready` is `false`, pods may not start.

```bash
# Check cert-manager certificate status
kubectl describe certificate -n <namespace>

# Check cert-manager logs
kubectl logs -n cert-manager deployment/cert-manager

# Check for pending CertificateRequests
kubectl get certificaterequest -n <namespace>
```

**Common causes**:

- cert-manager is not installed or not running
- The Issuer or ClusterIssuer does not exist or is not ready
- DNS validation is failing (for ACME/Let's Encrypt)

### TLS Connection Failures

**Symptoms**: Clients cannot connect with TLS enabled.

```bash
# Test TLS handshake directly
EXTERNAL_IP=$(kubectl get svc -n <namespace> \
  -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}')
openssl s_client -connect $EXTERNAL_IP:10260

# Check gateway logs
kubectl logs -n <namespace> <pod-name> -c gateway
```

**Common causes**:

- Client is not using the correct CA certificate
- Certificate SANs do not match the connection hostname
- The Secret is missing required keys (`tls.crt`, `tls.key`, `ca.crt`)

### Azure Key Vault Access Denied (Provided Mode)

**Symptoms**: Secret is not synced from Azure Key Vault. See the [Manual Provided Mode Setup Guide](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/MANUAL-PROVIDED-MODE-SETUP.md) for troubleshooting.

## Additional Resources

- [API Reference — TLS Types](../api-reference.md#tlsconfiguration) — Full field reference for TLSConfiguration and GatewayTLS
- [TLS Setup Scripts](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/README.md) — Automated setup and E2E testing
- [cert-manager Documentation](https://cert-manager.io/docs/)
