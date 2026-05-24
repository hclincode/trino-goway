---
title: authentication and authorization in trino-gateway
author: java-analyst
role: Java Analyst
component: trino-gateway
topics: [auth, mgmt-api, session-state]
date: 2026-05-24
status: draft
risk: high
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/architecture-overview.md
  - trino-gateway/jvm-dependencies-inventory.md
---

# Authentication and Authorization in trino-gateway

## Summary

trino-gateway has **two completely independent identity surfaces** in one process: (1) a gateway-admin surface that authenticates operators visiting the management REST/HTML API and gates each endpoint by a role (`ADMIN`/`USER`/`API`); and (2) a proxied-Trino surface where the gateway does **no authentication of its own** and simply forwards `Authorization` / `X-Trino-User` headers (and any session cookies) untouched to the chosen backend — the backend's own Trino auth stack decides accept or reject. The admin surface supports four modes (`oauth`, `form`, both, or `none`) toggled by which YAML config blocks are present; modes can stack via a fall-through chain. Role membership is resolved from one of two sources: a YAML-listed preset-users map, or LDAP (`memberOf` lookup keyed by the authenticated subject). This file is the spec; the Go rewrite must reproduce the wire behavior of all four modes and the role-mapping rules but is free to discard the JAX-RS plumbing.

## Key Findings

### Two identity surfaces, one process

Read this first; it is the single thing the Go rewrite is most likely to get wrong.

- **Surface 1 — gateway admin API.** All REST resources annotated with `@RolesAllowed(...)` are gated by an auth filter. The filter chain is bound in `HaGatewayProviderModule.configure` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/module/HaGatewayProviderModule.java:92-97`): if YAML key `authentication:` is present, `ChainedAuthFilter` is bound as the single `ContainerRequestFilter`; if absent, `NoopFilter` is bound (which fabricates a synthetic principal with all roles). The dynamic feature `ResourceSecurityDynamicFeature` then attaches that filter only to JAX-RS resources/methods that carry a `@RolesAllowed` annotation (`gateway-ha/src/main/java/io/trino/gateway/ha/security/ResourceSecurityDynamicFeature.java:39-45`).
- **Surface 2 — proxied Trino traffic.** The reverse-proxy endpoint (`RouteToBackendResource` in `io.trino.gateway.proxyserver`) is **not** annotated with `@RolesAllowed`. The `ResourceSecurityDynamicFeature` therefore does not install the auth filter onto it, so requests on `/v1/statement`, `/v1/info`, `/v1/query/...`, etc. flow through with no gateway-side authentication. Two `@PreMatching` filters in the security package (`QueryUserInfoParser`, `QueryMetadataParser`) inspect the request to extract a Trino user identity and parse the SQL — but they only *populate request properties* for the router (`gateway-ha/src/main/java/io/trino/gateway/ha/security/QueryUserInfoParser.java:59-74`, `gateway-ha/src/main/java/io/trino/gateway/ha/security/QueryMetadataParser.java:60-87`); they neither accept nor reject the request. The actual authentication of proxied requests is the backend Trino cluster's responsibility, with the gateway forwarding `Authorization` and `X-Trino-*` headers verbatim.

### The four authentication modes

Selected by which sub-blocks the YAML `authentication:` section contains. ChainedAuthFilter's constructor (`gateway-ha/src/main/java/io/trino/gateway/ha/security/util/ChainedAuthFilter.java:46-80`) builds an `ImmutableList<ContainerRequestFilter>` whose contents depend on which sub-managers were Guice-provided (each provider returns `null` when its config block is absent — see `HaGatewayProviderModule.java:130-150`).

| Mode | YAML present | Filters in chain (in order) | Token source per request |
|---|---|---|---|
| `none` | no `authentication:` key | `NoopFilter` only | n/a — principal is fabricated as `("user", "ADMIN_USER_API")` and `isUserInRole` returns true for every role (`NoopFilter.java:35-60`) |
| `oauth` only | `authentication.oauth: ...` | `LbFilter(LbAuthenticator)` | Either the `token` cookie or the `Authorization: Bearer <jwt>` header (`LbFilter.java:60-64`); JWT is verified against JWKS from `oauth.jwkEndpoint` |
| `form` only | `authentication.form: ...` | `LbFilter(FormAuthenticator)` then `BasicAuthFilter` | First attempt: `token` cookie or `Bearer` header (a self-signed JWT issued by the gateway's `/login` endpoint after form login); second attempt: `Authorization: Basic ...` (`ChainedAuthFilter.java:67-78`) |
| Both `oauth` and `form` | both sub-blocks present | `LbFilter(LbAuthenticator)` then `LbFilter(FormAuthenticator)` then `BasicAuthFilter` | All three are tried in order; first to succeed wins (`ChainedAuthFilter.java:82-96`) |

Fall-through semantics: each filter in the chain is invoked; if it throws any exception, the loop swallows the exception and tries the next one. If every filter fails, the chain throws `ForbiddenException("Authentication error")` (`ChainedAuthFilter.java:82-96`). The swallowed exception is **not logged** (`catch (Exception _)`) — operators will see only the final 403, which is an observable behavior tests can pin.

### What happens after authentication succeeds

Each filter installs a JAX-RS `SecurityContext` whose `getUserPrincipal()` returns an `LbPrincipal(name, memberOf)`. `name` is the user id; `memberOf` is an optional regex-matchable string of group memberships (see role mapping below). `isUserInRole(role)` is then evaluated lazily, **per-request, per-role-check**, by `LbAuthorizer.authorize` (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbAuthorizer.java:33-48`):

