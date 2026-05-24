---
title: Proxy request lifecycle — testable seams
author: java-qa
role: Java QA
component: trino-gateway
topics: [proxy-core, statement-protocol]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/test-infrastructure.java-qa.md
  - trino-gateway/routing-engine.java-qa.md
  - both/statement-protocol-invariants.java-qa.md
---

# Proxy request lifecycle — testable seams

## Summary

A QA-shaped map of the eight-stage proxy request path from "request arrives at gateway port" through "history row written," documenting what is observable at each stage so Go tests have well-defined assertion points. The single most important takeaway: each seam exposes a *named observable* (request attribute, http header, log line, cookie, persisted row), and every existing Java test asserts on one of those observables — there is no hidden state worth re-mocking, only a chain of well-defined boundaries to probe.

## Key Findings

### The eight seams (in execution order)

For each seam: the responsible Java class, the observable a Go test can assert on, and an existing-test pointer where available.

**Seam 1 — Path whitelisting (pre-match URI rewrite)**

- **What happens:** `RouterPreMatchContainerRequestFilter` runs as a JAX-RS `@PreMatching` filter; if the incoming path matches the whitelist (`statementPaths`, `/v1/query`, `/ui`, `/v1/info`, `/v1/node`, `/ui/api/stats`, `/oauth2`, or any configured `extraWhitelistPaths` regex), it rewrites the request URI to the synthetic internal path `/trino-gateway/internal/route_to_backend`. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouterPreMatchContainerRequestFilter.java:30-52`, `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/PathFilter.java:71-81`.
- **Observable:** routed requests reach `RouteToBackendResource` (`/trino-gateway/internal/route_to_backend`); unrouted requests hit the resource matching their actual URI (auth, admin API, web UI, etc.).
- **Existing test:** `TestPathFilter` (path-list semantics); `TestGatewayHaMultipleBackend.testCustomPath` verifies a configured custom path (`/v1/custom/extra`) gets routed and an unconfigured `/invalid` returns `404`. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:146-168`.
- **Go test seam:** assert on the response — request to a whitelisted path is forwarded (and gets a backend's response), request to a non-whitelisted, unconfigured path returns 404 from the gateway itself.

**Seam 2 — Query metadata parsing (request attribute population)**

- **What happens:** `QueryMetadataParser` (also `@PreMatching`, priority `PRE_AUTHORIZATION`) buffers the request body (`bufferEntity()`) and constructs a `TrinoQueryProperties` instance via `io.trino.sql.parser.SqlParser`, stashing it on the request as the attribute `trinoQueryProperties`. On parse failure, an empty properties object is used. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/QueryMetadataParser.java:60-87`. Gated by `requestAnalyzerConfig.analyzeRequest` (config-disable possible).
- **Observable:** the request attribute `trinoQueryProperties` is non-null and populated when `analyzeRequest=true` and body is parseable. Downstream selectors and `extractQueryIdIfPresent` consume it.
- **Existing tests:** `TestTrinoQueryProperties`, `TestQueryMetadataParser`, `TestQueryIdCachingProxyHandler.testQueryIdFromKill` (validates parse-driven query-id extraction from `kill_query` calls). `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/handler/TestQueryIdCachingProxyHandler.java:74-184`.
- **Go test seam:** since Go won't reuse Trino's Java SQL parser, the seam to assert on is the *resulting routing decision* and the *extracted query id* — not the intermediate properties object. Black-box the parser, test inputs/outputs.

**Seam 3 — Routing decision (group + cluster selection)**

- **What happens:** `RoutingTargetHandler.resolveRouting()` runs three sub-decisions in order: (a) extract query id if present (continuation request); (b) look up previous cluster via `RoutingManager.findBackendForQueryId` or via a `GatewayCookie` with a backend hint; (c) if no previous cluster, call `RoutingGroupSelector.findRoutingDestination` to compute a routing group, fall back to `defaultRoutingGroup` if blank, and call `RoutingManager.provideBackendConfiguration(routingGroup, user)` to pick a backend. Returns a `RoutingTargetResponse` containing `RoutingDestination(routingGroup, clusterHost, clusterUri, externalUrl)` and a possibly-modified request (header injection). `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:70-109, 153-172`.
- **Observable:** the URI ultimately observed by the mock backend (`RecordedRequest.getRequestUrl()` analogue) and the routing group recorded in the query history row (Seam 7 observable). The `Rerouting [scheme://host:port/path]--> [target]` line at `RoutingTargetHandler.java:174-183` is operator-only — not a wire contract and not asserted on; Go is free to use a different logger / format / phrasing.
- **Existing tests:** `TestRoutingGroupSelector` (582 lines, parameterized across rule fixtures), `TestQueryCountBasedRouter`, `TestStochasticRoutingManager`, `TestExternalRoutingGroupSelector`, `TestRoutingTargetHandler`, `TestRoutingManagerExternalUrlCache`, `TestRoutingManagerNotFound`. Routing semantics catalogued in detail in `[[routing-engine.java-qa.md]]`.
- **Go test seam:** assert on which mock backend received the request (recorded request path/host) and on the routing group recorded in the query history table. Do not assert on the `Rerouting` log line — operator-only.

**Seam 4 — Header transformation on forward**

- **What happens:** `ProxyRequestHandler.setupRequestHeaders` copies headers from the incoming servlet request to the outgoing airlift `Request.Builder`, with two exclusions: the case-insensitive skip list `["Accept-Encoding", "Host"]`, and `X-Forwarded-*` / `Forwarded` headers when `forwardedHeadersEnabled=false`. It always appends `Via: <protocol> TrinoGateway`. When `forwardedHeadersEnabled=true`, it also adds `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Port`, `X-Forwarded-Host` derived from the incoming request. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:82-84, 316-362`. Additionally, if a `RoutingSelectorResponse` returned external-routing headers, the request is wrapped in `HeaderModifyingRequestWrapper` so those headers override originals. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:101-105, 114-151`.
- **Observable:** headers received at the mock backend's `RecordedRequest.getHeader(name)` (or Go equivalent). Specifically: `Host` is dropped (the airlift client sets its own); `Accept-Encoding` is dropped; `Via` is added; `X-Forwarded-*` toggled by config; routing-supplied headers override originals.
- **Existing tests:** `TestForwardedHeadersDisabled` (forwarded-headers off path); `TestGatewayHaSingleBackend.testRequestDelivery` (incidentally exercises Host-header behaviour — see Seam 8 for the rewriting consequence).
- **Go test seam:** stand up a mock backend that records all received headers; for each header policy assert presence/absence/value.

**Seam 5 — Outbound HTTP execution (proxy → backend)**

- **What happens:** `ProxyRequestHandler.performRequest` builds an airlift `Request` with `setFollowRedirects(false)`, calls `httpClient.executeAsync(request, new ProxyResponseHandler(proxyResponseConfiguration))`. The `ProxyResponseHandler` reads up to `responseSize` bytes from the response input stream into a single `String` (UTF-8) and packages it as a `ProxyResponse(statusCode, headers, body)` record. **Streaming is NOT preserved** — the entire response body is buffered. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:184-201, 249-252`; `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:47-55`.
- **Observable:** the mock backend receives one outbound request; the configured `responseSize` cap (`ProxyResponseConfiguration`) is the upper bound on what the gateway will return — bodies larger than the cap are silently truncated by `readNBytes`.
- **Existing tests:** `TestProxyRequestHandler` (size cap not explicitly tested in what I read; flagged as a gap). Redirect-not-followed is asserted indirectly via the gateway being usable at all with downstream auth flows.
- **Go test seam:** mock backend returns a large body (> cap) and assert truncation behaviour; mock backend returns a 3xx redirect and assert the gateway returns that redirect unchanged rather than following it.

**Seam 6 — Async response binding and timeout**

- **What happens:** `ProxyRequestHandler.setupAsyncResponse` wraps the future in `bindAsyncResponse` with `withTimeout(asyncTimeout, ...)`. If the timeout elapses before the backend responds, the gateway returns `502 BAD_GATEWAY` with body `"Request to remote Trino server timed out after<duration>"`. `asyncTimeout` comes from `HaGatewayConfiguration.routing.asyncTimeout`. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:198-202, 239-247`.
- **Observable:** HTTP `502` with that exact body substring. The `proxy-%s` thread pool (cached, daemon) backs the async machinery — leaks visible on shutdown if any.
- **Existing tests:** none explicitly testing the timeout path that I located — **gap, covered as G5 in `[[test-gaps-and-risks.java-qa.md]]` and in the Go-side gap register `[[qa-gaps-and-risks.go-qa.md]]`**.
- **Go test seam:** mock backend that sleeps past the configured async timeout; assert `502` and body shape.

**Seam 7 — Query-id ↔ backend binding (for POST to a statement path)**

- **What happens:** when the outbound request is `POST` on a statement path, the response is decorated with `recordBackendForQueryId`: if the response status is `200 OK`, the JSON response body's `"id"` field is parsed as the query id, and three caches are populated — `setBackendForQueryId(queryId, backendUrl)`, `setRoutingGroupForQueryId(queryId, routingGroup)`, `setExternalUrlForQueryId(queryId, externalUrl)`. If status is non-200, the binding is skipped with an error log line. Either way, a `QueryHistoryManager.QueryDetail` is submitted asynchronously. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:188-196, 269-314`.
- **Observable:** subsequent `GET`/`DELETE` to `/v1/statement/.../<queryId>/...` is routed to the same backend via `RoutingManager.findBackendForQueryId`. The query history table grows by one row. The `trinoClusterHost` cookie is set on the response when `includeClusterInfoInResponse=true`. `ProxyRequestHandler.java:193-196`.
- **Existing tests:** `TestGatewayHaMultipleBackend.testTrinoClusterHostCookie` (cookie observable), `TestGatewayHaMultipleBackend.testDeleteQueryId` (DELETE on `nextUri` succeeds with 200/204), `BaseTestQueryHistoryManager` (history persistence). `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:198-256`.
- **Go test seam:** assert (a) follow-up request lands on the same mock backend, (b) `trinoClusterHost` cookie present, (c) query history row exists with expected `queryId`, `routingGroup`, `backendUrl`. The three observations are independent and any one being wrong is a regression.

**Seam 8 — Response body rewrite (nextUri → gateway)**

- **What happens:** strictly speaking, the gateway does NOT rewrite `nextUri` itself — Trino's coordinator generates `nextUri` using the `Host` header it received. Because the gateway forwards via airlift's HTTP client (which sets its own `Host: <backend-host>`), the *backend's* `nextUri` would normally point at the backend's host. The integration tests show this is in fact what happens for `MockWebServer` setups (response substring contains `http://localhost:<routerPort>`), but `TestGatewayHaSingleBackend.testRequestDelivery` asserts the response body contains `test.host.com` (the value of the client-supplied `Host: test.host.com` header). This means **Trino itself, behind the gateway, is reading some header to build `nextUri`** — likely `X-Forwarded-Host` when forwarded-headers are enabled, or the `Host` header (but the gateway drops `Host`). `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaSingleBackend.java:65-92`. **This is unclear and merits escalation to the Trino Expert — see Open Questions.**
- **Observable:** the response body's `nextUri` (when status is 200 OK on a `/v1/statement` POST) is a URL whose host:port the client can address. For the gateway to be transparent, that URL must address the gateway, not the backend.
- **Existing tests:** `TestGatewayHaSingleBackend.testRequestDelivery` (asserts on `Host` propagation through to `nextUri`), `TestGatewayHaMultipleBackend.testQueryDeliveryToMultipleRoutingGroups` (asserts the routerPort appears in body). `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:170-195`.
- **Go test seam:** end-to-end against a real `trinodb/trino` container; assert the gateway-returned `nextUri` host:port matches the gateway's bound address, then follow the URL through the gateway successfully.

### Cross-cutting observables

- **Cookies (when `gatewayCookies` enabled):** `GatewayCookie.PREFIX`-prefixed cookies carry backend stickiness; `OAuth2GatewayCookie` is set on requests to `/oauth2/*` paths; `trinoClusterHost` is set on responses to POST-to-statement-path when `includeClusterInfoInResponse=true`. Cookies have HMAC signatures (`GatewayCookie.signature`); tampering causes the cookie to be ignored and the request to route to the default group instead. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.testCookieSigning:334-374`.
- **Metrics:** `ProxyHandlerStats.recordRequest()` is called on POST to a statement path (`RouteToBackendResource.postHandler` at `:62-68`). Exposed under `/metrics` (Prometheus text format) via the JMX bridge. The metric name shape is JVM-coupled and may change.
- **Logging:** `Rerouting [...]--> [...]` info line per request; warning-level `Exception while loading queryId from cache` if cache load fails; error-level `Non OK HTTP Status code ... user: [...]` for failed proxy attempts. Log lines are not formally observable contracts but operators may grep them.
- **Async error handling:** any exception from the HTTP client becomes a `ProxyException` and is converted to `502 BAD_GATEWAY` with the exception message in the body. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:254-267`, `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:41-44`.

### Method routing (HTTP verbs)

`RouteToBackendResource` supports POST, GET, DELETE, PUT, HEAD. POST and PUT buffer the body in `MultiReadHttpServletRequest` so the body is readable both by `QueryMetadataParser` (Seam 2) and by `ProxyRequestHandler.postRequest` (Seam 5). Only POST records `ProxyHandlerStats.recordRequest()` and only POST on a statement path triggers the query-id binding (Seam 7). `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouteToBackendResource.java:42-103`.

## Behavior vs. Implementation Artifact

### Request body double-read via `MultiReadHttpServletRequest`

- **Observed behavior:** the POST/PUT handlers wrap the servlet request to allow the body to be read by both the metadata parser and the proxy forwarder. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouteToBackendResource.java:62-68, 91-103`.
- **Source of behavior:** `jvm-artifact`. Servlet input streams are single-pass; this wrapper is a workaround.
- **Rationale:** Servlet API constraint.
- **Go obligation:** `replicate-intent`. In Go, capture the body once into a `[]byte` and pass it to both parser and forwarder; no special wrapper needed. The *behavior* (parser and forwarder both see the same body) must be preserved; the wrapper class need not.

### Synthetic internal path `/trino-gateway/internal/route_to_backend`

- **Observed behavior:** the pre-match filter rewrites the URI to this internal path so a single JAX-RS resource handles all routed requests. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/RouterPreMatchContainerRequestFilter.java:36, 49-51`.
- **Source of behavior:** `jvm-artifact`. JAX-RS routing requires a registered `@Path`; the URI rewrite is the mechanism for dispatching all whitelisted paths to one resource.
- **Rationale:** framework binding model.
- **Go obligation:** `drop`. In Go, the router (`net/http` or chi or echo) can route by predicate directly; no synthetic path is needed. The path must NOT appear in any observable response (request URIs forwarded to the backend must be the original URI, not the synthetic one). Confirmed by source: `RouterPreMatchContainerRequestFilter.java:48-49` calls `request.setRequestUri(URI.create(ROUTE_TO_BACKEND))` on the JAX-RS `ContainerRequestContext`, which mutates the JAX-RS `UriInfo` only. The underlying servlet's `HttpServletRequest.getRequestURI()` is unaffected; `ProxyUtils.buildUriWithNewCluster` at `ProxyUtils.java:127-129` reads `request.getRequestURI()` and returns `backendHost + originalURI + ?originalQuery`, so the backend receives the client's original path. A Go invariant test ("backend receives original URI, not the synthetic internal path") is still worth pinning to lock the contract.

### Buffered response (no streaming)

- **Observed behavior:** `ProxyResponseHandler.handle` reads up to `responseSize` bytes into a single String, then returns. The full response is buffered before any byte reaches the client. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:47-55`.
- **Source of behavior:** `defensive-historical` likely. Streaming a JSON body that the gateway also needs to parse (`recordBackendForQueryId` reads the body to extract `"id"`) requires read-and-replay; buffering sidesteps that.
- **Rationale:** simplicity at the cost of memory headroom and TTFB.
- **Go obligation:** **`defer-to-expert`**. Trino result-set pages are small (a single `nextUri` poll), so buffering is probably fine; but `/ui` static asset responses and certain endpoints may be larger. The body-size cap is also a defensive truncation that operators may not realise applies. The Architect and Trino Expert should decide whether the Go rewrite streams or buffers, and whether the size cap is preserved as-is, removed, or replaced with a streamed limit.
- **Notes:** if Go decides to stream, it cannot do query-id extraction from the body — the binding has to happen by parsing the *first chunk* and re-encoding it for downstream. This is implementation work, not a behavior change.

### `Host` header drop and `nextUri` host derivation

- **Working hypothesis (treat as P0 unknown for the Go rewrite until confirmed):** Trino's coordinator derives `nextUri`'s host from `X-Forwarded-Host` (with `X-Forwarded-Proto` / `-Port`) when `http-server.process-forwarded=true` is set on the coordinator side, and the gateway populates those headers from the client's original `Host` when `routing.forwardedHeadersEnabled=true`. The test that proves this is a real-Trino e2e POST `/v1/statement` against the gateway with `forwardedHeadersEnabled` toggled on, asserting the response body's `nextUri.host:port` equals the gateway's externally-visible address. Until that test exists and is green, treat the mechanism as unverified and the Go behaviour as un-specifiable. Cross-listed as Open Question #1 below — escalated to both `@trino-expert` (mechanism confirmation) and `@architect` (Go-side strategy once mechanism is known).
- **Observed behavior:** the gateway drops `Host` when forwarding (`PRESERVED_HEADERS_TO_SKIP`), yet the integration test asserts the client-supplied `Host` value (`test.host.com`) appears in the gateway response body (i.e. in `nextUri`).
- **Source of behavior:** `unclear`. Without a real-Trino trace, the exact mechanism is ambiguous — the hypothesis above is the most plausible reading but is not yet source-confirmed against the Trino coordinator code path.
- **Go obligation:** `defer-to-expert`. Until we know whether the gateway's invariant is "Trino sees the gateway's externally-visible host so it can compose `nextUri`" or "the gateway rewrites `nextUri` in the response body," we cannot specify the Go behavior. See Open Question #1.

### `bufferEntity()` for body reuse on the metadata parser side

- **Observed behavior:** `QueryMetadataParser` calls `((ContainerRequest) requestContext).bufferEntity()` before reading the body. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/security/QueryMetadataParser.java:71-72`.
- **Source of behavior:** `jvm-artifact`. Jersey-specific API for body replay.
- **Go obligation:** `drop`. Body capture in Go is a one-liner (`io.ReadAll(r.Body)` then `r.Body = io.NopCloser(bytes.NewReader(buf))`); use a middleware that runs before the parser. No specialised framework call needed.

### Cookie HMAC signing

- **Observed behavior:** `GatewayCookie` is HMAC-signed with a configured key; tampered cookies are ignored and cause routing to fall back to the default group. The test asserts a tampered cookie results in `500` from the callback path. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.testCookieSigning:334-374`.
- **Source of behavior:** `gateway-design-intent`. The gateway exposes a cookie-based stickiness contract; an unauthenticated client must not be able to forge stickiness.
- **Go obligation:** `replicate-intent`. The Go rewrite must (a) reject tampered cookies — they cannot influence routing or stickiness; (b) preserve the `GatewayCookie.PREFIX` cookie name so existing clients with valid signed cookies continue to be recognised across the cutover; (c) return an *error* status on the tamper path. The specific status code (currently `500`) and HMAC algorithm choice are fair game in Go — `replicate-intent` not `replicate-exactly`. Cookie portability across implementations (same key, same signing scheme, same encoding) is a separate Architect decision; if a same-key migration is in scope, the algorithm becomes `replicate-exactly` by transitive constraint.
- **Notes:** the existing `500` is arguably a 4xx and the Go rewrite is encouraged to return a more accurate code (likely `401` or `403`); if any operator dashboards alert on `500`s from the callback path, flag the cutover.

### Cached query-id → backend mapping with backend-search fallback

- **Observed behavior:** `BaseRoutingManager.findBackendForUnknownQueryId` first checks query history, then if not found queries *every* backend with a `HEAD /v1/query/<queryId>` and returns the first 200. Only `isDone()` futures are checked, so this races; on miss it falls back to "first active backend in the default routing group." `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/BaseRoutingManager.java:184-239`.
- **Source of behavior:** `defensive-historical`. Cache loss after gateway restart leaves orphan query-ids that history may also lack; the search is a last-ditch reunion.
- **Go obligation:** `replicate-intent`. Behavior to preserve: a client polling a `nextUri` after a gateway restart should still reach the right backend. The implementation may use any equivalent strategy (e.g. JWT-encode the backend ID in the path, or actually wait for all probes). The race-on-`isDone()` is a bug-shaped artifact — do NOT replicate.
- **Notes:** flag the race as a known issue; the Go rewrite has an opportunity to fix it without regressing behavior.

### Random port allocation in tests

- **Observed behavior:** test classes pick router and backend ports via `Math.random()`.
- **Source of behavior:** `defensive-historical`. See `[[test-infrastructure.java-qa.md]]`.
- **Go obligation:** `drop`. Use `:0` and read back the bound port.

## Implications for Go Rewrite

- The eight-seam decomposition is implementation-agnostic. Whatever Go HTTP framework is chosen, each seam needs a probe point (middleware, handler hook, or explicit interface) so the Go test suite can target each one in isolation.
- Seams 2, 5, 8 are the ones where Java implementation details have the most leakage; treat them as black boxes and assert only on the wire observables (request URI / headers / body received at mock backend; response URI / headers / body received at client). Do not port the intermediate Java types.
- The streaming-vs-buffering question (Seam 5) is the largest open architectural call. Until resolved, the Go test suite should *not* assert specific TTFB behaviour or body-size truncation semantics; those will follow from the Architect's decision.
- The `nextUri` host derivation question (Seam 8) is the highest-risk wire invariant: getting this wrong silently breaks the entire `nextUri` polling protocol for clients that follow URLs. The Go rewrite must have a test that boots a real `trinodb/trino` and asserts the gateway-returned `nextUri` points back at the gateway, not the backend. This is non-negotiable.
- Three integration patterns from the Java suite map directly to Go: header-propagation (Seam 4), query-id binding via the response JSON's `"id"` (Seam 7), and cookie signing/tamper-detection (Seams 3 and 7). Port the test fixtures (specific header sets, specific JSON bodies, specific tampered-cookie payloads) verbatim.
- The query-id cache fallback (`searchAllBackendForQuery`) is a place where the Go rewrite can fix a known race without changing behavior; specify a test that asserts "after restart, a poll on a previously-routed query-id finds the right backend" rather than "the cache lookup races correctly."

## Test Strategy Hooks

- **Test level:** integration for Seams 1, 3, 4, 6, 7 (mock backends suffice); e2e (real `trinodb/trino` container) for Seam 8 and one happy-path proof of Seams 1-7 chained; unit for Seam 2 inputs/outputs only (treating the parser as a black box).
- **Fixtures required:** mock Trino HTTP server with full request recording (path, method, headers, body); real `trinodb/trino` container behind Go testcontainers; YAML config templates for the four routing modes (header, rules, external, query-count); a corpus of request bodies including `SELECT 1`, `CALL system.runtime.kill_query(...)`, valid prepared-statement headers, oversized body; tampered-cookie payloads for the signing test.
- **Observable signals:** mock backend's recorded request URI and headers (Seams 1, 4); the routing decision log line `Rerouting [...]--> [...]` (Seam 3); response status codes `200/204/404/500/502` (multiple seams); response body `nextUri` host:port (Seam 8); response `Set-Cookie` headers (Seams 3 and 7); query history table rows (Seam 7); gateway `/metrics` text (Seam 7).
- **Non-determinism risks:** Seam 6 timeout tests need a controllable clock or a mock backend that sleeps for a configurable duration — never rely on real wall time; Seam 7's async history write needs a *synchronisation hook* exposed by the Go implementation (e.g. a "history-write-completed" channel/callback consumable only from tests) — polling the history table is a hidden flake source and should be the fallback, not the default. **Forward ask to @architect:** please surface a test-only sync point for the history-write completion when designing the persistence write path. Seam 5's `Math.random()` port allocation must NOT be ported.

## Open Questions

- **#1 — @trino-expert + @architect (highest-risk wire invariant in this study, promoted to first position per @qa-tech-lead peer-review Rev 1):** how does Trino derive the host portion of `nextUri` in its response — `Host` header, `X-Forwarded-Host`, both, configuration-only? The test at `TestGatewayHaSingleBackend.testRequestDelivery` proves the gateway-supplied `Host` value reaches the response body via *some* mechanism, but the gateway drops `Host` when forwarding. Two-part answer required: **`@trino-expert`** confirms the exact mechanism (most likely `X-Forwarded-Host` consumption gated by coordinator-side `http-server.process-forwarded=true`, per the hypothesis above), and **`@architect`** owns the Go-side strategy once the mechanism is known (which headers the Go gateway must populate, how the externally-visible address is derived, whether forwarded-headers becomes mandatory or remains togglable, and how the coordinator-side flag is surfaced to operators). Getting this wrong silently breaks the entire `nextUri` polling protocol for any client that follows URLs — non-negotiable to resolve before Go proxy-core implementation begins.
  - **Update (post-sign-off):** mechanism half resolved by `@trino-expert` — confirmed `X-Forwarded-Host` consumption gated by coordinator-side `http-server.process-forwarded=true`. Full source-citation chain captured in `[[../both/gateway-coordinator-nexturi-contract.md]]` and inlined as resolved Open Question #1 in `[[../both/statement-protocol-invariants.java-qa.md]]`. **Working hypothesis above stays as audit trail of what was unknown at write-time; the file is not retroactively rewritten to claim foreknowledge.** Go-side strategy half (which headers the Go gateway populates, externally-visible-address derivation, mandatory-vs-togglable forwarded-headers, operator surfacing of the coordinator flag) is still owned by `@architect` and remains open.
- @trino-expert: is the response-body-buffering behaviour (Seam 5) compatible with all Trino response paths, or are there endpoints (UI assets, very large result-set pages) where buffering causes operational problems today? This informs whether the Go rewrite needs streaming.
- @architect: should the Go rewrite preserve the response-size cap (`ProxyResponseConfiguration.responseSize`) as a hard truncation, or replace it with a streamed limit / per-endpoint policy? This is partly a wire-contract question and partly a Go-side implementation choice.
- @architect: confirm that the gateway forwards the *original* request URI to the backend (not the synthetic `/trino-gateway/internal/route_to_backend`). This appears to be the case because `request.getRequestURI()` is decoupled from the JAX-RS internal rewrite, but should be tested.
- @qa-tech-lead: is the `Rerouting` log line considered a stable observable, or operator-only? **Resolved by @qa-tech-lead during peer review: operator-only.** Seam 3 observables updated to drop the log-line assertion; Go is free to use a different logger/format. Signal is now pinned via the routing-group field in the query history row (Seam 7) and the URI received at the mock backend.
- @architect: please surface a test-only synchronisation hook (channel, callback, or completion event) for the async query history write at Seam 7. Without one, every Seam-7 assertion in the Go test suite either polls the DB (flake-prone) or sleeps (flake-prone and slow). A test-only hook keeps the production path async while making tests deterministic.

## Cross-references

- `[[test-infrastructure.java-qa.md]]` — patterns and fixtures for booting the gateway, mock backends, and assertion helpers.
- `[[routing-engine.java-qa.md]]` — detailed semantics of Seam 3 (routing decision).
- `[[test-gaps-and-risks.java-qa.md]]` — Seams 5 (size cap), 6 (timeout), and 8 (nextUri) have thin or absent existing tests; the gaps doc enumerates the missing scenarios.
- `[[../both/statement-protocol-invariants.java-qa.md]]` — Trino wire invariants that bound what each seam may legitimately do.
