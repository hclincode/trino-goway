# TODO — routing-service

**PRD:** `routing-service/docs/PRD.md`
**Contract:** `internal/routing/routerpb/router.proto` (`trino.gateway.v1`)
**Gateway integration:** `internal/routing/external_grpc.go`, `internal/config/config.go` §ExternalConfig

Critical path: **RS-1 → RS-2 → RS-3 → RS-4 → RS-5 → RS-9**
Off critical path (start after RS-2): RS-6, RS-7, RS-8
Off critical path (start after RS-3): RS-10, RS-11, RS-12

**Status: COMPLETE** -- RS-1..RS-14 implemented, go-qa-verified, and committed to main (HEAD a06176d). Backlog below is deferred (not Phase 1).

---

## Phase 0: Repo scaffold + proto vendor

### Task RS-1 — Module scaffold + vendored proto

- [x] `routing-service/go.mod` — `module github.com/hclincode/trino-goway/routing-service`, `go 1.23`, initial deps: `google.golang.org/grpc`, `google.golang.org/protobuf`, `google.golang.org/grpc/health`, `github.com/expr-lang/expr`, `go.starlark.net`, `github.com/prometheus/client_golang`, `go.opentelemetry.io/otel`, `gopkg.in/yaml.v3`, `github.com/fsnotify/fsnotify`
- [x] `routing-service/go.sum` generated via `go mod tidy`
- [x] `routing-service/proto/router.proto` — **vendor copy** of `internal/routing/routerpb/router.proto`; update `option go_package` to `github.com/hclincode/trino-goway/routing-service/routerpb`; add **additive** Phase 1 required fields (PRD §4.1):
  - `string trino_source = 12;` on `RouteRequest` — from `X-Trino-Source`
  - `repeated string client_tags = 13;` on `RouteRequest` — from `X-Trino-Client-Tags`, pre-split on comma by gateway
  - Reserve field numbers 14–20 on `RouteRequest` and 4–10 on `RouteResponse` for future additions (comment: `// reserved for future use`)
- [x] `routing-service/proto/Makefile` (or `buf.gen.yaml`) — `protoc` invocation generating Go stubs into `routing-service/routerpb/`
- [x] `routing-service/routerpb/` — generated `router.pb.go` + `router_grpc.pb.go`; committed as generated artifacts
- [x] `routing-service/Makefile` — top-level convenience targets:
  - `make build` — `go build ./...`
  - `make test` — `go test -race ./...`
  - `make test-integration` — `go test -tags=integration -race ./internal/integration/...`
  - `make vet` — `go vet ./...`
  - `make lint` — `golangci-lint run ./...`
  - `make proto` — run the `protoc` invocation in `proto/`
  - `make all` — `build vet lint test` in order
  - `make starlark-test` / `make expr-test` — `go build -o bin/{tool} ./tools/{tool}` (source under `tools/`, output to `bin/`)
- [x] `routing-service/.golangci.yml` — lint config: `errcheck`, `govet`, `staticcheck`, `exhaustive`, `bodyclose`; mirrors the parent repo's lint profile
- [x] `routing-service/docs/CONVENTIONS.md` — documents:
  - **Stack:** Go 1.23, `google.golang.org/grpc` (insecure Phase 1), `google.golang.org/grpc/health`, `github.com/expr-lang/expr`, `go.starlark.net`, `github.com/prometheus/client_golang` (own registry, no global), `go.opentelemetry.io/otel`, `gopkg.in/yaml.v3`, `github.com/fsnotify/fsnotify`
  - **Layout:** `cmd/` for binaries, `internal/` for packages, `proto/` for the vendored `.proto` + `protoc` tooling, `routerpb/` for generated stubs, `docs/` for PRD/TODO/authoring guides
  - **DoD gate (every task):** `go build ./... && go vet ./... && go test -race ./... && golangci-lint run ./...` all green from `routing-service/`; integration tests: `go test -tags=integration -race ./internal/integration/...`
  - **Proto compat policy:** additive field additions (new optional fields) are backward-compatible; removing or renumbering fields is a breaking change requiring a `v2` package; the `.proto` is the stable wire contract between `routing-service` and trino-goway
  - **No global state:** no `init()` side effects that register metrics/providers; all wiring is explicit in `main.go`; no `prometheus.DefaultRegisterer`
  - **Sandboxing discipline:** no I/O functions registered in `expr` env or Starlark universe; `buildEnv` / `StarlarkRouteInput` expose only the `RouteInput` fields plus pure helper functions
  - **Decision log PII rule:** never log raw SQL body; always `sha256(body)[:8]` prefix; never log passwords/tokens from `parameter_map`
- [x] `routing-service/README.md` — brief: purpose, `routing.external.grpcAddr` integration point, build/run, `expr` + Starlark authoring pointer
- [x] `go vet ./...` from `routing-service/` passes
- [x] **DoD:** `go build ./...` + `go vet ./...` + `golangci-lint run ./...` pass from `routing-service/`; generated proto stubs compile against the module; `make all` exits 0

---

## Phase 1: gRPC server + health

### Task RS-2 — gRPC server skeleton + health protocol

Implements the `TrinoGatewayRouter` service wire and `grpc.health.v1.Health`. No routing logic yet — all `Route` calls return `default_routing_group` from config. This is the first integration point with trino-goway.

- [x] `routing-service/internal/server/server.go` — `Server` struct
  - `New(cfg *config.Config, log *slog.Logger) *Server`
  - `Start(ctx context.Context) error` — `grpc.NewServer` (insecure, Phase 1 matches `insecure.NewCredentials()` in the gateway); register `TrinoGatewayRouter` + `grpc.health.v1.Health`; `net.Listen("tcp", cfg.Addr)`; serve in goroutine; block until `ctx` done
  - `Stop()` — `grpcServer.GracefulStop()` (drain in-flight RPCs before exit); never `Stop()` (hard-kills)
  - `grpc.UnaryInterceptor` chain: recovery (panic→error), OTel trace propagation, metrics recording (pre-wired, no-op until Task RS-9)
- [x] `routing-service/internal/server/server.go` — `Route(ctx, *RouteRequest) (*RouteResponse, error)` stub:
  - Return `&RouteResponse{RoutingGroup: cfg.DefaultRoutingGroup}` always
  - Log `req.GetTrinoRequestUser().GetUser()`, `req.GetTrinoSource()`, `req.IsNewQuerySubmission()` at DEBUG
  - If `!req.GetTrinoQueryProperties().GetIsNewQuerySubmission()`: return `RouteResponse{}` immediately (empty = gateway default; service must not decide on non-new submissions — PRD §3)
