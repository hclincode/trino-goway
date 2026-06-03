---
title: Concurrency and lifecycle — Go-implementer addendum (literal-port hazards)
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - cross-cutting
  - proxy-core
  - health-checks
  - routing-engine
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/concurrency-model.architect.md
---

# Concurrency and lifecycle — Go-implementer addendum (literal-port hazards)

## Summary

This study is the **Implementer-side addendum** to `[[concurrency-model.architect.md]]` by `@architect` — read theirs first for the thread-pool inventory and the design-shaped goroutine-mapping recommendations. This file catalogs the **four Go-language patterns that produce bugs if ported literally from the Java thread-pool idiom** (rate-vs-interval ticking, Caffeine compute-if-absent loss, mixed-clock timeouts, emergent shutdown ordering) plus three concrete divergences from the architect's recommendations that need resolution before the proxy core is written (`sync.Map` vs `sync.RWMutex` for `backendToStatus`; `ristretto` vs `golang-lru` + `singleflight` for caches; `errgroup`+semaphore vs unbounded goroutines for cluster-stats fan-out). Everything else cross-references the architect's study.

## Key Findings

The thread-pool inventory and the design-shape goroutine mapping live in `[[concurrency-model.architect.md]]` (sections "Four thread-pool families" and "Implications for Go Rewrite"). Findings below are Implementer-side **literal-port hazards** the architect's study does not call out explicitly.

- **`scheduleAtFixedRate` is rate-based, `time.Ticker` is interval-based.** `ActiveClusterMonitor.java:66-92` calls `scheduleAtFixedRate(task, 0, taskDelay, taskDelay.getUnit())`. If a tick runs longer than `taskDelay`, Java tries to "catch up" — the next tick fires immediately on the single-thread executor. A naive Go port (`for { <-ticker.C; runTick() }`) drops the catch-up. The architect's recommendation (`time.NewTicker(taskDelay)`) implicitly adopts interval semantics. **For this use case I think interval semantics is more correct** (slow ticks shouldn't pile up against a backend) — but it's a behavior change worth flagging explicitly so `@qa-tech-lead` knows whether existing Java tests assert on the catch-up.
- **Caffeine `LoadingCache` guarantees loader-runs-at-most-once-per-key.** `BaseRoutingManager.java:257-263` builds caches with `Caffeine.newBuilder().maximumSize(10000).expireAfterAccess(30, MINUTES).build(loader)`. The compute-if-absent semantic means: under thundering-herd on a missing queryId, exactly one goroutine queries the DB; the rest wait. **Neither `sync.Map`, `hashicorp/golang-lru`, nor `ristretto` provides this on its own.** The architect's study mentions `sync.Map` for the queryId cache and `ristretto` "if eviction policy needs to mirror Caffeine"; neither preserves single-flight. Concrete recommendation: `hashicorp/golang-lru/v2` + `golang.org/x/sync/singleflight` (~20 LOC of glue). Without this, the Go gateway under load can fire N concurrent DB lookups for the same missing queryId.
- **`backendToStatus` write/read pattern is RWMutex-shaped, not Map-shaped.** `BaseRoutingManager.java:53,68` uses `ConcurrentHashMap<String, TrinoStatus>` for ~10 backends, mostly-read, occasionally-written by one writer (the monitor goroutine). `sync.Map` is optimized for "many keys, write-once-read-many" — wrong shape here. **My recommendation: `sync.RWMutex` + `map[string]TrinoStatus`.** This diverges from `[[concurrency-model.architect.md]]` ("a `*sync.Map` or a small typed wrapper") — needs alignment with `@architect` before either of us writes code.
- **`isInitialized` is a `volatile boolean`.** `ActiveClusterMonitor.java:40`. Plain `bool` in Go is unsafe under concurrent access; use `atomic.Bool`. Architect's study doesn't address this; it's small but easy to miss in a port.
- **Mixed-clock timeouts are a Go-specific footgun.** `bindAsyncResponse(...).withTimeout(asyncTimeout, ...)` is a single deadline in Java. In Go, three deadlines might combine accidentally: `http.Server.WriteTimeout` (server-wide), `http.Transport.ResponseHeaderTimeout` (per-request, headers-only), and `context.WithTimeout` on the request context. They measure different intervals and produce opaque "timeout — which one?" failure modes. **Concrete rule: use *only* `ctx, cancel := context.WithTimeout(r.Context(), asyncTimeout)` and propagate via `req.WithContext(ctx)`.** Don't set `Transport.ResponseHeaderTimeout` or per-server `WriteTimeout` for proxy paths. Architect's study says the same in passing; this study makes it the rule.
- **Emergent shutdown ordering has no Go equivalent.** `@PreDestroy` order is implicit in the Guice dependency graph. The architect's study notes the Go version replaces this with explicit `Start(ctx)`/`Stop(ctx)` — concrete addition: a tiny `lifecycle` package (~100 LOC) owns an ordered slice of `Component`s, iterates forward for start and reverse for stop, propagates a shared `context.Context` derived from `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`. Constructor wiring decides the order in `main`; the compiler enforces dependencies. Suggested filename: `internal/lifecycle`.
- **`Executors.newFixedThreadPool(20)` is not a Go pool.** The architect proposes "bounded goroutines via an `errgroup.WithContext`-scoped semaphore" for cluster-stats fan-out. I agree on the shape; the concrete pattern is `golang.org/x/sync/semaphore` with `Weighted(20)` and `sem.Acquire(ctx, 1)` / `sem.Release(1)` around each per-backend probe. Don't introduce a `workerpool` library.
- **Per-request `newCachedThreadPool` is gone — confirmed.** Architect correctly classifies `ProxyRequestHandler.java:87`'s cached pool as a JVM artifact to drop. In Go the transformation runs inline on the request goroutine; we don't need a separate executor. No divergence; flagging for clarity.

