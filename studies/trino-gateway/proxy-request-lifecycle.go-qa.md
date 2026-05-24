---
title: Proxy request lifecycle — testable seams for the Go rewrite
author: go-qa
role: Go QA
component: trino-gateway
topics: [proxy-core, statement-protocol, test-infra]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/test-infrastructure.go-qa.md
  - trino-gateway/routing-engine.go-qa.md
  - trino-gateway/test-gaps-and-risks.go-qa.md
  - trino-gateway/proxy-request-lifecycle.java-qa.md
---

# Proxy request lifecycle — testable seams for the Go rewrite

## Summary

The Java proxy lifecycle has eight observable seams a parity test can target: (1) routing-target resolution, (2) backend-URI build, (3) header forwarding policy, (4) X-Forwarded-* injection, (5) async-timeout fallback, (6) statement-POST → query-ID extraction and binding, (7) gateway cookie emission, and (8) error-to-status mapping. Each seam has a concrete request/response oracle in the existing Java tests or in the `ProxyRequestHandler` source. The Go rewrite should expose these as small interfaces so each seam is testable in isolation without bringing up the whole gateway.

## Key Findings

### Seam 1 — routing-target resolution
Entry point: `RoutingTargetHandler.resolveRouting(HttpServletRequest)` returns a `RoutingTargetResponse(RoutingDestination, modifiedRequest)`.
- Cite: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:70-87`.
- Logic order: (a) try to extract a query ID from the URL/body → if present, look up the previously-bound backend in `routingManager`; (b) else, check gateway cookies for a bound backend; (c) else, invoke the rule-based `RoutingGroupSelector` and pick a backend from the group via `routingManager.provideBackendConfiguration(group, user)`.
- Observable signals to assert:
  - Returned `RoutingDestination.routingGroup()` (string) — assertable directly.
  - Returned `RoutingDestination.clusterHost()` (string URL) — assertable directly.
  - Falls back to default routing group when selector returns null/empty (`RoutingTargetHandler.java:95-97`).
  - **Do not assert on log substrings.** Per qa-tech-lead: log lines are operator-only, not stable observables. The Go rewrite will use `log/slog`, which changes the format wholesale. Pin observability via the `routingGroup` field in the `query_history` row (Seam 6 binding behavior) — that's a structured, schema-stable signal.
- **externalHeaders REPLACE semantics (java-qa confirmed):** when the selector populates `RoutingDestination.externalHeaders()`, `resolveRouting` wraps the request in `HeaderModifyingRequestWrapper` (`RoutingTargetHandler.java:101-105, 114-151`). The wrapper REPLACES inbound values for any header name in the wrapper map — it does NOT merge. Specifics that must port:
  - `getHeader(name)` and `getHeaders(name)` return ONLY the wrapper value when `name` is in the custom map; the inbound value is shadowed (`:125-141`).
  - `customHeaders` is `Map<String, String>` (single-value); a multi-value inbound header (e.g. two `X-Trino-Session` values) collapses to the single wrapper value when shadowed.
  - `getHeaderNames()` returns the union (distinct) of inbound + wrapper names, so a wrapper-only name appears exactly once (`:143-150`).
  - The wrapper is a no-op when `externalHeaders().isEmpty()` is true — non-external selectors do not pay this overhead and produce no behavior difference here.
  - **Canonical Go test shape (lift verbatim from [[routing-engine.go-qa.md]] Routing-decision-protocol-contract):** external returns `{X-Trino-User: override, X-Custom: new}`; client sends `{X-Trino-User: original, X-Other: keep}`; backend sees `{X-Trino-User: override, X-Other: keep, X-Custom: new}`; plus a fourth assertion that a multi-value `X-Trino-Session` (rule sets one value, client sent two) collapses to the single wrapper value on the backend.
- Test seam in Go: a `RoutingTargetResolver` interface accepting `*http.Request` and returning a `RoutingDestination`. Unit-testable with `httptest.NewRequest`; no network needed. The external-headers REPLACE behavior is testable as either (a) a thin `http.Header` mutation step that runs post-resolution and pre-forward, or (b) a `RoundTripper` middleware — either way, the assertion shape above pins the contract.

### Seam 2 — backend URI build
Helper: `ProxyUtils.buildUriWithNewCluster(clusterHost, request)` rewrites scheme/host/port and preserves path + query string.
- Cite: invoked at `RoutingTargetHandler.java:81-82,107`.
- Observable signal: the resulting `URI` passed to the HTTP client (`ProxyRequestHandler.java:176-177`). Backend mock servers can assert the path/query they receive.
- Test seam in Go: pure function `buildUpstreamURL(base *url.URL, incoming *http.Request) *url.URL`. Highly table-testable.

### Seam 3 — header forwarding policy
`ProxyRequestHandler.setupRequestHeaders` iterates incoming headers, drops headers in `PRESERVED_HEADERS_TO_SKIP` (currently `Accept-Encoding`, `Host`), drops `X-Forwarded-*` / `Forwarded` headers unless `forwardedHeadersEnabled` is true, and adds a `Via: <protocol> TrinoGateway` header always.
- Cite: `ProxyRequestHandler.java:316-351`.
- Observable signals:
  - Backend never sees `Accept-Encoding` from client (assertable via recorded request on backend).
  - Backend never sees `Host` from client (assertable; Go's `httputil.ReverseProxy` does this differently — see Behavior vs. Artifact).
  - Backend sees `Via` header with literal suffix `" TrinoGateway"`.
  - When `forwardedHeadersEnabled=false`, backend sees zero `X-Forwarded-*` headers, even if client sent them.
- Test seam in Go: a `HeaderForwarder` interface or pure function `forwardHeaders(in http.Header, out http.Header, fwdEnabled bool)`. Table-testable with no network.
- Existing Java test: `TestForwardedHeadersDisabled.java` (forwarded-headers-disabled integration test).

### Seam 4 — X-Forwarded-* injection
When enabled, the gateway injects `X-Forwarded-For` (remote addr), `X-Forwarded-Proto` (scheme), `X-Forwarded-Port` (server port), `X-Forwarded-Host` (server name if non-null).
- Cite: `ProxyRequestHandler.java:353-362`.
- Observable signals: backend sees exact four headers with values matching the *gateway's* socket properties, not the client's headers. Client-provided `X-Forwarded-*` are dropped (per Seam 3).
- Test seam: same pure function as Seam 3 with `forwardedHeadersEnabled=true`.

### Seam 5 — async-timeout fallback
`ProxyRequestHandler.setupAsyncResponse` wraps the upstream future with `asyncTimeout` from config (`routing.asyncTimeout`). On timeout, returns `502 Bad Gateway` with body `"Request to remote Trino server timed out after<duration>"` and content-type `text/plain`.
- Cite: `ProxyRequestHandler.java:239-247`.
- Observable signals: HTTP `502`, `Content-Type: text/plain`, body contains substring `"timed out after"`. Note the missing space — replicating exactly means preserving that typo. **Defer to @architect/@trino-expert** on whether to fix.
- Test seam in Go: an upstream `RoundTripper` that blocks longer than the configured timeout. Use `context.WithTimeout` on the upstream request; assert `502` + body substring.

### Seam 6 — statement-POST → query-ID extraction and binding
On `POST` to any path matching `statementPaths` (default `/v1/statement`), the gateway parses the upstream JSON response, extracts `results.get("id")`, and binds it in `RoutingManager` via three calls: `setBackendForQueryId`, `setRoutingGroupForQueryId`, `setExternalUrlForQueryId`. It also records a `QueryDetail` to `QueryHistoryManager`. Failures to parse log at ERROR level but do not change response status.
- Cite: `ProxyRequestHandler.java:269-301`.
- Observable signals (structured, not log-based — per qa-tech-lead):
  - On success: the next request bearing the same query ID in its URL is routed to the *same* backend (this is the stickiness invariant). Assertable end-to-end: POST, take `id` from response, then GET `/v1/statement/queued/<id>/...` and verify it lands on the same backend mock.
  - On success: a `QueryHistory` row exists with `queryId`, `backendUrl`, `routingGroup`, `externalUrl`, `user`, `source` set. **This row is the canonical oracle for "the binding happened."**
  - On failure to parse: no `QueryHistory` row written; subsequent same-id request does NOT route stickily (falls back to rule engine). Use absence-of-row plus routing-decision as the oracle, not log text.
  - On 200-but-malformed-body: response status to client is still 200 (the failure is silent to clients — this is a real behavior to preserve, and a behavior the Java suite does NOT test).
  - On non-200 from upstream: no query-ID binding; client sees the upstream status code. Same row-absence oracle.
  - **Cookie-tamper response code (Seam 7 interaction):** when a forged/invalid gateway cookie is presented, the gateway must return an error status — `replicate-intent`, the specific 4xx vs 5xx code is fair game. Aligning with java-qa and qa-tech-lead on this stance.
- Test seam in Go: a `QueryBinder` interface with `Bind(queryID, backendURL, group, externalURL string)` injected into the post-response pipeline. The actual JSON-id-extraction is a pure function `extractQueryID(body []byte) (string, error)`. Both table-testable.
- Existing Java test for the path-extraction half: `TestQueryIdCachingProxyHandler.testExtractQueryIdFromUrl` — 15 oracle cases including queued/scheduled/executing/partialCancel paths and the UI variants. Lift these directly into a Go table test.
  - Cite: `TestQueryIdCachingProxyHandler.java:39-72`.

#### Seam 6 — history-disabled negative test

When `dataStore.queryHistoryEnabled: false`, the gateway must continue to proxy requests successfully but MUST NOT write any `query_history` rows. This is the "feature flag" negative-path for Seam 6's binding write — proxy succeeds, async history write is short-circuited.

- **Java oracle to lift:** `TestProxyRequestHandlerQueryHistoryDisabled.testQueryHistoryNotRecordedWhenDisabled` (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/proxyserver/TestProxyRequestHandlerQueryHistoryDisabled.java`). Shape: boot gateway with `test-config-with-query-history-disabled.yml`, run `SELECT 1` via Trino JDBC driver, capture the query ID from `TrinoResultSet.getQueryId()`, then assert `SELECT * FROM query_history WHERE query_id = :queryId` returns zero rows.
- **Oracle (two assertions, both required):**
  1. The proxied query succeeds end-to-end (JDBC driver returns `1`, query ID is non-null).
  2. The `query_history` table contains zero rows for that query ID after the binding window elapses.
