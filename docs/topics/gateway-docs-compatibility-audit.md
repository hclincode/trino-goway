# Upstream Gateway Docs vs trino-goway — Compatibility Audit

**Date:** 2026-06-03
**Author:** claude
**Status:** Analysis — findings only; any scope change requires team-lead sign-off per `docs/SCOPE.md` §5
**Basis:** `references/trino-gateway/docs/*` (Java gateway user documentation) compared against `docs/PRD.md`, `docs/SCOPE.md`, and the current Go implementation
**Related:** `docs/studies/trino-gateway/admin-api-completeness-gap.java-analyst.md`, `docs/studies/trino-gateway/behavioral-audit.trino-expert.md`

---

## TL;DR

For the **common operator path that `docs/PRD.md` targets** — external (HTTP/gRPC) routing, queryId sticky routing, OAuth2-cookie stickiness, health monitoring, query history, backend admin API — trino-goway is **feature-compatible** with the Java gateway as documented.

The documented divergences fall into two buckets:

1. **Divergent by design** — already ruled out in `docs/PRD.md`/`docs/SCOPE.md` (MVEL file rules, header routing, SQL content routing, pluggable router modules, Oracle, per-group DB, spooled sticky). No action; these are intentional.
2. **Gaps not yet captured in PRD/SCOPE** — surfaced by this audit. Several are **drop-in-cutover risks** for operators migrating from a stock Java deployment, and one is an **internal inconsistency** (PRD locks Prometheus `/metrics`, but it is unimplemented and not even a dependency).

The recommendations in the last section flag which gaps are GA-blocking for drop-in parity vs. which can be accepted-and-documented or folded into SCOPE as non-groomed.

---

## Sources reviewed

`design.md`, `routing-logic.md`, `routers.md`, `routing-rules.md`, `security.md`, `gateway-api.md`, `operation.md`, `installation.md`, `users.md` (adopter list — not relevant). Asset/PDF files skipped.

Status labels used below:

- **COMPATIBLE** — behavior matches as documented.
- **BY-DESIGN** — divergence already locked in PRD/SCOPE.
- **GAP** — Java capability with no Go equivalent, *not* currently captured in PRD/SCOPE.
- **IMPROVEMENT** — Go intentionally behaves better than Java.

---

## 1. Compatible (matched behavior)

