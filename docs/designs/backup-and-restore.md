# Backup and Restore Design

## Backup

### VolumeSnapshotClass

A [VolumeSnapshotClass](https://kubernetes.io/docs/concepts/storage/volume-snapshot-classes/) must exist before taking volume snapshots. It specifies which CSI driver to use for creating volume snapshots.

**CNPG's Approach:** CNPG requires users to manually create the VolumeSnapshotClass.

**Our Approach:** The DocumentDB operator automatically creates a VolumeSnapshotClass when a Backup resource is created, if one doesn't already exist.

#### Current Support

Currently, we only support **AKS (Azure Kubernetes Service)** with the **`disk.csi.azure.com`** CSI driver.

The operator will automatically create a VolumeSnapshotClass named `azure-disk-snapclass` configured with the Azure disk CSI driver when you create your first Backup resource.

### Backup CRD

We have our own Backup CRD and backup controller in the DocumentDB operator. When a Backup resource is created, it triggers a [Kubernetes Volume Snapshot](https://kubernetes.io/blog/2020/12/10/kubernetes-1.20-volume-snapshot-moves-to-ga/#what-is-a-volume-snapshot) on the primary instance of a DocumentDB cluster.

Since DocumentDB uses a [CloudNativePG (CNPG)](https://cloudnative-pg.io/) cluster as the backend, we leverage CNPG's backup functionality. When users create a DocumentDB Backup resource, the operator automatically creates a corresponding [CNPG Backup](https://cloudnative-pg.io/documentation/current/backup/) resource.

**Why not use CNPG Backup directly?**

In this phase, our Backup resource acts as a wrapper around CNPG Backup. We maintain our own CRD to support future enhancements:
- **Next phase:** Multi-region backup support
- **Future:** Multi-node backup capabilities

### Creating On-Demand Backups

Create an on-demand backup by applying the following resource:

```yaml
apiVersion: db.microsoft.com/preview
kind: Backup
metadata:
  name: backup-example
  namespace: documentdb-preview-ns
spec:
  cluster:
    name: documentdb-preview
```

## Scheduled Backup

### ScheduledBackup CRD

The ScheduledBackup CRD enables automated, recurring backups using [cron expressions](https://en.wikipedia.org/wiki/Cron).

**Why not use CNPG ScheduledBackup?**

CNPG's [ScheduledBackup](https://cloudnative-pg.io/documentation/current/backup/#scheduled-backups) creates CNPG Backup resources directly. Since we have our own Backup CRD with custom logic, we need our own ScheduledBackup implementation.

### Creating Scheduled Backups

Create a scheduled backup using a cron expression:

```yaml
apiVersion: db.microsoft.com/preview
kind: ScheduledBackup
metadata:
  name: backup-example
  namespace: documentdb-preview-ns
spec:
  schedule: "0 0 0 * * *"  # Daily at midnight
  cluster:
    name: documentdb-preview
```

## Retention Policy

Retention policies control how long backups are preserved before automatic deletion. The DocumentDB operator supports retention policies at multiple levels:

### Cluster-Level Retention

**Field:** `cluster.spec.backup.retentionPeriod`

**Purpose:** Defines how long backups are retained after the parent cluster is deleted.

**Example:**
```yaml
apiVersion: db.microsoft.com/preview
kind: DocumentDB
metadata:
  name: documentdb-preview
spec:
  backup:
    retentionPeriod: "30d"  # Retain backups for 30 days after cluster deletion
  # ...other fields...
```

### Backup-Level Retention

**Field:** `backup.spec.retentionPeriod`

**Purpose:** Defines how long an individual on-demand backup is retained before automatic deletion.

**Example:**
```yaml
apiVersion: db.microsoft.com/preview
kind: Backup
metadata:
  name: backup-example
spec:
  cluster:
    name: documentdb-preview
  retentionPeriod: "7d"  # Automatically delete after 7 days
```

### ScheduledBackup Retention

**Field:** `scheduledBackup.spec.retentionPeriod`

**Purpose:** Defines how long backups created by the scheduled job are retained.

**Example:**
```yaml
apiVersion: db.microsoft.com/preview
kind: ScheduledBackup
metadata:
  name: backup-example
spec:
  schedule: "0 0 0 * * *"
  cluster:
    name: documentdb-preview
  retentionPeriod: "14d"  # Keep scheduled backups for 14 days
```

Note: CNPG does not yet support retention policies for volume snapshots. This is an ongoing discussion in the CNPG community (see [issue #6009](https://github.com/cloudnative-pg/cloudnative-pg/issues/6009)).


## Deletion Behavior

- **Deleting a Backup resource:** Immediately deletes the associated volume snapshot
- **Deleting a ScheduledBackup resource:** Stops creating new backups but does not delete existing backups created by that schedule
- **Deleting a Cluster:** Backups are retained according to the cluster's `retentionPeriod` setting

## Restore

### Recovery from Backup

The operator supports bootstrapping a new cluster from an existing backup. In-place restoration is not currently supported.

**Recovery Example:**

```yaml
apiVersion: db.microsoft.com/preview
kind: DocumentDB
metadata:
  name: documentdb-preview-restore
  namespace: documentdb-preview-ns
spec:
  bootstrap:
    recovery:
      backup:
        name: backup-example
  ......
```
