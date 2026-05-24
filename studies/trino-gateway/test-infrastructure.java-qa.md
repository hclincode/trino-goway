---
title: trino-gateway test infrastructure inventory
author: java-qa
role: Java QA
component: trino-gateway
topics: [test-infra]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to: []
---

# trino-gateway test infrastructure inventory

## Summary

A map of how trino-gateway is tested today, what oracles already exist, and which test patterns the Go rewrite can port versus must reinvent. The single most important takeaway: the existing tests are predominantly **HTTP-boundary integration tests** (MockWebServer / TrinoContainer / Testcontainers) that translate cleanly into Go without requiring JVM internals — but a handful of unit tests reach into Java types (`HttpServletRequest`, `TrinoQueryProperties` with its `io.trino.sql` AST) and those oracles must be reconstructed as wire-level fixtures before they can be ported.

## Key Findings

### Test inventory shape

- 57 test source files vs. 146 main source files in `trino-gateway/gateway-ha/src/{test,main}/java`. Coverage is meaningful but not exhaustive; gaps are catalogued in the sibling `test-gaps-and-risks.java-qa.md` study.
- Test classes follow a `Test<Subject>` naming convention and use JUnit 5 (`@Test`, `@TestInstance(Lifecycle.PER_CLASS)`, `@BeforeAll`/`@AfterAll`) with AssertJ for fluent assertions (`org.assertj.core.api.Assertions.assertThat`). E.g. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:34-37`.

### Pattern 1 — HTTP-boundary integration tests with `okhttp3.mockwebserver`

This is the dominant pattern and the most directly portable to Go.

- A `MockWebServer` impersonates one or more Trino backends; the real gateway process is booted in-test against those mock backends via `HaGatewayLauncher.main(args)` with a generated config file. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:79-144`, `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/proxyserver/TestProxyRequestHandler.java:54-126`.
- Test clients are also `okhttp3.OkHttpClient` issuing `Request.Builder` calls to the gateway's router port (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:81, 146-168`).
- Mock backends use a `Dispatcher` (`okhttp3.mockwebserver.Dispatcher`) that pattern-matches on `RecordedRequest.getPath()` / `getMethod()` and returns canned `MockResponse` instances with status, headers, and body. Health checks are commonly stubbed at `/v1/info` returning `{"starting": false}`. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/proxyserver/TestProxyRequestHandler.java:74-97`, `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:99-119`.
- A reusable backend registration helper posts to the admin API to enroll backends and polls `/api/public/backends/{name}/state` until `TrinoStatus.HEALTHY` (10 attempts, 1s sleep). `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/HaGatewayTestUtils.java:135-194`.
- Test ports are randomised in `[20000, 32000)` per `HaGatewayTestUtils.buildGatewayConfig` (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/HaGatewayTestUtils.java:96-101`) and via inline `Math.random()` (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:76-77`). **Observable signal of flakiness risk** — this is a port-collision lottery, not a fixed contract. See the `Math.random()` port allocation block under Behavior vs. Implementation Artifact below for the recommended Go resolution.

### Pattern 2 — End-to-end against real Trino via Testcontainers

A small set of tests run an actual `trinodb/trino` container (`org.testcontainers.trino.TrinoContainer`) behind the gateway, proving end-to-end protocol behaviour against the genuine Trino implementation. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:67-92` (two `TrinoContainer` instances bound to routing groups `adhoc` and `scheduled`).

- This is the only path that exercises real `nextUri` rewriting and `QueryResults` JSON parsing against an authentic Trino response — see the `DELETE` of `queryResults.getNextUri()` at `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:234-256`.
- Configuration override is done by copying a classpath resource into the container: `withCopyFileToContainer(forClasspathResource("trino-config.properties"), "/etc/trino/config.properties")` at `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:88, 91`.
- A custom image substitutor exists (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/CustomTrinoImageNameSubstitutor.java`) configured via `testcontainers.properties` in the test classpath — likely for internal mirror support.

### Pattern 3 — Persistence parity via Testcontainers (`PostgreSQLContainer`, `OracleContainer`, MySQL)

- Postgres helper centralised at `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/util/TestcontainersUtils.java:27-37` with a dual wait strategy (port-listening AND log-message-matching "database system is ready to accept connections" twice). Two waits is intentional — Postgres restarts the listener after first init.
- Oracle helper: `gvenzl/oracle-free:23.9-slim` at `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/HaGatewayTestUtils.java:168-171`.
- Per-DB test subclasses (`TestDatabaseMigrationsMySql/Postgres/Oracle`, `TestQueryHistoryManagerMySql/Postgres/Oracle`, `TestExternalUrlQueryHistoryMySql/Postgres/Oracle`) inherit from `BaseTestDatabaseMigrations`, `BaseTestQueryHistoryManager`, `BaseExternalUrlQueryHistoryTest` — confirming the gateway treats schema and query history as a portable parity surface across three RDBMSes.

### Pattern 4 — Unit tests with mocked `HttpServletRequest`

- Routing rule selectors are tested by `org.mockito.Mockito.mock(HttpServletRequest.class)` plus `when(...).thenReturn(...)` stubs. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/router/TestRoutingGroupSelector.java:51, 82-100`.
- A reusable `QueryRequestMock` builder constructs a fully-populated `HttpServletRequest` with body and headers for tests that exercise SQL-parsing-dependent code paths. Used by `TestRoutingGroupSelector`, `TestQueryIdCachingProxyHandler`, `TestTrinoQueryProperties`, `TestQueryMetadataParser`. Reference: `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/util/QueryRequestMock.java`.
- **This is the layer that will NOT port directly.** Go has no `HttpServletRequest`. Tests in this pattern need to be re-shaped around the Go gateway's request abstraction (e.g. `*http.Request` plus whatever struct holds the cached `TrinoQueryProperties` equivalent). The *intent* of each test — given a body containing query X, the extracted query id is Y — is portable; the Java fixture is not.

### Pattern 5 — Configuration substitution and fixture templates

- Gateway configs are stored as YAML templates with `${VAR}` placeholders (e.g. `${APPLICATION_CONNECTOR_PORT}`, `${POSTGRESQL_JDBC_URL}`, `${RESOURCES_DIR}`) and substituted at test setup via `ConfigurationUtils.replaceEnvironmentVariables`. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/HaGatewayTestUtils.java:93-112`.
- Eight YAML test config templates live under `trino-gateway/gateway-ha/src/test/resources/`: `test-config-template.yml`, `test-config-with-routing-template.yml`, `test-config-with-routing-rules-api.yml`, `test-config-with-query-history-disabled.yml`, `test-config-with-forwarded-headers-disabled-template.yml`, plus auth-specific configs under `auth/`.
- Seven routing-rules YAML fixtures live at `trino-gateway/gateway-ha/src/test/resources/rules/`: `routing_rules_atomic.yml`, `routing_rules_concurrent.yml`, `routing_rules_if_statements.yml`, `routing_rules_priorities.yml`, `routing_rules_state.yml`, `routing_rules_trino_query_properties.yml`, `routing_rules_update.yml`. The first four are driven through `TestRoutingGroupSelector` parameterized cases (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/router/TestRoutingGroupSelector.java:72-80`) — they are de facto behavioural specs of the MVEL rule semantics.

### Pattern 6 — Differential / API-shape oracles

- `TestRoutingAPI` and `TestRoutingRulesManager` exercise the management REST surface; they double as a contract for the JSON shape of the admin endpoints.
- `TestObjectSerializable` verifies JSON serialization of domain objects — useful as a sanity-check oracle when the Go side picks a JSON library.
- `TestClusterStatsJMX` (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:400-412`) scrapes the `/metrics` Prometheus-style endpoint and asserts on `trino1_TrinoStatusHealthy` / `trino2_TrinoStatusHealthy` — a **JVM-coupled observable** (JMX-derived names), but the *line shape* is portable.

### Observable signals the test suite asserts on

A non-exhaustive but representative list of what existing tests already pin as oracles. Each is a candidate Go test assertion.

- **HTTP status codes:** `200` (OK), `204` (DELETE accepted, asserted as `between(200, 204)` at `TestGatewayHaMultipleBackend.java:255`), `404` (unknown path), `500` (cookie signature tamper, `TestGatewayHaMultipleBackend.java:373`), `502` (`BAD_GATEWAY` on backend timeout or proxy failure, `ProxyRequestHandler.java:242-247, 261-267`).
- **Headers — request side (gateway must propagate or honour):** `X-Trino-User`, `X-Trino-Routing-Group`, `X-Trino-Source`, `X-Trino-Catalog`, `X-Trino-Schema`, `X-Trino-Client-Tags`, `Authorization`, `Cookie`. Skip-list applied by `ProxyRequestHandler.shouldForwardHeader`: `Accept-Encoding`, `Host` (`ProxyRequestHandler.java:82-84, 340-351`).
- **Headers — request side (gateway adds):** `Via: <protocol> TrinoGateway` (`ProxyRequestHandler.java:326`), and when `forwardedHeadersEnabled`: `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Port`, `X-Forwarded-Host` (`ProxyRequestHandler.java:328-330, 353-362`). The `TestForwardedHeadersDisabled` test pins the negative case.
- **Headers — response side (gateway sets cookies):** `Set-Cookie` for `trinoClusterHost` when `includeClusterInfoInResponse=true` (`ProxyRequestHandler.java:193-196`); `Set-Cookie` for OAuth2 routing cookie at OAUTH2 paths (`ProxyRequestHandler.java:204-224`). Tested via `okhttp3.Cookie.parseAll` in `TestGatewayHaMultipleBackend.testTrinoClusterHostCookie / testCookieBasedRouting / testCookieSigning`.
- **Body content invariants:** the response body for `POST /v1/statement` against the gateway contains the gateway's own `http://localhost:<routerPort>` (proving `nextUri` rewrite). Asserted as a substring at `TestGatewayHaMultipleBackend.java:183, 194, 212, 228`. Cookie tamper test asserts status `500` and that `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/GatewayCookie.java` signature validation kicks in.
- **Side-effect signals:** `queryHistoryManager.submitQueryDetail(queryDetail)` at `ProxyRequestHandler.java:299` — query history rows are an observable side effect after each successful POST to a statement path. Tests under `BaseTestQueryHistoryManager` pin the read side.
- **Log lines:** the proxy rewrite is logged as `"Rerouting [scheme://host:port/path?query]--> [target]"` (`RoutingTargetHandler.java:174-183`). The `info`-level log line shape is not currently asserted by any test, but is a candidate stable observable.
- **Health-check endpoints:** `GET /trino-gateway/livez` returns `200`; `GET /trino-gateway/readyz` returns `200` once ready (polled up to 10s). Pinned at `TestGatewayHaMultipleBackend.java:377-398`.
- **Metrics endpoint:** `GET /metrics` returns a Prometheus-style text body containing `<backend-name>_TrinoStatusHealthy` lines (`TestGatewayHaMultipleBackend.java:400-412`).

### Non-determinism risks visible in the suite

- **Port collisions** from `Math.random()`-based port allocation across tests (see Pattern 1 above). A Go rewrite should allocate from `:0` and read back the bound port.
- **`sleepUninterruptibly` retry loops** for readiness — `HaGatewayTestUtils.verifyTrinoStatus` polls up to 10 seconds; `TestGatewayHaMultipleBackend.testHealthCheckEndpoints` polls up to 10 seconds. Long timeouts mask slow tests on CI.
- **Real-Trino e2e tests** are sensitive to image pull times and Trino startup time (~10s+). The custom image substitutor implies these have been flaky on the upstream image at least once.
- **`PER_CLASS` test lifecycle** with shared `BeforeAll` setup couples tests within a class — one bad fixture takes down the whole class. The Postgres / TrinoContainer rebuild between classes is expensive; running the full test class set sequentially is mandatory.
- **Stochastic router test** (`TestStochasticRoutingManager`) uses `java.util.Random` with no seed (`StochasticRoutingManager.java:27`). The single existing `@Test` (`TestStochasticRoutingManager.java:49-78`) sidesteps the non-determinism by adding five backends to one group, marking four UNHEALTHY, and asserting the one remaining HEALTHY backend (`test_group0`) is the one selected — a degenerate single-choice case. The router's actual stochastic behaviour (distribution across multiple HEALTHY backends) is therefore untested today; this is a coverage gap, not a flake risk, and is captured in `[[test-gaps-and-risks.java-qa.md]]`.

### Test-only modules and helpers worth replicating in Go

- `HaGatewayTestUtils` — backend registration, config template substitution, mock-backend bootstrap, health polling. (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/HaGatewayTestUtils.java`)
- `TestcontainersUtils` — DB container factories. (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/util/TestcontainersUtils.java`)
- `QueryRequestMock` — request fixture builder. (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/util/QueryRequestMock.java`)
- `TestingJdbcConnectionManager` — in-memory persistence wiring for tests.
- `TrinoGatewayRunner` — convenience launcher for ad-hoc manual testing. (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/TrinoGatewayRunner.java`)
- `OpenTracingCollector` — collects spans for tracing tests. (`trino-gateway/gateway-ha/src/test/java/io/trino/gateway/OpenTracingCollector.java`)

## Behavior vs. Implementation Artifact

### Mocking the Trino backend over HTTP

- **Observed behavior:** the gateway has been written to be testable against any HTTP server that mimics a small subset of Trino endpoints (`/v1/statement`, `/v1/query/{id}`, `/v1/info`, optional custom paths). See `prepareMockBackend` at `HaGatewayTestUtils.java:60-71` and dispatcher use at `TestGatewayHaMultipleBackend.java:99-119`.
- **Source of behavior:** `gateway-design-intent`. The gateway treats Trino backends as opaque HTTP endpoints; that is the design.
- **Rationale:** the gateway is an L7 proxy. Mocking at the HTTP boundary mirrors how it is deployed in production.
- **Go obligation:** `replicate-intent`. The Go test harness should provide an equivalent mock-Trino helper (e.g. `httptest.Server` with a configurable dispatcher). The Java helpers' specific shape need not be preserved.
- **Notes:** the dispatcher's `404` default-case is informative — it asserts the gateway does not silently rewrite or drop unmatched paths.

### Per-DB test parity for persistence

- **Observed behavior:** schema migration, query history, and external-URL caching each have parallel test classes for MySQL, Postgres, Oracle. Base classes hold shared assertions; subclasses spin up the per-engine container. `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/persistence/BaseTestDatabaseMigrations.java`, `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/router/BaseTestQueryHistoryManager.java`, `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/router/BaseExternalUrlQueryHistoryTest.java`.
- **Source of behavior:** `gateway-design-intent`. The gateway officially supports three DBs as durable stores.
- **Rationale:** operator choice — the gateway is meant to slot into existing infra.
- **Go obligation:** `replicate-intent`. The Go rewrite must continue to support the same three engines (or document any drop) and should preserve the base/subclass parity pattern (Go: shared test functions invoked from per-driver `_test.go` files, or table-driven).
- **Notes:** if the Go rewrite supports a superset (e.g. SQLite for dev), that's additive — but the three production DBs are oracle behavior.

### Mocked `HttpServletRequest` unit tests

- **Observed behavior:** routing-selector and request-classification tests stub `HttpServletRequest` directly (`TestRoutingGroupSelector.java:51, 82-100`; `TestQueryIdCachingProxyHandler.java` via `QueryRequestMock`).
- **Source of behavior:** `jvm-artifact`. The Servlet API type is a JVM contract; the tests use it because the code under test is written against it.
- **Rationale:** none beyond "this is the Java HTTP interface".
- **Go obligation:** `drop`. Reconstruct each scenario as: given an HTTP request with these headers/body, the selector returns this routing group. The fixtures port; the type does not.
- **Notes:** the table of `extractQueryIdIfPresent` URL fixtures at `TestQueryIdCachingProxyHandler.java:38-71` is one of the most portable, highest-value oracles in the suite — keep it character-for-character.

### `Math.random()` port allocation

- **Observed behavior:** tests pick ports in fixed ranges using `Math.random()`. `TestGatewayHaMultipleBackend.java:76-77`, `TestProxyRequestHandler.java:58-59`.
- **Source of behavior:** `defensive-historical`. Likely added when tests began running concurrently or in CI with other listeners.
- **Rationale:** none deeper than "two tests on the same machine shouldn't collide".
- **Go obligation:** `drop`. Bind to `:0` and read the port back from `net.Listener.Addr()`; deterministic and collision-free.

### `PER_CLASS` shared-fixture lifecycle

- **Observed behavior:** integration test classes use `@TestInstance(Lifecycle.PER_CLASS)` and share a gateway process across all `@Test` methods in the class.
- **Source of behavior:** `defensive-historical` / cost. Booting the gateway plus a Postgres container per method would be prohibitive.
- **Rationale:** cost amortisation.
- **Go obligation:** `replicate-intent`. Use Go's `TestMain` or per-package suite setup (e.g. `testing.M.Run`) to share gateway + containers across tests in the same package; do not boot per test.
- **Notes:** preserve test isolation by giving each test method a unique backend name / routing group, not a shared one — current Java tests sometimes share `trino1`/`trino2` across tests, which works because the tests are read-only on those.

## Implications for Go Rewrite

- The dominant test pattern (boundary-level HTTP tests with a mock Trino) is portable straight across to Go (`httptest.Server`, `net/http` client). The Go QA team can plan to mirror the Java integration suite structure 1:1 — same backend names, same routing groups, same observable assertions — without inheriting the Servlet API.
- The Java suite's serial-execution constraint (`PER_CLASS` shared fixtures + expensive container rebuilds) is a JVM-side cost concession, not a behavioural requirement. The Go rewrite can run tests within a package in parallel via `t.Parallel()` once each test owns its own gateway/backend ports (which the `:0` binding recommendation makes safe). Treat sequential execution as the floor, not the target.
- A Go equivalent of `HaGatewayTestUtils` should be the first piece of test infrastructure built; without it, every integration test becomes a wall of boilerplate. Suggested surface: `BootGateway(t, cfg)`, `RegisterBackend(t, gw, opts)`, `WaitHealthy(t, gw, backendName, timeout)`, `MockBackend(t, dispatcher)`.
- For the three persistence engines, plan to use Go Testcontainers (`github.com/testcontainers/testcontainers-go`); parity is observable behavior, not an artifact. Skipping a DB needs an explicit scope decision from the Architect, not silent omission.
- The TrinoContainer-driven end-to-end class is the only oracle that proves real-Trino protocol conformance. The Go rewrite needs at least one analogous test that boots a real `trinodb/trino` container behind the Go gateway and runs `SELECT 1` end-to-end. Without it, every other test is just asserting "Go gateway matches Go mock backend."
- Unit tests that stub `HttpServletRequest` (selectors, query-id extraction, `TrinoQueryProperties`) cannot be ported as code; they must be **re-derived from the test cases**. Treat the input fixtures (URLs, query bodies, header sets) as the authoritative oracle and rewrite the test scaffolding around Go-native request types.
- Address two known non-determinism patterns proactively: (a) allocate ports from `:0`; (b) avoid unseeded randomness in the stochastic router test — assert on the distribution or seed the RNG.
- The seven `routing_rules_*.yml` fixtures cannot be reused directly until the Architect picks the Go rule expression language. Either keep the YAML shape and replace MVEL semantics with a Go equivalent (and re-run the existing fixtures through the Go selector), or define new fixtures and use the Java ones only as semantic inspiration. Go QA cannot decide this.
- The metrics endpoint exposes JMX-derived names (`<backend>_TrinoStatusHealthy`); the Go rewrite will not have JMX. The naming convention is a wire-level observable that operators may dashboard against — flag this as a compatibility decision needed from the Architect, not a Go QA call.

## Test Strategy Hooks

- **Test level:** integration (HTTP-boundary against mock backends); e2e (against `trinodb/trino` container); unit (selector / query-id-extraction logic); differential (DB parity tests parameterised across MySQL/Postgres/Oracle).
- **Fixtures required:** mock Trino HTTP server with configurable dispatcher; real `trinodb/trino` container behind Go testcontainers; per-DB containers (Postgres 17, MySQL, Oracle Free 23.9-slim); YAML config templates with env-substitution support; routing-rules fixture set (TBD shape after Architect decision); table-driven URL/query-body corpora for selector tests.
- **Observable signals:** HTTP status codes (200/204/404/500/502); response body substring checks (`nextUri` rewrite); response headers (`Set-Cookie` shapes, `Via`, `X-Forwarded-*`); request headers received at mock backend (`Recorded­Request.getPath()` analogue in Go); side effects (rows in query history table; cluster-stats cache state); endpoints (`/trino-gateway/livez`, `/trino-gateway/readyz`, `/metrics`).
- **Non-determinism risks:** random port allocation (mitigate with `:0` binding); container startup latency (mitigate with explicit wait strategies); unseeded `Random` in stochastic router (seed it or assert distributions); `PER_CLASS`-style shared fixtures coupling tests (use per-test backend names to keep cases independent within a suite).

## Open Questions

- @architect: which Go rule expression language is being chosen for routing rules? This decides whether the seven `routing_rules_*.yml` fixtures can be reused directly or must be re-authored.
- @architect: should the Go rewrite preserve the JMX-derived `<backend>_TrinoStatusHealthy` metric names exposed on `/metrics`, or rename them? Operator dashboards may pin on these.
- @trino-expert: is the `MockWebServer`-based test pattern (no real Trino) considered authoritative for protocol-level claims by the trino-gateway team, or is the TrinoContainer e2e the only "real" oracle? This affects how heavily we lean on mocks in the Go suite.
- @qa-tech-lead: do you want me to produce a per-test-class portability assessment (port-as-is / re-author / drop) as a follow-up study, or keep that information embedded across the per-topic studies?

## Cross-references

- `[[proxy-request-lifecycle.java-qa.md]]` — testable seams in the proxy request path; this study describes the *plumbing* that exercises those seams.
- `[[routing-engine.java-qa.md]]` — concrete test oracles for routing semantics; uses the patterns documented here.
- `[[test-gaps-and-risks.java-qa.md]]` — behaviours with thin or absent Java tests; companion to this inventory.
- `[[../both/statement-protocol-invariants.java-qa.md]]` — wire-level Trino protocol contracts the gateway must preserve.
