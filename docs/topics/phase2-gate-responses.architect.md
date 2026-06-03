# Phase 2 Gate Responses — Architect

**Date:** 2026-05-24
**Author:** architect
**Status:** Final — locks open rulings for Phase 4 implementation
**Basis:** `topics/do-we-needs-golang-trino-gateway.md` (unanimous PROCEED WITH CAVEATS) + `PRD.md`

This document closes every open ruling from the Phase 2 discussion. Implementers must not re-derive these decisions from scattered notes; this is the authoritative record.

---

## 1. Library Decisions

All eight locked decisions from `PRD.md § Key Architecture Decisions` are confirmed for v1. Rationale per item:

**`chi` for routing — CONFIRMED.**
`chi` gives middleware stacking, route groups (`/v1/statement`, `/v1/spooled`, `/ui`, `/api/v1`), and `chi.URLParam` extraction without importing a full framework. `net/http` alone would require hand-rolling middleware chaining; `gorilla/mux` is in maintenance mode; `gin`/`echo` pull in unnecessary templating and binding machinery. `chi`'s zero-allocation approach on the hot path matters for the proxy-core, which is the highest-throughput package.

**No DI framework (explicit constructor wiring) — CONFIRMED.**
See Section 2 for the full ruling.

**Hand-rolled `http.Handler` (not `httputil.ReverseProxy`) — CONFIRMED.**
`httputil.ReverseProxy` provides no seam for the buffer-vs-stream split required at POST `/v1/statement` (see Section 3). It also hardwires hop-by-hop header removal logic that conflicts with Trino's `X-Trino-*` header family passthrough. The hand-rolled forwarder is ~150 LOC and provides clean seams for all eight proxy lifecycle assertions the QA rubric requires. `httputil.ReverseProxy` is ruled out for v1 and all future versions unless a new major Trino protocol version removes the buffer requirement.

**`hashicorp/golang-lru/v2` + `golang.org/x/sync/singleflight` for caching — CONFIRMED.**
`singleflight` is load-bearing for the DB-backed loader path in the 3-step cache-miss recovery chain: multiple concurrent requests for the same queryId must coalesce into one history lookup, not fan out into N parallel DB reads. `golang-lru/v2` provides a typed, size-bounded LRU with TTL expiry without pulling in Caffeine's window-TinyLFU complexity, which is not needed at ~10-key depth for `backendToStatus` and ~queryId-count depth for sticky routing. `ristretto` is ruled out: it is async-insert by design, violating Hard Invariant #3 (cache write before response flush). `expirable` (from the standard `sync` proposal) is not yet in stdlib as of 2026-05.

**`pressly/goose` for DB migrations — CONFIRMED.**
`goose` supports embedded Go migration files (`//go:embed`), which means the binary carries its own schema history with no external migration runner. `golang-migrate/migrate` requires a separate binary or a more complex embedding pattern. Both Postgres and MySQL are supported by `goose`. The migration table name will be the goose default (`goose_db_version`) — the Java `flyway_schema_history` table must be left intact if operators run both gateways side-by-side; Go migrations apply only to the `goose_db_version` table and must not touch `flyway_schema_history`.

**`sync.RWMutex` + `map[string]TrinoStatus` for `backendToStatus` — CONFIRMED.**
`sync.Map` is the wrong tool for this access pattern: ~10 keys, one writer (the monitor goroutine), many readers (every inbound routing request). `sync.Map` optimizes for the inverse pattern (many concurrent writers, stable-over-time key set) and introduces type erasure. `sync.RWMutex` + plain map gives O(1) reads under a shared read lock, a single writer path with clear ownership, and type safety. This is a permanently closed decision.

**`log/slog` — CONFIRMED.**
`slog` is in stdlib since Go 1.21, produces structured JSON output with level routing, and requires no third-party import. `zap` and `zerolog` are ruled out: they add a dependency, no meaningful throughput advantage exists at gateway request rates, and `slog` handlers are composable if operators need custom sinks. Log-line format must be stable and tested (the Java suite has zero log assertions; the Go suite must fix this gap).

