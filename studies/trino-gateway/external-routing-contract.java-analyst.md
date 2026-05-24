---
author: java-analyst
role: Java Analyst
component: trino-gateway
topics:
  - routing-engine
  - config
  - proxy-core
status: draft
risk: high
version_pins:
  trino-gateway: "submodule HEAD at time of study (2026-05-24)"
related-to:
  - trino-gateway/routing-engine.java-qa.md
  - trino-gateway/sql-parsing-for-routing.md
  - trino-gateway/mvel-rules-language.md
---

# External Routing Contract

## TL;DR

When `routingRules.rulesType: EXTERNAL` is configured, the gateway POSTs a JSON body to
an operator-supplied HTTP endpoint on every new inbound request. The body carries request
metadata plus optional SQL analysis artifacts. The endpoint returns a JSON object with the
routing group, optional header mutations, and optional error strings. This document
specifies every field on both sides precisely enough to implement the HTTP client and
design a gRPC proto without reading Java source.

---

## 1. Request Body — `RoutingGroupExternalBody`

### 1.1 Schema

Java record:
`gateway-ha/src/main/java/io/trino/gateway/ha/router/schema/RoutingGroupExternalBody.java`

The record is serialized to JSON with airlift's `JsonCodec`, which uses Jackson
under the hood. Field names map directly from the Java record component names (camelCase).
All fields are always present in the JSON object; none are omitted when null.

| JSON field | Java type | Nullable | Source |
|---|---|---|---|
| `trinoQueryProperties` | object \| `null` | yes | Populated only when `requestAnalyzerConfig.analyzeRequest = true` and SQL parsing succeeded |
| `trinoRequestUser` | object \| `null` | yes | Populated only when `requestAnalyzerConfig.analyzeRequest = true` |
| `contentType` | string | no | Hardcoded to `"application/json"` — not the original request `Content-Type` |
| `remoteUser` | string \| `null` | yes | `HttpServletRequest.getRemoteUser()` |
| `method` | string | no | HTTP method, e.g. `"POST"`, `"GET"` |
| `requestURI` | string | no | Path component only, e.g. `"/v1/statement"` |
| `queryString` | string \| `null` | yes | URL query string without the leading `?`, or null if none |
| `session` | object \| `null` | yes | `HttpServletRequest.getSession(false)` — null when no existing session |
| `remoteAddr` | string \| `null` | yes | Client IP address |
| `remoteHost` | string \| `null` | yes | Client hostname (may equal IP if no reverse DNS) |
| `parameters` | object | no | `Map<String, String[]>` — URL and form parameters; empty `{}` when none |

**Source:** `ExternalRoutingGroupSelector.java:152-163` constructs the record:

```java
return new RoutingGroupExternalBody(
    Optional.ofNullable(trinoQueryProperties),
    Optional.ofNullable(trinoRequestUser),
    "application/json",         // contentType is always this literal
    request.getRemoteUser(),
    request.getMethod(),
    request.getRequestURI(),
    request.getQueryString(),
    request.getSession(false),  // null when no session
    request.getRemoteAddr(),
    request.getRemoteHost(),
    request.getParameterMap());
```

### 1.2 Wire example — minimal (analyzeRequest=false)

```json
{
  "trinoQueryProperties": null,
  "trinoRequestUser": null,
  "contentType": "application/json",
  "remoteUser": null,
  "method": "POST",
  "requestURI": "/v1/statement",
  "queryString": null,
  "session": null,
  "remoteAddr": "10.0.0.42",
  "remoteHost": "10.0.0.42",
  "parameters": {}
}
```

### 1.3 Wire example — full (analyzeRequest=true, query parsed successfully)

