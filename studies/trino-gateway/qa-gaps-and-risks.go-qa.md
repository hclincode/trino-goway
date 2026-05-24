---
title: QA gaps and high-risk untested behaviors in trino-gateway
author: go-qa
role: Go QA
component: trino-gateway
topics: [test-infra, cross-cutting, routing-engine, proxy-core]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/test-infrastructure-inventory.go-qa.md
  - trino-gateway/proxy-lifecycle-testable-seams.go-qa.md
  - trino-gateway/routing-engine-test-oracle.go-qa.md
---

# QA gaps and high-risk untested behaviors in trino-gateway

## Summary

The Java suite gives strong coverage of routing-decision logic and SQL parsing but is thin on concurrency, observability assertions, error-path body contents, and any behavior that requires asserting on logs or metrics. The single largest risk to the Go rewrite's parity is that the routing engine is built on MVEL (JVM-only) and the SQL parser is built on `trino-parser` (JVM-only). Both have no drop-in Go replacement, and neither has a `replicate-exactly` path. This study lists the specific gaps and ranks them by likely impact.

## Key Findings

### Gap 1 — No concurrency or race-condition coverage (HIGH risk)
The Java suite has zero `@RepeatedTest`, no parallel-execution stress tests, no concurrent-load tests of the routing manager, and no race-detector tooling.
- Evidence (reproducible):
  ```
  grep -rln "@RepeatedTest\|Execution(.*CONCURRENT\|@Execution\|invokeAll\|CountDownLatch\|CyclicBarrier" \
    trino-gateway/gateway-ha/src/test --include="*.java"
  # returns: (no matches)
  ```
  A separate search for `Concurrent` in test sources returns only `BaseTestDatabaseMigrations.java` and `TestRoutingRulesManager.java`, both of which reference `ConcurrentHashMap` from production code paths rather than running anything concurrently from the test driver. The only file in the test tree with `concurrent` in its name is `src/test/resources/rules/routing_rules_concurrent.yml`, which is a routing-rules fixture file, not a test.
- What this means for Go: any concurrency bug in the Java gateway today is undetected by the existing test suite. We cannot use the Java suite as an oracle for "this won't deadlock" or "this won't corrupt state under load."
- Required Go investment: `go test -race` mandatory in CI; `goleak.VerifyNone(t)` in every package with goroutines; explicit concurrent tests for `RoutingManager` query-ID binding under simultaneous statement POSTs; explicit cancellation tests using `context.WithCancel`.

### Gap 2 — Body-buffering / streaming behavior is untested (HIGH risk)
The Java `ProxyRequestHandler` materializes upstream response bodies into memory to extract the query ID (`response.body()` is a byte buffer). There is no test that asserts large response bodies are not double-buffered, no memory-pressure test, no slow-client backpressure test.
- Evidence: `ProxyRequestHandler.java:281-301` reads body fully; no test exercises a body larger than a few hundred bytes.
- Impact for Go: if we adopt a streaming approach (`io.TeeReader` + bounded buffer) the Java suite cannot tell us we did it right. If we don't, the Go gateway may quietly degrade on real Trino result-set pages (~MB range).
- Required Go investment: test with a synthetic backend serving a 10MB response and assert (a) the client receives it streamed (`Transfer-Encoding: chunked` preserved, or content delivered as it arrives), (b) the gateway's resident memory does not balloon to 10MB × N concurrent requests, (c) the query-ID is still extracted correctly from the header of the JSON.

### Gap 3 — Log-line and metric-name assertions are absent (MEDIUM risk)
No Java test asserts on log content or metric values for routing decisions, health-check transitions, or proxy errors.
- Evidence (reproducible):
  ```
  grep -rln "LogCapture\|LogAppender\|ListAppender\|contains(\"Rerouting\")" \
    trino-gateway/gateway-ha/src/test --include="*.java"
  # returns: (no matches)
  ```
- Impact: in production, operators rely on log lines like `"Rerouting [...] --> [...]"` for debugging and on metrics for alerting. If the Go gateway changes log format or metric names, operator tooling breaks silently — no test catches it.
- Required Go investment: with the Go logger (recommend `log/slog`) and metrics library settled, write explicit assertions that key events emit stable log substrings (`"Rerouting "`, `"Failed to get QueryId"`, `"Non OK HTTP Status code"`, `"Proxy request failed: "`) and that key counters increment (proposed: `gateway_requests_total{routing_group=, backend=, status=}`, `gateway_routing_decision_seconds`, `gateway_health_transitions_total`).

