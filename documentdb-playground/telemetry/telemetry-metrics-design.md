# Design: Shared Metrics Infrastructure for pgmongo & DocumentDB

**Status:** Draft
**Date:** 2026-03-03
**Repos affected:** `pgmongo`, `orcasql-breadth`, `documentdb`, `documentdb-kubernetes-operator`

---

## Problem Statement

We need to expose database engine metrics (connection stats, query performance, replication lag, feature usage, index health, etc.) to users in both:

1. **pgmongo (internal managed service)** — via a metrics collector running in the VM agent sidecar (`VmAgent.SideCar.Postgres` in `orcasql-breadth`), emitting to MDM (hot path) and FluentBit (warm path).
2. **documentdb (OSS K8s operator)** — via an OpenTelemetry Collector sidecar emitting metrics via OTLP push and/or Prometheus pull to user-defined destinations.
3. **documentdb (OSS self-hosted)** — via a user-managed OTel Collector consuming shared query configs.

Since documentdb is the open-source version of pgmongo, the SQL query definitions that pull metrics from the engine should be **shared** so metrics stay in sync across all deployment modes.

### What Happens If We Don't Do This

- Metrics drift between internal and OSS — customers see different metrics than what SREs monitor.
- Duplicate maintenance burden — two separate query sets that must be manually kept in sync.
- OSS community can't contribute metrics improvements that flow back to the managed service.

---

## Goals / Non-Goals

