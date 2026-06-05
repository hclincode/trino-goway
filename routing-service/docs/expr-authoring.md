# Authoring `expr` routing programs

The `expr` method evaluates a single [expr-lang/expr](https://expr-lang.org)
expression that **returns a routing group name** (a non-empty string), or `""`
to **defer** to the next method. The program is compiled and type-checked at
load time; a compile error or a non-string return type is rejected and the
previously-loaded program stays live (keep-last-good).

## Request context

The request is exposed as `request` with these snake_case fields (mirroring
`engine.RouteInput`):

| Field | Type | Source |
|---|---|---|
| `request.source` | string | `X-Trino-Source` header |
| `request.client_tags` | []string | `X-Trino-Client-Tags`, pre-split on comma by the gateway |
| `request.user` | string | `X-Trino-User` header |
| `request.catalog` | string | `X-Trino-Catalog` header |
| `request.schema` | string | `X-Trino-Schema` header |
| `request.method` | string | HTTP method (`POST`, `GET`, …) |
| `request.uri` | string | request path, e.g. `/v1/statement` |
| `request.remote_addr` | string | client IP |
| `request.body` | string | raw SQL body (POST `/v1/statement` only) |
| `request.is_new` | bool | true for new query submissions (POST `/v1/statement`) |
| `request.param_map` | map[string]string | URL/form params (multi-valued comma-joined) |

> The service only makes a routing decision when `request.is_new` is true; the
> gateway handles polls/cancels itself.

## Helpers

- **`hashPct(s string) int`** — deterministic FNV-1a hash of `s` modulo 100
  (0–99). Use it for stable canary/blue-green splits, e.g.
  `hashPct(request.user) < 5 ? "canary" : "prod"`. The same input always maps to
  the same bucket.
- The expr-lang **built-ins** are available, including `hasPrefix`, `hasSuffix`,
  `split`, `matches` (regex), `len`, `in`, ternary `a ? b : c`, etc. See the
  [expr language definition](https://expr-lang.org/docs/language-definition).

## Return convention

- A **non-empty string** → that routing group (decision; stops the pipeline).
- `""` → **defer** to the next method.
- The program **must** evaluate to a string; returning any other type is a
  compile error.

## Sandbox

No I/O, network, filesystem, or time functions are exposed — only the `request`
fields and the pure helpers above. Programs are bounded by construction (no
loops over unbounded data), so there is no explicit step limit for `expr`.

## Worked example (PRD §6.2)

Scenario: `airflow`→`etl`; `superset`→`interactive` (5% canary →
`interactive-canary`); client tag `tier=premium`→`premium`; users
`…@analytics.acme.com`→ a computed `etl-<subdomain>`; else defer.

```
request.source == "airflow" ? "etl"
  : request.source == "superset" ? (hashPct(request.user) < 5 ? "interactive-canary" : "interactive")
  : "tier=premium" in request.client_tags ? "premium"
  : hasSuffix(request.user, "@analytics.acme.com") ? "etl-" + split(split(request.user, "@")[1], ".")[0]
  : ""
```

## Test it

```sh
make expr-test
# inline program + inline JSON input
./bin/expr-test --program 'request.source == "airflow" ? "etl" : ""' \
  '{"source":"airflow","is_new":true}'
# batch against a samples file with expectations (CI mode)
./bin/expr-test routes.expr --samples samples.yaml --expect expected.yaml
```

See also: [`starlark-authoring.md`](starlark-authoring.md),
[`mvel-to-expr-migration.md`](mvel-to-expr-migration.md).
