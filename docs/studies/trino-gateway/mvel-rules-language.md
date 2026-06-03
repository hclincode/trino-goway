---
title: MVEL rules language — contract surface for routing rules
author: java-analyst
role: Java Analyst
component: trino-gateway
topics: [routing-engine, cross-cutting]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to: [architecture-overview.md, jvm-dependencies-inventory.md, sql-parsing-for-routing.md]
---

# MVEL rules language — contract surface for routing rules

## Summary

The file-based routing-rule engine accepts a YAML file containing multiple "rule" documents, each of which carries a `condition` and one or more `actions` expressed as **MVEL** expression strings. MVEL is a JVM-only embedded expression language; there is no Go port. This file extracts the exact contract operators write against — the data the rule context exposes, the language features rules use, and how rules compose — so the Architect can choose a replacement (CEL, expr-lang, or scoping rules out of v1) with full visibility into what would break.

## Key Findings

### Rule file shape

A rules file is a YAML stream of multiple documents (separated by `---`). Each document is one rule:

```yaml
---
name: "airflow"
description: "if query from airflow, route to etl group"
priority: 0                       # integer, default 0; higher priority evaluated later (sort-asc applied)
condition: |
  request.getHeader("X-Trino-Source") == "airflow"
actions:
  - |
    result.put("routingGroup", "etl")
```

Fields on a rule (Jackson-bound in `MVELRoutingRule.java:44-61`):
- `name` (string, **required**)
- `description` (string, optional, defaults to empty)
- `priority` (integer, optional, defaults to 0)
- `condition` (string — MVEL expression returning boolean, **required**)
- `actions` (array of strings — MVEL expressions, **required**)

