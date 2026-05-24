---
title: HTTP forwarding patterns — Go-implementer mapping to net/http and httputil.ReverseProxy
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - proxy-core
  - statement-protocol
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/proxy-streaming-vs-buffering.go-implementer.md
  - trino-gateway/proxy-request-lifecycle.java-qa.md
  - trino-gateway/proxy-lifecycle-testable-seams.go-qa.md
---

# HTTP forwarding patterns — Go-implementer mapping to net/http and httputil.ReverseProxy

## Summary

Most of the Java proxy maps naturally to `net/http` — single `*http.Client` with a tuned `*http.Transport`, `chi` router for inbound, hand-rolled handler that does request rewriting and response copying. The Java pattern of one method per verb (`getRequest`, `postRequest`, `putRequest`, `deleteRequest`, `headRequest` — `ProxyRequestHandler.java:121-168`) is an Airlift-`HttpClient`-API artifact; in Go the right shape is one handler per route with the verb already in `r.Method`. The harder questions are: (1) `httputil.ReverseProxy` vs. hand-rolled forwarder — they're roughly equal effort for our needs and the hand-rolled path gives clearer control over the queryId-extraction seam; (2) the `PRESERVED_HEADERS_TO_SKIP` list (`Accept-Encoding`, `Host`) needs explicit handling that `ReverseProxy` doesn't do automatically; (3) Go's default hop-by-hop header stripping in `ReverseProxy` matches the Java behavior closely but the `Via` header injection (`ProxyRequestHandler.java:326`) is added by the Java code and must be added by ours. This study is the **forwarding-mechanics counterpart** to `[[proxy-streaming-vs-buffering.go-implementer.md]]` which covers the buffering decision; read that first for the queryId-extraction seam discussion.

## Key Findings

### `httputil.ReverseProxy` vs. hand-rolled — recommendation: hand-rolled forwarder

`net/http/httputil.ReverseProxy` is the obvious go-to for any proxy. But for this gateway specifically, the seams we need don't fit cleanly:

| Seam needed | ReverseProxy story | Hand-rolled story |
|---|---|---|
| Inject backend URL per request (routing decision) | `Director` callback rewrites `req.URL` and `req.Host` | Set `req.URL` and `req.Host` before `client.Do(req)` |
| Skip `Accept-Encoding` and `Host` from forwarded headers | Manual: `Director` deletes them; `ReverseProxy` does not strip these by default | One predicate: copy headers if not in skip-list |
| Add `Via` header | `Director` sets it | One line after copying headers |
| Add `X-Forwarded-*` (if enabled) | `Director` sets it (`ReverseProxy` does NOT auto-add by default in Go ≥1.20; the deprecated auto-add is off by default) | One block in handler |
| Read response body to extract `id` (queryId) on POST `/v1/statement` | `ModifyResponse` callback — but reading the body in `ModifyResponse` consumes it; need to wrap and replace `resp.Body` | Read full body into `[]byte`, parse JSON for `id`, write body to `w` |
| Inject cookies on response (`trinoClusterHost`, gateway cookies) | `ModifyResponse` mutates `resp.Header` | Set headers on `w.Header()` before `w.Write(body)` |
| Map any upstream error to `502 Bad Gateway` with a specific body string | `ErrorHandler` callback | Standard `if err != nil { http.Error(...) }` block |
| Apply a single wall-clock timeout to connect+send+receive+downstream-write | Set on request context via `WithContext`; `ReverseProxy` honors it | Set on request context via `WithContext` |

**Verdict:** the seams we need are about the same code volume either way. `ReverseProxy`'s value is mostly streaming + hop-by-hop header handling. Since `[[proxy-streaming-vs-buffering.go-implementer.md]]` argues for full buffering on statement paths anyway (to preserve queryId extraction parity), and since the hop-by-hop list is ~30 lines we can copy from the stdlib's `httputil/reverseproxy.go`, **the hand-rolled path is clearer and fits our seams better**. ~150 LOC of handler vs. ~120 LOC of `ReverseProxy` config + glue. The hand-rolled version makes the buffering choice explicit instead of hidden behind a callback.

**One caveat:** if we ever want true streaming for non-statement paths (the `@architect` open question in `[[proxy-streaming-vs-buffering.go-implementer.md]]`), `ReverseProxy` is the natural fit for those paths. A reasonable v1.5 evolution is: hand-rolled buffered forwarder for `/v1/statement` POSTs and follow-up GETs, `ReverseProxy` for `/ui/*` and other passthrough. **Don't optimize for that on day one** — write the hand-rolled forwarder and consider splitting later.

