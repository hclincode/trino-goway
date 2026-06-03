---
title: GatewayCookie Internals — Wire Format, HMAC Signing, and Lifecycle
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - proxy-core
  - routing-engine
  - auth
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/proxy-request-lifecycle.go-qa.md
  - trino-gateway/routing-engine.go-qa.md
---

# GatewayCookie Internals — Wire Format, HMAC Signing, and Lifecycle

## Summary

`GatewayCookie` is the signed-cookie mechanism trino-gateway uses to sticky-route follow-up requests to the same backend cluster. A cookie carries a JSON payload (base64url-encoded) plus an HMAC-SHA256 signature over the payload's unsigned fields. The signing algorithm, JSON serialization order, and wire encoding must be bit-identical in the Go rewrite for blue-green deployments to work without session disruption. This study documents every byte-level detail needed for that replication.

## Key Findings

- **Cookie name prefix is `TG.`** — every gateway cookie begins with this prefix. `GatewayCookie.PREFIX = "TG."` (`GatewayCookie.java:43`). The only subclass currently issued is `OAuth2GatewayCookie`, named `TG.OAUTH2` (`OAuth2GatewayCookie.java:25`).
- **Wire value is URL-safe base64 (no padding stripped, standard Java `getUrlEncoder()`) of the UTF-8 JSON representation** of the full `GatewayCookie` object (`GatewayCookie.java:160, 167`). Decoding: `Base64.getUrlDecoder().decode(cookie.getValue())` (`GatewayCookie.java:175`).
- **The HMAC-SHA256 input is the JSON serialization of `UnsignedGatewayCookie`**, not of the full `GatewayCookie`. The signature is computed before the `signature` field is embedded in the outer JSON (`GatewayCookie.java:146-148`).
- **HMAC output format is lowercase hex** — Guava's `HashCode.toString()` produces lowercase hexadecimal with no separators (`GatewayCookie.java:148`).
- **JSON key order is deterministic**: both `GatewayCookie` and `UnsignedGatewayCookie` carry `@JsonPropertyOrder(alphabetic = true)` (`GatewayCookie.java:34, 202`). Alphabetical Jackson ordering is required for the HMAC input to be reproducible.
- **Signing key is the raw UTF-8 bytes of `cookieSigningSecret`**, wrapped as `SecretKeySpec(secret.getBytes(UTF_8), "HmacSHA256")` (`GatewayCookieConfiguration.java:43`).
- **No `Path`, `Domain`, `Secure`, or `HttpOnly` cookie attributes are set**. `toCookie()` only sets `name`, `value`, and `Max-Age` (`GatewayCookie.java:158-162`). `toNewCookie()` sets the same three fields via `NewCookie.Builder` (`GatewayCookie.java:165-170`).
- **Max-Age is `ttl.toMillis() / 1000`** (integer division, seconds) (`GatewayCookie.java:161, 168`).
- **`routingPaths` uses prefix matching** (`path.startsWith(routingPath)`), not exact match. A routing path of `/oauth2` matches `/oauth2/callback`, `/oauth2/token`, etc. (`GatewayCookie.java:180`).
- **`deletePaths` uses exact match** (`contains`, not `startsWith`) (`GatewayCookie.java:184`).
- **The only cookie issued today is `TG.OAUTH2`**, issued on the first request whose path starts with `/oauth2` when no `TG.OAUTH2` cookie is already present (`ProxyRequestHandler.java:207-211`). No statement-routing cookie (`TG.routing` or similar) is issued; statement routing uses the query-history DB, not cookies.
- **HMAC failure throws `IllegalArgumentException`**, not a silent ignore. The `isValid()` call in `RoutingTargetHandler.getPreviousCluster()` is wrapped in a `filter` — an exception propagates and will cause a 500 if the tampered cookie is present (`GatewayCookie.java:194-197`, `TestGatewayHaMultipleBackend.java:373`).
- **Cookie invalidation (deletion) is triggered on every response** for any existing `TG.*` cookie that is either expired or whose request path matches `deletePaths`. The deletion response sets `value="delete"` and `Max-Age=0` (`ProxyRequestHandler.java:213-220`).