- **Porting notes for Go:**
  - **Replace the 2-second sleep with poll-with-timeout.** Java uses `sleepUninterruptibly(2, SECONDS)` as a "wait long enough for the async write that shouldn't happen" — a flake in either direction (false-pass if 2s is too short for the would-be write; false-fail if CI is slow). Go must use a bounded poll: query `query_history` every ~50ms until either a row appears (test FAILS — feature flag broken) or a deadline elapses (test PASSES — flag confirmed honored). Pick the deadline to comfortably exceed the gateway's async-write SLA; 2s as the deadline is fine, but the mechanism must be poll-not-sleep so a slow CI doesn't false-pass and a fast write doesn't false-pass either.
  - **Port `Math.random()` port allocation to `:0`.** Java uses `22001 + (int)(Math.random() * 1000)` for `routerPort`; Go must bind `127.0.0.1:0` and read back the port from `net.Listener.Addr()`. This is the same `drop` obligation called out for tests broadly.
  - **Pin the test fixture's config keys explicitly in Go.** The Java config (`test-config-with-query-history-disabled.yml`) sets `dataStore.queryHistoryEnabled: false` — that's the single load-bearing knob, plus the standard `dataStore.{jdbcUrl,user,password,driver}` envelope, `serverConfig.http-server.http.port`, `clusterStatsConfiguration.monitorType: INFO_API`, and `monitor.taskDelay: 1s`. The Go test fixture should hard-pin these keys (not load a YAML by reference) so a refactor of the Java fixture cannot silently desynchronize the Go test. Equivalent Go config shape:
    ```yaml
    dataStore:
      queryHistoryEnabled: false        # the flag under test
      jdbcUrl: <postgres testcontainer URL>
      user: <pg user>
      password: <pg password>
      driver: org.postgresql.Driver     # or Go-side equivalent driver name
    serverConfig:
      http-server.http.port: 0          # Go: bind 0, read back
    clusterStatsConfiguration:
      monitorType: INFO_API
    monitor:
      taskDelay: 1s
    ```
  - **Infrastructure dependencies:** a real Trino container (`trinodb/trino:476`-or-later equivalent) and a Postgres container, both via `testcontainers-go`. The driver must be a real Trino driver (Go equivalent: `trinodb/trino-go-client`) so the request actually traverses the proxy. JDBC-side capture of the query ID via `TrinoResultSet.getQueryId()` maps to `trino-go-client`'s `Rows.NextResultSet`/stmt-info equivalent — verify on first port.
