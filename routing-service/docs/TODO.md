# TODO — routing-service

**PRD:** `routing-service/docs/PRD.md`
**Contract:** `internal/routing/routerpb/router.proto` (`trino.gateway.v1`)
**Gateway integration:** `internal/routing/external_grpc.go`, `internal/config/config.go` §ExternalConfig

Critical path: **RS-1 → RS-2 → RS-3 → RS-4 → RS-5 → RS-9**
Off critical path (start after RS-2): RS-6, RS-7, RS-8
Off critical path (start after RS-3): RS-10, RS-11, RS-12

---

## Phase 0: Repo scaffold + proto vendor

### Task RS-1 — Module scaffold + vendored proto

- [ ] `routing-service/go.mod` — `module github.com/hclincode/trino-goway/routing-service`, `go 1.23`, initial deps: `google.golang.org/grpc`, `google.golang.org/protobuf`, `google.golang.org/grpc/health`, `github.com/expr-lang/expr`, `go.starlark.net`, `github.com/prometheus/client_golang`, `go.opentelemetry.io/otel`, `gopkg.in/yaml.v3`, `github.com/fsnotify/fsnotify`
- [ ] `routing-service/go.sum` generated via `go mod tidy`
- [ ] `routing-service/proto/router.proto` — **vendor copy** of `internal/routing/routerpb/router.proto`; update `option go_package` to `github.com/hclincode/trino-goway/routing-service/routerpb`; add **additive** Phase 1 required fields (PRD §4.1):
  - `string trino_source = 12;` on `RouteRequest` — from `X-Trino-Source`
  - `repeated string client_tags = 13;` on `RouteRequest` — from `X-Trino-Client-Tags`, pre-split on comma by gateway
  - Reserve field numbers 14–20 on `RouteRequest` and 4–10 on `RouteResponse` for future additions (comment: `// reserved for future use`)
- [ ] `routing-service/proto/Makefile` (or `buf.gen.yaml`) — `protoc` invocation generating Go stubs into `routing-service/routerpb/`
- [ ] `routing-service/routerpb/` — generated `router.pb.go` + `router_grpc.pb.go`; committed as generated artifacts
- [ ] `routing-service/Makefile` — top-level convenience targets:
  - `make build` — `go build ./...`
  - `make test` — `go test -race ./...`
  - `make test-integration` — `go test -tags=integration -race ./internal/integration/...`
  - `make vet` — `go vet ./...`
  - `make lint` — `golangci-lint run ./...`
  - `make proto` — run the `protoc` invocation in `proto/`
  - `make all` — `build vet lint test` in order
  - `make starlark-test` / `make expr-test` — build the CLI tools to `bin/`
- [ ] `routing-service/.golangci.yml` — lint config: `errcheck`, `govet`, `staticcheck`, `exhaustive`, `bodyclose`; mirrors the parent repo's lint profile
- [ ] `routing-service/docs/CONVENTIONS.md` — documents:
  - **Stack:** Go 1.23, `google.golang.org/grpc` (insecure Phase 1), `google.golang.org/grpc/health`, `github.com/expr-lang/expr`, `go.starlark.net`, `github.com/prometheus/client_golang` (own registry, no global), `go.opentelemetry.io/otel`, `gopkg.in/yaml.v3`, `github.com/fsnotify/fsnotify`
  - **Layout:** `cmd/` for binaries, `internal/` for packages, `proto/` for the vendored `.proto` + `protoc` tooling, `routerpb/` for generated stubs, `docs/` for PRD/TODO/authoring guides
  - **DoD gate (every task):** `go build ./... && go vet ./... && go test -race ./... && golangci-lint run ./...` all green from `routing-service/`; integration tests: `go test -tags=integration -race ./internal/integration/...`
  - **Proto compat policy:** additive field additions (new optional fields) are backward-compatible; removing or renumbering fields is a breaking change requiring a `v2` package; the `.proto` is the stable wire contract between `routing-service` and trino-goway
  - **No global state:** no `init()` side effects that register metrics/providers; all wiring is explicit in `main.go`; no `prometheus.DefaultRegisterer`
  - **Sandboxing discipline:** no I/O functions registered in `expr` env or Starlark universe; `buildEnv` / `StarlarkRouteInput` expose only the `RouteInput` fields plus pure helper functions
  - **Decision log PII rule:** never log raw SQL body; always `sha256(body)[:8]` prefix; never log passwords/tokens from `parameter_map`
