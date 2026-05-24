---
title: Trino protocol nuances that are hard to replicate outside the JVM
author: trino-expert
role: Trino & Trino-Gateway Expert
component: both
topics: [statement-protocol, proxy-core, session-state, routing-engine, cross-cutting]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino/statement-protocol-overview.md
  - trino/spooled-segments-and-redirects.md
  - trino/protocol-header-prefix-configurable.md
  - trino-gateway/architectural-intent.trino-expert.md
  - trino-gateway/jvm-dependencies-inventory.md
  - trino-gateway/mvel-rules-language.md
---

# Trino protocol nuances that are hard to replicate outside the JVM

## Summary

Most of the trino-gateway's Trino-protocol behavior is plain HTTP and ports cleanly to Go — header copy, response streaming, slug-bearing URL pass-through, 303 redirect handling. The "hard" parts are not in the wire protocol itself; they are in two specific gateway features that lean on JVM-only libraries: **MVEL-based routing rules** and **`TrinoQueryProperties` SQL parsing via `trino-parser`**. Everything else is either trivial (HTTP plumbing, JSON parsing of a few well-defined fields, regex extraction) or already handled outside the JVM by other Trino client SDKs (Python, Go, JS). This study separates "JVM idiom" from "Trino protocol" so the go/no-go discussion can scope the rewrite honestly.

## Key Findings

### A. Things that LOOK Trino-protocol-hard but are actually pure HTTP

These behaviors involve Trino specifics, but the gateway's role is purely transport — Go ports without protocol risk.

- **The `nextUri` polling loop.** Statement protocol is a sequence of GETs against a slug-bearing URL until `nextUri` is absent. The gateway never advances the loop; it just routes each individual GET. Pure HTTP plumbing. See [[../trino/statement-protocol-overview.md]].
- **Slug + token URLs.** The gateway never generates or validates slugs — it forwards URLs unchanged. The slug is `@ResourceSecurity(PUBLIC)` on the coordinator side; the gateway has zero protocol involvement in its construction. See [[../trino/statement-protocol-overview.md]] "Slug-and-token URLs are unguessable."
- **`X-Trino-*` request/response headers (the whole family).** All gateway operations on these are header copy or a single lookup-by-name. Trino client SDKs in Python, JS, and Go already implement this header set; the wire format is stable and documented.
- **Spooled segments and 303 redirects.** As long as the HTTP client doesn't auto-follow redirects, this works. The gateway never participates in the data path for `STORAGE` and `COORDINATOR_STORAGE_REDIRECT` modes; for `COORDINATOR_PROXY` and `WORKER_PROXY` modes it just streams bytes. See [[../trino/spooled-segments-and-redirects.md]].
- **OAuth2 / OIDC callback flow.** The gateway's role is to forward the redirect from the coordinator's IdP back to the client browser, plus optionally cookie-stick the session. Standard reverse-proxy OAuth2 behavior; not Trino-specific. (Cookie-signing is HMAC-SHA256 — trivial in Go.)
- **HEAD `/v1/statement` connection probes.** Just a GET-less method dispatch. Go's `net/http` handles this natively.
- **`X-Forwarded-*` header injection.** Five header names; identical to any modern reverse proxy. Go libraries already do this.
- **502/503/504 retry semantics.** The gateway just doesn't translate backend status codes — the client retries. Negative requirement, trivially satisfied.
- **Query-history persistence.** One `INSERT` per completed query. SQL, not Trino.

### B. The two genuinely JVM-bound features

These are the only two places in the gateway where Trino's Java implementation pulls in dependencies that Go cannot meaningfully substitute without a port.

#### B.1 MVEL rules engine (`routing.rulesType=FILE`)

- **What it is:** MVEL is a Java-runtime expression language. Rules files like `gateway-ha/src/test/resources/rules/routing_rules.yml` contain Java-flavored expressions (`request.getHeader('X-Trino-User') matches "etl.*"`) that operators evaluate against an incoming request to choose a routing group.
- **Why it's JVM-bound:** MVEL is `org.mvel2:mvel2`, a 670 KB JAR that compiles its own bytecode and executes against Java objects (`HttpServletRequest`, `TrinoQueryProperties`). There is no published Go port. Existing Go expression-language candidates (`google/cel-go`, `expr-lang/expr`, `antonmedv/expr`) have DIFFERENT syntax and DIFFERENT object models — translating rules is non-trivial and not byte-compatible.
- **Operator migration cost:** every existing MVEL rules file must be rewritten in whatever expression language the Go gateway adopts. This is a real cost for operators who maintain large rule sets.
- **Risk classification:** HIGH for any operator using `rulesType=FILE`; ZERO for operators using `rulesType=HEADER` or `rulesType=EXTERNAL`.
- **Source:** `gateway-ha/.../router/MVELRoutingRule.java`, `gateway-ha/.../router/FileBasedRoutingGroupSelector.java`, [[../trino-gateway/mvel-rules-language.md]] for full deep-dive.

