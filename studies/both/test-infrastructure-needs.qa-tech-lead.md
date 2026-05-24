---
title: Test infrastructure Go QA will need for the trino-gateway rewrite
author: qa-tech-lead
role: QA Tech Lead
component: both
topics: [test-infra, proxy-core, statement-protocol, cross-cutting]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/test-pyramid-strategy.qa-tech-lead.md
  - both/component-signoff-rubric.qa-tech-lead.md
---

# Test infrastructure Go QA will need for the trino-gateway rewrite

## Summary

The Go rewrite needs five pieces of test infrastructure before component implementation begins, not after: a port allocator, a hand-rolled Go mock Trino server for fast tests, a `testcontainers-go` harness for real-Trino/real-DB integration, a differential-testing rig that fronts both gateways with the same workload and diffs responses, and a goroutine-leak + race-detector + soak rig for concurrency. The bill of materials is small (about a dozen Go libraries, no exotic dependencies); the *design* of the mock and differential rigs is the real work. Without these in place at Phase-2-gate, the rewrite will accumulate untested code faster than tests can be written, and Go QA will be reactively backfilling.

## Key Findings

### What the Java suite uses (relevant subset)

- **Testcontainers** for Trino (`testcontainers-trino`), PostgreSQL, MySQL, Oracle — see `gateway-ha/pom.xml` test-scoped deps and `gateway-ha/src/test/java/io/trino/gateway/ha/util/TestcontainersUtils.java:23-38` for the Postgres helper with multi-strategy wait (`waitingFor(Wait.forListeningPort() + LogMessageWaitStrategy ".*ready to accept connections.*" x2`). The double-log-match wait is non-obvious and exists because Postgres logs "ready" once during init and again when restarting. The Go equivalent will need the same.
- **okhttp `MockWebServer`** as the hand-rolled HTTP fake — see `TestProxyRequestHandler.java:74-98` for a typical `Dispatcher` pattern that switches on `path` + `method`. This is the workhorse for non-statement-protocol tests; it's deliberately dumb.
- **A long-running dev harness**, `TrinoGatewayRunner` (`gateway-ha/src/test/java/io/trino/gateway/TrinoGatewayRunner.java:32-72`), which is *not* a test class — it's a `main` that boots two Trino containers + PostgreSQL + MySQL + a Jaeger tracing collector + the gateway. Useful for manual exploratory testing. The Go rewrite should have an equivalent under `cmd/devrunner/` or similar.
- **Config-as-template-with-env-var-substitution**: `HaGatewayTestUtils.buildGatewayConfig` (`gateway-ha/src/test/java/io/trino/gateway/ha/HaGatewayTestUtils.java:93-112`) reads a YAML template and substitutes `${VAR}` placeholders. Test fixtures live in `src/test/resources/test-config-*.yml`. The Go equivalent could use Go's `text/template` or just env-var substitution; either way the pattern (one fixture per scenario, substitute ports + DB creds at boot) is sound and we should keep it.

### What is missing from the Java suite (gaps Go QA must fill)

- **No concurrency / race tests.** The Java suite uses JUnit's `@TestInstance(PER_CLASS)` lifecycle (`TestGatewayHaMultipleBackend.java:57`) and serial test execution. There is no equivalent of `go test -race` and no soak/load suite. Go must add `go test -race` to default CI and a separate `vegeta`/`k6` soak suite.
- **No body-cap / large-response tests.** The `ProxyResponseHandler.responseSize` cap (`gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:50`) is untested. Go must test both under-cap and over-cap behavior.
- **No backend-fail-mid-request tests.** All `MockWebServer` dispatchers return cleanly. Go's mock-Trino fake must support "close connection mid-response", "delay response by N", "return malformed JSON", "return non-UTF-8 bytes".
- **No goroutine-leak detection.** N/A in Java but mandatory in Go. Every integration test should be wrapped with `goleak.VerifyNone(t)` or run under `goleak.VerifyTestMain`.
- **Differential testing against a previous version is absent.** Java has no analog because there's no second implementation to diff against. Go is rewriting Java, so differential is the most cost-effective protocol-fidelity test we have.

### Port allocation is currently a flake source

- Java tests pick ports via `20000 + (int)(Math.random()*1000)` (e.g. `TestGatewayHaMultipleBackend.java:76`). This will collide under parallel test execution, and in CI it has likely flaked silently.
- Go should bind `:0` and read back the assigned port via `net.Listener.Addr().(*net.TCPAddr).Port`. Concrete helper: `func freePort(t *testing.T) int` that allocates, immediately closes, and returns the port number. This is the standard Go idiom and removes the flake category entirely.

## Behavior vs. Implementation Artifact

### Java tests boot the full DI graph

