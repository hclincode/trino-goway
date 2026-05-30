---
title: Admin REST API Completeness Gap (Go vs Java)
author: java-analyst
role: Java Analyst
component: trino-gateway
topics:
  - mgmt-api
  - auth
  - cluster-registry
date: 2026-05-29
status: draft
risk: high
version_pins:
  trino-gateway: 171ce25
  trino-goway: main @ b4b3a57
related-to:
  - admin-api-surface.java-analyst.md
---

# Admin REST API Completeness Gap (Go vs Java)

## Summary

Audited all 38 admin endpoints from `[[admin-api-surface]]` against the Go implementation
under `internal/admin/`. Result (recomputed from matrix 2026-05-29):

- **COMPLETE: 19 / 38 endpoints (50%)**
- **PARTIAL: 18 / 38 endpoints (47%)**
- **MISSING: 1 / 38 endpoints (3%)**

Functional surface coverage for the read/CRUD admin paths used by scripts and the modern
webapp UI is largely COMPLETE; the gaps cluster in two areas:

1. **Static web UI serving** — `serveAssets`, `serveLogoSVG`, `serveIndex` are HTTP-200
   stubs that do not serve real bundle content. Not blocking E2E API tests, but blocks any
   browser-driven test.
2. **Login / SSO** — `/sso` and `/oidc/callback` are unimplemented placeholders; `/login`
   only handles the NOOP flow. The full OAuth2/OIDC and form/LDAP authentication flows are
   not present in v1 (this is documented as v1-scope in `USE_STORIES.md §4.5`).

**Hard E2E test blockers (Tasks 47 & 48):**

- One wire-shape mismatch on the webapp envelope JSON (request body field names for
  `findQueryHistory` use Java naming `user` vs Go naming `userName`).
- `webappGetRoutingRules` and `webappUpdateRoutingRules` are routed as `POST` in Go but
  Java exposes `getRoutingRules` as `GET` (`updateRoutingRules` is `POST` in both).
- `UIConfiguration` returns `{authType}` in Go but Java returns `{disablePages}` — wire
  shape disagreement on `/webapp/getUIConfiguration`.
- `DistributionResponse.startTime` Go format is `"2006-01-02T15:04:05.000Z"` (Zulu) vs
  Java `"yyyy-MM-dd'T'HH:mm:ss.SSSXXX"` which renders `+00:00` not `Z`.
- `QueryDetail` in Go is missing the `externalUrl` field on the wire (struct has it but
  `queryDetailFromRecord()` never populates it because `persistence.QueryRecord` has no
  `ExternalURL`).

USE_STORIES §4 acceptance criteria are met for the supported v1 surface; the wire-shape
gaps above need to be resolved before the diff harness can replay Java-recorded goldens.

---

## Endpoint Status Matrix

Endpoints are grouped by resource family, mirroring the order in `[[admin-api-surface]]`.

