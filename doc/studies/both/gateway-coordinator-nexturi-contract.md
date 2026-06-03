---
title: The gateway-coordinator nextUri contract — a silent dependency on http-server.process-forwarded
author: trino-expert
role: Trino & Trino-Gateway Expert
component: both
topics: [statement-protocol, proxy-core, config]
date: 2026-05-24
status: peer-reviewed
reviewer: java-analyst
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino/statement-protocol-overview.md
  - both/sticky-routing-contract.md
---

# The gateway-coordinator nextUri contract — a silent dependency on http-server.process-forwarded

## Summary

The trino-gateway works without rewriting response bodies because the **backend coordinator** is the one that constructs the `nextUri` value placed in `QueryResults`, and it does so using the request-side `ExternalUriInfo` which respects `X-Forwarded-*` headers — but ONLY if the coordinator is started with `http-server.process-forwarded=true`. This is a silent, undocumented, cross-system contract: if an operator deploys the gateway in front of a coordinator that has not set this flag, the `nextUri` will point to the coordinator's internal hostname and the client will fail to reach it. The Go rewrite inherits this contract verbatim.

## Key Findings

- Trino's coordinator builds `nextUri` from `ExternalUriInfo.baseUriBuilder()`, which derives host/port from the JAX-RS `UriInfo` of the incoming request.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/ExternalUriInfo.java:40-83`, used by `trino/core/trino-main/src/main/java/io/trino/dispatcher/LocalCoordinatorLocation.java:22-26` and by `QueuedStatementResource.createQueryResults()` (lines 277-306).

- Airlift's HTTP server (which Trino uses) builds the `UriInfo` base URI from forwarded headers only when `http-server.process-forwarded=true`. This is a per-Trino-coordinator config setting, **independent** of any gateway configuration.

- `ExternalUriInfo` also reads `X-Forwarded-Prefix` directly to support path-prefixed reverse proxies — but the host/port portion comes from airlift's pre-processing.
  Source: `ExternalUriInfo.java:35,42,48`.

- The Java gateway sends `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Port`, and `X-Forwarded-Host` to the backend ONLY when `routing.forwardedHeadersEnabled=true` in its own config (default: false).
  Source: `gateway-ha/.../ProxyRequestHandler.java:108,328-330,353-362`.

- The gateway does NOT rewrite `nextUri` or any other URI inside response bodies. Body content is forwarded verbatim, BUT the current Java implementation buffers and re-encodes it (it does NOT stream byte-for-byte).
  - `ProxyResponseHandler.handle` reads the response with `response.getInputStream().readNBytes((int) responseSize.toBytes())` and constructs a `String(bytes, StandardCharsets.UTF_8)` (`gateway-ha/.../ProxyResponseHandler.java:47-55`). Every proxied response is fully buffered into memory, capped at `responseSize`, and decoded as UTF-8.
  - `ProxyRequestHandler.buildResponse` then passes that `String` as the JAX-RS entity (`ProxyRequestHandler.java:231-237`), which serializes it back to bytes.
  - Two consequences this study originally understated: (1) any response body larger than `responseSize` is **silently truncated**; (2) non-UTF-8 binary bodies (currently none in the statement protocol, but a risk for `COORDINATOR_PROXY` spooled segments — see [[../trino/spooled-segments-and-redirects.md]]) are corruption-vulnerable. Credit to java-analyst for this correction.

- Combined, the contract is:
  - **Operator must set** `http-server.process-forwarded=true` on every backend coordinator.
  - **Operator must set** `routing.forwardedHeadersEnabled=true` on the gateway.
  - If both are true: `nextUri` host matches the gateway's externally-advertised hostname; clients follow it back through the gateway.
  - If either is false: clients receive `nextUri` values pointing at coordinator internal hostnames and queries appear to "hang" (clients fail to follow `nextUri`).

- The current gateway README and admin docs do **not document this dependency** prominently. It's an operational footgun. (Searched docs/ in trino-gateway submodule; nothing surfaces.)

## Behavior vs. Implementation Artifact

