# TODO

## Phase 0: Team Alignment

- [x] Task 1 — Agree on study insight template and file conventions (architect leads, all agents participate)

## Phase 1: Study

- [x] Task 2 — trino-expert studies trino & trino-gateway
- [x] Task 3 — java-analyst studies trino & trino-gateway
- [x] Task 4 — architect studies trino & trino-gateway
- [x] Task 5 — go-implementer studies trino & trino-gateway
- [x] Task 6 — java-qa studies trino & trino-gateway
- [x] Task 7 — qa-tech-lead studies trino & trino-gateway
- [x] Task 8 — go-qa studies trino & trino-gateway

## Phase 2: Topic Discussion

- [x] Task 9 — Discuss: Do we need a Go version of trino-gateway? (result: `topics/do-we-needs-golang-trino-gateway.md` — unanimous PROCEED WITH CAVEATS)

## Phase 3: Architecture Design + Targeted Studies

- [x] Task 10 — Architect writes `phase2-gate-responses.architect.md` (library decisions, DI stance, streaming/oracle/cookie rulings, 6th hard invariant, sequencing constraints; includes ruling on gRPC in v1 vs. Non-Groomed)
- [x] Task 11 — Go-implementer writes `SCOPE.md` (locked scope, deferred scope, reversal cost per item; team-lead sign-off required to change any ruling)
- [x] Task 12 — Go-implementer writes `gateway-cookies-and-sticky-routing.go-implementer.md` (cookie design: HMAC-SHA256 wire-compat with Java `GatewayCookie`, `wireCompat` config flag, `/v1/spooled/*` + `/v1/spooled/ack` sticky routing via `TG.*` cookie; required before proxy implementation starts)
- [x] Task 13 — trino-expert studies `/v1/spooled/*` URL structure in Trino source (`studies/trino/spooled-segment-protocol.trino-expert.md`): token format, whether queryId is encoded, redirect chain, and whether cookie is the only viable sticky mechanism
- [x] Task 14 — go-implementer studies `GatewayCookie.java` in depth (`studies/trino-gateway/gateway-cookie-internals.go-implementer.md`): HMAC-SHA256 payload format, `routingPaths` matching logic, cookie issue/validate/invalidate lifecycle; feeds into Task 12
- [x] Task 15 — java-analyst produces complete external routing contract study (`studies/trino-gateway/external-routing-contract.java-analyst.md`): all request fields (`RoutingGroupExternalBody`) and response fields (`ExternalRouterResponse`), which `trinoQueryProperties` sub-fields are empty without `trino-parser`, `propagateErrors` fallback behavior, header-forwarding and `excludeHeaders` policy; pin the exact JSON shapes that Go HTTP + gRPC transports must replicate
- [x] Task 16 — java-analyst or go-implementer catalogs admin REST API endpoints (`studies/trino-gateway/admin-api-surface.java-analyst.md`): all routes, request/response shapes, `@RolesAllowed` per endpoint; spec for Task 20 (`internal/admin`)

## Phase 4: Implementation

Critical path: **17 → 18 → 19 → 20 → 24**. Tasks 21, 22, 23, 25 off critical path (start after 17).

### Task 17 — `internal/config` + `internal/lifecycle` ✅

- [x] `go.mod` — `go mod init github.com/hclincode/trino-goway`, pin all dependencies
- [x] `internal/config/doc.go` — package doc comment
- [x] `internal/config/config.go` — top-level `Config` struct (nested: `Proxy`, `Admin`, `Monitor`, `DB`, `Routing`, `Auth`, `Cookie`)
- [x] `internal/config/config.go` — `Load(path string) (*Config, error)` YAML loader via `gopkg.in/yaml.v3`
- [x] `internal/config/config.go` — `Duration` custom unmarshaler (accepts `"10s"`, `"1m"`, `"1h30m"`)
- [x] `internal/config/config.go` — `DataSize` custom unmarshaler (accepts `"1MiB"`, `"512KB"`)
- [x] `internal/config/config.go` — `Validate()` — `admin.port ≠ proxy.port`, `responseSize > 0`, required fields
- [x] `internal/lifecycle/doc.go` — package doc comment
- [x] `internal/lifecycle/server.go` — `Server` struct wrapping proxy + admin `*http.Server`
- [x] `internal/lifecycle/server.go` — `Start(ctx)`: `ListenAndServe` both servers concurrently, surface startup errors
- [x] `internal/lifecycle/server.go` — `Stop(ctx)`: `Shutdown` both servers respecting context deadline
- [x] `internal/config/config_test.go` — table-driven: YAML loading, Duration/DataSize parsing, validation errors
- [x] `internal/lifecycle/server_test.go` — Start/Stop lifecycle, goroutine clean (goleak)
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 18 — `internal/persistence` ✅

