---
title: Trino client statement protocol — wire-level overview
author: trino-expert
role: Trino & Trino-Gateway Expert
component: trino
topics: [statement-protocol, session-state, proxy-core]
date: 2026-05-24
status: peer-reviewed
reviewer: java-analyst
risk: high
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino/protocol-header-prefix-configurable.md
  - trino/spooled-segments-and-redirects.md
  - both/gateway-coordinator-nexturi-contract.md
---

# Trino client statement protocol — wire-level overview

## Summary

A Trino client query is a long-lived HTTP conversation, not a single request. The client `POST`s SQL to `/v1/statement`; the coordinator returns a `QueryResults` JSON document containing a `nextUri` that the client `GET`s repeatedly until `nextUri` is absent. The host inside successive `nextUri` values shifts during the conversation — from "the host the POST was sent to" to "the coordinator's externally-advertised hostname" — and that single behavior is the central thing the gateway has to coexist with.

## Key Findings

- **Endpoints in scope for the gateway:**
  - `POST /v1/statement` — submit SQL. Body is the raw SQL string (text, not JSON).
    Source: `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:170-184`
  - `GET /v1/statement/queued/{queryId}/{slug}/{token}` — poll while query is in dispatch queue.
    Source: `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:206-221`
  - `DELETE /v1/statement/queued/{queryId}/{slug}/{token}` — cancel while queued.
    Source: `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:234-246`
  - `HEAD /v1/statement` — connection validation probe, returns 200 with empty body.
    Source: `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:161-167`
  - `GET /v1/statement/executing/{queryId}/{slug}/{token}` — poll once dispatched.
    Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/ExecutingStatementResource.java` (entry points around line 165, 175, 188)
  - `DELETE /v1/statement/executing/{queryId}/{slug}/{token}` — cancel while executing.
  - Additional management/UI endpoints (`/v1/query/{id}`, `/ui/...`) the gateway also proxies but which are not part of the client-statement contract.

- **Per-request response is a `QueryResults` JSON object** whose load-bearing field for the gateway is `id` (the queryId, format `\d+_\d+_\d+_\w+`, e.g. `20260524_120000_00001_abcde`) and `nextUri`. Other fields (`columns`, `data`, `stats`, `error`, `updateType`) are payload the gateway forwards but does not interpret.
  Source: doc `trino/docs/src/main/sphinx/develop/client-protocol.md:62-96`. Class definition: `trino/client/trino-client/src/main/java/io/trino/client/QueryResults.java`.

- **Dispatch-time host pivot.** The `nextUri` returned from the initial POST initially points back to the same host (`/v1/statement/queued/...`). Once the dispatcher schedules the query on a coordinator, the next polled response returns a `nextUri` whose **host is determined by `CoordinatorLocation.getUri(ExternalUriInfo)`** — i.e., the coordinator's view of its externally-reachable URL.
  Source: `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:445-465`, `trino/core/trino-main/src/main/java/io/trino/dispatcher/LocalCoordinatorLocation.java:22-26`, `trino/core/trino-main/src/main/java/io/trino/server/ExternalUriInfo.java:40-83`.

- **Slug + token is a per-query anti-CSRF / anti-replay mechanism.** The coordinator generates a 16-byte random key on query registration and HMAC-SHA1s `(context, token)` into the URL slug. Each GET advances the token monotonically; the coordinator rejects out-of-sequence tokens with `410 Gone`, and stale slugs with `404 Not Found`.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/Slug.java:23-60`, `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:248-254,392-398`.

- **Retry semantics are baked into the protocol.** A client receiving 502/503/504 should retry after 50-100 ms; a 429 carries a `Retry-After` header. Any other non-200 means query processing failed. Trino itself never emits 502/503/504 — these come from front intermediaries.
  Source: `trino/docs/src/main/sphinx/develop/client-protocol.md:28-37`.