- `ADMIN` → `principal.memberOf` matches `configuration.getAdmin()` (a regex)
- `USER` → matches `configuration.getUser()`
- `API` → matches `configuration.getApi()`
- any other role → returns false and logs a warning

When `authorization:` is absent from YAML, `NoopAuthorizer` is bound instead and `isUserInRole` returns true for every role (`HaGatewayProviderModule.java:122-128`, `NoopAuthorizer.java:24-30`). The `Authorizer` interface is in `io.trino.gateway.ha.security.util.Authorizer`.

### Where roles come from (`memberOf` resolution)

There are two sources, resolved in this order by `AuthorizationManager.getPrivileges(username)` (`gateway-ha/src/main/java/io/trino/gateway/ha/security/AuthorizationManager.java:43-56`):

1. **Preset users** — `presetUsers` map from YAML key `presetUsers:` (a map of username → `UserConfiguration(privileges, password)`). If `username` is in this map, its `privileges` string becomes the principal's `memberOf`.
2. **LDAP** — only if no preset-user entry was found and `authorization.ldapConfigPath` is set. The gateway runs `ldapConnectionTemplate.search(...)` against `ldapUserBaseDn` with filter `ldapUserSearch` (with `${USER}` substituted), then reads the `ldapGroupMemberAttribute` attribute and uses `entry.get(memberOf).toString()` as the `memberOf` string (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbLdapClient.java:102-120`, `:137-153`).

Notes: the `LbLdapClient.UserEntryMapper.map` implementation calls `entry.get(memberOf).toString()` without joining — this means the string form of an Apache Directory `Attribute`, which is implementation-specific. Tests that depend on multi-group LDAP users should pin this behavior carefully.

The **OAuth path bypasses `AuthorizationManager`** when `oauth.privilegesField` is set: `LbAuthenticator.authenticate` reads that claim directly from the JWT, joining list values with `_` to form the `memberOf` string (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbAuthenticator.java:51-79`). If `privilegesField` is absent the OAuth path falls back to `authorizationManager.getPrivileges(userId)` (`LbAuthenticator.java:81-86`).

### Role annotations on the gateway-admin surface

The current set of `@RolesAllowed` annotations (canonical inventory; see `grep -rn '@RolesAllowed' gateway-ha/src/main/java/io/trino/gateway/ha/resource/`):

