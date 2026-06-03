---
title: Spooled Segment Protocol — token format, routing, and flow
author: trino-expert
role: Trino & Trino-Gateway Expert
component: trino
topics:
  - statement-protocol
  - proxy-core
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino/spooled-segments-and-redirects.md
  - trino/statement-protocol-overview.md
---

# Spooled Segment Protocol — token format, routing, and flow

## Summary

The `/v1/spooled/{identifier}` token is an AES-encrypted, URL-safe Base64 blob that encodes internal storage metadata but does NOT contain a queryId recoverable without the shared secret key. Because the queryId cannot be extracted from the URL alone, the gateway cannot route spooled segment requests by URL parsing alone. Cookie-based sticky routing (emitting a `TG.*` cookie on the initial `POST /v1/statement` response that covers the `/v1/spooled` path prefix) is the only viable mechanism that avoids a server-side routing table.

## Key Findings

- **Two URL paths for spooled operations exist on the coordinator:**
  - `GET /v1/spooled/download/{identifier}` — fetch segment data.
  - `GET /v1/spooled/ack/{identifier}` — acknowledge consumption (triggers GC of the segment).
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/CoordinatorSegmentResource.java:64-106`.

- **Workers expose the same download path:**
  - `GET /v1/spooled/download/{identifier}` — proxied from the coordinator in `WORKER_PROXY` mode.
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/WorkerSegmentResource.java:36-55`.

- **The `{identifier}` token is AES-encrypted and opaque to outside observers.** `SpoolingManagerBridge.toUri()` AES-encrypts the raw binary handle, then URL-safe Base64-encodes it before placing it in the URL. Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingManagerBridge.java:129-138`.

- **The decrypted identifier for the filesystem spooling plugin encodes:** `[16-byte ULID UUID][2-byte encoding-length][encoding bytes][2-byte node-identifier-length][node-identifier bytes][1-byte isEncrypted boolean]`. The node identifier is the Trino coordinator/worker node ID — not the queryId. Source: `trino/plugin/trino-spooling-filesystem/src/main/java/io/trino/spooling/filesystem/FileSystemSpoolingManager.java:182-203`.

- **The `{identifier}` is a ULID** (lexicographically sorted by expiry epoch) for the filesystem plugin, generated at segment creation time. Source: `trino/plugin/trino-spooling-filesystem/src/main/java/io/trino/spooling/filesystem/FileSystemSpooledSegmentHandle.java:60-63`.

- **The queryId is never embedded in the URL.** The `SpoolingContext` passed to `SpoolingManager.create()` includes a `QueryId`, but it is not serialized into the public identifier — only the ULID, encoding, node identifier, and encryption flag are. Source: `trino/core/trino-spi/src/main/java/io/trino/spi/spool/SpoolingContext.java`, `FileSystemSpoolingManager.java:108-115,182-203`.

- **URLs are embedded in the `QueryResults` body**, inside the `data` field as `SpooledSegment` JSON objects with `uri` (download) and `ackUri` (acknowledge) fields. These are absolute URIs constructed from the coordinator's external base URI. Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingQueryDataProducer.java:63-68,77-83`.

