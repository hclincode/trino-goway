---
title: SQL parsing for routing — contract surface of TrinoQueryProperties
author: java-analyst
role: Java Analyst
component: trino-gateway
topics: [query-classification, routing-engine, statement-protocol]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to: [architecture-overview.md, jvm-dependencies-inventory.md, mvel-rules-language.md]
---

# SQL parsing for routing — contract surface of TrinoQueryProperties

## Summary

When the operator enables `requestAnalyzerConfig.analyzeRequest`, the gateway parses every `POST /v1/statement` body as Trino SQL and exposes a small set of facts about the parsed statement (query type, accessed tables, schemas, catalogs) to routing rules. The parsing depends on Trino's own ANTLR-based SQL parser — the second of the two contract-shaped JVM dependencies. This file enumerates exactly which AST features are consumed, which fields rules can read, the default-catalog/schema resolution semantics, the zstd+base64url-encoded prepared-statement header decoding, and the failure modes. The takeaway: the contract surface is narrow (~10 fields on the analysis object) but covers most Trino DDL/DML statement types, and the default-catalog/schema resolution rules are non-trivial.

## Key Findings

### Activation

SQL parsing is gated by `requestAnalyzerConfig.analyzeRequest` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/config/RequestAnalyzerConfig.java:25`). Default: **false**.

When `analyzeRequest == false`:
- The parser is never invoked.
- Routing rules cannot reference `trinoQueryProperties.*` (the binding is absent from the context map — `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/FileBasedRoutingGroupSelector.java:64-72`).
- Rules referencing `trinoQueryProperties` will fail at evaluation time (MVEL will see an unbound variable).
- Sticky routing by query id, cookie routing, header-based routing all still work.

When `analyzeRequest == true`:
- A `TrinoQueryProperties` object is built per inbound request and attached to the JAX-RS context.
- The first `maxBodySize` bytes of the request body are read; if exactly `maxBodySize` are read, parsing is skipped (the body is presumed truncated; see "Body size limit" below).
- The SQL is parsed via Trino's `SqlParser` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoQueryProperties.java:186, 214`).
- Tree-walked to extract the structured fields.
- Failures swallowed into a per-request `errorMessage` field (not propagated).

Default `maxBodySize` is `1_000_000` bytes (~1 MB) per `RequestAnalyzerConfig.java:20`.

### Contract surface — the methods rules can call

These are the public methods on `TrinoQueryProperties` that rules in `routing_rules_trino_query_properties.yml` and equivalents actually use:

| Method | Returns | What it means |
|---|---|---|
| `getBody()` | `String` | raw SQL text (possibly already substituted for `EXECUTE <name>` patterns; see "Prepared statements" below) |
| `getQueryType()` | `String` | the **AST class simple name** of the top-level statement, e.g. `"Query"`, `"Insert"`, `"CreateTable"`, `"Execute"`. Note: this is *not* Trino's `QueryType` enum — it is `statement.getClass().getSimpleName()`. See `TrinoQueryProperties.java:230` |
| `getResourceGroupQueryType()` | `String` | the coarser Trino classification: one of `"SELECT"`, `"EXPLAIN"`, `"DESCRIBE"`, `"ALTER_TABLE_EXECUTE"`, `"INSERT"`, `"UPDATE"`, `"DELETE"`, `"MERGE"`, `"ANALYZE"`, `"DATA_DEFINITION"`, or `"UNKNOWN"`. Mapping table at `StatementUtils.java:113-189`. `ExplainAnalyze` recurses into the inner statement (`StatementUtils.java:194-196`) |
| `getTables()` | `Set<QualifiedName>` (serialized as `Set<String>` for JSON) | all tables referenced, fully qualified to 3 parts (`catalog.schema.table`) using default catalog/schema if absent |
| `tablesContains(String)` | `boolean` | takes a `"catalog.schema.table"` string (with optional double-quoted parts), parses it the same way, and checks `tables.contains(...)`. Returns `false` on parse error |
| `getSchemas()` | `Set<String>` | the second component of every `tables` entry, plus schemas from DDL like `CREATE SCHEMA`, `SHOW SCHEMAS` |
| `getCatalogs()` | `Set<String>` | the first component of every `tables` entry, plus catalogs from DDL like `CREATE CATALOG`, `SHOW SCHEMAS FROM catalog` |
| `getCatalogSchemas()` | `Set<String>` | `"catalog.schema"` pairs for each table |
| `getDefaultCatalog()` | `Optional<String>` | value of the `X-Trino-Catalog` request header at the time of analysis |
| `getDefaultSchema()` | `Optional<String>` | value of the `X-Trino-Schema` request header |
| `isNewQuerySubmission()` | `boolean` | true iff request method is `POST` (i.e., this is `POST /v1/statement`, not a follow-up `GET /v1/statement/.../...`) |
| `isQueryParsingSuccessful()` | `boolean` | whether parsing succeeded |
| `getErrorMessage()` | `Optional<String>` | parse error message if any |
| `getQueryId()` | `Optional<String>` | populated only when the statement is a `CALL system.runtime.kill_query('query-id')` — extracted as a special case for routing cancellation requests back to the original backend |

