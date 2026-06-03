---
title: Admin REST API Surface
author: java-analyst
role: Java Analyst
component: trino-gateway
topics:
  - mgmt-api
  - auth
  - cluster-registry
  - persistence
  - routing-engine
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino-gateway: 171ce25
related-to: []
---

# Admin REST API Surface

## Summary

Trino Gateway exposes a REST API across several resource classes, serving three distinct audiences: operators managing backends via scripts/tooling (`@RolesAllowed("API")`), human administrators via the web UI (`@RolesAllowed("ADMIN")` and `@RolesAllowed("USER")`), and unauthenticated public consumers (`/api/public/*`). The Go rewrite must replicate all of these paths exactly — scripts and the existing web UI rely on them. There is no machine-generated OpenAPI/Swagger spec in the current codebase.

## Key Findings

- All admin endpoints live on the same HTTP port as the proxy (the gateway serves a single port for both Trino traffic and management). Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/config/HaGatewayConfiguration.java:28-43`.
- There is no OpenAPI/Swagger annotation or generation step anywhere in the source. The spec below is derived entirely from JAX-RS annotations.
- Three authentication modes exist (OIDC/OAuth2, form/LDAP, and no-auth). Which mode is active is controlled by whether `authentication:` is set in the YAML config. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java:92-97`.
- The `@RolesAllowed` check is only applied to endpoints that carry that annotation — endpoints with no annotation (e.g., `/api/public/*`, login endpoints) are completely open. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/ResourceSecurityDynamicFeature.java:40-46`.
- The `Result<T>` envelope (`code`, `msg`, `data`) is used by `GatewayWebAppResource` and `LoginResource` but NOT by `GatewayResource`, `HaGatewayResource`, `EntityEditorResource`, or `PublicResource`, which return bare JSON objects or plain strings. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/Result.java`.

---

## Summary Table

| Method | Path | Roles | Purpose |
|--------|------|-------|---------|
| GET | `/api/public/backends` | none | List all registered backends (public, no auth) |
| GET | `/api/public/backends/{name}` | none | Get a single backend by name (public, no auth) |
| GET | `/api/public/backends/{name}/state` | none | Get live cluster stats for a named backend (public, no auth) |
| GET | `/gateway` | API | Health ping — returns the string `"ok"` |
| GET | `/gateway/backend/all` | API | List all backends |
| GET | `/gateway/backend/active` | API | List active (enabled) backends |
| POST | `/gateway/backend/deactivate/{name}` | API | Mark a backend as inactive |
| POST | `/gateway/backend/activate/{name}` | API | Mark a backend as active |
| POST | `/gateway/backend/modify/add` | API | Register a new backend |
| POST | `/gateway/backend/modify/update` | API | Update an existing backend |
| POST | `/gateway/backend/modify/delete` | API | Delete a backend by name |
| GET | `/entity` | ADMIN | List all entity types (currently only `GATEWAY_BACKEND`) |
| POST | `/entity?entityType=GATEWAY_BACKEND` | ADMIN | Upsert a backend (also updates in-memory routing state) |
| GET | `/entity/{entityType}` | ADMIN | List all entities of the given type |
| GET | `/trino-gateway/livez` | none | Liveness probe — always returns `"ok"` |
| GET | `/trino-gateway/readyz` | none | Readiness probe — 503 until cluster monitor initialises |
| GET | `/trino-gateway` | none | Serve the admin web UI index.html |
| GET | `/trino-gateway/api/queryHistory` | USER | Fetch query history (scoped to caller unless ADMIN) |
| GET | `/trino-gateway/api/activeBackends` | USER | List active backends (legacy web-UI endpoint) |
| GET | `/trino-gateway/api/queryHistoryDistribution` | USER | Query-count-per-backend distribution map |
| POST | `/webapp/getAllBackends` | USER | List all backends with live cluster stats |
| POST | `/webapp/findQueryHistory` | USER | Paginated query history search |
| POST | `/webapp/getDistribution` | USER | Distribution and line-chart stats for the dashboard |
| POST | `/webapp/saveBackend` | ADMIN | Add a new backend (webapp flow) |
| POST | `/webapp/updateBackend` | ADMIN | Update a backend (webapp flow) |
| POST | `/webapp/deleteBackend` | ADMIN | Delete a backend (webapp flow) |
| GET | `/webapp/getRoutingRules` | ADMIN | List routing rules (errors if external rules type) |
| POST | `/webapp/updateRoutingRules` | ADMIN | Add or update a routing rule |
| GET | `/webapp/getUIConfiguration` | USER | Return UI config (disabled pages list) |
| GET | `/` | none | Redirect to `/trino-gateway` |
| POST | `/sso` | none | Initiate OAuth2 OIDC authorization code flow |
| GET | `/oidc/callback` | none | OIDC authorization code callback |
| POST | `/login` | none | Form-based login — returns JWT token |
| POST | `/logout` | none | Logout — always returns success |
| POST | `/userinfo` | USER | Return current user's roles and permissions |
| POST | `/loginType` | none | Return active auth type (`"form"`, `"oauth"`, `"none"`) |
| GET | `/trino-gateway/logo.svg` | none | Serve the gateway logo SVG |
| GET | `/trino-gateway/assets/{path}` | none | Serve static web UI assets |