- **Test seam in Go:** the `QueryBinder` interface from the main Seam 6 description is the right injection point; the history-disabled fixture should wire it to a no-op writer (or whatever the production short-circuit is) and assert (a) `Bind` is or isn't called per the production semantics, and (b) the persistence-layer row count is zero after polling.
- **Why this lifts cleanly:** the assertions are wire/persistence-layer observables (query succeeds + zero rows), not Java-internal-state observables. Direct port.

### Seam 7 — gateway cookie emission
When `cookiesEnabled=true` and the request path starts with `OAUTH2_PATH`, an `OAuth2GatewayCookie` is appended to the response. When `includeClusterInfoInResponse=true` and the request matched a statement path, a `trinoClusterHost` cookie is also added.
- Cite: `ProxyRequestHandler.java:181-196,204-224`.
- Observable signals:
  - `Set-Cookie` header with name `trinoClusterHost` on statement POST responses (assertable on `httptest` client side).
  - `Set-Cookie` for OAuth2 cookie on OAuth2-path requests.
  - `Set-Cookie` with `value=delete` and `max-age=0` when an inbound gateway cookie should be invalidated (path-based).
- Test seam in Go: a `CookiePolicy` interface called post-response that returns a `[]*http.Cookie` to append. Pure-function-friendly.

