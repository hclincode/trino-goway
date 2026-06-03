# SCOPE.md — trino-goway v1

**Status:** Locked  
**Authority:** Team lead sign-off required to change any ruling in sections 1 or 2.  
**Reference:** `docs/PRD.md`, `docs/topics/do-we-needs-golang-trino-gateway.md`

---

## 1. Locked In Scope (v1)

| Item | Description |
|---|---|
| HTTP reverse proxy core | Trino statement protocol, `nextUri` polling, sticky routing by queryId; hand-rolled `http.Handler`, not `httputil.ReverseProxy` |
| External routing selector — HTTP | Gateway POSTs `RoutingGroupExternalBody` JSON to a configured URL; service returns `ExternalRouterResponse` JSON |
| External routing selector — gRPC | Same field contract as HTTP transport; operators can reuse existing external routing services unchanged |
| QueryId sticky-routing — 3-step recovery chain | Cache hit → history lookup → fan-out HEAD probe → first-active-default fallback; no simplification permitted |
| Cluster health monitoring and backend registry | `backendToStatus` map; `sync.RWMutex`+`map`; one long-running goroutine per monitor tick |
| Query history persistence | Postgres + MySQL via `database/sql`+`sqlx`; `pressly/goose` migrations |
| Auth — OAuth2/OIDC | JWKS TTL caching (fixes per-request fetch defect); `github.com/MicahParks/keyfunc` |
| Auth — LDAP | `go-ldap/ldap` dependency |
| Auth — noop | Pass-through; zero external calls |
| Gateway cookies | HMAC-SHA256 wire-compatible with Java `GatewayCookie`; `TG.OAUTH2` cookie for OAuth2 flow stickiness; `wireCompat: true` default for blue/green; `wireCompat: false` available for clean-break deployments |
| Admin REST API | All routes and `@RolesAllowed` roles (`ADMIN`/`USER`/`API`) from Java surface; spec from `docs/studies/trino-gateway/admin-api-surface.java-analyst.md` (Task 16) |
| Web UI | Serve existing Java-compiled static bundle unchanged; embed compiled `webapp/` assets via `//go:embed`; no UI rewrite |
| Config migration tool — `goway-migrate-config` | One-shot binary: Java YAML → Go YAML; config compat is loose, not strict |

---

## 2. Locked Out of Scope (v1)

### File-Based Routing Rules — MVEL (permanently out)

**What it is:** Operators write YAML files containing MVEL expressions evaluated at runtime against the incoming HTTP request (`routing.rulesType=FILE`).  
**Why excluded:** MVEL is JVM-only with no Go port. No Go alternative preserves MVEL syntax. Without MVEL, `trino-parser` is also eliminated (its only non-MVEL use — `KILL QUERY` body parse — is a single regex). Keeping MVEL triples the v1 LOC estimate (2,500 → 6,000–8,000 LOC) with no improvement for operators who can use external routing.  
**Migration path:** Move rule logic into an external routing service (any language). Point gateway at it with `routing.rulesType=EXTERNAL`. Any MVEL rule replicates in ~10 LOC of Python, Go, or Node.

### File-Based Routing Rules — MVEL Replacement (CEL / expr-lang) (non-groomed)

**What it is:** Restore `rulesType=FILE` using a Go-native expression language (CEL or `expr-lang/expr`) instead of MVEL.  
**Why excluded:** Requires choosing an expression engine, porting all seven `routing_rules_*.yml` fixtures to new syntax (breaking config change), implementing per-request mutable state, priority ordering, hot-reload, and expression engine sandboxing. CEL is the team's named candidate (typed, sandboxed by construction) but no implementation decision has been made.  
**Promotion condition:** Operator survey shows material MVEL adoption; team lead approves expression engine choice and sandboxing plan.

### Header-Based Routing (`X-Trino-Routing-Group`) (non-groomed)

**What it is:** Route requests by reading the `X-Trino-Routing-Group` header directly, with no external service call.  
**Why excluded:** Adds a second routing code path alongside external routing. Trivial for an external routing service to implement as a one-liner by reading the header from forwarded request metadata.  
**Promotion condition:** Operator demand documented; team lead approves adding the second code path.

### SQL Content Routing (permanently out)

**What it is:** Route queries based on parsed SQL AST — e.g. route queries referencing catalog `hive` to cluster A.  
**Why excluded:** No Go Trino SQL parser exists. Building one from the ANTLR grammar creates a permanent version-tracking burden as Trino's grammar evolves. Operators can forward the query body to their external routing service and parse it there.  
**Promotion condition:** Would require a maintained Go Trino ANTLR grammar port; permanently deferred unless that artifact exists externally.

### Side-by-Side Preview Mode (not applicable)

