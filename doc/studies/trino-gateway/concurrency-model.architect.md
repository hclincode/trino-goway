---
title: Concurrency model — thread pools, async dispatch, goroutine mapping
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
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/jvm-idioms-not-to-port.md
---

# Concurrency model — thread pools, async dispatch, goroutine mapping

## Summary

The Java gateway uses four distinct thread-pool families plus JAX-RS async suspension to keep request threads from blocking on the outbound HTTP call. The Go rewrite collapses all of this into per-request goroutines with `context.Context` cancellation and two long-running goroutines (cluster monitor tick, history cleanup tick). No semantic concurrency primitive in the Java code requires anything more sophisticated than vanilla goroutines.

## Key Findings

- **Four thread-pool families in the Java impl:**
  1. *Inbound Jetty worker threads* — managed by `io.airlift:http-server`. Each inbound request lands on one of these. Configured via Airlift `http-server.*` properties in `serverConfig`.
  2. *Airlift HttpClient outbound executor* — internal to `io.airlift:http-client`, services the async outbound calls. Two named clients are bound: `@ForProxy` for data-path proxying (`ProxyServerModule.java:31`) and `@ForRouter`/`@ForMonitor` for control-path callouts (`BaseApp.java:191-192`).
  3. *Per-handler cached pool* for response transformation: `ProxyRequestHandler` creates `newCachedThreadPool(daemonThreadsNamed("proxy-%s"))` at construction (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:87`). All `ListenableFuture.transform` callbacks run here.
  4. *Scheduled executors* — three independent ones:
     - `ActiveClusterMonitor` uses a single-thread scheduler that ticks every `monitor.taskDelay`, fanning per-backend probes out to a fixed 20-thread pool (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/clustermonitor/ActiveClusterMonitor.java:46-93`).
     - `JdbcConnectionManager` uses a single-thread scheduler for history cleanup every 120 minutes (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/JdbcConnectionManager.java:40-41, 105-116`).
     - `FileBasedRoutingGroupSelector` uses Guava `Suppliers.memoizeWithExpiration` to refresh rules every `rulesRefreshPeriod` — this is *not* a separate thread; refresh happens lazily on the next request after expiry on the calling thread (`FileBasedRoutingGroupSelector.java:55`).
- **JAX-RS async suspension is what frees the inbound thread.** `RouteToBackendResource` handlers all take `@Suspended AsyncResponse asyncResponse` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouteToBackendResource.java:62, 75, 86, 95, 105`). The handler returns immediately after starting the outbound async future; the Jetty thread is released. The response is later resumed from the transformation executor via `bindAsyncResponse` (`ProxyRequestHandler.java:239-247`).
- **Timeout is enforced via `bindAsyncResponse(...).withTimeout(...)`.** `routing.asyncTimeout` (default in `RoutingConfiguration`) bounds how long the inbound side will wait before returning a synthetic 502 (`ProxyRequestHandler.java:239-247`). This is independent of the underlying HttpClient timeout.
- **No shared mutable state in the data path beyond two caches.** `BaseRoutingManager` (the abstract parent of `StochasticRoutingManager`) holds the queryId→backend, queryId→routingGroup, queryId→externalUrl caches (Caffeine). Reads happen on the inbound thread during `RoutingTargetHandler.getPreviousCluster`; writes happen on the transformation thread during `recordBackendForQueryId`. Caffeine handles concurrency.
- **Observer fanout uses Guice `Multibinder<TrinoClusterStatsObserver>`** to inject a `Set<>` of observers (`HaGatewayProviderModule.java:88-90`). The scheduler thread iterates the set sequentially — there is no per-observer parallelism on the publish side; the parallelism is in the per-backend probing fan-out.
- **`@PostConstruct` / `@PreDestroy` are the lifecycle hooks for the scheduled work.** No explicit `Start(ctx)` / `Stop(ctx)` discipline; Airlift's Bootstrap drives the annotations. There is no shutdown signal propagation — `executorService.shutdownNow()` interrupts whatever is in flight.

## Behavior vs. Implementation Artifact

### Per-request transformation pool
- **Observed behavior:** Every proxied request triggers `future.transform(...)` callbacks on a per-handler `newCachedThreadPool` (`ProxyRequestHandler.java:87, 192, 200`).
- **Source of behavior:** `jvm-artifact`. The cached pool exists because `ListenableFuture.transform` requires an `Executor`; using `directExecutor()` would run the transformation on the HttpClient's IO thread which is undesirable. The size and lifecycle of this pool are not load-bearing.
- **Go obligation:** `drop`. In Go, the transformation runs inline on the same goroutine that issued the outbound HTTP call. No separate pool needed.

