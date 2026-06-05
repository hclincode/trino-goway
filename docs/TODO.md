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

- [x] Task 9 — Discuss: Do we need a Go version of trino-gateway? (result: `docs/topics/do-we-needs-golang-trino-gateway.md` — unanimous PROCEED WITH CAVEATS)

## Phase 3: Architecture Design + Targeted Studies

- [x] Task 10 — Architect writes `phase2-gate-responses.architect.md` (library decisions, DI stance, streaming/oracle/cookie rulings, 6th hard invariant, sequencing constraints; includes ruling on gRPC in v1 vs. Non-Groomed)
- [x] Task 11 — Go-implementer writes `docs/SCOPE.md` (locked scope, deferred scope, reversal cost per item; team-lead sign-off required to change any ruling)
- [x] Task 12 — Go-implementer writes `gateway-cookies-and-sticky-routing.go-implementer.md` (cookie design: HMAC-SHA256 wire-compat with Java `GatewayCookie`, `wireCompat` config flag, `/v1/spooled/*` + `/v1/spooled/ack` sticky routing via `TG.*` cookie; required before proxy implementation starts)
- [x] Task 13 — trino-expert studies `/v1/spooled/*` URL structure in Trino source (`docs/studies/trino/spooled-segment-protocol.trino-expert.md`): token format, whether queryId is encoded, redirect chain, and whether cookie is the only viable sticky mechanism
- [x] Task 14 — go-implementer studies `GatewayCookie.java` in depth (`docs/studies/trino-gateway/gateway-cookie-internals.go-implementer.md`): HMAC-SHA256 payload format, `routingPaths` matching logic, cookie issue/validate/invalidate lifecycle; feeds into Task 12
- [x] Task 15 — java-analyst produces complete external routing contract study (`docs/studies/trino-gateway/external-routing-contract.java-analyst.md`): all request fields (`RoutingGroupExternalBody`) and response fields (`ExternalRouterResponse`), which `trinoQueryProperties` sub-fields are empty without `trino-parser`, `propagateErrors` fallback behavior, header-forwarding and `excludeHeaders` policy; pin the exact JSON shapes that Go HTTP + gRPC transports must replicate
- [x] Task 16 — java-analyst or go-implementer catalogs admin REST API endpoints (`docs/studies/trino-gateway/admin-api-surface.java-analyst.md`): all routes, request/response shapes, `@RolesAllowed` per endpoint; spec for Task 20 (`internal/admin`)

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
- [x] `internal/persistence/backend_test.go` — integration tests (testcontainers Postgres + MySQL)
- [x] `internal/persistence/history_test.go` — integration tests
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

### Task 24 — `cmd/trino-goway` ✅

- [x] `cmd/trino-goway/main.go` — three `*http.Client` instances (`proxyClient`, `monitorClient`, `routerClient`) with correct `CheckRedirect` config
- [x] `cmd/trino-goway/main.go` — full composition root wiring (Tasks 17–23 constructors in dependency order)
- [x] `cmd/trino-goway/main.go` — `//go:embed` web UI static bundle
- [x] `cmd/trino-goway/main.go` — SIGTERM/SIGINT → graceful `Stop(ctx)` with 30s deadline
- [x] `cmd/trino-goway/main.go` — startup log: config path, proxy port, admin port, `wireCompat` mode
- [x] `go build ./cmd/trino-goway` produces a static binary
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 25 — `cmd/goway-migrate-config` ✅

- [x] `cmd/goway-migrate-config/main.go` — CLI: `--input` Java YAML path, `--output` Go YAML path
- [x] `cmd/goway-migrate-config/migrate.go` — Java → Go field mapping for all config keys
- [x] `cmd/goway-migrate-config/testdata/` — Java YAML fixture + expected Go YAML fixture
- [x] `cmd/goway-migrate-config/migrate_test.go` — roundtrip tests with golden files
- [x] `go build ./cmd/goway-migrate-config` passes
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 29 — `cmd/mock-external-router` (HTTP mock) ✅

- [x] `cmd/mock-external-router/main.go` — `--port` flag (default 9000), `--group` flag (default `"default"`)
- [x] Handle `POST /route` (and any other path, so it works regardless of the configured URL suffix)
- [x] Pretty-print each incoming request body as indented JSON to stdout, prefixed with a timestamp
- [x] Always respond `200 OK` with `Content-Type: application/json` body
- [x] On bad JSON body: still print raw bytes, still return the default group (never 4xx)
- [x] `cmd/mock-external-router/main_test.go` — table-driven tests
- [x] `go build ./cmd/mock-external-router` produces a static binary
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Task 30 — `cmd/mock-external-router-grpc` (gRPC mock) ✅

- [x] `cmd/mock-external-router-grpc/main.go` — `--addr` flag (default `:9001`), `--group` flag (default `"default"`)
- [x] Implement `TrinoGatewayRouter.Route`: marshal `RouteRequest` to indented JSON via `protojson`, print to stdout with timestamp
- [x] Return `RouteResponse{RoutingGroup: <group>, Errors: [], ExternalHeaders: {}}` always
- [x] Register a gRPC reflection service so `grpcurl` can introspect without the `.proto`
- [x] `cmd/mock-external-router-grpc/main_test.go` — dial the server in-process (`bufconn`)
- [x] `go build ./cmd/mock-external-router-grpc` produces a static binary
- [x] `go vet ./...` + `golangci-lint run ./...` pass

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

### Phase 6: Team Review

Each review task produces a document in `docs/studies/`. Review tasks read the trino-goway implementation and cross-reference it against Phase 1–3 study findings. No code is written. All four tasks can run in parallel.

- [x] Task 31 — **trino-expert behavioral audit** (`docs/studies/trino-gateway/behavioral-audit.trino-expert.md`)
  - Cross-reference the actual `internal/proxy/` and `internal/routing/` implementation against behavioral contracts documented in `docs/studies/trino-gateway/architectural-intent.trino-expert.md` and `docs/studies/both/protocol-constraints-on-the-gateway.architect.md`
  - Flag any behavioral edge cases where trino-goway diverges from Java trino-gateway: header handling quirks, `nextUri` host construction, body passthrough, hop-by-hop stripping
  - Document intentional divergences (bugs fixed in Go) vs accidental gaps
  - Enumerate each behavior as: IMPLEMENTED / GAP / INTENTIONAL-DIVERGENCE, with evidence (file:line)
  - Flag which gaps are blockers for Phase 8 E2E tests vs acceptable in v1

