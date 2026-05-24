---
title: Routing engine — existing test oracle and Go parity strategy
author: go-qa
role: Go QA
component: trino-gateway
topics: [routing-engine, query-classification, test-infra]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/test-infrastructure-inventory.go-qa.md
  - trino-gateway/proxy-lifecycle-testable-seams.go-qa.md
  - trino-gateway/qa-gaps-and-risks.go-qa.md
---

# Routing engine — existing test oracle and Go parity strategy

## Summary

`TestRoutingGroupSelector` is the richest existing oracle in the Java suite: ~25 test methods covering header-driven routing, MVEL rule evaluation, SQL parsing for catalog/schema/table-based routing, prepared-statement extraction, client-tag matching, file-watch hot-reload, and error fallbacks. Almost every assertion shape is suitable for direct lift into Go table tests; the blocker is the MVEL fixtures, which embed Java expression syntax that no Go interpreter can evaluate. Per java-qa's three-option framing in `routing-engine.java-qa.md`, the Go rewrite must pick one of: (a) **embed a Go expression engine** (CEL or `expr-lang/expr`) and port the YAML fixtures to the new syntax, (b) **run a sidecar MVEL evaluator** behind the external-router API, or (c) **define a structured non-Turing-complete rule schema** that covers the existing fixture corpus and rejects anything fancier. My recommendation: option (a) with **CEL specifically** — typed, mature, used heavily in Kubernetes/Envoy/Istio so battle-tested in adjacent routing contexts. java-qa notes that the MVEL security hardening (excluding `Process`/`Runtime`) hints that option (c) is closer to original design intent; that's a fair counterpoint and the final pick is the architect's.

## Key Findings

### Existing oracle inventory

- **Header-based selector** (`RoutingGroupSelector.byRoutingGroupHeader()`): trivial — returns the `X-Trino-Routing-Group` header value or null.
  - Cite: `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/RoutingGroupSelector.java:28-37`.
  - Oracle: `TestRoutingGroupSelector.testByRoutingGroupHeader` (`:82-100`).
  - Signal: returned `routingGroup` string equals header value or is null.

- **Rule-engine selector via four equivalent rule files** (`provideRoutingRuleConfigFiles`): four rule files (`routing_rules_atomic.yml`, `routing_rules_priorities.yml`, `routing_rules_if_statements.yml`, `routing_rules_state.yml`) all encode the same logic four different ways: "if `X-Trino-Source == 'airflow'` then `etl`; if also `X-Trino-Client-Tags contains 'label=special'` then `etl-special`."
  - Cite: `TestRoutingGroupSelector.java:72-80`, `:102-115`, `:309-343`.
  - Why four files: the Java suite uses them to verify four different rule-engine paradigms (atomic conditions, priority-ordered, branching `if` inside actions, stateful map between rules) all reach the same routing decisions.
  - Signal: `routingGroup` equals `"etl"` when source is airflow alone; `"etl-special"` when also tagged `label=special`; `null` when source isn't airflow.

- **Basic auth → user → routing group**: `TestRoutingGroupSelector.testGetUserFromBasicAuth` extracts username from `Authorization: Basic <base64(user:pw)>` and uses MVEL rule `request.getUser() == 'will'` to route to `will-group`.
  - Cite: `:117-135`.
  - Signal: routing-group string equals `"will-group"`.

- **SQL parsing-driven routing**: rules can examine parsed-query catalog/schema/table sets to route. Examples from `TestRoutingGroupSelector`:
  - `testTrinoQueryPropertiesQueryDetails` — query references three tables across two catalogs; rule matches any catalog containing `catx` and routes to `tbl-group` (`:138-156`).
  - `testTrinoQueryPropertiesCatalogSchemas` — routing based on resolved catalog+schema pairs including session defaults (`:159-177`).
  - `testTrinoQueryPropertiesSessionDefaults` — when no body, headers `X-Trino-Catalog` + `X-Trino-Schema` drive routing (`:180-196`).
  - `testTrinoQueryPropertiesQueryType` — `INSERT INTO foo SELECT 1` routes via query-type detection to `type-group` (`:198-215`).
  - `testTrinoQueryPropertiesResourceGroupQueryType` — DDL `CREATE TABLE ...` routes via resource-group query-type classification to `resource-group-type-group` (`:217-234`).
  - `testTrinoQueryPropertiesAlternateStatementFormat` — when `clientsUseV2Format=true`, body is a JSON envelope with `query` + `preparedStatements`; routing parses the JSON and uses the inner SQL (`:236-253`).
  - `testTrinoQueryPropertiesPreparedStatementInHeader` — `EXECUTE statementN` resolves to the statement body from URL-encoded `X-Trino-Prepared-Statement` header (`:255-279`).
  - Signal: routing-group string equality.

