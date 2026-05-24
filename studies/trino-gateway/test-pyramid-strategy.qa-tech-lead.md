---
title: Test pyramid strategy for the Go rewrite of trino-gateway
author: qa-tech-lead
role: QA Tech Lead
component: trino-gateway
topics: [test-infra, proxy-core, routing-engine, statement-protocol, cross-cutting]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - both/test-infrastructure-needs.qa-tech-lead.md
  - both/component-signoff-rubric.qa-tech-lead.md
  - trino-gateway/test-infrastructure.java-qa.md
---

# Test pyramid strategy for the Go rewrite of trino-gateway

## Summary

A reverse-proxy gateway is integration-heavy by nature, and trino-gateway makes that worse by entangling four concerns the Go rewrite must not break: routing decisions, header forwarding, query-id-to-backend binding from a parsed JSON response, and a buffered (not streaming) proxy body. The Java suite is dominated by end-to-end tests that boot the whole gateway against a real `TrinoContainer` and `PostgreSQLContainer`; pure unit tests cover only the input-parsing and rule-evaluation pieces. The Go pyramid will look similar in shape but must add two test categories Java does not have today — protocol-fidelity (differential against the Java gateway) and concurrency stress — because Go's `net/http` differs from Jetty in exactly the places where bugs are silent and load-dependent.

## Key Findings

### The Java suite is end-to-end heavy, by necessity

- 57 Java test files under `trino-gateway/gateway-ha/src/test/java/`. The classes that exercise the proxy itself (`TestProxyRequestHandler`, `TestGatewayHaMultipleBackend`, `TestGatewayHaSingleBackend`, `TestGatewayHaWithRoutingRulesSingleBackend`, `TestForwardedHeadersDisabled`) all use the pattern `boot HaGatewayLauncher.main(args) → spin up MockWebServer or TrinoContainer backends → drive via okhttp HTTP requests → assert on response bodies/headers/cookies/DB state`. Citations: `gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:83-144` (setup), `gateway-ha/src/test/java/io/trino/gateway/proxyserver/TestProxyRequestHandler.java:69-108` (setup).
- The reason this is e2e-heavy is structural, not stylistic: the proxy class `ProxyRequestHandler` is wired with constructor-injected `HttpClient`, `RoutingManager`, `QueryHistoryManager`, and `HaGatewayConfiguration` (`gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:97-113`), and its work happens inside `performRequest` (`...:170-202`) which couples header transform → routing → HTTP call → JSON body parse → DB write → cookie attach in one method. There are no per-step seams that a fast unit test can call cleanly. Java QA's lifecycle-seams study (Task #24) will catalog these in detail.
- The one pure unit-test island is `getQueryDetailsFromRequest` (`...:303-314`), called directly from `TestProxyRequestHandler:154-187`. That's the exception that proves the rule.

### Existing test infra inventory (relevant excerpts; full inventory is Task #23)