- **Four retrieval modes** control the HTTP flow on `GET /v1/spooled/download/{identifier}` (`protocol.spooling.retrieval-mode`):
  - `STORAGE` — The `uri` in `SpooledSegment` is already a presigned object-storage URI. The client contacts storage directly; the coordinator is NOT in the data-path for the download. The `/v1/spooled/download` endpoint returns 503 if called in this mode.
  - `COORDINATOR_STORAGE_REDIRECT` — Coordinator returns `303 See Other` with `Location` pointing to a presigned object-storage URI.
  - `COORDINATOR_PROXY` — Coordinator streams segment bytes directly; response is `200 OK` with `application/octet-stream` body.
  - `WORKER_PROXY` — Coordinator returns `303 See Other` with `Location` rewriting the request URL to a randomly-selected active worker node (same path, different host/port).
  Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingConfig.java:160-167` and `CoordinatorSegmentResource.java:72-89`.

- **`GET /v1/spooled/ack/{identifier}` always returns `200 OK`** (or `500` on failure). It is a `GET`, not a `DELETE` or `POST`. Source: `CoordinatorSegmentResource.java:92-106`.

- **The JDBC driver uses two separate OkHttpClient instances**: one authenticated client (with `CookieJar`) for statement-protocol requests, and one unauthenticated client (without `CookieJar`) for segment downloads. Source: `trino/client/trino-jdbc/src/main/java/io/trino/jdbc/NonRegisteringTrinoDriver.java:71-82`, `trino/client/trino-client/src/main/java/io/trino/client/uri/HttpClientFactory.java:55-58`.

- **The unauthenticated segment HTTP client does NOT have `setupCookieJar` called.** Only the primary HTTP client in `toHttpClientBuilder` calls `setupCookieJar`. The segment client is constructed via `unauthenticatedClientBuilder`, which does not call `setupCookieJar`. Source: `HttpClientFactory.java:55-58,146-193`.

- **OkHttp's default follow-redirects is `true`.** Neither `unauthenticatedClientBuilder` nor the segment client explicitly disables redirect-following. The segment client will automatically follow `303 See Other` to the presigned URI or worker node. Source: OkHttp defaults (no `followRedirects(false)` call in `unauthenticatedClientBuilder` or at segment request build time; `OkHttpSegmentLoader.java:57-66`).

- **The `/v1/spooled/ack` request is fire-and-forget** on the client side: `OkHttpSegmentLoader.acknowledge()` enqueues the request asynchronously and logs but ignores failures. Source: `trino/client/trino-client/src/main/java/io/trino/client/OkHttpSegmentLoader.java:72-92`.

## Behavior vs. Implementation Artifact

### Token is AES-encrypted and opaque
- **Observed behavior:** `SpoolingManagerBridge.toUri(secretKey, identifier)` encrypts the raw binary handle with AES, then URL-safe Base64-encodes it. `SpoolingManagerBridge.fromUri(secretKey, identifier)` is the inverse. Without the shared AES-256 key (`protocol.spooling.shared-secret-key`), the identifier is undecodable. Source: `SpoolingManagerBridge.java:129-156`.
- **Source of behavior:** `protocol-required`. The encryption prevents clients from forging or enumerating segment identifiers. The ULID inside is expiry-ordered, so if it were unencrypted it would be trivially guessable.
- **Rationale:** Security — prevent unauthorized access to another query's results.
- **Go obligation:** `replicate-exactly` if the gateway ever needs to decode the token (it does not under cookie-stick design). For cookie-stick routing, the gateway passes the identifier through untouched; no decryption needed.
- **Notes:** The encryption key is shared across all coordinator/worker nodes in a cluster. It must match between the node that created the segment and the node decoding the identifier.

### queryId is absent from the URL
- **Observed behavior:** `SpoolingContext` carries a `QueryId`, but `FileSystemSpoolingManager.serialize()` writes only: ULID (16 bytes), encoding string, node identifier string, and isEncrypted flag. queryId is not serialized. Source: `FileSystemSpoolingManager.java:182-203`, `SpoolingContext.java`.
- **Source of behavior:** `gateway-design-intent` (no explicit comment, but the choice to omit queryId from the identifier is consistent with the design of a self-contained handle that can be validated by any node with the secret key).
- **Rationale:** The URL identifier is meant to be opaque and self-contained for security; embedding queryId would add no server-side value since the handle already locates the data.
- **Go obligation:** `replicate-exactly` at the level of "do not attempt URL-based queryId extraction for spooled paths." The Go rewrite must treat `/v1/spooled/download/{id}` and `/v1/spooled/ack/{id}` as requiring sticky routing by a mechanism other than URL parsing.
- **Notes:** A custom `SpoolingManager` plugin could theoretically embed a queryId in its identifier format, but the standard filesystem plugin does not.

### Segment client has no CookieJar
- **Observed behavior:** `NonRegisteringTrinoDriver` creates the segment HTTP client via `HttpClientFactory.unauthenticatedClientBuilder()`, which does NOT call `setupCookieJar`. The segment client therefore does NOT send or persist HTTP cookies. Source: `NonRegisteringTrinoDriver.java:75-77`, `HttpClientFactory.java:146-193`.
- **Source of behavior:** `gateway-design-intent`. The segment download is treated as a direct, unauthenticated fetch; cookies from the statement-protocol session are intentionally not included.
- **Rationale:** Segment URLs in `COORDINATOR_STORAGE_REDIRECT` mode redirect to object storage (S3, GCS, Azure) which uses presigned URLs for auth, not cookies. Including gateway cookies in those requests would be harmless but unnecessary. For `COORDINATOR_PROXY` and `WORKER_PROXY`, the segment identifier in the URL is sufficient.
- **Go obligation:** `defer-to-architect`. This is the critical finding for sticky routing: the JDBC client does NOT send cookies on segment requests. A `TG.*` cookie set on the `/v1/statement` POST response will NOT be echoed back on subsequent `GET /v1/spooled/download/{id}` requests from the JDBC driver. Cookie-based sticky routing as typically implemented is therefore incompatible with the JDBC driver's segment client.
- **Notes:** This is a significant constraint. See the Sticky Routing Mechanism Assessment section below.

### Ack is fire-and-forget on the client
- **Observed behavior:** `OkHttpSegmentLoader.acknowledge()` fires the `GET /v1/spooled/ack/{id}` request asynchronously and ignores failures (logs a warning on error). Source: `OkHttpSegmentLoader.java:72-92`.
- **Source of behavior:** `gateway-design-intent`. Ack is a best-effort GC hint; the coordinator has background pruning for un-acked segments.
- **Rationale:** Segment GC doesn't need to block the client reading results.
- **Go obligation:** `replicate-intent`. The gateway must pass ack requests to the correct backend, but a 404 on ack (wrong backend) is a non-fatal GC miss, not a data corruption. Priority is lower than ensuring download correctness.
- **Notes:** If ack goes to the wrong backend, the segment will be cleaned up by the coordinator's background pruner based on TTL.

### OkHttp client follows 303 redirects by default
- **Observed behavior:** The segment `OkHttpClient` does not set `followRedirects(false)`. OkHttp default is `followRedirects = true`. The client will follow `303 See Other` transparently to the presigned URI (in `COORDINATOR_STORAGE_REDIRECT` mode) or to the worker (in `WORKER_PROXY` mode). Source: `OkHttpSegmentLoader.java:57-66`, OkHttp source defaults.
- **Source of behavior:** `jvm-artifact`. OkHttp follows redirects by default unless explicitly disabled.
- **Rationale:** Transparent redirect-following is correct here: in `COORDINATOR_STORAGE_REDIRECT` and `WORKER_PROXY` modes, the `303` is an implementation detail that the client should not need to handle specially.
- **Go obligation:** `replicate-intent`. The Go proxy must pass `303` responses to the client unchanged (no internal redirect-following), so the client's OkHttp redirect-following handles it. This matches the existing Java gateway behavior (`setFollowRedirects(false)` on the gateway's own HTTP client). See `[[spooled-segments-and-redirects.md]]`.

## Implications for Go Rewrite

- **queryId-based URL routing is not possible for `/v1/spooled/*` paths.** The token is AES-encrypted with the cluster's shared secret; decrypting it requires the cluster's secret key, which the gateway should not possess. Even if it did, extracting queryId is not sufficient: the standard filesystem plugin does not embed queryId in the identifier at all.

- **Cookie-based sticky routing as used in the Java gateway does NOT work with the Trino JDBC driver.** The segment HTTP client (`unauthenticatedClientBuilder`) has no `CookieJar`, so cookies set on statement responses are not sent on segment requests. This invalidates the Java gateway's implicit assumption (and the current trino-goway v1 plan) that a `TG.*` cookie covering `/v1/spooled` would create stickiness.

- **Two viable v1 stances for the Go rewrite:**
  1. **Defer support** — document that multi-backend spooled-segment routing requires a single-backend deployment or `STORAGE` mode (client fetches from object storage directly). This avoids the routing problem entirely. Valid for `v1` if spooled multi-backend is not a hard requirement.
  2. **Server-side routing table** — record the `queryId → backend` mapping on `POST /v1/statement`, then route `GET /v1/spooled/*` by looking up the backend from the routing table. This requires a durable store (or in-memory with TTL matching segment TTL). The routing table approach is the only mechanism that works for JDBC clients that do not send cookies.
  An alternative worth exploring: the `SpooledSegment.uri` and `ackUri` embedded in `QueryResults` are absolute URIs pointing at the coordinator. If the gateway rewrites `nextUri` to point back at itself, does it also rewrite segment URIs? If segment URIs in the body are left pointing at the specific coordinator, the client goes directly there, bypassing the gateway — which is actually correct for single-cluster deployments. This warrants a separate analysis.

- **`STORAGE` mode is transparent to the gateway** — the `dataUri` in the `SpooledSegment` JSON is a presigned object-storage URL, never hitting `/v1/spooled/download`. The gateway only sees the statement-protocol traffic. No routing special-casing is needed.

- **`COORDINATOR_PROXY` and `WORKER_PROXY` modes require streaming, not buffering.** Segment data can be 8–16 MB+ per default config. Source: `SpoolingConfig.java:43-44`. The Go gateway must use `io.Copy` with a flushable response writer. See `[[spooled-segments-and-redirects.md]]` for the streaming obligation.

- **`/v1/spooled/ack/{id}` has the same routing requirement as download**, but failure is lower-risk (background pruning covers missed acks).

## Test Strategy Hooks

see paired QA study (none yet — request one from `@qa-tech-lead`).

- **Test level:** integration (live Trino + spooling enabled) + differential.
- **Fixtures required:**
  - Trino with `protocol.spooling.enabled=true` and each retrieval mode.
  - JDBC client issuing a query that produces spooled segments (result set larger than `inliningMaxRows`).
  - Multi-backend gateway config to confirm routing does/does not work.
- **Observable signals:**
  - In `STORAGE` mode: no `GET /v1/spooled/*` requests reach the gateway at all.
  - In `COORDINATOR_PROXY` mode: segment bytes arrive intact (checksum); no truncation.
  - In `WORKER_PROXY` mode: gateway passes the `303 See Other` with `Location` intact; client follows to worker.
  - Ack requests: a missed ack (404 on wrong backend) does not cause query failure; segment is cleaned up by TTL.
- **Non-determinism risks:** segment TTL expiry between download and ack; background pruning racing with test assertion.

## Open Questions

- **Does the Java gateway actually work for JDBC clients with spooling in multi-backend mode?** Given the JDBC driver has no CookieJar on the segment client, the Java gateway's cookie-stick design provides zero stickiness for segment downloads. If production deployments with spooling exist, they must be single-backend. `@architect` to confirm.
- **Do segment URIs in `QueryResults` point at the coordinator's external URL or the gateway URL?** If Trino constructs them from `ExternalUriInfo` (which reflects the host the client connected to, i.e., the gateway), then segment requests flow back through the gateway. If they reflect the coordinator's own URL directly, the client bypasses the gateway. This determines whether gateway-side routing is needed at all. `@trino-expert` self-note: trace `ExternalUriInfo` construction.
- **Would explicitly embedding queryId in segment URIs (as a query parameter) be a viable Trino protocol extension?** This would allow URL-based routing without a routing table or cookies. `@trino-expert` to assess feasibility as a Trino upstream contribution.
- **Is the CLI (trino-cli) also using the unauthenticated segment client without cookies?** Assumption is yes (shares the same `HttpClientFactory`), but needs verification. `@trino-expert`.

## Key Classes

| Class | Path | Role |
|---|---|---|
| `CoordinatorSegmentResource` | `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/CoordinatorSegmentResource.java` | JAX-RS resource for `/v1/spooled/download/{id}` and `/v1/spooled/ack/{id}` on coordinator; dispatches by retrieval mode |
| `WorkerSegmentResource` | `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/WorkerSegmentResource.java` | JAX-RS resource for `/v1/spooled/download/{id}` on workers (WORKER_PROXY mode) |
| `SpoolingManagerBridge` | `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingManagerBridge.java` | AES encrypt/decrypt for identifier URL encoding; delegates to plugin `SpoolingManager` |
| `SpoolingConfig` | `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingConfig.java` | Config: `protocol.spooling.retrieval-mode`, `shared-secret-key`, segment sizes |
| `SpoolingQueryDataProducer` | `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingQueryDataProducer.java` | Builds `SpooledSegment` JSON with `uri` and `ackUri` fields from coordinator base URI |
| `SpoolingManager` (SPI) | `trino/core/trino-spi/src/main/java/io/trino/spi/spool/SpoolingManager.java` | Plugin SPI: `create`, `location`, `handle`, `openInputStream`, `acknowledge`, `directLocation` |
| `SpooledSegmentHandle` (SPI) | `trino/core/trino-spi/src/main/java/io/trino/spi/spool/SpooledSegmentHandle.java` | Plugin SPI handle: `identifier()`, `encoding()`, `expirationTime()` |
| `SpooledLocation` (SPI) | `trino/core/trino-spi/src/main/java/io/trino/spi/spool/SpooledLocation.java` | Sealed: `CoordinatorLocation` (gateway routes) vs `DirectLocation` (presigned URI) |
| `SpoolingContext` (SPI) | `trino/core/trino-spi/src/main/java/io/trino/spi/spool/SpoolingContext.java` | Input to `create()`: encoding, queryId (not surfaced in URL), rows, size |
| `FileSystemSpoolingManager` | `trino/plugin/trino-spooling-filesystem/src/main/java/io/trino/spooling/filesystem/FileSystemSpoolingManager.java` | Reference implementation: serializes handle as ULID+encoding+nodeId+isEncrypted |
| `FileSystemSpooledSegmentHandle` | `trino/plugin/trino-spooling-filesystem/src/main/java/io/trino/spooling/filesystem/FileSystemSpooledSegmentHandle.java` | Concrete handle: ULID UUID (expiry-ordered), nodeIdentifier, encoding, optional encryption key |
| `SpooledSegment` (client) | `trino/client/trino-client/src/main/java/io/trino/client/spooling/SpooledSegment.java` | Wire type: `uri`, `ackUri`, `headers`, `metadata` |
| `OkHttpSegmentLoader` | `trino/client/trino-client/src/main/java/io/trino/client/OkHttpSegmentLoader.java` | Client-side HTTP fetch for segment data and ack (unauthenticated, no CookieJar) |
| `HttpClientFactory` | `trino/client/trino-client/src/main/java/io/trino/client/uri/HttpClientFactory.java` | Factory: `toHttpClientBuilder` (with CookieJar) vs `unauthenticatedClientBuilder` (without) |
| `NonRegisteringTrinoDriver` | `trino/client/trino-jdbc/src/main/java/io/trino/jdbc/NonRegisteringTrinoDriver.java` | JDBC entry point: creates two separate OkHttpClient instances — authenticated (statement) and unauthenticated (segments) |

## Cross-references

- `[[spooled-segments-and-redirects.md]]` — earlier study on the same topic; covers the Java gateway's handling of redirects and buffering hazards. This file is a deeper drill into the token format and client behavior. Both are complementary.
- `[[statement-protocol-overview.md]]` — parent statement protocol that delivers segment URIs to the client.
