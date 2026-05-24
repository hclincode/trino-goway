---
title: Library landscape and Go equivalents
author: architect
role: Architect / Tech Lead
component: trino-gateway
topics:
  - cross-cutting
  - config
  - persistence
  - observability
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/rewrite-hotspots.md
  - trino-gateway/jvm-idioms-not-to-port.md
---

# Library landscape and Go equivalents

## Summary

The Java gateway pulls in roughly thirty significant runtime dependencies, dominated by the Airlift family (Trino's in-house microservice toolkit) and the standard JVM ecosystem (Jackson, Guice, JDBI, Caffeine, Flyway, OkHttp, OIDC SDK). Most of these are *clean-map* — they exist for plumbing concerns that Go solves out of the box or with one well-known library. Three are *no-equivalent* hotspots that the Go port must own: MVEL (the rules expression engine), trino-parser (Trino's SQL grammar and AST), and the Nimbus oauth2-oidc-sdk. See [[rewrite-hotspots]] for the deep dive on those three.

## Key Findings

Source for this inventory: `trino-gateway/gateway-ha/pom.xml:50-300`.

### Airlift family (the Trino-internal microservice toolkit)

Trino-gateway is essentially "an Airlift app". Airlift bundles HTTP server, HTTP client, DI bootstrap, JAX-RS integration, JSON codecs, logging, JMX export, OpenMetrics export, tracing (OpenTelemetry), config validation, and unit types. The whole stack is JVM-specific.

| Airlift module | Purpose in the gateway | Go mapping | Verdict |
|---|---|---|---|
| `bootstrap` | App startup, config loading, lifecycle | Explicit `main` + composition root | drop |
| `http-server` | Jetty-based inbound HTTP server | `net/http` (+ `chi` router) | clean-map |
| `http-client` | Async outbound HTTP client | `*http.Client` (+ optional `golang.org/x/sync/errgroup`) | clean-map |
| `jaxrs` | Wires JAX-RS resources into Jetty | `net/http` handlers + `chi` route mounts | drop (no JAX-RS in Go) |
| `json` | `JsonCodec<T>` typed JSON | `encoding/json` (+ optional `jsoniter` for perf) | clean-map |
| `log` / `log-manager` | Logging facade + file rotation | `log/slog` + `lumberjack` for rotation | clean-map |
| `node` | Node-identity (env, pool, internal addr) | A small struct populated from env | clean-map |
| `concurrent` | Daemon thread factory etc. | Goroutines; no factory needed | drop |
| `units` | `Duration`, `DataSize` typed config | `time.Duration` + a tiny `DataSize` type or `humanize` | clean-map (one tiny custom type) |
| `stats` | Counters/timers exposed via JMX | Prometheus client (`prometheus/client_golang`) | clean-map |
| `jmx` / `jmx-http` | JMX MBean export + HTTP scrape | Drop entirely; Prometheus already covers it | drop |
| `openmetrics` | OpenMetrics-format scrape endpoint | Prometheus `/metrics` endpoint | clean-map |
| `tracing` | OpenTelemetry tracer init | `go.opentelemetry.io/otel` | clean-map |

### Generic JVM ecosystem

| Library | Purpose | Go mapping | Verdict |
|---|---|---|---|
| `com.google.inject:guice` | DI container | Explicit constructor wiring in `main`; optional `wire` (compile-time) | drop |
| `com.google.guava:guava` | Collections, `Suppliers.memoizeWithExpiration`, hashing | Standard library + small helpers; `singleflight` for memoization | clean-map (idiom shift) |
| `com.fasterxml.jackson.*` (databind, yaml, annotations) | YAML/JSON binding | `gopkg.in/yaml.v3` + `encoding/json` | clean-map |
| `com.github.ben-manes.caffeine:caffeine` | In-process cache with TTL/size eviction | `dgraph-io/ristretto` or `hashicorp/golang-lru/v2/expirable` | clean-map |
| `org.jdbi:jdbi3-core` + `jdbi3-sqlobject` | SQL DSL with annotated DAO interfaces | `database/sql` + `sqlx`; or `sqlc` for codegen | clean-map (idiom shift, see [[jvm-idioms-not-to-port]]) |
| `org.flywaydb:flyway-*` | Versioned SQL schema migrations | `golang-migrate/migrate` (drives the same V?__name.sql files) | clean-map |
| `com.squareup.okhttp3:okhttp-jvm` | HTTP client (used for some control-path callouts) | Already covered by `net/http`; nothing extra needed | clean-map (collapse) |
| `org.apache.commons:commons-pool2` | Generic object pool | Not commonly needed in Go; per-use sync.Pool or per-component pool | clean-map (drop if unused after pruning) |
| `org.weakref:jmxutils` | JMX export helpers | Drop with rest of JMX | drop |
| `org.glassfish.jersey.core:jersey-server` | JAX-RS implementation | Drop with rest of JAX-RS | drop |