```json
{
  "trinoQueryProperties": {
    "body": "SELECT * FROM tpch.tiny.orders LIMIT 10",
    "queryType": "Query",
    "resourceGroupQueryType": "SELECT",
    "defaultCatalog": "tpch",
    "defaultSchema": "tiny",
    "catalogs": ["tpch"],
    "schemas": ["tiny"],
    "catalogSchemas": ["tpch.tiny"],
    "tables": ["tpch.tiny.orders"],
    "isNewQuerySubmission": true,
    "isQueryParsingSuccessful": true,
    "errorMessage": null
  },
  "trinoRequestUser": {
    "user": "alice",
    "userInfo": null
  },
  "contentType": "application/json",
  "remoteUser": null,
  "method": "POST",
  "requestURI": "/v1/statement",
  "queryString": null,
  "session": null,
  "remoteAddr": "10.0.0.42",
  "remoteHost": "client.example.com",
  "parameters": {}
}
```

### 1.4 Request headers forwarded alongside body

In addition to the JSON body, the gateway forwards most of the original inbound
request's headers to the external service. This happens via
`ExternalRoutingGroupSelector.getValidHeaders()` (line 166-177). The `Content-Length`
header is always excluded; headers listed in `rulesExternalConfiguration.excludeHeaders`
are also excluded.

**Source:** `ExternalRoutingGroupSelector.java:72-75`

```java
this.excludeHeaders = ImmutableSet.<String>builder()
    .add("Content-Length")
    .addAll(rulesExternalConfiguration.getExcludeHeaders())
    .build();
```

The gateway adds `Content-Type: application/json; charset=utf-8` to the routing POST
request itself (line 97):

```java
Request request = preparePost()
    .addHeader(CONTENT_TYPE, JSON_UTF_8.toString())  // application/json; charset=utf-8
    .addHeaders(getValidHeaders(servletRequest))
    ...
```

---

## 2. Response Body — `ExternalRouterResponse`

### 2.1 Schema

Java record:
`gateway-ha/src/main/java/io/trino/gateway/ha/router/schema/ExternalRouterResponse.java`

| JSON field | Java type | Nullable | Semantics |
|---|---|---|---|
| `routingGroup` | string \| `null` | yes | Routing group name to select. Null or empty string falls through to `defaultRoutingGroup` |
| `errors` | array of strings \| `null` | yes | Error strings from routing service. Non-empty triggers either a 400 response (propagateErrors=true) or silent ignore (propagateErrors=false) |
| `externalHeaders` | object \| `null` | yes | `Map<String, String>` — headers to add or override on the proxied request. Null coerced to empty map in constructor |

**Source:** `ExternalRouterResponse.java:28-37`

```java
public record ExternalRouterResponse(
        @Nullable String routingGroup,
        List<String> errors,
        @Nullable Map<String, String> externalHeaders)
        implements RoutingGroupResponse
{
    public ExternalRouterResponse {
        externalHeaders = externalHeaders == null ? ImmutableMap.of() : ImmutableMap.copyOf(externalHeaders);
    }
}
```

### 2.2 Wire example — success, with header mutations

```json
{
  "routingGroup": "etl",
  "errors": [],
  "externalHeaders": {
    "X-Trino-Client-Tags": "['etl']",
    "X-Trino-Session": "query_max_memory=50GB,optimize_metadata_queries=false"
  }
}
```

### 2.3 Wire example — error, propagateErrors=true path

```json
{
  "routingGroup": null,
  "errors": ["Query references restricted table: tpch.tiny.orders"],
  "externalHeaders": {}
}
```

---

## 3. HTTP Wire Protocol

### 3.1 Request

| Property | Value |
|---|---|
| Method | `POST` |
| URL | Configured via `rulesExternalConfiguration.urlPath` |
| `Content-Type` header | `application/json; charset=utf-8` |
| Additional headers | Original inbound request headers minus `Content-Length` and the `excludeHeaders` list |
| Body | UTF-8 JSON of `RoutingGroupExternalBody` |

**Source:** `ExternalRoutingGroupSelector.java:95-101`

### 3.2 Expected response

- HTTP status `200 OK` is the success path. The body is parsed as `ExternalRouterResponse` JSON.
- Any non-200 status causes the airlift `JsonResponseHandler` to throw a `RuntimeException`,
  which is caught at line 135 (`catch (Exception e)`) and triggers the fallback to the
  `X-Trino-Routing-Group` header.
