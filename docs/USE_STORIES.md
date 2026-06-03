# trino-goway User Stories

trino-goway is a Go reverse proxy and load balancer that sits in front of one or more Trino
clusters. It is a behaviour-compatible rewrite of the Java `trino-gateway`: clients keep
talking the Trino HTTP protocol unchanged, and the gateway pins multi-statement queries to a
specific backend, surfaces an admin/management API, monitors backend health, and provides a
differential test harness so the Go gateway can be proven equivalent to the Java one.

This document captures the capabilities the gateway must deliver. Every acceptance criterion
maps to behaviour that already exists in the codebase; the list is meant to be read by
operators, integrators, and reviewers — not as an implementation spec.

Roles used in this document:

- **Trino user** — anyone (human or BI tool) submitting Trino queries through the gateway.
- **Platform engineer** — owns the Trino fleet; defines clusters, routing groups, and routing
  policy.
- **Gateway operator** — runs the gateway in production; cares about config, health, on-call,
  rollout.
- **CI pipeline** — automated jobs that verify Go-vs-Java equivalence and produce release
  artifacts.

---

## 1. Trino-protocol proxying

### 1.1 Submit a query through the gateway

*As a Trino user, I want to POST `/v1/statement` to the gateway and have my query routed to a
healthy Trino cluster so that I do not need to know which cluster I am hitting.*

**Acceptance Criteria**

- `POST /v1/statement` is accepted on the configured `proxy.port` (default `8080`).
- The request body is forwarded to the selected backend verbatim.
- The gateway resolves a backend before forwarding; if no backend can be selected the
  client receives `502 Bad Gateway` with body `no backend available`.
- The response from the backend is returned to the client with the original status code
  and headers (minus hop-by-hop headers).
- The gateway buffers the `/v1/statement` response so the `queryId` can be extracted before
  the body is flushed; the cap is `proxy.responseSize` (default `1 MiB`). An oversized
  response returns `502 Bad Gateway` with body `upstream response too large`.

### 1.2 Follow-up polls reach the same backend

*As a Trino user, I want subsequent `nextUri` polls (`GET /v1/query/<id>/...`) to land on
the same backend that started my query so that I can retrieve all of my rows.*

**Acceptance Criteria**

- After `POST /v1/statement`, the gateway extracts the JSON `id` field and caches `queryId →
  backendURL` **before** writing the response body to the client.
- On any later request whose path is `/v1/query/<queryId>` (with or without a trailing
  segment), the gateway routes to the cached backend if the entry exists.
- `/v1/statement/<queued|executing>/...` polls do NOT consult the cache; they are routed by
  the external router (or the default group) on each call. Backend affinity for those polls
  is only reliable when the external router is deterministic for the same `queryId`.
- The cache is a bounded in-process LRU (4096 entries) per gateway instance.
- Non-`/v1/statement` paths are streamed: the request and response bodies are piped
  byte-for-byte without buffering.

### 1.3 Cancel a running query

*As a Trino user, I want to issue `KILL QUERY '<queryId>'` and have it routed to the backend
that holds the query so that the cancellation actually takes effect.*

**Acceptance Criteria**

- A `POST` body matching the regex `(?i)KILL\s+QUERY\s+'(<queryId>)'` is detected before the
  external router is consulted.
- The extracted query ID is looked up in the query-history store; on a hit, the request is
  routed to that backend.
- The lookup is coalesced via singleflight so a flurry of duplicate kill requests does not
  fan out to the database.

### 1.4 Resume after a cache miss

*As a Trino user, I want to keep getting answers for my query even if the gateway has
restarted (and lost its in-memory query cache) so that long-running queries survive routine
gateway operations.*

**Acceptance Criteria**

- On any `/v1/query/<id>` request that misses the cache and cannot be answered by the
  external router, the gateway runs a 3-step recovery chain:
  1. Look up the queryID in the query-history DB.
  2. Concurrently issue `HEAD /v1/query/<id>` to every active backend (3-second deadline);
     the first one to return `200` wins.
  3. Fall back to the first active backend regardless of group.
- Recovery never returns the user a `404` for a missed cache lookup if at least one active
  backend exists.

### 1.5 Sticky OAuth2 routing

