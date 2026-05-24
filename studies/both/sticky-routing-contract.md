---
title: Sticky-routing contract — queryId→backend mapping that survives the host pivot
author: trino-expert
role: Trino & Trino-Gateway Expert
component: both
topics: [statement-protocol, routing-engine, session-state]
date: 2026-05-24
status: peer-reviewed
reviewer: java-analyst
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino/statement-protocol-overview.md
  - both/gateway-coordinator-nexturi-contract.md
---

# Sticky-routing contract — queryId→backend mapping that survives the host pivot

## Summary

A Trino query is a sequence of HTTP requests over many seconds-to-hours, and **every request after the initial POST must reach the same backend cluster that accepted the POST** — otherwise the slug check fails and the client sees `404 Not Found`. The gateway implements this stickiness through an in-memory `queryId → backendURL` cache, populated when the gateway parses the response of an initial `POST /v1/statement` and extracts `id` from the JSON body. The cache is the only thing standing between "load balancing" and "completely broken queries." The Go rewrite must preserve every aspect of how this binding is established and looked up.

## Key Findings

- **Initial POST is the only request where the routing decision is freely made.** All subsequent requests must be routed to the same cluster.
  - Subsequent polls/cancels arrive as GET/DELETE on `/v1/statement/queued/{queryId}/...` or `/v1/statement/executing/{queryId}/...`.
  - The gateway extracts the queryId from the path (or request body for kill_query), looks it up in `queryIdBackendCache`, and routes accordingly.
  - If the cache lookup misses (gateway restart, TTL expiry, different gateway instance), the cache's Caffeine loader runs `findBackendForUnknownQueryId` (`BaseRoutingManager.java:65,184-193`) — a 3-step recovery, NOT a fresh routing-rule eval. See "Cache-miss recovery" below.
  - Source: `gateway-ha/.../RoutingTargetHandler.java:70-87,153-172`; `gateway-ha/.../ProxyUtils.java:64-125`; `gateway-ha/.../BaseRoutingManager.java:65,184-239`.

- **Cache-miss recovery is a 3-step chain in `findBackendForUnknownQueryId`** (`BaseRoutingManager.java:184-239`), invoked automatically by Caffeine when the cache key is unknown:
  1. **Query history table lookup:** `queryHistoryManager.getBackendForQueryId(queryId)` reads from the durable `query_history` table. Hit → return the backend.
  2. **Fan-out HEAD probe:** if the table lookup returns null/empty, the gateway issues `HEAD /v1/query/{queryId}` in parallel against EVERY backend in `gatewayBackendManager.getAllBackends()` (5s connect + 5s read timeout). The first backend returning HTTP 200 wins; the gateway then `setBackendForQueryId(queryId, backend)` so future polls hit the cache.
  3. **Default fallback:** if all probes miss (or any fail), return `getActiveBackends(defaultRoutingGroup).findFirst()`. This is "first active backend in the default routing group", not "what the routing rules would have picked."
  - This recovery usually prevents the 404 that a naïve cache-miss + fresh-routing would cause — Trino's `/v1/query/{queryId}` HEAD endpoint returns 200 when the coordinator knows the query, regardless of slug.