- [x] `routing-service/internal/server/health.go` — `healthServer` implementing `grpc.health.v1.HealthServer`
  - `Check`: returns `SERVING` when `engine.Ready()` is true, `NOT_SERVING` otherwise
  - `Watch`: basic streaming implementation (send current status; re-send on status change via channel)
  - `engine.Ready()` is injected — false until the routing engine loads its first valid config (Task RS-3)
- [x] `routing-service/internal/config/config.go` — `Config` struct:
  ```
  Addr               string        // gRPC listen addr, default ":9001"
  DefaultRoutingGroup string       // fallback group; must be non-empty
  Methods            []MethodConfig // ordered provider configs
  ```
  `MethodConfig`: `Type string`, `Refresh Duration`, `Program string` (inline), `File string` (path); union — only one of Program/File non-empty
  `Load(path string) (*Config, error)` via `gopkg.in/yaml.v3`; `Validate()` — addr non-empty, defaultRoutingGroup non-empty, each method has exactly one of Program/File
- [x] `routing-service/internal/config/config_test.go` — table-driven:
  - Valid YAML with both `program:` and `file:` method variants round-trips correctly
  - Missing `addr` → `Validate()` error
  - Empty `default_routing_group` → `Validate()` error
  - Method with both `program` and `file` set → `Validate()` error
  - Method with neither `program` nor `file` → `Validate()` error
  - Unknown method `type` in config → no error at load time (registry decides at build time, not config parse)
- [x] `routing-service/cmd/routing-service/main.go` — flags: `--config` (path, required), `--log-level`; compose `Config` + `Server`; SIGTERM/SIGINT → `Stop()` with 30 s deadline; startup log: addr, default group, method count
- [x] `routing-service/internal/server/server_test.go` (`bufconn`-based, `go test -race`):
  - Health `NOT_SERVING` before `engine.SetReady(true)`; `SERVING` immediately after
  - `Watch` streams `NOT_SERVING` → `SERVING` transition without polling (assert the stream delivers the second status within 100 ms of `SetReady`)
  - `Route` with `is_new_query_submission=false` → `RouteResponse{RoutingGroup: ""}` returned immediately; no call to `Pipeline.Evaluate` (assert via a spy/counter)
  - `Route` with `is_new_query_submission=true` → returns `default_routing_group` (stub phase)
  - `GracefulStop` with an in-flight RPC: start a slow `Route` call that blocks 50 ms; call `Stop()`; assert the in-flight call completes before `Stop()` returns (not hard-killed)
  - `goleak.VerifyTestMain` — no goroutine leaks after server start/stop
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** `go build ./cmd/routing-service` produces a static binary; gateway configured with `routing.external.grpcAddr: localhost:9001` routes to `default_routing_group`; `grpcurl -plaintext localhost:9001 grpc.health.v1.Health/Check` returns `SERVING`

---

## Phase 2: Provider interface + registry + pipeline

### Task RS-3 — `RoutingMethod` interface + registry + ordered pipeline

Implements the extensibility core. No method logic yet — establishes the interface that every provider implements and the pipeline the `Route` RPC drives.

- [x] `routing-service/internal/engine/method.go` — `RoutingMethod` interface (from PRD §6.1):
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
- [x] `routing-service/internal/engine/registry.go` — `Registry`: `Register(typeName string, factory func() RoutingMethod)`; `Build(cfg MethodConfig) (RoutingMethod, error)` — looks up factory, calls `LoadConfig`; panics at init if a duplicate type is registered (fail-loud on misconfiguration)
- [x] `routing-service/internal/engine/pipeline.go` — `Pipeline` struct:
  - `New(methods []RoutingMethod, defaultGroup string) *Pipeline`
  - `Evaluate(ctx context.Context, in *RouteInput) (*Decision, error)` — iterate methods in order; first `Decision.Decided=true` wins; if none decide, return `Decision{RoutingGroup: defaultGroup, Decided: false}`; any method `Evaluate` error → log warn + skip that method (never propagate as gRPC error)
  - `Ready() bool` — true once at least one method is loaded or the pipeline has zero methods (pure-default mode)
- [x] `routing-service/internal/engine/input.go` — `FromProto(req *routerpb.RouteRequest) *RouteInput` — maps proto fields to `RouteInput`; `ClientTags` from `req.ClientTags` (pre-split by gateway); `Source` from `req.TrinoSource`; handles nil `TrinoQueryProperties` / `TrinoRequestUser` safely
- [x] `routing-service/internal/engine/pipeline_test.go` — table-driven (`go test -race`):
  - Two methods: first returns `Decided=true` with group `"etl"` → pipeline returns `"etl"`; second method is never called (assert call count via spy)
  - First method returns error → skipped; second method decides `"batch"` → pipeline returns `"batch"`; error is logged, not surfaced to caller
  - Both methods return `Decided=false` → pipeline returns `Decision{RoutingGroup: defaultGroup, Decided: false}`
  - Empty methods slice → returns `defaultGroup` immediately
  - `Ready()` is `false` before any method loads; becomes `true` after first successful `LoadConfig`; stays `true` if a subsequent reload fails
  - Pipeline ordering: three methods returning `Decided=true` in succession; assert only the first is called
- [x] `routing-service/internal/engine/input_test.go` — `FromProto` mapping:
  - `req.TrinoSource = "airflow"` → `RouteInput.Source == "airflow"`
  - `req.ClientTags = ["tag-a", "tag-b"]` → `RouteInput.ClientTags == ["tag-a", "tag-b"]`
  - `req.TrinoRequestUser.User = "alice"` → `RouteInput.User == "alice"`
  - `req.TrinoQueryProperties.DefaultCatalog = "hive"` → `RouteInput.Catalog == "hive"`
  - `req.TrinoQueryProperties.Body = "SELECT 1"` → `RouteInput.Body == "SELECT 1"`
  - Nil `TrinoQueryProperties` → all fields zero-value, no panic
  - Nil `TrinoRequestUser` → `User == ""`, no panic
  - `is_new_query_submission=false` → `RouteInput.IsNew == false`
- [x] Wire `Pipeline.Evaluate` into `server.Route` (replace the stub from Task RS-2); pass `engine.Ready()` to `healthServer`
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** pipeline unit tests green; `FromProto` correctly maps all PRD §4.1 fields; `Route` RPC now drives the method chain; gateway can be pointed at the service and routed deterministically

