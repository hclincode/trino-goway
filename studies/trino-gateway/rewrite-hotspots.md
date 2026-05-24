---
title: Rewrite hotspots — areas where the Go port lacks a clean library
author: architect
role: Architect / Tech Lead
component: trino-gateway
topics:
  - routing-engine
  - query-classification
  - auth
  - cross-cutting
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/library-landscape-go-mapping.md
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/component-build-order.architect.md
---

# Rewrite hotspots — areas where the Go port lacks a clean library

## Summary

Three pieces of the Java gateway have no drop-in Go equivalent and will dominate the rewrite cost: (1) the MVEL expression engine that powers file-based routing rules; (2) Trino's ANTLR-generated SQL parser used to extract catalogs/schemas/tables/queryType from request bodies for routing decisions; (3) the Nimbus OAuth2/OIDC SDK that backs LDAP/OAuth/OIDC authentication flows. A fourth, smaller hotspot is JMX-based cluster monitoring, which has no Go counterpart but is replaceable with a sibling probe transport. The viability of the entire Go rewrite hinges on how we handle MVEL and trino-parser, and a satisfactory answer to each requires a product decision, not just an engineering one.

## Key Findings

### Hotspot 1: MVEL rules engine

- **What it is:** MVEL is a Java-syntax expression compiler/evaluator. Routing rules are YAML documents with a `condition:` (a boolean MVEL expression) and `actions:` (MVEL statements that mutate a `result` map). The condition has access to `request`, `trinoQueryProperties`, `trinoRequestUser`, and a per-rule-set `state` map. See `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/MVELRoutingRule.java:34-126`.
- **What's exposed to expressions:** all of `java.util`, plus a curated allowlist of `java.lang` classes (`Boolean`, `Integer`, `String`, `Math`, etc.), explicitly excluding `Process` and `Runtime` (`MVELRoutingRule.java:71-92`). Also `FileBasedRoutingGroupSelector`. Conditions and actions are pre-compiled at rule-load time (`MVELRoutingRule.java:57-69`).
- **Why this is hard in Go:** MVEL is a *Java* expression language. Rules in the wild contain Java idioms — method chaining, `.contains()`, `.toLowerCase()`, `for (...)` loops, `new HashMap()`, `Math.random()`, regex via Java's `Pattern.matches`. Porting a corpus of rules requires either (a) replicating the *semantics* of those operations in a Go expression engine, or (b) requiring all operators to rewrite their rules in a Go-native DSL.
- **Candidate Go DSLs:**
  - `expr-lang/expr` — actively maintained, Go-native, type-checked, has visitor/AST, performant. Different syntax from MVEL (`x in list`, `len(x)`, no Java methods). Most idiomatic choice.
  - `tidwall/gjson` + plain Go config — no DSL, rules become Go code or JSON match expressions. Loses the operator-facing expressiveness that MVEL gives.
  - `google/cel-go` — Google's expression language. Standardised, used in Envoy, has type checker. Also different syntax.
- **Recommended path:**
  - **Do not attempt to embed a JVM or a transpiler.** GraalVM polyglot or a JS shim with QuickJS are both poor fits (deployment, perf, security review).
  - Use `expr-lang/expr` and accept that all rules must be rewritten. Provide a translation cookbook in docs and a small `mvel2expr` heuristic tool that handles common patterns (Java method-call → `expr` builtin where possible).
  - The data context (`request`, `trinoQueryProperties`, `trinoRequestUser`, `state`) maps cleanly — these are just structs/maps exposed to the evaluator.
- **Risk:** operators with sophisticated rule sets will resist migration. A reasonable mitigation is to keep the Java gateway running in parallel for the routing-decision tier (via a stage-1 "external routing rules" mode that calls out to a Java sidecar) during transition. The Java code already supports `byRoutingExternal` (REST callout) — we can use that as a fallback transport.

### Hotspot 2: Trino SQL parser for query classification

