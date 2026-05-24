---
title: Trino statement protocol — invariants the gateway must preserve
author: java-qa
role: Java QA
component: both
topics: [statement-protocol, session-state, proxy-core]
date: 2026-05-24
status: approved
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/proxy-request-lifecycle.java-qa.md
  - trino-gateway/routing-engine.java-qa.md
  - both/gateway-coordinator-nexturi-contract.md
  - trino/spooled-segments-and-redirects.md
---

# Trino statement protocol — invariants the gateway must preserve

## Summary

A wire-level QA spec of the Trino client/coordinator HTTP protocol invariants that the gateway must preserve to remain a transparent proxy. The single most important takeaway: the protocol is **stateful across multiple HTTP round-trips per query** (POST `/v1/statement` then GET `nextUri` repeatedly), and every round-trip carries a 40+-entry header vocabulary that the gateway must propagate bidirectionally; getting any of these wrong silently breaks specific client capabilities (session state, prepared statements, transactions, role propagation) rather than failing loudly.

## Key Findings

### The protocol in one paragraph

A Trino client submits a query as `POST /v1/statement` with the SQL as the request body and protocol headers (`X-Trino-User`, `X-Trino-Catalog`, …) on the request. The coordinator responds 200 with a `QueryResults` JSON object containing a query `id`, an `infoUri`, optional `data` (first result page), optional `columns`, and optional `nextUri` (next page to poll). The client polls `GET <nextUri>` (or `DELETE` to cancel) repeatedly, each response containing fresh data and a fresh `nextUri`, until `nextUri` is absent (terminal). All polls go through the same `id`, must reach the same coordinator, and may emit response headers that mutate client-side state (set/clear session, add/remove prepared statements, started/cleared transaction id, set role/path/catalog/schema, set/reset authorisation user). Source: `trino/client/trino-client/src/main/java/io/trino/client/QueryResults.java:33-75`, `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:61-96`.

### Request-side protocol header vocabulary (gateway must propagate)

Header names are `X-<ProtocolName>-<Header>` where `<ProtocolName>` defaults to `"Trino"` (legacy `Presto` also supported via `detectProtocol`). Reference: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:61-96, 382-399`.

| Header (canonical form) | Carries |
|---|---|
| `X-Trino-User` | request user; required for most operations |
| `X-Trino-Original-User` | user before impersonation |
| `X-Trino-Original-Roles` | roles before impersonation |
| `X-Trino-Source` | client identifier (e.g. `airflow`, `trino-cli`, `tableau`) |
| `X-Trino-Catalog` | default catalog for unqualified table refs |
| `X-Trino-Schema` | default schema for unqualified table refs |
| `X-Trino-Path` | catalog/schema search path |
| `X-Trino-Time-Zone` | session time zone |
| `X-Trino-Language` | session locale |
| `X-Trino-Trace-Token` | client-supplied trace id |
| `X-Trino-Session` | session properties (`key=value` lists, multi-valued) |
| `X-Trino-Role` | role assertions |
| `X-Trino-Prepared-Statement` | named prepared statements (`name=encoded-sql`, multi-valued) |
| `X-Trino-Transaction-Id` | transaction membership |
| `X-Trino-Client-Info` | freeform client metadata |
| `X-Trino-Client-Tags` | comma-separated tags (consumed by gateway routing rules too) |
| `X-Trino-Client-Capabilities` | feature flags advertised by client |
| `X-Trino-Resource-Estimate` | resource hints (cpu, memory, time) |
| `X-Trino-Extra-Credential` | extra credentials (per-catalog, etc.) |
| `X-Trino-Query-Data-Encoding` | requested encoding for response data |

The gateway's `ProxyRequestHandler.setupRequestHeaders` forwards all headers except its skip list (`Accept-Encoding`, `Host`). It does NOT specially handle any of these — they are forwarded by virtue of being passed through. That is the correct behaviour: the gateway is protocol-agnostic at the header level. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:82-84, 316-351`.

### Response-side protocol header vocabulary (gateway must propagate)

