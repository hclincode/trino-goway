# Product Requirements Document — trino-goway

**Date:** 2026-05-24  
**Status:** Draft  
**Decision basis:** `topics/do-we-needs-golang-trino-gateway.md` (unanimous PROCEED WITH CAVEATS, 7/7)

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

**v1 supports one routing mode:**

| Mode | How it works |
|---|---|
| **External routing** | Gateway POSTs request metadata to an operator-run HTTP or gRPC service; service returns the routing group |

External routing is the sole extensibility mechanism. Operators implement routing logic in their own service in any language they choose. The gateway is not the logic host.

---

## v1 Scope (Build This)

- HTTP reverse proxy core — Trino statement protocol, `nextUri` polling, sticky routing by queryId
- External routing selector (HTTP API + gRPC)
- QueryId sticky-routing with 3-step cache-miss recovery chain
- Cluster health monitoring and backend registry
- Query history persistence (Postgres + MySQL)
- Auth: OAuth2 (OIDC) + LDAP + noop
- Gateway cookies (HMAC-SHA256, wire-compatible with Java for blue/green)
- Admin REST API
- Web UI (serve existing Java-compiled static bundle unchanged)
- Config migration tool (`goway-migrate-config`) for one-shot conversion from Java YAML
- `SCOPE.md` — written artifact listing locked and deferred scope; reversals require team-lead sign-off

**Size estimate:** ~2,500–3,000 LOC (vs 13,600 in Java). QA: ~9–13 person-days.

---

## Tend to Do

Items in this section are planned but not committed to v1. They will be promoted to v1 scope or scheduled as a follow-on release based on implementation progress and operator demand.

### `/v1/spooled/*` Sticky Routing via Cookie

**Why it matters:** When Trino's spooling protocol is active, the coordinator generates `/v1/spooled/<token>` URLs for large result segments. These requests come back through the gateway. Without a sticky mechanism covering spooled paths, the gateway routes them to whichever backend the load balancer picks — which may not be the cluster holding the spooled data.

**Impact by deployment type:**

| Deployment | Impact if not implemented |
|---|---|
| Spooling disabled (most deployments) | None |
| Spooling + shared object storage (S3/GCS/Azure Blob) | None — segment accessible from any cluster |
| Spooling + local coordinator storage + multi-cluster | **Complete query failure** — segment GET lands on wrong cluster → 404 |

The third case is a hard failure, not degraded performance. Operators using local spooling storage with multiple clusters cannot use trino-goway without this feature.

**Proposed fix:** emit a `TG.*` cookie on POST `/v1/statement` responses with `routingPaths` covering `/v1/spooled` and `/v1/spooled/ack`, binding the client to the correct cluster for the duration of the result fetch. ~50 LOC; fits naturally into the cookie study deliverable.

**Recommended action:** fold into the `gateway-cookies-and-sticky-routing.go-implementer.md` cookie study. Promote to v1 scope if the study confirms the implementation is straightforward alongside the existing cookie work.

---

## Tend Not to Support

### File-Based Routing Rules (MVEL)

**Decision: Permanently out of scope. Will not be implemented.**

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

3. **The replacement path exists today.** Operators using MVEL rules can move their logic into an external routing service in any language they prefer. The gateway calls it once per uncached request. The migration effort is one small HTTP service, not a rule-by-rule rewrite.

4. **It eliminates the only other JVM-bound dependency (`trino-parser`).** The sole non-MVEL use of `trino-parser` in the gateway is a `KILL QUERY` body parse, replaceable with a 10-line regex. Dropping file-based routing eliminates `trino-parser` entirely — no Go Trino SQL parser is needed.

5. **Scope discipline.** The team studied this carefully. Minimal v1 (no MVEL/trino-parser) ≈ 2,500 LOC. Full v1 with file-based rules ≈ 6,000–8,000 LOC — a 3× increase with no user-visible improvement for operators who can use external routing instead.

**What operators lose:**

- Hot-reload YAML rule files without gateway restart
- Stateful multi-rule composition (`state.put`/`state.get` per request)
- Priority-ordered rule override
- Client-tag matching in rule expressions
- SQL-content-based routing (catalog/schema/table in conditions)

**Migration path for existing MVEL users:**

Run your routing logic as a small HTTP service. Point the gateway at it with `routing.rulesType=EXTERNAL`. The gateway POSTs request metadata (headers, user, client tags, query properties) to your service as JSON; your service returns `{"routingGroup": "etl"}`. You can replicate any MVEL rule in ~10 lines of Python, Go, or Node.

---

### SQL Content Routing (`trino-parser`)

**Decision: Out of scope for v1. Revisit in v2 only if operator demand justifies it.**

The Java gateway can route queries based on parsed SQL — e.g. "if this query references catalog `hive`, send to cluster A." This requires `trino-parser`, the full Trino ANTLR grammar compiled for Java.

There is no Go Trino SQL parser. Generating one from the ANTLR grammar creates a permanent version-tracking burden (grammar changes with every Trino release). The operator impact is low because SQL-content routing can be replicated by the external routing service if the operator forwards the query body.

---

### Oracle Database Backend

**Decision: Deferred to v2.**

No cgo-free Oracle driver exists for Go. The Java gateway supports Oracle via JDBC. v1 supports Postgres and MySQL only.

---

### Per-Routing-Group Database Isolation

**Decision: Dropped.**

`JdbcConnectionManager.getJdbi(routingGroupDatabase)` in the Java gateway allows each routing group to have its own database. No confirmed operator use was found. Dropped from scope permanently.

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
| Metrics | `prometheus/client_golang` |