- [x] `internal/persistence/doc.go` — package doc
- [x] `internal/persistence/db.go` — `Open(cfg Config) (*sqlx.DB, error)` (driver-agnostic Postgres/MySQL)
- [x] `migrations/00001_create_backend_registry.sql` — `gateway_backend` table (url, name, routing_group, active, created_at, updated_at)
- [x] `migrations/00002_create_query_history.sql` — `query_history` table (query_id, backend_url, user_name, source, created_at)
- [x] `internal/persistence/backend.go` — `BackendDAO`: `List`, `Upsert`, `Delete`, `SetActive`
- [x] `internal/persistence/history.go` — `HistoryDAO`: `Insert`, `LookupByQueryID`
- [ ] `internal/persistence/backend_test.go` — integration tests (testcontainers Postgres + MySQL)
- [ ] `internal/persistence/history_test.go` — integration tests
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 19 — `internal/routing` ✅

- [x] `internal/routing/routerpb/router.proto` — `TrinoGatewayRouter` service, `RouteRequest`/`RouteResponse`/`TrinoQueryProperties`/`TrinoRequestUser` messages
- [x] `internal/routing/routerpb/` — generated Go stubs (`protoc-gen-go`, `protoc-gen-go-grpc`)
- [x] `internal/routing/external_http.go` — HTTP transport: POST `RoutingGroupExternalBody` → `ExternalRouterResponse`, `context.WithTimeout`, fallback on any error
- [x] `internal/routing/external_grpc.go` — gRPC transport: `RouteRequest` → `RouteResponse`, same fallback semantics
- [x] `internal/routing/cache.go` — LRU queryId→backend cache (`golang-lru/v2`); singleflight for concurrent miss coalescing
- [x] `internal/routing/recovery.go` — 3-step chain: cache hit → history `LookupByQueryID` → HEAD probe fan-out → first-active default
- [x] `internal/routing/router.go` — `Router.Route(ctx, r)` orchestrates external selector + recovery chain; `KILL QUERY` regex extraction routes to history backend
- [x] `internal/routing/routing_test.go` — unit tests: cache hit/miss, all 3 recovery steps, propagateErrors, HTTP/gRPC fallback
- [x] `internal/routing/external_http.go` — forward inbound request headers to routing service (excluding `excludeHeaders` + `Content-Length`); filter `excludeHeaders` keys from `externalHeaders` response (filter applied in `Router.callExternal` for both HTTP + gRPC)
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 20 — `internal/proxy` ✅

- [x] `internal/proxy/doc.go` — package doc
- [x] `internal/proxy/proxy.go` — `Proxy` struct, `ServeHTTP` dispatcher, chi route registration
- [x] `internal/proxy/forward.go` — POST `/v1/statement`: buffer upstream response (bounded by `responseSize`), extract `queryId` from `nextUri`, write cache synchronously, forward buffered body
- [x] `internal/proxy/forward.go` — KILL QUERY regex: `KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'` on request body, route to history backend, replay body via `bytes.Reader`
- [x] `internal/proxy/forward.go` — all other paths: stream via `io.Copy`, zero buffering
- [x] `internal/proxy/headers.go` — `X-Forwarded-For/Proto/Host` injection; `externalHeaders` REPLACE semantics; `excludeHeaders` filtering
- [x] `internal/proxy/cookie.go` — `TG.OAUTH2` issue/validate/invalidate (`wireCompat: true` default); HMAC-SHA256, base64.URLEncoding with padding, airlift Duration format
- [x] `internal/proxy/proxy_test.go` — seam tests: `TestProxy_Seam1_NeverRewriteResponseBody`, `TestProxy_Seam2_RedirectFollowingDisabled`, `TestProxy_Seam3_CacheWriteBeforeResponseFlush`, `TestProxy_Seam4_ThreeStepRecoveryChain`, `TestProxy_Seam6_KillQueryRegexRouting`, `TestProxy_Seam7_ThreeClientPoolIsolation`
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 21 — `internal/monitor` ✅