---

## Phase 3: Method providers

### Task RS-4 — `expr` provider (expr-lang/expr)

- [x] `routing-service/internal/engine/providers/expr/provider.go` — `ExprProvider` struct implementing `RoutingMethod`
  - `Type() string` → `"expr"`
  - `LoadConfig(raw []byte)` — parse YAML `{program: "..."}` or `{file: "..."}` (load file content); compile via `expr.Compile(program, expr.Env(routeEnvType))` + `expr.AsKind(reflect.String)` (ensure program returns a string); store compiled `*vm.Program` atomically; return error without activating if compilation fails
  - `Evaluate(ctx, in)` — `expr.Run(prog, buildEnv(in))`; result string: non-empty → `Decision{RoutingGroup: result, Decided: true}`; empty string → `Decision{Decided: false}`; any `expr.Run` panic/error → `Decision{Decided: false}` + log warn
  - `buildEnv(in *RouteInput) map[string]any` — expose: `request` struct with fields `source`, `client_tags`, `user`, `catalog`, `schema`, `method`, `uri`, `remote_addr`, `body`, `is_new`; plus `hashPct` as a registered function: `hashPct(s string) int` — FNV-1a hash of `s` modulo 100, deterministic (for canary splits)
  - No I/O, no goroutines, no network in `buildEnv`; only pure functions registered
- [x] `routing-service/internal/engine/providers/expr/provider_test.go` — table-driven (`go test -race`):
  - `source == "airflow" ? "etl" : ""` + input `{source:"airflow"}` → `Decision{RoutingGroup:"etl", Decided:true}`
  - Same program + input `{source:"superset"}` → `Decision{Decided:false}`
  - `"tier=premium" in client_tags ? "premium" : ""` + `{client_tags:["tier=premium"]}` → `Decided:true`, group `"premium"`
  - `hashPct(user) < 5 ? "canary" : "prod"` — assert same `user` string always maps to the same bucket across 1000 calls (FNV-1a determinism); assert that for a set of 1000 distinct users, roughly 4–6% map to `< 5` (uniform distribution sanity; use a wide tolerance)
  - Program returning an integer (type mismatch) → `LoadConfig` returns error, no program activated
  - Program with syntax error → `LoadConfig` returns error
  - After a failed `LoadConfig`, old program is still served: load a valid program first; then attempt a bad reload; assert the valid program's decision still works
  - Runtime panic in `expr.Run` (simulated via a program that panics when called) → `Evaluate` returns `Decision{Decided:false}`, no goroutine crash
  - `is_new=false` passed to `buildEnv`: assert `request.is_new == false` is accessible in the expression
- [x] `routing-service/internal/engine/providers/expr/benchmark_test.go` — `BenchmarkExprEvaluate` using a realistic 3-branch program; assert p99 < 50 µs via `testing.B.ReportAllocs()` and a manual latency histogram over 10 000 iterations (not just `b.N` — use a time-bounded loop and assert the 99th percentile directly)
- [x] Register `ExprProvider` in `routing-service/cmd/routing-service/main.go` init block: `registry.Register("expr", func() engine.RoutingMethod { return expr.New() })`
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** all table cases pass; type-mismatch and syntax-error programs are rejected at load; keep-last-good verified; `hashPct` is deterministic and approximately uniform; benchmark p99 < 50 µs

### Task RS-5 — `script` provider (Starlark)

- [x] `routing-service/internal/engine/providers/script/provider.go` — `ScriptProvider` struct implementing `RoutingMethod`
  - `Type() string` → `"script"`
  - `LoadConfig(raw []byte)` — parse YAML `{file: "..."}` or `{program: "..."}`; parse + compile Starlark source via `starlark.FileProgram` / `starlark.ExecFile` in a scratch thread; verify the compiled program exports a `route` function accepting one argument; store compiled `*starlark.Program` atomically (swap on success only)
  - `Evaluate(ctx, in)` — create a `*starlark.Thread` with `thread.SetMaxSteps(10_000)` (CPU step cap); start a goroutine that calls `thread.Cancel("deadline")` when `ctx.Done()` fires; call the `route` function with a `StarlarkRouteInput` struct value built from `in`; result: `starlark.String` non-empty → `Decided: true`; `starlark.None` or empty string → `Decided: false`; any error (EvalError, step limit, deadline cancel) → `Decided: false` + log warn (never propagate)
  - `StarlarkRouteInput` — `starlark.Value` implementing `starlark.HasAttrs`: exposes read-only attrs `source`, `client_tags` (Starlark list of strings), `user`, `catalog`, `schema`, `method`, `uri`, `remote_addr`, `body`, `is_new`; `Freeze()` is a no-op (already immutable); no I/O methods exposed
  - Predeclared names injected into the Starlark universe: `hashPct` (same semantics as expr provider — FNV-1a mod 100, deterministic)
  - Never expose: `file`, `open`, any `os.*`, any network primitives; the sandbox is structural (no stdlib; only explicit predeclared names)
- [x] `routing-service/internal/engine/providers/script/provider_test.go` — table-driven (`go test -race`):
  - `def route(req): return "etl" if req.source == "airflow" else None` + `{source:"airflow"}` → `Decided:true`, group `"etl"`
  - Same script + `{source:"superset"}` → `Decided:false`
  - `hashPct` determinism: same `req.user` always yields the same bucket across 1000 Starlark calls
  - `return None` → `Decided:false`, no error
  - `return ""` → `Decided:false`, no error
  - Runtime error `1/0` → `Decided:false`, no panic, error logged
  - Syntax error in script → `LoadConfig` returns error; provider returns no program; subsequent `Evaluate` returns `Decided:false` (not a crash)
  - Keep-last-good: load valid script A; attempt reload of syntax-error script B; assert script A's decision still works
  - `is_new` and `client_tags` attrs accessible and correct in Starlark (`req.is_new == True`, `"tag-a" in req.client_tags`)
  - **Step-limit (must assert timing):** `def route(req): [i for i in range(10**9)]` — `Evaluate` returns `Decided:false` within **< 5 ms** wall clock (use `time.Now()` in the test; assert elapsed < 5 ms)
  - **Deadline propagation:** pass a `context.WithTimeout(ctx, 1ms)` to `Evaluate` for a slow script; assert `Decided:false` returned and the `thread.Cancel` goroutine exits cleanly (goleak)
  - **Sandbox negative tests** (each is a separate table row; all must compile/run without crashing the test process; all must return `Decided:false`):
    - `def route(req): load("os", "getenv"); return "x"` — `load()` not permitted; `LoadConfig` or `Evaluate` returns error
    - `def route(req): open("/etc/passwd")` — `open` not defined; `EvalError` → `Decided:false`
    - `def route(req): import sys` — `import` is not Starlark syntax; `LoadConfig` returns error
    - `def route(req): [1]*10**8` — large list allocation; step limit fires before OOM
    - `def route(req): x = {}; [x.update({i:i}) for i in range(10**7)]` — step limit fires
