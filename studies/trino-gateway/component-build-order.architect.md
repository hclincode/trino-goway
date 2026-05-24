---
title: Component build order for the Go rewrite
author: architect
role: Architect / Tech Lead
component: trino-gateway
topics:
  - cross-cutting
  - proxy-core
  - routing-engine
  - cluster-registry
  - persistence
  - mgmt-api
  - config
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/rewrite-hotspots.md
  - trino-gateway/concurrency-model.architect.md
---

# Component build order for the Go rewrite

## Summary

The team-lead's initial suggested order (proxy core → registry → routing → persistence → mgmt API → config → UI) is almost right. Two refinements: (1) **config** must come first, not last — every component reads it; (2) **persistence** must come before **registry**, because the registry needs durable backend storage. Otherwise the spine is right: each component produces a testable slice and unblocks the next. The deliberate decision baked into this order: pick the cheapest path to "the Go gateway can serve one query end-to-end against a fixed cluster" early, then add the variability (multiple clusters, routing rules, sticky cookies, auth, UI) on top. This is the order in which the Go Implementer should turn green CI checks on.

## Key Findings

### Final recommended order

1. **Config** — load YAML into a typed struct tree, env-var substitution, validation. No moving parts; everything depends on this.
2. **Persistence** — `database/sql` + `sqlx` + Postgres connection management. Migration runner (golang-migrate against existing `V?__*.sql`). Tables: `gateway_backend`, `query_history`. Tested against a `testcontainers-go/postgres`.
3. **Cluster registry** (DAO-wrapped) — `BackendRegistry` interface + impl backed by the `gateway_backend` table. CRUD via the management API path (later). For phase 1, registry can be config-seeded.
4. **Health checks (probes) + monitor loop** — `HealthProbe` interface + `INFO_API` impl + the single-goroutine `ActiveClusterMonitor`-equivalent. Updates the registry's per-backend `TrinoStatus`.
5. **Proxy core** — `net/http/httputil.ReverseProxy` with custom `Director` and `ModifyResponse`. Forwards everything; statementPaths are explicit. At this stage, routes everything to a hardcoded single backend.
6. **Query→backend binder** — `QueryBinder` interface + LRU-cache impl (in-memory only at first; persisted to `query_history` table second). Triggered by `ModifyResponse` for new POST `/v1/statement` responses; extracts `id` from JSON body.
7. **Backend selector** — `BackendSelector` interface + `StochasticBackendSelector` impl. Picks a healthy backend from the registry. Replaces the hardcoded single-backend wiring from step 5.
8. **Routing-group selector — header-based** — minimal `RoutingGroupSelector` that just reads `X-Trino-Routing-Group`. Composes with `BackendSelector` from step 7.
9. **Routing-group selector — rules engine (file-based)** — adds the `expr-lang/expr`-based rules evaluator. Conditional on `routingRules.rulesEngineEnabled`. Depends on step 11 if rules use `trinoQueryProperties`.
10. **Routing-group selector — external HTTP** — adds the HTTP-callout transport. Composes the same way.
11. **Query analyzer (SQL parser)** — `QueryAnalyzer` interface + ANTLR4-generated Trino parser + visitor. Populates `TrinoQueryProperties`. Conditional on `requestAnalyzerConfig.analyzeRequest`. Required for rules that reference SQL-level data.
12. **Sticky routing (cookies + queryId cache)** — gateway-signed cookies for sticky-by-cluster routing. `CookieSigner` + `GatewayCookie` types. Composes with the proxy core.
13. **Auth** — auth filters chain (basic, form, OIDC, JWT, LDAP). `Authenticator` interface + impls. Composes as middleware in front of the proxy and management endpoints.
14. **Management API** — REST endpoints for backend CRUD, query history queries, health-check view. Uses the registry, persistence, and auth from earlier steps.
15. **Web UI** — static files + admin views. Last because it depends on every prior component being stable.