- [ ] `routing-service/README.md` — brief: purpose, `routing.external.grpcAddr` integration point, build/run, `expr` + Starlark authoring pointer
- [ ] `go vet ./...` from `routing-service/` passes
- [ ] **DoD:** `go build ./...` + `go vet ./...` + `golangci-lint run ./...` pass from `routing-service/`; generated proto stubs compile against the module; `make all` exits 0

---

## Phase 1: gRPC server + health

### Task RS-2 — gRPC server skeleton + health protocol

Implements the `TrinoGatewayRouter` service wire and `grpc.health.v1.Health`. No routing logic yet — all `Route` calls return `default_routing_group` from config. This is the first integration point with trino-goway.

- [ ] `routing-service/internal/server/server.go` — `Server` struct
  - `New(cfg *config.Config, log *slog.Logger) *Server`
  - `Start(ctx context.Context) error` — `grpc.NewServer` (insecure, Phase 1 matches `insecure.NewCredentials()` in the gateway); register `TrinoGatewayRouter` + `grpc.health.v1.Health`; `net.Listen("tcp", cfg.Addr)`; serve in goroutine; block until `ctx` done
  - `Stop()` — `grpcServer.GracefulStop()` (drain in-flight RPCs before exit); never `Stop()` (hard-kills)
  - `grpc.UnaryInterceptor` chain: recovery (panic→error), OTel trace propagation, metrics recording (pre-wired, no-op until Task RS-9)
- [ ] `routing-service/internal/server/server.go` — `Route(ctx, *RouteRequest) (*RouteResponse, error)` stub:
  - Return `&RouteResponse{RoutingGroup: cfg.DefaultRoutingGroup}` always
  - Log `req.GetTrinoRequestUser().GetUser()`, `req.GetTrinoSource()`, `req.IsNewQuerySubmission()` at DEBUG
  - If `!req.GetTrinoQueryProperties().GetIsNewQuerySubmission()`: return `RouteResponse{}` immediately (empty = gateway default; service must not decide on non-new submissions — PRD §3)
- [ ] `routing-service/internal/server/health.go` — `healthServer` implementing `grpc.health.v1.HealthServer`
  - `Check`: returns `SERVING` when `engine.Ready()` is true, `NOT_SERVING` otherwise
  - `Watch`: basic streaming implementation (send current status; re-send on status change via channel)
  - `engine.Ready()` is injected — false until the routing engine loads its first valid config (Task RS-3)
- [ ] `routing-service/internal/config/config.go` — `Config` struct:
  ```
  Addr               string        // gRPC listen addr, default ":9001"
  DefaultRoutingGroup string       // fallback group; must be non-empty
  Methods            []MethodConfig // ordered provider configs
  ```
  `MethodConfig`: `Type string`, `Refresh Duration`, `Program string` (inline), `File string` (path); union — only one of Program/File non-empty
  `Load(path string) (*Config, error)` via `gopkg.in/yaml.v3`; `Validate()` — addr non-empty, defaultRoutingGroup non-empty, each method has exactly one of Program/File
- [ ] `routing-service/internal/config/config_test.go` — table-driven YAML round-trips; validation errors
- [ ] `routing-service/cmd/routing-service/main.go` — flags: `--config` (path, required), `--log-level`; compose `Config` + `Server`; SIGTERM/SIGINT → `Stop()` with 30 s deadline; startup log: addr, default group, method count
- [ ] `routing-service/internal/server/server_test.go` — in-process server (`bufconn`): `Route` returns default group; health returns `NOT_SERVING` before ready, `SERVING` after `engine.SetReady(true)`; `GracefulStop` drains an in-flight RPC before returning
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `go build ./cmd/routing-service` produces a static binary; gateway configured with `routing.external.grpcAddr: localhost:9001` routes to `default_routing_group`; `grpcurl -plaintext localhost:9001 grpc.health.v1.Health/Check` returns `SERVING`

