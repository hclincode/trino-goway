# Study File Conventions

These rules apply to every file under `studies/`. Read `TEMPLATE.md` for the file shape; this document is the naming, placement, and authoring contract.

## Folders

- `studies/trino/` — insights about Trino itself (the engine, the wire protocol, the client contract)
- `studies/trino-gateway/` — insights about the Java trino-gateway implementation
- `studies/both/` — insights spanning both, or about their interaction (e.g. auth handoff, protocol features the gateway must preserve)

Do not create new top-level folders during the study phase. If a need arises, message `architect` first.

## File names

- `kebab-case-insight-title.md` — all lowercase, hyphen-separated, no spaces.
- One insight per file. Broad overviews are OK as a single file (e.g. `architecture-overview.md`).
- The `author` frontmatter field is the source of truth for who wrote the file; no author suffix needed by default.
- **Paired-file convention:** when two authors independently study the same topic from different angles (e.g. a behavioral spec and a test spec for the same routing rule), share the base name and append the author as a suffix so siblings sort adjacent under `ls`:
  ```
  studies/trino-gateway/routing-rules-language.java-analyst.md
  studies/trino-gateway/routing-rules-language.java-qa.md
  ```

### Examples

- `studies/trino/statement-protocol-overview.md`
- `studies/trino-gateway/routing-engine.md`
- `studies/both/auth-handoff.md`

## Frontmatter field reference

- **`author`** — lowercase agent name as it appears in the team config (e.g. `go-implementer`, `java-analyst`).
- **`role`** — exact role string from the team config, with original casing (e.g. `Go Implementer`, `Java Analyst`). Both fields are greppable without case-folding.
- **`component`** — one of `trino`, `trino-gateway`, `both`. Must match the folder.
- **`topics`** — zero-or-more tags from the canonical list below. Lets readers find cross-folder studies on the same area: `grep -l 'topics:.*statement-protocol' studies/**/*.md`. **Must be written as a YAML list of kebab-case strings, not a comma-separated string,** so the grep idiom above is unambiguous:
  ```yaml
  topics:
    - proxy-core
    - statement-protocol
  ```
  An empty value is written as `topics: []`.
- **`status`** — see lifecycle below.
- **`risk`** — `high | medium | low`. Author's honest estimate of likelihood-times-blast-radius if the Go rewrite gets this wrong. Used by QA Tech Lead to triage test investment.
- **`version_pins`** — both submodule refs at time of study. Recommended for `studies/trino/**` and `studies/both/**`; optional but encouraged for `studies/trino-gateway/**`. Important because Trino's wire protocol evolves between releases; without this, "is this still true at trino@<later-release>?" reviews are guesswork.
- **`related-to`** — optional structured array of related study filenames (relative to `studies/`), for machine-queryable graph navigation. Inline `[[wikilinks]]` in prose are also fine.

## Canonical `topics:` list (authoritative — update this file when adding)

Use these as `topics:` array entries. Tag liberally; a file can have multiple topics.

- `proxy-core` — connection handling, request body buffering, generic forwarding, response streaming, error mapping
- `statement-protocol` — `/v1/statement` POST, `nextUri` polling, query ID extraction, cancellation, result-set lifecycle, spooled segments. Streaming and chunked responses also tag here.
- `session-state` — `X-Trino-Session`, set/clear/added/deallocated session headers, prepared statements, role headers, transaction ID headers, catalog/schema rewriting, `X-Trino-Client-Tags`. **Where things go:** `client-tags` and `prepared-statements` live here, not as separate topics.
- `query-classification` — SQL parsing, query-type detection, catalog/schema/table extraction, routing-rule input contract
- `cluster-registry` — backend cluster membership and metadata
- `health-checks` — active and passive health probing
- `routing-engine` — rule evaluation, routing decisions, group selection
- `persistence` — query history, durable state, schema
- `mgmt-api` — REST management endpoints
- `config` — config loading, validation, hot-reload
- `auth` — authentication and authorization
- `observability` — logging, metrics, tracing
- `web-ui` — admin web interface
- `test-infra` — mock Trino server design, testcontainers setup, differential harness, load rig — anything not belonging to a single component
- `cross-cutting` — anything spanning multiple components (lifecycle, shutdown, etc.) and not covered above

If you need a topic not on this list, message `architect` before creating it.

## Status lifecycle

- `draft` — author still working
- `peer-reviewed` — at least one other agent has read and commented; author has addressed or acknowledged the feedback
- `approved` — signed off:
  - Implementation-team studies (Trino Expert, Java Analyst, Architect, Go Implementer): signed off by `architect`
  - QA-team studies (Java QA, QA Tech Lead, Go QA): signed off by `qa-tech-lead`
- `superseded` — invalidated by a later study; do not delete. Add a cross-reference to the replacement so readers can follow.

## Authoring rules

1. **One insight per file.** If a finding splits cleanly into two, write two files. If your TL;DR cannot fit in one paragraph, split.
2. **Cite source by `path:line`** for every non-trivial claim about Java behavior. No uncited claims.
3. **Plain language, not Java idioms.** Describe behavior in terms of the HTTP wire and protocol, not Java types. Say "the response includes header `X-Trino-Set-Session`", not "the `ProxyResponseHandler` appends a `SetSessionProperty` entry". This is the discipline that makes specs portable to a Go implementer.
4. **Scope discipline.** Studies are bounded by their topic. If you find yourself documenting behavior that belongs to another topic, write a one-line cross-link and stop, rather than expanding scope. Without this, study files grow into informal architecture docs and become unreviewable.
5. **Status is honest.** Don't mark `peer-reviewed` until a teammate has actually read it. Don't mark `approved` yourself.
6. **Behavior vs. Artifact is mandatory where applicable.** Skip the section only if truly nothing applies, and say so explicitly rather than leaving it blank.

## Submodule rule (project-wide)

`trino/` and `trino-gateway/` are git submodules and read-only. Do not edit anything inside them. All study output goes under `studies/`. This mirrors the rule in `CLAUDE.md`.
