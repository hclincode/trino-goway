# Coding Conventions — trino-goway

**Status:** Draft — 6 open decisions pending team-lead sign-off (marked `[OPEN]`)  
**Applies to:** All Go code under `internal/`, `cmd/`, and `pkg/`

---

## 1. Module and Package Layout

**Module path:** `github.com/hclincode/trino-goway` — must match `option go_package` in all `.proto` files.

**Package naming rules:**
- No stutter: `routing.Router`, not `routing.RoutingRouter`
- No `_pkg`, `_lib`, `_util`, `_common`, `_helpers` suffixes or package names
- No `types/`, `utils/`, `common/`, or `helpers/` packages — these obscure ownership
- Package names are lowercase single words; underscores only in generated packages (e.g. `routerpb`)

**`internal/` vs `cmd/`:**
- All reusable domain-bearing code lives under `internal/`
- `cmd/trino-goway/main.go` only: composition root, signal handling, `slog` setup, process exit — no business logic
- `cmd/goway-migrate-config/main.go` only: CLI flag parsing, calls into `internal/config` — no migration logic inlined
- If two `cmd/` binaries need the same helper, it lives in `internal/`, never duplicated

**Package doc comments:** Every package must have a one-sentence doc comment in `doc.go` naming its responsibility:
```go
// Package routing selects a backend cluster for each inbound Trino request.
package routing
```

**Shared types:** Define in the package that owns the concept; import it. `persistence` owns `Backend` and `QueryHistoryRecord`. `routing` owns `RoutingGroup`. Never create a `types/` or `models/` package — it creates import cycles and inverts ownership.

---

## 2. Naming

**Exported vs unexported:** Export only when a different package must use it. If a type exists only so a test in another package can assert on it, redesign the test, not the export.

**Interface naming:** Define in the **consuming** package; name by behavior:
- `routing.Selector` not `routing.ExternalRoutingSelector`
- `proxy.Router` not `routing.RouterInterface`
- No `Service`, `Manager`, or `Handler` suffixes unless the type literally implements `http.Handler`

**Constructor naming:** Always `New{Type}`. Never `Create`, `Build`, `Make`, `Init`, or `Setup`.

**Error variable naming:**
- Sentinel errors: `var ErrBackendNotFound = errors.New("backend not found")` — prefix `Err`, exported
- Custom error types: `type OverflowError struct { ... }` — suffix `Error`, exported

**Config struct naming:** Each package owns its config as `Config`: `routing.Config`, `proxy.Config`. The package name provides the namespace. The top-level `config.Config` holds them as nested fields (see §4-D).

**Receiver naming:** Single-letter abbreviation from the type: `p` for `*Proxy`, `r` for `*Router`, `m` for `*Monitor`. Never `self` or `this`. Consistent across all methods on a type.

---

## 3. Error Handling

**Always wrap at package boundaries:**
```go
fmt.Errorf("routing: select backend: %w", err)
```
Prefix names the package or operation. Format: `"package: operation: detail"` — lowercase, no trailing period. Errors chain: `"proxy: read body: routing: select backend: context deadline exceeded"`.

**Never swallow errors.** `_ = someFunc()` is forbidden except for documented-infallible writers (e.g. `(*bytes.Buffer).Write`). When discarding, add a comment explaining why it is safe.

**Choosing the right error form:**
- `errors.Is(err, ErrXxx)` — caller needs to branch on a known condition
- `errors.As(err, &target)` — caller needs fields from a structured error type
- Never compare `err.Error()` strings to detect error types — wrapping breaks it

**No `panic` in library code.** Permitted only in `main.go` during startup for a genuinely unrecoverable state. Never in request handlers, goroutines, or constructors.

**HTTP handlers — always write a response.** Every code path calls `writeError` or writes a success response. A bare `return` without writing sends an implicit 200, which is always wrong.

**Log OR return, never both.** Returning an error means the handler boundary will log it. Handling an error internally means log it and do not propagate. Duplicate log lines make analysis unreliable.

---

## 4. Interfaces

**Define in the consumer package.** `proxy` defines `type Router interface { Route(...) }` and accepts it as a constructor parameter. `routing` defines `*Router` which satisfies it without importing `proxy`.

**1–3 methods maximum.** More signals the abstraction is doing too much or should be split.

**Accept interfaces, return concrete types.** Constructor parameters are interfaces (where a test fake is needed). Return types are concrete structs.

**Skip interfaces when unnecessary.** If there is exactly one implementation and no test fake is needed, use a concrete struct. Add the interface when a second implementation or fake appears.

**Compile-time interface assertion** (one per type, at the top of the file):
```go
var _ proxy.Router = (*Router)(nil)
```

---

## 5. Concurrency and Context

