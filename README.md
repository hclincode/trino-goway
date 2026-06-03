# trino-goway

A Go rewrite of [trino-gateway](https://github.com/trinodb/trino-gateway) — a reverse proxy and load balancer for one or more [Trino](https://trino.io) clusters.

See [docs/USE_STORIES.md](docs/USE_STORIES.md) for what the gateway does and the guarantees it provides.

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Building](#building)
- [Configuration](#configuration)
- [Running](#running)
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

---

## Configuration

Copy the annotated example and edit for your environment:

```bash
cp config.example.yaml config.yaml
$EDITOR config.yaml
```

See [config.example.yaml](config.example.yaml) for all fields with inline documentation. Minimum required fields for a single-cluster deployment:

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
| Admin API + health | 8090 |

Health endpoints (admin port):

```bash
curl http://localhost:8090/trino-gateway/livez   # always 200
curl http://localhost:8090/trino-gateway/readyz  # 200 after first monitor tick
```

> **Required Trino coordinator setting:** each Trino cluster behind the gateway must have
> `http-server.process-forwarded=true` in its `config.properties`. Without it the coordinator
> builds `nextUri` from its own bind address instead of the gateway's `X-Forwarded-*` headers,
> and all follow-up polls bypass the gateway — sticky routing silently breaks for every query.
> There is no error returned; queries simply stop routing through the gateway after the first
> response.

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