*As a Trino user authenticating against my cluster, I want my OAuth2 flow to stay pinned to
the cluster that issued my session so that the back-and-forth redirects do not bounce
between clusters.*

**Acceptance Criteria**

- Cookie issuance and validation are gated on `cookie.secret` being non-empty; with an
  empty secret the gateway never emits, validates, or rejects a `TG.OAUTH2` cookie.
- The first request that hits a path under `/oauth2` and does NOT carry a `TG.OAUTH2`
  cookie causes the gateway to emit a `Set-Cookie: TG.OAUTH2=...` that pins the chosen
  backend.
- The cookie is JSON-encoded, HMAC-SHA256 signed with `cookie.secret`, base64 (URL-safe)
  encoded, marked `HttpOnly`, `SameSite=Lax`, `Path=/`, and `Max-Age = cookie.ttl` (default
  `10m`).
- When a valid cookie is presented on a later request, the path is checked against the
  cookie's `deletePaths` (`/logout`, `/oauth2/logout`); if matched, a delete-cookie response
  is emitted.
- An expired cookie causes the gateway to emit a delete-cookie response and continue
  serving (it does NOT 401).
- A cookie with a bad HMAC or undecodable payload causes the gateway to respond with `500`;
  it never silently accepts a tampered cookie.
- When `cookie.wireCompat` is `true` (default), the cookie encoding and TTL format
  (`10.00m` etc.) match the Java gateway's wire format byte-for-byte.

### 1.6 Forwarded headers reach the backend

*As a platform engineer, I want Trino to see the original client's address, scheme, host,
and port so that audit logs and Trino's `X-Forwarded-*`-aware features work correctly.*

**Acceptance Criteria**

- The gateway sets `X-Forwarded-For` on the upstream request, appending the gateway's
  client IP to any existing value rather than overwriting it.
- `X-Forwarded-Proto` is `https` when the inbound connection was TLS, `http` otherwise.
- `X-Forwarded-Host` is the inbound `Host` header, host-only (no port; IPv6 literals
  preserved as `[addr]`).
- `X-Forwarded-Port` is the explicit port from the inbound `Host` header, or the scheme
  default (`80`/`443`) when none is present.
- Hop-by-hop headers (`Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`,
  `Te`, `Trailers`, `Transfer-Encoding`, `Upgrade`) are never forwarded.

### 1.7 External router can rewrite request headers

*As a platform engineer, I want my routing service to be able to inject headers (e.g. a
session tag) into the outbound request so that I can implement custom Trino client-side
behaviour without changing the gateway.*

**Acceptance Criteria**

- Headers returned by the external router in `externalHeaders` are set on the upstream
  request with REPLACE semantics (overwriting any pre-existing value).
- Any header listed in `routing.external.excludeHeaders` is stripped from the
  `externalHeaders` returned by the router (both HTTP and gRPC transports). The HTTP
  transport additionally strips those headers — plus `Content-Length` — from the inbound
  request headers it forwards to the router. The gRPC transport does not carry arbitrary
  inbound headers, so this filter is response-side only for gRPC.

---

## 2. Routing

### 2.1 External routing service

*As a platform engineer, I want the gateway to delegate routing decisions to a separate
service so that I can evolve routing policy independently from the gateway binary.*

**Acceptance Criteria**

- `routing.type` must be `EXTERNAL` (only supported type; validated at startup).
- If `routing.external.grpcAddr` is set, the gateway tries gRPC first; on any error it
  falls back to HTTP if `routing.external.url` is set.
- Both transports use the same per-call timeout (`routing.external.timeout`, default `1s`).
- The HTTP transport POSTs a JSON body containing method, URI, remote info, parameters,
  and a best-effort `trinoQueryProperties` block; it also forwards the original Trino
  headers (minus `excludeHeaders` and `Content-Length`) as HTTP headers on the request to
  the router. The gRPC transport sends an equivalent `RouteRequest` proto with the same
  fields, but does not forward arbitrary inbound headers. Both transports populate
  `trinoQueryProperties` with empty parser-derived fields and set `errorMessage` to
  `"trino-parser not available in Go v1"` (the Go gateway does not include a SQL parser).