---

## Authentication and Authorization

### Authentication mechanism

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/util/ChainedAuthFilter.java:47-97` and `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java:92-97`.

The gateway uses a **chained filter** that is only applied to endpoints annotated with `@RolesAllowed`. Endpoints without that annotation are completely unprotected.

The chained filter tries each configured sub-filter in order and accepts the first one that succeeds:

1. **OIDC/OAuth2 Bearer token** — if `authentication.oauth` is configured. Reads the JWT from the `token` cookie or `Authorization: Bearer <jwt>` header. The JWT is validated against the OIDC issuer's JWK endpoint.
2. **Form/LDAP Bearer token** — if `authentication.form` is configured. Same cookie/header lookup; the JWT is self-signed using the configured RSA key pair.
3. **HTTP Basic Auth** — if `authentication.form` is configured. Reads `Authorization: Basic <base64>`. Credentials are validated against the preset-users config or LDAP.

When `authentication:` is absent from config, `NoopFilter` is injected instead. It grants every caller `ADMIN`, `USER`, and `API` roles with no credential check. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/NoopFilter.java:38-52`.

Token cookie name: `token` (set as a session cookie valid for 86400 seconds, secure=true, path=`/`). Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/SessionCookie.java:26-33`.

### Authorization roles

Three roles are used in `@RolesAllowed`. Each maps to a regex pattern in the `authorization:` YAML block, matched against the principal's `memberOf` attribute (from LDAP or the JWT `privilegesField`). Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/LbAuthorizer.java:39-46`.

| Role | Description |
|------|-------------|
| `ADMIN` | Full administrative access; also granted all USER capabilities |
| `USER` | Read-only web UI access and query history |
| `API` | Script/tooling access for backend management (Gateway API) |

When `authorization:` is absent from config, `NoopAuthorizer` grants every role unconditionally. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/NoopAuthorizer.java`.

### Relationship to proxy auth

The admin API shares the same port and filter chain as the Trino proxy paths. The proxy paths (under `/v1/statement`, `/v1/query`, `/ui`, `/v1/info`, `/v1/node`, `/ui/api/stats`, `/oauth2`, and operator-configured extra paths) are forwarded directly to Trino backends and bypass the `@RolesAllowed` filter. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/PathFilter.java`.

---

## Data Models

### `ProxyBackendConfiguration` (backend object)

Used as the body for add/update/delete endpoints and returned by list endpoints.

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/config/ProxyBackendConfiguration.java`.

```
{
  "name":         string,          // unique backend identifier; required
  "proxyTo":      string,          // internal URL the gateway proxies to (e.g. "http://trino-1:8080")
  "externalUrl":  string | null,   // URL shown in UIs; falls back to proxyTo if null
  "active":       bool,            // default true; false means backend is disabled
  "routingGroup": string           // default "adhoc"; group for routing-rule matching
}
```

### `BackendResponse` (extended backend with cluster state)

Returned by `POST /webapp/getAllBackends`. Extends `ProxyBackendConfiguration` with live stats.

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/response/BackendResponse.java`.

```
{
  // all ProxyBackendConfiguration fields, plus:
  "queued":  int,     // queued query count from cluster monitor
  "running": int,     // running query count from cluster monitor
  "status":  string   // TrinoStatus enum stringified: "HEALTHY", "UNHEALTHY", "PENDING"
}
```

