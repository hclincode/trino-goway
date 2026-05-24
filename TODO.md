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

- [ ] Task 10 — Architect writes `phase2-gate-responses.architect.md` (library decisions, DI stance, streaming/oracle/cookie rulings, 6th hard invariant, Phase 2 sequencing constraints)
- [ ] Task 11 — Go-implementer writes `SCOPE.md` (locked scope, deferred scope, reversal cost per item)
- [ ] Task 12 — Go-implementer writes `gateway-cookies-and-sticky-routing.go-implementer.md` (cookie study; required before proxy implementation starts)

### Phase 4: Implementation

Order enforced by dependency:

- [ ] Task 13 — `internal/config` + `internal/lifecycle`
- [ ] Task 14 — `internal/persistence` (DAOs + migrations)
- [ ] Task 15 — `internal/routing` (external selector only)
- [ ] Task 16 — `internal/proxy` (after cookie study lands)
- [ ] Task 17 — `internal/monitor` (cluster health)
- [ ] Task 18 — `internal/auth`
- [ ] Task 19 — `cmd/trino-goway` (main + wiring)
- [ ] Task 20 — `cmd/goway-migrate-config` (config migration tool)

### Phase 5: QA Gates

- [ ] Task 21 — Build QA infra: port allocator + testcontainers-go postgres + goleak + misbehaving-backend fixture (gate to START proxy-core)
- [ ] Task 22 — Build differential harness: live Java↔Go side-by-side for Seams 1–8 + statement protocol (gate to DECLARE proxy-core COMPLETE)
- [ ] Task 23 — G1 test: `nextUri` host derivation against real Trino (first QA gate; only gap with a silent failure mode)
