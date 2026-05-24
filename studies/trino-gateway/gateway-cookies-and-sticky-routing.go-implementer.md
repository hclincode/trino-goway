---
title: Gateway Cookies and Sticky Routing — Wire Format, HMAC, and Spooled-Segment Scope Correction
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - proxy-core
  - statement-protocol
  - routing-engine
  - auth
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
related-to:
  - trino-gateway/gateway-cookie-internals.go-implementer.md
  - trino/spooled-segment-protocol.trino-expert.md
  - trino-gateway/proxy-streaming-vs-buffering.go-implementer.md
  - trino-gateway/routing-engine.go-qa.md
---

# Gateway Cookies and Sticky Routing — Wire Format, HMAC, and Spooled-Segment Scope Correction

## Summary

The Java gateway issues exactly one cookie type — `TG.OAUTH2` — used to sticky-route OAuth2 callback traffic to the backend that initiated the OAuth2 flow. Statement routing (`/v1/statement`) uses the query-history database, not cookies. The original v1 scope entry for `/v1/spooled/*` sticky routing via `TG.*` cookie is architecturally impossible: the Trino JDBC driver's segment client has no `CookieJar` and the Java gateway does not implement it. This file documents the exact wire format for `TG.OAUTH2`, the HMAC-SHA256 algorithm needed for Go wire-compat, and the recommended scope correction for `/v1/spooled/*`.

## Key Findings

- **Only one cookie type is issued:** `TG.OAUTH2`, on the first request to any path beginning with `/oauth2` when no `TG.OAUTH2` cookie is already present. Source: `gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:207-211`.
- **No statement-routing or spooled-segment cookie exists** in the Java implementation. `/v1/statement` stickiness uses the query-history database. Source: `gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java:154-157`.
- **Cookie value is `base64.URLEncoding` (with `=` padding) of the full `GatewayCookie` JSON.** Java uses `Base64.getUrlEncoder()` without `.withoutPadding()`. Go must use `base64.URLEncoding`, NOT `base64.RawURLEncoding`. Source: `GatewayCookie.java:160,167`.
- **HMAC-SHA256 is computed over `UnsignedGatewayCookie` JSON**, with keys in strict alphabetical order. Null fields are included (`"payload":null`). Source: `GatewayCookie.java:144-148,202`.
- **HMAC key is raw UTF-8 bytes of the config secret string** — not base64-decoded, not hex-decoded. Source: `gateway-ha/src/main/java/io/trino/gateway/ha/config/GatewayCookieConfiguration.java:43`.
- **HMAC digest is encoded as lowercase hex (64 chars)**: Guava's `HashCode.toString()` is lowercase. Source: `GatewayCookie.java:148`.
- **`ttl` is an airlift `Duration` string** (e.g. `"10.00m"`), not a number. This must appear in both the HMAC input and the outer `GatewayCookie` JSON. Source: `GatewayCookie.java:75-76`.
- **HMAC failure throws, not returns false.** `isValid()` throws `IllegalArgumentException` on signature mismatch; the caller propagates this as HTTP 500. Source: `GatewayCookie.java:193-197`, confirmed by `TestGatewayHaMultipleBackend.java:373`.
- **No `Path`, `Domain`, `Secure`, or `HttpOnly` attributes** are set on `Set-Cookie`. Source: `GatewayCookie.java:158-170`.
- **`/v1/spooled/*` cookie-based sticky routing is impossible with the standard Trino JDBC driver.** The driver's segment OkHttpClient is constructed without `CookieJar`. Cookies set on `POST /v1/statement` responses are never echoed on `GET /v1/spooled/*` requests. Source: `trino/client/trino-client/src/main/java/io/trino/client/uri/HttpClientFactory.java:55-58`.

---

## Section 1 — TG.OAUTH2 Cookie: Wire Format

### On-wire `Set-Cookie` header

```
Set-Cookie: TG.OAUTH2=<base64url>; Max-Age=600
```

