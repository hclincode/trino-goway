# Do We Need a Go Version of trino-gateway?

**Date:** 2026-05-24  
**Status:** Final — team consensus reached  
**Participants:** trino-expert, java-analyst, architect, go-implementer, java-qa, qa-tech-lead, go-qa  
**Outcome:** **PROCEED WITH CAVEATS** (unanimous, 7/7)

---

## Background

`trino-gateway` is a Java/JVM reverse proxy that load-balances and routes Trino queries across multiple backend clusters. It provides multi-cluster routing on a single URL, blue/green upgrade support, query history, cluster health monitoring, and an admin REST API. The Java codebase is ~13,600 LOC across 146 files.

This team was convened to answer: **Should we rewrite trino-gateway in Go?**

The study phase produced 30+ insight files in `studies/` covering the Trino statement protocol, trino-gateway internals, JVM entanglements, auth surface, persistence layer, test infrastructure, and Go implementation feasibility. This document synthesizes all seven team members' perspectives into a recommendation.

---

## Scope Decisions (Pre-locked by Team Lead)

The following scope decisions were made during the study phase and are binding for v1:

| Decision | Ruling |
|---|---|
| Oracle DB support | **Deferred to v2** — no cgo driver in v1; MySQL + Postgres only |
| Per-routing-group database isolation | **Dropped from v1** — `JdbcConnectionManager.getJdbi(routingGroupDatabase)` is a sharp Java edge with no confirmed operator use |
| SQL content routing (`trino-parser`) | **Dropped from v1** — ANTLR-Go regen creates a permanent version-tracking burden; defer to v2 if demanded |
| MVEL file-based routing rules | **Defer to v2** — no Go MVEL interpreter exists; CEL or `expr-lang/expr` replacement is a v2 decision |
| Web UI | **Serve existing static bundle unchanged** — embed compiled `webapp/` assets from the Java repo; no UI rewrite |

---

## Team Perspectives

### 1. Trino & Trino-Gateway Expert — PROCEED WITH CAVEATS

**Domain:** Trino wire protocol, gateway behavioral contract, JVM-bound surfaces.

**Position:** The wire protocol is fully portable to Go. The caveats are entirely about scope, not technical possibility.

**Key findings:**
- The entire Trino statement protocol — POST `/v1/statement`, `nextUri` polling, slug+token URLs, `X-Trino-*` header families, spooled-segment 303 redirects, `X-Forwarded-*` injection, HMAC-SHA256 gateway cookies, configurable header prefix — is pure HTTP plumbing. Python and JS Trino clients already implement all of it outside the JVM.
- There are **exactly two genuinely JVM-bound features**, both gated behind `routing.rulesType=FILE`: **MVEL** (no Go port) and **`trino-parser`** SQL AST parsing (multi-person-month port, permanent grammar-tracking burden). Both can be deferred from v1 without affecting operators using `rulesType=HEADER` or `rulesType=EXTERNAL`.
- The scope decision drives a **3× size range**: minimal v1 (without MVEL/trino-parser) ≈ **2,500 LOC**; full v1 (with file-based rules + SQL parsing) ≈ **6,000–8,000 LOC**.
- A Go rewrite naturally fixes two latent Java bugs: (a) `ProxyResponseHandler` buffers ALL responses with `readNBytes` + UTF-8 decode, silently truncating large responses and corrupting binary spooled segments — making `COORDINATOR_PROXY` and `WORKER_PROXY` modes effectively broken behind the current Java gateway; (b) JWKS is fetched per-request with no caching.

**Hard invariants that MUST land in v1 (non-negotiable):**
1. **Never rewrite response bodies.** `nextUri` points to the gateway because the backend coordinator builds it from `X-Forwarded-*` headers — not from any body manipulation. Body rewriting would simultaneously break OAuth2, spooled segments, and the queued→executing host pivot.
2. **Disable HTTP redirect-following globally.** `Client.CheckRedirect = func(...) error { return http.ErrUseLastResponse }`. The default Go `http.Client` follows 3xx; this would silently break spooled-segment downloads and OAuth2 IdP redirects.
3. **Sticky-routing cache write must complete before flushing the response.** A goroutine fire-and-forget would introduce a race not present in the Java implementation.
4. **Implement the 3-step cache-miss recovery chain** (`BaseRoutingManager.findBackendForQueryId:184-239`): history lookup → fan-out HEAD probe → first-active-default fallback. Simplifying to "re-run routing rules" causes cross-cluster query duplication on gateway restarts and TTL expiries.
5. **Document the `http-server.process-forwarded=true` cross-system contract** prominently — it is the reason `nextUri` works correctly and the Java gateway docs currently bury it.

