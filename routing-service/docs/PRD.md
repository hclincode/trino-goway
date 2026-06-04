# Product Requirements Document — routing-service

**Date:** 2026-06-04
**Status:** Draft — for review (output of a 4-perspective team discussion)
**Participants:** trino-expert · gateway-expert (trino-gateway) · goway-expert (trino-goway) · saas-tech-lead (Trino user / SaaS app)
**Decision basis:** the gRPC contract in `internal/routing/routerpb/router.proto` (`trino.gateway.v1`), `internal/routing/external_grpc.go`, `docs/PRD.md` §Routing Strategy, and the trino-gateway routing-rules docs/studies.

---

## 1. What this is

`routing-service` is a **standalone Go gRPC service** that acts as the **external routing selector** for the [trino-goway](../../README.md) gateway. For each new Trino query, the gateway calls the service over gRPC and uses the returned **routing group** to decide which cluster group serves the query. The service is where the routing *logic* lives — the role that the Java gateway's MVEL file-rules engine used to fill, but which trino-goway deliberately externalizes.

**Phase 1 is gRPC, in Go.** HTTP transport, mTLS, and a config-write API are later phases.

```
Trino client ──> trino-goway (gateway) ──gRPC Route()──> routing-service
                       │                                      │ (evaluates rules in memory)
                       │ <────── routing_group ───────────────┘
                       └──> selects a cluster in that group ──> Trino cluster
```

---

## 2. Goals

- Implement the `TrinoGatewayRouter` gRPC contract so trino-goway can use it as `routing.external.grpcAddr`.
- Decide the **initial routing group** for new query submissions from request metadata (source, client tags, user, catalog/schema), via a **declarative, hot-reloadable rule set**.
- Be **fast and fail-safe**: in-memory evaluation, no hot-path I/O, and never the cause of a user-visible error.
- Support the SaaS operator's core needs: **per-tenant / interactive-vs-batch / SLA-tier routing**, and **weighted canary / blue-green** with live, validated config changes.

## 3. Non-goals (Phase 1)