### Per-verb dispatch is a Java-API artifact

`ProxyRequestHandler.java:121-168` has five methods (`deleteRequest`, `getRequest`, `postRequest`, `putRequest`, `headRequest`) that differ only in which Airlift `Request.Builder` factory they call (`prepareDelete()` vs. `prepareGet()` etc.) and whether they attach a body. This split exists because Airlift's `HttpClient` requires choosing the builder up front. In Go, `*http.Client.Do(req)` is verb-agnostic — `req.Method` is just a string. **Concrete Go shape: one handler function per *route* (statement, ui, mgmt-api, info), not per verb.** Verb-specific logic (does this request have a body? does it need queryId extraction?) is `if r.Method == http.MethodPost && isStatementPath(r.URL.Path)`. Roughly 5x less code than the Java surface.

### Header forwarding — exact rules to replicate

Three header-handling rules to port exactly from `ProxyRequestHandler.java:316-362`:

1. **Skip list:** `Accept-Encoding` and `Host` are NEVER forwarded (`PRESERVED_HEADERS_TO_SKIP`, `:82-84`).
   - `Accept-Encoding`: stripped so the backend always returns uncompressed bytes the gateway can JSON-parse for `id` extraction.
   - `Host`: stripped because Go's `http.Client` sets `req.Host` from `req.URL.Host` automatically when forwarding.
   - **Go gotcha:** `r.Header.Get("Host")` returns empty string — Go's HTTP server strips `Host` from `r.Header` and puts it on `r.Host` instead. The skip-list check on `Host` is therefore a no-op in Go. Keep it for documentation/parity but don't rely on it.
2. **`X-Forwarded-*` conditional:** if `forwardedHeadersEnabled == false`, headers matching `X-Forwarded-*` or `Forwarded` (case-insensitive prefix; `:333-337`) are NOT forwarded. If enabled, the gateway *adds* its own `X-Forwarded-For`, `-Proto`, `-Port`, `-Host` (`:353-362`). **Go gotcha:** `httputil.ReverseProxy` automatically appends `X-Forwarded-For` *unless* `Director` sets it. For the hand-rolled path: copy then explicitly add — and don't double-add if the upstream client already sent one (Java strips client-supplied ones first when in disabled mode; preserve that toggle behavior).
3. **`Via` header:** always added as `Via: <proto> TrinoGateway` (`:326`, e.g. `Via: HTTP/1.1 TrinoGateway`). `httputil.ReverseProxy` does NOT add this — must be explicit.