---

## Phase 2: Provider interface + registry + pipeline

### Task RS-3 — `RoutingMethod` interface + registry + ordered pipeline

Implements the extensibility core. No method logic yet — establishes the interface that every provider implements and the pipeline the `Route` RPC drives.

- [ ] `routing-service/internal/engine/method.go` — `RoutingMethod` interface (from PRD §6.1):
  ```go
  type Decision struct {
      RoutingGroup    string
      ExternalHeaders map[string]string
      Errors          []string
      Decided         bool  // false = no opinion, continue pipeline
  }

  type RouteInput struct {
      Source      string
      ClientTags  []string
      User        string
      Catalog     string
      Schema      string
      Method      string
      URI         string
      RemoteAddr  string
      Body        string
      IsNew       bool
      ParamMap    map[string]string
  }

  type RoutingMethod interface {
      Type() string
      LoadConfig(raw []byte) error  // parse + compile/validate; activated only if valid
      Evaluate(ctx context.Context, in *RouteInput) (Decision, error)
  }
  ```
- [ ] `routing-service/internal/engine/registry.go` — `Registry`: `Register(typeName string, factory func() RoutingMethod)`; `Build(cfg MethodConfig) (RoutingMethod, error)` — looks up factory, calls `LoadConfig`; panics at init if a duplicate type is registered (fail-loud on misconfiguration)
- [ ] `routing-service/internal/engine/pipeline.go` — `Pipeline` struct:
  - `New(methods []RoutingMethod, defaultGroup string) *Pipeline`
  - `Evaluate(ctx context.Context, in *RouteInput) (*Decision, error)` — iterate methods in order; first `Decision.Decided=true` wins; if none decide, return `Decision{RoutingGroup: defaultGroup, Decided: false}`; any method `Evaluate` error → log warn + skip that method (never propagate as gRPC error)
  - `Ready() bool` — true once at least one method is loaded or the pipeline has zero methods (pure-default mode)
- [ ] `routing-service/internal/engine/input.go` — `FromProto(req *routerpb.RouteRequest) *RouteInput` — maps proto fields to `RouteInput`; `ClientTags` from `req.ClientTags` (pre-split by gateway); `Source` from `req.TrinoSource`; handles nil `TrinoQueryProperties` / `TrinoRequestUser` safely
- [ ] `routing-service/internal/engine/pipeline_test.go` — table-driven: first-decides wins; skip-on-error; all-defer returns default; nil methods list (pure-default); `Ready()` transitions
- [ ] Wire `Pipeline.Evaluate` into `server.Route` (replace the stub from Task RS-2); pass `engine.Ready()` to `healthServer`
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** pipeline unit tests green; `Route` RPC now drives the method chain; gateway can be pointed at the service and routed deterministically

---

## Phase 3: Method providers

### Task RS-4 — `expr` provider (expr-lang/expr)

- [ ] `routing-service/internal/engine/providers/expr/provider.go` — `ExprProvider` struct implementing `RoutingMethod`
  - `Type() string` → `"expr"`
  - `LoadConfig(raw []byte)` — parse YAML `{program: "..."}` or `{file: "..."}` (load file content); compile via `expr.Compile(program, expr.Env(routeEnvType))` + `expr.AsKind(reflect.String)` (ensure program returns a string); store compiled `*vm.Program` atomically; return error without activating if compilation fails
  - `Evaluate(ctx, in)` — `expr.Run(prog, buildEnv(in))`; result string: non-empty → `Decision{RoutingGroup: result, Decided: true}`; empty string → `Decision{Decided: false}`; any `expr.Run` panic/error → `Decision{Decided: false}` + log warn
  - `buildEnv(in *RouteInput) map[string]any` — expose: `request` struct with fields `source`, `client_tags`, `user`, `catalog`, `schema`, `method`, `uri`, `remote_addr`, `body`, `is_new`; plus `hashPct` as a registered function: `hashPct(s string) int` — FNV-1a hash of `s` modulo 100, deterministic (for canary splits)
  - No I/O, no goroutines, no network in `buildEnv`; only pure functions registered
