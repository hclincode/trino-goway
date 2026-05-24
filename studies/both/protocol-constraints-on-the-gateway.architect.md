---
title: Trino protocol constraints on any gateway intermediary
author: architect
role: Architect / Tech Lead
component: both
topics:
  - statement-protocol
  - proxy-core
  - session-state
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/rewrite-hotspots.md
---

# Trino protocol constraints on any gateway intermediary

## Summary

Independent of implementation language, any HTTP intermediary that sits between Trino clients and coordinators must preserve a small but load-bearing set of protocol invariants: queryId stability across the statement lifecycle, `nextUri` opaqueness to the client (the gateway *must* rewrite host but *must not* rewrite path/queryId), session-header passthrough (set/clear/added/role/transaction), prepared-statement passthrough (including `$zstd:` decoded values), and bounded-latency response semantics. The Java gateway satisfies these by extracting `id` from the POST `/v1/statement` response body and caching queryId→backend bindings; everything downstream of that cache assumes the binding is stable for the lifetime of the query. This study enumerates the constraints, the failure modes if violated, and the implementation choices each one forces on a Go rewrite.

## Key Findings

### The five whitelisted path families the gateway proxies

From `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/PathFilter.java:50-66` and `HttpUtils.java`, the gateway forwards these paths to backends:

| Path prefix | Purpose | Statefulness |
|---|---|---|
| `/v1/statement` | Statement protocol: POST submits, GET polls `nextUri`, DELETE cancels | queryId-stateful |
| `/v1/query` | Coordinator query-info endpoint | queryId-stateful |
| `/v1/info` | Coordinator info (uptime, version) | stateless |
| `/v1/node` | Worker node listing | stateless |
| `/ui` | Trino's admin web UI | session-stateful |
| `/ui/api/stats` | UI stats endpoint | stateless |
| `/oauth2` | OAuth2 redirect paths for the UI's OIDC flow | session-stateful |
| `(extraWhitelistPaths regex list)` | Operator-extended forwarded paths | unknown |

The statement-protocol invariants below focus on `/v1/statement` and `/v1/query`; UI and OAuth paths have their own sticky-routing story driven by cookies.

### The statement-protocol lifecycle the gateway must preserve

Authoritative reference: `trino/docs/src/main/sphinx/develop/client-protocol.rst` (in the trino submodule) and the implementation in `trino/core/trino-main/src/main/java/io/trino/server/protocol/ExecutingStatementResource.java`.

1. **Client submits**: `POST /v1/statement` with the SQL in the body and `X-Trino-User`, `X-Trino-Source`, `X-Trino-Catalog`, `X-Trino-Schema`, `X-Trino-Session`, `X-Trino-Client-Tags`, `X-Trino-Prepared-Statement`, etc. as request headers.
2. **Coordinator responds**: JSON envelope containing `id` (the queryId), `infoUri`, `nextUri`, optional `data`, optional `error`, and an updated stats block. The `nextUri` field is the absolute URL the client should poll next.
3. **Client polls**: GET on the `nextUri` returned in (2). Each poll returns a JSON envelope with optionally more `data`, a new `nextUri`, or no `nextUri` (terminal state). The path looks like `/v1/statement/<state>/<queryId>/<nonce>/<sequenceNumber>` where `<state>` ∈ `{queued, scheduled, executing, ...}`.
4. **Client may cancel**: DELETE on the queryId-bearing path. The coordinator transitions the query to `FAILED`. A `partialCancel` variant exists at `/v1/statement/<state>/partialCancel/<queryId>/...`.

### Constraints the gateway imposes on itself to keep the protocol working

