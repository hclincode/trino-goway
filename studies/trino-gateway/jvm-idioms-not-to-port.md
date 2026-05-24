---
title: JVM-idiomatic patterns that should NOT be ported to Go
author: architect
role: Architect / Tech Lead
component: trino-gateway
topics:
  - cross-cutting
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/library-landscape-go-mapping.md
  - trino-gateway/concurrency-model.architect.md
---

# JVM-idiomatic patterns that should NOT be ported to Go

## Summary

The Java gateway leans heavily on a small set of JVM idioms — Guice DI, JAX-RS resource classes, JDBI SQL Objects, `@PostConstruct`/`@PreDestroy`, `ListenableFuture` chains, JMX-exported MBeans, exception-as-control-flow, runtime FQCN class loading — that each have idiomatic Go alternatives. Mechanically translating these one-to-one would produce un-idiomatic Go code that is hard to review and harder to maintain. This study enumerates each pattern, explains what it's solving, and prescribes the Go alternative. The goal is to give the Go Implementer a single reference so they don't have to relitigate these decisions per file.

## Key Findings

### 1. Guice dependency injection
- **Where it appears:** every `@Inject` annotation in `io/trino/gateway/**`. `HaGatewayProviderModule` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java:72-198`) is the central wiring point; `BaseApp.configure` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/baseapp/BaseApp.java:121-143`) installs sub-modules.
- **What it's solving:** runtime composition of singletons from a tree of `@Provides` factories, with constructor injection for non-singletons. Lets the same module set be reused for tests with stubbed providers.
- **Go alternative:** **explicit constructor wiring in `main`** (sometimes called "compose root" or "constructor injection by hand"). For our scale (~30 singletons), the composition root is ~150 lines. Crucially:
  - Do **not** introduce a Go DI container (Wire, fx, dig). For a single-process service of this size, runtime DI containers obscure dependencies; compile-time codegen DI (Wire) is overkill.
  - Define each component's dependencies as constructor arguments. Build them top-down in `main`.
  - For tests, construct the component-under-test directly with stubbed dependencies — no module override system needed.
- **Why this matters:** new Go contributors should be able to trace dependency flow by reading `main.go`. A DI container hides it behind reflection and configuration.

### 2. JAX-RS resource classes
- **Where it appears:** every class annotated `@Path` / `@GET` / `@POST` etc. — `RouteToBackendResource`, `GatewayResource`, `LoginResource`, `HaGatewayResource`, `EntityEditorResource`, `GatewayHealthCheckResource`, `PublicResource`, `GatewayWebAppResource`, `GatewayViewResource`. Plus filters annotated `@PreMatching` / `@Provider`.
- **What it's solving:** declarative HTTP routing tied to typed handler methods. The container does path matching, parameter binding (`@PathParam`, `@QueryParam`, `@Context HttpServletRequest`, `@FormParam`, `@HeaderParam`), content-type negotiation, and async dispatch (`@Suspended`).
- **Go alternative:** **`net/http` handlers with a router (`chi` recommended) and explicit binding**. Each JAX-RS resource class becomes a Go struct with `RegisterRoutes(r chi.Router)` method that mounts its handlers on the router. Each handler is `func(http.ResponseWriter, *http.Request)`. Parameter extraction is explicit (`chi.URLParam(r, "id")`, `r.URL.Query().Get("k")`, `r.Header.Get("X-Foo")`). No annotations.
- **Why this matters:** Go's HTTP idiom is explicit handlers, not annotation-driven dispatch. Trying to recreate JAX-RS via reflection in Go would fight the language.

### 3. JDBI SQL Objects (annotated DAO interfaces)
- **Where it appears:** `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/dao/` — `GatewayBackendDao`, `QueryHistoryDao`. SQL is in `@SqlQuery` / `@SqlUpdate` annotations on interface methods; JDBI implements the interface at runtime.
- **What it's solving:** type-safe SQL with parameter binding by name and result mapping into POJOs.
- **Go alternative:** **`database/sql` + `sqlx` for ad-hoc queries**, or **`sqlc` for codegen** if we want compile-time-checked SQL. For our 2-table schema, `sqlx` is the right size. Pattern: each DAO struct holds `*sqlx.DB`; methods are explicit `func (d *GatewayBackendDao) Insert(ctx, ...) error { _, err := d.db.NamedExecContext(ctx, "INSERT ...", ...); return err }`.
- **Why this matters:** Go's DB idiom is explicit queries, not annotation-magic'd interfaces. Trying to recreate JDBI via reflection (a real Go library called `gorm` does this) would produce slow, unidiomatic code and obscure error messages.