- If both transports fail, the gateway falls back to `routing.defaultGroup` and then to the
  recovery chain (see 1.4); the request is not rejected solely because the router is down.

### 2.2 Routing group → backend resolution

*As a platform engineer, I want to assign each backend to a routing group so that traffic
classes (e.g. `adhoc`, `etl`, `dashboards`) can be steered to distinct clusters.*

**Acceptance Criteria**

- Each backend row carries a `routing_group` string.
- The router resolves a group name to the URL of any active backend whose `routing_group`
  matches.
- If no backend in the requested group is active, the recovery chain runs and ultimately
  the first active backend (regardless of group) is selected.

### 2.3 Single-cluster / dev mode

*As a gateway operator running a single Trino cluster, I want to use the gateway without
deploying a separate routing service so that the gateway is usable out of the box.*

**Acceptance Criteria**

- If neither `routing.external.url` nor `routing.external.grpcAddr` is configured, the
  external call is skipped and every request is routed to `routing.defaultGroup`.
- The annotated `config.example.yaml` documents this fallback so operators know it is a
  supported deployment mode.

---

## 3. Backend health monitoring

### 3.1 Active backend health probes

*As a gateway operator, I want the gateway to know which backends are healthy so that it
does not route queries to a cluster that is starting up, partitioned, or down.*

**Acceptance Criteria**

- A background monitor probes each active backend on `monitor.interval` (default `30s`).
- Each probe is `GET <backendURL>/v1/info` with a per-probe deadline of
  `monitor.checkTimeout` (default `5s`).
- A backend is `HEALTHY` only if it responds `200` with `{"starting": false}`; anything
  else (transport error, non-200, malformed body, `{"starting": true}`) is `UNHEALTHY`.
- An unprobed backend is `UNKNOWN`; a freshly upserted backend is `PENDING` until the next
  probe cycle.
- Probe status is updated by atomic swap, so admin reads never see a half-updated map.

### 3.2 Backend list reflects current configuration

*As a gateway operator, I want backends added or removed via the admin API to start being
probed and routed to without restarting the gateway.*

**Acceptance Criteria**

- A background refresher reloads the active backend list from the DB every `15s` and pushes
  it into the monitor.
- Backends upserted through `POST /entity?entityType=GATEWAY_BACKEND` immediately have
  their status seeded (`PENDING` if active, `UNHEALTHY` if not) so they are visible to the
  admin UI before the next probe cycle.

### 3.3 Process liveness and readiness

*As a gateway operator, I want Kubernetes-style probes so that orchestration can tell when
the gateway is alive and ready to take traffic.*

**Acceptance Criteria**

- `GET /trino-gateway/livez` on the admin port always returns `200 ok`.
- `GET /trino-gateway/readyz` returns `503 not ready` until the first monitor probe cycle
  has completed, then `200 ok`. This guarantees the gateway does not advertise readiness
  before any backend health is known.

---

## 4. Admin and management API

### 4.1 Backend CRUD via REST

*As a gateway operator, I want a REST API to list, add, update, delete, activate, and
deactivate backends so that I can integrate the gateway with my deployment automation.*

**Acceptance Criteria**

- The following endpoints exist on the admin port (default `8090`) under the `API` role:
  - `GET /gateway/backend/all`
  - `GET /gateway/backend/active`
  - `POST /gateway/backend/activate/{name}`
  - `POST /gateway/backend/deactivate/{name}`
  - `POST /gateway/backend/modify/add`
  - `POST /gateway/backend/modify/update`
  - `POST /gateway/backend/modify/delete` (body is the raw backend name, not JSON)
- The wire JSON for a backend is `{name, proxyTo, externalUrl?, active, routingGroup}`,
  matching the Java gateway's `ProxyBackend` shape.
- The same shape is also exposed read-only at `/api/public/backends`,
  `/api/public/backends/{name}`, and `/api/public/backends/{name}/state` (no auth).
- Backends are persisted in the `gateway_backend` table via upsert (Postgres `ON
  CONFLICT`, MySQL `ON DUPLICATE KEY UPDATE`).

### 4.2 Entity API (Java compatibility)

*As a gateway operator migrating from the Java gateway, I want the `/entity` API to keep
working so that existing automation does not need to change.*

**Acceptance Criteria**

