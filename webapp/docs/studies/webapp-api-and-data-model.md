---
title: Webapp API and Data Model
author: ui-analyst
role: UI Analyst
component: trino-gateway
topics: [webapp, api, data-model, backend-contract]
date: 2026-06-03
status: draft
risk: high
related-to: [webapp-feature-inventory.md, webapp-architecture-stack-and-ux.md]
---

# Webapp API and Data Model

## Summary

The original webapp communicates with thirteen distinct backend endpoints (auth + webapp). All webapp endpoints return a `{code, msg, data}` JSON envelope. This document maps every frontend API call to its source file, the request/response shape, and reconciles them against what the Go `internal/admin` server actually serves — flagging field mismatches that the rebuild must handle.

## Envelope format

All `/webapp/*` and `/login*` endpoints return:
```json
{ "code": 200, "msg": "Successful.", "data": <payload> }
```
- `code 200` = success; `code 401`/`403` = auth error; other codes = error message in `msg`.
- HTTP status is always `200 OK` even for errors, except for 401/403 which may be sent at the HTTP level too.
- The `ClientApi` (`src/api/base.ts`) checks both the HTTP status and `resJson.code`.

Special case: `GET /webapp/getRoutingRules` may return HTTP 204 (no body) when an external routing service is in use — the client treats this as `{isExternalRouting: true}` (`src/api/base.ts:22-24`).

---

## Auth endpoints

### `POST /loginType`
- **Source:** `src/api/webapp/login.ts:19`
- **Request:** `{}` (empty body)
- **Response data:** string — one of `"form"` | `"oauth"` | `"none"`
- **Go handler:** `handleLoginType` (registered in `internal/admin/router.go:119`)
- **Notes:** no auth required; determines which login UI to render.

### `POST /login`
- **Source:** `src/api/webapp/login.ts:3`
- **Request:** `{ username: string, password: string }`
- **Response data:** `{ token: string }`
- **Go handler:** `handleLogin`
- **Notes:** token is stored in zustand access store; not in localStorage (uses zustand persist which writes to localStorage via `StoreKey.Access`).

### `POST /sso`
- **Source:** `src/api/webapp/login.ts:11`
- **Request:** `{}` (empty body)
- **Response data:** `string` — the OAuth2 redirect URL
- **Go handler:** `handleSSO`
- **Notes:** client does `window.location.href = data` to initiate OAuth flow.

### `GET /oidc/callback`
- **Source:** not called directly by the SPA; the browser is redirected here by the IdP.
- **Go handler:** `handleOIDCCallback` (`router.go:120`)
- **Notes:** the callback handler presumably sets a `token` cookie that the SPA picks up on re-mount (`App.tsx:40-46`).

### `POST /userinfo`
- **Source:** `src/api/webapp/login.ts:15`
- **Request:** `{}` (empty body)
- **Response data:** user info object (merged directly into the access store)
- **Go handler:** `handleUserinfo` (requires `RoleUser`)
- **Response shape (used by access store):**
  ```ts
  {
    userId: string;
    userName: string;
    nickName: string;
    userType: string;
    email: string;
    phonenumber: string;
    sex: string;
    avatar: string;
    permissions: string[];   // page-level permission keys, e.g. ["dashboard","cluster"]
    roles: string[];         // e.g. ["ADMIN"] or ["USER"]
  }
  ```
- **Notes:** called automatically after token is set (`access.ts:52-55`). The `permissions` array gates page visibility; empty = all pages allowed.

### `POST /logout`
- **Source:** `src/api/webapp/login.ts:7`
- **Request:** `{}` (empty body)
- **Response data:** (ignored by client)
- **Go handler:** `handleLogout`

### `GET /webapp/getUIConfiguration`
- **Source:** `src/api/webapp/login.ts:23`
- **Request:** GET with no params
- **Response data:**
  ```ts
  { authType: string; disablePages?: string[] }
  ```
- **Go response shape (`webapp.go:73-75`):**
  ```go
  type UIConfiguration struct {
      AuthType string `json:"authType"`
  }
  ```
- **MISMATCH:** The frontend reads `res.disablePages` (`layout.tsx:28`), but the Go struct only has `authType`. The Go server does NOT return `disablePages`. Result: `disablePages` is always `undefined` → interpreted as empty → all pages shown. The rebuild must add `DisablePages []string` to `UIConfiguration` if page hiding is ever needed.
- **Notes:** the client checks `Object.keys(res).length == 0` for empty response — this is a bug in the original (`layout.tsx:27`): if the object has any keys (like `authType`) this branch is never taken. `disablePages` defaults to `['']` in the state, which means the `includes` check is against the empty-string itemKey and effectively never hides any real page.

---

## Webapp data endpoints

### `POST /webapp/getAllBackends`
- **Source:** `src/api/webapp/cluster.ts:4`
- **Request:** `{}` (empty body)
- **Response data:** `BackendData[]`

**Frontend type (`src/types/cluster.d.ts`):**
```ts
interface BackendData {
  name: string;
  proxyTo: string;
  active: boolean;
  routingGroup: string;
  externalUrl: string;
  queued: number;
  running: number;
  status: string; // "HEALTHY" | "UNHEALTHY" | "PENDING" | "UNKNOWN"
}
```

