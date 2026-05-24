---
title: Component sign-off rubric for the Go rewrite of trino-gateway
author: qa-tech-lead
role: QA Tech Lead
component: both
topics: [test-infra, cross-cutting]
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/test-pyramid-strategy.qa-tech-lead.md
  - both/test-infrastructure-needs.qa-tech-lead.md
---

# Component sign-off rubric for the Go rewrite of trino-gateway

## Summary

Define "done" for each component class up front, so the gate at the end of every Phase-3 iteration is enforceable, not negotiated. There are five evidence categories — protocol fidelity, behavioral coverage, concurrency safety, observability, and degradation behavior — and not every component needs all five. This document maps component classes to required evidence, so when Go QA hands a component back to me for sign-off, I can check a list rather than re-argue scope. The rubric is intentionally tight: anything not explicitly excluded is required.

## Key Findings

### Why a rubric at all

- The Phase-3 cycle in TODO.md (`/Users/hclin/github/trino-goway/TODO.md:39-51`) places component sign-off on the QA Tech Lead. Without a pre-agreed bar, every sign-off becomes a one-off discussion that the team will optimize for "ship it" rather than for the rewrite's quality promise.
- The Java suite has implicit acceptance bars (tests must compile, tests must pass) but no explicit bars on *which categories* of test must exist. That's a luxury we don't have for a rewrite — protocol-fidelity bugs slip through tests that all pass.

### What the Java tests do and don't cover (informing the rubric)

