# Proxy Seam Gap Analysis (go-qa)

Author: **go-qa**
Date: 2026-05-29
Inputs:
- `USE_STORIES.md § Hard Invariants` (12 invariants)
- `internal/proxy/proxy_test.go`, `internal/proxy/proxy_qa_test.go`, `internal/proxy/cookie_test.go`, `internal/proxy/headers_test.go`
- `internal/e2e/proxy_e2e_test.go`
- `cmd/goway-diff-harness/testdata/scenarios/*.yaml` (9 scenarios)

Coverage taxonomy used below:

- **COVERED** — at least one existing test directly asserts the invariant end-to-end (or sufficiently close to it for the seam being protected).
- **PARTIALLY-COVERED** — existing tests assert parts of the invariant (typically the helper function or a single dimension) but a critical dimension is unverified or only covered by a unit test of an internal helper rather than the integrated behaviour visible at the HTTP boundary.
- **NOT-COVERED** — no test asserts the invariant at all, or the only "coverage" is a comment in code/scenario YAML.

The diff-harness scenarios (`seam*.yaml`) are categorised as **live-mode shape comparators**, not protocol assertions: they only run under `//go:build diff` against a live Java/Go pair, they normalize away every header and body field that would actually catch the invariant breaking (`Set-Cookie`, `Date`, `id`, `nextUri`, etc.), and they are **not** part of `go test ./...`. They are therefore **at best supplementary** for these invariants and never sufficient on their own — they catch divergence between Go and Java, not violation of the absolute protocol contract. The same is true of the e2e `TestG1_NextURIHostDerivation`: it is a one-shot upstream-host-derivation gate, not an invariant suite.

---

## Coverage table