- **SQL parsing error fallback**: `testTrinoQueryPropertiesParsingError` — invalid SQL (`"SELECT * FROM table WHERE column = "`) makes parsing fail; rule engine receives a `TrinoQueryProperties` with `isQueryParsingSuccessful()==false` and routes to `"no-match"`. Also exposes the parse error via `getErrorMessage()` (Optional).
  - Cite: `:281-307`.
  - Signal: routing group `"no-match"` AND `TrinoQueryProperties.isQueryParsingSuccessful()` is false AND `getErrorMessage()` is non-empty.

- **Hot-reload via file change**: `testByRoutingRulesEngineFileChange` writes a rules file, creates a selector with 1ms refresh period, queries it (gets `etl`), overwrites the file, sleeps 2ms, queries again (gets `etl2`).
  - Cite: `:345-392`.
  - Signal: routing-group decision changes after file mtime advances and refresh window elapses.
  - **Flake risk**: `Thread.sleep(2 * refreshPeriod.toMillis())` is racy in CI under load. Go must NOT use sleep-based assertions here; needs a "reload-complete" signal or `Eventually(timeout)` polling.

- **Table-extraction oracle** (~30+ entries in `provideTableExtractionQueries`): SQL strings paired with the expected sets of catalogs, schemas, and qualified table names extracted from each statement.
  - Cite: `:394-499` and continues beyond (file is long).
  - Covers: `ALTER TABLE/SCHEMA/VIEW`, `CREATE TABLE/VIEW/SCHEMA/CATALOG/MATERIALIZED VIEW`, `DROP TABLE/SCHEMA/CATALOG`, `DESCRIBE`, `SHOW CREATE`, `SHOW SCHEMAS`, `SHOW TABLES`, `ALTER ... SET AUTHORIZATION`.
  - **This is the single most valuable artifact for Go testing.** It is a behavioral spec for the SQL parser's catalog/schema/table extraction. Every row should become a Go table-test entry.
  - **Blocker**: the underlying parser is `io.trino.sql.SqlParser` (trino-parser, JVM-only). For Go we need either (a) trino-parser-go (does not exist as of 2026-05) or (b) a focused Go parser that handles the subset of DDL/queries in this oracle. The oracle's 30+ entries are tractable; this is more like "write a focused Go parser" than "write a full SQL parser" — see `qa-gaps-and-risks.go-qa.md`.

- **External-router selector**: `RoutingGroupSelector.byRoutingExternal` posts request metadata to a configured URL and uses the returned `routingGroup` from a JSON response.
  - Cite: `RoutingGroupSelector.java:52-58`; test `TestExternalRoutingGroupSelector.java`.
  - Signal: routing-group string from external response is propagated.
  - This is the **easiest path to a 1:1 Go port**: no expression language needed, just a small HTTP client. We could even make the Go gateway support only header-based and external-API rule selection at first cut, deferring MVEL.

### Routing-decision protocol contract

- **Rule action contract**: rules write to a shared `result` map via `result.put(FileBasedRoutingGroupSelector.RESULTS_ROUTING_GROUP_KEY, "<group>")`. The selector inspects `result["routingGroup"]` after all rules run and returns it as `RoutingSelectorResponse.routingGroup`.
  - Cite: rule files in `src/test/resources/rules/*.yml`.
  - In Go: any rule DSL we choose must support the same "write-routing-decision" semantic. CEL with custom output bindings, `expr-lang/expr` with a typed output struct, or a typed Go DSL all work.

- **Rule-state passing**: `routing_rules_state.yml` initializes `state.put("triggeredRules", new HashSet())` in a `priority: 0` rule, then later rules check `state.get("triggeredRules").contains("airflow")`. This is genuinely stateful evaluation within a single request.
  - Cite: `src/test/resources/rules/routing_rules_state.yml`.
  - In Go: any DSL must expose a per-request mutable state map. Easy to add to most expression libraries.

