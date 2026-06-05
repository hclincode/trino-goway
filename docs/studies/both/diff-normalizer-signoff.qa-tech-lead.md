---
title: Diff-harness normalizer sign-off
author: qa-tech-lead
role: QA Tech Lead
component: both
topics: [test-infra, diff-harness, cross-cutting]
date: 2026-06-05
status: signed-off-with-findings
risk: low
version_pins:
  trino: 476
  trino-gateway: 19
related-to:
  - both/test-infrastructure-needs.qa-tech-lead.md
  - both/component-signoff-rubric.qa-tech-lead.md
  - both/proxy-seam-gap-analysis.go-qa.md
---

# Diff-harness normalizer sign-off

## Summary

I reviewed every `diff.ignoreHeaders` / `diff.ignoreBodyFields` / `rewriteHostPort`
entry across all 15 committed scenarios in
`cmd/goway-diff-harness/testdata/scenarios/*.yaml` against the normalizer-minimal
discipline: every entry that suppresses a difference must be justified by a real
source of Java↔Go non-determinism (timing, per-instance identity, queryId minting,
host:port, persistence timestamps), never by a contract field that a Trino client
actually reads.

**Verdict: SIGNED OFF.** No scenario normalizes away a load-bearing wire-contract
field. Every ignore entry traces to a documented, legitimate non-determinism
source, and each scenario file carries `[JUSTIFIED]` annotations. Two structural
findings below are about *enforcement strength* and one *coverage gap* — none block
sign-off, but F1 should be tightened before the scenario set grows much larger.

The normalizer code itself (`internal/diffharness/normalize.go`) is correctly
minimal: it only deletes the named headers, strips the named dotted JSON paths, and
rewrites the gateway host:port to a `<GATEWAY>` sentinel on both sides. It does NOT
canonicalize ordering, coerce types, lowercase, or wildcard — so it cannot silently
mask a real divergence beyond exactly the fields a scenario names. Body comparison
(`diff.go::diffBodies`) is structural JSON equality via go-cmp, falling back to byte
diff for non-JSON, which is the right default.

## What each ignore class buys, and why it is safe

| Ignore class | Scenarios | Justification (verified) |
|---|---|---|
| `Date` | all | Per-request wall clock; never equal across two live calls. |
| `Content-Length` | all | Drifts after host:port rewrite (sentinel ≠ real host length) and with per-instance identity strings; the *body* is still fully diffed, so length is redundant. |
| `Server` | all | Jetty vs `net/http`; not part of any gateway API contract. |
| `Set-Cookie` | most | Java may emit JSESSIONID/TG.* depending on session config; cookie wire-compat is asserted *positively* in seam7 (where Set-Cookie is deliberately NOT ignored). |
| `id`,`nextUri`,`infoUri`,`partialCancelUri` | statement scenarios | Trino mints fresh queryIds per submission; values cannot match across two independent submissions. Shape/presence is still asserted. |
| `stats.*Millis`,`stats.*Bytes`,`stats.processedRows` | statement scenarios | Timing- and execution-shape-dependent. |
| `stats.state` | poll scenarios | Poll-timing dependent (QUEUED/RUNNING/FINISHED/FINISHING). |
| `data` | poll scenarios | Row may stream in an earlier poll; terminal payload presence is non-deterministic. The seam asserts loop *shape*, not payload. |
| `warnings` | all statement | Trino's warning emitter is non-deterministic across runs. |
| `error.errorLocation`,`error.failureInfo`,`error.stack` | error scenarios | JVM-internal class names / version-dependent column numbering; NOT wire contract. `errorName`/`errorCode`/`errorType` are deliberately NOT ignored. |
| `createdAt`/`updatedAt`/`created_at`/`updated_at`/`submissionTime`/`captureTime` | persistence scenarios | Independent Postgres DBs → per-write timestamps differ. |
| `status` | health-probe-unhealthy | Probe interval differs Java vs Go defaults; the routing OUTCOME is the assertion, not the snapshot health value. |
| `nodeId`/`coordinatorId`/`nodeVersion`/`environment`/`uptime`/`startTime`/`starting` | /v1/info scenarios | Per-Trino-instance identity. |
| `source`,`backendUrl` | query-history-scoping | Trino auto-decorates source with version; backendUrl port differs per gateway. |
| `columns` (whole subtree) | statement-protocol-roundtrip | See F2 — this is the single broadest ignore; flagged but acceptable for that scenario. |
| `rewriteHostPort: true` | all | Both gateways bind different ports; both sides rewrite to `<GATEWAY>` so the JSON path / Location header is comparable. Symmetric, so it cannot hide a divergence other than the port itself. |

## Findings

### F1 — `[JUSTIFIED]` enforcement is per-file, not per-entry (process gap, low risk)