- `GatewayResource` (class-level) — `API` — `POST/GET /gateway/...` backend CRUD via the legacy resource
- `HaGatewayResource` (class-level) — `API` — `POST /gateway/backend/modify/...` backend modify endpoints
- `EntityEditorResource` (class-level) — `ADMIN` — `/entity` admin entity editor
- `GatewayWebAppResource` (per-method) — mix of `USER` (read endpoints) and `ADMIN` (mutation endpoints); 10 methods total
- `GatewayViewResource` (per-method) — `USER` on `api/queryHistory`, `api/activeBackends`, `api/queryHistoryDistribution`
- `LoginResource.restUserinfo` — `USER` (everything else in `LoginResource` is unprotected so login can bootstrap)

`PublicResource` (`/api/public/backends*`) has **no** `@RolesAllowed` and is therefore unauthenticated regardless of mode (`gateway-ha/src/main/java/io/trino/gateway/ha/resource/PublicResource.java:28-65`). The Go rewrite must preserve this — it is the only stable way for external load balancers / monitoring to see backend state without credentials.

### Token issuance for form-login

When a user POSTs `/login` with `{username, password}`, the gateway:

1. Validates credentials by checking LDAP if `form.ldapConfigPath` is set, then falling back to the preset-users map (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbFormAuthManager.java:142-158`).
2. Mints a self-signed RS256 JWT with `issuer="self"`, `subject=username`, signed using `form.selfSignKeyPair.privateKeyRsa` (`LbFormAuthManager.java:117-140`). Keys are loaded once at startup via Bouncy Castle from PEM files (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbKeyProvider.java:40-77`).
3. Returns `{"token": "<jwt>"}` in the response body. The client is expected to set this as the `token` cookie or pass it as `Bearer` in `Authorization` on subsequent requests.

Verification: `FormAuthenticator` → `LbFormAuthManager.getClaimsFromIdToken` → `LbTokenUtil.validateToken` with the gateway's own public key as the JWKS, `iss="self"`, no audience claim (`LbFormAuthManager.java:102-115`, `gateway-ha/src/main/java/io/trino/gateway/ha/security/LbTokenUtil.java:36-57`).

### OAuth / OIDC flow