- [x] `internal/monitor/doc.go` — package doc
- [x] `internal/monitor/monitor.go` — `Monitor` struct, `Start`/`Stop` lifecycle
- [x] `internal/monitor/monitor.go` — per-tick fan-out: `errgroup` goroutine per backend with `context.WithTimeout`; `atomic.Pointer[map[string]TrinoStatus]` for lock-free reads
- [x] `internal/monitor/monitor.go` — `GET /v1/info` health probe; mark `PENDING`→`HEALTHY`/`UNHEALTHY`
- [x] `internal/monitor/monitor_test.go` — tick fires concurrent probes, unhealthy backends marked, goleak clean
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 22 — `internal/auth` ✅

- [x] `internal/auth/doc.go` — package doc
- [x] `internal/auth/oidc.go` — OAuth2/OIDC middleware; JWKS background refresh (`time.Ticker` + `atomic.Pointer[keyfunc.Keyfunc]`); JWT validation on every request
- [x] `internal/auth/ldap.go` — LDAP bind auth middleware (`go-ldap/ldap/v3`)
- [x] `internal/auth/noop.go` — noop pass-through middleware
- [x] `internal/auth/roles.go` — ADMIN/USER/API role resolver (regex match against principal `memberOf`)
- [x] `internal/auth/auth_test.go` — unit tests: OIDC token validation, JWKS refresh, LDAP bind, noop pass-through
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 23 — `internal/admin` ✅

- [x] `internal/admin/doc.go` — package doc
- [x] `internal/admin/router.go` — chi route registration for all 36 endpoints; middleware chain (auth → role check → handler)
- [x] `internal/admin/backend.go` — `/gateway/*` + `/entity/*` endpoints; `POST /entity?entityType=GATEWAY_BACKEND` mutates health map immediately
- [x] `internal/admin/webapp.go` — `/webapp/*` endpoints with `Result<T>` envelope; `GET /webapp/getRoutingRules` returns empty list (v1 stub)
- [x] `internal/admin/health.go` — `/trino-gateway/livez` (always 200), `/trino-gateway/readyz` (200 after SetReady)
- [x] `internal/admin/query.go` — query history endpoints; non-ADMIN callers get user-scoped results only
- [x] `internal/admin/admin_test.go` — integration tests: backend CRUD, health probes, role enforcement
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 24 — `cmd/trino-goway`

- [ ] `cmd/trino-goway/main.go` — three `*http.Client` instances (`proxyClient`, `monitorClient`, `routerClient`) with correct `CheckRedirect` config
- [ ] `cmd/trino-goway/main.go` — full composition root wiring (Tasks 17–23 constructors in dependency order)
- [ ] `cmd/trino-goway/main.go` — `//go:embed` web UI static bundle
- [ ] `cmd/trino-goway/main.go` — SIGTERM/SIGINT → graceful `Stop(ctx)` with 30s deadline
- [ ] `cmd/trino-goway/main.go` — startup log: config path, proxy port, admin port, `wireCompat` mode
- [ ] `go build ./cmd/trino-goway` produces a static binary
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 25 — `cmd/goway-migrate-config`

- [ ] `cmd/goway-migrate-config/main.go` — CLI: `--input` Java YAML path, `--output` Go YAML path
- [ ] `cmd/goway-migrate-config/migrate.go` — Java → Go field mapping for all config keys
- [ ] `cmd/goway-migrate-config/testdata/` — Java YAML fixture + expected Go YAML fixture
- [ ] `cmd/goway-migrate-config/migrate_test.go` — roundtrip tests with golden files
- [ ] `go build ./cmd/goway-migrate-config` passes
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 29 — `cmd/mock-external-router` (HTTP mock)

Stand-alone HTTP server that acts as a drop-in external routing endpoint for local
development and manual testing. Lets operators point `routing.external.url` at
`http://localhost:<port>` and watch exactly what the gateway sends.

- [ ] `cmd/mock-external-router/main.go` — `--port` flag (default 9000), `--group` flag (default `"default"`)
- [ ] Handle `POST /route` (and any other path, so it works regardless of the configured URL suffix)
- [ ] Pretty-print each incoming request body as indented JSON to stdout, prefixed with a timestamp
- [ ] Always respond `200 OK` with `Content-Type: application/json` body:
  ```json
  {"routingGroup": "<group>", "errors": [], "externalHeaders": {}}
  ```
  where `<group>` comes from `--group`.
