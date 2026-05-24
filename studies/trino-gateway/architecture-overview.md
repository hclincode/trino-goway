---
title: trino-gateway architecture overview
author: java-analyst
role: Java Analyst
component: trino-gateway
topics: [cross-cutting, proxy-core, routing-engine, cluster-registry, mgmt-api, config]
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to: []
---

# trino-gateway architecture overview

## Summary

trino-gateway is a stateful HTTP reverse proxy in front of one or more Trino clusters. A single Java process accepts client HTTP requests, decides which backend cluster should serve each request (using a combination of URL path matching, request headers, sticky-routing cookies, prior query-id bindings, and an optional rules engine), proxies the request to that backend, and on the way back records query metadata for later inspection. A small REST and HTML admin surface lets operators add/remove clusters, view query history, and manage routing rules. This file is the orientation map; behavioral specs for each surface live in their own files (see Cross-references).

## Key Findings

### Process model and startup

- Single JVM process with one entry point: `HaGatewayLauncher.main(args)` takes exactly one argument, the path to a YAML config file (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/HaGatewayLauncher.java:107-119`).
- Startup steps, in order: (1) read YAML into `HaGatewayConfiguration` with environment-variable interpolation (`ConfigurationUtils.replaceEnvironmentVariables`); (2) run database migrations against the configured datastore via `FlywayMigration.migrate(...)`; (3) build a list of modules (defaults + user-listed `modules:` from config); (4) start the embedded HTTP server (`HaGatewayLauncher.java:51-94`).
- On configuration error the process exits with status code 100; other startup failures also exit with 100 (`HaGatewayLauncher.java:77-91`).

### Top-level package map (`io.trino.gateway`)

Two roots under `io.trino.gateway`: `baseapp` (one class, the JAX-RS/HTTP wiring shell) and `ha` (everything else). The `proxyserver` package is a peer of `ha` and `baseapp`. There are 146 Java files totalling ~13.6k LOC.

| Package | Purpose | Notable files |
|---|---|---|
| `baseapp` | Boots the embedded HTTP server, registers JAX-RS resources and JAX-RS providers, exports JMX metrics, allows dynamic loading of optional user-supplied modules and managed apps via FQCN strings in config | `BaseApp.java` |
| `ha` | Top-level launcher and utilities | `HaGatewayLauncher.java`, `util/` |
| `ha.module` | Single Guice provider module that wires routing, cluster monitoring, auth, persistence | `HaGatewayProviderModule.java` |
| `ha.config` | Plain Java config classes deserialized from YAML by Jackson; one class per logical section of the config file | `HaGatewayConfiguration.java` (root), 24 siblings |
| `ha.resource` | JAX-RS REST endpoints (HTTP API surface for clients of the gateway itself, not the proxied Trino traffic) | `GatewayResource`, `HaGatewayResource`, `PublicResource`, `LoginResource`, `GatewayHealthCheckResource`, `GatewayViewResource`, `GatewayWebAppResource`, `EntityEditorResource` |
| `ha.handler` | Pre-proxy request inspection and routing decision plumbing; helpers and stats | `RoutingTargetHandler`, `HttpUtils`, `ProxyUtils`, `ProxyHandlerStats`; `schema/RoutingDestination`, `schema/RoutingTargetResponse` |
| `ha.router` | Routing brain: backend registry, routing-group selectors, MVEL rule evaluation, sticky-routing cookies, per-request user/SQL extraction, query-history capture | `RoutingManager` (iface), `StochasticRoutingManager`, `QueryCountBasedRouter`, `BaseRoutingManager`, `RoutingGroupSelector` (iface) + `FileBasedRoutingGroupSelector` / `ExternalRoutingGroupSelector` (header-based default is an inline impl), `MVELRoutingRule`, `RoutingRulesManager`, `BackendStateManager`, `GatewayBackendManager` (iface) + `HaGatewayManager`, `QueryHistoryManager` (iface) + `HaQueryHistoryManager`, `GatewayCookie`, `OAuth2GatewayCookie`, `TrinoQueryProperties`, `TrinoRequestUser`, `StatementUtils`, `PathFilter`, `QueryType` |
| `ha.clustermonitor` | Background polling of backend Trino clusters; pluggable monitor implementations; observer fanout to health-tracking and stats sinks | `ActiveClusterMonitor` (the polling loop), `ClusterStatsMonitor` (iface), 5 impls (`Http`, `InfoApi`, `Jdbc`, `Jmx`, `Metrics`) plus `Noop`, `ClusterStats` (record-like), `TrinoStatus` (enum), `TrinoClusterStatsObserver`, `HealthCheckObserver`, `ClusterStatsObserver`, `ClusterMetricsStats(Exporter)`, `JmxAttribute`, `JmxResponse`, `MonitorUtils`, `ServerInfo`, `UiApiCookieJar` |
| `ha.security` | Authentication filters and authorization; LDAP, OAuth2/OIDC, form, basic, noop modes; principal/role context | `LbFilter` (the chain), `ChainedAuthFilter` (in `security/util`), `BasicAuthFilter`, `FormAuthenticator`, `LbOAuthManager`, `LbFormAuthManager`, `LbLdapClient`, `LbAuthenticator`, `LbAuthorizer`/`NoopAuthorizer`, `LbPrincipal`, `LbKeyProvider`, `LbTokenUtil`, `LbUnauthorizedHandler`, `NoopFilter`, `OidcCookie`, `SessionCookie`, `ResourceSecurityDynamicFeature`, `QueryMetadataParser`, `QueryUserInfoParser` |
| `ha.persistence` | JDBC connection management, schema migration, generic row-mapping | `JdbcConnectionManager`, `FlywayMigration`, `RecordAndAnnotatedConstructorMapper`; `dao/GatewayBackend(Dao)`, `dao/QueryHistory(Dao)` |
| `ha.domain` | DTOs for REST request/response bodies | `Result`, `RoutingRule`, `TableData`; `request/*`, `response/*` |
| `proxyserver` | The actual reverse-proxy HTTP I/O — receives client request, performs outbound call to Trino, streams response back, manages forwarded headers and gateway cookies | `RouteToBackendResource` (JAX-RS endpoint covering all HTTP methods), `RouterPreMatchContainerRequestFilter` (path-matcher that redirects qualifying requests onto `RouteToBackendResource`), `ProxyRequestHandler`, `ProxyResponseHandler`, `ProxyServerModule`, `MultiReadHttpServletRequest`, `ProxyException` |

### Two HTTP surfaces in one process

The gateway exposes two functionally distinct HTTP surfaces, served by the same embedded HTTP server on the same port:

1. **Proxied surface.** Any inbound path that matches the path-whitelist (`PathFilter`) — which always includes `/v1/statement` plus any operator-configured `statementPaths`, plus `/v1/info`, `/v1/info/state`, `/ui/`, `/oauth2/`, and operator-supplied `extraWhitelistPaths` — is rewritten by `RouterPreMatchContainerRequestFilter` to the internal URI `/trino-gateway/internal/route_to_backend` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouterPreMatchContainerRequestFilter.java:34,48-49`), where `RouteToBackendResource` dispatches by HTTP method into `ProxyRequestHandler` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouteToBackendResource.java:58-108`). These requests are forwarded to a chosen Trino backend.
2. **Self surface.** Everything else is a JAX-RS resource handled in-process: admin REST (`/gateway/...`, `/gateway/backend/modify/...`), public REST (`/api/public/backends...`), health (`/trino-gateway/livez`, `/trino-gateway/readyz`), login/auth (`/login`, `/logout`, `/sso`, `/oauth2/callback`, `/userinfo`, `/loginType`), web UI views (`/trino-gateway/api/...`, `/webapp/...`, `/entity/...`), and static assets.

The two surfaces share the same auth filter chain (`LbFilter` / `ChainedAuthFilter`) and the same JSON/Jackson stack.

### Control flow for a proxied request (orientation only — `proxy-request-lifecycle.md` for detail)

1. Client sends request to gateway (e.g., `POST /v1/statement` with SQL body).
2. `RouterPreMatchContainerRequestFilter.filter` runs pre-match (before JAX-RS path matching). If the path is whitelisted, the request URI is rewritten to `/trino-gateway/internal/route_to_backend` (`RouterPreMatchContainerRequestFilter.java:44-51`).
3. `RouteToBackendResource` receives the rewritten request and dispatches by HTTP method to its respective handler (`RouteToBackendResource.java:58-108`). For POST/PUT the body is read into a `MultiReadHttpServletRequest` so it can be inspected (e.g., as SQL text) and then re-read by the outbound proxy call.
4. `RoutingTargetHandler.resolveRouting(request)` decides which backend to send to (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:70-87`):
   - If the request URL contains a Trino query id and a backend was previously bound to that id, use the bound backend (sticky on query id).
   - Else if a `gateway_*` cookie names a backend that matches the current path, use that backend (sticky on cookie).
   - Else call `RoutingGroupSelector.findRoutingDestination(request)` to pick a routing group (from `X-Trino-Routing-Group` header, file-based rules, or external HTTP rules), fall back to `defaultRoutingGroup` if none, then call `RoutingManager.provideBackendConfiguration(group, user)` to pick a healthy backend in that group.
5. `ProxyRequestHandler.{get,post,put,delete,head}Request(...)` executes the outbound HTTP call asynchronously, copying through most request headers (skipping `Accept-Encoding` and `Host`; optionally injecting `X-Forwarded-*`), with redirects disabled and a global async timeout that yields a 502 on expiry.
6. On the way back, if this was a `POST` to a statement path and the response was 200, the `id` field of the JSON body is extracted and recorded as `queryId → backend` so future polls of `nextUri` can be routed back to the same backend; the query is also written to query history.

### Module wiring (Guice + Airlift Bootstrap)

- `HaGatewayLauncher` builds a fixed list of Airlift platform modules (`NodeModule`, `HttpServerModule`, `JmxModule`, `JmxHttpModule`, `JmxOpenMetricsModule`, `LogJmxModule`, `MBeanModule`, `JsonModule`, `JaxrsModule`, `TracingModule`) plus `HaGatewayProviderModule(configuration)` and `BaseApp(configuration)`, then loads any FQCN-listed user modules from config (`HaGatewayLauncher.java:56-70`, `BaseApp.java:93-106`).
- `HaGatewayProviderModule.configure()` binds the gateway-specific interfaces to their implementations (`HaGatewayManager`, `HaQueryHistoryManager`, `BackendStateManager`, `JdbcConnectionManager`, `AuthorizationManager`, `PathFilter`, `ActiveClusterMonitor`), and conditionally wires the auth filter chain based on whether `authentication:` is present in config (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java:78-99`).
- `@Provides` factories in the same module switch on config to produce a `RoutingGroupSelector` (header / file-rules / external) and a `ClusterStatsMonitor` (one of six variants) (`HaGatewayProviderModule.java:152-197`).
- `BaseApp.configure()` registers JAX-RS resources, the routing handler, query stats, and the default `RoutingManager` implementation (`StochasticRoutingManager`) via an optional binder so a user module can override it (`BaseApp.java:121-143`).
- Two extension points sit at module level: `modules:` (custom Guice modules with a `(HaGatewayConfiguration)` constructor) and `managedApps:` (FQCN classes bound as singletons, useful for adding extra background loops).

### State held in-process

The gateway is not purely stateless. In-process state includes:

- `RoutingManager` (Stochastic or QueryCount): per-`queryId` maps for `backend`, `routingGroup`, `externalUrl` (used for stickiness across the lifetime of a query). Default implementation `StochasticRoutingManager`. Implementations decide eviction policy.
- `BackendStateManager`: latest known per-backend health/stats.
- `ActiveClusterMonitor`: background polling loop.
- `RoutingRulesManager` / `FileBasedRoutingGroupSelector`: compiled MVEL rules with a configurable refresh period from a file on disk.
- Cached / pooled JDBC connections via `JdbcConnectionManager`.

Durable state lives in the configured datastore (MySQL / PostgreSQL / Oracle): `gateway_backend` table (registry of backends) and `query_history` table (one row per completed proxied query). See `persistence-and-db-schema.md` (planned).

### Embedded HTTP server provenance

The gateway uses Airlift's `HttpServerModule`. This is Jetty under the hood (Airlift packages and configures it). All public HTTP — both proxied and self surfaces — flows through this server. Outbound HTTP from the gateway to backends uses Airlift's `HttpClient`, with three named clients in the system: `@ForProxy` (request forwarding), `@ForMonitor` (cluster polling), `@ForRouter` (external rules engine calls) (`HaGatewayLauncher.java:23`, `BaseApp.java:191-192`, `ProxyServerModule.java:30`).

## Behavior vs. Implementation Artifact

### Pre-match URI rewriting to an internal path
- **Observed behavior:** A pre-match container request filter rewrites every whitelisted inbound URI to `/trino-gateway/internal/route_to_backend` so that JAX-RS path matching dispatches it to one resource class regardless of the original URL (`RouterPreMatchContainerRequestFilter.java:34-51`).
- **Source of behavior:** `jvm-artifact`. This is a JAX-RS pattern for unifying a wildcard set of incoming paths onto one handler under Jersey's path-matching semantics. The internal URI never appears on the wire.
- **Rationale:** It lets operators add new statement-style paths via config (`statementPaths`, `extraWhitelistPaths`) without having to register additional JAX-RS resources at startup.
- **Go obligation:** `drop`. A Go HTTP router (any of `chi`, `gin`, `gorilla/mux`, `net/http` with patterns) can simply install a catch-all handler keyed on a path predicate. The Go rewrite must preserve the *behavior* — any whitelisted inbound path is treated as a proxied request — but the URI-rewrite trick is a Jersey workaround.
- **Notes:** Test fixtures or external observability that asserts on `/trino-gateway/internal/route_to_backend` (if any exist) would be JVM-specific. None should leak to the client.

### Dynamic loading of user Guice modules and managed apps via FQCN strings
- **Observed behavior:** `modules:` and `managedApps:` config arrays accept fully-qualified Java class names; the launcher uses reflection to instantiate them at boot (`BaseApp.java:69-91, 145-166`).
- **Source of behavior:** `jvm-artifact`. This is a JVM classpath-extension mechanism — operators drop a JAR with custom modules on the classpath and add the FQCN to YAML.
- **Rationale:** Allows third-party extension without forking the gateway.
- **Go obligation:** `drop`. Go has no equivalent runtime classloading story. If the team needs an extension story, it should be designed natively (Go plugins, gRPC sidecar, embedded scripting, etc.) — but treat that as a v2 concern and skip in v1 unless an operator has shipped extensions that ride on this. **Open question for `@trino-expert`:** are public-facing trino-gateway extensions known to use `modules:` / `managedApps:`?
- **Notes:** Removing this is a one-line config schema change (fields go away).

### Optional binder for `RoutingManager`
- **Observed behavior:** `BaseApp` binds `RoutingManager` to `StochasticRoutingManager` via Guice's optional binder (`BaseApp.java:139-142`), letting a user module override it. `QueryCountBasedRouter` exists in the same package as a non-default alternative.
- **Source of behavior:** `gateway-design-intent`. The "pick which healthy backend in a routing group" strategy is intentionally pluggable.
- **Go obligation:** `replicate-intent`. The Go design should accept multiple `RoutingManager`-equivalent strategies (at least stochastic and query-count-based) selected via config; the binder mechanism itself is Java-specific.
- **Notes:** See `routing-engine.md` (planned) for the selection algorithms.

### Exit code 100 on startup failure
- **Observed behavior:** Config errors and any other startup throwable cause `System.exit(100)` (`HaGatewayLauncher.java:86, 90`).
- **Source of behavior:** `gateway-design-intent` (probably) or `defensive-historical` — `100` is unusual enough that it may be load-bearing for an operator's process supervisor. **Open question for `@trino-expert`:** does the trino-gateway readme/docs commit to exit-code 100, or is this incidental?
- **Go obligation:** `defer-to-expert`. If documented, `replicate-exactly`. If incidental, the Go rewrite can use the conventional `1` for fatal errors.

## Implications for Go Rewrite

- **Architecture is genuinely simple at the top.** One process, one HTTP server, one config file, two HTTP surfaces, ~13.6k LOC. The Go rewrite can mirror the package layout almost 1:1 — there is nothing in the top-level structure that needs to be rethought.
- **The Guice DI / Airlift Bootstrap shell does not produce specifiable behavior.** It is pure assembly. Architect should pick the Go equivalent (likely `wire` for compile-time DI or hand-rolled constructors; nothing fancy) without consulting any spec.
- **The 5-impl `ClusterStatsMonitor` and 3-impl `RoutingGroupSelector` plus 5-mode auth are the breadth of the system.** Volume, not depth. A Go interface + impl per variant is the obvious shape.
- **Two extension points (`modules:`, `managedApps:`) are JVM-only.** Drop them in v1 unless `@trino-expert` flags real-world dependence.
- **The `RouteToBackendResource` + pre-match-filter combo is the JAX-RS implementation of "match a path-prefix list and dispatch to a single handler."** In Go this is one `http.HandlerFunc` wired behind a path predicate; do not replicate the rewrite trick.
- **In-process state is small and well-bounded:** per-query-id stickiness map, per-backend last-seen stats, compiled MVEL rules (replacement-needed), pooled JDBC connections. All trivially modeled in Go.
- **Durable state is small:** two tables. Documented in `persistence-and-db-schema.md`.

## Test Strategy Hooks

- **Test level:** This is a survey; not directly testable. See paired QA studies for individual components.
- **Fixtures required:** n/a at this level.
- **Observable signals:** n/a at this level.
- **Non-determinism risks:** n/a at this level.

## Open Questions

- **Exit code 100 on startup failure** (`@trino-expert`): is this a documented operational contract or incidental?
- **`modules:` and `managedApps:` extension points** (`@trino-expert`): are there any known public consumers shipping custom Guice modules or managed apps via these hooks? If yes, the Go rewrite needs an answer; if no, drop them in v1.
- **`extraWhitelistPaths`** (`@trino-expert`): what is the intended use case — a known list of common operator paths to support, or truly arbitrary?

## Cross-references

- `[[proxy-request-lifecycle.md]]` — drill-down on steps 3-6 above
- `[[query-backend-binding.md]]` — drill-down on the `queryId → backend` mapping in step 6
- `[[routing-engine.md]]` — drill-down on `RoutingGroupSelector` and `RoutingManager.provideBackendConfiguration`
- `[[mvel-rules-language.md]]` — JVM-entanglement deep-dive on file-based rules
- `[[sql-parsing-for-routing.md]]` — JVM-entanglement deep-dive on `TrinoQueryProperties`
- `[[cluster-health-monitoring.md]]` — drill-down on `ActiveClusterMonitor` + `ClusterStatsMonitor` variants
- `[[backend-registry-and-mgmt-api.md]]` — drill-down on `GatewayBackendManager` and the `/gateway/*` REST endpoints
- `[[persistence-and-db-schema.md]]` — drill-down on the two DB tables and migrations
- `[[auth-overview.md]]` — drill-down on the auth filter chain and modes
- `[[gateway-cookies.md]]` — drill-down on stickiness, OAuth2 cookies, session
- `[[configuration-model.md]]` — drill-down on the YAML config tree
- `[[jvm-dependencies-inventory.md]]` — full inventory of JVM-ecosystem dependencies