## Section 1 — GatewayCookie Wire Format

### On-wire representation

The `Set-Cookie` header for a `TG.OAUTH2` cookie looks like:

```
Set-Cookie: TG.OAUTH2=<base64url>; Max-Age=600
```

where `<base64url>` is `Base64.getUrlEncoder().encodeToString(json.getBytes(UTF-8))`.

Java's `Base64.getUrlEncoder()` produces URL-safe base64 **with padding** (`=`). It does not strip `=` characters. Go must use `base64.URLEncoding` (not `base64.RawURLEncoding`) to match.

### Example decoded JSON (GatewayCookie)

```json
{
  "backend": "https://trino-backend.example.com",
  "deletePaths": ["/logout", "/oauth2/logout"],
  "name": "TG.OAUTH2",
  "payload": null,
  "priority": 0,
  "routingPaths": ["/oauth2"],
  "signature": "a1b2c3...64hexchars...",
  "ts": 1716540000000,
  "ttl": "10.00m"
}
```

Keys are alphabetically ordered because of `@JsonPropertyOrder(alphabetic = true)` on `GatewayCookie` (`GatewayCookie.java:34`).

**`null` fields are included in the JSON** because `GatewayCookie` does not annotate `@JsonInclude(NON_NULL)`. Jackson's default includes null values.

**`ttl` is serialized as airlift `Duration.toString()`** which produces a human-readable string like `"10.00m"` or `"1.00h"`. The exact format is `"<value>.<fraction><unit>"` from `io.airlift.units.Duration`, where units are `ns`, `us`, `ms`, `s`, `m`, `h`, `d`. Go must reproduce this exact string for the HMAC to match.

**`ts` is a JSON number** (milliseconds since Unix epoch, `System.currentTimeMillis()`).

## Section 2 — HMAC-SHA256 Signing Algorithm

### Step-by-step

1. **Build an `UnsignedGatewayCookie`** with the same fields as the outer `GatewayCookie` minus `signature`.
2. **Serialize `UnsignedGatewayCookie` to JSON** using airlift `JsonCodec` (backed by Jackson). The class is annotated `@JsonPropertyOrder(alphabetic = true)`, so keys appear in alphabetical order: `backend`, `deletePaths`, `name`, `payload`, `priority`, `routingPaths`, `ts`, `ttl` (`GatewayCookie.java:202`, `GatewayCookie.java:205`).
3. **Encode the JSON string as UTF-8 bytes**.
4. **Compute HMAC-SHA256** over those bytes using the secret key. The key is the raw UTF-8 bytes of the config string `cookieSigningSecret`, algorithm label `HmacSHA256` (`GatewayCookieConfiguration.java:43`). In Go: `hmac.New(sha256.New, []byte(cookieSigningSecret))`.
5. **Encode the 32-byte HMAC digest as lowercase hexadecimal** with no separators. Guava's `HashCode.toString()` does this; Go's `fmt.Sprintf("%x", digest)` or `hex.EncodeToString(digest)` produce identical output (`GatewayCookie.java:148`).
6. **Store the resulting 64-character hex string as the `signature` field** in the outer `GatewayCookie` JSON.

### Guava API used

```java
Hashing.hmacSha256(secretKey)        // secretKey is a javax.crypto.SecretKey
    .hashString(unsignedJson, UTF_8)  // input = UTF-8 bytes of UnsignedGatewayCookie JSON
    .toString()                       // output = lowercase hex string
```

`Hashing.hmacSha256(SecretKey)` accepts a `javax.crypto.SecretKey` directly (Guava 30+). The algorithm is standard HMAC-SHA256 (RFC 2104); there is nothing Guava-specific about the HMAC computation itself.