## Behavior vs. Implementation Artifact

### Fixed-size thread pools
- **Observed behavior:** `ActiveClusterMonitor` parallelizes per-backend stats calls across 20 threads (`ActiveClusterMonitor.java:46`); `BaseRoutingManager` parallelizes queryId-probe HTTP calls across 5 threads (`BaseRoutingManager.java:51`).
- **Source of behavior:** `defensive-historical` — bounding outbound HTTP fan-out is wise, but the specific numbers (5, 20) look like defaults rather than measured values.
- **Rationale:** Prevents the gateway from overwhelming a backend with concurrent probes, and bounds resource use on the gateway itself (each Java thread is ~1MB of stack).
- **Go obligation:** `replicate-intent`, not `replicate-exactly`. In Go, "fixed-size pool" is an anti-pattern — goroutines are cheap. The right Go shape is "unbounded goroutines + a `chan struct{}` or `golang.org/x/sync/semaphore` to cap concurrency". The concurrency cap should be a config knob with the same defaults (5, 20) for behavioral parity, but the implementation is goroutines, not workers.
- **Notes:** worth a measurement at QA time — these caps may be unnecessary in Go where goroutine stacks are 2KB initial. Document the knob, default to Java values, raise later if benchmarks justify it.

### Single-thread scheduled executor (serialized ticks)
- **Observed behavior:** `ActiveClusterMonitor` schedules a stats-collection task on `newSingleThreadScheduledExecutor()`. Because the executor has one thread, two ticks can never run concurrently — if a tick takes longer than `taskDelay`, the next tick queues and runs after.
- **Source of behavior:** `gateway-design-intent` — explicit serialization of cluster-stats collection.
- **Rationale:** Probably to avoid hammering backends and to keep observer notifications in order (`ActiveClusterMonitor.java:82-86` iterates observers; concurrent ticks would interleave observer notifications unpredictably).
- **Go obligation:** `replicate-exactly`. A naive Go port (`for { <-ticker.C; runTick() }`) preserves serialization but drops the "catch up after slow ticks" rate-vs-interval behavior. Concrete recommendation:
  ```go
  for {
      select {
      case <-ctx.Done(): return
      case <-ticker.C:
      }
      runTick(ctx)  // serialized by definition
  }
  ```
  This gives interval-based timing (more correct than Java's rate-based for this use case — slow ticks shouldn't pile up) plus serialization.