**Risks:**
- Auth scope is the second-largest decision: noop + OAuth2 covers ~80% of operator demand; LDAP adds `go-ldap/ldap` dependency.
- Cookie-signing key compatibility must be an explicit architect decision: hard-cutover vs. soft-cutover determines whether HMAC-SHA256 byte-compat with the Java `GatewayCookie` is required.
- Do not proxy `/v1/task/*`, `/v1/memory`, `/v1/node`, `/v1/event` — these are coordinator-internal SPI and the Java gateway deliberately omits them.

---

### 2. Java Analyst — PROCEED WITH CAVEATS

**Domain:** Full trino-gateway internals, JVM entanglement inventory, spec production.

**Position:** Proceed if and only if v1 scope defers MVEL file-based rules and trino-parser-based SQL parsing. With that scope cut, the rewrite is sound.

**Key findings:**
- 90%+ of the gateway's Java is plain HTTP/JSON/config plumbing that ports cleanly. The remaining ~10% is two specific JVM-bound surfaces (MVEL + trino-parser) concentrated in one config knob (`routing.rulesType=FILE`).
- The `kill_query` body parse — the **only** non-MVEL site that uses trino-parser — is replaceable with a single regex: `KILL\s+QUERY\s+'(\d+_\d+_\d+_\w+)'`. Deferring MVEL therefore also eliminates trino-parser entirely.
- The operator-facing contract is narrower than the codebase suggests: ~15 public methods on `TrinoQueryProperties`, ~12 fields in the external-routing JSON payload, 7 `@RolesAllowed` annotations across 3 roles (`ADMIN`/`USER`/`API`).
- **JVM idioms to explicitly NOT port** (from `studies/trino-gateway/jvm-idioms-not-to-port.md`): JAX-RS pre-match URI rewriting, Guice runtime DI, `modules:`/`managedApps:` FQCN classloading, Airlift `Bootstrap`+`ConfigurationFactory`, `FluentFuture.transform` chains, JMX MBeans. Treating these as design requirements instead of artifacts would inflate the rewrite 30–50% with zero user-visible value.

**Auth behavioral decisions required before coding (all four explicitly):
1. **JWKS caching**: Fix in Go — per-request JWKS fetch is a defect, not a contract. Use TTL + kid-refresh caching (`github.com/MicahParks/keyfunc`).
2. **302 swallow**: Restore the redirect-to-login UX (currently swallowed by `ChainedAuthFilter:91` catch-all). Flag for product since SPA may have built around the 403.
3. **HTTP 200 with `{"code":401}` body** (`AuthorizedExceptionMapper`): Replicate exactly — the SPA almost certainly depends on this shape.
4. **POST /logout no-op**: Defer — confirm SPA does client-side cookie clearing before deciding.

**Risks:**
- `X-Presto-*` alternate header prefix is buggy in the Java gateway (`HttpUtils.java` hardcodes `X-Trino-*`); fixing it in Go changes behavior for those deployments — needs product confirmation.
- Operator migration cost is unknown. Recommend a 1-week operator survey via OSS Slack/GitHub issues on MVEL rule file usage before architecture sign-off.

---

### 3. Architect / Tech Lead — PROCEED WITH CAVEATS

**Domain:** Go system design, library choices, concurrency model, component order.

**Position:** Proceed. The Java codebase is structurally a thin reverse proxy with a sticky-by-queryId routing layer, an out-of-band cluster monitor, and a JDBC-backed registry/history store wrapped in heavy Airlift/Guice scaffolding. The data path is small and tractable to port; complexity sits in the routing-decision layer (MVEL, `trino-parser`) and in scaffolding that does not need to survive translation. The cuts already locked by team-lead (MVEL deferred, `trino-parser` dropped, per-routing-group DB dropped, Oracle deferred, Web UI bundle reused) make the v1 target a small, low-risk Go service rather than a faithful JVM-shaped reimplementation.