### Per-statement-type behavior in the AST visitor

`TrinoQueryProperties.visitNode(...)` (`TrinoQueryProperties.java:352-452`) walks the AST and adds to the table/catalog/schema sets based on statement type. The exhaustive list of recognized AST node types (per the `switch` cases) is:

| AST class | Effect on extraction |
|---|---|
| `AddColumn` | add table being altered |
| `Analyze` | add table |
| `Call` | extract `queryId` if call is `system.runtime.kill_query(<string-literal>)` |
| `CreateCatalog` / `DropCatalog` | add catalog |
| `CreateMaterializedView` / `CreateTable` / `CreateView` / `CreateTableAsSelect` / `DropTable` | add table |
| `CreateSchema` / `DropSchema` | add catalog+schema |
| `Insert` | add target table |
| `Query` with `WITH` | record CTE table names in `temporaryTables` (excluded from outputs) |
| `RenameTable` / `RenameView` | add source + computed target (handles both 2-part and 3-part target forms) |
| `RenameMaterializedView` / `RenameSchema` | similar dual-add |
| `SetProperties` | add target table |
| `ShowColumns` / `ShowCreate` (non-schema) | add table |
| `ShowCreate` SCHEMA / `ShowSchemas` / `ShowTables` | add catalog+schema |
| `SetAuthorizationStatement` | add catalog+schema if `SCHEMA`, else table |
| `Table` (inside SELECT) | add table unless it's a CTE alias |
| `TableFunctionInvocation` | add the function's qualified name |
| anything else | no extraction; recurse into children |

Children are always recursed, so a `Query` with nested `Insert ... SELECT FROM (SELECT FROM t)` extracts the insert target and the read table.

The set of recognized AST classes is **closed** — adding a new SQL statement type to Trino requires updating this `switch`. Statement types not in the switch fall through to the default and recurse into children (no extraction, but no error).

### Default catalog/schema resolution

Tables in SQL can be written as 1-, 2-, or 3-part names: `t`, `s.t`, `c.s.t`. `qualifyName(...)` (`TrinoQueryProperties.java:503-513`) resolves all to 3-part form:

| Input parts | Resolution |
|---|---|
| 3 (`c.s.t`) | use as-is |
| 2 (`s.t`) | prefix with `defaultCatalog` (from `X-Trino-Catalog`); throw `RequestParsingException` if header absent |
| 1 (`t`) | prefix with `defaultCatalog` and `defaultSchema`; throw if either header absent |
| 4+ | throw `RequestParsingException` |

**A `RequestParsingException` here is caught at the top of `processRequestBody` and stored in `errorMessage`** (`TrinoQueryProperties.java:256-259`). The query is not failed; routing rules see `isQueryParsingSuccessful() == false` and `errorMessage` populated, but `tables`/`schemas`/`catalogs` may be partially populated (some statements may have parsed before the exception).

For `SHOW SCHEMAS FROM x`, single-part schema names are resolved against `defaultCatalog` (`TrinoQueryProperties.java:467-495`).

### Prepared statements — `X-Trino-Prepared-Statement` header

When the SQL is `EXECUTE <name>` or `EXECUTE IMMEDIATE 'sql'`, the gateway resubstitutes:

- `EXECUTE`: looks up `<name>` in a map built from the `X-Trino-Prepared-Statement` header (`TrinoQueryProperties.java:215-224`). The header is comma-separated `<url-encoded-name>=<url-encoded-value>`. The value may be wire-compressed: if it starts with the literal prefix `$zstd:`, the rest is base64url-decoded then zstd-decompressed (`TrinoQueryProperties.java:336-350`). After substitution, the prepared SQL text is re-parsed and the result drives all the extraction above.
- `EXECUTE IMMEDIATE 'sql'`: extracts the string literal inside `IMMEDIATE` and re-parses (`TrinoQueryProperties.java:225-228`).

**This is a real wire-protocol dependency.** The `$zstd:` prefix and the base64url-of-zstd-of-prepared-statement format are defined by Trino's own `io.trino.server.protocol.PreparedStatementEncoder` (the comment at `TrinoQueryProperties.java:338` cites this). A Go implementation must match exactly.

If `EXECUTE`'s `<name>` is not in the header map, the gateway logs an error, sets `queryType = "Execute"` and returns without table extraction — but the request is **not** failed. The proxied request still goes through; the backend will return its own error.

### Body size limit semantics

`maxBodySize` (default 1,000,000) bounds how much of the request body is read for parsing (`TrinoQueryProperties.java:187-200`):

- `BufferedReader.mark(maxBodySize)` is called so the body can be re-read by the downstream proxy.
- A `char[maxBodySize]` is allocated and read once.
- **If the read returns exactly `maxBodySize` characters, parsing is skipped.** The comment is explicit: "The body is truncated - there is a chance that it could still be syntactically valid SQL, for example if truncated on whitespace preceding a UNION. Exit out of caution." This is the conservative-safety choice — better an empty analysis than a wrong one.
- If the read returns 0 characters, parsing is skipped (logged as "query text is empty").

Operators with very large queries must raise `maxBodySize`. Without raising it, routing rules referencing `trinoQueryProperties.*` will see empty fields for those queries.

### Charset handling

Only `UTF-8` request bodies are parsed (`TrinoQueryProperties.java:273-282`). Other charsets are logged and skipped. The Trino protocol convention is UTF-8; non-UTF-8 should not occur in practice.

### v2 client format support