**`prometheus/client_golang` — CONFIRMED.**
Standard Prometheus client; no alternatives evaluated. The `promhttp.Handler()` endpoint must be served on a separate admin port from the proxy port to prevent proxied Trino traffic from reaching the metrics path.

---

## 2. DI Stance

**Ruling: Explicit constructor wiring only. Composition root lives exclusively in `cmd/trino-goway/main.go`. No factory functions that accept interface-typed parameters for the purpose of swapping implementations at runtime. No `wire`-generated code in v1.**

**How dependency injection flows:**

The composition root (`main.go`) calls each package's constructor in strict dependency order, passing concrete types. No package may call another package's constructor — packages declare their dependencies via constructor parameters and return a concrete struct (not an interface). Interfaces are used only at package boundaries where the test suite needs a fake, and the interface is defined in the consuming package (not the producing package), following Go convention.

Concretely:

```
main.go
  cfg  := config.Load(path)
  db   := persistence.Open(cfg.DB)
  reg  := persistence.NewRegistry(db)
  hist := persistence.NewHistory(db)
  mon  := monitor.New(cfg.Monitor, reg, monitorClient)   // monitorClient is *http.Client
  lru  := routing.NewCache(cfg.Routing)
  ext  := routing.NewExternalSelector(cfg.Routing, routerClient)  // routerClient is *http.Client
  rtr  := routing.NewRouter(lru, ext, hist, reg)
  auth := auth.New(cfg.Auth, jwksClient)
  prx  := proxy.New(cfg.Proxy, rtr, auth, proxyClient)   // proxyClient is *http.Client
  adm  := admin.New(cfg.Admin, reg, hist)
  srv  := lifecycle.NewServer(cfg, prx, adm, mon)
  srv.Start(ctx)
```

**Factory functions are permitted only in one case:** when the same constructor signature is needed in both production and test harness and the shape is purely mechanical (e.g., `newTestDB(t) *sql.DB`). These factories must live in `_test.go` files and must not be exported.

**No runtime DI.** The `modules:` / `managedApps:` FQCN dynamic-loading mechanism from the Java Guice configuration is permanently dropped. Any future extensibility point must be a compile-time import, not a runtime-loaded plugin.

**Lifecycle.** The composition root calls `Start(ctx)` on each component that needs it, in the order shown above, and defers `Stop(ctx)` in reverse order. No work may happen in constructors — constructors only capture parameters and initialize in-memory state. I/O connections (DB open, HTTP client warm-up) are deferred to `Start`. This makes constructors safe to call in tests without side effects.

---

## 3. Streaming / Buffering Ruling

**Ruling: Buffer on POST `/v1/statement` only. Stream everything else via `io.Copy`. This is final.**

**What "buffer only" means in practice:**

On POST `/v1/statement`, the proxy reads the entire upstream response body into a `[]byte` before forwarding it to the client. The reason: the proxy must extract the `nextUri` field's queryId from the JSON body to write the sticky-routing cache entry (Hard Invariant #3 — cache write before response flush). The buffer is bounded by `proxy.responseSize` (configurable, default 1 MiB). If the body exceeds `responseSize`, the proxy closes the upstream connection, returns HTTP 502 to the client, emits a high-severity log line, and increments `trino_goway_proxy_oversized_statement_response_total`. This cap must be tested; the Java suite does not test it.

The body buffer path is:
1. Read upstream response headers.
2. Read full upstream response body into `[]byte` (bounded by `responseSize`).
3. Attempt JSON decode to extract `queryId` from `nextUri` field.
4. Write sticky-routing cache entry (synchronous, not goroutine).
5. Write downstream response headers + buffered body.

The KILL QUERY regex match (`KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'`) runs on the **request** body (POST body from the client), not the response body. The proxy reads the POST body before routing, applies the regex to extract the queryId, uses it for routing target resolution (bypassing the normal routing group selection), then replays the body to the upstream unchanged. The body is read once into a `bytes.Reader`, which is seekable for replay.