### Seam 8 — error-to-status mapping
On a `ProxyException` from the upstream HTTP client, the handler logs `"Proxy request failed: <method> <uri>"` and returns `502 Bad Gateway` with `Content-Type: text/plain` and the exception message as body.
- Cite: `ProxyRequestHandler.java:254-267`.
- Observable signals: HTTP `502`, body equals the exception message string. Note this differs from the timeout 502 only in body content.
- Test seam in Go: assertable by stubbing the upstream `RoundTripper` to return an error.

### Cross-cutting concurrency notes
- The proxy is fully async (Airlift `FluentFuture` + an executor named `proxy-%s`). On `@PreDestroy`, the executor is `shutdownNow()`'d (`ProxyRequestHandler.java:115-119`). The Go equivalent must:
  - Propagate `context.Context` cancellation from the inbound request to the upstream HTTP call.
  - Cancel in-flight upstream requests on server shutdown via `http.Server.Shutdown(ctx)`.
  - Use `goleak.VerifyNone(t)` to confirm no goroutine outlives the test, on every package that spawns work.
- The `RoutingManager` query-ID → backend binding is shared mutable state across goroutines. Java relies on `ConcurrentMap`-style structures (`StochasticRoutingManager` implementation); the Go equivalent must use `sync.Map`, `sync.RWMutex`+map, or a TTL-bounded cache (`hashicorp/golang-lru/v2`). `-race` must be mandatory.

### Statement-paths configuration
`statementPaths` is a configurable list; default is `["/v1/statement"]` but operators can add custom paths. The Java tests verify a custom path (`/v1/custom`) works (`TestProxyRequestHandler.customPutEndpoint`).
- Cite: `ProxyRequestHandler.java:110`; usage at `:190`.
- Go obligation: the statement-path matcher must accept a list and use prefix matching (`path.startsWith(prefix)` in Java translates to `strings.HasPrefix(p, prefix)` in Go — exact replication).

