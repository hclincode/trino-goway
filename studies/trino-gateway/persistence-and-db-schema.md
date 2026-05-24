---
title: Persistence layer and DB schema
author: java-analyst
role: Java Analyst
component: trino-gateway
topics: [persistence, cluster-registry, observability]
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to: [architecture-overview.md, jvm-dependencies-inventory.md, backend-registry-and-mgmt-api.md]
---

# Persistence layer and DB schema

## Summary

Durable state is two tables in a relational database (MySQL, PostgreSQL, or Oracle): `gateway_backend` (the list of configured Trino backends, ~one row per cluster) and `query_history` (one row per proxied query, used to populate the admin UI history view and the per-minute distribution chart). All schema is in four short migrations (V1-V4) applied via Flyway at startup. Query history rows are auto-deleted on a 2-hour interval. The DAOs are JDBI annotation-based; query patterns are limited and easy to port. No transactions, no joins, no FK relationships.

## Key Findings

### Database choice

Driven by the `dataStore.jdbcUrl` config string. `FlywayMigration.getLocation(jdbcUrl)` switches on the URL prefix to pick the migration directory (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/FlywayMigration.java:27-39`):

| JDBC URL prefix | Migration set | Status |
|---|---|---|
| `jdbc:postgresql:` | `src/main/resources/postgresql/` | supported |
| `jdbc:mysql:` | `src/main/resources/mysql/` | supported |
| `jdbc:oracle:` | `src/main/resources/oracle/` | supported |
| anything else | n/a | `IllegalArgumentException` on startup |

`h2` exists as a test-only dependency (`pom.xml:333-338`) but is **not** routed by `FlywayMigration` — h2-based tests use a different bootstrapping path. The Go rewrite need only support the three production DBs.

### Schema

Two tables, no foreign keys, one index. After V4 migration (current state), both tables have these columns in all three databases:

#### `gateway_backend` (created V1, never altered)

| Column | Type (MySQL/Postgres) | Type (Oracle) | Notes |
|---|---|---|---|
| `name` | `VARCHAR(256)` PRIMARY KEY | `VARCHAR(256)` PRIMARY KEY | logical id of the cluster |
| `routing_group` | `VARCHAR(256)` | `VARCHAR(256)` | which group this cluster serves |
| `backend_url` | `VARCHAR(256)` | `VARCHAR(256)` | URL the gateway proxies to |
| `external_url` | `VARCHAR(256)` | `VARCHAR(256)` | URL exposed to clients (e.g., for redirects in `nextUri`) |
| `active` | `BOOLEAN` | `NUMBER(1)` | whether the cluster is currently receiving traffic |

Sources: `trino-gateway/gateway-ha/src/main/resources/mysql/V1__create_schema.sql:1-7`, `trino-gateway/gateway-ha/src/main/resources/postgresql/V1__create_schema.sql:1-7`, `trino-gateway/gateway-ha/src/main/resources/oracle/V1__create_schema.sql:1-7`. The DAO record at `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/dao/GatewayBackend.java:20-26` declares `name`, `routingGroup`, `backendUrl`, `externalUrl` as non-nullable; `active` is a `boolean` primitive.

#### `query_history`

Columns after all migrations (MySQL example; cross-DB differences noted inline):

| Column | Type (after V4) | Origin migration | Notes |
|---|---|---|---|
| `query_id` | `VARCHAR(256)` PRIMARY KEY | V1 | Trino-assigned query id from `id` field of POST /v1/statement response |
| `query_text` | MySQL `LONGTEXT`, Postgres `text`, Oracle `CLOB` | V1 (as `VARCHAR(256)`), widened V4 | full SQL of the query |
| `created` | `bigint` (MySQL/Postgres), `NUMBER` (Oracle) | V1 | epoch millis |
| `backend_url` | `VARCHAR(256)` | V1 | which backend served this query |
| `user_name` | `VARCHAR(256)` (nullable) | V1 | from `X-Trino-User` |
| `source` | `VARCHAR(256)` (nullable) | V1 | from `X-Trino-Source` |
| `routing_group` | `VARCHAR(255)` | V2 | which group was selected |
| `external_url` | `VARCHAR(255)` | V3 | external URL of the serving backend |

Index: `query_history_created_idx ON query_history(created)` (created in V1; Postgres uses `CREATE INDEX IF NOT EXISTS`, MySQL and Oracle do not).

Sources: V1 above; `V2__add_routingGroup_to_query_history.sql`, `V3__add_externalUrl_to_query_history.sql`, `V4__update_query_text_column_type.sql` in each DB's resources directory. The DAO record at `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/dao/QueryHistory.java:21-30` requires `queryId`, `queryText`, `backendUrl` non-null; `userName`, `source` nullable.

### Cross-DB migration quirks (the Go rewrite must preserve)

- **MySQL V1** uses `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX` (no `IF NOT EXISTS` on the index — MySQL didn't historically support it).
- **Postgres V1** uses `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`.
- **Oracle V1** uses plain `CREATE TABLE` (no `IF NOT EXISTS`) — relies on Flyway's migration history to skip re-runs.
- **V4 (query_text widening) differs per DB.** MySQL: `ALTER ... MODIFY query_text LONGTEXT`. Postgres: `ALTER COLUMN query_text TYPE text`. Oracle: a four-step add-clob/copy/drop-old/rename dance because Oracle does not allow widening to `CLOB` in place.

### Migration runner

`FlywayMigration.migrate(DataStoreConfiguration)` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/FlywayMigration.java:41-57`):
- Skipped entirely if `dataStore.runMigrationsEnabled == false` (`DataStoreConfiguration.java:99-102`, default `true`).
- Otherwise configures Flyway with `baselineOnMigrate=true`, `baselineVersion="0"` — meaning if the database already has tables but no `flyway_schema_history`, Flyway records baseline "0" and proceeds.
- Calls `flyway.repair()` before `flyway.migrate()` — `repair()` fixes the migration-history table if a previous migration left it in a bad state. This is conservative.
- Logs the number of migrations executed.
- **The runner is invoked from `HaGatewayLauncher.main(...)` before any Guice module loads** (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/HaGatewayLauncher.java:116`) — migrations complete before the HTTP server starts.

The Go-side rewrite using `golang-migrate/migrate` needs to translate this to: per-DB migration directory selection from JDBC-URL prefix, `baselineOnMigrate`-equivalent semantics (migrate's `force <version>` plus mark applied), and a `repair`-equivalent on startup. The four migrations themselves port directly after renaming `V1__name.sql` → `1_name.up.sql` (each migration also needs a `.down.sql` if the Go side wants reversible migrations; the Java side does not provide rollbacks).

### Connection management

One `Jdbi` instance per process, created in `HaGatewayProviderModule.createJdbi(DataStoreConfiguration)` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java:112-120`) with `SqlObjectPlugin` (for annotation-based DAOs) and a custom `RecordAndAnnotatedConstructorMapper` (for mapping result rows into Java records). JDBI's default connection handling is a connection-per-call against the JDBC URL — no explicit pool is configured at the gateway level (the JDBC driver may pool internally; for MySQL/Postgres this typically does not happen and connections are opened/closed per call).