These headers carry **state mutations the client must apply** before its next request. Forgetting any of them silently corrupts session state. Reference: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:83-96`.

| Header | Meaning |
|---|---|
| `X-Trino-Set-Catalog` | client must replace its current catalog |
| `X-Trino-Set-Schema` | client must replace its current schema |
| `X-Trino-Set-Path` | client must replace its current path |
| `X-Trino-Set-Session` | client must add/replace a session property (multi-valued) |
| `X-Trino-Clear-Session` | client must clear a session property (multi-valued) |
| `X-Trino-Set-Role` | client must apply a role change |
| `X-Trino-Set-Original-Roles` | client must apply original-role changes |
| `X-Trino-Query-Data-Encoding` | the encoding actually used for response data |
| `X-Trino-Added-Prepare` | new prepared statement added; client must remember it |
| `X-Trino-Deallocated-Prepare` | prepared statement removed; client must forget it |
| `X-Trino-Started-Transaction-Id` | a transaction was started; client must include this id in subsequent requests |
| `X-Trino-Clear-Transaction-Id` | transaction ended; client must stop sending the id |
| `X-Trino-Set-Authorization-User` | impersonation was set; client must apply |
| `X-Trino-Reset-Authorization-User` | impersonation was cleared; client must apply |

The gateway's `ProxyRequestHandler.buildResponse` iterates `response.headers()` and copies every (header, value) pair to the outgoing `Response.ResponseBuilder` unmodified. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:231-237`. The `ProxyResponseHandler` preserves multi-valued headers (`ListMultimap<HeaderName, String>`). `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:47-55`. That's the correct shape, but the multi-valued semantics MUST be tested — a Go rewrite that collapses to a single value silently corrupts session and prepared-statement state.

### The `QueryResults` JSON envelope (gateway must NOT alter)

Defined at `trino/client/trino-client/src/main/java/io/trino/client/QueryResults.java:33-188`. Field-by-field:

- `id` (String, required, never null) — the query id; canonical example shape `20200416_160256_03078_6b4yt` (date_time_sequence_nodeShort). The gateway extracts this from the JSON body via `ProxyRequestHandler.recordBackendForQueryId:282-285` to build its query-id ↔ backend cache. Field MUST NOT be renamed or removed by the gateway.
- `infoUri` (URI, required, never null) — link to the coordinator's `/ui/api/query/<id>` info endpoint. Built by the coordinator via the same `ExternalUriInfo.baseUriBuilder()` path as `nextUri` (see `[[gateway-coordinator-nexturi-contract.md]]`), so when the cross-system forwarded-header contract is correctly configured, `infoUri` ALSO points at the gateway — not a leak, same mechanism.
- `partialCancelUri` (URI, nullable) — link to cancel a partially-completed query. Same `baseUriBuilder()` source as `infoUri` and `nextUri`, same answer.
- `nextUri` (URI, nullable) — next page to poll. **THIS IS THE CRITICAL ONE.** If `nextUri` points at the backend host (not the gateway), the client follows it and bypasses the gateway. The mechanism (resolved): Trino's coordinator builds the URI from the request's JAX-RS base URI, which Airlift populates from `X-Forwarded-Host` / `-Proto` / `-Port` only when `http-server.process-forwarded=true`. The gateway forwards those headers when `routing.forwardedHeadersEnabled=true`. **Both flags, on independent systems.** Full source-citation chain: `[[gateway-coordinator-nexturi-contract.md]]`. The `TestGatewayHaSingleBackend.testRequestDelivery` test (which asserts the client-supplied `test.host.com` reaches the response body) only passes because both flags are on in the test environment.
- `columns` (List<Column>, nullable) — must be present on first page that returns data; cannot transition from non-null to a different shape.
- `data` (QueryData, nullable; serialized as `JsonInclude.NON_EMPTY` so a `null`/empty `data` is OMITTED from JSON entirely — see comment at `QueryResults.java:123`). **ODBC clients rely on `data` being absent (not `null`) when empty — binding contract on any re-serialisation step in the Go rewrite. See "`data` field omission when empty (ODBC compatibility)" in Behavior vs. Implementation Artifact below for the full obligation.** A Go rewrite that re-serializes the response (e.g. for body-rewriting purposes) must preserve this omission semantics.
- `stats` (StatementStats, required, never null).
- `error` (QueryError, nullable) — present on failure.
- `warnings` (List<Warning>, required, defaults to empty list).
- `updateType` (String, nullable) — present for DML.
- `updateCount` (OptionalLong, required) — present for DML.

