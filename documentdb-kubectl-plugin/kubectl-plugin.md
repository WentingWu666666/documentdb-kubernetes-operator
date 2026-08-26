# kubectl-documentdb Plugin

The `kubectl documentdb` plugin provides operational tooling for Azure Cosmos DB for MongoDB (DocumentDB) deployments managed by this operator. It targets day-two operations such as status inspection, event triage, backup and restore, and primary promotion workflows.

## Installation

Prebuilt archives are produced by the release workflow under `dist/kubectl-documentdb/` (GitHub Actions download). Each archive contains a platform-specific binary plus this project's MIT license. To install:

1. Download the archive that matches your operating system and CPU architecture.
2. Extract the archive and place the `kubectl-documentdb` binary somewhere on your `PATH` (for example `~/.local/bin`).
3. Ensure the binary is executable (`chmod +x ~/.local/bin/kubectl-documentdb` on Linux and macOS).

To build from source:

```bash
make build-kubectl-plugin           # builds bin/kubectl-documentdb for the host platform
make package-kubectl-plugin         # creates release archives for all supported platforms
```

Copy `bin/kubectl-documentdb` onto your `PATH` (renaming is not required). Verify installation with `kubectl documentdb --help`.

## Supported Commands

| Command | Purpose |
| --- | --- |
| `kubectl documentdb status` | Collects cluster-wide health information for a DocumentDB CR across all member clusters. |
| `kubectl documentdb events` | Streams Kubernetes events scoped to a DocumentDB CR, optionally following new events. |
| `kubectl documentdb promote` | Switches the primary cluster in a fleet by patching `spec.clusterReplication.primary` and waiting for convergence. |
| `kubectl documentdb backup create` | Starts an on-demand backup by creating a `Backup` resource. |
| `kubectl documentdb backup list` | Lists running, completed, failed, and skipped backups. |
| `kubectl documentdb backup schedule create` | Creates a recurring backup schedule by creating a `ScheduledBackup` resource. |
| `kubectl documentdb backup schedule list` | Lists backup schedules with their last and next run times. |
| `kubectl documentdb restore` | Creates a new DocumentDB cluster that bootstraps from an existing backup. |

Run `kubectl documentdb <command> --help` to review all flags. Key options include:

- `--documentdb`: name of the `DocumentDB` custom resource. Required by `status`, `events`, `promote`, `backup create`, and `backup schedule create`; optional as a filter on `backup list` and `backup schedule list`.
- `--namespace/-n`: namespace containing the resource. Defaults to `documentdb-preview-ns` for all commands.
- `--context`: kubeconfig context to use for hub-level operations (defaults to the current context).
- `--show-connections`: include connection strings in `status` output.
- `--follow/-f`: follow mode for `events` (enabled by default).
- `--since`: limit historical events to a relative duration (for example `--since=1h`).
- `--target-cluster`: target cluster name for `promote` (required).
- `--hub-context` and `--cluster-context`: override hub and target kubeconfig contexts when promoting.
- `--retention-days`: per-backup retention override for `backup create` and `backup schedule create`. Defaults to the cluster's `spec.backup.retentionDays`.
- `--status`: phase filter for `backup list` (`all`, `running`, `completed`, `failed`, `skipped`).
- `--wait`, `--wait-timeout`, and `--poll-interval`: block until a backup or restore reaches a terminal state.

## Backup and Restore

`backup` and `restore` operate on the `Backup`, `ScheduledBackup`, and `DocumentDB` custom resources in a single cluster, so they use `--context` (not `--hub-context`).

### Taking a backup

```bash
# Start a backup and return immediately
kubectl documentdb backup create --documentdb sample

# Start a backup, keep it for 7 days, and block until it finishes
kubectl documentdb backup create --documentdb sample --retention-days 7 --wait
```

The backup name defaults to `<documentdb>-<UTC timestamp>`; override it with `--name`. The command verifies the DocumentDB exists before creating the `Backup`, so a typo fails immediately instead of leaving a resource the operator can only reject later.

