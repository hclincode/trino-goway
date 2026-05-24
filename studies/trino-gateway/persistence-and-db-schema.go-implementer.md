---
title: Persistence layer — Go-implementer addendum (driver picks, dialect dispatch, Go-side gotchas)
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - persistence
  - cluster-registry
  - config
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/persistence-and-db-schema.md
  - trino-gateway/library-landscape-go-mapping.md
  - trino-gateway/jvm-dependencies-inventory.go-implementer.md
---

# Persistence layer — Go-implementer addendum (driver picks, dialect dispatch, Go-side gotchas)

## Summary

This study is the **Implementer-side addendum** to `[[persistence-and-db-schema.md]]` by `@java-analyst` (the source-of-truth schema/DAO catalog) and `[[library-landscape-go-mapping.md]]` by `@architect` (the proposed Go dep set). Read those first. This file lands four concrete decisions the Implementer needs before any `internal/persistence` package is written: (1) `database/sql` + `jmoiron/sqlx` + per-DB driver — agree with architect, with one nuance about Oracle; (2) `pressly/goose` over `golang-migrate` for migration driving — divergence from architect, with reason; (3) a small `dialect` enum + per-method query-variant dispatch instead of a SQL builder, because the dialect surface is tiny (4 LIMIT-vs-FETCH-FIRST sites, 1 BOOLEAN-vs-NUMBER(1)); (4) drop per-routing-group database isolation in v1 pending operator-use evidence from `@trino-expert`. Plus three Go-side gotchas neither prior study surfaces: BOOLEAN scanning across all three drivers needs `sql.NullBool` discipline, the cleanup goroutine needs an explicit `clock.Clock` seam for tests, and `db.SetMaxOpenConns`/`SetMaxIdleConns` must be configured explicitly because Go's defaults are unbounded-idle which behaves badly against the same DBs the Java side runs against.

## Key Findings

### Concrete picks for the persistence stack