**System-shape findings** (from my 8 architect studies):
- **Airlift collapses, not ports.** The ~13 Airlift modules collapse to `net/http` + `chi` + `slog` + `prometheus/client_golang` + `otel` + a ~100 LOC `internal/lifecycle` package + ~80 LOC of `internal/config/units` (`DataSize`, `Duration` custom unmarshalers). No DI framework. Guice → explicit constructor wiring; Go's compile-time dep graph is a strict improvement over Guice's runtime resolution, and removes the `@Provides`-with-`switch`-on-enum patterns in `HaGatewayProviderModule` that today silently fall through on misconfig.
- **The whole concurrency story is goroutines + `context.Context`.** The Java gateway has four thread-pool families (Jetty inbound, Airlift HttpClient outbound, per-handler cached transformation pool, three independent scheduled executors) plus JAX-RS `@Suspended AsyncResponse`. None of that has semantic content. The Go shape is: one goroutine per inbound request running the full pipeline, `req.WithContext(ctx)` for the outbound call, one long-running goroutine per long-running component (cluster monitor tick, history cleanup tick), `context.WithTimeout` for the `routing.asyncTimeout` → synthetic 502. The cached transformation pool, the `ListenableFuture.transform` chains, and the `@Suspended` machinery all drop.
- **Five-stage data path is the whole proxy.** `RouterPreMatchContainerRequestFilter` → `RoutingTargetHandler.resolveRouting` → outbound `HttpClient.executeAsync` → queryId extraction on POST `/v1/statement` 200 → response synthesis with cookie/header passthrough. In Go this is `pathFilter → routingResolver → backendSelector → proxyExecutor` with a separate `QueryBinder` interface called only on new statement responses. The JAX-RS pre-match URI rewrite to `/trino-gateway/internal/route_to_backend` is a pure JAX-RS dispatch trick and must not appear anywhere on the wire.
- **Three rewrite hotspots, in decreasing risk order:** (1) `ProxyRequestHandler` because of the buffer-vs-stream split (must buffer on POST `/v1/statement`, can stream elsewhere — naive `httputil.ReverseProxy` will get this wrong); (2) `BaseRoutingManager` because of the 3-step cache-miss recovery chain that trino-expert flagged as non-negotiable (history lookup → fan-out HEAD probe → first-active-default fallback); (3) the auth filter chain because of the four behavioral decisions java-analyst surfaced.
- **Config coupling is shallow but wide.** `HaGatewayConfiguration` aggregates ~20 sub-config POJOs in one fat YAML. `modules:` and `managedApps:` are lists of FQCNs reflectively loaded at boot — the documented extension mechanism with no cross-language equivalent. v1 ruling: drop FQCN extensibility, keep all in-tree components as explicit Go imports, document the break.