`JdbcConnectionManager` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/JdbcConnectionManager.java:34-117`) wraps the `Jdbi` instance and provides:
- `getJdbi()` — the default, single-DB Jdbi.
- `getJdbi(routingGroupDatabase)` — **per-routing-group database support.** When called with a non-null string, it builds a new Jdbi pointing at `<original-jdbc-url-with-database-segment-replaced-by-the-routing-group>`. The URL parsing is path-based: it takes the substring after the first `/` and re-resolves the URI's last path segment to the given group name (`JdbcConnectionManager.java:67-103`). Goal: per-routing-group query history tables in separate databases. **A new Jdbi is created per call — no caching, no pool.**
- Background cleanup: a single-thread scheduled executor deletes from `query_history` where `created < (now - queryHistoryHoursRetention hours)` every 120 minutes, starting 1 minute after construction (`JdbcConnectionManager.java:105-115`). Default retention is 4 hours (`DataStoreConfiguration.java:23`).

### DAO surfaces

#### `GatewayBackendDao` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/dao/GatewayBackendDao.java:21-71`)

Seven operations:

| Method | SQL | Notes |
|---|---|---|
| `findAll()` | `SELECT * FROM gateway_backend` | full table scan |
| `findFirstByName(name)` | `SELECT * FROM gateway_backend WHERE name = :name LIMIT 1` | `LIMIT 1` not portable to Oracle; see Oracle quirk below |
| `create(...)` | `INSERT INTO gateway_backend (...) VALUES (...)` | five named parameters |
| `update(...)` | `UPDATE gateway_backend SET ... WHERE name = :name` | full update of non-PK columns |
| `deactivate(name)` | `UPDATE gateway_backend SET active = false WHERE name = :name` | |
| `activate(name)` | `UPDATE gateway_backend SET active = true WHERE name = :name` | |
| `deleteByName(name)` | `DELETE FROM gateway_backend WHERE name = :name` | |

