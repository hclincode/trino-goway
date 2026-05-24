---
title: Routing engine behaviors and existing test oracle
author: java-qa
role: Java QA
component: trino-gateway
topics: [routing-engine, query-classification, session-state]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/proxy-request-lifecycle.java-qa.md
  - trino-gateway/test-infrastructure.java-qa.md
  - trino-gateway/test-gaps-and-risks.java-qa.md
  - trino-gateway/qa-gaps-and-risks.go-qa.md
---

# Routing engine behaviors and existing test oracle

## Summary

Inventory of the four routing-selector modes (header, file-based MVEL rules, external HTTP service, query-count) and the three routing-decision-feeders (request body, request headers, request user) — with the existing Java tests that pin each behaviour. The single most important takeaway: routing behaviour is testable as a pure function `request → routing group → backend`, with rich existing oracle coverage; the only blocker for Go portability is the **MVEL expression language** in the file-based selector, which has no Go equivalent and forces a fixture re-author or a sidecar.

## Key Findings

### Two-level decomposition: group selection then backend selection

The routing engine has two cleanly separated phases:

1. **`RoutingGroupSelector.findRoutingDestination(request)`** picks a routing *group name* (e.g. `"adhoc"`, `"etl"`, `"will-group"`) plus optional injected headers. Four implementations: header-only, file-based rules, external HTTP, all returning `RoutingSelectorResponse(routingGroup, externalHeaders)`. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/RoutingGroupSelector.java:26-65`.
2. **`RoutingManager.provideBackendConfiguration(routingGroup, user)`** picks a *backend cluster* from the active+healthy backends in that group. Two implementations: `StochasticRoutingManager` (uniform random) and `QueryCountBasedRouter` (least-loaded). `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/StochasticRoutingManager.java:38-46`, `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/QueryCountBasedRouter.java:233-241`.

The two are independent — any selector can be paired with any manager. Tests cover each combination through a mix of unit and integration paths.

### Four group-selector modes

**Mode 1 — Header-only (`byRoutingGroupHeader`)**

- **Behaviour:** read `X-Trino-Routing-Group`; if present, return it; if not, return `null` (which the caller treats as "use default group"). `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/RoutingGroupSelector.java:34-37`.
- **Existing tests:** `TestRoutingGroupSelector.testByRoutingGroupHeader:82-100`. Two cases (header present, header absent).
- **Observables:** the chosen routing group, and (transitively) which backend the request lands on.

**Mode 2 — File-based MVEL rules (`byRoutingRulesEngine`)**

- **Behaviour:** YAML file of `MVELRoutingRule` entries (`name`, `description`, `priority`, `condition`, `actions`); all rules whose `condition` evaluates `true` fire in priority order; their `actions` write into a shared `result` map (and an optional shared `state` map). `result.routingGroup` wins. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/FileBasedRoutingGroupSelector.java:58-81`, `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/MVELRoutingRule.java:94-126`.
- **Inputs available to MVEL expressions:** `request` (`HttpServletRequest`), `trinoQueryProperties` (`TrinoQueryProperties`), `trinoRequestUser` (`TrinoRequestUser`), `result` (mutable map written by actions), `state` (mutable map shared across rules for the same request). Imports include `java.util.*` and a whitelist of `java.lang` types (Math, String, Boolean, Integer, etc.), excluding `Process` and `Runtime` as security hardening. `MVELRoutingRule.java:71-92`.
- **File hot reload:** `FileBasedRoutingGroupSelector` uses `Suppliers.memoizeWithExpiration(loader, rulesRefreshPeriod)`. Default refresh is configurable per `RulesConfiguration`. `FileBasedRoutingGroupSelector.java:55`.
- **Existing tests:** 24+ test methods in `TestRoutingGroupSelector` cover four rule fixtures (`atomic`, `priorities`, `if_statements`, `state`) plus a TrinoQueryProperties-specific fixture and a file-change/reload test. Some of the more incisive:
  - `testByRoutingRulesEngine`: airflow source → `etl` group (parameterized across four fixtures). `:102-115`.
  - `testByRoutingRulesEngineSpecialLabel`: airflow + `label=special` client tag → `etl-special`. `:309-324`.
  - `testByRoutingRulesEngineNoMatch`: special label without airflow source → `null` (no group). `:326-343`.
  - `testGetUserFromBasicAuth`: Basic-auth user `will` → `will-group`. `:117-135`.
  - `testTrinoQueryPropertiesQueryDetails`: SQL with cross-catalog tables → `tbl-group`. `:137-156`.
  - `testTrinoQueryPropertiesQueryType`: INSERT statement → `type-group`. `:198-215`.
  - `testTrinoQueryPropertiesResourceGroupQueryType`: CREATE TABLE → `resource-group-type-group` (DATA_DEFINITION). `:217-234`.
  - `testTrinoQueryPropertiesAlternateStatementFormat`: v2 client body (JSON wrapper with `preparedStatements` + `query`). `:236-253`.
  - `testTrinoQueryPropertiesPreparedStatementInHeader`: `EXECUTE statement4` with the prepared-statement body in `X-Trino-Prepared-Statement` headers. `:255-279`.
  - `testTrinoQueryPropertiesParsingError`: malformed SQL → falls through to `no-match`, `trinoQueryProperties.isQueryParsingSuccessful() == false`, error message populated. `:281-307`.
  - `testByRoutingRulesEngineFileChange`: rule file modified on disk, new value picked up after refresh period (test uses `Thread.sleep`). `:345-392`.