#### B.2 `TrinoQueryProperties` — SQL parsing via `trino-parser`

- **What it is:** When MVEL rules reference catalog/schema/table affinity (e.g., "route queries against `hive.tpch.lineitem` to the BI cluster"), the gateway parses the SQL text using Trino's own `io.trino:trino-parser` library to extract catalog/schema/table identifiers, query type (SELECT vs DDL vs DML), and resource estimates.
- **Why it's JVM-bound:** `trino-parser` is the ANTLR-generated grammar that the Trino coordinator itself uses to parse SQL. Re-implementing it in Go is a multi-person-month effort and creates a permanent maintenance burden as Trino evolves its grammar. There is no Go port. (cf. `pingcap/tidb`'s `parser` package took years to stabilize for MySQL.)
- **Why a partial implementation is risky:** any divergence between the Go parser and the Java parser would cause rules to evaluate inconsistently — a query that the operator's rule says should go to cluster A would land on cluster B under the Go gateway. Silent routing bugs are worse than visible failures.
- **The narrow escape:** as noted in [[../both/sticky-routing-contract.md]], the **only** place outside MVEL rules where the gateway parses SQL is `kill_query` body parsing — and that single use case can be replaced by a 3-line regex (`KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'`). So if MVEL/file-based rules are dropped in v1, the Go gateway can eliminate `trino-parser` entirely.
- **Risk classification:** HIGH if MVEL file-based rules are in v1 scope; ZERO if they are deferred or replaced with external HTTP rules.
- **Source:** `gateway-ha/.../router/TrinoQueryProperties.java`, full inventory in `studies/trino-gateway/jvm-dependencies-inventory.md`.

### C. Things that look hard but are bounded-scope

These have non-trivial implementation but are well-defined and portable.

- **Configurable protocol header prefix (`X-Presto-` vs `X-Trino-`).** Detection algorithm is ~10 lines (`ProtocolHeaders.detectProtocol()`). See [[../trino/protocol-header-prefix-configurable.md]].
- **HMAC-SHA256 gateway cookies (`TG.*`, `OAuth2`-class).** Standard crypto in Go's `crypto/hmac`. JSON payload, base64 encoding. ~50 lines.
- **QueryId extraction from URL paths.** Single regex `\d+_\d+_\d+_\w+` against path tokens and query-string params. ~30 lines including the kill_query special case.
- **JSON parsing of `QueryResults.id` field.** Single field extraction; use `encoding/json` with a struct that has only that field. ~10 lines.
- **Active cluster monitoring via `/v1/info`.** Periodic HTTP GET, parse JSON, update in-memory state. Standard background-goroutine pattern.
- **Connection-keepalive and HTTP/2 negotiation.** Go's `net/http` handles both transparently. The Java gateway uses Airlift's HTTP client; Go's standard library is competitive.
- **Per-route timeout configuration.** Standard `context.WithTimeout` pattern, applied per request class.

### D. JVM idioms that masquerade as protocol features (do NOT port)

Things present in the Java gateway that are JVM artifacts, not Trino protocol — explicitly do not replicate in Go.

- **JAX-RS pre-match URI rewriting** to `/trino-gateway/internal/route_to_backend` (`RouterPreMatchContainerRequestFilter.java`). This is a Jersey workaround for path-prefix dispatch; in Go, use a path-prefix matcher directly. See [[../trino-gateway/architecture-overview.md]] "Pre-match URI rewriting."
- **Guice dependency injection.** The Go rewrite should use constructor injection or `wire` for compile-time DI. No runtime DI container needed.
- **`@Provides` factories switching on config to instantiate one of N strategy implementations.** In Go this is `switch config.X { case "a": return &Astrategy{} ... }`.
- **`modules:` and `managedApps:` FQCN-based extension points.** Go has no runtime classloading; drop these in v1. See [[../trino-gateway/architecture-overview.md]] "Dynamic loading."
- **Airlift `Bootstrap` and `ConfigurationFactory`.** Use any standard Go config library (`viper`, `koanf`, plain YAML decoding). No equivalent needed.
- **`FluentFuture.transform` chains for async response processing.** In Go, write straight-line code or use channels. Future-monad style is unidiomatic and unnecessary.
- **JMX MBean exposure.** Replace with `expvar` or Prometheus metrics; JMX is JVM-only and not a protocol feature.