- **Cache population happens only on `POST` to a statement path with 200 OK.**
  - `ProxyRequestHandler.recordBackendForQueryId()` runs after the proxied POST returns, parses the JSON response, extracts `id`, and calls `routingManager.setBackendForQueryId(queryId, backend)`.
  - If the response is non-200, no mapping is stored (and the client should receive the backend's error response unchanged).
  - The body is fully buffered into a `String` and parsed with Jackson at this point — the gateway is no longer streaming on POSTs.
  - Source: `gateway-ha/.../ProxyRequestHandler.java:188-202,269-301`.

- **Cache TTL is `expireAfterAccess(30 MINUTES)`** in the default `BaseRoutingManager`.
  - Source: `gateway-ha/.../BaseRoutingManager.java:257-262`.
  - This is a backend-local in-memory cache, not shared across gateway instances. **Multi-gateway HA deployments require sticky-session at the upstream load balancer** (e.g., AWS ALB with cookie stickiness), or the gateway's own `TG.*` cookies — see [[../trino-gateway/gateway-cookies-and-sticky-routing.md]] (todo: java-analyst).

- **Two query-id extraction mechanisms cooperate:**
  1. **Path-based** (`extractQueryIdIfPresent(path, ...)`): regex against `\d+_\d+_\d+_\w+` in path tokens or query-string params.
     Source: `gateway-ha/.../ProxyUtils.java:51,89-125`.
  2. **Body-based**, only for `POST` containing the substring `kill_query`: parses the request body via `TrinoQueryProperties` (which uses `trino-parser`) to extract the target queryId from `KILL QUERY '<id>'` syntax.
     Source: `gateway-ha/.../ProxyUtils.java:64-87`, `gateway-ha/.../TrinoQueryProperties.java`.

- **The cookie-based fallback** (`GatewayCookie`) provides a SECOND stickiness mechanism for sessions where the queryId isn't yet known (e.g., OAuth2 flows redirecting to a specific coordinator).
  - Cookies are HMAC-SHA256 signed with a configured key (`GatewayCookie.computeSignature()`).
  - Cookies carry `routingPaths` (which paths they apply to) and `deletePaths` (which paths invalidate them).
  - Multiple cookies are sorted by priority then timestamp; first match wins.
  - Source: `gateway-ha/.../GatewayCookie.java` whole file; lookup in `RoutingTargetHandler.java:153-172`.

- **The gateway sets `trinoClusterHost` cookie when `includeClusterHostInResponse=true`** — a debug/observability cookie distinct from the sticky-routing `TG.*` cookies.
  Source: `gateway-ha/.../ProxyRequestHandler.java:193-196`.

## Behavior vs. Implementation Artifact

### Body-buffering on POSTs that hit statement paths
- **Observed behavior:** When a POST request lands on a statement path, the gateway buffers the response body fully into memory, parses it as JSON to extract `id`, then forwards the buffered body to the client.
  Source: `gateway-ha/.../ProxyRequestHandler.java:269-301`; the buffering itself happens in `ProxyResponseHandler` (whole file).
- **Source of behavior:** `protocol-required` to extract queryId; `defensive-historical` for buffering rather than streaming-and-parsing.
- **Rationale:** The initial `QueryResults` body is small (header-only — no `data` yet), so buffering is acceptable. Streaming-and-parsing would be more complex; the team chose simplicity.
- **Go obligation:** `replicate-intent`. The Go gateway must extract queryId from the POST response body to populate the sticky cache. Buffering is acceptable BUT only for POST-to-statement-path responses. All other responses (GET polls, spooled segments, UI) should be streamed.
- **Notes:** Failure mode: if the POST response body is malformed or doesn't contain `id`, the gateway logs an error and proceeds without caching — meaning subsequent polls will likely 404 on the wrong backend. The Java behavior is to log+continue. The Go rewrite should do the same; failing the request would be a regression.

### Cache misses trigger an aggressive 3-step recovery, NOT a fresh routing-rule eval
- **Observed behavior:** When the queryId is not in `queryIdBackendCache`, Caffeine invokes the loader `findBackendForUnknownQueryId` (`BaseRoutingManager.java:65,184-193`), which (1) reads from `query_history` table, then (2) fans out parallel `HEAD /v1/query/{queryId}` probes to all backends and adopts the first 200-responder, then (3) falls back to "first active backend in the default routing group" if nothing matched.
  Source: `gateway-ha/.../BaseRoutingManager.java:65,184-239`.
- **Source of behavior:** `gateway-design-intent`. The design accepts the cost of a fan-out HTTP burst per cache miss in exchange for avoiding 404s during gateway restarts and TTL expiries.
- **Rationale:** Cluster affinity is best-effort but the gateway tries hard before giving up. The 5s timeouts on each probe bound the worst-case recovery latency at ~5s for the slowest backend.
- **Go obligation:** `replicate-intent`. The Go rewrite needs an equivalent recovery chain. Three explicit design decisions for the architect:
  1. **Query-history lookup**: requires the persistence layer to be wired into the routing path. If query history is descoped from v1, step 1 disappears and the recovery is just fan-out + default.
  2. **Fan-out probe**: trivial in Go (`errgroup` over backends with `context.WithTimeout(5s)`). The 5s deadline is load-bearing — too short and slow backends drop out; too long and one slow backend stalls every cache miss.
  3. **Default-fallback semantics**: "first active backend in default routing group" is intentional (deterministic, no rule re-eval). Don't "improve" this by running rules — it would create cross-cluster query duplication during the recovery window.
- **Notes:** This is much more elaborate than a naïve "miss → 404" model would suggest. The Java implementation has a measurable cost-on-miss but very rarely produces user-visible failures from stickiness loss. Mitigations the Java gateway still benefits from: long TTL (30 minutes), cookie-based stickiness (if enabled), upstream LB session affinity. The Go rewrite should preserve all three plus the recovery chain.

### `setBackendForQueryId` runs synchronously before the response is flushed
- **Observed behavior:** `recordBackendForQueryId` is the FIRST `FluentFuture.transform` in the chain (`ProxyRequestHandler.java:188-202`); `buildResponse` is the SECOND. Both run on the same executor before `setupAsyncResponse` flushes anything to the client. The cache is populated synchronously between "backend response received" and "response sent to client."
  Source: `gateway-ha/.../ProxyRequestHandler.java:188-202`. Cache write at `recordBackendForQueryId:285`; response build at `buildResponse:231-237`.
- **Source of behavior:** `gateway-design-intent`. Correctness over latency — the cache is guaranteed populated by the time the client could possibly issue its next poll.
- **Rationale:** A client that received a `QueryResults` JSON with `id=X` and `nextUri=Y` will issue GET `Y` next. By that time, the cache mapping `X → backend` is already in place, so the GET routes correctly. There is no first-poll-misroute race; an earlier draft of this study described one that does not exist.
- **Go obligation:** `replicate-exactly`. The Go handler must extract `id` and write the cache mapping BEFORE flushing the response body to the client. Standard pattern: handle the POST response, JSON-parse for `id`, write to cache, then write the response.
- **Notes:** A goroutine-based "fire-and-forget" cache write WOULD introduce a race that doesn't exist in Java today. Do not "optimize" by deferring the cache write. Credit to java-analyst for catching this — the earlier draft mis-read the Future chain.

### kill_query body-parsing is the ONLY case where the gateway reads request bodies for routing
- **Observed behavior:** Any other POST is routed solely on headers/path. POSTs with the substring `kill_query` trigger a body parse via `trino-parser` to extract the target queryId for routing.
  Source: `gateway-ha/.../ProxyUtils.java:81-86`.
- **Source of behavior:** `gateway-design-intent`. So that `KILL QUERY '20260524_120000_00001_abcde'` routes to the cluster that's running that query, not to the cluster that "would have" received it under normal routing rules.
- **Rationale:** Without this, kill commands would frequently land on the wrong cluster and silently no-op (or kill a different queryId on a different cluster — unlikely given queryId uniqueness, but the no-op case is the real bug).
- **Go obligation:** `replicate-intent`. The Go gateway must implement equivalent behavior. **BUT** it can do this with a much simpler regex extraction (`KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'`) rather than full SQL parsing. SQL parsing is overkill for this one feature, and avoiding it reduces the JVM-dependency risk flagged in the go/no-go.
- **Notes:** This is the SINGLE strongest argument that `trino-parser` is NOT actually load-bearing for the gateway's core routing. `@java-analyst: confirmed` — `ProxyUtils.extractQueryIdIfPresent` (`ProxyUtils.java:64-87`) only invokes `TrinoQueryProperties` for the kill_query path, and only consumes `trinoQueryProperties.getQueryId()`. The other `TrinoQueryProperties` consumer is `QueryMetadataParser` (`gateway-ha/.../security/QueryMetadataParser.java:60-87`), which populates a request attribute read by MVEL rules for routing-group selection — entirely decoupled from the queryId→backend binding path.

## Implications for Go Rewrite

- **Hard invariant: every queryId-bearing request must be routed to the same backend as the original POST**, modulo cache misses (which themselves are recovered via history-lookup + fan-out probe before falling back to default).
- The Go gateway must:
  1. Buffer + JSON-parse POST-to-statement responses to extract `id`.
  2. Populate an in-memory cache keyed by queryId with TTL ~30 minutes. **The cache write MUST complete before the response is flushed to the client** (Java does this; do not regress to a goroutine-based fire-and-forget).
  3. On every subsequent request, attempt queryId extraction (path regex + kill_query body parse) BEFORE consulting the routing-group selector.
  4. On cache miss, run the 3-step recovery: (a) query_history lookup, (b) fan-out `HEAD /v1/query/{queryId}` probes with ~5s per-backend timeout, (c) first-active-default-backend fallback.
- Cache implementation: use a TTL cache library (e.g., `github.com/hashicorp/golang-lru/v2/expirable` or `karlseguin/ccache`). Avoid building one — this is not the place for novelty.
- Fan-out probe implementation: `errgroup.Group` with `context.WithTimeout(5*time.Second)` per probe; collect all results, adopt first 200, race-resolve via a single shared `setBackendForQueryId` call (idempotent — last-writer-wins is fine).
- **Cookie-based sticky-routing is a separate mechanism** with its own contract: HMAC-SHA256 signed JSON cookies. Replicating it bit-identically is required if existing deployments share cookie state across Java↔Go during a migration window. Architect's call: hard-cutover or soft-cutover.
- **For kill_query: replace SQL parsing with a regex.** This removes a significant JVM-port risk without losing functionality. Document this divergence in the go/no-go.
- The cache must NOT be shared via DB or Redis in v1 (the Java gateway doesn't, and clients/operators don't expect it). Multi-instance gateway HA relies on upstream LB sticky sessions or `TG.*` cookies, NOT shared state. The fan-out probe is the cross-instance recovery mechanism.
- **Scope coupling:** if query_history persistence is descoped from v1, step (a) of the recovery chain disappears and the fan-out probe becomes the only pre-fallback recovery. Acceptable degradation; document the dependency in the architect's component build-order study.

## Test Strategy Hooks

- **Test level:** integration (multi-backend gateway) + unit (cache eviction, queryId regex).
- **Fixtures required:**
  - Two distinct mock-backend instances. The gateway should route POSTs by routing-group rules, then all subsequent requests for the resulting queryId to the SAME backend, regardless of routing-rule changes mid-query.
  - A test that submits via gateway, then bypasses the gateway-cache (by hitting a second gateway instance) to verify the cache-miss-routes-fresh fallback behavior.
- **Observable signals:**
  - `queryId` in POST response → backend access log shows ALL subsequent requests on that queryId hitting the same backend.
  - Cache hit/miss counter (`ProxyHandlerStats` equivalent) increments correctly.
  - First-poll-after-POST NEVER gets `404` from backend (cache is populated synchronously before response flush; if this regresses, it's a Go-only bug).
  - On a forced cache miss (restart gateway, then poll an in-flight query), backend access log shows the fan-out HEAD probe burst followed by the next poll landing on the correct backend without 404.
  - `KILL QUERY 'id'` routes to the backend running that id, not to the default routing group.
- **Non-determinism risks:**
  - Fan-out probe is parallel; result ordering is non-deterministic. Tests must assert "the right backend got it" not "this specific backend index won the race."
  - 30-minute TTL is too long for fast tests; the cache must be parameterized for test fixtures.
  - Probe timeout (5s) is too long for fast tests; also parameterize.

## Open Questions

- Should the Go gateway expose cache state via `/v1/gateway/queries` (debug endpoint)? Operationally useful, but adds a new API surface. `@architect`.
- For multi-gateway-instance HA: is there appetite for adding optional shared-cache backend (Redis) in v1, or defer? `@architect`.
- Confirm with `@trino-expert` whether the queryId regex `\d+_\d+_\d+_\w+` is a documented invariant of Trino's QueryId class. If not, we need a fallback identification scheme. (Self-note: this is me, and I can confirm from source — see `trino/core/trino-spi/src/main/java/io/trino/spi/QueryId.java` for the format constraint. Will verify in a follow-up edit.)
- ~~Is the `kill_query` body parse the ONLY case where request-body inspection drives routing?~~ **Resolved (`@java-analyst: confirmed`):** Yes — `ProxyUtils.extractQueryIdIfPresent` is the only body-parse site driving queryId→backend binding. `QueryMetadataParser` populates `TrinoQueryProperties` for MVEL rules (routing-group selection), but that's decoupled from this study's path.
- Should the fan-out HEAD probe be **bounded** to "active backends in the routing group the rule would have selected" rather than "all backends"? The Java implementation probes ALL backends including deactivated ones (`getAllBackends`). Bounding it would reduce probe cost during blue/green upgrades but could miss recovery on a not-fully-deactivated old cluster. `@architect`.

## Cross-references

- [[../trino/statement-protocol-overview.md]] — the queryId format and slug+token URL structure.
- [[gateway-coordinator-nexturi-contract.md]] — explains why the nextUri's host comes from forwarded headers and why the queryId mapping must survive the host pivot.
- [[../trino-gateway/gateway-cookies-and-sticky-routing.md]] (TODO, java-analyst) — the cookie-based stickiness mechanism that complements the cache.
- [[../trino-gateway/routing-engine-rules.md]] (TODO, java-analyst) — the rules engine that picks the initial backend.