- [x] Task 32 — **java-analyst admin API completeness audit** (`docs/studies/trino-gateway/admin-api-completeness-gap.java-analyst.md`)
  - Cross-reference every endpoint in `docs/studies/trino-gateway/admin-api-surface.java-analyst.md` against `internal/admin/` implementation
  - For each endpoint: COMPLETE (response shape matches Java wire format) / PARTIAL (exists but shape differs) / MISSING
  - Verify wire JSON shapes for `ProxyBackend`, `QueryDetail`, `TableData<T>`, and the `{code, msg, data}` webapp envelope match Java exactly
  - Identify any `@RolesAllowed` role mismatches between Java and Go
  - Output table feeds directly into Task 47 (admin E2E) and Task 48 (webapp E2E) as the authoritative checklist

- [x] Task 33 — **go-qa proxy seam gap analysis** (`docs/studies/both/proxy-seam-gap-analysis.go-qa.md`)
  - Map all 12 hard invariants from `docs/USE_STORIES.md § Hard Invariants` to existing tests in `internal/proxy/proxy_test.go`, `internal/e2e/proxy_e2e_test.go`, and `cmd/goway-diff-harness/testdata/scenarios/`
  - For each invariant: COVERED (cite test name) / PARTIALLY-COVERED (explain gap) / NOT-COVERED
  - Identify which invariants (#4 bounded buffering, #7 hop-by-hop, #8 X-Forwarded-For append, #9 externalHeaders REPLACE, #11 readyz timing, #12 three clients) have no black-box E2E test
  - Output feeds into Task 54 (hard invariants E2E) as the test specification

- [x] Task 34 — **qa-tech-lead E2E coverage gap document** (`docs/studies/both/e2e-coverage-plan.qa-tech-lead.md`)
  - Map every acceptance criterion in `docs/USE_STORIES.md` §1–§7 to: COVERED-BY-EXISTING-TEST (cite) / PLANNED-IN-TASK-N / NOT-COVERED
  - Identify acceptance criteria not verifiable via black-box (binary + HTTP) and propose white-box fallbacks
  - Confirm build-tag strategy (`//go:build e2e`) and CI integration points for Phase 8 tests
  - Sign-off document: Phase 8 may not begin until this document is committed

### Phase 7: E2E Test Infrastructure

Tasks 35–37 can start immediately (no Task 24 dependency). Task 38 is blocked by Task 24.

#### Task 35 — Extended fake Trino backend

Extends `internal/testutil/` with a Trino-protocol-aware fake that fully handles sticky-routing sequences, HEAD probe fan-out, and KILL QUERY detection. Needed by Phase 8 tests that cannot use a real Trino container.

- [x] `internal/testutil/trino_fake.go` — `TrinoFake` struct wrapping `httptest.Server`
  - `NewTrinoFake(t) *TrinoFake` — creates the server; registers `t.Cleanup(server.Close)`
  - `POST /v1/statement`: generate a valid queryId string (`<timestamp>_<seq>_<rand>_trino`); build `nextUri` using the inbound `Host` header (so `X-Forwarded-Host` rewrites propagate correctly); return Trino JSON `{id, nextUri, infoUri, stats:{state:"QUEUED"}}`; record `(queryId, requestBody, requestHeaders)`
  - `GET /v1/query/<queryId>` and any trailing path: return `{id, stats:{state:"FINISHED"}}` on first hit; record a hit per queryId
  - `HEAD /v1/query/<queryId>`: return `200 OK` if queryId was seen in a prior POST; `404` otherwise; record the HEAD probe
  - `DELETE /v1/query/<queryId>`: record the cancellation; return `200 OK`
  - `GET /v1/info`: return `{"starting":false}` by default; configurable via `SetStarting(bool)` to simulate not-yet-ready backends
  - Exported assertion helpers: `QueryIDs() []string`, `HitCount(queryId string) int`, `HeadProbes(queryId string) int`, `Cancellations() []string`, `ReceivedHeaders(queryId string) http.Header`
- [x] `internal/testutil/trino_fake_test.go` — table-driven tests for all handler paths; verify queryId format, nextUri construction, HEAD probe semantics
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 36 — Mock OIDC server

Minimal in-process OIDC server for OIDC auth E2E tests (Task 51). Serves a JWKS endpoint and issues RS256 JWTs with configurable claims.

- [x] `internal/testutil/oidc_server.go` — `OIDCServer` struct wrapping `httptest.TLSServer` (TLS required — OIDC JWKS URLs must be HTTPS in production-like configs)
  - `NewOIDCServer(t) *OIDCServer` — generates an RSA-2048 key pair in-process; starts TLS server; registers `t.Cleanup`
  - `GET /.well-known/jwks.json` — returns a single JWK entry for the signing key in standard JWKS JSON format
  - `IssueToken(sub string, groups []string, ttl time.Duration) string` — signs an RS256 JWT with `sub`, `groups` (array claim), `memberOf` (comma-joined string claim), `iss`, `exp`; returns the raw token string
  - `JWKSURL() string` — returns the HTTPS URL of the JWKS endpoint (for use in gateway config)
  - `RotateKey()` — generates a new RSA key pair and updates the JWKS response; old tokens become invalid after the gateway refreshes its keyfunc
- [x] `internal/testutil/oidc_server_test.go` — verify issued tokens validate against the JWKS; verify key rotation causes old tokens to reject; verify TLS cert is trusted by the test client
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 37 — Mock LDAP server

Minimal in-process LDAP server for LDAP auth E2E tests (Task 52). Supports bind auth and `memberOf` attribute lookup.

- [x] `internal/testutil/ldap_server.go` — `LDAPServer` struct
  - Use `github.com/glauth/glauth/v2` embedded or `github.com/nmcclain/ldap` in-process server; seed with configurable user entries: `{DN string, Password string, MemberOf []string}`
  - `NewLDAPServer(t, users []LDAPUser) *LDAPServer` — starts in-process LDAP; binds on a free port; registers `t.Cleanup`
  - `Addr() string` — returns `host:port` for use in gateway config (`auth.ldap.url: ldap://<addr>`)
  - `BindDN() string`, `BindPassword() string` — returns the service-account credentials seeded at construction
  - `UserBase() string` — returns the base DN for user search
- [x] `internal/testutil/ldap_server_test.go` — verify bind succeeds for known users, fails for bad password, returns memberOf correctly
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 38 — Full-stack E2E binary harness **[blocked by Task 24]**

Launches `trino-goway` as a subprocess (not in-process), wires Postgres via testcontainers, registers `TrinoFake` backends, waits for `/trino-gateway/readyz`, and exposes typed clients for proxy and admin ports. This is the canonical black-box harness for all Phase 8 tests.

- [x] `internal/e2e/harness/harness.go` — `Harness` struct
  - `New(t *testing.T, opts ...HarnessOption) *Harness` — starts Postgres container (testcontainers), runs `goose up`, writes a temp config YAML, execs `trino-goway --config <path>`, polls `/trino-gateway/readyz` (30 s deadline → `t.Fatal`); registers `t.Cleanup` (SIGTERM subprocess → wait 5 s → SIGKILL if needed → terminate containers)
  - `HarnessOption` functional options: `WithExternalHTTPRouter(url)`, `WithExternalGRPCRouter(addr)`, `WithAuth(authCfg)`, `WithCookieSecret(secret)`, `WithResponseSize(bytes)`, `WithMonitorInterval(d)` — each writes the relevant config section into the temp YAML
  - `ProxyURL() string` — `http://localhost:<proxyPort>`
  - `AdminURL() string` — `http://localhost:<adminPort>`
  - `ProxyClient() *http.Client` — `CheckRedirect: ErrUseLastResponse`; no auth
  - `AdminClient(bearerToken string) *http.Client` — injects `Authorization: Bearer <token>` on every request (bearer-token param is `""` for NOOP auth)
  - `AddBackend(t, name, group string) *testutil.TrinoFake` — starts a `TrinoFake`; calls `POST /entity?entityType=GATEWAY_BACKEND` on the admin port; polls until `GET /gateway/backend/all` shows the backend `HEALTHY` (15 s deadline)
  - `BinaryPath()` — resolved at startup from env `TRINO_GOWAY_BIN` or `./trino-goway` in the same directory as the test binary
- [x] `internal/e2e/harness/harness_test.go` (`//go:build e2e`) — smoke test: harness starts; proxy returns non-error response to a minimal request; `/trino-gateway/readyz` returns 200; cleanup exits cleanly; `goleak.VerifyTestMain`
- [x] `//go:build e2e` on all harness files
- [x] `go vet ./...` + `golangci-lint run ./...` pass

### Phase 8: E2E Tests (black-box via HTTP interface)

All Phase 8 tests carry `//go:build e2e`, are in `internal/e2e/`, use the `Harness` from Task 38, and treat `trino-goway` as a black box. All tests are blocked by Task 38. Tasks 51–52 additionally require Tasks 36–37.

#### Task 39 — E2E: Trino proxy protocol (USE_STORIES §1.1, §1.2, §1.6)

- [x] `internal/e2e/proxy_protocol_e2e_test.go`
- [x] `TestE2E_PostStatement_RoutesToBackend` — `POST /v1/statement` forwarded to registered backend; gateway returns 200 with valid Trino JSON `{id, nextUri}`; request body reaches backend verbatim (Hard Invariant #1)
- [x] `TestE2E_PostStatement_StickyRouting` — after first `POST /v1/statement`, subsequent `GET /v1/query/<queryId>` requests land on the same backend (`TrinoFake.HitCount` asserted) not any other backend
- [x] `TestE2E_PostStatement_ResponseBufferingCap` — backend returns body larger than `proxy.responseSize` → gateway returns `502 Bad Gateway` with body `upstream response too large`
- [x] `TestE2E_PostStatement_NoBackendAvailable` — no active backends registered → gateway returns `502 Bad Gateway` with body `no backend available`
- [x] `TestE2E_StreamingPath_NotBuffered` — `GET /v1/query/<id>` with a large backend response passes through intact; no 502; response body bytes match backend bytes (Hard Invariant #4)
- [x] `TestE2E_ForwardedHeaders_XForwardedHost` — backend receives `X-Forwarded-Host` matching the client's `Host` header (§1.6)
- [x] `TestE2E_ForwardedHeaders_XForwardedForAppends` — send request with existing `X-Forwarded-For: 1.2.3.4`; backend sees `1.2.3.4, <clientIP>` (Hard Invariant #8)
- [x] `TestE2E_HopByHopStripped` — request carrying `Connection: keep-alive` and `Transfer-Encoding: chunked`; backend does NOT receive those headers (Hard Invariant #7) — split into `_RequestDirection` and `_ResponseDirection` per go-qa gap analysis (both client→upstream and upstream→client must be covered)
- [x] `go vet -tags=e2e ./internal/e2e/...` passes

#### Task 40 — E2E: KILL QUERY routing (USE_STORIES §1.3, Hard Invariant #6)

- [x] `internal/e2e/kill_query_e2e_test.go`
- [x] `TestE2E_KillQuery_RoutesToOwnerBackend` — run a query on backend-A (records to query history); send `POST /v1/statement` with body `KILL QUERY '<queryId>'` while backend-B is the routing-group selection → request lands on backend-A, NOT backend-B; assert via `TrinoFake.HitCount`
- [x] `TestE2E_KillQuery_Lowercase` — `kill query` (lowercase) triggers the same routing behavior
- [x] `TestE2E_KillQuery_UnknownId` — queryId not in history → falls through to normal routing without error; no 502
- [x] `go vet -tags=e2e ./internal/e2e/...` passes

#### Task 41 — E2E: 3-step cache-miss recovery chain (USE_STORIES §1.4)

- [x] `internal/e2e/recovery_chain_e2e_test.go`
- [x] `TestE2E_Recovery_HistoryLookup` — submit a query (writes cache/history); subsequent `GET /v1/query/<queryId>` routed to original backend
- [x] `TestE2E_Recovery_HEADProbeFanout` — queryId unknown to cache AND history; backends placed in non-default groups so recovery chain fires; both fakes record a HEAD probe; falls back to first-active when all probes 404
- [x] `TestE2E_Recovery_FirstActiveFallback` — queryId unknown everywhere; first active backend selected; no 404 returned to client
- [x] `TestE2E_StatementPolls_BypassCache` (qa-tech-lead §1.2c) — `/v1/statement/<id>/executing/...` polls are forwarded by handleStream, not gated on cache hit
- [x] `go vet -tags=e2e ./internal/e2e/...` passes

#### Task 42 — E2E: External HTTP routing (USE_STORIES §2.1, HTTP transport) ✅

- [x] `internal/e2e/external_http_routing_e2e_test.go`
- [x] Uses inline `httptest.Server` replicating the `cmd/mock-external-router` contract
- [x] `TestE2E_ExternalHTTP_RoutingGroupUsed` — router returns `{"routingGroup":"etl"}`; backend in group `etl` receives request; backend in group `default` does not
- [x] `TestE2E_ExternalHTTP_ExternalHeadersReplace` — router returns `{"externalHeaders":{"X-Custom":"from-router"}}`; backend sees `X-Custom: from-router`; if client also sent `X-Custom: original`, only `from-router` value arrives (REPLACE semantics, Hard Invariant #9)
- [x] `TestE2E_ExternalHTTP_ExcludeHeaders` — `routing.external.excludeHeaders: ["X-Secret"]`; router request does NOT contain `X-Secret` from inbound; router response `externalHeaders` with `X-Secret` NOT injected upstream
- [x] `TestE2E_ExternalHTTP_FallbackOnRouterDown` — router URL points to a closed port; request still succeeds via `defaultGroup` fallback; no 502 to client
- [x] `TestE2E_ExternalHTTP_PropagateErrors` — router returns `{"errors":["access denied"]}`; config `propagateErrors: true`; client gets `400 Bad Request`
- [x] `TestE2E_ExternalHTTP_TimeoutFallback` — router endpoint delays beyond `routing.external.timeout`; request still served (fallback), no hang
- [x] `go vet ./...` pass

#### Task 43 — E2E: External gRPC routing (USE_STORIES §2.1, gRPC transport) ✅

- [x] `internal/e2e/external_grpc_routing_e2e_test.go`
- [x] In-process gRPC server bound to a real localhost TCP port (gateway subprocess reaches it over the wire)
- [x] `TestE2E_ExternalGRPC_RoutingGroupUsed` — gRPC router returns `routingGroup=etl`; request lands on `etl` backend
- [x] `TestE2E_ExternalGRPC_FallbackToHTTP` — gRPC addr configured but unreachable; HTTP url configured and reachable; gateway falls back to HTTP transport and succeeds
- [x] `TestE2E_ExternalGRPC_FallbackOnBothDown` — both gRPC and HTTP unreachable; `defaultGroup` fallback serves request
- [x] `TestE2E_ExternalGRPC_RouteRequestEquivalence` — RouteRequest method, request_uri, and trino_request_user.user populated from inbound headers
- [x] `go vet ./...` pass

#### Task 44 — E2E: Routing groups and single-cluster mode (USE_STORIES §2.2, §2.3) ✅

- [x] `internal/e2e/routing_groups_e2e_test.go`
- [x] `TestE2E_RoutingGroup_SteeringByGroup` — two backends in different groups (`adhoc`, `etl`); router returns `etl`; only `etl` backend receives requests
- [x] `TestE2E_RoutingGroup_RecoveryWhenGroupEmpty` — router returns a group with no healthy backends; recovery chain runs; first active backend (in any group) serves request
- [x] `TestE2E_SingleCluster_NoExternalRouter` — harness started with no `routing.external.url` or `grpcAddr`; every request routes to `defaultGroup`; no 502
- [x] `go vet ./...` pass

#### Task 45 — E2E: Backend health monitoring (USE_STORIES §3.1, §3.2) ✅

- [x] `internal/e2e/health_monitoring_e2e_test.go`
- [x] `TestE2E_Monitor_HealthyBackend` — `TrinoFake` returns `{"starting":false}` on `/v1/info`; after one monitor interval, admin API reports backend `HEALTHY`
- [x] `TestE2E_Monitor_UnhealthyBackend` — `TrinoFake.SetStarting(true)` → `/v1/info` returns `{"starting":true}`; monitor marks backend `UNHEALTHY`; routing skips it (request falls to other backend)
- [x] `TestE2E_Monitor_TransportError` — backend closed mid-test; `/v1/info` returns connection error; monitor marks `UNHEALTHY` within one probe interval
- [x] `TestE2E_Monitor_NewlyAddedBackend` — `POST /entity?entityType=GATEWAY_BACKEND`; immediately `GET /webapp/getAllBackends` shows backend with status `PENDING`; after probe interval, status transitions to `HEALTHY`
- [x] `TestE2E_Monitor_DeactivatedBackend` — `POST /gateway/backend/deactivate/{name}`; backend excluded from routing immediately (no requests reach it); status shown as `UNHEALTHY` in admin API
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 46 — E2E: Liveness and readiness probes (USE_STORIES §3.3, Hard Invariant #11) ✅

- [x] `internal/e2e/probes_e2e_test.go`
- [x] `TestE2E_Livez_AlwaysOK` — `GET /trino-gateway/livez` returns `200 ok` immediately after startup and after probe cycle
- [x] `TestE2E_Readyz_503BeforeFirstProbe` — harness started with `WithSkipReadyzWait()` + long `monitor.interval`; `GET /trino-gateway/readyz` returns `503 not ready` before any probe fires
- [x] `TestE2E_Readyz_200AfterFirstProbe` — harness with short monitor interval; poll `/trino-gateway/readyz` until `200` (15 s deadline); assert it transitions to 200 after first probe
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 47 — E2E: Admin CRUD API (USE_STORIES §4.1, §4.2)

- [x] `internal/e2e/admin_crud_e2e_test.go`
- [x] `TestE2E_Admin_BackendListEmpty` — `GET /gateway/backend/all` returns `[]` initially
- [x] `TestE2E_Admin_BackendAddActivateDeactivateDelete` — full lifecycle: add via `POST /gateway/backend/modify/add`; list shows it; `POST /gateway/backend/activate/{name}`; `GET /gateway/backend/active` includes it; `POST /gateway/backend/deactivate/{name}`; active list excludes it; `POST /gateway/backend/modify/delete` (raw name body); list is empty again
- [x] `TestE2E_Admin_BackendWireShape` — backend JSON has exactly `{name, proxyTo, externalUrl, active, routingGroup}`; no extra fields; all required fields present
- [x] `TestE2E_Admin_EntityAPI_AddAndList` — `POST /entity?entityType=GATEWAY_BACKEND` with backend JSON → `GET /entity/GATEWAY_BACKEND` returns it
- [x] `TestE2E_Admin_EntityAPI_ListTypes` — `GET /entity` returns `["GATEWAY_BACKEND"]`
- [x] `TestE2E_Admin_EntityAPI_UnknownType` — `POST /entity?entityType=WIDGETS` returns `500` (mirror Java behavior)
- [x] `TestE2E_Admin_EntityAPI_SeedsMonitorStatus` — `POST /entity` with `active:true` backend; immediate `POST /webapp/getAllBackends` shows status `PENDING` (not absent)
- [x] `TestE2E_Admin_PublicBackends_NoAuth` — `GET /api/public/backends` returns backends without any `Authorization` header
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 48 — E2E: Webapp endpoints (USE_STORIES §4.3)

- [x] `internal/e2e/webapp_e2e_test.go`
- [x] `TestE2E_Webapp_ResponseEnvelope` — all `/webapp/*` responses have `{code:200, msg:"Successful.", data:...}` on success and `{code:500, msg:"<reason>", data:null}` on error
- [x] `TestE2E_Webapp_GetAllBackends` — `POST /webapp/getAllBackends` returns backends with live `status` field (`"HEALTHY" | "UNHEALTHY" | "PENDING"`)
- [x] `TestE2E_Webapp_GetDistribution` — `POST /webapp/getDistribution` returns all required fields: `totalBackendCount`, `onlineBackendCount`, `offlineBackendCount`, `healthyBackendCount`, `unhealthyBackendCount`, `totalQueryCount`, `startTime` (ISO-8601)
- [x] `TestE2E_Webapp_GetUIConfiguration` — `POST /webapp/getUIConfiguration` returns `{authType}` matching configured auth mode
- [x] `TestE2E_Webapp_FindQueryHistory` — `POST /webapp/findQueryHistory` returns `TableData<QueryDetail>` shape; non-ADMIN caller's `userName` filter forced to own identity server-side
- [x] `TestE2E_Webapp_RoutingRulesStubs` — `POST /webapp/getRoutingRules` returns empty list; `POST /webapp/updateRoutingRules` returns success envelope
- [x] `TestE2E_Webapp_RoleEnforcement` — endpoints requiring `USER` or `ADMIN` role return `403` for a principal with no roles (NOOP auth, no role regex configured)
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 49 — E2E: Query history (USE_STORIES §4.4)

- [x] `internal/e2e/query_history_e2e_test.go`
- [x] `TestE2E_History_RecordedAfterStatement` — `POST /v1/statement` with `X-Trino-User: alice`; `GET /trino-gateway/api/queryHistory` (ADMIN auth) returns record with correct `backendUrl`, `queryId`, `userName: alice`
- [x] `TestE2E_History_AdminSeesAllUsers` — two queries by `alice` and `bob`; ADMIN caller sees both records
- [x] `TestE2E_History_UserScopedToOwn` — `alice` calls `GET /trino-gateway/api/queryHistory`; only sees own records even when passing `?userName=bob` query param
- [x] `TestE2E_History_Distribution` — `GET /trino-gateway/api/queryHistoryDistribution` returns `{backendUrl: count}` map with correct counts
- [x] `TestE2E_History_ActiveBackends` — `GET /trino-gateway/api/activeBackends` returns active backends in legacy wire format
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 50 — E2E: NOOP auth and role enforcement (USE_STORIES §5.1 noop, §5.2, §4.5)

- [x] `internal/e2e/auth_noop_e2e_test.go`
- [x] `TestE2E_NOOP_ProxyPortNoAuth` — proxy port accepts requests without any `Authorization` header; Trino request forwarded normally
- [x] `TestE2E_NOOP_AdminAnonymousPrincipal` — admin port with NOOP auth, no role regex configured; all role-protected endpoints return `403` (covered by `TestE2E_NOOP_AdminDeniedWithoutRegex`)
- [x] `TestE2E_NOOP_RoleGrantedByRegex` — configure `auth.authorization.admin: ".*"` (matches `anonymous`); ADMIN-only endpoints return `200` (covered by `TestE2E_NOOP_AdminGrantedByRegex`)
- [x] `TestE2E_Role_403OnInsufficientRole` — USER-role principal (matched by `auth.authorization.user` regex) calls ADMIN-only endpoint → `403 {"error":"forbidden"}`
- [x] `TestE2E_Userinfo_ReturnsRoles` — `POST /userinfo` returns `{userId, userName, roles, permissions}` with correct role list for the authenticated principal
- [x] `TestE2E_LoginType_ReportsNOOP` — `POST /loginType` returns auth type `none` when NOOP is configured
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 51 — E2E: OIDC auth (USE_STORIES §5.1 OIDC, §5.3) **[blocked by Tasks 36, 38]**

- [x] `internal/e2e/auth_oidc_e2e_test.go`
- [x] `TestE2E_OIDC_ValidToken_Admitted` — issue JWT via `OIDCServer.IssueToken`; send as `Authorization: Bearer <token>` on ADMIN-protected endpoint → `200`
- [x] `TestE2E_OIDC_InvalidToken_401` — malformed token; expired token; token signed by wrong key → all return `401` with `WWW-Authenticate: Bearer`
- [x] `TestE2E_OIDC_GroupsClaimMapsToRole` — JWT has `groups: ["platform-admin"]`; config `auth.authorization.admin: "platform-admin"`; caller gets ADMIN role; `POST /userinfo` confirms
- [x] `TestE2E_OIDC_JWKSRefresh` — `OIDCServer.RotateKey()`; wait for `jwksTtlSecs`; token issued with old key is rejected; token with new key is accepted
- [x] `TestE2E_OIDC_MissingJwksUrl_StartupFails` — start harness with `auth.type: OIDC` but no `jwksUrl`; subprocess exits non-zero; stderr contains config validation error
- [x] `TestE2E_OIDC_UnreachableJWKS_StartupFails` — gap §5.3b: `auth.type: OIDC` with `jwksUrl` pointing to a refused endpoint → subprocess exits non-zero
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 52 — E2E: LDAP auth (USE_STORIES §5.1 LDAP, §5.4) **[blocked by Tasks 37, 38]**

- [x] `internal/e2e/auth_ldap_e2e_test.go`
- [x] `TestE2E_LDAP_ValidCredentials_Admitted` — HTTP Basic with known user/password → `200` on ADMIN-protected endpoint
- [x] `TestE2E_LDAP_InvalidCredentials_401` — wrong password → `401 {"error":"..."}`
- [x] `TestE2E_LDAP_MemberOfMapsToRole` — user DN has `memberOf: cn=platform-admin,...`; config regex matches; caller gets ADMIN role
- [x] `TestE2E_LDAP_MissingUrl_StartupFails` — `auth.type: LDAP` without `url` → subprocess exits non-zero; config validation error in stderr
- [x] `TestE2E_LDAP_MissingUserBase_StartupFails` — `auth.type: LDAP` with `url` but no `userBase` → subprocess exits non-zero
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 53 — E2E: Gateway cookies (USE_STORIES §1.5, Hard Invariants #5, #10) **[blocked by Tasks 36, 38]**

- [x] `internal/e2e/cookie_e2e_test.go`
- [x] `TestE2E_Cookie_IssuedOnOAuth2Path` — `cookie.secret` non-empty; first request to `/oauth2/authorize` without cookie → response contains `Set-Cookie: TG.OAUTH2=...` with `HttpOnly`, `SameSite=Lax`, `Path=/`, `Max-Age` attributes
- [ ] `TestE2E_Cookie_StickyRouting` — second request with valid `TG.OAUTH2` cookie → routed to backend pinned by cookie, not by external router (deferred: current implementation does not yet honor cookie backend pin during routing)
- [x] `TestE2E_Cookie_ExpiryEmitsDeleteCookie` — send request with expired `TG.OAUTH2` cookie → response contains `Set-Cookie: TG.OAUTH2=; Max-Age=0`; request is still served (not 401)
- [x] `TestE2E_Cookie_TamperedHMAC_Returns500` — send request with `TG.OAUTH2` cookie where HMAC is corrupted → `500` (Hard Invariant #5); never silently treated as anonymous
- [ ] `TestE2E_Cookie_LogoutPath_DeletesCookie` — request to `/logout` or `/oauth2/logout` with valid cookie → delete-cookie emitted (covered by unit `TestCookie_DeleteOnLogout` / `TestCookie_DeleteOnOAuth2Logout` in `internal/proxy/cookie_test.go`; E2E omitted from this batch)
- [x] `TestE2E_Cookie_EmptySecret_NeverEmits` — `cookie.secret` empty → no `Set-Cookie: TG.OAUTH2` on any response; no cookie validation attempted
- [x] `TestE2E_Cookie_WireCompat_GoldenBytes` — `cookie.wireCompat: true`; issue a cookie; decode base64; verify JSON payload field order and HMAC input match golden file at `testdata/cookie_wire_compat.golden` (Hard Invariant #10) — golden pinned by Go implementation (key-sorted; runtime fields normalized to `<runtime>`); replace with Java-captured shape in follow-up
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 54 — E2E: Hard invariants black-box (all 12 from docs/USE_STORIES.md)

One test per invariant, verifiable solely through the HTTP interface. Informed by the gap analysis from Task 33.

- [x] `internal/e2e/hard_invariants_e2e_test.go`
- [x] `TestE2E_Inv1_NoBodyRewriting` — backend serves a known byte sequence for `/v1/statement`; client response body matches byte-for-byte; no mutation
- [x] `TestE2E_Inv2_NoRedirectFollowing` — backend returns `301 Location: http://other`; client receives `301`, NOT the redirect target's response
- [x] `TestE2E_Inv3_CacheWriteBeforeFlush` — two backends; POST + immediate sticky GET must land on same backend (proves cache write precedes response flush)
- [x] `TestE2E_Inv4_BoundedBuffering_OnlyStatement` — streaming-path backend returns 2 MiB body (`responseSize` is 1 MiB); gateway streams it through, no `502`; only `/v1/statement` is buffered
- [ ] `TestE2E_Inv5_TamperedCookieIs500` — covered by Task 53 `TestE2E_Cookie_TamperedHMAC_Returns500` (cross-reference)
- [ ] `TestE2E_Inv6_KillQueryByID` — covered by Task 40 (cross-reference)
- [x] `TestE2E_Inv7_HopByHopStripped_BothDirections` — both request and response directions verified in one test (closes go-qa gap analysis "both directions" finding)
- [x] `TestE2E_Inv8_XForwardedForAppends` — minimal inline assertion; substantive coverage in Task 39 `TestE2E_ForwardedHeaders_XForwardedForAppends`
- [x] `TestE2E_Inv9_ExternalHeadersReplace` — mock router injects `X-Custom: router-value`; client `X-Custom: client-value` is replaced (single-value REPLACE)
- [ ] `TestE2E_Inv10_CookieWireCompat` — covered by Task 53 `TestE2E_Cookie_WireCompat_GoldenBytes` (cross-reference)
- [x] `TestE2E_Inv11_ReadyzRequiresProbe` — minimal inline assertion; substantive coverage in Task 46 `TestE2E_Readyz_503BeforeFirstProbe`
- [x] `TestE2E_Inv12_ThreeHTTPClients_BehavioralSaturation` — slow router (500ms) + 20 concurrent proxy POSTs; assert `/trino-gateway/livez` responds <200ms under saturation
- [x] `go vet ./...` + `golangci-lint run ./...` pass

#### Task 55 — E2E: Java ↔ Go parity scenarios (extends Task 28 diff harness)

Extends the committed diff-harness scenario corpus to cover admin API, routing, and history behaviors not yet compared against the Java gateway.

- [x] Add 6 new scenario YAMLs under `cmd/goway-diff-harness/testdata/scenarios/`:
  - [x] `admin-backend-crud.yaml` — add/activate/deactivate/delete backend via `/gateway/*`; diff wire JSON shape at each step
  - [x] `external-routing-headers.yaml` — router injects `externalHeaders`; backend receives headers; diff upstream request seen by backend
  - [x] `kill-query-routing.yaml` — submit query; send `KILL QUERY '<id>'`; assert routed to same backend as original query
  - [x] `recovery-chain-history.yaml` — query recorded in history; new request for same queryId after cache clear → history-lookup routes correctly
  - [x] `health-probe-unhealthy.yaml` — mark backend unhealthy; submit request; verify unhealthy backend excluded
  - [x] `query-history-scoping.yaml` — two users submit queries; each sees only own records via webapp `findQueryHistory`
- [x] Every `diff.ignore*` entry in new scenarios carries a `[JUSTIFIED]` comment
- [x] All new scenarios pass `internal/diffharness/scenarios_validation_test.go::TestCommittedScenarios_LoadAndJustified`
- [ ] All new scenarios pass in `cmd/goway-diff-harness/live_test.go` under `//go:build diff` (deferred: requires Docker fleet bootstrap; gated by Tasks 38/42-44 wiring)
- [x] `go vet ./...` pass; `golangci-lint run ./...` not run in this task

---

## Phase 9: Prometheus Metrics & Observability

Realizes the `docs/PRD.md` locked decision "Metrics = `prometheus/client_golang`", which is currently **unimplemented** — there is no `/metrics` route and the dependency is absent (see `docs/topics/gateway-docs-compatibility-audit.md` §3.2).

**Baseline:** the Java gateway exposes an OpenMetrics endpoint at `/metrics` (`io.airlift:openmetrics` + `JmxOpenMetricsModule`, `HaGatewayLauncher`) carrying JVM/platform metrics **plus** a small set of application metrics: `ProxyHandlerStats.requestCount` (`CounterStat`), per-backend `ClusterMetricsStats.getActivationStatus` (gauge `1`/`0`/`-1`), and per-backend `TrinoStatus` health (`{cluster}_TrinoStatusHealthy`/`Unhealthy`/`Pending`, asserted in `TestGatewayHaMultipleBackend.testClusterStatsJMX`).

**Principle for this phase:**

- **Exclude JVM/Java-runtime metrics** (heap regions, GC pauses, classloader, JIT, thread pools) and **replace them with Go equivalents** — goroutines, Go GC, heap/alloc, process CPU/RSS/open-FDs — via the standard `prometheus/client_golang` collectors.
- **Mirror the gateway's application metrics** (proxied requests, per-backend activation + health status), then expand idiomatically (HTTP server, routing/recovery, auth, persistence).
- Serve OpenMetrics text on the **admin** listener (keeps scrape traffic off the proxy hot path), behind a config toggle, default path `/metrics`.
- Naming: `trino_goway_*` namespace, Prometheus-idiomatic names + labels (not JMX-derived). Task 64 documents the Java→Go name mapping for dashboard migration.

Every task carries `go vet ./...` + `golangci-lint run ./...` and unit tests; end-to-end exposure is verified by a scrape test (Task 63). No global registry — explicit constructor wiring per `docs/PRD.md` §Key Architecture Decisions.

### Task 56 — Metrics infrastructure + `/metrics` endpoint

- [ ] Add `github.com/prometheus/client_golang` to `go.mod`
- [ ] `internal/metrics/doc.go` — package doc
- [ ] `internal/metrics/registry.go` — own `*prometheus.Registry` (not the global default); `Handler()` via `promhttp.HandlerFor(reg, promhttp.HandlerOpts{EnableOpenMetrics: true})`
- [ ] `internal/config/config.go` — `MetricsConfig{Enabled bool (default true), Path string (default "/metrics")}` under a new `metrics:` node; extend `Validate()`
- [ ] Mount the metrics route on the **admin** server (`internal/admin`/`internal/lifecycle`); when `enabled=false`, do not register (route returns 404)
- [ ] `cmd/trino-goway/main.go` — explicit construction + injection of the registry into components that record metrics
- [ ] `internal/metrics/registry_test.go` — handler 200 + `Content-Type: application/openmetrics-text...`; disabled → not registered
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 57 — Go runtime + process collectors (JVM-metric replacement)

- [ ] Register `collectors.NewGoCollector()` (goroutines, threads, Go GC, heap/alloc/objects) on the registry
- [ ] Register `collectors.NewProcessCollector(...)` (process CPU seconds, resident/virtual memory, open FDs, start time)
- [ ] Code comment documenting these as the Go equivalent of the Java gateway's excluded JVM/process metrics
- [ ] Test: `go_goroutines`, `go_gc_*`, `process_*` families present in scrape output
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 58 — HTTP server metrics middleware (jetty/http-server equivalent)

- [ ] `internal/metrics/httpmw.go` — chi middleware recording `trino_goway_http_requests_total{listener,method,code}`, `trino_goway_http_request_duration_seconds` (histogram, by `listener,method`), `trino_goway_http_requests_in_flight{listener}`
- [ ] Mount on both proxy and admin listeners with a `listener` label (`proxy`/`admin`); use route patterns (not raw paths) to bound cardinality
- [ ] Test: counters increment, duration observed, in-flight gauge balances to zero
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 59 — Proxy + forwarding metrics (mirrors `ProxyHandlerStats`)

- [ ] `trino_goway_proxy_requests_total{backend,routing_group,outcome}` — `outcome` ∈ `ok|fallback|error|kill_query` (superset of Java `requestCount`)
- [ ] `trino_goway_proxy_upstream_duration_seconds` histogram (label `backend`)
- [ ] `trino_goway_proxy_oversized_responses_total` — the 502 fail-loud path (`internal/proxy/forward.go`)
- [ ] `trino_goway_proxy_statement_cache_writes_total` — sticky-cache writes (Hard Invariant #3)
- [ ] Inject a nil-safe metrics recorder interface into `internal/proxy` (consumer-owned, same pattern as `HistoryRecorder`)
- [ ] Test against the recorder; nil recorder is a no-op
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 60 — Backend health + activation metrics (mirrors `ClusterMetricsStats` + `TrinoStatus`)

- [ ] `trino_goway_backend_status{backend,status}` gauge encoding `HEALTHY|UNHEALTHY|PENDING` (mirror `{cluster}_TrinoStatus*`)
- [ ] `trino_goway_backend_activation_status{backend}` gauge `1`/`0`/`-1` (mirror `ClusterMetricsStats.getActivationStatus`)
- [ ] `trino_goway_backends{status}` and `trino_goway_backends_active` aggregate gauges
- [ ] Source from `internal/monitor` status map; register/unregister per-backend series as backends are added/removed — avoid stale series (mirror `ClusterMetricsStatsExporter` lifecycle)
- [ ] Test: gauges track monitor status transitions; removed-backend series are cleaned up
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 61 — Routing + recovery-chain metrics

- [ ] `trino_goway_router_calls_total{transport,outcome}` — `transport` ∈ `http|grpc`; `outcome` ∈ `ok|error|timeout|fallback`
- [ ] `trino_goway_router_call_duration_seconds{transport}` histogram
- [ ] `trino_goway_routing_cache_events_total{event}` — `hit|miss`
- [ ] `trino_goway_recovery_chain_steps_total{step}` — `history|probe|default` (Hard Invariant #4 observability)
- [ ] `trino_goway_kill_query_routes_total` (Hard Invariant #6)
- [ ] Instrument `internal/routing` (`external_http.go`, `external_grpc.go`, `cache.go`, `recovery.go`, `router.go`)
- [ ] Test
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 62 — Auth + persistence metrics

- [ ] Auth: `trino_goway_auth_requests_total{type,result}` (`type` ∈ `oidc|ldap|noop`; `result` ∈ `allow|deny`), `trino_goway_jwks_refresh_total{result}`, `trino_goway_jwks_keys` gauge (observability for the JWKS-caching fix)
- [ ] Persistence: `trino_goway_db_up` gauge, `trino_goway_query_history_inserts_total{result}`, `trino_goway_backend_refresh_total{result}` (the 15s reload loop in `cmd/trino-goway/main.go`)
- [ ] Instrument `internal/auth`, `internal/persistence`, and the backend-refresh loop
- [ ] Test
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 63 — E2E: `/metrics` scrape

- [ ] `internal/e2e/metrics_e2e_test.go` (`//go:build e2e`)
- [ ] `TestE2E_Metrics_Endpoint_Scrape` — GET admin `/metrics` → 200, OpenMetrics content-type, parses cleanly with `prometheus/common/expfmt`
- [ ] `TestE2E_Metrics_GoRuntimeFamilies` — `go_goroutines` + `process_*` present
- [ ] `TestE2E_Metrics_AppFamilies` — after a registered backend + a proxied request: `trino_goway_proxy_requests_total` and `trino_goway_backend_status` present with expected labels
- [ ] `TestE2E_Metrics_Disabled` — `metrics.enabled=false` → `/metrics` returns 404
- [ ] `goleak` clean
- [ ] `go vet -tags e2e ./internal/e2e/...` pass

### Task 64 — Docs, config, and PRD/SCOPE reconciliation

- [ ] `configs/config.example.yaml` — documented `metrics:` block (enabled, path)
- [ ] `README.md` — metrics section + Prometheus `scrape_configs` example targeting the admin port
- [ ] `docs/PRD.md` — mark the metrics decision as implemented
- [ ] `docs/SCOPE.md` — add "Prometheus metrics endpoint" to §1 Locked In Scope (requires team-lead sign-off per §5; this phase + the audit doc are the written rationale)
- [ ] `docs/topics/gateway-docs-compatibility-audit.md` — mark §3.2 resolved
- [ ] Java→Go metric-name mapping table (in the audit doc or a new reference) for dashboard migration
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

---

## Phase 10: Web UI backend support (webapp dependencies)

Go-side work the rebuilt web UI (`webapp/`, modern React) depends on. These realize already-in-scope features (Web UI, admin API, OIDC) — bug-fixes/completions, not new scope (no SCOPE §5 sign-off needed). The rebuilt frontend degrades gracefully without them (see `webapp/docs/PRD.md` §API reconciliation); each closes a gap in `docs/topics/gateway-docs-compatibility-audit.md`. Hand-off surfaced by the frontend analysis (`webapp/docs/studies/webapp-api-and-data-model.md`).

### Task 65 — Serve the real UI bundle + SPA fallback
- [ ] Replace the `cmd/trino-goway/web/dist` placeholder by embedding the `webapp` production build output (define the build→embed wiring; the frontend builds with base path `/trino-gateway/`)
- [ ] Wire `adminUIFS` (currently `_ = adminUIFS`, `main.go:157`) into `serveIndex`/`serveAssets`; implement `serveAssets` (currently a 404 stub, `internal/admin/router.go:213`) to serve embedded static assets
- [ ] SPA fallback: serve `index.html` for unknown GET sub-paths under the `/trino-gateway` base (browser-router deep links) — without shadowing real API routes
- [ ] Tests; `go vet ./...` + `golangci-lint run ./...` pass

### Task 66 — Complete the Web-UI OAuth2 login flow (audit §3.3)
- [ ] Implement `/sso` (initiate redirect) and `/oidc/callback` (currently 501, `authhandlers.go:72`) with the `token` cookie handoff the UI consumes on mount
- [ ] Tests; vet + lint pass

### Task 67 — Populate `externalUrl` in query history (audit §3.7 / M5)
- [ ] Add `external_url` to the query-history schema (or resolve via backend join); set on capture; emit `QueryDetail.externalUrl` on the wire so QueryId deeplinks + RoutedTo render
- [ ] Tests; vet + lint pass

### Task 68 — Always emit backend `externalUrl` (audit M6)
- [ ] Drop `,omitempty` on `ProxyBackend.externalUrl`; store/return `ExternalURL` on the backend record so the cluster table + history mapping resolve
- [ ] Tests; vet + lint pass

### Task 69 — Populate `getDistribution.lineChart`
- [ ] Fill the per-backend, per-minute query-count series (currently an empty map) from query history so the dashboard line chart renders
- [ ] Tests; vet + lint pass

### Task 70 — `getUIConfiguration.disablePages` + page permissions (audit §3.12)
- [ ] Return `disablePages` (and/or role→page permissions) so the UI sidebar can hide pages by role
- [ ] Tests; vet + lint pass

### Task 71 — `findQueryHistory` filters + `getRoutingRules` verb
- [ ] Ensure server-side `userName`/`backendUrl`/`pageSize` filters work (frontend aligns to these names); confirm `getRoutingRules` responds on the verb the frontend uses (no 405)
- [ ] Tests; vet + lint pass

---

## Phase 11: Routing-service integration & verification

The standalone external router now exists at `routing-service/` (Go gRPC; pluggable `expr` + Starlark methods, hot-reload, kill-switch, observability — see `routing-service/docs/`). It implements the `TrinoGatewayRouter` contract this gateway calls, so it serves as a **real external router for verifying the gateway's external-gRPC routing path** end-to-end (beyond `cmd/mock-external-router-grpc`). See `docs/PRD.md` §Routing Strategy → "Reference routing service".

### Task 72 — Gateway proto: `trino_source` + `client_tags` ✅

- [x] Add `trino_source` (12) + `client_tags` (13) to `internal/routing/routerpb/router.proto` (additive; field numbers wire-compatible with the routing-service vendored proto); regenerate stubs
- [x] Populate in `internal/routing/external_grpc.go` `buildProtoRequest` — `trino_source` from `X-Trino-Source`; `client_tags` from `X-Trino-Client-Tags` (comma-split, trimmed, empties dropped, absent → empty non-nil slice)
- [x] `internal/routing/routing_test.go` — source/tags matrix + `splitClientTags` unit tests
- [x] `go build ./... && go vet ./...` + routing tests pass (gateway side of routing-service RS-14)

### Task 73 — E2E: gateway ↔ real routing-service

- [ ] Harness helper to build + launch the `routing-service` binary (separate Go module) exposing its data-plane + admin addrs; point the gateway at it via the existing `WithExternalGRPCRouter(addr)` option
- [ ] `internal/e2e/routing_service_e2e_test.go` (`//go:build e2e`):
  - [ ] `X-Trino-Source=airflow` → routed to the `etl` group via a real `expr` rule (proves `trino_source` round-trips gateway → service → decision end-to-end)
  - [ ] `X-Trino-Client-Tags: tier=premium` → `premium` group (proves `client_tags` round-trip)
  - [ ] routing-service down / returns an error → gateway **falls back to `routing.defaultGroup`** (no request dropped — Hard Invariant)
  - [ ] kill-switch: disable a method via the `RoutingServiceAdmin` admin API → routing changes on the next request
- [ ] `goleak`-clean; harness tears down the routing-service process on cleanup
- [ ] `go vet ./...` + `golangci-lint run ./...` pass

### Task 74 — Parity check: mock vs real router (optional)

- [ ] For an equivalent rule, the same request through `cmd/mock-external-router-grpc` and the real `routing-service` yields the identical `routingGroup` — confirms the gateway treats any conformant `TrinoGatewayRouter` interchangeably
- [ ] `go vet ./...` pass
