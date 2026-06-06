# trino-goway

A Go rewrite of [trino-gateway](https://github.com/trinodb/trino-gateway) — a reverse proxy and load balancer for one or more [Trino](https://trino.io) clusters.

See [docs/USE_STORIES.md](docs/USE_STORIES.md) for what the gateway does and the guarantees it provides.

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Building](#building)
- [Configuration](#configuration)
- [Running](#running)
  - [Metrics](#metrics)
- [Development tools](#development-tools)
  - [Mock HTTP routing server](#mock-http-routing-server)
  - [Mock gRPC routing server](#mock-grpc-routing-server)
  - [Config migration tool](#config-migration-tool)
  - [Differential harness](#differential-harness)
- [Testing](#testing)
- [Database migrations](#database-migrations)

---

## Prerequisites

### Go

The project requires **Go 1.26.3** or later.

**Using [gvm](https://github.com/moovweb/gvm) (recommended):**

```bash
# Install gvm (if not already installed)
bash < <(curl -sSL https://raw.githubusercontent.com/moovweb/gvm/master/binscripts/gvm-installer)
source ~/.gvm/scripts/gvm

# Install and activate Go 1.26.3
gvm install go1.26.3
gvm use go1.26.3 --default
```

**Using the official installer:**

Download from [go.dev/dl](https://go.dev/dl/) and follow the platform instructions. Verify with:

```bash
go version
# go version go1.26.3 darwin/arm64
```

### Docker

Required only for integration tests (`//go:build integration`) and the differential harness (`//go:build diff`). Install [Docker Desktop](https://www.docker.com/products/docker-desktop/) or the Docker CLI for your platform.

### protoc (only if regenerating gRPC stubs)

The generated files (`internal/routing/routerpb/*.pb.go`) are committed. You only need `protoc` if you change `router.proto`.

```bash
# macOS
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

### grpcurl (optional — for manual gRPC testing)

[grpcurl](https://github.com/fullstorydev/grpcurl) lets you call the mock gRPC routing server from the command line without writing Go code.

```bash
# macOS
brew install grpcurl

# Linux (binary release)
GRPCURL_VERSION=1.9.1
curl -sSL "https://github.com/fullstorydev/grpcurl/releases/download/v${GRPCURL_VERSION}/grpcurl_${GRPCURL_VERSION}_linux_x86_64.tar.gz" \
  | tar -xz -C /usr/local/bin grpcurl

# Go install
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

Verify:

```bash
grpcurl --version
# grpcurl 1.9.1
```

### psql / mysql client (optional — for manual DB inspection)

Only needed if you want to query the gateway's Postgres or MySQL backend directly.

```bash
# macOS (Postgres)
brew install libpq
brew link --force libpq

# macOS (MySQL)
brew install mysql-client
```

---

## Building

```bash
# Build all binaries into the module root
go build ./cmd/...

# Build a specific binary
go build ./cmd/trino-goway
go build ./cmd/mock-external-router
go build ./cmd/mock-external-router-grpc
go build ./cmd/goway-migrate-config
go build ./cmd/goway-diff-harness
```

### Web UI bundle

The gateway embeds the React web UI (`webapp/`) via `//go:embed all:web/dist` in
`cmd/trino-goway/main.go`. The bundle is **not** committed; build it into the
embed directory before producing a release binary:

```bash
# Build the UI (pnpm + Vite, base path /trino-gateway/) and copy it into the
# Go embed directory, then build the gateway with the bundle baked in.
make build

# Or just rebuild the UI bundle:
make webapp

# Restore the embed dir to its placeholder state (UI not bundled):
make webapp-clean
```

A plain `go build ./cmd/trino-goway` without a prior `make webapp` still builds
and runs — the gateway serves a minimal placeholder shell until a real bundle is
embedded. The UI is served under `/trino-gateway` (SPA deep links fall back to
`index.html`); static assets are served from `/trino-gateway/assets/*`.

---

## Configuration

Copy the annotated example and edit for your environment:

```bash
cp configs/config.example.yaml config.yaml
$EDITOR config.yaml
```

See [configs/config.example.yaml](configs/config.example.yaml) for all fields with inline documentation. Minimum required fields for a single-cluster deployment:

```yaml
db:
  driver: postgres
  dsn: "host=localhost port=5432 dbname=trino_gateway user=gw password=secret sslmode=disable"

routing:
  defaultGroup: default

cookie:
  secret: "$(openssl rand -hex 32)"
```

---

## Running

```bash
./trino-goway --config config.yaml
```

Ports (defaults, overridable in config):

| Purpose | Default |
|---|---|
| Trino proxy | 8080 |
| Admin API + health + metrics | 8090 |

Health endpoints (admin port):

```bash
curl http://localhost:8090/trino-gateway/livez   # always 200
curl http://localhost:8090/trino-gateway/readyz  # 200 after first monitor tick
```

### Metrics

The gateway exposes Prometheus metrics in OpenMetrics format on the **admin port only**
(never the proxy port). The endpoint is enabled by default at `/metrics`:

```bash
curl http://localhost:8090/metrics
```

Configure it under the `metrics:` block (see [configs/config.example.yaml](configs/config.example.yaml)):

```yaml
metrics:
  enabled: true     # default true; set false to not register the route (404)
  path: /metrics    # default; must start with "/"
```

All gateway metrics use the `trino_goway_*` namespace and are served from a dedicated
registry (the process does not touch Prometheus' global default registry). Alongside them
the endpoint exposes the standard Go runtime (`go_*`) and process (`process_*`) collectors —
the Go-native equivalent of the Java gateway's JVM/process metrics. See
[docs/topics/gateway-docs-compatibility-audit.md §3.2](docs/topics/gateway-docs-compatibility-audit.md)
for the full Java→Go metric-name mapping.

Point Prometheus at the **admin** port. Because `/metrics` is unauthenticated (like the
health probes), keep the admin port off the public internet:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: trino-goway
    metrics_path: /metrics
    static_configs:
      - targets: ["trino-goway-host:8090"]   # admin port, not the proxy port
```

> **Note:** per-backend series (`trino_goway_backend_status`,
> `trino_goway_backend_activation_status`) appear once the monitor has observed a backend,
> which follows the gateway's backend-refresh cycle (~15s) after a backend is registered —
> not instantly on registration.

> **Required Trino coordinator setting:** each Trino cluster behind the gateway must have
> `http-server.process-forwarded=true` in its `config.properties`. Without it the coordinator
> builds `nextUri` from its own bind address instead of the gateway's `X-Forwarded-*` headers,
> and all follow-up polls bypass the gateway — sticky routing silently breaks for every query.
> There is no error returned; queries simply stop routing through the gateway after the first
> response.

### Live cluster stats

The gateway can report **live queued/running query counts** (plus worker-node count and a
per-user queued breakdown) per backend. The collector is selected by `clusterStats.monitorType`
and rides the existing health-monitor tick — there is no second scheduler. Four types:

| Type | Outbound HTTP | What it reports |
| --- | --- | --- |
| `INFO_API` *(default)* | none — reuses the `/v1/info` health verdict | `trinoStatus` only; counts 0 |
| `NOOP` | none | counts 0, status `UNKNOWN` |
| `UI_API` | Trino Web-UI API | live running/queued, `numWorkerNodes`, per-user `userQueuedCount` |
| `METRICS` | OpenMetrics endpoint | live running/queued + a threshold-gated status |

`JDBC` and `JMX` are not supported in v1 and are rejected at startup.

The default `INFO_API` issues **no extra outbound HTTP** and reports counts of 0 — byte-for-byte
identical to the Java gateway's default and to trino-goway's prior behavior. `UI_API` and
`METRICS` authenticate against each backend using the `backendState:` credentials and run on a
**dedicated 4th `*http.Client`** (`statsClient`), constructed only for those two types so the
default path keeps three HTTP pools (proxy / monitor / router). The `UI_API` session cookie jar
lives inside the collector, not on the shared transport.

Counts surface in two places, both fed from the same per-tick stats store:

- `GET /api/public/backends/{name}/state` returns the **M7 `ClusterStats` wire shape** —
  `{clusterId, runningQueryCount, queuedQueryCount, numWorkerNodes, trinoStatus, proxyTo,
  externalUrl, routingGroup, userQueuedCount}`. Under `INFO_API` the counts are 0 and the
  persistence-derived fields (`proxyTo`/`externalUrl`/`routingGroup`) are still populated.
- The web-UI `getAllBackends` table's queued/running columns.

See the `monitor:` stats knobs and the `clusterStats:` / `backendState:` blocks in
[configs/config.example.yaml](configs/config.example.yaml).

---

## Development tools

### Mock HTTP routing server

`cmd/mock-external-router` is a drop-in stub for the external routing service. It accepts any POST request, pretty-prints the request body to stdout, and returns a fixed routing group. Useful for seeing exactly what the gateway sends to your router.

```bash
go run ./cmd/mock-external-router --port 9000 --group default
```

Point the gateway at it:

```yaml
# config.yaml
routing:
  external:
    url: "http://localhost:9000/route"
```

Example output when the gateway routes a query:

```
2026-05-28T14:50:50Z  POST /route
{
  "method": "POST",
  "requestURI": "/v1/statement",
  "trinoRequestUser": {
    "user": "alice"
  },
  ...
}
```

### Mock gRPC routing server

`cmd/mock-external-router-grpc` implements the `TrinoGatewayRouter` gRPC service defined in `internal/routing/routerpb/router.proto`. Same behaviour as the HTTP mock: print every `RouteRequest` as indented JSON, return a fixed group.

```bash
go run ./cmd/mock-external-router-grpc --addr :9001 --group default
```

Point the gateway at it:

```yaml
# config.yaml
routing:
  external:
    grpcAddr: "localhost:9001"
```

**Introspect with grpcurl** (requires [grpcurl](#grpcurl-optional--for-manual-grpc-testing)):

```bash
# List available services (reflection is registered)
grpcurl -plaintext localhost:9001 list

# Describe the Route RPC
grpcurl -plaintext localhost:9001 describe trino.gateway.v1.TrinoGatewayRouter.Route

# Call Route manually
grpcurl -plaintext \
  -d '{"method":"POST","request_uri":"/v1/statement","trino_request_user":{"user":"alice"}}' \
  localhost:9001 trino.gateway.v1.TrinoGatewayRouter/Route
```

Expected response:

```json
{
  "routingGroup": "default"
}
```

### Config migration tool

Translates a Java trino-gateway `config.yaml` into the Go format:

```bash
go run ./cmd/goway-migrate-config \
  --input  /path/to/java-gateway-config.yaml \
  --output config.yaml
```

### Differential harness

`cmd/goway-diff-harness` runs YAML scenarios against the Java gateway and the Go gateway side-by-side and diffs the results.

```bash
# Replay recorded golden files (no Docker required)
go run ./cmd/goway-diff-harness replay \
  --scenarios cmd/goway-diff-harness/testdata/scenarios \
  --golden    cmd/goway-diff-harness/testdata/golden \
  --go-url    http://localhost:8080

# Record new golden files from a live Java gateway
go run ./cmd/goway-diff-harness record \
  --scenarios cmd/goway-diff-harness/testdata/scenarios \
  --golden    cmd/goway-diff-harness/testdata/golden \
  --java-url  http://localhost:8090

# Live side-by-side diff (requires Docker — boots Java gateway + Trino via testcontainers)
go test -tags=diff -v ./cmd/goway-diff-harness/

# Print a summary report from existing results
go run ./cmd/goway-diff-harness report \
  --golden cmd/goway-diff-harness/testdata/golden
```

---

## Testing

```bash
# Unit tests (no external dependencies)
go test ./...

# Race detector
go test -race ./...

# Integration tests (requires Docker)
go test -tags=integration ./...

# End-to-end tests — real Trino container (requires Docker)
go test -tags=e2e ./internal/e2e/...

# Differential harness — Java + Trino containers (requires Docker, slow ~60-90s)
go test -tags=diff ./cmd/goway-diff-harness/
```

Build tags summary:

| Tag | What it needs | Typical use |
|---|---|---|
| _(none)_ | nothing | per-PR CI, local dev |
| `integration` | Docker | DB layer tests |
| `e2e` | Docker | proxy E2E against real Trino |
| `diff` | Docker | Java↔Go differential harness |

### CI scheduling

The build tags are designed to split a fast per-PR gate from slower Docker-backed
suites that run on a schedule. Recommended wiring (no workflow files are committed
yet — this documents the intended split):

| Suite | Command | When it runs | Why |
|---|---|---|---|
| Unit + race | `go test -race ./...` | every push / PR | fast (no Docker), gates merge |
| Integration | `go test -tags=integration ./...` | every PR (Docker runner) | DB-layer tests; Postgres/MySQL containers |
| E2E | `go test -tags=e2e ./internal/e2e/...` | every PR (Docker runner) | proxy/auth/routing against a real Trino container |
| Differential | `go test -tags=diff -timeout 20m ./cmd/goway-diff-harness/` | **nightly** (scheduled) | boots Java gateway + Trino + Postgres via testcontainers (~60–90s first boot); too slow and image-heavy for per-PR |

Notes for the CI author:

- The `diff` job is **nightly**, not per-PR. It needs a Docker-enabled runner and
  outbound pulls for `trinodb/trino-gateway:19`, `trinodb/trino:476`, and
  `postgres:17-alpine` (pinned in `internal/diffharness/bootstrap.go`). The job is
  the only one that exercises the Java gateway, so it is where Java↔Go wire drift
  surfaces. Treat a non-PASS verdict as a release blocker, not a flake — investigate
  before re-running.
- The `diff` and `e2e` jobs both `t.Skip` cleanly when Docker is unavailable
  (`bootstrapOrSkip` / the e2e harness), so running them on a Docker-less runner is
  a no-op rather than a failure. CI must therefore assert the job actually ran
  (e.g. check for `--- PASS:` lines), not merely that the command exited 0.
- The diff harness validates its own scenario discipline at unit speed: the
  no-Docker `go test ./internal/diffharness/...` run includes
  `TestCommittedScenarios_LoadAndJustified`, which parses every scenario YAML and
  enforces a `[JUSTIFIED]` annotation on any file with diff-ignore entries. That
  guard runs in the per-PR gate, so normalizer drift is caught early even though the
  live diff only runs nightly. See the normalizer sign-off in
  `docs/studies/both/diff-normalizer-signoff.qa-tech-lead.md`.

### Linting

```bash
# Lint the main module (uses the root .golangci.yml)
golangci-lint run ./...

# Lint the routing-service module (separate Go module, own config)
cd routing-service && golangci-lint run ./...
```

The repo has two Go modules, each with its own golangci-lint config:

- **`.golangci.yml`** (root) governs the main module
  (`github.com/hclincode/trino-goway`).
- **`routing-service/.golangci.yml`** governs the routing-service module.

Both pin `version: "2"` and the same linter set (§D3 of
`docs/CODING_CONVENTIONS.md`): `errcheck`, `govet`, `staticcheck`, `bodyclose`,
`exhaustive`, `ineffassign`, `misspell`. The root config additionally enables the
built-in `std-error-handling` exclusion preset, which suppresses the unactionable
`defer resp.Body.Close()` / `fmt.Fprint*` error-return findings the gateway uses by
convention (in production and tests) — so the per-PR `golangci-lint run ./...` gate
is uniform instead of being hand-wrangled per file. `bodyclose` is excluded in
`_test.go` (response bodies on error-path assertions are intentionally not closed);
it still guards production code. The config is permissive: it was verified to
introduce zero new findings over the bare-default linter run.

---

## Database migrations

SQL migration files live in `migrations/`. Apply them with any standard migration tool (e.g. [golang-migrate](https://github.com/golang-migrate/migrate), [goose](https://github.com/pressly/goose), or plain `psql`/`mysql`):

```bash
# Example with psql
psql "$DSN" -f migrations/00001_create_gateway_backend.sql
psql "$DSN" -f migrations/00002_create_query_history.sql
```

Migration files:

| File | Purpose |
|---|---|
| `00001_create_gateway_backend.sql` | Backend registry table |
| `00002_create_query_history.sql` | Query history table for sticky routing |