- **Cookie name:** `TG.OAUTH2` — the prefix `TG.` is fixed (`GatewayCookie.PREFIX = "TG."`, `GatewayCookie.java:43`); the suffix `OAUTH2` comes from `OAuth2GatewayCookie.NAME` (`OAuth2GatewayCookie.java:25`). Future cookie types would follow the same `TG.{name}` pattern.
- **Cookie value:** `Base64.getUrlEncoder().encodeToString(fullGatewayCookieJSON.getBytes(UTF_8))` — URL-safe base64 **with** `=` padding. Source: `GatewayCookie.java:160,167`.
- **`Max-Age`:** `(int)(ttl.toMillis() / 1000)` — integer division, in seconds. For the default `"10.00m"` TTL: `Max-Age=600`. Source: `GatewayCookie.java:161,168`.
- **No other cookie attributes** (`Path`, `Domain`, `Secure`, `HttpOnly`) are present. Source: `GatewayCookie.java:158-170`.

### Full `GatewayCookie` JSON (decoded from the cookie value)

All 9 fields, alphabetically ordered (due to `@JsonPropertyOrder(alphabetic = true)` on `GatewayCookie`, `GatewayCookie.java:34`):

```json
{
  "backend":      "https://trino-cluster-a.example.com:8080",
  "deletePaths":  ["/logout", "/oauth2/logout"],
  "name":         "TG.OAUTH2",
  "payload":      null,
  "priority":     0,
  "routingPaths": ["/oauth2", "/logout", "/oauth2/logout"],
  "signature":    "a3f1b2c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2",
  "ts":           1716540000000,
  "ttl":          "10.00m"
}
```

Field reference:

| Field | JSON type | Notes |
|---|---|---|
| `backend` | string or null | `scheme://host[:port]` of the target cluster |
| `deletePaths` | array of string | Exact paths that trigger cookie deletion |
| `name` | string | Always starts with `TG.` |
| `payload` | string or null | Free-form; currently always null |
| `priority` | number (int) | Lower = higher priority; OAuth2 cookie uses `0` |
| `routingPaths` | array of string | Prefix-match paths the cookie is valid for |
| `signature` | string | 64-char lowercase hex HMAC-SHA256; outer object only |
| `ts` | number (int64) | Unix milliseconds, `System.currentTimeMillis()` |
| `ttl` | string | Airlift Duration string (see Section 3) |

**Critical:** null fields are included as `"key":null`. Jackson's default is to include null values; `GatewayCookie` does NOT annotate `@JsonInclude(NON_NULL)`. Do not omit null-valued fields from the JSON.

---

## Section 2 — HMAC-SHA256 Algorithm (Go)

The HMAC is computed over the `UnsignedGatewayCookie` JSON — the same 8 fields as `GatewayCookie` minus `signature`, in alphabetical key order.

### UnsignedGatewayCookie field order (alphabetical)

| Position | Field | JSON type |
|---|---|---|
| 1 | `backend` | string or null |
| 2 | `deletePaths` | array of string |
| 3 | `name` | string |
| 4 | `payload` | string or null |
| 5 | `priority` | number |
| 6 | `routingPaths` | array of string |
| 7 | `ts` | number |
| 8 | `ttl` | string |

### Go implementation

