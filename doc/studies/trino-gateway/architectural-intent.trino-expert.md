---
title: trino-gateway's architectural intent — what problems it actually solves
author: trino-expert
role: Trino & Trino-Gateway Expert
component: trino-gateway
topics: [cross-cutting, proxy-core, routing-engine, cluster-registry]
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/architecture-overview.md
  - trino/statement-protocol-overview.md
  - both/sticky-routing-contract.md
  - both/gateway-coordinator-nexturi-contract.md
---

# trino-gateway's architectural intent — what problems it actually solves

## Summary

trino-gateway is not a generic HTTP reverse proxy; it exists because the Trino client/coordinator protocol is **stateful, long-lived, and slug-authenticated**, and a stateless L4/L7 load balancer in front of Trino either breaks queries (wrong cluster on subsequent polls) or forces single-cluster deployments. Everything else the gateway does — query history, routing rules, cluster monitoring, blue/green upgrades — is incremental value layered on top of that one load-bearing job: **route every request for queryId X to the same backend cluster that accepted the POST for X**. A Go rewrite that gets sticky-routing right and nothing else would still be useful; a Go rewrite that gets every other feature right but breaks stickiness would be unshippable.

## Key Findings

- The official documentation lists four use cases (`trino-gateway/docs/index.md:9-16`):
  1. Single connections URL across multiple Trino clusters.
  2. Automatic per-query routing to dedicated clusters (workload / data-source isolation).
  3. No-downtime blue/green or canary cluster upgrades.
  4. Transparent capacity changes (add/remove clusters without restarting clients).

  Each is restated below in terms of the Trino protocol constraints that drive the implementation.

### Use case 1: single connection URL → solves the "Trino client expects one cluster" problem

- Trino clients (JDBC, ODBC, Python `trino-python-client`, CLI) take exactly one `host:port` and bind to it for the duration of every query. They have no concept of "try another coordinator on failure" or "round-robin across coordinators."
- The gateway's job here is to be the **one URL the client targets**, and silently spread queries across N backend coordinators.
- This is the trivial part — it's what any reverse proxy can do for the *initial* POST. The non-trivial part is keeping that masquerade intact for the polling requests that follow, which is use case 1's hidden tax.

### Use case 2: routing to dedicated clusters → solves the "different SLAs need different topologies" problem

- Real Trino deployments separate clusters by workload: ad-hoc BI, large ETL, ML feature backfills, customer-facing dashboards. Each cluster is sized and tuned differently (memory limits, spill config, connector quotas).
- The decision of "which cluster" depends on signals the client did not consciously provide: SQL shape (DDL vs SELECT vs INSERT), referenced catalogs, source-application tags, user identity.
- The gateway extracts these signals at request time and runs them through a routing rules engine (`X-Trino-Routing-Group` header, MVEL rules file, or external HTTP rules service) before picking a backend. See `studies/trino-gateway/routing-engine-test-oracle.go-qa.md` and the planned `routing-engine.md`.
- **Why this can't live in the client:** clients are shipped by Trino, not by the gateway operator; the operator cannot push routing logic into them. The gateway is the only edge the operator owns.

### Use case 3: blue/green upgrades → solves the "Trino has no in-place hot upgrade" problem

- Trino has no support for rolling coordinator upgrades within a single cluster — the coordinator is a singleton. To upgrade Trino, operators stand up a new cluster, drain the old one, switch traffic.
- The gateway makes the switch invisible: mark old cluster `deactivated`, new cluster `active`, then wait for in-flight queries to drain via the existing 30-minute sticky-routing cache.
- Source: `gateway-ha/.../HaGatewayManager.java` (the `activate`/`deactivate` admin endpoints), `gateway-ha/.../BaseRoutingManager.java:257-262` (cache TTL allows drain window).
- **Why this is a stickiness consumer, not a stickiness feature:** the gateway didn't invent a "draining" mode; it leverages the queryId→backend cache to keep already-started queries on the old cluster while sending new POSTs to the new cluster.

### Use case 4: capacity changes → solves the "Trino client connections are long-lived JDBC pools" problem

- Production clients hold JDBC connections in a pool that lives for hours or days. If a cluster's `host:port` is hardcoded in the JDBC URL, adding a cluster requires reconfiguring every client.
- The gateway's backend registry (`gateway_backend` table + `GatewayBackendManager` REST endpoints) lets operators add/remove clusters at runtime without touching clients.
- Source: `gateway-ha/.../HaGatewayResource.java`, `gateway-ha/.../HaGatewayManager.java` (admin REST surface).

### What the gateway does NOT do (and why)

These are negative-space findings — features the gateway intentionally omits, which a Go rewrite must also resist adding:

- **No query result caching.** Trino results are large, often single-use, and may carry row-level security context. Caching them at the gateway breaks both security and freshness assumptions.
- **No query rewriting.** The gateway does not modify SQL text. The closest it comes is parsing SQL for routing-decision input (`TrinoQueryProperties`), but it forwards the original text byte-for-byte.
- **No request body rewriting beyond pass-through.** Bodies of POSTs are buffered to extract queryId from responses (and to scan for `kill_query` in requests), but the bytes sent to the backend are byte-identical to what the client sent.
- **No response body rewriting.** `nextUri` and other URIs in `QueryResults` JSON are NEVER touched — see `studies/both/gateway-coordinator-nexturi-contract.md`.
- **No retry of failed backend calls.** A 5xx from the backend goes straight to the client. The client retries (Trino protocol contracts 502/503/504 are retryable from the client's perspective). See `trino/docs/src/main/sphinx/develop/client-protocol.md:28-37`.
- **No multi-cluster query fan-out, no query splitting, no cross-cluster joins.** The gateway is a 1:1 router; one client request maps to one backend cluster call.
- **No persistent queue.** Queries that arrive when no backend is healthy are routed to a backend and fail, not queued.
- **No state shared across gateway instances.** The queryId→backend cache is in-process; multi-instance HA depends on upstream LB sticky sessions or `TG.*` cookies, NOT on a shared backend.

### What the gateway adds beyond stickiness

In rough order of "necessary for a usable v1" to "nice to have":

1. **Sticky-routing cache** (load-bearing — drop and the system doesn't work).
2. **Backend registry + cluster activation/deactivation** (load-bearing — needed for use cases 3 & 4).
3. **Cluster health monitoring + auto-deactivation of unhealthy backends** (`ActiveClusterMonitor`, 6 monitor variants).
4. **Routing rules engine** (header / file-based MVEL / external HTTP).
5. **Query history persistence** (one row per query in `query_history`; powers observability/audit).
6. **Authentication and authorization layer** (5 modes: noop, basic, form, LDAP, OAuth2/OIDC).
7. **Admin REST + Web UI** for operator workflows.
8. **Gateway-cookie sticky routing** for sessions whose queryId isn't known yet (OAuth2 redirects).
9. **JMX / Prometheus metrics** (for ops dashboards).

A Go rewrite that ships items 1-4 is a functional v1. Items 5-9 are scope choices for the architect.

## Behavior vs. Implementation Artifact

### The "single connection URL" promise is the source of all complexity
- **Observed behavior:** Clients see one `host:port`; the gateway invisibly multiplexes them across many backends. This single design choice is what makes the gateway necessary at all.
- **Source of behavior:** `gateway-design-intent`.
- **Rationale:** Trino client SDKs cannot be reshipped to operators on demand. The gateway is the only edge where the operator can intervene.
- **Go obligation:** `replicate-exactly` at the wire level. Clients must continue to see one URL.
- **Notes:** The cost of this promise is the sticky-routing cache, the per-query state, and the requirement that the gateway never appears to "lose" a query mid-protocol. Every architectural choice in the system traces back to this.

### Use cases 3 (blue/green) and 4 (capacity changes) reuse sticky-routing as a draining mechanism
- **Observed behavior:** Operators flip backend state from `active` to `deactivated`; the gateway stops routing NEW queries to it but continues to honor queryId→backend mappings for in-flight queries. After ~30 minutes (cache TTL), the old cluster can be safely shut down.
  Source: `gateway-ha/.../BaseRoutingManager.java:257-262`, `gateway-ha/.../HaGatewayManager.java` (state transitions).
- **Source of behavior:** `gateway-design-intent`. The two-state model (`active`/`deactivated`) is intentional; "draining" isn't a separate state — it's an emergent property of the cache TTL.
- **Rationale:** Simplest possible model that works. No background draining job; cluster simply stops receiving new work and waits out the queryId mappings.
- **Go obligation:** `replicate-intent`. The Go gateway must preserve the same two-state model AND the property that `deactivated` backends continue receiving requests routed via the queryId cache. A "cleaner" three-state model (`active`/`draining`/`down`) would be a needless schema change.
- **Notes:** The 30-minute cache TTL is therefore a load-bearing config value for blue/green upgrades, not just a performance knob. Document it.

### Routing rules are an operator extension surface, not a Trino protocol feature
- **Observed behavior:** The MVEL rules language and `TrinoQueryProperties` SQL parsing exist solely so operators can write routing decisions in terms of the SQL being submitted.
  Source: `studies/trino-gateway/mvel-rules-language.md`, `gateway-ha/.../TrinoQueryProperties.java`.
- **Source of behavior:** `gateway-design-intent`. Trino itself doesn't expose "what catalogs does this query touch" outside its own internal planner.
- **Rationale:** Operators need to make routing decisions based on query characteristics; the gateway extracts those characteristics so rules can be expressed.
- **Go obligation:** `replicate-intent`. The Go gateway needs an equivalent rule-input extractor and an equivalent rules engine. The Java implementation choices (MVEL, trino-parser) are heavy JVM-bound dependencies; see [[../trino-gateway/jvm-dependencies-inventory.md]] and the go/no-go discussion.
- **Notes:** This is the single largest "scope vs. simplicity" trade in the rewrite. A Go gateway could ship with header-only and external-HTTP routing in v1 and add file-based rules in a later milestone — and ~80% of operator value would still land.

### Health-monitoring with 6 monitor variants is breadth, not depth
- **Observed behavior:** `ClusterStatsMonitor` has implementations for HTTP `/v1/info`, JDBC, JMX, Prometheus metrics endpoint, plus a NoOp.
  Source: `gateway-ha/.../clustermonitor/` package, surveyed in [[architecture-overview.md]].
- **Source of behavior:** `gateway-design-intent` + `defensive-historical`. Different deployments expose different telemetry surfaces, so several monitor types exist to fit the environment.
- **Rationale:** Operators can choose the monitor that matches what their Trino clusters already expose. The InfoApi monitor is the modern default; the others exist for environments where InfoApi is unavailable or unauthenticated.
- **Go obligation:** `replicate-intent`. v1 of the Go gateway needs at least the InfoApi monitor (HTTP probe of `/v1/info`). Other variants can be deferred unless [[../trino-gateway/jvm-dependencies-inventory.md]] flags them as required for some named user.
- **Notes:** None of the monitors are protocol-critical — a degraded monitor only causes the gateway to route to unhealthy clusters until the failure shows up as 5xx, after which the cluster gets auto-deactivated by a separate path.

## Implications for Go Rewrite

- **The minimum viable Go gateway is: HTTP server + sticky-routing cache + backend registry (in-memory or DB-backed) + InfoApi health monitor + header-based routing-group selection.** Roughly 1,500-2,500 LOC of idiomatic Go, against ~13.6k LOC of Java. This is the floor.
- **The maximum reasonable v1 Go gateway adds: query history persistence + external-HTTP routing rules + at least one auth mode (likely OAuth2/OIDC) + minimal Web UI for backend status.** Probably 5,000-7,000 LOC.
- **Features explicitly worth deferring past v1:** MVEL file-based rules (JVM-bound), JDBC/JMX/Prometheus monitor variants (operator can use the InfoApi monitor), LDAP auth (operator can use OAuth2 instead), Web UI beyond cluster-list-and-status, `modules:`/`managedApps:` extension points (no Go equivalent).
- **The blue/green-upgrade use case demands the 30-minute sticky-routing TTL.** Don't tune this down to "save memory" — that's not the cost being optimized.
- **The "no body rewriting" discipline must be a project-level invariant**, codified in code review, not just a coding convention. Any PR that JSON-parses a response body in the proxy path needs explicit justification.
- **The gateway operator is the user, not the Trino end-user.** Routing rules, backend registry, health policies, auth — all of these serve operator goals. The Trino client should be unaware the gateway exists.

## Test Strategy Hooks

- **Test level:** n/a — this is an intent/scoping survey, not a behavioral spec.
- **Fixtures required:** n/a.
- **Observable signals:** n/a. See per-feature studies for specific signals (sticky-routing cache hits, routing-rule outputs, health-monitor probes, etc.).
- **Non-determinism risks:** n/a at this level.

## Open Questions

- Are there production deployments today where use cases 1+2 alone (single URL + workload routing) carry all the value, with blue/green upgrades unused? If yes, the 30-minute TTL knob becomes a tuning question rather than a load-bearing constant. `@trino-expert` self-note: ask in the OSS channel.
- Is there demand for a "preview" routing mode — gateway evaluates rules but ALSO submits to the previous backend for differential testing? Out of v1 scope, but worth knowing if it's been requested. `@architect`.
- Should the Go gateway expose a "describe-routing-decision" endpoint (`/v1/gateway/explain?user=...&sql=...`) for operator debugging? This is a missing affordance in the Java gateway and would meaningfully improve operator UX. `@architect`.
- What proportion of trino-gateway users actively use the MVEL file-based rules vs. external-HTTP rules vs. header-only? Determines how much pressure there is to ship MVEL in v1. `@trino-expert` self-note: WebSearch in Task #9.

## Cross-references

- [[architecture-overview.md]] — the package-level structural overview (java-analyst).
- [[../trino/statement-protocol-overview.md]] — the wire-level protocol the gateway exists to multiplex.
- [[../both/sticky-routing-contract.md]] — the queryId→backend cache that makes use case 1 work.
- [[../both/gateway-coordinator-nexturi-contract.md]] — the cross-system contract that makes "single URL" work.
- [[mvel-rules-language.md]] — JVM-entanglement deep-dive on file-based routing rules.
- [[jvm-dependencies-inventory.md]] — what would have to be replaced or dropped in a Go port.