- `GET /entity` returns `["GATEWAY_BACKEND"]`.
- `POST /entity?entityType=GATEWAY_BACKEND` accepts a `ProxyBackend` JSON body, upserts it,
  and immediately updates the monitor's in-memory status (`PENDING` if active, `UNHEALTHY`
  if not).
- `GET /entity/{entityType}` returns all backends for `GATEWAY_BACKEND`; any other
  entity type returns `200 []` (empty array).
- `POST /entity` with an unknown `entityType` returns `500` (mirroring the Java gateway).
- Endpoints require the `ADMIN` role.

### 4.3 Webapp endpoints

*As a UI integrator, I want the legacy `/webapp/*` endpoints to behave the same as the
Java gateway so that the existing front-end can be served unmodified.*

**Acceptance Criteria**

- All `/webapp/*` responses use the envelope `{code, msg, data}` (`code: 200`,
  `msg: "Successful."` on success; `code: 500`, `msg: "<reason>"`, `data: null` on error).
- `POST /webapp/getAllBackends` returns backends with their live `status`
  (`"HEALTHY" | "UNHEALTHY" | "PENDING"`).
- `POST /webapp/findQueryHistory` accepts `{userName, backendUrl, queryId, source, page,
  pageSize}` and returns `TableData<QueryDetail>`; non-ADMIN callers have their `userName`
  filter forced to their own identity (server-side, not client-side).
- `POST /webapp/getDistribution` returns aggregate counts (`totalBackendCount`,
  `onlineBackendCount`, `offlineBackendCount`, `healthyBackendCount`,
  `unhealthyBackendCount`, `totalQueryCount`, average queries-per-minute /
  -per-second since process start, `distributionChart`, `lineChart`, ISO-8601
  `startTime`).
- `POST /webapp/getUIConfiguration` returns `{authType}`.
- `POST /webapp/getRoutingRules` / `updateRoutingRules` are v1 stubs that return an empty
  list; both require the `ADMIN` role (they are mounted in the admin-only group, not as
  read endpoints).
- `POST /webapp/saveBackend`, `/updateBackend`, `/deleteBackend` require the `ADMIN` role.
- `POST /webapp/getAllBackends`, `/findQueryHistory`, `/getDistribution`,
  `/getUIConfiguration` require the `USER` role.

### 4.4 Query history

*As a Trino user, I want to see my recent queries (where they ran, when, against which
cluster) so that I can debug performance and locate result sets.*

**Acceptance Criteria**

- `GET /trino-gateway/api/queryHistory` requires the `USER` role.
- ADMIN callers see the 100 most recent queries across all users.
- Non-ADMIN callers are filtered server-side to their own username; they cannot see other
  users' history by passing a username parameter.
- `GET /trino-gateway/api/queryHistoryDistribution` returns a `{backendUrl: count}` map,
  also scoped to the caller for non-ADMIN.
- `GET /trino-gateway/api/activeBackends` returns the list of active backends in the
  legacy wire format.

### 4.5 User identity and roles

*As a UI integrator, I want a single endpoint that tells me who is logged in and what roles
they hold so that the UI can adapt to the caller's permissions.*

**Acceptance Criteria**

- `POST /userinfo` (USER role) returns `{userId, userName, roles, permissions}` for the
  authenticated principal.
- `roles` contains any of `ADMIN`, `USER`, `API` that the principal qualifies for based on
  the regex-based authorization config.
- `POST /loginType` reports the active auth mode (`oauth` for OIDC, `form` for LDAP,
  `none` for NOOP).
- `POST /login` returns `{token: <username>}` under NOOP (dev/test); OIDC/LDAP login flows
  are not implemented in v1 and return an error envelope.
- `POST /logout` returns a success envelope.

---

## 5. Authentication and authorization

### 5.1 Pluggable auth backends

*As a gateway operator, I want to choose between no-auth, OIDC, and LDAP so that the
gateway fits into both dev and production environments.*

**Acceptance Criteria**

- `auth.type` may be `NOOP` (default), `OIDC`, or `LDAP`.
- `NOOP`: every request is treated as principal `anonymous`; no role assignments unless
  the regex happens to match the empty string. Useful for dev only.
