# TODO

## Pre-Workflow Tasks

- [ ] **Team discussion:** Do we need a Go version of trino-gateway?
  - All 7 agents should weigh in from their respective perspectives
  - Decision outcome determines whether to proceed with Plan A

---

## Plan A: Workflow (candidate — on hold until pre-tasks complete)

### Agent Team (7 members)

**Implementation Team**
- **Trino & Trino-Gateway Expert** — domain authority on Trino protocol and gateway behavior; consulted on demand; does not write code
- **Java Analyst** — reads `trino` (HTTP API surface) and `trino-gateway` (full internals); produces language-agnostic specs
- **Architect / Tech Lead** — Java + Go knowledge; designs Go system (packages, interfaces, concurrency, libraries, component order); coordinates with QA Tech Lead via coordination channel
- **Go Implementer** — implements Go code per Architect's design; escalates ambiguity rather than assuming

**QA Team**
- **Java QA** — reads `trino` and `trino-gateway` to derive behavioral test specs; consults Expert for intent vs artifact
- **QA Tech Lead** — owns test strategy; coordinates Java QA and Go QA; communicates with Architect via coordination channel; signs off on component completion
- **Go QA** — implements tests from Java QA's specs; reports failures to Implementer or escalates systemic gaps to QA Tech Lead

### Phase 1: Discovery

- Java Analyst → Trino coordinator HTTP API surface + Trino-Gateway internals spec
- Java QA → Trino protocol edge cases + Trino-Gateway behavioral test specs
- Expert on call for both
- **Gate:** Specs accepted by Architect and QA Tech Lead before Phase 2

### Phase 2: Architecture Design

- Architect → Go package layout, interfaces, concurrency model, library choices, ordered component list
- QA Tech Lead → test strategy (unit/integration/e2e per component), test tooling choices
- **Gate:** Architect + QA Tech Lead align on component order and testability via coordination channel

### Phase 3: Component Implementation Cycle (repeat per component)

```
1. Java Analyst   → detailed spec for component
2. Java QA        → test spec for component
3. [gate] Architect + QA Tech Lead approve both specs
4. Go Implementer → implements component
5. Go QA          → writes and runs tests
6. Failures?
     minor       → Go QA → Go Implementer → fix → re-test
     design issue → QA Tech Lead ↔ Architect → revised spec → fix
7. Component passes → marked done → next component
```

**Suggested component order:**
1. HTTP reverse proxy core (request forwarding to Trino)
2. Backend cluster registry and health checks
3. Routing engine (rules evaluation)
4. Persistence layer (query history, backend state)
5. REST management API
6. Configuration loading and validation
7. Web UI (if in scope)

### Phase 4: Integration & End-to-End

- Go QA runs integration tests across all assembled components
- Tests against mock Trino server
- Failures → Go Implementer; systemic gaps → Architect via coordination channel
- QA Tech Lead signs off when all acceptance criteria met
