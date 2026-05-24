---
title: Test gaps and high-risk untested behaviors
author: java-qa
role: Java QA
component: trino-gateway
topics: [test-infra, cross-cutting, proxy-core, routing-engine, statement-protocol, observability]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/test-infrastructure.java-qa.md
  - trino-gateway/proxy-request-lifecycle.java-qa.md
  - trino-gateway/routing-engine.java-qa.md
  - both/statement-protocol-invariants.java-qa.md
---

# Test gaps and high-risk untested behaviors

## Summary

Risk register of trino-gateway behaviours that have thin or absent Java test coverage and are at high risk of silent regression in the Go rewrite. The single most important takeaway: the existing Java suite covers happy paths and a handful of explicit edge cases well, but **failure-mode, concurrency, streaming, and operator-facing observability are systematically under-tested** — these are exactly the behaviours the Go rewrite needs new tests for, not ports.

## Key Findings

### Risk register (sorted high → low)

| # | Gap | Component | Risk | Mode |
|---|---|---|---|---|
| G1 | `nextUri` host derivation against real Trino | proxy-core, statement-protocol | **HIGH** | wire-correctness |
| G2 | Multi-valued protocol header round-trip | proxy-core, statement-protocol | **HIGH** | wire-correctness |
| G3 | Backend mid-query failure (connection reset, 5xx mid-poll) | proxy-core | **HIGH** | failure-mode |
| G4 | `QueryCountBasedRouter` concurrency / parallel `selectBackend` | routing-engine | **HIGH** | concurrency |
| G4a | Stochastic-router distribution (RandomBackendSelector with N equal-weight HEALTHY backends) | routing-engine | medium | concurrency / coverage |
| G5 | Async timeout (Seam 6) — no explicit test | proxy-core | **HIGH** | failure-mode |
| G6 | Response-body size cap behaviour (`responseSize`) | proxy-core | **HIGH** | failure-mode |
| G7 | `searchAllBackendForQuery` `isDone()` race | routing-engine | medium | concurrency / known-bug |
| G8 | Streaming vs. buffering behaviour of large responses | proxy-core, statement-protocol | medium | wire-correctness |
| G9 | External-router failure modes (network error, malformed JSON) | routing-engine | medium | failure-mode |
| G10 | Cluster-monitor failure modes (backend unreachable for stats) | health-checks | medium | failure-mode |
| G11 | Hot-reload race for routing rules under concurrent traffic | routing-engine | medium | concurrency |
| G12 | Gateway shutdown / graceful drain | cross-cutting | medium | lifecycle |
| G13 | DB migration failure mid-step | persistence | medium | failure-mode |
| G14 | Status-code preservation matrix (full enumeration) | proxy-core | medium | wire-correctness |
| G15 | `Authorization` header propagation under each auth mode | auth | medium | wire-correctness |
| G16 | Gateway cookie collision with backend cookies | auth, proxy-core | low | wire-correctness |
| G17 | Forwarded-headers behaviour when enabled (existing test only covers disabled) | proxy-core | low | wire-correctness |
| G18 | Spooled query data passthrough | statement-protocol | low | new-feature-shaped |
| G19 | Gzip response handling | proxy-core | low | wire-correctness |
| G20 | Metrics endpoint output shape stability | observability | low | operator-contract |

Detail follows.

### G1 — `nextUri` host derivation against real Trino

