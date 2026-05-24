---
title: trino-gateway architecture overview (system-design lens)
author: architect
role: Architect / Tech Lead
component: trino-gateway
topics:
  - proxy-core
  - cross-cutting
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/concurrency-model.architect.md
  - trino-gateway/library-landscape-go-mapping.md
  - trino-gateway/jvm-idioms-not-to-port.md
  - trino-gateway/component-build-order.architect.md
---

# trino-gateway architecture overview (system-design lens)

## Summary

trino-gateway is an Airlift-based JVM microservice that fronts one or more Trino coordinators with a single HTTP endpoint. It is structured as a thin reverse proxy with a sticky-by-queryId routing layer, a pluggable routing-group selector, an out-of-band cluster monitor, and a JDBC-backed registry/history store. All non-control traffic flows through one JAX-RS resource that catches every whitelisted path via a pre-match URI rewrite, picks a backend, then issues an async outbound HTTP call. The system-design takeaway: the data path is small and tractable to port; the complexity sits in the routing-decision layer (trino-parser, MVEL) and in the Airlift/Guice scaffolding around it.

## Key Findings

- **Single inbound entry point.** All Trino-protocol traffic is funnelled through `RouteToBackendResource` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouteToBackendResource.java:40-108`). The dispatch trick: `RouterPreMatchContainerRequestFilter` runs as a `@PreMatching` JAX-RS filter that rewrites any whitelisted request URI to `/trino-gateway/internal/route_to_backend` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouterPreMatchContainerRequestFilter.java:30-51`). Method-level handlers (`@POST`/`@GET`/`@DELETE`/`@PUT`/`@HEAD`) on the resource then receive the original request via `@Context HttpServletRequest`.
- **Five-stage data path.** For each routed request the system performs: (a) pre-match URI rewrite, (b) routing-target resolution (`RoutingTargetHandler.resolveRouting`, `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:70-87`), (c) async outbound HTTP via Airlift `HttpClient.executeAsync` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:188-202`), (d) for new POST `/v1/statement` responses, queryId extraction from the JSON body to populate the routing cache (`ProxyRequestHandler.java:269-301`), (e) response synthesis with cookie/header passthrough into the `@Suspended AsyncResponse`.
- **Two routing dimensions, layered.** First dimension picks a *routing group* (header `X-Trino-Routing-Group`, a file-based MVEL rules engine, or an external REST callout — see `RoutingGroupSelector.java:34-58`). Second dimension picks a *cluster* within that group (default `StochasticRoutingManager` is a uniform random pick, `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/StochasticRoutingManager.java:38-46`). Sticky routing for follow-up queryId-bearing requests bypasses both dimensions and goes straight to the cluster the original POST landed on.
- **Out-of-band cluster monitor.** `ActiveClusterMonitor` runs on a single-thread scheduler that ticks every `monitor.taskDelay`, fans out per-backend health checks across a fixed 20-thread pool, then notifies observer multibinder set (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/clustermonitor/ActiveClusterMonitor.java:46-93`). Probe transport is pluggable (INFO_API, UI_API, JDBC, JMX, METRICS, NOOP — `HaGatewayProviderModule.java:179-197`). Decoupled from the data path: a stale `ClusterStats` view is what the routing layer sees between ticks.
- **Persistence is small.** Two tables: `gateway_backend` (5 columns, ~the cluster registry) and `query_history` (6 columns, ~recent routing decisions). Schema is migrated by Flyway with per-DB SQL under `trino-gateway/gateway-ha/src/main/resources/{mysql,postgresql,oracle}/`. JDBI3 + SQL Objects for access (`JdbcConnectionManager.java:34-117`). A 120-minute scheduled task deletes old history entries.
- **Guice is the structural glue.** `HaGatewayProviderModule` wires the singletons (`router/RoutingManager` implementations, `BackendStateManager`, `QueryHistoryManager`, `AuthorizationManager`, `PathFilter`) and uses `@Provides` factories to switch implementations based on config (e.g. `getRoutingGroupSelector` and `getClusterStatsMonitor` are big `switch` expressions on config-enum values — `HaGatewayProviderModule.java:152-197`). `BaseApp` registers all JAX-RS resources via `jaxrsBinder`. Airlift's `Bootstrap` reads `serverConfig` properties and walks the module list to produce the running app (`HaGatewayLauncher.java:51-94`).
- **Configuration is a single fat YAML deserialized into a tree of POJOs.** `HaGatewayConfiguration` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/config/HaGatewayConfiguration.java:26-299`) aggregates ~20 sub-config objects. Two of those — `modules` and `managedApps` — are lists of fully-qualified Java class names that are reflectively loaded at boot (`BaseApp.java:69-91, 145-166`); this is the documented extension mechanism and has no clean cross-language equivalent.

## Behavior vs. Implementation Artifact

### Pre-match URL rewrite to a single resource
- **Observed behavior:** Every whitelisted path is rewritten to `/trino-gateway/internal/route_to_backend` before resource matching; the original URI is recovered from the underlying `HttpServletRequest`. `RouterPreMatchContainerRequestFilter.java:44-51`.
- **Source of behavior:** `jvm-artifact`. This is a JAX-RS-specific dispatch trick. Trino's protocol does not require a single endpoint; the gateway uses one resource class because JAX-RS pre-matching is the only place to intercept arbitrary URIs before routing.
- **Rationale:** Lets `extraWhitelistPaths` configuration extend the proxied set without registering N JAX-RS resources. Effectively turns JAX-RS into a generic catch-all servlet.
- **Go obligation:** `drop`. In Go this should be a single `http.Handler` mounted on a path prefix (or registered against each statementPath explicitly via a router), with no internal URI rewriting. The behaviour that matters is "match path against whitelist, then dispatch to proxy handler"; the rewrite is an implementation detail forced by JAX-RS.
- **Notes:** When porting, do not preserve the literal `/trino-gateway/internal/route_to_backend` URI in any externally observable way — it never appears on the wire.

### Buffered (not streamed) proxy responses
- **Observed behavior:** `ProxyResponseHandler.handle` reads up to `ProxyResponseConfiguration.responseSize` bytes from the backend response into a single `String` before completing the future; the response body is then re-emitted to the client. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:46-55` and `ProxyRequestHandler.java:231-237`.
- **Source of behavior:** `gateway-design-intent`. The buffering is deliberate: the gateway needs to read the JSON body of new POST `/v1/statement` responses to extract `id` and bind queryId→backend (`ProxyRequestHandler.java:281-289`). It also keeps response shape uniform between successful and exceptional paths.
- **Rationale:** Trino statement responses are JSON envelopes whose total size is bounded by `targetResultSize` on the coordinator side. Buffering avoids the complexity of streaming-while-inspecting.
- **Go obligation:** `replicate-intent`. Go should buffer the response only when it needs to inspect it (new POST `/v1/statement`); other paths can stream via `io.Copy`. Document this divergence: a Go rewrite using stream-passthrough on `nextUri` polls is *better*, not a regression, but the behavioural contract is the same (buffer when extraction is needed).
- **Notes:** A naive Go port that streams every response would break query→backend binding. A naive Go port that buffers every response would unnecessarily inflate memory under load. The split is by request kind.

