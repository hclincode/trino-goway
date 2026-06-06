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
- **No SQL parsing / table-aware routing (Phase 1).** No production-grade Go Trino parser exists; the parser-dependent proto fields arrive empty (see §5). Table/catalog-AST routing was unavailable in v1. **Update (Phase 9 / UC-RTG-04):** a **best-effort, in-service** heuristic analyzer now derives statement type and the catalogs/schemas/tables a query touches from the `body`, exposing them to routing rules. This is *not* a full Trino parser (a parse miss is normal and never an error); see §5 "SQL-aware routing inputs".
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

**SQL-aware fields (Phase 9 / UC-RTG-04).** The proto already carries `trino_query_properties.{query_type, catalogs, schemas, catalog_schemas, tables, is_query_parsing_successful}`. trino-goway v1 leaves these empty (`is_query_parsing_successful=false`). The routing-service now **derives them in-service** from `body` via a best-effort heuristic analyzer and exposes them to providers as `request.{query_type, query_category, catalogs, schemas, catalog_schemas, tables, parse_ok}`. **Forward-compatible:** if a future SQL-aware gateway populates the parsed proto fields itself, the service prefers those over re-parsing. No new proto field is required for this — the contract is unchanged.

> **Action for trino-goway:** add `trino_source` (field 12) + `client_tags` (field 13) to **`RouteRequest`** and populate them in the gRPC `buildProtoRequest`. Tracked as a gateway-side dependency for this project.

---

## 5. Phase 1 scope (gRPC, Go)

1. **gRPC server** implementing `TrinoGatewayRouter.Route` + `grpc.health.v1.Health` (Check + Watch). Insecure transport (matches the gateway client today). Graceful shutdown (`GracefulStop`).
2. **Routing-logic engine — pluggable & multi-method (see §6.1):** a `RoutingMethod` provider interface + registry + ordered evaluation pipeline. **Phase 1 ships two method providers — `expr` (expr-lang/expr) and `script` (Starlark)** — with a registry buffer for future methods (`rules`/CEL, `template`, `wasm`, …). `external` (delegate to a polyglot router) is available by config.
   - **`expr`** — an expr-lang program returning a group-name string (`""` = defer). MVEL-ish ternary / `in` / string ops; bounded by construction (expression-only, no loops).
   - **`script`** — a Starlark `route(req) -> group|None` function (procedural; `None` = defer), run under a `thread.SetMaxSteps` cap + wall-clock deadline.
   - Both see the same read-only context over the proto: `request.source`, `request.client_tags`, `request.user`, `request.catalog`, `request.schema`, `request.method`, `request.uri`, `request.remote_addr`, `request.body`, plus curated helpers (e.g. `hashPct(s)` for deterministic canary). No I/O/network exposed.
   - Both are **platform-admin-authored** (higher-trust); an empty/`None`/error result falls through to the next method and finally to `default_routing_group`.
3. **Canary / blue-green** — done inside the method via the deterministic `hashPct(user|tenant)` helper (e.g. `hashPct(user) < 5 → canary`), so the split lives in the hot-reloaded `expr`/`script` config; lowering the threshold to 0 is instant rollback on the next request. (A declarative `weighted_groups` construct returns with the future `rules` method.)
4. **Dynamic config (hot-reload)** — watch a config source (file via fsnotify in Phase 1); on change: **parse + validate against schema BEFORE activating**; on failure keep the last-known-good config live and emit an error metric + structured log with the failing diff; on success emit an **audit event** (timestamp, trigger, old/new config hash, changed-rule summary). Provide a **dry-run** path (CLI/sidecar) that reports which sample requests would route differently.
5. **Fail-safe behavior** — no method decides → `default_routing_group` (first-class outcome). Invalid/empty config **at startup** → health `NOT_SERVING` (never serve a broken method set). Treat `is_query_parsing_successful=false` / the `"trino-parser not available"` `error_message` as normal, never as an error signal. **The same fail-safe rule governs in-service SQL analysis (UC-RTG-04):** a parse miss yields empty structured fields + `parse_ok=false`, never an error; health stays `SERVING`.
6. **Validation at load** — `expr` / Starlark programs **compile** (and step-budget/type check) before activation; group names checked against the operator's group registry (or warn); over-broad logic warned (not blocked); tenant-scope isolation enforced (see §8).
7. **Observability** — Prometheus metrics, structured decision logs, OpenTelemetry tracing (see §7).
8. **Config & ops docs** — README, `expr` + Starlark authoring reference, an **MVEL→`expr` migration guide** (`expr` is the MVEL-closest syntax), and a small **Python reference `external` router** (the polyglot escape hatch, §6.1).
9. **SQL-aware routing inputs (Phase 9 / UC-RTG-04)** — a best-effort, **pure-Go heuristic** analyzer (`internal/sqlmeta`, behind a stable `SQLAnalyzer` interface) parses the query `body` **in the service** to derive statement type, a coarse category (`READ`/`WRITE`/`DDL`/…), and the catalogs/schemas/tables it touches, exposing them to `expr`/`script` as `request.{query_type, query_category, catalogs, schemas, catalog_schemas, tables, parse_ok}`. Rules can route writes to ETL or hive-touching reads to a warehouse group without a SQL parser on the gateway hot path. Toggle via `sqlParsing.enabled` (default on) with a `maxBodyBytes` cap; analysis fires only on `is_new` submissions and only when the proto did not already carry parsed fields. PII rule holds: the raw SQL is never logged (`sha256(body)[:8]` only) and the decision log carries **counts**, not identifiers. Backend rationale + the upgrade seam to a grammar-based parser are recorded in `docs/CONVENTIONS.md`.