- `OIDC`: the admin API requires `Authorization: Bearer <JWT>`. Tokens are validated
  against `auth.oidc.jwksUrl`; the JWKS is fetched at startup and refreshed every
  `auth.oidc.jwksTtlSecs` (default `300s`) in the background.
- `LDAP`: the admin API requires HTTP Basic credentials. The gateway service-binds with
  `bindDn/bindPassword`, searches `userBase` by `(userAttr=<username>)`, then re-binds as
  the user DN to verify the password.
- Failed auth returns `401` with body `{"error":"<msg>"}` and `WWW-Authenticate: Bearer`.
- The proxy port (Trino traffic) intentionally does not enforce gateway auth — Trino
  handles its own auth on each backend.

### 5.2 Role assignment via regex

*As a gateway operator, I want to map group memberships to gateway roles without writing
code so that I can reuse my existing IdP groups.*

**Acceptance Criteria**

- `auth.authorization.{admin, user, api}` are Java-compatible regex patterns matched
  against the principal's `memberOf` string.
- For OIDC, `memberOf` is built from the `groups` or `memberOf` JWT claim (comma-joined if
  a JSON array).
- For LDAP, `memberOf` is read directly from the user entry's `memberOf` attribute.
- An empty regex never matches — a role with no pattern configured is never granted.
- `RequireRole` middleware returns `403 {"error":"forbidden"}` when the principal does not
  hold the role.

### 5.3 OIDC bootstrap fails fast

*As a gateway operator, I want a misconfigured OIDC setup to cause startup to fail rather
than silently start with a broken auth path.*

**Acceptance Criteria**

- `auth.type: OIDC` requires `auth.oidc.jwksUrl`; missing it fails `Config.Validate()` at
  startup.
- The initial JWKS fetch must succeed before the OIDC middleware is wired into the admin
  server; if it fails, the gateway does not start.
- The background JWKS refresher logs (does not crash) on subsequent fetch failures; the
  most recently good JWKS keeps validating tokens until a successful refresh.

### 5.4 LDAP bootstrap fails fast

*As a gateway operator, I want LDAP misconfiguration caught at startup rather than at first
request.*

**Acceptance Criteria**

- `auth.type: LDAP` requires `auth.ldap.url` and `auth.ldap.userBase`; missing either
  fails `Config.Validate()` at startup.
- `auth.ldap.userAttr` defaults to `uid` when omitted.

---

## 6. Configuration and lifecycle

### 6.1 YAML configuration with defaults

*As a gateway operator, I want a single, minimal YAML config that fills in sensible
defaults so that I can boot the gateway without copying a 200-line example file.*

**Acceptance Criteria**

- The gateway reads `--config <path>` (default `config.yml`) at startup.
- Every section has documented defaults (see `config.example.yaml`): proxy `:8080` /
  `1 MiB` / `30s`; admin `:8090`; monitor `30s` / `5s`; routing `EXTERNAL` / `1s`; cookie
  `10m` / `wireCompat: true`; auth `NOOP`; OIDC `jwksTtlSecs: 300`; LDAP `userAttr: uid`.
- Duration strings use Go's format (`30s`, `1m`, `1h30m`); size strings accept
  `B/KB/KiB/MB/MiB/GB/GiB`.
- Validation rejects the config (and the gateway refuses to start) when:
  - `proxy.port == admin.port`
  - `proxy.responseSize <= 0`
  - `db.driver` is set to anything other than `postgres` or `mysql`
  - `routing.type` is not `EXTERNAL`
  - OIDC is selected without `jwksUrl`, or LDAP without `url`/`userBase`

### 6.2 Coordinated startup

*As a gateway operator, I want the proxy and admin ports to be bound before any background
worker starts so that probes (and unit tests) can discover the gateway by port without
race conditions.*

**Acceptance Criteria**

- The lifecycle layer binds both `proxy.port` and `admin.port` before serving traffic; if
  either bind fails the gateway returns the error and exits — the already-bound socket is
  closed first.
- The monitor and backend-refresh loop start after the ports are bound.
- `readyz` flips to `200` only once the first monitor probe cycle completes (driven by
  `Monitor.SetOnFirstTick` → `Admin.SetReady`).

### 6.3 Graceful shutdown on SIGTERM/SIGINT