### Synthetic 502 on async timeout
- **Observed behavior:** After `routing.asyncTimeout` elapses, the gateway returns a `BAD_GATEWAY` (502) with body `"Request to remote Trino server timed out after <duration>"` (`ProxyRequestHandler.java:241-246`).
- **Source of behavior:** `gateway-design-intent`. Bounded inbound latency is a contract — clients see a 502 instead of a hung connection.
- **Go obligation:** `replicate-exactly`. Status code 502 and the timeout-message format are observable. Use a `context.WithTimeout` per request and return a 502 with the same body shape on context expiry.
- **Notes:** Confirm the exact body string is part of any client contract (probably not — clients should look at status code). The status code 502 is load-bearing.

### Cleanup work started in constructor
- **Observed behavior:** `JdbcConnectionManager` calls `startCleanUps()` in its constructor (`JdbcConnectionManager.java:48`), launching the scheduled history-cleanup job before the object is fully published.
- **Source of behavior:** `defensive-historical`. No `@PostConstruct` was added, so the work happens in the constructor.
- **Go obligation:** `drop`. In Go, start scheduled work explicitly from a `Start(ctx)` method called by the composition root. Do not start side-effecting work in constructors.

## Implications for Go Rewrite

- **Library:** No DI/async-IO framework needed. The Go standard library (`net/http`, `context`, `time`, `sync`) covers everything in this concurrency story. For the outbound HTTP client, use a single shared `*http.Client` per backend-traffic-class (one for proxy, one for monitor, one for routing-external) so connection pools stay separated.
- **Interface:**
  - Lifecycle: every long-running component implements `Start(ctx context.Context) error` and is shut down by cancelling `ctx`. No annotation-driven hooks.
  - Cluster monitor: a single goroutine running `time.NewTicker(taskDelay)`; per-backend probes spawned as bounded goroutines via a `errgroup.WithContext`-scoped semaphore (replacing the fixed 20-thread pool).
  - Cleanup: a single goroutine with `time.NewTicker(2 * time.Hour)` for `query_history` deletion. No pool.
- **Concurrency:**
  - Per-request: one goroutine that runs the full pipeline (path filter → routing resolve → outbound `*http.Request` with `req.WithContext(ctx)` → response handling → `QueryBinder` callback for new POST `/v1/statement`).
  - Timeout: `ctx, cancel := context.WithTimeout(req.Context(), routing.AsyncTimeout)`; on `ctx.Err()` return 502 with the matching body.
  - Shared state: the queryId→backend cache becomes a `*sync.Map` or a small typed wrapper around it (or a third-party cache like `ristretto` if eviction policy needs to mirror Caffeine).
  - Observer fanout: a `chan ClusterStatsBatch` published to by the monitor goroutine, with N consumer goroutines (one per observer). Alternatively, sequential observer.Observe(stats) calls inside the monitor tick — match Java semantics by default.

## Test Strategy Hooks

- See paired QA studies: [[test-infrastructure-inventory]] and [[go-test-pyramid]].
- The architect-relevant concurrency test concerns:
  - Async timeout: assert 502 + body shape under a deliberately slow mock backend.
  - Cluster monitor isolation: assert that a single backend hanging on its health probe does not block stats publication for other backends (the Java code achieves this via the per-probe future; the Go port must achieve it via per-probe goroutines under a per-batch deadline).
  - Concurrent queryId binding: assert no double-binding of the same queryId under parallel POST `/v1/statement` storms (Caffeine in Java; `sync.Map.LoadOrStore` in Go).
- **Non-determinism risks:** the cluster monitor's "first tick happens immediately at startup" (`scheduleAtFixedRate(..., 0, ...)`, `ActiveClusterMonitor.java:92`) means tests that read `gatewayBackendManager` state right after start may see stale-by-one-tick data. The Go port should match this initial-tick behaviour.

## Open Questions

- @architect (self): should the Go port consolidate `@ForProxy`, `@ForRouter`, `@ForMonitor` into a single shared `*http.Client`, or preserve three separate clients to keep connection pools isolated? Likely keep three — preserves backpressure isolation. Decide before writing the proxy core.
- @trino-expert: is there any client behaviour that depends on the gateway's response *streaming* characteristics (e.g. partial reads triggering flush)? If yes, we cannot fully buffer the response even for queryId extraction, and the concurrency model has to handle "buffer the head, stream the tail".

## Cross-references

- [[architecture-overview.architect.md]] — overall data path this concurrency model implements
- [[jvm-idioms-not-to-port.md]] — `@PostConstruct`/`@PreDestroy`, `ListenableFuture`, `@Suspended` patterns
- [[library-landscape-go-mapping.md]] — Airlift HttpClient → `*http.Client` mapping details