| Concern | Pick | Rationale |
|---|---|---|
| SQL access | `database/sql` + `jmoiron/sqlx` | DAO surface is small (`GatewayBackendDao`: 7 methods, `QueryHistoryDao`: ~12 — see `[[persistence-and-db-schema.md]]` § "DAO surfaces"). `sqlx` adds named parameters and `StructScan` without imposing an ORM. `sqlc` is tempting for type-safety but adds a codegen step that is overkill at this size. |
| Postgres driver | `jackc/pgx/v5` registered via `pgx/v5/stdlib` | Pure Go, actively maintained, the de facto choice. Use the `stdlib` adapter so we stay on `database/sql` interfaces — switching to native `pgx` later is a search-and-replace if we ever need `COPY` or `LISTEN`. |
| MySQL driver | `go-sql-driver/mysql` | The only serious option. Tag `parseTime=true` and `tls=preferred` in the DSN explicitly. |
| Oracle driver | **Defer to a "no Oracle in v1" decision** (see `[[jvm-dependencies-inventory.go-implementer.md]]` divergence #3); if forced, `sijms/go-ora/v2` over `godror`. | `go-ora` is pure-Go (no cgo, no Oracle Instant Client install). `godror` is cgo + Instant Client — a deployment ergonomics step backward from the Java side, which only needs `ojdbc11`. The cost of Oracle parity is non-trivial and should be a `@team-lead` decision. |
| Migrations | `pressly/goose` (divergence from architect's `golang-migrate`) | Goose has a cleaner programmatic API (`goose.Up(db, migrationsDir)`) matching the Java flow where `FlywayMigration.migrate(...)` is called inline from `HaGatewayLauncher.main` (`HaGatewayLauncher.java:116`). `golang-migrate` is more often run as a separate binary. Both can drive the existing `V?__*.sql` files. Either way needs a small wrapper for `baseline-on-migrate` behavior. |
| Connection pool | `*sql.DB` with `SetMaxOpenConns(50)`, `SetMaxIdleConns(10)`, `SetConnMaxLifetime(30 * time.Minute)`, `SetConnMaxIdleTime(5 * time.Minute)` | Java side has no explicit pool (JDBI default is per-call connection). Go's `database/sql` defaults to `MaxOpenConns=0` (unbounded) and `MaxIdleConns=2` — both wrong shapes for this workload. Explicit numbers tied to typed config (see "Go-side gotcha #3"). |
| Migration source format | Keep `V?__name.sql` filenames as-is | Goose accepts these via a thin adapter (or rename to `00001_name.sql` once; trivial). Either works. `golang-migrate` wants `<v>_<name>.up.sql` + `.down.sql` pairs — that's a rename per file plus invented `.down.sql`s (the Java side has no rollbacks). Another small reason to prefer goose. |

### Three divergences from `[[persistence-and-db-schema.md]]` or `[[library-landscape-go-mapping.md]]`

These need resolution before the `internal/persistence` package is written; both of us coding to different libraries produces churn.

- **Migration tool.** Architect picks `golang-migrate/migrate` (`[[library-landscape-go-mapping.md]]` "DB: ... Migrations"); I recommend `pressly/goose` for the reasons in the table above. Small divergence — happy to defer if there's a reason I'm missing.
- **Per-routing-group database isolation.** `[[persistence-and-db-schema.md]]` correctly flags this as an open question to `@trino-expert`. **My Implementer position: don't port it in v1 unless `@trino-expert` returns evidence of operator use.** It's a sharp-edge feature (`Jdbi.create(...)` per call, no pool, URL surgery — see `JdbcConnectionManager.java:56-103`) and porting it as-is would mean per-routing-group `*sql.DB` instances, separate pools, all wired through a `map[string]*sql.DB` with lifecycle handling. Significant scope for a feature nobody may use.
- **DB cache architecture.** `[[persistence-and-db-schema.md]]` § "Database cache" describes Caffeine caching `findAll()` on `GatewayBackendDao`. Architect's library landscape proposes `expirable` or `ristretto`. **My recommendation: `hashicorp/golang-lru/v2` + `golang.org/x/sync/singleflight`** — same reasoning as the queryId cache in `[[concurrency-and-lifecycle-model.go-implementer.md]]`. Without single-flight, under load on a cold cache the gateway can hammer the DB with N concurrent `findAll()`s. This divergence is already in the concurrency study's Open Questions; flagging again here because it lands in this package.

### Three Go-side gotchas neither prior study surfaces

These are trap-detection findings — not picks.

- **BOOLEAN-vs-NUMBER(1) scanning needs explicit driver-aware handling.** Postgres native `BOOLEAN` scans cleanly into Go `bool` via `lib/pq`/`pgx`. MySQL's `TINYINT(1)` also scans into `bool` with `go-sql-driver/mysql` (config flag `parseTime=true` is unrelated; bool handling is default). Oracle's `NUMBER(1)` scanned via `sijms/go-ora/v2` comes back as `int64`/`float64` depending on metadata — does **not** auto-coerce to `bool`. Either: (a) define `GatewayBackend.Active` as `bool` and add a per-dialect scan adapter, or (b) use `sql.NullBool` and convert in the DAO. I lean (a) with a tiny `dialect.ScanBool` helper. The Java side gets this "for free" via JDBI; the Go side must own it.
- **The cleanup goroutine in `JdbcConnectionManager.java:105-115` calls `scheduleWithFixedDelay` against the system clock.** Direct port: `time.NewTicker(2 * time.Hour)`. **Wrong for tests** — integration tests of retention behavior would otherwise need real wall-clock waits or scary `time.Sleep` hacks. The retention sweep component must take a `clock.Clock` (`jonboulle/clockwork` or `benbjohnson/clock`) in its constructor so tests inject a fake clock and `clock.Advance(3 * time.Hour)` to verify a sweep happens. Same seam pattern as the cluster-stats monitor — see `[[concurrency-and-lifecycle-model.go-implementer.md]]` "Fake-clock seam".
- **Connection-pool defaults are an operational footgun.** `database/sql` defaults to `MaxOpenConns=0` (unbounded) and `MaxIdleConns=2`. Against a small MySQL/Postgres deployment with `max_connections=100`, the gateway can exhaust the DB during a routing-cache cold start. The Java side avoids this because JDBI opens and closes connections per call (no idle pool to worry about, but also no reuse). For the Go side: **explicit pool numbers must be in the `DataStoreConfiguration` Go equivalent** with sane defaults (`MaxOpenConns=50`, `MaxIdleConns=10`, `ConnMaxLifetime=30m`, `ConnMaxIdleTime=5m`). Operators upgrading from the Java side may see new behavior — call this out in the migration guide.

### What the Java code tells us about Go shape

(Cross-references to `[[persistence-and-db-schema.md]]`; not duplicating the schema catalog.)

- **`DataStoreConfiguration.java:16-108` is a flat 7-field POJO.** Maps cleanly to a Go struct with `yaml.v3` tags. Fields: `jdbcUrl`, `user`, `password`, `driver`, `queryHistoryEnabled` (default `true`), `queryHistoryHoursRetention` (default `4`), `runMigrationsEnabled` (default `true`). The `driver` field is unused in the Java code (drivers are loaded by JDBC URL prefix in `FlywayMigration.getLocation`); the Go version can drop it or keep it as a no-op for config parity. Recommend dropping with a one-line migration-guide note.
- **`FlywayMigration.getLocation(jdbcUrl)` (`FlywayMigration.java:27-39`) is a URL-prefix switch.** Reproduce in Go as a `dialect.FromJDBCURL(string) (Dialect, error)` function returning an enum (`Postgres | MySQL | Oracle`) and an embedded `fs.FS` per dialect for migration files via `go:embed`. Embedding migrations means no runtime file lookup — the binary is self-contained.
- **The DAO surface has exactly 4 dialect-divergent query sites** (`pageQueryHistory`, `findRecentQueries`, `findRecentQueriesByUserName`, plus `findFirstByName` which uses `LIMIT 1` — see `[[persistence-and-db-schema.md]]` "DAO surfaces"). For 4 sites, a `dialect.IsOracle()` check + per-method query-string variant is cleaner than dragging in `Masterminds/squirrel` or `doug-martin/goqu`. ~15 LOC of conditional SQL beats a 5kLOC dep.
- **Two pre-startup ordering rules to preserve** (from `[[persistence-and-db-schema.md]]`): migrations must complete before the HTTP server accepts traffic, and the DB must be available before any sticky-routing fallback lookup. Both belong in the `internal/lifecycle` ordering (see `[[concurrency-and-lifecycle-model.go-implementer.md]]`). Concrete order: `migrate → dataSource → routingCache (warm) → httpServer`.

## Behavior vs. Implementation Artifact

### Per-call Jdbi vs. per-process `*sql.DB`
- **Observed behavior:** JDBI's default is no connection pool — `Jdbi.create(url, user, pw)` builds a Jdbi that opens a JDBC connection per call. `JdbcConnectionManager.java:62-65` constructs a fresh Jdbi for every `getJdbi(routingGroupDatabase)` invocation in the per-routing-group path.
- **Source of behavior:** `jvm-artifact` — JDBI's default behavior, plus a per-group convenience that "happens to work" because the connect-per-call cost is amortized over query latency.
- **Rationale:** Simplicity over performance. The persistence layer is not on the hot proxy path.
- **Go obligation:** `replicate-intent`, not `replicate-exactly`. The intent is "open connections as needed, don't leak"; the Go shape is a single `*sql.DB` with an explicit pool. **Do not** mirror the per-call `Jdbi.create` pattern — that would be a worse-than-Java footgun in Go where `*sql.DB`'s pool is the standard abstraction.
- **Notes:** if per-routing-group DBs are kept, `map[string]*sql.DB` with explicit lifecycle (close on shutdown). Otherwise drop the feature.

### `flyway.repair()` before every `migrate()`
- **Observed behavior:** `FlywayMigration.java:54` runs `repair()` unconditionally to clean up partially-failed migrations.
- **Source of behavior:** `defensive-historical`.
- **Rationale:** Idempotent self-heal on restart.
- **Go obligation:** `replicate-intent`. Goose exposes `goose.Reset` (different semantics — drops everything) and a `GooseDBVersion` table. The closest equivalent is: on startup, query the goose version table; if any row has `tstamp` but no `version_id` (or a row was never committed), the migration crashed mid-way — log a warning and proceed. `golang-migrate` has `Force(version int)` which is closer to Flyway's repair in shape. Either way: ~30 LOC wrapper in `internal/persistence/migrate`.
- **Notes:** if this turns out to need significant code (>100 LOC), reconsider the architect's `golang-migrate` pick — its `Force` may give us repair-like semantics for less code.

### `baselineOnMigrate=true` + `baselineVersion="0"`
- **Observed behavior:** `FlywayMigration.java:51-52`. If `flyway_schema_history` doesn't exist but tables do, record baseline "0" and proceed.
- **Source of behavior:** `gateway-design-intent` — backward compatibility for operators upgrading from pre-Flyway gateway versions.
- **Rationale:** In-place upgrade without dropping tables.
- **Go obligation:** `replicate-intent`. Wrapper logic: check for the goose/golang-migrate version table; if absent but `gateway_backend` exists, insert the equivalent of "baseline" marker and proceed. **Critical for operator-visible behavior** — without this, the first run of the Go gateway against a Java-side-populated DB will try to re-run V1 against existing tables and fail with `relation already exists`.
- **Notes:** the migration-history table name is a separate decision (see `[[persistence-and-db-schema.md]]` Open Questions). My implementer view: keep `flyway_schema_history` for ops compatibility. The metadata format inside the table differs between Flyway and goose/migrate, but operators don't usually query it — they just care that "the table exists and isn't empty" matches their alerting.

### Silent V1-vs-V4 column-type evolution
- **Observed behavior:** V1 creates `query_text VARCHAR(256)`; V4 widens to `LONGTEXT`/`text`/`CLOB` per DB. Oracle's V4 does a 4-step add-clob/copy/drop/rename dance (`oracle/V4__update_query_text_column_type.sql:1-4`) because Oracle disallows in-place widening to `CLOB`.
- **Source of behavior:** `defensive-historical` — operators with V1 already applied need an in-place upgrade path.
- **Go obligation:** `replicate-exactly`. The migration files themselves can be copied verbatim into the Go binary via `go:embed`. **Do not** rewrite V4 — it's a DB-side decision, not a code-side one. Tests should run the full V1→V4 sequence against each DB (use `testcontainers-go` for MySQL/Postgres; Oracle testcontainer is heavy — see Test Strategy).

## Implications for Go Rewrite

- **Package layout:** `internal/persistence` with subpackages `migrate/`, `dialect/`, `backend/` (DAO), `history/` (DAO). The `dialect/` package owns the enum and per-dialect SQL strings. The DAOs depend on `*sqlx.DB` and a `dialect.Dialect` — nothing else.
- **One small `dialect` enum, not a SQL builder.** With only 4-5 dialect-divergent sites, conditional SQL strings keep the code obvious. Re-evaluate if dialect divergence grows past 10 sites.
- **`go:embed` the migration files** from the Java side's `src/main/resources/{postgresql,mysql,oracle}/` (copied into the Go repo's `internal/persistence/migrate/sql/`). Treat them as source-of-truth synced periodically. Embedded migrations keep the binary self-contained — no runtime file lookup.
- **Explicit pool sizing in config.** Add `MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`, `ConnMaxIdleTime` to the Go `DataStore` config struct with defaults that won't surprise operators. Document the new knobs.
- **The cleanup goroutine takes `ctx context.Context` and a `clock.Clock`.** Same shape as the cluster-stats monitor. Composes into `internal/lifecycle`.
- **Drop the `driver` field from `DataStoreConfiguration` in the Go version** (unused in Java; redundant with `jdbcUrl` prefix). One-line migration note.
- **Defer Oracle and per-routing-group DB.** Both are scope creep for v1 absent evidence of operator use. Flag for `@team-lead` decision.

## Test Strategy Hooks

- **Test level:** integration (against real DBs via `testcontainers-go`) for SQL behavior; unit tests for the dialect dispatcher and migrate wrapper.
- **Fixtures required:**
  - **Per-dialect migration replay:** start an empty Postgres/MySQL container, run all migrations, assert schema introspection matches the Java side's output (column types, indexes, PKs).
  - **Baseline-on-migrate fixture:** populate a container with the V1 schema directly (no version table), run migrate, assert the version table is created with baseline marker and subsequent migrations proceed.
  - **Fake-clock cleanup test:** inject `clockwork.NewFakeClock`, insert 100 rows with `created = now - 5h` and 100 with `created = now`, advance clock by 2h+1s, assert the first batch is gone and the second remains. No real `time.Sleep`.
  - **Pool exhaustion test:** set `MaxOpenConns=2`, spawn 100 goroutines doing `findAll()`, assert all complete within bounded time (no deadlock) and that `db.Stats().WaitCount > 0` (proving the pool is doing its job).
  - **Oracle CLOB-roundtrip fixture** (if Oracle in scope): insert a 10MB `query_text`, retrieve, assert byte-equality. This is the V4 migration's purpose.
- **Observable signals:** schema diffs (use `pgdump --schema-only` / `mysqldump --no-data` and compare), row counts before/after cleanup, `db.Stats().{OpenConnections,InUse,Idle,WaitCount}` for pool behavior.
- **Non-determinism risks:** testcontainers startup time is the main flake source — pre-warm in a `TestMain` and reuse across the package. Oracle testcontainer is ~2 minutes cold-start; budget accordingly or skip in CI by default.
- See paired QA study (none yet — flagging `@go-qa` for paired coverage of the persistence layer tests).

## Open Questions

- `@architect`: **`pressly/goose` vs `golang-migrate/migrate/v4`?** Small divergence with real ergonomic differences (programmatic API, file naming). Pick one before either of us writes the migrate wrapper.
- `@architect`: **`hashicorp/golang-lru/v2` + `singleflight` vs `expirable`/`ristretto` for the `findAll()` cache?** Same question as in the concurrency study — answer should be consistent across both. Single-flight matters whenever there's a DB-backed loader behind the cache.
- `@architect`: confirm: drop the `driver` field from `DataStoreConfiguration` in the Go config? It's unused in the Java code (drivers resolved by URL prefix).
- `@architect`: confirm: keep the `flyway_schema_history` table name for ops compatibility, or rebrand to `goose_db_version` / `schema_migrations`?
- `@team-lead`: **Oracle in v1?** Goes hand-in-hand with the cgo-or-pure-Go driver decision. See `[[jvm-dependencies-inventory.go-implementer.md]]` Open Questions.
- `@trino-expert`: per-routing-group database isolation — documented feature with known users, or a one-off contribution? If unused, the Go rewrite drops it and the persistence layer gets dramatically simpler.
- `@trino-expert`: `GatewayBackendDao.findFirstByName` uses `LIMIT 1` which Oracle does not support — does this DAO actually fire against Oracle, or is the `LIMIT 1` dead-code-against-Oracle? (Inherited from `[[persistence-and-db-schema.md]]` open questions.)
- `@qa-tech-lead`: are operator-facing DB-side artifacts (migration table name, column types) considered part of the contract we're preserving, or can we rebrand freely with a one-line migration note?

## Cross-references

- `[[persistence-and-db-schema.md]]` — schema, DAO catalog, observed call sites. This addendum does not re-catalog those.
- `[[concurrency-and-lifecycle-model.go-implementer.md]]` — the cleanup goroutine pattern, the cache + single-flight argument, the lifecycle ordering rules.
- `[[jvm-dependencies-inventory.go-implementer.md]]` — Oracle driver choice rationale, migration tool divergence.
- `[[library-landscape-go-mapping.md]]` — architect's proposed Go dep set this study diverges from in two places.
- `[[config-coupling-depth.architect.md]]` — `DataStoreConfiguration` is one of the ~20 sub-configs catalogued there.
