---
title: trino-gateway test infrastructure inventory (Java) and Go-side equivalents
author: go-qa
role: Go QA
component: trino-gateway
topics: [test-infra, cross-cutting]
date: 2026-05-24
status: approved
risk: medium
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/proxy-request-lifecycle.go-qa.md
  - trino-gateway/routing-engine.go-qa.md
  - trino-gateway/test-gaps-and-risks.go-qa.md
  - trino-gateway/test-pyramid-strategy.qa-tech-lead.md
  - both/test-infrastructure-needs.qa-tech-lead.md
---

# trino-gateway test infrastructure inventory (Java) and Go-side equivalents

## Summary

The Java repo ships 57 JUnit 5 test classes split between (a) JVM-only unit tests using Mockito and a hand-rolled `QueryRequestMock`, and (b) integration tests that spin up real Trino and real Postgres/MySQL/Oracle via Testcontainers, with `MockWebServer` standing in for custom backends. Every category maps cleanly onto a Go equivalent (stdlib `testing` + `httptest.Server` + `testcontainers-go`), but the JVM-specific gateway bootstrap (`HaGatewayLauncher.main(args)` + Guice + Airlift) has no direct Go analog — Go tests will need to compose the proxy via constructor injection of small interfaces instead.

## Key Findings

- Test framework: JUnit 5 (`org.junit.jupiter.api.*`) with `@TestInstance(PER_CLASS)` and `@ParameterizedTest`/`@MethodSource` for data-driven cases. Assertions are AssertJ (`org.assertj.core.api.Assertions.assertThat`). Mocking is Mockito (`mock(...)`, `when(...).thenReturn(...)`).
  - Cite: `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/router/TestRoutingGroupSelector.java:25-31`, `TestProxyRequestHandler.java:27-49`.
  - Go equivalent: stdlib `testing` + table-driven tests (`if got != want { t.Errorf(...) }`) + `gomock` or hand-rolled mocks per interface size. `testify` deliberately dropped per qa-tech-lead — stdlib idioms are cleaner for a project this size and avoid the assert-vs-require teaching cost. Reach for `testify/suite` only if we end up needing JUnit-style lifecycle hooks; default to `TestMain` + `t.Cleanup` first.

- Mock HTTP backend: `okhttp3.mockwebserver.MockWebServer` is the standard fake-Trino stand-in. It supports `setDispatcher(...)` with path/method dispatch and `RecordedRequest` for assertions on what the gateway forwarded.
  - Cite: `TestProxyRequestHandler.java:54-98` (dispatcher with health-check, custom-PUT, HEAD); `TestGatewayHaMultipleBackend.java:79,99-119` (multi-path dispatcher with OAuth2 + custom paths).
  - Observable signals from MockWebServer that tests assert on: response status, response body bytes, response headers (e.g. `Content-Type: application/json;charset=utf-8` at `TestProxyRequestHandler.java:145`), and post-hoc inspection of recorded requests (path, method, headers, body).
  - Go equivalent: `net/http/httptest.Server` plus a request-capture middleware. There's no first-class `RecordedRequest` — we'll wrap a handler to push captured `*http.Request` clones onto a channel or slice protected by a mutex. Library option: `github.com/h2non/gock` or `github.com/jarcoal/httpmock` if a more declarative DSL is wanted, but stdlib `httptest` is sufficient and idiomatic.

- In-process fake HTTP client: For tests that exercise a `ClusterStatsMonitor` against a synthetic upstream, the Java tests use Airlift's `TestingHttpClient` with a lambda producing `TestingResponse.mockResponse(status, mediaType, body)`. This avoids opening a socket.
  - Cite: `TestClusterStatsMonitor.java:99-131`.
  - Go equivalent: pass a `*http.Client` whose `Transport` is a `http.RoundTripper` implemented as a func. Trivial to write; no dependency needed.