`internal/diffharness/scenarios_validation_test.go:53-56` enforces only that
`[JUSTIFIED]` appears **at least once** in a file that has any ignore entries
(`justifiedCount >= 1`). It does not check that every individual ignore entry has a
matching justification. Today every file is hand-annotated well, so the *intent* of
the discipline holds — but the test would stay green if a future author appended a
new ignore field without justifying it, as long as one old `[JUSTIFIED]` line
survives. This is the exact "drift toward ignore-anything-noisy" failure the test's
own doc-comment warns against.

Recommendation (not blocking): strengthen the validation to require
`justifiedCount >= totalIgnores`, or better, parse a per-entry inline justification
convention (e.g. a trailing `# [JUSTIFIED] reason` on each list item). Until then,
the human sign-off in this doc is the backstop. Owner: whoever extends the scenario
set next; coordinate with go-qa (Task #20).

### F2 — `statement-protocol-roundtrip` ignores the entire `columns` subtree (acceptable, documented)

This is the broadest single ignore in the set. The scenario justifies it: it asserts
`columns` are *present* (the field is not removed wholesale from the response, only
its value-comparison is suppressed) but not the inner `typeSignature` tree, which
drifts in non-load-bearing nested-type metadata across Trino versions. Because
`stripJSONFields` deletes the key entirely, "present but value-ignored" actually
means "key removed on both sides" — so a regression where one gateway *dropped*
`columns` would still be caught structurally only if the other also dropped it. In
practice both sides hit the same Trino, so the drop would be symmetric and this is
fine for a same-Trino diff. Sibling scenarios (seam1/seam4/seam5) assert the
statement envelope *with* a narrower ignore set and do not blanket-ignore `columns`,
so column-shape regressions are still covered elsewhere. Acceptable; no change.

### F3 — `external-routing-headers` is not exercisable by the current live harness (coverage gap, not a normalizer issue)

The scenario's own header documents this: it requires BOTH gateways to be wired to
the same mock external router. The live harness (`cmd/goway-diff-harness/live_test.go::startGoGateway`)
configures the Go gateway with `Type: EXTERNAL` but **no external service URL**, and
`bootstrap.go` provisions no shared mock router. So in the current fleet this
scenario falls through to the default group rather than testing the externalHeaders
injection path. The scenario stays structurally valid and validation-clean; its
deeper assertion is owned by `internal/proxy/proxy_test.go::TestProxy_ExternalHeaders`.
This is a harness-wiring gap (TODO Task 42-44 follow-up), not a normalizer defect —
recorded here so the live PASS in step 3 is not misread as proving the external-router
path. Tracked for Task #20 / a future scenario-wiring task.

## First live-fleet run (Task #19 step 3)

I executed `go test -tags=diff -run TestLive_SeamScenarios_DiffPasses
./cmd/goway-diff-harness/` against a real Docker fleet (trinodb/trino-gateway:19 +
Postgres 17 + Trino 476). This was the first time the live harness had ever been
run end-to-end. It immediately surfaced — and I fixed — **three bootstrap bugs that
had silently prevented the live test from ever passing**, then exposed two
harness-design gaps that are NOT normalizer/Go-code defects.

### Bootstrap bugs found and fixed (the live run was never green before this)

| # | Symptom | Root cause | Fix |
|---|---|---|---|
| B1 | Java gateway container exited code 1 at boot; `UnrecognizedPropertyException: "authenticationType"` | `java-gateway-config.yaml.tmpl` declared `authentication.authenticationType: "noop"`, but trino-gateway 19's `AuthenticationConfiguration` only knows `defaultType`/`oauth`/`form`. No such `noop` property exists. | Removed the `authentication` block entirely. In v19 auth filters are only installed when an `oauth:`/`form:` sub-block is present (`ChainedAuthFilter`); with neither, the gateway serves anonymously. |
| B2 | Java gateway exited code 100; `Invalid configuration property node.environment: should match [a-z0-9][_a-z0-9]*` | `node.environment: diff-harness` — airlift `NodeConfig` forbids the hyphen. | Changed to `diff_harness`. |
| B3 | Every proxied request returned `HTTP 406 ... does not allow processing of the X-Forwarded-For header` | The Trino container ran with default config; both gateways inject `X-Forwarded-*` (`internal/proxy/headers.go`, matching Java `ProxyRequestHandler.addForwardedHeaders`), which Trino rejects unless `http-server.process-forwarded=true`. trino-gateway's own integration tests set this same flag. | Added `internal/diffharness/testdata/trino-config.properties` (image default + `process-forwarded=true`) and mounted it at `/etc/trino/config.properties` in `bootstrap.go`. |

After B1–B3 the fleet boots cleanly and scenarios actually execute. I also added the
Java/Go status codes to the `live_test.go` failure message (they were previously
invisible, which is why these bugs were never diagnosed).

### Remaining live blockers — handed to the diff-harness owner / Task #20

The run is now diagnostic, not green. The Go gateway behaves correctly in every
case; the failures are on the Java side or in the harness wiring:

- **L1 — proxy seams: `java=500`, `go=200`.** All `/v1/*` proxy scenarios (seam1-8,
  external-routing, health-probe, kill-query) show the **Java gateway returning 500
  with an empty body** while the Go gateway returns Trino's correct 200 JSON. This is
  the trino-gateway "route before the cluster monitor marks the backend healthy"
  behavior: `bootstrap.go::registerBackend` seeds the backend, but the
  `monitorType: INFO_API` health loop has not yet flagged `trino-shared` OK by the
  time scenarios fire, so the gateway 500s. **The harness needs a readiness gate** —
  poll the Java gateway until a `/v1/statement` (or its backend-state API) returns
  2xx before running scenarios. The 4 `ERROR` verdicts (recovery-chain, seam3, seam5,
  statement-protocol) are downstream of this: step 1 returned the 500 with no JSON,
  so `nextUri`/`next` extraction failed.
- **L2 — admin/webapp scenarios: `java=200`, `go=404`.** `admin-backend-crud`
  (`/gateway/backend/all`) and `query-history-scoping`
  (`/trino-gateway/api/queryHistory`) fail because `live_test.go::startGoGateway`
  composes **only the proxy + router** — no admin or webapp handler is mounted, so
  the Go side 404s on those paths. The live harness's Go target is not the full
  gateway. To exercise these scenarios the harness must mount the admin/webapp
  handlers (and give the Go side a DB), or these two scenarios should be excluded
  from the proxy-only live target and run under a separate full-gateway target.

Neither L1 nor L2 is a normalizer defect or a Go-code bug — the normalizer sign-off
stands. They are harness-completeness items. L1 is a one-function readiness gate; L2
is a larger "stand up the full Go gateway in the live harness" task that overlaps
go-qa's Task #20 (diff scenarios pass under live fleet).

## Sign-off

The normalizer and the committed scenario diff policies are approved for nightly
`-tags=diff` use. The three bootstrap bugs (B1–B3) are fixed in this change. The two
non-blocking review findings (F1 enforcement, F3 coverage) and the two live blockers
(L1 readiness gate, L2 full-gateway target) are handed to go-qa via Task #20. F2 is
acceptable as-is. **The live fleet does not yet produce an all-PASS run** — that is
gated on L1 + L2, which I did not fabricate around: no golden files were invented,
and the live test correctly reports FAIL until the harness is completed.

## Task #20 closeout — live fleet GREEN (go-qa, qa-tech-lead review)

go-qa completed Task #20; I reviewed the result. The live fleet now passes:
**14/14 in-target scenarios PASS** across two consecutive clean runs, with one
documented exclusion. Resolutions:

- **L1 (readiness gate) — DONE.** `bootstrap.go::waitGatewayRoutable()` polls
  `POST /v1/statement` (SELECT 1) against the Java gateway until 2xx before any
  scenario fires, so the `INFO_API` monitor has marked the backend healthy first.
  This is load-bearing: the probe must be a statement POST (not `/v1/info`), because
  proving statement-routability is exactly what the 500-gating needs.
- **L2 (full Go gateway in live target) — DONE.** `live_test.go::startGoGateway` now
  muxes `admin.New` + `proxy.New` by `isAdminPath`, with in-memory `backendStore` and
  a single shared `historyStore` (proxy-write/admin-read), `auth.Noop` + wide-open
  authz. The trino-shared backend is seeded with the **display** URL
  (`http://trino:8080`, matching Java) and rewritten to the host-mapped URL only for
  routing — so `/gateway/backend/all` is byte-identical to Java without ignoring
  `proxyTo`/`externalUrl`, keeping the scenario's own added backend value-asserted.

### F1 enforcement — CLOSED

`scenarios_validation_test.go` now enforces per-entry inline `# [JUSTIFIED] <reason>`
on every `ignoreHeaders`/`ignoreBodyFields` list item (replacing the file-level
`justifiedCount >= 1`). I verified it both directions: all 15 scenarios pass, and an
injected unjustified entry produces a precise file+field failure. Un-gameable by
stale tokens. F1 from the original review is resolved.

### New justified ignores (folded into the policy)

| Field | Scenarios | Justification |
|---|---|---|
| `stats.rootStage` (whole subtree) | seam5, statement-protocol | Per-stage execution-engine detail tree (`stageId`/`state`/`done`/`subStages` + timers). Not the client-facing statement-protocol contract (clients read top-level `stats.state`/`progressPercentage`/`completedSplits`/`nodes`). **Why the whole subtree, not the timing leaves:** `rootStage` is recursive — `subStages` is itself a tree of `StageStats`, each with its own `cpuTimeMillis`/`wallTimeMillis` (Trino `StageStats.java`), so per-leaf enumeration is incomplete and brittle to query/version change. The minimal *stable* ignore unit is the whole subtree. The structural fields it hides are not client-read and are themselves execution-shape/poll-timing dependent. |
| `stats.analysisTimeMillis`, `stats.finishingTimeMillis`, `stats.planningTimeMillis` | seam5, statement-protocol | Pure per-run execution timers, same class as the already-justified `elapsed/queued/cpu/wallTimeMillis`. |
| `Vary`, `X-Content-Type-Options` | admin-backend-crud | Per-gateway header defaults (Jetty vs net/http), not the admin-API contract — same class as `Server`. |

*(Review history: this oscillated leaf↔wholesale across several crossed messages. I
reconciled it to ground truth by reading the committed scenario files directly: the
**final committed state is `stats.rootStage` WHOLESALE**, and this doc matches it. The
recursion argument above is why wholesale is the correct stable unit. The leaf-scoped
alternative was more minimal but brittle to the `subStages` recursion; we chose
stability. Source of truth is the YAML, not the message thread.)*

**Guardrail verified intact:** the only normalized stats are `*Millis`/`*Bytes`
timers and the non-contract `rootStage` subtree. The load-bearing top-level stats —
`nodes`, `completedSplits`, `queued`, `queuedSplits`, `totalSplits`,
`progressPercentage` — are **not** ignored and matched across the live run. If a
future run diverges on one of those, it must be investigated, not ignored.

### query-history-scoping — DROPPED from the live target (supersedes F3-class note)

Excluded from the live diff target (`liveExcludedScenarios` in `live_test.go`),
kept as a unit-only assertion (`internal/admin/admin_test.go`). The reason is
**fleet-history isolation**, verified against the live run (not assumed) — and this
corrected an earlier static-analysis guess of mine that I want on the record:

- I had theorized an *endpoint mismatch* (that Java served query history only at
  `POST /webapp/findQueryHistory` and would 404 the Go-style path). **The live bytes
  disproved that.** trino-gateway 19 DOES serve `GET /trino-gateway/api/queryHistory`
  → 200, and with `X-Trino-User: alice` it returns the FULL list **unscoped**
  (admin-sees-all): 7 records across users `alice`, `bob`, `diff-harness`, and
  `diff-harness-readiness`. So neither the 404 nor the scoping premise holds; the real
  Java behavior is admin-sees-all.
- The decisive, architectural reason it can't be a live diff: that 7-record set
  includes the bootstrap **readiness-gate `SELECT 1`** (user `diff-harness-readiness`,
  sent to Java only) plus cross-scenario Java traffic. The per-run replayed Go
  in-memory store only ever sees what the diff runner sends to BOTH sides, so it can
  never mirror the Java-only records — the record COUNT/SET diverges by construction,
  unfixable by tolerant diffing. We deliberately did **not** build a trusting-noop
  shim or weaken the readiness gate to manufacture a pass.

external-routing-headers stays IN the live target (passes as a plain proxy diff).
The committed scenario YAML header and the `live_test.go` exclusion comment were
rewritten to state this verified behavior (go-qa, on review).

### Latent normalizer gap — tracked, not fixed (NEW finding)

go-qa found, and I confirmed, that `normalize.go::deleteJSONPath` only handles
`map[string]any` — when the node is a JSON **array** (`[]any`), the type assertion
fails and it returns the node unchanged. So `ignoreBodyFields` **silently no-ops on
array-typed response bodies** (e.g. `/gateway/backend/all`, which returns a JSON
array). This is a real latent gap: the F1 validator will dutifully require a
justification for an `ignoreBodyFields` entry that does nothing on an array response,
giving false confidence. It did not affect Task #20 because the admin-backend-crud
wiring sidesteps field-stripping entirely (the display-URL seed keeps the array
byte-identical). Tracked as a follow-up — array-path traversal in the normalizer, or
at minimum a validator/diff warning when an `ignoreBodyFields` path targets an array.

**RESOLVED (Task #23, go-qa):** `deleteJSONPath` now descends into `[]any` — a
dotted path is applied to every element of any array encountered (top-level array
bodies and arrays nested under a key both work). Object-body behavior is unchanged
(regression-guarded). Covered by `normalize_test.go::TestStripJSONFields_{TopLevelArray,
NestedArray,MapUnchanged}`. The live fleet stays green; admin-backend-crud keeps its
display-URL seed (so `proxyTo` stays value-asserted rather than stripped).