### `extraWhitelistPaths`
Configured patterns (regex) that bypass authentication and route as proxy requests. Test config includes `/v1/custom.*` and `/custom/logout.*` (`test-config-template.yml:18-20`).
- Test seam: routing decisions must apply equally to whitelisted paths.

## Behavior vs. Implementation Artifact

### `Host` header is NOT forwarded to backend
- **Observed behavior:** `PRESERVED_HEADERS_TO_SKIP` contains `"Host"` (`ProxyRequestHandler.java:82-84`).
- **Source of behavior:** `protocol-required` — when reverse-proxying, the upstream Host header must reflect the backend, not the inbound client.
- **Rationale:** Standard reverse-proxy behavior; sending the gateway's `Host` to Trino would break virtual-host routing.
- **Go obligation:** `replicate-exactly`. Note: Go's `httputil.ReverseProxy.Director` does NOT do this automatically — it copies the request URL but leaves `Host` alone unless explicitly rewritten. We must set `outReq.Host = backendURL.Host` (or leave it empty so `http.Client` derives it from `outReq.URL.Host`).
- **Notes:** Write an explicit test asserting the backend's `RecordedRequest.Host` equals the backend authority, not the inbound client's.

### `Accept-Encoding` is NOT forwarded
- **Observed behavior:** Stripped (`ProxyRequestHandler.java:82-84`).
- **Source of behavior:** `gateway-design-intent` — the gateway wants to receive identity-encoded responses so it can parse the query-ID JSON without decompressing.
- **Rationale:** Avoids needing to decompress upstream responses just to extract the `id` field.
- **Go obligation:** `replicate-exactly`.
- **Notes:** A failure here is silent in production (queries route but lose stickiness because ID extraction fails). The Java suite does not test this. **Flag for the Go suite — explicit test.**

### `Via: <protocol> TrinoGateway` injected on every request
- **Observed behavior:** `ProxyRequestHandler.java:326`.
- **Source of behavior:** `protocol-required` (RFC 7230 §5.7.1 — proxies should add `Via`).
- **Go obligation:** `replicate-exactly`. Naming question: do we keep the literal string `"TrinoGateway"` or change to `"TrinoGoway"`? **Defer to @architect**. From a parity-test angle, keeping `TrinoGateway` lets us run the same backend assertions against Java and Go.

### Async-timeout error body missing a space
- **Observed behavior:** `"Request to remote Trino server timed out after" + asyncTimeout` (no space between `after` and the duration). `ProxyRequestHandler.java:245`.
- **Source of behavior:** `defensive-historical` — typo.
- **Go obligation:** `defer-to-expert`. From a parity perspective I'd replicate exactly; from a UX perspective I'd fix. @architect please decide.

### Random per-request executor (`newCachedThreadPool`)
- **Observed behavior:** A per-`ProxyRequestHandler` cached thread pool with daemon threads.
- **Source of behavior:** `jvm-artifact` — Airlift's async model requires explicit executors.
- **Go obligation:** `drop`. Goroutines are the executor; the request `context.Context` plus an `*http.Client` per gateway instance is sufficient.

## Implications for Go Rewrite

- Each of the eight seams should map to a small interface or pure function injected into the request pipeline. This is non-negotiable for testability — a monolithic `ServeHTTP` that does routing + header forwarding + upstream call + cookie emission inline is impossible to unit-test cleanly. Suggested interfaces (final API is up to @go-implementer):
  - `RoutingTargetResolver`
  - `HeaderForwarder` (or just a pure function)
  - `QueryIDExtractor` (URL- and body-based; pure)
  - `QueryBinder` (side-effect interface; mockable)
  - `CookiePolicy`
  - `UpstreamClient` (wraps `*http.Client` so we can inject a fake `RoundTripper` in tests)