```go
import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "math"
    "time"
)

// UnsignedGatewayCookie — declare fields in alphabetical order so encoding/json
// serializes them in alphabetical order (field declaration order = JSON output order
// for encoding/json structs). This produces a bit-identical HMAC input to Java.
type UnsignedGatewayCookie struct {
    Backend      *string  `json:"backend"`
    DeletePaths  []string `json:"deletePaths"`
    Name         string   `json:"name"`
    Payload      *string  `json:"payload"`
    Priority     int      `json:"priority"`
    RoutingPaths []string `json:"routingPaths"`
    Ts           int64    `json:"ts"`
    Ttl          string   `json:"ttl"`
}

// GatewayCookie — same fields plus signature, all in alphabetical order.
type GatewayCookie struct {
    Backend      *string  `json:"backend"`
    DeletePaths  []string `json:"deletePaths"`
    Name         string   `json:"name"`
    Payload      *string  `json:"payload"`
    Priority     int      `json:"priority"`
    RoutingPaths []string `json:"routingPaths"`
    Signature    string   `json:"signature"`
    Ts           int64    `json:"ts"`
    Ttl          string   `json:"ttl"`
}

// computeSignature reproduces Java's GatewayCookie.computeSignature.
// Key = raw UTF-8 bytes of the secret string (NOT decoded).
// Input = JSON of UnsignedGatewayCookie with alphabetical keys, nulls included.
// Output = lowercase hex (64 chars).
func computeSignature(unsigned UnsignedGatewayCookie, secret string) (string, error) {
    unsignedJSON, err := json.Marshal(unsigned)
    if err != nil {
        return "", fmt.Errorf("marshal unsigned cookie: %w", err)
    }
    key := []byte(secret)                // raw UTF-8 bytes — NOT base64/hex decoded
    mac := hmac.New(sha256.New, key)
    mac.Write(unsignedJSON)
    digest := mac.Sum(nil)              // 32 bytes
    return hex.EncodeToString(digest), nil  // lowercase hex, 64 chars
}

// encodeCookie produces the Set-Cookie value: base64url WITH padding.
// Go: base64.URLEncoding, NOT base64.RawURLEncoding.
func encodeCookie(c GatewayCookie) (string, error) {
    fullJSON, err := json.Marshal(c)
    if err != nil {
        return "", fmt.Errorf("marshal cookie: %w", err)
    }
    return base64.URLEncoding.EncodeToString(fullJSON), nil
}

// decodeCookie reverses encodeCookie.
func decodeCookie(value string) (GatewayCookie, error) {
    data, err := base64.URLEncoding.DecodeString(value)
    if err != nil {
        return GatewayCookie{}, fmt.Errorf("base64 decode: %w", err)
    }
    var c GatewayCookie
    if err := json.Unmarshal(data, &c); err != nil {
        return GatewayCookie{}, fmt.Errorf("json unmarshal: %w", err)
    }
    return c, nil
}
```

**Gotchas — do not get these wrong:**

1. `base64.URLEncoding` (WITH `=` padding), not `base64.RawURLEncoding` (WITHOUT padding). Java's `Base64.getUrlEncoder()` includes `=`.
2. HMAC key is `[]byte(secret)` — the raw UTF-8 string bytes, NOT `base64.StdDecoding.DecodeString(secret)`.
3. HMAC hex must be lowercase. `hex.EncodeToString` is correct. Never apply `strings.ToUpper`.
4. Go struct fields must be declared in alphabetical order (by JSON tag name) because `encoding/json` serializes in field declaration order, not alphabetical. Verify with a known-good Java-produced fixture.
5. `omitempty` must NOT be used on any field. Null-valued pointer fields must serialize as `null`.

---

## Section 3 — Airlift Duration Format

The `ttl` field must be an airlift `Duration.toString()` string, not a Go `time.Duration` numeric value. Java serializes `10m` as `"10.00m"`.

### Algorithm

Find the largest unit where `value >= 1.0`; format as `"%.2f<unit>"`.

Units in descending order: `d`, `h`, `m`, `s`, `ms`, `us`, `ns`.

### Go implementation

```go
// airliftDurationString converts a Go time.Duration to an airlift Duration string.
// Examples: 10*time.Minute → "10.00m", time.Hour → "1.00h", 500*time.Millisecond → "500.00ms".
func airliftDurationString(d time.Duration) string {
    units := []struct {
        div  float64
        name string
    }{
        {float64(24 * time.Hour), "d"},
        {float64(time.Hour), "h"},
        {float64(time.Minute), "m"},
        {float64(time.Second), "s"},
        {float64(time.Millisecond), "ms"},
        {float64(time.Microsecond), "us"},
        {1.0, "ns"},
    }
    nanos := float64(d.Nanoseconds())
    for _, u := range units {
        v := nanos / u.div
        if v >= 1.0 {
            return fmt.Sprintf("%.2f%s", v, u.name)
        }
    }
    return fmt.Sprintf("%.2fns", nanos)
}
```

