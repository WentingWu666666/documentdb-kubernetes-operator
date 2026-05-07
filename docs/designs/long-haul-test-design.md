# Long Haul Test Design — DocumentDB Kubernetes Operator

**Issue:** [#220](https://github.com/documentdb/documentdb-kubernetes-operator/issues/220)  
**Status:** Driver active (Phases 1a–1f and 2c, 2f, 2g, 2h complete; PR [#348](https://github.com/documentdb/documentdb-kubernetes-operator/pull/348))

---

## Terminology

- **DocumentDB cluster** — the database cluster managed by the operator (the `DocumentDB` CR and its pods).
- **Kubernetes cluster** — the infrastructure cluster where the operator and DocumentDB run.

When unqualified, "cluster" refers to the **DocumentDB cluster**.

---

## Problem Statement

E2E tests run for 15–60 minutes from a clean state. They cannot detect bugs whose **accumulation rate is tied to real operations** — memory leaks, lock-table bloat, CR-history drift, upgrade-under-state failures. These bugs surface only after days of continuous operation.

### The Core Insight

> Long-haul testing isn't a different testing *technique* — it's what happens when the failure modes you need to detect require more time and operations than fit in any bounded test run.

You can't speed up "memory leaked per reconciliation cycle" — you need many real reconciliation cycles. The long-haul infrastructure (persistent cluster, event journal, alerting) exists because these tests can't be attended, can't be reset between runs, and need accumulation that no existing test type provides.

### Eight Failure Modes

**Primary** (drive architecture decisions — without specific components, we'd miss these):

| # | Failure Mode | Why It Drives Architecture |
|---|---|---|
| 1 | **Operational Residue** | Resources leak proportional to operations → needs trend analysis on per-component metrics |
| 3 | **Concurrent Data + Control Plane** | Needs continuous data-plane traffic during control-plane ops |
| 7 | **Random Operation Sequences** | Needs weighted-random scheduler + journal for reproduction |
| 8 | **Performance Degradation** | Needs baseline metrics + continuous statistical comparison |

**Emergent** (free by running long — no special components needed):

| # | Failure Mode | Why It's Emergent |
|---|---|---|
| 2 | **Reconciliation Idempotency at Scale** | Millions of reconcile loops happen naturally over days |
| 4 | **Upgrade Under Accumulated State** | Upgrade after days of operations tests this naturally |
| 5 | **Repeated Crash-Recovery Cycles** | Chaos operations over time create compounding partial states |
| 6 | **Environmental Event Overlap** | Real infrastructure events just happen |

---

## Design Principles

These are **negative constraints** — each prevents accidentally hiding a failure mode:

| Principle | What It Prevents |
|---|---|
| **P1.** Bug Amplifier, Not Production Replica | Restarting pods (like production) would reset memory, hiding FM1 |
| **P2.** Don't Change the Measuring Instrument | Changing workload mid-run contaminates the signal — can't tell system bug from test change |
| **P3.** Workload Is Deployment-Blind | L2 workload must not import `client-go` — enables fair comparison across clusters |
| **P4.** Per-Component Attribution From Day One | Without separate series for operator/DB, a memory climb at hour 30 is undiagnosable |
| **P5.** Realistic Concurrency, Not Stress | 20–50 clients (production-like), not 1000+ (stress test is a different program) |
| **P6.** Forward-Only Upgrades; Workload Runs Through | Draining before upgrade hides exactly the upgrade bugs we're testing (FM4) |
| **P7.** Random With Journal, Not Scripted | Scripted sequences only find bugs humans imagined — journal enables reproduction |
| **P8.** Human in the Loop for Alerts | Auto-filed issues cause alert fatigue; humans review before creating issues |

---

## Architecture

### Layered Model

```
┌────────────────────────────────────────────────────────────────┐
│ L4: REPORTING / PASS-FAIL GATES                                │
│     RSS slope · error rate · p99 latency · recovery time       │
│     Trend: this period vs previous · Cluster A vs B delta      │
├────────────────────────────────────────────────────────────────┤
│ L3: METRICS COLLECTION                                         │
│     Prometheus: operator /metrics, postgres-exporter            │
│     cAdvisor / metrics-server for pod-level RSS, CPU           │
├────────────────────────────────────────────────────────────────┤
│ L2: DATA-PLANE WORKLOAD  (deployment-agnostic)                 │
│     Input: mongodb:// connection string + workload config      │
│     Output: structured events (start/end/error/latency)        │
│     MUST NOT import k8s libraries (P3)                         │
├────────────────────┬───────────────────────────────────────────┤
│ L1a: DEPLOYMENT    │ L1b: OPERATION SCHEDULER                  │
│      HARNESS       │      (control-plane workload)             │
│                    │                                           │
│ Provisions or      │  Weighted random k8s operations:          │
│ connects to k8s    │  scale, kill, failover, backup, upgrade   │
│ cluster.           │  Targets Cluster A only.                  │
│                    │  Preconditions + cooldowns + journaled.    │
│ Output:            │  Uses client-go.                          │
│ {conn_string,      │  Output: journal events                   │
│  metrics_targets}  │                                           │
└────────────────────┴───────────────────────────────────────────┘
```

### Cluster Topology — Current vs. Target

**Today (PR #348):** the driver targets a **single DocumentDB cluster**. This is enough to exercise FM1, FM3, FM4, FM7 and to surface most operator-side regressions, and it is what the live `longhaul-aks` canary runs.

**Target state:** add a second **Baseline** DocumentDB cluster (control group) so signals become attributable. The Baseline is left untouched by the operation scheduler:

| Observation | Diagnosis |
|---|---|
| Cluster A degrades, B stable | Per-cluster bug — caused by operations on A |
| Both A and B degrade | Operator-level bug — leak in the shared operator |
| B degrades, A stable | Infrastructure noise — dismiss |

**Future orchestration rules:**
- Operations target **Cluster A only** (Baseline stays stable)
- Data-plane traffic runs on **both** clusters (same load — fair comparison)
- Operator upgrades apply to both (single operator instance)
- DB upgrades can be staggered across clusters (tests mixed-version fleet)

### Failure Mode → Component Mapping

| Failure Mode | Architecture Component |
|---|---|
| FM1: Operational Residue | Health Monitor (per-component trend analysis) |
| FM3: Concurrent Data + Control | Data-Plane Workload + Operation Scheduler (simultaneous) |
| FM4: Upgrade Under Accumulated State | Lifecycle: upgrade after days of accumulated CRs |
| FM7: Random Operation Sequences | Operation Scheduler (weighted random + journal) |
| FM8: Performance Degradation | Health Monitor (baseline comparison: A vs B, this period vs last) |

---

## Lifecycle Model

### Continuous Operation

The test runs **continuously** — no cycles, no resets. Workload, metrics, operations, and health monitoring all run as long-lived processes. The system accumulates real state (PVC growth, CR history, operator memory) exactly as it would in production.

| Activity | Trigger | Disrupts system? |
|---|---|---|
| Workload traffic, metrics, operation scheduling | Always running | No |
| Report generation, journal rotation | Timer (default 48h) | No |
| Operator/DB upgrade | Release workflow | Briefly (pod restarts) |

### Upgrades Triggered From Release Workflow

Upgrades are triggered by the **operator release workflow** — when a new version is published, the release workflow updates the canary's target version (e.g., via ConfigMap patch or Helm upgrade). The harness detects `target ≠ current` and executes the upgrade.

**Baseline gate:** An upgrade doesn't fire immediately. The harness enforces a minimum accumulation period (default 48h) since the last upgrade — ensuring we always test "upgrade after accumulated state" (FM4).

**Collapse rule:** If multiple versions arrive while the gate is closed, only the latest executes. Long-haul is not a per-release regression suite — CI covers that.

**Workload runs through:** No drain, no quiesce. Traffic continues during upgrades. Draining before upgrade hides the bugs we're testing (P6).

### Cluster Retirement

Replace a cluster when: hardware EOL, Kubernetes version too old, or accumulated state exceeds practical limits (e.g., PVC full). Retirement = provision new cluster + start fresh accumulation.

---

## Operations Catalog

Status legend: ✅ implemented in PR #348 · 🔜 planned

| Operation | Category | Target | Precondition | Status |
|---|---|---|---|---|
| Scale Up | Topology | A | `instancesPerNode` < `MaxInstances` (CRD upper bound: 3) | ✅ |
| Scale Down | Topology | A | `instancesPerNode` > `MinInstances` (CRD lower bound: 1) | ✅ |
| DocumentDB Version Upgrade | Lifecycle | A | `instancesPerNode` ≥ 2 (rolling restart needs a peer) | ✅ |
| Controlled Failover | HA | A | cluster healthy, ≥2 instances | 🔜 |
| Kill Primary Pod | Chaos | A | cluster healthy | 🔜 |
| Drain Node | Chaos | A | multi-node cluster | 🔜 |
| Trigger Backup | Data Protection | A | no backup running | 🔜 |
| Verify Backup | Data Protection | A | backup exists | 🔜 |
| Configuration Change | Config | A | cluster healthy | 🔜 |
| Operator Upgrade | Lifecycle | Both | target ≠ current + gate open | 🔜 |

### Sequencing Constraints

| Constraint | Rule | Rationale |
|---|---|---|
| Min Topology | Never scale below `LONGHAUL_MIN_INSTANCES` (default 1, CRD floor) and never above `LONGHAUL_MAX_INSTANCES` (default 3, CRD ceiling) | Stays inside the spec.instancesPerNode range the operator's CRD admits |
| Concurrent Ops | Max 1 disruptive op at a time | Overlapping disruptions are non-diagnosable |
| Cooldown | Min gap between same-category ops (default 5 min) | Let cluster stabilize |
| Steady-State Gate | Health check must pass before next op | Ensures recovery from previous op |
| Backup Isolation | No topology changes during backup | Backup assumes stable topology |
| Region Cardinality | At most 2 region changes per reporting period | Avoids replication thrash |

### Per-Operation Outage Policy

Each operation declares expected disruption and recovery budget:

```go
type OutagePolicy struct {
    AllowedDowntime      time.Duration // reserved — not yet enforced by ExceededPolicy()
    AllowedWriteFailures int           // tolerated errors during window
    MustRecoverWithin    time.Duration // e.g., 5min to return to steady state
}
```

`AllowedDowntime` is declared on the policy struct but not yet consumed by the journal's
`ExceededPolicy()` check — disruption verdicts today are based on `AllowedWriteFailures`
and `MustRecoverWithin`. Wiring `AllowedDowntime` is tracked as future work.

---

## Data Plane Workload

### Writer Model (Durability Oracle)

- Multiple writer goroutines, each with unique `writer_id`
- Each write: `{writer_id, seq, payload, checksum, timestamp}`
- Track states: **attempted** → **acknowledged** → **verified**
- `writeConcern: majority` for durability claims
- Unique index on `(writer_id, seq)` detects duplicates

### Reader/Verifier Model

- Periodic full-scan: no gaps in acknowledged sequences per writer
- Checksum validation on read-back
- `readConcern: majority` to avoid false negatives from replica lag
- Lag-aware: don't flag replication delay as data loss

### Metrics

- `longhaul_writes_{attempted,acknowledged,failed}`
- `longhaul_reads_total`, `longhaul_verification_failures`
- `longhaul_write_latency_ms`, `longhaul_read_latency_ms`

---

## Observability

### Per-Component Attribution (P4)

Separate metrics for: operator RSS (Go), DB pod RSS (postmaster + backends), goroutine count, reconcile rate, API call rate. Without this, a climb at hour 30 is undiagnosable.

### Leak Detection

- Sample memory/CPU at fixed intervals
- Linear regression over last N samples
- Alert if slope exceeds threshold (configurable)
- 48h+ runs recommended for reliable signal vs noise

### Alerting (Human-in-the-Loop — P8)

- Hourly health check detects failures → posts to workflow summary + optional Slack
- Maintainer reviews evidence → manually triggers issue creation via `workflow_dispatch`
- No auto-created issues — reduces noise from transient/infra failures
- Deduplication: skips if an open `long-haul-failure` issue already exists

---

## Deployment & Portability

### Cloud-Agnostic Design

The test binary uses only the Kubernetes API and MongoDB wire protocol — runs on AKS, EKS, GKE, Kind, or any conformant cluster. No cloud-provider dependencies in test code.

### Configuration

All config is read from environment variables by `cmd/longhaul/main.go` via
`config.LoadFromEnv()`. The `LONGHAUL_ENABLED` flag exists in `config.go` but the binary
entry point does not check it — running the driver simply runs the driver. The flag is
retained for any future Ginkgo-style integration that wants a CI safety gate.

| Variable | Required | Default | Description |
|---|---|---|---|
| `LONGHAUL_CLUSTER_NAME` | Yes | — | Target DocumentDB CR name |
| `LONGHAUL_NAMESPACE` | No | `default` | Kubernetes namespace of the target CR |
| `LONGHAUL_MAX_DURATION` | No | `30m` | Max run duration; `0s` means run until failure |
| `LONGHAUL_MONGO_URI` | No | derived | Override the mongo URI; otherwise built from the gateway service |
| `LONGHAUL_NUM_WRITERS` | No | `5` | Concurrent writer goroutines |
| `LONGHAUL_NUM_VERIFIERS` | No | `2` | Concurrent verifier goroutines |
| `LONGHAUL_OP_COOLDOWN` | No | `5m` | Minimum interval between disruptive ops |
| `LONGHAUL_RECOVERY_TIMEOUT` | No | `5m` | Max time the cluster may stay unhealthy after an op |
| `LONGHAUL_STEADY_STATE_WAIT` | No | `60s` | Required healthy window before the next op fires |
| `LONGHAUL_MIN_INSTANCES` | No | `1` | Lower bound for scale-down (`spec.instancesPerNode`; CRD floor is 1) |
| `LONGHAUL_MAX_INSTANCES` | No | `3` | Upper bound for scale-up (`spec.instancesPerNode`; CRD ceiling is 3) |
| `LONGHAUL_REPORT_INTERVAL` | No | `1h` | How often the `longhaul-report` ConfigMap is written |

### Running

**Local development (anyone):**
```bash
cd test/longhaul
LONGHAUL_CLUSTER_NAME=documentdb-sample LONGHAUL_NAMESPACE=documentdb-test-ns \
  LONGHAUL_MAX_DURATION=10m \
  go run ./cmd/longhaul
```

The driver expects a reachable kubeconfig (in-cluster or `KUBECONFIG`) and uses it to
read the DocumentDB CR, watch pods, and pull metrics-server samples.

**Unit tests:**
```bash
cd test/longhaul
go test ./...
```

**Persistent canary (core team):** the live driver runs in `documentdb-test-ns` on the
[`longhaul-aks`](https://ms.portal.azure.com/#@microsoft.onmicrosoft.com/resource/subscriptions/81901d5e-31aa-46c5-b61a-537dbd5df1e7/resourceGroups/longhaul-rg/providers/Microsoft.ContainerService/managedClusters/longhaul-aks/workloads)
cluster. Two GitHub Actions workflows wire it up:

- `LONGHAUL - Build Test Driver Image` — builds and publishes the driver image to GHCR on
  pushes to `developer/wentingwu/long-haul-tests` (and on every PR merge into `main` once
  this PR lands).
- `LONGHAUL - Deploy Test Driver to AKS` — chained via `workflow_run` on the build's
  success. Uses a long-lived ServiceAccount-token kubeconfig stored in the
  `LONGHAUL_KUBECONFIG` repo secret to roll the `longhaul-test` Deployment.
- `LONGHAUL - Monitor` — hourly cron that fetches `kubectl get cm longhaul-report -o yaml`
  and posts a workflow summary; alerts if the report is stale or shows failures.

The driver runs as a `Deployment` (not a Job): if a process exits, it is restarted, and a
human reviews the `longhaul-report` ConfigMap rather than chasing ghost Jobs. The
DocumentDB cluster itself is never deleted by the driver — it is kept around for
post-mortem investigation when failures fire.

### Failure Tiers

| Tier | Example | Action |
|---|---|---|
| **Fatal** (stop) | Acknowledged write lost, checksum mismatch, cluster unrecoverable >10min | Artifact dump + preserve cluster + exit non-zero |
| **Degraded** (continue) | Operator pod restarted, write timeout during expected disruption | Log to journal, continue if recovery within budget |
| **Warning** (monitor) | Memory trending up, reconcile latency increasing | Log warning, no stop |

---

## Implementation Phases

| Phase | Scope | Status |
|---|---|---|
| **1a** | Project skeleton + config | ✅ Complete |
| **1b** | Data-plane workload (writers, oracle, verifiers) | ✅ Complete |
| **1c** | Event journal (disruption window tracking) | ✅ Complete |
| **1d** | Health monitor (steady-state detection, leak alerts) | ✅ Complete |
| **1e** | Scale operations + weighted-random scheduler | ✅ Complete |
| **1f** | `longhaul-report` ConfigMap reporter | ✅ Complete |
| **2a** | Backup & restore operations | 🔜 Planned |
| **2b** | HA primitives beyond what scale gives (controlled failover) | 🔜 Planned |
| **2c** | DocumentDB version upgrade operation | ✅ Complete |
| **2d** | Chaos operations (pod eviction, operator restart) | 🔜 Planned |
| **2e** | Failure tiers + auto-recovery refinements | Partial — current verdict logic uses `AllowedWriteFailures` + `MustRecoverWithin` |
| **2f** | In-cluster `Deployment` packaging (Dockerfile, RBAC, Helm-style manifests) | ✅ Complete |
| **2g** | Auto-deploy chain (`workflow_run` from image build → AKS rollout) | ✅ Complete |
| **2h** | Hourly monitor workflow + alerting | ✅ Complete |
| **3** | Two-cluster topology (Primary + Baseline) and multi-region canary | 🔜 Planned |

Each phase is a self-contained, demoable increment (~1-2 PRs).

---

## Learnings from Other Projects

| Project | Pattern We Adopt | Pattern We Skip |
|---|---|---|
| **Strimzi** | Run-until-failure loops; metrics collection | JUnit (we run a standalone Go binary, not a test framework) |
| **CloudNative-PG** | Failover via pod delete + SIGSTOP; metrics-server sampling | Ginkgo framework (we use a long-lived `Deployment` instead) |
| **CockroachDB** | Chaos runner; separate workload from disruption; roachstress | Custom roachtest framework (too heavy) |
| **Vitess** | Background stress goroutine; per-query tracking | No fault injection (we need disruptive ops) |

**Universal pattern:** Separate workload from disruptions, run concurrently, verify against acknowledged-write oracle, use per-operation disruption budgets.

---

## Open Questions

1. Which Kubernetes cluster for the persistent canary? (Any conformant cluster works)
2. Desired SLO targets (e.g., 99.9% write success during steady state)?
3. Multi-region canary scope (Phase 3) — AKS Fleet integration?