- For the upstream HTTP call, prefer composing the proxy from `http.Handler` + `*http.Client` over using `httputil.ReverseProxy` wholesale. `ReverseProxy` is great for simple cases but its hook surface (`Director`, `ModifyResponse`, `ErrorHandler`) is too narrow to express "parse the response body to extract a query-ID and bind it in a side-channel" cleanly. A hand-rolled handler gives us complete control.
- Statement-POST response handling is the most behavior-rich seam and has the highest test investment: ID extraction (pure, table-test), binding (interface mock), error-path handling (table-test including malformed JSON, non-200 status, body-too-large guard).
- Body buffering for ID extraction: the Java code reads the upstream response into memory (`response.body()` is materialized). The Go equivalent must either (a) buffer the full body in memory and write it back to the client, or (b) tee the body to both a JSON parser and the client. For large result sets (`/v1/statement` responses can include up to ~1MB of data per page), **option (b) with `io.TeeReader` + a bounded ring buffer is preferable** to keep streaming semantics intact. Worth a test asserting the client sees the response streaming, not buffered.
- Context propagation: every test that exercises the proxy should assert that cancelling the inbound `Request.Context()` cancels the upstream request. The Java suite has no equivalent assertion — this is a Go-specific reliability gain.

## Test Strategy Hooks

- **Test level:** mix.
  - Unit (pure-function): seams 2, 3, 4, header policy, ID extraction (URL + body).
  - Unit (interface mock): seams 1, 6 (binder), 7 (cookie policy), 8 (error mapping).
  - Integration (`httptest.Server` backends): seams 5, 6 end-to-end stickiness, 7 emitted on real responses.
  - Integration (testcontainers Trino): full statement protocol roundtrip (needed for confidence that real Trino responses match our parse assumptions).
- **Fixtures required:** sample real-Trino POST responses to `/v1/statement` (capture once, store as golden files); the rules YAMLs (translated from MVEL); a fake upstream that supports configurable latency for timeout tests; cancelable RoundTripper for context-propagation tests.
- **Observable signals:** status codes (200/502), `Content-Type`, `Set-Cookie` headers (`trinoClusterHost`, OAuth2), `Via` header on backend-side recorded request, absence of `Host`/`Accept-Encoding`/`X-Forwarded-*` on backend-side recorded request (when fwd disabled), routing-decision string from `RoutingTargetResolver`, query-ID binding state in `QueryBinder` mock, `query_history` row presence/absence and field values, and timeout-error body substring `"timed out after"` (the only string-content oracle worth keeping — it's user-visible response body, not log output). **No log-substring oracles** — log format is operator-only and not stable across the Go rewrite (which will use `log/slog`).
- **Non-determinism risks:**
  - Async timeout test needs deterministic blocking — use a `chan struct{}` in the fake upstream, not `time.Sleep`.
  - Stickiness test needs the binder mock to be observed AFTER the response handler completes — use the binder's `Bind` invocation as the sync point, not arbitrary sleeps.
  - Goroutine leak in failed-upstream paths — `goleak.VerifyNone(t)` in `TestMain`.

## Open Questions

- @architect: do we keep the literal string `"TrinoGateway"` in the `Via` header for parity, or rebrand to `"TrinoGoway"`?
- @architect: do we fix the missing space in the timeout error body, or replicate the typo?
- @architect/@go-implementer: agree on whether the proxy is built on `httputil.ReverseProxy` (simpler, less flexible) or hand-rolled `http.Handler` (more code, full control). I argue for hand-rolled given seam 6's complexity.
- @trino-expert: confirm `extractQueryIdIfPresent` handles every statement-path variant Trino actually uses today. The Java test enumerates queued/scheduled/executing/partialCancel and UI variants — are there others post-`trino@481` we should add?
- ~~@java-qa: are there `TestProxyRequestHandlerQueryHistoryDisabled`-style negative-path tests I should also lift into Go?~~ **RESOLVED by java-qa:** yes; the history-disabled feature-flag negative test is lifted as the new "Seam 6 — history-disabled negative test" subsection above, with poll-with-timeout / `:0` port / pinned-config-keys porting notes.

## Cross-references

- [[test-infrastructure.go-qa.md]] — overall tooling inventory.
- [[routing-engine.go-qa.md]] — routing-rule oracle in detail.
- [[test-gaps-and-risks.go-qa.md]] — including the MVEL parity problem.
