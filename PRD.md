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
1. `phase2-gate-responses.architect.md` — all library decisions, DI stance, streaming/oracle/cookie rulings, sequencing constraints
2. `gateway-cookies-and-sticky-routing.go-implementer.md` — cookie design study (required before proxy implementation)

### Phase 3 — Implementation
Order enforced by dependency:
1. `internal/config` + `internal/lifecycle`
2. `internal/persistence` (DAOs + migrations)
3. `internal/routing` (external selector only)
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

## Non-Groomed Features

Items in this section have no timeline and no implementation commitment. They may be promoted based on operator demand, but require an explicit team-lead decision to move forward.

### Header-Based Routing (`X-Trino-Routing-Group`)

Route requests by reading the `X-Trino-Routing-Group` header directly, with no external service call. Trivial to implement (~10 LOC) but adds a second routing code path alongside external routing. The external routing service can implement this as a one-liner by reading the header from the forwarded request metadata.

### File-Based Routing Rules (MVEL replacement)

Restore `rulesType=FILE` using a Go-native expression language (CEL or `expr-lang/expr`) instead of MVEL. Would require choosing an expression engine, porting all seven `routing_rules_*.yml` fixtures to new syntax (breaking config change), implementing per-request mutable state, priority ordering, hot-reload, and expression engine sandboxing. CEL is the team's named recommendation (typed, sandboxed by construction). Operators who need rule-based routing today should use the external routing selector.

### SQL Content Routing

Route queries based on parsed SQL — e.g. "if this query references catalog `hive`, send to cluster A." No Go Trino SQL parser exists as of 2026-05. Building one from the ANTLR grammar creates a permanent version-tracking burden as Trino's grammar evolves. Operators can forward the query body to their external routing service and parse it there.

### Oracle Database Backend

Support Oracle as a persistence backend. Blocked on the absence of a cgo-free Go Oracle driver. v1 supports Postgres and MySQL only.

### Per-Routing-Group Database Isolation

Each routing group gets its own database connection pool. No confirmed operator use was found during the study phase.

### `SCOPE.md` Artifact

A written document listing locked scope, deferred scope, and the reversal cost per item. Useful for preventing scope creep during implementation. Low effort; promote to Phase 2 deliverable if the team decides to formalize scope governance.

---

*Reference: `topics/do-we-needs-golang-trino-gateway.md` · `studies/CONVENTIONS.md` · `TODO.md`*