### UnsignedGatewayCookie JSON field order (alphabetic)

| Position | Field name    | JSON type | Notes |
|----------|---------------|-----------|-------|
| 1        | `backend`     | string or null | scheme + authority, e.g. `"https://host:8080"` |
| 2        | `deletePaths` | array of string | exact-match paths that trigger deletion |
| 3        | `name`        | string | always starts with `TG.` |
| 4        | `payload`     | string or null | free-form; currently always null |
| 5        | `priority`    | number | integer; lower = higher priority in sort |
| 6        | `routingPaths`| array of string | prefix-match paths the cookie is valid for |
| 7        | `ts`          | number | Unix milliseconds (Long) |
| 8        | `ttl`         | string | airlift Duration string, e.g. `"10.00m"` |

### Jackson null-field behavior

Jackson's default serializer includes `null`-valued fields. `payload` and `backend` can be null and will appear as `"payload":null`, `"backend":null` in the HMAC input. Do not omit null fields.

## Section 3 — Cookie Fields and Payload

Both `GatewayCookie` (the full signed object) and `UnsignedGatewayCookie` (the HMAC input) carry the same data fields (`GatewayCookie.java:202-293`):

| Field          | Type              | Purpose |
|----------------|-------------------|---------|
| `ts`           | `Long`            | Cookie creation timestamp in Unix ms |
| `name`         | `String`          | Cookie HTTP name, always prefixed `TG.` |
| `payload`      | `String` (nullable) | Free-form string; currently always null |
| `backend`      | `String` (nullable) | Target backend, format `scheme://host[:port]` |
| `routingPaths` | `List<String>`    | Path prefixes for which this cookie is valid |
| `deletePaths`  | `List<String>`    | Exact paths that trigger cookie deletion |
| `ttl`          | `Duration`        | Cookie lifetime; used for both Max-Age and expiry check |
| `priority`     | `int`             | Sort key when multiple cookies match; lower int = higher priority |
| `signature`    | `String`          | 64-char lowercase hex HMAC-SHA256; outer object only |

The `GatewayCookie` outer object carries all 9 fields including `signature`. The `UnsignedGatewayCookie` inner object carries the first 8 (no `signature`).

### Only OAuth2 cookie is issued today

The only concrete subclass, `OAuth2GatewayCookie`, is constructed with:

- `name = "TG.OAUTH2"`
- `payload = null`
- `backend = scheme + "://" + authority` of the forwarded request
- `routingPaths = ["/oauth2"] + oauth2GatewayCookieConfiguration.deletePaths`
- `deletePaths = oauth2GatewayCookieConfiguration.deletePaths` (default: `["/logout", "/oauth2/logout"]`)
- `ttl = oauth2GatewayCookieConfiguration.lifetime` (default: `Duration.valueOf("10m")`)
- `priority = 0`

Source: `OAuth2GatewayCookie.java:28-37`, `OAuth2GatewayCookieConfiguration.java:26-28`.

## Section 4 — routingPaths Coverage

### Matching rule: prefix match

`matchesRoutingPath` uses `path.startsWith(routingPath)` (`GatewayCookie.java:180`). A routing path `/oauth2` matches any request path that begins with `/oauth2`, including `/oauth2/callback`, `/oauth2/token`, and `/oauth2` itself.

### Matching rule for deletion: exact match

`matchesDeletePath` uses `deletePaths.contains(path)` (`GatewayCookie.java:184`). Only exact string equality triggers deletion. `/logout` does not match `/logout?foo=bar`.

### Default OAuth2 routing paths

The `OAuth2GatewayCookie` constructor adds the delete paths to the routing paths so that the cookie is also sent on logout requests (allowing deletion to be triggered). Constructed as:

```java
routingPaths = Streams.concat(Stream.of("/oauth2"), deletePaths.stream()).toList()
```