The file is loaded via `FileBasedRoutingGroupSelector.readRulesFromPath(...)` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/FileBasedRoutingGroupSelector.java:83-99`), sorted by natural order (priority ascending — `MVELRoutingRule.compareTo` compares `priority` ints, `MVELRoutingRule.java:101-107`), and re-read on a configurable refresh period (`memoizeWithExpiration` per `FileBasedRoutingGroupSelector.java:55`).

### Evaluation model

Per request (`FileBasedRoutingGroupSelector.findRoutingDestination`, `FileBasedRoutingGroupSelector.java:58-81`):

1. Build a `data` map (read-only inputs) and an empty `state` map (mutable across rules in this request).
2. Build an empty `result` map (the rule's output channel).
3. For every rule in sorted order:
   - Evaluate `condition` in a context where `data` keys are top-level variables and `state` is bound under the name `state`. If true:
   - Evaluate each `action` in turn, with the same variables plus `result` bound.
4. After all rules, return `result.get("routingGroup")` as the selected routing group (the constant `RESULTS_ROUTING_GROUP_KEY = "routingGroup"`, `FileBasedRoutingGroupSelector.java:44`).

Important semantics that follow:
- **All rules run on every request.** There is no "stop on first match" — later rules can overwrite earlier rules' `result.put("routingGroup", ...)` writes. Operators rely on `priority` to control which rule wins.
- **`state` is per-request scratch space**, used to communicate between rules in the same request (see the `routing_rules_state.yml` test fixture — earlier rule `state.put("triggeredRules", new HashSet())`, later rule checks `state.get("triggeredRules").contains("airflow")`).
- **Sort is ascending by priority.** Looking at the test fixtures, operators use small ints with the convention that the "default fallback" rule has lowest priority (`priority: -1` in `routing_rules_trino_query_properties.yml:67`) so it runs first and gets overwritten by more-specific rules.

### Rule context — the `data` map

This is the operator-facing API. Keys come from `FileBasedRoutingGroupSelector.java:64-72`.

Always present:
- **`request`** — the `HttpServletRequest`. Operators commonly call `request.getHeader("X-Trino-Source")`, `request.getHeader("X-Trino-Client-Tags")`, and similar.

Present only when `requestAnalyzerConfig.analyzeRequest == true`:
- **`trinoQueryProperties`** — the parsed SQL view (see `[[sql-parsing-for-routing.md]]` for the full member set). Operators call:
  - `trinoQueryProperties.tablesContains("cat.schema.tbl")` — qualified-name table check
  - `trinoQueryProperties.getSchemas()` — `Set<String>`
  - `trinoQueryProperties.getCatalogs()` — `Set<String>`
  - `trinoQueryProperties.getCatalogSchemas()` — `Set<String>` of `"catalog.schema"` pairs
  - `trinoQueryProperties.getQueryType()` — string like `"SELECT"`, `"INSERT"`, `"CREATE_TABLE"`
  - `trinoQueryProperties.getResourceGroupQueryType()` — Trino's coarser classification, e.g. `"DATA_DEFINITION"`
  - `trinoQueryProperties.getDefaultCatalog()` / `getDefaultSchema()` — `Optional<String>`
- **`trinoRequestUser`** — the parsed user. Operators call `trinoRequestUser.userExistsAndEquals("will")`.

The `state` and `result` maps are bound separately (not in `data`).

### MVEL language features actually used by operators

Sampled from `trino-gateway/gateway-ha/src/test/resources/rules/*.yml`. This is the contract surface a replacement language must cover:

- **Boolean equality / inequality:** `==`, `!=` (string equality is value-equality, unlike Java's `==`)
- **Boolean operators:** `&&`, `||`, `!`
- **Null check:** `x == null`
- **String `contains`:** `request.getHeader("X-Trino-Client-Tags") contains "label=special"` — MVEL's `contains` is an *operator*, not a method call. It works on strings and collections (and via java.lang autoboxing on whatever else).
- **Method calls on Java objects:** `.getHeader(...)`, `.contains(...)`, `.toLowerCase`, `.equals(...)`, `.isEmpty()`, `.getSchemas().contains(...)`. **MVEL allows omitting parentheses on no-arg methods** — note `.toLowerCase` without `()` in `routing_rules_trino_query_properties.yml:31` and `.getCatalogSchemas` without `()` at line 22.
- **`Optional.of(...)` usage:** `java.util.Optional.of("other_catalog")` — full FQCN allowed (matches MVEL's `parserContext.addPackageImport("java.util")`, `MVELRoutingRule.java:73`).
- **`new HashSet()` / `new HashMap()`:** explicit instantiation, supported because `MVELRoutingRule` imports `java.util` (`MVELRoutingRule.java:73`). **MVEL does NOT support type parameters** — a comment in `routing_rules_state.yml:9-10` notes this: "using one will result in a syntax error. Effectively this results in all objects of classes that support parametrization being declared as ParametrizedClass<Object>".
- **`if (cond) { ... } else { ... }`:** imperative blocks inside an action string (see `routing_rules_if_statements.yml:6-11`). MVEL supports statement-block actions, not just expressions.
- **Map operations:** `result.put(key, value)`, `state.get(key)`, `state.get("triggeredRules").add(...)`.
- **Constants referenced via Java type:** `FileBasedRoutingGroupSelector.RESULTS_ROUTING_GROUP_KEY` is used in many rules (`routing_rules_trino_query_properties.yml:6` etc.) — this works because `MVELRoutingRule.initializeParserContext` explicitly imports `FileBasedRoutingGroupSelector` into the parser context (`MVELRoutingRule.java:91`). **Some test rules use the literal string `"routingGroup"` instead** (`routing_rules_update.yml:5`, `routing_rules_state.yml:20`) — both forms are valid because the constant *is* `"routingGroup"`.

### MVEL features explicitly NOT supported (or deliberately restricted)

`MVELRoutingRule.initializeParserContext` allow-lists a subset of `java.lang`, **excluding `Process` and `Runtime`** (`MVELRoutingRule.java:75-92`). The comment is specific: "Members of java.lang, excluding potential security hazards such as Process and Runtime." Operators cannot shell out from rules.

Beyond the explicit allow-list (`Boolean`, `Byte`, `Character`, `Double`, `Enum`, `Exception`, `Float`, `Integer`, `Long`, `Math`, `Short`, `StrictMath`, `String`, `StringBuffer`, `StringBuilder`, plus `java.util` via package import, plus `FileBasedRoutingGroupSelector`), rules cannot reference other classes by short name (FQCN still works, e.g., `java.util.Optional`).

There is no defensive limit on rule execution time, no recursion bound, no per-rule memory budget. A pathological rule can hang the request thread.

### What rules can mutate

- `result` — the output channel. Rules write `routingGroup` here. Operators *also* write other keys to `result` (e.g., the test fixture `routing_rules_trino_query_properties.yml:40` writes the raw string key `"routingGroup"`), but only `result.get("routingGroup")` is read back by `FileBasedRoutingGroupSelector`. Other keys are silently discarded.
- `state` — scratch across rules in this request.
- They can also call methods on `request`, `trinoQueryProperties`, `trinoRequestUser` that have side effects (the gateway's own classes only expose read-only-ish methods; the `HttpServletRequest` could in principle be mutated but no rule in the test fixtures does so).

There is no path for a rule to mutate cross-request state or persistent state. Good — this is a hard constraint a replacement should preserve.

### `actions` is a list, not a single block

`actions` is `List<String>`, with each element compiled independently and executed sequentially (`MVELRoutingRule.java:60, 125`). This is functionally identical to a single action with semicolon-separated statements, but operators frequently use the list form for readability (one action per side effect).

### Refresh

The selector wraps rule loading in `Suppliers.memoizeWithExpiration(...)` with `rulesRefreshPeriod` from `RoutingRulesConfiguration` (`FileBasedRoutingGroupSelector.java:55`). After expiry, the next request triggers a reload from disk. There is no signal-driven reload, no file-watch.

### Failure modes

- Rule file fails to parse at load: `RuntimeException` thrown from `readRulesFromPath` (`FileBasedRoutingGroupSelector.java:96-98`). Because `memoizeWithExpiration` propagates the exception, every request thereafter throws until the file is fixed.
- Individual MVEL expression fails to compile at load: `MVELRoutingRule` constructor throws (via `compileExpression(...)`) — same effect.
- Individual MVEL expression fails to execute at request time: `executeExpression` throws an MVEL runtime exception. This is **not caught** in `FileBasedRoutingGroupSelector.findRoutingDestination` — it propagates up to the request handler. Effect on the client request needs tracing in `[[routing-engine.md]]`.
- Module-level boot fallback: at module-construction time, if `byRoutingRulesEngine(...)` throws, the gateway falls back to `byRoutingGroupHeader()` (header-only) for the rest of the process lifetime (`HaGatewayProviderModule.java:158-172`). This is a **boot-time** fallback only — runtime failures do not re-fall-back.

### The `EXTERNAL` rules selector is an alternative

`RoutingRulesConfiguration.rulesType` can be `FILE` or `EXTERNAL` (`HaGatewayProviderModule.java:159-168`). EXTERNAL POSTs the request context to an operator-supplied HTTP endpoint that returns the chosen routing group. EXTERNAL avoids MVEL entirely and is the obvious bridge if the team wants to defer the MVEL replacement decision. Covered in `[[routing-engine.md]]`.

## Behavior vs. Implementation Artifact

### MVEL parentheses-optional method calls
- **Observed behavior:** Test rules use `.toLowerCase` (no parens) and `.getCatalogSchemas` (no parens) interchangeably with `.getSchemas()` (with parens). Both evaluate fine.
- **Source of behavior:** `jvm-artifact` — this is MVEL's "property accessor" syntactic sugar for no-arg methods.
- **Rationale:** Convenience for property-bean style access.
- **Go obligation:** `replicate-intent` (the *capability* — calling no-arg accessors) but the **syntax** can be language-dependent. A replacement using CEL would write `obj.getCatalogSchemas()` always; expr-lang would write `obj.getCatalogSchemas()` or `obj.getCatalogSchemas`. Operators need a migration note either way.
- **Notes:** This is a small footgun for a mechanical converter — both forms appear in production-style rules.

### `result` map keys other than `routingGroup` are silently ignored
- **Observed behavior:** A rule can `result.put("foo", "bar")` and it has no effect because only `result.get("routingGroup")` is read.
- **Source of behavior:** `defensive-historical` — the `result` map is overgeneral for what is currently a single-output channel.
- **Rationale:** Possibly a forward-compatibility hook (future "set this routing group AND this header AND this max-runtime").
- **Go obligation:** `defer-to-expert`. **Open question for `@trino-expert`:** does any documented rule write other keys to `result`, or does the test of the codebase rely on `result` carrying more than `routingGroup`? If single-output, the Go side can simplify `result` to a single `string` return value.

### Allow-listed `java.lang` classes; `Process`/`Runtime` excluded
- **Observed behavior:** Rules can use `String`, `Integer`, `Math`, etc., but not `Process` or `Runtime`.
- **Source of behavior:** `gateway-design-intent` — explicit security hardening.
- **Rationale:** Prevent rule files (which may be operator-edited at runtime) from shelling out.
- **Go obligation:** `replicate-intent`. The Go expression language must **not** allow arbitrary code execution from rule files. CEL is the safest choice (sandboxed by design); expr-lang requires explicit allow-listing of host functions.
- **Notes:** This is a real security commitment, not incidental.

### All rules always run; later rules can overwrite earlier rules
- **Observed behavior:** No short-circuit. Use `priority` to order.
- **Source of behavior:** `gateway-design-intent`.
- **Rationale:** Lets operators compose rules — "set default; then override for specific cases."
- **Go obligation:** `replicate-exactly`. The semantics matter; many operator rule files rely on this.
- **Notes:** If a replacement language has a different default (e.g., first-match-wins), the converter or runtime must adapt.

### `state` is mutable across rules in one request
- **Observed behavior:** Rule A writes `state.put(...)`, rule B reads `state.get(...)`. Used in test fixture `routing_rules_state.yml` to dedupe "did airflow rule already fire?"
- **Source of behavior:** `gateway-design-intent`.
- **Rationale:** Enables multi-stage rules without re-evaluating expensive conditions.
- **Go obligation:** `replicate-intent`. Whatever replacement language is chosen needs an equivalent per-request scratch space.

### Per-request reload semantics
- **Observed behavior:** Rules cached for `rulesRefreshPeriod`, then reloaded on next request.
- **Source of behavior:** `gateway-design-intent` — operators want to edit rules without restarting.
- **Go obligation:** `replicate-intent`. The Go side can use `time.Ticker` or `fsnotify` — implementation detail.

## Implications for Go Rewrite

- **The MVEL contract is bounded but real.** It is not a vague "Java expression language"; it is a specific set of features (boolean logic, string `contains`, method calls on a fixed set of objects, mutable `result` and `state` maps, no-arg method shorthand, list-of-actions). A Go replacement covering all of these is feasible.
- **CEL (`google/cel-go`) is the strongest candidate.** It is sandboxed by design, has Google-grade implementation quality, supports custom function libraries (so `trinoQueryProperties.tablesContains(...)` can be exposed), and has clear error messages. The main downside is operators must learn CEL syntax — but CEL is closer to MVEL than most alternatives (booleans, strings, dot access).
- **`expr-lang/expr` is the second candidate.** More dynamic, supports method calls on Go structs naturally, optional Pratt-style parentheses, and an `if/else` statement. Closer to MVEL's feel, but its sandbox guarantees are weaker (depends on what host functions are exposed).
- **The replacement decision should be made by the Architect after weighing operator migration cost vs. runtime safety.** This file is the contract input; the choice is theirs.
- **`actions: [str, str, ...]` translates cleanly to a list-of-expressions.** Or to a single statement-block expression. Architect's choice.
- **A mechanical converter is feasible for ~80% of operator rules.** The pattern `request.getHeader("X")` → CEL `request.headers["X"]` is one find/replace. `trinoQueryProperties.tablesContains(...)` → `trinoQueryProperties.tables.exists(t, t == ...)` is templated. The bits that need human attention: no-arg method shorthand, `new HashSet()`, `if/else` action blocks, raw `Optional` usage.
- **EXTERNAL rules selector is an out.** If the team wants to defer the MVEL decision, ship v1 with only header-based and external-HTTP rule modes. Operators with file-based rules either keep running the Java gateway, or write an HTTP rule service in their language of choice. This is the lowest-risk v1 scope.
- **Don't preserve `result` as a free-form map.** If the answer to the open question is "only `routingGroup` is read", simplify the contract to a single return value in v1 — fewer ways for rules to misbehave.

## Test Strategy Hooks

- **Test level:** unit (rule engine in isolation) + integration (rules + request context end-to-end). The existing `routing_rules_*.yml` fixtures in `gateway-ha/src/test/resources/rules/` are an excellent oracle: each fixture asserts a specific input → routing group decision. The Go QA team should consider porting these as the differential test set. See paired QA study (planned).
- **Fixtures required:** rule YAML files, a request-builder fixture exposing `X-Trino-*` headers, optional SQL bodies (for `analyzeRequest=true` paths), the `trinoRequestUser` shape.
- **Observable signals:** the returned routing group string. Optionally rule-evaluation-error logs/metrics (need to verify what gets logged).
- **Non-determinism risks:** rule refresh timing — tests must not depend on real wall-clock for the memoize-with-expiration. Inject a clock.

## Open Questions

- **`@trino-expert`:** Are file-based MVEL rules documented as part of the public trino-gateway interface? If yes, replacement is a documented breaking change. If no (e.g., always documented as "subject to change"), the Architect has more freedom.
- **`@trino-expert`:** Does any documented rule write keys other than `routingGroup` to `result`? Or is the multi-key shape an unused future-proofing?
- **`@trino-expert`:** Has there ever been a security review of MVEL's allow-list (e.g., is there a way to escape it via reflection in `java.lang.Class`)? Not blocking, but informs how aggressive the Go-side sandbox must be.
- **`@architect`:** Prefer CEL, expr-lang, or "scope MVEL out of v1"? Each implies different operator migration story.
- **`@architect`:** Whose mechanical converter — ship one with v1 release notes, or punt to operators?

## Cross-references

- `[[architecture-overview.md]]`
- `[[jvm-dependencies-inventory.md]]`
- `[[sql-parsing-for-routing.md]]` — defines `trinoQueryProperties.*` members
- `[[routing-engine.md]]` — wider context for how the selector slots into routing
- `[[configuration-model.md]]` — the `routingRules:` config section
