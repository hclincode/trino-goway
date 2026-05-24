---
title: JVM dependencies inventory and Go-rewrite impact
author: java-analyst
role: Java Analyst
component: trino-gateway
topics: [cross-cutting]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to: [architecture-overview.md]
---

# JVM dependencies inventory and Go-rewrite impact

## Summary

This file catalogues every runtime dependency in `gateway-ha/pom.xml` and classifies each as either a **contract dependency** (its behavior is part of the user-facing contract and a Go replacement must match it) or an **implementation dependency** (purely internal — the Architect picks any Go equivalent). The two most worrying entries are `mvel2` (user-authored rule expressions) and `trino-parser` (SQL inspection feeding routing decisions). Everything else has a serviceable Go equivalent or is pure assembly that can be dropped without behavioral consequence.

## Key Findings

### Classification system

For each dependency I record:
- **Role:** what it does in the gateway.
- **Class:** `contract` (observable to users — config syntax, wire format, on-disk format, or security primitive) vs. `implementation` (internal plumbing the user never sees).
- **Go-side concern:** what the Architect needs to think about.

### Compile/runtime dependencies (from `trino-gateway/gateway-ha/pom.xml`)

#### Airlift platform (`io.airlift:*`)

`bootstrap`, `concurrent`, `http-client`, `http-server`, `jaxrs`, `jmx`, `jmx-http`, `json`, `log`, `log-manager`, `node`, `openmetrics`, `stats`, `tracing`, `units`, `aircompressor-v3` (`pom.xml:111-190`).

- **Role:** Airlift is Trino's in-house service framework. `bootstrap` wires Guice; `http-server` is Jetty packaged with config plumbing; `http-client` is a Jetty-based async HTTP client; `jaxrs` is Jersey wired into the server; `json` is Jackson wired with codec helpers; `log`/`log-manager` is logging; `jmx`/`jmx-http`/`openmetrics`/`stats` is metrics export; `tracing` is OTel-style spans; `units` is duration/data-size types. `aircompressor-v3` is a JVM-native compression library; the gateway uses it specifically to zstd-decompress `X-Trino-Prepared-Statement` header values.
- **Class:** All `implementation` except `aircompressor-v3` (`contract`-adjacent — zstd is a wire-format detail of how the Trino client may have compressed a header that the gateway must parse).
- **Go-side concern:** The Architect should pick standard Go equivalents — `net/http` + `chi`/`gin`/`fiber` for HTTP, `slog` for logging, `prometheus/client_golang` or OpenMetrics for metrics, `go.opentelemetry.io/otel` for tracing, `encoding/json` for JSON. The zstd codec needs `github.com/klauspost/compress/zstd` (mature, widely used). No spec output is needed from me on these — they're internal to the runtime.

#### Dependency injection — Guice (`com.google.inject:guice` + multibindings)

- **Role:** Wires the application graph. Provider methods choose monitor/auth/routing-selector implementations from config.
- **Class:** `implementation`.
- **Go-side concern:** Pick a Go DI strategy (most likely hand-rolled constructors, possibly `google/wire` for compile-time DI). Zero behavioral consequence — spec output for individual components describes intended wiring conditions in plain prose.

#### JAX-RS / Jersey (`jakarta.ws.rs-api`, `org.glassfish.jersey.core:jersey-server`)

- **Role:** HTTP resource framework (`@Path`, `@GET`, `@Suspended AsyncResponse`, etc.) and the pre-match container request filter mechanism the gateway uses to redirect proxied paths to one resource class.
- **Class:** `implementation`.
- **Go-side concern:** Any Go HTTP router with middleware support. The "pre-match URI rewrite" pattern collapses to a path predicate + dispatch handler.

#### Servlet API (`jakarta.servlet:jakarta.servlet-api`)

- **Role:** `HttpServletRequest`/`HttpServletResponse` interfaces used in the handler layer for raw request introspection.
- **Class:** `implementation`.
- **Go-side concern:** Use `*http.Request` / `http.ResponseWriter`.

#### Configuration deserialization — Jackson + YAML (`jackson-core`, `jackson-databind`, `jackson-annotations`, `jackson-dataformat-yaml`)