### `QueryDetail` (query history record)

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/QueryHistoryManager.java:43-154`.

```
{
  "queryId":      string,   // Trino query ID
  "queryText":    string,   // SQL text
  "user":         string,   // Trino user
  "source":       string,   // Trino source tag
  "backendUrl":   string,   // internal proxyTo URL of the backend used
  "captureTime":  long,     // epoch milliseconds when query was recorded
  "routingGroup": string,   // routing group used
  "externalUrl":  string    // externalUrl of the backend used
}
```

Ordering: results are sorted descending by `captureTime`.

### `TableData<T>` (paginated result)

Used by `POST /webapp/findQueryHistory`.

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/TableData.java`.

```
{
  "total": long,       // total matching records (before pagination)
  "rows":  array<T>    // page of records
}
```

### `QueryHistoryRequest` (findQueryHistory body)

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/request/QueryHistoryRequest.java`.

```
{
  "page":        int | null,    // 1-based page index; default 1
  "size":        int | null,    // page size; default 10
  "user":        string | null, // filter by user; ADMIN may pass any value; non-ADMIN has this forced to their own identity
  "externalUrl": string | null, // filter by backend externalUrl
  "queryId":     string | null, // filter by Trino query ID
  "source":      string | null  // filter by Trino source tag
}
```

### `QueryDistributionRequest` (getDistribution body)

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/request/QueryDistributionRequest.java`.

```
{
  "latestHour": int | null   // look-back window in hours; default 1
}
```

### `DistributionResponse` (getDistribution response data field)

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/response/DistributionResponse.java`.

```
{
  "totalBackendCount":       int,
  "onlineBackendCount":      int,      // backends with active=true
  "offlineBackendCount":     int,      // backends with active=false
  "healthyBackendCount":     int,      // backends with TrinoStatus=HEALTHY
  "unhealthyBackendCount":   int,      // backends NOT HEALTHY (includes PENDING)
  "totalQueryCount":         long,     // total queries in the look-back window
  "averageQueryCountMinute": float,    // totalQueryCount / (latestHour * 60)
  "averageQueryCountSecond": float,    // totalQueryCount / (latestHour * 3600)
  "startTime":               string,   // ISO-8601 with ms: "2024-01-02T03:04:05.678+00:00" (gateway process start time, UTC)
  "distributionChart": [
    {
      "backendUrl":  string,
      "name":        string,
      "queryCount":  long
    }
  ],
  "lineChart": {
    "<backendName>": [
      {
        "epochMillis": long,
        "backendUrl":  string,
        "name":        string,
        "queryCount":  long
      }
    ]
  }
}
```

### `RoutingRule` (routing rule object)

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/RoutingRule.java`.

```
{
  "name":        string,         // unique rule name; required
  "description": string,         // human description; defaults to "" if null
  "priority":    int,            // higher = evaluated first; defaults to 0 if null; ties are unordered
  "actions":     array<string>,  // list of routing action strings (e.g. routing group assignments)
  "condition":   string          // rule condition expression; required
}
```

### `Result<T>` envelope

Used by `GatewayWebAppResource` and `LoginResource` responses. The HTTP status code is always 200; application-level success/failure is indicated by `code`.

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/Result.java`.

```
{
  "code": int,     // 200 = success, 500 = failure
  "msg":  string,  // human message; "Successful." on success
  "data": T | null // payload; null on void success or failure
}
```

### `RestLoginRequest` (login body)

Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/domain/request/RestLoginRequest.java`.

```
{
  "username": string,
  "password": string
}
```

---

## Resource Groups

### Group 1: Public Backends (`/api/public`)