- [ ] `routing-service/internal/engine/providers/expr/provider_test.go` — table-driven:
  - `source == "airflow" ? "etl" : ""` routes airflow to etl, others defer
  - `"tier=premium" in client_tags ? "premium" : ""` tag matching
  - `hashPct(user) < 5 ? "canary" : "prod"` deterministic split (assert same user always same bucket)
  - Invalid program → `LoadConfig` returns error, old program still serves
  - Runtime panic in expr → `Decided: false`, no crash
- [ ] Register `ExprProvider` in `routing-service/cmd/routing-service/main.go` init block: `registry.Register("expr", func() engine.RoutingMethod { return expr.New() })`
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `expr` method routes correctly per tests; `LoadConfig` errors leave old program live

### Task RS-5 — `script` provider (Starlark)

- [ ] `routing-service/internal/engine/providers/script/provider.go` — `ScriptProvider` struct implementing `RoutingMethod`
  - `Type() string` → `"script"`
  - `LoadConfig(raw []byte)` — parse YAML `{file: "..."}` or `{program: "..."}`; parse + compile Starlark source via `starlark.FileProgram` / `starlark.ExecFile` in a scratch thread; verify the compiled program exports a `route` function accepting one argument; store compiled `*starlark.Program` atomically (swap on success only)
  - `Evaluate(ctx, in)` — create a `*starlark.Thread` with `thread.SetMaxSteps(10_000)` (CPU step cap); start a goroutine that calls `thread.Cancel("deadline")` when `ctx.Done()` fires; call the `route` function with a `StarlarkRouteInput` struct value built from `in`; result: `starlark.String` non-empty → `Decided: true`; `starlark.None` or empty string → `Decided: false`; any error (EvalError, step limit, deadline cancel) → `Decided: false` + log warn (never propagate)
  - `StarlarkRouteInput` — `starlark.Value` implementing `starlark.HasAttrs`: exposes read-only attrs `source`, `client_tags` (Starlark list of strings), `user`, `catalog`, `schema`, `method`, `uri`, `remote_addr`, `body`, `is_new`; `Freeze()` is a no-op (already immutable); no I/O methods exposed
  - Predeclared names injected into the Starlark universe: `hashPct` (same semantics as expr provider — FNV-1a mod 100, deterministic)
  - Never expose: `file`, `open`, any `os.*`, any network primitives; the sandbox is structural (no stdlib; only explicit predeclared names)
- [ ] `routing-service/internal/engine/providers/script/provider_test.go` — table-driven:
  - `def route(req): return "etl" if req.source == "airflow" else None` routes airflow, defers others
  - `def route(req): return "canary" if hashPct(req.user) < 5 else "prod"` — deterministic bucket
  - Infinite loop `def route(req): [x for x in range(10**9)]` — `SetMaxSteps` fires, returns `Decided: false` within < 5 ms
  - Script with syntax error → `LoadConfig` returns error, old script still serves
  - Script `return None` → `Decided: false` (not an error)
  - Script `return ""` → `Decided: false`
  - Script runtime error (`1/0`) → `Decided: false`, no crash
- [ ] Register `ScriptProvider` in `main.go` init: `registry.Register("script", func() engine.RoutingMethod { return script.New() })`
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** Starlark provider routes correctly; step-limit test proves CPU-bound scripts cannot hang the RPC; sandboxing test confirms no stdlib escape

---

## Phase 4: Harness guardrails

### Task RS-6 — Hot-reload + validate-before-activate

Depends on RS-3 (pipeline). Can start after RS-3.