### E. Protocol features deliberately omitted from the gateway

Trino has protocol surfaces the gateway does not touch. The Go rewrite should consciously preserve these omissions, not "complete" them.

- **`/v1/task/*` worker endpoints** — internal to Trino, not part of the client protocol. The gateway should never see these.
- **`/v1/memory`, `/v1/node` admin endpoints** — coordinator-internal. Not in the proxy whitelist.
- **`/v1/event` event listener push** — coordinator-internal SPI surface; not a client protocol.
- **Resource group management** — Trino-side concern, not gateway-side.

If a Go rewrite tries to "helpfully" proxy these, it would expose coordinator internals to clients and create new attack surface.

## Behavior vs. Implementation Artifact

### MVEL rules: protocol-compatible operator extension surface
- **Observed behavior:** Operators write expression-language rules that the gateway evaluates per-request to choose a routing group. The rules are entirely an operator-extension surface; Trino itself has no concept of them.
  Source: `gateway-ha/.../router/MVELRoutingRule.java`, [[../trino-gateway/mvel-rules-language.md]].
- **Source of behavior:** `gateway-design-intent` + `jvm-artifact`. The CONCEPT of operator-defined routing rules is design intent; the CHOICE of MVEL is a JVM artifact from a time when MVEL was a popular embedded expression language.
- **Rationale:** Operators needed a way to express "which cluster" decisions in terms of request properties without recompiling the gateway. MVEL was the path of least resistance in 2018-era JVM ecosystem.
- **Go obligation:** `replicate-intent`, NOT `replicate-exactly`. The Go gateway must support operator-extension routing rules. The expression language can be different (CEL, `expr`, or a custom mini-DSL). A migration tool — or a documented manual translation — for existing MVEL rule files is mandatory if file-based rules ship in v1.
- **Notes:** Architect should weigh "ship file-based rules with new expression language" vs. "ship external-HTTP rules only in v1, MVEL replacement in v2." The latter is dramatically lower risk.

### `trino-parser` SQL parsing: real coupling, narrowly used
- **Observed behavior:** SQL text is parsed with the same library Trino uses, to extract structured query metadata (catalog/schema/table, query type) for use in routing rules and query-history capture.
  Source: `gateway-ha/.../router/TrinoQueryProperties.java`.
- **Source of behavior:** `defensive-historical`. Using the same parser as Trino was the safe choice — guaranteed to match. The cost (heavy JVM dependency) was acceptable when the gateway lived in the same monorepo as Trino.
- **Rationale:** Avoid grammar drift between gateway and coordinator. Avoid re-implementing SQL parsing.
- **Go obligation:** `replicate-intent` ONLY IF MVEL file-based rules with catalog/schema/table predicates are in v1 scope. Otherwise `drop`. The `kill_query` special case is a regex.
- **Notes:** If shipping requires partial parsing, scope it ruthlessly: identify the exact set of SQL fragments operators reference in rules today (likely catalog name only), build a regex/lexer for ONLY those, and document the limitation. Full SQL parsing is out of scope.