- **What it is:** When the rules engine has `analyzeRequest: true`, the gateway parses incoming SQL with Trino's own ANTLR-generated parser, walks the AST, and extracts: `queryType` (e.g. `Select`, `CreateTable`), `resourceGroupQueryType`, the set of fully-qualified `tables`, `catalogs`, `schemas`, `catalogSchemas`, and (for `CALL system.runtime.kill_query`) an extracted `queryId`. See `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoQueryProperties.java:177-512`.
- **Why this is hard in Go:** the parser is generated from Trino's ANTLR4 grammar file (`trino/core/trino-grammar/src/main/antlr4/io/trino/grammar/sql/SqlBase.g4`). The grammar evolves between Trino versions (functions, syntax, statement types). Trino does not publish a stand-alone Go parser, and ANTLR4's Go runtime is well-supported but the *grammar file* lives in the Trino repo and is co-evolved with the Java AST classes.
- **Candidate Go paths:**
  - **Regenerate from the grammar via ANTLR4 Go target.** Run `antlr4 -Dlanguage=Go SqlBase.g4` against the trino/ submodule's grammar file, vendor the output. The grammar file itself is `Apache-2.0`-licensed alongside the rest of Trino. The Go AST visitor would need to be hand-written to mirror the visitor patterns in `TrinoQueryProperties`.
  - **Use `trinodb/trino-go-client`'s parser** — there isn't one. The Go client does not parse SQL; it just submits it.
  - **Use `xwb1989/sqlparser` or `pingcap/tidb-parser`** — both parse a different SQL dialect. Wrong tool.
  - **Re-implement only the parts the gateway needs.** The visitor in `TrinoQueryProperties.visitNode` handles ~30 statement types. A minimal Go extractor could regex out the leading keyword for `queryType` and use a hand-rolled "find identifiers after `FROM`/`INSERT INTO`/`UPDATE`" scanner. This loses fidelity on `WITH` queries, prepared statements, qualifying defaults, etc.
- **Recommended path:**
  - **Regenerate via ANTLR4 Go target, pinned to the same Trino grammar version the gateway is pinned to (currently 481).** Treat the generated parser as a vendored dependency that we re-roll when the gateway bumps trino version.
  - Hand-write the visitor to mirror `TrinoQueryProperties.visitNode` (`TrinoQueryProperties.java:352-452`) — it is large but mechanical.
  - Make `analyzeRequest` opt-in, as it is in Java. Many operators do not use SQL-aware routing rules; for them the parser is dead weight.
- **Risk:** the grammar is large (~3000 lines of ANTLR). Generation produces megabytes of Go code. Maintenance burden = re-running codegen on every trino version bump, plus visitor updates when new statement kinds appear. Acceptable but not trivial.

### Hotspot 3: OAuth2 / OIDC

- **What it is:** the gateway implements OIDC code flow for the management UI (`LbOAuthManager`, `OidcCookie`), JWT/JWKS verification for API auth, plus LDAP and basic-form auth. Uses `com.nimbusds:oauth2-oidc-sdk:11.37.1` for discovery, code exchange, state/nonce handling, userinfo, PKCE.
- **Why this is hard in Go:** `coreos/go-oidc` covers the common case — discovery, ID-token verification, userinfo — but has narrower coverage of OIDC edge cases (response modes, request-object signing, encrypted ID tokens, federated identities). Nimbus's SDK is comprehensive; go-oidc plus go-jose is comprehensive *enough* for typical deployments.
- **Recommended path:** `coreos/go-oidc/v3` for the OIDC bits, `golang-jwt/jwt/v5` for direct JWT, `go-jose/go-jose/v4` for any JOSE primitives that don't fit the higher-level libs, and explicit code for state/nonce/PKCE. Acceptable but not turnkey. Plan to backfill any specific feature gaps the gateway's existing OIDC tests exercise.
- **Risk:** moderate. Most production OIDC providers (Okta, Auth0, Google, AzureAD, Keycloak) work fine with go-oidc. Issues tend to be specific to enterprise-IdP edge cases.

### Hotspot 4 (lesser): JMX-based cluster monitoring

- **What it is:** one of the cluster-stats probe transports is `JMX` — the gateway hits the backend coordinator's JMX-over-HTTP endpoint and extracts metrics by MBean name. See `ClusterStatsJmxMonitor`.
- **Why this is hard in Go:** there is no Go JMX client (JMX is a JVM-specific protocol). Even Airlift exposes it via an HTTP shim (`io.airlift:jmx-http`).
- **Recommended path:** Trino exposes the same metrics via its `INFO_API` (`/v1/info`) and via OpenMetrics. Default the Go gateway to `INFO_API`. Document `JMX` as unsupported in the Go port unless an operator demands it (in which case they can run a sidecar that scrapes JMX and exposes the same shape to the Go gateway via a custom HTTP transport). Likely no operator does — confirm with `@trino-expert`.

## Behavior vs. Implementation Artifact

### MVEL conditions can mutate state across rules
- **Observed behavior:** `evaluateAction` writes into a shared `state` map that is available to subsequent rules in the same evaluation pass (`FileBasedRoutingGroupSelector.java:60-79`, `MVELRoutingRule.java:119-126`). This means rules can carry forward computation between each other.
- **Source of behavior:** `gateway-design-intent`. The feature was added explicitly to support pre-compute-once, branch-many rule shapes.
- **Go obligation:** `replicate-exactly`. Whichever Go DSL we pick must give rules a writable shared `state` map within a single request evaluation. `expr-lang/expr` supports this via the environment map.
- **Notes:** Confirm in [[routing-engine-and-rule-evaluation]] (java-analyst) whether any in-the-wild rules rely on this. If not, we can simplify.