- [ ] `routing-service/internal/reload/watcher.go` — `ConfigWatcher` struct
  - `New(path string, pipeline *engine.Pipeline, registry *engine.Registry, log *slog.Logger) *ConfigWatcher`
  - `Start(ctx context.Context)` — `fsnotify.NewWatcher`; watch the config file (and all `file:` script paths referenced in the current config); on `fsnotify.Write` or `fsnotify.Create`: call `reload()` in a goroutine; debounce 100 ms (discard bursts)
  - `reload()`:
    1. Parse + validate the new config via `config.Load`
    2. For each method: call `RoutingMethod.LoadConfig` with the method's raw config bytes
    3. If any step fails: log error with diff summary (old config hash vs new), increment `config_reload_errors_total`, emit structured audit event `{trigger: "file_change", result: "error", diff: ...}`, **keep the current pipeline live** (last-known-good)
    4. If all succeed: atomically swap the pipeline's method slice; increment `config_reload_success_total`; emit audit event `{result: "ok", new_hash: ...}`
  - `Stop()` — close the fsnotify watcher
- [ ] `routing-service/internal/reload/watcher_test.go` — write a valid config file; assert initial load; overwrite with invalid config; assert old pipeline still serves; overwrite with valid config; assert new pipeline activates; goleak clean
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** file change activates new config; invalid file never disrupts live traffic; audit events emitted

### Task RS-7 — Dry-run CLI tool (`routing-service-validate`)

Depends on RS-3, RS-4, RS-5. Can start after RS-5.

- [ ] `routing-service/cmd/routing-service-validate/main.go` — standalone CLI
  - Flags: `--config <path>` (required), `--samples <path>` (optional; YAML file of sample `RouteInput` records), `--diff` (compare against a baseline config)
  - Without `--samples`: parse + compile the config; print `OK` or validation errors; exit 0/1
  - With `--samples`: load samples; run pipeline against each; print table: `sample_id | input_summary | new_group | (old_group if --diff)`; highlight rows where new ≠ old
  - Exit 0 if config valid; exit 1 on any compile/validation error; exit 2 if `--diff` shows routing changes (allows CI to gate on unexpected route changes)
- [ ] `routing-service/cmd/routing-service-validate/validate_test.go` — valid config exits 0; invalid exits 1; sample diff detected
- [ ] `go build ./cmd/routing-service-validate` passes
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `routing-service-validate --config routes.yaml --samples samples.yaml` prints routing table; CI can block deploys on unexpected changes

### Task RS-8 — Kill-switch + method-level disable

Depends on RS-3 (pipeline). Can start after RS-3.

- [ ] `routing-service/internal/engine/pipeline.go` — extend `Pipeline`:
  - `DisableMethod(typeName string)` — atomically mark the named method as disabled; `Evaluate` skips disabled methods; takes effect on the next request (sub-second propagation — no restart required)
  - `EnableMethod(typeName string)` — re-enable; config + compiled program already resident
  - `DisabledMethods() []string` — introspection
- [ ] `routing-service/internal/server/server.go` — expose a `DisableMethod`/`EnableMethod` gRPC admin method (unary, admin-only placeholder; no auth in Phase 1 — document as "must be firewalled; mTLS required in Phase 2"):
  - `rpc DisableMethod(DisableMethodRequest) returns (DisableMethodResponse)` — added to a new `RoutingServiceAdmin` service in `router.proto` (separate service, separate registration)
  - `DisableMethodRequest { string type = 1; }`, `DisableMethodResponse { bool ok = 1; string message = 2; }`
- [ ] `routing-service/internal/engine/pipeline_test.go` — extend: disable `expr`; pipeline falls through to `script`; re-enable; `expr` decides again; assert sub-millisecond propagation (no sleep needed — atomic)
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `DisableMethod("script")` over gRPC stops the Starlark provider without restart; `EnableMethod` restores it

---

## Phase 5: Observability

### Task RS-9 — Prometheus metrics + structured decision logs + OTel tracing

Depends on RS-2 (server), RS-3 (pipeline). Can be partially started after RS-2.