**Streaming paths (all other routes):**

Every other request/response pair streams via `io.Copy` with no intermediate buffer. This includes:
- `nextUri` polling (`GET /v1/statement/{queryId}/{token}`)
- `/v1/spooled/{token}` segment downloads
- `/v1/spooled/ack/{token}` acknowledgments
- `/ui/*` static assets (served from embedded bundle, not proxied)
- `/api/v1/*` admin API responses

**`/v1/spooled/*` specifically:**
`/v1/spooled/{token}` segment GETs stream via `io.Copy` from the upstream backend to the client. No buffering. No body inspection. The sticky-routing decision for these paths uses the `TG.*` cookie emitted on the POST `/v1/statement` response — not queryId extraction. The gateway reads the cookie, resolves the backend, opens the upstream connection, and pipes bytes bidirectionally. Corrupting or buffering spooled segment bytes is the exact bug the Go rewrite is fixing in the Java gateway (`ProxyResponseHandler` truncation). The `io.Copy` path must never be wrapped with a `bytes.Buffer` on segment download paths.

---

## 4. Oracle Ruling

**Ruling: Oracle is Non-Groomed. v1 ships with Postgres and MySQL only. This is permanent until the condition below is met.**

The blocker is the absence of a cgo-free Go Oracle driver as of 2026-05. The two available options are:
- `godror` — requires Oracle Instant Client (C shared library + cgo). Incompatible with the statically-linked binary goal.
- `mattn/go-oci8` — same cgo requirement.

**What would need to change to promote Oracle from Non-Groomed:**

1. A cgo-free pure-Go Oracle driver must be published and reach v1.0 stability (no alpha/beta). The driver must pass the same `database/sql` interface tests used for Postgres and MySQL.
2. The driver must support `goose` migrations (i.e., `goose` must add a `"oracle"` dialect, or the driver must be compatible with the `database/sql` generic dialect).
3. An explicit team-lead decision to accept the Oracle driver as a dependency (supply-chain review).
4. A test environment with a real Oracle instance must be available for CI (e.g., `testcontainers-go` with the Oracle Free image).

Until all four conditions are met, no Oracle code may be merged. Any PR that adds an Oracle code path requires team-lead sign-off and a corresponding `testcontainers-go` Oracle integration test.

---

## 5. Cookie Wire-Compat Ruling

**Ruling: `wireCompat: true` is the correct default. It is the safe default for all operators.**

**`wireCompat: true` (default) means:**
The gateway produces and validates cookies that are bit-identical to those produced by the Java `GatewayCookie` implementation: HMAC-SHA256 over a JSON payload with fields matching the Java `GatewayCookie` struct (field names, field order, JSON encoding). An operator running Go and Java gateways behind the same load balancer during a blue/green cutover will see cookie reads succeed on either gateway — a client that received a cookie from the Java gateway can be served by the Go gateway and vice versa.

Implementers must replicate the exact JSON serialization the Java `GatewayCookie` uses: field order must match Jackson's default alphabetical serialization order (or the Java order confirmed in Task 14's study). A single byte difference in the HMAC input invalidates all in-flight cookies for that client.