---

## Hard Invariants (Must Not Break in v1)

1. **Never rewrite response bodies.** `nextUri` is built by the coordinator from `X-Forwarded-*` headers, not body manipulation.
2. **Disable redirect-following globally.** The default Go `http.Client` follows 3xx; this breaks spooled-segment downloads and OAuth2 redirects.
3. **Sticky-routing cache write completes before flushing the response.** No goroutine fire-and-forget.
4. **3-step cache-miss recovery chain.** History lookup → fan-out HEAD probe → first-active-default fallback. Simplifying causes cross-cluster query duplication.
5. **Document `http-server.process-forwarded=true` prominently.** It is the reason `nextUri` works and the Java docs bury it.
6. **`KILL QUERY` regex routing.** `KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'` on POST bodies must route to the cluster running the query, not the rule-selected cluster.

---

## Roadmap

### Phase 1 — Discovery (Complete)
- All 7 team members studied `trino/` and `trino-gateway/` submodules
- 30+ insight files written to `studies/`
- Go/no-go discussion: `topics/do-we-needs-golang-trino-gateway.md`

### Phase 2 — Architecture Design (Next)
Earliest deliverables (required before implementation starts):
1. `phase2-gate-responses.architect.md` — all library decisions, DI stance, streaming/oracle/cookie rulings, Phase 2 sequencing constraints
2. `SCOPE.md` — locked scope, deferred scope, reversal cost per item
3. `gateway-cookies-and-sticky-routing.go-implementer.md` — cookie design study (required before proxy implementation)

### Phase 3 — Implementation
Order enforced by dependency:
1. `internal/config` + `internal/lifecycle`
2. `internal/persistence` (DAOs + migrations)
3. `internal/routing` (header selector + external selector)
4. `internal/proxy` (after cookie study lands)
5. `internal/monitor` (cluster health)
6. `internal/auth`
7. `cmd/trino-goway` (main + wiring)
8. `cmd/goway-migrate-config` (config migration tool)

### Phase 4 — QA Gates
- Gate to START proxy-core: port allocator + testcontainers-go postgres + goleak + misbehaving-backend fixture
- Gate to DECLARE proxy-core COMPLETE: differential harness (live Java↔Go side-by-side for Seams 1–8 + statement protocol)
- G1 (`nextUri` host derivation against real Trino) must be the first QA gate — it's the only gap with a silent failure mode

---

## Non-Prioritized Features

Items in this section are not on the roadmap. They may be revisited based on operator demand, but no timeline is attached and no implementation work should begin without an explicit team-lead decision to promote them.

### Header-Based Routing (`X-Trino-Routing-Group`)

Support routing by inspecting the `X-Trino-Routing-Group` header on incoming requests — the gateway reads the header value and routes directly to that group, with no external service call. Trivial to implement (~10 LOC) but adds a second routing code path that operators must reason about alongside external routing.

The external routing service can implement header-based routing trivially by reading `X-Trino-Routing-Group` from the request metadata the gateway forwards. Keeping it in the external service keeps the gateway's routing surface to a single code path.

Operators who want header routing today: implement it as a one-liner in their external routing service.

### File-Based Routing Rules (MVEL replacement)

Restore the `rulesType=FILE` routing mode using a Go-native expression language (CEL or `expr-lang/expr`) instead of MVEL. Would require:
- Choosing an expression engine and porting all seven `routing_rules_*.yml` fixture files to the new syntax (breaking config change for operators)
- Implementing per-request mutable `state` map, priority ordering, `if/else` action bodies
- Hot-reload file watcher with deterministic reload-complete signal
- Expression engine sandboxing (must block subprocess exec, filesystem access, network sockets — Java's MVEL config explicitly blocks `Process` and `Runtime`)

CEL is the team's named recommendation if this is ever revisited (typed, sandboxed by construction, used in Kubernetes/Envoy/Istio). `expr-lang/expr` is the alternative (simpler API, lower operator friction, requires explicit sandboxing).

Operators who need rule-based routing today should use the external routing selector.

### SQL Content Routing

Route queries based on parsed SQL — e.g. "if this query references catalog `hive`, send to cluster A." Requires a Go Trino SQL parser covering the statement forms in `TestRoutingGroupSelector.provideTableExtractionQueries` (~30 DDL/DML forms). No Go Trino parser exists as of 2026-05. Building one from the ANTLR grammar creates a permanent version-tracking burden as the Trino grammar evolves.

Operators who need SQL-content routing today can forward the query body to their external routing service and parse it there.

### Oracle Database Backend

Support Oracle as a persistence backend for query history and cluster registry. Blocked on the absence of a cgo-free Go Oracle driver. v1 supports Postgres and MySQL only.

### Per-Routing-Group Database Isolation

Each routing group gets its own JDBC connection pool pointing at a separate database (`JdbcConnectionManager.getJdbi(routingGroupDatabase)` in the Java gateway). No confirmed operator use was found during the study phase. Dropped from scope.

### Side-by-Side Preview Mode

Run the Go gateway alongside the Java gateway in shadow-traffic mode: Go logs its routing decision for each request alongside what Java decided, without actually serving traffic. Useful for validating parity before cutover. Not required for v1; the differential harness in Phase 4 covers this for QA purposes.

---

*Reference: `topics/do-we-needs-golang-trino-gateway.md` · `studies/CONVENTIONS.md` · `TODO.md`*