- **Observables:** the chosen routing group; for parsing failures, `TrinoQueryProperties.isQueryParsingSuccessful()` is `false` and `getErrorMessage()` is non-empty (these are observable on the cached request attribute, not the response).

**Mode 3 — External HTTP service (`byRoutingExternal`)**

- **Behaviour:** POST a `RoutingGroupExternalBody` JSON envelope to a configured URL; the response is `ExternalRouterResponse(routingGroup, externalHeaders, errors)`. If `errors` is non-empty and `propagateErrors=true`, throws `WebApplicationException(400)` (`BAD_REQUEST`) with the errors as the body. On any exception, falls back to the value of `X-Trino-Routing-Group` (so the external mode degrades gracefully to header mode). `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/ExternalRoutingGroupSelector.java:90-141`.
- **Request envelope shape:** `RoutingGroupExternalBody(trinoQueryProperties, trinoRequestUser, contentType, remoteUser, method, requestUri, queryString, session, remoteAddr, remoteHost, parameterMap)`. `ExternalRoutingGroupSelector.java:143-164`.
- **Header policy:** all incoming headers except `Content-Length` and the configured `excludeHeaders` are forwarded to the external service. `ExternalRoutingGroupSelector.java:72-75, 166-177`.
- **Response header injection:** the external response's `externalHeaders` (after `excludeHeaders` filter and null-removal) are merged into the proxied request via `HeaderModifyingRequestWrapper`. This is how an external router can rewrite or inject headers on the downstream request. `ExternalRoutingGroupSelector.java:120-133`.
- **Existing tests:** `TestExternalRoutingGroupSelector` — exercises happy path, errors-with-propagate-errors, errors-without-propagate-errors, header injection, fallback on exception.
- **Observables:** the body the external service receives (`RoutingGroupExternalBody` JSON shape), the gateway's behaviour on each branch (200 with valid body, 200 with errors + propagateErrors=true → 400 from gateway, 200 with errors + propagateErrors=false → fallback, network error → fallback), and which mock backend the request finally lands on.

**Mode 4 — (implicit) cookie/query-id sticky routing — bypasses the selector**

