# Making GitHub Container Registry (GHCR) Packages Public

This guide explains how to make the container images and Helm charts published by this repository publicly accessible.

## Overview

This repository publishes the following packages to GitHub Container Registry (GHCR):

1. **Container Images:**
   - `ghcr.io/wentingwu666666/documentdb-kubernetes-operator/operator` - Main operator image
   - `ghcr.io/wentingwu666666/documentdb-kubernetes-operator/sidecar` - Sidecar injector image
   - `ghcr.io/wentingwu666666/documentdb-kubernetes-operator/documentdb` - DocumentDB image
   - `ghcr.io/wentingwu666666/documentdb-kubernetes-operator/gateway` - Gateway image

2. **Helm Chart:**
   - `ghcr.io/wentingwu666666/documentdb-operator` - Helm chart for the operator

By default, all packages published to GHCR are **private** and require authentication to pull. Making them public allows anyone to pull the images without authentication.

## Why Make Packages Public?

Making your packages public is useful when:

- You want to share your container images with the community
- You want users to easily install your Helm chart without authentication
- You want to enable CI/CD pipelines to pull images without managing credentials
- You're building open-source software that should be freely accessible

## How to Make Packages Public

### Method 1: Via GitHub Web UI (Recommended)

This is the most straightforward method and works for all package types.

#### For Container Images:

1. Navigate to your GitHub profile or organization page: https://github.com/WentingWu666666
2. Click on the **"Packages"** tab at the top
3. Find and click on the package you want to make public (e.g., `operator`, `sidecar`, `documentdb`, or `gateway`)
4. Click on **"Package settings"** on the right side
5. Scroll down to the **"Danger Zone"** section
6. Under **"Change package visibility"**, click **"Change visibility"**
7. Select **"Public"**
8. Confirm by typing the package name and clicking **"I understand the consequences, change package visibility"**

#### For Helm Charts:

Follow the same steps as above, but look for the `documentdb-operator` Helm chart package.

### Method 2: Set Default Visibility for Future Packages

To automatically make all new packages public:

1. Go to your GitHub profile or organization settings
2. Navigate to **"Packages"** in the left sidebar
3. Under **"Package creation"**, select **"Public"** as the default visibility
4. Click **"Save"**

**Note:** This only affects newly created packages. Existing packages will need to be changed individually using Method 1.

### Method 3: For Organization Administrators

If this repository is under an organization:

1. Go to the organization settings: https://github.com/organizations/WentingWu666666/settings/packages
2. Under **"Package creation"**, set the default visibility to **"Public"**
3. Existing packages will still need to be changed individually

## Verifying Package Visibility

After making a package public, you can verify it by:

1. **Testing an unauthenticated pull:**
   ```bash
   # For container images (no docker login required)
   docker pull ghcr.io/wentingwu666666/documentdb-kubernetes-operator/operator:latest
   
   # For Helm charts (no helm registry login required)
   helm pull oci://ghcr.io/wentingwu666666/documentdb-operator --version <version>
   ```

2. **Checking the package page:**
   - Visit the package URL (e.g., https://github.com/WentingWu666666/documentdb-kubernetes-operator/pkgs/container/documentdb-kubernetes-operator%2Foperator)
   - Public packages will show a **"Public"** badge

## Important Considerations

### Security

- **Public packages can be pulled by anyone**, including anonymous users
- Ensure your images don't contain secrets, credentials, or sensitive information
- Review your Dockerfile and build process to prevent accidental inclusion of secrets

### Access Control

- Making a package public doesn't give others permission to push new versions
- Only users with write permissions to the repository or package can publish updates
- The GitHub Actions workflow uses `packages: write` permission to publish

### Existing Workflows

The existing GitHub Actions workflows in this repository are already configured correctly:

- **Build workflow** (`.github/workflows/build_images.yml`): Builds and pushes images with `packages: write` permission
- **Release workflow** (`.github/workflows/release_images.yml`): Promotes and publishes releases
- **Helm chart workflow** (`.github/workflows/create_helm.yml`): Publishes Helm charts

No changes to these workflows are needed to support public packages.

## Troubleshooting

### "Package not found" error after making it public

- Wait a few minutes for the change to propagate
- Clear your local Docker/Helm cache
- Try pulling again without being logged in

### Cannot change package visibility

- Ensure you have admin permissions on the package or repository
- For organization packages, you may need organization owner permissions
- Some package types may have restrictions based on your GitHub plan

## Additional Resources

- [GitHub Docs: Configuring a package's access control and visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility)
- [GitHub Docs: Working with the Container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
- [About permissions for GitHub Packages](https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages)

## Summary

To make your packages public:

1. Go to https://github.com/WentingWu666666?tab=packages
2. For each package (operator, sidecar, documentdb, gateway, documentdb-operator):
   - Click on the package name
   - Click "Package settings"
   - Change visibility to "Public"
3. Verify by pulling without authentication

Your packages will then be freely accessible to anyone who wants to use them!