- **Test stack** (from `gateway-ha/pom.xml`): JUnit 5 + AssertJ + Mockito + Testcontainers (`testcontainers-trino`, `-postgresql`, `-mysql`, `-oracle-free`) + okhttp `MockWebServer` + H2 + `trino-jdbc` + `trino-client`. The choice of `trino-client` matters — assertions about response shape use the real `QueryResults` JSON codec (`TestGatewayHaMultipleBackend.java:234-256`), so the gateway is being held to the actual wire contract, not a homegrown approximation.
- **Mock-backend pattern**: `MockWebServer` with a `Dispatcher` keyed off `request.getPath()`/`getMethod()` returns canned `MockResponse` bodies (`TestProxyRequestHandler.java:74-98`, `TestGatewayHaMultipleBackend.java:99-119`). This is the fast path. The real-Trino path uses `TrinoContainer("trinodb/trino")` with a copied `trino-config.properties` (`TestGatewayHaMultipleBackend.java:87-92`).
- **DB pattern**: every gateway-boot test uses a real `PostgreSQLContainer` via `TestcontainersUtils.createPostgreSqlContainer()`, not H2. H2 is used only for `seedRequiredData` migration verification (`HaGatewayTestUtils.java:73-81`). Implication: the Go suite cannot pretend persistence is a unit-level concern.
- **Port allocation**: tests pick router/backend ports via `20000 + (int)(Math.random()*1000)` (`TestGatewayHaMultipleBackend.java:76-77`). This is a flake source — a test-infra issue Go QA must improve on (use `:0` and read back, or a port allocator).
- **Coverage gaps in the Java suite** (handed off to Task #28 for the risk register): no streaming/chunked-response tests; no concurrency/load tests; no backend-fail-mid-query tests; cancellation tested only via DELETE on `nextUri` (`TestGatewayHaMultipleBackend.java:234-256`) but no abort-during-stream; no body-size cap tests of `ProxyResponseHandler.responseSize`.

### A critical structural finding: the proxy is buffered, not streaming

- `ProxyResponseHandler.handle` (`gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:47-55`) reads the entire backend response into a `String` capped at `responseSize` bytes, returning a record `ProxyResponse(int statusCode, ListMultimap headers, String body)`. There is no chunked passthrough, no streaming `Reader`. The downstream `recordBackendForQueryId` (`ProxyRequestHandler.java:269-301`) requires the full body to JSON-parse the `id` field — so buffering is load-bearing on POST `/v1/statement`, not just incidental.
- **This is a behavioral spec the Go rewrite must preserve or explicitly change.** A naive Go reimplementation using `httputil.ReverseProxy` would stream by default — that would break `recordBackendForQueryId` because the body would be consumed by the response writer before the JSON could be parsed. The Go design must either (a) buffer POST `/v1/statement` responses up to the same cap, then write, or (b) tee the body to a JSON-id-extractor stream while forwarding. This is on the architect's plate but it has a direct testing consequence: we need a body-size-cap test and a "body-larger-than-cap" behavioral test, neither of which exists in Java today.

### Routing has two layers and they're tested unevenly

- `RoutingTargetHandler.resolveRouting` (`gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:71-89`) first checks for a "previous cluster" binding (i.e. this request is part of an existing query's `nextUri` chain → route back to the same backend), and only otherwise consults the `RoutingGroupSelector` strategy. There is no unit test for the prefer-previous-cluster logic; it's only covered transitively through `testDeleteQueryId` and similar e2e tests (`TestGatewayHaMultipleBackend.java:234-256`).
- The `RoutingGroupSelector` family — `FileBasedRoutingGroupSelector`, `ExternalRoutingGroupSelector`, header-based, `MVELRoutingRule`, `QueryCountBasedRouter`, `StochasticRoutingManager` — has unit-test coverage of varying depth in `src/test/java/io/trino/gateway/ha/router/`. Java QA will catalog this per-strategy in Task #25.
- **Key implication for the Go pyramid:** unit tests can cover each `RoutingGroupSelector` strategy in isolation; the *integration* of "previous-cluster wins over rule" must be tested in an integration test that also exercises the persistence layer (because previous-cluster is looked up via `routingManager.findBackendForQueryId`, which can be backed by either the in-memory `RoutingManager` or the DB-backed `BaseRoutingManager`). This is one of the seams Go QA will need explicit fixtures for.

## Behavior vs. Implementation Artifact

### Buffered (non-streaming) proxy response body

- **Observed behavior:** the proxy reads the backend response fully into memory (capped at `responseSize`) before forwarding to the client. `ProxyResponseHandler.java:47-55`.
- **Source of behavior:** `gateway-design-intent`. Required to support `recordBackendForQueryId` (`ProxyRequestHandler.java:269-301`), which JSON-parses the response to extract the Trino query `id` and persist it to the query-history DB and the in-memory routing map. Without buffering, the `id` cannot be extracted on POST `/v1/statement` without consuming the body before it's written to the client.
- **Rationale:** the query→backend binding is the gateway's central data structure. It's how subsequent `nextUri` polls find their way back to the right cluster. The buffer is the price of that binding.
- **Go obligation:** `replicate-intent`. The Go design may stream non-statement responses, but POST `/v1/statement` responses must be buffered (or tee'd) to the same effect. Memory cap must be configurable and tested.
- **Notes:** the `responseSize` cap is the only mechanism preventing OOM on a malicious or buggy backend. The Java suite does not test the cap behavior — that gap must be closed in Go.

### Cookie-based sticky routing via signed cookies

- **Observed behavior:** when `cookiesEnabled`, the gateway attaches signed `GatewayCookie`s to identify the bound backend for OAuth2 flows; tampered cookies cause routing fallback (`TestGatewayHaMultipleBackend.java:335-374`, returning HTTP 500 on tamper).
- **Source of behavior:** `gateway-design-intent`. Solves the Trino OAuth2 handshake's need for affinity across the initiate/callback redirect pair.
- **Go obligation:** `replicate-exactly`. Signature scheme must be wire-compatible if Go and Java gateways ever run side-by-side during migration; otherwise `replicate-intent` is enough. Defer to architect for migration-overlap stance.
- **Notes:** HTTP 500 on tampered cookie is a Java implementation choice (an exception thrown deep in cookie parsing). Go could return 400 with a cleaner error — that's an `artifact`, not a behavior.

### Header forwarding skip-list

- **Observed behavior:** `Accept-Encoding` and `Host` are stripped before forwarding (`ProxyRequestHandler.java:82-84`). `X-Forwarded-*` headers are conditionally added based on `forwardedHeadersEnabled`. The forwarded HTTP header `Via: <protocol> TrinoGateway` is always added (`...:326`).
- **Source of behavior:** `protocol-required` (hop-by-hop headers should not be forwarded) + `ops-affordance` (`X-Forwarded-*` for downstream visibility).
- **Go obligation:** `replicate-intent`. The skip-list should be expanded to the full RFC 7230 hop-by-hop set in Go (Java has a `TODO` on this at `ProxyRequestHandler.java:339`) — `Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailers`, `Transfer-Encoding`, `Upgrade`. This is one place where the Go rewrite should *improve*, not strictly replicate.
- **Notes:** the `Via` header format is `<protocol> TrinoGateway`. Differential tests should pin this exactly.

### Random ports for test isolation

- **Observed behavior:** `routerPort = 20000 + (int)(Math.random()*1000)` per test class.
- **Source of behavior:** `jvm-artifact`. JUnit + Maven Surefire forking model encourages random port pick-and-pray.
- **Go obligation:** `drop`. Go tests should bind `:0` and read back the assigned port. This eliminates a flake source.

## Implications for Go Rewrite

The Go test pyramid should look like this. Percentages are rough investment, not test counts.

- **Unit (15-20%)** — fast, no I/O, table-driven. Covers: per-`RoutingGroupSelector` strategy, header skip-list, cookie sign/verify round-trips, query-id JSON extraction, MVEL-equivalent rule evaluation (whatever Go rule engine the architect picks), config validation. These will run in milliseconds and are the only category where Go's pyramid is "normal".
- **Integration in-process (35-40%)** — boot the Go gateway in-process against `httptest.Server` mock backends and a `testcontainers-go` PostgreSQL. Covers: full proxy lifecycle, routing decision + dispatch, query-id binding, cookie attachment, header forwarding end-to-end, error mapping. This is the bulk of the suite and the analogue of the existing Java `MockWebServer`-based tests. Should be runnable on a developer laptop in under a minute.
- **Integration with real Trino (20-25%)** — `testcontainers-go` TrinoContainer for cases where mock-Trino is not faithful enough: `nextUri` chain, DELETE cancellation, actual JSON response shape, error response round-trip, large result sets, X-Trino-Session round-trip. Slower (each container boot is ~10s), so this tier should be gated behind a `-tags integration` build tag.
- **Differential (10-15%)** — *this is new for the Go rewrite, no Java analog.* Boot the Java gateway and the Go gateway side-by-side against the same Trino, fire identical requests, diff the responses (body, status, headers, cookies) with `go-cmp` and a normalizer for non-deterministic fields (timestamps, ports). This is the only way to catch wire-protocol drift that unit tests will miss. Must run in CI, even if slow. See `[[test-infrastructure-needs.qa-tech-lead.md]]` for the harness design.
- **Concurrency / load (5-10%)** — *also new, no Java analog.* Use `go test -race` for all integration tests by default, plus a separate small load suite (k6 or vegeta) that exercises the proxy under sustained concurrent requests, validates graceful shutdown drains in-flight requests, and detects goroutine leaks via `goleak`. Required for proxy-core sign-off, not for every component.
- **End-to-end / acceptance (~5%)** — a small handful of tests that drive `trino-cli` (or the Go `trino-go-client`) end-to-end through the Go gateway against real Trino. The "if this works, the gateway works" smoke suite. Manual gate before release.

Per-component test-level recommendations:

| Component | Unit | In-proc int. | TrinoContainer int. | Differential | Concurrency | E2E |
|---|---|---|---|---|---|---|
| Proxy core (HTTP forwarding) | thin | heavy | heavy | **required** | **required** | smoke |
| Routing engine | **heavy** | medium | thin | medium | n/a | n/a |
| Query-id binding / persistence | thin | **heavy** | medium | medium | medium | n/a |
| Backend registry + health checks | medium | medium | thin | n/a | medium | n/a |
| REST mgmt API | medium | **heavy** | n/a | low | n/a | n/a |
| Config loading | **heavy** | thin | n/a | n/a | n/a | n/a |
| Auth/OIDC | medium | **heavy** | medium | **required** | n/a | smoke |

## Test Strategy Hooks

- **Test level:** this *is* the test-level strategy document; the table above is the answer.
- **Fixtures required:** see `[[test-infrastructure-needs.qa-tech-lead.md]]` for the concrete inventory. Headline items: hand-rolled Go statement-protocol fake, `testcontainers-go` Trino + PostgreSQL, a differential harness wrapping both gateways, port allocator, `goleak` integration.
- **Observable signals:** for the proxy specifically — HTTP status, response body byte-for-byte, full header set (including `Via`, `Set-Cookie`, hop-by-hop strips), DB rows in `query_history`, in-memory query-id→backend binding state, `goleak` goroutine count delta, race-detector output.
- **Non-determinism risks:** port allocation (mitigated by `:0`); container boot timing (mitigated by health-check polling); test ordering between in-memory routing state and DB-backed routing state (mitigated by per-test DB schema or per-test gateway instance); concurrent request ordering in load tests (mitigated by asserting on aggregates, not individual responses).

## Open Questions

- `@architect` — do we commit to the buffered-then-tee approach for POST `/v1/statement` responses, or do we want the Go rewrite to stream non-statement responses and only buffer statement ones? The choice changes which tests are mandatory.
- `@architect` — is migration overlap (Java and Go gateways running side-by-side, e.g. blue/green) in scope? If yes, signed-cookie format must be wire-compatible and the differential harness becomes a hard gate. If no, signed-cookie is just `replicate-intent`.
- `@trino-expert` — is the `Via: <protocol> TrinoGateway` header value contractual for any downstream consumer (logging, audit, client-side dedup), or is it informational? If contractual, it must be byte-exact in the differential tests.
- `@trino-expert` — for the `nextUri` rewrite (gateway must rewrite backend `nextUri` to point at itself), is there any case where Trino sends back an absolute URL the gateway *should* leave untouched (e.g. info pages, redirects)? Need this to scope differential tests.
- `@java-qa` — for Task #28's gap register, can you confirm there are no existing tests for `responseSize` cap behavior? I want to make sure I'm not duplicating a test that already exists when I add it to the Go must-have list.

## Cross-references

- `[[test-infrastructure-needs.qa-tech-lead.md]]` (sibling, will live under `studies/both/`) — the concrete tooling and harness design that this strategy requires.
- `[[component-signoff-rubric.qa-tech-lead.md]]` (sibling, under `studies/both/`) — how the pyramid translates into per-component sign-off bars.
- `[[test-infrastructure.java-qa.md]]` (Java QA, Task #23) — the full Java-side test infra inventory that this study summarizes the relevant parts of.
- `[[proxy-request-lifecycle.java-qa.md]]` (Java QA, Task #24) — the lifecycle-seams catalog that informs what's testable at each step.
- `[[routing-engine.java-qa.md]]` (Java QA, Task #25) — the routing-strategy oracle.
- `[[statement-protocol-invariants.java-qa.md]]` (Java QA, Task #27, will live under `studies/both/`) — the wire-protocol invariants that the differential tests will pin.
- `[[test-gaps-and-risks.java-qa.md]]` (Java QA, Task #28) — the risk register for behaviors that have no Java test today.