Known-good fixtures (for unit tests):

| Go duration | Airlift string |
|---|---|
| `10 * time.Minute` | `"10.00m"` |
| `time.Hour` | `"1.00h"` |
| `500 * time.Millisecond` | `"500.00ms"` |
| `24 * time.Hour` | `"1.00d"` |
| `90 * time.Second` | `"1.50m"` |
| `time.Nanosecond` | `"1.00ns"` |

The `ttl` value is read from config as an airlift Duration string and stored as-is; no conversion through `time.Duration` is necessary if the config parser retains the original string. If conversion is needed (e.g., for TTL expiry checks), convert to `time.Duration` for arithmetic, then call `airliftDurationString` to re-serialize.

---

## Section 4 — Cookie Lifecycle

### Issue: when the gateway sets a cookie

A `TG.OAUTH2` cookie is set on the response when **all** of the following hold:

1. Cookies are enabled in config (`gatewayCookieConfiguration.enabled = true`).
2. The forwarded request URI starts with `/oauth2` (prefix match).
3. The incoming request does NOT already carry a `TG.OAUTH2` cookie.

Source: `gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java:207-211`.

The cookie is populated with:
- `name = "TG.OAUTH2"`
- `backend = scheme + "://" + authority` of the target cluster
- `routingPaths = ["/oauth2", "/logout", "/oauth2/logout"]`
- `deletePaths = ["/logout", "/oauth2/logout"]`
- `ttl` = configured lifetime (default `"10.00m"`)
- `priority = 0`
- `payload = null`
- `ts = System.currentTimeMillis()`

Source: `OAuth2GatewayCookie.java:28-37`.

No `TG.*` cookie is issued for `POST /v1/statement`, `GET /v1/statement/*`, or any `/v1/spooled/*` path.

### Validate: when the gateway reads and verifies a cookie

On every request (in `RoutingTargetHandler.getPreviousCluster()`):

1. If cookies are enabled and the request carries any `TG.*` cookies:
2. Decode each `TG.*` cookie: `base64.URLEncoding` decode → JSON unmarshal.
3. For each decoded cookie, call `isValid()`:
   - **Expiry check:** `currentTimeMs > ts + ttl.toMillis()` → expired → exclude.
   - **HMAC check:** recompute signature over `UnsignedGatewayCookie` JSON; compare against stored `signature`. If mismatch: **throw / return HTTP 500**. Do not silently ignore.
4. Exclude cookies with empty `backend`.
5. Exclude cookies whose `routingPaths` do not prefix-match the request URI (`strings.HasPrefix(requestPath, routingPath)`).
6. Sort surviving cookies: first by `priority` ascending, then by `ts` ascending for ties.
7. Use the first cookie's `backend` as the previous cluster.

Source: `RoutingTargetHandler.java:153-171`, `GatewayCookie.java:178-197`.

**HMAC failure returns HTTP 500, not HTTP 400.** This is the confirmed Java behavior. Source: `TestGatewayHaMultipleBackend.java:373` (`assertThat(callbackResponse.code()).isEqualTo(500)`).

### Delete: when the gateway removes a cookie

On every response, the gateway iterates all existing `TG.*` cookies in the request and marks any cookie for deletion if **either**:

- The cookie is invalid (expired or bad HMAC), OR
- The request path exactly matches any entry in the cookie's `deletePaths` (`deletePaths.contains(path)` — exact match, no prefix).

Deletion is a `Set-Cookie` header with `value="delete"` and `Max-Age=0`. No `Path`, `Domain`, `Secure`, or `HttpOnly` attributes are set on the deletion response either.

Source: `ProxyRequestHandler.java:213-220`.

### Cookie attributes summary