**Go response type (`backend.go:27-33`):**
```go
type BackendResponse struct {
  ProxyBackend          // name, proxyTo, externalUrl (omitempty), active, routingGroup
  Queued  int    `json:"queued"`
  Running int    `json:"running"`
  Status  string `json:"status"`
}
```

**MISMATCH:** `ExternalURL` in Go is `externalUrl,omitempty` — if a backend has no external URL set, the field is absent from the JSON. The frontend assumes it is always present. The rebuild should ensure backends always have a non-empty `externalUrl` or handle absence gracefully.

**MISMATCH:** `Queued` and `Running` are always `0` in the current Go implementation — the monitor does not fetch these from Trino. The UI displays them. This is not a rebuild bug, just a known limitation.

- **Auth:** `RoleUser` required

### `POST /webapp/saveBackend`
- **Source:** `src/api/webapp/cluster.ts:8`
- **Request:** `ProxyBackend` — `{ name, routingGroup, proxyTo, externalUrl, active }`
- **Response data:** the saved `ProxyBackend` object
- **Go handler:** `webappSaveBackend` (calls `Backends.Upsert`)
- **Auth:** `RoleAdmin` required

### `POST /webapp/updateBackend`
- **Source:** `src/api/webapp/cluster.ts:12`
- **Request:** `ProxyBackend` (full object including name)
- **Response data:** the updated `ProxyBackend` object
- **Go handler:** `webappUpdateBackend` (calls `Backends.Upsert`)
- **Auth:** `RoleAdmin` required

### `POST /webapp/deleteBackend`
- **Source:** `src/api/webapp/cluster.ts:16`
- **Request:** `{ name: string }` — only the name is used
- **Response data:** `true` (boolean)
- **Go handler:** `webappDeleteBackend` (decodes full `ProxyBackend`, uses only `Name`)
- **Auth:** `RoleAdmin` required

### `POST /webapp/findQueryHistory`
- **Source:** `src/api/webapp/history.ts:4`
- **Request (frontend sends):**
  ```ts
  {
    page: number;
    size: number;         // ← "size"
    externalUrl?: string; // ← "externalUrl" (RoutedTo filter)
    user?: string;        // ← "user"
    queryId?: string;     // ← "queryId"
    source?: string;      // ← "source"
  }
  ```
- **Request (Go expects, `webapp.go:94-101`):**
  ```go
  type FindQueryHistoryRequest struct {
      UserName   string `json:"userName"`   // ← "userName" not "user"
      BackendURL string `json:"backendUrl"` // ← "backendUrl" not "externalUrl"
      QueryID    string `json:"queryId"`    // matches
      Source     string `json:"source"`     // matches
      Page       int    `json:"page"`       // matches
      PageSize   int    `json:"pageSize"`   // ← "pageSize" not "size"
  }
  ```
