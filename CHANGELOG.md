# Changelog

## [Unreleased]

### Major Features
- **ImageVolume Deployment**: The operator uses ImageVolume (GA in Kubernetes 1.35) to mount the DocumentDB extension as a separate image alongside a standard PostgreSQL base image.

### Breaking Changes
- **Removed combined mode support**: The legacy combined-image deployment mode for Kubernetes < 1.35 has been removed. Kubernetes 1.35+ is now required.

### Enhancements & Fixes
- CI E2E test matrix updated to cover K8s 1.35+ on amd64 and arm64 architectures
- Kind setup script (`kind_with_registry.sh`) defaults to K8s 1.35 node image for local development
- Public documentation updated to require Kubernetes 1.35+

## [0.1.3] - 2025-12-12

### Major Features
- **Change the CRD API version to match documentdb.io**

## [0.1.2] - 2025-12-05

### Major Features
- **Local High-Availability Support**
- **Single Cluster Backup and Restore**
- **MultiCloud Setup Guide**

### Enhancements & Fixes
- Documentation to configure OpenTelemetry, Prometheus and Grafana
- Bug Fix: Show Status and Connection String in Status
- Update scripts and docs for Multi-Region and Multi-Cloud Setup
- Add Cert Manager to Operator