- **Role:** Deserializes the YAML config file into Java classes; also deserializes JSON bodies in REST endpoints and proxied responses (for `id` extraction).
- **Class:** `contract` (for YAML config — operators write the config file and the field shape is a documented contract); `implementation` (for internal JSON).
- **Go-side concern:** Use `gopkg.in/yaml.v3` for config; `encoding/json` for JSON. Field naming convention must match (Jackson's default is camelCase Java fields → camelCase YAML keys; verify against actual config files). Environment-variable interpolation (`${ENV_VAR}` syntax) in YAML is handled by the gateway's own `ConfigurationUtils.replaceEnvironmentVariables`, not Jackson — Go side replicates this manually.

#### Validation (`jakarta.validation-api`)

- **Role:** Bean-validation annotations on config classes (e.g., `@NotNull`).
- **Class:** `implementation`.
- **Go-side concern:** `github.com/go-playground/validator` or hand-rolled validation in config-load.

#### Persistence — JDBI + Flyway + JDBC drivers (`org.jdbi:jdbi3-core`, `jdbi3-sqlobject`, `org.flywaydb:flyway-core` + `flyway-mysql`/`flyway-database-postgresql`/`flyway-database-oracle`, `mysql-connector-j`, `org.postgresql:postgresql`, `com.oracle.database.jdbc:ojdbc11-production`, `io.trino:trino-jdbc`)

- **Role:** JDBI is a SQL-mapping layer over JDBC (annotation-driven DAOs in `dao/`). Flyway runs migrations from `V*__*.sql` files in `src/main/resources/{mysql,postgresql,oracle}/`. Three JDBC drivers are bundled at runtime (MySQL, PostgreSQL, Oracle); `trino-jdbc` is included specifically for `ClusterStatsJdbcMonitor` (the gateway queries `system.runtime.queries` over JDBC for health stats).
- **Class:** `contract` — the table schema, the migration history, and the set of supported datastores are all user-visible commitments.
- **Go-side concern:** The schema is small (2 tables, see `persistence-and-db-schema.md`) and the migration history is short (V1-V4). The Go side needs (a) a SQL library — `database/sql` with `jmoiron/sqlx`, or `sqlc`/`ent`/`bun` for richer ergonomics; (b) a migration tool — `golang-migrate/migrate` is the conventional pick, but it would need to ingest the existing Flyway-style files OR the team accepts that migrations are forked. **Note:** Flyway-style `V1__name.sql` files are not directly migrate-compatible (migrate uses `1_name.up.sql` / `.down.sql`); the schema can be migrated to migrate's format mechanically. The Go side also needs three database drivers — `go-sql-driver/mysql`, `lib/pq` or `jackc/pgx`, and `sijms/go-ora/v2` (Oracle). For Trino JDBC queries in the JDBC monitor: the Trino HTTP statement protocol is directly usable from Go (no JDBC dependency needed) — see `cluster-health-monitoring.md`.

#### Connection pooling (`org.apache.commons:commons-pool2`)

- **Role:** Generic object pooling used by `LbLdapClient` (LDAP connection pool).
- **Class:** `implementation`.
- **Go-side concern:** LDAP libraries (e.g., `go-ldap/ldap/v3`) include their own pooling story.

#### Cache (`com.github.ben-manes.caffeine:caffeine`)

- **Role:** In-memory cache used by `BaseRoutingManager` for backend selection (per `BaseRoutingManager.java:280` line-count, ~280 LOC) and elsewhere for short-lived caches.
- **Class:** `implementation`.
- **Go-side concern:** `dgraph-io/ristretto` or `hashicorp/golang-lru` or a plain `sync.Map` with TTL eviction. The cache semantics (TTL, max size, eviction policy) must be documented per use site — see component-specific studies.

#### Guava (`com.google.guava:guava`)

- **Role:** Pervasive: `ImmutableList`/`ImmutableMap` collections, `FluentFuture` (for async response composition in `ProxyRequestHandler`), `Streams`.
- **Class:** `implementation`.
- **Go-side concern:** Standard Go collections + goroutines / channels (or `golang.org/x/sync/errgroup`). The `FluentFuture.transform(...).catching(...)` pattern in `ProxyRequestHandler` translates to plain function composition in Go.

#### Routing-rule expression engine — MVEL2 (`org.mvel:mvel2:2.5.2.Final`)

- **Role:** MVEL ("MVFLEX Expression Language") compiles and evaluates expressions in user-authored routing-rule YAML files. Each rule's `condition:` and `actions:` are MVEL expression strings evaluated against an in-process context map containing request properties and parsed query properties.
- **Class:** **`contract` — high impact.** Operators write MVEL expressions in their on-disk routing-rule files. There is no public Go port of MVEL.
- **Go-side concern:** **Blocker for true drop-in.** Options:
  1. Replace MVEL with an embedded Go expression language. The well-supported candidates are CEL (`google/cel-go`, type-checked, well-documented), `expr-lang/expr` (more dynamic, closer in feel to MVEL), or `antonmedv/expr` (now the same project as expr-lang). Operators rewrite their rule files; v1 ships a migration guide and possibly a converter.
  2. Embed a JVM-based rule evaluator as a sidecar process. Rejected on principle — defeats the rewrite.
  3. Drop user-authored rule files in v1, support only header-based and external-HTTP routing. Aggressive but feasible.
- See `[[mvel-rules-language.md]]` for the contract surface so the Architect can choose a replacement informedly.

#### Trino SQL parser (`io.trino:trino-parser:481`)

- **Role:** ANTLR-generated Trino SQL parser. The gateway uses it in `TrinoQueryProperties` (716 LOC) to parse statement bodies and extract `queryType` (SELECT/INSERT/CREATE_TABLE/etc.), `tables`, `schemas`, `catalogs`, `defaultSchemas`, `defaultCatalogs`. These extracted fields are inputs to routing rules.
- **Class:** **`contract` — high impact.** Rules can reference fields that only exist because the SQL was parsed. Operators can write a rule `trinoQueryProperties.queryType == "SELECT"` and it must evaluate identically.
- **Go-side concern:** **Blocker.** Options:
  1. Port the Trino ANTLR grammar to ANTLR-Go (`antlr/antlr4`). Significant effort; risk of subtle divergence as Trino's grammar evolves between versions.
  2. Implement a much smaller, behavior-focused parser in Go targeting only the AST fields actually consumed (`queryType`, `tables`, `schemas`). Possible because the gateway uses a narrow subset — confirmed by reading `TrinoQueryProperties.java`.
  3. Embed Trino's SQL parsing as a sidecar HTTP service backed by the real Java library. Most-faithful, runtime-heaviest.
  4. Drop SQL-aware routing in v1, support only header/external routing. Aggressive.
- See `[[sql-parsing-for-routing.md]]` for the actual set of fields extracted, so the Architect can scope.

#### LDAP (`org.apache.directory.api:api-all:2.1.7`)

- **Role:** Apache Directory LDAP API used by `LbLdapClient` for LDAP bind/search authentication.
- **Class:** `implementation` (the LDAP protocol itself is the contract; the Java library is just one client implementation).
- **Go-side concern:** `go-ldap/ldap/v3` is the standard Go LDAP client. Config-surface (bind DN template, search filter, etc.) must match the existing `LdapConfiguration` shape.

#### JWT and OAuth2/OIDC (`com.auth0:java-jwt:4.5.1`, `com.auth0:jwks-rsa:0.24.0`, `com.nimbusds:nimbus-jose-jwt:10.9`, `com.nimbusds:oauth2-oidc-sdk:11.37.1`)

- **Role:** Two parallel JWT/OAuth2 stacks present. auth0's `java-jwt` + `jwks-rsa` for issuing/parsing internal session JWTs (`LbKeyProvider`, `LbTokenUtil`). Nimbus's `nimbus-jose-jwt` and `oauth2-oidc-sdk` are used by `LbOAuthManager` for OAuth2/OIDC flow against external IdPs (`LbOAuthManager.java` is the larger file at 212 LOC).
- **Class:** `implementation` (the JWT format and OAuth2/OIDC protocols are the contracts).
- **Go-side concern:** `golang-jwt/jwt/v5` for JWT, `coreos/go-oidc/v3` + `golang.org/x/oauth2` for OIDC. Both well-supported. The contract is the OAuth2/OIDC protocol shape (authorization endpoint, token endpoint, userinfo endpoint, JWKS URI, redirect URI, scopes) — see `auth-overview.md` (planned).

#### Crypto (`org.bouncycastle:bcprov-jdk18on:1.84`)

- **Role:** BouncyCastle crypto provider; used incidentally by JWT/JWKS code.
- **Class:** `implementation`.
- **Go-side concern:** Go's `crypto/*` stdlib covers everything needed for JWT and OAuth2/OIDC.

#### HTTP client (alternate) — OkHttp (`com.squareup.okhttp3:okhttp-jvm`)

- **Role:** Used by `ExternalRoutingGroupSelector` for HTTP calls to the external routing rules engine (alongside the Airlift HttpClient used elsewhere — investigate why two clients in `[[routing-engine.md]]`).
- **Class:** `implementation`.
- **Go-side concern:** Use `net/http`.

#### JMX exporter (`org.weakref:jmxutils`)

- **Role:** Exposes `ProxyHandlerStats` and `ClusterMetricsStats` via JMX (and `JmxOpenMetricsModule` re-exposes them as Prometheus-format text).
- **Class:** `implementation` (the JMX surface itself is observable, but operators consume the OpenMetrics text endpoint in practice).
- **Go-side concern:** Wire metrics directly to OpenMetrics / Prometheus in Go — JMX is not needed. The exported metric names must match what operators scrape; document and preserve them.

#### Servlet/JAX-RS supporting (`jakarta.annotation-api`)

- **Role:** `@PreDestroy`, `@PostConstruct` annotations for lifecycle hooks.
- **Class:** `implementation`.
- **Go-side concern:** Explicit `Start()`/`Stop()` methods or a lifecycle interface.

### Runtime-only dependencies (extra)

- **`io.trino:trino-jdbc:481`** (scope=runtime) — bundled because `ClusterStatsJdbcMonitor` connects to backends via JDBC. **Contract-adjacent:** if operators have configured the JDBC monitor, replacing it with HTTP-statement-based polling changes the on-the-wire interaction with backends (but not what the operator sees in metrics). See `cluster-health-monitoring.md`.

### Test dependencies (informational — no Go obligation)

- JUnit 5, AssertJ, Mockito, testcontainers (mysql, postgresql, oracle-free, trino), h2 (in-process DB for fast tests), docker-java-api, OkHttp `mockwebserver`, `trino-client` (for end-to-end test traffic). Tells us that the existing Java test suite uses real Trino containers and real databases — the QA team should mirror that approach where feasible. Not specifiable by me; flagging for `@qa-tech-lead`.

### Maven plugins worth knowing about

- `maven-shade-plugin` produces a fat-jar (`jar-with-dependencies`) — the gateway distributes as one JAR plus a `config.yaml`. Go ships as a static binary plus config; same operator UX.
- `frontend-maven-plugin` builds the React-ish webapp under `webapp/` using `pnpm`. The output is copied to `target/classes/static/` and served by the gateway. The Go rewrite serves the same built assets via `embed.FS` or a static-file handler — no behavioral change.
- `maven-surefire-plugin` sets `reuseForks=false` "because we call main() in test setups" (`pom.xml:503-505`). This is a JVM artifact — Guice singletons survive across tests unless the JVM is forked. Go tests don't have this problem.

## Behavior vs. Implementation Artifact

### MVEL as the rule expression language
- **Observed behavior:** Operators write routing rules as MVEL expression strings in YAML; the gateway compiles and evaluates them per request (`MVELRoutingRule.java`).
- **Source of behavior:** `gateway-design-intent` — MVEL was deliberately chosen as a user-facing config surface.
- **Rationale:** MVEL is small, dynamic, JVM-embeddable, and was already familiar to the Trino-adjacent ecosystem in the early days of the gateway.
- **Go obligation:** `defer-to-expert`. The Go rewrite must support *some* expression language for rules. Choice is an Architect decision. See `[[mvel-rules-language.md]]` for the contract surface.
- **Notes:** This is the single biggest config-compatibility decision in the project.

### Three bundled JDBC drivers (MySQL, PostgreSQL, Oracle)
- **Observed behavior:** Gateway can use any of three RDBMS at runtime, picked by JDBC URL.
- **Source of behavior:** `gateway-design-intent` — multi-database support is documented.
- **Rationale:** Different orgs have different operational DB preferences.
- **Go obligation:** `replicate-intent`. The Go rewrite should support the same three at minimum. The schema across all three is similar but each has its own `V1__create_schema.sql`/etc. file with vendor-specific types (Oracle's `NUMBER`, MySQL's `MEDIUMTEXT`, Postgres's `TEXT`). See `[[persistence-and-db-schema.md]]`.
- **Notes:** The h2 in-process DB is test-only — not required for runtime.

### Trino SQL parser dependency for routing
- **Observed behavior:** Gateway parses every proxied SQL statement and exposes the parse tree's salient facts (query type, tables, catalogs, schemas) to the routing engine.
- **Source of behavior:** `gateway-design-intent` — the documented "request analyzer" feature.
- **Rationale:** Routing rules need to consult query semantics (e.g., "send all writes to cluster X, reads to cluster Y", "route based on accessed table").
- **Go obligation:** `defer-to-expert`. Architect must choose how to satisfy this in Go. See `[[sql-parsing-for-routing.md]]` for the full contract.
- **Notes:** Behavior is gated by `requestAnalyzerConfig.analyzeRequest` (default-on in some paths) — if disabled, the SQL-parse fields are simply absent from rule context, and operators must avoid referencing them in rules.

### Airlift HttpServer (Jetty under the hood)
- **Observed behavior:** Inbound HTTP termination, including TLS, HTTP/2, request body buffering, async response support.
- **Source of behavior:** `jvm-artifact` — choice of HTTP server is internal.
- **Go obligation:** `drop`. Go's `net/http` covers everything needed. HTTP/2 and TLS are stdlib.

## Implications for Go Rewrite

- **Two contract-shaped JVM dependencies are the only real obstacles to a faithful Go rewrite: MVEL and trino-parser.** Every other dependency is internal plumbing with a serviceable Go equivalent.
- **Both contract dependencies sit in the routing layer.** If routing-rule features are scoped down in v1 (drop file-based rules, support only header + external HTTP), MVEL and trino-parser both become non-issues. This is the single scope lever that most affects rewrite feasibility.
- **The frontend (`webapp/`) is built by an external pnpm pipeline already and copied into `target/classes/static/` at package time.** No JVM coupling — same artifacts work in Go.
- **Flyway migration files are SQL-with-conventional-filenames, not JVM bytecode.** They port trivially to `golang-migrate` after a filename rewrite (V1 = 1_, etc.). The schema content does not change.
- **Three databases × four migrations × small DAO surface = small persistence layer.** Specifiable in one or two files.
- **JMX is replaceable by OpenMetrics; operators already consume the OpenMetrics endpoint.** No JMX needed in Go. Metric names must be preserved (operator scrape configs).
- **No Java-the-language features used by the gateway have no Go equivalent** (reflection-based module loading via `modules:` / `managedApps:` is the closest, and the recommendation is to drop it).

## Test Strategy Hooks

- **Test level:** n/a — this is a dependency catalogue, not a behavior spec.
- See paired QA studies for testability of the components that depend on each library.

## Open Questions

- **`@trino-expert`:** Do public-facing trino-gateway docs commit to the MVEL rule syntax? (If yes, a config-syntax break needs to be called out loudly in v1 release notes. If no, replacement is purely an Architect decision.)
- **`@trino-expert`:** How widely is `ClusterStatsJdbcMonitor` used vs. `InfoApiMonitor`? If JDBC is niche, the Go rewrite can skip it and use HTTP-only monitoring, removing the `trino-jdbc` dependency entirely.
- **`@architect`:** The two HTTP-client choices (Airlift + OkHttp) in the Java code may be incidental. Worth confirming with the next reader of the routing-engine code that there's nothing protocol-specific about OkHttp's use.
- **`@qa-tech-lead`:** The Java test suite uses real Trino containers (`testcontainers-trino`) for integration tests. Should the Go QA strategy mirror this, or use a hand-rolled mock Trino server for speed?

## Cross-references

- `[[architecture-overview.md]]`
- `[[mvel-rules-language.md]]`
- `[[sql-parsing-for-routing.md]]`
- `[[persistence-and-db-schema.md]]`
- `[[cluster-health-monitoring.md]]`
- `[[auth-overview.md]]`
