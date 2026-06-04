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
2. **Routing-logic engine — pluggable & multi-method (see §6.1):** a `RoutingMethod` provider interface + registry + ordered evaluation pipeline, shipping the `rules`, `template` (Go `text/template`), `script` (Starlark), and `external` (delegate) methods in v1, with a buffer for future methods (`expr`, `wasm`, …) and uniform harness-enforced guardrails. The default `rules` method uses declarative YAML with **CEL** (`google/cel-go`) conditions:
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

### 6.1 Routing-logic methods — pluggable, multi-method, extensible

**Product decision (overrides the earlier "declarative-only v1" lean):** the service supports **multiple routing-logic methods** — declarative rules, **templating**, and **scripting** — and is **designed so new methods can be added without touching the core** (a buffer for future methods). The earlier discussion's *safety* conclusions are retained, but as **cross-cutting guardrails enforced by the engine harness** rather than a reason to ship only one method.

#### Architecture — a `RoutingMethod` provider + evaluation pipeline

A single stable internal interface; each method is a provider behind it; a **registry** maps a method `type` → provider factory. This interface **is the extensibility buffer** — a new method is a new provider registration plus a config schema, with zero changes to the gRPC layer, the pipeline, or the guardrails.

```go
type Decision struct {
    RoutingGroup    string            // "" = no opinion / defer to next method
    ExternalHeaders map[string]string
    Errors          []string          // hard policy violation only
    Decided         bool              // true = this method made a definitive call
}

type RoutingMethod interface {
    Type() string                                   // "rules" | "template" | "script" | "external" | "wasm" | ...
    LoadConfig(raw []byte) error                    // parse + VALIDATE; activated only if valid (hot-reload)
    Evaluate(ctx context.Context, in *RouteInput) (Decision, error)
}
```

**Pipeline:** the `Route` RPC runs an **ordered chain** of the enabled methods (order is config). Each returns a `Decision` or "no opinion" (`Decided=false`, empty group); the **first definitive decision wins**; if none decide, the gateway's default group applies. The harness — not the method — enforces the global wall-clock budget, per-method step/time limits, metrics, dry-run, and fallback. This composes cleanly: declarative rules handle 95%, a script/template method handles the long tail, all under one safety envelope.

#### Methods

| `type` | Engine | Phase | Trust class |
|---|---|---|---|
| `rules` | Declarative YAML + **CEL** conditions (+ `routing_group_expr` CEL→string) | **v1 (default)** | low — broadly usable, incl. tenant-scoped |
| `template` | **Go `text/template`** rendering a group name from request attributes (sandboxed: only the funcs we expose; deterministic; no I/O) | **v1** | low |
| `script` | **Starlark** (`go.starlark.net`) — Python-like, structural sandbox, `thread.SetMaxSteps` step cap, deterministic | **v1** | high — platform-admins only |
| `external` | Delegate to another gRPC/HTTP router (chains the polyglot escape hatch — e.g. a **Python** `TrinoGatewayRouter` impl) | **v1** | n/a (out-of-process) |
| `expr` | `expr-lang/expr` — MVEL-closest syntax, for migration familiarity | buffer (opt-in) | high |
| `wasm` | WebAssembly modules — the strategic "any sandboxed language" plug | **buffer (future)** | high |
| ML / others | future providers | buffer | per-provider |

The **buffer for new methods** is concrete: (a) the `RoutingMethod` interface + registry; (b) a config `type` discriminator per method; (c) reserved proto/extension fields. `wasm` is the intended long-term answer for "arbitrary language, safely sandboxed, hot-swappable."

#### Cross-cutting guardrails (apply to every method, enforced by the harness)

- **Fail-safe:** any method error/timeout/over-budget → that method is skipped and the chain continues; ultimately the gateway default — never a 5xx to the user.
- **Resource limits:** global per-request wall-clock budget + per-method step/CPU caps (e.g. Starlark `SetMaxSteps`, `context` deadline + `thread.Cancel`); no I/O/network/syscalls exposed to any in-process method.
- **Trust tiers:** low-risk methods (`rules`, `template`) authorable within tenant namespaces; high-risk methods (`script`, `wasm`, `expr`) **platform-admins only**. Tenant-namespace isolation enforced at load.
- **Change safety:** validate-before-activate (keep last-known-good), **mandatory dry-run** (replay candidate config/script against recent or synthetic traffic, show the routing diff), **staged % rollout** for high-risk methods, **instant kill-switch** (a `DisableMethod`/`DisableScript` op with sub-second propagation), full **audit** (author, timestamp, content hash, dry-run reviewed), and per-`method`/`rule_id`/`script_id` **metrics + error alerts**.
- **No raw SQL in logs** (hash/prefix only).

#### Why this shape

It gives operators the multiple methods you want (declarative + templating + scripting) **today**, keeps the safety envelope the team insisted on (the guardrails live in the harness, so adding a method doesn't reopen the risk), and the provider interface means new methods (`expr`, `wasm`, ML, …) drop in later without a redesign. Full external/other-language logic remains a first-class `external` method (the gRPC boundary is polyglot by design; a **Python reference router** ships in v1).

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
3. ~~Is the `state` cross-rule map needed in Phase 1?~~ **RESOLVED (§6.1):** not modeled in the `rules` method — rewrite as compound conditions. Scriptable-routing question **RESOLVED (§6.1):** the engine is pluggable & multi-method — `rules` (CEL) + `template` + `script` (Starlark) ship in v1 under uniform harness guardrails, with a registry buffer for future methods (`expr`, `wasm`) and `external` for polyglot/Python.
4. Confirm the trino-goway proto additions (§4.1) are in scope for this effort (they require a gateway change + release).

## 12. Success criteria

- trino-goway routes through the service via `routing.external.grpcAddr`; with the service down/slow, the gateway falls back to default with zero user-visible errors.
- Source/client-tags/user/tenant rules produce the expected groups; canary weights split as configured and roll back instantly on a live config change.
- Config hot-reloads with validate-before-activate, dry-run, and audit; invalid config never serves.
- p99 decision latency within budget; metrics/decision-logs/traces present; no raw SQL in logs.

---

*Phase 1: gRPC + Go. This PRD is the discussion output; §11 items to confirm before implementation planning (CONVENTIONS → TODO → build).*
