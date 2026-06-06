# trino-goway vs trino-gateway — Use-Case Comparison & Checklist

> Maps every use case in [`trino-gateway-use-cases.md`](./trino-gateway-use-cases.md) (the
> Java reference) to the **current** state of the Go rewrite (`trino-goway`), with a checklist
> and evidence.
>
> - **trino-goway:** `main` @ `01c1346` (post Phase 12: live cluster stats + M7 public-state
>   shape; the reference `routing-service` is at Phase 9: SQL-aware routing inputs).
> - **trino-gateway:** `references/trino-gateway` (~`171ce25`).
> - **Date:** 2026-06-06.
> - Supersedes the stale `docs/studies/trino-gateway/admin-api-completeness-gap.java-analyst.md`
>   (dated 2026-05-29, before Phase 10 closed M5/M6/M2/M3 and the OAuth2/assets work).

## Status legend

| Mark | Meaning |
|---|---|
| ✅ | **Implemented & compatible** — behavior matches the Java gateway. |
| ➕ | **Implemented & enhanced** — goway does this *and more* than Java (superset). |
| ⚠️ | **Partial / wire-divergent** — present but differs from Java in shape, format, or completeness. |
| ❌ | **Not implemented** — absent (usually an intentional v1/architecture decision). |

A checked box `- [x]` means the capability is present in goway (✅/➕, or ⚠️ where the *function*
works but a wire detail differs). An unchecked box `- [ ]` means the capability is materially
missing or stubbed (❌, or ⚠️ where a real sub-feature is absent).

## Scoreboard

| Area | Total | ✅/➕ | ⚠️ | ❌ |
|---|---|---|---|---|
| A. Proxy / protocol (PXY) | 12 | 10 | 2 | 0 |
| B. Routing (RTG) | 9 | 6 | 1 | 2 |
| C. Monitoring & lifecycle (MON) | 10 | 10 | 0 | 0 |
| D. Admin / management API (ADM) | 26 | 21 | 5 | 0 |
| E. Auth & authz (AUTH) | 12 | 10 | 2 | 0 |
| F. Web UI (UI) | 9 | 7 | 1 | 1 |
| G. Observability (OBS) | 3 | 3 | 0 | 0 |
| **Total** | **81** | **67** | **11** | **3** |

**Headline:** the Trino-protocol proxy, routing-group resolution, health monitoring,
lifecycle, persistence, live queued/running cluster stats (UC-MON-02 / M7), the full
admin/management API surface, regex authz, OIDC, and the Web-UI backend are all present and
compatible. SQL-aware routing (UC-RTG-04) is now realized **in the reference `routing-service`**
(Phase 9) rather than the gateway, so content-aware routing works end-to-end. The three
remaining hard gaps are all **intentional architecture decisions**: no internal MVEL rules
engine and no internal routing-rules CRUD/editor (goway delegates routing policy to an
external service and keeps the proxy thin).

---

## A. Trino-protocol proxying — `UC-PXY-*`