**`context.Context` is always the first parameter** of any function that does I/O, makes a network call, queries the DB, or blocks on an external resource. Never stored in a struct field.

**Goroutine ownership.** The goroutine that launches a goroutine ensures it terminates. Every launch site has a comment stating the termination condition:
```go
// goroutine exits when ctx is cancelled
go func() { ... }()
```

**Use `errgroup` for concurrent fan-out:**
- `errgroup.Group` when all results are needed (e.g. health-check fan-out per backend tick)
- `errgroup.WithContext` when the first failure should cancel remaining work (e.g. HEAD-probe fan-out in the 3-step cache-miss recovery chain — first healthy backend cancels all others)
- Never use bare `sync.WaitGroup` for fan-out that can fail — errors cannot propagate through `WaitGroup`

**No `time.Sleep` for coordination.** Use channels, `sync.Cond`, or context deadlines. For retry backoff:
```go
select {
case <-time.After(backoff):
case <-ctx.Done():
    return ctx.Err()
}
```

**`sync.WaitGroup.Add` before the goroutine starts.** Placing it inside creates a window where `wg.Wait()` can return prematurely.

**Channel direction in signatures:** `chan<- T` send-only, `<-chan T` receive-only. Never bare `chan T` unless the function manages both ends.

---

## 6. Struct and Constructor Conventions

**Constructors (`New{Type}`) only:** assign fields and validate config. No I/O, no goroutines, no network connections. Safe to call in tests without side effects.

**`Start(ctx context.Context) error`:** network access, DB connections, long-running goroutines. Called by composition root in dependency order.

**`Stop(ctx context.Context) error`:** drain in-flight requests, close connections, cancel goroutines. Must respect the context deadline. Deferred by composition root in reverse construction order.

**Config validation in `Start`:** fail fast with a descriptive error:
```go
return fmt.Errorf("proxy: responseSize must be > 0, got %d", cfg.ResponseSize)
```
Never silently apply a default for an invalid config value.

**No exported fields on types with methods.** Config structs (plain data, YAML/JSON unmarshaling) may have exported fields; structs with behavior have all unexported fields.

---

## 7. Logging (slog)

**Always structured key-value pairs:**
```go
slog.Info("backend selected", "backend", b.URL, "queryId", qid)
// NOT: slog.Info(fmt.Sprintf("selected %s for %s", b.URL, qid))
```

**Log levels:**
| Level | Use |
|---|---|
| `DEBUG` | Per-request detail (routing decision, cache hit/miss) — disabled in production |
| `INFO` | Lifecycle events (server started, backend added/removed, migration applied) |
| `WARN` | Degraded-but-continuing state (external routing unreachable, fell back to default group) |
| `ERROR` | Failures affecting a specific request (upstream 5xx, auth error) |

**Required keys on proxy-path log lines:** include `"backend"` and `"queryId"` where known.

**Never log sensitive data.** Auth tokens, cookie values, HMAC secrets, passwords. Log presence, not value:
```go
slog.Debug("auth header present", "present", r.Header.Get("Authorization") != "")
```

**One log line per event.** No multi-line messages; log slices as attributes: `slog.Info("backends probed", "backends", urls)`.

---

## 8. Comments

**Exported types and functions always have a doc comment** starting with the exported name:
```go
// Router selects a backend cluster for the given request.
type Router struct { ... }
```

**Unexported symbols:** comment only when the WHY is non-obvious — a hidden constraint, a spec reference, a bug workaround. Never restate what the code does.

**No `// TODO`, `// FIXME`, `// HACK`.** Open a GitHub issue and reference it: `// See #42.`

**No commented-out code.** Version control preserves history.

---

## 9. Import Organization

Three groups, blank-line separated:
```go
import (
    // 1. stdlib
    "context"
    "net/http"

    // 2. external
    "github.com/go-chi/chi/v5"
    lru "github.com/hashicorp/golang-lru/v2"

    // 3. internal
    "github.com/hclincode/trino-goway/internal/config"
    "github.com/hclincode/trino-goway/internal/routing"
)
```

`goimports` enforces this automatically — run on editor save; CI fails on unformatted files. Generated files (e.g. `routerpb/*.go`) are excluded.

---

## 10. HTTP Handler Conventions

**Handler signature:** `func (x *X) ServeHTTP(w http.ResponseWriter, r *http.Request)`. No custom context types.

**Route parameters:** `chi.URLParam(r, "name")` for path params. `r.URL.Query().Get("name")` for query string. Never `r.Form` on proxy-path handlers — it triggers body parsing which corrupts the request body.

**Middleware:** returns `http.Handler`, calls `next.ServeHTTP(w, r)`. Standard `net/http` chain — no framework-specific context types.