No authentication required. No `@RolesAllowed` annotation. Source class: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/PublicResource.java`.

---

#### `GET /api/public/backends`

**Purpose:** List all registered backend clusters regardless of active status. Intended for monitoring scripts and external tooling that must not need credentials.

**Request:** No body, no query params.

**Response:**
- `200 OK` — `Content-Type: application/json`
- Body: JSON array of `ProxyBackendConfiguration` objects.

**Roles:** none (unauthenticated).

---

#### `GET /api/public/backends/{name}`

**Purpose:** Retrieve a single backend by its unique name.

**Path params:**
- `name` (string) — backend name

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — `ProxyBackendConfiguration` object
- `404 Not Found` — empty body when no backend with that name exists

**Roles:** none.

---

#### `GET /api/public/backends/{name}/state`

**Purpose:** Return live cluster stats (query counts, health status) for the named backend.

**Path params:**
- `name` (string) — backend name

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — a `ClusterStats` object. Fields include at minimum:
  - `trinoStatus`: string (`"HEALTHY"`, `"UNHEALTHY"`, `"PENDING"`)
  - `queuedQueryCount`: int
  - `runningQueryCount`: int
- `404 Not Found` — when no backend with that name exists

**Roles:** none.

---

### Group 2: Gateway Backend Management (`/gateway`, `/gateway/backend`, `/gateway/backend/modify`)

Used by scripts and tooling. Role: `API`. Sources:
- `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayResource.java`
- `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/HaGatewayResource.java`

---

#### `GET /gateway`

**Purpose:** Liveness ping for the gateway admin API; used to confirm the endpoint is reachable and the caller has valid API credentials.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — body is the plain string `"ok"` (JSON string).

**Roles:** `API`.

---

#### `GET /gateway/backend/all`

**Purpose:** List all backend clusters regardless of active status.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — JSON array of `ProxyBackendConfiguration`.

**Roles:** `API`.

---

#### `GET /gateway/backend/active`

**Purpose:** List only backends where `active=true`.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — JSON array of `ProxyBackendConfiguration` (only active entries).

**Roles:** `API`.

---

#### `POST /gateway/backend/deactivate/{name}`

**Purpose:** Set a backend's `active` flag to `false`, stopping the gateway from routing new queries to it.

**Path params:**
- `name` (string) — backend name

**Request:** No body.

**Response:**
- `200 OK` — empty body on success
- `404 Not Found` — `Content-Type: text/plain` — error message string when no backend found

**Roles:** `API`.

---

#### `POST /gateway/backend/activate/{name}`

**Purpose:** Set a backend's `active` flag to `true`. The backend is first marked `PENDING` (not immediately healthy) until the cluster monitor verifies it.

**Path params:**
- `name` (string) — backend name

**Request:** No body.

**Response:**
- `200 OK` — empty body on success
- `404 Not Found` — `Content-Type: text/plain` — error message string when no backend found

**Roles:** `API`.

---

#### `POST /gateway/backend/modify/add`

**Purpose:** Register a new backend cluster. Returns the saved configuration (with any defaults filled in).

**Request:**
- `Content-Type: application/json`
- Body: `ProxyBackendConfiguration` object. Required fields: `name`, `proxyTo`. Optional: `externalUrl`, `active` (default `true`), `routingGroup` (default `"adhoc"`).

**Response:**
- `200 OK` — `Content-Type: application/json` — `ProxyBackendConfiguration` as persisted (with defaults applied).

**Roles:** `API`.

---

#### `POST /gateway/backend/modify/update`

**Purpose:** Update an existing backend's configuration. The backend is identified by `name`.

**Request:**
- `Content-Type: application/json`
- Body: `ProxyBackendConfiguration` object. `name` must match an existing backend.

**Response:**
- `200 OK` — `Content-Type: application/json` — `ProxyBackendConfiguration` as updated.

**Roles:** `API`.

---

#### `POST /gateway/backend/modify/delete`

**Purpose:** Permanently remove a backend from the registry by name.

**Request:**
- `Content-Type: application/json`
- Body: plain string — the backend name (not a JSON object; raw string body).

**Response:**
- `200 OK` — empty body.

**Roles:** `API`.

**Important:** The body is a raw string (the name), not a JSON object. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/HaGatewayResource.java:58-63`.

---

### Group 3: Entity Editor (`/entity`)

CRUD interface used by the legacy admin UI. Role: `ADMIN` (class-level). Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/EntityEditorResource.java`.

Currently only one entity type exists: `GATEWAY_BACKEND`.

---

#### `GET /entity`

**Purpose:** Return the list of known entity types.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — JSON array of entity type strings, currently `["GATEWAY_BACKEND"]`.

**Roles:** `ADMIN`.

---

#### `POST /entity?entityType=GATEWAY_BACKEND`

**Purpose:** Upsert a backend. On success, the in-memory routing table is updated immediately: if `active=true`, the backend is set to `PENDING` (awaiting health check); if `active=false`, it is set to `UNHEALTHY`.

**Query params:**
- `entityType` (string, required) — currently only `"GATEWAY_BACKEND"` is valid; any other value throws 500.
- `useSchema` (string, optional) — reserved, currently unused.

**Request:**
- `Content-Type: application/json`
- Body: `ProxyBackendConfiguration` JSON object.

**Response:**
- `200 OK` — empty body on success.
- `500 Internal Server Error` — if `entityType` is missing or unknown.

**Roles:** `ADMIN`.

---

#### `GET /entity/{entityType}`

**Purpose:** List all entities of the given type.

**Path params:**
- `entityType` (string) — must be `"GATEWAY_BACKEND"`.

**Query params:**
- `useSchema` (string, optional) — reserved, currently unused.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json`
  - For `GATEWAY_BACKEND`: JSON array of `ProxyBackendConfiguration`.