| Area | Java (docs) | trino-goway | Notes |
|---|---|---|---|
| External routing — HTTP | `rulesType: EXTERNAL`; POST request metadata, expect `{routingGroup, errors, externalHeaders}` | `routing.type: EXTERNAL`, HTTP transport | Request/response field contract matches (`routing-rules.md` §EXTERNAL ↔ PRD §External Routing Contract). |
| External routing — gRPC | (not in Java) | gRPC transport, same fields | Go superset; gRPC has no Java equivalent. |
| `excludeHeaders` / `propagateErrors` / `externalHeaders` REPLACE | documented | `ExternalConfig.ExcludeHeaders`, `proxy.propagateErrors`, REPLACE semantics | Matches. |
| QueryId sticky routing | extract queryId from `v1/statement/.../queryid/...`, pin to cluster | cache + 3-step recovery | Matches; recovery chain is a Go-side hardening (Hard Invariant #4). |
| OAuth2 cookie stickiness | `gatewayCookieConfiguration` + `oauth2GatewayCookieConfiguration` (`routingPaths`/`deletePaths`/`lifetime`, default 10m) | `TG.OAUTH2` cookie, `cookie.ttl` default 10m, `wireCompat` | Matches incl. default lifetime; HMAC-signed. |
| Cluster health states | `TrinoStatus`: PENDING / HEALTHY / UNHEALTHY | `internal/monitor/status.go` identical tri-state | Matches. |
| Health endpoints | `/trino-gateway/livez` (always 200), `/trino-gateway/readyz` (200 after DB + first probe, else 503) | same semantics | Matches (`probes_e2e_test.go`). |
| Backend admin API | `/gateway/backend/modify/{add,update,delete}`, `/activate|deactivate/{name}`, `/all`, `/active` | implemented (`internal/admin/backend.go`) | Matches the documented CRUD surface. |
| Query history | recorded + UI links to cluster query pages | persisted (Postgres/MySQL) + `/trino-gateway/api/queryHistory*` | Matches (see §3 for `externalUrl` wire gap). |
| Auth — OAuth2/OIDC | `authentication.oauth` | `auth.type: OIDC` | Functionally compatible; config surface differs (loose compat, by design). |
| Auth — LDAP | `Form/LDAP` | `auth.type: LDAP` (HTTP Basic → LDAP bind) | Compatible *as a credential check*; not the Java web-form login (see §3). |
| Authorization roles | regex `admin`/`user`/`api` | `auth.authorization.{admin,user,api}` regex | Matches the regex model. |
| Graceful shutdown | deactivate → queries still retrievable by queryId | deactivate + history lookup in recovery chain | Matches. |
| `http-server.process-forwarded=true` requirement | mentioned in `installation.md` | **documented prominently** (Hard Invariant #5, README, config) | Matches and improves (see §4). |

---

## 2. Divergent by design (already locked in PRD/SCOPE)

These need no action — they are intentional and recorded. Listed for completeness so reviewers don't re-litigate them.

| Java capability | Ruling | Reference |
|---|---|---|
| Default `X-Trino-Routing-Group` header routing | Out (non-groomed) — operators implement in their external service | `SCOPE.md` §2 Header-Based Routing |
| File-based MVEL routing rules (priorities, `state`, flow control, hot-reload) | Out (permanent) | `SCOPE.md` §2 MVEL; `routing-rules.md` is the full feature we drop |
| SQL content routing / `trinoQueryProperties` (tables/catalogs/schemas/queryType) | Out (permanent) — envelope sent but SQL-parsed fields empty | `PRD.md` §Routing Contract, `SCOPE.md` §2 SQL Content Routing |
| Pluggable router `modules` (Guice classloading) | Out — no DI/runtime classloading | `PRD.md` §Key Architecture Decisions (DI: none) |
| Oracle backend / per-routing-group DB isolation | Non-groomed | `SCOPE.md` §2 |
| `/v1/spooled/*` gateway-level sticky routing | Non-groomed (3 architectural blockers) | `SCOPE.md` §2, `PRD.md` §Non-Groomed |

---

## 3. Gaps NOT yet captured in PRD/SCOPE  ⚠️ (the actionable findings)

Each item is a documented Java capability with no current Go equivalent and **no entry** in PRD/SCOPE. Severity is from a drop-in-replacement standpoint.

### 3.1 Load-aware routing + cluster load stats — **GAP (medium)**
`routers.md`/`operation.md`: Java ships `StochasticRoutingManager` (random) and `QueryCountBasedRouterProvider` (routes to the least-loaded cluster *per user* using running/queued query counts). Stats come from `clusterStatsConfiguration.monitorType: UI_API|JDBC` + the `backendState` credentials block.
- **Go today:** the monitor probes `/v1/info` for health only — no running/queued counts, no per-user load awareness. Intra-group cluster selection is not load-based.
- **Knock-on:** `GET /api/public/backends/{name}/state` is documented to return *live query counts*; Go can only return health.
- **PRD note:** PRD says external routing returns a group and the gateway "picks a cluster from that group" but never specifies a load-aware strategy. Operators relying on `QueryCountBasedRouter` lose that behavior.

### 3.2 Prometheus `/metrics` endpoint — **RESOLVED (Phase 9, Tasks 56–64)**
`operation.md`: Java exposes OpenMetrics at `/metrics` for Prometheus scraping.
- **Go today:** the gateway exposes OpenMetrics at `/metrics` on the **admin listener** (configurable via `metrics.{enabled,path}`; enabled by default). Metrics are served from a dedicated `*prometheus.Registry` (the global default registry is never used) via `promhttp.HandlerFor(..., {EnableOpenMetrics:true})`. `prometheus/client_golang` is a direct dependency. This reconciles the prior PRD-vs-implementation inconsistency — `PRD.md` §Key Architecture Decisions locked "Metrics = `prometheus/client_golang`", and that decision is now implemented and marked done.
- Coverage: Go runtime (`go_*`) + process (`process_*`) collectors, HTTP server, proxy/forwarding, backend health/activation, routing/recovery-chain, and auth/persistence metrics, all under the `trino_goway_*` namespace. E2E-verified by `internal/e2e/metrics_e2e_test.go`.

#### Java → Go metric-name mapping (dashboard migration)

The Go gateway does not reproduce Java metric names verbatim — the Java names are JMX/Dropwizard-style and per-cluster, whereas the Go names follow Prometheus/OpenMetrics conventions (`trino_goway_*` namespace, dimensions carried as labels rather than baked into the metric name). Use this table to port existing dashboards/alerts:

| Java (concept / JMX) | Go metric | Notes |
|---|---|---|
| JVM heap / GC / threads | `go_*` (e.g. `go_goroutines`, `go_gc_duration_seconds`, `go_memstats_*`) | Go runtime collector; the Go-native equivalent of the JVM metrics. |
| Process CPU / RSS / FDs / uptime | `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds`, `process_start_time_seconds` | Process collector. |
| `ClusterStats` / `{cluster}_TrinoStatus*` | `trino_goway_backend_status{backend,status}` | `status` ∈ `healthy\|unhealthy\|pending`; one series per backend per status (1 on the current status, 0 otherwise). |
| `ClusterMetricsStats.getActivationStatus` | `trino_goway_backend_activation_status{backend}` | `1` active, `0` inactive, `-1` unknown. |
| (aggregate cluster counts) | `trino_goway_backends{status}`, `trino_goway_backends_active` | Cluster-wide rollups. |
| `ProxyHandlerStats` request counters | `trino_goway_proxy_requests_total{backend,routing_group,outcome}` | `outcome` ∈ `ok\|fallback\|error\|kill_query`. |
| Proxy upstream latency | `trino_goway_proxy_upstream_duration_seconds{backend}` | Histogram. |
| Oversized-response / fail-loud | `trino_goway_proxy_oversized_responses_total` | The 502 oversized-`/v1/statement` path. |
| Sticky-routing cache writes | `trino_goway_proxy_statement_cache_writes_total` | Hard Invariant #3 (cache write before flush). |
| External-router calls | `trino_goway_router_calls_total{transport,outcome}`, `trino_goway_router_call_duration_seconds{transport}` | `transport` ∈ `http\|grpc`; `outcome` ∈ `ok\|error\|timeout\|fallback`. |
| Routing cache hit/miss | `trino_goway_routing_cache_events_total{event}` | `event` ∈ `hit\|miss`. |
| Recovery-chain steps | `trino_goway_recovery_chain_steps_total{step}` | `step` ∈ `history\|probe\|default`. |
| KILL QUERY routing | `trino_goway_kill_query_routes_total` | |
| HTTP server (any listener) | `trino_goway_http_requests_total{listener,method,pattern,code}`, `trino_goway_http_request_duration_seconds{listener,method,pattern}`, `trino_goway_http_requests_in_flight{listener}` | `listener` ∈ `proxy\|admin`; `pattern` is the route template (bounded cardinality). |
| Auth decisions | `trino_goway_auth_requests_total{type,result}` | `type` ∈ `oidc\|ldap\|noop`; `result` ∈ `allow\|deny`. |
| JWKS refresh / key count | `trino_goway_jwks_refresh_total{result}`, `trino_goway_jwks_keys` | |
| DB reachability / history inserts / backend reloads | `trino_goway_db_up`, `trino_goway_query_history_inserts_total{result}`, `trino_goway_backend_refresh_total{result}` | `result` ∈ `ok\|error`. |

> **Migration note:** dashboards that selected a single cluster by metric name (e.g. `clusterA_TrinoStatus`) must switch to a label selector (e.g. `trino_goway_backend_status{backend="http://clusterA:8080",status="healthy"}`).

### 3.3 Web-UI OAuth2 login flow (`/sso`, `/oidc/callback`) — **RESOLVED (Task 66)**
`security.md`: the gateway UI authenticates operators via the OAuth2/OIDC handshake (`oidc/callback`).
- **Go today:** the full authorization-code flow is implemented. `POST /sso` returns the IdP authorization URL in the `Result` envelope and sets a short-lived, HttpOnly, callback-scoped state cookie carrying CSRF `state` + `nonce`. `GET /oidc/callback` verifies the state, exchanges the code at the token endpoint (`auth.OIDCWebLogin`, endpoints resolved via OIDC discovery or explicit config), validates the id_token signature (JWKS) and nonce, sets the non-HttpOnly `token` cookie the SPA reads on mount (`useConsumeOidcCookie`), clears the state cookie, and redirects to `/trino-gateway`. Config: `auth.oidc.redirectUrl` (+ optional `authorizationEndpoint`/`tokenEndpoint`). When `redirectUrl` is unset, `/sso` reports "SSO not configured" and only Bearer-token API auth is available.

### 3.4 Form / preset-user authentication — **GAP (low–medium)**
`security.md`: Java's default `authentication.defaultType` is `form`, backed by `presetUsers` (static user/password/privileges in config) with an RSA `selfSignKeyPair`.
- **Go today:** auth types are OIDC, LDAP (HTTP Basic→bind), NOOP. There is no form login page and no static preset-user store. Operators using form/preset-user auth must switch to OIDC/LDAP.

### 3.5 Built-in TLS termination — **GAP (low–medium)**
`security.md`: Java can terminate HTTPS itself (`http-server.https.*` keystore config).
- **Go today:** plaintext listeners only; TLS is assumed at the load balancer / ingress. Fine for most deployments, but operators relying on gateway-terminated TLS must add an LB.

### 3.6 `externalUrl` field on backends and query history — **RESOLVED**
`gateway-api.md`: backend records carry an optional `externalUrl`; query history links use it.
- **Backend side (M6) — RESOLVED (Task 68):** `gateway_backend` now has an `external_url` column (`migrations/00003_add_backend_external_url.sql`); `persistence.Backend.ExternalURL` persists it; `ProxyBackend.externalUrl` dropped `omitempty` and is always emitted. When unset it falls back to `proxyTo`, matching Java's `ProxyBackendConfiguration.getExternalUrl()`.
- **Query-history side (M5) — RESOLVED (Task 67):** `query_history` now has an `external_url` column (`migrations/00004_add_query_history_external_url.sql`); it is stamped at capture time by resolving the routed backend's external URL (Java's `ProxyRequestHandler` sets `queryDetail.externalUrl` from the routing destination). `QueryDetail.externalUrl` is emitted on `/trino-gateway/api/queryHistory`, falling back to `backendUrl` for rows captured before the column existed.

### 3.7 `/webapp/findQueryHistory` request field names — **RESOLVED (deviation documented, Task 71)**
`admin-api-completeness-gap` M-row: Go expects `{userName, backendUrl, queryId, source, page, pageSize}`; Java expects `{user, externalUrl, queryId, source, page, size}`. This is a deliberate Go-side naming convention; the rebuilt web UI aligns its request to the Go names (`webapp/src/api/endpoints/history.ts`), and the server-side `userName`/`backendUrl`/`pageSize` filters are verified end to end (`TestAdmin_WebappFindQueryHistory_Filters`). Clients written against Java's field names must use the Go names. The companion verb item — `getRoutingRules` is POST and answers 204 for external routing (no 405) — is also resolved (Task 71).

### 3.8 `extraWhitelistPaths` — **GAP (low)**
`design.md`/`installation.md`: operators configure extra request URIs (regexes) to forward to Trino.
- **Go today:** forwarded paths are fixed in code; no configurable whitelist. Operators forwarding non-default paths lose that knob.

### 3.9 `routing.forwardedHeadersEnabled=false` strip mode — **GAP (low)**
`design.md`: Java can be configured to strip all client `X-Forwarded-*`/`Forwarded` headers and skip adding its own (so `nextUri` points straight at the backend).
- **Go today:** `internal/proxy/headers.go` *always* injects `X-Forwarded-*`. No strip toggle. Acceptable given our architecture (Hard Invariant #1/#5 depend on forwarding), but the alternate deployment mode is unavailable.

### 3.10 Configurable `databaseCache` — **GAP (low)**
`operation.md`: Java caches the backend list (`expireAfterWrite`/`refreshAfterWrite`) so routing survives transient DB outages.
- **Go today:** there is *implicit* resilience — `runBackendRefresh` reloads every 15s and only overwrites the in-memory set on success, so the last-known list persists if the DB blips — but no configurable cache with explicit expiry/refresh semantics.

### 3.11 `requestAnalyzerConfig.isClientsUseV2Format` (commercial V2 client protocol) — **GAP (low)**
`routing-rules.md`: a toggle for commercial Trino V2-style request structure. Not supported in Go. Niche; relevant only to commercial distributions.

### 3.12 `pagePermissions` + static `/trino-gateway/assets/*` — **RESOLVED**
`security.md`: per-role UI page gating (`dashboard`/`cluster`/`resource-group`/`selector`/`history`). And `admin-api-completeness-gap` notes `/trino-gateway/assets/{path}` is a 404 stub.
- **Static assets — RESOLVED (Task 65):** the production UI bundle is embedded via `//go:embed all:web/dist` (built with `make webapp`; Vite/pnpm, base path `/trino-gateway/`). `serveIndex`/`serveAssets`/`serveLogoSVG` serve it; `/trino-gateway/assets/*` returns content-hashed assets with immutable caching, and unmatched GETs under `/trino-gateway/*` fall back to `index.html` (SPA deep links) without shadowing API/probe routes. A code placeholder is served when no bundle is built.
- **Page permissions — RESOLVED (Task 70):** `getUIConfiguration` now returns `disablePages` (config `ui.disablePages`, always an array). Per-role page permissions are configured via `auth.authorization.pagePermissions` (role→`"page1_page2"`); the resolved per-user union is returned in `/userinfo`'s `permissions` (Java `processPagePermissions` parity: a role with no entry grants all pages). The UI hides sidebar entries by role, `permissions`, and `disablePages`.

---

## 4. Where trino-goway improves on Java (IMPROVEMENT)

| Improvement | Java behavior | trino-goway |
|---|---|---|
| Oversized response handling | silently truncates body to `maxBodySize` → malformed JSON | returns `502 upstream response too large` (fail-loud) — `PRD.md` Goal 3 |
| JWKS fetching | per-request fetch (rate-limit risk) | background TTL cache (`jwksTtlSecs`, default 300s) — `PRD.md` Goal 3 |
| `process-forwarded=true` | buried in Java docs | prominent (Hard Invariant #5, README, `configs/config.example.yaml`) |
| Correctness tooling | — | race detector + goroutine-leak checks in CI — `PRD.md` Goal 4 |
| Deployment | JVM, Guice/Airlift startup, heap tuning | single static binary, no JVM |

---

## 5. Recommendations

These are findings, not scope changes. Per `docs/SCOPE.md` §5, promoting any item into scope needs a written rationale + team-lead sign-off.

**Reconcile before any "drop-in replacement" GA claim (PRD Goal 1):**
- **3.2 `/metrics`** — **DONE (Phase 9).** Implemented on the admin listener under the `trino_goway_*` namespace; the prior PRD-vs-implementation contradiction is reconciled. See §3.2 above for the Java→Go metric mapping.
- **3.3 UI OAuth2 login** — `/sso` + `/oidc/callback` are stubs; the UI is unusable with OIDC until completed. Decide: implement, or document the UI as "API-only / reverse-proxy-auth" for v1.
- **3.6 / 3.7 `externalUrl` + `findQueryHistory` field names** — wire-shape/API-breaking. Either match Java's shape or document the deviation explicitly (the completeness study already proposes both paths).

**Candidate SCOPE additions (non-groomed; decide on operator demand):**
- **3.1 load-aware routing / cluster stats** — the most material *functional* gap vs Java. Note that PRD's "external service owns routing" philosophy can't cover *intra-group least-loaded* selection, since the gateway picks the cluster. Worth an explicit ruling.
- **3.4 form/preset-user auth**, **3.5 gateway TLS termination**, **3.8 `extraWhitelistPaths`**, **3.10 `databaseCache`** — small, well-bounded; add as non-groomed with promotion conditions.

**Accept-and-document (low impact / by-architecture):**
- **3.9 `forwardedHeadersEnabled=false`**, **3.11 V2 client format**, **3.12 page permissions + assets stub** (assets stub is really a UI-completeness task, tracked separately).

**Suggested next step:** fold §3.2/3.3/3.6/3.7 into the existing GA checklist (they are correctness/parity, not new scope), and open a SCOPE sign-off discussion for §3.1 specifically, since it is the one place the "external service owns all routing" model has a genuine blind spot.

---

*Reference: `references/trino-gateway/docs/*` · `docs/PRD.md` · `docs/SCOPE.md` · `docs/studies/trino-gateway/admin-api-completeness-gap.java-analyst.md`*