Default result: `["/oauth2", "/logout", "/oauth2/logout"]` (`OAuth2GatewayCookie.java:32-33`).

### No /v1/statement or /v1/spooled cookie coverage

The `TG.OAUTH2` cookie's routing paths are OAuth2 paths only. The `/v1/statement` and `/v1/spooled` paths are not covered by any current `GatewayCookie`. Statement routing sticky-ness is provided by the query-history database, not cookies (`RoutingTargetHandler.java:154-157`).

If a future `TG.*` cookie were created for statement routing, the same `startsWith` logic would apply. `/v1/statement` as a routing path would cover `/v1/statement`, `/v1/statement/20240101_000000_00001_aaaaa`, and so forth.

### `Path` cookie attribute

Neither `toCookie()` nor `toNewCookie()` calls `setPath()` or `.path()`. No `Path` attribute is present in the `Set-Cookie` header (`GatewayCookie.java:158-170`). The browser or HTTP client defaults to the request path's directory, but this is irrelevant for Trino CLI clients. The gateway validates routing applicability via `matchesRoutingPath` at read time, not via the `Path` attribute.

## Section 5 — Cookie Lifecycle

### Issue: when the gateway SETS a cookie

A `TG.OAUTH2` cookie is set on the response when **all** of the following hold (`ProxyRequestHandler.java:207-211`):

1. Cookies are enabled (`gatewayCookieConfiguration.enabled = true`).
2. The forwarded request URI starts with `/oauth2`.
3. The incoming request does not already carry a `TG.OAUTH2` cookie.

The cookie is added to the JAX-RS `Response` via `cookieBuilder` before the response is sent to the client. The cookie carries the backend as `scheme://host:port` of the target cluster URL (`ProxyRequestHandler.java:226-228`).

No `TG.*` cookie is issued for POST `/v1/statement` in the current implementation.

### Validate: when the gateway READS and verifies a cookie

In `RoutingTargetHandler.getPreviousCluster()` (`RoutingTargetHandler.java:158-171`):

1. Cookies are enabled and the request carries at least one cookie.
2. All cookies whose name starts with `TG.` are decoded (base64url + JSON parse).
3. Each decoded cookie has `isValid()` called on it.
4. `isValid()` first checks TTL: `System.currentTimeMillis() > ts + ttl.toMillis()` → `false` if expired.
5. `isValid()` then verifies HMAC: recomputes the signature over `UnsignedGatewayCookie` JSON and compares against the stored `signature`. If the signature is missing or mismatched, it **throws `IllegalArgumentException`** with message `"Invalid cookie signature"` after logging an error (`GatewayCookie.java:193-197`).
6. Cookies with an empty `backend` field are excluded (`RoutingTargetHandler.java:163`).
7. Cookies whose routing path does not prefix-match the request URI are excluded (`RoutingTargetHandler.java:164`).
8. Surviving cookies are sorted by `compareTo`: first by `priority` ascending, then by `ts` ascending for ties (`GatewayCookie.java:153-155`).
9. The **first** (lowest priority, oldest) cookie's `backend` is used as the previous cluster.

**On HMAC failure**: `isValid()` throws rather than returning false. This means a tampered cookie causes a 500 error to the client, not a silent re-route. This is confirmed by the integration test at `TestGatewayHaMultipleBackend.java:373` (`assertThat(callbackResponse.code()).isEqualTo(500)`).

### Invalidate: when the gateway DELETES a cookie

In `ProxyRequestHandler.getOAuth2GatewayCookie()` (`ProxyRequestHandler.java:213-220`):

When the incoming request carries existing `TG.*` cookies and the request does NOT match the OAuth2 issue condition (i.e., the path is not `/oauth2` or the cookie already exists), the gateway iterates over all `TG.*` cookies and checks each one:

```java
.filter(c -> !c.isValid() || c.matchesDeletePath(remoteUri.getPath()))
```