| # | Invariant | Status | Test(s) | Gap | Phase 8 task |
|---|-----------|--------|---------|-----|--------------|
| 1 | No body rewriting (request and response forwarded verbatim; `/v1/statement` only buffers to extract `queryId`) | COVERED | `TestProxy_Seam1_NeverRewriteResponseBody` (proxy_test.go:99); `TestProxy_Degradation_MalformedJSONBody` (proxy_qa_test.go:126); seam1-body-passthrough.yaml | Request-body verbatim path is asserted indirectly via `TestProxy_Seam6_KillQueryRegexRouting` (upstream sees body verbatim). No dedicated `request` body-pass-through assertion against a non-/v1/statement upload path (e.g. PUT body or large multipart). | Task 39 (`TestE2E_Inv1_NoBodyRewriting`) — also restated as Task 54. |
| 2 | No redirect following (proxy client returns 3xx verbatim) | COVERED | `TestProxy_Seam2_RedirectFollowingDisabled` (proxy_test.go:127); seam2-redirect-not-followed.yaml (live shape check); `startGateway` in `proxy_e2e_test.go:172` re-asserts `CheckRedirect = ErrUseLastResponse` is the wiring used. | Unit test injects a no-follow client into the proxy rather than verifying that *production wiring* in `cmd/trino-goway/main.go` installs `CheckRedirect`. Composition root is therefore not under test. | Task 54 (`TestE2E_Inv2_NoRedirectFollowing`) — black-box, exercising the actual binary's proxy client. |
| 3 | WriteCache before flush (queryId→backend cached before status/body sent to client) | COVERED | `TestProxy_Seam3_CacheWriteBeforeResponseFlush` (proxy_test.go:159) — uses an interceptor that records ordering of `WriteCache` vs first body `Write`; seam3-cache-write-before-flush.yaml (live consequence). | Ordering is asserted via in-process interception. There is no end-to-end test exercising the consequence under concurrency (two clients racing the POST→GET window — i.e. that a second client polling for `<queryId>` while the first POST is still flushing actually lands on the cached backend). | Task 54 (`TestE2E_Inv3_CacheWriteBeforeFlush`) — concurrent POST/GET race against the binary. |
| 4 | Bounded response buffering (only `/v1/statement` buffers; everything else streams) | **PARTIALLY-COVERED** | `TestProxy_Degradation_OversizeResponseBody` (proxy_qa_test.go:91) asserts the 1 MiB cap on `/v1/statement`. | The *negative* half of the invariant — "every other path streams the body byte-for-byte without buffering" — is **not** asserted anywhere. Nothing proves that `GET /v1/query/<id>` accepts a response larger than `proxy.responseSize` and streams it through. A future refactor that accidentally pushed the buffering into `handleStream` would pass every existing test. | Task 39 (`TestE2E_StreamingPath_NotBuffered`) and Task 54 (`TestE2E_Inv4_BoundedBuffering_OnlyStatement`). |
| 5 | Tampered cookies are loud (HMAC mismatch / undecodable payload → 500, never silent) | COVERED | `TestCookie_HMACTamper` (cookie_test.go:117) — one-byte tamper → 500; `TestCookie_HMACFixture` (cookie_test.go:46) — pinned signature; `TestCookie_DisabledWhenSecretEmpty` (cookie_test.go:269) — secret-empty path does not 500 on bad cookies. | None material — payload-undecodable path is not explicitly fuzzed, but the HMAC tamper path is the most likely break and is asserted. | Task 53 (`TestE2E_Cookie_TamperedReturns500`) and Task 54 (`TestE2E_Inv5_TamperedCookieIs500`) for the binary-level assertion. |
| 6 | KILL QUERY routes by ID (POST body matching `(?i)KILL\s+QUERY\s+'(<id>)'` is detected before external router) | **PARTIALLY-COVERED** | `TestProxy_Seam6_KillQueryRegexRouting` (proxy_test.go:294) — asserts the proxy passes the buffered body verbatim to `router.Route` via `RouteInput.Body`. seam6-killquery-routing.yaml — live shape parity only. | The proxy-side assertion is "body is forwarded to the router"; the actual regex extraction + history-lookup + routing-by-history-backend lives in `internal/routing` and is only black-box-verified there. Nothing in the proxy suite proves that `KILL QUERY '...'` lands on the *owner* backend rather than any active backend. Lowercase / whitespace / quoted-variant regex behaviour is not tested at the proxy boundary. | Task 40 (`TestE2E_KillQuery_RoutesToOwnerBackend`, `TestE2E_KillQuery_Lowercase`, `TestE2E_KillQuery_UnknownId`) and Task 54 (`TestE2E_Inv6_KillQueryByID`). |
| 7 | Hop-by-hop headers stripped both ways (request AND response) | **PARTIALLY-COVERED** | `TestIsHopByHop` (proxy_test.go:446) — unit test of the predicate. `TestCopyHeaders_SkipsHopByHop` (proxy_test.go:478) — unit test of the helper. Code path: `copyHeaders` is invoked in `buildUpstreamRequest` (forward.go:117) AND on the downstream response copy (forward.go:66, 98). | **No integration test asserts that the downstream response actually has hop-by-hop headers stripped.** A backend emitting `Transfer-Encoding: chunked` or `Connection: close` is never tested against the gateway's client-facing response. Helper coverage alone does not prove the helper is invoked on *both* legs — the `handleStatement` and `handleStream` call sites are not covered by an assertion that watches the client receive a `Connection`-less response. **This is a real downstream-direction gap, exactly as called out in the task description.** | Task 39 (`TestE2E_HopByHopStripped`) — currently scoped to *request* direction only ("backend does NOT receive those headers"). Task 54 (`TestE2E_Inv7_HopByHopStripped`) cross-references Task 39 but the response-direction stripping is **still uncovered by the current Phase 8 plan**. Recommendation: extend Task 39/54 to also assert a backend-sent `Connection: keep-alive` is **not** in the gateway's downstream response. |
| 8 | `X-Forwarded-For` appends, never overwrites | COVERED | `TestInjectHeaders_AppendsXForwardedFor` (headers_test.go:72) — explicit `existing, clientIP` shape; `TestInjectHeaders_XForwarded` (proxy_test.go:496) — no-prior path. | Both branches (empty / non-empty existing XFF) are asserted at the proxy unit level. Not asserted end-to-end against the binary — a future refactor of `injectHeaders` is caught only by these unit tests. | Task 39 (`TestE2E_ForwardedHeaders_XForwardedForAppends`) and Task 54 (`TestE2E_Inv8_XForwardedForAppends`). |
| 9 | `externalHeaders` use REPLACE semantics (Set, not Add) | COVERED | `TestInjectHeaders_ExternalHeaders_Replace` (headers_test.go:42) — pre-existing `X-Custom: old` + router-injected `X-Custom: new` → upstream receives `[new]` only (single value). | Unit-level only; relies on `RouteResult.ExternalHeaders` being a `map[string]string` and `http.Header.Set` semantics. No black-box test using a real external router proves collisions resolve in favour of the router under the binary wiring (e.g. case-folding around router headers vs. caller headers). | Task 42 (`TestE2E_ExternalHTTP_ExternalHeadersReplaced`) and Task 54 (`TestE2E_Inv9_ExternalHeadersReplace`). |
| 10 | Java wire compatibility (cookie JSON field order, signature input, base64 padding, airlift Duration `10.00m`) | COVERED | `TestCookie_HMACFixture` (cookie_test.go:46) — pinned hex signature against a fixed payload; `TestCookie_EncodeDecodeRoundTrip` (cookie_test.go:75) — `payload: null` present, TTL `10.00m`; `TestCookie_FormatTTL_WireCompatVsNanos` (cookie_test.go:291); `TestCookie_ParseTTL_AllForms` (cookie_test.go:303); `TestAirliftDurationString` (proxy_test.go:456). | The pinned HMAC fixture (`123194ddd...`) IS the wire-compat golden — strong coverage. The one gap is that the test was computed by Go-self, not cross-checked against a fresh Java emission; if both languages happened to drift in the same direction the fixture would still pass. Task 53 plans `cookie_wire_compat.golden` for explicit cross-language assertion. | Task 53 (`TestE2E_Cookie_WireCompat_GoldenBytes`) and Task 54 (`TestE2E_Inv10_CookieWireCompat`). |
| 11 | `readyz` requires a probe cycle (503 until first probe completes) | **PARTIALLY-COVERED** | `internal/admin/admin_test.go:204` (`readyz 503 before SetReady`) and `:211` (`readyz 200 after SetReady`) test the **admin** server's flag directly. | The integration that makes this an *invariant* — the lifecycle wiring `Monitor.SetOnFirstTick → Admin.SetReady` — is **not** exercised by any test. A regression that forgot to wire the callback (or wired it before the first tick) would pass the admin unit test and never be caught. The proxy package has no readyz test at all. | Task 46 (`TestE2E_Readyz_503BeforeFirstProbe`, `TestE2E_Readyz_200AfterFirstProbe`) and Task 54 (`TestE2E_Inv11_ReadyzRequiresProbe`). |
| 12 | Three HTTP clients (proxy / monitor / router are distinct `*http.Client`s with independent timeouts) | **NOT-COVERED** | `TestProxy_Seam7_ThreeClientPoolIsolation` (proxy_test.go:354) — name suggests coverage but the test only asserts that *the proxy uses the client passed into its Config*. It does NOT verify that proxy / monitor / router each receive different clients at composition time, nor that one client's saturation does not starve another. | The actual behavioural invariant — "a slow routing service cannot exhaust connections used for real query traffic" — has **no test at any level**. Composition root (`cmd/trino-goway/main.go`) is not under test. There is no behavioural-isolation test (e.g. fill the router client's connection pool, prove proxy traffic still flows). The misleading test name actively *obscures* the gap. | Task 54 (`TestE2E_Inv12_ThreeHTTPClients`) — currently spec'd as either "admin metrics" check OR "behavioural saturation". **Strong recommendation**: implement the behavioural-saturation variant — it is the only one that catches accidental client sharing (e.g. somebody passing `routerClient` to the proxy by mistake). |