| Attribute | Set? | Value |
|---|---|---|
| Name | yes | `TG.OAUTH2` (or `TG.{name}`) |
| Value | yes | base64url of GatewayCookie JSON |
| Max-Age | yes | `ttl.toMillis() / 1000` (integer seconds) |
| Path | no | — |
| Domain | no | — |
| Secure | no | — |
| HttpOnly | no | — |

---

## Section 5 — wireCompat Flag

### wireCompat: true (default, required for blue/green deployments)

Use the exact algorithm documented in Sections 1–4:

- Cookie value: `base64.URLEncoding` of `GatewayCookie` JSON with alphabetical keys, null fields included
- HMAC input: `UnsignedGatewayCookie` JSON with alphabetical keys, null fields included
- HMAC key: raw UTF-8 bytes of secret string
- HMAC output: lowercase hex
- `ttl`: airlift Duration string

This produces bit-identical cookie values to the Java gateway for the same inputs.

### wireCompat: false (optional, for clean-break Go-only deployments)

An alternative, Go-native format may be used. Example simplifications:
- Omit null fields from JSON (standard `encoding/json` behavior with `omitempty`)
- Use `base64.RawURLEncoding` (no padding)
- Store `ttl` as nanoseconds integer

This is self-consistent within a Go-only fleet but incompatible with Java.

### What breaks during blue/green if wireCompat diverges

During a blue/green cutover, some requests land on Java nodes and some on Go nodes. A client's cookie lifecycle may cross node types:

1. Java node issues `TG.OAUTH2` cookie to client.
2. Client's next request lands on Go node.
3. Go node decodes the Java-format cookie, recomputes HMAC over `UnsignedGatewayCookie` JSON.
4. If Go reconstructs different JSON (field order, null handling, duration format, base64 padding), the HMAC check fails.
5. Go node returns HTTP 500 to the client.

The same failure occurs in reverse (Go issues cookie, Java validates). Any single-byte difference in the HMAC input causes a complete mismatch. `wireCompat: false` must only be used after all Java nodes are decommissioned.

---

## Section 6 — /v1/spooled/* Sticky Routing: Scope Correction

### Original scope entry (in SCOPE.md and PRD.md)

> `/v1/spooled/*` sticky routing — Emit `TG.*` cookie on POST `/v1/statement` responses covering `/v1/spooled` and `/v1/spooled/ack`; prevents cross-cluster segment GET failures for operators using Trino spooling with local coordinator storage across multiple clusters.

### Why this is not implementable via cookies

Three independent facts make cookie-based `/v1/spooled/*` sticky routing impossible:

**1. The Trino JDBC driver does not send cookies on segment requests.**

The JDBC driver creates two separate OkHttpClient instances: one authenticated client (with `CookieJar`) for statement-protocol traffic, and one unauthenticated client (without `CookieJar`) for segment downloads. The segment client is constructed via `HttpClientFactory.unauthenticatedClientBuilder()`, which does not call `setupCookieJar`.

Result: any `TG.*` cookie the gateway sets on a `POST /v1/statement` response is silently ignored for all subsequent `GET /v1/spooled/download/{id}` and `GET /v1/spooled/ack/{id}` requests. Cookies are never sent. Cookie-based stickiness provides zero benefit.

Source: `trino/client/trino-client/src/main/java/io/trino/client/uri/HttpClientFactory.java:55-58`, `trino/client/trino-jdbc/src/main/java/io/trino/jdbc/NonRegisteringTrinoDriver.java:71-82`.

**2. The segment identifier is AES-256 encrypted and opaque — queryId is not recoverable from the URL.**

`SpoolingManagerBridge.toUri()` AES-encrypts the raw binary handle and URL-safe Base64-encodes it before embedding it in the URL. The encrypted blob contains a ULID (segment expiry ordering), encoding type, and node identifier — NOT the queryId. The gateway cannot extract a queryId from `/v1/spooled/download/{identifier}` without the cluster's AES shared secret, and even with the secret the standard filesystem plugin does not embed queryId in the identifier at all.