### Later phases (recorded, not committed)
- **HTTP transport** (the gateway supports both; operators may run both as belt-and-suspenders).
- **mTLS** — gateway swaps `insecure.NewCredentials()` → `credentials.NewTLS(...)` (one line); service accepts optional TLS at startup. Config: `grpcCertFile`/`grpcKeyFile`/`grpcCAFile`.
- **Config-write API** — role-gated (mTLS + tenant/admin-scoped JWT), tenant-namespaced authoring, replacing/augmenting file-based config.
- **`resource_group_hint`** response extension; **SQL/table-aware routing** if a maintained Go Trino parser appears or via a parsing sidecar.

---

## 6. Config sketch (Phase 1)

```yaml
default_routing_group: adhoc          # gateway default when all methods defer
methods:                              # ordered chain — first definitive decision wins
  - type: expr                        # expr-lang — fast expression decisions
    refresh: 30s
    program: |
      request.source == "airflow" ? "etl"
        : request.source == "superset" ? (hashPct(request.user) < 5 ? "interactive-canary" : "interactive")
        : "tier=premium" in request.client_tags ? "premium"
        : ""                          # "" = defer to next method / default
  - type: script                      # Starlark — procedural long-tail
    refresh: 30s
    file: routes.star                 # defines route(req) -> group name or None
```

