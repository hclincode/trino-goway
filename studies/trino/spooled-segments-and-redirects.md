---
title: Spooled-segment protocol — out-of-band data paths the gateway must not break
author: trino-expert
role: Trino & Trino-Gateway Expert
component: trino
topics: [statement-protocol, proxy-core]
date: 2026-05-24
status: peer-reviewed
reviewer: java-analyst
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino/statement-protocol-overview.md
  - both/gateway-coordinator-nexturi-contract.md
---

# Spooled-segment protocol — out-of-band data paths the gateway must not break

## Summary

Modern Trino clients can opt into the "spooled" result-set protocol: instead of inlining row data in `QueryResults`, the coordinator returns segment handles whose data is fetched from a separate endpoint — and depending on cluster config, that fetch may be a `303 See Other` redirect to a presigned object-storage URL or directly to a Trino worker. The current trino-gateway has no spooling-aware code; it works only because it passes responses through unmodified and follows the redirect contract by NOT auto-following redirects (`setFollowRedirects(false)`). Any Go rewrite that "improves" on this behavior — by following redirects internally, rewriting Location headers, or buffering segment data — will break spooled clients.

## Key Findings

- Clients opt in by sending `X-Trino-Query-Data-Encoding: json+zstd` (or `json+lz4`, etc.) on the initial POST. If the coordinator supports the requested encoding, response `QueryResults` documents carry segment handles in their `data` field instead of inline rows, and the response includes `X-Trino-Query-Data-Encoding` echoing the negotiated encoding.
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:82,90` (request/response header names).

- Segment fetch endpoints on the coordinator:
  - `GET /v1/spooled/download/{identifier}` — fetch a spooled segment.
  - `GET /v1/spooled/ack/{identifier}` — acknowledge consumption (allows the coordinator to garbage-collect the segment).
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/CoordinatorSegmentResource.java:64-106`.

- Segment fetch endpoint on workers:
  - `GET /v1/spooled/download/{identifier}` (same path as coordinator; routed to whichever node is responsible).
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/WorkerSegmentResource.java:36-50`.

- Four retrieval modes (operator-configurable via `protocol.spooling.retrieval-mode`):
  - `STORAGE` — client receives presigned URI directly, fetches from object storage. Coordinator/gateway is not in the data path.
  - `COORDINATOR_STORAGE_REDIRECT` — client hits coordinator's `/v1/spooled/download/...`, receives `303 See Other` with Location pointing to a presigned object-storage URI.
  - `COORDINATOR_PROXY` — client hits coordinator's `/v1/spooled/download/...`, receives the segment bytes streamed through the coordinator.
  - `WORKER_PROXY` — client hits coordinator's `/v1/spooled/download/...`, receives `303 See Other` redirecting to a worker node, then streams from there.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingConfig.java:160-167` and `CoordinatorSegmentResource.java:72-89`.

- The current trino-gateway:
  - Sets `setFollowRedirects(false)` on its HTTP client (`gateway-ha/.../ProxyRequestHandler.java:184-186`). 303 redirects are returned to the client verbatim.
  - Does NOT special-case `/v1/spooled/*` paths in routing rules or sticky binding. Spooled-segment downloads are routed using the same generic flow.
  - Does NOT extract a queryId from `/v1/spooled/...` paths (the segment identifier is opaque, not a queryId).
  - Does NOT set any `TG.*` cookie whose `routingPaths` covers `/v1/spooled`, so the cookie-stickiness fallback in `RoutingTargetHandler.getPreviousCluster` (`RoutingTargetHandler.java:153-172`, prefix `startsWith` match against `routingPaths`) never matches spooled paths in practice. Net: multi-backend spooled-download routing today is effectively random — whichever backend the routing-group selector picks at the moment, regardless of where the segment lives.
  - Source: `gateway-ha/.../ProxyUtils.java:51,89-125` — queryId extraction only matches statement / UI paths, not spooled paths.

- This is an **untested behavior** and almost certainly an oversight (multi-cluster spooling was added later and the gateway was never updated), not a deliberate design choice. In single-cluster deployments it never manifests. In multi-cluster gateway deployments with spooling enabled, segment-download requests will 404 against the wrong backend with no affinity recovery.

