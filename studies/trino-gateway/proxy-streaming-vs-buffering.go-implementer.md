---
title: Proxy is fully buffered, not streaming — Go rewrite must decide whether to preserve that
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
related-to: []
---

# Proxy is fully buffered, not streaming — Go rewrite must decide whether to preserve that

## Summary

The Java gateway does **not** stream backend responses to clients. `ProxyResponseHandler.handle` reads up to `responseSize` bytes from the backend response into a single Java `String` in heap, then `ProxyRequestHandler.buildResponse` hands that whole `String` back to JAX-RS as a complete entity. Request bodies are also fully buffered: `postRequest` / `putRequest` take a pre-read `String statement` and rebuild the body with `StaticBodyGenerator`. This is a load-bearing implementation choice: it bounds memory per request, simplifies error mapping, and lets the gateway parse the response JSON to extract `id` (the Trino `queryId`) before forwarding. A "streaming-by-default" Go implementation using `httputil.ReverseProxy` would be an unintentional behavior change with second-order consequences.

## Key Findings

- **Response is fully buffered into a heap-resident `String`.** `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:47-55` constructs `new String(response.getInputStream().readNBytes((int) responseSize.toBytes()), UTF_8)`. The cap is `proxyResponseConfiguration.getResponseSize()` (configurable; default lives in `ProxyResponseConfiguration`). Any response exceeding the cap is silently truncated by `readNBytes`, not errored.
- **Response body is a `String`, not a `byte[]`.** That's not a Java type detail to ignore — it implies an **assumption that responses are UTF-8 text**. Binary responses (spooled segments, future protocol additions) would be corrupted by round-tripping through `String`. The Trino statement protocol is JSON today, but the gateway also proxies `/v1/info`, `/ui/*` and other paths via the same pipeline.
- **Request body is also fully buffered.** `ProxyRequestHandler.postRequest` (`ProxyRequestHandler.java:139-148`) takes a `String statement` parameter — the JAX-RS resource layer has already drained the servlet input stream into a string before calling the proxy. `createStaticBodyGenerator(statement, UTF_8)` then re-encodes it. Same UTF-8 assumption applies.
- **The buffering is what enables `queryId` extraction.** `recordBackendForQueryId` (`ProxyRequestHandler.java:281-285`) calls `OBJECT_MAPPER.readValue(response.body(), HashMap.class)` and pulls `results.get("id")`. This only works because the full body is in memory. A streaming proxy would have to either (a) peek-then-tee, or (b) parse JSON incrementally — both more complex than the current code.
- **Async response with a single timeout.** `setupAsyncResponse` (`ProxyRequestHandler.java:239-247`) wraps the future with `bindAsyncResponse(...).withTimeout(asyncTimeout, ...)`. This is a wall-clock deadline on the *entire* round-trip including buffering, not a slow-loris-style read timeout. Behaviorally, that's a different SLA from `http.Server.ReadTimeout` / `WriteTimeout` in Go.
- **Error mapping is uniform: anything that throws → `502 Bad Gateway`.** `handleProxyException` (`ProxyRequestHandler.java:254-258`) collapses all upstream failures (including the `readNBytes` IOException above) into `502` with the exception message as the body. The async-timeout path also produces `502` with a fixed `"Request to remote Trino server timed out after"` message (`ProxyRequestHandler.java:242-246`).
- **No connection reuse from client to gateway is implied here.** That's the Java HTTP server's concern (Jetty via Airlift), not this code. But the *backend* side uses `@ForProxy HttpClient` (Airlift), which does pool connections — the Go equivalent is `http.Transport` with `MaxIdleConnsPerHost`.
- **Skipped request headers:** `Accept-Encoding` and `Host` are forwarded *only* if not in `PRESERVED_HEADERS_TO_SKIP` (`ProxyRequestHandler.java:82-84`, applied in `shouldForwardHeader` at `:340-351`). `Accept-Encoding` is stripped, presumably so the backend always returns uncompressed JSON the gateway can parse. `Host` is stripped because the backend's `Host` is set by the new `Request.Builder`. Any Go implementation must reproduce this skip-list exactly.

