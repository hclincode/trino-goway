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

### Phase 3: Architecture Design

- [ ] Task 10 — Architect writes `phase2-gate-responses.architect.md` (library decisions, DI stance, streaming/oracle/cookie rulings, 6th hard invariant, sequencing constraints)
- [ ] Task 11 — Go-implementer writes `SCOPE.md` (locked scope, deferred scope, reversal cost per item; team-lead sign-off required to change any ruling)
- [ ] Task 12 — Go-implementer writes `gateway-cookies-and-sticky-routing.go-implementer.md` (cookie design + evaluate `/v1/spooled/*` sticky routing via cookie; required before proxy implementation starts)

### Phase 4: Implementation

Order enforced by dependency:

- [ ] Task 13 — `internal/config` + `internal/lifecycle` (YAML loader, custom unmarshalers for DataSize/Duration, explicit Start/Stop lifecycle)
- [ ] Task 14 — `internal/persistence` (DAOs + goose migrations; Postgres + MySQL; query history + cluster registry tables)
- [ ] Task 15 — `internal/routing` (external routing selector: HTTP API + gRPC; queryId sticky-routing with 3-step cache-miss recovery chain)
- [ ] Task 16 — `internal/proxy` (reverse proxy core: Trino statement protocol, `nextUri` polling, gateway cookies, header forwarding; after Task 12 lands)
- [ ] Task 17 — `internal/monitor` (cluster health monitoring + backend registry; three separate `*http.Client` instances for proxy/monitor/external-routing)
- [ ] Task 18 — `internal/auth` (OAuth2/OIDC + LDAP + noop; JWKS TTL caching)
- [ ] Task 19 — `internal/admin` (admin REST API)
- [ ] Task 20 — `cmd/trino-goway` (main + constructor wiring + embed web UI static bundle from Java repo)
- [ ] Task 21 — `cmd/goway-migrate-config` (one-shot Java YAML → Go YAML config migration tool)

### Phase 5: QA Gates

- [ ] Task 22 — Build QA infra: port allocator + testcontainers-go Postgres + goleak + misbehaving-backend fixture (gate to START proxy-core landing)
- [ ] Task 23 — G1 test: `nextUri` host derivation against real Trino container (first QA gate; the only gap with a silent failure mode)
- [ ] Task 24 — Build differential harness: live Java↔Go side-by-side for proxy Seams 1–8 + statement protocol (gate to DECLARE proxy-core COMPLETE)