*As a gateway operator, I want SIGTERM to drain in-flight requests instead of dropping
them so that rolling deploys do not produce client errors.*

**Acceptance Criteria**

- On `SIGTERM` or `SIGINT`, the root context is cancelled.
- Both proxy and admin HTTP servers are shut down concurrently with a `30s` deadline.
- The monitor and backend-refresh goroutines are stopped and waited on before the process
  exits.
- The process exits with status `0` on a clean shutdown (no fatal startup error).

### 6.4 Persistence is required in v1

*As a gateway operator, I want the gateway to refuse to start without a database so that I
do not accidentally run a stateless gateway that loses backends and query history on
restart.*

**Acceptance Criteria**

- `db.driver` must be `postgres` or `mysql`. A missing driver causes startup to return
  `"db.driver must be configured"` from the composition root; an unknown driver is
  rejected earlier by `Config.Validate()` with
  `"db.driver must be \"postgres\" or \"mysql\", got <value>"`.
- Embedded `goose` migrations are run during `persistence.Open`; the gateway does not
  start if migrations fail.
- Both DAOs (backends and history) use parameterized queries via `sqlx`.

### 6.5 Three distinct HTTP clients

*As a gateway operator, I want proxying, monitoring, and routing-service calls to use
isolated HTTP clients so that a slow routing service cannot exhaust connections used for
real query traffic.*

**Acceptance Criteria**

- `proxyClient` uses `proxy.requestTimeout` and never follows redirects (responses with
  3xx codes are returned to the client verbatim).
- `monitorClient` uses `monitor.checkTimeout`.
- `routerClient` uses `routing.external.timeout`.
- The same `monitorClient` is reused for the recovery chain's `HEAD` probes (same timeout
  profile).

---

## 7. Migration tooling

### 7.1 Java config → Go config

*As a gateway operator migrating from the Java gateway, I want a tool that translates my
existing `config.yml` to the Go gateway's format so that I do not have to hand-port every
field.*

**Acceptance Criteria**

- `goway-migrate-config --input <java.yml> [--output <go.yml>] [--dry-run]` reads a Java
  trino-gateway config and writes the equivalent Go config to a file or stdout.
- Any value the migrator cannot translate is emitted as a `# WARNING: ...` comment at the
  top of the output rather than silently dropped.
- The tool exits non-zero on read, parse, or marshal errors.

---

## 8. Differential testing

### 8.1 Replay scenarios against committed goldens

*As a CI pipeline, I want each PR to verify that the Go gateway still produces the same
HTTP responses as the recorded Java reference so that drift from Java is caught at PR
time, not in production.*

**Acceptance Criteria**

- `goway-diff-harness replay --go-url <url> [--scenarios <dir>] [--goldens <dir>]
  [--format text|json]` runs every scenario YAML against the Go gateway and diffs the
  normalized response against a committed golden JSON file.
- Replay does not require a running Java gateway.
- The harness exits non-zero on any `FAIL` or `ERROR`; text output groups results per
  scenario with a `PASS/FAIL/SKIP/ERROR` summary line.

### 8.2 Record goldens from a live Java gateway

*As a CI pipeline, I want a nightly job that re-records the goldens from a live Java
gateway so that the reference is always current with the latest Java release we support.*

**Acceptance Criteria**

- `goway-diff-harness record --java-url <url> [--scenarios <dir>] [--goldens <dir>]`
  replays each scenario against the Java gateway, normalizes the response with the
  scenario's `DiffPolicy`, and writes one `<scenario>.json` file per scenario.
- Each golden carries a `version` tag; replay refuses to consume a golden with the wrong
  version and reports `re-record`.
- A nightly bootstrap helper (`BootstrapContainers`, behind the `diff` build tag) brings
  up Postgres + Java gateway + Trino in testcontainers and registers a default backend so
  the record job has a working Java target.

### 8.3 Live Java-vs-Go comparison

*As a CI pipeline, I want a nightly job that runs both gateways side by side and diffs
their live responses so that we detect Java-side behaviour changes that the goldens would
otherwise hide.*

**Acceptance Criteria**

- `goway-diff-harness live --java-url <url> --go-url <url> [--scenarios <dir>]
  [--format text|json]` replays each scenario against both gateways concurrently,
  normalizes both sides with the scenario's `DiffPolicy`, and emits per-scenario verdicts.