- Java covers happy-path routing + basic OAuth2 + basic DB persistence + a small set of header/cookie checks (`TestGatewayHaMultipleBackend.java`, `TestGatewayHaSingleBackend.java`). It does not cover concurrency, soak, body cap, mid-request failure, or protocol-fidelity against a prior version. (See full gap register in `[[test-gaps-and-risks.java-qa.md]]`, Task #28.)
- The rubric below makes the categories Java is missing **required for the Go rewrite**, because that's exactly where a rewrite is most likely to drift.

## Behavior vs. Implementation Artifact

### Java's implicit bar: "tests pass"

- **Observed behavior:** the Java suite has no explicit per-component sign-off process; CI green is the bar.
- **Source of behavior:** `gateway-design-intent`. Reasonable for a mature codebase with low rewrite risk.
- **Go obligation:** `drop`. We need an explicit per-component bar because the rewrite has high regression risk and our test pyramid is broader than Java's.

## Implications for Go Rewrite

### The five evidence categories

For each component the Go Implementer hands to Go QA, the following categories may apply. Mandatory categories per component class are listed below.

1. **Protocol fidelity** — wire-level behavior matches the Java gateway, demonstrated by differential tests passing on the agreed scenario set. Required for anything on the request/response path (proxy core, statement protocol, auth).
2. **Behavioral coverage** — happy paths, error paths, edge cases (empty input, oversize input, missing headers, malformed input) all have explicit tests. Coverage measured by branch coverage of the component's exported surface, with a floor of 80% for unit-testable logic, no floor for integration-only logic.
3. **Concurrency safety** — runs cleanly under `go test -race`, no goroutine leaks under `go.uber.org/goleak`, graceful shutdown drains in-flight requests, no shared-mutable state without explicit synchronization. Required for any component holding state or fanning out goroutines.
4. **Observability** — required logs, metrics, traces are emitted with the agreed names/labels and asserted in tests. Required for components the operator needs to debug live (proxy core, health checks, routing, mgmt API).
5. **Degradation behavior** — explicit tests for "what does this component do when its dependency is unavailable / slow / returning errors". Required for proxy core (backend down/slow), routing engine (no eligible backend), persistence (DB down), config (file missing/malformed), health checks (target unreachable).

### Per-component-class rubric

| Component class | Protocol fidelity | Behavioral cov. | Concurrency safety | Observability | Degradation |
|---|---|---|---|---|---|
| **Proxy core** (request forwarding, header rewrite, body buffer, response forwarding) | **required** (differential) | **required** | **required** (race + leak + shutdown) | **required** (request log, latency metric, error counter) | **required** (backend-down, backend-slow, body-oversize) |
| **Statement protocol binding** (query-id extraction, nextUri rewrite) | **required** (differential) | **required** | **required** (race; multi-statement concurrency) | **required** (binding metric, lookup miss counter) | **required** (malformed JSON, missing id field) |
| **Routing engine** (selector strategies, rule eval) | recommended (differential on header → backend mapping) | **required** | **required** if any selector caches state | **required** (decision log per request, per-strategy metric) | **required** (no eligible backend, all backends down) |
| **Cluster registry + health checks** (active probing, state machine) | n/a | **required** | **required** (concurrent probe + reader race) | **required** (health state metric per backend, probe duration histogram) | **required** (probe timeout, probe HTTP error, container restart) |
| **Persistence** (query history, backend state) | n/a | **required** (schema migration applied, all CRUD round-trips) | **required** (concurrent write/read) | recommended | **required** (DB down, connection pool exhausted, schema version mismatch) |
| **Mgmt API** (REST endpoints) | recommended (differential on body shapes) | **required** | **required** (concurrent write to backend list) | recommended | **required** (auth failure, malformed JSON, invalid backend URL) |
| **Config loading + validation** | n/a | **required** (every config field exercised, both valid and invalid) | n/a (load is once at startup) | **required** (effective config logged at INFO with secrets redacted) | **required** (file missing, malformed YAML, env-var unset) |
| **Auth/OIDC** | **required** (differential on full handshake) | **required** | **required** (concurrent sessions) | **required** (auth success/failure counters, token validation latency) | **required** (IdP down, expired token, invalid signature) |
| **Web UI** | n/a (separate-vs-defer decision) | basic smoke | n/a | n/a | n/a |

### Coverage floor

- Branch coverage on unit-testable code: **80% minimum**, measured via `go test -coverprofile`. Below 80% requires written justification and my explicit approval.
- Coverage on integration-only code: not measured by line count (misleading for HTTP handlers). Measured instead by "does every documented behavior have at least one test that asserts on the observable signal".

### Evidence the Go Implementer hands to me at sign-off

1. Link to the merged PR(s) for the component.
2. CI run with all of: unit (`go test`), unit + race (`go test -race`), integration (`-tags integration`), differential (where required), and lint (`golangci-lint`) passing.
3. Coverage report with the unit-level number and a note on which integration-only behaviors are covered.
4. For protocol-fidelity components: the differential scenarios run, the diff outputs, and any normalizer additions made during the work (with my pre-approval if normalizer scope changed).
5. For concurrency-safety components: leak-check output, race-detector output, and the graceful-shutdown test result.
6. For degradation-required components: the chaos test results (backend down, dep down, etc.) with the documented expected behavior and observed behavior.

If any of those are missing, I send it back. No "we'll add that later". Adding it later means it doesn't happen.

### The "I sign off" decision tree

1. Does the component's class match a row in the rubric? If not, I name the missing class and assign a row before reviewing.
2. Is the evidence list above complete and CI-green? If not, return with the missing piece named.
3. For each `required` cell in the row, is there at least one test that exercises it and the test is asserting on the right signal (per `[[test-pyramid-strategy.qa-tech-lead.md]]`)? If not, return.
4. Have I or `java-qa` raised an open question in a study that pertains to this component, and is it still open? If yes, the question must be resolved before sign-off — either answered, or explicitly deferred with a created follow-up task.
5. Is there an architectural debt this component adds (TODO, hack, deferred design)? If yes, it must have a follow-up task created and linked.
6. If all of 1-5 pass: signed off, status updated, next component unblocked.

### What I do NOT enforce in the rubric

- Specific Go libraries beyond the test-infrastructure set in `[[test-infrastructure-needs.qa-tech-lead.md]]`. Architect's call.
- Specific package layout. Architect's call.
- Performance targets (latency, throughput). Defer until we have a measured Java baseline; create a separate study then.
- Documentation completeness. Out of scope for QA; talk to the team lead about whether docs are a separate gate.

## Test Strategy Hooks

- **Test level:** rubric is meta, applies across all levels.
- **Fixtures required:** none specific to this document; it depends on `[[test-infrastructure-needs.qa-tech-lead.md]]` being in place.
- **Observable signals:** the rubric itself is the observable for the sign-off process — a yes/no on each cell.
- **Non-determinism risks:** the only risk is that I drift from the rubric over time (especially under deadline pressure). Mitigation: revisit this document at each Phase-3 milestone and explicitly re-affirm or amend before signing the next component.

## Open Questions

- `@architect` — does the team agree to the 80% unit-coverage floor? It's tight; some teams prefer 70% or no floor. I'd rather negotiate it now than at the first sign-off.
- `@architect` — for "Protocol fidelity" rows marked required (proxy core, statement protocol, auth), are you committed to the Java differential harness as the oracle, or do you want a written behavioral spec to differential against instead? My preference is differential against live Java because it's cheaper to maintain.
- `@team-lead` — is the web UI in scope? The rubric assumes it's not unless we decide otherwise; defer until that's settled.
- `@go-qa` — when you're ready to implement tests, please scan this rubric and flag anything that's unimplementable as written. Better to fix the rubric than to ship tests that don't actually meet it.
- `@java-qa` — for the "Behavioral coverage" rows, your gap register (Task #28) will largely define what "happy + edge + error" means per component. Once that's drafted, I'll cross-reference it from each row so the rubric isn't abstract.

## Cross-references

- `[[test-pyramid-strategy.qa-tech-lead.md]]` (sibling, under `studies/trino-gateway/`) — the strategy this rubric enforces at sign-off.
- `[[test-infrastructure-needs.qa-tech-lead.md]]` (sibling) — the infrastructure the rubric presumes exists.
- `[[test-gaps-and-risks.java-qa.md]]` (Java QA, Task #28) — the gap register that defines per-component "behavioral coverage" content.