- [x] `routing-service/internal/engine/providers/script/benchmark_test.go` — `BenchmarkStarlarkEvaluate` with a realistic 4-branch `route(req)` function; assert p99 < 1 ms via a time-bounded 10 000-iteration loop with latency histogram
- [x] Register `ScriptProvider` in `main.go` init: `registry.Register("script", func() engine.RoutingMethod { return script.New() })`
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** all table cases pass; step-limit test terminates in < 5 ms; all 5 sandbox-negative inputs are handled without crashing; keep-last-good and deadline propagation verified; benchmark p99 < 1 ms

---

## Phase 4: Harness guardrails

### Task RS-6 — Hot-reload + validate-before-activate

Depends on RS-3 (pipeline). Can start after RS-3.

- [x] `routing-service/internal/reload/watcher.go` — `ConfigWatcher` struct
  - `New(path string, pipeline *engine.Pipeline, registry *engine.Registry, log *slog.Logger) *ConfigWatcher`
  - `Start(ctx context.Context)` — `fsnotify.NewWatcher`; watch the config file (and all `file:` script paths referenced in the current config); on `fsnotify.Write` or `fsnotify.Create`: call `reload()` in a goroutine; debounce 100 ms (discard bursts)
  - `reload()`:
    1. Parse + validate the new config via `config.Load`
    2. For each method: call `RoutingMethod.LoadConfig` with the method's raw config bytes
    3. If any step fails: log error with diff summary (old config hash vs new), increment `config_reload_errors_total`, emit structured audit event `{trigger: "file_change", result: "error", diff: ...}`, **keep the current pipeline live** (last-known-good)
    4. If all succeed: atomically swap the pipeline's method slice; increment `config_reload_success_total`; emit audit event `{result: "ok", new_hash: ...}`
  - `Stop()` — close the fsnotify watcher
- [x] `routing-service/internal/reload/watcher_test.go` (`go test -race`):
  - Write a valid config file with an `expr` method routing `source=="a"→"group-a"`; start watcher; assert pipeline routes `"a"` → `"group-a"`
  - Overwrite with an **invalid** config (syntax error); wait > 100 ms (debounce); assert pipeline **still** routes `"a"` → `"group-a"` (last-known-good); assert `config_reload_errors_total` incremented by 1; assert structured audit event `{result: "error"}` emitted
  - Overwrite with a valid config routing `"a"` → `"group-b"`; wait for debounce + reload; assert pipeline now routes `"a"` → `"group-b"`; assert `config_reload_success_total` incremented; assert audit event `{result: "ok"}`
  - **Concurrent-traffic test:** start 10 goroutines each making 100 `Evaluate` calls in a loop; mid-way trigger a valid config reload; assert no call returns an error and no goroutine panics (atomic swap must never expose a nil pipeline mid-flight)
  - **Debounce test:** write 5 rapid file-write events within 50 ms (well within the 100 ms debounce window); assert `reload()` is called exactly once after the debounce settles (use a spy counter)
  - `goleak.VerifyTestMain` — fsnotify goroutine and reload goroutine must not leak after `Stop()`
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** valid change atomically swaps pipeline; invalid change keeps last-good + records error metric + emits audit event; concurrent traffic unaffected during reload; debounce coalesces rapid writes to one reload; no goroutine leaks

### Task RS-7 — Dry-run CLI tool (`routing-service-validate`)

Depends on RS-3, RS-4, RS-5. Can start after RS-5.

- [x] `routing-service/cmd/routing-service-validate/main.go` — standalone CLI
  - Flags: `--config <path>` (required), `--samples <path>` (optional; YAML file of sample `RouteInput` records), `--diff` (compare against a baseline config)
  - Without `--samples`: parse + compile the config; print `OK` or validation errors; exit 0/1
  - With `--samples`: load samples; run pipeline against each; print table: `sample_id | input_summary | new_group | (old_group if --diff)`; highlight rows where new ≠ old
  - Exit 0 if config valid; exit 1 on any compile/validation error; exit 2 if `--diff` shows routing changes (allows CI to gate on unexpected route changes)
- [x] `routing-service/cmd/routing-service-validate/validate_test.go` — valid config exits 0; invalid exits 1; sample diff detected
- [x] `go build ./cmd/routing-service-validate` passes
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** `routing-service-validate --config routes.yaml --samples samples.yaml` prints routing table; CI can block deploys on unexpected changes

### Task RS-8 — Kill-switch + method-level disable

Depends on RS-3 (pipeline). Can start after RS-3.

- [x] `routing-service/internal/engine/pipeline.go` — extend `Pipeline`:
  - `DisableMethod(typeName string)` — atomically mark the named method as disabled; `Evaluate` skips disabled methods; takes effect on the next request (sub-second propagation — no restart required)
  - `EnableMethod(typeName string)` — re-enable; config + compiled program already resident
  - `DisabledMethods() []string` — introspection
- [x] `routing-service/internal/server/server.go` — expose a `DisableMethod`/`EnableMethod` gRPC admin method (unary, admin-only placeholder; no auth in Phase 1 — document as "must be firewalled; mTLS required in Phase 2"):
  - `rpc DisableMethod(DisableMethodRequest) returns (DisableMethodResponse)` — added to a new `RoutingServiceAdmin` service in `router.proto` (separate service, separate registration)
  - `DisableMethodRequest { string type = 1; }`, `DisableMethodResponse { bool ok = 1; string message = 2; }`
