---
title: Go-implementer view on the dependency inventory — concrete substitutions and Go-side gotchas
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - cross-cutting
  - routing-engine
  - query-classification
  - persistence
  - auth
  - config
  - observability
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/jvm-dependencies-inventory.md
  - trino-gateway/library-landscape-go-mapping.md
---

# Go-implementer view on the dependency inventory — concrete substitutions and Go-side gotchas

## Summary

This study is the **Implementer counterpart** to both `[[jvm-dependencies-inventory.md]]` by `@java-analyst` (contract-vs-implementation classification) and `[[library-landscape-go-mapping.md]]` by `@architect` (recommended initial Go dep set). Read both first; this file calls out the **three Go-specific gotchas** that neither surfaces (Caffeine's compute-if-absent semantics require `singleflight`, Airlift's `DataSize`/`Duration` need a custom YAML unmarshaler, JMX must be deleted not ported), plus **three concrete divergences from the architect's library picks** that need resolution before the proxy core is written (`golang-lru/v2` + `singleflight` vs `ristretto`/`golang-lru/v2/expirable` for caches; `pressly/goose` vs `golang-migrate` for migrations; Oracle driver choice). Everything else cross-references existing studies instead of duplicating their catalogs.

## Key Findings

### The three gaps that matter

(Architect's `[[rewrite-hotspots.md]]` covers the first two with concrete recommendations; I'm noting alignment and where I'd push back if at all.)

- **`org.mvel:mvel2` — JVM expression language used for routing rules.** `MVELRoutingRule.java:30-32`, conditions/actions are arbitrary MVEL strings evaluated against a request-derived `Map<String, Object>` (`:110-126`). No Go MVEL implementation exists. **Architect's pick (which I support): `expr-lang/expr` plus a `mvel2expr` heuristic translation tool and a docs cookbook.** Migration cost is real — every existing rule file needs editing — but unavoidable. Aligned.

- **`io.trino:trino-parser` — runtime SQL parser pulled in as a Maven dep.** `StatementUtils.java:29-92` imports ~60 AST nodes; `TrinoQueryProperties` (716 LOC) extracts catalogs/schemas/tables/query-type for routing. **Architect's pick: regenerate via ANTLR4 Go target against the same Trino grammar version, treat as vendored dep re-rolled on each trino-version bump.** This is option (3) from my original draft but better-specified (ANTLR-Go vs hand-rolled partial grammar). Implementer assessment: tractable but creates a version-rebumping treadmill — every Trino release bump touches a generated Go parser. If we go this route, suggest making it a CI job rather than a manual step. Otherwise aligned.

- **`io.airlift:*` — Trino's in-house JVM platform.** 13 Airlift modules covering HTTP, JSON, log, JMX, OpenMetrics, tracing, JSR-380 config validation, `DataSize`/`Duration` units. No equivalent ecosystem in Go — assemble from `net/http`, `log/slog`, `prometheus/client_golang`, `go.opentelemetry.io/otel`, `go-playground/validator/v10`, plus one small internal `airliftish` package (~100 LOC) for `Duration`/`DataSize` YAML unmarshaling. Architect's library landscape covers the bulk; my Test Hooks below add the units gotcha.

### Three divergences from `[[library-landscape-go-mapping.md]]`

These are the rows where my Go pick differs from the architect's. They need resolution before the proxy core is written — both of us coding to different cache or migration libraries would produce churn.

| Concern | Architect pick | My pick | Reason for divergence |
|---|---|---|---|
| **Cache library for queryId→{backend, routingGroup, externalUrl}** | `hashicorp/golang-lru/v2/expirable`, or `ristretto` if size-bounded eviction matters | `hashicorp/golang-lru/v2` + `golang.org/x/sync/singleflight` (~20 LOC of glue) | Caffeine's `LoadingCache` guarantees the loader runs at most once concurrently per key — `expirable` and `ristretto` don't. Without single-flight, under thundering-herd on a missing queryId the gateway can fire N concurrent DB lookups for the same key. See `[[concurrency-and-lifecycle-model.go-implementer.md]]` Open Questions. |
| **DB migration tool** | `golang-migrate/migrate` (drives the same `V?__*.sql` files) | `pressly/goose` | `goose` has a cleaner programmatic API for running migrations from `main` (matching Java's `HaGatewayLauncher.main → FlywayMigration.migrate(...)` flow) instead of a separate binary. Both can drive the existing `V?__*.sql` files after a small rename. This is a small divergence — happy to defer to architect's pick if there's a reason. |
| **Oracle driver, if Oracle is in scope** | `sijms/go-ora/v2` (pure-Go) or `godror/godror` (cgo, official) | **Recommend not committing to Oracle v1 parity at all** | The architect's question — "Is Oracle support in scope?" — should be answered "no for v1" unless `@team-lead` has a customer commitment we don't know about. Both Go drivers have caveats (cgo vs community-maintained). This is the kind of promise that's painful to walk back. Strongly flagging for `@team-lead`. |

### Three Go-side gotchas neither prior study surfaced

These are not picks — they're trap-detection findings.

- **Airlift `Duration` and `DataSize` need a custom YAML unmarshaler**, not just a `time.Duration` field. Java parses `"30s"`, `"5m"`, `"32MB"`, `"1GB"`; Go's `time.Duration` handles the time strings via `time.ParseDuration` (good), but byte-size strings have no stdlib parser. `dustin/go-humanize` parses similar formats but with different precision rules (MB vs MiB conventions). My recommendation: one small internal `airliftish` package (~100 LOC) with custom `yaml.Unmarshaler` for both. Without typed wrappers, every byte-size config field degrades to `string` or `int64` and we lose validation.
- **JMX must be dropped, not ported.** Architect's library landscape already classifies JMX as `drop` (correctly). My additional point: this needs a **v1 release-note callout** so operators currently scraping JMX know to migrate to the Prometheus endpoint. Otherwise it becomes a "we forgot" silent regression at deploy time.
- **Caffeine's `LoadingCache` single-flight is a correctness issue, not a perf issue.** Reiterating because it's the most important of the three gotchas: see divergence #1 above.

### Two Go-side opinions for architect to lock in

- **Plain constructor injection, no `google/wire`.** Architect's library landscape lists "explicit constructor wiring in `main`; optional `wire` (compile-time)". My stronger position: don't introduce `wire`. At our component count (~20 components), compile-time DI adds more complexity than it removes. Asking architect to lock this in `[[library-landscape-go-mapping.md]]` so we don't accidentally pull `wire` in later.
- **MVEL replacement language.** Architect's `[[rewrite-hotspots.md]]` already lands on `expr-lang/expr` with a translation cookbook + `mvel2expr` heuristic tool — we're aligned. **SQL parser strategy:** architect proposes regenerating via the **ANTLR4 Go target** with the same Trino grammar version, treated as a vendored dep re-rolled on each gateway trino-version bump. This is sharper than my study's option (3) (hand-rolled partial grammar) and probably the right call — the visitor port is "large but mechanical" per architect's read. Implementer assessment: tractable if we accept the version-rebumping treadmill. I'll defer to architect's plan.

### What the inventory tells us

- **Routine library swaps:** ~85% of the dep tree. No design blockers.
- **The Caffeine cache pattern is widely used.** `BaseRoutingManager` keeps three `LoadingCache`s for queryId→backend, queryId→routingGroup, queryId→externalUrl. The load function falls back to the query history DB on miss. This is a well-defined seam: a small Go interface `Cache[K, V]` with an in-memory implementation (`hashicorp/golang-lru/v2`) and the DB-backed loader injected as a `Loader[K, V] func(K) (V, error)`. Worth a tiny shared package.
- **`io.airlift:units.DataSize` and `Duration` are everywhere.** Config files use strings like `"32MB"`, `"30s"`. The Go side needs custom YAML unmarshalers that parse these — not hard, but easy to miss and produce confusing errors. One internal package, ~100 LOC, can cover both.
- **JMX should be dropped, not ported.** No equivalent in Go and no good reason to add one. Anyone consuming the gateway's JMX metrics today should be redirected to the Prometheus endpoint we'd add. This is an operational migration story, not a tech blocker.
- **Two DB drivers we may regret promising:** Oracle (via `sijms/go-ora`, community-maintained) and any Trino version Trino-JDBC supports that the Go client doesn't yet. Recommend explicitly *not* committing to Oracle parity for v1.

## Behavior vs. Implementation Artifact

### MVEL expression evaluation
- **Observed behavior:** rules files declare `condition` and `actions` as MVEL source strings; the gateway compiles them at config-load time and evaluates against a request-derived `Map<String, Object>` (`MVELRoutingRule.java:44-126`).
- **Source of behavior:** `gateway-design-intent` — flexible, user-extensible routing rules.
- **Rationale:** MVEL was chosen because it's a JVM expression language with safe-by-default scoping. The exclusion of `Process` and `Runtime` (`MVELRoutingRule.java:75-92`) makes the sandboxing explicit but not airtight (any `Class.forName` route exists in principle).
- **Go obligation:** `defer-to-expert`. The *behavior* (declarative rules with conditional logic) must be preserved. The *artifact* (MVEL syntax, JVM types in scope) must not — there is no MVEL in Go. The architect must pick the replacement expression language and document the migration story for existing users. My recommendation if asked: `expr-lang/expr` (better feel, simpler types) over `cel-go` (stricter, harder migration).

### `trino-parser` runtime dependency
- **Observed behavior:** the gateway parses inbound SQL using the actual Trino SQL parser to extract query type, catalogs, schemas, tables, and prepared-statement names (`StatementUtils.java`, `TrinoQueryProperties.java`). These extracted fields are then available as MVEL rule inputs.
- **Source of behavior:** `gateway-design-intent` — content-aware routing.
- **Rationale:** Lets operators write rules like "DDL statements go to the maintenance cluster" or "queries touching catalog `analytics` go to the analytics cluster" — these require parsing the SQL, not just looking at headers.
- **Go obligation:** `defer-to-expert`. This is a scope question, not an implementation question. Three serious options exist (drop the feature, shell out to a Java sidecar, write a minimal SQL grammar) and each has different tradeoffs for users and for the team. Strong recommendation: take this to `@team-lead` for an explicit decision before any Go code is written; getting halfway through implementation and discovering this feature can't ship is the worst outcome.

### Airlift framework dependency
- **Observed behavior:** `HaGatewayLauncher.java:53-94` composes an Airlift `Bootstrap` with `NodeModule`, `HttpServerModule`, `JaxrsModule`, etc. Configuration is typed and validated up-front; lifecycle is managed via `@PostConstruct`/`@PreDestroy`; Guice wires everything.
- **Source of behavior:** `jvm-artifact` — Airlift is Trino's chosen platform, used because the gateway is a Trino-org project.
- **Rationale:** Consistency with Trino itself; battle-tested in production.
- **Go obligation:** `replicate-intent`. The *intent* is "typed config, validated up front, with deterministic lifecycle". The *artifact* (Guice + 13 Airlift modules) does not port. Substitute: a `Config` struct unmarshaled from YAML, a small `Component { Start(ctx) / Stop(ctx) }` interface set, explicit constructor wiring, `prometheus/client_golang` + `slog` + `otel`.

## Implications for Go Rewrite

- **Three hard decisions belong to architect/team-lead before component work begins:**
  1. MVEL replacement language: `expr-lang/expr` vs `cel-go` vs declarative-only.
  2. SQL content routing: drop, sidecar, or partial-parser.
  3. Oracle DB support: in or out of v1.
  Without these, design specs for routing-engine and persistence-layer components will be guesswork.
- **Plan to write one small internal `airliftish` package** that provides: `Duration` and `DataSize` YAML unmarshaling, a `Component` lifecycle interface, structured logging shim. Keeps the Airlift-equivalent footprint contained instead of leaking across the codebase.
- **Caffeine substitute:** a single `cache` package with a small generic interface (`Get(K)`, `Put(K, V)`, `GetOrLoad(K, loader) (V, err)`) backed by `hashicorp/golang-lru/v2` plus a `time.Timer`-driven TTL eviction goroutine. Don't reach for `ristretto` unless we measure cache-size pressure first — it's heavier and harder to reason about.
- **Drop JMX without apology.** Document it explicitly so it's not a "we forgot" issue at QA review time.
- **Test deps:** Go QA will choose; flagging `testcontainers-go` as the established path for DB integration tests so we're aligned with the Java side's pattern.

## Test Strategy Hooks

- **Test level:** n/a for this study (inventory, not behavior). Per-dependency testability concerns belong to the component studies that use them.
- **Fixtures required:** n/a.
- **Observable signals:** n/a.
- **Non-determinism risks:** n/a.
- See paired QA study (none — this is an architectural inventory, no QA pair expected).

## Open Questions

- `@architect`: pick the MVEL replacement language. My input on tradeoffs is above; the call is yours, and it affects user migration docs.
- `@architect`: decide the SQL-content-routing strategy. This is the single biggest scope decision for the Go rewrite. Recommend taking it to `@team-lead` before any router code is written.
- `@team-lead`: is Oracle DB support a v1 requirement? `sijms/go-ora` is community-maintained, not vendor; that's a real ops risk to commit to.
- `@trino-expert`: how often does the Trino SQL grammar change between releases in ways that would affect the gateway's query-type classification? Trying to gauge the maintenance cost of option (3) — building a minimal grammar.
- `@java-analyst`: confirm `TrinoRequestUser`, `TrinoQueryProperties`, and the request analyzer config (`requestAnalyserClientsUseV2Format`, `requestAnalyserMaxBodySize`) — these are the inbound parsing seams I haven't fully traced. Are they all on the parser-dependent path?

## Cross-references

- `[[proxy-streaming-vs-buffering.go-implementer.md]]` — how the proxy handles HTTP bodies; references `io.airlift:http-client` and `JsonCodec`.
- `[[concurrency-and-lifecycle-model.go-implementer.md]]` — covers the Guice/Airlift lifecycle substitution in more detail.