### Gateway does not rewrite URIs in response bodies (the invariant) — but currently buffers them (the implementation)
- **Observed behavior:** `ProxyRequestHandler.buildResponse()` passes `response.body()` straight through to the JAX-RS response builder; no JSON parsing of the body occurs at this layer. However, the upstream `ProxyResponseHandler.handle` has already buffered the bytes into a `String` (UTF-8) capped at `responseSize`.
  Source: `gateway-ha/.../ProxyRequestHandler.java:231-237`; `gateway-ha/.../ProxyResponseHandler.java:47-55`.
- **Source of behavior:** Split.
  - **Not-rewriting URIs**: `protocol-required` (rewriting would also need `infoUri`/`partialCancelUri`/spool URIs, etc. — see "Other URIs" below) + `gateway-design-intent` (Trino's external-URI mechanism was specifically designed to make body rewriting unnecessary).
  - **Buffer-into-String**: `defensive-historical`. The buffering exists primarily so `recordBackendForQueryId` can re-parse the POST response with Jackson to extract `id` (`ProxyRequestHandler.java:269-301`). Applying the same response handler to all methods (GET polls, HEAD, DELETE, spooled segments) is an over-application of that mechanism, not an intentional choice.
- **Rationale:** Trino's external-URI mechanism was specifically designed to make body rewriting unnecessary. Doing it in the gateway would duplicate logic and create drift. Buffering the body is a convenience for queryId extraction, not a protocol requirement.
- **Go obligation:** Split.
  - **The URI-non-rewriting invariant**: `replicate-exactly`. The Go gateway MUST NOT parse-and-rewrite response bodies for the purpose of fixing up URIs.
  - **The body-buffering implementation**: `improve-over-java`. Buffer ONLY for POST-to-statement-path responses (where queryId extraction is required); stream every other method. This fixes the silent-truncation bug for large responses and the UTF-8 decoding hazard for binary bodies.
- **Notes:** A "helpful" Go rewrite that JSON-parses every response to fix up URLs would (a) be slow, (b) break on spooled-encoding bodies that are not plain JSON, (c) break the `infoUri`/`partialCancelUri` fields, (d) introduce a new failure surface (parse errors). See `studies/trino-gateway/proxy-streaming-vs-buffering.go-implementer.md` for the architect-side analysis of the streaming-vs-buffering choice.

### Dependency on `http-server.process-forwarded=true` is implicit
- **Observed behavior:** The gateway sends `X-Forwarded-*` headers (when its config enables this) but never validates that the backend will honor them. If the backend ignores them, `nextUri` values are wrong and the client appears stuck — but no error is raised by the gateway.
- **Source of behavior:** `defensive-historical`. The two settings evolved independently in Trino and the gateway.
- **Rationale:** Trino can't safely default `process-forwarded=true` because doing so on a cluster that's NOT behind a trusted reverse proxy would let any client spoof its source IP.
- **Go obligation:** `replicate-intent` plus `add-ops-affordance`. Replicate the forwarding behavior. **Additionally**: the Go gateway should consider a one-time startup probe (HEAD or HEAD-and-inspect) to detect misconfigured backends, emit a clear log warning, and surface a `/v1/gateway/status` field. This is an improvement-over-Java suggestion; flag for the Architect.
- **Notes:** If the probe approach is rejected, at minimum the Go gateway docs MUST call out this dependency on the very first config page. Today's silent failure mode is a known support-ticket generator.

### Other URIs in `QueryResults` follow the same contract
- **Observed behavior:** Besides `nextUri`, the `QueryResults` JSON document contains `infoUri` (link to the query info UI) and may contain `partialCancelUri`. Both are built via the same `ExternalUriInfo` path on the coordinator.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/Query.java` (and parents) — uses `externalUriInfo.baseUriBuilder()` for all of these.
- **Source of behavior:** `protocol-required`.
- **Rationale:** Same as `nextUri` — the client needs reachable URLs.
- **Go obligation:** `replicate-exactly` by NOT touching them. The discipline of byte-identical body forwarding takes care of this for free.
- **Notes:** If anyone proposes "selective body rewriting" for the Go gateway, they need to enumerate ALL URI-bearing fields and version that list against Trino's schema evolution. Strong argument for "don't start down this path."

### `X-Forwarded-Prefix` for path-mounted gateways
- **Observed behavior:** Trino's `ExternalUriInfo` independently reads `X-Forwarded-Prefix` (not standard, not in RFC 7239) to support `https://example.com/trino-prod/` style mountings.
  Source: `ExternalUriInfo.java:35,42`.
- **Source of behavior:** `gateway-design-intent` on the Trino side, supporting reverse-proxy deployments.
- **Rationale:** Allows path-prefixed deployments where the gateway is `/trino-prod/...` instead of at the root.
- **Go obligation:** `replicate-intent`. The Go gateway, if deployed at a path prefix, must inject `X-Forwarded-Prefix: /trino-prod`. The Java gateway today does NOT inject this automatically; it expects operators to set up an upstream proxy that does. Worth deciding: should the Go gateway accept a `gateway.pathPrefix` config and inject the header? Probably yes — cheap addition, removes a footgun.
- **Notes:** This is one of the few places where the Go rewrite can be measurably better than the Java original.

## Implications for Go Rewrite

- **Hard invariant: byte-identical body FORWARDING (no URI rewriting).** Never JSON-parse response bodies for the purpose of altering URIs. Document this as a project-level invariant.
- **Streaming-vs-buffering is a Go-side improvement, not a "replicate-exactly" constraint.** The Java gateway buffers everything (and silently truncates over `responseSize`). The Go rewrite should buffer ONLY for POST-to-statement-path responses (where queryId extraction needs the body) and stream everything else. This fixes a real Java bug; see `studies/trino-gateway/proxy-streaming-vs-buffering.go-implementer.md`.
- The Go gateway's forwarded-header injection must mirror Java exactly: `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Port`, `X-Forwarded-Host` when the corresponding config is on.
- Consider adding `X-Forwarded-Prefix` injection when a `gateway.pathPrefix` config is set. This is new behavior, not a regression risk.
- Consider a startup-time validation probe against each registered backend to verify `http-server.process-forwarded=true` is in effect (e.g., POST a HEAD with a recognizable `X-Forwarded-Host`, observe the response). Surface failures in logs and `/v1/gateway/status`.
- The Architect should require the Go README to document the gateway-coordinator config dependency on the first ops page. This is the #1 misconfiguration in the Java gateway's issue tracker (anecdotally — needs WebSearch confirmation in Task #9).
- This contract applies equally to OAuth2 redirects, spooled-segment 303s, and any future `Location:` or URL-in-body field — the discipline is "don't touch URIs the backend emitted."

## Test Strategy Hooks

- **Test level:** integration (real Trino) + differential (vs. Java gateway).
- **Fixtures required:**
  - Backend Trino with `http-server.process-forwarded=true` AND with it false — exercise both.
  - Gateway with `routing.forwardedHeadersEnabled=true` AND false — exercise both.
  - A client at a different hostname than the gateway, to make wrong-host failures visible.
  - A path-prefixed deployment fixture if pathPrefix is implemented.
- **Observable signals:**
  - For each `[backend process-forwarded] × [gateway forwarded-enabled]` combo, the host portion of `nextUri` in the response body matches expected:
    - Both true → gateway's externally-advertised host.
    - Backend true, gateway false → backend's internal host (failure).
    - Backend false → backend's internal host regardless of gateway (failure).
  - The full QueryResults body, sans the host portion of URIs, is byte-identical between the gateway-mediated response and a direct-to-coordinator response.
- **Non-determinism risks:** none structural; only the usual race between query lifecycle states.

## Open Questions

- Should the Go gateway add a startup probe for backend forwarded-header support? Cheap, high-value, no regression risk. `@architect` for design call.
- Should `X-Forwarded-Prefix` injection be a v1 feature or deferred? `@architect`.
- Is there a known production deployment that uses both `protocol.v1.alternate-header-name` AND a path-prefixed gateway? The combination is corner-case but should be tested at least once. `@java-qa`.

## Cross-references

- [[../trino/statement-protocol-overview.md]] — `nextUri` semantics in the parent protocol.
- [[../trino/spooled-segments-and-redirects.md]] — same byte-identity discipline applies to `Location:` headers on 303 responses.
- [[sticky-routing-contract.md]] — how the gateway makes the `nextUri` work even when polls land on the gateway and need to be re-routed to the same backend.