### 4. `@PostConstruct` / `@PreDestroy` lifecycle
- **Where it appears:** `ActiveClusterMonitor.start()` and `.stop()` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/clustermonitor/ActiveClusterMonitor.java:62-100`); `ProxyRequestHandler.shutdown()` (`ProxyRequestHandler.java:115-119`); plus several others.
- **What it's solving:** Guice-managed lifecycle hooks that the container invokes after construction and before shutdown.
- **Go alternative:** **explicit `Start(ctx context.Context) error` / `Stop(ctx context.Context) error` methods called by the composition root**. The composition root maintains a `[]Lifecycle` slice; `main` calls `Start` on each, blocks on a signal channel, then calls `Stop` on each in reverse order.
- **Why this matters:** lifecycle as data is clearer than lifecycle as annotation. New contributors can grep for `Start(ctx)` calls.

### 5. `ListenableFuture` `.transform(...)` chains
- **Where it appears:** `ProxyRequestHandler.performRequest` (`ProxyRequestHandler.java:188-202`). The async pipeline reads as `executeHttp(...).transform(recordBackend, executor).transform(buildResponse, executor).catching(handleProxyException, directExecutor())`.
- **What it's solving:** non-blocking composition of async operations on a JAX-RS suspended response, with explicit error handlers.
- **Go alternative:** **straight-line code on a single goroutine per request**. Each request's handler runs the full pipeline; `*http.Client` is synchronous from the caller's perspective. Errors are returned, not caught via continuation.
- **Why this matters:** Go's concurrency model is "block the goroutine, that's cheap". Continuation-passing style in Go (e.g. callback chains with channels) is much harder to read than straight-line code. The Java code uses CPS because Jetty threads are expensive to block; Go goroutines are not.

### 6. JMX-exported MBeans
- **Where it appears:** anything bound via `org.weakref.jmx.guice.ExportBinder.newExporter(binder).export(X.class).withGeneratedName()` — e.g. `ProxyHandlerStats` (`BaseApp.java:136`). The classes carry `@Managed` annotations on getters.
- **What it's solving:** runtime exposure of counters/timers as JMX MBeans, scrapable via JMX HTTP or JMX/RMI.
- **Go alternative:** **Prometheus metrics registered with the default registry, exposed at `/metrics`**. Use `prometheus.NewCounterVec`, `prometheus.NewHistogramVec`, etc. Drop the JMX export path entirely; it doesn't fit Go's monitoring ecosystem.
- **Why this matters:** modern observability is Prometheus/OpenMetrics-based; JMX is a JVM-specific protocol. Operators running a Go service expect `/metrics`, not JMX-over-HTTP.

### 7. Exception-as-control-flow
- **Where it appears:** several places throw exceptions for non-error conditions:
  - `WebApplicationException` is thrown from `ProxyRequestHandler.handleProxyException` to signal a 502 response (`ProxyRequestHandler.java:254-267`)
  - `RequestParsingException` is thrown inside `TrinoQueryProperties` visitor to signal "could not extract identifier" — caught and converted to an `errorMessage` field (`TrinoQueryProperties.java:498-501`)
  - `HaGatewayConfigurationException` thrown from setter validators (`HaGatewayConfiguration.java:280-298`)
- **What it's solving:** non-local exit from deep call stacks where the caller chain wasn't designed to return errors.
- **Go alternative:** **explicit `error` returns**. Where the Java code uses an exception as a "return value with extra steps", Go uses an `error`. For HTTP-level errors, write to `http.ResponseWriter` and `return`; do not panic. Panics are reserved for unrecoverable bugs.
- **Why this matters:** Go's error idiom is values, not stack-unwinding. The JAX-RS `WebApplicationException` pattern doesn't translate; there's no container catching it.

### 8. Runtime FQCN class loading (`modules:`, `managedApps:`)
- **Where it appears:** `BaseApp.addModules` and `BaseApp.addManagedApps` (`BaseApp.java:69-106, 145-166`).
- **What it's solving:** runtime extension — operators add their own JAR + YAML entry, no rebuild needed.
- **Go alternative:** **build-time inclusion**. Go does not support classpath-style runtime loading without `plugin.Open` (which has serious caveats: same-Go-version, same-build-flags, Linux/Mac-only, no Windows). Don't go there. Instead:
  - For v1 of the Go port: document that custom modules require building from source. Provide stable extension interfaces (`type RoutingPlugin interface { ... }`).
  - For larger extensions: use the external-routing HTTP transport that already exists (`byRoutingExternal`) so operators can run their custom logic as a sidecar.
- **Why this matters:** plugin-style hot extension is the wrong shape for Go services. Be explicit about the migration path so operators with custom modules are not surprised.

### 9. Guice `Multibinder<Set<X>>`
- **Where it appears:** `HaGatewayProviderModule.configure` binds `Set<TrinoClusterStatsObserver>` via `Multibinder` (`HaGatewayProviderModule.java:88-90`). Consumers (`ActiveClusterMonitor`) `@Inject Set<TrinoClusterStatsObserver>` and iterate.
- **What it's solving:** plugin-style fanout where multiple unrelated components want to observe the same event.
- **Go alternative:** **explicit `[]Observer` slice constructed in the composition root**. The component that publishes events takes a `[]Observer` (or a single `Observer` that internally fans out). For our two-observer case, this is trivial.
- **Why this matters:** the multibinder is a DI container feature; without a container, the pattern collapses to a plain slice.

### 10. Static singleton `*PropertiesProvider` for non-DI-managed classes
- **Where it appears:** `GatewayCookieConfigurationPropertiesProvider.getInstance()` (`HaGatewayProviderModule.java:105-109`). Used because `GatewayCookie` is a value object Jackson constructs, not a Guice singleton — but it needs the cookie-signing key.
- **What it's solving:** giving Jackson-constructed value objects access to runtime config.
- **Go alternative:** **pass the config explicitly to whoever constructs the value object**. For cookie signing, build a `*CookieSigner` once in the composition root and have the handler use it to sign/verify cookies. Value objects (the cookie payload itself) hold only data, no signing logic.
- **Why this matters:** singletons-via-static are global state by another name. Go has the same anti-pattern (`var x = ...`), but we should resist it here.

## Behavior vs. Implementation Artifact

### Default `RoutingManager` via `OptionalBinder.setDefault`
- **Observed behavior:** `BaseApp.configure` binds `RoutingManager` via `newOptionalBinder(binder, RoutingManager.class).setDefault().to(StochasticRoutingManager.class)` (`BaseApp.java:139-142`). This means a downstream module loaded via `modules:` YAML can override the default by binding `RoutingManager` to a different impl.
- **Source of behavior:** `gateway-design-intent`. The override hook is documented for users who want a non-stochastic routing manager (e.g. the `QueryCountBasedRouter` provider that the Java code already ships, in `module/QueryCountBasedRouterProvider.java`).
- **Go obligation:** `replicate-intent`. In Go, this is an explicit selector in the composition root — read a config field, pick an impl, inject it. The configuration becomes the override mechanism; not a runtime-loaded module.

### `RolesAllowedDynamicFeature` for `@RolesAllowed`-driven authz
- **Observed behavior:** `BaseApp.registerAuthFilters` registers `RolesAllowedDynamicFeature` (Jersey-provided) so that JAX-RS handler methods annotated `@RolesAllowed("ADMIN")` are gated automatically (`BaseApp.java:181-184`).
- **Source of behavior:** `jvm-artifact`. Jersey-specific declarative authz.
- **Go obligation:** `drop`. In Go, write a middleware `RequireRole("admin", handler)` and wrap each handler at registration time. No annotation magic.

## Implications for Go Rewrite

- **Library:** none of these patterns require a third-party library. Go's standard library plus `chi` and `sqlx` cover them all.
- **Interface:** the major shape that emerges from following the rules above:
  - `main.go` builds the dependency graph explicitly: ~150 lines of `x := NewX(deps); y := NewY(x, deps); ...`
  - Each component exposes `Start(ctx) error` / `Stop(ctx) error` where applicable
  - HTTP routing lives in `RegisterRoutes(r chi.Router)` methods, not annotations
  - DAOs are explicit method-on-struct, not interface-with-annotations
  - Async behaviour is straight-line code, not future chains
  - Metrics are Prometheus, not JMX
  - Errors are `error` values, not exceptions
- **Concurrency:** as documented in [[concurrency-model.architect]], goroutine-per-request + a small number of long-running goroutines for ticks. No thread pools, no executors.

## Test Strategy Hooks

- See paired QA studies: [[test-infrastructure-inventory]], [[go-test-pyramid]].
- Pattern-specific test concerns:
  - **Composition root tests:** unit-test the composition root by constructing it with a no-op config and verifying every `Start` succeeds and every `Stop` is idempotent. Catches lifecycle ordering bugs early.
  - **HTTP handler tests:** standard `httptest.NewRecorder` + `httptest.NewRequest`. No JAX-RS test container.
  - **DAO tests:** `database/sql` + a `testcontainers-go/postgres` instance per test (or per-suite for speed). Same fidelity as the Java JDBI + testcontainers setup.

## Open Questions

- @architect (self): is there value in a single typed "lifecycle" registration helper (e.g. `app.Register(component)` that captures the `Start`/`Stop` pair into a slice), or is a hand-written `start()` / `stop()` in `main.go` sufficient? For ~10 components, hand-written wins on clarity.
- @qa-tech-lead: do we want a single test-only composition-root helper that builds the full graph with sensible test defaults? Likely yes — every integration test benefits.

## Cross-references

- [[architecture-overview.architect.md]] — overall data path these patterns implement
- [[library-landscape-go-mapping.md]] — library choices that enable these idiom replacements
- [[concurrency-model.architect.md]] — `Start(ctx)` / `Stop(ctx)` lifecycle pattern in detail
