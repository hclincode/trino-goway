# routing-service

A standalone Go gRPC service implementing the `TrinoGatewayRouter` contract for
[trino-goway](../README.md). The gateway sends per-request metadata; the service
evaluates ordered routing **methods** (`expr` expressions or Starlark `script`s)
and returns a routing group name. Routing is in-memory only — no DB or network
calls on the hot path.

## Ports

The service listens on three separate addresses (all configurable; defaults shown):

| Port | Purpose | Config key |
|---|---|---|
| `:9001` | gRPC data plane — `TrinoGatewayRouter/Route` + `grpc.health.v1.Health` | `addr` |
| `:9091` | Prometheus `/metrics` (HTTP) | `metricsAddr` |
| `:9092` | gRPC admin plane — `RoutingServiceAdmin` kill-switch | `adminAddr` |

## Gateway integration

```yaml
# trino-goway config.yaml
routing:
  external:
    grpcAddr: "localhost:9001"
    timeout: "200ms"
  defaultGroup: "default"
```

The gateway falls back to `routing.defaultGroup` on **any error or timeout** —
the routing-service is never a hard dependency for query execution. Within the
service, a method that defers or errors is skipped; if none decide, the
service-level `defaultRoutingGroup` is returned.

## Build & run

```sh
cd routing-service/
make build                       # or: go build ./cmd/routing-service
./routing-service --config config.yaml [--log-level info]
```

Config: see [`docs/config.example.yaml`](docs/config.example.yaml).

```sh
grpcurl -plaintext localhost:9001 grpc.health.v1.Health/Check   # SERVING once config loaded
curl http://localhost:9091/metrics
```

## Configuration

```yaml
addr: ":9001"
metricsAddr: ":9091"
adminAddr: ":9092"
tracingEndpoint: ""          # OTLP/gRPC endpoint; empty = tracing disabled
defaultRoutingGroup: "default"
methods:                     # evaluated in order; first non-empty group wins
  - type: expr
    program: |
      request.source == "airflow" ? "etl" : ""
  - type: script
    file: "routes.star"      # or inline: program: |
```

Each method sets exactly one of `program:` (inline) or `file:` (path).

## Authoring routing logic

- `expr` expressions — [`docs/expr-authoring.md`](docs/expr-authoring.md)
- Starlark `script`s — [`docs/starlark-authoring.md`](docs/starlark-authoring.md)
- Migrating from trino-gateway MVEL rules — [`docs/mvel-to-expr-migration.md`](docs/mvel-to-expr-migration.md)
- A non-Go router (polyglot escape hatch) — [`examples/python-router/`](examples/python-router/)

The `request.*` (expr) / `req.*` (Starlark) context exposes snake_case fields:
`source`, `client_tags`, `user`, `catalog`, `schema`, `method`, `uri`,
`remote_addr`, `body`, `is_new`, `param_map`. The `hashPct(s) -> 0..99` helper
gives deterministic canary buckets. Return `""` (expr) / `None` (Starlark) to
defer to the next method.

## CLI tools

Author and test rules with the same providers production uses:

```sh
make starlark-test expr-test
# single input (inline JSON or a .json path)
./bin/expr-test --program 'request.source == "airflow" ? "etl" : ""' '{"source":"airflow","is_new":true}'
./bin/starlark-test routes.star '{"source":"airflow","user":"alice","is_new":true}'
# batch CI mode with expectations
./bin/expr-test routes.expr --samples samples.yaml --expect expected.yaml
```

## Dry-run validation (CI gate)

`routing-service-validate` loads a config the exact way the running service does
(parse + compile every method) without serving:

```sh
go build -o bin/routing-service-validate ./cmd/routing-service-validate
./bin/routing-service-validate --config config.yaml                          # validity only
./bin/routing-service-validate --config config.yaml --samples samples.yaml   # routing table
./bin/routing-service-validate --config new.yaml --samples samples.yaml \
    --diff --baseline current.yaml                                           # diff vs baseline
```

Exit codes: **0** valid / no diff · **1** invalid config (parse/compile/validate)
· **2** `--diff` detected a changed routing outcome (use to block deploys on
unexpected route changes).

## Admin kill-switch

The `RoutingServiceAdmin` gRPC service (on `adminAddr`) disables/enables a method
at runtime — effective on the **next** request, no restart:

- `DisableMethod{type}` / `EnableMethod{type}` / `ListDisabled`

> **Security (Phase 1):** the admin plane has **no authentication**. Bind
> `adminAddr` to a firewalled, operator-only network. mTLS + a scoped credential
> are Phase 2.

## Observability

- **Metrics** (`/metrics`, own Prometheus registry — no global default):
  `routing_service_requests_total{source,routing_group,method_type,outcome}`
  (outcome ∈ `decided|fallback|error`), `routing_service_fallback_total`,
  `routing_service_decision_duration_seconds`,
  `routing_service_config_reload_total{result}`,
  `routing_service_config_version{hash}`,
  `routing_service_method_disabled{type}`.
- **Decision logs**: structured, sampled ~10% steady-state / 100% on fallback;
  raw SQL is never logged — only `sha256(body)[:8]`.
- **Tracing**: an OTel span per `Route` (`otelgrpc` stats handler propagates the
  gateway's parent trace); enabled by setting `tracingEndpoint` to an OTLP/gRPC
  collector.

## Hot reload

The config file is watched (fsnotify, 100ms debounce). On change the new method
set is built and **validated before activation** (atomic swap); an invalid
change keeps the last-known-good config live and records an error metric + audit
event.

## Proto regeneration

```sh
make proto        # or: ./proto/generate.sh
```

Requires `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` on PATH. Generated stubs
are committed under `routerpb/`. The `.proto` files (`router.proto`,
`admin.proto`) are the stable wire contract.

## Development

```sh
make all                # build + vet + lint + test
make test               # unit tests (-race)
make test-integration   # integration tests (-tags=integration -race)
make lint               # golangci-lint (pinned; see docs/CONVENTIONS.md)
```

DoD gate: `go build ./... && go vet ./... && go test -race ./... && golangci-lint run ./...`