### Gap 4 — Failed-statement-POST silent fall-through (MEDIUM risk)
If the upstream returns `200 OK` but the body is not valid JSON (or `"id"` is missing), the Java code logs at ERROR and continues — the client gets the 200 response but the query is NOT bound for stickiness. Subsequent requests for that query will be re-routed by the rule engine (likely to a different backend), and the original backend's in-flight query becomes orphaned.
- Evidence: `ProxyRequestHandler.java:289-296` — catch block logs and returns the unmodified response.
- No test for this case.
- Impact: real-world failure mode. Likely caused by an upstream Trino bug or by a non-Trino server impersonating one. Production users have probably been bitten.
- Required Go investment: explicit test for "200 with malformed body" — assert (a) response is still 200, (b) query is NOT bound in the binder mock, (c) error log line is emitted, AND (d) the `query_history` row IS still inserted (with `queryId=null` or empty). The Java code calls `queryHistoryManager.submitQueryDetail(queryDetail)` unconditionally at `ProxyRequestHandler.java:299`, *after* the try/catch and outside the `if (statusCode==OK)` branch — so a regression that drops the history row on parse failure would slip past a test that only checks (a)-(c).

### Gap 5 — Non-200 from upstream during statement POST (MEDIUM risk)
If upstream returns e.g. `500 Internal Server Error` to a statement POST, the Java code logs ERROR and forwards the upstream response unchanged. No query binding occurs.
- Evidence: `ProxyRequestHandler.java:294-296`.
- No test for this case.
- Required Go investment: explicit test — assert response status is the upstream's status (502 / 500 / 503), body is the upstream's body, no query binding side-effect.