**Hop-by-hop headers** — Go's stdlib `httputil/reverseproxy.go` strips them via `removeConnectionHeaders` and a hardcoded `hopHeaders` list. The Java side uses Airlift's `HttpClient` which also handles this (RFC 7230 § 6.1). **For the hand-rolled forwarder, copy the `hopHeaders` list from `net/http/httputil/reverseproxy.go` verbatim** — `Connection`, `Proxy-Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `Te`, `Trailer`, `Transfer-Encoding`, `Upgrade`. Plus any header listed in the request's `Connection` header itself. ~20 LOC of stdlib-copy.

### `*http.Client` and `*http.Transport` tuning

The Java side gets its `HttpClient` from Airlift via `@ForProxy` injection (`ProxyRequestHandler.java:97-99`); the actual config lives in `ProxyServerModule` (which I haven't traced — flagging for `@java-analyst`). For Go, one `*http.Client` per traffic class — there are at most two: the proxy client (forwards to Trino backends) and the control client (health probes, stats). Concrete numbers, subject to load-test:

```go
proxyClient := &http.Client{
    Transport: &http.Transport{
        MaxIdleConns:          100,          // total across all hosts
        MaxIdleConnsPerHost:   20,           // per backend; we have ~10 backends
        MaxConnsPerHost:       0,            // 0 = unlimited; bounded by goroutines instead
        IdleConnTimeout:       90 * time.Second,
        TLSHandshakeTimeout:   10 * time.Second,
        ResponseHeaderTimeout: 0,            // DO NOT SET — use ctx deadline instead, per [[proxy-streaming-vs-buffering.go-implementer.md]]
        DisableCompression:    true,         // we strip Accept-Encoding; this is belt-and-braces
        ForceAttemptHTTP2:     true,
    },
    Timeout: 0,                              // DO NOT SET — use ctx deadline instead
    CheckRedirect: func(*http.Request, []*http.Request) error {
        return http.ErrUseLastResponse       // mirrors Java's setFollowRedirects(false) at :185
    },
}
```

**Critical:** `Client.Timeout` and `Transport.ResponseHeaderTimeout` must NOT be set. The end-to-end deadline lives in `context.Context` (see `[[concurrency-and-lifecycle-model.go-implementer.md]]` "Mixed-clock timeouts are a Go-specific footgun"). Setting both produces opaque "which timeout fired?" failures.

`CheckRedirect: ErrUseLastResponse` is the Go equivalent of Java's `setFollowRedirects(false)` (`:185`). Without it, `*http.Client` follows 3xx by default — wrong for a transparent proxy.

### Cookie injection on response

`getOAuth2GatewayCookie` (`ProxyRequestHandler.java:204-224`) returns a list of cookies that get appended to the response. Two distinct mechanisms:
- **OAuth2 path-bound cookies:** if the backend URL path starts with the OAuth2 path and there's no existing cookie, build a fresh `OAuth2GatewayCookie` from the backend's scheme://authority.
- **Generic gateway cookies:** for each gateway cookie present on the inbound request, evaluate its validity and `matchesDeletePath` and produce a deletion cookie (`maxAge=0`) if needed.

In Go, this maps to a `[]*http.Cookie` slice the handler builds before writing the response. `http.SetCookie(w, c)` appends `Set-Cookie` headers. The cookie types themselves (`GatewayCookie`, `OAuth2GatewayCookie`) are plain Go structs with JSON marshaling — no JAX-RS `NewCookie.Builder` equivalent needed. The auth-cookie design is a study unto itself (`[[gateway-cookies-and-sticky-routing]]` — currently pending) — flagging that the Go implementation depends on that design being finalized first.

### `setFollowRedirects(false)` is load-bearing for the statement protocol

Backends can return 3xx (e.g. spooled-segment redirects to S3). The gateway must NOT follow them on behalf of the client — the client (typically Trino client libs) is expected to handle them itself. The Java code makes this explicit (`:185`); Go's default is "follow up to 10 redirects" which would break the spooled-segment protocol silently. **The `CheckRedirect: ErrUseLastResponse` setting on the `*http.Client` is mandatory, not optional.** Tag for Go QA differential testing.

### One small `ProxyHandler` struct, three responsibilities

Cleanest Go shape based on the Java code's actual seams:

```go
type ProxyHandler struct {
    client          *http.Client
    routing         RoutingManager           // for setBackendForQueryId
    history         QueryHistoryManager
    statementPaths  []string                 // from config
    forwardHeaders  bool                     // from config
    asyncTimeout    time.Duration            // from config
    bufferLimit     int64                    // from config; see [[proxy-streaming-vs-buffering.go-implementer.md]]
    cookieSigner    *GatewayCookieSigner     // see [[config-loading.go-implementer.md]] gotcha #3
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 1. Route → backend (consults routing.SelectBackend)
    // 2. Rewrite request (headers, body if buffered)
    // 3. ctx, cancel := context.WithTimeout(r.Context(), h.asyncTimeout)
    // 4. client.Do(req)
    // 5. If statement-path POST: read body, parse id, record mapping
    // 6. Copy response (headers, cookies, status, body) to w
    // 7. On any error: 502 with exact Java-side message string
}
```

~150 LOC of handler. The routing decision happens in `routing.SelectBackend(r) (*Backend, error)` — a separate component owned by the routing study. Cookies come from `h.cookieSigner` (constructor-injected; see `[[config-loading.go-implementer.md]]` for the singleton-replacement story).

## Behavior vs. Implementation Artifact

### Per-verb method dispatch
- **Observed behavior:** `ProxyRequestHandler` has separate methods for each HTTP verb (`ProxyRequestHandler.java:121-168`).
- **Source of behavior:** `jvm-artifact` — Airlift's `HttpClient` requires choosing the builder factory (`prepareGet()`, `preparePost()`) up front.
- **Rationale:** The Airlift API shape forces this split.
- **Go obligation:** `drop`. One handler dispatches on `r.Method`. ~5x less code.

### `setFollowRedirects(false)`
- **Observed behavior:** Backend redirects (3xx) are forwarded verbatim to the client, not followed by the gateway (`ProxyRequestHandler.java:185`).
- **Source of behavior:** `protocol-required`. The Trino statement protocol allows backends to redirect to spooled segments; only the client library knows what to do with these.
- **Go obligation:** `replicate-exactly`. Set `CheckRedirect: ErrUseLastResponse` on the `*http.Client`. Without this, the Go gateway breaks the spooled-segment protocol silently.
- **Notes:** Go QA differential test: backend returns 307 with `Location` header → gateway response is 307 with `Location` header unchanged. Easy to verify.

### `Via` header injection
- **Observed behavior:** Every forwarded request gets `Via: <proto> TrinoGateway` added (`ProxyRequestHandler.java:326`).
- **Source of behavior:** `gateway-design-intent` — RFC 7230 § 5.7.1 compliance + operator visibility.
- **Go obligation:** `replicate-exactly`. One line: `req.Header.Add("Via", r.Proto+" TrinoGateway")`. `httputil.ReverseProxy` does NOT add this automatically.
- **Notes:** if multiple gateways are chained, RFC says append (comma-separated). Java's `addHeader` appends; Go's `Header.Add` also appends. Should be correct by default.

### Conditional `X-Forwarded-*` add and strip
- **Observed behavior:** Behavior gated on `forwardedHeadersEnabled` config (`:328-330, :340-350`):
  - Enabled: gateway adds `X-Forwarded-For`, `-Proto`, `-Port`, `-Host` based on the inbound request (`:353-362`).
  - Disabled: gateway strips any client-supplied `X-Forwarded-*` headers AND does not add its own.
- **Source of behavior:** `gateway-design-intent` — operators choose whether to trust upstream forwarded headers.
- **Go obligation:** `replicate-exactly`. Two-mode behavior matters for downstream auth that consumes these headers.
- **Notes:** `httputil.ReverseProxy` auto-appends `X-Forwarded-For` unless `Director` clears it. Hand-rolled path explicitly controls add/strip; ReverseProxy path needs Director-level care.

### `PRESERVED_HEADERS_TO_SKIP` (`Accept-Encoding`, `Host`)
- **Observed behavior:** Headers in this list never forward (`ProxyRequestHandler.java:82-84, :340-351`).
- **Source of behavior:** mixed — `Accept-Encoding`: `gateway-design-intent` (forces uncompressed response so JSON-parse for `id` works); `Host`: `protocol-required` (the HTTP client sets `Host` from URL).
- **Go obligation:** `replicate-exactly` for `Accept-Encoding`; `Host` is structurally handled by `net/http` (see Go gotcha in Key Findings).
- **Notes:** if `[[proxy-streaming-vs-buffering.go-implementer.md]]`'s "buffer-only-on-statement-paths" v1.5 plan happens, `Accept-Encoding` could be allowed on non-statement paths to improve UI latency. Don't optimize yet.

## Implications for Go Rewrite

- **Recommend hand-rolled forwarder for v1**, with `httputil.ReverseProxy` as a backup for any non-statement paths if we later split. Cleaner mapping of the seams we have.
- **One `*http.Client` per traffic class** (proxy, control). Constructed in `main`, shared via constructor injection.
- **No `Client.Timeout`, no `Transport.ResponseHeaderTimeout`.** End-to-end deadline lives in `context.Context`. Cross-reference `[[concurrency-and-lifecycle-model.go-implementer.md]]`.
- **`CheckRedirect: ErrUseLastResponse` is mandatory.** Reproduce Java's `setFollowRedirects(false)`.
- **Hop-by-hop header stripping:** copy `hopHeaders` from `net/http/httputil/reverseproxy.go`. ~20 LOC.
- **`Via` header injection in the handler.** One line; easy to miss.
- **`X-Forwarded-*` conditional add/strip respects the `forwardedHeadersEnabled` config.** Two-mode behavior — both paths matter for downstream auth.
- **Cookie support depends on the cookie design study** (currently pending: `[[gateway-cookies-and-sticky-routing]]`). Don't write the cookie injection code until that design lands.
- **One handler struct, ~150 LOC.** Routing decision and queryId-mapping are separate components consumed via interfaces — keeps the handler testable.

## Test Strategy Hooks

- **Test level:** integration (handler + mock backend) for the full forwarding path; unit tests for the header skip/forward predicates.
- **Fixtures required:**
  - **Skip-list correctness:** inbound request with `Accept-Encoding: gzip` → backend receives no `Accept-Encoding`. Inbound with `Host: gateway.example.com` → backend receives `Host: backend.example.com`.
  - **`Via` header:** inbound `HTTP/1.1` → outbound has `Via: HTTP/1.1 TrinoGateway`. Inbound already has `Via: 1.1 client-proxy` → outbound has `Via: 1.1 client-proxy, HTTP/1.1 TrinoGateway` (RFC append semantics).
  - **`X-Forwarded-*` enabled:** inbound from `10.0.0.5` over HTTPS to port 443 → outbound has `X-Forwarded-For: 10.0.0.5`, `X-Forwarded-Proto: https`, `X-Forwarded-Port: 443`.
  - **`X-Forwarded-*` disabled:** client supplies `X-Forwarded-For: spoofed` → backend receives no `X-Forwarded-*` headers at all.
  - **Redirect non-following:** backend returns `307` with `Location` → client sees `307` with `Location` unchanged. No follow.
  - **Hop-by-hop stripping:** client sends `Connection: keep-alive, X-Custom`, `X-Custom: foo` → backend receives no `X-Custom` (listed in `Connection`).
  - **End-to-end timeout:** backend hangs → client sees `502` with body `"Request to remote Trino server timed out after<duration>"` (exact Java string for monitoring parity).
  - **Backend error:** backend returns connection-refused → client sees `502` with sensible body.
- **Observable signals:** outbound request headers (use a mock that records all received headers); response status, body bytes, headers, set-cookies on the client side.
- **Non-determinism risks:** timeout assertions need bracket tolerances (`elapsed >= timeout && elapsed < timeout + slack`); cross-implementation differential tests against the Java side will be flaky on strict equality.
- See paired QA study `[[proxy-lifecycle-testable-seams.go-qa.md]]` for the test-pyramid breakdown of these scenarios.

## Open Questions

- `@architect`: confirm hand-rolled forwarder over `httputil.ReverseProxy` for v1? Both work; the choice affects the package layout (`internal/proxy/forwarder.go` either way, but internal structure differs).
- `@architect`: should the v1 forwarder support websockets / SSE / `Upgrade`? The Java code doesn't (statement protocol is plain POST/GET polling), but `httputil.ReverseProxy` supports `Upgrade` and some Trino setups use it for UI. Affects the hand-rolled-vs-ReverseProxy decision. (Probably v2 concern.)
- `@architect`: confirm one `*http.Client` per traffic class (proxy + control) vs. one shared? Affects pool sizing math.
- `@java-analyst`: trace `ProxyServerModule` — what's the actual Airlift `HttpClient` config (idle/timeout/pool numbers)? I need to size the Go `*http.Transport` to match for behavioral parity.
- `@java-analyst`: confirm Java's `setFollowRedirects(false)` covers all 3xx codes — not just 30{1..8}. Need exhaustive list for the Go test fixture.
- `@trino-expert`: does the Trino statement protocol use `Upgrade` on any path the gateway sees? If yes, the v1 hand-rolled forwarder needs to support it (today's `[[proxy-streaming-vs-buffering.go-implementer.md]]` assumes plain HTTP request/response).
- `@qa-tech-lead`: are the exact response-body strings for `502` errors (e.g. `"Request to remote Trino server timed out after..."`) part of the contract for operator alerting? If yes, byte-identical parity is mandatory.
- `@go-qa`: paired coverage on the header-skip/forward predicates? They're easy to write and easy to regress.

## Cross-references

- `[[proxy-streaming-vs-buffering.go-implementer.md]]` — the buffering decision, queryId-extraction seam, `String`-vs-`[]byte` argument. This study covers the *mechanics* of forwarding; that one covers the *body-handling* policy.
- `[[concurrency-and-lifecycle-model.go-implementer.md]]` — async-timeout, `context.Context` propagation, mixed-clock-timeout footgun.
- `[[config-loading.go-implementer.md]]` — cookie-signer injection that replaces the Java singleton.
- `[[proxy-request-lifecycle.java-qa.md]]` — Java QA's behavioral spec of the proxy lifecycle.
- `[[proxy-lifecycle-testable-seams.go-qa.md]]` — Go QA's test-pyramid for these seams.
- `[[gateway-cookies-and-sticky-routing]]` — pending; the cookie injection design.