- The **response-buffering hazard** that affects all proxied paths also affects spooled segments specifically: `ProxyResponseHandler.handle` (`gateway-ha/.../ProxyResponseHandler.java:47-55`) calls `response.getInputStream().readNBytes((int) responseSize.toBytes())` and packages the result as a UTF-8 `String`. For `STORAGE` and `COORDINATOR_STORAGE_REDIRECT` modes this is fine (small 303 bodies). For `COORDINATOR_PROXY` and `WORKER_PROXY` modes where the gateway proxies segment bytes, this means **segments larger than `responseSize` are silently truncated** and binary segment encodings are corrupted by UTF-8 decoding. Net: those two modes are effectively broken behind the current Java gateway, regardless of routing. Credit to java-analyst for surfacing this implementation detail.

## Behavior vs. Implementation Artifact

### `setFollowRedirects(false)` is load-bearing
- **Observed behavior:** The gateway's airlift HTTP client is configured to not follow 3xx redirects. 303 responses from `/v1/spooled/download/...` are returned to the client unchanged.
  Source: `gateway-ha/.../ProxyRequestHandler.java:184-186`.
- **Source of behavior:** `protocol-required`. The presigned URI is signed for the **client's** identity context (or for direct anonymous fetch from storage); if the gateway followed it, the gateway's identity would be the one streaming the bytes, defeating the entire point of the redirect mode.
- **Rationale:** Decouples the data path from the metadata path, so result-set bytes don't bottleneck through the gateway. For S3-redirect mode, lets the client pull directly from object storage with one round-trip after the redirect.
- **Go obligation:** `replicate-exactly`. The Go gateway's HTTP client MUST NOT auto-follow redirects on proxied requests. If using `net/http`, set `Client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }`.
- **Notes:** This applies to ALL proxied paths, not just spooled — the Java gateway disables redirect-following globally. OAuth2 flows ALSO depend on this: the gateway returns the IdP's 302 to the client, which navigates the browser.

### `Location` header on 3xx is forwarded verbatim
- **Observed behavior:** `ProxyRequestHandler.buildResponse()` forwards all backend response headers to the client unmodified (`gateway-ha/.../ProxyRequestHandler.java:231-237`).
- **Source of behavior:** `protocol-required` for the redirect modes to work.
- **Rationale:** A presigned S3 URI must reach the client byte-identical to how the storage signed it.
- **Go obligation:** `replicate-exactly`. Do NOT URL-rewrite Location headers. Do NOT canonicalize hostnames in them.
- **Notes:** This is the same invariant as for `nextUri` in the body, applied to redirect responses.

### No queryId extraction and no cookie coverage for `/v1/spooled/*` paths
- **Observed behavior:** The gateway's `ProxyUtils.extractQueryIdIfPresent()` does not match `/v1/spooled/...` paths (`ProxyUtils.java:51,89-125`). The cookie-based fallback in `RoutingTargetHandler.getPreviousCluster` (`RoutingTargetHandler.java:153-172`) uses `cookie.matchesRoutingPath(request.getRequestURI())` which is a prefix `startsWith` against the cookie's `routingPaths` field — but the gateway never sets any cookie whose `routingPaths` covers `/v1/spooled`, so the cookie path doesn't recover spooled routing either. Net: spooled-segment GETs fall through to the routing-group selector.
- **Source of behavior:** `oversight`. Spooled-segment support was added to Trino after the gateway's routing surface stabilized, and the gateway was never updated to cover the new paths. Originally classified as `unclear`; the evidence (no queryId-extraction path, no cookie coverage, no path-prefix special-case anywhere in `RoutingTargetHandler`) leans heavily toward "nobody got around to it" rather than "deliberate decision." Credit to java-analyst for sharpening this.
- **Rationale:** Single-backend deployments work without any special handling; multi-backend gateway deployments with spooling enabled simply weren't an exercised configuration when the code was written.
- **Go obligation:** `defer-to-architect`. Two acceptable v1 stances:
  1. **Defer support** — document that multi-backend spooled-segment routing is out of scope, refuse to start with `routing.backends.length > 1 && spooling.enabled` (or warn). Single-backend deployments work fine.
  2. **Cookie-stick explicitly** — set a `TG.*` cookie on POST-to-statement responses whose `routingPaths` includes `/v1/spooled` and `/v1/spooled/ack`, signed with the same HMAC key as other gateway cookies. Cleanest path; requires deciding cookie scope.
  The `defer-to-expert` framing in the earlier draft was a deflection; the Go rewrite is the moment to pick one.
- **Notes:** Flag as a regression candidate in QA. A single-cluster differential test will not catch it.