---

## Summary counts

- **COVERED**: 7 — #1, #2, #3, #5, #8, #9, #10
- **PARTIALLY-COVERED**: 4 — #4, #6, #7, #11
- **NOT-COVERED**: 1 — #12
- **Total**: 12

---

## Critical gaps (must be addressed before go-live)

The following invariants protect production correctness and have insufficient coverage today. Each is mapped to its planned Phase 8 task; if Phase 8 ships without these, the gateway may regress silently on a future refactor.

### Critical gap A — Invariant #12 (three HTTP clients), NOT-COVERED

Why this matters: This is the only invariant with **zero** behavioural coverage. The misleadingly-named `TestProxy_Seam7_ThreeClientPoolIsolation` asserts only that the proxy uses the client passed in; it does not prove that three distinct clients are wired, nor that they are isolated under load. The motivating risk is concrete — a slow router exhausting the proxy's connection pool would silently degrade query traffic in production and never trigger a single existing test.

What must ship: Task 54 (`TestE2E_Inv12_ThreeHTTPClients`) implemented as a *behavioural-saturation* test, not a log-or-metrics inspection. Saturate the router client's pool (e.g. point `routing.external.url` at a server that holds connections), then prove that `POST /v1/statement` traffic completes within its own timeout budget. This is the only assertion that catches accidental client sharing at the composition root.

