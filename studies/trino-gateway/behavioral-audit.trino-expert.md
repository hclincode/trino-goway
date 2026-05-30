---
title: trino-goway Go implementation — behavioral audit against Java contracts
author: trino-expert
role: Trino & Trino-Gateway Expert
component: trino-goway
topics: [proxy-core, routing-engine, cluster-monitor, gateway-cookies, lifecycle]
date: 2026-05-29
status: draft
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
  trino-goway: HEAD (post-Task-30, pre-Phase-8)
related-to:
  - trino-gateway/architectural-intent.trino-expert.md
  - both/protocol-constraints-on-the-gateway.architect.md
  - both/statement-protocol-invariants.java-qa.md
  - both/jvm-bound-protocol-nuances.trino-expert.md
  - USE_STORIES.md
  - PRD.md
---

# trino-goway Go implementation — behavioral audit against Java contracts

## Summary

This audit walks the Go implementation in `internal/proxy`, `internal/routing`, `internal/monitor`, and `cmd/trino-goway` against:

- `USE_STORIES.md` §1 (Trino-protocol proxying) and §2/§3/§6 (routing, health, lifecycle clauses that the proxy depends on).
- `PRD.md` § Hard Invariants (1–7) and `USE_STORIES.md` § Hard Invariants (1–12).
- The behavioral contracts in `studies/both/protocol-constraints-on-the-gateway.architect.md`, `studies/both/statement-protocol-invariants.java-qa.md`, and `studies/both/jvm-bound-protocol-nuances.trino-expert.md`.

**Headline:** the Go proxy core is in good shape for the v1 scope laid down by `SCOPE.md` and `PRD.md`. All seven Hard Invariants in `PRD.md` are implemented; ten of the twelve in `USE_STORIES.md` are implemented as written. The two material gaps are protocol-shape concerns inherited from the Trino statement protocol that the Java gateway has *and the Go gateway will need before declaring drop-in parity*:

1. **Multi-valued protocol headers are not preserved on the response path** — `internal/proxy/headers.go:28-34` copies via `dst[k] = vv` (assignment, not append) which is correct for first-write, but the upstream-request builder at `forward.go:115-121` likewise rebuilds the header map directly. This works for now, but the response-side does not iterate `Set-Cookie`-style multi-valued headers safely if the gateway ever runs middleware that touches `w.Header()` first. Today this is a latent risk, not a present bug — flagged because the Phase 8 e2e tests will need to exercise it.
2. **`KILL QUERY` regex routing is implemented but DOES NOT go through `singleflight`** as `USE_STORIES.md` §1.3 acceptance criterion bullet 3 requires. The 3rd bullet of §1.3 says *"The lookup is coalesced via singleflight so a flurry of duplicate kill requests does not fan out to the database."* The KILL path in `routing/router.go:128-134` calls `r.recovery.recoverBackend(ctx, queryID)`, which internally singleflights the *history lookup* (`recovery.go:84-86`), so the criterion is met in practice via the recovery chain. **NOT a blocker — clarified below.**

Beyond that, every behavior listed in `USE_STORIES.md` §1 (Trino-protocol proxying) is implemented, and the two intentional divergences from Java (oversized-response 502, JWKS TTL caching) are bug fixes called out in `PRD.md` § Goals.

## Audit table — `USE_STORIES.md` §1 (Trino-protocol proxying)