### Why the team-lead's order needed two tweaks

- **Config first.** The team-lead's order put config 6th; that doesn't survive contact with the codebase. Every constructor in the Java impl takes `HaGatewayConfiguration` (or a sub-config). In Go, every `New*` function will take a typed config struct. Building config first means each subsequent component has a stable input shape.
- **Persistence before registry.** The team-lead's order put persistence 4th and registry 2nd. In the Java impl, `HaGatewayManager` (the registry) is backed by `GatewayBackendDao` (a JDBI SQL Object), which needs a working DB connection. Building registry without persistence forces a temporary in-memory shim that the persistence step has to retrofit out — wasted motion.

### What each phase produces

A short statement of the smallest demo each phase enables — Go Implementer should use these as "definition of done" markers for the phase:

| Phase | Demo |
|---|---|
| 1. Config | `./trino-goway --config example.yaml` parses YAML, validates, prints loaded struct, exits 0. |
| 2. Persistence | Migrations run against a local Postgres; `SELECT * FROM gateway_backend` returns the seeded row. |
| 3. Cluster registry | `GET /api/backends` returns the rows from `gateway_backend`. |
| 4. Health checks | After a tick, registry shows each backend's `TrinoStatus`. Probes happen on a 1-minute interval. |
| 5. Proxy core | `POST /v1/statement` against the gateway forwards to a hardcoded backend and returns its response unchanged. |
| 6. Query binder | After a `POST /v1/statement`, the queryId→backend map is populated; a subsequent `nextUri` poll for that queryId routes to the same backend. |
| 7. Backend selector | A `POST /v1/statement` with no prior binding picks one of the healthy backends in the registry stochastically. |
| 8. Header-based routing group | `X-Trino-Routing-Group: adhoc` lands the query on a backend with `routingGroup=adhoc`. |
| 9. Rules engine | A simple rule (`condition: 'source == "tableau"', actions: ['result.put("routingGroup", "bi")']`) routes a Tableau-sourced query to the `bi` group. |
| 10. External routing | A rules-external HTTP server returns `{"routingGroup": "x"}` and the gateway honours it. |
| 11. Query analyzer | A rule referencing `trinoQueryProperties.catalogs.contains("ml")` correctly routes queries that touch the `ml` catalog. |
| 12. Sticky cookies | After a query lands on cluster A, subsequent requests with the gateway cookie continue to land on A. |
| 13. Auth | Basic auth with valid credentials succeeds; OIDC discovery → login → ID-token verification round-trips correctly. |
| 14. Management API | Admin can CRUD backends via the UI; query history is searchable. |
| 15. Web UI | Admin dashboard renders backend statuses, recent queries, login. |

### Parallel-work seams

Some pairs of phases can be built concurrently by different engineers without blocking each other. Useful if the team grows beyond go-implementer:

- Phases 2 + 4 (persistence + health probes against a stub registry interface) — different files, different libraries.
- Phases 9 + 11 (rules engine + query analyzer) — both depend on the proxy core but are independent of each other if rules don't yet reference `trinoQueryProperties`.
- Phase 13 (auth) is largely independent of phases 5–12 — it's middleware composed in `main.go` and can be built against a stub handler.
- Phase 15 (Web UI) can start as soon as phase 14 (management API) is stable.

### What blocks what

Critical-path chain (each entry must complete before the next can finish):

```
config → persistence → registry → proxy core → query binder → backend selector
                                                                      ↓
                                              header-based routing group selector
                                                                      ↓
                                                          rules engine ←→ query analyzer
                                                                      ↓
                                              sticky cookies + management API + auth
                                                                      ↓
                                                                  web UI
```

The dependency graph is mostly linear with a small fork at routing engine / query analyzer.

## Behavior vs. Implementation Artifact