### Auth / crypto

| Library | Purpose | Go mapping | Verdict |
|---|---|---|---|
| `com.auth0:java-jwt` + `jwks-rsa` | JWT verification + JWKS fetch | `golang-jwt/jwt/v5` + small JWKS fetcher (or `MicahParks/keyfunc`) | clean-map |
| `com.nimbusds:nimbus-jose-jwt` | JOSE primitives | `go-jose/go-jose/v4` | clean-map |
| `com.nimbusds:oauth2-oidc-sdk` (`jdk11` classifier, v11.37.1) | Full OIDC client (discovery, code flow, token endpoint, userinfo, PKCE) | `coreos/go-oidc/v3` + manual code for the corner cases | **loose-map**, see [[rewrite-hotspots]] |
| `org.bouncycastle:bcprov-jdk18on` | Crypto provider | Standard library `crypto/*` (+ `golang.org/x/crypto` for non-FIPS algos) | clean-map |
| `org.apache.directory.api:api-all` | LDAP client | `go-ldap/ldap/v3` | clean-map |

### JDBC drivers (runtime-loaded based on `jdbcUrl` scheme)

| Driver | Go equivalent |
|---|---|
| `org.postgresql:postgresql` | `jackc/pgx/v5` (+ `pgx/v5/stdlib` for `database/sql`) |
| `com.mysql:mysql-connector-j` | `go-sql-driver/mysql` |
| `com.oracle.database.jdbc:ojdbc11-production` | `sijms/go-ora/v2` (pure-Go) or `godror/godror` (cgo, official) |
| (H2 — test-only) | none needed; tests can use Postgres-in-Docker via testcontainers-go |

### Trino-internal

| Library | Purpose | Go mapping | Verdict |
|---|---|---|---|
| `io.trino:trino-parser` (v481) | ANTLR-generated Trino SQL grammar + AST + visitor | **no-equivalent**, see [[rewrite-hotspots]] |
| `io.trino:trino-jdbc` (v481, test scope only — for testcontainers-trino setup) | Trino JDBC client | `trinodb/trino-go-client` for any direct Trino calls; not in data path |
| `io.trino:trino-client` (test scope) | Trino client protocol structs | `trinodb/trino-go-client` |
| `io.airlift:aircompressor-v3` (zstd for prepared statements) | Decompresses `$zstd:` prepared statement headers | `klauspost/compress/zstd` | clean-map |

### Expression engine

| Library | Purpose | Go mapping | Verdict |
|---|---|---|---|
| `org.mvel:mvel2` (v2.5.2) | Java-syntax expression compiler + evaluator used for routing rules | **no-equivalent**, see [[rewrite-hotspots]] |

## Behavior vs. Implementation Artifact

### Airlift `Bootstrap`-driven configuration property model
- **Observed behavior:** Server-tuning properties are passed as a flat `Map<String, String>` from YAML's `serverConfig:` key into Airlift `Bootstrap.setRequiredConfigurationProperties` (`HaGatewayLauncher.java:73`). Modules then declare typed config classes that Airlift validates via JSR-380 annotations.
- **Source of behavior:** `jvm-artifact`. Airlift's split between "structured top-level YAML" and "flat properties for server tuning" is its own convention.
- **Go obligation:** `drop`. A Go config is a single typed struct loaded from YAML. Server tuning (e.g. read/write timeouts) becomes typed fields, not a string map. Document the renamed config keys for operators migrating from the Java gateway.

