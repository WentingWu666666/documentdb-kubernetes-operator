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

### Supported Modes

| Mode | Description | Best For |
|------|-------------|----------|
| `Disabled` (default) | No TLS encryption | Development and testing only |
| `SelfSigned` | Automatic certificates via cert-manager with a self-signed CA | Development, testing, and environments without external PKI (Public Key Infrastructure) |
| `Provided` | Bring your own certificates (for example, from Azure Key Vault) | Production with centralized certificate management |
| `CertManager` | Custom cert-manager issuers (for example, Let's Encrypt, corporate CA) | Production with existing cert-manager infrastructure |

### Prerequisites

- **SelfSigned mode**: [cert-manager](https://cert-manager.io/) must be installed in the cluster
- **CertManager mode**: [cert-manager](https://cert-manager.io/) installed, plus a configured Issuer or ClusterIssuer
- **Provided mode**: A Kubernetes TLS Secret containing `tls.crt`, `tls.key`, and `ca.crt`

## Configuration

Select your TLS mode below. Each tab shows the complete YAML configuration and connection instructions.

=== "Disabled"

    !!! danger "Not recommended for production"

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
          mode: SelfSigned # (1)!
    ```

    1. Requires [cert-manager](https://cert-manager.io/) installed in the cluster. The operator handles CA and certificate generation automatically.

    The operator will:

    1. Create a self-signed CA Issuer
    2. Generate a CA certificate
    3. Create a server certificate signed by the CA
    4. Mount the certificate in the gateway pod

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

    CertManager mode uses a custom cert-manager Issuer or ClusterIssuer to issue certificates. This is ideal for production environments with existing PKI infrastructure.

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
            secretName: my-documentdb-tls
    ```

    1. Name of your cert-manager Issuer or ClusterIssuer resource.
    2. Use `ClusterIssuer` for cluster-scoped issuers, or `Issuer` for namespace-scoped.
    3. Subject Alternative Names — add all DNS names clients will use to connect.

    #### CertManager Field Reference

    | Field | Type | Required | Default | Description |
    |-------|------|----------|---------|-------------|
    | `issuerRef.name` | string | Yes | — | Name of the cert-manager Issuer or ClusterIssuer. |
    | `issuerRef.kind` | string | No | `Issuer` | Kind of the issuer: `Issuer` (namespace-scoped) or `ClusterIssuer` (cluster-scoped). |
    | `issuerRef.group` | string | No | `cert-manager.io` | API group of the issuer. |
    | `dnsNames` | []string | No | — | Subject Alternative Names for the certificate. |
    | `secretName` | string | No | Auto-generated | Name of the Kubernetes Secret to store the issued certificate. |

=== "Provided"

    Provided mode lets you supply your own TLS certificates. This is ideal when certificates are managed externally (for example, from Azure Key Vault, HashiCorp Vault, or a corporate CA).

    First, create a Kubernetes TLS Secret with your certificates:

    ```bash title="Create TLS secret"
    kubectl create secret generic my-documentdb-tls -n default \
      --from-file=tls.crt=server.crt \
      --from-file=tls.key=server.key \
      --from-file=ca.crt=ca.crt
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

    1. The Secret must contain three keys: `tls.crt` (server certificate), `tls.key` (private key), and `ca.crt` (CA certificate).

    #### Secret Requirements

    The TLS Secret must contain these keys:

    | Key | Description |
    |-----|-------------|
    | `tls.crt` | Server certificate (PEM-encoded). |
    | `tls.key` | Private key for the server certificate (PEM-encoded). |
    | `ca.crt` | Certificate Authority certificate used to sign the server certificate (PEM-encoded). |

#### Example: Let's Encrypt with ClusterIssuer

First, create a ClusterIssuer:

```yaml title="letsencrypt-clusterissuer.yaml"
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - http01:
          ingress:
            class: nginx
```

Then reference it in your DocumentDB resource using the CertManager tab above.

### Azure Key Vault Integration

For production Azure deployments, use the Secrets Store CSI driver to sync certificates from Azure Key Vault:

1. Enable the CSI driver on your AKS cluster
2. Create a `SecretProviderClass` that references your Key Vault certificate
3. The CSI driver syncs the certificate as a Kubernetes Secret
4. Reference the synced Secret name in `provided.secretName`

For a complete walkthrough, see the [Manual Provided Mode Setup Guide](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/MANUAL-PROVIDED-MODE-SETUP.md).

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

**Symptoms**: Secret is not synced from Azure Key Vault.

```bash
# Check SecretProviderClass status
kubectl describe secretproviderclass -n <namespace>

# Check CSI driver pods
kubectl get pods -n kube-system -l app=secrets-store-csi-driver
```

**Common causes**:

- Managed identity does not have `Key Vault Secrets User` role on the Key Vault
- The Key Vault firewall is blocking access from the AKS cluster
- The CSI driver addon is not enabled on the cluster

## Security Context

The DocumentDB gateway runs with a hardened security context:

- **Non-root execution**: All containers run as non-root users
- **No privilege escalation**: `allowPrivilegeEscalation: false`
- **Read-only root filesystem**: Where applicable

TLS certificates are mounted as read-only volumes into the gateway container. The operator manages certificate lifecycle without requiring elevated privileges.

## Additional Resources

- [Complete TLS Setup Guide](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/README.md) — Automated scripts for TLS setup
- [E2E Testing Guide](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/E2E-TESTING.md) — Automated TLS testing
- [Manual Provided Mode Setup](https://github.com/documentdb/documentdb-kubernetes-operator/blob/main/documentdb-playground/tls/MANUAL-PROVIDED-MODE-SETUP.md) — Step-by-step Azure Key Vault integration
- [API Reference — TLS Types](../api-reference.md#tlsconfiguration) — Auto-generated reference for TLSConfiguration, GatewayTLS, and related types
- [cert-manager Documentation](https://cert-manager.io/docs/)