- **MISMATCH (CRITICAL):** Three field name mismatches:
  1. Frontend sends `user`, Go reads `userName` — user filter always empty on the Go side.
  2. Frontend sends `externalUrl` (the backend's external URL), Go reads `backendUrl` — backend/URL filter always empty.
  3. Frontend sends `size`, Go reads `pageSize` — page size never applied, Go uses zero value (unfiltered or default).
- **Response data:**
  ```ts
  { total: number; rows: HistoryDetail[] }
  ```
  Matches Go `TableData[QueryDetail]`:
  ```go
  type TableData[T any] struct {
      Total int64 `json:"total"`
      Rows  []T   `json:"rows"`
  }
  ```

**Frontend `HistoryDetail` type (`src/types/history.d.ts`):**
```ts
interface HistoryDetail {
  queryId: string;
  queryText: string;
  user: string;
  source: string;
  backendUrl: string;
  captureTime: number; // epoch ms
  routingGroup: string;
  externalUrl: string;
}
```

**Go `QueryDetail` (`query.go:11-20`):**
```go
type QueryDetail struct {
  QueryID      string `json:"queryId"`
  QueryText    string `json:"queryText"`
  User         string `json:"user"`
  Source       string `json:"source"`
  BackendURL   string `json:"backendUrl"`
  CaptureTime  int64  `json:"captureTime"` // epoch ms
  RoutingGroup string `json:"routingGroup"`
  ExternalURL  string `json:"externalUrl"`
}
```
- **MISMATCH:** Go's `queryDetailFromRecord` (`query.go:22-33`) does NOT populate `ExternalURL` — it is always empty string in the response. The History page uses `record.externalUrl` to build the QueryId link (`history.tsx:66`) and to display RoutedTo. The rebuild must populate `ExternalURL` from the backend mapping or store it on the query record.

- **Auth:** `RoleUser` required; non-ADMIN callers have `userName` forced server-side.

### `POST /webapp/getDistribution`
- **Source:** `src/api/webapp/dashboard.ts:4`
- **Request:** `{}` (empty body)
- **Response data:** `DistributionDetail`

**Frontend type (`src/types/dashboard.d.ts`):**
```ts
interface DistributionDetail {
  totalBackendCount: number;
  offlineBackendCount: number;
  onlineBackendCount: number;
  healthyBackendCount: number;
  unhealthyBackendCount: number;
  totalQueryCount: number;
  averageQueryCountMinute: number;
  averageQueryCountSecond: number;
  distributionChart: DistributionChartData[];
  lineChart: Record<string, LineChartData[]>;
  startTime: string; // ISO-8601
}

interface DistributionChartData {
  backendUrl: string;
  queryCount: number;
  name: string;
}

interface LineChartData {
  epochMillis: number;
  backendUrl: string;
  queryCount: number;
  name: string;
}
```

**Go response (`webapp.go:43-70`):**
```go
type DistributionResponse struct {
  TotalBackendCount     int                    `json:"totalBackendCount"`
  OnlineBackendCount    int                    `json:"onlineBackendCount"`
  OfflineBackendCount   int                    `json:"offlineBackendCount"`
  HealthyBackendCount   int                    `json:"healthyBackendCount"`
  UnhealthyBackendCount int                    `json:"unhealthyBackendCount"`
  TotalQueryCount       int64                  `json:"totalQueryCount"`
  AvgQueryCountMinute   float64                `json:"averageQueryCountMinute"`
  AvgQueryCountSecond   float64                `json:"averageQueryCountSecond"`
  StartTime             string                 `json:"startTime"`
  DistributionChart     []ChartPoint           `json:"distributionChart"`
  LineChart             map[string][]TimePoint `json:"lineChart"`
}
```
- **Note:** all field names match. `TotalQueryCount` is `int64` in Go but `number` in the frontend — fine for values within JS safe integer range.
- **Note:** current Go implementation always returns `LineChart: map[string][]TimePoint{}` (empty map, `webapp.go:222`). The line chart renders no series. The rebuild must populate line chart data from actual query history if this feature is needed.
- **Auth:** `RoleUser` required

### `GET /webapp/getRoutingRules` (via `POST` in original, but GET in router)
- **Source:** `src/api/webapp/routing-rules.ts:4` — calls `api.get('/webapp/getRoutingRules')`
- **Request:** GET, no params
- **Response data:** `RoutingRulesData[]` or HTTP 204 → `{isExternalRouting: true}`

**Frontend type (`src/types/routing-rules.d.ts`):**
```ts
interface RoutingRulesData {
  name: string;
  description: string;
  priority: number;
  actions: string[];
  condition: string;
}
```

**Go response (`webapp.go:239`):** always returns `[]RoutingRule{}` (empty array).

**MISMATCH:** The Go router registers this as `POST /webapp/getRoutingRules` (`router.go:188`) but the frontend calls `api.get(...)` (HTTP GET). This will always return 405 Method Not Allowed. Either the router must be changed to GET or the client must use POST.

- **Auth:** `RoleAdmin` required

### `POST /webapp/updateRoutingRules`
- **Source:** `src/api/webapp/routing-rules.ts:9`
- **Request:** `RoutingRulesData` (single rule object)
- **Response data:** `[]RoutingRule{}` (empty array in current implementation)
- **Go handler:** `webappUpdateRoutingRules` — currently a no-op stub.
- **Auth:** `RoleAdmin` required

### `POST /webapp/getUIConfiguration` (note: GET in source)
- Actually `GET /webapp/getUIConfiguration` — see auth endpoints above.
- Registered in the `RoleUser` group in `router.go:179`.

---

## Global property API (unused in the main UI)

The `src/api/webapp/global-property.ts` file defines five endpoints:
- `POST /webapp/findGlobalProperty`
- `POST /webapp/getGlobalProperty`
- `POST /webapp/saveGlobalProperty`
- `POST /webapp/updateGlobalProperty`
- `POST /webapp/deleteGlobalProperty`

None of these are called from any component in the current codebase. They are dead code in the original webapp and have no corresponding Go handlers in `internal/admin/router.go`. The rebuild can safely omit them initially.

---

## API Mismatch Summary

| # | Endpoint | Field | Frontend sends | Go expects | Impact |
|---|---|---|---|---|---|
| 1 | `findQueryHistory` | user filter field | `user` | `userName` | User filter never applied |
| 2 | `findQueryHistory` | backend filter field | `externalUrl` | `backendUrl` | Backend filter never applied |
| 3 | `findQueryHistory` | page size field | `size` | `pageSize` | Pagination size never applied (defaults to 0 = unfiltered) |
| 4 | `findQueryHistory` response | external URL | `externalUrl` | always `""` | QueryId links broken; RoutedTo column empty |
| 5 | `getUIConfiguration` response | page hiding | reads `disablePages` | struct has no `disablePages` | Page hiding never works |
| 6 | `getRoutingRules` | HTTP method | GET | POST registered | Always 405 |
| 7 | `getAllBackends` response | external URL | always present | `omitempty` | Missing field on backends without externalUrl |
| 8 | `getDistribution` response | line chart | expects populated data | always `{}` | Line chart always empty |

The rebuild should fix mismatches 1–4 and 6 as part of achieving functional parity.