### Goals
- **G1**: Single shared config format for SQL metric queries, consumed by both pgmongo (C# sidecar) and documentdb (OTel Collector).
- **G2**: OTel-native metrics protocol for the K8s operator path — both push (OTLP) and pull (Prometheus) modes.
- **G3**: Multi-source metrics collection — engine, gateway, host, and optionally CNPG metrics — through a single OTel Collector sidecar per pod.
- **G4**: Config lives at the `pgmongo/oss/` boundary for automatic sync between internal and OSS.
- **G5**: Support environment-specific overrides (pgmongo may need extra internal-only metrics and emission routing).

### Non-Goals
- **NG1**: Defining the full set of v1 metrics to expose (follow-up work — this designs the **infrastructure**).
- **NG2**: Implementing gateway metrics instrumentation (gateway has zero metrics today — separate workstream in `documentdb` repo).
- **NG3**: Replacing existing OS-level metrics collection in pgmongo (`vmmetrics_collector` stays as-is).
- **NG4**: Installing Prometheus or any monitoring backend for users — that's their responsibility.

---

## Architecture Overview

```
pgmongo (internal managed service)              documentdb (OSS K8s operator)
┌──────────────────────────────┐                ┌──────────────────────────────────────────┐
│  VM                          │                │  K8s Pod (per DocumentDB instance)        │
│ ┌────────────────┐           │                │ ┌──────────────┐  ┌────────────────────┐ │
│ │ PostgreSQL     │           │                │ │ PostgreSQL   │  │ OTel Collector     │ │
│ │ (pgmongo)      │◄─SQL──┐  │                │ │ (documentdb) │◄─┤ (sidecar)          │ │
│ └────────────────┘       │  │                │ └──────────────┘  │  ├ sqlquery recv    │ │
│ ┌────────────────┐       │  │                │ ┌──────────────┐  │  ├ hostmetrics recv │ │
│ │ VmAgent.SideCar│       │  │                │ │ Gateway      │◄─┤  ├ prometheus recv  │ │
│ │ .Postgres (C#) │───────┘  │                │ │ (Rust)       │  │  └─► OTLP push     │ │
│ │  reads same    │          │                │ └──────────────┘  │  └─► Prom pull     │ │
│ │  YAML queries  │──► MDM   │                │                   └────────────────────┘ │
│ │                │──► Fluent │                └──────────────────────────────────────────┘
│ └────────────────┘          │
│ ┌────────────────┐          │                documentdb-local (Docker)
│ │ vmmetrics      │          │                ┌──────────────────────────────────────────┐
│ │ _collector     │──► MDM   │                │  docker compose up                        │
│ │ (OS only)      │          │                │  ┌─────────────┐  ┌──────────────────┐   │
│ └────────────────┘          │                │  │documentdb-  │  │ OTel Collector    │   │
└──────────────────────────────┘                │  │local :10260 │◄─┤ (separate        │   │
                                                │  │       :9712 │  │  container)       │   │
                                                │  └─────────────┘  │  --config merge:  │   │
                                                │                   │  engine_metrics +  │   │
                                                │                   │  local config      │   │
                                                │                   └──────────────────┘   │
                                                └──────────────────────────────────────────┘

Shared across all paths:
  engine_metrics.yaml  (OTel sqlquery receiver format — queries only)
```

---

## Decision Log

This section documents every design decision made, the alternatives considered, and why the chosen option was selected. Decisions are numbered chronologically.

---

### Decision 1: Shared Config Format

**Question**: What format should the shared metric query definitions use?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: OTel sqlquery receiver YAML** ✅ | Adopt OTel Collector's `sqlqueryreceiver` YAML format as-is | OTel-native — zero transform for OTel Collector; community-known format; well-documented | C# sidecar must parse OTel's YAML schema; format tied to OTel contrib lifecycle |
| B: Custom shared schema + adapters | Define our own YAML schema, build adapters to OTel format | Full control; simpler per-consumer parser; can embed pgmongo-specific fields natively | Must maintain OTel adapter; custom format deters OSS contributors; another format in ecosystem |
| C: SQL views as contract | Define metrics as PostgreSQL views — both consumers just `SELECT *` | Single source of truth in engine; no external config files | Metric metadata (description, unit) doesn't fit SQL; adding a metric requires extension upgrade; slow iteration |
| D: Do nothing | Separate metric definitions per environment | No coordination needed | Metrics drift; double maintenance; contradicts OSS alignment goal |

**Decision**: **Option A — OTel sqlquery receiver YAML format**.

**Why**:
- DocumentDB's OSS story is OTel-native. Using OTel's own format means zero transformation for the most common K8s deployment.
- The format is well-documented and understood by the observability community. OSS contributors can add metrics without learning a custom schema.
- The C# parser cost is manageable — OTel sqlquery format is simple YAML (`queries → metrics` mappings). `YamlDotNet` deserialization to strongly-typed POCOs is straightforward.
- This is a **two-way door** — config format can be changed later since both consumers are new.

**Reconsider if**: OTel sqlquery receiver format changes drastically (unlikely — it's stable in contrib); or if histogram support is needed from SQL (sqlquery has limited histogram support).

---

### Decision 2: Config Separation — Pure OTel vs pgmongo Routing

**Question**: Should the shared config include pgmongo-specific fields (emission routing, collection intervals)?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| A: Single file with `x_` extension fields | Add custom `x_emit_to`, `x_collection_interval` fields to the shared YAML | One file to maintain; all config in one place | OTel sqlquery receiver uses strict YAML parsing — **unknown fields cause errors**; breaks direct consumability; confuses OSS contributors |
| **B: Separate configs** ✅ | Pure OTel `engine_metrics.yaml` + pgmongo-only `sidecar_routing.yaml` | Shared config is directly consumable by OTel Collector; clean OSS boundary | Two files to maintain for pgmongo path; routing rules must match metric names |

**Decision**: **Option B — Separate configs**.

**Why**: The OTel sqlquery receiver rejects unknown YAML fields. Adding `x_` fields would break the "directly consumable by OTel Collector" property — the core value proposition of Decision 1. Emission routing is a pgmongo-internal concern that should never cross the OSS boundary.

**Files**:
- `engine_metrics.yaml` — Pure OTel sqlquery format. Lives in `documentdb/telemetry/` (synced to `pgmongo/oss/telemetry/`).
- `sidecar_routing.yaml` — pgmongo-internal. Maps metric names to hot/warm path emission. Lives in `pgmongo/telemetry/` only.

---

### Decision 3: pgmongo Collector Host

**Question**: Which orcasql-breadth component should host the new metrics collector?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Extend VmAgent.SideCar.Postgres (C#)** ✅ | Add `SharedMetricsCollectorService` to existing C# sidecar | Already has PG connection + existing metrics collectors; lowest effort; natural location | C# — not Rust/Go; adds to already-large sidecar (45+ service classes) |
| B: Extend vmmetrics_collector (Rust) | Add PG client to existing Rust metrics daemon | Has Collector/Emitter traits + MDM/FluentD emission; same language as OTel custom receiver | Needs tokio-postgres dependency; currently OS-only — scope expansion |
| C: New standalone Rust binary | Purpose-built metrics process | Clean architecture; no blast radius | Another process to deploy/monitor; duplicates infrastructure |

**Decision**: **Option A — Extend VmAgent.SideCar.Postgres**.

**Why**:
- The C# sidecar already has direct PG access (`NpgsqlDataSource`), existing metrics collectors (`PgmongoMetricsCollector.cs`, `PostgresMetricsCollector.cs`), and runs at the right location next to PostgreSQL.
- The new `SharedMetricsCollectorService` runs as an `IHostedService` — no new process to deploy.
- vmmetrics_collector stays OS-only (clean separation: OS metrics vs DB metrics).

**Implication**: The shared YAML config needs parsing in C# (for pgmongo) and is consumed natively by OTel Collector (for documentdb). Both have mature YAML libraries.

---

### Decision 4: vmmetrics_collector Stays OS-Only

**Question**: Should `vmmetrics_collector` be extended to collect database metrics?

**Decision**: **No** — `vmmetrics_collector` stays as-is, collecting only OS-level metrics (CPU, memory, disk, network).

**Why**: It has no PostgreSQL client dependency and adding one would be a scope expansion. The C# sidecar already has PG access and is the natural home for DB metrics (Decision 3). Clean separation of concerns.

---

### Decision 5: Migration Strategy — Strangler Fig

**Question**: How do we transition from existing hardcoded metrics collectors to the new shared-config-driven collector?

**Decision**: **Strangler fig pattern** — the new `SharedMetricsCollectorService` runs alongside existing `PgmongoMetricsCollector.cs` etc. Over time, hardcoded queries migrate into the shared YAML config and old classes are deprecated.

**Why**: Avoids big-bang migration risk. Each metric can be migrated independently. Old and new collectors coexist safely (metrics are idempotent — duplicate emission is harmless and can be deduplicated at the backend).

---

### Decision 6: Config Delivery to C# Sidecar

**Question**: How do YAML configs from `pgmongo` reach the `VmAgent.SideCar.Postgres` process in `orcasql-breadth`?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Shared volume mount** ✅ | pgmongo container copies configs to `/mnt/pgmongometrics/`; sidecar reads from same mount | Follows existing `HostConfig.json` pattern; config version = engine version | Requires startup ordering (sidecar retries until files appear) |
| B: NuGet package | Package YAMLs as NuGet, reference in sidecar .csproj | Explicit versioning | Heavyweight for YAML files; version bump for every config change |
| C: Git submodule | Reference pgmongo repo from orcasql-breadth | Direct source access | Fragile in CI; manual bump for every change |
| D: Build-time CI copy | CI pipeline copies files cross-repo | Automated | Cross-repo CI dependency; versioning unclear |

**Decision**: **Option A — Shared volume mount at `/mnt/pgmongometrics/`**.

**Why**: This is the exact pattern already used for `HostConfig.json` delivery (pgmongo writes to shared mount, sidecar reads). Config changes deploy with pgmongo, not the sidecar. No new dependencies needed.

---

### Decision 7: Config Scope — Queries-Only vs Full OTel Template

**Question**: Should `engine_metrics.yaml` be a full OTel Collector config template or just the queries array?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| A: Full OTel Collector config | Include receivers, processors, exporters, pipelines | Ready to run directly with `otelcol --config` | Forces opinionated exporter choice; C# sidecar must navigate OTel structure to extract queries; bakes in assumptions |
| B: Queries only (no examples) | Just the `queries:` array | Simplest; both consumers parse directly | New users don't know how to use it without examples |
| **C: Queries-only primary + example full configs** ✅ | `engine_metrics.yaml` = queries only; `examples/` directory has full OTel configs + docker-compose | Core file is consumable by both C# sidecar and OTel Collector; examples give quick-start path | Must keep examples in sync with queries (CI check mitigates) |

**Decision**: **Option C — Queries-only primary + example full configs + docker-compose**.

**Why**:
- The core tension: same query definitions serve two consumers with incompatible needs (C# sidecar wants just queries; OTel Collector needs full config with receivers/exporters).
- Making the primary file queries-only means both consumers can parse it directly without navigating OTel config structure.
- The existing operator playground validates this pattern: separates `queries.yaml` from `config-template.yaml`, merging with `yq` in `generate-config.sh`.
- Examples give self-hosted users a quick-start path. CI validates examples contain all queries from the primary file.

---

### Decision 8: OTel Collector Topology — Sidecar per Pod

**Question**: In the K8s operator path, should there be one OTel Collector per DocumentDB pod (sidecar), one DaemonSet per node, or one shared collector per cluster?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Sidecar per pod** ✅ | Each DocumentDB pod gets its own OTel Collector container | Simple — no service discovery; self-contained; standard pattern (Datadog, Istio, Linkerd); no SPOF; `localhost` access to PG and procfs; operator manages lifecycle via CNPG plugin; config isolation per instance | N collectors for N pods; ~50MB memory per sidecar; collector upgrade forces DB pod restart |
| B: DaemonSet per node | One OTel Collector DaemonSet pod per node, collects from all DocumentDB pods on that node | Collector lifecycle decoupled from DB; collector upgrades don't restart PG; resource-efficient when `instancesPerNode > 1` | Requires pod discovery + dynamic config regen; `hostmetrics` gives node-level not pod-level; bad config kills monitoring for all instances on node; needs remote PG connection |
| C: Central collector Deployment | One OTel Collector Deployment (1-2 replicas) collects from all pods | Fewest collectors; lowest total memory | `sqlquery` receiver takes one datasource — needs N receiver instances for N pods; requires service discovery controller; dynamic config regen on scale-up; SPOF; can't do `hostmetrics`; network hop to PG; PG credentials per instance |
| D: Hybrid — sidecar on primary, central for replicas | Primary gets full sidecar; replicas scraped by central collector | Full metrics on primary (most important); fewer sidecars | Two collection architectures; format divergence; operator manages both sidecar AND Deployment; user confusion |

**Decision**: **Option A — Sidecar per pod**.

**Why**:
- **The scalability concern is smaller than it appears.** OTel Collector uses ~50MB memory / ~0.01 CPU cores. PostgreSQL uses 2-16GB. The sidecar is <3% of pod resources. For a 10-replica cluster, that's 500MB total for OTel vs 20-160GB for PG — noise.
- **This is the industry standard.** Datadog Agent, Istio Envoy, Linkerd proxy all use sidecar-per-pod at massive scale. The pattern is proven.
- **Eliminates three problem categories entirely**: (1) no pod discovery needed (localhost), (2) no remote credential management (Unix socket or localhost TCP with trust auth), (3) no dynamic config regeneration (static per-pod config).
- **`hostmetrics` is pod-scoped** — sidecar sees the pod's cgroup-scoped `/proc`. DaemonSet sees node-level `/proc` and would require cgroup filtering to attribute metrics to specific pods.
- **Config isolation** — bad config in one sidecar affects only one instance. DaemonSet bad config takes out all instances on a node.
- **No SPOF** — one sidecar failing affects only one pod.
- **Scale-up is automatic** — new pods get sidecars via CNPG plugin injection. No reconfig needed.

**Accepted tradeoff**: Collector upgrades require DB pod restart (sidecar containers share pod lifecycle for image changes). CNPG handles this safely via rolling restart, but it's disruptive for a pure monitoring change.

**Reconsider if**: (1) Users routinely run 50+ replicas per cluster; (2) Collector upgrades become frequent (monthly+) and DB restart cost is painful; (3) CNPG adds native OTLP push support upstream (eliminates sidecar need entirely); (4) `instancesPerNode` commonly exceeds 3 (DaemonSet resource savings become meaningful).

> **Deep comparison of Sidecar vs DaemonSet**: See [Appendix A](#appendix-a-sidecar-vs-daemonset-deep-comparison) for the full analysis including credential management, gateway integration, config change blast radius, and collector upgrade lifecycle.

---

### Decision 9: CNPG Built-in Monitoring — Explored, Then Moved Away

**Context**: CNPG v1.28.1 has a built-in Prometheus exporter on port 9187 in every pod. It supports custom SQL queries via ConfigMap (`spec.monitoring.customQueriesConfigMap`), hot-reloading, 30s cache TTL, and uses the `pg_monitor` role. It has 10 default monitoring queries (backends, pg_database, pg_replication, etc.).

**Question**: Should we use CNPG's built-in exporter instead of an OTel Collector sidecar?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| A: Use CNPG's built-in exporter | Leverage existing Prometheus exporter on :9187 | Already in the image (free); supports custom queries; hot-reload | Prometheus-only output (not OTel); uses `postgres_exporter` query format (GAUGE/COUNTER/LABEL) — different from OTel sqlquery format — **breaks shared config goal**; no OTLP push; no hostmetrics |
| **B: OTel Collector sidecar — disable CNPG exporter** ✅ | Inject OTel Collector sidecar; set `DisableDefaultQueries: true` on CNPG monitoring | Same OTel sqlquery config format everywhere (solves three-format problem); supports push AND pull; unified pipeline for all metric sources | Must inject sidecar (more operator work); CNPG exporter is "wasted" |

**Decision**: **Option B — OTel Collector sidecar, disable CNPG's built-in exporter**.

**Why**: The core goal is "same config format everywhere." CNPG's exporter uses `postgres_exporter` format (`GAUGE`/`COUNTER`/`LABEL` usage types) which is different from OTel sqlquery format. Using CNPG's exporter would require maintaining two query formats — one for pgmongo C# sidecar (OTel sqlquery) and one for K8s (postgres_exporter) — defeating the shared config goal.

**Three-format problem — SOLVED by this decision:**

| Consumer | Format |
|----------|--------|
| C# sidecar (pgmongo) | OTel sqlquery YAML |
| K8s OTel Collector (documentdb operator) | OTel sqlquery YAML |
| Self-hosted OTel Collector | OTel sqlquery YAML |

---

### Decision 10: Monitoring CRD Shape — Embedded vs Separate

**Question**: Should the DocumentDB operator have a separate CRD for monitoring, or embed it in the main DocumentDB CRD?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Embedded in DocumentDB CRD** ✅ | `spec.monitoring` field on the existing `DocumentDB` resource | Follows CNPG pattern exactly; single resource to manage; simpler operator reconciliation | Larger CRD; monitoring changes require reconciling the whole resource |
| B: Separate MonitoringConfig CRD | Independent CRD referencing a DocumentDB resource | Independent lifecycle; smaller CRDs | Requires cross-resource reconciliation; CNPG doesn't do this; more complexity for users |

**Decision**: **Option A — Embedded in DocumentDB CRD** as `spec.monitoring`.

**Why**: CNPG embeds monitoring in its Cluster spec, not as a separate CRD. Following the same pattern keeps our CRD familiar to CNPG users and simplifies the operator (single reconciliation loop, no cross-resource watches).

---

### Decision 11: PodMonitor Removed from CRD

**Context**: PodMonitor is a **Prometheus Operator CRD**, not a CNPG CRD. CNPG is deprecating its built-in PodMonitor management. PodMonitor only tells Prometheus where to scrape — it doesn't collect metrics itself.

**Decision**: **Remove `PodMonitorSpec` from our CRD**. We do not manage PodMonitor resources.

**Why**:
- PodMonitor is a Prometheus Operator concept. Including it in our CRD couples us to the Prometheus Operator.
- CNPG is deprecating its own PodMonitor fields.
- In pull mode, we auto-add `prometheus.io/*` annotations (Decision 20) which is the standard Kubernetes-native discovery mechanism — works with any Prometheus-compatible scraper without requiring Prometheus Operator.

---

### Decision 12: Prometheus Server is User's Responsibility

**Decision**: Neither the DocumentDB operator nor CNPG installs, configures, or manages a Prometheus server.

**Why**: Users choose their own monitoring stack (Prometheus, Grafana Cloud, Datadog, Azure Monitor, etc.). We expose metrics in standard formats (OTLP push, Prometheus pull). Installing Prometheus is out of scope.

---

### Decision 13: OTel Protocol for DocumentDB Metrics (Major Pivot)

**Context**: After exploring CNPG's built-in Prometheus exporter (Decision 9), the user explicitly chose: "for our documentdb, I want to expose metrics as OTel protocol."

**Decision**: **OTel is the metrics protocol for DocumentDB K8s operator**. All metrics flow through an OTel Collector sidecar using OTel-native receivers, processors, and exporters.

**Why**:
- OTel is the industry standard for observability. Aligning with it means compatibility with the broadest set of backends.
- Solves the three-format problem — same OTel sqlquery YAML everywhere.
- Enables both push (OTLP) and pull (Prometheus) from a single collector.
- Enables multi-source collection (engine + gateway + host + CNPG) through one sidecar.

---

### Decision 14: OTel Sidecar Injection Mechanism — CNPG Plugin

**Question**: How does the OTel Collector sidecar get injected into DocumentDB pods?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: CNPG plugin system** ✅ | Use CNPG's gRPC-based plugin interface (`cnpg-i-sidecar-injector.documentdb.io`) for JSONPatch injection | CNPG-native; same pattern as existing gateway sidecar injection; no additional infrastructure; supports lifecycle hooks | Requires implementing a CNPG plugin; limited to JSONPatch mutations |
| B: Mutating admission webhook | Standalone webhook that intercepts Pod creation | Full control over pod mutation; independent of CNPG | Extra Deployment + Service + TLS cert to manage; races with CNPG's own pod management; harder to coordinate lifecycle |
| C: Direct pod template patching | Modify CNPG Cluster's `spec.containers` or template | No plugin needed | CNPG has **no `additionalContainers` field** — all container injection must go through plugins; would require forking CNPG |

**Decision**: **Option A — CNPG plugin system**.

**Why**: CNPG explicitly requires all container injection to go through its plugin system (there's no `additionalContainers` field on the Cluster spec). The gateway sidecar is already injected this way — we follow the same pattern. No additional infrastructure to manage.

---

### Decision 15: Disable CNPG Exporter When OTel Enabled

**Decision**: When `monitoring.enabled=true`, the operator sets `DisableDefaultQueries: true` on CNPG's `MonitoringConfiguration`. This stops CNPG's built-in Prometheus exporter from running queries.

**Why**: Avoids duplicate PostgreSQL query load. The OTel Collector sidecar is the single source of metrics. Running both would execute every query twice.

**Implementation**: `getMonitoringConfiguration()` in `cnpg_cluster.go` sets `DisableDefaultQueries: &true` when OTel monitoring is enabled.

---

### Decision 16: Push and Pull Export Modes

**Question**: Should the OTel Collector support push only, pull only, or both?

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| A: Push only (OTLP) | Only OTLP exporter to remote endpoint | Simplest; OTLP is the OTel-native protocol | Doesn't serve Prometheus-native K8s users; many K8s stacks are pull-based |
| B: Pull only (Prometheus) | Only Prometheus exporter on a port | Serves most K8s users; simple | Can't push to cloud backends (Grafana Cloud, Datadog); not OTel-native |
| **C: Both push and pull** ✅ | OTLP exporter + Prometheus exporter, independently configurable | Serves all users — cloud push AND K8s pull; maximum flexibility | Slightly more CRD fields; both exporters in config |

**Decision**: **Option C — Both push and pull modes**.

**Why**: K8s monitoring stacks vary widely. Some users have Prometheus + Grafana (pull). Others use Grafana Cloud, Datadog, or Azure Monitor (push). Supporting both is the only way to avoid forcing users to adapt their existing monitoring infrastructure to us.

---

### Decision 17: CRD Shape for Push/Pull — Flat Siblings

**Question**: How should push and pull export configurations be structured in the CRD?

| Option | Description | CRD Shape |
|--------|-------------|-----------|
| **A: Flat push/pull as siblings** ✅ | `push` and `pull` are top-level sibling fields under `monitoring` | `spec.monitoring.push: {...}` / `spec.monitoring.pull: {...}` |
| B: Nested under `exporter` | Both nested under a single `exporter` field | `spec.monitoring.exporter.push: {...}` / `spec.monitoring.exporter.pull: {...}` |
| C: Mode enum | Single `mode` field with enum: push/pull/both | `spec.monitoring.mode: push` + `spec.monitoring.pushConfig: {...}` |

**Decision**: **Option A — Flat push/pull as siblings**.

**Why**:
- Clearest naming — `push` and `pull` are self-explanatory at the top level.
- Shallowest nesting — less YAML indentation for users.
- Independent enablement — push and pull are orthogonal (enable one, both, or neither independently).
- At least one must be configured when `monitoring.enabled=true`.

**Implication**: The existing `Exporter *OTelExporterSpec` field will be renamed to `Push *PushExporterSpec`. A new `Pull *PullExporterSpec` will be added.

---

### Decision 18: OTLP is Push-Only — Pull Uses Prometheus Format

**Question**: In pull mode, can we expose OTLP instead of Prometheus format?

**Decision**: **No.** Pull mode uses Prometheus/OpenMetrics exposition format on a configurable port (default 9464).

**Why**: OTLP is a **push-only protocol by design** — it's a client-initiated POST to a remote endpoint. There is no "scrape OTLP" concept in the specification. Every monitoring tool that does pull-based scraping (Prometheus, Grafana Agent, Datadog Agent, Victoria Metrics, Thanos) speaks Prometheus exposition format. The OTel Collector's `prometheus` exporter actually serves OpenMetrics format (backward-compatible superset of Prometheus format).

---

### Decision 19: Default Pull Port — 9464

**Decision**: Pull mode defaults to port **9464**.

**Why**: 9464 is the IANA-registered port for OpenTelemetry Prometheus exporter. It's the OTel Collector's `prometheus` exporter default.

---

### Decision 20: Auto-Add Prometheus Annotations for Pull Mode

**Decision**: When pull mode is enabled, the operator automatically adds standard Prometheus annotations to the pod:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "9464"
prometheus.io/path: "/metrics"
```

**Why**: These annotations are the standard Kubernetes-native mechanism for Prometheus service discovery. This works with any Prometheus-compatible scraper without requiring the Prometheus Operator or PodMonitor CRDs (see Decision 11).

#### Annotation Injection Point

Our controller doesn't create pods directly — CNPG does. Two options for where to inject:

| Option | How | Pros | Cons |
|--------|-----|------|------|
| A: CNPG Cluster `spec.metadata.annotations` | Operator sets annotations on the Cluster spec in `cnpg_cluster.go`; CNPG propagates to all pods | Simple; single place | Annotations exist even if sidecar injection fails — Prometheus scrapes a port with nothing listening |
| **B: CNPG sidecar plugin JSONPatch** ✅ | The same plugin that injects the OTel Collector container also adds annotations in the same JSONPatch | Annotations **coupled to sidecar** — if sidecar isn't injected, no annotations; atomic operation | Slightly more plugin logic |

**Decision**: **Option B** — inject annotations via the CNPG sidecar plugin. The annotations should only exist when the OTel Collector sidecar is actually running and serving `:9464`. The plugin already does JSONPatch to add the container; adding annotations in the same patch is trivial.

#### Pull Mode Use Cases

The following use cases show how users consume metrics in pull mode. In all cases, the user's existing monitoring stack auto-discovers our pods via annotations — **zero DocumentDB-specific configuration needed**.

**Use Case 1: Prometheus + Grafana (most common K8s stack)**
User already has Prometheus in-cluster (e.g., via `kube-prometheus-stack` Helm chart). Prometheus's `kubernetes_sd_configs` with `role: pod` discovers our annotations automatically.
```
DocumentDB Pod :9464 ◄── Prometheus scrapes ──► Grafana dashboard
```
**Prerequisite**: Prometheus configured with pod-level service discovery (default in most Helm charts).

**Use Case 2: Grafana Alloy / Grafana Agent**
Grafana Alloy (successor to Grafana Agent) also respects `prometheus.io/*` annotations for auto-discovery.
```
DocumentDB Pod :9464 ◄── Grafana Alloy scrapes ──► Grafana Cloud
```

**Use Case 3: Datadog Agent**
Datadog Agent supports OpenMetrics scraping via pod annotations. Users add Datadog's own annotation:
```yaml
ad.datadoghq.com/otel-collector.checks: |
  {"openmetrics": {"instances": [{"prometheus_url": "http://%%host%%:9464/metrics"}]}}
```

**Use Case 4: Victoria Metrics / Thanos**
Both support `prometheus.io/*` annotation-based discovery — same as Use Case 1.

**Use Case 5: Azure Monitor (Container Insights)**
Azure Monitor's Prometheus scraping addon discovers pods via the same `prometheus.io/*` annotations.

**When to use pull vs push**:

| Scenario | Recommended Mode |
|----------|-----------------|
| Prometheus already in cluster | **Pull** — zero config, auto-discovered via annotations |
| Cloud-hosted backend (Grafana Cloud, Datadog SaaS, Azure Monitor) | **Push** — OTLP directly to cloud endpoint |
| Both in-cluster Prometheus AND cloud backend | **Both** — pull for local dashboards, push for cloud |
| Air-gapped / no external egress | **Pull** — everything stays in cluster |
| High-cardinality with remote write concerns | **Push** — OTLP is more efficient than Prometheus remote write |

---

### Decision 21: Multi-Source Collection — Single OTel Collector, Multiple Receivers

**Question**: Should we collect metrics from just the engine, or also from gateway, host OS, and CNPG?

| Source | Current State | Collection Method |
|--------|--------------|-------------------|
| **Engine** (DocumentDB/PG) | No metrics exposed | OTel `sqlquery` receiver (SQL queries) |
| **Gateway** (Rust) | **No metrics at all** — `TelemetryProvider` trait is unimplemented, no Prometheus library | OTel `prometheus` receiver (scrape gateway — FUTURE, blocked) |
| **VM/Node** (CPU/mem/disk/net) | K8s provides some via cAdvisor | OTel `hostmetrics` receiver |
| **CNPG** (operator-level) | Built-in exporter on :9187 (we disabled it) | OTel `prometheus` receiver (scrape :9187 — opt-in) |

**Options for multi-source:**

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Single OTel Collector, multiple receivers** ✅ | One sidecar, multiple receiver configs | Minimal resource overhead; unified output; consistent resource attributes; incremental delivery | All metrics share one failure domain; larger OTel Collector config |
| B: Separate collection per source | Multiple sidecars: OTel for engine, node_exporter for VM, CNPG exporter for PG | Independent failure domains; each source independently manageable | 3+ sidecars per pod — resource waste; multiple scrape targets; format inconsistency (Prometheus + OTLP mixed) |
| C: Engine-only | Only collect engine metrics via sqlquery | Simplest; ships fastest | Incomplete picture; users deploy their own sidecars anyway |

**Decision**: **Option A — Single OTel Collector with multiple receivers**.

**Why**:
- OTel Collector is explicitly designed for this pattern — running multiple receivers in one collector is the standard approach.
- One sidecar = one resource budget. K8s users hate per-pod sidecar sprawl.
- Unified output — all metrics come from one place with consistent resource attributes (`documentdb.instance`, `k8s.namespace`, `k8s.pod`).
- Each source is an independent receiver — can be shipped incrementally:

```
Phase 1: Engine metrics (sqlquery) + Push/Pull modes
Phase 2: Host metrics (hostmetrics receiver)
Phase 3: CNPG metrics (prometheus scrape of :9187) — opt-in
Phase 4: Gateway metrics (blocked until gateway instrumented)
```

---

### Decision 22: CNPG Metrics — Opt-In, Default Off

**Question**: Should CNPG's built-in PG stats (:9187) be collected by default?

**Decision**: `cnpgMetrics.enabled` defaults to **`false`**.

**Why**:
- CNPG metrics (pg_stat_*, replication info) largely overlap with our `engine_metrics.yaml` queries.
- Enabling it re-enables CNPG's exporter process = additional PG query load.
- Advanced users can opt in when they specifically want CNPG-formatted metrics alongside our OTel ones.

**When enabled**: Operator changes `getMonitoringConfiguration()` to set `DisableDefaultQueries: false`, re-enabling :9187. The OTel Collector scrapes it locally (localhost:9187) — port is NOT exposed externally. All metrics flow through the unified push/pull output.

---

### Decision 23: Host Metrics — Default On

**Decision**: `hostMetrics.enabled` defaults to **`true`**.

**Why**: CPU/memory/disk/network is universally useful. Zero PG query overhead (reads `/proc`, `/sys`). Very lightweight.

---

### Decision 24: Gateway Metrics Field — Reserved, Not Implemented

**Decision**: `gatewayMetrics` field exists in the CRD now (reserves the name) but is a **no-op** until the gateway is instrumented.

**Why**:
- The Rust gateway (`pg_documentdb_gw`) has **zero metrics exposure today**. `TelemetryProvider` trait exists but has NO implementations. No Prometheus library in `Cargo.toml`.
- CRD field additions are non-breaking (additive). Reserving the field now avoids an additive-but-surprising field later.
- Gateway instrumentation is a **separate workstream in the `documentdb` repo** — prerequisites:

| Work Item | Repo | Description |
|-----------|------|-------------|
| Add `prometheus` crate | `documentdb` | Cargo dependency |
| Implement `TelemetryProvider` | `documentdb` | Fill existing trait shell |
| Add HTTP `/metrics` endpoint | `documentdb` | New listener |
| Expose metrics port in container | `documentdb` | Dockerfile change |
| Add `prometheus/gateway` receiver | `operator` | Scrape `localhost:{port}` |

---

### Decision 25: OSS Consumption — Helm Chart Bundles Config

**Question**: How do K8s operator users and self-hosted users consume `engine_metrics.yaml`?

**For K8s operator users:**
- The Helm chart bundles `engine_metrics.yaml` at operator release time.
- Helm renders it into a ConfigMap, which the OTel Collector sidecar mounts.
- Version coupling: operator release tracks the engine version it supports — good enough for config alignment.

**For self-hosted users:**
Three consumption paths:
1. **Docker-compose quick-start**: Clone repo, run `docker compose up` with full example configs.
2. **Copy from container image**: `docker cp documentdb:/usr/share/documentdb/telemetry/ ./telemetry/`
3. **Download from GitHub**: `curl -O` the specific version tag.

---

### Decision 26: Sidecar per Pod is Scalable

**Context**: Concern raised about OTel Collector sidecar running on every pod (primary + all replicas).

**Analysis**:

| Topology | Pods | OTel Sidecars | OTel Total Memory |
|----------|------|---------------|-------------------|
| 1 primary + 2 replicas | 3 | 3 | ~150 MB |
| 1 primary + 5 replicas | 6 | 6 | ~300 MB |
| 1 primary + 10 replicas (extreme) | 11 | 11 | ~550 MB |

Compare to PostgreSQL using 2-16 GB per pod. OTel sidecar is **<3% of pod resources**.

**Decision**: **Sidecar per pod is acceptable**. No architecture change needed.

**Why**:
- ~50 MB per sidecar is noise vs multi-GB PostgreSQL.
- Industry standard pattern (Datadog, Istio, Linkerd all do sidecar-per-pod).
- Central collector alternative (Decision 8, Option B) adds massive complexity for minimal resource savings.
- Push fan-in is fine — OTLP backends handle thousands of simultaneous senders.
- Operator manages sidecar lifecycle — users don't see the complexity.

**Phase 2 optimization (if needed)**: Differentiate primary vs replica OTel configs. Replicas skip write-path queries and gateway metrics. Operator uses CNPG labels (`cnpg.io/instanceRole: primary|replica`) to select config variant. This saves SQL query load, not OTel Collector resources.

**Reconsider if**: Users run 50+ replicas per cluster (extreme); OTel Collector memory exceeds 200MB; or CNPG adds native OTLP push (eliminates sidecar need entirely).

---

### Decision 27: documentdb-local Metrics — Docker-Compose with Config Merge

**Context**: `documentdb-local` is a **single Docker container** (`ghcr.io/documentdb/documentdb/documentdb-local:latest`) that runs PostgreSQL + DocumentDB extension + Rust gateway — no Kubernetes, no CNPG, no operator. How do we expose engine metrics for this deployment mode?

**Container internals** (from `emulator_entrypoint.sh`):
- PostgreSQL on port **9712** (configurable via `--pg-port`)
- Gateway on port **10260** (configurable via `--documentdb-port`)
- 4 log-tailing background processes
- Default user: `default_user` / `Admin100`
- Trust auth: same `pg_hba.conf` trust configuration as K8s

**Options evaluated**:

| Option | Description | Image change? | User overhead | Same shared config? |
|--------|-------------|---------------|---------------|---------------------|
| **A: Separate OTel Collector container via docker-compose** ✅ | User runs `docker compose up` — gets documentdb-local + OTel Collector + optional Prometheus/Grafana | None | One command | ✅ Yes — same `engine_metrics.yaml` |
| B: Embed OTel Collector inside documentdb-local | Add OTel Collector as 5th background process in entrypoint | +150MB image bloat | Zero | ✅ Yes |
| C: Wait for gateway `/metrics` endpoint | Gateway exposes Prometheus endpoint natively | Yes (future) | Minimal | ❌ No — different format, no SQL metrics |

**Decision**: **Option A — Separate OTel Collector container via docker-compose**.

**Why**:
- Zero changes to the `documentdb-local` image.
- Users who don't want metrics pay zero cost (just run the container directly, skip compose).
- Uses the **exact same `engine_metrics.yaml`** — Goal G1 satisfied for all four deployment modes.
- Ships immediately — just example files + documentation.
- Clean separation of concerns: DB is DB, collector is collector.

**How OTel Collector merges shared queries with local config:**

The OTel Collector supports **multiple `--config` flags** that are **deep-merged** in order (maps merge recursively, arrays/scalars: last wins). This is the key mechanism:

```
otelcol-contrib --config=/etc/otel/engine_metrics.yaml \
                --config=/etc/otelcol-contrib/config.yaml
```

**File 1: `engine_metrics.yaml`** (shared, from `documentdb/telemetry/`):
```yaml
# Shared across ALL deployment modes — queries only
receivers:
  sqlquery:
    queries:
      - sql: "SELECT count(*) as active FROM pg_stat_activity WHERE state = 'active'"
        metrics:
          - metric_name: documentdb.connections.active
            value_column: active
            value_type: gauge
      - sql: "SELECT ..."
        metrics:
          - metric_name: documentdb.replication.lag_bytes
            value_column: lag
            value_type: gauge
      # ... all shared metric queries
```

**File 2: `otel-collector-local.yaml`** (deployment-specific, for documentdb-local):
```yaml
# Local-specific: datasource, exporters, pipelines
receivers:
  sqlquery:
    driver: postgres
    datasource: "host=documentdb port=9712 user=postgres dbname=postgres sslmode=disable"
    collection_interval: 30s

exporters:
  prometheus:
    endpoint: "0.0.0.0:9464"

service:
  pipelines:
    metrics:
      receivers: [sqlquery]
      exporters: [prometheus]
```

**After deep merge** (engine_metrics.yaml + otel-collector-local.yaml):
```yaml
receivers:
  sqlquery:
    driver: postgres                          # from local config
    datasource: "host=documentdb port=..."    # from local config
    collection_interval: 30s                  # from local config
    queries:                                  # from engine_metrics.yaml (preserved — local didn't define queries)
      - sql: "SELECT count(*) ..."
        metrics: [...]
      - sql: "SELECT ..."
        metrics: [...]

exporters:
  prometheus:
    endpoint: "0.0.0.0:9464"                 # from local config

service:
  pipelines:
    metrics:
      receivers: [sqlquery]
      exporters: [prometheus]                 # from local config
```

**The merge works because**: `engine_metrics.yaml` defines `receivers.sqlquery.queries` (array) and the local config defines `receivers.sqlquery.driver/datasource/collection_interval` (scalars). Since they're **different keys** under the same `receivers.sqlquery` map, deep merge combines them cleanly.

> **Critical rule**: `engine_metrics.yaml` must NOT define `driver`, `datasource`, or `collection_interval`. The local/deployment config must NOT define `queries`. This separation is what makes the merge work.

**Docker-compose example** (`documentdb/telemetry/examples/docker-compose.yaml`):

```yaml
services:
  documentdb:
    image: ghcr.io/documentdb/documentdb/documentdb-local:latest
    ports:
      - "10260:10260"   # MongoDB-compatible API (gateway)
      - "9712:9712"     # PostgreSQL direct access
    environment:
      - DOCUMENTDB_ADMIN_USER=admin
      - DOCUMENTDB_ADMIN_PASSWORD=password

  otel-collector:
    image: otel/opentelemetry-collector-contrib:latest
    command: ["--config=/etc/otel/engine_metrics.yaml", "--config=/etc/otelcol-contrib/config.yaml"]
    volumes:
      - ../engine_metrics.yaml:/etc/otel/engine_metrics.yaml:ro
      - ./otel-collector-local.yaml:/etc/otelcol-contrib/config.yaml:ro
    ports:
      - "9464:9464"     # Prometheus scrape endpoint
    depends_on:
      documentdb:
        condition: service_started

  # --- Optional: full observability stack ---
  prometheus:
    image: prom/prometheus:latest
    volumes:
      - ./prometheus.yaml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3000:3000"
    environment:
      - GF_AUTH_ANONYMOUS_ENABLED=true
      - GF_AUTH_ANONYMOUS_ORG_ROLE=Admin
    volumes:
      - ./grafana-datasource.yaml:/etc/grafana/provisioning/datasources/ds.yaml:ro
```

**User experience**:
```bash
# Clone and run — full observability in one command
git clone https://github.com/documentdb/documentdb.git
cd documentdb/telemetry/examples
docker compose up -d

# Access:
#   MongoDB API:  localhost:10260
#   PostgreSQL:   localhost:9712
#   Prometheus:   localhost:9090
#   Grafana:      localhost:3000
#   OTel metrics: localhost:9464/metrics
```

**This same `--config` merge pattern applies to ALL deployment modes:**

| Deployment | File 1 (shared queries) | File 2 (deployment-specific) | Merge mechanism |
|---|---|---|---|
| **K8s operator** | ConfigMap from Helm chart | Operator-generated OTel config (ConfigMap) | `--config` merge in sidecar args |
| **documentdb-local** | Volume mount from repo | `otel-collector-local.yaml` | `--config` merge in docker-compose command |
| **Self-hosted OTel** | Downloaded from GitHub | User's own OTel config | `--config` merge by user |
| **pgmongo** | N/A (C# sidecar parses queries directly) | `sidecar_routing.yaml` | C# code reads queries array |

---

### Decision 28: Monitoring Enable/Disable — ConfigMap Hot-Reload

**Context**: Users want to toggle `monitoring.enabled` in the DocumentDB CRD without restarting PostgreSQL or gateway containers.

**Kubernetes constraint**: You **cannot** stop/start a single container in a pod without pod recreation. But you **can** change what the container does through configuration.

**Options evaluated**:

| Option | PG restart on toggle? | Latency | Idle cost | Toggle freely? |
|--------|----------------------|---------|-----------|----------------|
| **A: ConfigMap noop toggle** ✅ | No (hot-reload) | ~30-60s | ~15MB when disabled | ✅ Yes |
| B: Add/remove sidecar from CNPG spec | Yes (rolling restart) | Minutes | Zero when disabled | ❌ Disruptive |
| C: K8s native sidecar stop/start | Yes (no such API exists) | N/A | N/A | ❌ Not possible |

**Decision**: **Option A — ConfigMap hot-reload toggle (inject on first enable, config-control thereafter)**.

**How it works**:

1. **First `monitoring.enabled: true`**: Operator adds OTel sidecar to CNPG Cluster spec + creates ConfigMap with active config. **One-time rolling restart** (CNPG-managed, safe).
2. **`monitoring.enabled: false`**: Operator updates ConfigMap to **noop config** (no receivers, empty pipelines). OTel Collector hot-reloads within 30-60s. **Zero restart.**
3. **Re-enable `monitoring.enabled: true`**: Operator updates ConfigMap to active config. **Zero restart.**

**State machine**:
```
                     [first enable]                [disable]
  No sidecar ────────────────────► Active config ─────────────► Noop config
  (never enabled)   rolling restart                config-only   (sidecar idles)
                                       ▲                              │
                                       └──────────────────────────────┘
                                              [re-enable] config-only
```

**Noop config** (applied when `monitoring.enabled: false`):
```yaml
# OTel Collector runs but does nothing — ~15MB idle, <0.1% CPU
receivers: {}
exporters:
  noop: {}
service:
  pipelines: {}
```
> OTel Collector v0.96+ supports empty `pipelines: {}`. For older versions, use a single `debug` exporter with `verbosity: none`.

**Full lifecycle matrix**:

| User Action | Operator Behavior | Pod Impact |
|---|---|---|
| First `monitoring.enabled: true` | Add sidecar to CNPG spec + create active ConfigMap | ⚠️ Rolling restart (one-time) |
| `monitoring.enabled: false` | Update ConfigMap to noop | ✅ Zero restart |
| Re-enable `monitoring.enabled: true` | Update ConfigMap to active | ✅ Zero restart |
| Change collection interval / queries | Update ConfigMap | ✅ Zero restart (hot-reload) |
| Change push/pull endpoint | Update ConfigMap | ✅ Zero restart (hot-reload) |
| Change OTel Collector image | Update CNPG Cluster spec | ⚠️ Rolling restart |

**Why**: The only operations requiring PG restart are first-enable and collector image upgrades. All day-to-day configuration changes (enable/disable, change intervals, change endpoints, add queries) are ConfigMap hot-reloads — zero downtime.

**Reconsider if**: K8s adds in-place container add/remove API (no KEP exists today), which would make Option B viable without restarts.

---

### Decision 29: Sidecar Routing Config Format (`sidecar_routing.yaml`)

**Question**: What is the schema of `sidecar_routing.yaml` — the pgmongo-internal config that maps shared metric queries to emission paths, collection intervals, and pgmongo-specific runtime behavior?

**Context**: The shared `engine_metrics.yaml` (Decision 1) is pure OTel sqlquery format — it contains only queries and metric definitions. Everything pgmongo-specific (which emission path, which collection frequency, which node roles run a query, query timeouts, warm-path table names) belongs in this separate routing file.

**Reference**: This design is informed by the pgmongo metrics design ([PR #1800447](https://msdata.visualstudio.com/CosmosDB/_git/pgmongo/pullrequest/1800447)), which uses PostgreSQL GUCs in key-value format:
```ini
pgmongo_metrics.<name> = 'query=<SQL>;table=<warm_path_table>;frequency=<s>;timeout=<s>;citus_role=<role>;replication_role=<role>;disabled=<bool>'
```

The PR's GUC approach embeds everything (query + routing) in a single string. Our design separates concerns: queries in OTel YAML (shared), routing in `sidecar_routing.yaml` (internal). The routing config captures the same concepts as the GUC fields minus the query itself.

#### Schema

```yaml
# pgmongo/telemetry/sidecar_routing.yaml
# Maps shared metric queries to pgmongo emission paths and runtime behavior.
# This file is INTERNAL ONLY — never synced to documentdb OSS.
#
# Matching: Each routing rule uses an exact metric_name from engine_metrics.yaml.
# The sidecar matches by the first metric_name in each query's metrics[] array.
# Exact-name matching — no globs, no ordering ambiguity.
#
# Unmatched metrics: Any metric in engine_metrics.yaml that does NOT match any routing
# rule is STILL EMITTED using the defaults below. The routing file controls HOW to emit
# (path, interval, role), not WHETHER to emit. If a query is in engine_metrics.yaml,
# it will be collected. To suppress a specific metric, add a routing rule with
# disabled: true.

# No version field — will add if schema evolves incompatibly

# Defaults apply to any metric without a matching routing rule
defaults:
  collection_interval: 60s       # How often to run the SQL query
  query_timeout: 5s              # Per-query SQL timeout (cancel if exceeded)
  emit_to: [hot_path]            # Default: emit to MDM only (no warm_path_table = no warm path)
  citus_role: coordinator        # Which Citus nodes run: coordinator | worker | all
  replication_role: primary      # Which replication role runs: primary | standby | all
  disabled: false                # Default: all metrics enabled

# Per-query routing rules
# Keyed by exact metric_name (first metric in each query's metrics[] array)
routing:
  # PG built-in — no min_schema_version (always safe)
  - metric_name: "documentdb.db.connections"       # pg_stat_database metrics
    collection_interval: 60s
    query_timeout: 5s
    emit_to: [hot_path, warm_path]                 # MDM + MDSD
    warm_path_table: PgMongoDatabaseStats           # MDSD table name (only when warm_path in emit_to)
    citus_role: coordinator
    replication_role: primary

  # Extension metric — requires schema 0.105-0
  - metric_name: "documentdb.feature.usage"         # feature usage counters
    collection_interval: 60s
    query_timeout: 10s
    emit_to: [warm_path]                            # Analytics only — not real-time alerting
    warm_path_table: PgMongoFeatureUsage
    citus_role: coordinator
    replication_role: primary
    min_schema_version: "0.105-0"                   # documentdb_api_catalog.feature_usage() added in 105

  # PG built-in — no min_schema_version
  - metric_name: "documentdb.db.connections.by_state"  # per-state connection counts
    collection_interval: 15s
    query_timeout: 3s
    emit_to: [hot_path]
    citus_role: all
    replication_role: all

  # PG built-in — no min_schema_version
  - metric_name: "documentdb.replication.replay_lag"   # replication lag
    collection_interval: 10s
    query_timeout: 3s
    emit_to: [hot_path]                                # Critical for alerting
    citus_role: all
    replication_role: primary
```

#### Field Reference

| Field | Type | Default | Description | Source (from PR GUC) |
|-------|------|---------|-------------|---------------------|
| `metric_name` | string (exact) | — | Exact `metric_name` from `engine_metrics.yaml` (first metric in the query's `metrics[]` array). No globs — 1:1 match. | N/A (GUC uses per-metric key) |
| `collection_interval` | duration | `60s` | How often to execute the query. Maps to GUC `frequency`. | `frequency` |
| `query_timeout` | duration | `5s` | SQL statement timeout. Query is cancelled if exceeded. | `timeout` |
| `emit_to` | list of strings | `[hot_path]` | Emission targets: `hot_path` (MDM/StatsD), `warm_path` (MDSD/FluentD). | Implicit: `table` present = warm, absent = hot |
| `warm_path_table` | string | — | MDSD table name for warm path logging. Required when `warm_path` in `emit_to`. Schema auto-generated from query result columns + timestamp. | `table` |
| `citus_role` | enum | `coordinator` | Which Citus node roles execute this query: `coordinator`, `worker`, `all`. | `citus_role` |
| `replication_role` | enum | `primary` | Which replication roles execute this query: `primary`, `standby`, `all`. | `replication_role` |
| `disabled` | bool | `false` | If `true`, skip this metric group. Useful for emergency shutoff without removing the rule. | `disabled` |
| `min_schema_version` | string | — | Minimum extension schema version required. Omit for PG built-in queries (always safe). Build-time filter skips queries where `min_schema_version > target_schema`. See Decision 33. | N/A (new) |

#### Decision 30: Unmatched Metrics Behavior

**Question**: If a metric is defined in `engine_metrics.yaml` but has no matching rule in `sidecar_routing.yaml`, should the C# sidecar emit it or skip it?

**Decision**: **Emit with defaults** — the routing file controls *how* to emit, not *whether* to emit.

**Rationale**:

| Option | Behavior | Verdict |
|--------|----------|---------|
| **A: Emit with defaults** ✅ | Unmatched metric → collected using `defaults{}` (hot_path, 60s, coordinator, primary-only) | Chosen |
| **B: Skip silently** | Unmatched metric → not collected at all | Rejected: silent failure, easy to forget adding routing rules |
| **C: Skip with warning** | Unmatched metric → not collected, warning logged | Rejected: still requires manual routing rule for every new query |

**Why Option A**:
1. **`engine_metrics.yaml` is the source of truth** for *what* to collect. If a query is there, it was added intentionally. Routing controls *how*, not *whether*.
2. **Defaults are conservative** — hot_path only (no warm path without explicit table name), 30s interval, primary-only. Safe baseline that won't pollute MDSD.
3. **Matches GUC model** — in the PR's GUC design, a metric registered without `table=` emits hot-path-only. Same behavior.
4. **To suppress a metric**, add an explicit `disabled: true` routing rule. This is intentional and auditable, unlike the absence of a rule (which could be an oversight).

**Resulting mental model**:
```
engine_metrics.yaml defines WHAT to collect   → every query runs unless disabled
sidecar_routing.yaml defines HOW to emit      → overrides defaults per metric group
disabled: true in routing is the ONLY way     → to suppress a metric in pgmongo
```

#### Decision 31: Exact Metric Names (Not Glob Patterns) in Routing Rules

**Question**: Should `sidecar_routing.yaml` rules use glob patterns (`match: "documentdb.db.*"`) or exact metric names (`metric_name: "documentdb.db.connections"`) to identify which query a rule applies to?

**Decision**: **Exact metric names** — each routing rule specifies the exact `metric_name` (the first metric in the query's `metrics[]` array) as a hash-map key.

**Rationale**:

| Dimension | Glob (`documentdb.db.*`) | Exact (`documentdb.db.connections`) |
|-----------|--------------------------|--------------------------------------|
| **Ordering risk** | First-match-wins is fragile — reordering rules changes behavior silently | No ordering — hash map lookup, O(1) |
| **warm_path_table** | Shared per group — but warm path needs per-query table names anyway | Per-query — natural fit since each query has a unique MDSD table |
| **Adding new queries** | May auto-match (convenient but surprising if wrong group) | Must add explicit routing rule (safe — falls to defaults per Decision 30) |
| **Disabling** | `documentdb.debug.*` disables a family — convenient | Must list each debug metric — but few debug metrics in practice |
| **GUC alignment** | GUCs are per-metric keys, not glob groups | ✅ 1:1 alignment with GUC model |
| **Implementation** | Requires glob matching engine + ordering care | Simple `Dictionary<string, RoutingRule>` lookup |

**Key insight**: `warm_path_table` is per-query anyway (different queries produce different schemas → different MDSD tables). So globs don't actually save rules for warm-path metrics — the main use case.

**For bulk disable**: If we later need to disable a whole family, we can add a `disabled_prefixes` top-level field as a simpler mechanism than glob rules. Not needed in v1.

#### How the C# Sidecar Uses Both Files Together

```
┌─────────────────────────────┐     ┌──────────────────────────────┐
│  engine_metrics.yaml        │     │  sidecar_routing.yaml        │
│  (shared, OSS)              │     │  (pgmongo-internal)          │
│                             │     │                              │
│  queries:                   │     │  routing:                    │
│    - sql: "SELECT ..."      │     │    - metric_name:            │
│      metrics:               │     │        "documentdb.db        │
│        - metric_name: X     │◄────│         .connections"        │
│          value_column: Y    │     │      collection_interval: 60s│
│          type: gauge        │     │      emit_to: [hot, warm]    │
│                             │     │      warm_path_table: Stats   │
│                             │     │      citus_role: coordinator  │
│                             │     │      replication_role: primary│
└─────────────────────────────┘     └──────────────────────────────┘
         │                                    │
         └───────────── JOIN ─────────────────┘
                        │
                        ▼
              ┌──────────────────┐
              │  Collector Loop  │
              │                  │
              │  For each query: │
              │  1. Get first    │
              │     metric_name  │
              │  2. Match routing│
              │  3. Version gate │
              │  4. Check role   │
              │  5. Execute SQL  │
              │  6. Emit to      │
              │     hot/warm     │
              └──────────────────┘
```

**Matching algorithm**:
1. For each `queries[]` entry in `engine_metrics.yaml`, take the first `metric_name` in its `metrics[]` array.
2. Look up that exact `metric_name` in `routing[].metric_name` (hash map — O(1), no ordering ambiguity).
3. **If no rule matches, apply `defaults`** (Decision 30 — emit with defaults, not skip).
4. If the resolved config has `disabled: true`, skip this query entirely.
5. **Version gate** (Decision 33): If `min_schema_version` is set and exceeds current extension schema version, skip this query (log at debug level).
6. Check `citus_role` and `replication_role` against current node — skip if not applicable.
7. Execute SQL with `query_timeout`. On success, emit via `emit_to` targets.

#### Mapping from PR GUC Format to Two-File Format

The PR's GUC format packs everything into one string per metric:
```ini
pgmongo_metrics.stat_database = 'query=SELECT * FROM documentdb_stat_database;table=PgMongoDatabaseStats;frequency=60;timeout=30;citus_role=coordinator;replication_role=primary;disabled=false'
```

Our two-file approach splits this:

| GUC Field | Where It Lives | Why |
|-----------|---------------|-----|
| `query` | `engine_metrics.yaml` → `queries[].sql` | Shared with OSS — same SQL in OTel Collector and C# sidecar |
| `table` | `sidecar_routing.yaml` → `routing[].warm_path_table` | pgmongo-internal emission concept |
| `frequency` | `sidecar_routing.yaml` → `routing[].collection_interval` | Different intervals for pgmongo vs OSS (OSS uses OTel Collector's `collection_interval`) |
| `timeout` | `sidecar_routing.yaml` → `routing[].query_timeout` | Operational tuning, pgmongo-internal |
| `citus_role` | `sidecar_routing.yaml` → `routing[].citus_role` | pgmongo-specific (Citus sharding) — not applicable to OSS single-node |
| `replication_role` | `sidecar_routing.yaml` → `routing[].replication_role` | pgmongo-specific (HA topology) — OSS has its own HA via CNPG |
| `disabled` | `sidecar_routing.yaml` → `routing[].disabled` | Operational toggle, pgmongo-internal |
| N/A (new) | `sidecar_routing.yaml` → `routing[].min_schema_version` | Version gating for binary↔schema gap (Decision 33) |

#### GUC Coexistence: Routing Config vs. Runtime GUC Overrides

The PR design registers metric configs as PostgreSQL GUCs (`pgmongo_metrics.*`), which allows runtime modification via `ALTER SYSTEM`. Our `sidecar_routing.yaml` is a static file delivered via shared volume mount.

**Design choice**: `sidecar_routing.yaml` is the **primary** configuration source. GUC-based runtime overrides are a **future enhancement** (not in v1).

| Aspect | sidecar_routing.yaml (v1) | GUC overrides (future v2) |
|--------|---------------------------|---------------------------|
| **When to use** | Standard deployment — config changes via redeploy | Emergency runtime changes without redeploy |
| **Scope** | All metrics | Per-metric override |
| **Persistence** | File on shared volume | `postgresql.conf` / `ALTER SYSTEM` |
| **Restart required** | No (sidecar watches file for changes) | No (`pg_reload_conf()`) |
| **Priority** | Base config | Overrides `sidecar_routing.yaml` values |

**Why file-first in v1**: File-based config is version-controlled, auditable, and consistent with how `engine_metrics.yaml` and other configs are delivered. GUC overrides add complexity (merge logic, priority rules, persistence across restarts) — defer to v2 if needed.

#### Naming Convention Integration (from PR)

The PR introduces automatic metric/dimension detection via column naming conventions:
- Columns ending with units (`_count`, `_bytes`, `_seconds`, `_ms`, `_percent`) → metric values
- Other columns → dimensions

Our OTel-format `engine_metrics.yaml` already has **explicit** `value_column` and `attribute_columns` for each metric — no auto-detection needed. However, for future GUC-discovered metrics (v2), the sidecar can fall back to naming convention detection.

| Config Source | Metric Detection | When Used |
|---------------|-----------------|-----------|
| `engine_metrics.yaml` (OTel format) | Explicit `value_column` + `attribute_columns` | Always (v1) |
| GUC-discovered (`pgmongo_metrics.*`) | Naming convention auto-detection | Future (v2) |

#### Emission Path Details

**Hot Path (MDM)**:
- Protocol: StatsD UDP (port 8125 → MDSD → Geneva → MDM)
- Metric name format: `documentdb_stat_database_connections` (dots replaced with underscores per MDM convention)
- Dimensions: flat key-value pairs from `attribute_columns` + `static_attributes`
- Latency: ~30s end-to-end
- Use for: real-time alerting, dashboards, SLO monitoring

**Warm Path (MDSD/FluentD)**:
- Protocol: TCP MessagePack (port 24224 → FluentD → MDSD → Kusto)
- Data format: full row logged as structured event with auto-generated schema
- Table name: from `warm_path_table` in routing config
- Extra column: `timestamp` (added automatically by collector)
- Latency: 1-5 minutes end-to-end
- Use for: analytics, historical trends, debugging, feature usage tracking

#### C# POCOs (updated from earlier section)

```csharp
// sidecar_routing.yaml POCO (pgmongo-internal only)

public class RoutingConfig
{
    public string Version { get; set; }
    public RoutingDefaults Defaults { get; set; }
    public List<RoutingRule> Routing { get; set; }
}

public class RoutingDefaults
{
    [YamlMember(Alias = "collection_interval")]
    public string CollectionInterval { get; set; }  // "30s"
    
    [YamlMember(Alias = "query_timeout")]
    public string QueryTimeout { get; set; }  // "5s"
    
    [YamlMember(Alias = "emit_to")]
    public List<string> EmitTo { get; set; }  // ["hot_path", "warm_path"]
    
    [YamlMember(Alias = "citus_role")]
    public string CitusRole { get; set; }  // "all"
    
    [YamlMember(Alias = "replication_role")]
    public string ReplicationRole { get; set; }  // "primary"
}

public class RoutingRule
{
    [YamlMember(Alias = "metric_name")]
    public string MetricName { get; set; }  // exact: "documentdb.db.connections"
    
    [YamlMember(Alias = "collection_interval")]
    public string CollectionInterval { get; set; }
    
    [YamlMember(Alias = "query_timeout")]
    public string QueryTimeout { get; set; }
    
    [YamlMember(Alias = "emit_to")]
    public List<string> EmitTo { get; set; }
    
    [YamlMember(Alias = "warm_path_table")]
    public string WarmPathTable { get; set; }
    
    [YamlMember(Alias = "citus_role")]
    public string CitusRole { get; set; }
    
    [YamlMember(Alias = "replication_role")]
    public string ReplicationRole { get; set; }
    
    public bool Disabled { get; set; }
    
    [YamlMember(Alias = "min_schema_version")]
    public string MinSchemaVersion { get; set; }  // "0.108-0" — null = PG built-in, always safe
}
```

#### Why Not Just Use GUCs Directly?

| Dimension | GUC-only (PR approach) | File + GUC (our approach) |
|-----------|----------------------|---------------------------|
| **Version control** | GUCs in `postgresql.conf` — not in app repo | `sidecar_routing.yaml` in `pgmongo/telemetry/` — version-controlled |
| **Shared config** | Query embedded in GUC string — can't share with OTel | Queries in `engine_metrics.yaml` — shared across deployments |
| **Runtime changes** | ✅ `ALTER SYSTEM` + `pg_reload_conf()` | File: watch + reload. GUC override: future v2 |
| **Discovery** | ✅ `pg_settings WHERE name LIKE 'pgmongo_metrics.%'` | File is explicit — no discovery needed |
| **Auditability** | Harder — GUC changes aren't git-tracked | ✅ File changes are PR-reviewed |
| **Multi-environment** | Must set GUCs per environment | File delivered with image — consistent |

**Answer**: GUC-only works but loses the shared-config-with-OSS goal (query embedded in GUC string can't be reused by OTel Collector). Our approach uses GUCs as an *override layer* (v2), not the primary source.

#### Decision 33: Version Compatibility Strategy (Binary↔Schema Gap)

**Problem**: In pgmongo's upgrade architecture, binary is always one version ahead of schema:

```
After binary upgrade:   Binary = N, Schema = N-1
After ALTER EXTENSION:  Binary = N, Schema = N-1  (schema upgrades FROM N-2 TO N-1, not to N)
After complete_upgrade: Binary = N, Schema = N-1, Cluster = N-1  (cluster version only)
Next upgrade cycle:     Schema finally reaches N (via N-1 → N during next upgrade)
```

**Why GUCs don't solve this**: PostgreSQL GUCs are registered via `DefineCustomStringVariable()` in the `.so` library's `_PG_init()` — they're **binary-level**, not schema-level. When binary N loads, ALL N GUCs appear in `pg_settings`, including new ones whose queries reference schema-N objects. But schema is N-1, so those objects don't exist yet. **This is the same problem whether we use GUCs or YAML.**

**Why documentdb (OSS) is unaffected**: In documentdb-k8s and documentdb-local, binary and schema are always in sync (operator controls both). The version gap is purely a pgmongo internal constraint.

**Decision**: **Backward-compatible queries by construction (S1)** — enforced via `min_schema_version` field in the pgmongo-internal routing file.

**How it works (three layers)**:

**Layer 1: `min_schema_version` in routing file (the gating mechanism)**

Each extension-specific metric in `sidecar_routing.yaml` declares the minimum extension schema version its query requires. PG built-in queries (pg_stat_*, citus_stat_*) omit this field — they're always safe.

```yaml
# sidecar_routing.yaml — version gating examples
routing:
  # PG built-in — no version gate needed
  - metric_name: "documentdb.db.connections"
    warm_path_table: PgMongoDatabaseStats

  # Extension query — needs schema 0.105-0
  - metric_name: "documentdb.collection.stats"
    warm_path_table: CollectionStats
    min_schema_version: "0.105-0"

  # New in release 110 — needs schema 0.110-0
  - metric_name: "documentdb.replication.lag_details"
    warm_path_table: ReplicationLagDetails
    min_schema_version: "0.110-0"
```

**Layer 2: Build-time filtering (the enforcement)**

pgmongo's build pipeline filters engine_metrics.yaml to the safe subset:

```python
# pgmongo build step: filter metrics for version safety
def filter_metrics_for_schema(metrics, routing, target_schema_version):
    """
    target_schema_version = N-1 (the guaranteed schema after this upgrade)
    """
    safe_queries = []
    for query in metrics['queries']:
        name = query['metrics'][0]['metric_name']
        rule = routing_map.get(name)

        if rule is None or rule.get('min_schema_version') is None:
            safe_queries.append(query)  # PG built-in — always safe
        elif rule['min_schema_version'] <= target_schema_version:
            safe_queries.append(query)  # Extension query — schema is old enough
        else:
            log(f"Skipping {name}: needs schema {rule['min_schema_version']}, "
                f"target is {target_schema_version}")

    return safe_queries
```

**Layer 3: CI validation (the safety net)**

CI in the pgmongo repo checks consistency:

```
CI: metrics-compat-check

1. Parse engine_metrics.yaml + internal_engine_metrics.yaml → all metrics
2. Parse sidecar_routing.yaml → routing entries
3. For each extension-specific metric (references documentdb_api.*, helio_api.*, etc.):
   - MUST have a routing entry with min_schema_version
   - If missing → ❌ FAIL with message:
     "Metric 'X' references extension objects but has no min_schema_version
      in sidecar_routing.yaml. Add: min_schema_version: '0.NNN-0'"
4. For PG built-in metrics (references only pg_*, citus_stat_*):
   - Should NOT have min_schema_version (warn if present — unnecessary)
```

**Extension-specific detection heuristic** (for CI):
```python
EXTENSION_SCHEMAS = [
    "documentdb_api.", "documentdb_api_catalog.", "documentdb_api_internal.",
    "documentdb_data.", "pgmongo.", "helio_api.", "helio_core.",
]

def is_extension_specific(sql):
    return any(schema in sql.lower() for schema in EXTENSION_SCHEMAS)
```

**Developer workflow** (single PR, good DX):

```
1. Developer adds new view in schema upgrade script (release N)
   → pgmongo--N-1--N.sql: CREATE FUNCTION documentdb_api.new_view()...

2. Developer adds metric query in engine_metrics.yaml (same PR)
   → - metric_name: documentdb.new_metric
       sql: "SELECT * FROM documentdb_api.new_view()"

3. Developer adds routing entry with version gate (same PR)
   → sidecar_routing.yaml:
       - metric_name: "documentdb.new_metric"
         warm_path_table: NewMetric
         min_schema_version: "0.N-0"

4. CI validates ✅ (compat entry exists, version matches)
5. PR merges — all three changes co-located in one PR
```

**Release effects**:

| Release | Binary | Schema (post-upgrade) | This metric in pgmongo? | This metric in documentdb-k8s? |
|---------|--------|----------------------|------------------------|-------------------------------|
| N | N | N-1 | ❌ Filtered (needs N, has N-1) | ✅ Active (no gap) |
| N+1 | N+1 | N | ✅ Active (needs N, has N) | ✅ Active |

**What gets the one-release lag** (pgmongo only):

| Query Type | Examples | Lag in pgmongo? | Lag in documentdb? |
|------------|---------|-----------------|-------------------|
| PG built-in | `pg_stat_activity`, `pg_stat_database` | **None** | **None** |
| Citus built-in | `citus_stat_statements` | **None** | **None** |
| Extension (existing objects) | Queries using objects from schema ≤ N-1 | **None** | **None** |
| Extension (new objects in this release) | Queries using objects added in schema N | **One release** | **None** |

**Why no separate compat file in OSS**: The version gap is pgmongo-specific. `engine_metrics.yaml` in documentdb OSS contains ALL queries — documentdb-k8s and documentdb-local use them all directly (binary = schema, no gap). The `min_schema_version` field lives in `sidecar_routing.yaml` which is already internal-only. **Zero OSS footprint for an internal constraint.**

**Consumer matrix**:

| Consumer | Reads | Version-filters? | Result |
|----------|-------|-------------------|--------|
| **documentdb-k8s OTel** | `engine_metrics.yaml` | No — binary = schema | All queries active |
| **documentdb-local OTel** | `engine_metrics.yaml` | No — binary = schema | All queries active |
| **pgmongo build** | All 3 files | Yes — `min_schema_version <= N-1` | Safe subset only |
| **pgmongo CI** | All 3 files | Validates consistency | Catches missing entries |

**Reconsider if**:
- pgmongo changes its upgrade architecture to bring schema to N (not N-1) during binary upgrade → version gating becomes unnecessary
- The one-release lag for new extension metrics is unacceptable → use runtime version check as complement (query `SELECT extversion FROM pg_extension` at sidecar startup)

---

## CRD Schema

### Current State (implemented in code)

```go
type MonitoringSpec struct {
    Enabled            bool
    Exporter           *OTelExporterSpec           // OTLP push destination
    EngineMetrics      *EngineMetricsSpec
    CustomQueriesConfigMap []cnpgv1.ConfigMapKeySelector
    CollectorImage     string
    CollectionInterval *int32                      // 5-3600, default 30
    Resources          *corev1.ResourceRequirements
}
```

### Target State (after push/pull + multi-source changes)

```go
type MonitoringSpec struct {
    Enabled            bool

    // Export modes (at least one required when enabled)
    Push               *PushExporterSpec           // OTLP push to remote endpoint
    Pull               *PullExporterSpec           // Prometheus pull on local port

    // Metric sources
    EngineMetrics      *EngineMetricsSpec          // SQL queries → PG (existing)
    GatewayMetrics     *GatewayMetricsSpec         // Scrape gateway /metrics (future)
    HostMetrics        *HostMetricsSpec            // CPU/mem/disk/net from /proc
    CnpgMetrics        *CnpgMetricsSpec            // Scrape CNPG :9187 (opt-in)

    // Custom user queries
    CustomQueriesConfigMap []cnpgv1.ConfigMapKeySelector

    // Collector configuration
    CollectorImage     string
    CollectionInterval *int32                      // 5-3600, default 30
    Resources          *corev1.ResourceRequirements
}

type PushExporterSpec struct {              // Renamed from OTelExporterSpec
    Endpoint          string               // Required. "host:4317" or "https://host:4318/v1/metrics"
    Protocol          string               // grpc|http, default grpc
    Insecure          bool                 // default false
    Headers           map[string]string
    HeadersFromSecret *corev1.LocalObjectReference
}

type PullExporterSpec struct {             // NEW
    Enabled           bool                 // default false
    Port              *int32               // default 9464
}

type EngineMetricsSpec struct {            // Existing
    Disabled          bool
    ConfigMapOverride *cnpgv1.ConfigMapKeySelector
}

type GatewayMetricsSpec struct {           // NEW — future, no-op for now
    Enabled           bool                 // default true when available
}

type HostMetricsSpec struct {              // NEW
    Enabled           bool                 // default true
    CollectionInterval *int32              // default 30
}

type CnpgMetricsSpec struct {             // NEW
    Enabled           bool                 // default false
}

type MonitoringStatus struct {
    Enabled              bool
    EngineMetricsConfigMap string
    PushEndpoint         string
    PullPort             *int32
    CollectorReady       bool
}
```

### Example CRD Usage

```yaml
apiVersion: documentdb.azure.com/preview
kind: DocumentDB
metadata:
  name: my-cluster
spec:
  monitoring:
    enabled: true
    push:
      endpoint: "otel-collector.observability:4317"
      protocol: grpc
    pull:
      enabled: true
      port: 9464
    engineMetrics:
      disabled: false
    hostMetrics:
      enabled: true
    cnpgMetrics:
      enabled: false    # opt-in for CNPG's built-in PG stats
    collectionInterval: 30
```

---

## Generated OTel Collector Config (by Operator)

The operator generates the full OTel Collector config at reconcile time from CRD fields:

```yaml
receivers:
  sqlquery:
    driver: postgres
    datasource: "host=localhost dbname=postgres user=postgres sslmode=disable"  # trust auth — no password needed
    queries: ${file:/etc/otel/engine_metrics.yaml}
    collection_interval: 30s

  hostmetrics:                        # when hostMetrics.enabled
    collection_interval: 30s
    scrapers:
      cpu: {}
      memory: {}
      disk: {}
      network: {}
      filesystem: {}

  prometheus/cnpg:                    # when cnpgMetrics.enabled
    config:
      scrape_configs:
        - job_name: cnpg
          scrape_interval: 30s
          static_configs:
            - targets: ["localhost:9187"]

  # FUTURE: prometheus/gateway when gateway exposes /metrics

processors:
  resource:
    attributes:
      - key: service.name
        value: documentdb
        action: upsert
      - key: k8s.pod.name
        value: ${env:POD_NAME}
        action: upsert

exporters:
  otlp:                               # when push configured
    endpoint: "${PUSH_ENDPOINT}"
    tls:
      insecure: ${PUSH_INSECURE}
  prometheus:                          # when pull configured
    endpoint: "0.0.0.0:9464"

service:
  pipelines:
    metrics:
      receivers: [sqlquery, hostmetrics]   # dynamic based on enabled sources
      processors: [resource]
      exporters: [otlp, prometheus]        # dynamic based on push/pull config
```

---

## File Layout

```
documentdb/                          (OSS repo, public GitHub)
  telemetry/
    engine_metrics.yaml              ← Core engine metrics — queries-only (OTel sqlquery format)
    README.md                        ← Schema documentation + how to add metrics + config merge guide
    examples/
      docker-compose.yaml            ← Ready-to-run: documentdb-local + OTel + Prometheus + Grafana
      otel-collector-local.yaml      ← Deployment-specific: datasource + exporters (for docker-compose)
      otel-collector-otlp.yaml       ← Standalone example: queries + OTLP push export
      prometheus.yaml                ← Prometheus scrape config → OTel :9464
      grafana-datasource.yaml        ← Grafana auto-provisioning for Prometheus

pgmongo/                             (internal repo)
  oss/telemetry/                     ← Synced from documentdb (same as above)
    engine_metrics.yaml
    examples/ ...
  telemetry/                         ← Internal-only (outside OSS boundary)
    internal_engine_metrics.yaml     ← Extra internal-only engine metric queries (same OTel format)
    sidecar_routing.yaml             ← Emission routing: hot/warm path, intervals, timeouts, Citus/replication roles (Decision 29)

documentdb-kubernetes-operator/
  operator/src/api/preview/
    documentdb_types.go              ← MonitoringSpec, PushExporterSpec, PullExporterSpec, etc.
  operator/src/internal/cnpg/
    cnpg_cluster.go                  ← getMonitoringConfiguration() — disables CNPG exporter
  helm/
    files/engine_metrics.yaml        ← Bundled at release time → ConfigMap
```

---

## Cross-Deployment Matrix

| Source | pgmongo (internal) | K8s operator | documentdb-local (Docker) | Self-hosted OTel |
|--------|--------------------|--------------|--------------------------|------------------|
| **Engine** | C# sidecar (same `engine_metrics.yaml`) | OTel sidecar — sqlquery receiver | OTel container — sqlquery receiver via docker-compose | OTel — sqlquery receiver (user-managed) |
| **Config merge** | C# parses queries directly | `--config` merge: engine_metrics.yaml + operator-generated config | `--config` merge: engine_metrics.yaml + otel-collector-local.yaml | `--config` merge: engine_metrics.yaml + user config |
| **Routing** | `sidecar_routing.yaml` → hot/warm path, Citus/replication roles | OTel pipeline config (operator-generated) | OTel pipeline config (user-defined) | OTel pipeline config (user-defined) |
| **Gateway** | C# managed gateway has its own metrics | OTel prometheus scrape (FUTURE) | Not exposed (FUTURE) | OTel prometheus scrape (FUTURE) |
| **VM/Host** | vmmetrics_collector (existing Rust) | OTel hostmetrics receiver | N/A (Docker host not instrumented) | OTel hostmetrics receiver |
| **CNPG** | N/A (no CNPG in managed) | OTel prometheus scrape :9187 (opt-in) | N/A (no CNPG) | N/A |

**Shared across all four modes**: `engine_metrics.yaml` (queries-only). Everything else is deployment-specific.

---

## Delivery Phasing

```
Phase 1 (ship first):  Engine metrics (sqlquery) + Push/Pull CRD + OTel sidecar injection
Phase 2 (easy add):    Host metrics (hostmetrics receiver) — no PG dependency
Phase 3 (opt-in):      CNPG metrics (prometheus scrape of :9187) — re-enable :9187
Phase 4 (blocked):     Gateway metrics (requires documentdb repo instrumentation)
```

Each phase is independently shippable. No dependencies between phases except Phase 1 establishing the sidecar.

---

## Open Questions

| # | Question | Why It Matters | Default If No Answer |
|---|----------|---------------|---------------------|
| 1 | PG user/role for OTel Collector's sqlquery receiver? | Permissions for `pg_stat_*` views. Currently `pg_hba.conf` uses `trust` auth (no password needed for any user), so credential management is trivially solved today. But `trust` is a security concern for production — if/when it's tightened, we need a plan. | Use `postgres` user with trust auth for now. When pg_hba.conf is hardened: create `pg_monitor` role, use Unix socket with peer auth (sidecar) or password-from-Secret (if DaemonSet). |
| 2 | OTel Collector container image — `contrib` vs custom build? | `contrib` is ~150MB; custom can be smaller | Start with `contrib`, optimize later |
| 3 | Connection string — how does sidecar get PG connection details? | Must be automated, not user-configured. With trust auth, connection is trivial: `host=localhost user=postgres dbname=postgres`. | Operator generates from CNPG Cluster status. With trust auth: `host=localhost user=postgres dbname=postgres sslmode=disable` |
| 4 | ConfigMap structure — single vs separate for queries vs collector config? | Affects reconciliation granularity | Two ConfigMaps: one for queries (from Helm), one for generated OTel config |
| 5 | Resource defaults for OTel Collector sidecar? | Must be reasonable out of the box | 64Mi request, 128Mi limit, 10m CPU request, 100m CPU limit |
| 6 | Should `gatewayMetrics` CRD field ship now as no-op? | CRD field additions are non-breaking | Yes — reserves the name |
| 7 | `hostmetrics` needs `/proc` and `/sys` — security context implications? | May need `hostPID: true` or `procMount` | CNPG pods already have procfs access; verify in e2e test |
| 8 | Should CNPG metrics be relabeled to match our naming conventions? | `pg_stat_*` names vs `documentdb.*` namespace | Add `metricstransform` processor (Phase 3) |
| 9 | When will `pg_hba.conf` be hardened beyond `trust`? | Affects credential strategy for monitoring collectors. With `trust`, both sidecar and DaemonSet connect without credentials. With password auth, sidecar uses Unix socket peer auth (zero-cred); DaemonSet needs password from Secret + TLS. | Treat `trust` as current state; design monitoring to work with `trust` now and adapt when auth is tightened. Sidecar's Unix socket path makes it auth-model-agnostic. |
| 10 | Should gateway OTel SDK push to sidecar or directly to user destination? | Gateway will push OTLP (Decision 24 future work). If it pushes to sidecar's OTLP receiver (localhost:4317), sidecar aggregates. If gateway pushes directly, sidecar doesn't see gateway metrics. | Gateway pushes to sidecar's OTLP receiver at localhost:4317. Keeps single aggregation point. |

---

## Appendix A: Sidecar vs DaemonSet Deep Comparison

This appendix provides the comprehensive analysis behind Decision 8. It was produced after multiple rounds of adversarial questioning that corrected several overclaims in the initial analysis.

### A.1 Context: What Each Topology Looks Like

```
Sidecar Topology (chosen):
┌──────────────────── Node ─────────────────────┐
│  ┌── Pod 1 ──────────────┐                    │
│  │ PostgreSQL ◄── sqlquery│                    │
│  │ Gateway ──OTLP──► OTel │──► OTLP push      │
│  │ /proc ◄── hostmetrics  │◄── Prom pull :9464 │
│  └────────────────────────┘                    │
│  ┌── Pod 2 ──────────────┐                    │
│  │ PostgreSQL ◄── sqlquery│                    │
│  │ Gateway ──OTLP──► OTel │──► OTLP push      │
│  │ /proc ◄── hostmetrics  │◄── Prom pull :9464 │
│  └────────────────────────┘                    │
└────────────────────────────────────────────────┘

DaemonSet Topology (rejected):
┌──────────────────── Node ─────────────────────┐
│  ┌── Pod 1 ─────────┐  ┌── Pod 2 ──────────┐ │
│  │ PostgreSQL        │  │ PostgreSQL         │ │
│  │ Gateway ──OTLP──┐ │  │ Gateway ──OTLP──┐  │ │
│  └──────────────────┘ │  └──────────────────┘  │ │
│                       ▼                        ▼ │
│          ┌── DaemonSet Pod ──────────────────┐ │
│          │ OTel Collector                     │ │
│          │  ├ sqlquery/pod1 (TCP to pod1-ip)  │ │
│          │  ├ sqlquery/pod2 (TCP to pod2-ip)  │ │
│          │  ├ OTLP receiver :4317 (hostPort)  │ │
│          │  ├ hostmetrics (node-level /proc)  │ │
│          │  └─► OTLP push / Prom pull         │ │
│          └────────────────────────────────────┘ │
└────────────────────────────────────────────────┘
```

### A.2 Credential Management (Updated with pg_hba.conf Trust Auth)

**Current state**: `pg_hba.conf` is configured with `trust` authentication:

```
host all         all 0.0.0.0/0  trust    ← anyone can connect, no password needed
host all         all ::0/0      trust    ← same for IPv6
host replication all all         trust    ← replication too
```

**Today, credential management is a non-issue for BOTH topologies.** Any process that can reach the PG port can connect without a password. This eliminates the credential delta between sidecar and DaemonSet.

**However, `trust` auth is a security concern that will likely be hardened.** When that happens:

| Auth Model | Sidecar Impact | DaemonSet Impact |
|---|---|---|
| **`trust` (current)** | Connect via `host=localhost user=postgres` — works | Connect via `host=<pod-ip> user=postgres` — works |
| **`md5`/`scram-sha-256` (future)** | Unix socket with `local ... peer` in pg_hba.conf — no password needed | Must get password from Secret, mount to DaemonSet |
| **`cert` (future, strict)** | Mount client cert via same-pod Secret — easy | Mount client cert + handle rotation across DaemonSet pods |

**Key insight**: The sidecar has a **forward-compatible credential path** via Unix socket peer auth. When `trust` is removed, sidecar adds one line to pg_hba.conf (`local all pg_monitor peer`) and connects with zero secrets. DaemonSet must build a credential distribution system.

**The operator's existing `executeSQLCommand`** uses `kubectl exec` → `psql -U postgres` (Unix socket inside the container). This proves the operator CAN manage PG, but it uses a fundamentally different connection mechanism than what a DaemonSet collector needs. The operator doesn't "have credentials" — it bypasses them via pod exec.

### A.3 Shared Config Format — Both Topologies Work

**Previous overclaim (corrected)**: DaemonSet can't address the shared config goal.

**Corrected**: The same `engine_metrics.yaml` works with both topologies. Only the `datasource` line differs:

```yaml
# Sidecar datasource
datasource: "host=localhost port=5432 user=postgres dbname=postgres sslmode=disable"

# DaemonSet datasource (per pod)
datasource: "host=10.244.0.15 port=5432 user=postgres dbname=postgres sslmode=disable"
```

The queries themselves are identical. The shared config goal (G1) is satisfied by either topology.

### A.4 Gateway OTel Integration

When the Rust gateway introduces OTel SDK instrumentation, it will **push** metrics via OTLP. The gateway is always a push source regardless of collector topology.

| Aspect | Sidecar | DaemonSet |
|---|---|---|
| **Gateway push target** | `localhost:4317` (OTel Collector sidecar's OTLP receiver) | `<node-ip>:<hostPort>` (DaemonSet's OTLP receiver) |
| **Config in gateway** | `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317` — hardcoded, no env injection needed | `OTEL_EXPORTER_OTLP_ENDPOINT=http://$(NODE_IP):4317` — needs Downward API env var |
| **DaemonSet restart** | Gateway container is in same pod — if sidecar restarts, gateway stays running. OTel SDK buffers and reconnects. | Gateway stays running. OTel SDK buffers and reconnects. Same behavior. |
| **Collector upgrade** | ⚠️ Pod restart includes gateway | ✅ Gateway untouched |

Both topologies work. Sidecar is simpler (no Downward API), DaemonSet decouples upgrades.

### A.5 Blast Radius Analysis

#### Collector crash

| Scenario | Sidecar | DaemonSet |
|---|---|---|
| **What happens** | 1 sidecar crashes → Kubernetes restarts the container (not the pod). PG and gateway stay running. | DaemonSet pod crashes → all instances on that node lose monitoring. |
| **Metrics gap** | 1 instance, ~5-15s | All instances on node, ~10-30s |
| **Gateway push during gap** | OTel SDK buffers in memory, reconnects on sidecar restart | OTel SDK buffers in memory, reconnects on DaemonSet restart |
| **sqlquery during gap** | 1 instance stops collecting engine metrics | All instances on node stop collecting |

#### Bad config change

| Scenario | Sidecar | DaemonSet |
|---|---|---|
| **Blast radius** | 1 instance (each sidecar has independent config) | ⚠️ ALL instances on ALL nodes (single ConfigMap) |
| **Detection** | 1 pod's monitoring down | All monitoring down — more obvious but worse impact |
| **Recovery** | Revert ConfigMap → sidecar reloads | Same — revert ConfigMap → DaemonSet reloads |

#### Per-instance config override (e.g., disable engine metrics for replica-2 only)

| Scenario | Sidecar | DaemonSet |
|---|---|---|
| **Mechanism** | Per-pod ConfigMap override or annotation | DaemonSet must maintain per-instance config sections |
| **Operator complexity** | Low — each sidecar has independent config | High — single config must map instance identities to settings |

### A.6 Collector Upgrade Lifecycle (Key DaemonSet Advantage)

This is the strongest argument for DaemonSet:

| Scenario | Sidecar | DaemonSet |
|---|---|---|
| **Config change (toggle metrics)** | ✅ ConfigMap hot-reload, no pod restart, PG untouched | ✅ ConfigMap hot-reload, no pod restart, PG untouched |
| **Collector image upgrade** | ⚠️ **Pod restart required** — new image means new container spec → CNPG rolling restart of all DB pods | ✅ **DaemonSet rolling update only** — DB pods completely untouched |
| **Impact of collector upgrade** | Each DB pod restarts sequentially (brief PG unavailability per pod) | Only DaemonSet pods restart — zero DB impact |
| **Upgrade frequency** | Each collector upgrade = DB disruption → discourage frequent upgrades | Collector upgrades are cheap → can upgrade freely |

**Why this matters**: If we expect to iterate on the OTel Collector configuration, image, or version frequently (likely in early phases), the sidecar topology means each collector change forces a database rolling restart. This creates pressure to batch collector changes and delay upgrades.

**Mitigation for sidecar**: Kubernetes 1.29+ has native sidecar containers (KEP-753) that support independent restart. When CNPG adopts this, sidecar collector upgrades won't require DB pod restart. This is not available today but is the trajectory.

### A.7 Resource Efficiency

| Scenario | Sidecar (50MB each) | DaemonSet (80MB each) |
|---|---|---|
| 3 pods, 3 nodes (instancesPerNode: 1) | 3 × 50MB = 150MB | 3 × 80MB = 240MB | 
| 3 pods, 1 node (instancesPerNode: 3) | 3 × 50MB = 150MB | 1 × 80MB = 80MB ✅ |
| 6 pods, 3 nodes (instancesPerNode: 2) | 6 × 50MB = 300MB | 3 × 80MB = 240MB ✅ |
| 10 pods, 10 nodes (instancesPerNode: 1) | 10 × 50MB = 500MB | 10 × 80MB = 800MB |

DaemonSet is only more efficient when `instancesPerNode > 1`. With the default `instancesPerNode: 1`, DaemonSet uses MORE memory (needs more receiver instances, bigger config).

### A.8 Operator Implementation Complexity

| Responsibility | Sidecar | DaemonSet |
|---|---|---|
| **Pod discovery** | None — localhost | Watch K8s API for DocumentDB pods, track pod IPs, handle scale events |
| **Config generation** | Static per-pod config | Dynamic multi-instance config: N sqlquery receivers, N datasources, regen on scale |
| **Lifecycle management** | CNPG plugin JSONPatch injection — atomic with pod creation | Operator creates/updates DaemonSet resource, manages affinity rules |
| **Credential management (with trust)** | `host=localhost user=postgres` | `host=<pod-ip> user=postgres` (must know pod IPs) |
| **Credential management (post-trust)** | Unix socket peer auth — zero-cred | Password from Secret + TLS |
| **Gateway OTLP endpoint** | Hardcoded `localhost:4317` | Inject `NODE_IP` via Downward API |
| **hostmetrics scope** | Pod-level `/proc` (cgroup-scoped) | Node-level `/proc` — needs cgroup filtering for per-pod attribution |

**Estimated complexity delta**: DaemonSet requires ~3× more operator code for pod discovery, dynamic config, and credential management.

### A.9 Revised Honest Comparison Summary

| Dimension | Sidecar ✅ | DaemonSet | Winner |
|---|---|---|---|
| **Credentials (trust auth)** | localhost, no password | remote TCP, no password | Tie (today) |
| **Credentials (future auth)** | Unix socket peer — zero-cred | Password + TLS from Secret | **Sidecar** |
| **Shared config** | Same engine_metrics.yaml | Same engine_metrics.yaml | Tie |
| **Pod discovery** | None needed | Must discover & track pod IPs | **Sidecar** |
| **Dynamic config** | Static | Regen on scale events | **Sidecar** |
| **Crash blast radius** | 1 instance | All instances on node | **Sidecar** |
| **Bad config blast radius** | 1 instance | All instances everywhere | **Sidecar** |
| **Collector upgrade** | ⚠️ Forces DB pod restart | ✅ DB untouched | **DaemonSet** |
| **Config hot-reload** | Works, DB unaffected | Works, DB unaffected | Tie |
| **Per-instance override** | Natural (independent config) | Complex (multi-instance config) | **Sidecar** |
| **Gateway push endpoint** | `localhost:4317` (trivial) | `<node-ip>:4317` (Downward API) | **Sidecar** |
| **hostmetrics scope** | Pod-scoped (cgroup) | Node-scoped (needs filtering) | **Sidecar** |
| **Resource efficiency** | ~50MB/pod (always) | Saves when instancesPerNode > 1 | Conditional |
| **Operator complexity** | Low (~CNPG plugin) | ~3× more code | **Sidecar** |

**Score**: Sidecar wins 8 dimensions, DaemonSet wins 1 (collector upgrade lifecycle), 4 ties.

### A.10 Corrections Log

During the design review, three overclaims were identified and corrected:

| Original Claim | Correction |
|---|---|
| "DaemonSet is untenable for engine metrics" | DaemonSet is viable but requires ~3× more operator code. `sqlquery` receiver works with remote TCP — it just needs pod discovery and dynamic config. |
| "DaemonSet can't address shared config goal" | Same `engine_metrics.yaml` works with both topologies. Only the `datasource` line differs. |
| "DaemonSet can't do per-instance OTLP push" | DaemonSet CAN do per-instance OTLP push via resource attributes that distinguish instances. Different UX but works. |

### A.11 Relationship to Playground DaemonSet Design

The `documentdb-playground/telemetry/telemetry-design.md` describes a DaemonSet-based OTel Collector for **infrastructure monitoring** (kubeletstats, k8s_cluster, filelog). This design is **complementary, not competing**:

| Aspect | Playground DaemonSet | Our Sidecar |
|---|---|---|
| **Scope** | Cluster infrastructure metrics | Engine + app metrics |
| **Receivers** | kubeletstats, k8s_cluster, filelog | sqlquery, hostmetrics, prometheus, OTLP |
| **PG connection** | None | Yes — SQL queries |
| **Managed by** | User-deployed | Operator-managed |
| **Coexistence** | ✅ Can run alongside sidecar | ✅ Can run alongside DaemonSet |

Users wanting both infrastructure and engine metrics would deploy the playground DaemonSet for cluster-level observability AND use our operator-managed sidecar for engine-level observability.