- There is no explicit status code enumeration in the routing selector; any exception
  from `httpClient.execute()` (network error, non-200, parse failure) falls through to
  the same fallback path (section 5).

**Source:** `ExternalRoutingGroupSelector.java:104-140`

### 3.3 Timeout and retry

The HTTP client is airlift's `HttpClient`, bound with the qualifier `@ForRouter`
(`BaseApp.java:192`). It is configured via the `serverConfig` section using the prefix
`router`:

```yaml
serverConfig:
  router.http-client.request-timeout: 1s
```

Refer to [Trino HTTP client properties](https://trino.io/docs/current/admin/properties-http-client.html)
for the full list of airlift HTTP client options. The defaults apply when not overridden.
There is **no retry logic** in the routing selector itself — a single `httpClient.execute()`
call is made and the result is used as-is.

**Source:** `ExternalRoutingGroupSelector.java:104`, `BaseApp.java:192`, `docs/routing-rules.md:64-73`

---

## 4. `externalHeaders` and `excludeHeaders` Policy

### 4.1 `externalHeaders` — semantics

`externalHeaders` returned by the routing service are applied to the inbound
request **before it is proxied to the Trino cluster**. The application is an
OVERRIDE/ADD, not a replacement:

- If the key already exists in the inbound request, the value from `externalHeaders`
  replaces the existing value.
- If the key is new, it is added.
- Original headers not referenced in `externalHeaders` are left unchanged.

This is implemented by wrapping `HttpServletRequest` with a
`HeaderModifyingRequestWrapper` (inner class in `RoutingTargetHandler.java:114-151`).
The wrapper overrides `getHeader()`, `getHeaders()`, and `getHeaderNames()` to inject
the custom headers while preserving all originals.

**Source:** `RoutingTargetHandler.java:102-108`, `RoutingTargetHandler.java:114-151`

### 4.2 `excludeHeaders` — two distinct roles

`excludeHeaders` is configured under `rulesExternalConfiguration.excludeHeaders` and
serves **two separate filtering purposes**:

1. **Routing POST request:** Headers in this list are stripped from the original inbound
   request headers before they are forwarded to the external routing service.
   (`ExternalRoutingGroupSelector.getValidHeaders()`, line 166-177)

2. **`externalHeaders` application:** After the routing service returns its response,
   any key in `externalHeaders` that is also in `excludeHeaders` is silently dropped
   before the header is applied to the proxied request.
   (`ExternalRoutingGroupSelector.java:121-129`)

`Content-Length` is always excluded from the routing POST (hardcoded at line 72-75).
It is NOT automatically excluded from `externalHeaders` application — only the explicit
list applies there.

**Configuration example (from `docs/routing-rules.md`):**

```yaml
routingRules:
  rulesEngineEnabled: true
  rulesType: EXTERNAL
  rulesExternalConfiguration:
    urlPath: https://router.example.com/gateway-rules
    excludeHeaders:
      - 'Authorization'
      - 'Accept-Encoding'
    propagateErrors: false
```

**Source:** `ExternalRoutingGroupSelector.java:72-75`, `ExternalRoutingGroupSelector.java:119-133`

---

## 5. `propagateErrors` Behavior

Full decision tree for `ExternalRoutingGroupSelector.findRoutingDestination()`:

```
httpClient.execute() called
│
├── Throws Exception (network error, non-200, JSON parse failure, etc.)
│   ├── Is a WebApplicationException? → rethrow immediately (bubbles to client)
│   └── Otherwise → log error, fall back to X-Trino-Routing-Group header value
│
└── Returns ExternalRouterResponse response
    │
    ├── response == null → throw RuntimeException("Unexpected response: null")
    │   → caught by outer catch → fall back to X-Trino-Routing-Group header
    │
    └── response != null
        │
        ├── response.errors() is non-null and non-empty?
        │   ├── propagateErrors == true
        │   │   → log warning, throw WebApplicationException(400 BAD_REQUEST)
        │   │     with errors array as response entity → client receives HTTP 400
        │   └── propagateErrors == false
        │       → silently ignore errors, continue routing
        │         (routingGroup from response is still used if present)
        │
        └── Build filteredHeaders from externalHeaders
            (skip keys in excludeHeaders, skip null values)
            → return RoutingSelectorResponse(routingGroup, filteredHeaders)
```

Downstream in `RoutingTargetHandler.getRoutingTargetResponse()`:
- If `routingGroup` is null or empty string → use `defaultRoutingGroup`
- If `routingGroup` is set → use it to find a backend cluster

**Source:** `ExternalRoutingGroupSelector.java:92-141`, `RoutingTargetHandler.java:89-108`

---

## 6. `trinoQueryProperties` — Field-by-Field Analysis

### 6.1 All JSON fields

| JSON key | Type | Requires trino-parser | Notes |
|---|---|---|---|
| `body` | string | no (raw string) | Raw SQL text from request body. Empty string `""` if body absent or oversized. |
| `queryType` | string | **yes** | Java AST node class name, e.g. `"Query"`, `"CreateTable"`, `"Insert"`. Empty `""` if parse failed. |
| `resourceGroupQueryType` | string | **yes** | Resource-group classification: `"SELECT"`, `"INSERT"`, `"DATA_DEFINITION"`, etc. Empty if parse failed. |
| `defaultCatalog` | string \| null | no | Value of `X-Trino-Catalog` header. Absent → null (serialized as JSON null). |
| `defaultSchema` | string \| null | no | Value of `X-Trino-Schema` header. Absent → null. |
| `catalogs` | array of strings | **yes** | Set of catalog names referenced in the SQL. Empty array `[]` if parse failed. |
| `schemas` | array of strings | **yes** | Set of schema names referenced. Empty array if parse failed. |
| `catalogSchemas` | array of strings | **yes** | Set of `"catalog.schema"` strings. Empty if parse failed. |
| `tables` | array of strings | **yes** | Set of fully-qualified table names `"catalog.schema.table"`. Serialized via custom `QualifiedNameJsonSerializer` which calls `QualifiedName.toString()`. Empty if parse failed. |
| `isNewQuerySubmission` | boolean | no | `true` if the request is a POST (new query submission). |
| `isQueryParsingSuccessful` | boolean | **yes** | `true` if `errorMessage` is empty. Derived field. |
| `errorMessage` | string \| null | **yes** | Parse error message if SQL parsing failed, null otherwise. Serialized as JSON string or null. |

**Fields NOT serialized (marked `@JsonIgnore`):**
- `queryId` — extracted for `kill_query` procedure routing, not exposed externally.

### 6.2 Go v1 strategy

Because Go v1 does not embed `trino-parser`, the fields that require SQL parsing cannot
be populated. Go v1 should populate the `trinoQueryProperties` object as follows when
`analyzeRequest = true`:

| Field | Go v1 value |
|---|---|
| `body` | Raw request body string (read and buffer the body) |
| `queryType` | `""` (empty) |
| `resourceGroupQueryType` | `""` (empty) |
| `defaultCatalog` | Value of `X-Trino-Catalog` header, or null |
| `defaultSchema` | Value of `X-Trino-Schema` header, or null |
| `catalogs` | `[]` (empty array) |
| `schemas` | `[]` (empty array) |
| `catalogSchemas` | `[]` (empty array) |
| `tables` | `[]` (empty array) |
| `isNewQuerySubmission` | `true` if method is POST |
| `isQueryParsingSuccessful` | `false` (no parser) |
| `errorMessage` | `"trino-parser not available in Go v1"` or null |

External routing services that depend on `queryType`, `catalogs`, `schemas`, `tables`,
or `catalogSchemas` will receive empty values from Go v1. Operators using these fields
must be aware of this limitation.

**Source:** `TrinoQueryProperties.java:119-146`, `TrinoQueryProperties.java:515-643`

### 6.3 `trinoRequestUser` fields

| JSON key | Type | Notes |
|---|---|---|
| `user` | string \| null | Extracted username, or null if extraction failed. Serialized as JSON string inside an Optional wrapper — Jackson renders `Optional.of("alice")` as `"alice"` and `Optional.empty()` as `null`. |
| `userInfo` | string \| null | OpenID Connect `UserInfo` JSON string. Non-null only when `oauthTokenInfoUrl` is configured and token exchange succeeded. Serialized via `UserInfoJsonSerializer` as a JSON string (the `UserInfo.toJSONString()` output), or null if absent. |

Username extraction priority order (source: `TrinoRequestUser.java:132-146`):

1. `X-Trino-User` header
2. `Authorization: Basic <base64>` — extracts the username portion before `:`
3. `Authorization: Bearer <token>` — tries JWT claim (`tokenUserField`, default `email`), then OIDC UserInfo endpoint
4. `Trino-UI-Token` or `__Secure-Trino-ID-Token` cookie — decoded as JWT, extracts `tokenUserField` claim

**Source:** `TrinoRequestUser.java:51-99`

---

## 7. Proposed gRPC Proto

The following proto3 definition mirrors the Java wire contract. Field numbers are chosen
to be stable. Naming uses snake_case throughout (proto3 convention).

```proto
syntax = "proto3";

package trino.gateway.routing.v1;

option go_package = "github.com/trino-goway/gen/routing/v1;routingv1";

// ExternalRoutingService is the gRPC equivalent of the HTTP POST routing endpoint.
// The gateway calls Route() once per new inbound request.
service ExternalRoutingService {
  rpc Route(RoutingRequest) returns (RoutingResponse);
}

// RoutingRequest mirrors RoutingGroupExternalBody.
message RoutingRequest {
  // Nullable — present only when analyzeRequest=true
  optional TrinoQueryProperties trino_query_properties = 1;
  // Nullable — present only when analyzeRequest=true
  optional TrinoRequestUser trino_request_user = 2;

  // Always "application/json" in current Java implementation.
  string content_type = 3;

  // Nullable — HttpServletRequest.getRemoteUser()
  optional string remote_user = 4;

  // HTTP method: "POST", "GET", etc.
  string method = 5;

  // Path component of the request URI, e.g. "/v1/statement"
  string request_uri = 6;

  // URL query string without leading "?", absent if none.
  optional string query_string = 7;

  // HttpSession attributes. Absent when getSession(false) returns null.
  // In practice almost always absent for Trino client requests.
  optional HttpSessionInfo session = 8;

  // Client IP address.
  optional string remote_addr = 9;

  // Client hostname.
  optional string remote_host = 10;

  // URL and form parameters. Key = parameter name, values = all values for that name.
  map<string, StringList> parameters = 11;

  // Inbound request headers forwarded to the routing service (after excludeHeaders filtering).
  // Included for gRPC parity with the HTTP variant where original headers are forwarded.
  map<string, StringList> headers = 12;
}

// TrinoQueryProperties mirrors the serialized form of TrinoQueryProperties.java.
message TrinoQueryProperties {
  // Raw SQL body. Empty when body is absent or exceeds maxBodySize.
  string body = 1;

  // Java AST class name, e.g. "Query", "CreateTable". Empty when parsing fails.
  string query_type = 2;

  // Resource group type: "SELECT", "INSERT", "DATA_DEFINITION", etc. Empty when parsing fails.
  string resource_group_query_type = 3;

  // X-Trino-Catalog header value. Absent if header not present.
  optional string default_catalog = 4;

  // X-Trino-Schema header value. Absent if header not present.
  optional string default_schema = 5;

  // Catalogs referenced in SQL. Empty when parsing fails.
  repeated string catalogs = 6;

  // Schemas referenced in SQL. Empty when parsing fails.
  repeated string schemas = 7;

  // "catalog.schema" pairs referenced in SQL. Empty when parsing fails.
  repeated string catalog_schemas = 8;

  // Fully-qualified table names "catalog.schema.table". Empty when parsing fails.
  repeated string tables = 9;

  // True when the request is a POST (new query submission).
  bool is_new_query_submission = 10;

  // True when SQL parsing succeeded (no errorMessage).
  bool is_query_parsing_successful = 11;

  // Parse error message. Absent on success.
  optional string error_message = 12;
}

// TrinoRequestUser mirrors the serialized form of TrinoRequestUser.java.
message TrinoRequestUser {
  // Extracted username. Absent if not found.
  optional string user = 1;

  // OpenID Connect UserInfo as a JSON string. Absent unless OIDC token exchange succeeded.
  optional string user_info = 2;
}

// HttpSessionInfo carries the session attributes when a session exists.
// In practice this is almost always absent for Trino client requests.
message HttpSessionInfo {
  string id = 1;
  int64 creation_time = 2;
  int64 last_accessed_time = 3;
  map<string, string> attributes = 4;
}

// StringList wraps a repeated string for use in maps.
message StringList {
  repeated string values = 1;
}

// RoutingResponse mirrors ExternalRouterResponse.
message RoutingResponse {
  // Target routing group. Empty string or absent → use defaultRoutingGroup.
  optional string routing_group = 1;

  // Error messages from the routing service. Non-empty + propagateErrors=true → HTTP 400.
  repeated string errors = 2;

  // Headers to add or override on the request before forwarding to Trino.
  // Keys in excludeHeaders are ignored by the gateway.
  map<string, string> external_headers = 3;
}
```

---

## 8. Key Source Files

| Class | Path |
|---|---|
| `RoutingGroupExternalBody` | `gateway-ha/src/main/java/io/trino/gateway/ha/router/schema/RoutingGroupExternalBody.java` |
| `ExternalRouterResponse` | `gateway-ha/src/main/java/io/trino/gateway/ha/router/schema/ExternalRouterResponse.java` |
| `ExternalRoutingGroupSelector` | `gateway-ha/src/main/java/io/trino/gateway/ha/router/ExternalRoutingGroupSelector.java` |
| `TrinoQueryProperties` | `gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoQueryProperties.java` |
| `TrinoRequestUser` | `gateway-ha/src/main/java/io/trino/gateway/ha/router/TrinoRequestUser.java` |
| `RoutingSelectorResponse` | `gateway-ha/src/main/java/io/trino/gateway/ha/router/schema/RoutingSelectorResponse.java` |
| `RoutingGroupSelector` (interface) | `gateway-ha/src/main/java/io/trino/gateway/ha/router/RoutingGroupSelector.java` |
| `RoutingTargetHandler` | `gateway-ha/src/main/java/io/trino/gateway/ha/handler/RoutingTargetHandler.java` |
| `RulesExternalConfiguration` | `gateway-ha/src/main/java/io/trino/gateway/ha/config/RulesExternalConfiguration.java` |
| `RequestAnalyzerConfig` | `gateway-ha/src/main/java/io/trino/gateway/ha/config/RequestAnalyzerConfig.java` |
| `RoutingRulesConfiguration` | `gateway-ha/src/main/java/io/trino/gateway/ha/config/RoutingRulesConfiguration.java` |
| `RulesType` | `gateway-ha/src/main/java/io/trino/gateway/ha/config/RulesType.java` |
| `HaGatewayProviderModule` | `gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java` |
| `BaseApp` | `gateway-ha/src/main/java/io/trino/gateway/baseapp/BaseApp.java` |
| Routing rules docs | `docs/routing-rules.md` |
| Unit tests | `gateway-ha/src/test/java/io/trino/gateway/ha/router/TestExternalRoutingGroupSelector.java` |
| Integration tests | `gateway-ha/src/test/java/io/trino/gateway/ha/handler/TestRoutingTargetHandler.java` |