### Routing-group selector chosen at module-load via try/catch fallback
- **Observed behavior:** If the configured rules engine fails to initialize (FILE or EXTERNAL), the system silently falls back to the header-only selector. `HaGatewayProviderModule.java:158-174`.
- **Source of behavior:** `defensive-historical`. Effectively a soft-fail to keep the gateway serving even with a broken rules file.
- **Rationale:** Reduces blast radius of a malformed rules YAML. The cost: a deploy that breaks the rules file degrades to "header-only routing" silently — operators may not notice until traffic starts going to the wrong cluster.
- **Go obligation:** `replicate-intent` with louder logging. Match the fallback behaviour but emit a high-severity log/metric so operators see the degradation.
- **Notes:** This is a place where the Java behaviour is arguably wrong but baked into the production contract. Confirm with `trino-expert` whether operators actually rely on the soft-fail before changing it.

### Component lifecycle via `@PostConstruct` / `@PreDestroy`
- **Observed behavior:** Singletons like `ActiveClusterMonitor` start scheduled work in `@PostConstruct` and stop in `@PreDestroy` (`ActiveClusterMonitor.java:62-100`). `JdbcConnectionManager` starts its cleanup scheduler in the constructor (`JdbcConnectionManager.java:48`).
- **Source of behavior:** `jvm-artifact`. Standard Jakarta annotation hooks invoked by the Airlift/Guice bootstrap. Inconsistent: some classes use annotations, some use the constructor — there's no architectural rule.
- **Go obligation:** `drop`. In Go this is explicit `Start(ctx)` / `Stop(ctx)` methods called by the composition root. Inconsistency between constructor-start and annotation-start should not be preserved.
- **Notes:** The cleanup scheduler in `JdbcConnectionManager`'s constructor is a soft anti-pattern (side effects during construction); flagging it for the Go port to fix.