- Both sides see the same per-step Extract chain (`pathFromVar` resolves to the same
  variable on both runs), so multi-step scenarios with `nextUri` polling work without
  coupling to a specific gateway host.

### 8.4 Reproducible normalization

*As a reviewer, I want to know exactly what gets ignored when comparing two responses so
that a passing diff cannot hide a regression by stripping a real header.*

**Acceptance Criteria**

- Normalization is controlled per-scenario via `DiffPolicy.{IgnoreHeaders,
  IgnoreBodyFields, RewriteHostPort}` — no global allowlist.
- `RewriteHostPort` replaces each side's `host:port` with the sentinel `<GATEWAY>` in both
  headers and body before diffing, so URLs pointing at the gateway compare equal.
- Body diffs prefer structural JSON comparison (via `go-cmp`); non-JSON bodies fall back
  to byte-level diff.
- Adding a new entry to `IgnoreHeaders` / `IgnoreBodyFields` requires a justification
  comment in the scenario YAML (project policy).

### 8.5 CLI re-report

*As an operator triaging a failed CI run, I want to re-render the harness output without
re-running the scenarios so that I can read the JSON artifact in the same format as the
console output.*

**Acceptance Criteria**

- `goway-diff-harness report --input <results.json> [--format text|json]` re-renders an
  existing results file. Non-zero exit on any `FAIL` or `ERROR`.

---

## Hard Invariants

These are the non-negotiable protocol guarantees of the gateway. Every change to the proxy,
routing, or cookie layers must preserve them.

1. **No body rewriting.** The gateway never alters request or response bodies. The
   `/v1/statement` response is read into a buffer only to extract the `queryId`; the bytes
   are written back to the client unchanged.

2. **No redirect following.** The proxy's HTTP client returns 3xx responses verbatim
   (`CheckRedirect → http.ErrUseLastResponse`). Trino clients must see the gateway's
   redirect, not a downstream one.

3. **WriteCache before flush.** On `POST /v1/statement`, the `queryId → backendURL` cache
   write happens *before* the response status and body are written to the client. A client
   that receives the response and immediately polls is guaranteed to land on the same
   backend.

4. **Bounded response buffering.** Only `POST /v1/statement` buffers the upstream response,
   and only up to `proxy.responseSize`. Every other path streams the body with `io.Copy`.

5. **Tampered cookies are loud.** A `TG.OAUTH2` cookie with an HMAC mismatch or undecodable
   payload returns `500` to the client. The gateway never silently treats a tampered cookie
   as anonymous.

6. **KILL QUERY routes by ID.** A `POST` body matching `(?i)KILL\s+QUERY\s+'(<id>)'` is
   inspected before any external router call; the cancellation is forwarded to the backend
   that owns the query.

7. **Hop-by-hop headers are stripped both ways.** `Connection`, `Keep-Alive`,
   `Proxy-Authenticate`, `Proxy-Authorization`, `Te`, `Trailers`, `Transfer-Encoding`, and
   `Upgrade` are removed from both the upstream request and the downstream response.

8. **`X-Forwarded-For` appends, never overwrites.** When the inbound request already has an
   `X-Forwarded-For` header, the gateway's client IP is appended (`existing, clientIP`),
   preserving the original chain.

9. **`externalHeaders` use REPLACE semantics.** Headers returned by the external router are
   `Set` (not `Add`) on the upstream request — collisions with caller-supplied headers
   resolve in favor of the router.

10. **Java wire compatibility (default on).** With `cookie.wireCompat: true` (the default),
    the `TG.OAUTH2` cookie's JSON field order, signature input, base64 encoding (URL-safe
    with padding), and TTL string format (airlift Duration: `10.00m`) match the Java
    gateway byte-for-byte.

11. **`readyz` requires a probe cycle.** `/trino-gateway/readyz` returns `503` until the
    monitor's first probe cycle completes. The gateway never advertises readiness before
    any backend health is known.

12. **Three HTTP clients.** Proxy, monitor, and external-router traffic each use their own
    `*http.Client` with their own timeout. A misbehaving router cannot starve query
    proxying of connections.