| # | Endpoint | Method | Java Role | Go Role | Status | Notes |
|---|----------|--------|-----------|---------|--------|-------|
| 1 | `/api/public/backends` | GET | none | none | COMPLETE | Wire shape verified. |
| 2 | `/api/public/backends/{name}` | GET | none | none | COMPLETE | 404 on miss verified. |
| 3 | `/api/public/backends/{name}/state` | GET | none | none | PARTIAL | Returns `BackendResponse` (`{name, proxyTo, …, queued, running, status}`); Java returns a `ClusterStats` object with `trinoStatus`, `queuedQueryCount`, `runningQueryCount`. Field names differ. |
| 4 | `/gateway` | GET | API | API | COMPLETE | JSON-encoded string `"ok"`. |
| 5 | `/gateway/backend/all` | GET | API | API | COMPLETE | Array of `ProxyBackend`. |
| 6 | `/gateway/backend/active` | GET | API | API | COMPLETE | Array of active `ProxyBackend`. |
| 7 | `/gateway/backend/deactivate/{name}` | POST | API | API | PARTIAL | Java returns 404 with `text/plain` error message string; Go returns 404 via `http.Error` which is also `text/plain`. Format of the message differs but contract holds. |
| 8 | `/gateway/backend/activate/{name}` | POST | API | API | PARTIAL | Same as deactivate; also Java's activate sets the in-memory routing state but Go's `activateBackend` does not call `StatusMut.SetBackendStatus(... PENDING)`. Java behavior note in `[[admin-api-surface]]` §"Behavior vs Implementation Artifact" only attributes the in-memory transition to `/entity` upserts — but verify with `@architect`. |
| 9 | `/gateway/backend/modify/add` | POST | API | API | COMPLETE | Echoes `ProxyBackend` body. |
| 10 | `/gateway/backend/modify/update` | POST | API | API | COMPLETE | Same as add (Go uses `Upsert`). |
| 11 | `/gateway/backend/modify/delete` | POST | API | API | COMPLETE | Raw-string body verified at `internal/admin/backend.go:255`. |
| 12 | `/entity` | GET | ADMIN | ADMIN | COMPLETE | Returns `["GATEWAY_BACKEND"]`. |
| 13 | `/entity?entityType=GATEWAY_BACKEND` | POST | ADMIN | ADMIN | PARTIAL | Go echoes the `ProxyBackend` JSON in the response body (`backend.go:313`); Java returns 200 with **empty body** (`EntityEditorResource.updateEntity`). Wire-format disagreement that can break clients relying on empty body. |
| 14 | `/entity/{entityType}` | GET | ADMIN | ADMIN | PARTIAL | Java throws 500 on unknown `entityType`; Go returns `200 []` (`backend.go:320`). Wire-shape disagreement. USE_STORIES §4.2 explicitly says Go behavior is `200 []` (acceptance criterion) — so Go follows USE_STORIES, but diverges from Java wire format. |
| 15 | `/trino-gateway/livez` | GET | none | none | COMPLETE | Plain `"ok"` text. |
| 16 | `/trino-gateway/readyz` | GET | none | none | PARTIAL | Java body during startup is `"Trino Gateway is still initializing"`; Go body is `"not ready"` (`health.go:19`). Body text differs (status code matches). |
| 17 | `/trino-gateway` | GET | none | none | PARTIAL | Returns hardcoded `<!DOCTYPE html>…Trino Gateway` stub (`router.go:202`), not the SPA `index.html`. Comment says "replaced by embedded static bundle in production" — but no embed exists yet. |
| 18 | `/trino-gateway/api/queryHistory` | GET | USER | USER | PARTIAL | `QueryDetail.externalUrl` is empty on the wire — `queryDetailFromRecord` (`query.go:23`) does not set it because `persistence.QueryRecord` has no `ExternalURL` column. Java populates it from the backend record. |
| 19 | `/trino-gateway/api/activeBackends` | GET | USER | USER | COMPLETE | Array of active `ProxyBackend`. |
| 20 | `/trino-gateway/api/queryHistoryDistribution` | GET | USER | USER | COMPLETE | Map of `backendUrl → count`. Scoping verified in tests (`admin_gaps_test.go:473`). |
| 21 | `/webapp/getAllBackends` | POST | USER | USER | PARTIAL | `BackendResponse.queued` and `BackendResponse.running` are always 0 in Go because `backendResponseFromPersistence` (`backend.go:45`) only fills `Status`. Status field correct (`HEALTHY/UNHEALTHY/PENDING`). |
| 22 | `/webapp/findQueryHistory` | POST | USER | USER | PARTIAL | **Request body shape mismatch:** Go expects `{userName, backendUrl, queryId, source, page, pageSize}` (`webapp.go:94`); Java expects `{user, externalUrl, queryId, source, page, size}` (per `QueryHistoryRequest.java`). Field names `user` vs `userName`, `externalUrl` vs `backendUrl`, `size` vs `pageSize` differ. USE_STORIES §4.3 documents the Go names — but those names disagree with Java's wire format. |
| 23 | `/webapp/getDistribution` | POST | USER | USER | PARTIAL | Wire-shape OK on most fields, but `startTime` format differs (Go `"2006-01-02T15:04:05.000Z"` Zulu vs Java `"yyyy-MM-dd'T'HH:mm:ss.SSSXXX"` which prints `+00:00`). `lineChart` is always empty in Go (intentional v1 stub). `distributionChart` `queryCount` populated only from records returned by `FindByFilter(PageSize:10000, Page:1)`. |
| 24 | `/webapp/saveBackend` | POST | ADMIN | ADMIN | COMPLETE | `Result<ProxyBackend>` envelope verified. |
| 25 | `/webapp/updateBackend` | POST | ADMIN | ADMIN | COMPLETE | Same shape as saveBackend. |
| 26 | `/webapp/deleteBackend` | POST | ADMIN | ADMIN | COMPLETE | Takes full `ProxyBackend`, uses only `name` (`webapp.go:284`). `Result<bool>` envelope. |
| 27 | `/webapp/getRoutingRules` | **GET** in Java / **POST** in Go | ADMIN | ADMIN | PARTIAL | **HTTP method mismatch.** Java uses `GET` (per `[[admin-api-surface]]` table). Go registers it as `POST` (`router.go:188`). USE_STORIES §4.3 also says POST. Java's 204 + envelope-with-code-500 for external rules engine is not replicated. |
| 28 | `/webapp/updateRoutingRules` | POST | ADMIN | ADMIN | PARTIAL | Stub returns empty list always; does not parse `RoutingRule` request body or apply changes. USE_STORIES §4.3 documents this as a v1 stub. |
| 29 | `/webapp/getUIConfiguration` | **GET** in Java / **POST** in Go | USER | USER | PARTIAL | **HTTP method mismatch** (Java GET, Go POST). **Wire shape mismatch:** Java returns `{disablePages: array<string>}`, Go returns `{authType: string}`. The Go shape matches USE_STORIES §4.3 ("returns `{authType}`") but diverges from Java entirely. |
| 30 | `/` | GET | none | none | COMPLETE | Redirects 303 to `/trino-gateway` (`router.go:93`). |
| 31 | `/sso` | POST | none | none | PARTIAL | Returns 302 redirect to `IssuerURL` if `auth.Type=OIDC` (stub), else 500. Does not implement the OAuth2 PKCE flow, does not write the `oidc-state` cookie. v1 placeholder. |
| 32 | `/oidc/callback` | GET | none | none | PARTIAL | Always returns 501 Not Implemented (`authhandlers.go:72`). v1 placeholder. |
| 33 | `/login` | POST | none | none | PARTIAL | NOOP returns `{token: <username>}` (matches Java's NOOP special case). LDAP/OIDC return error envelope. Java's LDAP returns a real JWT. v1 limitation. |
| 34 | `/logout` | POST | none | none | COMPLETE | Returns `{code:200, msg:"Successful.", data:null}`. |
| 35 | `/userinfo` | POST | USER | USER | COMPLETE | Returns `{userId, userName, roles, permissions}`. `permissions` always empty array — Java may populate from `pagePermissions` config map (out of v1 scope per USE_STORIES §4.5). |
| 36 | `/loginType` | POST | none | none | COMPLETE | Returns `"oauth" | "form" | "none"` based on `auth.Type`. |
| 37 | `/trino-gateway/logo.svg` | GET | none | none | PARTIAL | Returns a 1-byte empty `<svg/>` placeholder (`router.go:207`); Java serves the real classpath asset. Not load-bearing for E2E API tests. |
| 38 | `/trino-gateway/assets/{path}` | GET | none | none | MISSING | Stub returns 404 unconditionally (`router.go:213`). Blocks any browser-driven E2E test. |

---

## Wire Shape Mismatches

The following wire-shape differences will cause E2E parity tests (Task 47, Task 48,
Task 55) to fail when comparing recorded Java goldens against the Go implementation.

### M1. `webappFindQueryHistory` request body field names

- **Java** (`QueryHistoryRequest.java`): `{user, externalUrl, queryId, source, page, size}`
- **Go** (`webapp.go:94`): `{userName, backendUrl, queryId, source, page, pageSize}`

Three field name disagreements: `user→userName`, `externalUrl→backendUrl`, `size→pageSize`.
USE_STORIES §4.3 documents the Go names — the spec was written to match Go, not Java.

**Impact:** any caller built against the Java client will send a JSON body whose fields the
Go handler ignores, so the user-scoping enforcement also silently breaks (the
`req.UserName` ends up empty, which for an ADMIN caller returns all records but is
effectively "no filter"; for non-ADMIN the handler forces it to the caller's name anyway).

**Resolution options:** rename Go fields to match Java (requires updating USE_STORIES §4.3
and any test cases that send the Go shape), OR add JSON unmarshal alias tags accepting both.

### M2. `/webapp/getUIConfiguration` response payload

- **Java** (`UIConfiguration.java`): `{disablePages: array<string> | null}`
- **Go** (`webapp.go:73`): `{authType: string}`

USE_STORIES §4.3 says "returns `{authType}`" — so Go matches USE_STORIES but contradicts
Java. The frontend bundled with the Java gateway will not get its expected `disablePages`
list; the Go-bundled frontend (if it exists) cannot read `authType` from Java's wire
format.

**Resolution:** decide whether the Go frontend or Java parity is authoritative; add the
other field if both are needed.

### M3. `/webapp/getRoutingRules` HTTP method

- **Java**: `GET` (annotated `@GET` on `GatewayWebAppResource.getRoutingRules`)
- **Go**: `POST` (router.go:188)

Same for `getUIConfiguration` (Java GET, Go POST). A Java-recorded golden using GET will
not match a Go server that only accepts POST.

USE_STORIES §4.3 says these endpoints use POST — the Go side again matches USE_STORIES,
not Java.

**Resolution:** either register the Go routes as GET (matching Java) or update USE_STORIES
to call out the v1 method deviation explicitly so the diff harness can record the variance.

### M4. `DistributionResponse.startTime` format

- **Java**: `yyyy-MM-dd'T'HH:mm:ss.SSSXXX` → e.g. `"2024-01-02T03:04:05.678+00:00"`
- **Go** (`webapp.go:220`): `"2006-01-02T15:04:05.000Z"` → e.g. `"2024-01-02T03:04:05.678Z"`

Both are valid ISO-8601 representations of UTC, but the literal byte sequences differ
(`Z` vs `+00:00`). A byte-level comparison of the JSON body will fail.

**Resolution:** change Go's format string to `"2006-01-02T15:04:05.000-07:00"` (Go's
equivalent of `XXX`) and `.UTC()` to force the zone, producing `+00:00`.

### M5. `QueryDetail.externalUrl` always empty in Go

- **Java**: populated from `Backend.externalUrl` of the routed backend (or null)
- **Go** (`query.go:23`): the `ExternalURL` field is declared in the struct but
  `queryDetailFromRecord` never assigns it — `persistence.QueryRecord` has no
  `ExternalURL` field to copy from.

**Impact:** all Java goldens that include a non-null `externalUrl` in query history
records will diff against the Go empty string.

**Resolution:** add an `ExternalURL` column to the query history schema and write it on
query capture, OR document the deviation and add `externalUrl` to `DiffPolicy.IgnoreBodyFields`
in any scenario that involves query history.

### M6. `ProxyBackend.externalUrl` omitted when empty

- **Java** wire shape always emits `externalUrl` (even as `null`).
- **Go** (`backend.go:21`): tagged `json:"externalUrl,omitempty"`. When empty, the field is
  dropped from the JSON output.

**Impact:** `{"name":"x","proxyTo":"http://x","externalUrl":null,"active":true,"routingGroup":"adhoc"}` (Java)
vs `{"name":"x","proxyTo":"http://x","active":true,"routingGroup":"adhoc"}` (Go).
Structural diff (go-cmp on `map[string]any`) detects this; byte diff certainly does.

Also: Go never stores `ExternalURL` in `persistenceBackendFromProxy` — the column does
not exist in the Backend struct, so even if a caller passes `externalUrl` it is dropped
on the persist path.

**Resolution:** drop `,omitempty`, add `ExternalURL` to `persistence.Backend`, and
populate it in `proxyBackendFromPersistence`.

### M7. `/api/public/backends/{name}/state` returns `BackendResponse`, not `ClusterStats`

- **Java**: returns `ClusterStats` JSON: `{trinoStatus, queuedQueryCount, runningQueryCount, …}`
- **Go**: returns `BackendResponse` JSON: `{name, proxyTo, …, queued, running, status}`

Two structural differences: field names (`trinoStatus` vs `status`, `queuedQueryCount`
vs `queued`, `runningQueryCount` vs `running`) and presence of the full `ProxyBackend`
fields in Go output.

**Resolution:** add a dedicated `ClusterStats` shape and a separate handler, or document
this as a v1 deviation in USE_STORIES.

### M8. `/entity` upsert response body

- **Java**: empty body (200 OK with no payload).
- **Go**: echoes the submitted `ProxyBackend` as JSON.

Goldens that record an empty body will diff against Go's JSON object.

**Resolution:** change Go to `w.WriteHeader(200)` with no body write.

### M9. `/trino-gateway/readyz` body during startup

- **Java**: `"Trino Gateway is still initializing"`
- **Go**: `"not ready"`

Status code (503) matches. Only the response body text differs.

---

## Missing Endpoints

Strictly MISSING (no handler at all). The remaining gaps are PARTIAL (handler exists but
diverges from Java behavior).

### MISSING-1. `/trino-gateway/assets/{path}` content

`router.go:213` `serveAssets` returns `http.NotFound` unconditionally. Java serves real
asset bytes from the classpath at `/static/assets/<path>` with MIME-type sniffing. Path
traversal protection (`..` blocked, non-canonical paths return 404) is also absent
because there is nothing to traverse.

**E2E impact:** Tasks 47-48 (admin CRUD + webapp endpoints) do not depend on assets.
Browser-driven scenarios (none planned in the current task list) would be blocked.

### Implicit MISSING via PARTIAL: full auth surface

These are listed as PARTIAL in the matrix because a handler does exist, but the handler
is a stub that does not perform the documented behavior. They are equivalent to MISSING
for E2E auth tests (Task 51, Task 52):

- `/sso` — does not implement OAuth2 PKCE; does not set `oidc-state` cookie; just emits
  a stub 302 to the issuer URL.
- `/oidc/callback` — returns 501 Not Implemented; does not exchange code for token, does
  not validate nonce, does not set the `token` session cookie.
- `/login` — only the NOOP branch (which echoes the username as the "token") is wired up.
  LDAP and OIDC return `resultErr("login not implemented…")`. Java's LDAP path issues a
  real signed JWT.
- `/userinfo` `permissions` field — always returns empty array. Java derives the list
  from the `pagePermissions` config map.

**E2E impact:** Tasks 51 and 52 (OIDC and LDAP E2E) cannot pass against the Go gateway
as-is. USE_STORIES §4.5 explicitly notes these as v1 deferred — so this is expected scope,
not a regression.

---

## Role-Allowed Mismatches

A side-by-side check of `@RolesAllowed` (Java) against the `auth.RequireRole(...)` middleware
chains in `internal/admin/router.go`:

| Endpoint | Java Role | Go Role | Match? |
|----------|-----------|---------|--------|
| `/gateway/*` (all) | API | API | ✓ |
| `/entity/*` (all) | ADMIN | ADMIN | ✓ |
| `/trino-gateway/api/queryHistory` | USER | USER | ✓ |
| `/trino-gateway/api/activeBackends` | USER | USER | ✓ |
| `/trino-gateway/api/queryHistoryDistribution` | USER | USER | ✓ |
| `/webapp/getAllBackends` | USER | USER | ✓ |
| `/webapp/findQueryHistory` | USER | USER | ✓ |
| `/webapp/getDistribution` | USER | USER | ✓ |
| `/webapp/getUIConfiguration` | USER | USER | ✓ |
| `/webapp/saveBackend` | ADMIN | ADMIN | ✓ |
| `/webapp/updateBackend` | ADMIN | ADMIN | ✓ |
| `/webapp/deleteBackend` | ADMIN | ADMIN | ✓ |
| `/webapp/getRoutingRules` | ADMIN | ADMIN | ✓ |
| `/webapp/updateRoutingRules` | ADMIN | ADMIN | ✓ |
| `/userinfo` | USER | USER | ✓ |
| `/login`, `/logout`, `/loginType`, `/sso`, `/oidc/callback` | none | none | ✓ |
| `/api/public/*` | none | none | ✓ |
| `/trino-gateway/livez`, `/readyz` | none | none | ✓ |
| `/trino-gateway`, `/trino-gateway/logo.svg`, `/trino-gateway/assets/*` | none | none | ✓ |
| `/` | none | none | ✓ |

**No role mismatches were found.** The cross-role denial semantics (granting only USER
does not implicitly grant ADMIN) are correctly tested in
`internal/admin/admin_gaps_test.go:107`.

The only subtlety worth flagging: Java's NOOP authorizer grants ALL roles unconditionally
(`NoopAuthorizer`). Go's behavior under `auth.Type: NOOP` depends on the `authorization:`
regex block — if the operator omits it, every regex is empty and `HasRole` returns false
for every role (per `internal/auth/roles.go:35`), making the entire admin API
inaccessible. USE_STORIES §5.1 documents this ("`NOOP`: no role assignments unless the
regex happens to match the empty string") but it is a behavior divergence from Java.

---

## USE_STORIES §4 Acceptance Criteria Verdict

| AC | Verdict | Notes |
|----|---------|-------|
| §4.1 Backend CRUD via REST — endpoints exist under API role | ✅ PASS | All 7 endpoints present with correct roles. |
| §4.1 Wire shape `{name, proxyTo, externalUrl?, active, routingGroup}` | ⚠️ PARTIAL | `externalUrl` is `omitempty` (not always emitted) AND never populated from persistence (no column). |
| §4.1 Same shape at `/api/public/backends*` | ✅ PASS | List + by-name endpoints conform. State endpoint diverges (see M7). |
| §4.1 Persisted via Postgres `ON CONFLICT` / MySQL `ON DUPLICATE KEY UPDATE` | ✅ PASS | Verified via `BackendStore.Upsert` use in handlers. |
| §4.2 `GET /entity` returns `["GATEWAY_BACKEND"]` | ✅ PASS | `backend.go:279`. |
| §4.2 `POST /entity` upserts + flips monitor (PENDING/UNHEALTHY) | ✅ PASS | `backend.go:303` + tests at `admin_test.go:535`. |
| §4.2 `GET /entity/{type}` returns backends; unknown type → `200 []` | ✅ PASS | Explicit `200 []` per USE_STORIES (divergent from Java's 500 — see row 14). |
| §4.2 `POST /entity` unknown type → 500 | ✅ PASS | `backend.go:287`. |
| §4.2 ADMIN role required | ✅ PASS | `router.go:144`. |
| §4.3 `/webapp/*` envelope `{code, msg, data}` | ✅ PASS | `Result[T]` in `webapp.go:13`. |
| §4.3 `getAllBackends` returns live `status` | ✅ PASS | But `queued/running` always 0 (PARTIAL — see row 21). |
| §4.3 `findQueryHistory` request shape | ⚠️ PARTIAL | Field names disagree with Java (M1). Spec text matches Go. |
| §4.3 Non-ADMIN `findQueryHistory` username forced server-side | ✅ PASS | `webapp.go:117`. Test: `admin_gaps_test.go:352`. |
| §4.3 `getDistribution` response fields all present | ⚠️ PARTIAL | All fields present; `startTime` format diverges from Java (M4); `lineChart` always empty. |
| §4.3 `getUIConfiguration` returns `{authType}` | ✅ PASS (vs USE_STORIES) | Diverges from Java's `{disablePages}` (M2). |
| §4.3 Routing rule stubs return empty list, ADMIN-only | ✅ PASS | `webapp.go:238`. |
| §4.3 Save/update/delete ADMIN-only | ✅ PASS | `router.go:187`. |
| §4.3 getAllBackends/findQueryHistory/getDistribution/getUIConfiguration USER-only | ✅ PASS | `router.go:175`. |
| §4.4 `/trino-gateway/api/queryHistory` USER role | ✅ PASS | `router.go:164`. |
| §4.4 ADMIN → 100 most recent | ✅ PASS | `query.go:47`. |
| §4.4 Non-ADMIN scoped server-side to own username | ✅ PASS | `query.go:50`. Test: `admin_gaps_test.go:208`. |
| §4.4 `queryHistoryDistribution` scoped for non-ADMIN | ✅ PASS | `query.go:100`. Test: `admin_gaps_test.go:483`. |
| §4.4 `activeBackends` legacy wire format | ✅ PASS | `query.go:74`. |
| §4.5 `/userinfo` returns `{userId, userName, roles, permissions}` | ⚠️ PARTIAL | `permissions` always empty array. |
| §4.5 `/loginType` reports auth mode | ✅ PASS | `authhandlers.go:11`. |
| §4.5 `/login` NOOP returns `{token: <username>}` | ✅ PASS | `authhandlers.go:39`. |
| §4.5 `/login` OIDC/LDAP return error envelope | ✅ PASS | Documented v1 scope. |
| §4.5 `/logout` returns success envelope | ✅ PASS | `authhandlers.go:50`. |

Overall, the §4 acceptance criteria are MET for the documented v1 scope; the wire-shape
divergences are between Go and Java, not between Go and USE_STORIES.

---

## Recommendations for Task 47 (Admin CRUD E2E) and Task 48 (Webapp Endpoints E2E)

These tasks should be implementable today against the Go gateway with the following
constraints:

1. **Test against the Go-native wire shapes**, not Java goldens. Specifically use:
   - `findQueryHistory` body field names: `userName`, `backendUrl`, `pageSize`.
   - `getUIConfiguration` response: `{authType}`.
   - `getRoutingRules` / `getUIConfiguration` HTTP method: POST.
   - `getAllBackends` `BackendResponse.queued/running` = 0 (do not assert otherwise).
   - `DistributionResponse.startTime` ends in `Z` (Zulu), not `+00:00`.
   - `QueryDetail.externalUrl` is always empty.
   - `ProxyBackend.externalUrl` is omitted when empty (no `null`).

2. **Do NOT test against the Java-recorded goldens for the affected endpoints** until the
   wire-shape gaps M1–M9 are resolved. Either:
   - Fix the Go side to match Java (then record fresh goldens), OR
   - Record Go-native goldens and document the deviation in scenario YAML
     `DiffPolicy.IgnoreBodyFields` with the justification from this study.

3. **Skip `/sso`, `/oidc/callback`, full `/login` in Task 51/52** — these are documented
   v1 deferred. Add explicit tests asserting the stub behavior (501 / 500 / error envelope)
   rather than the real flow.

4. **Skip `/trino-gateway/assets/*` and `/trino-gateway/logo.svg` content tests** — the
   handlers exist but serve stub content. Test the 200 status code only.

## Open Questions

- Should `/webapp/getUIConfiguration` return Java's `{disablePages}` or Go's `{authType}`?
  Both? USE_STORIES says `{authType}`. Java frontends expect `{disablePages}`. Needs a
  decision before recording goldens. (`@architect` should arbitrate.)
- The Go `activateBackend` does not flip monitor state to PENDING; Java's `/gateway/backend/activate`
  also does NOT (only `/entity` does, per `[[admin-api-surface]]` §"Behavior vs
  Implementation Artifact"). So Go is consistent with Java here — but should `activateBackend`
  also flip the in-memory state for UX consistency with `/entity`? (`@go-implementer`.)
- `QueryDetail.externalUrl` requires a schema migration to populate. Is this in v1 scope?
  (`@architect` + `@go-qa`.)

## Cross-references

- `[[admin-api-surface]]` — the authoritative Java endpoint surface this audit was run against.
- `[[external-routing-contract]]` — for the routing-rule endpoints (which are v1 stubs).
- `[[persistence-and-db-schema]]` — for the `ExternalURL` column gap on QueryRecord and Backend.