**What it is:** Run the Go gateway in shadow-traffic mode alongside Java, logging its routing decision for each request without serving real traffic.  
**Why excluded:** When all routing logic lives in an external service (the only routing mode trino-goway supports), Go and Java both call the same service and get the same routing group by definition. There is no Go-vs-Java routing algorithm to compare. Cutover confidence is provided by the Phase 4 differential harness (proxy behavior, not routing decisions) and a gradual traffic ramp.  
**Promotion condition:** Not applicable; architectural constraint, not a priority decision.

### Oracle Database Backend (non-groomed)

**What it is:** Oracle as a persistence backend for query history and cluster registry.  
**Why excluded:** No cgo-free Go Oracle driver exists as of 2026-05. v1 supports Postgres and MySQL only.  
**Promotion condition:** A production-quality cgo-free Go Oracle driver becomes available; operator demand is confirmed.

### `/v1/spooled/*` Gateway-Level Sticky Routing (non-groomed)

**What it is:** Route spooled segment GET requests to the coordinator that owns the segment.
**Why excluded:** Three independent blockers found in Phase 3 study: (1) Trino JDBC driver uses a separate `OkHttpClient` without `CookieJar` for segment downloads — cookies set on `/v1/statement` responses are never sent on segment GETs; (2) segment identifier is AES-256 encrypted with Trino's internal key — queryId is not recoverable from the URL; (3) the Java gateway does not implement this either — routing uses the query-history DB, not cookies. See `docs/studies/trino-gateway/gateway-cookies-and-sticky-routing.go-implementer.md` §6.
**Operator guidance:** Use `STORAGE` mode (presigned URIs) for multi-cluster deployments, or configure load-balancer session affinity outside the gateway.
**Promotion condition:** A viable mechanism is identified that does not require body rewriting (Hard Invariant #1) or Trino's internal spooling key.

### Per-Routing-Group Database Isolation (non-groomed)

**What it is:** Each routing group gets its own database connection pool (`JdbcConnectionManager.getJdbi(routingGroupDatabase)` pattern from Java).  
**Why excluded:** No confirmed operator use was found during the study phase. Adds connection-pool management complexity with no known beneficiary.  
**Promotion condition:** At least one operator confirms active reliance on this isolation; team lead approves the connection pool design.

---

## 3. Reversal Cost Table

| Item | Reversal Cost | Condition to Promote |
|---|---|---|
| File-based routing — MVEL replacement (CEL/expr-lang) | ~800–1,200 LOC; 8–12 person-days (engine integration, hot-reload, sandboxing, fixture migration) | Operator survey shows MVEL adoption; team lead approves engine + sandboxing plan |
| Header-based routing (`X-Trino-Routing-Group`) | ~30–50 LOC; 0.5 person-days | Documented operator demand; team lead approves second routing code path |
| SQL content routing | ~2,000–4,000 LOC; 20–40 person-days (Go ANTLR grammar port + permanent grammar-tracking maintenance) | Externally-maintained Go Trino SQL parser exists; team lead approves grammar-tracking burden |
| Side-by-side preview mode | Not reversible — architectural constraint | N/A |
| Oracle database backend | ~200–400 LOC; 3–5 person-days (driver integration + migrations) plus ongoing maintenance | cgo-free Go Oracle driver available in production quality; confirmed operator demand |
| Per-routing-group DB isolation | ~150–300 LOC; 2–3 person-days (connection pool per group, config surface) | At least one operator confirms active use; team lead approves pool design |
| `/v1/spooled/*` gateway sticky routing | Unbounded — blocked by architectural constraints, not LOC | Viable mechanism identified that avoids body rewriting and Trino internal key dependency |

---

## 4. Hard Invariants (Reference)

These seven invariants MUST NOT be violated in any implementation task. See `docs/PRD.md § Hard Invariants` for the authoritative definitions and rationale.

1. Never rewrite response bodies.
2. Disable redirect-following globally (`CheckRedirect: ErrUseLastResponse`).
3. Sticky-routing cache write completes before flushing the response — no goroutine fire-and-forget.
4. Implement the 3-step cache-miss recovery chain (history lookup → fan-out HEAD probe → first-active-default fallback).
5. Document `http-server.process-forwarded=true` prominently.
6. `KILL QUERY` regex routing: route to the cluster running the query, not the rule-selected cluster.
7. Three separate `*http.Client` instances: proxy, monitor, and external-routing must never share a pool.

---

## 5. Sign-off Policy

Any change to sections 1 or 2 requires all three of the following in the same git commit:

1. **Written rationale** in a new file under `docs/topics/` documenting the change, operator demand evidence, and implementation cost.
2. **Team-lead acknowledgment** in the git commit message explicitly referencing the `docs/topics/` discussion doc by filename.
3. **Updated `SCOPE.md`** reflecting the new ruling.

Changes that arrive without all three artifacts will be reverted.