#### Constraint 1: queryId→backend binding must be established before the first poll
- **Why:** the client receives `nextUri` from the coordinator. The `nextUri` host is the gateway's host (because the client's original request went to the gateway, and the coordinator's response is proxied unchanged). The client polls the same gateway. If the gateway routes the poll to a *different* backend than the one that issued the queryId, the second backend returns 404.
- **How Java does it:** in `ProxyRequestHandler.recordBackendForQueryId` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:269-301`). After the coordinator's `POST /v1/statement` response is buffered, the gateway parses the JSON, extracts `id`, and `routingManager.setBackendForQueryId(id, backend)`. Subsequent polls call `findBackendForQueryId` in `RoutingTargetHandler.getPreviousCluster` (`RoutingTargetHandler.java:153-172`).
- **Required:** `replicate-exactly`. The Go port must extract `id` from new POST `/v1/statement` responses before the response is forwarded to the client, otherwise the client's next poll has no binding.

#### Constraint 2: `nextUri` paths must be opaque to the gateway
- **Why:** Trino owns the `nextUri` path format. It encodes `state`, `queryId`, `nonce`, `sequenceNumber`. The gateway must not parse/rewrite the path beyond extracting the queryId for routing decisions. Rewriting the path would break polling.
- **How Java does it:** `ProxyUtils.extractQueryIdIfPresent` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/handler/ProxyUtils.java:79-121`) uses regex to extract queryId only; it does not rewrite the path. The path is forwarded as-is via `buildUriWithNewCluster(backendHost, request)` (`ProxyUtils.java:123-126`) which just prepends the new backend host.
- **Required:** `replicate-exactly`. Go's `httputil.ReverseProxy` makes this natural — set `req.URL.Host` and `req.URL.Scheme`, leave `req.URL.Path` alone.

#### Constraint 3: Session headers must round-trip unchanged
- **Why:** Trino's session is stateless on the wire — every request carries the full session via `X-Trino-Session`, `X-Trino-Catalog`, `X-Trino-Schema`, `X-Trino-Path`, `X-Trino-Role`, `X-Trino-Time-Zone`, `X-Trino-Language`, `X-Trino-Client-Tags`, `X-Trino-Trace-Token`. The coordinator's responses include `X-Trino-Set-Session`, `X-Trino-Clear-Session`, `X-Trino-Added-Prepare`, `X-Trino-Deallocated-Prepare`, `X-Trino-Set-Role`, `X-Trino-Set-Catalog`, `X-Trino-Set-Schema`, `X-Trino-Set-Path`, `X-Trino-Started-Transaction-Id`, `X-Trino-Clear-Transaction-Id` so the client can update its session. The gateway is an intermediary; if any of these are dropped or altered, session state diverges and the client misbehaves.
- **How Java does it:** all headers are forwarded both directions by default in `ProxyRequestHandler.setupRequestHeaders` (`ProxyRequestHandler.java:316-331`) and in `ProxyRequestHandler.buildResponse` (`ProxyRequestHandler.java:231-237`). Only `Accept-Encoding` and `Host` are explicitly skipped on the outbound (`PRESERVED_HEADERS_TO_SKIP`, `ProxyRequestHandler.java:82-84`).
- **Required:** `replicate-exactly`. The Go port must default to "pass through all `X-Trino-*` headers both directions". `httputil.ReverseProxy` does this by default but strips `Hop-by-hop` headers (Connection, Keep-Alive, etc.) which is correct.