## Implications for Go Rewrite

- **Library:** No single Go equivalent of Airlift exists — its job (HTTP server + DI + config + JMX + Jetty + JAX-RS in one bag) splits into `net/http` + a router (e.g. `chi`) + a config loader + explicit constructors + Prometheus client. See [[library-landscape-go-mapping]] for the per-library mapping.
- **Interface:** The proxy data path can be a single `http.Handler` (or one per statementPath) composed of: `pathFilter → routingResolver → backendSelector → proxyExecutor`. Each stage is a small interface; the heavy stage is `routingResolver` because it pulls in the rules engine (see [[rewrite-hotspots]]). The query→backend binding is a separate `QueryBinder` interface that the proxyExecutor calls for new `POST /v1/statement` responses only.
- **Concurrency:** The Jetty async + `ListenableFuture` chain in `performRequest` (`ProxyRequestHandler.java:170-202`) becomes a single goroutine per request with `context.Context` cancellation. The cached thread pool for transformation is unnecessary in Go — transformations can run inline on the request goroutine. The single-threaded scheduler for cluster monitor maps to a `time.Ticker` in a dedicated goroutine. See [[concurrency-model.architect]] for the full mapping.

## Test Strategy Hooks

- See paired QA studies: [[test-infrastructure-inventory]] (existing Java test infra), [[proxy-request-lifecycle-testable-seams]] (where to break out test seams in the Go port).
- The architect-relevant test concern: differential tests against the Java gateway should treat the buffered-vs-streamed response path as the highest-risk behavioural divergence. Any test that asserts byte-equality of response bodies on `nextUri` polls is fine; tests that depend on partial-response delivery timing will diverge.

## Open Questions

- @trino-expert: Is the "soft-fail to header-only routing when rules fail to load" behaviour intentional and contractual, or is it a defensive accident? Affects whether the Go port should hard-fail.
- @trino-expert: Are there clients that depend on the gateway *streaming* response chunks (e.g. for very large `Values` results) rather than buffering full responses? If yes, the response-size cap is a known cliff and the Go port should preserve streaming on result paths.
- @java-analyst: Is anything other than the JSON `id` field in the POST `/v1/statement` response load-bearing for the gateway? Asking because a Go port could parse just `id` (cheap) rather than the whole body, if no other field is consumed.

## Cross-references

- [[concurrency-model.architect.md]] — full thread-pool and goroutine mapping
- [[library-landscape-go-mapping.md]] — per-library Java→Go translation
- [[rewrite-hotspots.md]] — MVEL, trino-parser, OIDC SDK
- [[config-coupling-depth.architect.md]] — how deep the YAML→POJO coupling goes
- [[jvm-idioms-not-to-port.md]] — Guice, JAX-RS, JDBI patterns not to recreate
- [[component-build-order.architect.md]] — sequence in which Go components should be built
- `../both/protocol-constraints-on-the-gateway.architect.md` — wire-level constraints any intermediary must preserve