### Status-code semantics

- **POST `/v1/statement`** — `200` with `QueryResults` body on accept; `401`/`403` on auth failure; `400` on malformed (rare; usually returned later via `QueryResults.error`). The body, not the status, carries query-level errors.
- **GET `<nextUri>`** — `200` with `QueryResults`; `404` if the query is unknown (long after completion); `503` while results are still being prepared (the client SHOULD retry with backoff — see `StatementClientV1`).
- **DELETE `<nextUri>` (or `partialCancelUri`)** — `200` or `204` on accepted cancellation. Test asserts `between(200, 204)` at `trino-gateway/gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java:255`.
- **HEAD `/v1/query/<id>`** — `200` if the query is known, `404` otherwise. Used internally by the gateway's `searchAllBackendForQuery` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/router/BaseRoutingManager.java:199-227`).
- **GET `/v1/info`** — coordinator info (`{"starting": true|false, ...}`); the gateway probes this for health (`{"starting": false}` means HEALTHY in `ClusterStats`).

The gateway forwards these status codes unchanged (`ProxyResponseHandler` returns whatever the backend returned), with two exceptions:
- `502 BAD_GATEWAY` when the gateway's async timeout fires. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:239-247`.
- `502 BAD_GATEWAY` when the backend HTTP client throws (`handleProxyException`). `:254-267`.

### Polling lifecycle (the "stateful" part)

```
Client                     Gateway                    Backend
  |                          |                          |
  | POST /v1/statement       |                          |
  |  body: SQL               |                          |
  |  headers: X-Trino-*      |                          |
  | -----------------------> |                          |
  |                          | Route → backend B        |
  |                          | POST /v1/statement       |
  |                          | ------------------------>|
  |                          |                          |
  |                          | 200 { id, nextUri, ... } |
  |                          | <------------------------|
  |                          | Cache id → B             |
  |                          | Write history row        |
  |                          | Set trinoClusterHost     |
  | 200 { id, nextUri, ... } |   cookie (optional)      |
  | <----------------------- |                          |
  |                          |                          |
  | GET <nextUri>            |                          |
  | -----------------------> |                          |
  |                          | Lookup id → B            |
  |                          | GET <nextUri>            |
  |                          | ------------------------>|
  |                          | 200 { id, nextUri, ... } |
  |                          | <------------------------|
  | 200 { id, nextUri, ... } |                          |
  | <----------------------- |                          |
  |                          |                          |
  | ... repeat until nextUri is absent ...
  |                          |                          |
  | DELETE <nextUri>         |                          |
  |  (or omit; coordinator GCs)                         |
  | -----------------------> |                          |
  |                          | Lookup id → B            |
  |                          | DELETE <nextUri>         |
  |                          | ------------------------>|
  |                          | 200 / 204                |
  |                          | <------------------------|
  | 200 / 204                |                          |
  | <----------------------- |                          |
```

**The invariant:** all rows after the first MUST reach the same backend B that the first request was routed to. If they don't, the coordinator on the wrong backend returns 404 and the client crashes the query. The gateway accomplishes this via the query-id ↔ backend cache (`RoutingManager.findBackendForQueryId`). Cache loss → `BaseRoutingManager.findBackendForUnknownQueryId` falls back to query history table or backend-probing (`[[../trino-gateway/proxy-request-lifecycle.java-qa.md]]` Seam 7).

### Spooling and chunked response data (Trino 451+)

Newer Trino versions support **spooled query data** (e.g. `Query-Data-Encoding: json+zstd` or `json+lz4`), where the response body's `data` field is a list of URIs pointing at segments stored externally (S3, etc.). The client GETs each segment separately. Full breakdown of the four retrieval modes (`STORAGE`, `COORDINATOR_STORAGE_REDIRECT`, `COORDINATOR_PROXY`, `WORKER_PROXY`) is in `[[../trino/spooled-segments-and-redirects.md]]`. From the gateway's perspective:

- Segment URIs in the response body point at the spool location, not the backend or the gateway.
- The client may or may not bypass the gateway for the spool GETs depending on the spool URL scheme and retrieval mode.
- The gateway's body buffering at `ProxyResponseHandler.handle:50` does not need to be aware of spooled data — it just forwards the JSON envelope.

Resolved by @trino-expert: spooled-segment routing is **out of scope for v1** per the architect's working assumption. The current Java gateway "works" with spooled data **only because** of two load-bearing properties the Go rewrite MUST preserve: (a) the airlift HTTP client is configured `setFollowRedirects(false)` (`ProxyRequestHandler.java:184`), so 303 redirects on spool fetches pass through to the client untouched; (b) response bodies pass through unmodified, so segment-handle URIs embedded in the `QueryResults` JSON reach the client untouched. **The Go gateway MUST replicate both: do NOT follow redirects in the proxy HTTP client, and do NOT rewrite `Location` headers on 3xx responses.** The cross-cluster spooled-segment routing question (no queryId in spool paths, so the gateway can't sticky-route them) is open but does not affect single-cluster correctness — same answer as before for single-cluster deployments.

### Authentication boundary

Auth is largely *not* protocol-shaped at the wire level — it's an HTTP `Authorization` header plus a body of cookies (`OAuth2GatewayCookie`, `OidcCookie`, etc.). The gateway has its own auth layer (`LbFilter`, `LbAuthenticator`, `LbAuthorizer`) that can run *in front of* the backend. Mistakes here:

- Forgetting to forward `Authorization` to the backend → backend rejects every request. The gateway DOES forward by default (no skip-list entry for `Authorization`); good.
- Stripping `WWW-Authenticate` from backend responses → clients can't 401-handshake. The gateway DOES forward (no special response filtering); good.
- Cookie collisions between gateway-issued and Trino-issued cookies → the gateway namespaces its cookies with `GatewayCookie.PREFIX` (currently `trinoUI`-style names; check the constant) to avoid collisions.

### Headers and bodies that are NOT protocol but are STILL important