Result: URL-based routing (extract queryId from URL, look up backend) is also not viable without breaking the encryption boundary.

Source: `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingManagerBridge.java:129-138`, `trino/plugin/trino-spooling-filesystem/src/main/java/io/trino/spooling/filesystem/FileSystemSpoolingManager.java:182-203`.

**3. The Java gateway does not implement this feature either.**

The Java gateway issues no cookie covering `/v1/spooled`. `/v1/statement` stickiness is provided by the query-history database (`RoutingTargetHandler.java:154-157`), not cookies. There is no `TG.SPOOLED` or similar cookie class in the codebase.

### Recommended options (for the team to decide)

**Option A (recommended): Remove `/v1/spooled/*` sticky routing from v1 scope.**

This matches Java behavior. Operators using `COORDINATOR_PROXY` or `WORKER_PROXY` spooling mode with multiple clusters must configure a single-backend routing group for spooled queries, or use `STORAGE` mode (client fetches directly from object storage — the gateway is not in the segment data-path).

Rationale: Option A is the only approach that does not require body rewriting or breaking a Hard Invariant.

**Option B: Server-side segment routing table.**

Record the `queryId → backend` mapping on `POST /v1/statement` and look it up when `GET /v1/spooled/*` arrives. This is the only mechanism that works for JDBC clients (no cookie support).

Problems:
- Requires a durable or in-memory routing table with TTL matching segment TTL.
- Segment URLs in `QueryResults` bodies point at the coordinator's external URL. Whether they route back through the gateway or bypass it depends on `http-server.process-forwarded` configuration — this relationship needs separate analysis before any implementation.
- If segment URLs bypass the gateway (direct coordinator access), the routing table is never consulted and the problem does not exist for single-cluster deployments.

This option requires additional study before it can be scoped.

**Option C: Operator-level load balancer affinity.**

Require operators using `COORDINATOR_PROXY` spooling with multi-cluster routing groups to configure session-level or connection-level affinity at the load balancer (e.g., HAProxy `balance source`, nginx `ip_hash`). This is outside the gateway's control and removes the problem from the gateway's responsibility.

Problem: breaks the gateway's value proposition of transparent load balancing and fails for clients with NAT or rotating IPs.

### Recommendation

**Choose Option A.** It matches the Java gateway's actual behavior, requires no additional implementation, and adds no Hard Invariant violations. The scope entry in `SCOPE.md` and `PRD.md` should be removed under the sign-off policy (Section 5 of `SCOPE.md`).

The scope change requires:
1. A `topics/` document with rationale and evidence.
2. Team-lead acknowledgment in the git commit.
3. Updated `SCOPE.md`.

---

## Section 7 — Key Source Files

All paths are relative to the `trino-gateway/` submodule root.

| File | Relevance |
|---|---|
| `gateway-ha/src/main/java/io/trino/gateway/ha/router/GatewayCookie.java` | Wire format, HMAC computation (`computeSignature` at line 144), `toCookie`/`toNewCookie` (lines 158–170), `fromCookie` (line 173), `isValid` (line 188), `UnsignedGatewayCookie` inner class (line 202) |
| `gateway-ha/src/main/java/io/trino/gateway/ha/router/OAuth2GatewayCookie.java` | Only concrete subclass; `NAME = "TG.OAUTH2"`, `OAUTH2_PATH = "/oauth2"`, default field values |
| `gateway-ha/src/main/java/io/trino/gateway/ha/config/GatewayCookieConfiguration.java` | HMAC key construction: `SecretKeySpec(secret.getBytes(UTF_8), "HmacSHA256")` at line 43 |
| `gateway-ha/src/main/java/io/trino/gateway/ha/config/GatewayCookieConfigurationPropertiesProvider.java` | Singleton provider; `enabled` flag; `getCookieSigningKey()` |
| `gateway-ha/src/main/java/io/trino/gateway/ha/config/OAuth2GatewayCookieConfiguration.java` | Default values: `routingPaths=["/oauth2"]`, `deletePaths=["/logout","/oauth2/logout"]`, `lifetime="10m"` |
| `gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java` | Cookie issuance (`getOAuth2GatewayCookie` at line 204) and deletion logic (lines 213–220) |
| `gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java` | Cookie validation and routing (`getPreviousCluster` at line 153); HMAC failure propagation |
| `gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java` | Integration tests: `testCookieSigning` (line 335) confirms tampered cookie → HTTP 500; `testOAuth2Flow` tests full issue/delete cycle |
| `gateway-ha/src/test/resources/test-config-template.yml` | Example config: `enabled: true`, `cookieSigningSecret: "kjlhbfrewbyuo452cds3dc1234ancdsjh"` |

