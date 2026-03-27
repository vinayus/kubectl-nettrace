# kubectl-nettrace

A kubectl plugin that traces the network path between two Kubernetes workloads hop by hop.
Shows source health, NetworkPolicy evaluation, Service routing, EndpointSlice health, DNS requirements, and Gateway API routes.
No external dependencies. Works on any cluster, any CNI.

## Installation

### Manual

```bash
go install github.com/vinayus/kubectl-nettrace@latest
mv $(go env GOPATH)/bin/kubectl-nettrace /usr/local/bin/
```

### Build from source

```bash
git clone https://github.com/vinayus/kubectl-nettrace.git
cd kubectl-nettrace
go build -o kubectl-nettrace .
mv kubectl-nettrace /usr/local/bin/
```

## Usage

```bash
kubectl nettrace <source> <target> [flags]
```

### Supported workload types

| Type | Example |
|------|---------|
| `pod` | `pod/api-abc123` |
| `deploy` | `deploy/api` |
| `sts` | `sts/postgres` |
| `svc` | `svc/postgres` |

### Examples

```bash
# Same namespace
kubectl nettrace deploy/api svc/postgres -n production

# Pod to pod
kubectl nettrace pod/api-abc123 pod/postgres-0 -n production

# Deployment to StatefulSet
kubectl nettrace deploy/api sts/postgres -n production

# Cross-namespace
kubectl nettrace deploy/api sts/postgres --src-ns frontend --dst-ns data

# Port-specific NetworkPolicy evaluation
kubectl nettrace deploy/api svc/postgres -n production --port 5432
```

### Flags

| Flag | Description |
|------|-------------|
| `-n, --namespace` | Default namespace for both source and target (default: `default`) |
| `--src-ns` | Source namespace (overrides `-n`) |
| `--dst-ns` | Destination namespace (overrides `-n`) |
| `--port` | Port to evaluate for NetworkPolicy rules (default: any) |

### Output

```
Tracing: deploy/api [production] → svc/postgres [production]

HOP  TYPE             NAME                          STATUS
1    Deployment       api (3/3 ready)               ✓ 3/3 ready
2    Egress Policy    allow-db-egress               ✓ allowed
3    Service          postgres (ClusterIP)          ✓ ClusterIP: 10.96.0.10
4    EndpointSlice    postgres                      ~ 2 ready, 1 not ready
                        postgres-0 (10.0.2.8)       ✓ ready
                        postgres-1 (10.0.2.9)       ✓ ready
                        postgres-2 (10.0.2.10)      ✗ not ready
5    Ingress Policy   allow-app-ingress             ✓ allowed
6    Service          postgres (3/3 ready)          ✓ 3/3 ready

Result: ✓ Path clear (with warnings)
```

### Status symbols

| Symbol | Meaning |
|--------|---------|
| `✓` | Confirmed clear |
| `✗` | Blocked or failed |
| `~` | Informational |

### What it checks

1. **Source health** — pod readiness count for the workload
2. **Egress NetworkPolicy** — which policies select the source and whether they allow traffic to the target
3. **DNS** — flags cross-namespace traffic that requires FQDN instead of short service name
4. **Service** — type, ClusterIP, and `trafficDistribution` (zone/node-aware routing)
5. **EndpointSlice** — which backing pods are ready and which are not
6. **Gateway API** — detects HTTPRoute/GRPCRoute if Gateway API CRDs are installed
7. **Ingress NetworkPolicy** — which policies select the target and whether they allow traffic from the source
8. **Target health** — pod readiness count for the target workload

Exit code is `1` if any hop is blocked, `0` if path is clear.

## Requirements

- `kubectl` configured with a valid kubeconfig
- Go 1.21+ (to build from source)
- Kubernetes 1.29+ recommended (EndpointSlice, `trafficDistribution` GA)