- `Content-Type` (request: typically `application/json; charset=utf-8` for statement POST, with body containing SQL as plain text — yes, JSON content type with non-JSON body; that's by Trino convention). The gateway forwards this; tests use it (`TestGatewayHaMultipleBackend.java:65, 158`).
- `Content-Encoding` (response: may be `gzip`). The gateway's outgoing `setupRequestHeaders` strips `Accept-Encoding` from incoming requests (`ProxyRequestHandler.java:82-84`) — so the backend will not gzip its response to the gateway. The gateway then forwards uncompressed to the client. **This is a real behaviour difference** vs. backend-direct: gateway-proxied responses are NOT gzipped even if the client requested gzip. Confirmed deliberate per @trino-expert: the strip exists because the gateway buffers POST-to-statement responses to extract the queryId via Jackson (`ProxyRequestHandler.java:269-301`), and compressed bodies would force gateway-side gunzip. The decision is `defensive-historical`, and only relevant on the POST response path — `nextUri` GET poll responses are typically too small for gzip to matter.
- `Set-Cookie` (response: from backend OR gateway). Multi-valued.
- `Via` (request: gateway adds `<protocol> TrinoGateway`).

## Behavior vs. Implementation Artifact

### Forwarding the entire request-header set verbatim

- **Observed behavior:** the gateway propagates all incoming headers to the backend except the skip list (`Accept-Encoding`, `Host`) and (when disabled) the `X-Forwarded-*` family.
- **Source of behavior:** `protocol-required`. The Trino protocol is unbounded in its forward-evolution of headers (new headers are added as features land); a gateway that only forwards a known set will silently break new clients.
- **Go obligation:** `replicate-exactly`. The Go rewrite must default-allow, not default-deny. Skip list is acceptable; opt-in allow-list is not.
- **Notes:** the skip of `Accept-Encoding` (to disable backend gzip) is confirmed intentional and `defensive-historical` per @trino-expert — the gateway buffers POST-to-statement responses to extract the queryId via Jackson, so a compressed backend response would force gateway-side gunzip. Only the POST response path is affected; GET-poll bodies are typically too small for gzip to matter. If the Go rewrite adopts a streaming peek-and-replay reader (e.g. `bufio.Reader.Peek` on the first chunk to extract `id`), gzip can pass through transparently and the strip becomes optional — meaningful TTFB win on large response bodies.

### Forwarding the entire response-header set verbatim

- **Observed behavior:** `buildResponse` iterates `response.headers()` (a `ListMultimap`) and copies each entry. `ProxyRequestHandler.java:233-237`.
- **Source of behavior:** `protocol-required`. The 14 response-side `X-Trino-*` headers each mutate client state; losing any silently corrupts state.
- **Go obligation:** `replicate-exactly`. Test: each of the 14 response headers, when present on the backend response, appears on the gateway response with the same value(s). Critically: multi-valued headers (`X-Trino-Set-Session`, `X-Trino-Added-Prepare`) must remain multi-valued in the gateway response.

### Buffering the response body (single read into a String)

- **Observed behavior:** `ProxyResponseHandler.handle` reads up to `responseSize` bytes into a single String and packages as `ProxyResponse`. `trino-gateway/gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyResponseHandler.java:47-55`.
- **Source of behavior:** `defensive-historical` per @trino-expert — buffering exists to enable query-id extraction from the body via Jackson, and the `Accept-Encoding` strip is its companion (see the Skipping `Host`/`Accept-Encoding` block).
- **Go obligation:** `defer-to-expert`. Buffering bounds memory headroom and TTFB; if any Trino endpoint returns large bodies, the cap silently truncates. Most `nextUri` poll responses are small (KBs), so buffering is fine for the hot path. The risk is `/ui` static assets and spooled-segment passthrough.
- **Notes:** If Go decides to stream, it MUST still extract the query id from the first response chunk and re-encode/replay (peek-and-replay reader, e.g. `bufio.Reader.Peek` on the first chunk). Streaming additionally unlocks the `Accept-Encoding`-passthrough optimisation since the gateway no longer needs to gunzip the body to extract the id from the first JSON chunk.

### Not following redirects on the outbound proxy HTTP client

- **Observed behavior:** the proxy HTTP client is constructed with `setFollowRedirects(false)` (`ProxyRequestHandler.java:184`). 3xx responses from the backend pass through to the client untouched, with the backend's `Location` header intact.
- **Source of behavior:** `protocol-required` per @trino-expert. The spooled-data segment retrieval modes (`STORAGE`, `COORDINATOR_STORAGE_REDIRECT`, `COORDINATOR_PROXY`, `WORKER_PROXY`) rely on the client following the redirect itself; if the gateway followed the redirect, it would unwrap the indirection and break the segment-handle contract.
- **Go obligation:** `replicate-exactly`. The Go HTTP client used for backend proxy calls MUST disable redirect-following (`http.Client{CheckRedirect: func(...) error { return http.ErrUseLastResponse }}` is the standard idiom). The Go rewrite MUST NOT rewrite `Location` headers on 3xx responses regardless of streaming/buffering decisions on the body.
- **Notes:** this property is currently load-bearing for spooled-data passthrough even though spooled-data routing is out of scope for v1. A regression on this (a default `http.Client` that follows redirects) silently breaks any deployment that enables Trino spooling, while passing all current Java tests (which don't exercise spooled responses).

### Not rewriting `infoUri` / `partialCancelUri` / `nextUri` in the response body

- **Observed behavior:** the gateway does not parse the JSON response body to rewrite URIs in `QueryResults`. The Trino coordinator itself builds `nextUri`, `infoUri`, and `partialCancelUri` via `ExternalUriInfo.baseUriBuilder()` (used by `LocalCoordinatorLocation.getUri()` and the same call site for all three URIs), which Airlift's embedded HTTP server populates from `X-Forwarded-Host` / `X-Forwarded-Proto` / `X-Forwarded-Port` **only when the coordinator is started with `http-server.process-forwarded=true`**. On the gateway side, those headers are added when `routing.forwardedHeadersEnabled=true` (`ProxyRequestHandler.java:108, 328-330, 353-362`).
- **Source of behavior:** `protocol-required` (cross-system contract). Confirmed by @trino-expert; full source-citation chain in `[[../both/gateway-coordinator-nexturi-contract.md]]`.
- **Go obligation:** `replicate-exactly`. The Go rewrite must (a) forward the `X-Forwarded-Host` / `-Proto` / `-Port` triple to the backend whenever forwarded-headers are enabled, deriving them from the gateway's externally-visible address as the Java code does; (b) document that the coordinator-side `http-server.process-forwarded=true` is a load-bearing operator requirement and surface it in any gateway documentation. There is no Go-side workaround if the coordinator config is missing.
- **Notes:** the cross-system contract fails OPEN — when either flag is missing, queries appear to hang (clients poll the backend host directly, the gateway never sees the polls, the request-id binding has nothing to do, and the client times out) rather than producing a loud error. The single end-to-end test against a real `trinodb/trino` container at line 250 of this study is the only thing that catches misconfig; mock backends will not. **All three URIs (`nextUri`, `infoUri`, `partialCancelUri`) share this mechanism** — see the resolved Q3 in Open Questions below. Spooled-segment URIs do NOT share this mechanism and are governed separately by `[[../trino/spooled-segments-and-redirects.md]]`.

### Skipping `Host` and `Accept-Encoding` headers

- **Observed behavior:** these two are dropped on forward. `ProxyRequestHandler.java:82-84`.
- **Source of behavior:** `Host` is `jvm-artifact`/protocol — the HTTP client sets its own `Host` header per the destination URI; the original is meaningless. `Accept-Encoding` is `defensive-historical` to prevent gzipped backend responses (which would complicate body reading).
- **Go obligation:** `replicate-intent`. The Go HTTP client (`net/http`) will likewise set its own `Host` header. For `Accept-Encoding`, the Go decision depends on whether the body is buffered or streamed; if streamed, the gateway can pass gzip through transparently (modulo query-id extraction, which then needs a streaming JSON peek).

### Status-code translation on gateway-internal failures

- **Observed behavior:** any internal failure becomes `502 BAD_GATEWAY`. `ProxyRequestHandler.java:243, 261-267`.
- **Source of behavior:** `gateway-design-intent`. The gateway is a 7-layer proxy; 502 is the canonical "upstream failed" code.
- **Go obligation:** `replicate-exactly`. Test: configure a backend that drops connections; gateway returns 502. Test: configure async timeout < backend response time; gateway returns 502 with body starting with `"Request to remote Trino server timed out"`.

### Caching by query id (sticky routing)

- **Observed behavior:** once a query id is bound to a backend, all subsequent requests carrying that id reach the same backend. `RoutingManager.setBackendForQueryId` / `findBackendForQueryId`.
- **Source of behavior:** `protocol-required`. The coordinator that started the query is the only one that knows about it.
- **Go obligation:** `replicate-exactly`. Test: POST `/v1/statement` to two-backend gateway; record which backend got the request; GET the returned `nextUri`; assert the same backend got the GET.

### `data` field omission when empty (ODBC compatibility)

- **Observed behavior:** `QueryResults.getRawData()` is annotated `@JsonInclude(JsonInclude.Include.NON_EMPTY)` so empty/null `data` is omitted from JSON entirely. `QueryResults.java:121-128`.
- **Source of behavior:** `defensive-historical` for ODBC compatibility (comment in source).
- **Go obligation:** the gateway does not currently re-serialize the response, so this is preserved by virtue of not touching the body. **If Go ever re-serializes** (for rewriting purposes), the JSON marshaller must honour the same omission rule — Go's `encoding/json` requires `omitempty` plus a custom marshaller for proper "absent vs null vs empty list" distinction.

### Multi-valued protocol headers

- **Observed behavior:** `X-Trino-Session`, `X-Trino-Prepared-Statement`, `X-Trino-Set-Session`, `X-Trino-Clear-Session`, `X-Trino-Added-Prepare`, `X-Trino-Deallocated-Prepare`, `X-Trino-Role`, `X-Trino-Resource-Estimate`, `X-Trino-Extra-Credential`, `X-Trino-Set-Role` are all multi-valued in practice. Clients send (and coordinators emit) one header instance per session property / prepared statement / role.
- **Source of behavior:** `protocol-required`.
- **Go obligation:** `replicate-exactly`. Test: send a request with three `X-Trino-Session: a=1`, `X-Trino-Session: b=2`, `X-Trino-Session: c=3` headers; assert the backend receives three separate header values, not a single joined string. Same for response-side: backend responds with three `X-Trino-Added-Prepare` headers; client receives three.

### Protocol-name auto-detection (Trino vs Presto)

- **Observed behavior:** `ProtocolHeaders.detectProtocol` lets a deployment support `X-Presto-*` headers as an alias for `X-Trino-*` when an alternate header name is configured. `ProtocolHeaders.java:382-399`.
- **Source of behavior:** `defensive-historical` — Trino was forked from Presto, and legacy clients still send `X-Presto-*`.
- **Go obligation:** `defer-to-expert`. Is Presto compatibility in scope for the Go rewrite? If yes, the gateway must propagate `X-Presto-*` headers identically. If no, document the drop.

## Implications for Go Rewrite

- The Go gateway MUST default-forward all request headers (skip list, not allow list) and all response headers (verbatim, multi-valued preserved). Header policy is the single highest-risk surface; a regression here breaks features silently rather than loudly.
- The query-id binding is the *primary* sticky-routing contract. The Go rewrite must extract `id` from the first 200 response on a POST to a statement path and persist (cache + history) before returning to the client; failure to persist means the next poll lands on a random backend.
- The `nextUri` / `infoUri` / `partialCancelUri` host derivation depends on a cross-system contract: the gateway must forward `X-Forwarded-Host` / `-Proto` / `-Port` (gated by `routing.forwardedHeadersEnabled=true`), AND the coordinator must be started with `http-server.process-forwarded=true`. Both flags, on independent systems. The contract fails OPEN (queries appear to hang) rather than loud. Two consequences: (a) an end-to-end test against a real `trinodb/trino` container with both flags configured is non-negotiable — mock backends will hide the bug; (b) the coordinator-side flag is a load-bearing operator requirement and MUST be surfaced in Go gateway operator documentation. See `[[gateway-coordinator-nexturi-contract.md]]`.
- **The Go proxy HTTP client MUST disable redirect-following and MUST NOT rewrite `Location` headers on 3xx responses.** This property is load-bearing for Trino's spooled-data segment retrieval modes even though spooled-data routing is out of v1 scope; a default `http.Client` that follows redirects silently breaks spooling-enabled deployments while passing all current Java tests. Use `http.Client{CheckRedirect: func(...) error { return http.ErrUseLastResponse }}`.
- Multi-valued headers must be tested explicitly. Build a fixture corpus: request with N session headers + M prepared-statement headers; assert backend receives all of them; assert response with X added-prepare + Y set-session headers reaches the client unaltered.
- Status code preservation MUST be exhaustive: 200, 204, 400, 401, 403, 404, 502, 503 each tested via mock backend that returns that code; assert the gateway returns the same.
- Body buffering decisions interact with header policy: if streaming, `Accept-Encoding` can pass through; if buffering, it must continue to be stripped to avoid handling gzip on the gateway. Make this decision before locking the test suite.
- Spooled query data is currently out of test scope and likely out of architectural scope for the first cut. Flag explicitly so it isn't accidentally tested or accidentally broken.
- `data: null` vs absent-`data` JSON omission is a contract for ODBC clients. If the Go rewrite ever re-serialises the body, this must be preserved.

## Test Strategy Hooks

- **Test level:** e2e (real `trinodb/trino` container) for `nextUri` rewrite, polling lifecycle end-to-end, real protocol header round-trips; integration (mock backend) for status-code preservation, header propagation, query-id binding, multi-valued header preservation, body-size cap; unit for query-id extraction from URL paths (already pinned by `TestQueryIdCachingProxyHandler`).
- **Fixtures required:** real `trinodb/trino` container with at least one configured catalog (`memory` suffices); mock backend that can return canned `QueryResults` JSON with specified `id`, `nextUri`, `data`; mock backend with configurable status codes; multi-valued header request and response fixtures; spooled-data response fixture (for future use).
- **Observable signals:** every response-side `X-Trino-*` header by name; HTTP status codes verbatim; response body's `id` field (extracted by gateway and used to bind sticky routing — assertable via subsequent poll landing on same backend); response body's `nextUri` host:port (assertable as gateway address); `Set-Cookie` for `trinoClusterHost`; the protocol-headers list (use `ProtocolHeaders.Headers` enum as the authoritative checklist).
- **Non-determinism risks:** real Trino startup time (~10s+, container image pull adds more) — use `testcontainers` wait strategies, not sleeps; query polling intervals vary based on Trino state machine — assert on "eventually nextUri absent" not specific poll counts; multi-valued header ordering is not guaranteed (test on set-equality, not list-equality).

## Open Questions

- @trino-expert: how exactly does Trino determine the host portion of `nextUri` it emits? **Resolved by @trino-expert: `ExternalUriInfo.baseUriBuilder()` reads the request's JAX-RS base URI; Airlift populates it from `X-Forwarded-Host` / `-Proto` / `-Port` only when `http-server.process-forwarded=true` on the coordinator. Gateway forwards those headers when `routing.forwardedHeadersEnabled=true`. Both flags required on independent systems. Full source-citation chain: `[[gateway-coordinator-nexturi-contract.md]]`.** Behavior block 4 above has been upgraded from `defer-to-expert` to `replicate-exactly` accordingly.
- @trino-expert: is stripping `Accept-Encoding` from the forward request a deliberate gateway choice, or an oversight? **Resolved by @trino-expert: deliberate, `defensive-historical`. The gateway buffers POST-to-statement responses to extract the queryId via Jackson; compressed bodies would force gateway-side gunzip. Only the POST response path is affected; if Go streams-and-peeks instead of buffering, gzip can pass through transparently.**
- @trino-expert: is `infoUri` (and `partialCancelUri`) leaking the backend host a problem in practice? **Resolved by @trino-expert: not a leak. Both URIs are built via the same `ExternalUriInfo.baseUriBuilder()` path as `nextUri`, so when the cross-system forwarded-header contract is correctly configured, all three URIs point at the gateway.**
- @trino-expert: are spooled query data segments in scope for gateway-side rewriting? **Resolved by @trino-expert: out of scope for v1. Two load-bearing properties of the current Java gateway MUST be replicated by Go: (a) the proxy HTTP client does NOT follow redirects (`setFollowRedirects(false)`), and (b) response bodies pass through unmodified. Do NOT rewrite `Location` headers on 3xx responses. Multi-cluster spool routing remains open but does not affect single-cluster correctness. Full mode breakdown: `[[../trino/spooled-segments-and-redirects.md]]`.**
- @architect: is `X-Presto-*` legacy compatibility in scope for the Go rewrite? Touches the `detectProtocol` machinery in `ProtocolHeaders`.
- @qa-tech-lead: how exhaustively should we test the 14 response-side state-mutation headers? One test per header (14 tests) or table-driven across all? Both are reasonable; I'd lean table-driven for maintenance, but if the rule is "one test per behaviour" we should do 14.

## Cross-references

- `[[../trino-gateway/proxy-request-lifecycle.java-qa.md]]` — Seam 4 (header transformation) and Seam 8 (response body / `nextUri` derivation).
- `[[../trino-gateway/routing-engine.java-qa.md]]` — `X-Trino-Routing-Group`, `X-Trino-Source`, `X-Trino-Client-Tags` consumed by routing rules.
- `[[../trino-gateway/test-gaps-and-risks.java-qa.md]]` — multi-valued header round-trips, spooled data, gzip behaviour are all currently untested.