**`wireCompat: false` means:**
The gateway uses a Go-native cookie format (implementer's discretion on JSON field naming and ordering, provided the HMAC-SHA256 construction is otherwise identical). Cookies produced under `wireCompat: false` are not readable by the Java gateway and vice versa.

**Who should use `wireCompat: false`:**
Operators performing a hard-cutover (all traffic moves to Go in a single deployment event with no blue/green overlap). These operators do not need cross-gateway cookie compatibility; they will invalidate all sessions at cutover time anyway. `wireCompat: false` gives them freedom to evolve the cookie format in future versions without being locked to Java's field layout.

**Default enforcement:** `wireCompat: true` must be the compiled default and must be documented prominently. Operators must explicitly set `wireCompat: false` to opt out. A startup log line must record which mode is active so operators can confirm their configuration.

---

## 6. 6th Hard Invariant

**Ruling: The 6th Hard Invariant is the three-separate-`*http.Client`-instances rule.**

**Hard Invariant #6: Three separate `*http.Client` instances must exist — one for proxy traffic, one for cluster health monitoring, and one for external-routing callouts. These clients must never be shared or aliased.**

**Formal statement:**
The composition root (`main.go`) must instantiate exactly three `*http.Client` values: `proxyClient`, `monitorClient`, `routerClient`. No package may accept a generic `*http.Client` parameter and use it for more than one of these roles. The three clients have distinct configuration requirements:

| Client | Key configuration |
|---|---|
| `proxyClient` | Long timeouts (configurable per `proxy.requestTimeout`); `CheckRedirect: ErrUseLastResponse` (Hard Invariant #2); large transport connection pool |
| `monitorClient` | Short timeouts (configurable per `monitor.checkTimeout`); `CheckRedirect: ErrUseLastResponse`; small pool |
| `routerClient` | Short timeouts (configurable per `routing.asyncTimeout`); standard redirect behavior; separate pool |

**Why this is a Hard Invariant and not just a design guideline:**

Sharing clients across roles collapses connection pool isolation. A spike in external-routing latency would exhaust the shared pool's connections, starving the proxy-path or monitor. This is the exact failure the Java `@ForProxy` / `@ForMonitor` / `@ForRouter` Guice qualifiers prevent. In Go, the only enforcement mechanism is making the separation explicit at the composition root. A single shared `http.DefaultClient` is the most common Go beginner mistake in proxy implementations and would silently degrade the gateway under load with no obvious symptom until connection pool exhaustion.

Additionally, `proxyClient` and `monitorClient` both require `CheckRedirect: ErrUseLastResponse` (Hard Invariant #2), while `routerClient` must follow redirects normally (an external routing service may redirect). If a shared client is used, one of these requirements must be violated.

**Why this over the other candidates:**

The `externalHeaders` REPLACE semantics ruling is already captured in the PRD's response field table ("REPLACE semantics — see Hard Invariants") and is effectively documented. The `nextUri` host-rewrite prohibition is already Hard Invariant #1 ("Never rewrite response bodies"). The three-client rule is the only design-level invariant with a silent, hard-to-diagnose failure mode that is not covered by the existing five invariants. It deserves formal status.

---

## 7. gRPC in v1 vs. Non-Groomed

**Ruling: gRPC external routing IS in v1 scope. This is confirmed.**

The HTTP and gRPC transports share identical field contracts. Implementing gRPC adds one `.proto` file, one generated stub, and ~100 LOC of transport adapter — it does not change the routing engine, the field validation logic, or the fallback behavior. Deferring gRPC to v2 would require operators who have already built gRPC-based routing services against the Java gateway to maintain an HTTP shim, which is a worse operator experience than shipping both transports together.

**Proto file schema:**

The service name is `TrinoGatewayRouter`. The proto package is `trino.gateway.v1`.

```proto
syntax = "proto3";
package trino.gateway.v1;
option go_package = "github.com/yourorg/trino-goway/internal/routing/routerpb";

service TrinoGatewayRouter {
  rpc Route(RouteRequest) returns (RouteResponse);
}

message RouteRequest {
  TrinoQueryProperties trino_query_properties = 1;
  TrinoRequestUser     trino_request_user     = 2;
  string               content_type           = 3;
  string               remote_user            = 4;
  string               method                 = 5;
  string               request_uri            = 6;
  string               query_string           = 7;
  string               session                = 8;
  string               remote_addr            = 9;
  string               remote_host            = 10;
  map<string, string>  parameter_map          = 11;
}

message RouteResponse {
  string               routing_group    = 1;
  map<string, string>  external_headers = 2;
  repeated string      errors           = 3;
}

message TrinoQueryProperties {
  // All sub-fields present for schema completeness.
  // Fields requiring trino-parser (tables, catalogs, schemas, query_type)
  // will always be empty in v1. Document this prominently.
  string query_type = 1;
  repeated string tables   = 2;
  repeated string catalogs = 3;
  repeated string schemas  = 4;
}

message TrinoRequestUser {
  string user  = 1;
  string group = 2;
}
```

Field names in the proto use `snake_case` per proto3 convention; JSON serialization (for the HTTP transport) uses `camelCase` per the standard JSON-proto mapping. The JSON field names match the original `RoutingGroupExternalBody` Java field names — confirm exact casing in Task 15's study before code-write.

**Unary RPC, not streaming.**
Each routing decision is a discrete request/response pair with no state carried between calls. Streaming RPC would add connection management complexity for no protocol benefit. The external routing service is called once per inbound Trino request; latency is bounded by `routing.asyncTimeout`. Unary is correct.

**Fallback behavior is identical to HTTP:**
If the gRPC call fails (connection refused, deadline exceeded, non-OK status), the gateway falls back to the default routing group. This matches the HTTP transport fallback and must be tested with the same fault-injection fixtures.

---

## 8. Sequencing Constraints

**Hard ordering constraints for Phase 4 (Tasks 17–25):**

```
Task 17 (config + lifecycle)
  └─ blocks ALL other tasks (every package imports config types and lifecycle Start/Stop)

Task 18 (persistence)
  └─ requires: Task 17
  └─ blocks: Task 19 (history lookup in 3-step chain), Task 21 (registry read), Task 23 (admin API)

Task 15 (external routing contract study — Phase 3)
  └─ blocks: Task 19 (routing field schema must be pinned before implementation)

Tasks 12, 13, 14 (cookie + spooled studies — Phase 3)
  └─ block: Task 20 (proxy cannot implement cookie emission without these)

Task 16 (admin API surface study — Phase 3)
  └─ blocks: Task 23

Task 19 (routing)
  └─ requires: Task 17, Task 18, Task 15
  └─ blocks: Task 20 (proxy calls routing to select backend)

Task 20 (proxy)
  └─ requires: Task 17, Task 19, Tasks 12–14
  └─ blocks: Task 24 (main wiring)

Task 21 (monitor)
  └─ requires: Task 17, Task 18
  └─ no downstream blocks within Phase 4 (monitor is a consumer of registry, not a provider to proxy)
  └─ note: Task 21 and Task 22 can run in parallel with each other and with Task 19

Task 22 (auth)
  └─ requires: Task 17
  └─ blocks: Task 20 (proxy calls auth for OIDC/LDAP validation)
  └─ note: Task 22 may start in parallel with Task 19 if Task 17 is complete

Task 23 (admin)
  └─ requires: Task 17, Task 18, Task 16

Task 24 (main + wiring)
  └─ requires: Tasks 17–23 (all packages must be compilable)

Task 25 (migrate-config tool)
  └─ requires: Task 17 (config type definitions)
  └─ independent of Tasks 18–24 (no runtime dependency)
  └─ can run in parallel with Tasks 18–23
```

**Critical path:**

```
Task 17 → Task 18 → Task 19 → Task 20 → Task 24
```

Tasks 21, 22, 23, and 25 are off the critical path and can be parallelized against each other and against Task 19 once Task 17 is complete.

**Phase 3 study tasks that gate Phase 4:**
Task 15 must complete before Task 19 starts. Tasks 12, 13, 14 must complete before Task 20 starts. Task 16 must complete before Task 23 starts. These Phase 3 tasks are currently unstarted and are the scheduling risk for Phase 4 start dates.

**Do not start Task 20 until Task 22 is complete.**
Auth is called inside the proxy handler on every inbound request. Implementing the proxy with a stub auth interface and retrofitting the real auth later has historically caused behavioral gaps in the auth filter chain that are hard to catch in unit tests. Build auth first, wire it at proxy construction time.

---

## 9. Open Risks

Risks that are not covered by Hard Invariants 1–6 and that the team must watch during Phase 4:

**R1: JSON cookie field order is unspecified in Go.**
`encoding/json` does not guarantee struct field serialization order. The Java `GatewayCookie` HMAC input depends on a specific JSON byte sequence. If the Go serialization produces a different field order than Jackson's default, `wireCompat: true` will be silently broken — cookies will be produced correctly but validation against Java-issued cookies will fail. Mitigation: Task 14 must confirm the exact byte sequence of the Java HMAC input, and the Go implementation must use an explicit `json.Marshal` with a fixed-order struct or a manually constructed byte sequence. A test must assert byte-level equality of the HMAC input against a golden value extracted from the Java implementation.

**R2: `X-Presto-*` alternate header compatibility is a silent behavior change.**
The Java `HttpUtils.java` hardcodes `X-Trino-*` and does not correctly handle the `X-Presto-*` alternate prefix in all paths. If Go fixes this, deployments that rely on the Java bug's behavior (sending `X-Presto-Source` expecting it to be ignored) will see behavior changes. This must be a product decision (Open Question #8 from Phase 2) locked before proxy-core implementation, not discovered during QA.

**R3: `trinoQueryProperties` empty fields must be documented at the wire level.**
The gRPC and HTTP routing request bodies will contain empty `tables`, `catalogs`, `schemas`, and `queryType` fields in v1 (no `trino-parser`). External routing services that branch on these fields will silently fall through to default behavior. This is correct per PRD scope but must appear in the operator documentation and in the proto comments. A routing service that worked against the Java gateway and branched on SQL-content fields will not fail loudly — it will route everything to the default group without warning.

**R4: Differential harness readiness is a schedule dependency.**
Task 28 (differential harness: Java↔Go side-by-side) is gated by Task 27 (G1 nextUri test), which is gated by Task 26 (QA infra). If Phase 5 QA infra slips, the proxy-core cannot be declared complete even if all Phase 4 implementation tasks are done. The team must keep Task 26 on track in parallel with Phase 4 implementation — it should start as soon as Task 17 is mergeable.

**R5: Admin port / proxy port separation must be enforced at config validation time.**
If `admin.port` and `proxy.port` are set to the same value, the `promhttp.Handler()` endpoint and `/api/v1/*` admin routes would be reachable from the same listener as proxied Trino traffic. This is a security issue (Prometheus metrics exposed to Trino clients) and a routing conflict. Config validation in Task 17 must assert `admin.port != proxy.port` and fail startup with a clear error message if violated.

**R6: `http-server.process-forwarded=true` misconfiguration has a silent failure mode.**
Hard Invariant #5 says to document this prominently. The additional risk is that the failure mode (coordinator builds `nextUri` with its own bind address instead of the gateway address) produces valid JSON responses with a working queryId — the client can decode the response — but all subsequent `nextUri` polls go directly to the coordinator, bypassing the gateway. Sticky routing is silently broken for those queries. No error is returned. The startup-time check proposed in Open Question #15 (emit a warning if `routing.forwardedHeadersEnabled=true` but coordinator-side config appears absent) should be implemented even if it is heuristic-based. A heuristic warning is better than silent failure.

**R7: Goose migration vs. Flyway schema history coexistence.**
If an operator runs both Java and Go gateways against the same database (during blue/green), the Go gateway's `goose_db_version` table and the Java gateway's `flyway_schema_history` table will both exist. Goose does not touch `flyway_schema_history` and vice versa. This is safe as long as both tools are managing the same physical schema and their migrations are compatible. The risk is schema drift: if the Go migration adds a column that the Java DAO does not expect, the Java gateway may fail queries on that table. Mitigation: during the blue/green overlap period, Go migrations must be backward-compatible with the Java gateway's DAO layer. Document this constraint in the migration files themselves.

---

*Closes Phase 2 open questions: #2, #3, #4, #5, #6, #10, #11, #12. Remaining open: #1 (MVEL operator survey), #7 (auth behavioral flags — product decision), #8 (X-Presto-* — product decision), #14 (gradual cutover strategy — team-lead + architect before Phase 3 ships).*
