# Migrating MVEL routing rules to `expr`

[trino-gateway](https://github.com/trinodb/trino-gateway)'s `RoutingRulesEngine`
evaluates **MVEL** rules that read a `request` (an `HttpServletRequest`) and
mutate a `result` map. The routing-service replaces that with `expr` programs (or
Starlark scripts) that **return the routing group directly**. This guide maps the
common MVEL patterns to their `expr` equivalents.

## Model differences

| MVEL (trino-gateway) | `expr` (routing-service) |
|---|---|
| Rule reads `request.getHeader(...)` | `request.<field>` (pre-extracted, snake_case) |
| Rule writes `result.put("routingGroup", "etl")` | program **returns** `"etl"` |
| "no match" leaves `result` untouched | return `""` to **defer** |
| All rules run; later rules overwrite | methods run in order; **first non-empty wins** |
| Java exceptions surface | errors are swallowed → defer (fail-safe) |

## Pattern mapping

| MVEL | `expr` |
|---|---|
| `request.getHeader("X-Trino-Source") == "airflow"` | `request.source == "airflow"` |
| `request.getHeader("X-Trino-User")` | `request.user` |
| `request.getHeader("X-Trino-Client-Tags").contains("tier=premium")` | `"tier=premium" in request.client_tags` |
| `request.getHeader("X-Trino-Catalog") == "hive"` | `request.catalog == "hive"` |
| `result.put("routingGroup", "etl")` | return value `"etl"` |
| `A ? B : C` (ternary) | `A ? B : C` (identical) |
| `value =~ "pattern"` (regex match) | `request.source matches "pat.*"` (expr `matches` is a binary operator, not a function) |
| string `.startsWith(...)` / `.endsWith(...)` | `hasPrefix(...)` / `hasSuffix(...)` |
| string `.split(...)` | `split(s, sep)` |

### Example

MVEL:

```text
request.getHeader("X-Trino-Source") == "airflow"
    ? result.put("routingGroup", "etl")
    : (request.getHeader("X-Trino-Client-Tags").contains("tier=premium")
        ? result.put("routingGroup", "premium") : "")
```

`expr`:

```text
request.source == "airflow" ? "etl"
  : "tier=premium" in request.client_tags ? "premium"
  : ""
```

## What is NOT portable

The routing-service runs **no SQL parser** in Go Phase 1, so any MVEL rule that
routed on parsed-query structure has no equivalent: the proto carries these
fields but they are **always empty** —
`query_type`, `catalogs`, `schemas`, `catalog_schemas`, `tables`,
`resource_group_query_type`, `is_query_parsing_successful`. Rules depending on
table/catalog-from-SQL routing must wait for a parser-backed phase or route on
the available header signals (`source`, `client_tags`, `user`, `catalog`,
`schema`) instead.

For **multi-statement / imperative** MVEL rules, use the Starlark `script` method
(`def route(req):` with a full function body) — see
[`starlark-authoring.md`](starlark-authoring.md).