Authorization-Code with PKCE-style nonce hashing (not strict PKCE — see below). Driven by `LbOAuthManager` (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbOAuthManager.java`):

1. `POST /sso` → `LbOAuthManager.getAuthorizationCodeResponse()` (`LoginResource.java:72-82`, `LbOAuthManager.java:140-156`). Builds an OIDC `AuthenticationRequest` (Nimbus SDK) with a random `state` and `nonce`, then returns `200 OK` with a `Result` body containing the IdP URL the SPA should redirect to, **plus** an `__Secure-Trino-Gateway-OIDC` cookie carrying `state|sha256(nonce)` (`OidcCookie.java:37-49`). Cookie is `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/oidc/callback`, 15-minute max age.
2. `GET /oidc/callback?code=...&state=...` → `LoginResource.callback` reads the OIDC cookie, verifies `state` matches and a `nonce` is present, then calls `LbOAuthManager.exchangeCodeForToken` (`LoginResource.java:84-105`, `LbOAuthManager.java:95-133`). This POSTs the code to `oauth.tokenEndpoint` using HTTP Basic with `(clientId, clientSecret)`, parses the OIDC response, validates the id_token's `nonce` claim equals `sha256(originalNonce)`, then returns `302` to `oauth.redirectWebUrl` (or `/`) with a `Set-Cookie: token=<idToken>; Secure; Path=/; Max-Age=86400` (`SessionCookie.java:26-36`).
3. Subsequent requests carry the `token` cookie. `LbAuthenticator.authenticate(idToken)` → `LbOAuthManager.getClaimsFromIdToken(idToken)` re-fetches the JWKS from `oauth.jwkEndpoint` per request via `UrlJwkProvider` (no caching — see "Behavior vs. Artifact" below).

### Logout

`POST /logout` returns `{"result":"ok"}` and **does not clear the token cookie** (`LoginResource.java:123-130`). `SessionCookie.logOut()` exists and constructs a clearing cookie (`SessionCookie.java:38-51`) but is unused. This appears to be an intentional client-side-logout design (the SPA clears its own state) — flag for `@architect` and `@trino-expert` confirmation.

### `loginType` endpoint

`POST /loginType` returns `{"loginType": "form" | "oauth" | "none"}` based on which managers were bound (`LoginResource.java:169-186`). The SPA uses this to decide which login UI to show. If both are configured, **`form` wins** — meaning a deployment configured for both will present form-login UI to humans by default.

### `LbUnauthorizedHandler` redirect target

When an `LbFilter` authentication attempt fails *and the chain has no further filters to try*, the `WebApplicationException` it throws carries a `302` to either `/sso` or `/login` depending on `authentication.defaultType` (`gateway-ha/src/main/java/io/trino/gateway/ha/security/LbUnauthorizedHandler.java:24-37`). Important detail: in `ChainedAuthFilter` this 302 is caught and **discarded** along with all other exceptions; the chain ultimately throws `ForbiddenException` which becomes a 403 (handled by `AuthorizedExceptionMapper`). The 302 only escapes when an `LbFilter` is the *sole* filter on a route — not via `ChainedAuthFilter` today. Tests should pin this: in current code the redirect-to-login UX exists only as latent code, not as observable behavior on a `ChainedAuthFilter` deployment. Likely a bug; flag for `@architect`.

### `AuthorizedExceptionMapper` swallows 403 → 200

`AuthorizedExceptionMapper` (`gateway-ha/src/main/java/io/trino/gateway/ha/security/AuthorizedExceptionMapper.java:24-35`) maps `ForbiddenException` whose message equals Jersey's localised `USER_NOT_AUTHORIZED()` string into a `200 OK` with body `{"code":401,"msg":"...","data":null}`. **The HTTP status is `200`, not `401`** — the 401 is conveyed only inside the JSON payload. Tests written against status codes alone will silently pass for unauthorized responses. This is a strong "replicate-exactly" obligation for the Go rewrite if the SPA depends on it.

### `QueryUserInfoParser` / `QueryMetadataParser` (proxy-side filters, not auth)

These two `@PreMatching` filters (`QueryUserInfoParser.java:39-40`, `QueryMetadataParser.java:38-39`) run with `@Priority` `PRE_AUTHENTICATION=500` and `PRE_AUTHORIZATION=1500` respectively (`GatewayFilterPriorities.java:18-22`, both lower than JAX-RS `AUTHENTICATION=2000` so they run first). They:

- inspect path through `PathFilter.isPathWhiteListed` (statement paths, `/v1/query`, `/ui`, `/v1/info`, `/v1/node`, `/ui/api/stats`, `/oauth2`, plus any operator regex);
- on a whitelisted path, parse a `TrinoRequestUser` (from `Authorization`/`X-Trino-User`) and a `TrinoQueryProperties` (parsed SQL) and stash them as request properties for downstream routing to read.

They are **inputs to the router, not authentication**. The Go rewrite needs these for routing rules but should not confuse them with admin auth.

## Behavior vs. Implementation Artifact

### Chain order: OAuth → Form-JWT → Basic

- **Observed behavior:** when both modes are configured, every request is tried as OAuth bearer JWT first, then form-self-signed-JWT, then basic-auth credentials. First success wins; no caching of which mode worked for a given client.
- **Source of behavior:** `gateway-design-intent`.
- **Rationale:** lets one deployment serve both human SSO traffic and machine-to-machine API traffic over the same routes with the same `@RolesAllowed` annotations.
- **Go obligation:** `replicate-exactly`. Reordering would change which credential type wins for clients that happen to send multiple.
- **Notes:** the `catch (Exception _)` blanket swallow (`ChainedAuthFilter.java:91`) hides everything including configuration errors and IO failures while contacting the JWKS endpoint. The Go rewrite should preserve the fall-through semantics but consider logging swallowed errors at DEBUG.

### JWKS fetched per request, no caching

- **Observed behavior:** `LbOAuthManager.getClaimsFromIdToken` constructs a fresh `UrlJwkProvider(jwkEndpoint.toURL())` on every authenticated request (`LbOAuthManager.java:172`).
- **Source of behavior:** `jvm-artifact` plus likely oversight. The auth0 `UrlJwkProvider` has variants with caching/rate-limiting but they are not used here.
- **Rationale:** unclear; likely never benchmarked. With many concurrent OAuth clients this becomes an external HTTP round-trip per request to the IdP's JWKS endpoint.
- **Go obligation:** `replicate-intent`. The Go rewrite should validate the same JWTs against the same JWKS but with a TTL-cached, refresh-on-kid-miss provider. This is a behavior change but matches what every other gateway does and removes a real availability dependency.
- **Notes:** flag for `@architect` confirmation that we treat this as a defect to fix rather than a contract to preserve.

### Failed-auth `302` not reachable through `ChainedAuthFilter`

- **Observed behavior:** `LbUnauthorizedHandler.buildResponse()` returns `302 -> /sso` or `/login`, but `ChainedAuthFilter` catches and discards it, so the chain only ever emits `403` from `ForbiddenException`.
- **Source of behavior:** `defensive-historical`. Earlier code may have used `LbFilter` directly without chaining; the redirect-to-login UX was lost when chaining was introduced.
- **Rationale:** unclear; likely accidental.
- **Go obligation:** `defer-to-expert`. Need `@architect` decision: replicate the 403-only behavior, or restore the 302 redirect (better UX, possible SPA breakage).

### `POST /logout` is a no-op

- **Observed behavior:** the endpoint returns `{"result":"ok"}` without clearing the `token` cookie (`LoginResource.java:123-130`); the working clearing-cookie code in `SessionCookie.logOut()` is dead.
- **Source of behavior:** `gateway-design-intent` (likely — client-side-only logout SPA), but `unclear` because `SessionCookie.logOut()` exists and is never called.
- **Rationale:** unknown. Trino-gateway's SPA may delete the cookie client-side.
- **Go obligation:** `defer-to-expert`. If the SPA expects server-side cookie clearing, the Go rewrite should do it; otherwise replicate the no-op.

### `AuthorizedExceptionMapper` returns `200` carrying `401` JSON

- **Observed behavior:** `ForbiddenException` matching Jersey's localized `USER_NOT_AUTHORIZED()` message becomes `HTTP 200 OK` with `{"code":401, ...}` body.
- **Source of behavior:** `defensive-historical` — typical of older SPAs that need to read JSON error bodies even from unauthorized responses without triggering browser auth prompts.
- **Rationale:** SPA consumption convenience.
- **Go obligation:** `replicate-exactly` while the existing SPA is in use; revisit when the SPA can handle `401` status directly.

### Synthetic `("user", "ADMIN_USER_API")` principal in `none` mode

- **Observed behavior:** `NoopFilter` installs a principal with that exact username and a `memberOf` of `"ADMIN_USER_API"`, and `isUserInRole(anything)` returns true.
- **Source of behavior:** `gateway-design-intent` — anonymous-everything dev mode.
- **Rationale:** local dev / smoke tests / behind-network-perimeter deployments.
- **Go obligation:** `replicate-intent`. The actual string `"user"` and `"ADMIN_USER_API"` are unlikely to be observed by anything other than the audit-log code if it exists; the Go equivalent should produce a principal that satisfies all role checks.

## Implications for Go Rewrite

- **Treat the two surfaces as truly independent.** The auth code in the Go rewrite should be a thin filter around the admin REST handlers and should not touch the proxy data path. Conflating them is the largest design risk.
- **Mode selection lives in config, not in code paths.** A clean Go design is: build a single `chainAuthenticator` whose constituent links are determined entirely by which YAML sub-blocks exist (`oauth`, `form`, neither). Reproduces the existing semantics without per-mode branching at the call sites.
- **The `Authorizer` interface is one method (`authorize(principal, role, ctx) bool`).** Map cleanly to a Go function type.
- **Role mapping is regex-against-`memberOf`.** Avoid recreating the Apache Directory `Attribute.toString()` semantics; instead specify in the Go rewrite that `memberOf` for LDAP-sourced principals is the joined attribute values in some canonical form. Flag for QA differential testing.
- **Replace the per-request JWKS fetch.** The Go rewrite should use a TTL-caching JWKS client (e.g. `github.com/MicahParks/keyfunc`) with refresh-on-unknown-kid. This is a behavior change worth making explicit.
- **Preserve the `200`/`401`-in-body and the `403`-no-redirect quirks** until SPA work removes the need; both are observable behaviors current clients may depend on.
- **The `LbKeyProvider` uses Bouncy Castle to read PKCS#8 / X.509 PEM.** Go's standard `crypto/x509` + `crypto/rsa` covers this without a third-party dep.
- **Do not port `LbLdapClient.UserEntryMapper`'s `entry.get(memberOf).toString()` literally.** Specify the intended Go behavior (e.g. join multiple values with a delimiter, or expose the list to the regex as a single joined string) and write a differential test.

## Test Strategy Hooks

- **Test level:** differential (Java vs. Go) for all four modes + role-mapping behaviors; unit for individual authenticator components.
- **Fixtures required:**
  - Mock OIDC IdP that issues real RS256 JWTs (a small Go test server with rotating keys) plus a JWKS endpoint;
  - Static `presetUsers.yaml` fixtures covering empty privileges, single role, multi-role-joined-with-underscore;
  - In-process LDAP for `LbLdapClient` differential (Apache Directory has an in-memory server; for Go side use `glauth` or `go-ldap` test fixtures);
  - YAML fixtures for each of the four modes including the `defaultType: oauth|form` variants.
- **Observable signals:**
  - Status code (`403` vs. `200`, never `401` direct) on unauthorized requests;
  - JSON body shape `{"code":401, ...}` on `ForbiddenException`-style mapper output;
  - `Set-Cookie` on `/sso`, `/oidc/callback`, `/login` (cookie name, `Secure`/`HttpOnly`/`SameSite`/`Path`/`Max-Age`);
  - `Location` header value on the 302 from `/oidc/callback`;
  - Response body of `POST /loginType` for each mode combination;
  - `userinfo` response body shape for each principal source.
- **Non-determinism risks:**
  - JWKS-per-request behavior means the Java side will issue real HTTP calls to the mock IdP — test must either run the IdP locally or freeze JWKS at startup; the Go cached version will not, so differential test must allow N (Java) vs. 1 (Go) JWKS fetches;
  - Nonce/state randomness in `/sso` is not deterministic; tests must capture+replay rather than fix values;
  - LDAP connection pool warmup may introduce timing skew on the first request.

## Open Questions

- **@architect:** is the failed-auth-no-redirect behavior (`ChainedAuthFilter` swallows the 302) a bug we should fix in the Go rewrite or a contract we should preserve?
- **@architect:** is JWKS-per-request a contract or a defect? Strong recommendation to treat it as a defect and add caching in Go.
- **@architect / @trino-expert:** does the SPA depend on the `200` HTTP status with `{"code":401}` body? If yes, document; if no, the Go rewrite should return real `401`.
- **@trino-expert:** does any production deployment rely on `POST /logout` not clearing the cookie? Need this for the logout-semantics decision.
- **@qa-tech-lead:** for the LDAP `memberOf` regex, what's the differential testing budget? This is a high-blast-radius behavior to get wrong silently.

## Cross-references

- `[[architecture-overview.md]]` — overall package map; this file zooms into `ha.security` and `ha.security.util`.
- `[[jvm-dependencies-inventory.md]]` — covers Nimbus OAuth2 SDK, auth0 JWT, Apache Directory LDAP, Bouncy Castle in their broader inventory context.
- `[[../both/statement-protocol-invariants.java-qa.md]]` — the proxied-surface side; together with this file, those two documents together cover the gateway's complete identity behavior.