**Roles:** `ADMIN`.

---

### Group 4: Health Probes (`/trino-gateway/livez`, `/trino-gateway/readyz`)

No auth. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayHealthCheckResource.java`.

---

#### `GET /trino-gateway/livez`

**Purpose:** Kubernetes/load-balancer liveness probe. Always returns 200 as long as the JVM is running.

**Request:** No body.

**Response:**
- `200 OK` — body: plain string `"ok"`.

**Roles:** none.

---

#### `GET /trino-gateway/readyz`

**Purpose:** Kubernetes readiness probe. Returns 503 until `ActiveClusterMonitor` completes its first health-check pass.

**Request:** No body.

**Response:**
- `200 OK` — body: plain string `"ok"` — when initialized.
- `503 Service Unavailable` — body: `"Trino Gateway is still initializing"` — during startup.

**Roles:** none.

---

### Group 5: Legacy Web UI JSON API (`/trino-gateway/api`)

These endpoints back the older web UI. Role: `USER` (method-level). Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayViewResource.java`.

**ADMIN-scoped behaviour:** When the caller holds the `ADMIN` role, query history is returned unfiltered. When the caller only holds `USER`, results are restricted to queries by that user.

---

#### `GET /trino-gateway/api/queryHistory`

**Purpose:** Fetch recent query history. Non-ADMIN callers only see their own queries.

**Request:** No body, no query params.

**Response:**
- `200 OK` — `Content-Type: application/json` — JSON array of `QueryDetail`, sorted descending by `captureTime`.

**Roles:** `USER`.

---

#### `GET /trino-gateway/api/activeBackends`

**Purpose:** Return the list of active (enabled) backends. Equivalent to `GET /gateway/backend/active` but on a different path for the legacy UI.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — JSON array of `ProxyBackendConfiguration` (active only).

**Roles:** `USER`.

---

#### `GET /trino-gateway/api/queryHistoryDistribution`

**Purpose:** Return a map of backend-name to query count, computed across the full query history visible to the caller.

**Request:** No body.

**Response:**
- `200 OK` — `Content-Type: application/json` — JSON object: `{ "<backendName>": <queryCount int> }`. If a backend has been deleted since the query was recorded, the raw `backendUrl` is used as the key.

**Roles:** `USER`.

---

### Group 6: Modern Web App API (`/webapp`)