Both methods are platform-admin-authored; an empty/`None`/error result falls through to the next method and finally to `default_routing_group` (which should match the gateway's `routing.defaultGroup`).

### 6.1 Routing-logic methods — pluggable, multi-method, extensible

**Product decision:** the routing engine is **pluggable and multi-method**, designed so new methods can be added without touching the core (a buffer for future methods). **Phase 1 ships two methods — `expr` (expr-lang) and `script` (Starlark)** (confirmed scope). Declarative `rules` (CEL) and `template` are deferred to the registry buffer; `external` (polyglot) stays available by architecture. The earlier discussion's *safety* conclusions are retained as **cross-cutting guardrails enforced by the engine harness**.

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

**Pipeline:** the `Route` RPC runs an **ordered chain** of the enabled methods (order is config). Each returns a `Decision` or "no opinion" (`Decided=false`, empty group); the **first definitive decision wins**; if none decide, the gateway's default group applies. The harness — not the method — enforces the global wall-clock budget, per-method step/time limits, metrics, dry-run, and fallback. This composes cleanly: e.g. an `expr` method handles the common fast cases and a `script` method handles the procedural long tail, all under one safety envelope. (A future declarative `rules` method can front the chain for the auditable 95%.)

#### Methods

| `type` | Engine | Phase | Trust class |
|---|---|---|---|
| `expr` | `expr-lang/expr` — MVEL-ish expression returning a group name; bounded (no loops) | **v1** | high — platform-admins only |
| `script` | **Starlark** (`go.starlark.net`) — Python-like procedural; structural sandbox, `thread.SetMaxSteps` cap | **v1** | high — platform-admins only |
| `external` | Delegate to another gRPC/HTTP router (polyglot escape hatch — e.g. a **Python** `TrinoGatewayRouter`) | available by config | n/a (out-of-process) |
| `rules` | Declarative YAML + **CEL** (+ `routing_group_expr`); auditable, tenant-scoped self-serve | buffer (fast-follow) | low |
| `template` | **Go `text/template`** → group name | buffer | low |
| `wasm` | WebAssembly — "any sandboxed language", hot-swappable | buffer (future) | high |
| ML / others | future providers | buffer | per-provider |

The **buffer for new methods** is concrete: (a) the `RoutingMethod` interface + registry; (b) a config `type` discriminator per method; (c) reserved proto/extension fields. `wasm` is the intended long-term answer for "arbitrary language, safely sandboxed, hot-swappable."

#### Cross-cutting guardrails (apply to every method, enforced by the harness)

- **Fail-safe:** any method error/timeout/over-budget → that method is skipped and the chain continues; ultimately the gateway default — never a 5xx to the user.
- **Resource limits:** global per-request wall-clock budget + per-method step/CPU caps (e.g. Starlark `SetMaxSteps`, `context` deadline + `thread.Cancel`); no I/O/network/syscalls exposed to any in-process method.
- **Trust tiers:** both Phase-1 methods (`expr`, `script`) are higher-trust → **platform-admins only**; v1 has no tenant self-serve routing (the low-risk declarative `rules`/`template` methods, authorable within tenant namespaces, are the fast-follow that adds it). Tenant-namespace isolation enforced at load.
- **Change safety:** validate-before-activate (keep last-known-good), **mandatory dry-run** (replay candidate config/script against recent or synthetic traffic, show the routing diff), **staged % rollout** for high-risk methods, **instant kill-switch** (a `DisableMethod`/`DisableScript` op with sub-second propagation), full **audit** (author, timestamp, content hash, dry-run reviewed), and per-`method`/`rule_id`/`script_id` **metrics + error alerts**.
- **No raw SQL in logs** (hash/prefix only).

#### Why this shape

Phase 1 gives operators `expr` + `script` today; the harness holds the safety envelope so new methods later (`rules`/CEL, `template`, `wasm`, ML) need no redesign — they register behind the same interface. Full external/other-language logic stays available via the `external` method (the gRPC boundary is polyglot by design — implement `TrinoGatewayRouter` in Python/JVM/Node and point the chain at it).

### 6.2 Worked examples — `expr` and `script` (same scenario)

Scenario: `airflow`→`etl`; `superset`→`interactive` (5% canary → `interactive-canary`); client tag `tier=premium`→`premium`; user `…@analytics.acme.com`→computed `etl-analytics`; else defer.

**`expr`** (program returns the group name; `""` = defer):

```
request.source == "airflow" ? "etl"
  : request.source == "superset" ? (hashPct(request.user) < 5 ? "interactive-canary" : "interactive")
  : "tier=premium" in request.client_tags ? "premium"
  : hasSuffix(request.user, "@analytics.acme.com") ? "etl-" + split(split(request.user, "@")[1], ".")[0]
  : ""
```

**`script`** (Starlark `route(req)`; `None` = defer):

```python
def route(req):
    if req.source == "airflow":
        return "etl"
    if req.source == "superset":
        return "interactive-canary" if hashPct(req.user) < 5 else "interactive"
    if "tier=premium" in req.client_tags:
        return "premium"
    if req.user.endswith("@analytics.acme.com"):
        return "etl-" + req.user.split("@")[1].split(".")[0]
    return None
```

Both run under the harness: empty/`None`/error/over-budget → next method → `default_routing_group`; `hashPct` is the provided deterministic 0–99 helper for canary; no I/O is exposed.

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
3. **RESOLVED (§6.1):** the engine is pluggable & multi-method; **Phase 1 ships `expr` (expr-lang) + `script` (Starlark)** under uniform harness guardrails (confirmed scope). Declarative `rules`/CEL + `template` are registry-buffer fast-follows; `external` covers polyglot/Python. (Cross-rule `state` map not modeled.)
4. Confirm the trino-goway proto additions (§4.1) are in scope for this effort (they require a gateway change + release).

## 12. Success criteria

- trino-goway routes through the service via `routing.external.grpcAddr`; with the service down/slow, the gateway falls back to default with zero user-visible errors.
- Source/client-tags/user/tenant rules produce the expected groups; canary weights split as configured and roll back instantly on a live config change.
- Config hot-reloads with validate-before-activate, dry-run, and audit; invalid config never serves.
- p99 decision latency within budget; metrics/decision-logs/traces present; no raw SQL in logs.

---

*Phase 1: gRPC + Go. This PRD is the discussion output; §11 items to confirm before implementation planning (CONVENTIONS → TODO → build).*