### Prepared statement headers can be zstd-compressed and base64-encoded
- **Observed behavior:** `TrinoQueryProperties.decodePreparedStatementFromHeader` detects a `$zstd:` prefix on `X-Trino-Prepared-Statement` values and decompresses them via `io.airlift:aircompressor-v3` (`TrinoQueryProperties.java:336-350`).
- **Source of behavior:** `protocol-required`. Trino added this in v447 for very large prepared statements that exceeded HTTP header size limits.
- **Go obligation:** `replicate-exactly`. Use `klauspost/compress/zstd` and `encoding/base64` (URL-safe variant — `RawURLEncoding`). Verify against trino's `PreparedStatementEncoder` source.

### MVEL imports `FileBasedRoutingGroupSelector` class
- **Observed behavior:** `MVELRoutingRule.initializeParserContext` imports the `FileBasedRoutingGroupSelector` class into the rules engine (`MVELRoutingRule.java:91`), so rules can reference `FileBasedRoutingGroupSelector.RESULTS_ROUTING_GROUP_KEY` (the literal string `"routingGroup"`).
- **Source of behavior:** `gateway-design-intent`. Avoids hardcoding the key string in user-authored rules.
- **Go obligation:** `replicate-intent`. Expose a constant (`RESULTS_ROUTING_GROUP_KEY = "routingGroup"`) in the Go DSL's environment so rules can reference it symbolically. The exact name should be matched for migration ergonomics.

## Implications for Go Rewrite

- **Library:**
  - MVEL → `expr-lang/expr` (recommended) or `google/cel-go` (alternative)
  - Trino parser → ANTLR4 Go target, regenerated from the trino submodule's `SqlBase.g4`
  - OIDC → `coreos/go-oidc/v3` + `golang-jwt/jwt/v5` + `go-jose/go-jose/v4`
  - JMX → drop; document `INFO_API` as the default and document `JMX` as unsupported
- **Interface:**
  - `RulesEvaluator interface { Evaluate(req RoutingInput) (RoutingDecision, error) }` — abstracts away whichever expression engine we pick
  - `QueryAnalyzer interface { Analyze(body []byte, defaultCatalog, defaultSchema string) (TrinoQueryProperties, error) }` — abstracts away the SQL parser
  - Both interfaces are mockable, which lets the proxy core be built without picking the engine first (see [[component-build-order.architect]])
- **Concurrency:**
  - `expr-lang/expr` programs are pre-compiled and the compiled form is goroutine-safe (`vm.Run` takes the environment per call). Cache compiled programs at rule-load time.
  - ANTLR4 Go parsers are NOT thread-safe at the parser-instance level. Create a parser per request, or pool them.

## Test Strategy Hooks

- See paired QA studies: [[routing-engine-behaviors-and-existing-test-oracle]], [[gaps-and-high-risk-untested-behaviors]].
- Hotspot-specific test concerns:
  - **MVEL → expr translation:** maintain a "golden rules" file with N representative MVEL rules and their expected `(input → routingGroup)` decisions; the Go port must produce identical decisions for all N on the same inputs. Run as a differential test between Java gateway and Go gateway against a recorded HTTP corpus.
  - **SQL parser:** maintain a corpus of `(query text, defaultCatalog, defaultSchema) → expected (queryType, tables, catalogs, schemas)` cases derived from the Java `TrinoQueryProperties` unit tests; the Go visitor must produce identical outputs.
  - **OIDC:** mock IdP (`oauth2-mock-server` or similar) recording known flows; Go port must complete each flow with the same observable state.

## Open Questions

- @trino-expert: how often does the Trino SQL grammar materially change between major versions? Affects re-codegen cadence and risk.
- @architect (self): can we punt on Oracle support for the Go rewrite v1, given the cgo/driver fragmentation? Worth a product decision.
- @qa-tech-lead: what corpus size of recorded rules + queries do we need before sign-off? 100 rules + 1000 SQL examples? More? Drives Phase-1 test fixture effort.

## Cross-references

- [[library-landscape-go-mapping.md]] — full dependency landscape this study drills into
- [[component-build-order.architect.md]] — where these hotspots land in the build sequence
- `../both/protocol-constraints-on-the-gateway.architect.md` — protocol facts that constrain how aggressive the rewrites can be