Backs the current Vue.js admin web application. Uses the `Result<T>` envelope for all responses. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayWebAppResource.java`.

All endpoints: `Content-Type: application/json` for both request and response.

---

#### `POST /webapp/getAllBackends`

**Purpose:** Return all backends enriched with live cluster stats (query counts and health status).

**Request:** No body (despite being POST).

**Response:**
- `200 OK` — `Result<array<BackendResponse>>`

**Roles:** `USER`.

---

#### `POST /webapp/findQueryHistory`

**Purpose:** Paginated, filtered query history search. For non-ADMIN callers the `user` filter is silently overridden to the caller's identity.

**Request:**
- Body: `QueryHistoryRequest` JSON object.

**Response:**
- `200 OK` — `Result<TableData<QueryDetail>>`

**Roles:** `USER`.

---

#### `POST /webapp/getDistribution`

**Purpose:** Aggregate query distribution statistics and time-series data for the dashboard.

**Request:**
- Body: `QueryDistributionRequest` JSON object.

**Response:**
- `200 OK` — `Result<DistributionResponse>`

**Roles:** `USER`.

---

#### `POST /webapp/saveBackend`

**Purpose:** Add a new backend (web UI flow).

**Request:**
- Body: `ProxyBackendConfiguration` JSON object.

**Response:**
- `200 OK` — `Result<ProxyBackendConfiguration>` — the persisted configuration.

**Roles:** `ADMIN`.

---

#### `POST /webapp/updateBackend`

**Purpose:** Update an existing backend (web UI flow).

**Request:**
- Body: `ProxyBackendConfiguration` JSON object.

**Response:**
- `200 OK` — `Result<ProxyBackendConfiguration>` — the updated configuration.

**Roles:** `ADMIN`.

---

#### `POST /webapp/deleteBackend`

**Purpose:** Delete a backend by name (web UI flow).

**Request:**
- Body: `ProxyBackendConfiguration` JSON object. Only `name` is used.

**Response:**
- `200 OK` — `Result<bool>` — `data: true` on success.

**Roles:** `ADMIN`.

---

#### `GET /webapp/getRoutingRules`

**Purpose:** Return the list of routing rules. If the rules engine is enabled and the rules type is `EXTERNAL` (meaning rules are managed by an external service), the endpoint returns 204 with an error message.

**Request:** No body.

**Response:**
- `200 OK` — `Result<array<RoutingRule>>`
- `204 No Content` — `Result<null>` with `code: 500` and `msg: "Routing rules are managed by an external service"` when `routingRules.rulesType = EXTERNAL`.

**Roles:** `ADMIN`.

---

#### `POST /webapp/updateRoutingRules`

**Purpose:** Add or update a single routing rule. Returns the complete updated rule list.

**Request:**
- Body: `RoutingRule` JSON object.

**Response:**
- `200 OK` — `Result<array<RoutingRule>>` — the full updated list after applying the change.

**Roles:** `ADMIN`.

---

#### `GET /webapp/getUIConfiguration`

**Purpose:** Return the gateway's UI configuration (list of pages that should be hidden in the web UI).

**Request:** No body.

**Response:**
- `200 OK` — `Result<UIConfiguration>` where `UIConfiguration` is:
  ```
  {
    "disablePages": array<string> | null
  }
  ```

**Roles:** `USER`.

---

### Group 7: Authentication and Session (`/`, `/sso`, `/oidc/callback`, `/login`, `/logout`, `/userinfo`, `/loginType`)

No `@RolesAllowed` except `/userinfo` which requires `USER`. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/LoginResource.java`.

---

#### `GET /`

**Purpose:** Redirect the browser to the web UI.

**Request:** No body.

**Response:**
- `303 See Other` — `Location: /trino-gateway`

**Roles:** none.

---

#### `POST /sso`

**Purpose:** Initiate the OIDC authorization code flow by redirecting the browser to the identity provider. Returns 500 if OAuth is not configured.

**Request:**
- `Content-Type: application/json`
- No body required.

**Response:**
- `302 Found` (or `303`) — redirect to the IDP authorization endpoint with `state` and `nonce` encoded into an `oidc-state` cookie.
- `500 Internal Server Error` — if `authentication.oauth` is not configured.

**Roles:** none.

---

#### `GET /oidc/callback`

**Purpose:** OIDC authorization code callback. Exchanges the code for tokens, validates the nonce from the OIDC cookie, issues a session cookie (`token`), and redirects the browser to the web UI.

**Query params:**
- `code` (string) — authorization code from the IDP
- `state` (string) — state value to be verified against the OIDC cookie

**Cookie params:**
- `oidc-state` — set during `/sso`; contains state and nonce

**Response:**
- `302 Found` — on success, sets `Set-Cookie: token=<jwt>; Path=/; Secure` and redirects to `/`.
- `401 Unauthorized` — if the cookie is missing, the state does not match, or the nonce is missing.
- `500 Internal Server Error` — if OAuth is not configured.

**Roles:** none.

---

#### `POST /login`

**Purpose:** Form/LDAP login. Returns a JWT that the client stores and sends as `Authorization: Bearer <jwt>` or in the `token` cookie on subsequent requests.

**Request:**
- `Content-Type: application/json`
- Body: `RestLoginRequest`

**Response:**
- `200 OK` — `Result<{ "token": string }>` on success. The `token` value is a JWT.
- `200 OK` — `Result<{ "token": "<username>" }>` — special case: if neither form auth nor OAuth is configured (`NoopFilter` mode), the username itself is returned as the token.
- `500 Internal Server Error` — if form auth is not configured but OAuth is (wrong endpoint for OAuth mode).

**Roles:** none.

---

#### `POST /logout`

**Purpose:** Clears the session. Currently always returns success; the actual cookie clearing is expected to be handled client-side or by `oauth2GatewayCookieConfiguration.deletePaths`.

**Request:**
- `Content-Type: application/json`
- No body required.

**Response:**
- `200 OK` — `Result<null>` with `code: 200, msg: "Successful.", data: null`.

**Roles:** none.

---

#### `POST /userinfo`

**Purpose:** Return the authenticated user's identity, roles, and page permissions.