- **Notes:** if there is observable behavior depending on "tick missed during slow run still fires immediately after", we'd need to use `time.Tick` differently or buffer the channel. I don't *think* that behavior is depended on, but `@java-analyst` or `@qa-tech-lead` should confirm whether any tests assert on tick frequency under load.

### Wall-clock async timeout
- **Observed behavior:** `withTimeout(asyncTimeout, ...)` applies to the entire future chain after `bindAsyncResponse`.
- **Source of behavior:** `gateway-design-intent`.
- **Rationale:** Simple end-to-end SLA.
- **Go obligation:** `replicate-intent`. Use `context.WithTimeout(r.Context(), asyncTimeout)` on the inbound request, propagate to the outbound HTTP client. **Do not** mix `http.Server.WriteTimeout` (server-wide), `http.Transport.ResponseHeaderTimeout` (per-request, headers-only), and a `context.Deadline` — those are different clocks measuring different intervals, and combining them produces opaque failure modes.
- **Notes:** the Java code's error message on timeout is `"Request to remote Trino server timed out after" + asyncTimeout`. Reproduce exactly for monitoring/alert continuity — see `[[proxy-streaming-vs-buffering.go-implementer.md]]`.

### Guice-ordered shutdown
- **Observed behavior:** components annotated `@PreDestroy` are invoked in reverse dependency order at JVM shutdown. The shutdown contract is not explicit in code.
- **Source of behavior:** `jvm-artifact` — emergent from Airlift + Guice.
- **Rationale:** It "just works" in the Java idiom; Guice knows the dependency graph from constructor injection.
- **Go obligation:** **must** be replaced with an explicit ordering. Concrete recommendation: a `Lifecycle` struct holding an ordered list of `Component { Start(ctx) error; Stop(ctx) error }`. `Start` iterates forward, `Stop` iterates in reverse. Wire-up code (the `main` package) decides the order explicitly. This is the kind of thing future-Go-Implementer will thank present-Go-Implementer for not papering over.
- **Notes:** the start order in `HaGatewayLauncher.java` does not directly reveal the runtime dependency order — it's hidden in Guice's resolution. To recover it, we'd need to either trace the constructor graph manually (medium effort) or wire it explicitly from scratch in Go and let the compile errors guide the order. I'd recommend the second.

## Implications for Go Rewrite