Trino submodule files relevant to spooled routing analysis:

| File | Relevance |
|---|---|
| `trino/client/trino-client/src/main/java/io/trino/client/uri/HttpClientFactory.java:55-58` | Segment client constructed without `CookieJar` |
| `trino/client/trino-jdbc/src/main/java/io/trino/jdbc/NonRegisteringTrinoDriver.java:71-82` | Two OkHttpClient instances: authenticated (CookieJar) and unauthenticated (no CookieJar) |
| `trino/core/trino-main/src/main/java/io/trino/server/protocol/spooling/SpoolingManagerBridge.java:129-138` | AES-256 encryption of segment identifier |
| `trino/plugin/trino-spooling-filesystem/src/main/java/io/trino/spooling/filesystem/FileSystemSpoolingManager.java:182-203` | Identifier serialization: ULID + encoding + node ID; no queryId |

---

## Behavior vs. Implementation Artifact

### Cookie value is base64url WITH padding

- **Observed behavior:** `Base64.getUrlEncoder().encodeToString(...)` — Java's default URL encoder includes `=` padding. Source: `GatewayCookie.java:160,167`.
- **Source of behavior:** `jvm-artifact` — the author did not call `.withoutPadding()`, so padding is included by default.
- **Go obligation:** `replicate-exactly` for `wireCompat: true`. Use `base64.URLEncoding`. For `wireCompat: false`, `base64.RawURLEncoding` is acceptable.
- **Notes:** HTTP cookie values may contain `=`; no compatibility issues observed.

### HMAC failure returns HTTP 500

- **Observed behavior:** `isValid()` throws `IllegalArgumentException` on signature mismatch. The `RoutingTargetHandler` uses `.filter(GatewayCookie::isValid)` which does not catch the exception, propagating it as an unhandled error → HTTP 500. Source: `GatewayCookie.java:193-197`, `RoutingTargetHandler.java:162`, `TestGatewayHaMultipleBackend.java:373`.
- **Source of behavior:** `defensive-historical` — throwing on tampered cookie prevents silent fallback to un-routed behavior.
- **Go obligation:** `replicate-intent` — return an error on HMAC mismatch and propagate as HTTP 500. Do not silently re-route.
- **Notes:** HTTP 400 (Bad Request) would be semantically more correct for a client-supplied bad cookie, but the Java behavior is 500. The architect should decide whether to preserve 500 or correct to 400 in the Go rewrite.

### No cookie covers /v1/statement or /v1/spooled