#### Constraint 4: Prepared-statement headers may be `$zstd:`-compressed
- **Why:** Trino v447+ added zstd compression of `X-Trino-Prepared-Statement` to handle very large prepared SQL. The gateway has to decode these when the rules engine wants to read them (because the rules engine doesn't see them in compressed form).
- **How Java does it:** `TrinoQueryProperties.decodePreparedStatementFromHeader` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoQueryProperties.java:336-350`).
- **Required:** `replicate-exactly` only on the routing-decision path (where the rules engine needs to inspect them). On the proxy passthrough path, the header is forwarded as-is — *never* re-encode or re-compress.
- **Notes:** This is a place where the gateway does protocol-level processing only for its own use; the *backend* must see whatever the *client* sent. Don't introduce a bug where the gateway decodes, evaluates, then forwards the decoded form.

#### Constraint 5: Cancellation is in-band via DELETE
- **Why:** clients cancel by issuing `DELETE` on the queryId-bearing path. The gateway must route the DELETE to the same backend the query lives on, or cancellation silently fails.
- **How Java does it:** `RouteToBackendResource.deleteHandler` (`RouteToBackendResource.java:81-88`) uses the same `RoutingTargetHandler.resolveRouting` flow, which consults the queryId→backend cache for the queryId extracted from the path.
- **Required:** `replicate-exactly`. The Go port's queryId→backend cache must back DELETE routing identically.

#### Constraint 6: `system.runtime.kill_query()` is a queryId-bearing CALL
- **Why:** Trino also exposes query cancellation via `CALL system.runtime.kill_query(query_id => '...')`. From the gateway's perspective this is a `POST /v1/statement` whose body happens to be a kill-call. The gateway must route this POST to the same backend the *target queryId* lives on, not to a fresh backend.
- **How Java does it:** `TrinoQueryProperties.extractQueryIdFromCall` extracts the target queryId from the SQL body (`TrinoQueryProperties.java:454-464`); `ProxyUtils.extractQueryIdIfPresent` for POST checks if the body contains `kill_query` and, if so, returns the extracted target queryId (`ProxyUtils.java:79-95`). This means the kill request is routed to the same backend as the original query.
- **Required:** `replicate-exactly`. The Go port must do this. Caveat: it requires the SQL parser (constraint 4 hotspot) to recognize the `CALL` form. Without the parser, kill_query routing breaks. Document this dependency.

#### Constraint 7: Bounded-latency response — return 502 on backend hang
- **Why:** clients have their own timeouts; if the gateway hangs, the client gives up and the queryId may be orphaned (no further polls means coordinator GCs the result). The gateway should produce a deterministic timeout error so the client can react.
- **How Java does it:** `bindAsyncResponse(...).withTimeout(asyncTimeout, ...)` (`ProxyRequestHandler.java:239-247`) returns 502 with body `"Request to remote Trino server timed out after <duration>"`.
- **Required:** `replicate-exactly` on status code (502). Body string is informational and arguably operator-only; preserve approximately.

#### Constraint 8: Spooled segments (Trino v441+)
- **Why:** Trino v441+ added a spooled-segment protocol where large result chunks are stored externally (e.g. S3) and the response envelope returns *references* to those chunks. The client fetches the chunks directly from the spool storage, bypassing the gateway. From the gateway's perspective this is mostly invisible — the JSON envelope changes shape but the gateway treats it as opaque body.
- **How Java does it:** the gateway has no special handling for spooled segments today; it forwards the envelope as-is. The client follows the spool URL directly.
- **Required:** `replicate-intent`. The Go port should not interfere with spooled-segment URLs in response bodies. If gateway-rewriting of `nextUri` is ever extended to rewrite *all* URLs in responses, spool URLs must be excluded. Today neither implementation does that — confirm with `@trino-expert` whether any operator runs the gateway in a mode that would require it.

### Constraints the gateway does NOT have to satisfy (despite myth)

- **Per-query authn re-validation:** the coordinator does its own authn on every request. The gateway can be auth-blind for the data path.
- **HTTP/2 termination:** Trino's protocol is plain HTTP/1.1 (statement polling is sequential; multiplexing buys nothing). The gateway can stay HTTP/1.1-only without losing functionality.
- **Server-Sent Events / WebSocket:** none of the Trino protocol uses these. Pure request/response.

## Behavior vs. Implementation Artifact

### POST request body is fully buffered to extract queryId
- **Observed behavior:** for new POST `/v1/statement`, the entire response body is buffered (up to `proxyResponseConfiguration.responseSize`), parsed as JSON to extract `id`, then forwarded to the client (`ProxyRequestHandler.java:269-301`).
- **Source of behavior:** `protocol-required` for the *fact* of binding queryId→backend; `gateway-design-intent` for the *mechanism* of buffering the whole body to do it.
- **Go obligation:** `replicate-intent`. The Go port can use a streaming JSON decoder that emits the value of `"id"` as soon as it parses past it, then stream the rest of the body to the client. This is strictly better than buffering — same protocol semantics, lower memory. Document this as an intentional improvement.
- **Notes:** The Java approach has a hidden constraint — if the response is *larger* than `responseSize`, the body is truncated and the client receives a malformed JSON response. The Go port with streaming extraction avoids this cliff entirely.

### gateway cookie sticky routing is non-protocol
- **Observed behavior:** the gateway sets a signed cookie with `backend=<host>` so subsequent requests on related paths re-land on the same backend (`RoutingTargetHandler.getPreviousCluster`, `RoutingTargetHandler.java:153-172`).
- **Source of behavior:** `gateway-design-intent`. Trino's protocol does not require sticky routing — every request carries enough information to be served by any coordinator (in theory). Sticky routing is a gateway-specific affordance for cases where backend coordinators have local state (e.g. session-scoped resources, partial cancellation state).
- **Go obligation:** `replicate-intent`. Implement the same signed-cookie scheme; same cookie name prefix (`TG.`), same HMAC-SHA256 signature, same priority/TTL semantics. Operators with deployments dependent on sticky cookies must continue to work after the port.

### Response includes `trinoClusterHost` cookie when `includeClusterHostInResponse: true`
- **Observed behavior:** an operator-visible debug aid. When enabled, every new POST `/v1/statement` sets a `trinoClusterHost` cookie with the backend host (`ProxyRequestHandler.java:193-195`).
- **Source of behavior:** `ops-affordance`. Useful for debugging — operators can see which backend served a query.
- **Go obligation:** `replicate-intent`. Match the cookie name and value format.

## Implications for Go Rewrite

- **Library:** `net/http/httputil.ReverseProxy` is the right base. Its model maps cleanly: set `Director` to do path-untouched host-swap, set `ModifyResponse` to do queryId extraction for new POST `/v1/statement`. For everything else, the default behaviour is correct.
- **Interface:**
  - `BackendBinder interface { Bind(queryId string, backend string); Lookup(queryId string) (backend string, ok bool) }` — the queryId cache, populated by ModifyResponse, consulted by Director.
  - `RequestClassifier interface { ClassifyRequest(*http.Request) (kind RequestKind, queryId string) }` — classifies the incoming request as `NewStatement | StatementPoll | StatementCancel | KillQuery | ControlPlane`. The kind determines whether ModifyResponse should run the binder and whether Director should look up the cache.
  - `SQLAnalyzer interface { ExtractTargetQueryId(body []byte) (queryId string, ok bool) }` — used only for the kill_query CALL form.
- **Concurrency:**
  - The queryId cache must be goroutine-safe and tolerate read-after-write race (a slow POST response might land its binding after the first poll has already been dispatched — the Java code accepts this race because the poll will 404 once and the client will retry; preserve this semantics, do not add synchronization that blocks polls on POST completion).
  - Streaming JSON extraction in `ModifyResponse` runs on the response goroutine; backpressure to the client is preserved by Go's `io.Pipe` or by wrapping the response body in a TeeReader.

## Test Strategy Hooks

- See paired QA studies: [[trino-statement-protocol-what-the-gateway-proxies]] (java-qa), [[statement-protocol-invariants-the-gateway-must-preserve]] (java-qa).
- Protocol-level test concerns:
  - **Differential against Java gateway:** the highest-value tests. Record a corpus of representative client interactions (Trino CLI, JDBC, Python client) against the Java gateway, then replay against the Go gateway. Assert: same response status codes, same response headers (modulo `Server` / `Date`), same response bodies, same backend selection for each queryId.
  - **Statement lifecycle:** for each of {happy completion, error, client-cancel, gateway-timeout, backend-hang, backend-down-during-poll}, a test verifying the gateway behaves identically.
  - **Session header round-trip:** assert every `X-Trino-*` header in the request reaches the backend unchanged; every `X-Trino-Set-*`/`X-Trino-Clear-*` header in the backend response reaches the client unchanged.
  - **kill_query routing:** test that `CALL system.runtime.kill_query('<id>')` lands on the same backend the original query lives on, not a fresh backend.
  - **Prepared statement zstd:** test that a request with `X-Trino-Prepared-Statement: $zstd:...` is forwarded with the header intact and is also correctly decoded for rules evaluation.
- **Non-determinism risks:**
  - The race between POST response landing and the first poll arriving — test must deliberately exercise the timing.
  - Spooled-segment URLs in response bodies should not be touched; test with a backend that returns segment URLs to verify the gateway forwards them verbatim.

## Open Questions

- @trino-expert: are there protocol features added in trino versions > 481 (the pinned version) that we should design the Go gateway to accommodate? Examples: HTTP/2 mandates, new header types, alternative result formats.
- @trino-expert: does any production operator rewrite spooled-segment URLs through the gateway, or do all clients reach the spool storage directly? Affects whether we need a "spool URL rewriting" feature.
- @java-analyst: please confirm the full list of `X-Trino-*` response headers (set/clear/added/role/transaction/etc.) so the Go test suite has a complete passthrough check. Cross-link to your session-state study when written.
- @qa-tech-lead: what's the right shape for the "differential against Java gateway" test harness? A recorded HAR file? A live side-by-side comparison rig? Affects how we structure phase-5+ tests.

## Cross-references

- [[../trino-gateway/architecture-overview.architect.md]] — where these constraints land in the data path
- [[../trino-gateway/rewrite-hotspots.md]] — SQL parser dependency that constraint 6 depends on
- [[../trino-gateway/concurrency-model.architect.md]] — the goroutine model that hosts the queryId cache race
- [[trino-statement-protocol-what-the-gateway-proxies]] — paired java-qa study, full protocol enumeration
- [[statement-protocol-invariants-the-gateway-must-preserve]] — paired java-qa study, invariant-by-invariant test specs