- **Behaviour:** `RoutingTargetHandler.getPreviousCluster` runs *before* any selector; if a query id is present and the routing manager knows its backend, or if a `GatewayCookie` with a backend is present and matches the request path, the request bypasses the selector entirely. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:70-87, 153-172`.
- **Observable:** continuation requests for the same query id route to the same backend (proven by the gateway-returned `nextUri` host:port being addressable).
- **Existing tests:** indirectly by `TestGatewayHaMultipleBackend.testDeleteQueryId:234-256` (DELETE on `nextUri` succeeds because routing manager remembers the backend), `testTrinoClusterHostCookie:198-232` (cookie stickiness), and `testCookieSigning:334-374` (tampered cookies are rejected).

### Two backend-selector modes

**`StochasticRoutingManager`** — uniform random over all active healthy backends in the group. `StochasticRoutingManager.java:38-46`. Test: `TestStochasticRoutingManager`. **Random seed is unseeded** (`Random RANDOM = new Random()` at `:27`), which is fine for production but a flakiness vector for any test asserting on a specific cluster choice. The single existing `@Test` (`TestStochasticRoutingManager.java:49-78`) sidesteps the issue by adding five backends to one group, marking four UNHEALTHY (`:71-74`), and asserting the one remaining HEALTHY backend (`test_group0`) is selected (`:76-77`) — a degenerate single-choice case, not a tolerance-of-stochasticism. The router's actual distribution behaviour is therefore untested today; treat as a coverage gap (cross-listed in `[[test-gaps-and-risks.java-qa.md]]`), not a flake risk.

**`QueryCountBasedRouter`** — picks the least-loaded backend, with a three-level comparator:
1. fewer queries queued *for that user* on this cluster
2. fewer queries queued *cluster-wide*
3. fewer queries running cluster-wide

After a routing decision, the local `clusterStats` map is incremented immediately (`updateLocalStats`) so the next request sees the updated state without waiting for the next stats refresh. The increment policy: if the user already has queued queries, assume the new query is also queued; otherwise assume it goes straight to running. `QueryCountBasedRouter.java:166-203, 233-241`.

- **Existing tests:** `TestQueryCountBasedRouter` (303 lines, 5 backends `c1`-`c5` + 1 unhealthy, exhaustive user/cluster combinations).
- **Observable:** the chosen backend URL for each call, and the `clusterStats()` view (`@VisibleForTesting` accessor at `QueryCountBasedRouter.java:36-40`).

### Fallback chain when group has no backends

`BaseRoutingManager.provideBackendConfiguration` filters by routing group and health, picks via the subclass strategy, and **if empty, falls back to `provideDefaultBackendConfiguration`** which uses the configured default routing group. If the default group is also empty, throws `IllegalStateException("Number of active backends found zero")`. `BaseRoutingManager.java:104-110, 91-97`.

- **Behavior:** routing group requested but unavailable → silently routed to default group; default group unavailable → request fails.
- **Observable:** mock backend receives a request that the rule directed to a group it does *not* belong to (because the requested group had no backends).
- **Existing tests:** `TestRoutingManagerNotFound` (the not-found-fallback test by name; specifics in source).

### Query-id continuation lookup with fallback

When a query id is present but not cached, `BaseRoutingManager.findBackendForUnknownQueryId` checks the query history table; if still not found, calls `searchAllBackendForQuery` which issues `HEAD /v1/query/<id>` to every backend in parallel and returns the first that responds 200. The search loop only checks `isDone()` futures, so it races (a slow backend that holds the answer can be missed); on miss, falls back to "first active backend in the default routing group." `BaseRoutingManager.java:184-239`.

- **Observable:** a poll on a `nextUri` whose query id was forgotten still produces a response (right backend or wrong backend, you cannot tell from outside).
- **Existing tests:** `TestRoutingManagerExternalUrlCache` covers cache loading; `searchAllBackendForQuery` has no direct test.

### Routing-decision observable surface (summary)

For a Go test, the assertions available are:

| Observable | How to assert | Existing-test reference |
|---|---|---|
| Routing group chosen | `RoutingSelectorResponse.routingGroup()` returned by the selector | `TestRoutingGroupSelector` throughout |
| External headers injected | Mock backend records headers; assert presence/value | `TestExternalRoutingGroupSelector` |
| Backend cluster chosen | Mock backend's recorded request, OR `routingManager.findBackendForQueryId(id)` after the call | `TestGatewayHaMultipleBackend.testQueryDeliveryToMultipleRoutingGroups:170-195` |
| Routing-decision log line | `Rerouting [scheme://host:port/path]--> [target]` info-level line at `RoutingTargetHandler.java:182` | Not asserted today; candidate observable |
| Query history row | DB row contains `routingGroup`, `backendUrl`, `queryId`, `user`, `source`, `queryText` | `BaseTestQueryHistoryManager` |
| Cluster-stats cache state | `QueryCountBasedRouter.clusterStats()` returns updated counts | `TestQueryCountBasedRouter` |
| Parsing failure on rules path | `trinoQueryProperties.isQueryParsingSuccessful()` is `false` and `getErrorMessage()` non-empty | `TestRoutingGroupSelector.testTrinoQueryPropertiesParsingError:281-307` |
| External router error propagation | Gateway returns HTTP 400 with the error list as body | `TestExternalRoutingGroupSelector` |

### Rule-fixture corpus (oracle for MVEL semantics)

Located at `trino-gateway/gateway-ha/src/test/resources/rules/`:

- `routing_rules_atomic.yml` — two rules, demonstrating priority-free conditions on `X-Trino-Source` + `X-Trino-Client-Tags`.
- `routing_rules_priorities.yml` — uses `priority` field to enforce ordering.
- `routing_rules_if_statements.yml` — embedded if/else logic in MVEL expressions.
- `routing_rules_state.yml` — uses the shared `state` map across rules within the same request.
- `routing_rules_concurrent.yml` — exercises thread-safety of rule evaluation.
- `routing_rules_trino_query_properties.yml` — uses `trinoQueryProperties` methods (table extraction, query type, default catalog/schema). The richest oracle for SQL-aware routing semantics.
- `routing_rules_update.yml` — used by hot-reload tests.

These seven fixtures, paired with the `TestRoutingGroupSelector` parameterized cases, constitute the **authoritative behavioural spec of routing-rule semantics** for the gateway. Whatever expression language the Go rewrite picks must produce the same routing decisions for the same inputs against equivalent rule files.

### Routing modes that DO NOT exist in the Java gateway

For scope clarity:

- No per-user weighted random routing.
- No round-robin within a group.
- No consistent-hash routing (e.g. on a session key).
- No latency-aware or geographic routing.
- No multiple-selector composition (you pick one selector mode per gateway instance, not a chain).
- No A/B percentage split between two backends.

If any of these is in scope for the Go rewrite, it is a *new* feature, not a port.

## Behavior vs. Implementation Artifact

### MVEL expression language for rules

- **Observed behavior:** rule `condition` and `action` fields are MVEL expressions evaluated against a Java object graph (`HttpServletRequest`, `TrinoQueryProperties`, `TrinoRequestUser`, two mutable maps). MVEL is a JVM-only expression language.
- **Source of behavior:** `jvm-artifact` from the perspective of the public contract — operators write expressions in MVEL, but the *semantic intent* (header equality, string-contains, set-membership, query-type check) is portable.
- **Rationale:** MVEL was chosen as a lightweight JVM-embedded scripting language for rule files. There is no protocol-level reason to use MVEL specifically.
- **Go obligation:** `defer-to-expert`. Three viable paths, ranked by preference for further investigation:
  - **(a)** embed an MVEL-equivalent Go expression engine and port the seven YAML fixtures to the new syntax, breaking operator compatibility but preserving behaviour. Go QA's `[[qa-gaps-and-risks.go-qa.md]]` independently recommends **CEL** (`cel-go`) as the specific instance of this option; `Expr` (`expr-lang/expr`) is a viable alternative. Both java-qa and go-qa converge on this option as the most plausible path.
  - **(b)** run a sidecar MVEL evaluator and call it as the "external router".
  - **(c)** define a structured (non-Turing-complete) rule schema that covers the existing fixture corpus and reject anything fancier.

  The choice is the Architect's; QA's role is to ensure whatever path is picked has its decisions oracle-tested against the seven fixtures' input/output pairs. **Sandboxing requirement applies to all three paths** — see the Implications section below.
- **Notes:** the existence of MVEL security hardening upstream (excluding `Process` / `Runtime` imports) is a clue that MVEL was intentionally restricted; that fact alone does not rule any specific path out, but it does shape what "equivalent" means — see the Implications-section bullet on sandboxing.

### Query property extraction via Trino SQL AST (`io.trino.sql.parser.SqlParser`)

- **Observed behavior:** `TrinoQueryProperties` parses the request body as Trino SQL and exposes catalogs, schemas, tables, query type, and resource-group query type via getter methods consumable by rules. `routing_rules_trino_query_properties.yml` rules use `trinoQueryProperties.tablesContains(...)`, `getCatalogs()`, `getSchemas()`, `getQueryType()`, `getResourceGroupQueryType()`.
- **Source of behavior:** `gateway-design-intent` for what is exposed (catalogs, tables, query type); `jvm-artifact` for *how* (the Java AST types are not portable).
- **Rationale:** operators want to route based on what the query touches, not just on headers.
- **Go obligation:** `defer-to-expert`. The Architect needs to decide whether to ship a Go Trino-compatible SQL parser, call out to one, or downgrade the routing surface to non-SQL-aware. The seven `routing_rules_trino_query_properties.yml`-derived test cases ARE the spec of what must work.
- **Notes:** `TestRoutingGroupSelector.testTrinoQueryPropertiesParsingError:281-307` proves the gateway is resilient to parse failure (falls through to default group, surfaces error via `TrinoQueryProperties.getErrorMessage()`). This resilience is part of the contract.

### Group fallback to default when group has no backends

- **Observed behavior:** if the rule-chosen group has no active+healthy backends, silently route to the default group's backends instead. `BaseRoutingManager.java:104-110`.
- **Source of behavior:** `gateway-design-intent`. Avoids hard-failing a request just because operators forgot to bind a backend to a routing group.
- **Rationale:** graceful degradation.
- **Go obligation:** `replicate-exactly`. This is observable behavior (test: `TestRoutingManagerNotFound`). Mark with a clear log line so operators can spot the fallback in monitoring.

### External-router exception → fallback to header

- **Observed behavior:** any exception in `ExternalRoutingGroupSelector.findRoutingDestination` (network error, JSON parse failure, etc.) results in returning a `RoutingSelectorResponse` based on the `X-Trino-Routing-Group` header instead. `ExternalRoutingGroupSelector.java:135-141`.
- **Source of behavior:** `gateway-design-intent`. The external router is not a single point of failure — when it dies, the gateway keeps routing on header values.
- **Rationale:** availability.
- **Go obligation:** `replicate-exactly`. Test: external router returns 500, gateway still routes the request to the header-named group. Test: external router connection refused, same fallback. Test: external router returns invalid JSON, same fallback. These are not covered exhaustively in Java today; promote to first-class Go tests.

### Cluster-stats local-update racing with the stats refresh

- **Observed behavior:** after `QueryCountBasedRouter.selectBackend` returns, the local `clusterStats` map is incremented immediately so subsequent decisions reflect the just-routed query. The full refresh from the cluster monitor overwrites these counts wholesale every refresh interval. `QueryCountBasedRouter.java:189-203, 206-212, 233-241`.
- **Source of behavior:** `gateway-design-intent`. Prevents thundering-herd of queries to the same cluster between stats refreshes.
- **Rationale:** load balancing under sub-refresh-interval load bursts.
- **Go obligation:** `replicate-intent`. The exact heuristic ("if user has any queued queries, assume this new one queues") is a tuning choice that may legitimately differ; what must be preserved is the property that two concurrent routing decisions don't both pile onto the same cluster. Synchronisation discipline must be tested (concurrent `provideBackendConfiguration` calls should produce a balanced distribution).
- **Notes:** existing tests do not stress the concurrency angle — they call `selectBackend` serially. This is a high-priority gap for the Go rewrite. Documented as **G4** in `[[test-gaps-and-risks.java-qa.md]]` and as **Gap 1** in the Go-side register `[[qa-gaps-and-risks.go-qa.md]]`.

### `searchAllBackendForQuery` race on `isDone()`

- **Observed behavior:** when a query id's backend is unknown, the gateway HEADs every backend in parallel and accepts the first 200; but it only iterates `responseCodes.entrySet()` and checks `isDone()`, **without waiting**, so the answer-bearing backend can be missed entirely if it's slow. `BaseRoutingManager.java:220-227`.
- **Source of behavior:** `defensive-historical`. Probably a `Future.get()` was simplified to `isDone()` at some point.
- **Go obligation:** `replicate-intent`. The intent (find a backend that knows the query, fall back gracefully) must be preserved; the race-on-`isDone()` should be fixed in the rewrite. Test the intent: after gateway restart with cache wiped, a poll for a previously-routed query id reaches the right backend (assuming history persisted).
- **Notes:** flag this in the architect's design review — Go's `select { case <-future: }` plus a small timeout is the obvious fix.

## Implications for Go Rewrite

- The routing engine is testable as a pure function (`request → routing group, request → backend cluster`) with rich existing fixtures. Go QA can build a table-driven test suite that runs the same input set against the Go selector and asserts equivalence with the Java oracle.
- The MVEL+SQL-AST entanglement in the file-based selector is the single largest open question. Until the Architect decides the Go expression language and SQL-parsing strategy, the Go QA team cannot finalise the rule-evaluation test suite. We can, however, freeze the *input set* (the seven YAML fixtures plus the parameterized test cases) and the *expected outputs* as language-neutral oracle pairs.
- The external-router mode is the easiest to port: it's a pure HTTP-over-JSON contract. Port it first; it gives operators an escape hatch (write the rule logic in any language) and validates the routing pipeline before the MVEL question is solved.
- The query-count balancer needs *concurrent* tests in the Go rewrite — the existing Java tests are serial and would not have caught a regression on the local-update synchronisation. Use Go's `testing.RunParallel` or explicit goroutines + atomic counters to drive load through `provideBackendConfiguration`.
- The stochastic router should be testable with a seeded RNG injected via the constructor, so tests can assert specific picks rather than distributions. The Java code's `static final Random RANDOM = new Random()` is a testability anti-pattern not worth porting.
- The group-fallback-to-default and the external-router-exception-fallback are graceful-degradation contracts — these are exactly the kinds of behaviour that get accidentally broken in a rewrite. Promote both to first-class Go tests; they protect operators from silent failures.
- The seven rule fixtures, plus the body/header inputs from `TestRoutingGroupSelector`, plus the table-extraction queries from `provideTableExtractionQueries`, should be checked into the Go test repo verbatim and treated as the routing-semantics oracle.
- **No arbitrary code execution from rule files is a wire-level security invariant the Go rewrite must preserve.** The Java gateway intentionally restricts MVEL (excluding `Process` / `Runtime` imports and similar JVM hooks). Whatever expression engine or rule schema the Go rewrite picks — CEL, Expr, structured DSL, sidecar — it must guarantee a rule file *cannot* (a) exec a subprocess, (b) read or write arbitrary filesystem paths, (c) open network sockets to non-routing destinations, (d) call into reflection on host objects. This is a non-negotiable property; an expression engine that does not offer a sandbox or capability-restricted evaluation mode should be rejected on this ground alone. CEL is sandboxed by construction; Expr requires explicit configuration to restrict the allowed function set. Add this as an evaluation criterion when the Architect picks the engine.

## Test Strategy Hooks

- **Test level:** unit for selector semantics (table-driven `request → group`); unit for backend-pickers (`StochasticRoutingManager`, `QueryCountBasedRouter`) with controlled stats fixtures; integration for the full proxy → selector → manager → backend path (mock backend records the chosen URL); load/concurrency for `QueryCountBasedRouter.selectBackend` under parallel calls; integration for external-router mode using a controllable test HTTP server.
- **Fixtures required:** the seven YAML rule files (port verbatim, possibly with MVEL → target-language transliteration); the test-case body+header sets from `TestRoutingGroupSelector` (also verbatim); a fake "external router" HTTP server with switchable response (valid / errors+propagate / errors+silent / network error / invalid JSON / inject headers); `ClusterStats` fixture builder with parameterised running/queued/user counts; a routing-rules fixture file path that the gateway can hot-reload mid-test.
- **Observable signals:** the selector's returned routing group; the manager's returned backend URL; the mock backend's recorded URI and headers (proves end-to-end); HTTP `400` from the gateway when external returns errors with `propagateErrors=true`; the `Rerouting` log line if pinned as observable; query history row with `routingGroup` field; `clusterStats()` snapshot reflecting post-decision local update.
- **Non-determinism risks:** unseeded `Random` in stochastic router (Go: inject `*rand.Rand`); rule hot-reload tests rely on `Thread.sleep` past the refresh period (Go: use a fakeable clock OR explicit cache invalidation); external-router tests are time-sensitive if the HTTP client has long timeouts (Go: use `httptest.Server` with sub-second behaviour); concurrent backend selection tests need explicit synchronisation rather than wall-clock sleeps.

## Open Questions

- @architect: which Go expression language for routing rules? Until this is decided, the file-based selector's test suite cannot be finalised. (Cross-listed in `[[test-infrastructure.java-qa.md]]`.)
- @architect: is SQL-aware routing (catalog/schema/table extraction, query-type detection) in scope for the Go rewrite? If yes, what is the SQL-parsing strategy? If no, what is the migration story for operators relying on `trinoQueryProperties.tablesContains` and friends?
- @trino-expert: the `searchAllBackendForQuery` `isDone()` race — is this a known bug (in which case the Go rewrite should fix it) or an intentional bound on latency (in which case behaviour to preserve)?
- @trino-expert: when `propagateErrors=true` and the external router returns errors, the gateway returns HTTP 400 with the error list. Is the body shape (array of strings) part of any documented contract, or can the Go rewrite reshape it?
- @qa-tech-lead: should I draft separate test specs for each rule fixture (one spec per YAML file, with input/output table), or one omnibus spec? I'd lean toward per-fixture since each is a distinct semantic concern, but happy to consolidate. **Resolved by @qa-tech-lead during peer review: per-fixture, but Phase 3 (component implementation cycle) work — not part of the current study phase. Keep this omnibus inventory as the current artifact; fragment into per-fixture specs only when component implementation begins.**

## Cross-references

- `[[proxy-request-lifecycle.java-qa.md]]` — Seam 3 (routing decision) referenced from the lifecycle map.
- `[[test-infrastructure.java-qa.md]]` — patterns for the selector unit tests, the mock-backend integration tests, and the external-router fake HTTP server.
- `[[test-gaps-and-risks.java-qa.md]]` — concurrency gap on `QueryCountBasedRouter`, the `searchAllBackendForQuery` race, untested external-router failure modes.
- `[[../both/statement-protocol-invariants.java-qa.md]]` — query-id continuation routing depends on the protocol invariant that the same query-id is used across `nextUri` polls.