- [ ] `routing-service/internal/metrics/metrics.go` — own `*prometheus.Registry` (no global):
  - `routing_service_requests_total{source, routing_group, method_type, outcome}` — `outcome` ∈ `decided|deferred|error|fallback`
  - `routing_service_decision_duration_seconds` — histogram (label `method_type`); target p99 ≤ 1 ms for in-memory eval
  - `routing_service_fallback_total` — counter; alert threshold: `> 1%` of requests over 5 m window (PRD §7)
  - `routing_service_config_reload_total{result}` — `result` ∈ `ok|error`
  - `routing_service_config_version` — gauge with label `hash` (active config content hash)
  - `routing_service_method_disabled{type}` — gauge 1 if disabled, 0 if enabled
  - Expose on a `/metrics` HTTP endpoint on a separate port (`cfg.MetricsAddr`, default `:9091`); `promhttp.HandlerFor(reg, ...)` with `EnableOpenMetrics: true`
- [ ] `routing-service/internal/logging/decision.go` — `DecisionLogger`:
  - Log each `Route` call at DEBUG; sample at ~10% at INFO steady-state; always log at INFO on fallback (PRD §7)
  - Log fields: `rule_id` (method type that decided), `input_attributes` (source, user — **never raw body/SQL**; body → `sha256(body)[:8]` prefix only), `routing_group`, `latency_ms`, `config_version_hash`
  - `DecisionLogger.ShouldLog(isFallback bool) bool` — 10% sample rate + always-on for fallback
- [ ] `routing-service/internal/tracing/tracing.go` — OTel setup:
  - `Init(cfg TracingConfig) (*trace.TracerProvider, error)` — OTLP exporter (endpoint configurable; disabled if empty); resource with `service.name=routing-service`
  - In `server.Route`: `tracer.Start(ctx, "TrinoGatewayRouter/Route")`; propagate incoming gRPC trace context via `otelgrpc.UnaryServerInterceptor`; add span attrs: `routing.group`, `routing.source`, `routing.method_type`
- [ ] `routing-service/internal/metrics/metrics_test.go` — after N `Route` calls: counters match; histogram observed; fallback counter increments on method skip
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `curl :9091/metrics` returns OpenMetrics text with all named families; decision logs at INFO on fallback; `grpcurl` trace propagates span to collector

---

## Phase 6: CLI test tools

### Task RS-10 — `starlark-test` CLI tool

Standalone tool to test a Starlark routing script against a set of sample inputs without running the full service. Useful for script authoring, CI validation, and pre-deploy dry-run.

Depends on RS-5 (Starlark provider).

- [ ] `routing-service/tools/starlark-test/main.go` — flags:
  - `--script <path>` (required) — `.star` file to test
  - `--samples <path>` (required) — YAML file of sample `RouteInput` records (same schema as `routing-service-validate` samples)
  - `--max-steps <n>` (default `10000`) — override the step budget for this test run
  - `--verbose` — print full input + decision per sample; without it, print only mismatches or errors
  - `--expect <path>` (optional) — YAML of `{sample_id: expected_group}` expectations; exit non-zero if any expectation fails
- [ ] Sample YAML schema (`RouteInput` record):
  ```yaml
  - id: airflow-etl
    source: airflow
    user: pipeline@acme.com
    client_tags: []
    catalog: hive
    is_new: true
  - id: superset-interactive
    source: superset
    user: alice@acme.com
    is_new: true
  ```
- [ ] Output format (tabular):
  ```
  SAMPLE                  GROUP           LATENCY   STATUS
  airflow-etl             etl             0.12ms    OK
  superset-interactive    interactive     0.09ms    OK
  (no-match)              (deferred)      0.08ms    OK
  ```
  On step-limit hit: status = `STEP_LIMIT`; on script error: status = `ERROR: <msg>`
- [ ] `routing-service/tools/starlark-test/main_test.go` — run a valid script against 3 samples; assert output; run a step-limit-tripping script; assert `STEP_LIMIT` in output and non-zero exit; run with `--expect` mismatch; assert non-zero exit
- [ ] `go build ./tools/starlark-test` produces a static binary
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `starlark-test --script routes.star --samples samples.yaml --expect expected.yaml` exits 0 on match; exits non-zero on any expectation miss or script error; usable in CI without a running service

### Task RS-11 — `expr-test` CLI tool

Standalone tool to test an `expr-lang` routing program against sample inputs. Mirror of `starlark-test` but for the `expr` provider.

Depends on RS-4 (expr provider).