- **Why it matters:** if the Go gateway's response body's `nextUri` points at the backend instead of the gateway, every client poll bypasses the gateway and the routing/session/history features silently break. Worst-case failure mode: routing works for the first request and then mysteriously stops working.
- **Current Java coverage:** indirect only. `TestGatewayHaSingleBackend.testRequestDelivery` (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaSingleBackend.java:65-92`) asserts the response body contains the client-supplied `Host` value (`test.host.com`) — but does NOT trace the mechanism (is it `X-Forwarded-Host`? `Host` propagation despite gateway dropping it? Trino-side `http-server.public-url` config?).
- **What's needed:** an e2e test that boots a real `trinodb/trino` container behind the Go gateway, posts a `SELECT 1`, parses the response `nextUri`, and asserts the host:port is the gateway's bound address. Then GETs that `nextUri` through the gateway and asserts a 200 reaches it. And a negative test that proves the gateway's behaviour when the mechanism is broken (e.g. `forwardedHeadersEnabled=false` and no `X-Forwarded-Host`).
- **Open question to @trino-expert** flagged in `[[../both/statement-protocol-invariants.java-qa.md]]`.

### G2 — Multi-valued protocol header round-trip

- **Why it matters:** `X-Trino-Set-Session`, `X-Trino-Added-Prepare`, `X-Trino-Clear-Session`, etc. are multi-valued; the entire prepared-statement and session-property feature surfaces depend on them. A gateway that collapses N values to a single comma-joined value corrupts state silently.
- **Current Java coverage:** none direct that I located. The integration tests don't assert on multi-valued response headers; the test for `X-Trino-Prepared-Statement` (request side) uses a single-value header at `TestRoutingGroupSelector.java:255-279`.
- **What's needed:** mock backend returns a response with three distinct `X-Trino-Added-Prepare` headers; gateway-proxied response must contain three distinct values (not joined, not the last-wins). Same for `X-Trino-Set-Session`. Same on the request side: client sends three `X-Trino-Session: a=1`, `b=2`, `c=3` headers; mock backend records that it received three separate values.

### G3 — Backend mid-query failure

- **Why it matters:** the most common production failure mode is a backend dying or returning a 5xx in the middle of a query lifecycle. Client behaviour depends on whether the gateway propagates the failure faithfully (`nextUri` poll → backend 503 → client sees 503 and retries) or wraps it (`nextUri` poll → backend 503 → gateway returns 502, client interprets as terminal). The current behaviour wraps to 502 for any client exception, but pure HTTP 503 responses *should* pass through.
- **Current Java coverage:** the proxy-handler tests don't exercise mid-lifecycle backend failures; they test single-shot POST/GET success and 404 dispatching. `TestProxyRequestHandler.java` covers happy + 404 paths only.
- **What's needed:** mock backend that returns 503 on a `nextUri` GET; assert gateway returns 503 with the same body (or at minimum the same status). Mock backend that closes the connection mid-response; assert gateway returns 502 with a meaningful body. Mock backend that returns a `QueryResults` JSON whose `error` field is non-null; assert gateway forwards body unchanged.

### G4 — `QueryCountBasedRouter` concurrency

- **Why it matters:** the router's local-stats update is `synchronized` (`QueryCountBasedRouter.java:206, 234`) but the existing tests are all serial (`TestQueryCountBasedRouter` calls `selectBackend` from one thread). A regression that removes synchronisation, or a Go port that uses sloppy mutex discipline, would produce thundering-herd routing under bursty load that no existing test catches.
- **Current Java coverage:** none. `TestQueryCountBasedRouter` (303 lines) is entirely single-threaded.
- **What's needed:** Go test that calls `provideBackendConfiguration` from N goroutines simultaneously and asserts (a) no panic, (b) the resulting cluster-stats snapshot is internally consistent (sum of increments equals N), (c) the distribution is "balanced" within tolerance (no single cluster gets >K% of requests when all are equal-load). This is a new Go-side test; nothing to port.

### G5 — Async timeout (Seam 6)

- **Why it matters:** `ProxyRequestHandler` returns `502 BAD_GATEWAY` with body `"Request to remote Trino server timed out after<duration>"` when the configured `asyncTimeout` fires. Operators rely on this for SLA signalling; a regression that returns 504 instead, or silently hangs, is operationally damaging.
- **Observable signals (must be asserted, not implied):**
  - HTTP status `502 BAD_GATEWAY` (NOT 504, NOT 500, NOT 200-with-error-body).
  - Response body starts with the exact prefix `"Request to remote Trino server timed out after"` followed by the configured duration.
  - Wall-clock elapsed time between request dispatch and response: `asyncTimeout ± budget`, never indefinitely hung.
  - No backend-side connection leak: the backend's in-flight request is cancelled / connection torn down, not left dangling for the full backend-side timeout.
- **Current Java coverage:** none that I located. The code path at `ProxyRequestHandler.java:239-247` is uncovered by any direct test. Coverage state: **0% — purely defensive code that has never been exercised by a test.**
- **What's needed:**
  - Mock backend that sleeps for `asyncTimeout + 100ms`; assert gateway returns 502 within `asyncTimeout + budget`; assert response body starts with the expected prefix.
  - Boundary test: backend responds *exactly* at `asyncTimeout` (race condition — gateway should produce one of two outcomes consistently, not flap).
  - Backend-cancellation test: after the gateway returns 502, assert the backend either observed a client disconnect or received a cancellation, not that it ran the full request to completion in the background (resource leak).
- **Cross-references:** Seam 6 in `[[proxy-request-lifecycle.java-qa.md]]`; this is the timeout half of the proxy-core degradation-test requirement in go-qa's component sign-off rubric. Pair with G3 (backend mid-query failure) — both are proxy-core failure-mode tests, both need the same "misbehaving backend" fixture.

### G6 — Response-body size cap

- **Why it matters:** `ProxyResponseHandler.handle` uses `readNBytes((int) responseSize.toBytes())`, which silently truncates anything larger than the cap. A backend returning a 100MB UI asset against a 50MB cap returns a 50MB UTF-8 string that may be invalid mid-byte. The client then gets a malformed response.
- **Current Java coverage:** none. The cap is exercised implicitly (every test runs under the default cap) but never tested at the boundary.
- **What's needed:** mock backend returns a body that is `responseSize - 1`, `responseSize`, `responseSize + 1`, and `responseSize * 2` bytes; assert gateway behaviour at each. Also test: mock backend returns a multi-byte UTF-8 sequence whose final byte falls exactly at the cap; assert the gateway does not return a partial multi-byte sequence (or document that it does).

### G7 — `searchAllBackendForQuery` race

- **Why it matters:** the fallback query-id lookup probes all backends in parallel and accepts the first 200, but only iterates `responseCodes.entrySet()` and checks `isDone()`, so the answer-bearing backend can be missed if it's slow. `BaseRoutingManager.java:220-227`. The Go rewrite has an obvious opportunity to fix this (`select` over channels) but must not regress the intent.
- **Current Java coverage:** none direct. `TestRoutingManagerExternalUrlCache` covers cache behaviour but not the fallback search.
- **What's needed:** test that simulates restart-with-cache-loss: pre-populate query history with `(queryId, backend1)`; cold-start a gateway with cache empty; poll for `queryId`; assert it lands on `backend1`. Test the race: backend1 holds the answer but is slow; backend2 returns 404 fast; in the current Java code the gateway would mis-route to backend2's "first active backend" fallback. The Go rewrite should test the intent (request reaches backend1).

### G8 — Streaming vs. buffering for large responses

- **Why it matters:** depends on the Architect's streaming decision. If buffering: G6 applies. If streaming: there's a new test surface (TTFB, chunk preservation, body integrity under partial backend response). The Java code buffers; therefore there's no streaming test today.
- **Current Java coverage:** none on streaming (because the gateway doesn't stream).
- **What's needed:** TBD, gated on architect decision.

### G9 — External-router failure modes

- **Why it matters:** `ExternalRoutingGroupSelector` is designed to fall back to header-mode on any exception (`ExternalRoutingGroupSelector.java:135-141`), but the *kinds* of exceptions matter — a connection-refused vs. a 500 response vs. an invalid JSON body vs. a timeout each go down slightly different code paths. Only the happy path is heavily tested.
- **Current Java coverage:** `TestExternalRoutingGroupSelector` covers some failures (errors-with-propagate, errors-without-propagate, request body shape) but I did not find tests for: connection refused, 500 from the external service, invalid JSON in the response, timeout.
- **What's needed:** test each failure mode explicitly with a `httptest.Server` that simulates the failure; assert the gateway falls back to header-mode routing in each case.

### G10 — Cluster-monitor failure modes

- **Why it matters:** `ClusterStatsHttpMonitor` polls each backend's `/v1/info` periodically; if the poll fails (timeout, connection refused, 500), the backend should transition to `UNHEALTHY` and be removed from routing. Bad behaviour: backend flaps between HEALTHY/UNHEALTHY on transient failures (no debouncing); backend marked HEALTHY based on a stale cache despite current pings failing.
- **Current Java coverage:** `TestClusterStatsMonitor` exists but I have not read its contents; it likely covers happy paths. Failure modes are unclear from naming alone.
- **What's needed:** read `TestClusterStatsMonitor` to map current coverage; add: backend unreachable for K consecutive polls → transitions to UNHEALTHY; backend returns 500 → behaviour matches connection failure; backend recovers → transitions back to HEALTHY without delay; concurrent stats updates from multiple monitor types (HTTP + JMX) don't corrupt state.

### G11 — Hot-reload race for routing rules

- **Why it matters:** `FileBasedRoutingGroupSelector` uses `Suppliers.memoizeWithExpiration` which is lock-free but may briefly serve stale rules during the swap. Under concurrent traffic mid-reload, some requests may evaluate against the old rules and some against the new, with no guarantee of atomicity. The single existing test (`TestRoutingGroupSelector.testByRoutingRulesEngineFileChange`) is single-threaded and uses `Thread.sleep`.
- **Current Java coverage:** the file-change test exists but is serial.
- **What's needed:** test that runs concurrent traffic (N goroutines posting requests) while modifying the rules file mid-flight; assert (a) no panic, (b) every routing decision is internally consistent (the rule that fired and the resulting group match a single rules-file generation), (c) eventual consistency (all requests after T evaluate against the new rules).

### G12 — Gateway shutdown / graceful drain

- **Why it matters:** `ProxyRequestHandler.shutdown()` calls `executor.shutdownNow()` (`ProxyRequestHandler.java:115-119`), which interrupts in-flight requests rather than draining. `BaseRoutingManager.shutdown()` is the same. In production, this means SIGTERM during a deployment kills in-flight queries' connections rather than letting them complete; clients see truncated responses or 502s.
- **Current Java coverage:** none. Lifecycle / shutdown is untested at the integration level.
- **What's needed:** test that starts a long-running request through the gateway, triggers shutdown, and asserts (a) the in-flight request either completes or fails cleanly within a bounded time, (b) no goroutine leaks. The "right" behaviour (drain vs. abort) is a design decision — but whatever it is, it should be tested.

### G13 — DB migration failure mid-step

- **Why it matters:** the gateway uses Flyway-managed migrations against MySQL/Postgres/Oracle. A migration that fails halfway through (e.g. DDL succeeds, DML fails) leaves the schema in an indeterminate state. Restart behaviour depends on Flyway's checksum tracking.
- **Current Java coverage:** `BaseTestDatabaseMigrations` + per-engine subclasses test successful migrations. I did not see explicit failure-mode tests.
- **What's needed:** inject a failure into a specific migration step (e.g. a migration that references a non-existent column on Oracle) and assert the gateway either retries cleanly or surfaces a meaningful error to the operator. Lower priority than G1-G11 because the failure mode is loud (gateway fails to start) rather than silent.

### G14 — Status-code preservation matrix

- **Why it matters:** the proxy is expected to forward backend status codes verbatim, but I have not seen a single test that enumerates 200, 204, 400, 401, 403, 404, 500, 503, 504 and asserts each is preserved end-to-end. Coverage is incidental — `404` is exercised because dispatchers default to it, `200` is exercised because tests need success, but the rest may regress.
- **Current Java coverage:** partial; status codes that appear in existing tests: 200, 204 (`testDeleteQueryId`), 404 (custom path test), 500 (cookie tamper).
- **What's needed:** table-driven test: for each canonical status code, mock backend returns it; assert gateway returns it. One exception: 5xx from the backend should NOT be wrapped to 502 unless the connection actually failed.

### G15 — `Authorization` header propagation under each auth mode

- **Why it matters:** the gateway has multiple auth modes (Basic, Form, OAuth2, OIDC, LDAP). Each mode may strip, replace, or augment the `Authorization` header before forwarding to the backend. Tests for each auth mode exist but I have not verified they all assert on the *forwarded* header value at the backend.
- **Current Java coverage:** `TestBasicCredentials`, `TestLbAuthenticator`, `TestLbLdapClient`, `TestOIDC` — coverage exists for the auth flow itself; coverage for forwarded-header content is unclear.
- **What's needed:** for each auth mode, table-driven test: client sends incoming `Authorization` header A; assert backend receives `Authorization` header B (where B may be A, transformed, or absent depending on mode). This is the kind of test that catches accidental privilege leaks.

### G16 — Gateway cookie collision with backend cookies

- **Why it matters:** the gateway issues cookies (`GatewayCookie.PREFIX`-prefixed, `trinoClusterHost`, OAuth2 cookies); the backend may also issue cookies (`X-Trino-Set-Cookie`-style, OAuth state, etc.). A name collision could cause the gateway to mis-parse a backend-issued cookie as its own, or vice versa.
- **Current Java coverage:** none explicit. The cookie tests focus on signing and stickiness, not collision.
- **What's needed:** mock backend that responds with a cookie whose name happens to match the `GatewayCookie.PREFIX`; assert the gateway forwards it without confusing it for an internal cookie.

### G17 — Forwarded-headers behaviour when enabled

- **Why it matters:** `forwardedHeadersEnabled=true` adds `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Port`, `X-Forwarded-Host`. The existing test (`TestForwardedHeadersDisabled`) covers only the disabled path. Per G1, `X-Forwarded-Host` may be the mechanism by which `nextUri` gets its host — so this is potentially co-load-bearing with the highest-risk gap.
- **Current Java coverage:** disabled path only.
- **What's needed:** test that asserts each of the four forwarded headers reaches the backend with the right value when forwarded-headers are enabled; chained-proxy test (client sends its own `X-Forwarded-For: <upstream>`; gateway appends its own value; backend sees the chain).

### G18 — Spooled query data passthrough

- **Why it matters:** newer Trino versions return data segments via external URIs (S3, etc.). If the gateway is expected to passthrough these URIs unchanged, fine; if it's expected to proxy the segment GETs through itself (for auth or audit reasons), that's a new code path.
- **Current Java coverage:** none.
- **What's needed:** Open Question for the Trino Expert (cross-listed in `[[../both/statement-protocol-invariants.java-qa.md]]`). If "passthrough," a test that asserts the gateway does not rewrite spool URIs.

### G19 — Gzip response handling

- **Why it matters:** the gateway strips `Accept-Encoding` from forwarded requests, so backend responses are uncompressed and bandwidth is wasted. If a future Go rewrite re-enables gzip (because it streams instead of buffers, say), the wire shape changes — clients may see gzip when they didn't before.
- **Current Java coverage:** none — gzip path is short-circuited by the header strip.
- **What's needed:** decision from Architect on streaming + gzip. Then: test that asserts the agreed wire shape (gzip or not) reaches the client.

### G20 — Metrics endpoint output shape stability

- **Why it matters:** `/metrics` returns Prometheus-formatted lines like `trino1_TrinoStatusHealthy`. The naming convention is JMX-derived and JVM-specific; operators may have dashboards pinned on these names. The single existing test (`TestGatewayHaMultipleBackend.testClusterStatsJMX:400-412`) asserts on substring presence only.
- **Current Java coverage:** substring assertion only.
- **What's needed:** decision from Architect on whether to preserve names. Then: snapshot test of the full `/metrics` output for a known cluster topology.

### Summary by failure pattern

- **Wire-correctness gaps:** G1, G2, G14, G15, G17, G19 — these regress *silently*; a Go rewrite would pass all integration tests against mock backends and still break real clients.
- **Failure-mode gaps:** G3, G5, G6, G9, G10, G13 — happy paths work; specific failure modes are untested.
- **Concurrency gaps:** G4, G7, G11 — single-threaded tests hide threading bugs.
- **Lifecycle gaps:** G12 — shutdown/startup state machines untested.
- **Operator-contract gaps:** G20 — observability surface shape untested.
- **New-feature-shaped gaps:** G18, G8 — depends on architect/expert decisions about scope.

## Behavior vs. Implementation Artifact

n/a — this file is a meta-study of test coverage, not of system behaviour. Each gap above points back to the substantive study where the behaviour/artifact decomposition lives.

## Implications for Go Rewrite

- The Go rewrite needs **new tests, not ports**, for every gap in this register. Porting the Java suite is necessary but not sufficient — it would cover what's covered today, not what's gapped.
- Prioritise the six HIGH-risk gaps (G1, G2, G3, G4, G5, G6) before any code is written. These define the test bar a Go implementation must clear before being declared "feature-parity."
- G1 (nextUri host) is the single most expensive bug to ship; budget for an e2e test against real Trino as a P0 deliverable.
- Concurrency gaps (G4, G7, G11) need explicit goroutine-based tests with N-of-K assertions on distribution / consistency. These are easier to write in Go than in Java (Go's `testing.RunParallel` and goroutines beat JUnit + ExecutorService) — take the opportunity.
- Failure-mode gaps (G3, G5, G6, G9) all share a pattern: configure mock backend to misbehave in a specific way, assert gateway behaviour. A reusable "misbehaving backend" test helper (`MockBackend.respondWithStatus(503)`, `.respondAfterDelay(2*time.Second)`, `.closeConnectionMidResponse()`) pays for itself many times over.
- For the operator-contract gaps (G20, parts of G15, G17), get the wire shape decided BEFORE writing tests; otherwise tests will encode the wrong contract.

## Test Strategy Hooks

- **Test level:** mix — most gaps need integration tests with controllable mock backends; G1 needs e2e against real Trino; G4/G7/G11 need unit-or-integration concurrency tests with goroutines; G12/G13 need lifecycle/process-shutdown tests.
- **Fixtures required:** "misbehaving backend" helper (slow, error-returning, connection-closing, multi-valued-header-emitting); real `trinodb/trino` container for G1; rules-file template that can be rewritten mid-test; auth fixtures for each mode (G15); a way to inject Flyway migration failures for G13.
- **Observable signals:** the same set as the prior three studies — HTTP status, headers (single and multi-valued), body content, log lines, history DB rows, cluster-stats cache, metrics endpoint, cookies. The gap is not in the *kind* of observation but in the *scenario* being observed.
- **Non-determinism risks:** every concurrency test (G4, G7, G11) needs a tolerance budget for distribution assertions; every timeout test (G5) needs a fakeable clock or generous-but-bounded real-time budget; every lifecycle test (G12, G13) needs cleanup discipline so a failure doesn't leak processes/containers.

## Open Questions

- @qa-tech-lead: should we triage this register before the Go rewrite begins (decide which gaps are blockers vs. follow-ups), or treat all HIGH-risk gaps as blockers by default?
- @qa-tech-lead: do you want me to expand each HIGH-risk gap into its own per-gap test spec, or wait until the Architect has produced the Go design so the test specs can be Go-shaped?
- @trino-expert: G1 (nextUri host derivation) is by far the highest-risk gap. Will you take this question first, before I expand any other gaps?
- @architect: G6 (response-size cap) and G8 (streaming) are interrelated and both depend on your decision about body handling. Until that lands, both gaps stay in the "shape unknown" bucket.
- @architect: G18 (spooled data) and G20 (metric naming) depend on scope decisions only you can make.

## Cross-references

- `[[test-infrastructure.java-qa.md]]` — describes the test patterns we DO have; this file is the inverse map.
- `[[proxy-request-lifecycle.java-qa.md]]` — Seams 5, 6, 8 are the largest gaps.
- `[[routing-engine.java-qa.md]]` — concurrency and external-router failure-mode gaps live here.
- `[[../both/statement-protocol-invariants.java-qa.md]]` — G1, G2, G14, G17, G18, G19 are all wire-protocol-shaped.