- **Priority ordering**: rules with explicit `priority` execute in numeric order; higher-priority later rules override earlier `result` writes (`routing_rules_priorities.yml`).
  - In Go: must replicate.

- **External header injection**: a rule can set `result.put("externalHeaders", Map.of("key", "value"))`. These headers are wrapped into the forwarded request via `HeaderModifyingRequestWrapper` (`RoutingTargetHandler.java:114-151`).
  - Signal: backend-side `RecordedRequest.getHeader("key")` returns the injected value.
  - In Go: the rule DSL must allow returning a header map alongside the routing group.

### Default-routing-group fallback

If the selector returns an empty/null `routingGroup`, `RoutingTargetHandler` falls back to `haGatewayConfiguration.getRouting().getDefaultRoutingGroup()` (`RoutingTargetHandler.java:95-97`).
- Signal: when no rule matches, request still routes (to the default group's backend), not 502.
- Edge case to test: what if the default group has no healthy backends? `routingManager.provideBackendConfiguration(...)` behavior is the next seam — see `TestRoutingManagerNotFound.java`.

### Sticky routing via cookies

When `cookiesEnabled=true`, the `RoutingTargetHandler.getPreviousCluster` path checks request cookies whose name starts with `GatewayCookie.PREFIX` for one whose `matchesRoutingPath(request.getRequestURI())` is true and whose `backend` is non-empty; if found, that backend is used instead of running the rule engine.
- Cite: `RoutingTargetHandler.java:158-170`.
- Signal: subsequent request bearing a previously-emitted gateway cookie lands on the same backend the cookie was issued for.
- Existing test depth: integration tests in `TestGatewayHaMultipleBackend` exercise this end-to-end with `OkHttpClient` cookie jar.

## Behavior vs. Implementation Artifact

### MVEL as the rule language
- **Observed behavior:** Rules are MVEL 2 expressions evaluated by `org.mvel2.MVEL` against a request object.
- **Source of behavior:** `defensive-historical` — MVEL was a convenient JVM-embeddable scripting language at the time of the original gateway design.
- **Rationale:** Lets operators author rules in a familiar Java-like expression syntax without recompiling.
- **Go obligation:** `replicate-intent`, not `replicate-exactly`. There is no Go MVEL interpreter. Aligning with java-qa's three-option list (`routing-engine.java-qa.md` §"MVEL expression language for rules"): (a) embed a Go expression engine (CEL or `expr-lang/expr`) — my recommendation, with CEL as the specific instance; (b) sidecar MVEL evaluator behind the external-router API; (c) structured non-Turing-complete rule schema. See `qa-gaps-and-risks.go-qa.md` for parity-risk discussion.
- **Notes:** Whatever the choice, we LOSE the ability to use the existing MVEL rule files verbatim. We can however lift the *behavioral assertions* from `TestRoutingGroupSelector` (input request → expected routing group) verbatim by writing equivalent Go rule files for each Java fixture.

### Routing-rules YAML structure
- **Observed behavior:** YAML documents separated by `---`, each with `name`, `description`, optional `priority`, `condition`, `actions` (list of strings).
- **Source of behavior:** `defensive-historical` — easy to author and version-control.
- **Go obligation:** `replicate-intent`. Preserving YAML shape is friendly to operators; the only changes should be in the `condition`/`actions` expression language. Keep `name`/`description`/`priority` as-is.
- **Notes:** The expression-language change is a config-format breaking change for operators. The Architect/Java Analyst should document a migration story.

### `getUser()` short-circuit on MVEL request object
- **Observed behavior:** MVEL rules call `request.getUser()` directly to get the authenticated user.
- **Source of behavior:** `defensive-historical` — the `request` object is the actual `HttpServletRequest` and `getUser()` is a custom wrapper method.
- **Go obligation:** `replicate-intent`. The Go expression context should expose the same logical fields (user, headers, query properties, client tags) with method/property names of our choosing.

### Stateful rules (`state.put`/`state.get`)
- **Observed behavior:** Per-request mutable map shared across rule evaluations.
- **Source of behavior:** `gateway-design-intent` — lets rule authors compose decisions across rules without boolean explosion in conditions.
- **Go obligation:** `replicate-exactly` (semantics, not syntax). Expose a per-request `state` map in the Go rule context.

## Implications for Go Rewrite

- **Rule-engine choice is on the critical path.** I cannot write rule-engine parity tests until this is settled. Recommended decision criteria for @architect:
  - Must support: per-request mutable state, priority ordering, multi-action expressions, comparison and string-contains operators, conditional (`if/else`) inside actions.
  - Nice to have: type checking at rule-load time (CEL has this; MVEL does not — Go could gain a safety win here).
  - Easiest port: drop scripting entirely and require operators to compile in Go rule code. Acceptable for a small operator base, hostile for large. **Defer to @architect on team size assumption.**
- **The table-extraction oracle is portable as data even if the parser is not.** I propose: keep the test inputs verbatim, keep the expected catalog/schema/table sets verbatim, write a Go parser that handles exactly this subset of statements. If a future statement type is added to the oracle, we expand the Go parser. This is bounded scope.
- **The four-equivalent-rule-files trick is worth preserving in Go.** It catches whole classes of rule-engine bugs (priority sort wrong, state map not initialized, action interpreter doesn't support `if`) that single-rule-file tests miss.
- **Hot-reload tests need a determinism rewrite.** Expose a `chan struct{}` or callback from the file watcher; tests block on it instead of sleeping.
- **External-router selector is the lowest-risk first deliverable.** If we want a Go gateway shipping fast with limited scope, header-based routing + external HTTP-based routing covers a meaningful operator population without needing MVEL parity. MVEL parity becomes a v2 deliverable.

## Test Strategy Hooks

- **Test level:**
  - Unit: header-based selector, query-ID URL extraction (already cited), priority ordering of rules.
  - Unit (with parsed input): each row of `provideTableExtractionQueries` becomes a Go test case.
  - Integration: rule-engine end-to-end against a `httptest.Server` backend, asserting which backend received the request.
  - Differential (proposed): identical request → both Java and Go gateway → same routing group. Requires a Java gateway in a testcontainer; see `qa-gaps-and-risks.go-qa.md` for cost.
- **Fixtures required:** rule YAML files translated to whatever Go DSL we pick; query-string oracle table from `TestRoutingGroupSelector.provideTableExtractionQueries`; example invalid SQL strings for parse-failure fallback test.
- **Observable signals:** routing-group string equality (predominant), `TrinoQueryProperties.isQueryParsingSuccessful` boolean, parse error `Optional<String>` presence, backend-side recorded request bearing externally-injected headers, log substring `"Failed to parse routing rule"` (if Go rule loader logs that — not present in Java per my search).
- **Non-determinism risks:**
  - File-watch reload — banned `time.Sleep`; use signal channel or `Eventually` with deadline.
  - Concurrent rule evaluation against a shared `state` map — must hold per-request, not globally. If we accidentally share, `-race` will catch it.
  - YAML loading order on disk — if the rule file contains multiple documents, list order must be deterministic.

## Open Questions

- @architect: which rule-expression language for Go? CEL, `expr-lang/expr`, or compiled-in typed DSL? This decision blocks ~half of my routing-engine tests.
- @trino-expert: is `provideTableExtractionQueries` complete coverage of routing-relevant SQL forms, or are there statement types operators use today that aren't covered (e.g. `EXPLAIN`, `WITH ... INSERT`, `MERGE`)?
- @java-qa: please confirm the `result.put("externalHeaders", ...)` semantics — when a rule sets external headers, do they replace or merge with inbound headers? My reading of `HeaderModifyingRequestWrapper` is replace, but I want this confirmed before writing the Go test.
- ~~@qa-tech-lead: differential harness in scope?~~ **RESOLVED:** yes, nightly-gated with per-PR record/replay smoke. Design in `studies/both/test-infrastructure-needs.qa-tech-lead.md` item 4.

## Cross-references

- [[test-infrastructure-inventory.go-qa.md]] — tooling inventory.
- [[proxy-lifecycle-testable-seams.go-qa.md]] — proxy-side seams (routing target is seam 1).
- [[qa-gaps-and-risks.go-qa.md]] — MVEL and trino-parser entanglement, gaps in Java test coverage.
