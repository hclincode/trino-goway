# Feature Gap: Trino Gateway (Java) vs. trino-goway (Go)

> Features present in the upstream **Trino Gateway (Java)** that **trino-goway (Go)** does not have.
> Generated from a full source/doc inventory of both projects. Grouped by significance and
> annotated with whether each gap is a deliberate scope decision or genuinely not-yet-built.

## Core routing / decision-making

| Feature | Java (trino-gateway) | trino-goway (Go) |
|---|---|---|
| **MVEL file-based routing rules** | Condition/action rules engine, priority ordering, state passing, file source + hot-reload (`FileBasedRoutingGroupSelector`, `MVELRoutingRule`) | ❌ Out of scope — external routing service only; `/webapp/getRoutingRules` returns `204` |
| **SQL query parsing (`TrinoQueryProperties`)** | Parses SQL; extracts tables, catalogs, schemas, query type for use in routing rules | ❌ No Go Trino parser; query properties sent empty (headers only) |
| **Built-in adaptive routing** | `QueryCountBasedRouter` (least-loaded-per-user) + `StochasticRoutingManager` (random) | ❌ No built-in LB algorithm — picks any active backend in the group; load logic lives in external router |
| **Resource Groups management** | `resource_groups`, `selectors`, `exact_match_source_selectors` tables + UI pages (resource-group, selector) | ❌ Not implemented; only entity type is `GATEWAY_BACKEND` |
| **Pluggable router SPI / module system** | Custom `RoutingManager` loaded via FQCN modules; `RouterBaseModule` extension point | ❌ No plugin/SPI — extensibility is "run an external routing service" |

## Health checks / monitoring

| Feature | Java | Go |
|---|---|---|
| **JDBC cluster-stats collector** | Health via direct JDBC query execution | ❌ Explicitly rejected at startup ("not supported in v1") |
| **JMX cluster-stats collector** | Monitors Trino JMX attributes | ❌ Explicitly rejected at startup |

Shared collectors at parity: `NOOP`, `INFO_API`, `UI_API`, `METRICS`.

## Persistence / auth

| Feature | Java | Go |
|---|---|---|
| **Oracle database backend** | Supported (Flyway migrations) | ❌ Postgres + MySQL/MariaDB only (no cgo-free Oracle driver) |
| **Form/Basic preset-user auth** | `FormAuthenticator` with preset users/passwords + RSA session tokens | ❌ Only NOOP / OIDC / LDAP; `/login` is a no-op |
| **Opaque-token introspection** | OAuth `tokenInfoUrl` / userinfo exchange with caching | ❌ OIDC validates JWTs via JWKS only (no opaque-token introspection path) |
| **Multi-source user extraction** | header, Basic, Bearer JWT, `Trino-UI-Token` / `__Secure-Trino-ID-Token` cookies | ⚠️ More limited (header/principal-based) |
| **DB caching layer** | `DatabaseCacheConfiguration` — caches backend/history lookups, serves during DB outage | ❌ No fallback cache (per-instance queryId LRU only) |
| **Query-history retention policy** | Configurable retention (default 24h) + `queryHistoryEnabled` toggle | ⚠️ Persists history but no documented auto-purge/retention |

## Observability / proxy / UI

| Feature | Java | Go |
|---|---|---|
| **OpenTelemetry tracing** | OTEL exporter (HTTP/protobuf), tracing | ❌ Prometheus metrics only, no tracing |
| **JMX / Micrometer metrics export** | JMX MBeans per cluster + Micrometer | ❌ Prometheus `trino_goway_*` only |
| **Routing-rules editor UI** | Functional editor page | ❌ Stub page (endpoints return `204`) |
| **UI internationalization** | `webapp/src/locales/` multi-language | ❌ Not present |
| **Custom/extra statement paths** | `extraWhitelistPaths` + configurable statement path list for custom Trino distros | ❌ Fixed paths |
| **V2 request format support** | `RequestAnalyzerConfig` v2 client support | ❌ N/A |
| **Generic (non-OAuth2) sticky cookie** | `GatewayCookie` for queryId stickiness | ⚠️ Only `TG.OAUTH2` cookie + in-memory queryId cache |

## How to read this

The gaps fall into two buckets:

- **Deliberate scope decisions** (documented in `SCOPE.md`): MVEL rules, SQL parsing, JDBC/JMX
  collectors, Oracle, resource groups, per-group DB isolation. The design bet is that all
  query-inspection/routing intelligence lives in an **external routing service** (HTTP or gRPC),
  so the gateway stays a thin, parser-free proxy. trino-goway ships a reference routing-service
  with an `expr`+Starlark engine to fill the MVEL role.
- **Genuinely not-yet-built** (could be added within the architecture): OpenTelemetry tracing,
  form/preset-user auth, DB caching/fallback, query-history retention, opaque-token introspection,
  i18n, resource-groups management.

**Biggest functional difference:** trino-goway cannot make routing decisions based on SQL content
or built-in load metrics on its own — it delegates that to an external service — whereas the Java
gateway has MVEL rules, SQL parsing, and query-count adaptive routing built in.