**Error responses:** JSON body, `Content-Type: application/json`, shared `writeError` helper in `admin` package:
```go
func writeError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(struct {
        Error string `json:"error"`
    }{Error: msg})
    // _ is the one permitted discard: Write errors are unactionable after WriteHeader
}
```

---

## 11. Formatting and Tooling

- **`goimports`** (subsumes `gofmt`): required; CI fails on diff
- **`go vet`**: required
- **`golangci-lint`**: required with the linter set in §D3
- **Line length**: no hard limit; wrap signatures at ~100 chars when they exceed screen width
- **`_` variables**: permitted only in blank imports, range-only loops, and compile-time interface assertions (§4)

---

## 12. Test File and Package Structure

**File placement:** `foo_test.go` alongside `foo.go` in the same directory. No separate `tests/` tree.

**Package declaration:** use the external package (`package routing_test`) for integration and behavioral tests. Use the internal package (`package routing`) only when testing unexported identifiers that genuinely cannot be observed externally; document the reason in a comment.

**Test helpers:** `t.Helper()` as the first line of every helper function — failure line numbers point to the call site, not the helper.

**No `init()` in test files.** Setup belongs in `TestMain` or `t.Cleanup`.

**`TestMain` scope:** `cmd/trino-goway` gets `TestMain` with `goleak.VerifyTestMain(m)`. Packages that spin up shared containers also get a `TestMain` — those must also call `goleak.VerifyTestMain`. Individual packages use `t.Cleanup` for per-test teardown.

---

## 13. Table-Driven Tests

**Mandatory for multi-case tests.** Any test with more than one logical case must be table-driven using `[]struct{ name string; ... }` or a named struct type.

**`name` field required.** Every table entry has a `name string`. Use it as the subtest label: `t.Run(tc.name, func(t *testing.T) { ... })`. Names must be human-readable: `"empty queryId"`, not `"case 3"`.

**Golden-value comparisons.** Wire-format tests (HMAC cookie bytes, exact JSON payloads, HTTP snapshots) compare byte-for-byte against a stored `testdata/` fixture — never re-derive the expected value in the test itself.

---

## 14. Assertions — `[OPEN: D1]`

**Team decision required — see Open Decisions section.**

**If stdlib only (Option A):** use `t.Errorf` (non-fatal) and `t.Fatalf` (fatal).

**If testify (Option B — architect recommendation):** `require` (fatal) for preconditions; `assert` (non-fatal) for outcome assertions. Never mix them backwards. Only `require` and `assert` packages — no `mock`, no `suite`.

---

## 15. Database and Container Tests

**No mocking the database.** Integration tests that exercise persistence code use real containers via `testcontainers-go`. No mock DB interfaces, stub drivers, or in-memory SQLite.

**Container lifecycle.** Start in `TestMain` (suite-scoped) or top of test function (test-scoped). Always register `t.Cleanup` calling `container.Terminate`. A leaked container is a hard CI failure.

**Port allocation.** Use `testcontainers-go`'s `container.MappedPort` — never hardcode `5432` or `3306`. Parallel test runs must not conflict.

**Migrations.** Run `goose up` against the test container before each suite using the project's migration files.

**Build tags:**
- Fast unit tests: `//go:build !integration`
- Container tests: `//go:build integration`
- E2E tests (live Trino): `//go:build e2e`

CI runs unit + integration. Local default (`go test ./...`) runs unit only.

---

## 16. Goroutine Leak Detection

**`goleak.VerifyTestMain(m)` in every `TestMain`.** This includes `cmd/trino-goway` and any package-level `TestMain` that starts shared infrastructure.

**Long-lived goroutines in tests.** Tests that start a background goroutine (e.g. monitor goroutine) must register a `t.Cleanup` that calls `Stop(ctx)` and blocks until it returns. Goleak checks after cleanup runs.

**No `time.Sleep` for synchronization.** Use channels, `sync.WaitGroup`, or condition variables. `time.Sleep` in tests is a flakiness signal.

---

## 17. Race Detector

**All CI runs use `go test -race ./...`.** No package is exempt.

**No suppression.** No `//nolint:race` or `t.Skip()` to suppress race findings. Fix the race.

**Shared fixtures.** Test fixtures written once and read by concurrent subtests must be initialized in `TestMain` or `sync.Once` before subtests run.

---

## 18. Proxy Seam Tests (Hard Invariant Coverage)

**One test per seam asserting the invariant directly.** A "200 OK" response is not sufficient.

**Naming:** `TestProxy_Seam{N}_{description}`:
- `TestProxy_Seam1_NeverRewriteResponseBody`
- `TestProxy_Seam2_RedirectFollowingDisabled`
- `TestProxy_Seam3_CacheWriteBeforeResponseFlush`
- `TestProxy_Seam6_KillQueryRegexRouting`
- `TestProxy_Seam7_ThreeClientPoolIsolation`

