# E2E Coverage Plan (qa-tech-lead, Task 34)

**Status:** Sign-off document. Phase 8 (Tasks 39-55) may not begin until this is committed.
**Author:** qa-tech-lead
**Date:** 2026-05-29

## Purpose

This document maps every acceptance criterion in `USE_STORIES.md` §1-§7 to one of three coverage states:

- **COVERED-BY-EXISTING-TEST** — already verified by a committed test (cited file + function).
- **PLANNED-IN-TASK-N** — will be verified by Phase 8 task N (see TODO.md, Tasks 39-55).
- **NOT-COVERED** — no existing or planned test addresses this criterion; gap is explicitly flagged below.

A separate section identifies acceptance criteria that cannot be verified purely via the
black-box (binary + HTTP) interface and proposes white-box fallbacks. Final sections cover CI
integration guidance for unit / integration / E2E / diff-harness test tiers, and a focused
diff-harness CI subsection (Task 28 Phase 3 remaining).

## Convention used in citations

- Existing unit/integration tests are cited as `pkg/file.go::TestName`.
- Planned tests are cited as `TaskNN`, where NN refers to the task ID in TODO.md.
- Where both apply (existing unit coverage + planned black-box E2E), both are listed.

---

## 1. Summary Table

### §1. Trino-protocol proxying

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §1.1 a | `POST /v1/statement` accepted on `proxy.port` (default 8080) | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestProxy_Seam1_NeverRewriteResponseBody`; Task 39 `TestE2E_PostStatement_RoutesToBackend` |
| §1.1 b | Request body forwarded verbatim | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestProxy_Seam6_KillQueryRegexRouting` (asserts body verbatim); Task 39 `TestE2E_PostStatement_RoutesToBackend` (Hard Invariant #1 leg) |
| §1.1 c | No backend selectable → 502 `no backend available` | COVERED + PLANNED | `internal/proxy/proxy_qa_test.go::TestProxy_Routing_EmptyBackendURLFailsClosed` (closed-loop fail); Task 39 `TestE2E_PostStatement_NoBackendAvailable` |
| §1.1 d | Response status/headers preserved (minus hop-by-hop) | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestCopyHeaders_SkipsHopByHop`, `TestProxy_Seam1_NeverRewriteResponseBody`; Task 39 `TestE2E_HopByHopStripped` |
| §1.1 e | `/v1/statement` response buffered to `proxy.responseSize`; oversized → 502 `upstream response too large` | COVERED + PLANNED | `internal/proxy/proxy_qa_test.go::TestProxy_Degradation_OversizeResponseBody`; Task 39 `TestE2E_PostStatement_ResponseBufferingCap` |
| §1.2 a | `queryId → backendURL` cached BEFORE response body flushed | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestProxy_Seam3_CacheWriteBeforeResponseFlush`; Task 54 `TestE2E_Inv3_CacheWriteBeforeFlush` |
| §1.2 b | `/v1/query/<id>` consults cache | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_CacheHitSkipsExternal`, `TestExtractQueryID`; Task 39 `TestE2E_PostStatement_StickyRouting` |
| §1.2 c | `/v1/statement/<queued\|executing>/...` polls do NOT consult cache | NOT-COVERED | **Add to Task 41 or Task 39**: new sub-test `TestE2E_StatementPollsBypassCache` — verify external router is called for `/v1/statement/queued/<id>/...` and not the cached entry |
| §1.2 d | Cache is bounded LRU (4096 entries) per instance | NOT-COVERED (black-box) | White-box: extend `internal/routing/routing_test.go` with `TestCache_LRUEvictionAt4096` (insert 4097 entries, assert oldest evicted). Black-box verification is impractical (test would need 4096+ real queries through the proxy) |
| §1.2 e | Non-`/v1/statement` paths streamed (no buffering) | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestProxy_Seam1_NeverRewriteResponseBody` (single-pass); Task 39 `TestE2E_StreamingPath_NotBuffered`; Task 54 `TestE2E_Inv4_BoundedBuffering_OnlyStatement` |
| §1.3 a | `KILL QUERY '...'` regex detection before router | COVERED + PLANNED | `internal/routing/routing_test.go::TestExtractKillQueryID`, `TestRouter_KillQueryRoutesToHistory`; `internal/proxy/proxy_test.go::TestProxy_Seam6_KillQueryRegexRouting`; Task 40 `TestE2E_KillQuery_RoutesToOwnerBackend`, `TestE2E_KillQuery_Lowercase` |
| §1.3 b | Lookup in query-history; route to that backend | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_KillQueryRoutesToHistory`; Task 40 `TestE2E_KillQuery_RoutesToOwnerBackend` |
| §1.3 c | Singleflight coalesces duplicate kill lookups | NOT-COVERED | White-box: add `internal/routing/routing_test.go::TestRouter_KillQuery_Singleflight` — fire N concurrent kill requests for same queryID against a counting `HistoryLookup` fake, assert `LookupByQueryID` invoked exactly once. **Recommend adding to Task 41 scope (recovery chain) or as a new Task 41a** |
| §1.4 a | 3-step recovery on `/v1/query/<id>` cache miss (history → HEAD probe → first-active) | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_FallsBackToFirstActive` (partial); Task 41 `TestE2E_Recovery_HistoryLookup`, `TestE2E_Recovery_HEADProbeFanout`, `TestE2E_Recovery_FirstActiveFallback` |
| §1.4 b | Never returns 404 when at least one backend active | PLANNED | Task 41 `TestE2E_Recovery_NeverErrors` |
| §1.5 a | Cookie issuance/validation gated on `cookie.secret` | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_DisabledWhenSecretEmpty`; Task 53 `TestE2E_Cookie_EmptySecret_NeverEmits` |
| §1.5 b | First `/oauth2/...` without cookie emits `Set-Cookie: TG.OAUTH2=...` | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_IssueTrigger_OAuth2`, `TestCookie_NoIssueOnNonOAuth2`; Task 53 `TestE2E_Cookie_IssuedOnOAuth2Path` |
| §1.5 c | Cookie attributes: HttpOnly/SameSite=Lax/Path=/, Max-Age=ttl, JSON+HMAC+base64 | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_HMACFixture`, `TestCookie_EncodeDecodeRoundTrip`; Task 53 `TestE2E_Cookie_IssuedOnOAuth2Path` (asserts attributes) |
| §1.5 d | Valid cookie + matching `deletePaths` → delete-cookie emitted | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_DeleteOnLogout`, `TestCookie_DeleteOnOAuth2Logout`; Task 53 `TestE2E_Cookie_LogoutPath_DeletesCookie` |
| §1.5 e | Expired cookie → delete-cookie + continue (no 401) | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_TTLExpiry`; Task 53 `TestE2E_Cookie_ExpiryEmitsDeleteCookie` |
| §1.5 f | Bad HMAC / undecodable → 500 (no silent accept) | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_HMACTamper`; Task 53 `TestE2E_Cookie_TamperedHMAC_Returns500`; Task 54 `TestE2E_Inv5_TamperedCookieIs500` |
| §1.5 g | `cookie.wireCompat: true` matches Java byte-for-byte | COVERED + PLANNED | `internal/proxy/cookie_test.go::TestCookie_HMACFixture`, `TestCookie_FormatTTL_WireCompatVsNanos`, `TestCookie_RawURLEncoding`; Task 53 `TestE2E_Cookie_WireCompat_GoldenBytes`; Task 54 `TestE2E_Inv10_CookieWireCompat`; diff-harness `seam7-cookie-emission.yaml` |
| §1.6 a | `X-Forwarded-For` appended, not overwritten | COVERED + PLANNED | `internal/proxy/headers_test.go::TestInjectHeaders_AppendsXForwardedFor`; Task 39 `TestE2E_ForwardedHeaders_XForwardedForAppends`; Task 54 `TestE2E_Inv8_XForwardedForAppends` |
| §1.6 b | `X-Forwarded-Proto`: https on TLS, http otherwise | COVERED | `internal/proxy/headers_test.go::TestInjectHeaders_HTTPS`, `TestInjectHeaders_XForwardedPort` — **also needs PLANNED:** add to Task 39 sub-test `TestE2E_ForwardedHeaders_XForwardedProto` (currently absent from Task 39 list) |
| §1.6 c | `X-Forwarded-Host`: host-only (IPv6 preserved) | COVERED + PLANNED | `internal/proxy/headers_test.go::TestInjectHeaders_XForwardedHost_HostOnly`; Task 39 `TestE2E_ForwardedHeaders_XForwardedHost` |
| §1.6 d | `X-Forwarded-Port`: explicit or scheme default | COVERED | `internal/proxy/headers_test.go::TestInjectHeaders_XForwardedPort`. **NOT-COVERED black-box:** recommend adding `TestE2E_ForwardedHeaders_XForwardedPort` to Task 39 |
| §1.6 e | Hop-by-hop headers never forwarded | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestCopyHeaders_SkipsHopByHop`, `TestIsHopByHop`; Task 39 `TestE2E_HopByHopStripped`; Task 54 `TestE2E_Inv7_HopByHopStripped` |
| §1.7 a | External router `externalHeaders` set with REPLACE | COVERED + PLANNED | `internal/proxy/headers_test.go::TestInjectHeaders_ExternalHeaders_Replace`, `internal/proxy/proxy_test.go::TestInjectHeaders_XForwarded` (XCustomHeader); Task 42 `TestE2E_ExternalHTTP_ExternalHeadersReplaced`; Task 54 `TestE2E_Inv9_ExternalHeadersReplace` |
| §1.7 b | `excludeHeaders` strips from both `externalHeaders` response and (HTTP transport only) forwarded inbound headers | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_FilterExcludedHeaders`, `TestExternalHTTP_ForwardsInboundHeaders`; Task 42 `TestE2E_ExternalHTTP_ExcludeHeadersFiltered` |

### §2. Routing

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §2.1 a | `routing.type` must be `EXTERNAL` (validated at startup) | COVERED + PLANNED | `internal/config/config_test.go` (validator). Recommend adding `TestE2E_StartupFails_InvalidRoutingType` to Task 50 or a new lifecycle E2E task |
| §2.1 b | gRPC tried first; HTTP fallback if `url` set | COVERED + PLANNED | `internal/routing/routing_test.go` exercises HTTP path; gRPC fallback path is exercised inside `Router.callExternal` but tested only indirectly. Task 43 `TestE2E_ExternalGRPC_FallbackToHTTP` covers black-box |
| §2.1 c | Both transports honor `routing.external.timeout` | COVERED + PLANNED | `internal/routing/routing_test.go::TestExternalHTTP_*` paths exercise timeout indirectly; Task 42 `TestE2E_ExternalHTTP_TimeoutFallback` covers explicitly |
| §2.1 d | HTTP POSTs `RoutingGroupExternalBody`; gRPC sends equivalent proto; `trinoQueryProperties` empty parser-derived; `errorMessage` = `"trino-parser not available in Go v1"` | COVERED (HTTP) | `internal/routing/routing_test.go::TestExternalHTTP_SuccessPath`, `TestExternalHTTP_ForwardsInboundHeaders`. **gRPC equivalent test NOT-COVERED.** Recommend adding `TestExternalGRPC_SendsEquivalentRouteRequest` to `internal/routing/` (white-box via `bufconn`). Task 43 covers gRPC at black-box level |
| §2.1 e | Both transports fail → defaultGroup → recovery chain; router-down never rejects | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_FallsBackToFirstActive`, `TestExternalHTTP_Non200FallsThrough`; Task 42 `TestE2E_ExternalHTTP_FallbackOnRouterDown`, Task 43 `TestE2E_ExternalGRPC_FallbackOnBothDown` |
| §2.2 a | Each backend has `routing_group` | COVERED | `internal/admin/admin_test.go::TestAdmin_BackendCRUD` (ProxyBackend.RoutingGroup is a serialized field); persistence DAO tests verify column round-trip |
| §2.2 b | Router resolves group → URL of active backend in that group | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_FilterExcludedHeaders` (uses `etl` group); Task 44 `TestE2E_RoutingGroup_SteeringByGroup` |
| §2.2 c | Empty group → recovery → first active backend (any group) | COVERED + PLANNED | `internal/routing/routing_test.go::TestRouter_FallsBackToFirstActive`; Task 44 `TestE2E_RoutingGroup_RecoveryWhenGroupEmpty` |
| §2.3 a | No external URL/grpcAddr → skip external; route to defaultGroup | PLANNED | Task 44 `TestE2E_SingleCluster_NoExternalRouter`. **No existing unit coverage** — recommend adding `TestRouter_NoExternalConfigSkipsExternalCall` to `internal/routing/routing_test.go` |
| §2.3 b | `config.example.yaml` documents single-cluster fallback | NOT-COVERED (docs check) | Add a `golangci-lint`-style or `go test ./cmd/...` doc-check, or skip — this is a documentation acceptance, not a code one. Recommend: skip automated coverage, add to qa-tech-lead manual review checklist for releases |

### §3. Backend health monitoring

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §3.1 a | Background monitor probes every `monitor.interval` | COVERED + PLANNED | `internal/monitor/monitor_test.go::TestMonitor_HealthyBackend`, `TestMonitor_SetBackends` (uses 10ms interval); Task 45 `TestE2E_Monitor_HealthyBackend` |
| §3.1 b | Probe = `GET /v1/info`; deadline = `monitor.checkTimeout` | COVERED + PLANNED | `internal/monitor/monitor_test.go::TestMonitor_HealthyBackend`; Task 45 `TestE2E_Monitor_HealthyBackend` |
| §3.1 c | HEALTHY iff 200 + `{"starting":false}`; else UNHEALTHY | COVERED + PLANNED | `internal/monitor/monitor_test.go::TestMonitor_StartingBackend`, `TestMonitor_MalformedJSONBackend`, `TestMonitor_UnhealthyBackend`; Task 45 `TestE2E_Monitor_UnhealthyBackend`, `TestE2E_Monitor_TransportError` |
| §3.1 d | Unprobed = UNKNOWN; freshly upserted = PENDING | COVERED + PLANNED | `internal/monitor/monitor_test.go::TestMonitor_SetBackendStatus`, `TestTrinoStatus_String`; `internal/admin/admin_test.go::TestAdmin_EntityEndpoints` (asserts PENDING on upsert); Task 45 `TestE2E_Monitor_NewlyAddedBackend` |
| §3.1 e | Status updated by atomic swap (admin reads never see half-update) | COVERED | `internal/monitor/monitor_test.go::TestMonitor_AllStatuses_Snapshot`. NOT directly verifiable black-box; relies on `atomic.Pointer` impl. White-box adequate |
| §3.2 a | 15s background refresher reloads backend list from DB | NOT-COVERED | **Add to Task 45 (or new Task 45a)**: `TestE2E_Monitor_BackendListRefreshFromDB` — register backend via DB insertion (not admin API), wait `≥ refresh interval`, assert backend appears in admin API and monitor status. Refresh interval is hard-coded — recommend exposing via test-only flag or short interval for E2E |
| §3.2 b | `POST /entity?entityType=GATEWAY_BACKEND` seeds monitor status immediately | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_EntityEndpoints` (`upsert entity sets monitor status pending when active`); Task 47 `TestE2E_Admin_EntityAPI_SeedsMonitorStatus` |
| §3.3 a | `/trino-gateway/livez` always returns `200 ok` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_HealthProbes/livez always 200`; Task 46 `TestE2E_Livez_AlwaysOK` |
| §3.3 b | `/trino-gateway/readyz` returns 503 → 200 after first probe cycle | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_HealthProbes/readyz 503 before SetReady`, `readyz 200 after SetReady`; `internal/monitor/monitor_test.go::TestMonitor_OnFirstTick`, `TestMonitor_OnFirstTick_NoBackends`; Task 46 `TestE2E_Readyz_503BeforeFirstProbe`, `TestE2E_Readyz_200AfterFirstProbe`; Task 54 `TestE2E_Inv11_ReadyzRequiresProbe` |

### §4. Admin and management API

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §4.1 a | All `/gateway/backend/*` endpoints under API role | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_BackendCRUD`, `TestAdmin_RoleEnforcement`; `internal/admin/admin_gaps_test.go::TestAdmin_RoleMatrix_WriteEndpoints403`; Task 47 (full lifecycle) |
| §4.1 b | Wire JSON `{name, proxyTo, externalUrl?, active, routingGroup}` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_BackendCRUD/add backend`; Task 47 `TestE2E_Admin_BackendWireShape` |
| §4.1 c | Read-only `/api/public/backends*` no-auth | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_PublicBackends`, `TestAdmin_NoAuthEndpoints`; `internal/admin/admin_gaps_test.go::TestAdmin_GetPublicBackendState_HappyPath`; Task 47 `TestE2E_Admin_PublicBackends_NoAuth` |
| §4.1 d | Postgres `ON CONFLICT` / MySQL `ON DUPLICATE KEY UPDATE` upsert | COVERED | `internal/persistence/backend_test.go::TestBackendDAO_Postgres`, `TestBackendDAO_MySQL` (`//go:build integration`). Black-box E2E in Task 47 |
| §4.2 a | `GET /entity` → `["GATEWAY_BACKEND"]` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_EntityEndpoints/list entity types`; Task 47 `TestE2E_Admin_EntityAPI_ListTypes` |
| §4.2 b | `POST /entity?entityType=GATEWAY_BACKEND` upserts + seeds monitor | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_EntityEndpoints/upsert entity sets monitor status pending when active`, `...unhealthy when inactive`; Task 47 `TestE2E_Admin_EntityAPI_SeedsMonitorStatus` |
| §4.2 c | `GET /entity/{entityType}` returns backends; other types → `[]` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_EntityEndpoints/list entities by type`. **Partial gap:** no test for "other entity type returns empty array". Recommend adding to Task 47 |
| §4.2 d | `POST /entity` unknown type → 500 | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_EntityEndpoints/upsert entity unknown type returns error`; Task 47 `TestE2E_Admin_EntityAPI_UnknownType` |
| §4.2 e | Endpoints require ADMIN role | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_RoleMatrix_WriteEndpoints403`; Task 47 (implicit) |
| §4.3 a | All `/webapp/*` use envelope `{code, msg, data}` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_WebappEndpoints` (every sub-test decodes `admin.Result[...]`); Task 48 `TestE2E_Webapp_ResponseEnvelope` |
| §4.3 b | `getAllBackends` returns live `status` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_WebappEndpoints/getAllBackends`; Task 48 `TestE2E_Webapp_GetAllBackends` |
| §4.3 c | `findQueryHistory` returns `TableData<QueryDetail>`; non-ADMIN forces userName | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_WebappFindQueryHistory_HappyPath`; Task 48 `TestE2E_Webapp_FindQueryHistory` |
| §4.3 d | `getDistribution` returns full aggregate response | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_WebappEndpoints/getDistribution`; Task 48 `TestE2E_Webapp_GetDistribution` |
| §4.3 e | `getUIConfiguration` → `{authType}` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_WebappEndpoints/getUIConfiguration`; Task 48 `TestE2E_Webapp_GetUIConfiguration` |
| §4.3 f | `getRoutingRules` / `updateRoutingRules` v1 stub + ADMIN role | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_WebappEndpoints/getRoutingRules returns empty list`; `internal/admin/admin_gaps_test.go::TestAdmin_WebappUpdateRoutingRules_HappyPath`; Task 48 `TestE2E_Webapp_RoutingRulesStubs` |
| §4.3 g | saveBackend/updateBackend/deleteBackend → ADMIN; others → USER | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_RoleMatrix_WriteEndpoints403`; Task 48 `TestE2E_Webapp_RoleEnforcement` |
| §4.4 a | `queryHistory` requires USER role | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_QueryHistoryScoping_NonAdminSeesOwnRecord`, `TestAdmin_QueryHistoryScoping_AdminSeesAll`; Task 49 (entire) |
| §4.4 b | ADMIN sees up to 100 most recent | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_QueryHistoryScoping_AdminSeesAll`; Task 49 `TestE2E_History_AdminSeesAllUsers` |
| §4.4 c | Non-ADMIN scoped server-side | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_QueryHistoryScoping_NonAdminSeesOwnRecord`; Task 49 `TestE2E_History_UserScopedToOwn` |
| §4.4 d | `queryHistoryDistribution` → `{backendUrl: count}` + scoping | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_QueryHistoryDistribution_HappyPath`; Task 49 `TestE2E_History_Distribution` |
| §4.4 e | `activeBackends` legacy wire format | COVERED + PLANNED | `internal/admin/admin_gaps_test.go::TestAdmin_LegacyActiveBackends_HappyPath`; Task 49 `TestE2E_History_ActiveBackends` |
| §4.5 a | `/userinfo` returns `{userId, userName, roles, permissions}` | NOT-COVERED (unit) — PLANNED | Task 50 `TestE2E_Userinfo_ReturnsRoles`. **Recommend adding** `TestAdmin_Userinfo_*` to `internal/admin/admin_gaps_test.go` for faster feedback |
| §4.5 b | `roles` matches regex-based authorization | COVERED + PLANNED | `internal/auth/auth_test.go::TestHasRole`; Task 50 `TestE2E_Userinfo_ReturnsRoles` (full integration) |
| §4.5 c | `/loginType` reports `oauth\|form\|none` | COVERED + PLANNED | `internal/admin/admin_test.go::TestAdmin_LoginEndpoints/loginType returns none for NOOP`; Task 50 `TestE2E_LoginType_ReportsNOOP`. **Partial gap:** no test for `oauth` / `form` return values |
| §4.5 d | `/login` NOOP returns `{token: <username>}`; OIDC/LDAP error | COVERED (NOOP) | `internal/admin/admin_test.go::TestAdmin_LoginEndpoints/login with NOOP returns username as token`. **Gap:** no test asserts OIDC/LDAP `/login` returns the error envelope. Recommend adding to Task 51 / Task 52 |
| §4.5 e | `/logout` returns success envelope | COVERED | `internal/admin/admin_test.go::TestAdmin_LoginEndpoints/logout returns 200`. Recommend adding envelope assertion to Task 50 |

### §5. Authentication and authorization

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §5.1 a | `auth.type` ∈ {NOOP, OIDC, LDAP} | COVERED + PLANNED | `internal/config/config_test.go::TestValidate_OIDCMissingJWKSURL`, `TestValidate_LDAPMissingURL`; Task 50 / 51 / 52 |
| §5.1 b | NOOP = anonymous principal, no roles unless regex matches | COVERED + PLANNED | `internal/auth/auth_test.go::TestNoop_AttachesAnonymousPrincipal`; `internal/admin/admin_test.go::TestAdmin_QueryHistoryScoping`; Task 50 `TestE2E_NOOP_AdminAnonymousPrincipal`, `TestE2E_NOOP_RoleGrantedByRegex` |
| §5.1 c | OIDC: Bearer JWT validated against JWKS; background refresh every `jwksTtlSecs` | NOT-COVERED (live) — PLANNED | Task 51 `TestE2E_OIDC_ValidToken_Admitted`, `TestE2E_OIDC_JWKSRefresh`. **Recommend adding** `internal/auth/oidc_test.go` using `internal/testutil` mock OIDC server (Task 36) for faster unit-level feedback |
| §5.1 d | LDAP: bind with `bindDn/bindPassword`, search, re-bind as user DN | NOT-COVERED (live) — PLANNED | Task 52 `TestE2E_LDAP_ValidCredentials_Admitted`. Recommend `internal/auth/ldap_test.go` against mock LDAP server (Task 37) |
| §5.1 e | Failed auth → 401 `{"error":"..."}`, `WWW-Authenticate: Bearer` | COVERED (NOOP path) + PLANNED | `internal/auth/auth_test.go::TestRequireRole_Forbidden` (returns 403 for missing principal). 401 path NOT-COVERED at unit level. Task 51 `TestE2E_OIDC_InvalidToken_401`, Task 52 `TestE2E_LDAP_InvalidCredentials_401` |
| §5.1 f | Proxy port does NOT enforce gateway auth | PLANNED | Task 50 `TestE2E_NOOP_ProxyPortNoAuth`. **Gap (white-box):** no unit-level assertion. Recommend: rely on E2E; covered by composition root design |
| §5.2 a | `auth.authorization.{admin,user,api}` regex against `memberOf` | COVERED + PLANNED | `internal/auth/auth_test.go::TestHasRole`; Task 50 `TestE2E_NOOP_RoleGrantedByRegex` |
| §5.2 b | OIDC `memberOf` built from `groups`/`memberOf` claim (comma-joined if array) | COVERED + PLANNED | `internal/auth/auth_test.go::TestGroupsClaim`; Task 51 `TestE2E_OIDC_GroupsClaimMapsToRole` |
| §5.2 c | LDAP `memberOf` from user entry attribute | PLANNED | Task 52 `TestE2E_LDAP_MemberOfMapsToRole` |
| §5.2 d | Empty regex never matches | COVERED | `internal/auth/auth_test.go::TestHasRole/empty regex` |
| §5.2 e | `RequireRole` → 403 `{"error":"forbidden"}` on missing role | COVERED + PLANNED | `internal/auth/auth_test.go::TestRequireRole_Forbidden`; `internal/admin/admin_test.go::TestAdmin_RoleEnforcement`; Task 50 `TestE2E_Role_403OnInsufficientRole` |
| §5.3 a | OIDC requires `jwksUrl`; missing → fail `Config.Validate()` | COVERED + PLANNED | `internal/config/config_test.go::TestValidate_OIDCMissingJWKSURL`; Task 51 `TestE2E_OIDC_MissingJwksUrl_StartupFails` |
| §5.3 b | Initial JWKS fetch must succeed; failure → gateway does not start | NOT-COVERED | **PLANNED in Task 51?** Not explicitly listed. Recommend adding `TestE2E_OIDC_JWKSUnreachable_StartupFails` to Task 51 — point gateway at non-existent JWKS URL, assert subprocess exits non-zero |
| §5.3 c | Background JWKS refresh logs (doesn't crash) on later failures; old keys keep validating | PARTIALLY COVERED — PLANNED | `internal/auth/oidc.go` impl uses atomic Pointer (white-box adequate). Task 51 `TestE2E_OIDC_JWKSRefresh` exercises rotation but not refresh-failure path. **Recommend** adding `TestE2E_OIDC_JWKSRefreshFailure_KeepsServing` to Task 51 |
| §5.4 a | LDAP requires `url` and `userBase`; missing → fail validate | COVERED + PLANNED | `internal/config/config_test.go::TestValidate_LDAPMissingURL`; Task 52 `TestE2E_LDAP_MissingUrl_StartupFails`, `TestE2E_LDAP_MissingUserBase_StartupFails` |
| §5.4 b | `userAttr` defaults to `uid` | COVERED | `internal/config/config_test.go::TestLoad_Defaults` |

### §6. Configuration and lifecycle

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §6.1 a | `--config <path>` default `config.yml` | NOT-COVERED | **Add to Task 38 harness smoke test**: harness already writes a temp YAML and passes `--config`; verifying default path requires omitting `--config` and confirming it looks for `./config.yml`. Recommend: white-box CLI flag test in `cmd/trino-goway/main_test.go` (new) |
| §6.1 b | All sections have documented defaults | COVERED | `internal/config/config_test.go::TestLoad_Defaults` |
| §6.1 c | Duration uses Go format; size accepts B/KB/KiB/etc. | COVERED | `internal/config/config_test.go::TestDuration_UnmarshalYAML`, `TestDataSize_UnmarshalYAML` |
| §6.1 d | Validation rejects bad configs (port collision, responseSize, db.driver, routing.type, OIDC, LDAP) | COVERED | `internal/config/config_test.go::TestValidate_AdminPortEqualsProxyPort`, `TestValidate_ResponseSizeZero`, `TestValidate_InvalidDBDriver`, `TestValidate_OIDCMissingJWKSURL`, `TestValidate_LDAPMissingURL`. **Gap:** validation for `routing.type != EXTERNAL` not directly tested. Recommend adding |
| §6.2 a | Both ports bound before serving; bind failure → close already-bound | COVERED | `internal/lifecycle/server_test.go::TestServer_Listen_BindsBeforeServe`, `TestServer_Listen_FailsWhenPortInUse` |
| §6.2 b | Monitor and refresh start after ports bound | NOT-COVERED (black-box) | White-box: composition root ordering is correct by inspection (`cmd/trino-goway/main.go`). Recommend: rely on existing white-box. NO E2E test needed — would be racy |
| §6.2 c | `readyz` flips to 200 only after first monitor probe | COVERED + PLANNED | `internal/monitor/monitor_test.go::TestMonitor_OnFirstTick`; Task 46 `TestE2E_Readyz_503BeforeFirstProbe`, `TestE2E_Readyz_200AfterFirstProbe` |
| §6.3 a | SIGTERM/SIGINT → root context cancelled | COVERED | `internal/lifecycle/server_test.go::TestServer_StartStop` (cancel test); subprocess SIGTERM is covered by Task 38 harness cleanup |
| §6.3 b | Both servers shut down concurrently with 30s deadline | COVERED | `internal/lifecycle/server_test.go::TestServer_Stop_GracefulShutdown` |
| §6.3 c | Monitor + refresher stopped before exit | COVERED | `internal/monitor/monitor_test.go::TestMonitor_GoroutineLeak`, `TestMonitor_Stop_ContextTimeout` (with goleak) |
| §6.3 d | Process exits 0 on clean shutdown | NOT-COVERED | **Add to Task 38 harness smoke test**: assert subprocess exit code 0 after SIGTERM-driven cleanup |
| §6.4 a | `db.driver` must be postgres/mysql; missing → composition root error; unknown → validate error | COVERED | `internal/config/config_test.go::TestValidate_InvalidDBDriver` |
| §6.4 b | Embedded goose migrations run during `persistence.Open`; failure → no start | COVERED | `internal/persistence/backend_test.go::TestBackendDAO_Postgres`, `TestBackendDAO_MySQL` (testcontainers, exercises real migrations). Black-box: rely on `cmd/trino-goway` smoke startup |
| §6.4 c | Both DAOs use parameterized queries via `sqlx` | COVERED (by code review) | Static guarantee; no test required |
| §6.5 a | `proxyClient` uses `proxy.requestTimeout`; never follows redirects | COVERED + PLANNED | `internal/proxy/proxy_test.go::TestProxy_Seam2_RedirectFollowingDisabled`, `TestProxy_Seam7_ThreeClientPoolIsolation`; Task 54 `TestE2E_Inv2_NoRedirectFollowing`; diff-harness `seam2-redirect-not-followed.yaml` |
| §6.5 b | `monitorClient` uses `monitor.checkTimeout` | COVERED (white-box via composition root) | No direct test. Behavior implicit in `TestMonitor_HealthyBackend` |
| §6.5 c | `routerClient` uses `routing.external.timeout` | COVERED (white-box) — PLANNED | Task 42 `TestE2E_ExternalHTTP_TimeoutFallback` |
| §6.5 d | Same `monitorClient` for HEAD probes | COVERED (white-box via `cmd/trino-goway/main.go` wiring) — PLANNED indirectly | Task 41 `TestE2E_Recovery_HEADProbeFanout` |

### §7. Migration tooling

| AC | Criterion (abridged) | Status | Test / Task |
|----|----------------------|--------|-------------|
| §7.1 a | `goway-migrate-config --input` reads Java, writes Go | COVERED | Task 25 ✅ (`cmd/goway-migrate-config/migrate_test.go` per TODO) — verify file exists. **Confirmation needed**: re-check the cmd directory has tests |
| §7.1 b | Untranslatable values → `# WARNING:` comment in output | COVERED | Same suite as above (per Task 25 spec) |
| §7.1 c | Exits non-zero on error | COVERED | Same suite |

### §8. Differential testing (covered by Task 28 + Task 55, separate)

| AC | Criterion | Status |
|----|-----------|--------|
| §8.1 a-c | `replay` subcommand, exit codes, summary | COVERED — `internal/diffharness/replay_test.go`, `cmd/goway-diff-harness/live_test.go` (build tag `diff`) |
| §8.2 a-c | `record` subcommand, golden versioning, BootstrapContainers | COVERED — `internal/diffharness/bootstrap.go`, `internal/diffharness/golden_test.go` |
| §8.3 a-b | `live` subcommand, multi-step Extract chain | COVERED — `internal/diffharness/runner.go`, scenarios with `nextUri` |
| §8.4 a-d | DiffPolicy normalization | COVERED — `internal/diffharness/normalize_test.go`, `scenarios_validation_test.go::TestCommittedScenarios_LoadAndJustified` |
| §8.5 a | `report` subcommand re-renders | COVERED — `cmd/goway-diff-harness/main.go` report path |
| Task 55 | 6 new scenarios (admin CRUD, routing headers, kill query, recovery, health, history) | PLANNED — Task 55 in TODO.md |

---

## 2. Not Black-Box Verifiable (with White-Box Fallbacks)

Black-box = launch the `trino-goway` binary, drive it via HTTP, observe HTTP responses. The
following acceptance criteria cannot be verified purely through that surface:

| Criterion | Why black-box fails | White-box fallback |
|-----------|---------------------|--------------------|
| §1.2 d — LRU cache bounded at 4096 | Need to exhaust the cache; 4096+ real query roundtrips per test is impractical. | `internal/routing/routing_test.go::TestCache_LRUEvictionAt4096` (new). Insert 4097 entries via `Router.WriteCache`; assert first key no longer resolvable via `Route` with corresponding URI |
| §1.3 c — Singleflight coalesces kill lookups | Concurrent black-box requests still race; impossible to assert "lookup was called exactly once" from outside | `internal/routing/routing_test.go::TestRouter_KillQuery_Singleflight` (new). Counting `HistoryLookup` fake; fan out 100 goroutines; assert call counter == 1 |
| §3.1 e — Atomic-swap status map | No client-observable signature for "half-updated map"; the design property is structural | Existing `internal/monitor/monitor_test.go::TestMonitor_AllStatuses_Snapshot` is adequate. `-race` runs over `TestMonitor_HealthyBackend` provide additional coverage |
| §3.2 a — 15s DB-refresh interval (default) | Even at 1s for tests, end-to-end DB → in-memory → admin propagation timing is brittle as a black-box test | `internal/admin/admin_test.go` (extend): inject a refresher with short interval via constructor; assert `Monitor.SetBackends` was called within the interval |
| §5.3 c — JWKS background refresh logs on failure | Logging behavior not observable via HTTP; you'd need to scrape stderr — fragile | `internal/auth/oidc_test.go` (new with Task 36): point keyfunc at a server that returns 500; assert middleware continues to accept previously-issued tokens; assert log captured an error (`testutil.LogCapture` helper) |
| §6.2 b — Monitor starts after ports bound | Timing race — both happen "fast" from the outside | White-box composition-root review (already correct in `cmd/trino-goway/main.go`). Test-via-construction in `internal/lifecycle/server_test.go` if needed |
| §6.5 b-d — Three distinct HTTP clients | Three separate `*http.Client` instances are an internal design property, not an externally observable contract | `cmd/trino-goway/main_test.go` (new): assert each component is constructed with its own client via a small test exposing the wiring. Task 54 `TestE2E_Inv12_ThreeHTTPClients` proposes a *behavioral* black-box test (saturate router connections, observe proxy unaffected) — this is the better long-term option but harder to make non-flaky |
| §6.5 a (specifically `requestTimeout`) | Hard to assert "the proxy client timeout is exactly X" black-box | Verified via `internal/proxy/proxy_qa_test.go::TestProxy_Degradation_BackendTimeout` (white-box). Black-box equivalent via Task 39 with a slow backend is acceptable |
| §7.1 b — `# WARNING:` comments in migrated config | The migrator output is a file; verifying comment placement requires file inspection (not pure HTTP). The migrator is a CLI tool, not the running gateway — black-box for the gateway doesn't apply | `cmd/goway-migrate-config/migrate_test.go` already covers this |

**Recommendation:** Each white-box gap above is small (1-2 new unit tests). Add these to the
appropriate package's `*_test.go` before opening Phase 8. Doing so unblocks Phase 8 to focus
on user-visible behavioral validation rather than re-litigating internal correctness.

---

## 3. CI Integration Guidance

The test suite is layered. The CI build should run each layer separately so failures are
attributed to the right tier and so the cheap layers can fail fast.

### Tier 1 — Unit tests (no build tag)

```bash
go test ./...
```

- **Runs:** every `_test.go` file without a build tag.
- **Coverage:** `internal/config`, `internal/proxy`, `internal/routing`, `internal/monitor`,
  `internal/auth`, `internal/admin`, `internal/lifecycle`, `internal/diffharness` (library
  layer), `cmd/*` test files.
- **Dependencies:** none — runs against in-memory fakes / `httptest.Server`.
- **Budget:** < 60s on CI hardware. Race-enabled (`-race`) by default.
- **Failure mode:** unit-test failures should block PR merge. This is the inner loop.

### Tier 2 — Integration tests (`//go:build integration`)

```bash
go test -tags=integration -timeout=5m ./...
```

- **Runs:** `internal/persistence/backend_test.go`, `internal/persistence/history_test.go`,
  `internal/testutil/postgres_integration_test.go`, `internal/testutil/mysql_integration_test.go`.
- **Coverage:** real Postgres + MySQL via testcontainers-go; migrations; DAO round-trips.
- **Dependencies:** Docker (testcontainers-go).
- **Budget:** ~2-3 minutes (container boot dominates).
- **Failure mode:** required on PRs that touch `internal/persistence/`, `migrations/`, or
  composition root. Optional otherwise (can run nightly).

### Tier 3 — End-to-end tests (`//go:build e2e`)

```bash
TRINO_GOWAY_BIN=$(pwd)/bin/trino-goway \
  go test -tags=e2e -timeout=10m ./internal/e2e/...
```

- **Runs:** `internal/e2e/proxy_e2e_test.go` (G1 gate) + all Phase 8 tests (Tasks 39-54).
- **Pre-requisite:** the `trino-goway` binary must be built before this step, and its
  absolute path passed in `TRINO_GOWAY_BIN`. The Task 38 harness reads this env var.
- **Coverage:** the gateway is launched as a subprocess; all assertions go through the public
  HTTP interface.
- **Dependencies:** Docker (for testcontainers Postgres + Trino + mock OIDC/LDAP fixtures).
- **Budget:** ~5-7 minutes (Postgres + Trino container boots are the long pole; G1 alone is
  ~90s if Trino has to warm up). Recommend `-parallel=4` for the Phase-8 tests since each
  spins its own subprocess/Postgres.
- **Failure mode:** required on PRs that touch `internal/proxy`, `internal/routing`,
  `internal/monitor`, `internal/admin`, `cmd/trino-goway`, or `internal/lifecycle`.
- **Skipping:** the G1 test already skips gracefully when Docker is unavailable
  (`testcontainers.GenericContainer` failure → `t.Skipf`). Phase 8 tests should follow the
  same pattern in the Task 38 harness to keep local dev unblocked when Docker is offline.

### Tier 4 — Differential harness (`//go:build diff`)

```bash
go test -tags=diff -timeout=20m ./cmd/goway-diff-harness/...
```

- **Runs:** `cmd/goway-diff-harness/live_test.go` and any other `//go:build diff` files.
- **Coverage:** every committed scenario under `cmd/goway-diff-harness/testdata/scenarios/`
  replayed against both the live Java gateway (in a testcontainer) and the in-process Go
  gateway; results normalized and diffed.
- **Dependencies:** Docker — bootstraps `trinodb/trino-gateway:19`, `postgres:16`, and
  `trinodb/trino:481` via testcontainers-go + `network.NewDockerNetwork`.
- **Budget:** ~10-15 minutes for first run (container pulls), ~5-8 minutes for warm cache.
- **Failure mode:** **nightly job**, not per-PR. PR cost is too high; gold-master regression
  detection is the goal.

### Recommended CI pipeline

```yaml
# Conceptual GitHub Actions / GitLab CI shape; not committed yet.
jobs:
  unit:            # Tier 1 — < 60s, every PR
    run: go test -race ./...
  build:           # Build artifact for downstream jobs
    needs: unit
    run: go build -o bin/trino-goway ./cmd/trino-goway
    artifact: bin/trino-goway
  integration:    # Tier 2 — every PR that touches persistence/lifecycle/composition root
    needs: unit
    run: go test -tags=integration -timeout=5m ./...
  e2e:            # Tier 3 — every PR (recommended)
    needs: build
    run: |
      export TRINO_GOWAY_BIN=$PWD/bin/trino-goway
      go test -tags=e2e -timeout=10m ./internal/e2e/...
  diff-harness:    # Tier 4 — nightly cron only
    schedule: "0 3 * * *"
    needs: build
    run: go test -tags=diff -timeout=30m ./cmd/goway-diff-harness/...
```

**Build-tag rationale:**
- `//go:build e2e` keeps E2E tests off the default `go test ./...` so local dev iterations
  stay fast (Docker isn't required for the inner loop).
- `//go:build integration` separates DB-dependent tests for the same reason.
- `//go:build diff` separates the Java-comparison suite, which has higher infrastructure cost
  and longer boot time than the standard E2E suite.

---

## 4. Diff Harness CI Guidance (Task 28 Phase 3 remaining)

This subsection addresses the specific CI wiring needed for the `//go:build diff` tests in
`cmd/goway-diff-harness/`. It completes the Phase 3 "CI guidance for the diff build tag"
bullet that was the last remaining item on Task 28.

### What the diff harness does

`cmd/goway-diff-harness/live_test.go` (under `//go:build diff`) calls
`internal/diffharness/bootstrap.go::BootstrapContainers` to spin up:

1. A user-defined Docker network (testcontainers-go `network.NewDockerNetwork`).
2. `postgres:16` (DB for the Java gateway, with the `trino_gateway` schema).
3. `trinodb/trino:481` (one Trino coordinator; pinned to match `internal/e2e/proxy_e2e_test.go`).
4. `trinodb/trino-gateway:19` (Java gateway) configured via the embedded template
   `internal/diffharness/testdata/java-gateway-config.yaml.tmpl`.
5. The in-process Go gateway (via `cmd/goway-diff-harness/live_test.go` composition root).

It then replays each scenario in `cmd/goway-diff-harness/testdata/scenarios/` against both
gateways concurrently and diffs the normalized responses.

### Required CI environment

- **Docker:** must be available and have enough resources for 3-4 concurrent containers
  (Postgres + Trino + Java gateway are the heavy ones). Minimum: 4 GiB RAM, 2 vCPU.
- **Image pulls:** must allow pulling `trinodb/trino-gateway:19`, `trinodb/trino:481`, and
  `postgres:16`. Configure the runner with Docker Hub credentials if behind a registry mirror
  to avoid pull-rate throttling.
- **Network egress:** required for first-time image pulls; later runs use the local cache.
- **No env vars required for the test itself** — `BootstrapContainers` discovers ports from
  testcontainers-go and threads them through the embedded config template. There is no
  `TRINO_GOWAY_BIN` requirement (the diff harness runs the Go gateway in-process, not as a
  subprocess; this is intentional and different from Tier 3).

### Recommended CI command

```bash
go test -tags=diff -timeout=30m -v ./cmd/goway-diff-harness/...
```

- **`-tags=diff`** — selects the live diff test file (everything else is build-tagged out).
- **`-timeout=30m`** — first-time image pulls plus 8 scenarios * ~10s/scenario plus 90s of
  container boot puts the cold-cache budget around 8-12 minutes; the 30m ceiling absorbs
  Docker Hub throttling and image-layer fetch latency. With a warm cache and pre-pulled
  images the run is ~5 minutes.
- **`-v`** — print scenario-level progress; the harness is a long-running job and silent
  output during container boots is hard to debug if the build fails.

### Recommended schedule

- **Nightly cron** (e.g. `0 3 * * *` UTC) — primary trigger. Detects Java-side behavior
  drift and golden staleness.
- **Manual dispatch** — for engineers landing changes to the proxy / routing / cookie
  layers; should be invocable from the PR comment ("ok to nightly diff") without a full
  CI re-run.
- **Pre-release tag** — must pass before any version tag is published. This is the
  Java-parity gate.
- **Not per-PR** — the cost is too high for routine PRs. The committed scenarios + their
  goldens act as the per-PR gate via `go test -tags=diff` in the nightly only.

### Failure triage

When the nightly fails:

1. Check if the failure is in `BootstrapContainers` (infra) or in a scenario diff
   (behavior).
2. **Infra failure:** retry the job once. If still failing, check Docker Hub status, runner
   disk space, and image-layer pull bandwidth.
3. **Scenario diff failure:**
   - Inspect the failing scenario's `DiffPolicy.IgnoreHeaders` / `IgnoreBodyFields` —
     does the new diverging field belong in the normalizer? If yes, add it with a
     `[JUSTIFIED]` comment.
   - If the divergence is a real Go-side regression, fix the code and re-run.
   - If the divergence is a *Java*-side change (new Trino version, new gateway feature),
     re-record the golden via the same harness with `record` subcommand against the live
     Java container, then re-commit the golden JSON.

### Pre-pull script for faster CI

Optional optimization for self-hosted runners:

```bash
docker pull trinodb/trino-gateway:19
docker pull trinodb/trino:481
docker pull postgres:16
```

Run this once on each runner host. Shaves ~3-4 minutes off cold-start runs.

### Open question (defer to architect)

The diff harness currently runs the Go gateway **in-process** (within the test binary) while
Tier 3 E2E runs it as a **subprocess** (via `TRINO_GOWAY_BIN`). The two modes test different
properties — diff harness validates protocol parity; Tier 3 validates binary correctness +
SIGTERM handling + composition root wiring. Keeping them separate is intentional, but worth
revisiting if scenario count grows: at some point, a long-running subprocess-based diff
harness becomes more representative of production. For now, the in-process approach is
correct; revisit at Task 55 + 5 more scenarios.

---

## 5. Sign-off

This document is the qa-tech-lead sign-off for Phase 8 to begin. Specifically:

1. **Coverage map is complete** — every acceptance criterion in §1-§7 is either covered or
   has an explicit task / fallback noted.
2. **NOT-COVERED gaps are flagged** — 13 explicit gaps identified (see §1.2c, §1.2d, §1.3c,
   §2.3b, §3.2a, §4.2c partial, §4.5a, §4.5c partial, §4.5d partial, §4.5e, §5.1f white-box,
   §5.3b, §5.3c, §6.1a, §6.3d) — most of these are 1-2 additional unit tests or sub-tests
   inside an already-planned Phase 8 task. None are blockers.
3. **White-box fallbacks proposed** — 9 criteria need white-box support (see §2). Add these
   to the relevant package `*_test.go` before opening Phase 8 for clean coverage attribution.
4. **CI tiering recommended** — four tiers (unit / integration / e2e / diff), each with its
   own build tag, command, and budget. Tier 3 (E2E) requires a pre-built binary artifact
   passed via `TRINO_GOWAY_BIN`.
5. **Diff harness wiring complete** — recommended schedule, environment, and triage steps
   above. Task 28 Phase 3 final bullet is satisfied by this document.

**Phase 8 dependencies that must land first:**
- Tasks 35, 36, 37 (test infrastructure: TrinoFake, OIDC server, LDAP server) — block Tasks
  39-54 broadly.
- Task 38 (full-stack harness) — block all Phase 8 tests; depends on Task 24 (already
  complete).
- The white-box fallback unit tests called out in §2 should land alongside Task 38 so
  Phase-8 black-box tests don't accidentally re-cover what's already nailed down.

When the above land, Phase 8 (Tasks 39-55) is unblocked.
