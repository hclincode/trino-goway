# Product Requirements Document — trino-goway

**Date:** 2026-05-24  
**Status:** Draft  
**Decision basis:** `docs/topics/do-we-needs-golang-trino-gateway.md` (unanimous PROCEED WITH CAVEATS, 7/7)

---

## What Is This

`trino-goway` is a Go rewrite of [`trino-gateway`](https://github.com/trinodb/trino-gateway) — a reverse proxy that load-balances Trino SQL queries across multiple backend clusters. The Java original is ~13,600 LOC backed by Guice, Airlift, and JAX-RS. The Go version targets a statically-linked binary with no JVM, no heap tuning, and no Guice startup.

---

## Goals

- Drop-in replacement for `trino-gateway` for the common operator configuration (external routing, query stickiness)
- Single static binary; no JVM runtime required
- Fix two known Java bugs: response body truncation on large spooled segments, per-request JWKS fetching with no caching
- Go's race detector and goroutine-leak tooling provide correctness guarantees the Java suite cannot

---

## Routing Strategy

**v1 supports one routing mode with two transports:**

| Transport | How it works |
|---|---|
| **HTTP** | Gateway POSTs a JSON envelope to a configured URL; service returns a JSON response |
| **gRPC** | Gateway calls a configured gRPC endpoint with the same fields; service returns the same response shape |

Both transports carry identical request and response fields. The field contract matches the original trino-gateway exactly so operators can reuse existing external routing services.

### External Routing Contract

**Request fields** (same as `RoutingGroupExternalBody` in the original):

| Field | Type | Notes |
|---|---|---|
| `trinoQueryProperties` | object | SQL-parsed properties; fields requiring `trino-parser` (tables, catalogs, schemas, queryType) will be empty — SQL content routing is not supported in v1 |
| `trinoRequestUser` | object | Authenticated user info |
| `contentType` | string | Request `Content-Type` header |
| `remoteUser` | string | Remote user from request |
| `method` | string | HTTP method |
| `requestUri` | string | Request URI |
| `queryString` | string | Query string |
| `session` | string | Session info |
| `remoteAddr` | string | Client remote address |
| `remoteHost` | string | Client remote host |
| `parameterMap` | map | Request parameter map |
| `trinoSource` | string | `X-Trino-Source` (e.g. `airflow`, `superset`) — the primary routing signal. gRPC proto field `trino_source` (12); HTTP transport forwards the header directly. |
| `clientTags` | array | `X-Trino-Client-Tags`, pre-split on comma (trimmed, empties dropped). gRPC proto field `client_tags` (13); HTTP transport forwards the header directly. |

**Response fields** (same as `ExternalRouterResponse` in the original):

| Field | Type | Notes |
|---|---|---|
| `routingGroup` | string | Target routing group name |
| `externalHeaders` | map | Headers to inject into the forwarded request (REPLACE semantics — see Hard Invariants) |
| `errors` | array | Error strings; if non-empty and `propagateErrors=true`, gateway returns HTTP 400 to the client |

**Fallback behavior:** if the external service is unreachable, returns an error, or returns invalid JSON — the gateway falls back gracefully and routes the request to the default routing group. No request is dropped.

External routing is the sole routing extensibility mechanism. Operators implement routing logic in their own service in any language they choose. The gateway is not the logic host.

### Reference routing service (`routing-service/`)

A production-ready reference external router ships in this repo at [`routing-service/`](../routing-service/) — a standalone Go gRPC service implementing `TrinoGatewayRouter` with a pluggable, multi-method engine (`expr` + Starlark routing logic), hot-reload with validate-before-activate, a kill-switch admin API, and Prometheus/OTel observability (see `routing-service/docs/`). It serves three purposes: a fork-ready example for operators, the **verification harness** for the gateway's external-gRPC routing path (real rules end-to-end, not just `cmd/mock-external-router-grpc`), and the home for routing logic that the deliberately-dropped MVEL file rules used to host. Standalone CLI tools (`routing-service/tools/{expr-test,starlark-test}`) evaluate routing logic exactly as the service would.

> **gRPC signal additions:** the gRPC transport carries structured fields rather than raw headers, so `RouteRequest` now includes `trino_source` (field 12) and `client_tags` (field 13) — the two most-used routing signals — populated by `buildProtoRequest` from `X-Trino-Source` / `X-Trino-Client-Tags`. Field numbers are wire-compatible with the routing-service's vendored proto.

---

## v1 Scope (Build This)

- HTTP reverse proxy core — Trino statement protocol, `nextUri` polling, sticky routing by queryId
- External routing selector (HTTP API + gRPC)
- QueryId sticky-routing with 3-step cache-miss recovery chain
- Cluster health monitoring and backend registry
- Live cluster stats (UC-MON-02 / M7) — config-selectable collector (`INFO_API` default / `NOOP` / `UI_API` / `METRICS`) feeding live queued/running counts into the `getAllBackends` UI table and the M7 `ClusterStats` public backend-state endpoint (`GET /api/public/backends/{name}/state`). Implemented in Phase 12. Includes the `INFO_API` correction that `{"starting": true}` maps to `PENDING` (matching Java) rather than the prior `UNHEALTHY`.
- Query history persistence (Postgres + MySQL)
- Auth: OAuth2 (OIDC) + LDAP + noop
- Gateway cookies (HMAC-SHA256, wire-compatible with Java `GatewayCookie`; `TG.OAUTH2` cookie for OAuth2 flow stickiness; `wireCompat: true` default for blue/green)
- Admin REST API
- Web UI (serve existing Java-compiled static bundle unchanged)
- Config migration tool (`goway-migrate-config`) for one-shot conversion from Java YAML

**Size estimate:** ~2,500–3,000 LOC (vs 13,600 in Java). QA: ~9–13 person-days.

---

## Tend Not to Support

### File-Based Routing Rules (MVEL)

The Java gateway supports a file-based routing rule engine where operators write YAML files containing MVEL expressions:

```yaml
name: route-airflow
condition: "request.getHeader('X-Trino-Source') == 'airflow'"
actions:
  - "result.put('routingGroup', 'etl')"
```

MVEL is evaluated at runtime against the incoming HTTP request, letting operators change routing logic without recompiling.

**Why we are not supporting this:**

1. **No Go equivalent of MVEL.** MVEL is a JVM-only expression language. The two closest Go alternatives — `expr-lang/expr` and `google/cel-go` — both require rewriting every operator rule file in a new syntax, breaking compatibility regardless.
2. **File-based routing is not the recommended pattern going forward.** External routing (HTTP/gRPC) is strictly more powerful: the operator's routing service can call databases, use ML models, read from Kafka, or apply arbitrarily complex logic — none of which is possible inside a MVEL expression file.
3. **The replacement path exists today.** Operators using MVEL rules can move their logic into an external routing service in any language they prefer. The migration effort is one small HTTP service, not a rule-by-rule rewrite.
4. **It eliminates the only other JVM-bound dependency (`trino-parser`).** The sole non-MVEL use of `trino-parser` is a `KILL QUERY` body parse, replaceable with a 10-line regex. Dropping file-based routing eliminates `trino-parser` entirely.
5. **Scope discipline.** Minimal v1 (no MVEL/trino-parser) ≈ 2,500 LOC. Full v1 with file-based rules ≈ 6,000–8,000 LOC — a 3× increase with no user-visible improvement for operators who can use external routing instead.

**Migration path:** run your routing logic as a small HTTP service. Point the gateway at it with `routing.rulesType=EXTERNAL`. The gateway POSTs request metadata (headers, user, client tags, query properties) as JSON; your service returns `{"routingGroup": "etl"}`. Any MVEL rule can be replicated in ~10 lines of Python, Go, or Node.

### Header-Based Routing (`X-Trino-Routing-Group`)

Route requests by reading the `X-Trino-Routing-Group` header directly, with no external service call. Trivial to implement (~10 LOC) but adds a second routing code path alongside external routing. The external routing service can implement this as a one-liner by reading the header from the forwarded request metadata.

### File-Based Routing Rules (MVEL replacement)

Restore `rulesType=FILE` using a Go-native expression language (CEL or `expr-lang/expr`) instead of MVEL. Would require choosing an expression engine, porting all seven `routing_rules_*.yml` fixtures to new syntax (breaking config change), implementing per-request mutable state, priority ordering, hot-reload, and expression engine sandboxing. CEL is the team's named recommendation (typed, sandboxed by construction). Operators who need rule-based routing today should use the external routing selector.

### SQL Content Routing

Route queries based on parsed SQL — e.g. "if this query references catalog `hive`, send to cluster A." No Go Trino SQL parser exists as of 2026-05. Building one from the ANTLR grammar creates a permanent version-tracking burden as Trino's grammar evolves. Operators can forward the query body to their external routing service and parse it there.

### Side-by-Side Preview Mode

Run the Go gateway in shadow-traffic mode alongside Java, logging its routing decision for each request without serving real traffic — intended to validate Go/Java routing parity before cutover.

**Why this no longer applies:** when all routing logic lives in an external service (the only routing mode trino-goway supports), Go and Java both call the same service and get the same routing group by definition. There is no Go-vs-Java routing algorithm to compare. Cutover confidence comes from the Phase 4 differential harness (proxy behavior, not routing decisions) and a gradual traffic ramp.

---

## Key Architecture Decisions (Locked)

| Decision | Ruling |
|---|---|
| HTTP framework | `chi` for route groups + middleware |
| DI framework | None — explicit constructor wiring only |
| Proxy implementation | Hand-rolled `http.Handler`, not `httputil.ReverseProxy` |
| Cache library | `hashicorp/golang-lru/v2` + `golang.org/x/sync/singleflight` |
| DB migrations | `pressly/goose` |
| `backendToStatus` map | `sync.RWMutex` + `map[string]TrinoStatus` |
| Config compat | Loose — `goway-migrate-config` one-shot binary |
| Cookie wire compat | Soft-cutover default (`wireCompat: true`): bit-identical HMAC-SHA256 + JSON matching Java `GatewayCookie`; `wireCompat: false` available for clean-break deployments |
| Streaming | Buffer only on POST `/v1/statement`; stream all other paths via `io.Copy` |
| Redirect following | Disabled globally (`CheckRedirect: ErrUseLastResponse`) |
| Logger | `log/slog` |
| Metrics | `prometheus/client_golang` — **implemented (Phase 9):** OpenMetrics `/metrics` on the admin listener, own `*prometheus.Registry` (no global default), `trino_goway_*` namespace |

> **Metrics timing note:** per-backend health series (`trino_goway_backend_status`, `trino_goway_backend_activation_status`) populate only after the backend-refresh loop (~15s, the same `backendRefreshInterval` that reloads the active backend set from the DB) pushes a newly-registered backend into the monitor. A freshly-added backend's series is therefore expected to be absent for up to one refresh cycle — this is normal, not a missing metric.

---

## Hard Invariants (Must Not Break in v1)

1. **Never rewrite response bodies.** `nextUri` is built by the coordinator from `X-Forwarded-*` headers, not body manipulation.
2. **Disable redirect-following globally.** The default Go `http.Client` follows 3xx; this breaks spooled-segment downloads and OAuth2 redirects.
3. **Sticky-routing cache write completes before flushing the response.** No goroutine fire-and-forget.
4. **3-step cache-miss recovery chain.** History lookup → fan-out HEAD probe → first-active-default fallback. Simplifying causes cross-cluster query duplication.
5. **Document `http-server.process-forwarded=true` prominently.** It is the reason `nextUri` works and the Java docs bury it.
6. **`KILL QUERY` regex routing.** `KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'` on POST bodies must route to the cluster running the query, not the rule-selected cluster.
7. **Separate `*http.Client` instances per concern.** Proxy, monitor, and external-routing clients must never share a pool. `proxyClient` and `monitorClient` set `CheckRedirect: ErrUseLastResponse`; `routerClient` follows redirects. A 4th `statsClient` is added for cluster-stats collection but only when `clusterStats.monitorType` ∈ {`UI_API`, `METRICS`} — the default `INFO_API`/`NOOP` path keeps three live pools and issues no stats HTTP. The `UI_API` cookie jar lives inside the collector, not on the shared transport. Pool isolation prevents backpressure on one path starving the others.

---

## Roadmap

See `TODO.md` for the full phase breakdown and task list.

---

## Non-Groomed Features

Items in this section have no timeline and no implementation commitment. They may be promoted based on operator demand, but require an explicit team-lead decision to move forward.

### Oracle Database Backend

Support Oracle as a persistence backend. Blocked on the absence of a cgo-free Go Oracle driver. v1 supports Postgres and MySQL only.

### Per-Routing-Group Database Isolation

Each routing group gets its own database connection pool. No confirmed operator use was found during the study phase.

### `/v1/spooled/*` Gateway-Level Sticky Routing

Route spooled segment GET requests to the coordinator that owns the segment.

**Why not in v1:** Three independent blockers found during Phase 3 study:
1. The Trino JDBC driver uses a separate `OkHttpClient` without `CookieJar` for segment downloads — cookies set on `POST /v1/statement` responses are never sent on `GET /v1/spooled/*` requests.
2. The segment identifier is AES-256 encrypted with Trino's internal spooling key — queryId is not recoverable from the URL, so the gateway cannot route by URL parsing.
3. The Java gateway does not implement this feature either — `/v1/statement` routing uses the query-history database; there is no statement-routing or spooled-routing cookie.

**Operator guidance:** Use `STORAGE` mode (presigned URIs) for multi-cluster deployments, or configure load-balancer session affinity at the infrastructure layer. `COORDINATOR_PROXY` mode with multiple clusters and no external session affinity will cause segment routing failures — this is a known Java gateway limitation too.

**Promotion condition:** A mechanism is identified that does not require body rewriting (Hard Invariant #1) or Trino's internal spooling key.

---

*Reference: `docs/topics/do-we-needs-golang-trino-gateway.md` · `docs/studies/CONVENTIONS.md` · `TODO.md`*