**Oracle quirk:** `findFirstByName` uses `LIMIT 1` which Oracle does not support. This DAO is presumably only invoked when caller knows there is at most one row, or `LIMIT` is handled in a way I haven't traced. **Open question for `@trino-expert`:** does `GatewayBackendDao.findFirstByName` ever actually fire against an Oracle datastore in production, or is its only purpose duplication detection where the result is expected to be null?

#### `QueryHistoryDao` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/persistence/dao/QueryHistoryDao.java:25-189`)

More complex. Most queries have a `WithFetch` variant for Oracle (which doesn't support `LIMIT/OFFSET` and needs `FETCH FIRST ... ROWS ONLY`). Java default methods dispatch on a boolean `isLimitUnsupported` flag.

| Method | What it returns |
|---|---|
| `findRecentQueries(...)` | up to 2000 most-recent rows ordered by `created` desc |
| `findRecentQueriesByUserName(user, ...)` | as above, filtered by user |
| `findBackendUrlByQueryId(qid)` | single column lookup — but called only when in-memory cache misses; see below |
| `findRoutingGroupByQueryId(qid)` | as above for routing group |
| `findExternalUrlByQueryId(qid)` | as above for external url |
| `pageQueryHistory(user, externalUrl, queryId, source, limit, offset, ...)` | paged list with optional filters (each filter is `(:x IS NULL OR col = :x)`) |
| `count(user, externalUrl, queryId, source)` | row count matching paged-filter predicates |
| `findDistribution(created)` | aggregation: `SELECT FLOOR(created/60000) AS minute, backend_url, COUNT(1) AS query_count FROM query_history WHERE created > :created GROUP BY FLOOR(created/60000), backend_url` — used by the distribution chart in the admin UI |
| `insertHistory(...)` | append-only insert |
| `deleteOldHistory(created)` | retention cleanup |

There is no `UPDATE` on `query_history`. The DAO is append-only-plus-cleanup.

### Observed call sites (orientation; not exhaustive)

- `HaQueryHistoryManager` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/HaQueryHistoryManager.java`) — wraps `QueryHistoryDao` and serves the admin UI's history queries plus the `findBackendForQueryId` fallback.
- `HaGatewayManager` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/HaGatewayManager.java`) — wraps `GatewayBackendDao` for the admin API (add/update/delete cluster) and for the runtime "what backends are in this routing group?" lookup. See `[[backend-registry-and-mgmt-api.md]]`.
- Sticky-routing fallback: `RoutingTargetHandler.getPreviousCluster(...)` calls `routingManager.findBackendForQueryId(queryId)`, which in `StochasticRoutingManager` first checks an in-memory map; on miss it falls through to a DB lookup against `query_history.backend_url`. This means a query started before a gateway restart can still be routed correctly if the history row is still present.

### Database cache (Caffeine)

`DatabaseCacheConfiguration` exists; `HaGatewayManager` (per package overview) uses Caffeine to cache `findAll()` results from `GatewayBackendDao` to avoid hammering the DB on every routing decision. Cache TTL is operator-configurable. **Open question for follow-up study `[[backend-registry-and-mgmt-api.md]]`:** what is the cache invalidation discipline when admin API mutates the backends? — needs tracing.

