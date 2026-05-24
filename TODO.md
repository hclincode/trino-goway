# TODO

## Phase 0: Team Alignment

- [x] Task 1 — Agree on study insight template and file conventions (architect leads, all agents participate)

## Phase 1: Study

- [x] Task 2 — trino-expert studies trino & trino-gateway
- [x] Task 3 — java-analyst studies trino & trino-gateway
- [x] Task 4 — architect studies trino & trino-gateway
- [x] Task 5 — go-implementer studies trino & trino-gateway
- [x] Task 6 — java-qa studies trino & trino-gateway
- [x] Task 7 — qa-tech-lead studies trino & trino-gateway
- [x] Task 8 — go-qa studies trino & trino-gateway

## Phase 2: Topic Discussion

- [x] Task 9 — Discuss: Do we need a Go version of trino-gateway? (result: `topics/do-we-needs-golang-trino-gateway.md` — unanimous PROCEED WITH CAVEATS)

## Backlog

### Phase 3: Architecture Design + Targeted Studies

- [ ] Task 10 — Architect writes `phase2-gate-responses.architect.md` (library decisions, DI stance, streaming/oracle/cookie rulings, 6th hard invariant, sequencing constraints; includes ruling on gRPC in v1 vs. Non-Groomed)
- [ ] Task 11 — Go-implementer writes `SCOPE.md` (locked scope, deferred scope, reversal cost per item; team-lead sign-off required to change any ruling)
- [ ] Task 12 — Go-implementer writes `gateway-cookies-and-sticky-routing.go-implementer.md` (cookie design: HMAC-SHA256 wire-compat with Java `GatewayCookie`, `wireCompat` config flag, `/v1/spooled/*` + `/v1/spooled/ack` sticky routing via `TG.*` cookie; required before proxy implementation starts)
- [ ] Task 13 — trino-expert studies `/v1/spooled/*` URL structure in Trino source (`studies/trino/spooled-segment-protocol.trino-expert.md`): token format, whether queryId is encoded, redirect chain, and whether cookie is the only viable sticky mechanism
- [ ] Task 14 — go-implementer studies `GatewayCookie.java` in depth (`studies/trino-gateway/gateway-cookie-internals.go-implementer.md`): HMAC-SHA256 payload format, `routingPaths` matching logic, cookie issue/validate/invalidate lifecycle; feeds into Task 12
- [ ] Task 15 — java-analyst produces complete external routing contract study (`studies/trino-gateway/external-routing-contract.java-analyst.md`): all request fields (`RoutingGroupExternalBody`) and response fields (`ExternalRouterResponse`), which `trinoQueryProperties` sub-fields are empty without `trino-parser`, `propagateErrors` fallback behavior, header-forwarding and `excludeHeaders` policy; pin the exact JSON shapes that Go HTTP + gRPC transports must replicate
- [ ] Task 16 — java-analyst or go-implementer catalogs admin REST API endpoints (`studies/trino-gateway/admin-api-surface.java-analyst.md`): all routes, request/response shapes, `@RolesAllowed` per endpoint; spec for Task 20 (`internal/admin`)

### Phase 4: Implementation

Order enforced by dependency:

- [ ] Task 17 — `internal/config` + `internal/lifecycle` (YAML loader, custom unmarshalers for DataSize/Duration, explicit Start/Stop lifecycle)
- [ ] Task 18 — `internal/persistence` (DAOs + goose migrations; Postgres + MySQL; query history + cluster registry tables)
- [ ] Task 19 — `internal/routing` (external routing selector: HTTP + gRPC transports with identical field contract matching original `RoutingGroupExternalBody`/`ExternalRouterResponse`; queryId sticky-routing with 3-step cache-miss recovery chain; `propagateErrors` fallback; after Task 15 lands)
- [ ] Task 20 — `internal/proxy` (reverse proxy core: Trino statement protocol, `nextUri` polling, gateway cookies, `/v1/spooled/*` sticky routing via `TG.*` cookie, header forwarding; after Tasks 12–14 land)
- [ ] Task 21 — `internal/monitor` (cluster health monitoring + backend registry; three separate `*http.Client` instances for proxy/monitor/external-routing)
- [ ] Task 22 — `internal/auth` (OAuth2/OIDC + LDAP + noop; JWKS TTL caching)
- [ ] Task 23 — `internal/admin` (admin REST API; after Task 16 lands)
- [ ] Task 24 — `cmd/trino-goway` (main + constructor wiring + embed web UI static bundle from Java repo)
- [ ] Task 25 — `cmd/goway-migrate-config` (one-shot Java YAML → Go YAML config migration tool)

### Phase 5: QA Gates

- [ ] Task 26 — Build QA infra: port allocator + testcontainers-go Postgres + goleak + misbehaving-backend fixture (gate to START proxy-core landing)
- [ ] Task 27 — G1 test: `nextUri` host derivation against real Trino container (first QA gate; the only gap with a silent failure mode)
- [ ] Task 28 — Build differential harness: live Java↔Go side-by-side for proxy Seams 1–8 + statement protocol (gate to DECLARE proxy-core COMPLETE)