**Request:**
- `Content-Type: application/json`
- No body required (user is identified from the `Authorization` header or `token` cookie).

**Response:**
- `200 OK` — `Result<{ "userId": string, "userName": string, "roles": array<string>, "permissions": array<string> }>`
  - `roles` contains zero or more of: `"ADMIN"`, `"USER"`, `"API"`
  - `permissions` is a list of page identifiers the caller is allowed to access (from the `pagePermissions` config map)

**Roles:** `USER`.

---

#### `POST /loginType`

**Purpose:** Return the active authentication type so the frontend knows which login UI to show.

**Request:**
- `Content-Type: application/json`
- No body.

**Response:**
- `200 OK` — `Result<string>` where `data` is one of:
  - `"form"` — form/LDAP auth is active
  - `"oauth"` — OIDC/OAuth2 is active
  - `"none"` — no auth configured

**Roles:** none.

---

### Group 8: Static Web UI Assets (`/trino-gateway/logo.svg`, `/trino-gateway/assets/*`)

No auth. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/baseapp/WebUIStaticResource.java`.

---

#### `GET /trino-gateway/logo.svg`

**Purpose:** Serve the gateway logo from the classpath at `/static/logo.svg`.

**Response:**
- `200 OK` — SVG content
- `404 Not Found` — if the resource is not in the classpath

**Roles:** none.

---

#### `GET /trino-gateway/assets/{path}`

**Purpose:** Serve bundled static web UI assets (JavaScript, CSS, fonts, etc.) from the classpath at `/static/assets/<path>`.

**Path params:**
- `path` (string) — path within the assets directory; path traversal is blocked (non-canonical paths return 404).

**Response:**
- `200 OK` — file contents with MIME type inferred from extension
- `404 Not Found` — if path is non-canonical (potential traversal) or resource not found

**Roles:** none.

---

#### `GET /trino-gateway`

**Purpose:** Serve the SPA index.html.

**Response:**
- `200 OK` — `Content-Type: text/html` — contents of `/static/index.html` from classpath.

**Roles:** none.

---

## OpenAPI / Swagger

No OpenAPI or Swagger spec is generated or shipped. There are no `@OpenAPIDefinition`, `@Operation`, or Swagger annotations anywhere in the source tree. The Go implementer must treat this document as the authoritative spec.

---

## Behavior vs. Implementation Artifact

### `POST /entity?entityType=GATEWAY_BACKEND` updates in-memory routing state immediately

- **Observed behavior:** When a backend is upserted via `EntityEditorResource.updateEntity()`, the routing manager's in-memory health map is updated synchronously: active backends become `PENDING` (not `HEALTHY`), inactive backends become `UNHEALTHY`. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/EntityEditorResource.java:91-100`.
- **Source of behavior:** `ops-affordance` — prevents a newly activated backend from receiving traffic before the health check confirms it is reachable.
- **Rationale:** Avoiding black-hole routing to a backend that is registered but not yet confirmed healthy.
- **Go obligation:** `replicate-exactly` — omitting this would route queries to potentially-down backends immediately upon activation.
- **Notes:** The `/gateway/backend/activate/{name}` endpoint in `GatewayResource` does NOT do this update — it only persists the flag. Only the entity editor path performs the in-memory transition. The `/webapp/saveBackend` and `/webapp/updateBackend` paths go through `gatewayBackendManager.addBackend/updateBackend`, which may or may not update in-memory state depending on the `HaGatewayManager` implementation.

### Non-ADMIN callers on `/webapp/findQueryHistory` have `user` filter forced

- **Observed behavior:** If the caller does not have the `ADMIN` role, the `user` field in `QueryHistoryRequest` is silently replaced with the caller's authenticated username. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayWebAppResource.java:124-136`.
- **Source of behavior:** `ops-affordance` — users must not be able to read other users' query history.
- **Go obligation:** `replicate-exactly`.

### Same applies to `GET /trino-gateway/api/queryHistory` and `GET /trino-gateway/api/queryHistoryDistribution`

- **Observed behavior:** `GatewayViewResource.getUserNameForQueryHistory()` returns the caller's username for non-ADMIN callers, which is then passed as a filter to the query history manager. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayViewResource.java:50-58`.
- **Go obligation:** `replicate-exactly`.

### `POST /webapp/deleteBackend` takes a full `ProxyBackendConfiguration` but only uses `name`