## Behavior vs. Implementation Artifact

### Two storage backends with different `BOOLEAN`-vs-`NUMBER(1)` types
- **Observed behavior:** Schema is identical across DBs except where vendor-specific types are needed (`BOOLEAN` vs `NUMBER(1)`, `LONGTEXT` vs `text` vs `CLOB`).
- **Source of behavior:** `defensive-historical` — the DBs differ in supported types, not in semantics.
- **Go obligation:** `replicate-exactly`. The Go rewrite must keep the same physical column types per DB so an upgrade in place works — operators with an Oracle datastore will have an `active NUMBER(1)` column; the Go ORM/driver must read it as bool.
- **Notes:** `database/sql` with `lib/pq` handles BOOLEAN natively; with `go-sql-driver/mysql` BOOLEAN is `TINYINT(1)` and works with `sql.Scan` into a `bool`; with `sijms/go-ora` NUMBER(1) is read as int and the app needs to coerce.

### `baselineOnMigrate=true` + `baselineVersion="0"`
- **Observed behavior:** If the DB has tables but no Flyway history, Flyway records baseline "0" and proceeds (`FlywayMigration.java:51-52`).
- **Source of behavior:** `gateway-design-intent`. Operators upgrading from a pre-Flyway gateway version (or who created the tables manually) should not have to drop and re-run.
- **Rationale:** Forward compatibility for in-place upgrades.
- **Go obligation:** `replicate-intent`. `golang-migrate` has `force` semantics that approximate this; the Go side should support: "if `flyway_schema_history` exists, use it; if not but tables exist, baseline at 0; if neither, run from scratch."
- **Notes:** The Flyway history table is named `flyway_schema_history`. The Go side has two choices:
  1. Keep the Flyway history table format and write to it (operator-compatible).
  2. Migrate operators to a new `schema_migrations`-style table on first run.
  Architect decision.

### Query history cleanup runs at startup + every 120 minutes
- **Observed behavior:** Background single-thread executor sweeps old rows from `query_history` on a 2-hour cadence (`JdbcConnectionManager.java:105-115`).
- **Source of behavior:** `gateway-design-intent` — query history grows unbounded otherwise.
- **Rationale:** Default 4-hour retention with a 2-hour sweep means rows live 4-6 hours.
- **Go obligation:** `replicate-intent`. The Go side uses `time.NewTicker(2 * time.Hour)` and a context-aware goroutine. Default retention should be the same (4 hours) for operator-visible behavior parity.
- **Notes:** Operators on slow / non-self-managing DBs (e.g., Oracle with strict size limits) may rely on this. Do not silently change the cleanup cadence.

### Per-routing-group database via URL path rewriting
- **Observed behavior:** `JdbcConnectionManager.getJdbi(routingGroupDatabase)` constructs a new Jdbi every call by surgically replacing the database name in the JDBC URL's path (`JdbcConnectionManager.java:56-103`).
- **Source of behavior:** `gateway-design-intent`. The feature allows isolating per-routing-group query history into separate databases.
- **Rationale:** Multi-tenant or compliance-driven isolation.
- **Go obligation:** `defer-to-expert`. **Open question for `@trino-expert`:** is per-routing-group database isolation a documented, used feature, or a hook that one team added and nobody else relies on?
- **Notes:** The implementation has a sharp edge — it creates a fresh Jdbi (no connection pool) per call. If the Go rewrite preserves this, document the perf implications.

### `flyway.repair()` before every `migrate()`
- **Observed behavior:** Repair runs unconditionally on every startup (`FlywayMigration.java:54`).
- **Source of behavior:** `defensive-historical`. `repair()` cleans up a broken migration history (e.g., a previous migration that crashed mid-way and left an `In Progress` entry).
- **Rationale:** Idempotent self-heal.
- **Go obligation:** `replicate-intent`. `golang-migrate` exposes a `force` operation that approximates repair. The Go side should always run a repair-equivalent before applying.
- **Notes:** Document this behavior in operator docs.