## Behavior vs. Implementation Artifact

### Full-buffer-then-forward response handling
- **Observed behavior:** Backend response is read into a UTF-8 `String` capped at `responseSize` bytes; client sees the response only after the gateway has it all. `ProxyResponseHandler.java:47-55`.
- **Source of behavior:** `gateway-design-intent` — the body is needed for `queryId` extraction and `502` mapping. The size cap is `ops-affordance` to bound heap.
- **Rationale:** Lets the gateway record `queryId → backend` mapping on POST `/v1/statement` responses (`ProxyRequestHandler.java:269-301`). Without buffering, you cannot read the body, extract `id`, and still forward the response unmodified — at least not without parsing JSON while streaming, which the current code does not attempt.
- **Go obligation:** `replicate-intent`. The Go rewrite should buffer the response on *paths where it must extract `queryId`* (POST to `statementPaths`), and may stream other paths. A full move to streaming is possible but is a behavior change — escalate to `@architect` before committing.
- **Notes:** The `String`-not-`byte[]` choice is what concerns me most. Today it's safe because Trino's statement protocol is JSON. If a future Trino release adds spooled binary segments served through `/v1/statement` polling, this design corrupts them. Worth a `@trino-expert` check on whether any current path under `statementPaths` can return non-UTF-8 bytes.

### Silent truncation at `responseSize` cap
- **Observed behavior:** `readNBytes((int) responseSize.toBytes())` returns up to N bytes and returns normally; the gateway forwards the truncated body with the original status code and headers. No error, no log, no `Content-Length` adjustment (`ProxyResponseHandler.java:50`).
- **Source of behavior:** `defensive-historical` / `ops-affordance` — heap-bound protection.
- **Rationale:** Prevents a runaway-large response from OOM'ing the gateway. The cap is configurable.
- **Go obligation:** `defer-to-expert`. Silent truncation produces an *invalid* JSON body downstream — the Trino client would then fail to parse, but with no useful diagnostic. I would propose the Go version logs at WARN and adds a response header (`X-Gateway-Truncated: true`) so the failure mode is debuggable. Needs `@architect` sign-off — that's a behavior addition, not a port.

### Async wall-clock timeout
- **Observed behavior:** A single `asyncTimeout` applied via `withTimeout(asyncTimeout, ...)` covers connect + send + receive + buffer-into-string + downstream handler chain (`ProxyRequestHandler.java:242-246`).
- **Source of behavior:** `gateway-design-intent`.
- **Rationale:** Simple SLA: "client waits at most X for a Trino response."
- **Go obligation:** `replicate-intent` using `context.WithTimeout` on the outbound request and the downstream write. **Do not** reuse `http.Transport.ResponseHeaderTimeout` alone — that only covers headers, not body buffering. A context-bound deadline on the whole round-trip is the right Go analog.

## Implications for Go Rewrite

