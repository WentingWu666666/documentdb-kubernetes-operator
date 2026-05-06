# Long Haul Tests

Long haul tests validate that DocumentDB Kubernetes Operator clusters remain healthy under
continuous load over extended periods. They run a canary workload that writes and reads data,
performs management operations, and checks for data integrity.

> **Status:** Phase 1 complete. Canary workload (writers + verifiers), operation scheduler,
> health monitor, and in-cluster deployment are implemented with a placeholder cluster client.
> Phase 2 will add real Kubernetes operations. See [design document](../../docs/designs/long-haul-test-design.md).

## Project Structure

```
test/longhaul/
├── go.mod                # Separate Go module
├── README.md             # This file
├── Dockerfile            # Multi-stage build for in-cluster deployment
├── cmd/longhaul/
│   └── main.go           # Standalone binary entry point
├── deploy/
│   ├── setup.yaml        # Namespace + credentials + DocumentDB CR (bootstrap)
│   ├── job.yaml          # Kubernetes Job + ConfigMap manifest
│   └── rbac.yaml         # ServiceAccount + RBAC roles
├── config/
│   ├── config.go         # Config struct, env var loading, validation
│   └── config_test.go    # Config unit tests
├── workload/
│   ├── writer.go         # Continuous writers with sequence tracking
│   └── verifier.go       # Gap/checksum verification
├── monitor/
│   ├── health.go         # ClusterClient interface + health monitor
│   └── leakdetect.go     # Resource leak detector
├── operations/
│   ├── scale.go          # Scale up/down operations
│   └── scheduler.go      # Operation scheduling with cooldowns
├── journal/
│   └── journal.go        # Structured event log + disruption windows
└── report/
    └── checkpoint.go     # Periodic reporter (ConfigMap + stdout)
```

- **`test/longhaul/`** — The long-running canary. Designed to run for hours/days.
- **`test/longhaul/cmd/longhaul/`** — Standalone binary, deployed as a Kubernetes Job.
- **`test/longhaul/deploy/`** — Kubernetes manifests for in-cluster deployment.
- **`test/longhaul/config/`** — Config parsing and validation. Fast unit tests, safe for CI.

## Quick Start

### Prerequisites

- A running Kubernetes cluster with DocumentDB deployed
- `kubectl` configured to access the cluster
- Go 1.25+

### Run the Config Unit Tests

These are fast and require no cluster:

```bash
cd test/longhaul
go test ./config/ -v
```

### Run Locally

For development and validation against a port-forwarded cluster:

```bash
cd test/longhaul

LONGHAUL_MONGO_URI="mongodb://user:pass@localhost:10260/?directConnection=true&authMechanism=SCRAM-SHA-256&tls=true&tlsInsecure=true" \
LONGHAUL_CLUSTER_NAME=documentdb-cluster \
LONGHAUL_NAMESPACE=documentdb-test-ns \
LONGHAUL_MAX_DURATION=5m \
go run ./cmd/longhaul/
```

### Deploy as Kubernetes Job (Recommended for Real Runs)

This is the intended deployment model. The test runs inside the cluster with direct
access to the DocumentDB service (no port-forward needed).

```bash
cd test/longhaul

# 1. Build and push the container image
docker build -t <your-registry>/longhaul-test:latest -f Dockerfile .
docker push <your-registry>/longhaul-test:latest

# 2. Create the MongoDB credentials secret
kubectl create secret generic longhaul-mongo-credentials \
  --from-literal=uri='mongodb://docdb:YourPass@documentdb-service-documentdb-cluster.documentdb-test-ns.svc:10260/?directConnection=true&authMechanism=SCRAM-SHA-256&tls=true&tlsInsecure=true' \
  -n documentdb-test-ns

# 3. Deploy RBAC and Job
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/job.yaml

# 4. Monitor progress
kubectl logs -f job/longhaul-test -n documentdb-test-ns

# 5. Check result
kubectl get job longhaul-test -n documentdb-test-ns
```

To re-run, delete the old Job first:

```bash
kubectl delete job longhaul-test -n documentdb-test-ns
kubectl apply -f deploy/job.yaml
```

## Configuration

All configuration is via environment variables.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LONGHAUL_MONGO_URI` | Yes | — | MongoDB connection string to the DocumentDB gateway. |
| `LONGHAUL_CLUSTER_NAME` | Yes | — | Name of the target DocumentDB cluster CR. |
| `LONGHAUL_NAMESPACE` | No | `default` | Kubernetes namespace of the target cluster. |
| `LONGHAUL_MAX_DURATION` | No | `30m` | Max test duration. Use `0s` for run-until-failure. |
| `LONGHAUL_NUM_WRITERS` | No | `5` | Number of concurrent writers. |
| `LONGHAUL_NUM_VERIFIERS` | No | `2` | Number of concurrent verifiers. |
| `LONGHAUL_OP_COOLDOWN` | No | `5m` | Cooldown between management operations. |
| `LONGHAUL_RECOVERY_TIMEOUT` | No | `5m` | Max wait for cluster recovery after an operation. |
| `LONGHAUL_MIN_REPLICAS` | No | `1` | Minimum replicas for scale-down operations. |
| `LONGHAUL_MAX_REPLICAS` | No | `3` | Maximum replicas for scale-up operations. |
| `LONGHAUL_REPORT_INTERVAL` | No | `1h` | How often to write checkpoint reports to ConfigMap. |

## CI Safety

The long haul test binary is deployed as a Kubernetes Job on a dedicated AKS cluster.
It does **not** run in any PR-gated CI workflow.

The config unit tests (`test/longhaul/config/`) run unconditionally and are included in normal
CI test runs — they are fast (~0.002s) and require no cluster.