### Single-backend hardcoding in phase 5
- **Observed (planned) behavior:** the phase-5 proxy core does not consult the registry; it forwards to a backend URL hardcoded into the config.
- **Source of behavior:** `gateway-design-intent` — *deliberate* simplification for phase ordering. Lets us validate the proxy path end-to-end before the registry has any data.
- **Go obligation:** `drop` once phase 7 is complete. The hardcoded backend wiring should be deleted, not left as a config-flag fallback.
- **Notes:** this is the only phase whose output is *throwaway*; every other phase's code survives into the final binary.

### Phase 11 (query analyzer) is opt-in even after it's built
- **Observed behavior in Java:** `requestAnalyzerConfig.analyzeRequest` defaults to `false`. When false, the SQL parser is never invoked; rules don't have access to `trinoQueryProperties`. Most deployments leave it off.
- **Source of behavior:** `gateway-design-intent` — parser is expensive and most deployments don't need SQL-aware routing.
- **Go obligation:** `replicate-exactly`. Default `analyzeRequest = false`; skip parser construction when off. Important for our ordering: phase 11 can be punted to later (after MVP) if the initial deployment doesn't need it.

## Implications for Go Rewrite

- **Library:** the order constrains library selection per phase, but no surprises beyond what's in [[library-landscape-go-mapping]]. The one ordering-driven library decision: pick the cache library (LRU vs ristretto) before phase 6, since the query binder is the first cache user.
- **Interface:** each phase produces one or two new interfaces. They land in this order:
  1. `Config` (concrete struct)
  2. `*sqlx.DB` (concrete) + `DAO` types
  3. `BackendRegistry interface`
  4. `HealthProbe interface`, `ClusterMonitor` (concrete)
  5. `Proxy http.Handler` (concrete)
  6. `QueryBinder interface`
  7. `BackendSelector interface`
  8. `RoutingGroupSelector interface`
  9. (impl of `RoutingGroupSelector`, not a new interface)
  10. (impl)
  11. `QueryAnalyzer interface`
  12. `CookieSigner interface`
  13. `Authenticator interface`, `Authorizer interface`
  14. management resource handlers (concrete)
  15. static file serving (concrete)
- **Concurrency:** the monitor loop appears in phase 4, well before the proxy core in phase 5. This is intentional — it shakes out the `Start(ctx)`/`Stop(ctx)` lifecycle conventions in a small component before the proxy core inherits them.

## Test Strategy Hooks

- See paired QA studies: [[component-sign-off-rubric-for-the-go-rewrite]], [[go-test-pyramid]].
- Architect-relevant test-ordering concerns:
  - Each phase's "definition of done" demo above should have an end-to-end test that exercises *that demo* and runs in CI. These tests grow incrementally; by phase 15 we have ~15 e2e tests covering the whole stack.
  - QA Tech Lead should sign off on each phase's test set before the next phase starts. This is the cross-team gate (architect ↔ qa-tech-lead) explicitly called out in the architect role prompt.
  - Differential tests (Go gateway vs Java gateway against the same recorded HTTP corpus) become possible starting at phase 5. Earlier phases are unit-only.

## Open Questions

- @qa-tech-lead: do we want a hard rule that "phase N+1 cannot start until phase N's e2e demo is green in CI"? Loose answer is yes; tight answer might be too restrictive given parallel-work seams. Discuss before phase 5.
- @architect (self): for phases 9 + 11 (rules engine + query analyzer), the dependency direction is "rules can use analyzer output". Should phase 11 ship first, even though it's larger, so phase 9 can integrate with it immediately? Probably yes, but it pushes the rules engine demo back. Decide at the time.
- @go-implementer: any concerns with the proposed phase boundaries? Anything that should be one phase but is split, or vice versa?

## Cross-references

- [[architecture-overview.architect.md]] — the system this order is building toward
- [[rewrite-hotspots.md]] — the libraries that gate phases 9 and 11
- [[concurrency-model.architect.md]] — the lifecycle pattern that phase 4 establishes
- [[library-landscape-go-mapping.md]] — per-phase library picks