### Async timeout AND response buffering both break segment streaming today
- **Observed behavior:** Two compounding issues make `COORDINATOR_PROXY` and `WORKER_PROXY` modes effectively broken behind the current Java gateway:
  1. **Buffering**: `ProxyResponseHandler.handle` reads the segment with `readNBytes((int) responseSize.toBytes())` and decodes as UTF-8 (`ProxyResponseHandler.java:47-55`). Segments larger than `responseSize` are silently truncated; binary segment bytes are corrupted by UTF-8 decoding.
  2. **Async timeout**: `asyncTimeout` (`ProxyRequestHandler.java:108,239-247`) bounds the whole proxied call. If a large segment exceeds this, the gateway returns `502 Bad Gateway` mid-stream.
- **Source of behavior:** `defensive-historical` for the timeout (sized for statement-protocol latencies). `defensive-historical` for the buffering (originally needed for POST queryId extraction; over-applied to all responses).
- **Rationale:** Statement polls should be fast; long hangs indicate backend trouble. Body buffering was the simplest way to JSON-parse the POST response.
- **Go obligation:** `improve-over-java`. Two distinct fixes:
  - **Stream, don't buffer**: use `io.Copy(w, resp.Body)` with a flushable response writer for all non-POST-to-statement-path responses. This fixes silent truncation and UTF-8 corruption in one shot.
  - **Per-route-class timeouts**: short timeout (~30s) for `/v1/statement/...` polls (preserves Trino heartbeat semantics), longer (or idle-read-only) for `/v1/spooled/download/...` streams.
  Together these promote `COORDINATOR_PROXY` / `WORKER_PROXY` modes from "broken" to "working." See `studies/trino-gateway/proxy-streaming-vs-buffering.go-implementer.md` for the architect-side framing.
- **Notes:** Untimed streams need a different protection (idle-read timeout, bandwidth quota) — the Go implementation should not just remove the timeout.

## Implications for Go Rewrite

- **Globally disable HTTP client redirect-following.** Treat 3xx responses as opaque pass-throughs.
- **Stream response bodies, do not buffer.** A buffered response to a 100MB segment is a memory disaster. Use `io.Copy` with a flushable response writer.
- **Add per-path-class timeout configuration.** Statement-poll timeout (~30s) is wrong for segment downloads. The Go rewrite is a good time to introduce this distinction, but do it explicitly.
- **`/v1/spooled/*` routing is an open design question.** Two acceptable v1 stances:
  1. Defer: do not support spooled-segment routing in a multi-backend gateway, document the limitation. Single-backend deployments work fine.
  2. Cookie-stick: rely on `TG.*` cookies set during the statement protocol to also direct spooled-segment fetches. Requires the original POST to set a cookie whose `routingPaths` include `/v1/spooled`.
- **Acknowledge endpoint (`/v1/spooled/ack/{id}`)** has the same stickiness requirement as download.
- **Do not introduce gateway-side caching of spooled segments** — they are typically encrypted/single-use, and caching them would break security assumptions.

## Test Strategy Hooks

- **Test level:** integration (real Trino with spooling enabled) + differential against the Java gateway.
- **Fixtures required:**
  - A Trino testcontainer with `protocol.spooling.enabled=true` and one of each retrieval mode.
  - A client sending `X-Trino-Query-Data-Encoding: json+zstd` and asserting the segment bytes match expected.
  - Multi-backend gateway config to exercise the sticky-routing-for-spooling gap.
- **Observable signals:**
  - 303 responses on `/v1/spooled/download/...` have Location preserved byte-identical to backend's.
  - In `COORDINATOR_PROXY` mode, body bytes are identical (use a checksum, not a deep compare).
  - In multi-backend mode, segment download succeeds on the same cluster that produced the query (look at the backend access logs).
  - Spooled-segment streams are not subject to the same async-timeout as statement polls.
- **Non-determinism risks:** segment GC; if `/v1/spooled/ack/...` is not called by the client, the coordinator may delete the segment between the redirect and the client's follow-up. This isn't a gateway concern but can flake tests.

## Open Questions

- Is there a Trino release note (likely in 446+ / 460+ range) that documents the gateway-compatibility contract for spooling? `@trino-expert` self-note: search release notes in Task #9.
- Is the `/v1/spooled/ack` endpoint REQUIRED for the client, or is it a no-op cleanup that the gateway can drop on failure? `@trino-expert` to clarify.
- Does any production multi-backend gateway deployment have spooling enabled today? If no, defer; if yes, this is urgent. `@architect`

## Cross-references

- [[statement-protocol-overview.md]] — the parent statement protocol that spooling extends.
- [[../both/gateway-coordinator-nexturi-contract.md]] — same `nextUri`-untouched discipline applies to Location headers here.