- [ ] `routing-service/tools/expr-test/main.go` — flags:
  - `--program <string>` — inline expr program (mutually exclusive with `--file`)
  - `--file <path>` — file containing the expr program
  - `--samples <path>` (required) — same YAML schema as `starlark-test`
  - `--verbose`, `--expect <path>` — same semantics as `starlark-test`
- [ ] Output format — same tabular layout as `starlark-test`; status `COMPILE_ERROR` if program fails to compile; `RUNTIME_ERROR: <msg>` if `expr.Run` errors; `DEFERRED` if program returns `""`
- [ ] `routing-service/tools/expr-test/main_test.go` — inline program `source == "airflow" ? "etl" : ""` against samples; assert `etl` for airflow source, `DEFERRED` for others; invalid program → `COMPILE_ERROR` + non-zero exit; `--expect` mismatch → non-zero exit
- [ ] `go build ./tools/expr-test` produces a static binary
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** `expr-test --program '...' --samples samples.yaml` reports group or `DEFERRED` per sample; CI-friendly exit codes

---

## Phase 7: Integration test + docs

### Task RS-12 — Integration test: gateway ↔ routing-service round-trip

An in-process integration test that dials the real routing-service binary (or starts it in-process via `bufconn`) and verifies the full `Route` RPC contract from the gateway's perspective.

Depends on RS-2, RS-3, RS-4, RS-5.

- [ ] `routing-service/internal/integration/roundtrip_test.go` — `//go:build integration`
  - Start routing-service in-process (`bufconn`) with a test config containing one `expr` method and one `script` method
  - Construct a `RouteRequest` (same as `buildProtoRequest` in `internal/routing/external_grpc.go`):
    - `is_new_query_submission: true`, `trino_source: "airflow"`, `trino_request_user.user: "pipeline@acme.com"`
  - Assert `RouteResponse.routing_group == "etl"` (from the expr method)
  - Send a non-new request (`is_new_query_submission: false`): assert `routing_group == ""` (service defers immediately)
  - Send a request that matches no rule: assert `routing_group == "default"` (pipeline default)
  - Kill the `script` method via `DisableMethod("script")`; send request that would match the script; assert `expr` still decides (or default if neither method matches)
  - `grpc.health.v1.Health/Check` returns `SERVING` after pipeline ready; returns `NOT_SERVING` before first config load
  - `goleak.VerifyTestMain`
- [ ] `routing-service/internal/integration/roundtrip_test.go` — verify `trino_source` + `client_tags` fields round-trip from proto → `RouteInput` correctly (PRD §4.1 field contract)
- [ ] `routing-service/internal/integration/roundtrip_test.go` — verify metrics: after 10 Route calls, `routing_service_requests_total` counter == 10; at least 1 `fallback_total` when all methods skip
- [ ] `go test -tags=integration -race ./internal/integration/...` passes
- [ ] `go vet ./...` + `golangci-lint run ./...` pass
- [ ] **DoD:** full `Route` RPC contract verified; `trino_source`/`client_tags` fields verified; health lifecycle verified; metrics verified; race detector clean

### Task RS-13 — Docs + config example + MVEL→expr migration guide

- [ ] `routing-service/README.md` — complete: purpose, build (`go build ./cmd/routing-service`), run (`./routing-service --config config.yaml`), gateway config (`routing.external.grpcAddr: host:9001`), health probe, metrics scrape, build tags for integration tests
- [ ] `routing-service/docs/config.example.yaml` — annotated example covering `addr`, `default_routing_group`, one `expr` method, one `script` method, canary split with `hashPct`, `metrics_addr`
- [ ] `routing-service/docs/expr-authoring.md` — `expr` language reference for routing: available `request.*` fields, `hashPct`, `hasSuffix`, `split`, return conventions (`"" = defer`), error handling, step-limit note (bounded by construction — no explicit limit needed for `expr`)
- [ ] `routing-service/docs/starlark-authoring.md` — Starlark language reference for routing: available `req.*` attrs, `hashPct`, `None = defer`, `thread.SetMaxSteps` note (implicit via harness — operator does not set it), no I/O, freeze semantics, error handling
- [ ] `routing-service/docs/mvel-to-expr-migration.md` — MVEL→expr mapping table (PRD §5 reference):
  - `request.getHeader("X-Trino-Source") == "airflow"` → `request.source == "airflow"`
  - `request.getHeader("X-Trino-Client-Tags").contains("tier=premium")` → `"tier=premium" in request.client_tags`
  - `request.getHeader("X-Trino-User")` → `request.user`
  - `result.put("routingGroup", "etl")` → return value `"etl"` (expr program returns the group directly)
  - Ternary `A ? B : C` — identical syntax in both
  - Regex `=~ "pattern"` in MVEL → `matches(request.source, "pat.*")` in expr
  - Multi-statement MVEL rules → Starlark `script` method (with `def route(req):` body)