- [x] **UC-PXY-01** Submit a query — ✅ `internal/proxy/forward.go` (verbatim forward, 502 `no backend available`). E2E `TestE2E_PostStatement_RoutesToBackend`.
- [x] **UC-PXY-02** Sticky follow-up polls — ✅ cache write **before** flush (Hard Invariant #3). `internal/proxy/forward.go`, `internal/routing/cache.go`. E2E `TestE2E_PostStatement_StickyRouting`.
- [x] **UC-PXY-03** KILL QUERY routing — ✅ regex detect → history lookup → owner backend. `internal/proxy/forward.go`, `internal/routing/router.go`. E2E `kill_query_e2e_test.go`.
- [x] **UC-PXY-04** Cache-miss recovery — ➕ **enhanced**: 3-step chain (history → concurrent `HEAD` probe fan-out → first-active). The HEAD-probe step is a goway addition over Java. `internal/routing/recovery.go`. E2E `recovery_chain_e2e_test.go`.
- [ ] **UC-PXY-05** Sticky OAuth2 routing by cookie — ⚠️ the `TG.OAUTH2` cookie is **issued, validated, and wire-compatible** (HMAC-SHA256, airlift TTL format), but the gateway **does not yet read the cookie's pinned backend during routing** (`TestE2E_Cookie_StickyRouting` is the deferred Task-53 item). Issuance/validation: `internal/proxy/cookie.go`. **Gap:** routing-by-cookie-pin.
- [x] **UC-PXY-06** Tamper-evident cookie — ✅ bad HMAC/undecodable → `500` (Hard Invariant #5). `internal/proxy/cookie.go`. E2E `TestE2E_Cookie_TamperedHMAC_Returns500`.
- [x] **UC-PXY-07** Forwarded headers — ✅ `X-Forwarded-For` appends; Proto/Host/Port set. `internal/proxy/headers.go`. E2E `TestE2E_ForwardedHeaders_*`.
- [x] **UC-PXY-08** Hop-by-hop stripping (both ways) — ✅ Hard Invariant #7. E2E `TestE2E_HopByHopStripped_*`.
- [x] **UC-PXY-09** Preserve redirects — ✅ `CheckRedirect → ErrUseLastResponse` (Hard Invariant #2). E2E `TestE2E_Inv2_NoRedirectFollowing`.
- [ ] **UC-PXY-10** Spooled-segment sticky routing — ⚠️ cookie wire-format + `/v1/spooled/*` design exist (studies + `cookie.go`), but full segment stickiness rides the same cookie-pin-during-routing path deferred in UC-PXY-05. **Gap:** end-to-end spooled stickiness not verified.
- [x] **UC-PXY-11** Stream large bodies — ✅ only `/v1/statement` is buffered (bounded by `responseSize`); all else `io.Copy` (Hard Invariant #4). E2E `TestE2E_StreamingPath_NotBuffered`.
- [x] **UC-PXY-12** Inject upstream headers (REPLACE) — ✅ Hard Invariant #9. E2E `TestE2E_ExternalHTTP_ExternalHeadersReplace`.

---

## B. Routing — `UC-RTG-*`

- [ ] **UC-RTG-01** Internal MVEL rules engine — ❌ **intentionally absent.** goway supports only `routing.type: EXTERNAL` (validated at startup; `internal/config/config.go`). No in-process rule evaluation. *Architecture decision:* routing policy lives in an external service.
- [ ] **UC-RTG-02** Routing-rules CRUD via API/UI — ❌ stub. `POST /webapp/getRoutingRules` → `204` (signals "external routing in use"); `updateRoutingRules` returns empty list. `internal/admin/webapp.go:298`. No internal rule storage/editor.
- [x] **UC-RTG-03** External routing service — ➕ **enhanced**: goway supports **HTTP *and* gRPC** transports (Java external router is HTTP-only) and ships a standalone reference `routing-service/`. `internal/routing/external_http.go`, `external_grpc.go`. E2E `external_{http,grpc}_routing_e2e_test.go`, `routing_service_e2e_test.go`.
- [x] **UC-RTG-04** SQL-aware routing inputs — ⚠️ **relocated to the routing-service.** The *gateway* still ships no SQL parser — it forwards the raw SQL `body` and leaves the `trinoQueryProperties` parser fields empty (`errorMessage: "trino-parser not available in Go v1"`, `internal/routing/external_http.go`). But the reference `routing-service` now **parses that body** (Phase 9 / RS-15–17) to derive query type, catalogs, schemas and tables, exposing them to `expr`/Starlark rules — so content-aware routing works end-to-end in the goway system, just realized in the router (cf. RTG-01/RTG-03 delegation). `routing-service/internal/sqlmeta/`, `routing-service/internal/engine/`. Tests: routing-service `sqlmeta`/engine unit + `internal/integration/content_routing_test.go`.
- [x] **UC-RTG-05** Routing group → backend resolution — ✅ `internal/routing/router.go`. E2E `routing_groups_e2e_test.go`.
- [x] **UC-RTG-06** Default / single-cluster mode — ✅ no external config → everything routes to `defaultGroup`. E2E `TestE2E_SingleCluster_NoExternalRouter`.
- [x] **UC-RTG-07** Fallback on router failure — ✅ router error/timeout → `defaultGroup` (not rejected). E2E `TestE2E_ExternalHTTP_FallbackOnRouterDown`, `TestE2E_ExternalHTTP_TimeoutFallback`.
- [x] **UC-RTG-08** `excludeHeaders` policy — ✅ both transports (response-side) + HTTP request-side. E2E `TestE2E_ExternalHTTP_ExcludeHeaders`.
- [x] **UC-RTG-09** Header/context forwarding to router — ✅ method, URI, remote, params, user; ➕ also `trino_source` + `client_tags` (Task 72). `internal/routing/external_grpc.go::buildProtoRequest`. *(Note: the gateway sends the `trinoQueryProperties` parser fields empty; the routing-service derives query type/tables/catalogs from the forwarded `body` itself — see RTG-04.)*

---

## C. Backend health, config & lifecycle — `UC-MON-*`

- [x] **UC-MON-01** Active health probes — ✅ `/v1/info`, `200 {"starting":false}` ⇒ HEALTHY. `internal/monitor/monitor.go`. E2E `health_monitoring_e2e_test.go`.
- [x] **UC-MON-02** Live queued/running cluster stats — ✅ live queued/running via a config-selectable collector (INFO_API default / UI_API / METRICS; JDBC/JMX v1-deferred). `internal/clusterstats/`. E2E `TestE2E_ClusterStats_*`.
- [x] **UC-MON-03** Hot backend list reload — ✅ 15s DB refresher → monitor. `cmd/trino-goway/main.go`. E2E `TestE2E_Monitor_NewlyAddedBackend`.
- [x] **UC-MON-04** Liveness probe — ✅ `/trino-gateway/livez` → `200 ok`. `internal/admin/health.go`.
- [x] **UC-MON-05** Readiness probe — ✅ `503` until first probe cycle, then `200` (Hard Invariant #11). *(Minor: body text `not ready` vs Java `Trino Gateway is still initializing`.)* E2E `probes_e2e_test.go`.
- [x] **UC-MON-06** YAML config + defaults + validation — ✅ `internal/config/config.go`; rejects dup ports, bad driver, non-EXTERNAL routing, OIDC w/o jwksUrl, etc.
- [x] **UC-MON-07** Coordinated startup — ✅ ports bind before workers; readyz flips after first probe. `internal/lifecycle/server.go`.
- [x] **UC-MON-08** Graceful shutdown — ✅ SIGTERM/SIGINT, 30s deadline, workers joined. `cmd/trino-goway/main.go`.
- [x] **UC-MON-09** Durable persistence — ✅ Postgres/MySQL via `sqlx` + embedded `goose` migrations. `internal/persistence/`.
- [x] **UC-MON-10** Connection isolation — ✅ proxy/monitor/router clients (Hard Invariant #12), plus a **conditional 4th `statsClient`** built only for the `UI_API`/`METRICS` cluster-stats collectors (Phase 12; INFO_API/NOOP reuse the monitor verdict, no extra client). `cmd/trino-goway/main.go:98`. E2E `TestE2E_Inv12_ThreeHTTPClients_*`.

---

## D. Admin / management API — `UC-ADM-*`

- [x] **UC-ADM-01** `GET /gateway` ping — ✅ `"ok"`. `internal/admin/backend.go:221`.
- [x] **UC-ADM-02** List all backends — ✅ `backend.go:227`.
- [x] **UC-ADM-03** List active backends — ✅ `backend.go:243`.
- [x] **UC-ADM-04** Activate — ✅ `backend.go:259`. *(Like Java's `/gateway/activate`, does not flip in-memory monitor state; only `/entity` does — consistent with Java.)*
- [x] **UC-ADM-05** Deactivate — ✅ `backend.go:271`.
- [x] **UC-ADM-06** Add — ✅ `backend.go:283`.
- [x] **UC-ADM-07** Update — ✅ (upsert) `backend.go:300`.
- [x] **UC-ADM-08** Delete (raw-string body) — ✅ `backend.go:317`.
- [x] **UC-ADM-09** List entity types — ✅ `["GATEWAY_BACKEND"]` `backend.go:340`.
- [ ] **UC-ADM-10** Upsert entity — ⚠️ upsert + monitor flip (PENDING/UNHEALTHY) ✅; unknown type → `500` ✅; **but echoes the `ProxyBackend` JSON on success whereas Java returns an empty body** (M8). `backend.go:346`.
- [ ] **UC-ADM-11** List entities — ⚠️ `GET /entity/{type}`: unknown type returns **`200 []`** (goway, per USE_STORIES §4.2) vs Java **`500`**. `backend.go:380`.
- [x] **UC-ADM-12** Public list backends — ✅ `backend.go:157`.
- [x] **UC-ADM-13** Public get backend (404 on miss) — ✅ `backend.go:173`.
- [x] **UC-ADM-14** Public backend state — ✅ returns `ClusterStatsResponse` (`{clusterId,runningQueryCount,queuedQueryCount,numWorkerNodes,trinoStatus,proxyTo,externalUrl,routingGroup,userQueuedCount}`) — M7 closed; counts live under UI_API/METRICS, `0` under INFO_API; unobserved default populated from persistence (choice b). `backend.go::getPublicBackendState`. E2E `TestE2E_ClusterStats_InfoAPI_PublicStateShape`.
- [x] **UC-ADM-15** `getAllBackends` + stats — ✅ live `status`; `queued`/`running` now flow from the stats store (live under UI_API/METRICS; `0` under INFO_API default). `webapp.go`. E2E `TestE2E_ClusterStats_*`.
- [x] **UC-ADM-16** `saveBackend` — ✅ `Result<ProxyBackend>`. `webapp.go:310`.
- [x] **UC-ADM-17** `updateBackend` — ✅ `webapp.go:327`.
- [x] **UC-ADM-18** `deleteBackend` (full object, name only) — ✅ `Result<bool>`. `webapp.go:344`.
- [ ] **UC-ADM-19** `getDistribution` — ⚠️ all aggregate fields present and `lineChart` now populated (Task 69), **but `startTime` renders `…Z` (Zulu) vs Java `…+00:00`** (M4). `webapp.go:160,243`.
- [x] **UC-ADM-20** `getUIConfiguration` — ✅/⚠️ goway returns a **superset** `{authType, disablePages}` (Java returns only `{disablePages}`; `disablePages` was added in Task 70). *(Method: goway `POST` vs Java `GET`.)* `webapp.go:280`.
- [x] **UC-ADM-21** Query history (legacy), non-ADMIN scoped — ✅ `query.go:45`. E2E `query_history_e2e_test.go`.
- [x] **UC-ADM-22** Active backends (legacy) — ✅ `query.go:81`.
- [x] **UC-ADM-23** Query distribution (legacy), scoped — ✅ `query.go:98`.
- [ ] **UC-ADM-24** `findQueryHistory` — ⚠️ non-ADMIN server-side scoping ✅, but **request field names differ**: goway `{userName, backendUrl, queryId, source, page, pageSize}` vs Java `{user, externalUrl, queryId, source, page, size}` (M1). *(Method: goway `POST`, same as Java.)* `webapp.go:110`.
- [x] **UC-ADM-25** `QueryDetail.externalUrl` populated — ✅ **fixed (Task 67)**; falls back to `backendUrl`. `query.go:25`, migration `00004`.
- [x] **UC-ADM-26** `ProxyBackend` wire shape — ✅ **fixed (Task 68)**: `externalUrl` always emitted, falls back to `proxyTo`; `,omitempty` dropped. `backend.go:19,77`.

---

## E. Authentication & authorization — `UC-AUTH-*`

- [ ] **UC-AUTH-01** No-auth (dev) mode — ⚠️ goway NOOP treats the caller as `anonymous` and grants **no roles unless an authz regex matches** — Java NOOP grants `ADMIN`+`USER`+`API` unconditionally. Documented divergence (USE_STORIES §5.1). `internal/auth/noop.go`, `roles.go`.
- [x] **UC-AUTH-02** OIDC / OAuth2 bearer — ✅ JWKS fetch at startup + background refresh; Bearer JWT validation. `internal/auth/oidc.go`. E2E `auth_oidc_e2e_test.go`.
- [ ] **UC-AUTH-03** Form / LDAP login → JWT — ⚠️ LDAP works as **HTTP Basic** middleware on protected endpoints (`internal/auth/ldap.go`), but the `POST /login` JSON flow returns an **error envelope** for LDAP (no self-signed JWT issuance like Java). `internal/admin/authhandlers.go:52`.
- [x] **UC-AUTH-04** Web-UI OAuth2 initiation (`/sso`) — ✅ **implemented (Task 66)**: returns IdP auth URL, sets HttpOnly state+nonce cookie. `authhandlers.go:78`.
- [x] **UC-AUTH-05** OIDC callback — ✅ **implemented (Task 66)**: state/nonce verify, code exchange, `token` cookie, redirect. `authhandlers.go:114`.
- [x] **UC-AUTH-06** Role mapping via regex — ✅ `memberOf` from JWT `groups`/`memberOf` or LDAP attr; empty regex never matches. `internal/auth/roles.go`.
- [x] **UC-AUTH-07** Role enforcement — ✅ per-route middleware; cross-role denial; proxy port unauthenticated. `internal/admin/router.go`. E2E `auth_noop_e2e_test.go`.
- [x] **UC-AUTH-08** Page permissions — ✅ **added (Task 70)** `auth.ResolvePagePermissions`. `authhandlers.go:222`.
- [x] **UC-AUTH-09** Userinfo — ✅ `{userId, userName, roles, permissions}`, permissions now populated. `authhandlers.go:204`.
- [x] **UC-AUTH-10** Login-type discovery — ✅ `oauth`/`form`/`none`. `authhandlers.go:31`.
- [x] **UC-AUTH-11** Logout — ✅ success envelope. `authhandlers.go:69`.
- [x] **UC-AUTH-12** Session cookie — ✅ `token` cookie (1-day) set on OIDC callback. `authhandlers.go:158`.

---

## F. Web UI — `UC-UI-*`

> The goway Web UI is a **rebuilt modern React app** (`webapp/`, built via `make webapp` and
> embedded with `//go:embed`), functionally covering the Java UI's pages.

- [x] **UC-UI-01** Serve SPA shell — ✅ **implemented (Task 65)** + SPA deep-link fallback. `internal/admin/router.go:283` (fallback `:268`).
- [x] **UC-UI-02** Serve static assets — ✅ **implemented (Task 65)**, content-hashed + traversal-guarded (was MISSING in the old study). `router.go:172`.
- [x] **UC-UI-03** Serve logo — ✅ (embedded bundle, placeholder fallback). `router.go:171`.
- [x] **UC-UI-04** Root redirect — ✅ `303 → /trino-gateway`. `router.go:156`.
- [x] **UC-UI-05** Dashboard — ✅ backend supports it (`getDistribution`/`getAllBackends`); queued/running tiles are live under the UI_API/METRICS collectors (`0` under the INFO_API default).
- [x] **UC-UI-06** Clusters page — ✅ full CRUD via admin API.
- [x] **UC-UI-07** Query history page — ✅ deep-links resolve via `externalUrl` (ADM-25).
- [ ] **UC-UI-08** Routing-rules editor — ❌ hidden (204 from `getRoutingRules`); no internal rules (RTG-01/02).
- [x] **UC-UI-09** Login pages — ✅ `loginType` + OIDC SSO + `disablePages`/permissions drive the UI. ⚠️ LDAP form-login limited (AUTH-03).

---

## G. Observability — `UC-OBS-*`

- [x] **UC-OBS-01** Metrics endpoint — ➕ `/metrics` OpenMetrics on the **admin** listener; **Go runtime + process collectors replace JVM metrics**, plus a broad app-metrics set under `trino_goway_*`. `internal/metrics/`. E2E `metrics_e2e_test.go`.
- [x] **UC-OBS-02** Per-backend health/activation metrics — ✅ `trino_goway_backend_status{backend,status}`, `trino_goway_backend_activation_status{backend}` (mirror `{cluster}_TrinoStatus*` / activation). `internal/metrics` + `internal/monitor` observer.
- [x] **UC-OBS-03** Proxied request counter — ✅ `trino_goway_proxy_requests_total{backend,routing_group,outcome}` (superset of Java `requestCount`).

---

## H. Capabilities only in trino-goway (Go-only additions)

These have **no Java counterpart** — they are net-new in the rewrite:

- [x] **gRPC external routing transport** + standalone reference `routing-service/` (expr + Starlark rules, hot-reload, kill-switch).
- [x] **3-step recovery chain** with concurrent `HEAD`-probe fan-out (Java relies on cache + history only).
- [x] **`goway-migrate-config`** — Java config → Go config translator (`cmd/goway-migrate-config`).
- [x] **`goway-diff-harness`** — differential Java↔Go test harness (record/replay/live/report), normalizer with per-entry justification, live fleet bring-up.
- [x] **Mock routers** for testing — `cmd/mock-external-router` (HTTP) and `cmd/mock-external-router-grpc`.
- [x] **Single static Go binary**, no JVM; Go-idiomatic Prometheus metrics; explicit DI (no global registry).

---

## Persisting wire-shape divergences (quick reference)

For consumers building against Java goldens. Each is a *byte/shape* difference, not a missing
function; the diff harness handles them via justified `IgnoreBodyFields` where needed.

| # | Endpoint / field | Java | trino-goway | UC |
|---|---|---|---|---|
| M1 | `findQueryHistory` body | `{user, externalUrl, size}` | `{userName, backendUrl, pageSize}` | ADM-24 |
| M3 | `getRoutingRules` / `getUIConfiguration` verb | `GET` | `POST` | ADM-20, RTG-02 |
| M4 | `getDistribution.startTime` | `…+00:00` | `…Z` | ADM-19 |
| M8 | `POST /entity` success body | empty | echoes `ProxyBackend` | ADM-10 |
| M9 | `readyz` startup body | `Trino Gateway is still initializing` | `not ready` | MON-05 |
| — | `/entity/{unknown}` | `500` | `200 []` | ADM-11 |
| — | NOOP role grant | all roles | none unless regex matches | AUTH-01 |

> Previously-flagged gaps now **closed** in goway: M2 (`disablePages` added), M5
> (`QueryDetail.externalUrl` populated), M6 (`ProxyBackend.externalUrl` always emitted),
> **M7 (`/api/public/backends/{name}/state` now returns the `ClusterStats` 9-field shape)
> and the `queued`/`running` always-`0` gap (counts live under UI_API/METRICS, `0` under
> the INFO_API default — UC-MON-02 / ADM-14 / ADM-15)**, assets/SPA serving, `/sso` +
> `/oidc/callback`, `/userinfo` permissions. See *Cluster-stats divergences* below for the
> two intentional Phase 12 behavioral divergences.

### Cluster-stats divergences (Phase 12 — UC-MON-02 / M7)

The M7 row and the "queued/running always 0" row above are **closed** in Phase 12: the
public backend-state endpoint now returns the `ClusterStats` 9-field shape and counts are
live under the `UI_API`/`METRICS` collectors (0 under the `INFO_API` default, which matches
the Java default byte-for-byte). Two **intentional** behavioral divergences from Java are
recorded here so they are not later mistaken for accidental gaps:

- **Unobserved-default object populated from persistence (not Java's null).** For a backend
  that has not been collected yet (or under `INFO_API`/`NOOP`), the store returns zero counts,
  but at the M7 admin boundary the handler fills `proxyTo`/`externalUrl`/`routingGroup` from
  the persistence row (reusing `externalURLOrProxyTo`) rather than emitting Java's raw null —
  matching the existing Go convention where `proxyBackendFromPersistence` already populates
  `externalUrl` where Java leaves it null. `userQueuedCount` stays null (omitted from JSON)
  until a `UI_API` collection fills it. Also, `trinoStatus` uses the shared
  `internal/clusterstatus` enum with a new `UNKNOWN` member, so an unprobed backend reports
  `UNKNOWN` (the admin label no longer collapses Unknown→`PENDING`).

- **One UI_API session reused across ticks (vs Java's fresh-login-per-GET).** Java's
  `ClusterStatsHttpMonitor` re-runs `POST /ui/login` before every `/ui/api/stats` and
  `/ui/api/query` GET. trino-goway's `UI_API` collector logs in once, holds the session cookie
  in a per-collector jar, and reuses it across probe ticks (re-logging in only after a `401`).
  This is a deliberate optimization — fewer logins, same observable counts — and is not a
  protocol incompatibility.

---

## Intentional non-goals (the 3 ❌)

| UC | Capability | Why absent in goway |
|---|---|---|
| RTG-01 | Internal MVEL rules engine | Routing policy delegated to an external service (HTTP/gRPC) + reference `routing-service`. |
| RTG-02 | Internal routing-rules CRUD/editor | Follows from RTG-01; `getRoutingRules` returns 204 ("external routing in use"). |
| UI-08 | Routing-rules editor page | Follows from RTG-01/02 (editor hidden under external routing). |

> **No longer a non-goal:** UC-RTG-04 (SQL-aware routing inputs) — the gateway still ships no
> in-process SQL parser, but the reference `routing-service` now parses the forwarded SQL `body`
> (Phase 9) to derive query type/catalogs/schemas/tables for content-aware routing, so the
> capability exists end-to-end (realized in the router, not the gateway).
</content>