Backups are taken from the primary cluster only. In a multi-region deployment a `Backup` created against a standby is marked `skipped` by the operator, and `--wait` reports that as an error.

### Listing backups

```bash
# Every backup in the namespace
kubectl documentdb backup list

# Only the backups of one cluster that are still running
kubectl documentdb backup list --documentdb sample --status running

# Only the backups produced by a given schedule
kubectl documentdb backup list --scheduled-backup nightly
```

The table reports phase, the owning schedule (if any), start/stop/expiry times, and the DocumentDB schema version captured at backup time. Newest backups are listed first.

### Scheduling backups

```bash
# Back up every day at 02:00, keeping each backup for 14 days
kubectl documentdb backup schedule create --documentdb sample --schedule "0 2 * * *" --retention-days 14

kubectl documentdb backup schedule list --documentdb sample
```

The schedule name defaults to `<documentdb>-schedule`. Cron expressions are validated locally with the same parser the operator uses, so an invalid expression is rejected before the resource is created.

### Restoring

```bash
# Preview the manifest that would be created
kubectl documentdb restore --from-backup sample-20260101-020000 --name sample-restored --dry-run

# Create the restored cluster and wait for it to become healthy
kubectl documentdb restore --from-backup sample-20260101-020000 --name sample-restored --wait
```

`restore` builds a **new** DocumentDB resource; it never overwrites an existing cluster. The new spec is cloned from the DocumentDB the backup was taken from, so storage, resources, and version settings carry over, with two deliberate changes:

- `spec.bootstrap.recovery.backup.name` is set to the backup being restored.
- `spec.clusterReplication` is dropped, because the restored cluster starts standalone.

Use `--source-documentdb` when the original cluster no longer exists and you want to use another cluster's spec as the template. By default only `completed` backups can be restored; `--allow-incomplete-backup` overrides that guard.

A restore must target a binary at or above the schema version recorded on the backup. The command prints the backup's schema version so you can check this before the operator rejects the restore.

## Kubeconfig Expectations

`status` gathers information from every cluster listed in `spec.clusterReplication.clusterList`. For each entry the plugin attempts to load a kubeconfig context with the same name. Create or rename contexts accordingly so that `kubectl documentdb status` can authenticate to each member cluster.

The plugin never modifies kubeconfig files; it only reads them through `client-go`.

## Output Highlights

- **Status** prints a table containing cluster role, phase, pod readiness, service endpoints, and any retrieval errors per member cluster. Pass `--show-connections` to include the hub-reported primary connection string.
- **Events** prints the latest matching events immediately and switches to watch mode while `--follow` remains true.
- **Promote** patches the DocumentDB resource in the fleet hub, then (unless `--skip-wait` is used) polls both the hub and the target cluster until the reconciliation reports the desired primary cluster.
- **Backup** prints the created resource name and, with `--wait`, exits non-zero when the backup ends in `failed` or `skipped` so it can be used in scripts.
- **Restore** prints the rendered manifest with `--dry-run`, otherwise creates the DocumentDB and reports the backup's schema version.

## Troubleshooting

- Ensure the operator has already synchronized status for the target resource; otherwise `status` may report unknown phases.
- If you see context lookup errors, verify the context name exists via `kubectl config get-contexts` and matches the cluster list entry.
- Promotion waits until `status.status` reports a healthy phase on both hub and target contexts. Use `--poll-interval` and `--wait-timeout` to tune.
- A backup stuck before `running` usually means no default `VolumeSnapshotClass` exists. Check `kubectl documentdb events --documentdb <name>` for the operator's warning.
- `restore` reports `source DocumentDB ... not found` when the original cluster has been deleted. Pass `--source-documentdb` to point at another cluster whose spec should be used as the template.

## Contributing

The plugin is a standalone Go module located in `documentdb-kubectl-plugin`. Use the Makefile targets above to rebuild after code changes. Unit tests for the plugin should live alongside the command implementations under `documentdb-kubectl-plugin/cmd`.
