# Authoring `script` (Starlark) routing functions

The `script` method runs a [Starlark](https://github.com/google/starlark-go)
program that **must define `def route(req):`**. `route` returns a routing group
name (non-empty string) or `None`/`""` to **defer** to the next method. The
script is parsed and compiled at load time; a syntax error or a missing `route`
function is rejected and the previously-loaded script stays live (keep-last-good).

## Request context

`route(req)` receives `req`, a frozen struct with these read-only attributes
(same snake_case contract as the `expr` method):

| Attribute | Type | Source |
|---|---|---|
| `req.source` | str | `X-Trino-Source` |
| `req.client_tags` | list[str] | `X-Trino-Client-Tags`, pre-split by the gateway |
| `req.user` | str | `X-Trino-User` |
| `req.catalog` | str | `X-Trino-Catalog` |
| `req.schema` | str | `X-Trino-Schema` |
| `req.method` | str | HTTP method |
| `req.uri` | str | request path |
| `req.remote_addr` | str | client IP |
| `req.body` | str | raw SQL body (POST `/v1/statement`) |
| `req.is_new` | bool | true for new query submissions |
| `req.param_map` | dict[str,str] | URL/form params |

### Routing on query content (UC-RTG-04)

When SQL parsing is enabled (`sqlParsing.enabled: true`, the default), the
service analyses the query `body` **in-process** (best-effort, pure-Go) and adds
these read-only attributes so `route(req)` can act on what a query *does* — its
statement type and the catalogs/schemas/tables it touches:

| Attribute | Type | Meaning |
|---|---|---|
| `req.query_type` | str | statement keyword, e.g. `"SELECT"`, `"INSERT"`, `"CREATE"` |
| `req.query_category` | str | coarse class: `READ`/`WRITE`/`DDL`/`DML`/`EXPLAIN`/`OTHER` |
| `req.catalogs` | list[str] | catalogs the query touches (sorted, de-duplicated) |
| `req.schemas` | list[str] | schemas the query touches |
| `req.catalog_schemas` | list[str] | `"catalog.schema"` pairs |
| `req.tables` | list[str] | qualified table references, e.g. `"hive.sales.orders"` |
| `req.parse_ok` | bool | `True` when analysis recognised the statement |

**Best-effort / `parse_ok` semantics.** Analysis is heuristic, not a full Trino
parser, and a parse miss is **never an error**: when `req.parse_ok` is `False`
all SQL attributes are empty and your rule should fall back to header/source
routing. Gate content rules on `parse_ok`:

```python
def route(req):
    if req.parse_ok and req.query_category == "WRITE":
        return "etl"
    if req.parse_ok and "hive" in req.catalogs:
        return "warehouse"
    if req.source == "airflow":
        return "etl"
    return None
```

The attributes are only populated for new submissions (`req.is_new`) and only
when the gateway did not already supply parsed fields. The decision log records
counts only — never the parsed identifiers, never the raw SQL.

## Helpers

- **`hashPct(s)`** — deterministic 0–99 bucket (same semantics as the `expr`
  provider) for canary splits: `hashPct(req.user) < 5`.
- Standard Starlark expression syntax: `if/else`, comprehensions, `in`,
  `str.endswith`, `str.split`, etc.

## Return convention

- Non-empty **string** → that routing group (decision).
- **`None`** or `""` → defer to the next method.

## Sandbox & limits

- **Structural sandbox**: no stdlib, no `load()`, no `open`/`file`/`os`/network.
  Only `req` and `hashPct` are predeclared. `import` is not Starlark syntax.
- **Step limit**: each `route` call runs under a **10,000-step** CPU budget
  (`thread.SetMaxExecutionSteps`); exceeding it cancels the call → defer. The
  operator does not set this — the harness enforces it. (`tools/starlark-test`
  exposes `--max-steps` to experiment locally without affecting production.)
- **Deadline**: the request context deadline cancels a slow script → defer.
- `req` is frozen before the call (immutable), so scripts cannot mutate shared
  state.

Any error, step-limit, or deadline → the method defers (logged warn), never a
hard failure, so a buggy script can never fail a query.

## Worked example (PRD §6.2)

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

## Test it

```sh
make starlark-test
# single input
./bin/starlark-test routes.star '{"source":"airflow","user":"alice","is_new":true}'
# batch CI validation with expectations
./bin/starlark-test routes.star --samples samples.yaml --expect expected.yaml
```

See also: [`expr-authoring.md`](expr-authoring.md),
[`mvel-to-expr-migration.md`](mvel-to-expr-migration.md).