- **Cannot use `httputil.ReverseProxy` unmodified for statement paths.** `ReverseProxy` is streaming-first and does not give a natural "buffer body, peek `id`, then forward" hook. Realistic options: (a) custom `RoundTrip` followed by manual `io.Copy` *after* a buffered prefix read, or (b) `ReverseProxy.ModifyResponse` with full body read+rewrap — but `ModifyResponse` is called before the body is sent, so this works. Implementer recommendation: `ModifyResponse` for paths that need `queryId` extraction; plain forward for others. Final call belongs to `@architect`.
- **Buffer cap must be a typed config knob, not silently inferred.** The Java code reads `proxyResponseConfiguration.getResponseSize()` (a `DataSize`). Go config should expose it as `ResponseBufferLimit` of type `int64` (bytes) and propagate it to the buffer wrapper. Default value lives in `ProxyResponseConfiguration` Java class — need to copy that constant verbatim into the Go default to preserve operational behavior.
- **`String` vs `[]byte`:** in Go we should use `[]byte` throughout the proxy path, **not** `string`. Cheaper (no extra allocation for the conversion that happens in Java), avoids the UTF-8-implicit-conversion trap, and future-proofs against binary responses on `statementPaths`. This is a place where Go can be more correct than Java, not less.
- **Single async timeout:** put the deadline in a `context.Context` attached to the inbound request (`http.Request.Context()`) plus a `context.WithTimeout`. On timeout, write `502 Bad Gateway` with the exact same body text Java emits (`"Request to remote Trino server timed out after"` plus the duration), for behavior parity with any monitoring/alerts users have.
- **Skip-list parity:** the `PRESERVED_HEADERS_TO_SKIP` (`Accept-Encoding`, `Host`) and the `isForwardedHeader` predicate (`ProxyRequestHandler.java:333-337`) must be reproduced exactly. `httputil.ReverseProxy.Director` strips `Connection` and hop-by-hop headers automatically, but not these two — they need explicit handling.
- **Backend HTTP client config:** Airlift's `@ForProxy HttpClient` lives behind `ForProxy.java` and is configured in `ProxyServerModule`. Go equivalent is a dedicated `*http.Client` with a tuned `http.Transport` — pool sizes, idle timeouts, TLS config. This is its own small spec; not in scope for this study, but worth flagging for the architect's package layout.

## Test Strategy Hooks

- **Test level:** integration (proxy + mock backend) plus unit tests for the body-wrapper layer.
- **Fixtures required:** mock Trino backend that can serve (a) a normal `/v1/statement` POST returning `{"id": "20260524_120000_00001_abcde", ...}`, (b) a response larger than the buffer cap (verify truncation behavior matches Java's silent truncate, or whatever the architect decides), (c) a backend that hangs to test the async-timeout path, (d) a non-UTF-8 byte sequence on a non-statement path to verify the `[]byte` change doesn't regress.
- **Observable signals:** response status code (`200` from backend → `200` to client; backend error → `502` with exact message), response body bytes (full equality on small responses, length-equality on capped), response headers (skip-list correctness), Trino-side mapping side effect (`RoutingManager.setBackendForQueryId` invoked exactly once for POST to statement paths).
- **Non-determinism risks:** Java's `bindAsyncResponse` lives on a thread pool; Go's `context.Deadline` is monotonic-clock-based. Cross-implementation differential tests on timeout boundaries will be flaky if the assertion is strict equality on the deadline — use bracketed assertions (`elapsed >= timeout && elapsed < timeout + slack`).
- See paired QA study (none yet — flagging `@go-qa` for paired coverage).

## Open Questions

- `@trino-expert`: do any paths matched by `statementPaths` ever return non-UTF-8 bytes (binary spooled segments, etc.) in any released Trino version we care about? If yes, the `String`-based Java code is already wrong and we should not replicate it.
- `@architect`: should the Go rewrite silently truncate at the buffer cap (Java parity) or add `X-Gateway-Truncated` + WARN log (proposed improvement)? Operationally meaningful — affects what client tools see when the cap is hit.
- `@architect`: is "streaming for non-statement paths, buffer for statement paths" an acceptable scope expansion for v1, or should v1 be strict parity (buffer everything) and streaming come later?
- `@java-analyst`: the JAX-RS resource that calls `postRequest` already has the body as a `String`. What pre-processing happens there (size limit, encoding, content-type check)? I haven't traced the inbound pipeline yet.

## Cross-references

- `[[concurrency-and-lifecycle-model.go-implementer.md]]` — the async-timeout, executor pools, and `@PreDestroy` lifecycle are documented separately.
- `[[jvm-dependencies-inventory.go-implementer.md]]` — Airlift `HttpClient`, `DataSize`, `Duration`, `JsonCodec` substitutions live there.