- Real-Trino integration via Testcontainers: `org.testcontainers.trino.TrinoContainer("trinodb/trino")` is used in multi-backend gateway tests and in `TestClusterStatsMonitor`. Image is `trinodb/trino:476` (pinned) in cluster-stats tests; unpinned `trinodb/trino` (latest) in `TestGatewayHaMultipleBackend`.
  - Cite: `TestGatewayHaMultipleBackend.java:87-93`; `TestClusterStatsMonitor.java:50-55`.
  - Observable signals tests rely on: cluster `/v1/info` returns `{"starting": false}` once warmed up, `/v1/jmx/...` JMX endpoint is reachable when RMI-enabled config is mounted, metrics endpoint returns Prometheus-style text. The wait for "healthy" is a polling loop with up to 10×1s sleeps (`HaGatewayTestUtils.verifyTrinoStatus`:181-193).
  - Go equivalent: `github.com/testcontainers/testcontainers-go` plus a thin wrapper. Pin the image. Replace the sleep loop with a `wait.ForHTTP("/v1/info")` strategy (testcontainers-go supports this natively) — better than a hand-rolled 10×1s loop.
  - Image pinning is mandatory; using `trinodb/trino` (unpinned) is a determinism risk we should NOT replicate.

- Database integration via Testcontainers: `PostgreSQLContainer`, `MySQLContainer`, `OracleContainer` (`gvenzl/oracle-free:23.9-slim`). Schema is loaded from `gateway-ha/src/main/resources/gateway-ha-persistence-*.sql` migration files via JDBI.
  - Cite: `HaGatewayTestUtils.java:73-81,168-170`; `TestDatabaseMigrationsPostgreSql.java`, `TestDatabaseMigrationsMySql.java`, `TestDatabaseMigrationsOracle.java`.
  - Go equivalent: testcontainers-go has first-class modules for each. Schema bootstrap can be done with `migrate` (`github.com/golang-migrate/migrate/v4`) or by executing the same SQL files. We should NOT carry over JDBI-specific assumptions (it's a Java JDBC convenience layer with no Go analog).

- Configuration fixtures: YAML templates in `src/test/resources/test-config-*.yml` with `${ENV:VAR}` interpolation done by `ConfigurationUtils.replaceEnvironmentVariables`. Tests generate a per-run temp file (`File.createTempFile`) and pass its path as the gateway's first CLI arg.
  - Cite: `HaGatewayTestUtils.java:83-112`; sample template `src/test/resources/test-config-template.yml` (random per-run ports for `APPLICATION_CONNECTOR_PORT`/`ADMIN_CONNECTOR_PORT`).
  - Go equivalent: `t.TempDir()` + `os.WriteFile` + the same env-style placeholder substitution. Use `t.Cleanup` for teardown. Or: pass config as a `*Config` struct directly to the Go gateway constructor in unit tests and reserve YAML templates for integration tests that exercise the loader path.

- Random per-run port allocation: tests use `int port = 20000 + (int)(Math.random()*1000)` (e.g. `TestProxyRequestHandler.java:58-59`, `TestGatewayHaMultipleBackend.java:76-77`).
  - This is a flake risk — collisions are possible, and ports leak across rapid test reruns. We should NOT replicate. Go offers `net.Listen("tcp", ":0")` + read back `Addr().(*net.TCPAddr).Port`, which is collision-free and what `httptest.NewServer` already does internally.

- Routing-rule fixtures: YAML rule files in `src/test/resources/rules/` (`routing_rules_atomic.yml`, `routing_rules_priorities.yml`, `routing_rules_if_statements.yml`, `routing_rules_state.yml`, `routing_rules_trino_query_properties.yml`, `routing_rules_concurrent.yml`, `routing_rules_update.yml`). Loaded by `RoutingGroupSelector.byRoutingRulesEngine(rulesConfigPath, ...)`.
  - Cite: `TestRoutingGroupSelector.java:72-80`.
  - These fixtures bake in MVEL syntax (`condition: "request.getHeader(...) == 'airflow'"`, `actions: "result.put(...)"`). They are NOT directly portable to Go — MVEL is a JVM expression language with no Go interpreter. See `test-gaps-and-risks.go-qa.md` for the implication.

- Test bootstrap: Integration tests start the full gateway by calling `HaGatewayLauncher.main(new String[]{ configFile })`, which initializes Guice modules, the Jetty HTTP server, the routing manager, the DB connection pool, etc. There is no equivalent `main(args)` pattern in Go test code; we'll wire components via constructor injection in tests and only call `cmd/trino-goway` end-to-end for true e2e tests.
  - Cite: `TestProxyRequestHandler.java:102-105`.

- Hand-rolled servlet/JAX-RS request mock: `io.trino.gateway.ha.util.QueryRequestMock` builds a Mockito-mocked `HttpServletRequest` with method, URI, headers, body input stream, and (optionally) a parsed `TrinoQueryProperties` attribute via real `QueryMetadataParser.filter(...)`. Used pervasively by unit tests of routing-rule selection and query parsing.
  - Cite: `QueryRequestMock.java:53-171`; usage in `TestRoutingGroupSelector.java:109-115`.
  - Go equivalent: `httptest.NewRequest(method, target, body)` returns a fully-formed `*http.Request` with no mocking needed at all. This is a significant ergonomics win for Go tests — what takes ~120 lines of mock setup in Java becomes a one-liner.

- Observable signal categories the existing Java tests assert on (these are exactly what the Go suite must replicate):
  - HTTP response status (`200`, `404`, `502`) — `TestProxyRequestHandler.java:127,132,144,150`.
  - HTTP response header value (`Content-Type` exact match) — `TestProxyRequestHandler.java:145`.
  - HTTP response body byte equality — `TestProxyRequestHandler.java:127`.
  - Routing-decision return value (`routingGroup` string equals or is null) — `TestRoutingGroupSelector.java:92,99,114`.
  - Domain object field equality on parser output (`QueryDetail.getUser()`, `getSource()`, `getQueryText()`, `getBackendUrl()`) — `TestProxyRequestHandler.java:183-186`.
  - `Optional<String>` extraction (`extractQueryIdIfPresent` `hasValue` / `isEmpty`) — `TestQueryIdCachingProxyHandler.java:39-71`.
  - Cluster-state enum (`TrinoStatus.HEALTHY` / `UNHEALTHY`) — `TestClusterStatsMonitor.java:93,144,177,185`.
  - Backend-side observation: recorded request path/method/headers/body via `MockWebServer.takeRequest()` (used elsewhere in `TestGatewayHaMultipleBackend`).
  - Negative signals: assertion that an `Optional` is empty when parse target is invalid — `TestQueryIdCachingProxyHandler.java:66-71`.
  - No log-line assertions found in the Java suite. **This is a gap** — log substring assertions appear nowhere, so we have no oracle for "the gateway logs the routing decision in format X." See `test-gaps-and-risks.go-qa.md`.
  - No metric-name assertions found in the Java suite. Likewise a gap.

- Race / concurrency tooling in the Java suite: none observed. The Java tests rely on the JVM memory model and Mockito's thread-safety; there is no equivalent of `go test -race`. **This means the Java suite cannot tell us whether the gateway has data races** — we discover those only in Go. We should commit to `-race` in CI from day one and `goleak` in every package that spawns goroutines.

- Specific Java test artifacts we will NOT replicate in Go:
  - `Math.random()` ports (use `:0`).
  - Mockito-based servlet mocking (use `httptest.NewRequest`).
  - `Thread.sleep(2 * refreshPeriod.toMillis())` for file-watch wait (`TestRoutingGroupSelector.java:384`) — flaky-by-construction; use a `fsnotify` event channel or `Eventually` polling with a real deadline.
  - JDBI persistence wiring (use `database/sql` + `pgx` or `sqlx`).

## Behavior vs. Implementation Artifact

### Mock HTTP backend selection (`MockWebServer` → `httptest.Server`)
- **Observed behavior:** Java tests bring up an in-process HTTP server, stub responses by path/method, and let tests inspect what the gateway forwarded.
- **Source of behavior:** `defensive-historical` — Java has no stdlib HTTP test server; OkHttp's `MockWebServer` is the de facto standard.
- **Rationale:** Avoid socket flakiness, run tests in-process.
- **Go obligation:** `replicate-intent` (not the library — use stdlib `httptest.Server`).
- **Notes:** Go's `httptest.Server` is stdlib and has identical semantics. The `RecordedRequest` pattern needs a thin custom wrapper.

### Random port selection (`Math.random()`)
- **Observed behavior:** Tests pick a TCP port from a 1000-wide window and hope for no collision.
- **Source of behavior:** `jvm-artifact` — work-around for Jetty needing a port at config-time before binding.
- **Rationale:** Java's Jetty initialization wants a port up front; reading back an OS-assigned port is awkward in that stack.
- **Go obligation:** `drop`. Use `httptest.NewServer` (auto-assigned port) or `net.Listen("tcp", ":0")`.
- **Notes:** This is a determinism bug in the Java suite; do not carry it over.

### `Thread.sleep`-based synchronization in file-watch tests
- **Observed behavior:** `Thread.sleep(2 * refreshPeriod.toMillis())` to wait for rule-file change to be picked up (`TestRoutingGroupSelector.java:384`).
- **Source of behavior:** `defensive-historical` — no first-class hook to "wait until rule reload completed."
- **Rationale:** Java tests have no easy way to wait on an internal cache invalidation.
- **Go obligation:** `replicate-intent` only — must NOT use `time.Sleep`. Use either an `fsnotify` event channel exposed for tests, a `chan struct{}` reload-completed signal, or an `Eventually(fn, timeout)` helper with a strict deadline.
- **Notes:** This is the canonical Go-flake pattern. PRs that use `time.Sleep` for sync should not be approved — QA Tech Lead holds sign-off authority on this gate.

### `TrinoContainer("trinodb/trino")` (unpinned latest)
- **Observed behavior:** `TestGatewayHaMultipleBackend` uses unpinned `trinodb/trino:latest`.
- **Source of behavior:** `defensive-historical` — convenience.
- **Rationale:** None defensible.
- **Go obligation:** `drop`. Always pin Trino image versions in Go integration tests.
- **Notes:** Cluster-stats tests already pin to `:476` — follow that pattern everywhere.

## Implications for Go Rewrite

- **Toolchain decision (confirmed by qa-tech-lead, see `studies/both/test-infrastructure-needs.qa-tech-lead.md`):**
  - stdlib `testing` + table-driven assertions (no `testify` — stdlib idioms are cleaner; reach for `testify/suite` only if JUnit-style lifecycle hooks become painful, default to `TestMain` + `t.Cleanup`)
  - `gomock` or hand-rolled mocks per interface size — pick whichever is cheaper per case, do not blanket-mandate
  - `testcontainers-go` for the integration tier
  - `go.uber.org/goleak` mandatory in every package that spawns goroutines
  - `github.com/golang-migrate/migrate/v4` for DB schema bootstrap
  - `github.com/tsenart/vegeta` for soak / throughput baseline
  - `golangci-lint` as part of the sign-off CI bundle (per the rubric)
  - No need to mirror Airlift's `TestingHttpClient` — wrapping `*http.Client.Transport` is trivial. WireMock-style record/replay is not needed; the Java suite has none either.
- **Test layering:** Unit tests for routing-rule evaluation, query-ID extraction, header forwarding rules, and request parsing should be table-driven and not touch the network. Integration tests for full proxy behavior should use `httptest.Server` for fake backends and `testcontainers-go` Trino only where real Trino behavior matters (cluster-stats monitors, OAuth handoff, end-to-end query lifecycle).
- **Integration-test build tag:** Use `//go:build integration` for testcontainers-based tests so `go test ./...` stays fast for developers without Docker running.
- **CI must run `-race` and `goleak` from day one** — the Java suite has no equivalent guard, so any concurrency bug in the Go rewrite will only be caught by Go's tooling.
- **Rule-file fixtures cannot be reused verbatim.** The four rule YAML files we want to reuse for differential testing (`routing_rules_atomic.yml`, `routing_rules_priorities.yml`, `routing_rules_if_statements.yml`, `routing_rules_state.yml`) all embed MVEL syntax. Either (a) we choose a Go expression language and rewrite the fixtures, or (b) we declare a config-format break and translate to a Go-friendly DSL. See `test-gaps-and-risks.go-qa.md` for the cost.
- **Differential testing is the most reliable parity oracle.** Confirmed in scope by qa-tech-lead: live Java gateway in CI gated to nightly; record/replay smoke per PR. Full design in `studies/both/test-infrastructure-needs.qa-tech-lead.md` item 4 — architect sign-off required before the harness is built.

## Test Strategy Hooks

- **Test level:** cross-cutting; informs all levels.
- **Fixtures required:** Trino image pin (`trinodb/trino:476` to match Java tests); Postgres/MySQL/Oracle test images; copies of the Java `src/test/resources/rules/*.yml` (translated where MVEL is present); reuse of `trino-config.properties`, `trino-config-with-rmi.properties`, `jvm-with-rmi.config` for testcontainers-driven Trino instances.
- **Observable signals:** see "Observable signal categories" above — all 9 categories are present in the Java suite and must be present in the Go suite.
- **Non-determinism risks:**
  - Random ports — solved by `:0`.
  - Cluster warm-up — solved by `wait.ForHTTP` strategy.
  - File-watch reload latency — needs an exposed completion signal.
  - Unpinned container images — pin everything.
  - Sleep-based assertions — banned; use `Eventually` with deadline.

## Open Questions

- ~~@qa-tech-lead: confirm tooling baseline.~~ **RESOLVED:** stdlib `testing` + `gomock` + `testcontainers-go` + `goleak`. `testify` dropped. See `studies/both/test-infrastructure-needs.qa-tech-lead.md`.
- ~~@qa-tech-lead: differential-test harness in scope?~~ **RESOLVED:** yes, nightly-gated, with record/replay smoke per PR. Architect sign-off required before build.
- @architect: is Docker-in-CI available? Required for all testcontainers-based integration tests. (qa-tech-lead flagging in consolidated note.)
- @java-qa: please confirm the rules-file fixtures and `TestRoutingGroupSelector` data-table queries (the `provideTableExtractionQueries` MethodSource) are the canonical routing-decision oracle. I'd like to lift those directly into Go table tests once MVEL is resolved.
- @architect: how do we want to handle log-line and metric-name oracles? The Java suite has no assertions on either. qa-tech-lead's standing recommendation: `log/slog` (stdlib) for logger, `prometheus/client_golang` for metrics — needs architect ratification.

## Cross-references

- [[proxy-request-lifecycle.go-qa.md]] — companion study of the proxy lifecycle from a seam-identification angle.
- [[routing-engine.go-qa.md]] — what the existing routing tests cover and what we lift directly.
- [[test-gaps-and-risks.go-qa.md]] — untested behaviors and MVEL parity risk.
- [[../trino-gateway/test-pyramid-strategy.qa-tech-lead.md]] — the proposed test-pyramid shape for the rewrite (qa-tech-lead).
- [[../both/test-infrastructure-needs.qa-tech-lead.md]] — consolidated test-infra stack and differential-harness design.