### `findBackendForQueryId` reads from `query_history` after in-memory miss
- **Observed behavior:** A query started before the gateway restart can still be routed correctly because the persisted history row tells the gateway which backend it was sent to.
- **Source of behavior:** `gateway-design-intent` — graceful gateway restarts without breaking in-flight queries.
- **Go obligation:** `replicate-intent`. If the Go rewrite supports `query_history` at all, this fallback path must work.
- **Notes:** Implications for sequencing: the DB must be available *before* any client request, otherwise the fallback fails. Don't reorder startup.

## Implications for Go Rewrite

- **The schema is small enough to write by hand:** 2 tables, 8 columns total in the wide table, no foreign keys, no joins. A Go SQL layer using `database/sql` + `jmoiron/sqlx` works fine. No need for an ORM.
- **The DAO surface is also small:** 7 methods on backends, ~12 on history. Each is one query. A hand-rolled `repository.go` per table is ~200 LOC.
- **Cross-DB SQL dialects matter.** The Java code uses two SQL variants per query (`LIMIT` and `FETCH FIRST`) plus per-DB type definitions. The Go side has options:
  1. Write two query variants per method and dispatch on DB type (mirrors Java).
  2. Use a query-builder (`squirrel`, `goqu`) that emits dialect-aware SQL.
  3. Drop Oracle in v1. Significant scope reduction but cuts off an existing user base.
- **Migrations port directly with a filename rewrite.** Use `golang-migrate` and a per-DB filesystem-mounted source. Baseline-on-migrate semantics need a small custom wrapper.
- **In-process caches around the DAOs are user-invisible.** The Caffeine cache on `findAll()` and the in-memory `queryId → backend` map are perf optimizations, not contracts. The Go side can re-implement them with `sync.Map` + TTL or a real cache lib without operator-visible change.
- **The `flyway_schema_history` table is a real operator-visible artifact** — operators may have alerting on it. Either preserve the table name or document the rename.
- **No transactions, no joins.** This persistence layer is comically simple. Do not over-engineer the Go side.

## Test Strategy Hooks

- **Test level:** integration tests against real DBs (the Java side uses testcontainers for MySQL/Postgres/Oracle plus h2 for in-process speed). Unit tests against an in-memory shim are insufficient — vendor-specific SQL must be exercised.
- **Fixtures required:** migration replay on an empty DB (smoke test), upgrade-from-V0 (insert pre-existing tables, run migration, verify history table written with baseline 0), per-DB `LIMIT`/`FETCH FIRST` parity tests, cleanup-sweep timing test (inject clock).
- **Observable signals:** row counts after operations, schema introspection, log lines from migration runner (`Performed N migrations`).
- **Non-determinism risks:** the 120-minute cleanup ticker is not test-friendly with real wall-clock; inject a clock. testcontainers startup time may cause flakes on slow CI — pre-warm or use h2 for non-vendor-specific tests.

## Open Questions

- **`@trino-expert`:** Is per-routing-group database isolation (`getJdbi(routingGroupDatabase)`) a documented feature with known users, or a one-off contribution? If unused, the Go rewrite drops it.
- **`@trino-expert`:** Does `GatewayBackendDao.findFirstByName` ever execute against Oracle? (`LIMIT 1` is not Oracle-compatible.)
- **`@trino-expert`:** Should the Go rewrite preserve the `flyway_schema_history` table name, or rebrand to `schema_migrations`?
- **`@architect`:** Drop Oracle in v1, or commit to multi-dialect SQL from day one?
- **`@architect`:** ORM/sqlx/sqlc choice for the Go side?

## Cross-references

- `[[architecture-overview.md]]`
- `[[jvm-dependencies-inventory.md]]`
- `[[backend-registry-and-mgmt-api.md]]` — consumes `GatewayBackendDao`
- `[[query-backend-binding.md]]` — consumes `findBackendForQueryId`
- `[[configuration-model.md]]` — defines `dataStore:` config section