Any cookie that is **either** invalid (expired or bad signature) **or** whose delete path exactly matches the forwarded URI path gets a deletion response: `value="delete"`, `Max-Age=0`.

This means:
- Cookies expire naturally via `Max-Age` on the client side.
- The gateway actively deletes cookies when the request hits a delete path (e.g., `/logout`) or when the cookie is already expired/invalid.
- No explicit "query complete" signal triggers cookie deletion; deletion is path-triggered.

### TTL / Max-Age

`Max-Age` is set to `(int)(ttl.toMillis() / 1000)` — integer division, in seconds (`GatewayCookie.java:161, 168`). For the default `10m` lifetime, `Max-Age=600`.

## Section 6 — Go Replication Algorithm

The following pseudocode produces a bit-identical cookie value given the same inputs:

```
// Inputs:
//   name           string        (e.g. "TG.OAUTH2")
//   payload        *string       (nullable; currently always nil/null)
//   backend        *string       (nullable; e.g. "https://trino-host:8080")
//   routingPaths   []string      (e.g. ["/oauth2", "/logout", "/oauth2/logout"])
//   deletePaths    []string      (e.g. ["/logout", "/oauth2/logout"])
//   ttlMillis      int64         (duration in milliseconds, e.g. 600000 for 10m)
//   ttlString      string        (airlift format, e.g. "10.00m")
//   priority       int           (e.g. 0)
//   ts             int64         (Unix ms, e.g. time.Now().UnixMilli())
//   signingSecret  string        (from config.cookieSigningSecret)

// Step 1: Build the UnsignedGatewayCookie struct.
// Fields must serialize in ALPHABETICAL key order:
//   backend, deletePaths, name, payload, priority, routingPaths, ts, ttl

unsigned := {
    "backend":      backend,        // string or JSON null
    "deletePaths":  deletePaths,    // array
    "name":         name,           // string, must start with "TG."
    "payload":      payload,        // string or JSON null
    "priority":     priority,       // number
    "routingPaths": routingPaths,   // array
    "ts":           ts,             // number (Long)
    "ttl":          ttlString,      // string in airlift Duration format
}

// Step 2: Serialize to JSON with alphabetical key ordering, null fields included.
//   In Go: use encoding/json with a struct that has json tags in sorted order,
//   OR use a map[string]any and sort keys manually.
//   Jackson null-inclusion is the default; do NOT omit null fields.
unsignedJSON := marshalAlphabetical(unsigned)  // UTF-8 bytes

// Step 3: Compute HMAC-SHA256.
//   Key = raw UTF-8 bytes of signingSecret (NOT base64-decoded, NOT hex-decoded).
//   Input = UTF-8 bytes of unsignedJSON string.
mac := hmac.New(sha256.New, []byte(signingSecret))
mac.Write(unsignedJSON)
digest := mac.Sum(nil)  // 32 bytes

// Step 4: Encode digest as lowercase hex (64 characters).
signature := hex.EncodeToString(digest)

// Step 5: Build the full GatewayCookie JSON.
// Keys must also be alphabetical:
//   backend, deletePaths, name, payload, priority, routingPaths, signature, ts, ttl
full := {
    "backend":      backend,
    "deletePaths":  deletePaths,
    "name":         name,
    "payload":      payload,
    "priority":     priority,
    "routingPaths": routingPaths,
    "signature":    signature,
    "ts":           ts,
    "ttl":          ttlString,
}
fullJSON := marshalAlphabetical(full)

// Step 6: Base64url-encode the full JSON (WITH padding '=').
//   Go: base64.URLEncoding.EncodeToString(fullJSON)  -- NOT RawURLEncoding
cookieValue := base64.URLEncoding.EncodeToString([]byte(fullJSON))

// Step 7: Build the Set-Cookie header.
//   Name: name (e.g. "TG.OAUTH2")
//   Value: cookieValue
//   Max-Age: int(ttlMillis / 1000)
//   No Path, Domain, Secure, or HttpOnly attributes.
```