- **Heartbeat semantics.** Polling `nextUri` is the heartbeat. The coordinator records a heartbeat on each valid request; without one, the query is reaped after `query.client.timeout` (default ~5 min). Long stalls in the gateway therefore look identical to a dead client.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/ExecutingStatementResource.java:164-167` (`recordHeartbeat`).

- **Cancellation is by DELETE on the current `nextUri`-shaped path**, not on the queryId directly. The slug must match.
  Source: same as above, `cancelQuery`.

- **Query data encoding is negotiated via headers.** Modern clients can opt into spooled segments by sending `X-Trino-Query-Data-Encoding`; if accepted, the coordinator echoes it in `X-Trino-Query-Data-Encoding` response header.
  Source: `trino/client/trino-client/src/main/java/io/trino/client/ProtocolHeaders.java:30, 44, 82, 90, 180, 187`. See [[spooled-segments-and-redirects.md]] for the routing consequences.

## Behavior vs. Implementation Artifact

### Initial POST returns `/v1/statement/queued/...` URI on **same host**, not yet on the dispatched coordinator
- **Observed behavior:** The first POST is handled by the dispatcher, which has not yet selected a coordinator for execution. It returns a `nextUri` pointing to itself at `/v1/statement/queued/{queryId}/{slug}/{token}`. Only after dispatch does the subsequent poll start returning `/v1/statement/executing/...` URIs (which may be on a different node when the coordinator-worker topology grows).
- **Source of behavior:** `protocol-required`. Documented in `client-protocol.md`. Inherent to the queued/executing split.
- **Rationale:** Decouples query admission from query placement. Lets the dispatcher rate-limit, queue, and authorize before committing cluster resources.
- **Go obligation:** `replicate-intent`. The gateway is not the dispatcher and does not implement this split itself — it just proxies. But the gateway must recognize that the **host** in `nextUri` changes mid-conversation and must NOT rewrite `nextUri` in a way that pins the client to a single backend host. See [[both/gateway-coordinator-nexturi-contract.md]].
- **Notes:** This pivot is the reason the gateway's queryId→backend mapping must persist across requests (see `BaseRoutingManager.java:56,261` — `expireAfterAccess(30 MINUTES)`).

### `nextUri` host is built from request-side `ExternalUriInfo`, which honors `X-Forwarded-*`
- **Observed behavior:** `LocalCoordinatorLocation.getUri()` returns `externalUriInfo.baseUriBuilder()`, which is the request's base URI. Trino's HTTP server, when configured with `http-server.process-forwarded=true`, derives that base URI from `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Forwarded-Port`, and `X-Forwarded-Prefix`. Without that config, base URI is the literal backend host:port.
- **Source of behavior:** `protocol-required`. The protocol assumes `nextUri` is a URL the client can reach.
- **Rationale:** Allows the coordinator to be deployed behind reverse proxies without rewriting response bodies in the proxy.
- **Go obligation:** `replicate-exactly`. The Go gateway MUST send `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Forwarded-Port` when `forwardedHeadersEnabled` is true (current Java behavior — `ProxyRequestHandler.java:353-362`). It MUST NOT rewrite `nextUri` in the response body.
- **Notes:** This is a **silent dependency** on backend Trino config. If the operator sets up the gateway but forgets `http-server.process-forwarded=true` on the coordinator, clients will get `nextUri` values pointing to internal hostnames and fail to follow them. The Go rewrite should not "helpfully" rewrite `nextUri` in the body — see paired study [[both/gateway-coordinator-nexturi-contract.md]] for why this is load-bearing.

### Slug-and-token URLs are unguessable by design
- **Observed behavior:** Slug is HMAC-SHA1 of `(context, token)` keyed by 16 random bytes per query, plus a "y" version prefix. Each `nextUri` carries a monotonically incremented token. An attacker who knows the queryId still cannot construct a valid slug.
- **Source of behavior:** `protocol-required` for replay protection; `defensive-historical` for the "y" prefix (commit comment indicates this was added for troubleshooting purposes, anticipating a future v2 slug format).
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/Slug.java:49-54`.
- **Rationale:** Without the slug, GET `/v1/statement/queued/{queryId}/0/0` could be guessed and used to siphon another user's query results. The slug is the only authn signal on the polling URLs (which are `@ResourceSecurity(PUBLIC)`).
- **Go obligation:** `drop`. The gateway does NOT generate slugs — it forwards them. The Go rewrite has no slug logic to replicate. The relevant Go obligation is to never log slugs at INFO level (they are effectively per-query bearer tokens).
- **Notes:** Implications for observability — masking `nextUri` in logs is wise.

### HEAD `/v1/statement` as connection probe
- **Observed behavior:** `HEAD /v1/statement` returns 200 with no body and no query side-effect. Used by JDBC drivers and CLI to validate the connection without consuming query slots.
  Source: `trino/core/trino-main/src/main/java/io/trino/dispatcher/QueuedStatementResource.java:161-167`.
- **Source of behavior:** `gateway-design-intent` (Trino-side, but the gateway must preserve it). Not formally documented as part of the wire protocol but assumed by all production clients.
- **Rationale:** Client preflight check; especially used after authentication redirects to verify the credential is good.
- **Go obligation:** `replicate-exactly`. The Go gateway must route HEAD requests to a backend and return its response. `RouteToBackendResource.java:101-108` shows the Java gateway does support HEAD.
- **Notes:** Easy to miss in test suites that only exercise GET/POST/DELETE. Flag for QA.

