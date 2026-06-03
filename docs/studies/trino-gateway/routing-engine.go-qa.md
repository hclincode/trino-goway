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
  - trino-gateway/test-infrastructure.go-qa.md
  - trino-gateway/proxy-request-lifecycle.go-qa.md
  - trino-gateway/test-gaps-and-risks.go-qa.md
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

- **Rule-engine selector via seven rule fixtures** (`provideRoutingRuleConfigFiles` + sibling test methods): the `src/test/resources/rules/` directory ships seven YAML fixtures, each exercising a distinct rule-engine capability. The first four are equivalents that encode the same logic four ways ("if `X-Trino-Source == 'airflow'` then `etl`; if also `X-Trino-Client-Tags contains 'label=special'` then `etl-special`"); the remaining three exercise capabilities not covered by the equivalence set:
  - `routing_rules_atomic.yml` — atomic conditions, one rule per outcome. Cite: `TestRoutingGroupSelector.java:103-115` via `provideRoutingRuleConfigFiles`. Purpose: baseline rule evaluation, no priority/state/branching.
  - `routing_rules_priorities.yml` — `priority`-ordered execution where a later high-priority rule overrides an earlier `result` write. Purpose: verifies priority sort + last-writer-wins semantics.
  - `routing_rules_if_statements.yml` — branching `if/else` inside the `actions` list. Purpose: verifies the action interpreter supports conditional bodies, not just expression statements.
  - `routing_rules_state.yml` — per-request mutable `state` map; a `priority: 0` rule initializes `state.put("triggeredRules", new HashSet())`, later rules check `state.get(...).contains(...)`. Purpose: verifies stateful evaluation within a single request.
  - `routing_rules_concurrent.yml` — single-rule fixture (`X-Trino-Source == "datagrip"` → `adhoc`) used by `testRoutingRuleEngineConcurrent` to fire many simultaneous evaluations against a shared selector. Purpose: race detection on the rule-evaluation path. **In Go: re-run this via `-race` + parallel `t.Parallel()` subtests; the fixture itself is trivial, the value is the concurrent driver.**
  - `routing_rules_trino_query_properties.yml` — multi-rule fixture exercising `trinoRequestUser`, `trinoQueryProperties.tablesContains`, `getSchemas/getCatalogs/getCatalogSchemas`, `getQueryType`, `getResourceGroupQueryType`. Purpose: drives the seven `testTrinoQueryProperties*` methods (`:138-279`). **This is the surface that depends on `trino-parser`; the rule expressions reference parser outputs.**
  - `routing_rules_update.yml` — fixture used by `testByRoutingRulesEngineFileChange` (`:345-392`) to seed the initial rule before the file-watch reload test overwrites it. Purpose: hot-reload oracle's "before" state.
  - Cite (driver): `TestRoutingGroupSelector.java:72-80` (the `provideRoutingRuleConfigFiles` source method only enumerates the four-equivalent set); the other three fixtures are referenced by name in their specific test methods.
  - Signal: `routingGroup` equals `"etl"` when source is airflow alone; `"etl-special"` when also tagged `label=special`; `null` when source isn't airflow. For the trino-query-properties fixture: `"will-group"`/`"tbl-group"`/`"catalog-schema-group"`/`"type-group"`/`"resource-group-type-group"` per the seven sub-method oracles below.
  - **MVEL parity caveat (whole-fixture-set):** the *inputs* (header sets, request bodies, SQL strings) and the *outputs* (expected routing-group strings) port verbatim to Go and form the seven-fixture-worth of behavioral oracle. The *rule expressions themselves* (the MVEL bodies in each YAML's `condition`/`actions` field) cannot port until the Go DSL is chosen — see the rule-language decision in `test-gaps-and-risks.go-qa.md` and the Implications section below. Tracking this split: write the Go fixtures as YAML stubs with the same `name`/`priority`/`description` skeletons and a `condition: TODO(go-dsl)` placeholder; populate `condition`/`actions` only once the architect's DSL choice lands.

- **Sibling corpora worth lifting into the same test package:** beyond the seven rule fixtures, two table-driven oracles in adjacent test classes belong to the same routing test surface and should port together so reviewers see the routing-decision oracle in one place:
  - **URL → query-ID extraction table** (`TestQueryIdCachingProxyHandler.testExtractQueryIdFromUrl`, `:38-71`): 15 URL-pattern cases covering `queued`/`scheduled`/`executing`/`partialCancel`, custom statement paths, `/v1/query/...`, `/ui/api/query/...{,/killed,/preempted}`, `/ui/troubleshooting?queryId=...`, `/ui/query.html?<queryId>`, `/login?redirect=...`, plus three negative cases. **Lift verbatim as a Go table-test; pure function, no rule engine involved.** This oracle is the URL-side companion to the routing-decision oracle and shares the same fixture-corpus shape.
  - **Header-mode routing table** (`TestRoutingGroupSelector.testByRoutingGroupHeader`, `:82-100`): two cases that drive the trivial header-based selector. Tiny corpus, but the negative case (header absent → `null`) is the only place that pins the "no header, no opinion" contract for the header selector. Lift as a 2-row Go table-test alongside the URL extraction table.
  - These two are NOT MVEL-dependent and can land in the Go suite immediately, before the rule-engine DSL decision.

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
  - **Blocker**: the underlying parser is `io.trino.sql.SqlParser` (trino-parser, JVM-only). For Go we need either (a) trino-parser-go (does not exist as of 2026-05) or (b) a focused Go parser that handles the subset of DDL/queries in this oracle. The oracle's 30+ entries are tractable; this is more like "write a focused Go parser" than "write a full SQL parser" — see `test-gaps-and-risks.go-qa.md`.

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

- **External header injection** (REPLACE, not merge — java-qa confirmed): a rule can set `result.put("externalHeaders", Map.of("key", "value"))`. These headers are wrapped into the forwarded request via `HeaderModifyingRequestWrapper` (`RoutingTargetHandler.java:114-151`). The wrapper's `getHeader(name)` and `getHeaders(name)` return ONLY the wrapper-supplied value when the name is in `customHeaders`, completely shadowing the inbound value — see `RoutingTargetHandler.java:125-141`. `getHeaderNames()` is the union (distinct) of inbound and wrapper-supplied names (`:143-150`), so wrapper-supplied names always appear once whether the client sent them or not. The wrapper is only applied when `routingDestination.externalHeaders().isEmpty()` is false — non-external selectors are a no-op for this path (`:101-105`).
  - Signal: backend-side `RecordedRequest.getHeader("key")` returns the wrapper value when the rule set `key`; returns the client's value when the rule did NOT set `key`.
  - **Multi-value flattening:** the wrapper's `customHeaders` is a `Map<String, String>` (single value per name). If a rule needs to set a multi-valued header (e.g. `X-Trino-Session` with multiple session properties), the rule must pre-concatenate values into one string; `getHeaders(name)` then returns a one-element enumeration. Multi-valued client headers under the same name are dropped wholesale, not merged. **Test in Go: backend sees exactly one value for any wrapper-set name.**
  - **Canonical Go test shape** (four-assertion table — assert all four in one test to lock the contract):
    1. External selector returns `{"X-Trino-User": "override", "X-Custom": "new"}` (override + new-key)
    2. Client sends `{"X-Trino-User": "original", "X-Other": "keep"}`
    3. Backend assertions: `X-Trino-User == "override"` (replaced, not merged), `X-Other == "keep"` (untouched), `X-Custom == "new"` (added), and `getHeaderNames()`-equivalent (Go: `len(req.Header[name])`) for `X-Custom` is 1 (no duplicate listing).
    4. **Multi-value flattening probe:** rule sets `{"X-Trino-Session": "a=1,b=2"}`; client sends `X-Trino-Session: x=9` AND `X-Trino-Session: y=8` (two values). Backend sees exactly one `X-Trino-Session` value equal to `"a=1,b=2"`; the client's two values are gone.
  - In Go: the rule DSL must allow returning a header map alongside the routing group. The wrapper-equivalent (whether it's a `RoundTripper` middleware or a `http.Header` mutation step) must implement REPLACE-not-merge semantics and accept a `map[string]string` (single value per key) to mirror the Java contract bit-for-bit.

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
- **Go obligation:** `replicate-intent`, not `replicate-exactly`. There is no Go MVEL interpreter. Aligning with java-qa's three-option list (`routing-engine.java-qa.md` §"MVEL expression language for rules"): (a) embed a Go expression engine (CEL or `expr-lang/expr`) — my recommendation, with CEL as the specific instance; (b) sidecar MVEL evaluator behind the external-router API; (c) structured non-Turing-complete rule schema. See `test-gaps-and-risks.go-qa.md` for parity-risk discussion.
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
  - Differential (proposed): identical request → both Java and Go gateway → same routing group. Requires a Java gateway in a testcontainer; see `test-gaps-and-risks.go-qa.md` for cost.
- **Fixtures required:** rule YAML files translated to whatever Go DSL we pick; query-string oracle table from `TestRoutingGroupSelector.provideTableExtractionQueries`; example invalid SQL strings for parse-failure fallback test.
- **Observable signals:** routing-group string equality (predominant), `TrinoQueryProperties.isQueryParsingSuccessful` boolean, parse error `Optional<String>` presence, backend-side recorded request bearing externally-injected headers, log substring `"Failed to parse routing rule"` (if Go rule loader logs that — not present in Java per my search).
- **Non-determinism risks:**
  - File-watch reload — banned `time.Sleep`; use signal channel or `Eventually` with deadline.
  - Concurrent rule evaluation against a shared `state` map — must hold per-request, not globally. If we accidentally share, `-race` will catch it.
  - YAML loading order on disk — if the rule file contains multiple documents, list order must be deterministic.

## Open Questions

- @architect: which rule-expression language for Go? CEL, `expr-lang/expr`, or compiled-in typed DSL? This decision blocks ~half of my routing-engine tests.
- @trino-expert: is `provideTableExtractionQueries` complete coverage of routing-relevant SQL forms, or are there statement types operators use today that aren't covered (e.g. `EXPLAIN`, `WITH ... INSERT`, `MERGE`)?
- ~~@java-qa: please confirm the `result.put("externalHeaders", ...)` semantics — when a rule sets external headers, do they replace or merge with inbound headers?~~ **RESOLVED by java-qa:** REPLACE, not merge. `HeaderModifyingRequestWrapper.getHeader/getHeaders` returns wrapper-supplied values only when the name is in the custom map, shadowing inbound (`RoutingTargetHandler.java:114-151`). `getHeaderNames` is union-distinct. Wrapper applies only when `externalHeaders` non-empty (`:101-105`). Multi-value collapses to single value (the map is `Map<String, String>`). Canonical four-assertion Go test shape locked in the Routing-decision-protocol-contract subsection above.
- ~~@qa-tech-lead: differential harness in scope?~~ **RESOLVED:** yes, nightly-gated with per-PR record/replay smoke. Design in `studies/both/test-infrastructure-needs.qa-tech-lead.md` item 4.

## Cross-references

- [[test-infrastructure.go-qa.md]] — tooling inventory.
- [[proxy-request-lifecycle.go-qa.md]] — proxy-side seams (routing target is seam 1).
- [[test-gaps-and-risks.go-qa.md]] — MVEL and trino-parser entanglement, gaps in Java test coverage.