(Architect's `[[concurrency-model.architect.md]]` covers the design-shape mapping. Items below are concrete library/pattern picks plus a few additions.)

- **Tiny `internal/lifecycle` package** (~100 LOC) owning the ordered `Component` slice, `Start(ctx)`/`Stop(ctx)` iteration, and `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` integration. Replaces Airlift `Bootstrap` + `@PostConstruct`/`@PreDestroy`.
- **Cache layer:** `hashicorp/golang-lru/v2` + `golang.org/x/sync/singleflight` for queryId→{backend, routingGroup, externalUrl}. Architect's draft mentions `sync.Map` and `ristretto` — neither preserves Caffeine's single-flight load. See divergence note in Open Questions.
- **Concurrency cap on cluster-stats fan-out:** `golang.org/x/sync/semaphore.Weighted(20)`. Don't use a worker-pool library.
- **Atomic primitives for `volatile`-equivalent flags:** `atomic.Bool` for `isInitialized`; `atomic.Int64` for counters that may move to metrics.
- **Mutex pick for `backendToStatus`:** `sync.RWMutex` + `map[string]TrinoStatus`, not `sync.Map`. Cleaner, faster for this read-heavy / single-writer pattern at ~10 keys. See divergence note in Open Questions.
- **Every long-lived goroutine takes `ctx context.Context`.** No exceptions. Shutdown correctness depends on context propagation end-to-end.
- **Fake-clock seam for tests:** `benbjohnson/clock` or `jonboulle/clockwork`. The scheduled-monitor and cleanup goroutines should accept a `Clock` interface in their constructor, not call `time.NewTicker` directly. Flagging for `@go-qa` paired-coverage planning.

## Test Strategy Hooks

- **Test level:** unit tests for the `lifecycle` package; integration tests for the scheduled monitor (with fake clock); load tests for goroutine-fan-out under high backend count.
- **Fixtures required:** fake-clock injection point on the periodic-monitor component (matches the seam the Java code already has via Airlift's `Ticker` or equivalent — needs confirmation from `@java-analyst`); test harness that can spin up N mock backends to exercise the fan-out semaphore.
- **Observable signals:** goroutine count steady-state (no leaks across many start/stop cycles), tick monotonicity (no double-fires per period), shutdown completion within a bounded `stop(ctx)` deadline.
- **Non-determinism risks:** any test that asserts on goroutine counts must use `runtime.NumGoroutine()` *after* `runtime.GC()` and tolerate some slack — the runtime maintains background goroutines we don't control. Concurrency cap tests need to use synchronized fixtures (a `chan` the test fills/drains) rather than `time.Sleep` to assert "we capped at N".
- See paired QA study (none — flagging `@go-qa` for paired coverage of the lifecycle and scheduled-monitor tests).

## Open Questions

**Three divergences from `[[concurrency-model.architect.md]]` that need resolution before the proxy core is written:**

- `@architect`: **`sync.Map` vs `sync.RWMutex` for `backendToStatus`.** Your study says "a `*sync.Map` or a small typed wrapper around it"; mine recommends `sync.RWMutex + map[string]TrinoStatus`. For ~10 backends with one writer and many readers, RWMutex is the better Go shape. `sync.Map` is optimized for "many keys, write-once-read-many" — not this pattern. Pick one before either of us writes code.
- `@architect`: **`ristretto` vs `golang-lru/v2 + singleflight` for the queryId caches.** Caffeine's compute-if-absent guarantees the loader runs at most once concurrently per key. Neither `sync.Map` nor `ristretto` provides this. Without single-flight, under thundering-herd on a missing queryId the gateway can fire N concurrent DB lookups. Pick one before either of us writes code.
- `@architect`: **interval-vs-rate ticking for `ActiveClusterMonitor`.** Your study implicitly adopts interval semantics (`time.NewTicker`). I agree it's the more correct shape but it's a behavior change from Java's `scheduleAtFixedRate` (which catches up after slow ticks). Worth a one-line note in your final draft to make the choice deliberate.

**Other:**

- `@architect`: am I right that we should drop Guice-style DI entirely and use plain constructors? Your study assumes this; explicit confirmation in `[[library-landscape-go-mapping.md]]` would prevent accidental `google/wire` introduction later.
- `@java-analyst`: are there places in the Java code where `@PreDestroy` ordering matters for correctness (e.g. the monitor must stop *before* the cache it writes into is cleared)? If yes, list them so the Go shutdown order can preserve the dependency.
- `@java-analyst`: confirm there are no `synchronized` blocks I missed. I read the high-traffic paths; haven't audited every file.
- `@qa-tech-lead`: does any existing Java test assert on `scheduleAtFixedRate`'s "catch up after slow ticks" behavior? Answer affects the interval-vs-rate choice above.
- `@trino-expert`: the `taskDelay` default for `ActiveClusterMonitor` — is there a Trino-side coupling (e.g. `/v1/info` rate limits)? Need to size safely.

## Cross-references

- `[[proxy-streaming-vs-buffering.go-implementer.md]]` — the async-timeout path is detailed there as well; this study covers the executor side.
- `[[jvm-dependencies-inventory.go-implementer.md]]` — covers Guice, Airlift Bootstrap, and Caffeine substitution decisions; this study covers the resulting Go concurrency patterns.