### Configurable header prefix: ported by replicating one detection function
- **Observed behavior:** Trino lets operators set `protocol.v1.alternate-header-name=Presto`; the coordinator then accepts both header families and reflects the family back in responses.
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:382-399`, [[../trino/protocol-header-prefix-configurable.md]].
- **Source of behavior:** `protocol-required` for clients using the alternate prefix.
- **Rationale:** Legacy Presto client compatibility.
- **Go obligation:** `replicate-intent`. Port `detectProtocol()` (~10 lines). Treat the prefix as per-request, not a constant.
- **Notes:** This is the cleanest example of "Trino protocol nuance that has a small but exact Go equivalent."

### Async response handling: idiom is JVM, mechanism is HTTP
- **Observed behavior:** The Java gateway uses `airlift.jaxrs.AsyncResponseHandler` + `FluentFuture.transform` chains to schedule "extract queryId from response" and "store backend mapping" as post-response continuations.
  Source: `gateway-ha/.../ProxyRequestHandler.java:188-202`, design.md:30-34.
- **Source of behavior:** `jvm-artifact`. The async-continuation style is idiomatic to the Java reactive ecosystem.
- **Rationale:** Don't block the request thread; enable non-blocking I/O throughout.
- **Go obligation:** `drop` the idiom; `replicate-intent` for the behavior. In Go, a single goroutine per request handles the request lifecycle including post-response cleanup. No future-monad needed.
- **Notes:** Pay attention to the "cache write happens after response is sent" race documented in [[../both/sticky-routing-contract.md]]; the Go rewrite can fix this synchronously without losing throughput.

## Implications for Go Rewrite

- **The Trino wire protocol itself is fully portable to Go.** No part of the statement protocol, slug/token mechanism, header families, spooled segments, or redirect modes requires JVM-only capabilities.
- **There are exactly two hard JVM-bound features: MVEL rules and trino-parser-based SQL parsing.** Both belong to `routing.rulesType=FILE` with catalog/schema/table predicates. Both can be deferred from v1 with minimal user impact (operators can use `HEADER` or `EXTERNAL` rule types).
- **Scope decision drives the rewrite size:**
  - **Minimal v1** (no MVEL, no SQL parsing, header/external rules only): ~2,500 LOC, no JVM-dependency port required. Trino-protocol risk is low and bounded.
  - **Full v1** (with file-based rules in a new expression language + partial SQL parsing for catalog matching): ~6,000-8,000 LOC plus migration tooling. Trino-protocol risk concentrated in the SQL parser surface area.
- **Recommended posture for the go/no-go discussion:** the Go rewrite is feasible. It is also feasible to make it dramatically simpler than the Java version IF MVEL is dropped or replaced. The Architect's biggest scoping decision is the rules engine.
- **Maintenance cost asymmetry:** Trino's wire protocol evolves slowly (header set changes maybe once per year). Trino's SQL grammar evolves with every release. A Go gateway that doesn't parse SQL has near-zero ongoing protocol-tracking burden; one that does parse SQL inherits a permanent obligation.
- **For the migration path:** existing operators with MVEL rules cannot move to a Go gateway without rule translation. Provide a side-by-side mode (run Java and Go in parallel, comparing routing decisions) if MVEL replacement ships before MVEL retirement.

## Test Strategy Hooks

- **Test level:** differential (Go gateway vs Java gateway against the same backend) for everything in category A and C; targeted unit tests for category B port decisions.
- **Fixtures required:**
  - Full mock-coordinator suite (already planned in `studies/trino-gateway/test-infrastructure-inventory.go-qa.md`).
  - A corpus of real-world MVEL rules files to test the replacement engine against — needs to be solicited from operators.
  - SQL snippets exercising the catalog/schema/table extraction surface for any partial SQL parser.
- **Observable signals:**
  - Routing-decision equality across gateways for category A behaviors (any deviation is a bug).
  - Performance parity on the no-rules path (the gateway should not be slower than a thin Go reverse proxy on this path).
- **Non-determinism risks:**
  - Differential testing against MVEL rules requires byte-stable input ordering; otherwise hash-map iteration order in rules can shift decisions.
  - SQL parser corner cases (whitespace, comments, multi-statement) need explicit fixtures.

## Open Questions

- What is the actual production usage breakdown of `rulesType=FILE` vs `rulesType=HEADER` vs `rulesType=EXTERNAL`? Determines whether MVEL is v1-critical. `@trino-expert` self-note: solicit data via the OSS Slack / GitHub issues in Task #9.
- For MVEL replacement, which Go expression language has the best operator ergonomics? `cel-go` is most polished and security-audited; `expr-lang/expr` has more flexible syntax closer to MVEL. `@architect` / `@go-implementer`.
- Are there Trino protocol features added in 2025-2026 that this study underweighs (e.g., new streaming response formats, push-based query events)? `@trino-expert` self-note: review release notes 460→481.
- Does the existing Java gateway have any code path that depends on Java's HTTP/2 server-push semantics (rare in reverse proxies, but worth checking)? Go's HTTP/2 server-push support is more limited. `@java-analyst`.

## Cross-references

- [[../trino/statement-protocol-overview.md]] — the wire protocol that mostly ports trivially.
- [[../trino/protocol-header-prefix-configurable.md]] — example of a nuance with a small exact equivalent.
- [[../trino/spooled-segments-and-redirects.md]] — example of "looks Trino-specific, is actually HTTP."
- [[../trino-gateway/architectural-intent.trino-expert.md]] — frames what the gateway is FOR, which constrains what must port.
- [[../trino-gateway/jvm-dependencies-inventory.md]] — concrete dep-by-dep inventory underlying this study.
- [[../trino-gateway/mvel-rules-language.md]] — MVEL deep dive.
- [[../both/sticky-routing-contract.md]] — the kill_query escape hatch from full SQL parsing.