- **No sticky / queryId routing.** The gateway owns query→cluster affinity (LRU cache + recovery chain). The service only sees and acts on *new* submissions and must ignore non-new requests.
- **No intra-group cluster selection / load balancing.** The service returns a *group*; the gateway picks the cluster within it. (Replicating cluster-load stats here would duplicate the gateway's monitor and make the service stateful — explicitly rejected.)
- **No SQL parsing / table-aware routing.** No production-grade Go Trino parser exists; the parser-dependent fields arrive empty (see §5). Table/catalog-AST routing is unavailable in v1 and must be documented as such.
- **No HTTP transport, no mTLS, no config-write API** (all later phases).

---

## 4. The contract (gRPC `TrinoGatewayRouter`)

Service: `trino.gateway.v1.TrinoGatewayRouter`, RPC `Route(RouteRequest) returns (RouteResponse)`. The service vendors a **copy of the `.proto`** (not the gateway's generated Go package) and runs its own `protoc`; the proto is the stable wire contract.

**Inputs the service relies on** (from `RouteRequest`):
- `trino_request_user.user` — from `X-Trino-User` (per-user routing/quotas).
- `trino_query_properties.default_catalog` / `default_schema` — from `X-Trino-Catalog`/`X-Trino-Schema`.
- `trino_query_properties.body` — raw SQL (regex/prefix heuristics only; best-effort).
- `trino_query_properties.is_new_query_submission` — **the gate**: only `true` (POST `/v1/statement`) should produce a routing decision.
- `method`, `request_uri`, `remote_addr`/`remote_host`, `parameter_map`.

**Response semantics** (`RouteResponse`):
- `routing_group` — the chosen group. **Empty/absent is a valid "defer to gateway default" signal, NOT an error.** The gateway resolves empty → `routing.defaultGroup`; the service must never treat "no rule matched" as an error.
- `external_headers` — **ADD/OVERRIDE** semantics on the proxied request (replace matching keys, add new, leave others untouched). The gateway filters `excludeHeaders` — the service need not know that list. Use canonical `X-Trino-*` names (X-Presto-* are legacy aliases).
- `errors` — **reserved for hard policy violations only** (e.g. "user not permitted", "restricted-data deny-list"), because `propagateErrors=true` turns these into an HTTP 400 to the client. Never use `errors` for "no rule matched" or "group not found".

### 4.1 Required proto additions (trino-goway dependency) ⚠️

The two **most-used routing signals in practice are missing from the current proto**, and the gRPC transport forwards *only* structured fields (not raw inbound headers). Phase 1 therefore requires a coordinated **additive, backward-compatible** change to `trino.gateway.v1` in trino-goway, plus the gateway populating them:

- `string trino_source` — `X-Trino-Source` (e.g. `airflow`, `superset`, `dbt`). The primary routing key in nearly every real rule set.
- `repeated string client_tags` — `X-Trino-Client-Tags`, **pre-split** on comma at the gateway (avoids every router re-parsing the ambiguous format).

Reserved for later (not Phase 1): `RouteResponse.resource_group_hint` (inject as `X-Trino-Resource-Group`), and an optional tenant identifier field if header-derived tenancy proves insufficient. These are additive and need only a field number reserved now.

> **Action for trino-goway:** add `trino_source` + `client_tags` to `RouteRequest`/`TrinoQueryProperties` and populate them in the gRPC `buildProtoRequest`. Tracked as a gateway-side dependency for this project.

---

## 5. Phase 1 scope (gRPC, Go)

1. **gRPC server** implementing `TrinoGatewayRouter.Route` + `grpc.health.v1.Health` (Check + Watch). Insecure transport (matches the gateway client today). Graceful shutdown (`GracefulStop`).
2. **Rule engine** — declarative YAML rules with **CEL** (`google/cel-go`) conditions:
   - Each rule: `name` (req), `priority` (int, default 0), `condition` (CEL→bool), `routing_group` (string) or `routing_group_expr` (CEL→string, computed group name — see §6.1), optional `external_headers` (map).
   - Evaluation matches the Java engine: **all matching rules run in ascending priority; last writer wins** (no first-match short-circuit). Service-level `default_routing_group` for the no-match case.
   - CEL context is a flat struct over the proto: `request.source`, `request.client_tags`, `request.user`, `request.catalog`, `request.schema`, `request.method`, `request.uri`, `request.remote_addr`, `request.body`. CEL chosen for being **sandboxed by construction** (no process/fs/network) and load-time type-checkable.
   - `state` cross-rule map: documented as a known gap for v1 (most real rules don't need it); add later if demanded.
3. **Weighted routing (canary / blue-green)** — a rule may resolve to a **weighted split** across groups (e.g. `stable: 95, canary: 5`), addressable by tenant or as a random percentage. Weights are live-reloadable; setting a weight to 0 is instant rollback (applied on the next request, no grace period).
4. **Dynamic config (hot-reload)** — watch a config source (file via fsnotify in Phase 1); on change: **parse + validate against schema BEFORE activating**; on failure keep the last-known-good config live and emit an error metric + structured log with the failing diff; on success emit an **audit event** (timestamp, trigger, old/new config hash, changed-rule summary). Provide a **dry-run** path (CLI/sidecar) that reports which sample requests would route differently.
5. **Fail-safe behavior** — no rule match → `default_routing_group` (first-class outcome). Invalid/empty config **at startup** → health `NOT_SERVING` (never serve an empty rule set). Treat `is_query_parsing_successful=false` / the `"trino-parser not available"` `error_message` as normal, never as an error signal.
6. **Validation at load** — group names exist in the operator's group registry (or warn), canary weights sum to 100, no circular references, over-broad conditions warned (not blocked), tenant-scope isolation enforced (see §8).
7. **Observability** — Prometheus metrics, structured decision logs, OpenTelemetry tracing (see §7).
8. **Config & ops docs** — README, rule-config reference, a **migration guide** from MVEL `routing_rules.yml`, and a **Python reference implementation** of the `TrinoGatewayRouter` contract (the sanctioned path for procedural / other-language routing, §6.1).

### Later phases (recorded, not committed)
- **HTTP transport** (the gateway supports both; operators may run both as belt-and-suspenders).
- **mTLS** — gateway swaps `insecure.NewCredentials()` → `credentials.NewTLS(...)` (one line); service accepts optional TLS at startup. Config: `grpcCertFile`/`grpcKeyFile`/`grpcCAFile`.
- **Config-write API** — role-gated (mTLS + tenant/admin-scoped JWT), tenant-namespaced authoring, replacing/augmenting file-based config.
- **`resource_group_hint`** response extension; **SQL/table-aware routing** if a maintained Go Trino parser appears or via a parsing sidecar.

---

## 6. Routing rule model (config sketch)

```yaml
default_routing_group: adhoc          # service fallback for "no rule matched"
rules_refresh_period: 30s             # hot-reload cadence (plus fsnotify on change)

rules:
  - name: airflow-etl
    priority: 0
    condition: 'request.source == "airflow"'
    routing_group: etl
    external_headers: { X-Trino-Client-Tags: "etl" }

  - name: superset-interactive
    priority: 0
    condition: 'request.source == "superset"'
    routing_group: interactive

  - name: premium-tenant
    priority: 10
    condition: 'request.user.startsWith("acme-")'
    routing_group: premium

  - name: canary-rollout                # weighted split (blue/green)
    priority: 20
    condition: 'request.source == "superset"'
    weighted_groups: { interactive: 95, interactive-canary: 5 }
```

`default_routing_group` here is the *service's* default; it should match the gateway's `routing.defaultGroup` unless the operator intentionally diverges — document the coupling.

### 6.1 Scriptability decision — declarative-first, polyglot for procedural

**Question raised:** should cluster admins author routing logic via an embedded script/expression engine (a Go MVEL-like/templating lib, or Python)?

**Decision (team consensus): no general-purpose embedded scripting engine in v1.** v1 stays declarative — YAML rules + CEL. Rationale and the sanctioned paths:

1. **CEL already covers ~95% of real routing.** Across all seven Java MVEL rule fixtures, every pattern maps cleanly to declarative CEL; the two apparent gaps — the cross-rule `state` map and Java `Optional` wrappers — dissolve in the new model (rewrite as a compound condition; compare plain strings). `state` appears once, pedagogically; **do not model it in v1**.
2. **One scoped extension covers the dynamic cases:** an optional **`routing_group_expr`** rule field — a CEL expression that *returns* the group name (e.g. `'"etl-" + request.user.split("@")[1]'`) for domain/team-computed groups. Stays inside CEL's sandbox/termination guarantees; it is **not** "scripting."
3. **For genuinely procedural logic in any language — including Python — use the polyglot gRPC boundary, not an embedded engine.** The gateway↔service contract is already gRPC, so a team can implement `TrinoGatewayRouter` in Python (`grpcio`), JVM, Node, etc. and point trino-goway at it. v1 ships a **Python reference implementation** alongside the Go one. This is the sanctioned "use Python for routing logic" answer — no CGo, no sidecar, no embedded interpreter.
4. **Why not embed a scripting engine now:**
   - **Shared-tenant hot path = blast radius.** A buggy/expensive script (ReDoS, runaway loop, heavy allocation) degrades routing latency for *all* tenants, can trip the gateway circuit breaker, and silently falls everyone to the default group.
   - **Auditability.** A YAML rule diff is reviewable in a PR / at 2 am during an incident; a procedural-script diff requires mentally executing code.
   - **Security surface.** Every engine version bump must be re-audited for sandbox escapes; needs a threat model.
   - **Real Python is worst in-process:** embedded CPython (CGo) is non-viable (GIL serialization, ~8 MB RSS/interpreter, 50–200 ms startup); a Python sidecar only adds a hop and is redundant with writing the router in Python directly.
5. **If a scripting tier is ever added (v2+),** it is gated behind demonstrated demand + a threat-model + sandbox spec, and scoped: **platform-admins only** (never tenant admins), sandbox (no I/O/network), **hard step + wall-clock limits → auto-fallback**, **mandatory dry-run**, **staged % rollout for scripts**, **instant kill-switch** (`DisableScript`), audit, and per-`script_id` error metrics. Engine choice deferred to then: **Starlark** (structural sandbox, `thread.SetMaxSteps`, deterministic) vs **expr-lang/expr** (MVEL-closest syntax, easiest migration); saas-tech-lead prefers staying CEL-only. **Not** Lua/goja (manual sandbox hardening), **not** embedded CPython.

**Net answer to "is it a good idea?":** scripting is a footgun in a shared-tenant hot path. Declarative CEL (+ `routing_group_expr`) is the right v1; the **polyglot gRPC router — including a Python reference impl — is the correct home for arbitrary procedural/Python logic**.

---

## 7. Non-functional requirements

**Latency (hot path).** Routing logic is in-memory only — **no DB/network calls per request**; rule eval target **< 1 ms CPU**. End-to-end gRPC round-trip target **p99 ≤ ~10 ms** co-located (same zone; cross-AZ not acceptable in Phase 1). Operators set `routing.external.timeout` to **200–500 ms** (well above p99, well below the 1 s default) and enable a **client-side circuit breaker** in the gateway so a degraded service fast-fails to default instead of cascading into query timeouts. *(saas-tech-lead's 2 ms aspiration is the steady-state goal; goway-expert's ≤100 ms is the hard ceiling — design to the former, budget for the latter.)*

**Observability.**
- Metrics (labels: `routing_group`, `tenant`, `source`, `rule_id`): request count, **fallback count/rate**, decision-latency histogram, config-reload success/error, active config version.
- Alerts: `fallback_rate > 1%` (5m), `decision_latency_p99 > 5 ms`, `config_reload_error > 0`.
- Decision logs: structured, **sampled ~10% steady-state / 100% on fallback**; include `rule_id`, input attributes used, group chosen, latency, config-version hash. **Never log raw SQL** — hash or truncated prefix only (PII / proprietary logic).
- Tracing: an OTel span per `Route()`, **propagating the gateway's parent trace**.

**HA & scale.** Stateless; config cached locally from a shared source; horizontally scalable with no inter-replica coordination. **Cold start < 500 ms.** `grpc.health.v1.Health` readiness returns `NOT_SERVING` until the initial config is loaded + validated. Multi-AZ deployable; co-located with the gateway.

---

## 8. Multi-tenancy & security

- **Routing by tenant** (the headline SaaS use case): per-tenant isolated groups for noisy-neighbor + cost chargeback; top-spend customers get dedicated single-cluster groups (modeled as a normal group like `acme-dedicated`, never a backend URL). Tenant identity derived in Phase 1 from `X-Trino-User` mapping / source / client-tags / client IP; a dedicated tenant header would require the reserved proto addition (§4.1).
- **Tenant-namespace isolation in config**, enforced at load: a rule authored under tenant A **cannot match** traffic attributed to tenant B (validated, not by convention).
- **Phase 1 transport is insecure gRPC** (matches the gateway). Any future config-write API requires **mTLS + scoped JWT**; no unauthenticated config mutation.
- Decision logs must not leak SQL/PII (hash only).

---

## 9. Integration with trino-goway

Gateway-side knobs the operator sets (existing): `routing.external.grpcAddr` (enables gRPC), `routing.external.timeout` (per-call deadline), `routing.external.excludeHeaders` (gateway-applied), `routing.defaultGroup` (fallback). The gateway already handles, and the service must **not** duplicate: `excludeHeaders` filtering, empty→default fallback, `X-Forwarded-*` injection, response buffering, `nextUri`, auth, cookies, queryId stickiness. The service **must** document which headers it emits in `external_headers` so operators can `excludeHeaders` them if desired.

**Hard dependency:** the §4.1 proto additions (`trino_source`, `client_tags`) in trino-goway — without them, the two primary routing signals aren't available over gRPC.

---

## 10. Stakeholder perspectives (the discussion)

- **trino-expert** — Flagged the missing `X-Trino-Source` / `X-Trino-Client-Tags` (now §4.1); "no full SQL parsing in v1"; act only on `is_new_query_submission`; empty group is "defer", not error; `session` is effectively always null; mandate canonical `X-Trino-*` headers.
- **gateway-expert** — Defined the rule model (all-rules-run, ascending priority, last-wins, service-level default), the `external_headers`/`errors` semantics, "stay in your lane" on cluster selection, the MVEL→CEL migration mapping, and CEL-as-sandbox.
- **goway-expert** — Pinned the exact call/fallback semantics (single long-lived conn, per-call timeout, fall back to default on ANY error), statelessness, proto-versioning/compat, health/graceful-shutdown/co-location, and the key nuance that gRPC forwards only structured fields (→ §4.1).
- **saas-tech-lead** — Drove the product requirements: per-tenant/SLA/interactive-vs-batch routing, weighted canary + instant rollback, validate-before-activate hot-reload + dry-run + audit, the latency budget + circuit breaker, observability/alerts, fail-safe defaults, tenant isolation, and "never log raw SQL".

**Resolved tensions:** latency target reconciled (design to ~2 ms, budget to ≤100 ms ceiling); config authoring resolved as **file-based hot-reload + dry-run in Phase 1**, role-gated write API deferred; canary modeled as a **weighted-group** construct inside the same rule model; tenant identity header-derived in Phase 1 with a reserved proto field for later.

---

## 11. Open questions (confirm before build)

1. Tenant identity source for Phase 1 — `X-Trino-User` mapping vs. a gateway-injected `X-Tenant-ID` (proto add)? 
2. Group-name validation — does the service get the gateway's group registry (config injection / capability endpoint), or only warn on unknown groups?
3. ~~Is the `state` cross-rule map needed in Phase 1?~~ **RESOLVED (§6.1):** not modeled in v1 — rewrite as compound conditions. Scriptable-routing question also **RESOLVED (§6.1):** declarative + CEL only; polyglot gRPC (incl. Python reference impl) for procedural logic; embedded scripting deferred to v2 behind a threat model.
4. Confirm the trino-goway proto additions (§4.1) are in scope for this effort (they require a gateway change + release).

## 12. Success criteria

- trino-goway routes through the service via `routing.external.grpcAddr`; with the service down/slow, the gateway falls back to default with zero user-visible errors.
- Source/client-tags/user/tenant rules produce the expected groups; canary weights split as configured and roll back instantly on a live config change.
- Config hot-reloads with validate-before-activate, dry-run, and audit; invalid config never serves.
- p99 decision latency within budget; metrics/decision-logs/traces present; no raw SQL in logs.

---

*Phase 1: gRPC + Go. This PRD is the discussion output; §11 items to confirm before implementation planning (CONVENTIONS → TODO → build).*
