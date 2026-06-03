---
title: Trino protocol header prefix is configurable, not hardcoded "X-Trino-"
author: trino-expert
role: Trino & Trino-Gateway Expert
component: trino
topics: [statement-protocol, session-state, proxy-core]
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino/statement-protocol-overview.md
---

# Trino protocol header prefix is configurable, not hardcoded "X-Trino-"

## Summary

The `X-Trino-User`, `X-Trino-Session`, etc. headers are not a fixed string — Trino lets operators configure an alternate header prefix (most commonly `X-Presto-` for legacy clients). A coordinator booted with `protocol.v1.alternate-header-name=Presto` accepts BOTH `X-Trino-*` and `X-Presto-*` request headers and emits response headers in whichever family the client used. Any Go gateway code that pattern-matches header names by the literal string `X-Trino-` is wrong.

## Key Findings

- The header prefix is generated at runtime, not compiled in. The Java implementation: `"X-" + protocolName + "-" + headerName`. With `protocolName="Trino"` (default) you get `X-Trino-User`; with `protocolName="Presto"` you get `X-Presto-User`.
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:105-108`.

- The coordinator detects which family the client is using by scanning the request's header names for any starting with the configured alternate prefix. If both `X-Trino-*` and `X-Presto-*` headers are present on the same request, the coordinator **throws `ProtocolDetectionException`** and rejects the request.
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:382-399`.

- The set of headers covered by the prefix:
  - Request: User, Original-User, Original-Roles, Source, Catalog, Schema, Path, Time-Zone, Language, Trace-Token, Session, Role, Prepared-Statement, Transaction-Id, Client-Info, Client-Tags, Client-Capabilities, Resource-Estimate, Extra-Credential, Query-Data-Encoding
  - Response: Set-Catalog, Set-Schema, Set-Path, Set-Session, Clear-Session, Set-Role, Set-Original-Roles, Query-Data-Encoding, Added-Prepare, Deallocated-Prepare, Started-Transaction-Id, Clear-Transaction-Id, Set-Authorization-User, Reset-Authorization-User
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:61-96`.

- The current trino-gateway code is **already prefix-agnostic by accident**: `ProxyRequestHandler.setupRequestHeaders()` iterates all incoming request headers and forwards them unchanged (`ProxyRequestHandler.java:316-331`). The only place the gateway pattern-matches `X-Trino-` literally is for routing-rule inputs (e.g. `X-Trino-User` extraction in `RoutingTargetHandler`), and configuration entries like `HttpUtils.USER_HEADER`.

- Risk locations in the Java gateway where the prefix is hardcoded:
  - `trino/core/trino-main/src/main/java/io/trino/client/ProtocolHeaders.java` constants (in Trino source, not gateway).
  - In trino-gateway: `gateway-ha/src/main/java/io/trino/gateway/ha/handler/HttpUtils.java` defines `USER_HEADER` etc. as literal `X-Trino-*` strings.
  - `gateway-ha/src/main/java/io/trino/gateway/ha/handler/ProxyUtils.java:42` — `SOURCE_HEADER = HeaderName.of("X-Trino-Source")`.
  - `gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoRequestUser.java` reads `X-Trino-User` for the routing input.

  These are **bugs against `X-Presto-*` deployments** in the Java gateway. The Go rewrite should not propagate them.

## Behavior vs. Implementation Artifact

### Header-prefix hardcoding in routing input extraction
- **Observed behavior:** The Java gateway extracts `X-Trino-User` from incoming requests by literal header name for use in routing decisions and query history. If a client sends `X-Presto-User`, this lookup returns null.
  Source: `gateway-ha/src/main/java/io/trino/gateway/ha/handler/HttpUtils.java` (header constants), `gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoRequestUser.java`.
- **Source of behavior:** `defensive-historical`. The constant pre-dates wide use of `protocol.v1.alternate-header-name`; the Java gateway was largely written when `X-Trino-` was the only realistic case.
- **Rationale:** Convenience / simplicity. There is no protocol-required reason to hardcode this.
- **Go obligation:** `replicate-intent`. The Go gateway should detect the protocol family per-request — mirroring `ProtocolHeaders.detectProtocol()` — and look up the user header in whichever family the client is using. Fall back to `X-Trino-User` when no alternate is configured. Backwards-compatibility cost is zero.
- **Notes:** Requires a config field on the gateway: `routing.protocolAlternateHeaderName` (string, default empty / "Trino"). When set, the gateway should detect cross-family header pollution and reject with the same 400-class response the coordinator gives.

### Coordinator rejects mixed-family header sets
- **Observed behavior:** If a request has both `X-Trino-User` and `X-Presto-User`, the coordinator throws `ProtocolDetectionException` and the request fails.
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:391-394`.
- **Source of behavior:** `protocol-required` (Trino's invariant — having both families ambiguous).
- **Rationale:** Protects against client misconfiguration where two SDK layers each set their own family.
- **Go obligation:** `replicate-intent`. The Go gateway should NOT inject `X-Trino-*` headers that would mix with client-sent `X-Presto-*`. Today the Java gateway's only injection is `Via` and `X-Forwarded-*`, both safe. Avoid being clever.
- **Notes:** The gateway might be tempted to "normalize" by translating `X-Presto-*` → `X-Trino-*` before forwarding. **Don't.** That breaks the cookie-like contract where the client uses the same family on POST that it received in the response.

## Implications for Go Rewrite

- Treat the protocol header prefix as a per-request property, derived by inspecting incoming request headers, not a constant.
- Provide a config `routing.protocolAlternateHeaderName` (or follow whatever naming the Architect chooses); when empty, default to detecting only `X-Trino-*`.
- The `detectProtocol()` algorithm (~10 lines in Java) is trivial to port; the Architect/Implementer should make it the source of truth for the user, source, session, etc. header lookups.
- This is also relevant for the Go gateway's **session-state** awareness if any future routing rule reads session properties — those headers also follow the configurable prefix.
- Cookie family separator: when emitting `TG.*` gateway cookies, use names independent of the Trino protocol prefix. Today's gateway already does (`GatewayCookie.PREFIX = "TG."`); preserve this.

## Test Strategy Hooks

- **Test level:** unit + differential.
- **Fixtures required:**
  - Two mock-backend variants: one accepting only `X-Trino-*`, one only `X-Presto-*`.
  - A test matrix of `[default | alternate=Presto]` × `[client sends X-Trino-* | X-Presto-* | both]`.
- **Observable signals:**
  - User-extraction for routing rules: with alternate=Presto and client sending `X-Presto-User: alice`, the routing decision must see `alice`.
  - Mixed-family request: gateway returns 400-class with a body identifying the conflict (the Java gateway today does NOT do this — it falls through to forwarding both, and the coordinator rejects. The Go rewrite should consider rejecting earlier, but this is a behavior change worth flagging).
- **Non-determinism risks:** none.

## Open Questions

- Do any production deployments actually configure `protocol.v1.alternate-header-name` today? If not, this is low-priority for v1 of the Go rewrite. If yes, it's load-bearing. `@architect` for product/ops scoping.
- Should the Go gateway reject mixed-family requests at the edge (better UX, clearer error) or pass them through (matches Java today)? `@qa-tech-lead` for behavior-policy call.

## Cross-references

- [[statement-protocol-overview.md]] — the headers covered by this prefix.