**Hard architectural invariants for v1** (extending trino-expert's protocol invariants with design-level rules):
1. **Explicit `Start(ctx)` / `Stop(ctx)` lifecycle.** No work in constructors. `JdbcConnectionManager`'s constructor-launched cleanup scheduler is a soft anti-pattern in Java; do not preserve it. The composition root owns startup and shutdown order.
2. **Three separate `*http.Client` instances** (proxy / monitor / external-routing-callout). Keeps connection pools isolated and preserves backpressure separation that today's Java `@ForProxy` / `@ForMonitor` / `@ForRouter` provides implicitly.
3. **Buffer-vs-stream by request kind, not globally.** Statement-path responses buffer up to `responseSize`; other paths stream via `io.Copy`. Document the split.
4. **Soft-fail on rules-file load failure → header-only routing, loud log.** Replicate-intent on the Java fallback in `HaGatewayProviderModule:158-174` but emit a high-severity log + metric so operators see the degradation. The current Java behavior is silent and arguably wrong; the Go port keeps the safety net and fixes the visibility.

**Open library divergences I will resolve in writing before any component starts code-write** (these block Phase 2, not the decision to proceed):
- Cache: `hashicorp/golang-lru/v2` + `singleflight` vs `ristretto` vs `expirable` — `singleflight` matters for the DB-backed loader path; my call is `golang-lru/v2 + singleflight` unless Caffeine's window-TinyLFU eviction is load-bearing somewhere I haven't found.
- Migration tool: `pressly/goose` vs `golang-migrate/migrate` — leaning goose for simpler embedding and Go-native migrations.
- `backendToStatus` map: `sync.RWMutex`+`map` vs `sync.Map` — former is correct for ~10-key, single-writer-many-reader; `sync.Map` is the wrong tool here.
- `ActiveClusterMonitor` ticking: `time.NewTicker` (interval semantics — ticks drop on overrun) vs rate-driven scheduling (Java's `scheduleAtFixedRate` resyncs to wall clock). Java picks rate; for v1, match by computing next-tick from the start time, not by chaining `time.After`. Document the choice.

**Why "with caveats" not "with full confidence":** the six condition items in the team Conclusion are real preconditions, not formalities. If MVEL or `trino-parser` get smuggled back into v1 scope, the LOC budget triples and the schedule risk goes up materially; if the four library divergences are left to the first implementer to decide ad-hoc, we get inconsistent patterns across components; if the four auth behaviors are not pinned by product before the auth component is coded, we ship with subtly different login UX. Each is cheap to fix upfront and expensive to retrofit.

---

### 4. Go Implementer — PROCEED WITH CAVEATS

**Domain:** Go implementation feasibility, library choices, code-level translation strategy.

**Position:** Proceed with the locked v1 scope. If the scope holds and library divergences resolve before code-write, confidence is high. If either slips, downgrade to do-not-proceed-yet.

**Key findings:**
- The proxy core, persistence, and config layers are each ~150–200 LOC of straightforward Go. The hand-rolled forwarder (~150 LOC) is preferable to `httputil.ReverseProxy` for the seams needed. `CheckRedirect: ErrUseLastResponse` is load-bearing (see trino-expert invariant #2).
- Persistence is two tables, ~12 DAO methods, no joins, no transactions — `database/sql` + `sqlx` handles it in one file per table.
- Three Go correctness wins not available in Java: `[]byte` proxy path (future-proofs against binary responses), explicit `Component{Start/Stop}` lifecycle (compiler-enforced shutdown order replaces Guice `@PreDestroy` magic), `errors.Join`-aggregated config validation (surfaces all errors per launch instead of fail-fast).

**Implementation risks:**
- **R1:** Library-pick churn if architect divergences aren't resolved before `internal/proxy` or `internal/persistence` packages are started.
- **R2:** Scope creep — the three scope rulings (Oracle deferred, per-routing-group DB dropped, SQL content routing dropped) must be written into a `SCOPE.md` and require explicit team-lead sign-off to reverse.
- **R3:** Operator config compatibility ruling needed — strict YAML compat (same file works unchanged) vs. loose compat (migration tool) materially affects `internal/config` implementation effort.
- **R4:** Operator-visible strings and headers must be byte-identical by default: `"Request to remote Trino server timed out after"`, `Via: <proto> TrinoGateway`, `flyway_schema_history` table name. Cheap to honor, expensive to retrofit.
- **R5:** Non-statement paths (`/ui/*`) will buffer in v1 rather than stream — acceptable latency tradeoff for initial release.

---

### 5. Java QA — PROCEED WITH CAVEATS

**Domain:** Java behavioral invariants, test oracle analysis, QA coverage gaps.

**Position:** Inferred from studies — the Java QA studies consistently frame the rewrite as feasible but flag significant testing gaps that must be closed in Go.

**Key findings (from `proxy-request-lifecycle.java-qa.md`, `routing-engine.java-qa.md`, `test-infrastructure.java-qa.md`):**
- Eight well-defined proxy lifecycle seams provide clean Go test assertion points: path whitelisting, query metadata parsing, routing decision, header transformation, outbound execution, response body buffering, query-ID extraction + cache write, and query history persistence.
- The buffered (non-streaming) proxy response body is **behavioral spec, not artifact** — the buffer is load-bearing for POST `/v1/statement` queryId extraction. Go must buffer (or tee) statement-path responses. The response size cap must be configurable and tested — the Java suite does not test it.
- Random port selection (`20000 + Math.random()*1000`) in Java tests is a JVM artifact to drop — Go tests should use `:0` and read back the assigned port.
- Cookie-based sticky routing (signed `GatewayCookie`) is behavioral: wire-compatible HMAC-SHA256 if Go and Java gateways run side-by-side; otherwise replicate intent.
- The `Via: <protocol> TrinoGateway` header format is an operator-facing wire contract — differential tests should pin it.

**Critical test gaps the Go suite must close (not present in Java):**
- Body-size-cap behavior (`responseSize` truncation) — currently untested in Java
- Concurrency/race coverage — zero concurrent tests exist in the Java suite
- Log-line and metric-name assertions — no Java test asserts on log content or counter values
- Failed-statement-POST silent fall-through (non-JSON body on 200 OK) — known failure mode, no test

---

### 6. QA Tech Lead — PROCEED WITH CAVEATS

**Domain:** Test strategy, tooling choices, component sign-off rubric, cross-team coordination.

**Position:** Stated explicitly in Task #7 completion: proceed with caveats. The caveats are now concrete and budgeted.

**Key findings (from `test-pyramid-strategy.qa-tech-lead.md`, `test-infrastructure-needs.qa-tech-lead.md`, `component-signoff-rubric.qa-tech-lead.md`):**
- **Test infrastructure budget:** 12–18 person-days (accepted by team lead) for six pieces of infra: port allocator, mock Trino fake, `testcontainers-go` integration, differential harness (Java↔Go routing comparison oracle), leak/race/soak rig, and config fixtures. The differential harness is the most architecturally interesting and needs architect design review before Go QA builds it.
- **Go test pyramid shape**: protocol-fidelity tests (differential against Java gateway) and concurrency stress tests are additions the Java suite lacks entirely — they're mandatory for the Go suite, not optional.
- The buffered-not-streaming finding is load-bearing for the Go architecture, not just for testing — it affects the proxy-core design directly.
- **Component sign-off rubric** (5 evidence categories): protocol fidelity, behavioral coverage, concurrency safety, observability, and degradation handling — mapped per-component class, with a decision tree for sign-off.

**Open questions for architect:**
- DI framework choice (affects component wiring and testing isolation)
- Differential harness as oracle vs. spec (determines whether Java gateway must remain running during Go QA phase)
- Buffered-vs-streaming final stance for non-statement paths

---

### 7. Go QA — PROCEED WITH CAVEATS

**Domain:** Go test implementation, HTTP test patterns, race detection, Go test ecosystem fit.

**Position:** Stated explicitly in Task #8 completion: proceed with caveats. Go's test ecosystem fits this domain well.

**Key findings (from `test-infrastructure-inventory.go-qa.md`, `proxy-lifecycle-testable-seams.go-qa.md`, `routing-engine-test-oracle.go-qa.md`, `qa-gaps-and-risks.go-qa.md`):**
- Go's test stack (`net/http/httptest`, `testcontainers-go`, `goleak`, `-race` flag) fits the gateway domain naturally. `httptest.NewRecorder` and `httptest.NewServer` replace `MockWebServer`; `testcontainers-go` replaces `testcontainers-java`; `go test -race` provides race detection the Java suite has no equivalent for.
- The existing Java test data for SQL parsing (`provideTableExtractionQueries` in `TestTrinoQueryProperties`) is largely portable as table-driven test data for whatever Go expression evaluator replaces MVEL.
- **MVEL and trino-parser block ~half of the routing-engine test surface** until the architect picks a replacement strategy. This is the single largest schedule risk for Go QA.
- Four critical Java test gaps that Go QA must address proactively: no concurrency tests, no body-buffering/streaming tests, no log-line/metric assertions, no failed-statement-POST silent fall-through test.
- Go QA gains over Java: goroutine leak detection (`goleak`), race detection (`-race`), deterministic port allocation (`:0`), and `httptest.Server` teardown vs. container lifecycle complexity.

**Risk:** No paired Go QA coverage specs yet for `internal/lifecycle`, `internal/config`, `internal/persistence`. These must land before component sign-off, not after.

---

## Open Questions (Carry Into Architecture Phase)

| # | Question | Owner | Priority |
|---|---|---|---|
| 1 | Operator survey: what % of users actively use `rulesType=FILE` with MVEL? | java-analyst + team-lead | High — before architecture lock |
| 2 | Auth scope for v1: noop + OAuth2 only, or include LDAP? | architect + team-lead | High |
| 3 | Cookie-signing key compatibility: hard-cutover vs soft-cutover? | architect | High |
| 4 | Operator config compatibility: strict YAML compat or migration tool? | architect | High |
| 5 | Architect library divergences: cache lib, migration tool, map concurrency, ticker semantics | architect | Must resolve before code-write |
| 6 | Differential harness: oracle vs spec? Does Java gateway need to keep running during Go QA? | architect + qa-tech-lead | High |
| 7 | Auth behavioral decisions (4 flags from java-analyst): JWKS caching, 302 swallow, 200+401 body, logout no-op | product + architect | Before auth component |
| 8 | `X-Presto-*` alternate header compatibility: fix (behavior change) or replicate bug? | product | Before proxy-core |
| 9 | Side-by-side "preview mode" (Go logs Java's routing decision for comparison): build or skip? | team-lead | Before cutover planning |
| 13 | Expression engine sandboxing: whichever Go engine is chosen (CEL is sandboxed by construction; `expr-lang/expr` requires explicit restriction) must provably prevent rule files from exec'ing subprocesses, reading/writing arbitrary filesystem paths, or opening network sockets — the Java MVEL config intentionally blocks `Process` and `Runtime` imports; this must not regress | architect | Before routing-engine implementation |
| 14 | Gradual cutover strategy: given the silent-failure-mode profile (nextUri breakage, header collapse, redirect-rewrite), the Go gateway should run alongside Java behind a percentage cutover initially — shadow-traffic mode, side-by-side diff, gradual ramp. Plan must be scoped before Phase 3 ships. | team-lead + architect | Before Phase 3 |
| 15 | Startup-time operator check: if `routing.forwardedHeadersEnabled=true` on the gateway but coordinator-side `http-server.process-forwarded=true` appears to be absent, emit a warning (or readyz probe failure). Prevents the most common silent misconfiguration. | go-implementer | Before proxy-core ships |
| 10 | Via header parity: `Via: <proto> TrinoGateway` — byte-identical or allow reformatting? | architect | Before proxy-core |
| 11 | Silent 200 + broken stickiness on malformed upstream body (Gap 4): preserve with new tests, or convert to 502? | architect | Before proxy-core |
| 12 | Go test toolchain: `stdlib testing` (no testify) + `gomock`/hand-rolled + `testcontainers-go` + `goleak` + `golang-migrate` + `vegeta` + `golangci-lint` — settled by go-qa + qa-tech-lead; architect to confirm no objection | architect | Before Phase 2 infra build |

---

## Risks Summary

| Risk | Severity | Mitigation |
|---|---|---|
| Scope creep (MVEL/trino-parser sneak back into v1) | High | Write `SCOPE.md`; team-lead sign-off required to reverse rulings |
| Library divergences cause implementer churn | High | Architect resolves in writing before first component starts |
| Operator migration cost (MVEL rule file users) | Medium | 1-week OSS Slack/GitHub survey before architecture lock |
| External-routing JSON field subset (no trino-parser → empty `tables`/`catalogs`/`schemas`) | Medium | Document precisely; feature flag for affected operators |
| Auth mock IdP needed for Go QA e2e tests | Medium | Small Go HTTP server with rotating RS256 keys + JWKS endpoint |
| Architect agent unresponsive during Task #9 | Low | Mitigated by 7-study corpus and direct team-lead coordination |

---

## Conclusion and Recommendation

**UNANIMOUS RECOMMENDATION: PROCEED WITH CAVEATS**

All seven team members independently reached the same position. The Trino wire protocol is fully portable to Go. The JVM entanglement is real but bounded — concentrated in two features (`routing.rulesType=FILE` with MVEL and trino-parser) behind one config knob, both deferrable to v2 with zero impact on operators using header-based or external routing.

**Why proceed:**
- Minimal v1 (~2,500 LOC, no JVM-dependency port) delivers a statically-linked Go binary with no JVM heap tuning, no Guice startup, no JMX — meaningful operational simplification.
- The rewrite fixes two real latent bugs: response body truncation/corruption for spooled-segment modes, and per-request JWKS fetching with no caching.
- Go's test tooling (`-race`, `goleak`, `:0` ports, `httptest`) provides correctness guarantees the Java suite cannot offer.
- The Go rewrite is also an opportunity to fix four auth behavioral quirks documented by java-analyst, improving UX without breaking compatibility.

**Conditions for proceeding:**
1. MVEL file-based rules **out** of v1 scope.
2. `trino-parser` SQL content routing **out** of v1 scope.
3. Architect resolves all four library divergences in writing before any component implementation begins.
4. Four auth behavioral decisions made explicitly by product before auth component coding.
5. Operator config compatibility ruling (strict vs. migration tool) made before `internal/config` is started.
6. `SCOPE.md` written and maintained — reversing any scope ruling requires explicit team-lead sign-off.

**If any condition cannot be met:** defer until it can. The rewrite is low-risk only with the v1 scope discipline intact.

---

*Study corpus: 30+ files in `studies/` — see `studies/CONVENTIONS.md` for index structure.*  
*Key studies: `studies/both/jvm-bound-protocol-nuances.trino-expert.md`, `studies/trino-gateway/jvm-dependencies-inventory.md`, `studies/trino-gateway/mvel-rules-language.md`, `studies/both/gateway-coordinator-nexturi-contract.md`, `studies/trino-gateway/test-pyramid-strategy.qa-tech-lead.md`, `studies/both/test-infrastructure-needs.qa-tech-lead.md`.*