### Gap 6 — Header forwarding edge cases untested (MEDIUM risk)
The Java suite does not assert on which headers are forwarded for any header except (implicitly via routing) `X-Trino-Source`, `X-Trino-Routing-Group`, `X-Trino-Client-Tags`. There are no tests for:
- Case-insensitive `Host` skip (Java's `equalsIgnoreCase` covers it; the Go test should cover both `Host` and `host`).
- Empty header values.
- Headers with non-ASCII characters.
- Required Go investment: explicit table tests for header forwarding edge cases.
- See also the dedicated Behavior-vs-Artifact block below for `X-Forwarded-For` (standards non-compliance, called out separately because the architect needs to decide).

### Gap 7 — Cookie behavior under expiration / signature mismatch (LOW risk)
`GatewayCookie.isValid()` checks signature and expiration. The Java suite tests cookie behavior end-to-end in `TestGatewayHaMultipleBackend` but does not unit-test the signature-validation edge cases (tampered signature, expired ts, wrong secret).
- Required Go investment: targeted unit tests for cookie validation.

### Gap 8 — `Math.random()` port collisions (LOW operational, HIGH flake risk)
Tests use random-port-in-1000-window allocation. Across rapid CI runs of the same process, port collisions are possible (1-in-1000 per allocation × many ports per test).
- Evidence: `TestProxyRequestHandler.java:58-59` and similar.
- Impact: occasional CI flakes unrelated to the code under test.
- Go obligation: use `:0` everywhere — eliminated by construction.

### Gap 9 — Time-based file-watch hot-reload test is racy (LOW operational, MEDIUM flake risk)
`testByRoutingRulesEngineFileChange` writes file, sleeps 2× the 1ms refresh period (so 2ms), then expects to observe the new rule. On a loaded CI box this can fail randomly.
- Evidence: `TestRoutingGroupSelector.java:362-392`.
- Go obligation: must not replicate. Expose a reload-complete signal channel for tests.

### Gap 10 — No tests for OAuth2 callback failure / cookie tampering
The Java suite tests the OAuth2 happy path via `TestGatewayHaMultipleBackend` (initiate path + callback path) but does not test:
- Callback with mismatched state.
- Tampered or expired OAuth2 cookie.
- Cookie set in plaintext but `cookieSigningSecret` was rotated.
- Required Go investment: explicit security-flavored tests if we keep OAuth2 in the Go rewrite.

### Gap 11 — No load / capacity tests
There are no benchmarks, no JMH harnesses, no synthetic-load harnesses in the Java suite. We have zero data on throughput, latency percentiles under load, or behavior near connection-pool exhaustion.
- Impact: when the Go rewrite ships, we'll have nothing to compare to. Any claim of "X% faster" or "X% lower latency" is unfalsifiable without first establishing a baseline.
- Required Go investment: a small load harness (k6, `vegeta`, or stdlib `testing.B` for in-process benchmarks) and a one-shot measurement of the current Java gateway under the same load. This is a one-time cost.

### MVEL parity risk (HIGH)
The Java rule engine uses MVEL 2 (`io.trino.gateway.ha.router.MVELRoutingRule`). There is no Go MVEL interpreter. Aligning with java-qa's three-option framing (`routing-engine.java-qa.md` §"MVEL expression language for rules") so the architect sees one shortlist:
- **(a) Embed a Go expression engine (CEL or `expr-lang/expr`)** and port the seven YAML fixtures to the new syntax. Breaks operator config compatibility but preserves behavior. Easier static analysis than MVEL.
- **(b) Run a sidecar MVEL evaluator** and call it as the "external router". Highest operator continuity (rule files unchanged); adds an operational component.
- **(c) Define a structured (non-Turing-complete) rule schema** that covers the existing fixture corpus and rejects anything fancier. Breaks operator config compatibility; gains type safety and lock-in to a small surface.
- A fourth option also exists if the team accepts feature loss: **(d) Drop scripted rules entirely; require operators to use header-based routing or the external HTTP selector.** Smallest scope. Major capability regression.
- **My (go-qa) recommendation: option (a), specifically with CEL** — stable, typed at compile time, used heavily in Kubernetes/Envoy/Istio so battle-tested in adjacent routing contexts. java-qa notes the MVEL security hardening (excluding `Process`/`Runtime`) hints option (c) is closer to original design intent — a fair counterpoint. Final pick is the architect's.

### trino-parser parity risk (HIGH)
`TrinoQueryProperties` uses `io.trino.sql.SqlParser` to extract catalog/schema/table sets from query text and to classify query types. This parser is huge (the entire Trino grammar) and is JVM-only. Choices:
1. **Drop SQL-parsing-based routing; require operators to set `X-Trino-Routing-Group` explicitly or use catalog/schema headers only.** Smallest scope. Major capability regression.
2. **Port a Trino-grammar-aware Go parser.** Massive effort. There is no upstream Go Trino parser as of 2026-05.
3. **Write a focused Go parser that handles the statement types in `provideTableExtractionQueries`** (~30 statement forms). Bounded scope, growable as needed. Worse than full parser but tractable.
4. **Bridge to the Java parser via JNI or subprocess.** Adds JVM dependency back; defeats most of the Go-rewrite goals.
- Recommendation: option 3. Lift the existing 30-row oracle into Go test cases and grow the Go parser to satisfy them. Document explicitly that statement forms not in the oracle route via the default group until the parser is extended.

## Behavior vs. Implementation Artifact

### Random ports in tests
- **Observed behavior:** `Math.random()`-based port selection.
- **Source of behavior:** `jvm-artifact` — Jetty config-before-bind issue.
- **Go obligation:** `drop`.

### `Thread.sleep` for hot-reload sync
- **Observed behavior:** Fixed sleep tied to refresh period.
- **Source of behavior:** `defensive-historical`.
- **Go obligation:** `drop`. Use exposed reload signal or `Eventually`.

### MVEL rule language
- **Observed behavior:** Rules authored in MVEL 2.
- **Source of behavior:** `defensive-historical`.
- **Go obligation:** `defer-to-expert`. See @architect decision.

### trino-parser-based query analysis
- **Observed behavior:** Full Trino SQL grammar parsing for routing.
- **Source of behavior:** `gateway-design-intent` — rule authors want to write `query.tables contains 'foo'` style rules.
- **Go obligation:** `defer-to-expert`. Subset-parser approach (option 3 above) is my recommendation but not in my lane.

### Silent fallthrough on 200-with-malformed-body
- **Observed behavior:** `IOException` caught, logged, response forwarded unchanged. The query is not bound for stickiness; subsequent requests for that query will likely re-route to a different backend. The `query_history` row is still inserted (with `queryId=null`).
- **Source of behavior:** `defensive-historical` — better to deliver to the client than to fail the request entirely.
- **Go obligation:** `defer-to-expert`. Architect to decide: (a) preserve current behavior (deliver to client, log + counter for ops alerting); (b) convert to 502 so the failure surfaces. See Open Questions L164.
- **Notes:** Whichever way the decision goes, the Go test must pin the chosen behavior AND assert the `query_history` row insert side-effect.

### `X-Forwarded-For` replaces rather than appends (standards non-compliance)
- **Observed behavior:** When `forwardedHeadersEnabled=true`, the gateway sets `X-Forwarded-For` to its own view of the client `remoteAddr`, discarding any client-supplied chain. Cite: `ProxyRequestHandler.java:355` — `requestBuilder.addHeader(X_FORWARDED_FOR, servletRequest.getRemoteAddr())`.
- **Source of behavior:** `defensive-historical` — likely an oversight rather than intent. Standard reverse-proxy convention (RFC 7239 §4, and the de-facto rule for `X-Forwarded-For`) is to *append* the gateway's view to the existing comma-separated chain, preserving the audit trail of upstream hops.
- **Rationale:** None defensible. A client behind two layers of proxy already has a non-empty `X-Forwarded-For`; replacing it destroys the chain and breaks any backend logging or rate-limiting that reads it. This is also visible to security and audit tooling.
- **Go obligation:** `defer-to-expert`. Architect to decide between (a) `replicate-exactly` for parity (preserve the bug; document it as known and add a Go test pinning the current behavior); (b) fix to standards-compliant append-with-comma-separator (add a Go test pinning the new behavior; mark as an intentional behavior change in the migration docs).
- **Notes:** This is the only standards-non-compliance I've found so far that meaningfully affects downstream consumers. Worth raising explicitly in the go/no-go discussion. The Java test suite does not cover either path.

## Implications for Go Rewrite

- **The MVEL+trino-parser choice is the single largest schedule risk.** I cannot estimate test effort for the routing engine until @architect picks a rule-engine path and a SQL-parser path. I recommend timeboxing this decision before any routing-rule tests are written.
- **`go test -race` mandatory in CI** — non-negotiable from a Go QA standpoint, no exceptions. Catches the entire class of concurrency bugs that the Java suite cannot.
- **`goleak.VerifyNone(t)` in TestMain** of every package with goroutines (proxy, health checks, routing cache, file watcher).
- **Establish a Java-gateway throughput baseline now**, before we have anything to compare to. Otherwise we'll be unable to make defensible perf claims about the Go rewrite.
- **Plan extra test investment** on:
  - The malformed-body-200 silent-fall-through path (Gap 4).
  - Concurrent statement-POST query-binding (Gap 1).
  - Large-response streaming behavior (Gap 2).
  - These are the highest-likelihood-of-production-failure gaps and the Java suite gives us nothing to lean on.

## Test Strategy Hooks

- **Test level:** cross-cutting; affects all levels.
- **Fixtures required:** large-body backend (10MB+), concurrent-load harness, cancellation-injecting backend, malformed-JSON backend.
- **Observable signals:**
  - `goleak` zero-goroutine assertion at test end.
  - `-race` detector quiet for the duration of the test binary.
  - Memory delta during 10MB body test stays below e.g. 2MB (allowing streaming overhead and buffer reuse).
  - Backend recorded request shows the gateway sent through cancellation context after client disconnect.
- **Non-determinism risks:** all the patterns banned above (sleep-based sync, random ports, unpinned containers, racy file-watch waits).

## Open Questions

- @architect: MVEL replacement choice (blocking).
- @architect: trino-parser replacement choice (blocking for ~half of routing-engine tests).
- @architect: rebrand `"TrinoGateway"` in `Via` header to `"TrinoGoway"`, or preserve for parity?
- @architect: fix the missing-space typo in the timeout error body?
- @architect: do we keep the silent-fall-through on malformed 200 body, or convert to a 502?
- @architect: do we replicate the `X-Forwarded-For` replace-instead-of-append behavior, or fix to RFC-7239 append-with-comma? (See dedicated Behavior-vs-Artifact block above.)
- @qa-tech-lead: differential-test harness scope decision. **Answered (2026-05-24):** hybrid — record/replay smoke per PR, live differential nightly, gated to nightly CI. Scenario YAML files (not raw Go tests) for differential cases. Architect to weigh in on whether record/replay is acceptable for PR gating. Full design in `studies/trino-gateway/test-pyramid-strategy.qa-tech-lead.md` (cross-folder reference; lives in `trino-gateway/` per qa-tech-lead's authorship).
- @qa-tech-lead: do we want a Java-gateway baseline throughput measurement before the Go rewrite, or is "feature parity wins" sufficient? **Answered (2026-05-24):** yes, baseline now. Without it, any Go perf claim is unfalsifiable; the baseline also defends against being pressured to ship a Go rewrite that's actually slower. One-time cost, `vegeta`-driven harness against the local Java gateway is enough. Go QA owns the sub-task (to be created).
- @go-implementer: which structured logger and metrics library will we adopt? This determines what log substrings and metric names are stable for the Go QA assertions.

## Cross-references

- [[test-infrastructure-inventory.go-qa.md]]
- [[proxy-lifecycle-testable-seams.go-qa.md]]
- [[routing-engine-test-oracle.go-qa.md]]
- [[test-pyramid-strategy.qa-tech-lead.md]] — QA Tech Lead's pyramid (informs the test-level decisions referenced throughout this study)