### Critical correctness constraints for wire-compat

1. **Alphabetical key order in both the HMAC input and the outer cookie JSON.** Use a Go struct with fields in the right order and `omitempty` must NOT be used (null fields must appear).
2. **Base64 URL encoding WITH padding.** `base64.URLEncoding`, not `base64.RawURLEncoding`.
3. **HMAC key is the raw secret string bytes**, not decoded or hashed.
4. **HMAC hex is lowercase.** `hex.EncodeToString` is correct; `strings.ToUpper` must not be applied.
5. **`ttl` JSON value is the airlift Duration string** (e.g. `"10.00m"`), not a number. The Go rewrite must reproduce this exact string format. The airlift format is: value formatted to 2 decimal places + abbreviated unit. Example: 600 seconds = `"10.00m"`, 3600 seconds = `"1.00h"`, 500 milliseconds = `"500.00ms"`. Choose the largest unit where the value is >= 1.
6. **`ts` is a JSON number** (Long), not a string.
7. **`null` fields serialize as `null`**, not omitted.

### airlift Duration string format

The `io.airlift.units.Duration.toString()` method formats duration using the largest unit where the value remains a whole or fractional number with 2 decimal places. Units in descending order: `d` (days), `h` (hours), `m` (minutes), `s` (seconds), `ms` (milliseconds), `us` (microseconds), `ns` (nanoseconds). The format is `"%.2f%s"` — always two decimal places.

Go implementation:

```go
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
        {float64(time.Nanosecond), "ns"},
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

## Section 7 — wireCompat Implications

If the Go gateway produces a cookie value that differs from what the Java gateway would produce for the same logical payload, the following consequences occur:

1. **Blue-green deployment session disruption**: A request routed through the Java gateway sets a `TG.OAUTH2` cookie. The client's next request lands on the Go gateway. The Go gateway reads the cookie, decodes the JSON, and calls its own HMAC verification. If the JSON it reconstructs differs from the original (even by a single character — field order, null handling, duration format), the HMAC check fails and the gateway throws `IllegalArgumentException`, returning HTTP 500 to the client.

2. **Go-to-Go is self-consistent but isolated**: If the Go gateway issues and validates its own cookies without any Java gateway in the path, any internally consistent format works. Differences only matter in mixed deployments.

3. **Affected scenarios**:
   - `wireCompat: false`: Only affects mixed Go+Java deployments. Go-only deployments are unaffected.
   - `wireCompat: true` (required for blue-green): Every byte of the cookie value must match.

4. **What causes format divergence**:
   - Different JSON key ordering (must be alphabetical).
   - `omitempty` on null fields (must be included as null).
   - `base64.RawURLEncoding` instead of `base64.URLEncoding` (missing `=` padding).
   - HMAC hex in uppercase instead of lowercase.
   - Duration serialized as nanoseconds number instead of airlift string (e.g. `600000000000` vs `"10.00m"`).
   - Secret key preprocessing (e.g., base64-decoding the secret before use) — the Java code does NOT decode it.

## Section 8 — Key Source Files

| File | Key elements |
|------|-------------|
| `gateway-ha/src/main/java/io/trino/gateway/ha/router/GatewayCookie.java` | Main class: wire format, HMAC computation (`computeSignature` at line 144), `toCookie`/`toNewCookie` at lines 158-170, `fromCookie` at line 173, `isValid` at line 188, `matchesRoutingPath` at line 178, `matchesDeletePath` at line 183, `UnsignedGatewayCookie` inner class at line 202 |
| `gateway-ha/src/main/java/io/trino/gateway/ha/router/OAuth2GatewayCookie.java` | Only concrete subclass; defines `NAME = "TG.OAUTH2"`, `OAUTH2_PATH = "/oauth2"`, and default field values |
| `gateway-ha/src/main/java/io/trino/gateway/ha/config/GatewayCookieConfiguration.java` | Key creation: `setCookieSigningSecret` wraps UTF-8 bytes in `SecretKeySpec` with algorithm `HmacSHA256` (line 43) |
| `gateway-ha/src/main/java/io/trino/gateway/ha/config/GatewayCookieConfigurationPropertiesProvider.java` | Singleton provider; `enabled` flag; `getCookieSigningKey()` |
| `gateway-ha/src/main/java/io/trino/gateway/ha/config/OAuth2GatewayCookieConfiguration.java` | Default values: `routingPaths=["/oauth2"]`, `deletePaths=["/logout","/oauth2/logout"]`, `lifetime="10m"` |
| `gateway-ha/src/main/java/io/trino/gateway/proxyserver/ProxyRequestHandler.java` | Cookie issuance (`getOAuth2GatewayCookie` at line 204) and deletion logic (lines 213-220) |
| `gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java` | Cookie validation and routing (`getPreviousCluster` at line 153); HMAC failure propagation |
| `gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java` | Initialization of both cookie configuration providers (lines 105-109) |
| `gateway-ha/src/test/resources/test-config-template.yml` | Example configuration: `enabled: true`, `cookieSigningSecret: "kjlhbfrewbyuo452cds3dc1234ancdsjh"` |
| `gateway-ha/src/test/java/io/trino/gateway/ha/TestGatewayHaMultipleBackend.java` | Integration tests: `testCookieSigning` (line 335) confirms tampered cookie returns HTTP 500; `testOAuth2Flow` tests full issue/invalidate cycle |

## Behavior vs. Implementation Artifact

### Alphabetical JSON key ordering

- **Observed behavior:** `@JsonPropertyOrder(alphabetic = true)` on both `GatewayCookie` and `UnsignedGatewayCookie` ensures deterministic JSON key ordering regardless of JVM field declaration order (`GatewayCookie.java:34, 202`).
- **Source of behavior:** `gateway-design-intent` — the ordering is intentional to make HMAC verification reproducible across JVM instances (e.g., different JVM implementations may reflect fields in different declaration order).
- **Rationale:** HMAC-SHA256 over serialized JSON requires the serialized form to be bit-identical on every node. Alphabetical ordering provides a canonical form without a separate canonicalization step.
- **Go obligation:** `replicate-exactly` — any deviation breaks cross-instance and cross-implementation cookie validation.
- **Notes:** Jackson's alphabetical ordering is by field name string sort (`String.compareTo`), which is Unicode code-point order. For ASCII field names this is the same as standard alphabetical. The field names in use are all ASCII.

### HMAC failure throws, not returns false

- **Observed behavior:** `isValid()` throws `IllegalArgumentException` on signature mismatch (`GatewayCookie.java:194-197`). The call site in `RoutingTargetHandler` uses `.filter(GatewayCookie::isValid)` which does not catch exceptions (`RoutingTargetHandler.java:162`).
- **Source of behavior:** `defensive-historical` — throwing rather than returning false means a tampered cookie surfaces as an error rather than silently falling back to unrouted behavior.
- **Rationale:** Prevents an attacker from forcing unrouted behavior by presenting an invalid cookie; they get an error instead.
- **Go obligation:** `replicate-intent` — Go should return an error (not just `false`) on signature mismatch, and the caller should propagate it as a 500 (or 400) rather than silently ignoring it.
- **Notes:** The current test confirms HTTP 500 for tampered cookies (`TestGatewayHaMultipleBackend.java:373`). Whether 400 (Bad Request) would be more appropriate is a design question for the architect.

### Base64url with padding

- **Observed behavior:** `Base64.getUrlEncoder()` (without `.withoutPadding()`) is used, so the output includes `=` padding characters (`GatewayCookie.java:160, 167`).
- **Source of behavior:** `jvm-artifact` — Java's default URL encoder includes padding; the code author did not explicitly strip it.
- **Go obligation:** `replicate-exactly` for `wireCompat: true`; use `base64.URLEncoding`. For `wireCompat: false`, `base64.RawURLEncoding` would also work in a Go-only fleet.
- **Notes:** HTTP cookie values can contain `=` but some older frameworks strip it. No issue has been observed with this in the Java implementation.

## Implications for Go Rewrite

- The Go implementation needs a canonical JSON marshaler that guarantees alphabetical key order. The standard `encoding/json` marshaler outputs struct fields in declaration order, not alphabetical. Either define structs with fields in alphabetical order, or use `encoding/json` with a `map[string]any` and sort keys before encoding.
- A Go struct-based approach is strongly preferred for wire-compat: declare struct fields in the exact alphabetical order and verify with a round-trip test against a known Java-produced cookie.
- The airlift `Duration` string format must be implemented in Go. It is not a standard library format. Keep it in a single function with a unit test comparing against known Java outputs (e.g., `600000ms → "10.00m"`, `3600000ms → "1.00h"`, `500ms → "500.00ms"`).
- `wireCompat: true` should be the default; `wireCompat: false` can allow a simplified format (e.g., nanoseconds as number) for Go-only deployments.
- HMAC key material is the **raw secret string bytes** (UTF-8), not base64-decoded. This is the most common source of HMAC incompatibility when porting between languages.
- No `Path`, `Domain`, `Secure`, or `HttpOnly` attributes are set today. The Go implementation should match this exactly for wire-compat. Adding `Secure` or `HttpOnly` in the Go rewrite would break nothing functionally but would differ from the Java behavior — defer to architect.
- The `isValid()` exception on HMAC failure should be preserved or escalated to a proper HTTP 400/500. Silent cookie ignoring would weaken the security property.

## Test Strategy Hooks

- **Test level:** unit (HMAC signing) + integration (round-trip cookie issue and validate through the gateway).
- **Fixtures required:** a static `cookieSigningSecret`, a fixed `ts` value (override `time.Now()`), fixed `backend`/`routingPaths`/`deletePaths`/`ttl`. Compare the produced base64url cookie value against a known-good value produced by running the Java code with the same inputs.
- **Observable signals:** exact `Set-Cookie` header value; HTTP 500 on tampered cookie; correct backend routing when valid cookie is present.
- **Non-determinism risks:** `ts` is set to `System.currentTimeMillis()` at construction time. Tests must inject a fixed timestamp or the HMAC input will differ on every run. The `setTs(Long)` mutator exists for exactly this purpose in Java tests (`GatewayCookie.java:139`).

## Open Questions

- `@trino-expert`: Is `/v1/spooled` routing expected to use cookies in the future, or will it continue to use query-history DB lookup like `/v1/statement`?
- `@architect`: Should the Go implementation add `Secure` and `HttpOnly` attributes when the gateway is configured for HTTPS? This improves security but breaks wire-compat with the Java gateway.
- `@architect`: The `priority` field exists in the data model but `OAuth2GatewayCookie` always sets it to `0`. Is multi-priority cookie routing intended for future use, or is it safe to simplify the Go implementation to single-cookie semantics?
- `@architect`: Should HMAC failure return HTTP 400 (client error: bad cookie) or HTTP 500 (current Java behavior)? The current 500 is technically incorrect per HTTP semantics.

## Cross-references

- `[[proxy-request-lifecycle.go-qa.md]]` — covers the broader request flow; this study focuses solely on cookie signing mechanics.
- `[[routing-engine.go-qa.md]]` — covers routing group selection; the cookie is one of several routing inputs.
- `[[jvm-dependencies-inventory.go-implementer.md]]` — Guava `Hashing`, airlift `JsonCodec`, and airlift `units.Duration` are all JVM dependencies with no direct Go equivalents; see that study for mapping notes.