### Heartbeat-by-polling: idle gateway looks dead
- **Observed behavior:** Every successful `nextUri` GET is treated as a client heartbeat. If polling stops for `query.client.timeout` (default 5 minutes), the coordinator marks the query abandoned and tears it down.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/ExecutingStatementResource.java:166`.
- **Source of behavior:** `protocol-required`.
- **Rationale:** Prevents leaked queries from clients that disappear mid-conversation.
- **Go obligation:** `replicate-exactly`. The gateway must not buffer / delay client polls beyond the configured async timeout. The Java gateway's `asyncTimeout` is the bound on how long a single GET can hang before the gateway gives up (`ProxyRequestHandler.java:108, 239-247`).
- **Notes:** A Go implementation that uses backpressure or queues GET requests could inadvertently cause queries to time out. The gateway must be near-zero-latency on the poll path.

## Implications for Go Rewrite

- The Go gateway must implement HEAD, GET, POST, DELETE, **and PUT** on the routed path. PUT is not part of the documented statement protocol but `RouteToBackendResource.java:90-99` accepts it — likely for the spooled `/v1/spooled/...` endpoints or future endpoints. Don't drop it.
- The `nextUri` field in the response body must be returned **byte-for-byte unmodified**. Do not parse-and-rewrite the JSON body. This is the single most important invariant.
- The queryId regex `\d+_\d+_\d+_\w+` (`ProxyUtils.java:51`) is the stable extraction pattern. Use the same pattern in Go.
- The Go gateway must support `forwardedHeadersEnabled=true` semantics: forward client IP/scheme/port via `X-Forwarded-*` headers so the coordinator can build correct `nextUri` values.
- Async timeout on the polling path is a tunable (Java default `30s` via `HaGatewayConfiguration.getRouting().getAsyncTimeout()`); the Go implementation should preserve a similar timeout knob.
- 502/503/504 from the gateway are expected, normal, and retried by clients. The gateway should NOT translate transient backend errors into 500 — that breaks client retry behavior.
- The Go gateway must not strip or rewrite `Authorization`, `X-Trino-*`, or `Cookie` headers on the request side (today: `ProxyRequestHandler.java:82-84,316-351` only strips `Accept-Encoding` and `Host`, plus `X-Forwarded-*` when forwarded-headers is disabled).

## Test Strategy Hooks

- **Test level:** differential (gateway-vs-Java-gateway against a shared mock backend) + integration (gateway-in-front-of-real-Trino).
- **Fixtures required:**
  - A mock Trino coordinator returning the queued→dispatched→executing pivot, including non-trivial `nextUri` host changes.
  - A real Trino testcontainer for integration.
  - A SQL of size > spool threshold to exercise the spooled-segment path.
- **Observable signals:**
  - QueryResults `nextUri` byte-equality between gateway-out and backend-in.
  - `queryId` extraction stability across all four URL shapes (queued, executing, partialCancel, query_id query-param).
  - HEAD returns 200 with empty body.
  - DELETE returns 204 with empty body (`QueuedStatementResource.java:245` returns `noContent()`).
  - Stat counter (`proxyHandlerStats.recordRequest()`) increments only for POST whose URI starts with `V1_STATEMENT_PATH`. Source: `RouteToBackendResource.java:65-67` (not `ProxyRequestHandler.java:190` as the earlier draft cited — that line is the if-guard for `recordBackendForQueryId`, no stats counter there. Credit to java-analyst for the correction.).
- **Non-determinism risks:**
  - Query lifecycle is timing-dependent. A test that POSTs and immediately polls may see `queued` or `executing` state non-deterministically.
  - The coordinator's heartbeat-timeout creates flaky tests under slow CI. Use mocked clocks where possible.

## Open Questions

- Does the Trino client protocol guarantee that the **path portion** of `nextUri` is stable across versions, or can a future Trino release change the slug format / token semantics in a way that breaks the gateway's queryId regex? `@trino-expert` self-note: needs WebSearch in Task #9 of protocol changelog.
- Is there an end-of-stream signal more precise than "`nextUri` absent"? Some sources hint at a `partialCancel` path that lets the client request a partial-cancel without ending the query — relevant to routing? `@java-analyst`
- What's the actual behavior when a gateway-to-backend connection drops mid-poll? Does the client see 502 and retry against the gateway, which then re-resolves the same queryId→backend mapping? `@java-qa`

## Cross-references

- [[protocol-header-prefix-configurable.md]] — the `X-Trino-` prefix is NOT hardcoded; it can be `X-Presto-` or any other operator-configured value.
- [[spooled-segments-and-redirects.md]] — spooling protocol that side-steps the gateway via 303 redirects to S3 or workers.
- [[../both/gateway-coordinator-nexturi-contract.md]] — the silent dependency on `http-server.process-forwarded=true`.
- [[../both/sticky-routing-contract.md]] — queryId→backend mapping that survives the host pivot.