### Critical gap B — Invariant #7 (hop-by-hop headers stripped both ways), PARTIALLY-COVERED

Why this matters: Only the helper `copyHeaders` and the predicate `isHopByHop` are unit-tested. The *downstream-direction* code path (forward.go:66, 98 — gateway → client) is not asserted by any test. A backend that sends `Connection: close` or `Transfer-Encoding: chunked` would leak that header to the client today if `copyHeaders` regressed at the response-copy call site, and no test would fail.

What must ship: The currently-planned Task 39 `TestE2E_HopByHopStripped` is scoped to the *request* direction only ("backend does NOT receive those headers"). Task 54 simply cross-references Task 39 and inherits the same scope. **Task 39 (or Task 54) must be extended** to also include a backend that emits hop-by-hop response headers and assert the client never sees them. Without that, the downstream half of Invariant #7 remains uncovered after Phase 8 lands.

### Critical gap C — Invariant #11 (readyz requires probe cycle), PARTIALLY-COVERED

Why this matters: The admin package tests the flag in isolation; nothing tests the *wiring* — `Monitor.SetOnFirstTick → Admin.SetReady` — that turns the flag into an invariant. A composition-root regression that forgot to register the callback, or registered it during startup before the first probe, would never be caught. This invariant is the single Kubernetes-visible signal that prevents the gateway from advertising readiness with zero backend health knowledge.

What must ship: Task 46 (`TestE2E_Readyz_503BeforeFirstProbe`, `TestE2E_Readyz_200AfterFirstProbe`) — both required. The 503-before path must use a deliberately long `monitor.interval` to force the pre-tick window to be observable; the 200-after path must assert the one-time transition. Task 54 then cross-references.

### Critical gap D — Invariant #4 (bounded buffering only on /v1/statement), PARTIALLY-COVERED

Why this matters: We have the *positive* half ("/v1/statement does buffer with a cap"). We do not have the *negative* half ("every other path streams"). If a refactor accidentally moved the buffer into `handleStream`, every existing test would still pass, but a large `/v1/query/<id>` response (typical of a wide result page) would suddenly OOM or 502 in production.

What must ship: Task 39 (`TestE2E_StreamingPath_NotBuffered`) and Task 54 (`TestE2E_Inv4_BoundedBuffering_OnlyStatement`) — both must use a backend response larger than `proxy.responseSize` on a non-`/v1/statement` path and assert success + byte-identity.

### Non-critical but worth noting — Invariant #6 (KILL QUERY routing)

The proxy-side seam is covered (body-to-router forwarding). The end-to-end *destination* property (kill lands on the *owner* backend, not whichever the router currently picks) is verified only by `internal/routing` unit tests. Task 40 covers this comprehensively; the gap closes when Task 40 ships. Not flagged as a launch-blocker because the existing routing tests are strong, but the binary-level assertion in Task 40 is necessary for confidence under the actual composition.

---

## Recommendations to qa-tech-lead (Task 34) and team-lead

1. **Adjust Task 39 / Task 54 scope** for Invariant #7 to explicitly cover the response-direction hop-by-hop strip (add a backend that emits `Connection: close` and assert the client never sees it).
2. **Commit Task 54 Inv12 to the behavioural-saturation variant** — the "verify via admin metrics or startup log" option in the current TODO leaves the actual isolation property unverified.
3. **Rename or split `TestProxy_Seam7_ThreeClientPoolIsolation`** — it does not test client pool isolation; it tests dependency injection. Leaving the current name in place actively misleads future readers about Invariant #12 coverage. Tracked under Task 54.
4. **Optional but valuable**: Task 53's `cookie_wire_compat.golden` should be regenerated by a live Java gateway (not by Go) to close the "both languages drift together" loophole on Invariant #10.

---

## Method notes

- "COVERED" was awarded only when an existing test would fail if the invariant were violated by a plausible refactor. Tests that exercise a helper in isolation while leaving the integrated call site unverified were classified PARTIALLY-COVERED.
- Diff-harness scenarios (`seam*.yaml`) are not counted as primary coverage. They are out-of-band live-mode shape comparators (build tag `diff`, normalize most invariant-signal fields away). They are correctly designed for their purpose (Java↔Go parity) but they are not protocol-contract assertions.
- `internal/e2e/proxy_e2e_test.go::TestG1_NextURIHostDerivation` is a single host-derivation gate and explicitly skips when Docker is unavailable; it cannot be relied on as part of the invariant suite.
