# Trino Gateway — User & Admin Use Cases

> Catalog of the user-facing and admin-facing capabilities of the **Java `trino-gateway`**
> (the reference implementation under `references/trino-gateway/`), enumerated as discrete
> use cases. This is the *reference surface*; the companion document
> [`trino-goway-use-cases-comparison.md`](./trino-goway-use-cases-comparison.md) checks each
> one against the Go rewrite (`trino-goway`).
>
> - **Reference:** `references/trino-gateway` (gateway-ha module), pinned ~`171ce25`.
> - **Derived from:** JAX-RS resource classes, router/security packages, and the study set
>   under `docs/studies/trino-gateway/` (`admin-api-surface`, `external-routing-contract`,
>   `authentication-and-authorization`, `routing-engine`, `mvel-rules-language`,
>   `sql-parsing-for-routing`, `gateway-cookie-internals`, `persistence-and-db-schema`).
> - **Date:** 2026-06-05.

## Actors

| Actor | Description |
|---|---|
| **Trino user** | Any client (human or BI tool / driver) submitting Trino queries through the gateway. |
| **Platform engineer** | Owns the Trino fleet; defines clusters, routing groups, and routing policy. |
| **Gateway operator** | Runs the gateway in production; owns config, health, rollout, on-call. |
| **Gateway admin** | Manages backends/rules/users through the admin API or Web UI. |
| **UI user** | Uses the bundled Web UI (dashboard, clusters, history, routing rules). |

## Use-case ID scheme

`UC-<AREA>-<n>` where AREA ∈ `PXY` (proxy/protocol), `RTG` (routing), `MON` (monitoring &
lifecycle), `ADM` (admin/management API), `AUTH` (auth & authz), `UI` (web UI), `OBS`
(observability).

---

## A. Trino-protocol proxying (Trino user) — `UC-PXY-*`