**Key seam implementations:**
- **Invariant #1:** assert byte-identical response body between backend and client
- **Invariant #2:** assert 3xx from backend is forwarded unchanged, not followed
- **Invariant #3:** use a delayed fake backend; assert cache is populated before response reaches client's `ResponseRecorder`
- **Invariant #4:** simulate cache miss; assert all three recovery steps (history → HEAD probe → default) are attempted in order
- **Invariant #7:** assert proxy, monitor, router clients are distinct pointer values with correct `CheckRedirect` config

**Differential harness (Task 28):** standalone binary in `cmd/goway-diff-harness/`, not a `go test` test. Triggered separately in the Phase 5 QA pipeline.

**Note:** The full enumeration of all 8 proxy seams must be defined and numbered in an architecture document before seam tests are written.

---

## 19. Benchmark Conventions

**Benchmark files:** `*_bench_test.go`, separate from `*_test.go`. Run with `go test -run=^$ -bench=.`.

**`b.ReportAllocs()` is mandatory** as the first statement in every benchmark. Allocation count is as important as ns/op on the proxy hot path.

**Canonical benchmark:** complete round-trip through the full handler stack using a real `httptest.Server` backend. Micro-benchmarks of isolated functions supplement but do not substitute.

**10% regression budget.** A PR that regresses the proxy hot-path benchmark by >10% in ns/op or allocs/op requires explicit team-lead sign-off. Attach `go test -bench` output for both base and PR commits to the PR description.

---

## 20. Test Data and Fixtures

**Golden files in `testdata/`.** Expected wire-format values (HMAC bytes, JSON payloads, HTTP response snapshots) stored under `testdata/` within the package:
```
internal/cookie/testdata/hmac_sha256_cookie.bin
internal/routing/testdata/route_request.json
```

**Update with `-update` flag.** Each test reading a golden file implements `-update`:
```go
var update = flag.Bool("update", false, "update golden files")
```
Run: `go test -run=TestFoo -update`. Commit the updated file as a deliberate, reviewable artifact.

**Never generate expected wire values inside the test.** Re-executing the same production code path cannot detect a systematic error in that path. The expected value must be a fixed committed artifact.

---

## Open Decisions

These 6 items need team-lead sign-off before the document is locked.

### D1: Assertion library — stdlib vs testify

| Option | Description |
|---|---|
| **A — stdlib only** | `t.Errorf` / `t.Fatalf`; zero dependencies; Go team recommendation |
| **B — testify (recommended)** | `require` + `assert` packages only; `require.NoError` prevents test-continues-past-fatal bugs; `assert.Equal` gives readable struct diffs |

Architect recommendation: **Option B.** The QA rubric demands byte-level HMAC cookie assertions and sticky-routing chain verification where `require.NoError` prevents misleading downstream failures. Constraint: only `require` and `assert` — no `mock`, no `suite`.

### D2: Error wrapping style

Architect recommendation (two rules):
1. Package-level sentinels: `var ErrX = errors.New("x")` — so `errors.Is` works
2. Call-site wrapping: `fmt.Errorf("pkg: op: %w", err)` — always add context, always `%w`

No bare `return err` without context except in thin forwarders where caller context is already sufficient.

### D3: Linter set

Architect recommendation — `golangci-lint` with this exact set:
```yaml
linters:
  enable:
    - govet
    - errcheck
    - staticcheck
    - ineffassign
    - unused
    - goimports
    - revive
    - gosec
    - bodyclose   # non-negotiable: unclosed response bodies are the exact leak class Go rewrite fixes
```

### D4: Config struct shape

Architect recommendation: **nested** (`cfg.Monitor.Interval`, `cfg.DB.Driver`). Maps directly to YAML structure; passes only the relevant sub-config to each constructor; avoids 8+ parameter constructors or full-config coupling.

### D5: Admin API error response format

| Option | Shape |
|---|---|
| **A — simple (recommended)** | `{"error": "backend not found"}` |
| **B — Java envelope** | `{"code": 404, "msg": "backend not found", "data": null}` |

Architect recommendation: **Option A.** The Java gateway itself only uses the `Result<T>` envelope on `/webapp/*`, not on `/gateway/*` or `/entity/*`. No consistent Java contract to mirror — Go standardizes on the simple form.

### D6: Shared test utilities location

Architect recommendation: **`internal/testutil/`** for shared infrastructure (fake Trino backend, port allocator, testcontainers DB setup). Single-interface fakes used by only one package stay local in `_test.go` files. The `internal/` path keeps it module-private.

---

*Reference: `PRD.md` · `SCOPE.md` · `topics/phase2-gate-responses.architect.md`*