- **Observed behavior:** every gateway-boot test calls `HaGatewayLauncher.main(args)` with a YAML config (`TestGatewayHaMultipleBackend.java:125-126`, `TestProxyRequestHandler.java:104-105`). There are no "construct the proxy handler with stub dependencies" tests because Guice + airlift's `Bootstrap` make that prohibitively verbose.
- **Source of behavior:** `jvm-artifact`. The Guice + airlift bootstrap is the only practical way to wire the system in JUnit; manually constructing the dependency graph would be hundreds of lines per test.
- **Go obligation:** `drop`. The Go architect's package layout should expose constructors that take dependencies as plain arguments (no DI framework, or a very thin one), so unit tests can pass mocks without booting the world. This is one of the structural improvements the rewrite buys us — *if* the architecture preserves it.
- **Notes:** if the architect adopts `uber-go/fx` or `google/wire` for DI, we lose this property. I want explicit confirmation before that's done. Flagged as an open question.

### Random-port test isolation

- See findings above. **Go obligation:** `drop`. Use `:0` + `Addr()` readback.

### Per-test PostgreSQL container

- **Observed behavior:** each test class spins up its own `PostgreSQLContainer` (`TestcontainersUtils.java:27`).
- **Source of behavior:** `defensive-historical`. Container reuse is possible (Testcontainers supports `withReuse`) but per-test isolation eliminates cross-test pollution.
- **Go obligation:** `replicate-intent`. Use `testcontainers-go` with per-test (or per-package) container; per-test if it's fast enough, per-package if container boot dominates. Postgres boot is ~3-5s; per-package is the right default.
- **Notes:** alternative is `ory/dockertest`. testcontainers-go is more idiomatic and the Java pattern transfers cleanly.

## Implications for Go Rewrite

The infrastructure plan, in priority order.

### 1. Port allocator (1 day)

A tiny `testutil` package with `func FreePort(tb testing.TB) int` (uses `:0` + close + return). Mandatory; everything else assumes it.

### 2. Hand-rolled mock Trino server (3-5 days)

A configurable Go HTTP server that speaks the subset of the Trino statement protocol the gateway is sensitive to:

- POST `/v1/statement` → respond with realistic `QueryResults` JSON: `id` (configurable or auto-generated), `infoUri`, `nextUri`, `columns`, `data`, `stats`, `error`.
- Subsequent GET on a `nextUri` → walk through a scripted state machine (QUEUED → RUNNING → FINISHED) with configurable delay between states.
- DELETE on `nextUri` → return 204; record cancellation for assertion.
- Configurable failure injection: connection drop mid-response, slow body, malformed JSON, non-200 status, oversize body.
- Recording of all incoming requests (headers, body, path, method) for post-test assertions.

Library choices: `net/http` + `httptest.NewServer`. No mocking framework needed; this is plain Go. Optional: `github.com/h2non/gock` or `github.com/jarcoal/httpmock` for the recording side, but I lean against them — the test is the documentation, and a custom fake is more honest about what we actually need.

Owned by Go QA (Task to be created in Phase 2). This is the single largest piece of test infrastructure and the one most likely to set the test pace.

### 3. testcontainers-go harness (1-2 days)

- `gateway/internal/testutil/containers/postgres.go` — Postgres container with the same wait strategy as the Java helper (listening port + log message twice). Equivalent of `TestcontainersUtils.createPostgreSqlContainer()`.
- `gateway/internal/testutil/containers/trino.go` — TrinoContainer wrapper using `trinodb/trino` Docker image with `trino-config.properties` mounted.
- `gateway/internal/testutil/containers/mysql.go` — MySQL container (if MySQL persistence is in scope; defer until then).
- Gating: `//go:build integration` tag on tests that use containers; CI runs both `-short` (no containers) and full (`-tags integration`) in parallel matrix jobs.

Library: `github.com/testcontainers/testcontainers-go` + per-DB modules. Well-maintained, idiomatic Go port of the Java library.

### 4. Differential testing harness (5-7 days, **the most architecturally interesting piece**)

The goal: assert that for a defined set of request workloads, the Go gateway and the Java gateway produce equivalent responses against the same backend Trino. "Equivalent" means: identical status code, identical body modulo normalizers, identical header set modulo a configurable skip-list (timestamps, request IDs, port numbers in `Set-Cookie`).

Design sketch:

```
+--------+      +-------------+       +-------+
| driver | ---> | java-gw:NN  | --->  | trino |
|        | --\  +-------------+   \-> |       |
|        |   \                       +-------+
|        |    \  +-------------+    /
|        |     ->| go-gw:MM    | ---/
+--------+      +-------------+

diff(java-response, go-response) → reported as test result
```

