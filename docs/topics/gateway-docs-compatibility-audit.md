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

### 3.2 Prometheus `/metrics` endpoint — **GAP (high) + internal inconsistency**
`operation.md`: Java exposes OpenMetrics at `/metrics` for Prometheus scraping.
- **Go today:** **no `/metrics` route, and `prometheus/client_golang` is not a dependency** — despite `PRD.md` §Key Architecture Decisions locking "Metrics = `prometheus/client_golang`".
- This is both a drop-in monitoring break (existing scrape configs fail) **and** a PRD-vs-implementation inconsistency that should be reconciled (implement it, or amend the PRD).

### 3.3 Web-UI OAuth2 login flow (`/sso`, `/oidc/callback`) — **GAP (high for UI users)**
`security.md`: the gateway UI authenticates operators via the OAuth2/OIDC handshake (`oidc/callback`).
- **Go today:** `/sso` is a stub 302 and `/oidc/callback` returns 501 (per `admin-api-completeness-gap.java-analyst.md` rows 31–32). The proxy-side JWT validation works; the *interactive UI login* does not. Operators who log in to the web UI via OIDC cannot complete the flow.

### 3.4 Form / preset-user authentication — **GAP (low–medium)**
`security.md`: Java's default `authentication.defaultType` is `form`, backed by `presetUsers` (static user/password/privileges in config) with an RSA `selfSignKeyPair`.
- **Go today:** auth types are OIDC, LDAP (HTTP Basic→bind), NOOP. There is no form login page and no static preset-user store. Operators using form/preset-user auth must switch to OIDC/LDAP.

### 3.5 Built-in TLS termination — **GAP (low–medium)**
`security.md`: Java can terminate HTTPS itself (`http-server.https.*` keystore config).
- **Go today:** plaintext listeners only; TLS is assumed at the load balancer / ingress. Fine for most deployments, but operators relying on gateway-terminated TLS must add an LB.

### 3.6 `externalUrl` field on backends and query history — **GAP (medium, wire-shape)**
`gateway-api.md`: backend records carry an optional `externalUrl`; query history links use it.
- **Go today:** the schema/struct omit `externalUrl` (`admin-api-completeness-gap` M5/M6): `ProxyBackend.externalUrl` is `omitempty` (Java always emits it, even `null`), and `QueryDetail.externalUrl` is always empty (no DB column). Breaks byte-level wire parity and any UI/tooling depending on the field.

### 3.7 `/webapp/findQueryHistory` request field names — **GAP (medium, API-breaking)**
`admin-api-completeness-gap` M-row: Go expects `{userName, backendUrl, queryId, source, page, pageSize}`; Java expects `{user, externalUrl, queryId, source, page, size}`. Three field-name disagreements break API clients written against Java.

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

### 3.12 `pagePermissions` + static `/trino-gateway/assets/*` — **GAP (low)**
`security.md`: per-role UI page gating (`dashboard`/`cluster`/`resource-group`/`selector`/`history`). And `admin-api-completeness-gap` notes `/trino-gateway/assets/{path}` is a 404 stub.
- **Go today:** the compiled UI bundle is served, but per-page permission enforcement is not implemented, and the assets route is stubbed (blocks browser-driven E2E and full UI use).

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
- **3.2 `/metrics`** — implement (PRD already locked the library) or amend the PRD. This is the cleanest fix and removes an internal contradiction.
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