- **Observed behavior:** The only issued cookie is `TG.OAUTH2` with `routingPaths = ["/oauth2", "/logout", "/oauth2/logout"]`. Statement-protocol stickiness is provided by the query-history database. No `TG.*` cookie covers `/v1/statement` or `/v1/spooled`. Source: `ProxyRequestHandler.java:207-211`, `RoutingTargetHandler.java:154-157`.
- **Source of behavior:** `gateway-design-intent` — query ID sticky routing via DB is more reliable than cookie-based routing because it survives client reconnects and cookie deletion.
- **Go obligation:** `replicate-intent` — do not issue a `/v1/spooled` cookie. Use the query-history DB for `/v1/statement` stickiness (already covered by the 3-step recovery chain requirement in Hard Invariant #4).

---

## Implications for Go Rewrite

- Implement `UnsignedGatewayCookie` and `GatewayCookie` as Go structs with JSON tags declared in alphabetical order (by tag name), no `omitempty`. Verify with a round-trip test against a known Java-produced cookie.
- `airliftDurationString()` is a required utility function; it is not in any Go standard library. Keep it in a dedicated function with a table-driven unit test.
- The signing key is the raw secret string — do not decode it. This is the most common port-to-port HMAC incompatibility.
- The scope entry for `/v1/spooled/*` cookie sticky routing should be removed from `SCOPE.md` and `PRD.md` before implementation begins to avoid wasted implementation effort. A `topics/` rationale document and team-lead sign-off are required.
- `/v1/spooled/*` path handling still needs to be proxied correctly (streaming via `io.Copy` for `COORDINATOR_PROXY` mode); only the sticky routing via cookie is removed from scope.
- HMAC failure must return HTTP 500 (to match Java behavior) or HTTP 400 (if the architect decides to correct the semantics). Do not silently swallow the error.

---

## Test Strategy Hooks

- **HMAC unit test:** fixed `cookieSigningSecret`, fixed `ts`, fixed field values → assert exact 64-char lowercase hex signature against a known-good Java-produced value.
- **Cookie encode/decode round-trip:** issue → encode → decode → verify all fields, especially null `payload` and airlift `ttl` string.
- **wireCompat cross-check:** produce a cookie from Go with `wireCompat: true`; feed the raw cookie value to the Java integration test fixture; confirm Java accepts and routes correctly.
- **HMAC tamper test:** alter one byte of the cookie value after encoding; gateway must return HTTP 500, not 200 or silent re-route.
- **Delete path test:** request to `/logout` with an existing `TG.OAUTH2` cookie must elicit a `Set-Cookie: TG.OAUTH2=delete; Max-Age=0` response.
- **Non-determinism risk:** `ts = time.Now().UnixMilli()` changes every millisecond. Tests must inject a fixed timestamp to get reproducible HMAC inputs.

See `[[gateway-cookie-internals.go-implementer.md]]` for additional test strategy detail.

---

## Open Questions

- `@architect`: Should HMAC failure return HTTP 400 (client error: bad cookie) or HTTP 500 (current Java behavior)? HTTP 500 is technically incorrect per RFC 7231.
- `@architect`: Confirm that removing `/v1/spooled/*` cookie sticky routing from v1 scope is acceptable; initiate the sign-off process if so.
- `@trino-expert`: Do segment URIs in `QueryResults` bodies point at the gateway (via `X-Forwarded-Host`) or directly at the coordinator? If they bypass the gateway, `/v1/spooled/*` routing at the gateway level is irrelevant for single-cluster deployments.
- `@architect`: Should `Secure` and `HttpOnly` attributes be added to cookies when the gateway is serving HTTPS? This improves security but breaks wire-compat with the Java gateway.
- `@architect`: The `priority` field exists in the data model but `OAuth2GatewayCookie` always sets it to `0`. Is multi-priority cookie routing intended for future use, or is it safe to simplify to single-cookie semantics in Go?

---

## Cross-references

- `[[gateway-cookie-internals.go-implementer.md]]` — detailed Java source analysis of `GatewayCookie` internals; this file supersedes its Go replication guidance with corrected scope.
- `[[trino/spooled-segment-protocol.trino-expert.md]]` — full analysis of segment identifier format, retrieval modes, and JDBC client architecture.
- `[[proxy-streaming-vs-buffering.go-implementer.md]]` — streaming obligation for `/v1/spooled/*` proxy (still required; only cookie sticky routing is removed).
- `[[routing-engine.go-qa.md]]` — routing group selection; cookie is one input to `getPreviousCluster`.
- `[[jvm-dependencies-inventory.go-implementer.md]]` — Guava `Hashing`, airlift `JsonCodec`, airlift `units.Duration` mappings.