- **Driver**: Go program that reads request scenarios from a YAML/JSON file (a request template + a list of variable substitutions), fires each at both gateways concurrently, captures responses, normalizes (timestamps, ports, query IDs), diffs with `github.com/google/go-cmp/cmp`.
- **Backend**: single TrinoContainer shared between both gateways. Both gateways are registered as separate backends in each others' configs, or they share a backend config — to be designed.
- **CI gating**: differential suite runs on every PR that touches a `protocol-fidelity` package (proxy-core, statement-protocol). Failure blocks merge.
- **Test scenarios**: minimum set on day one — simple SELECT 1, multi-page result set, OAuth2 handshake, query cancellation, error response, oversize result. Each scenario lives as one YAML file; adding scenarios is appending files, not writing code.

The reason this is the most interesting piece: the *normalizer* is where protocol-fidelity bugs hide. If the normalizer strips a header that's actually load-bearing, we'll pass differential and break clients. The normalizer set must itself be reviewed by the QA Tech Lead and the Trino Expert before the harness ships. Iterative.

### 5. Concurrency / leak / soak rig (2-3 days)

- `go.uber.org/goleak` in every integration test (`defer goleak.VerifyNone(t)`).
- `go test -race` enabled in CI by default; not a separate job.
- `vegeta` for the soak suite (`github.com/tsenart/vegeta`). One target: a fixed RPS load against the proxy for N minutes with assertion on p99 latency and zero 5xx. Owned by Go QA, run nightly.
- Graceful-shutdown test: drive load, send SIGTERM, assert all in-flight requests complete cleanly and no new requests are accepted. Use `signal.NotifyContext` + custom test driver.

### 6. Config fixture pattern (0.5 day)

Port the Java pattern: a `studies/trino-gateway`-style `test-config-*.yml` directory under Go's `internal/testutil/fixtures/`, with `${VAR}` substitution via `os.ExpandEnv` or `text/template`. One fixture per major scenario class.

### Total commitment

About 12-18 person-days for all six pieces. Test infrastructure should be the first work after Phase-2 gate; it cannot be deferred to "after the first component is built" because the first component's tests are the first consumer.

## Test Strategy Hooks

- **Test level:** this document defines the *means* by which the test-level strategy (see `[[test-pyramid-strategy.qa-tech-lead.md]]`) is implemented. It is itself the meta-test-strategy.
- **Fixtures required:** the test infrastructure described above *is* the fixture suite. Self-referential by design.
- **Observable signals:** for the test infra itself, the observable signal is "tests can be written quickly, run quickly, and fail honestly". Measure: time to write a new integration test (target: under 30 min for someone familiar with the suite); time to run the full unit + integration suite (target: under 5 min on CI); test flake rate (target: under 1% across one week of CI runs).
- **Non-determinism risks:** the differential harness is the most flake-prone piece because it depends on both gateways being healthy and on the normalizer covering the right fields. Mitigation: retry-with-backoff for transient diffs, and a quarantine list for known-flaky scenarios with mandatory weekly review.

## Open Questions

- `@architect` — will the Go rewrite use a DI framework (`uber-go/fx`, `google/wire`) or plain constructors? Plain constructors preserve the ability to unit-test components in isolation, which is a major win over the Java suite. Strongly prefer plain constructors.
- `@architect` — is `testcontainers-go` acceptable as a hard dependency for the integration test tier, or do you want a `docker compose`-based fallback for environments where Docker-in-Docker is hard? My preference: testcontainers-go, gated behind a build tag, no fallback. Docker is a reasonable test-time requirement in 2026.
- `@architect` — for the differential harness, do we run the Java gateway under its own JVM in CI (slow but real), or do we record-and-replay Java responses once and diff against the recording (fast but stale)? My preference: live JVM, gated to a nightly job, with a record/replay smoke test on every PR.
- `@trino-expert` — what's the smallest Trino subset (Java image variant, plugins) that we can run in `TrinoContainer` for differential tests? `trinodb/trino` defaults are ~1.5GB; if we can use a slimmer image with only the `tpch` connector, container boot drops materially.
- `@go-qa` — once Phase 2 starts, you own implementation of items 1-6 above. Want to coordinate on owner-per-item and order before that?
- `@team-lead` — items 1-6 represent 12-18 person-days. If the team's expected pace is 1-2 person-days per component, this is up-front overhead worth ~10 components. Confirm this is acceptable before Phase 2 begins.

## Cross-references

- `[[test-pyramid-strategy.qa-tech-lead.md]]` (sibling, under `studies/trino-gateway/`) — the strategy this infrastructure serves.
- `[[component-signoff-rubric.qa-tech-lead.md]]` (sibling) — the sign-off bar that the infrastructure must support measuring.
- `[[test-infrastructure.java-qa.md]]` (Java QA, Task #23, sibling) — the full Java-side inventory that this study borrows from selectively.