| ID | Use case | Reference behavior |
|---|---|---|
| **UC-PXY-01** | **Submit a query** | `POST /v1/statement` is accepted and forwarded verbatim to a selected healthy backend; the backend's response (status, headers minus hop-by-hop, body) is returned to the client. The client never needs to know which cluster it hit. |
| **UC-PXY-02** | **Sticky follow-up polls** | The gateway extracts the `queryId` from the `/v1/statement` response and caches `queryId → backend`. Subsequent `GET /v1/query/<id>/...` polls are routed back to the same backend so the client can drain all rows. |
| **UC-PXY-03** | **Cancel a running query (`KILL QUERY`)** | A `POST` body matching `KILL QUERY '<queryId>'` is detected; the query ID is looked up in query history and the kill is routed to the backend that owns the query. |
| **UC-PXY-04** | **Survive gateway restart / cache miss** | A `/v1/query/<id>` request that misses the in-memory cache is recovered via the query-history store (and, in the Go rewrite, a HEAD-probe fan-out), so long-running queries survive routine gateway operations. |
| **UC-PXY-05** | **Sticky OAuth2 routing via cookie** | During a backend's OAuth2 login redirect dance, the gateway pins the session to the issuing cluster with a signed `TG.*` (`GatewayCookie`) so redirects do not bounce between clusters. HMAC-signed, `HttpOnly`, path-scoped, TTL-bounded. |
| **UC-PXY-06** | **Tamper-evident sticky cookie** | A cookie with a bad signature/undecodable payload is rejected loudly (never silently treated as anonymous). |
| **UC-PXY-07** | **Forward client identity headers** | The gateway injects `X-Forwarded-For` (appending, not overwriting), `X-Forwarded-Proto`, `X-Forwarded-Host`, `X-Forwarded-Port` so Trino sees the real client. |
| **UC-PXY-08** | **Strip hop-by-hop headers** | `Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `Te`, `Trailers`, `Transfer-Encoding`, `Upgrade` are removed in both directions. |
| **UC-PXY-09** | **Preserve redirects** | The proxy returns 3xx responses to the client verbatim; it does not follow downstream redirects. |
| **UC-PXY-10** | **Spooled-segment / sticky segment protocol** | `/v1/spooled/*` segment downloads stay pinned to the cluster that issued them (via the gateway cookie / segment routing). |
| **UC-PXY-11** | **Stream large non-statement bodies** | All paths other than the buffered `/v1/statement` response are streamed byte-for-byte without buffering. |
| **UC-PXY-12** | **Inject upstream headers from router** | Headers the routing service returns (`externalHeaders`) are set on the upstream request (REPLACE semantics) so operators can add session tags without changing the gateway. |

---

## B. Routing (Platform engineer) — `UC-RTG-*`

| ID | Use case | Reference behavior |
|---|---|---|
| **UC-RTG-01** | **Internal rules-engine routing (MVEL)** | With `routingRules.rulesEngineEnabled: true` and an internal rules file, the gateway evaluates ordered MVEL rule conditions against request properties and assigns a routing group — *without* an external service. Rules are hot-reloadable from the rules file. |
| **UC-RTG-02** | **Routing rules CRUD via API/UI** | Admins list rules (`GET /webapp/getRoutingRules`) and add/update a rule (`POST /webapp/updateRoutingRules`) with `{name, description, priority, actions, condition}`; the Web UI exposes a rules editor. |
| **UC-RTG-03** | **External routing service** | With `routingRules.rulesType: EXTERNAL`, the gateway delegates the group decision to a separate HTTP service (`POST` of `RoutingGroupExternalBody` → `ExternalRouterResponse`). `getRoutingRules` returns 204 so the UI hides the rules editor. |
| **UC-RTG-04** | **SQL-aware routing inputs** | The gateway parses the SQL (`trino-parser`) to populate `trinoQueryProperties` (catalogs, schemas, tables, query type) so rules / the external router can route on query content. |
| **UC-RTG-05** | **Routing group → backend resolution** | Each backend carries a `routingGroup`; the resolved group name maps to an active backend in that group. |
| **UC-RTG-06** | **Default / single-cluster mode** | With no routing rules and a single cluster, traffic falls to the default group, so the gateway is usable out of the box. |
| **UC-RTG-07** | **Routing fallback on router failure** | If the external router errors or times out, the gateway falls back to the default group rather than rejecting the request. |
| **UC-RTG-08** | **`excludeHeaders` policy** | Configured headers are stripped from both the request forwarded to the router and the `externalHeaders` it returns. |
| **UC-RTG-09** | **Header & request-context forwarding to router** | The router request carries method, URI, remote info, query params, the `trinoQueryProperties` block, the Trino user, and (HTTP transport) the inbound Trino headers. |

---

## C. Backend health, config & lifecycle (Gateway operator) — `UC-MON-*`

| ID | Use case | Reference behavior |
|---|---|---|
| **UC-MON-01** | **Active backend health probes** | A background monitor probes each active backend's `/v1/info` on an interval; a backend is `HEALTHY` only on `200 {"starting": false}`, else `UNHEALTHY`; unprobed/just-added is `PENDING`. |
| **UC-MON-02** | **Live cluster stats** | The cluster monitor collects per-backend queued/running query counts (surfaced in the Web UI and `/api/public/backends/{name}/state`). |
| **UC-MON-03** | **Hot backend list reload** | Backends added/removed via the admin API begin being probed/routed without a gateway restart. |
| **UC-MON-04** | **Liveness probe** | `GET /trino-gateway/livez` always returns `200 ok`. |
| **UC-MON-05** | **Readiness probe** | `GET /trino-gateway/readyz` returns `503` until the first monitor cycle completes, then `200`. |
| **UC-MON-06** | **YAML configuration with defaults** | A single YAML config with documented defaults boots the gateway; validation rejects bad config (e.g. duplicate ports). |
| **UC-MON-07** | **Coordinated startup** | Listener(s) bind before background workers start; readiness flips only after the first probe cycle. |
| **UC-MON-08** | **Graceful shutdown** | SIGTERM/SIGINT drains in-flight requests with a deadline before exit. |
| **UC-MON-09** | **Durable persistence** | Backends and query history persist to a relational DB (Postgres/MySQL via JDBI); the gateway is not stateless. |
| **UC-MON-10** | **Connection isolation** | Proxy traffic, health probes, and routing-service calls use isolated HTTP clients so a slow router cannot starve query proxying. |

---

## D. Admin / management API (Gateway operator / admin) — `UC-ADM-*`

> Three overlapping backend-management surfaces exist (`/gateway/*` for scripts, `/entity/*`
> for legacy automation, `/webapp/*` for the UI), plus public read endpoints. All share the
> proxy's port in Java (single port for traffic + management).

| ID | Use case | Reference behavior | Role |
|---|---|---|---|
| **UC-ADM-01** | API ping | `GET /gateway` → `"ok"`. | API |
| **UC-ADM-02** | List all backends | `GET /gateway/backend/all` → `[ProxyBackend]`. | API |
| **UC-ADM-03** | List active backends | `GET /gateway/backend/active`. | API |
| **UC-ADM-04** | Activate backend | `POST /gateway/backend/activate/{name}`. | API |
| **UC-ADM-05** | Deactivate backend | `POST /gateway/backend/deactivate/{name}`. | API |
| **UC-ADM-06** | Add backend | `POST /gateway/backend/modify/add` (JSON `ProxyBackend`). | API |
| **UC-ADM-07** | Update backend | `POST /gateway/backend/modify/update`. | API |
| **UC-ADM-08** | Delete backend | `POST /gateway/backend/modify/delete` (**raw string body** = name). | API |
| **UC-ADM-09** | List entity types | `GET /entity` → `["GATEWAY_BACKEND"]`. | ADMIN |
| **UC-ADM-10** | Upsert entity | `POST /entity?entityType=GATEWAY_BACKEND`; updates in-memory monitor state immediately (active→`PENDING`, inactive→`UNHEALTHY`); **empty body** on success; unknown type → `500`. | ADMIN |
| **UC-ADM-11** | List entities | `GET /entity/{entityType}`; unknown type → `500`. | ADMIN |
| **UC-ADM-12** | Public: list backends | `GET /api/public/backends` (no auth). | none |
| **UC-ADM-13** | Public: get backend | `GET /api/public/backends/{name}`; `404` on miss. | none |
| **UC-ADM-14** | Public: backend state | `GET /api/public/backends/{name}/state` → `ClusterStats` (`trinoStatus`, `queuedQueryCount`, `runningQueryCount`). | none |
| **UC-ADM-15** | Webapp: all backends + stats | `POST /webapp/getAllBackends` → `Result<[BackendResponse]>` with live `queued`, `running`, `status`. | USER |
| **UC-ADM-16** | Webapp: save backend | `POST /webapp/saveBackend`. | ADMIN |
| **UC-ADM-17** | Webapp: update backend | `POST /webapp/updateBackend`. | ADMIN |
| **UC-ADM-18** | Webapp: delete backend | `POST /webapp/deleteBackend` (full object, uses only `name`). | ADMIN |
| **UC-ADM-19** | Webapp: distribution dashboard | `POST /webapp/getDistribution` → counts (`total/online/offline/healthy/unhealthy`), `totalQueryCount`, avg/min & /sec since start, `distributionChart`, `lineChart`, ISO-8601 `startTime` (`+00:00` offset format). | USER |
| **UC-ADM-20** | Webapp: UI configuration | `GET /webapp/getUIConfiguration` → `{disablePages}`. | USER |
| **UC-ADM-21** | Query history (legacy) | `GET /trino-gateway/api/queryHistory` → `[QueryDetail]`; non-ADMIN scoped to own user. | USER |
| **UC-ADM-22** | Active backends (legacy) | `GET /trino-gateway/api/activeBackends`. | USER |
| **UC-ADM-23** | Query distribution (legacy) | `GET /trino-gateway/api/queryHistoryDistribution` → `{backendName→count}`; non-ADMIN scoped. | USER |
| **UC-ADM-24** | Webapp: paginated history search | `POST /webapp/findQueryHistory` body `{user, externalUrl, queryId, source, page, size}` → `TableData<QueryDetail>`; non-ADMIN `user` forced server-side. | USER |
| **UC-ADM-25** | `QueryDetail.externalUrl` populated | History records carry the routed backend's `externalUrl` so QueryId deep-links resolve. | USER |
| **UC-ADM-26** | `ProxyBackend` wire shape | `{name, proxyTo, externalUrl, active, routingGroup}`, `externalUrl` always present (falls back to `proxyTo`). | — |

---

## E. Authentication & authorization (Gateway admin / UI user) — `UC-AUTH-*`

| ID | Use case | Reference behavior |
|---|---|---|
| **UC-AUTH-01** | **No-auth (dev) mode** | With no `authentication:` config, `NoopFilter` grants every caller `ADMIN`+`USER`+`API` unconditionally. |
| **UC-AUTH-02** | **OIDC / OAuth2 bearer** | JWT read from `token` cookie or `Authorization: Bearer`, validated against the OIDC issuer's JWKS. |
| **UC-AUTH-03** | **Form / LDAP login** | `POST /login` validates credentials against preset-users or LDAP and **issues a self-signed JWT** the client stores; HTTP Basic is also accepted on protected endpoints. |
| **UC-AUTH-04** | **Web-UI OAuth2 initiation** | `POST /sso` starts the authorization-code flow, sets an `oidc-state` cookie (state + nonce), redirects to the IdP. |
| **UC-AUTH-05** | **OIDC callback** | `GET /oidc/callback` exchanges the code, validates nonce/state, sets the `token` session cookie, redirects to the UI. |
| **UC-AUTH-06** | **Role mapping via regex** | `ADMIN`/`USER`/`API` roles map to regex patterns matched against the principal's `memberOf` (LDAP attr or JWT claim). |
| **UC-AUTH-07** | **`@RolesAllowed` enforcement** | Only annotated endpoints are protected; cross-role denial (USER ≠ ADMIN) is enforced; proxy paths bypass gateway auth. |
| **UC-AUTH-08** | **Page permissions** | `pagePermissions` config maps roles → page identifiers; surfaced via `/userinfo` `permissions`. |
| **UC-AUTH-09** | **Userinfo** | `POST /userinfo` → `{userId, userName, roles, permissions}`. |
| **UC-AUTH-10** | **Login type discovery** | `POST /loginType` → `"oauth" \| "form" \| "none"` so the UI shows the right login. |
| **UC-AUTH-11** | **Logout** | `POST /logout` clears the session / returns success; cookie delete-paths honored. |
| **UC-AUTH-12** | **Session cookie** | `token` cookie (1-day, `Secure`, path `/`). |

---

## F. Web UI (UI user) — `UC-UI-*`

| ID | Use case | Reference behavior |
|---|---|---|
| **UC-UI-01** | **Serve SPA shell** | `GET /trino-gateway` serves `index.html`. |
| **UC-UI-02** | **Serve static assets** | `GET /trino-gateway/assets/{path}` serves bundled JS/CSS/fonts (path-traversal blocked). |
| **UC-UI-03** | **Serve logo** | `GET /trino-gateway/logo.svg`. |
| **UC-UI-04** | **Root redirect** | `GET /` → `303` to `/trino-gateway`. |
| **UC-UI-05** | **Dashboard page** | Distribution charts, query counts, backend health from `getDistribution`/`getAllBackends`. |
| **UC-UI-06** | **Clusters page** | Add/activate/deactivate/delete backends via the UI. |
| **UC-UI-07** | **Query history page** | Search/paginate history; deep-link to a query's cluster via `externalUrl`. |
| **UC-UI-08** | **Routing rules editor** | View/edit internal routing rules (hidden when external routing is in use). |
| **UC-UI-09** | **Login pages** | Form login or SSO-initiation per `loginType`; role-driven sidebar via `disablePages`/permissions. |

---

## G. Observability (Gateway operator) — `UC-OBS-*`

| ID | Use case | Reference behavior |
|---|---|---|
| **UC-OBS-01** | **Metrics endpoint** | `/metrics` exposes OpenMetrics (airlift `JmxOpenMetricsModule`): JVM/platform metrics **plus** app metrics — `ProxyHandlerStats.requestCount`, per-backend activation gauge (`1/0/-1`), per-backend `TrinoStatus` health gauges. |
| **UC-OBS-02** | **Per-backend health/activation metrics** | `{cluster}_TrinoStatusHealthy/Unhealthy/Pending`, activation status. |
| **UC-OBS-03** | **Proxied request counter** | `requestCount` counter for proxied requests. |

---

## Notes on scope

- **Single port (Java):** Java serves proxy + management on one port; path filtering (`PathFilter`,
  `extraWhitelistPaths`) distinguishes forwarded Trino traffic from admin requests.
- **Three management surfaces:** `/gateway/*`, `/entity/*`, and `/webapp/*` all mutate backends;
  operators may use any. All must be preserved by a compatible rewrite.
- **Envelope split:** `/webapp/*` and `/login*` use the `Result<T>` `{code,msg,data}` envelope;
  `/gateway/*`, `/entity/*`, `/api/public/*`, and health probes return bare JSON/text.
</content>
</invoke>