| # | Behavior | Status | Evidence | Notes |
|---|---|---|---|---|
| 1.1.a | `POST /v1/statement` accepted on `proxy.port` (default `:8080`) | IMPLEMENTED | `proxy/proxy.go:68`; default port `config/config.go:194` | chi `r.Post("/v1/statement", ...)` |
| 1.1.b | Request body forwarded verbatim | IMPLEMENTED | `proxy/forward.go:18-34`, `forward.go:115-121` | `bytes.NewReader(reqBody)`; no JSON rewrite. Honors Hard Invariant #1 |
| 1.1.c | `502 Bad Gateway` body `"no backend available"` when no backend | IMPLEMENTED | `proxy/forward.go:28-32`, `forward.go:81-85` | Exact body string match |
| 1.1.d | Response status + headers passed through (minus hop-by-hop) | IMPLEMENTED | `proxy/forward.go:66-72`; `headers.go:10-34` | Hop-by-hop list matches Hard Invariant #7 |
| 1.1.e | `POST /v1/statement` response buffered up to `proxy.responseSize` | IMPLEMENTED | `proxy/forward.go:47-59` | Uses `io.LimitReader(body, limit+1)` to detect overflow without reading entire body |
| 1.1.f | Oversized → `502 Bad Gateway` body `"upstream response too large"` | IMPLEMENTED — INTENTIONAL-DIVERGENCE vs Java | `proxy/forward.go:54-58` | Java truncates silently (`PRD.md` § Goals item 3, `studies/both/protocol-constraints-on-the-gateway.architect.md:106-110`). Go fails loud. |
| 1.2.a | Extract JSON `id`, cache `queryId→backendURL` BEFORE writing body | IMPLEMENTED | `proxy/forward.go:60-72`; cache write happens before `w.WriteHeader(...)` + `w.Write(buf)` | Hard Invariant #3 explicit in code comment |
| 1.2.b | Subsequent `/v1/query/<id>` polls use cached backend | IMPLEMENTED | `routing/router.go:136-142`, `router.go:244-260` | `extractQueryID` matches `/v1/query/<id>` prefix only |
| 1.2.c | `/v1/statement/<queued\|executing>/...` polls do NOT consult cache | IMPLEMENTED | `routing/router.go:245-260` | `extractQueryID` returns `""` for `/v1/statement/...` paths, so step 2 cache-hit branch is skipped. The next call (external router) is consulted for every poll — matches the documented behavior. |
| 1.2.d | Cache is bounded LRU (4096 entries) | IMPLEMENTED | `routing/cache.go:7-28` | `hashicorp/golang-lru/v2`, default size 4096 |
| 1.2.e | Non-`/v1/statement` paths streamed byte-for-byte | IMPLEMENTED | `proxy/forward.go:77-105` | `io.Copy(w, upResp.Body)`, no buffering. Hard Invariant #4 |
| 1.3.a | POST body matching `(?i)KILL\s+QUERY\s+'(<id>)'` detected | IMPLEMENTED | `routing/router.go:18-19`, `router.go:128-134`, `router.go:236-242` | Regex literal matches `PRD.md` § Hard Invariants #6 exactly |
| 1.3.b | Detection happens BEFORE the external router | IMPLEMENTED | `routing/router.go:128-134` | KILL handling is step 1 in `Route()`, external selector is step 3 |
| 1.3.c | KILL routed to history-store backend | IMPLEMENTED | `routing/router.go:130`, `recovery.go:82-89` | Uses the recovery chain (history first); if history misses, the chain falls back to HEAD probes + first-active |
| 1.3.d | Lookup is coalesced via singleflight | IMPLEMENTED | `routing/recovery.go:84-86`, `recovery.go:42-78` | The history lookup is wrapped in a custom singleflight equivalent (no `golang.org/x/sync/singleflight` import, but functionally equivalent — `do()` coalesces duplicate concurrent callers per `queryID`). Acceptable; the contract is the *behavior*, not the library. |
| 1.4.a.1 | Recovery: history DB lookup | IMPLEMENTED | `routing/recovery.go:82-89` | Step 1 of `recoverBackend` |
| 1.4.a.2 | Recovery: concurrent HEAD probe fan-out (3-second deadline, first 200 wins) | IMPLEMENTED | `routing/recovery.go:11`, `recovery.go:104-152` | `headProbeTimeout = 3 * time.Second`, fan-out via wg+buffered channel |
| 1.4.a.3 | Recovery: first-active fallback (any group) | IMPLEMENTED | `routing/recovery.go:101` (`return backends[0].URL`) | Step 3 |
| 1.4.b | Recovery never returns 404 if at least one active backend exists | IMPLEMENTED | `routing/router.go:144-172`, `recovery.go:91-101` | Eventually falls through to first-active backend, never to "no backend" |
| 1.5.a | Cookie issuance/validation gated on `cookie.secret` non-empty | IMPLEMENTED | `proxy/cookie.go:50-53` | `if p.cfg.Cookie.Secret == "" { return true }` (no cookie work at all) |
| 1.5.b | First `/oauth2` request without cookie emits `Set-Cookie: TG.OAUTH2=...` pinning backend | IMPLEMENTED | `proxy/cookie.go:72-76`, `cookie.go:83-131` | Backend URL stored in cookie's `backend` field |
| 1.5.c | Cookie is JSON-encoded, HMAC-SHA256 signed, base64-url, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Max-Age=cookie.ttl` | IMPLEMENTED | `proxy/cookie.go:122-131`, `cookie.go:175-183`, `cookie.go:185-205` | Default TTL `10m` per `config/config.go:220` |
| 1.5.d | Valid cookie + path matches `deletePaths` → delete-cookie response | IMPLEMENTED | `proxy/cookie.go:64-68`, `cookie.go:209-216` | `deletePaths` defaults match Java: `/logout`, `/oauth2/logout` |
| 1.5.e | Expired cookie → delete-cookie response + continue (no 401) | IMPLEMENTED | `proxy/cookie.go:60-63`, `cookie.go:163-169` | `validateCookie` returns `valid=false, tampered=false` for expired; caller path runs `deleteCookie(w)` and returns `true` |
| 1.5.f | Bad HMAC or undecodable payload → `500` (never silently accept) | IMPLEMENTED | `proxy/cookie.go:57-59`, `forward.go:67-70`, `forward.go:99-102` | `validateCookie` returns `tampered=true`; caller short-circuits to `http.StatusInternalServerError` body `"invalid gateway cookie"`. Hard Invariant #5. |
| 1.5.g | `cookie.wireCompat: true` (default) → byte-compatible with Java | IMPLEMENTED | `proxy/cookie.go:186-205`, `cookie.go:230-296` | Alphabetical JSON field order, `base64.URLEncoding` with padding, airlift TTL format (`10.00m`). Existing unit tests in `cookie_test.go` pin the wire format. |
| 1.6.a | `X-Forwarded-For` appends (does not overwrite) | IMPLEMENTED | `proxy/headers.go:38-47` | Hard Invariant #8 |
| 1.6.b | `X-Forwarded-Proto` = `https` if TLS, else `http` | IMPLEMENTED | `proxy/headers.go:49-54` | `r.TLS != nil` test |
| 1.6.c | `X-Forwarded-Host` = inbound `Host`, host-only (IPv6 literals preserved) | IMPLEMENTED | `proxy/headers.go:56-58`, `headers.go:70-83` | `hostOnly()` handles `[addr]` prefix |
| 1.6.d | `X-Forwarded-Port` = explicit port or scheme default (80/443) | IMPLEMENTED | `proxy/headers.go:60-62`, `headers.go:85-105` | `forwardedPort()` |
| 1.6.e | Hop-by-hop headers never forwarded | IMPLEMENTED | `proxy/headers.go:10-25`, `forward.go:115-121`, `headers.go:28-34` | All 8 names from `USE_STORIES.md` §1.6.e present in `hopByHopHeaders` map |
| 1.7.a | External-router `externalHeaders` applied with REPLACE semantics | IMPLEMENTED | `proxy/headers.go:64-67` | `upReq.Header.Set(k, v)`, not `Add`. Hard Invariant #9 |
| 1.7.b | `routing.external.excludeHeaders` stripped from `externalHeaders` (HTTP + gRPC) | IMPLEMENTED | `routing/router.go:196-210` | `filterExcludedHeaders` applied to both transports |
| 1.7.b' | HTTP transport strips `excludeHeaders` + `Content-Length` from inbound headers forwarded to router | IMPLEMENTED | `routing/external_http.go:94-104` | gRPC does not carry inbound headers, so this clause is HTTP-only by construction |

## Audit table — `USE_STORIES.md` §2 (Routing) — proxy-relevant clauses

| # | Behavior | Status | Evidence | Notes |
|---|---|---|---|---|
| 2.1.a | `routing.type` must be `EXTERNAL` (startup validation) | IMPLEMENTED | `config/config.go:283-285` | Rejects any other value |
| 2.1.b | gRPC tried first if `grpcAddr` set; HTTP fallback if `url` set | IMPLEMENTED | `routing/router.go:184-194` | `callExternal` checks `r.grpcSelector != nil` first, then HTTP |
| 2.1.c | Both transports use `routing.external.timeout` (default `1s`) | IMPLEMENTED | `routing/external_http.go:81-86`, `external_grpc.go:45-50`; default `config/config.go:207` | Per-call `context.WithTimeout` |
| 2.1.d | HTTP body fields + header passthrough + gRPC proto equivalence; `errorMessage = "trino-parser not available in Go v1"` | IMPLEMENTED | `routing/external_http.go:140-156`, `external_grpc.go:66-84` | Identical sentinel string on both transports |
| 2.1.e | Both transports fail → defaultGroup → recovery chain; never reject solely because router is down | IMPLEMENTED | `routing/router.go:144-172` | Empty group + nil/empty backend list falls through to recovery + first-active |
| 2.3.a | If no external URL/grpcAddr configured, every request goes to `routing.defaultGroup` | IMPLEMENTED | `routing/external_http.go:71-73`, `external_grpc.go:25-27` | Both selectors return `("", nil, nil, nil)` when not configured; `callExternal` then sees empty group and `router.go:151-153` substitutes `r.cfg.DefaultGroup` |

## Audit table — `USE_STORIES.md` §3 (Health) — proxy-relevant clauses

| # | Behavior | Status | Evidence | Notes |
|---|---|---|---|---|
| 3.1.a | Background monitor probes each backend on `monitor.interval` (default `30s`) | IMPLEMENTED | `monitor/monitor.go:163-178`; default `config/config.go:201` | `time.NewTicker(m.cfg.Interval.D)` |
| 3.1.b | Probe is `GET /v1/info` with `monitor.checkTimeout` (default `5s`) | IMPLEMENTED | `monitor/monitor.go:231-266`; default `config/config.go:202` | `http.MethodGet`, `url+"/v1/info"`, `context.WithTimeout(ctx, m.cfg.CheckTimeout.D)` |
| 3.1.c | `HEALTHY` iff `200` + `{"starting": false}`; everything else `UNHEALTHY` | IMPLEMENTED | `monitor/monitor.go:249-265` | All three failure branches (non-200, decode error, `starting: true`) return `StatusUnhealthy` |
| 3.1.d | Unprobed = `UNKNOWN`; freshly upserted active = `PENDING`, inactive = `UNHEALTHY` | IMPLEMENTED | `monitor/status.go:6-15`, `monitor/monitor.go:90-100`; admin seeding `admin/backend.go:304-311` | Note: seeding only happens through `/entity?entityType=GATEWAY_BACKEND` (USE_STORIES §4.2). The `/gateway/backend/modify/*` endpoints do NOT seed status — `USE_STORIES.md` §3.2 only requires `/entity` to seed, so this matches the literal acceptance criterion. |
| 3.1.e | Probe status updated by atomic swap | IMPLEMENTED | `monitor/monitor.go:44`, `monitor.go:215-218` | `atomic.Pointer[map[string]TrinoStatus]`; single `.Store(&next)` after fan-in |
| 3.2.a | Background refresher reloads backend list every `15s` | IMPLEMENTED | `cmd/trino-goway/main.go:37`, `main.go:239-252` | `backendRefreshInterval = 15 * time.Second` |
| 3.3.a | `GET /trino-gateway/livez` always 200 ok | IMPLEMENTED | `admin/health.go:6-11` | Static `200 ok` |
| 3.3.b | `GET /trino-gateway/readyz` 503 until first probe cycle, then 200 | IMPLEMENTED | `admin/health.go:15-25`, `monitor/monitor.go:180-185`, `cmd/trino-goway/main.go:189` | `Monitor.SetOnFirstTick(adminHandler.SetReady)`; `firstTickOnce.Do(...)` fires after the first `probe()` regardless of whether any backend exists. Hard Invariant #11. |

## Audit table — `USE_STORIES.md` § Hard Invariants

| # | Invariant | Status | Evidence | Notes |
|---|---|---|---|---|
| HI-1 | No body rewriting (response or request) | IMPLEMENTED | `proxy/forward.go:62-72`, `forward.go:133-139` | Buffer is read into `buf`, written back via `w.Write(buf)` unchanged. `extractQueryIDFromBody` only `json.Unmarshal`s, never modifies. |
| HI-2 | No redirect following | IMPLEMENTED | `cmd/trino-goway/main.go:75-80` | `proxyClient.CheckRedirect = func(...) { return http.ErrUseLastResponse }` |
| HI-3 | `WriteCache` before flush | IMPLEMENTED | `proxy/forward.go:60-72` | Cache write at line 63 precedes `w.WriteHeader` at line 71 and `w.Write` at line 72 |
| HI-4 | Bounded response buffering (only POST `/v1/statement`) | IMPLEMENTED | `proxy/forward.go:47-59` (buffer); `forward.go:77-105` (stream all other paths via `io.Copy`) | `responseSize` enforced; default 1 MiB |
| HI-5 | Tampered cookies are loud (500) | IMPLEMENTED | `proxy/cookie.go:57-59`, `cookie.go:137-161`; `forward.go:67-70`, `forward.go:99-102` | Bad HMAC, undecodable base64, or unparseable JSON all return `tampered=true` → caller emits `500 invalid gateway cookie` |
| HI-6 | KILL QUERY routes by ID (pre-router) | IMPLEMENTED | `routing/router.go:18-19`, `router.go:128-134` | Regex matches `PRD.md` § Hard Invariants #6 literal: `(?i)KILL\s+QUERY\s+'([0-9]+_[0-9]+_[0-9]+_\w+)'` |
| HI-7 | Hop-by-hop headers stripped both ways | IMPLEMENTED | `proxy/headers.go:10-34`, `forward.go:115-121`, `forward.go:66`, `forward.go:98` | `copyHeaders` filters on response path; per-key check on request path |
| HI-8 | `X-Forwarded-For` appends, never overwrites | IMPLEMENTED | `proxy/headers.go:38-47` | `existing+", "+clientIP` |
| HI-9 | `externalHeaders` REPLACE semantics | IMPLEMENTED | `proxy/headers.go:64-67` | `Set`, not `Add` |
| HI-10 | Java wire compatibility for `TG.OAUTH2` (default on) | IMPLEMENTED | `proxy/cookie.go:186-205`, `cookie.go:230-296`; default `config/config.go:221` | Alphabetical JSON field order, URL-safe base64 *with* padding, airlift Duration format |
| HI-11 | `readyz` requires a probe cycle | IMPLEMENTED | `monitor/monitor.go:180-185`, `cmd/trino-goway/main.go:189` | `firstTickOnce.Do` ⇒ `adminHandler.SetReady()`, never fires before `probe()` returns |
| HI-12 | Three HTTP clients (proxy / monitor / router) | IMPLEMENTED | `cmd/trino-goway/main.go:75-86` | Three distinct `*http.Client` literals; `monitorClient` is reused for HEAD probes in the recovery chain (`main.go:115`), which matches `USE_STORIES.md` §6.5.d |

## Audit table — `PRD.md` § Hard Invariants

| # | Invariant | Status | Evidence | Notes |
|---|---|---|---|---|
| PRD-HI-1 | Never rewrite response bodies | IMPLEMENTED | (same as HI-1 above) | |
| PRD-HI-2 | Disable redirect-following globally | IMPLEMENTED | (same as HI-2 above) | `routerClient` does NOT set `CheckRedirect` — Java doesn't either; spec at `PRD.md:156` explicitly carves out the router client. Confirmed by `cmd/trino-goway/main.go:84-86` (no `CheckRedirect`). |
| PRD-HI-3 | Sticky-routing cache write completes before flush | IMPLEMENTED | (same as HI-3 above) | |
| PRD-HI-4 | 3-step cache-miss recovery chain | IMPLEMENTED | (same as HI-1.4 above) | `recovery.go:82-102` |
| PRD-HI-5 | Document `http-server.process-forwarded=true` prominently | OUT-OF-SCOPE-FOR-CODE — DOC OBLIGATION | `studies/both/statement-protocol-invariants.java-qa.md:260` calls this a documentation requirement on the operator side | Not a Go code change — need to confirm `README.md` or operator docs flag this. README check below. |
| PRD-HI-6 | `KILL QUERY` regex routing | IMPLEMENTED | (same as HI-6 above) | |
| PRD-HI-7 | Three separate `*http.Client` instances; `proxyClient`+`monitorClient` set `CheckRedirect: ErrUseLastResponse`; `routerClient` follows redirects | PARTIAL | `cmd/trino-goway/main.go:75-86`: `proxyClient` sets `CheckRedirect`. `monitorClient` does NOT set `CheckRedirect`. `routerClient` follows by default (correct). | **GAP — minor.** `monitorClient` should set `CheckRedirect: http.ErrUseLastResponse` per `PRD.md:156`. Currently, a misbehaving backend that returns a 3xx on `/v1/info` would cause the monitor's `http.Client` to follow the redirect with the default policy (up to 10 hops). Not a security issue, but a subtle conformance gap with the PRD. |

## Bugs fixed in Go (INTENTIONAL-DIVERGENCE from Java)

These are deliberate improvements over the Java reference, called out in `PRD.md` § Goals or in the study documents.

1. **Oversized `/v1/statement` response → loud 502 instead of silent truncation.**
   `proxy/forward.go:54-58` returns `502 upstream response too large` when the buffered upstream body exceeds `proxy.responseSize`. The Java gateway truncates the body to `responseSize`, producing a malformed JSON response the client cannot parse (`studies/both/protocol-constraints-on-the-gateway.architect.md:106-110`, `PRD.md:19`). Failing loud is correct; truncating to broken JSON is a known Java bug.

2. **JWKS TTL caching, no per-request fetch.**
   `PRD.md:19` calls this out as one of the two Java bugs the Go rewrite fixes. The Go OIDC middleware fetches the JWKS at startup and refreshes every `jwksTtlSecs` (default `300s`) in the background (`USE_STORIES.md` §5.1, §5.3). Java fetches per-request, hammering the IdP. Out of scope for the proxy module audit but listed here for completeness.

3. **gRPC routing transport added.**
   `SCOPE.md:15` lists "External routing selector — gRPC" as v1-locked-in scope; Java only offers the HTTP transport. The Go transport is wire-equivalent and field-for-field aligned with the HTTP version (`routing/external_grpc.go:66-108` vs `external_http.go:137-186`).

4. **Custom `singleflight` instead of `golang.org/x/sync/singleflight`.**
   The recovery chain uses a hand-rolled coalescer in `routing/recovery.go:43-78` instead of the standard library. Behaviorally equivalent for the `LookupByQueryID` path; the rationale is unclear (no comment), and `golang.org/x/sync/singleflight` is already on the dependency list (see `PRD.md` "Key Architecture Decisions": `golang.org/x/sync/singleflight`). Not a divergence in behavior, but a divergence from the stated library choice. Worth a follow-up to either delete the custom impl or document why it differs.

## Gaps — material vs cosmetic

### GAP-1 (cosmetic, PRD-HI-7 partial) — `monitorClient.CheckRedirect` not set ✅ RESOLVED 2026-05-28

- **What:** `cmd/trino-goway/main.go` built `monitorClient` with only `Timeout`; no `CheckRedirect`.
- **PRD says:** "proxyClient and monitorClient set CheckRedirect: ErrUseLastResponse; routerClient follows redirects" (`PRD.md:156`).
- **Resolution:** `cmd/trino-goway/main.go:81-86` now sets `CheckRedirect: func(...) error { return http.ErrUseLastResponse }` on `monitorClient`. PRD-HI-7 conformance restored.

### GAP-2 (doc, PRD-HI-5) — `http-server.process-forwarded=true` not documented ✅ RESOLVED 2026-05-28

- **What:** Operators needed prominent documentation that the *coordinator-side* setting `http-server.process-forwarded=true` is a load-bearing requirement for `nextUri` to point at the gateway. Without it, the cross-system contract fails OPEN — clients silently bypass the gateway.
- **Resolution:**
  - `README.md:167-172` now contains a prominent block-quote warning in the Running section.
  - `config.example.yaml:8-19` contains a banner comment block at the top of the file documenting the requirement and its silent failure mode.
- **Phase 8 status:** No longer a blocker for real-Trino e2e tests.

### GAP-3 (test-coverage only, not behavior) — Multi-valued response headers not exercised

- **What:** `proxy/headers.go:28-34` uses `dst[k] = vv` which works as long as `dst` (the `http.ResponseWriter.Header()`) hasn't been pre-populated for any of the same keys. Currently no middleware does that. But Trino's protocol relies on multi-valued response headers (`X-Trino-Set-Session`, `X-Trino-Added-Prepare`, etc., per `studies/both/statement-protocol-invariants.java-qa.md:244-248`) and the existing unit tests do not verify they are preserved as a *list*.
- **Today's behavior:** Correct, because Go's `net/http` server does not seed `w.Header()` before the handler runs.
- **Risk:** If a future contributor inserts middleware that writes to `w.Header()` before the proxy handler (e.g. observability headers), the assignment will clobber rather than append. The codebase currently has no such middleware.
- **Blocks Phase 8?** No, but it should be on the e2e checklist (Task 53/54).

### Note on §1.3.d (KILL QUERY singleflight)

The acceptance criterion *"The lookup is coalesced via singleflight so a flurry of duplicate kill requests does not fan out to the database"* is met by the `recovery.recoverBackend` path: `recovery.go:84-86` wraps `history.LookupByQueryID` in `sf.do(queryID, ...)`. The KILL route in `router.go:130` calls `recoverBackend`, so the singleflight applies transparently. Code-readability nit: this is non-obvious and worth a comment in `router.go:128-134` so future readers don't try to add singleflight at the KILL site.

## Blockers for Phase 8 E2E tests

Strictly, *zero* GAPs in the audit table prevent Phase 8 from compiling and running. GAP-1 and GAP-2 were both resolved on 2026-05-28; the only remaining item is:

1. **GAP-3 (test-coverage gap, not a bug)** — Phase 8 should include explicit assertions that multi-valued `X-Trino-Set-Session` and `X-Trino-Added-Prepare` round-trip with `len(headers[k]) == N`, not `len(headers[k]) == 1`. The current implementation passes this assertion (verified by inspection of `headers.go`) but the test suite does not pin it.

**No proxy-core or routing-core behavior gap will cause a Phase 8 e2e test against a real Java gateway to diverge in the proxy/routing paths.** The diff-harness scenarios (Task 55) should run green for §1.1 through §1.7 and §2 paths.

## What is *not* in scope of this audit

This audit covers `internal/proxy`, `internal/routing`, `internal/monitor`, and `cmd/trino-goway`. The following audit surfaces belong to sibling Phase-7 audits and are addressed in their own studies:

- **Admin REST + webapp endpoints** (`USE_STORIES.md` §4): `studies/trino-gateway/admin-api-surface.java-analyst.md` (Task 16) and the upcoming java-analyst completeness audit (Task 32).
- **Auth backends** (`USE_STORIES.md` §5): the OIDC/LDAP/NOOP implementations live in `internal/auth`; covered by Task 33 (go-qa proxy seam gap analysis) and by Tasks 51–52 e2e.
- **Differential testing** (`USE_STORIES.md` §8): the diff-harness lives in `internal/diffharness`; covered by Task 55.
- **Lifecycle invariants beyond port-binding order** (`USE_STORIES.md` §6.2–6.3): covered by `internal/lifecycle` tests and the existing `studies/trino-gateway/concurrency-and-lifecycle-model.go-implementer.md`.

## Implications for Phase 8

1. The proxy core is e2e-ready. Tasks 39 (Proxy protocol), 40 (KILL QUERY), 41 (recovery chain), 42–44 (External routing + groups), 53–54 (Cookies + invariants) can be implemented against the current code with high confidence.
2. The two `GAP-1` and `GAP-2` items are 1-line and 1-paragraph fixes respectively; recommend handling them as preconditions to Task 38 (Full-stack E2E binary harness) so the harness reflects PRD-conformant behavior from day one.
3. The `singleflight` library choice (custom vs `golang.org/x/sync/singleflight`) deserves a follow-up issue, but it is a code-quality matter, not a behavior gap.
4. The README/operator docs gap (`GAP-2`) is the only "this will surprise an operator at deploy time" gap in the audit and should be prioritized over the cosmetic fixes.

## Cross-references

- `[[architectural-intent.trino-expert.md]]` — what the gateway is FOR
- `[[../both/protocol-constraints-on-the-gateway.architect.md]]` — implementation-language-independent invariants
- `[[../both/statement-protocol-invariants.java-qa.md]]` — wire-level QA spec the gateway must satisfy
- `[[../both/jvm-bound-protocol-nuances.trino-expert.md]]` — which Java idioms NOT to port
- `[[../../USE_STORIES.md]]` — operator-facing acceptance criteria (this audit's primary checklist)
- `[[../../PRD.md]]` — locked-in scope, key architecture decisions, hard invariants
- `[[../../SCOPE.md]]` — what is in and out of v1