### OkHttp-based HTTP client mixed with Airlift HTTP client
- **Observed behavior:** Most outbound calls go through Airlift's `HttpClient`; a few control-path calls use OkHttp directly (e.g. `UiApiCookieJar`, `ClusterStatsHttpMonitor`).
- **Source of behavior:** `defensive-historical`. OkHttp is more ergonomic for cookie-jar-style flows; Airlift HttpClient wasn't easy to use for that. Inconsistency was the path of least resistance in Java.
- **Go obligation:** `replicate-intent`. Single `*http.Client` family in Go; cookie handling via `net/http/cookiejar` if needed. Do not preserve the OkHttp-vs-Airlift split.

## Implications for Go Rewrite

- **Library:** Recommended initial dep set (subject to revision):
  - HTTP: `net/http` + `github.com/go-chi/chi/v5`
  - Reverse proxy: `net/http/httputil.ReverseProxy` as the basis, with custom `Director` and `ModifyResponse` for queryId binding and header rewriting
  - Config: `gopkg.in/yaml.v3` + a single typed struct tree mirroring `HaGatewayConfiguration`
  - DB: `database/sql` + `jmoiron/sqlx` (idiomatic for our small schema) + `jackc/pgx/v5/stdlib` + `go-sql-driver/mysql` (+ Oracle driver only if Oracle support is in scope — confirm with @architect)
  - Migrations: `github.com/golang-migrate/migrate/v4` reading the *existing* `V?__*.sql` files from the Java repo's resources (subject to verifying they're compatible Postgres/MySQL syntax without Flyway-specific extensions)
  - Cache: `hashicorp/golang-lru/v2/expirable` for the queryId→backend caches; `ristretto` if size-bounded eviction becomes important
  - Auth: `golang-jwt/jwt/v5`, `coreos/go-oidc/v3`, `go-ldap/ldap/v3`
  - Metrics: `prometheus/client_golang`
  - Tracing: `go.opentelemetry.io/otel`
  - Logging: `log/slog` (standard library)
  - JSON: `encoding/json` (no need for a third-party serializer yet)
  - YAML: `gopkg.in/yaml.v3`
  - Zstd (for `$zstd:` prepared statement decoding): `klauspost/compress/zstd`
- **Interface:** Each of the above slots into a single interface owned by its component. The data path declares `Cache[K, V]`, `BackendRegistry`, `QueryBinder`, `BackendSelector`, `RulesEvaluator`, `HealthProbe`, `Authenticator`, `Authorizer` — keeping the third-party library types out of cross-package signatures. This is the standard Go "small interfaces at consumption points" idiom.
- **Concurrency:** Most libraries listed are concurrency-safe. The two that need care: `*http.Client` should be a singleton per traffic class (cheap; no DI needed), and `database/sql` connection pool sizing should be explicit at startup (`db.SetMaxOpenConns`, `db.SetMaxIdleConns`) rather than left at defaults.

## Test Strategy Hooks

- See paired QA studies: [[test-infrastructure-inventory]], [[go-test-pyramid]].
- Library-choice test concern: any library marked `loose-map` (the OIDC SDK in particular) needs differential tests against the Java gateway's behaviour on a recorded HTTP corpus before sign-off. See [[rewrite-hotspots]] for the per-library test plan.

## Open Questions

- @architect (self): is Oracle support in scope for the Go rewrite? Affects whether we pull in `godror` (cgo) or settle for a pure-Go driver with reduced fidelity. Need a product call.
- @qa-tech-lead: are we OK driving Flyway-formatted migrations through `golang-migrate`, or do we want to keep Flyway as a build-time tool and have the Go service just expect the schema to exist?
- @trino-expert: does the gateway use anything from `io.airlift:aircompressor-v3` beyond zstd decompression of prepared statement headers? Affects whether we need just `klauspost/compress/zstd` or also `klauspost/compress/snappy`/`lz4`.

## Cross-references

- [[architecture-overview.architect.md]] — where each library plugs into the data path
- [[rewrite-hotspots.md]] — deep dive on the three no-equivalent / loose-map libraries
- [[jvm-idioms-not-to-port.md]] — Guice, JDBI SQL Objects, JAX-RS resource classes
- [[config-coupling-depth.architect.md]] — how the YAML config maps to typed structs