`requestAnalyzerConfig.clientsUseV2Format` (default false) toggles a fallback (`TrinoQueryProperties.java:203-212`): if the request body looks like JSON of shape `{"query": "...", "preparedStatements": {...}}` (per a commercial Trino fork's proposed v2 protocol — the comment at `TrinoQueryProperties.java:650-651` notes "This is known to be used by some commercial extensions of Trino, but is not implemented in Trinodb Trino"), the JSON is decoded and `query` is used as the SQL, `preparedStatements` map merged in. Bad JSON is silently swallowed and standard format is assumed.

### Two construction paths

`TrinoQueryProperties` has two constructors:

1. `TrinoQueryProperties(ContainerRequestContext, isClientsUseV2Format, maxBodySize)` (`TrinoQueryProperties.java:163-175`) — the "fresh from request" path that actually invokes the parser. This is what runs per inbound request when `analyzeRequest` is true.
2. `TrinoQueryProperties(@JsonCreator @JsonProperty(...) ...)` (`TrinoQueryProperties.java:118-146`) — the "deserialize from JSON" path used when the gateway POSTs the analysis to an **external** routing-rules service (`ExternalRoutingGroupSelector`). The external service receives a JSON-serialized view of the analysis, evaluates rules, and returns the chosen routing group. In this path, parsing happens once in the gateway and the result is sent over the wire.

The JSON shape used by path (2) is the contract for any external routing-rules implementation:

```json
{
  "body": "SELECT * FROM t",
  "queryType": "Query",
  "resourceGroupQueryType": "SELECT",
  "tables": ["cat.sch.t"],
  "defaultCatalog": "cat",
  "defaultSchema": "sch",
  "catalogs": ["cat"],
  "schemas": ["sch"],
  "catalogSchemas": ["cat.sch"],
  "isNewQuerySubmission": true,
  "errorMessage": null,
  "isQueryParsingSuccessful": true
}
```

(`tables` is serialized as a string array via the custom `QualifiedNameJsonSerializer` at `TrinoQueryProperties.java:696-714`.)

### Failure modes

| Failure | Effect |
|---|---|
| `ParsingException` during `SqlParser.createStatement(...)` | logged at INFO level; `errorMessage` populated; **no fields populated**; request proceeds |
| `RequestParsingException` from `qualifyName` / `setCatalogAndSchemaNameFromSchemaQualifiedName` (e.g., missing default catalog for unqualified table) | logged at WARN; `errorMessage` populated; partial extraction; request proceeds |
| `IOException` reading request body | logged at WARN; `errorMessage` populated; request proceeds |
| Prepared statement header malformed (no `=` separator) | `RequestParsingException` caught at outer level; `errorMessage` populated; request proceeds |
| Body exceeds `maxBodySize` | silent skip (logged at WARN); fields empty |
| Body empty | silent skip (logged at WARN); fields empty |
| Non-UTF-8 charset | silent skip (logged at DEBUG); fields empty |
| `EXECUTE <name>` with unknown name | logged at ERROR; `queryType = "Execute"`; tables empty; request proceeds |
| zstd decompression failure on prepared statement | **uncaught** — propagates as runtime exception. **Open question for `@trino-expert`:** what is the correct behavior here? Probably should be caught and treated as a parse failure |

### The `Statement.getClass().getSimpleName()` query-type contract

`queryType` is the **Java class name** of the parsed `Statement` (`TrinoQueryProperties.java:230`). Examples observed in test fixtures:

- `"Query"` (SELECT)
- `"Insert"`
- `"CreateTable"`
- `"Execute"`

This is a low-level contract that ties rule files to the Trino parser's class names. If Trino renames a class (e.g., splits `Query` into `Select` and `Query`), rules referencing `getQueryType().equals("Query")` break.

The `resourceGroupQueryType` field is the more stable contract (uses Trino's enumeration, e.g., `"DATA_DEFINITION"`).

### The `Set<QualifiedName>` table contract

Per the test fixture `routing_rules_trino_query_properties.yml:11-15`, a rule does:
```
trinoQueryProperties.tablesContains("cat_default.\"schem_\\\"default\".tblz")
&& trinoQueryProperties.tablesContains("cat_default.schemy.tbly")
```

So:
- Identifier quoting (`"name with spaces"`) must be supported.
- Backslash-escaped quotes (`\\"`) inside quoted identifiers must work.
- 3-part dotted naming.

The `parseIdentifierStringToQualifiedName(name)` method (`TrinoQueryProperties.java:545-588`) is a hand-rolled mini-parser implementing exactly this convention. Any Go rewrite must implement the same parsing for `tablesContains(...)` calls in rules.

## Behavior vs. Implementation Artifact

### `queryType` is the AST class simple name
- **Observed behavior:** Rules read `trinoQueryProperties.getQueryType().toLowerCase().equals("insert")` etc.
- **Source of behavior:** `jvm-artifact` — the value happens to be `Statement.getClass().getSimpleName()`, but it's *visible* to operators in their rule files.
- **Rationale:** Convenience: Trino's AST class names are essentially the SQL statement types.
- **Go obligation:** `replicate-exactly`. The Go rewrite must emit the same string values (`"Query"`, `"Insert"`, `"CreateTable"`, ...). This means the Go parser (whatever it is) must classify into the same names.
- **Notes:** Document this contract loudly. The mapping is implicit in Trino's grammar; need to enumerate the full set as part of `[[routing-engine.md]]`.

### `resourceGroupQueryType` is the Trino `QueryType` enum
- **Observed behavior:** Rules read `trinoQueryProperties.getResourceGroupQueryType().equals("DATA_DEFINITION")`.
- **Source of behavior:** `protocol-required` (well, gateway-design-intent — but it mirrors Trino's own QueryType classification).
- **Go obligation:** `replicate-exactly`. The mapping from AST class to query-type label is at `StatementUtils.java:113-189` (a closed list of ~50 entries). Port it verbatim.

### `tablesContains` uses identifier-aware parsing with backslash-escaping
- **Observed behavior:** `tablesContains("cat.\"schem_\\\"default\".tbl")` matches a table whose schema is `schem_"default`.
- **Source of behavior:** `gateway-design-intent`.
- **Go obligation:** `replicate-exactly`. Hand-port the ~40-line `parseIdentifierStringToQualifiedName` mini-parser to Go.

### `EXECUTE`-with-unknown-name silently leaves `queryType="Execute"` and proceeds
- **Observed behavior:** No statement substitution, no parse, just `queryType="Execute"` and the request proxies.
- **Source of behavior:** `gateway-design-intent` — the backend will reject the request with a proper error.
- **Go obligation:** `replicate-intent`. The Go rewrite must not fail the request; the backend is the source of truth on whether the prepared statement exists (it might exist on the backend even if the gateway doesn't see the header).
- **Notes:** Important: do not "improve" this by failing fast.

### `maxBodySize`-exact-read means truncation, skip parsing
- **Observed behavior:** If exactly `maxBodySize` bytes are read, parsing is skipped on the theory that the body might be truncated.
- **Source of behavior:** `defensive-historical`. Comment in code is explicit.
- **Go obligation:** `replicate-exactly`. The Go rewrite must apply the same heuristic; operators may rely on the safe-default behavior.
- **Notes:** A more precise alternative would be to check if the underlying stream has more data after the read. The Java approach is approximate; preserve it for compatibility.

### Schema name with >2 parts is logged but not failed
- **Observed behavior:** `setCatalogAndSchemaNameFromSchemaQualifiedName` (`TrinoQueryProperties.java:493`) emits `log.error("Schema has >2 parts: %s", schema)` for malformed input and silently does nothing.
- **Source of behavior:** `defensive-historical`. The grammar shouldn't produce this, but defensive code.
- **Go obligation:** `replicate-intent`. Log a warning, do nothing else.

### `errorMessage` is set on parse failure but the request still proceeds
- **Observed behavior:** Parse failures populate `errorMessage` and the rule engine sees `isQueryParsingSuccessful() == false`. Rules can choose to route on this signal; if rules don't, the request still proxies.
- **Source of behavior:** `gateway-design-intent`. The gateway is a proxy; it doesn't fail requests because *it* can't parse the SQL — the backend may parse it fine.
- **Go obligation:** `replicate-exactly`. Crucial: do not fail requests on gateway-side parse failures.

### `getQueryId` is set only for `CALL system.runtime.kill_query(...)`
- **Observed behavior:** `extractQueryIdFromCall` (`TrinoQueryProperties.java:454-464`) special-cases this one call, throwing if the argument is not a string literal.
- **Source of behavior:** `gateway-design-intent`. Cancellation requests must route to the backend that owns the query; the only way to know which backend that is from a `CALL` statement is to extract the query id and consult the sticky-routing map.
- **Go obligation:** `replicate-exactly`. The Go rewrite must recognize this exact procedure name and pull the query id out of the AST.
- **Notes:** Document this special case clearly. A general Go-side approach: walk the AST, find any `Call` with name matching `system.runtime.kill_query`, extract its first arg if a string literal.

### `clientsUseV2Format` JSON envelope is a commercial-fork-only protocol
- **Observed behavior:** When enabled, the gateway tries to JSON-decode the body as `{query, preparedStatements}` before falling back to assuming raw SQL.
- **Source of behavior:** `defensive-historical` — supports a commercial Trino fork's wire protocol.
- **Rationale:** The comment is explicit (`TrinoQueryProperties.java:650-651`): "known to be used by some commercial extensions of Trino, but is not implemented in Trinodb Trino".
- **Go obligation:** `defer-to-expert`. **Open question for `@trino-expert`:** is this still active and used? If the commercial extension has died or moved on, drop it.

## Implications for Go Rewrite

- **The contract surface is much narrower than the dependency suggests.** The gateway uses Trino's full parser (~700+ AST node classes) but only inspects ~30 specific node types (`AddColumn`, `Analyze`, `Call`, `Create*`, `Drop*`, `Insert`, `Query`, `Rename*`, `Set*`, `Show*`, `Table`, `TableFunctionInvocation`). Everything else is "recurse into children, ignore".
- **Three feasible Go-side strategies, in increasing order of completeness:**
  1. **Hand-port a minimal classifier.** Build a Go parser that recognizes only the statement-starting tokens (`SELECT`, `INSERT`, `UPDATE`, `DELETE`, `CREATE`, `DROP`, `ALTER`, `MERGE`, `EXECUTE`, `CALL`, `SHOW`, `DESCRIBE`, `EXPLAIN`) and extracts the immediately-following table reference using a simple identifier-and-quoted-identifier lexer. Covers 80%+ of operator rules. Misses: nested table refs in subqueries, CTEs, multi-statement parsing, `EXECUTE` substitution edge cases.
  2. **Port the Trino ANTLR grammar to ANTLR-Go.** ANTLR has a Go runtime. Significant up-front effort (the grammar is ~4000 lines and evolves); the gateway only needs the parser, not the analyzer. Provides the most-faithful behavior but introduces an ongoing Trino-version-sync burden.
  3. **Sidecar to the real Trino parser.** Run a small Java service exposing `POST /parse {sql, defaultCatalog, defaultSchema} → {queryType, tables, ...}`. Use Trino's actual parser. Highest fidelity, weakest deployment story.
- **Strategy 1 is recommended for v1.** The 30 node types are documented above; the resource-group classification table is in `StatementUtils.java:113-189`. A Go-native implementation is finite work and gives the team control. Document any subset of behavior not supported in v1 release notes.
- **The `$zstd:` + base64url prepared-statement encoding is a real protocol contract.** Go side: `github.com/klauspost/compress/zstd` + stdlib `encoding/base64.URLEncoding`.
- **The JSON shape for external routing-rules services is a documented contract.** Any operator running an external rules service depends on the field names and types listed above. Preserve them byte-for-byte in the Go rewrite.
- **Default-catalog/schema resolution is non-trivial.** A bug here cascades into wrong routing. The resolution table above is short — port it carefully and test heavily.
- **Body-size truncation behavior must be preserved.** Operators may already be tuning `maxBodySize`; the silent-skip-on-truncation semantic is a safety feature, not a bug.
- **Do not throw on gateway-side parse failures.** This is the single most important behavioral rule. The gateway is not a SQL validator; it is a router. Bad SQL is the backend's problem.

## Test Strategy Hooks

- **Test level:** unit (table-extraction from individual statements) + differential (run the same statement through Java and Go side, compare extracted fields). The Java test fixtures (`gateway-ha/src/test/resources/rules/routing_rules_trino_query_properties.yml`) are an oracle — port them to Go test inputs.
- **Fixtures required:** corpus of representative Trino queries covering all 30 statement types, plus quoted-identifier and 3-part-naming edge cases. Prepared-statement zstd-encoded header fixtures.
- **Observable signals:** the extracted `(queryType, resourceGroupQueryType, tables, schemas, catalogs, defaultCatalog, defaultSchema, errorMessage, queryId)` tuple.
- **Non-determinism risks:** none in parsing itself. Avoid testing against real Trino backends in this layer.

## Open Questions

- **`@trino-expert`:** Has the Trino team committed to keeping the AST class names stable across versions? Specifically: are `Query`, `Insert`, `CreateTable`, etc. names that rule files can safely assume forever?
- **`@trino-expert`:** Is `clientsUseV2Format` still actively used by any commercial Trino fork? If not, drop it from v1.
- **`@trino-expert`:** Is `analyzeRequest` defaulted to true or false in any documented configuration template? (Default-false in code; operators have to opt in.)
- **`@trino-expert`:** What is the expected behavior when zstd decompression of a `$zstd:`-prefixed prepared statement fails? (Currently the exception propagates uncaught.)
- **`@architect`:** Strategy 1 (hand-port a minimal classifier), 2 (ANTLR-Go), or 3 (sidecar) for the Go rewrite?
- **`@architect`:** Should the Go rewrite preserve the JSON shape for external routing-rules services exactly (including field name `isQueryParsingSuccessful` with the `is` prefix), or take the opportunity to v2 the protocol?

## Cross-references

- `[[architecture-overview.md]]`
- `[[jvm-dependencies-inventory.md]]`
- `[[mvel-rules-language.md]]` — `trinoQueryProperties` is the second-richest object in the MVEL rule context
- `[[routing-engine.md]]` — `FileBasedRoutingGroupSelector` and `ExternalRoutingGroupSelector` consume `TrinoQueryProperties`
- `[[query-backend-binding.md]]` — `getQueryId` for `kill_query` routing is consumed here
- `[[configuration-model.md]]` — `requestAnalyzerConfig:` section
