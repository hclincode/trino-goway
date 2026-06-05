# Conventions — routing-service

## Module path

`github.com/hclincode/trino-goway-routing-service`

Separate `go.mod` under `routing-service/`; independent of the parent `trino-goway` module.

## Stack (locked for Phase 1)

| Concern | Library | Notes |
|---|---|---|
| gRPC server | `google.golang.org/grpc` | Insecure transport (Phase 1); `GracefulStop` on shutdown |
| gRPC health | `google.golang.org/grpc/health/grpc_health_v1` | Part of the main grpc module; no separate module entry |
| Proto generated stubs | `google.golang.org/protobuf` | Generated via `protoc`; stubs committed under `routerpb/` |
| `expr` routing method | `github.com/expr-lang/expr` | Compile-at-load; `AsKind(reflect.String)`; no I/O |
| `script` routing method | `go.starlark.net` | `thread.SetMaxSteps(10_000)` + deadline cancel; structural sandbox |
| Config + hot-reload | `gopkg.in/yaml.v3` + `github.com/fsnotify/fsnotify` | Validate-before-activate; atomic swap |
| Metrics | `github.com/prometheus/client_golang` | Own `*prometheus.Registry`; no global default; `/metrics` on separate port |
| Tracing | `go.opentelemetry.io/otel` (+ `sdk`, `exporters/otlp/otlptrace/otlptracegrpc`) + `contrib/.../otelgrpc` | `otelgrpc.NewServerHandler` (stats handler; the interceptor form is deprecated); W3C parent-trace propagation; provider passed explicitly (no `otel.SetTracerProvider`) |
| Test leak detection | `go.uber.org/goleak` | `goleak.VerifyTestMain(m)` in every package with goroutines |

No new direct dependencies may be added without explicit team-lead approval. Adding a dependency requires updating this file and the go.mod in the same commit.

## Layout

```
routing-service/
├── cmd/
│   └── routing-service/   # main binary
├── docs/                  # PRD, TODO, CONVENTIONS, authoring guides
├── internal/
│   ├── config/            # Config struct, Load, Validate
│   ├── engine/            # RoutingMethod interface, registry, pipeline, RouteInput
│   │   └── providers/
│   │       ├── expr/      # ExprProvider
│   │       └── script/    # ScriptProvider (Starlark)
│   ├── integration/       # //go:build integration tests
│   ├── logging/           # DecisionLogger
│   ├── metrics/           # Prometheus registry + collectors
│   ├── reload/            # ConfigWatcher (fsnotify)
│   ├── server/            # gRPC Server, healthServer
│   └── tracing/           # OTel setup
├── proto/                 # Vendored router.proto + generate.sh
├── routerpb/              # Generated Go stubs (committed)
└── tools/
    ├── expr-test/         # expr-test CLI
    └── starlark-test/     # starlark-test CLI
```

## Pinned linter version

**golangci-lint `v2.12.2`** (built with Go 1.26.3).

Install the pinned version once after cloning (or when the pin changes):

```sh
make install-lint
# equivalent: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
```

The version is defined in the Makefile `GOLANGCI_LINT_VERSION` variable. Updating the pin
requires changing that variable, re-running `make install-lint`, and updating this document
in the same commit.

## DoD gate (every task must pass before marking done)

Run from `routing-service/`:

```sh
go build ./... && go vet ./... && go test -race ./... && golangci-lint run ./...
```

Integration tests (RS-12 and later):

```sh
go test -tags=integration -race -v ./internal/integration/...
```

Benchmarks (RS-4/RS-5 providers):

```sh
go test -bench=. -benchtime=5s -count=3 ./internal/engine/providers/...
```

## Proto compatibility policy

- The `.proto` file in `routing-service/proto/` is the stable wire contract.
- Additive changes (new optional fields, new reserved numbers) are backward-compatible.
- Removing or renumbering fields requires a `v2` package bump and explicit team-lead approval.
- The generated stubs in `routerpb/` are committed; regenerate via `make proto` or `./proto/generate.sh`.

## No global state

- No `init()` side-effects that register metrics or providers.
- All wiring is explicit in `cmd/routing-service/main.go`.
- Never use `prometheus.DefaultRegisterer` or `prometheus.DefaultGatherer`.
- Never use `otel.SetTracerProvider` global setter — pass the provider explicitly.

## Sandboxing discipline

- The `expr` provider's `buildEnv` must expose **only** `RouteInput` fields and pure helper functions (e.g. `hashPct`). No network, filesystem, or time functions.
- The `script` provider's Starlark universe must expose **only** `RouteInput` attrs and `hashPct`. Never register `open`, `file`, `os`, `load`, `http`, or any I/O builtins.
- `StarlarkRouteInput.Freeze()` must be called before passing to any Starlark thread.

## Decision log PII rule

- Never log raw SQL body text. Always log `sha256(body)[:8]` (8-hex-char prefix).
- Never log values from `param_map` that may contain passwords or tokens.
- Structured log fields: `rule_id`, `source`, `user`, `routing_group`, `latency_ms`, `config_version_hash`.