- [ ] `routing-service/docs/python-reference-router/` — minimal Python reference implementation of `TrinoGatewayRouter` (PRD §5 polyglot escape hatch):
  - `server.py` — `grpcio` server implementing `Route`; reads `ROUTING_CONFIG` env var; returns `etl` for `source=airflow`, otherwise default
  - `requirements.txt`
  - `README.md` — "point the gateway at this with `routing.external.grpcAddr: localhost:9001`"
- [ ] `go vet ./...` pass
- [ ] **DoD:** an operator can follow `README.md` end-to-end from zero to a running routing-service wired to trino-goway; MVEL operators have a concrete migration path

---

## Phase 8: Gateway proto dependency (coordinated with trino-goway)

### Task RS-14 — Add `trino_source` + `client_tags` to `RouteRequest` in trino-goway

**Tracked as a trino-goway task.** Listed here as a dependency and coordination point. The routing-service proto already has these fields (added in Task RS-1). This task is complete when trino-goway populates them.

- [ ] `internal/routing/routerpb/router.proto` in trino-goway — add `string trino_source = 12;` and `repeated string client_tags = 13;` to `RouteRequest` (additive, backward-compatible)
- [ ] `internal/routing/external_grpc.go` — `buildProtoRequest`: populate `TrinSource` from `req.Header("X-Trino-Source")`; populate `ClientTags` by splitting `req.Header("X-Trino-Client-Tags")` on `","` (trim spaces per element)
- [ ] Regenerate `routerpb/` Go stubs
- [ ] `internal/routing/routing_test.go` — assert `TrinSource` + `ClientTags` round-trip in `buildProtoRequest` unit tests
- [ ] `go vet ./...` + `golangci-lint run ./...` pass on trino-goway
- [ ] **DoD:** gateway sends `trino_source` and `client_tags` in every `Route` RPC; routing-service `expr`/`script` providers can use `request.source` and `request.client_tags` for real traffic routing

---

## Backlog (not committed for Phase 1)

- **HTTP transport** — gateway supports both; operators may run HTTP + gRPC as belt-and-suspenders
- **mTLS** — swap `insecure.NewCredentials()` for `credentials.NewTLS(tlsCfg)` on both sides; requires `grpcCertFile`/`grpcKeyFile`/`grpcCAFile` config knobs in both services
- **Config-write API** — role-gated (mTLS + scoped JWT), tenant-namespaced authoring via a new admin gRPC service; replaces/augments file-based config
- **Declarative `rules` method (CEL)** — auditable YAML + CEL; lower-trust tenant self-serve authoring within tenant namespaces; registered in the method registry without touching the pipeline or gRPC layer
- **`template` method** — Go `text/template` returning a group name; ultra-low-overhead for simple pattern substitution
- **`wasm` method** — WebAssembly sandbox; "any language, safely sandboxed, hot-swappable"; the intended long-term answer for arbitrary-language routing logic
- **`RouteResponse.resource_group_hint`** proto extension — inject as `X-Trino-Resource-Group` on the proxied request
- **Tenant identity header** — reserved proto field for an explicit tenant identifier if header-derived tenancy proves insufficient
- **Group-name registry validation** — service receives (or queries) the gateway's group registry; warns (or errors) on unknown group names in rules
- **`staged % rollout` for high-risk methods** — gate new method activation behind a traffic percentage ramp before full cut-over
