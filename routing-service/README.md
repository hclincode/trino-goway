# routing-service

A standalone Go gRPC service that implements the `TrinoGatewayRouter` contract for
[trino-goway](../README.md). It receives per-request metadata from the gateway, evaluates
routing logic (via `expr` expressions or Starlark scripts), and returns a routing group name.

## Gateway integration

Point trino-goway at this service with:

```yaml
# trino-goway config.yaml
routing:
  external:
    grpcAddr: "localhost:9001"
    timeout: "200ms"
```

The gateway falls back to `routing.defaultGroup` on any error or timeout — the routing-service
is never a hard dependency for query execution.

## Build

```sh
cd routing-service/
go build ./cmd/routing-service
```

Or via make:

```sh
make build
```

## Run

```sh
./routing-service --config config.yaml
```

Config file format: see `docs/config.example.yaml`.

## Health probe

```sh
grpcurl -plaintext localhost:9001 grpc.health.v1.Health/Check
```

Returns `SERVING` once the initial config is loaded and validated.

## Metrics

Prometheus metrics are exposed on a separate port (default `:9091`):

```sh
curl http://localhost:9091/metrics
```

## Proto regeneration

```sh
make proto
# or directly:
./proto/generate.sh
```

Requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on PATH.

## Authoring routing logic

- `expr` expressions: see `docs/expr-authoring.md`
- Starlark scripts: see `docs/starlark-authoring.md`
- Migrating from MVEL rules: see `docs/mvel-to-expr-migration.md`

## CLI test tools

```sh
# Test a Starlark script against a single input
make starlark-test
./bin/starlark-test routes.star '{"source":"airflow","is_new":true}'

# Test an expr program against a single input
make expr-test
./bin/expr-test routes.expr '{"source":"airflow","is_new":true}'
```

## Development

```sh
make all          # build + vet + lint + test
make test         # unit tests only (-race)
make test-integration  # integration tests (-tags=integration -race)
make lint         # golangci-lint
```

DoD gate: `go build ./... && go vet ./... && go test -race ./... && golangci-lint run ./...`