- [ ] On bad JSON body: still print raw bytes, still return the default group (never 4xx — mirrors Java's lenient behaviour)
- [ ] `go build ./cmd/mock-external-router` produces a static binary
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 30 — `cmd/mock-external-router-grpc` (gRPC mock) **[blocked by Task 29]**

gRPC counterpart to Task 29. Implements the `TrinoGatewayRouter` service defined in
`internal/routing/routerpb/router.proto` and behaves identically: print every
`RouteRequest`, return a fixed `RouteResponse`.

- [ ] `cmd/mock-external-router-grpc/main.go` — `--addr` flag (default `:9001`), `--group` flag (default `"default"`)
- [ ] Implement `TrinoGatewayRouter.Route`: marshal `RouteRequest` to indented JSON via `protojson`, print to stdout with timestamp
- [ ] Return `RouteResponse{RoutingGroup: <group>, Errors: [], ExternalHeaders: {}}` always
- [ ] Use `google.golang.org/grpc` + `google.golang.org/protobuf/encoding/protojson` (both already in `go.mod`)
- [ ] Register a gRPC reflection service so `grpcurl` can introspect without the `.proto`
- [ ] `go build ./cmd/mock-external-router-grpc` produces a static binary
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

## Backlog

### Phase 5: QA Gates

- [x] Task 25 — `cmd/goway-migrate-config` ✅
- [x] Task 26 — Build QA infra ✅
  - [x] `internal/testutil/portalloc.go` — random available port allocator
  - [x] `internal/testutil/postgres.go` — testcontainers-go Postgres setup helper
  - [x] `internal/testutil/mysql.go` — testcontainers-go MySQL setup helper
  - [x] `internal/testutil/backend.go` — misbehaving fake Trino backend (`httptest.Server`: configurable latency, error injection, 3xx responses)
  - [x] `internal/testutil/goleak.go` — `VerifyTestMain` wrapper used by all `TestMain` functions
  - [x] `go vet ./...` + `golangci-lint run ./...` pass
- [x] Task 27 — G1 test: `nextUri` host derivation against real Trino container (`//go:build e2e`; first QA gate — only silent failure mode) — `internal/e2e/proxy_e2e_test.go::TestG1_NextURIHostDerivation`
- [ ] Task 28 — Differential harness: `cmd/goway-diff-harness/` — live Java↔Go side-by-side for proxy Seams 1–8 + statement protocol (gate to DECLARE proxy-core COMPLETE)
  - [x] **Phase 1** — `internal/diffharness/` library (scenario, normalize, diff, runner) + `cmd/goway-diff-harness/` CLI with `live`/`replay`/`record`/`report` subcommands (replay/record/report stubbed for Phase 2). 83% unit coverage, end-to-end CLI smoke passing against two httptest fakes. Smoke scenario: `seam1-body-passthrough.yaml`.
  - [x] **Phase 2** — Java gateway container bootstrap (`internal/diffharness/bootstrap.go`, `trinodb/trino-gateway:19` + Postgres + shared Trino via `testcontainers-go/network`, embedded config template at `internal/diffharness/testdata/java-gateway-config.yaml.tmpl`). `record`/`replay`/`report` subcommands wired with `Golden` on-disk format under `cmd/goway-diff-harness/testdata/golden/`. `cmd/goway-diff-harness/live_test.go` under `//go:build diff` boots the fleet + in-process Go gateway and asserts all committed scenarios PASS. Library coverage 85.2%.
  - [x] **Phase 3 scenarios** — committed 8 new YAML scenarios under `cmd/goway-diff-harness/testdata/scenarios/`: seam2-redirect-not-followed, seam3-cache-write-before-flush, seam4-router-result-handling, seam5-async-timeout, seam6-killquery-routing, seam7-cookie-emission, seam8-upstream-error, statement-protocol-roundtrip. Every diff.ignore* entry carries a `[JUSTIFIED]` comment per the normalizer-minimal discipline; enforced by `internal/diffharness/scenarios_validation_test.go::TestCommittedScenarios_LoadAndJustified`. CLI smoke tests scoped to seam1 only (the smoke fake is intentionally minimal — Phase-3 scenarios are validated end-to-end by the `//go:build diff` `live_test.go` against the real fleet). `go test -race` clean on both packages.
  - [ ] **Phase 3 remaining** — CI guidance for the `diff` build tag; qa-tech-lead normalizer sign-off; first nightly `live_test.go` execution to bake in any timing surprises and commit the resulting golden files.