- **Observed behavior:** The endpoint accepts the full backend object but calls `deleteBackend(backend.getName())`. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayWebAppResource.java:214-218`.
- **Source of behavior:** `defensive-historical` — likely copy-paste from save/update.
- **Go obligation:** `replicate-intent` — accept the full object for compatibility, use only `name`.

### `GET /webapp/getRoutingRules` returns 204 (not 200) for the external-rules-engine case

- **Observed behavior:** When `routingRules.rulesEngineEnabled=true` and `routingRules.rulesType=EXTERNAL`, the endpoint returns `Response.Status.NO_CONTENT` (204) with a `Result` body containing `code: 500`. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/GatewayWebAppResource.java:229-232`.
- **Source of behavior:** `gateway-design-intent` — the UI uses this to know it should not show the rules editor.
- **Go obligation:** `replicate-exactly` — the web UI likely checks for 204 to decide whether to render the routing rules editor.

### `POST /gateway/backend/modify/delete` body is a raw string, not JSON

- **Observed behavior:** The method signature is `removeBackend(String name)` with no `@Consumes` annotation. JAX-RS passes the raw request body as the `String name` parameter. Source: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/resource/HaGatewayResource.java:58-63`.
- **Source of behavior:** `defensive-historical` — inconsistent with add/update.
- **Go obligation:** `replicate-exactly` — existing scripts send a raw string body.

---

## Implications for Go Rewrite

- The Go implementation must serve all listed paths on the same listener as the Trino proxy (single port). Path-based dispatch must distinguish admin paths from forwarded proxy paths.
- No Go framework imposes `@RolesAllowed` semantics; the auth middleware must be applied per-route (or per-group) matching the Java class/method-level annotation pattern.
- The `Result<T>` envelope is only used by the `/webapp/*` and `/login*` groups. The `/gateway/*`, `/entity/*`, and `/api/public/*` groups return bare JSON. The Go implementer must not add the `Result<T>` wrapper to groups that do not use it.
- There are effectively three overlapping backend-management APIs: `/entity`, `/gateway/backend/modify`, and `/webapp`. All three must be replicated. Operators may use any of them.
- The OIDC callback uses a cookie (`oidc-state`) to pass state and nonce between the redirect and callback. The Go implementation needs cookie-based CSRF state for the OAuth2 flow.
- The `extraWhitelistPaths` config option accepts regex patterns. The path filter applies these to decide whether to forward to a Trino backend or handle as an admin request. This configuration must be honoured by the Go router.
- There is no database migration or schema management in these REST resources — persistence is via JDBI to a relational DB. The Go implementer will need to replicate the schema separately.

## Test Strategy Hooks

- **Test level:** integration — all routes require an HTTP listener with auth middleware and a backing store.
- **Fixtures required:** configured database (PostgreSQL), preset-users config for form auth, at least one registered backend.
- **Observable signals:** HTTP status codes, response body shape (envelope vs. bare), `Set-Cookie` headers on `/oidc/callback`, readiness probe behaviour during startup.
- **Non-determinism risks:** `readyz` timing during startup; `getDistribution` line-chart grouping depends on epoch-millisecond bucketing; `startTime` in `DistributionResponse` is set at process start.

See paired QA study if one is created.

## Open Questions

- What does `ClusterStats` serialize to (exact field names)? `GET /api/public/backends/{name}/state` returns it directly. `@architect` or `@java-qa` should enumerate the serialized fields from `ClusterStats.java`.
- The `pagePermissions` config map controls the `permissions` field in `/userinfo`. The mapping logic is in `LbFormAuthManager.processPagePermissions()` and `LbOAuthManager.processPagePermissions()`. This logic should be studied separately if the Go implementation needs to replicate the permission strings exactly. `@java-analyst` follow-up.
- `POST /entity?useSchema=...` — the `useSchema` query param is declared but the comment says "TODO: make the gateway backend database sensitive". No database schema selection happens today. @architect to decide if this must be preserved.
- `/webapp/updateRoutingRules` description says "add or update" — whether it upserts by name or always appends depends on `RoutingRulesManager`. `@java-analyst` to trace the manager implementation if the Go implementer needs exact semantics.

## Cross-references

- `[[routing-engine.md]]` — for how routing rules are evaluated (separate topic from this management API)
- `[[cluster-registry.md]]` — for the data model of cluster health state (`ClusterStats`)
- `[[auth-handoff.md]]` — for how gateway auth interacts with Trino's own auth