- [x] `routing-service/internal/engine/pipeline_test.go` — extend with kill-switch cases (`go test -race`):
  - Pipeline with `expr` then `script`; `expr` decides `"etl"`; `DisableMethod("expr")`; next call: `expr` is skipped, `script` decides; assert no restart needed (call happens in same process, no sleep)
  - `DisableMethod("expr")`; verify `DisabledMethods()` returns `["expr"]`; `EnableMethod("expr")`; verify `DisabledMethods()` returns `[]`; assert `expr` decides again on the next call
  - Disable both methods; assert pipeline returns `defaultGroup` on the next call
  - Disable a method that does not exist (unknown type): `DisableMethod("unknown")` is a no-op and does not panic
  - **Propagation latency:** call `DisableMethod`, then immediately (same goroutine, no sleep) call `Evaluate`; assert the disabled method is not invoked — the atomic check takes effect within the same call (no sleep needed because it's the same goroutine post-disable)
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** `DisableMethod` takes effect on the very next `Evaluate` call; `EnableMethod` restores it; unknown type is a no-op; `DisabledMethods()` reflects current state accurately

---

## Phase 5: Observability

### Task RS-9 — Prometheus metrics + structured decision logs + OTel tracing

Depends on RS-2 (server), RS-3 (pipeline). Can be partially started after RS-2.

- [x] `routing-service/internal/metrics/metrics.go` — own `*prometheus.Registry` (no global):
  - `routing_service_requests_total{source, routing_group, method_type, outcome}` — `outcome` ∈ `decided|deferred|error|fallback`
  - `routing_service_decision_duration_seconds` — histogram (label `method_type`); target p99 ≤ 1 ms for in-memory eval
  - `routing_service_fallback_total` — counter; alert threshold: `> 1%` of requests over 5 m window (PRD §7)
  - `routing_service_config_reload_total{result}` — `result` ∈ `ok|error`
  - `routing_service_config_version` — gauge with label `hash` (active config content hash)
  - `routing_service_method_disabled{type}` — gauge 1 if disabled, 0 if enabled
  - Expose on a `/metrics` HTTP endpoint on a separate port (`cfg.MetricsAddr`, default `:9091`); `promhttp.HandlerFor(reg, ...)` with `EnableOpenMetrics: true`
- [x] `routing-service/internal/logging/decision.go` — `DecisionLogger`:
  - Log each `Route` call at DEBUG; sample at ~10% at INFO steady-state; always log at INFO on fallback (PRD §7)
  - Log fields: `rule_id` (method type that decided), `input_attributes` (source, user — **never raw body/SQL**; body → `sha256(body)[:8]` prefix only), `routing_group`, `latency_ms`, `config_version_hash`
  - `DecisionLogger.ShouldLog(isFallback bool) bool` — 10% sample rate + always-on for fallback
- [x] `routing-service/internal/tracing/tracing.go` — OTel setup:
  - `Init(cfg TracingConfig) (*trace.TracerProvider, error)` — OTLP exporter (endpoint configurable; disabled if empty); resource with `service.name=routing-service`
  - In `server.Route`: `tracer.Start(ctx, "TrinoGatewayRouter/Route")`; propagate incoming gRPC trace context via `otelgrpc.UnaryServerInterceptor`; add span attrs: `routing.group`, `routing.source`, `routing.method_type`
- [x] `routing-service/internal/metrics/metrics_test.go` (`go test -race`):
  - Send 10 `Route` calls all deciding via `expr` method, group `"etl"` → assert `routing_service_requests_total{method_type="expr",routing_group="etl",outcome="decided"}` == 10
  - Send 5 calls where both methods skip → assert `routing_service_fallback_total` == 5 and `routing_service_requests_total{outcome="fallback"}` == 5
  - Send 3 calls where a method returns an error → assert `routing_service_requests_total{outcome="error"}` == 3 and those 3 are NOT also counted as `fallback`
  - Assert `routing_service_decision_duration_seconds` histogram has observations (bucket counts > 0) after any `Route` call
  - Trigger a config reload success → assert `routing_service_config_reload_total{result="ok"}` increments; trigger a reload failure → assert `routing_service_config_reload_total{result="error"}` increments
  - `DisableMethod("expr")` → assert `routing_service_method_disabled{type="expr"}` gauge == 1; `EnableMethod` → gauge == 0
  - `/metrics` HTTP endpoint returns 200, `Content-Type` contains `application/openmetrics-text` or `text/plain`, body parses cleanly with `github.com/prometheus/common/expfmt`
- [x] `routing-service/internal/logging/decision_test.go`:
  - Call `DecisionLogger` with a `RouteInput` where `Body = "SELECT * FROM secrets"` → assert logged `body` field is `sha256("SELECT * FROM secrets")[:8]`, NOT the raw SQL
  - Call with `isFallback=true` → `ShouldLog` returns `true` always
  - Call with `isFallback=false` 1000 times → assert `ShouldLog` returns `true` for approximately 8–12% of calls (10% rate with wide tolerance)
  - Log fields present: `rule_id`, `input_attributes`, `routing_group`, `latency_ms`, `config_version_hash`
- [x] `routing-service/internal/tracing/tracing_test.go`:
  - Start an in-memory OTel span exporter; run a `Route` call with a parent trace context injected via gRPC metadata; assert the emitted span has `routing.group`, `routing.source`, `routing.method_type` attributes set and parent span ID matches the injected context
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** all counter/histogram assertions pass; body redaction verified; fallback always-logs verified; parent trace context propagation verified; `/metrics` endpoint serves OpenMetrics text

---

## Phase 6: CLI test tools

### Task RS-10 — `starlark-test` CLI tool

Standalone tool to load a Starlark routing script, build the request context from a given input, run `route(req)`, and print the execution result. Runs under the same `SetMaxSteps` cap and structural sandbox as the production provider. The primary use case is interactive script authoring; the batch `--samples` mode is the basis for dry-run CI validation.

Depends on RS-5 (Starlark provider).

**Interface:**
```
starlark-test <script-path> <input>
```
- `arg1` (`<script-path>`) — path to the `.star` file to load; must define `def route(req):`
- `arg2` (`<input>`) — the request input, one of:
  - an inline JSON object: `'{"source":"airflow","user":"pipeline@acme.com","is_new":true}'`
  - a path to a `.json` file: `./request.json`
  - JSON key→`RouteInput` field mapping: `source`, `user`, `client_tags` (array of strings), `catalog`, `schema`, `method`, `uri`, `remote_addr`, `body`, `is_new` (bool), `param_map` (object); all keys optional, zero-value if absent

**Single-input output (stdout):**
```
group:   etl
latency: 0.14ms
status:  OK
```
`status` values: `OK`, `STEP_LIMIT` (step cap hit), `ERROR: <msg>` (script runtime error), `DEFERRED` (script returned `None` or `""`)

**Additional flags:**
- `--max-steps <n>` (default `10000`) — override the step budget for this invocation only; does not affect the service's production cap
- `--samples <path>` — run against a YAML batch file of inputs; `arg2` is ignored; prints one table row per sample
- `--expect <path>` (requires `--samples`) — YAML of `{sample_id: expected_group}`; exit non-zero on any expectation miss
- `--verbose` — print the deserialized `RouteInput` fields before the result

**Batch YAML schema** (for `--samples`):
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

**Batch output:**
```
SAMPLE                  GROUP           LATENCY   STATUS
airflow-etl             etl             0.12ms    OK
superset-interactive    interactive     0.09ms    OK
no-match                (deferred)      0.08ms    OK
```

**Usage examples:**
```sh
# single input, inline JSON (the primary authoring loop)
starlark-test routes.star '{"source":"airflow","user":"alice","is_new":true}'

# single input from a JSON file
starlark-test routes.star ./request.json

# batch CI validation with expectations
starlark-test routes.star --samples samples.yaml --expect expected.yaml

# raise step budget for profiling without hitting the production cap
starlark-test routes.star '{"source":"airflow"}' --max-steps 100000
```

- [x] `routing-service/tools/starlark-test/main.go` — implement the interface above; detect `arg2` as JSON file vs inline by checking whether the value is a valid file path that exists; build a `RouteInput` from the parsed JSON; invoke `ScriptProvider.Evaluate` directly (reuse the production provider, same sandbox + limits); single-input: print key:value lines; batch (`--samples`): print table; exit 0 on success, non-zero on script error, step limit, or expectation miss
- [x] `routing-service/tools/starlark-test/main_test.go` — table-driven, each case run via `exec.Command` or by calling `main` with captured stdout:
  - **Exit-code matrix:**
    | scenario | expected exit | expected status in output |
    |---|---|---|
    | valid script + matching input | 0 | `OK` |
    | valid script + non-matching input | 0 | `DEFERRED` |
    | step-limit script + any input | non-zero | `STEP_LIMIT` |
    | script missing `route` function | non-zero | `ERROR: ...` |
    | script syntax error | non-zero | `ERROR: ...` |
    | `--samples` + `--expect` all match | 0 | table, all `OK` |
    | `--samples` + `--expect` one mismatch | non-zero | mismatch row highlighted |
  - **Output == production provider output:** run the same input through `ScriptProvider.Evaluate` directly in the test; assert `starlark-test`'s printed group exactly matches the provider's `Decision.RoutingGroup` (or `(deferred)` if `Decided=false`)
  - **Step-limit timing:** step-limit script exits within < 500 ms wall clock (generous for CI; production contract is < 5 ms per RS-5, but CI process overhead allowed here)
  - `--max-steps 1` forces step limit on any non-trivial script; assert exit non-zero
- [x] `go build ./tools/starlark-test` produces a static binary
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** exit-code matrix verified; tool output matches production provider for same input; step-limit enforced; `--samples`/`--expect` CI mode tested end-to-end

### Task RS-11 — `expr-test` CLI tool

Standalone tool to compile an `expr-lang` routing program, evaluate it against a given input, and print the result. Mirror of `starlark-test` for the `expr` provider. Uses the same `ExprProvider` code path as production.

Depends on RS-4 (expr provider).

**Interface:**
```
expr-test <program-path> <input>
```
- `arg1` (`<program-path>`) — path to a file containing the expr program; alternatively supplied via `--program <string>` for inline use (in which case `arg2` is still the second positional or `--input`)
- `arg2` (`<input>`) — same format as `starlark-test`: inline JSON object or path to a `.json` file; same `RouteInput` field mapping

**Single-input output:**
```
group:   etl
latency: 0.09ms
status:  OK
```
`status` values: `OK`, `COMPILE_ERROR: <msg>` (program failed to compile), `RUNTIME_ERROR: <msg>` (eval error), `DEFERRED` (program returned `""`)

**Additional flags:**
- `--program <string>` — inline expr program source; mutually exclusive with `arg1` file path
- `--samples <path>` — same batch YAML as `starlark-test`; `arg2` ignored
- `--expect <path>` (requires `--samples`) — same expectations YAML as `starlark-test`
- `--verbose` — print deserialized `RouteInput` before result

**Usage examples:**
```sh
# program from file, input inline
expr-test routes.expr '{"source":"airflow","is_new":true}'

# program inline (no file needed)
expr-test --program 'source == "airflow" ? "etl" : ""' '{"source":"airflow"}'

# batch CI check
expr-test routes.expr --samples samples.yaml --expect expected.yaml

# input from JSON file
expr-test routes.expr ./request.json
```

- [x] `routing-service/tools/expr-test/main.go` — implement the interface above; use `ExprProvider.LoadConfig` to compile (catches type errors at load time, same as production) and `ExprProvider.Evaluate` to run; single-input: print key:value; batch: print table; exit codes match `starlark-test`
- [x] `routing-service/tools/expr-test/main_test.go` — table-driven, same structure as `starlark-test`:
  - **Exit-code matrix:**
    | scenario | expected exit | expected status in output |
    |---|---|---|
    | valid program + matching input | 0 | `OK` |
    | valid program + non-matching input | 0 | `DEFERRED` |
    | program with compile/type error | non-zero | `COMPILE_ERROR: ...` |
    | program returning integer (type mismatch) | non-zero | `COMPILE_ERROR: ...` |
    | program runtime error | non-zero | `RUNTIME_ERROR: ...` |
    | `--samples` + `--expect` all match | 0 | table, all `OK` |
    | `--samples` + `--expect` one mismatch | non-zero | mismatch row highlighted |
  - **Output == production provider output:** run the same program + input through `ExprProvider.Evaluate` directly; assert `expr-test` printed group matches `Decision.RoutingGroup` exactly
  - `--program` inline takes precedence over `arg1` file when both provided; assert error if neither is given
  - `arg1` pointing to a non-existent file → non-zero exit, `ERROR: ...` in output
- [x] `go build ./tools/expr-test` produces a static binary
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** exit-code matrix verified; tool output matches production provider for same input; compile-error and type-error programs rejected; `--samples`/`--expect` CI mode tested end-to-end

---

## Phase 7: Integration test + docs

### Task RS-12 — Integration test: gateway ↔ routing-service round-trip

An in-process integration test that dials the real routing-service binary (or starts it in-process via `bufconn`) and verifies the full `Route` RPC contract from the gateway's perspective.

Depends on RS-2, RS-3, RS-4, RS-5.

- [x] `routing-service/internal/integration/roundtrip_test.go` — `//go:build integration`
  - Start routing-service in-process (`bufconn`) with a test config containing one `expr` method and one `script` method
  - Construct a `RouteRequest` (same as `buildProtoRequest` in `internal/routing/external_grpc.go`):
    - `is_new_query_submission: true`, `trino_source: "airflow"`, `trino_request_user.user: "pipeline@acme.com"`
  - Assert `RouteResponse.routing_group == "etl"` (from the expr method)
  - Send a non-new request (`is_new_query_submission: false`): assert `routing_group == ""` (service defers immediately)
  - Send a request that matches no rule: assert `routing_group == "default"` (pipeline default)
  - Kill the `script` method via `DisableMethod("script")`; send request that would match the script; assert `expr` still decides (or default if neither method matches)
  - `grpc.health.v1.Health/Check` returns `SERVING` after pipeline ready; returns `NOT_SERVING` before first config load
  - `goleak.VerifyTestMain`
- [x] `routing-service/internal/integration/roundtrip_test.go` — verify `trino_source` + `client_tags` fields round-trip from proto → `RouteInput` correctly (PRD §4.1 field contract)
- [x] `routing-service/internal/integration/roundtrip_test.go` — verify metrics: after 10 Route calls, `routing_service_requests_total` counter == 10; at least 1 `fallback_total` when all methods skip
- [x] `go test -tags=integration -race ./internal/integration/...` passes
- [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] **DoD:** full `Route` RPC contract verified; `trino_source`/`client_tags` fields verified; health lifecycle verified; metrics verified; race detector clean

### Task RS-13 — Docs + config example + MVEL→expr migration guide

- [x] `routing-service/README.md` — complete: purpose, build (`go build ./cmd/routing-service`), run (`./routing-service --config config.yaml`), gateway config (`routing.external.grpcAddr: host:9001`), health probe, metrics scrape, build tags for integration tests
- [x] `routing-service/docs/config.example.yaml` — annotated example covering `addr`, `default_routing_group`, one `expr` method, one `script` method, canary split with `hashPct`, `metrics_addr`
- [x] `routing-service/docs/expr-authoring.md` — `expr` language reference for routing: available `request.*` fields, `hashPct`, `hasSuffix`, `split`, return conventions (`"" = defer`), error handling, step-limit note (bounded by construction — no explicit limit needed for `expr`)
- [x] `routing-service/docs/starlark-authoring.md` — Starlark language reference for routing: available `req.*` attrs, `hashPct`, `None = defer`, `thread.SetMaxSteps` note (implicit via harness — operator does not set it), no I/O, freeze semantics, error handling
- [x] `routing-service/docs/mvel-to-expr-migration.md` — MVEL→expr mapping table (PRD §5 reference):
  - `request.getHeader("X-Trino-Source") == "airflow"` → `request.source == "airflow"`
  - `request.getHeader("X-Trino-Client-Tags").contains("tier=premium")` → `"tier=premium" in request.client_tags`
  - `request.getHeader("X-Trino-User")` → `request.user`
  - `result.put("routingGroup", "etl")` → return value `"etl"` (expr program returns the group directly)
  - Ternary `A ? B : C` — identical syntax in both
  - Regex `=~ "pattern"` in MVEL → `matches(request.source, "pat.*")` in expr
  - Multi-statement MVEL rules → Starlark `script` method (with `def route(req):` body)
- [x] `routing-service/docs/python-reference-router/` — minimal Python reference implementation of `TrinoGatewayRouter` (PRD §5 polyglot escape hatch):
  - `server.py` — `grpcio` server implementing `Route`; reads `ROUTING_CONFIG` env var; returns `etl` for `source=airflow`, otherwise default
  - `requirements.txt`
  - `README.md` — "point the gateway at this with `routing.external.grpcAddr: localhost:9001`"
- [x] `go vet ./...` pass
- [x] **DoD:** an operator can follow `README.md` end-to-end from zero to a running routing-service wired to trino-goway; MVEL operators have a concrete migration path

---

## Phase 8: Gateway proto dependency (coordinated with trino-goway)

### Task RS-14 — Add `trino_source` + `client_tags` to `RouteRequest` in trino-goway

**Tracked as a trino-goway task.** Listed here as a dependency and coordination point. The routing-service proto already has these fields (added in Task RS-1). This task is complete when trino-goway populates them.

- [x] `internal/routing/routerpb/router.proto` in trino-goway — add `string trino_source = 12;` and `repeated string client_tags = 13;` to `RouteRequest` (additive, backward-compatible)
- [x] `internal/routing/external_grpc.go` — `buildProtoRequest`: populate `TrinSource` from `req.Header("X-Trino-Source")`; populate `ClientTags` by splitting `req.Header("X-Trino-Client-Tags")` on `","` (trim spaces per element)
- [x] Regenerate `routerpb/` Go stubs
- [x] `internal/routing/routing_test.go` — assert `TrinSource` + `ClientTags` round-trip in `buildProtoRequest` unit tests
- [x] `go vet ./...` + `golangci-lint run ./...` pass on trino-goway
- [x] **DoD:** gateway sends `trino_source` and `client_tags` in every `Route` RPC; routing-service `expr`/`script` providers can use `request.source` and `request.client_tags` for real traffic routing

---

## Testing & Verification Strategy

This section defines the test pyramid, conventions, threat-model coverage, and the exact CI command matrix for the routing-service. All implementation tasks are bound by these constraints.

---

### Test pyramid

**Unit tests** (run on every `go test ./...`):
- One `_test.go` file per package; table-driven with named subtests (`t.Run`).
- Cover: `config` (load/validate), `engine/pipeline` (ordering, skip-on-error, all-defer, ready transitions), `engine/input` (`FromProto` field mapping including nil cases), each provider (`expr`, `script`) with the full case matrix above, `reload/watcher` (valid/invalid/concurrent), `metrics` (counter/histogram values, body redaction, `/metrics` endpoint), `logging/decision` (PII redaction, sample rate), `tracing` (parent context propagation).
- No network, no file I/O in unit tests except `reload/watcher` which uses a real temp file (that is its subject under test).

**Integration tests** (`//go:build integration`, run via `go test -tags=integration -race ./internal/integration/...`):
- Full `Route` RPC contract over a real TCP socket (`bufconn`): proto round-trip, non-new-submission early return, pipeline default, kill-switch, health lifecycle, metrics after N calls.
- These require no external services (Postgres, containers) — only the in-process gRPC server.
- Run in CI as a separate step; always with `-race`.

**Manual / developer verification via CLI tools**:
- `starlark-test` and `expr-test` are the primary interactive verification loop during script/expression authoring.
- The `--samples`/`--expect` YAML pair is the "golden suite" for a deployment: committed fixtures under `tools/testdata/`; `routing-service-validate --diff` is the CI gate that exits non-zero if routing changes unexpectedly.

---

### Always `-race`; always `goleak`

Every `go test` invocation in CI and in the DoD gates uses `-race`:
```
go test -race ./...
go test -tags=integration -race ./internal/integration/...
```

Every package with goroutines (server, reload watcher, Starlark cancel goroutine, metrics HTTP server) must have a `TestMain` that calls `goleak.VerifyTestMain(m)`. Goroutine sources to watch:
- `grpc.NewServer` goroutines (leak if `GracefulStop` not called)
- `fsnotify.NewWatcher` internal goroutine (leak if watcher not closed)
- Starlark `thread.Cancel` goroutine spawned per `Evaluate` call (must exit after cancel or step-limit)
- Prometheus HTTP server goroutine (leak if listener not closed on test teardown)

---

### Sandbox-escape and hostile-input coverage

A dedicated fuzz/table test in `internal/engine/providers/script/sandbox_test.go` and `internal/engine/providers/expr/sandbox_test.go` throws hostile inputs at each provider and asserts they are bounded or rejected:

**Starlark sandbox table** (each row: script source → expected outcome):
| Input | Expected |
|---|---|
| `def route(req): load("os", "getenv"); return "x"` | `LoadConfig` or `Evaluate` error; `Decided:false` |
| `def route(req): open("/etc/passwd")` | `EvalError` (undefined name); `Decided:false` |
| `def route(req): import sys` | `LoadConfig` error (invalid syntax) |
| `def route(req): [1]*10**8` | Step limit fires; `Decided:false`; returns in < 10 ms |
| `def route(req): x = {}; [x.update({str(i):i}) for i in range(10**7)]` | Step limit fires; `Decided:false` |
| `def route(req): route(req)` (infinite recursion) | Step limit or stack overflow caught; `Decided:false`, no panic |
| `def route(req): return 42` (wrong return type) | `EvalError` (type mismatch on caller side); `Decided:false` |

**expr sandbox table**:
| Input | Expected |
|---|---|
| Program returning an integer literal | `LoadConfig` error (`AsKind(String)` check) |
| Program referencing an undefined variable | `LoadConfig` error (type-check) |
| Program calling a non-existent function `fetch("http://evil")` | `LoadConfig` error |
| Program with deeply nested ternary (> 100 levels) | Compiles or compile-errors; does not hang; `Evaluate` returns quickly |

These tests are **not** fuzz tests (no `testing.F`) in Phase 1 — they are table-driven with pre-enumerated hostile inputs. A `//go:build fuzz` fuzz target is noted in the Backlog for future hardening.

---

### Latency verification

Latency regressions are caught by benchmarks in each provider package. The benchmarks are run in CI via:
```
go test -bench=. -benchtime=5s -count=3 ./internal/engine/providers/...
```
Target values (hard limits — if a benchmark's median exceeds these, the PR is flagged):
- `BenchmarkExprEvaluate` (3-branch realistic program): p99 < 50 µs
- `BenchmarkStarlarkEvaluate` (4-branch realistic `route(req)` function): p99 < 1 ms

The benchmark helpers use a time-bounded loop (not just `b.N`) to capture a latency histogram and assert the 99th percentile value directly:
```go
// collect 10_000 timings, sort, assert timings[9900] < threshold
```

---

### Golden suites and CI dry-run gate

The `--samples`/`--expect` YAML pairs are committed as `tools/testdata/` fixtures. There is one fixture set per provider type, covering all PRD §6.2 scenarios (airflow→etl, superset→interactive with canary, client-tag routing, user-domain routing).

The CI dry-run gate runs on every PR that touches any provider, config, or script file:
```
./bin/routing-service-validate --config config.yaml --samples tools/testdata/samples.yaml --diff --baseline tools/testdata/baseline.yaml
```
Exit code 2 = unexpected routing change = PR blocked. Exit code 1 = config invalid = PR blocked. Exit code 0 = safe to merge.

---

### Negative config tests (consolidated)

These are covered by `config_test.go` and `registry_test.go`:
- Invalid YAML (malformed) → `config.Load` returns parse error
- Unknown method `type` in config → `Registry.Build` returns error (type not registered)
- Method compile error (bad expr / bad Starlark) → `LoadConfig` returns error; `Pipeline` not activated with the bad method
- Both `program` and `file` set in the same method → `Validate()` error
- Neither `program` nor `file` set → `Validate()` error
- Empty `default_routing_group` → `Validate()` error
- `addr` already in use → `Server.Start` returns bind error

---

### Coverage expectation

- Unit tests: ≥ 80% statement coverage per package (enforced via `go test -coverprofile` in CI; `golangci-lint`'s `cyclop` linter flags functions over complexity 15 for test attention)
- Integration tests: cover the end-to-end `Route` contract; not counted toward per-package unit coverage
- Explicitly excluded from coverage: generated `routerpb/` stubs, `cmd/routing-service/main.go` (tested via integration), `tools/*/main.go` (tested via their own `_test.go`)

---

### Exact CI command matrix

```sh
# Phase 0 gate (scaffold)
go build ./...
go vet ./...
golangci-lint run ./...

# Phase 1–6 gate (all tasks)
go build ./...
go vet ./...
go test -race -coverprofile=coverage.out ./...
golangci-lint run ./...

# Integration gate (RS-12, runs in a separate CI job)
go test -tags=integration -race -v ./internal/integration/...

# Benchmark gate (providers, runs on PR touching providers/)
go test -bench=. -benchtime=5s -count=3 ./internal/engine/providers/...

# Dry-run CI gate (runs on PR touching any config, provider, or script file)
go build -o bin/routing-service-validate ./cmd/routing-service-validate
./bin/routing-service-validate \
  --config tools/testdata/config.yaml \
  --samples tools/testdata/samples.yaml \
  --diff --baseline tools/testdata/baseline.yaml
# exit 0 = safe; exit 1 = config invalid; exit 2 = unexpected route change

# CLI tool smoke (runs on every build)
go build -o bin/starlark-test ./tools/starlark-test
go build -o bin/expr-test ./tools/expr-test
./bin/starlark-test tools/testdata/route.star tools/testdata/airflow.json
./bin/expr-test tools/testdata/route.expr tools/testdata/airflow.json
```

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
