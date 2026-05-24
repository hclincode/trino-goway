---
title: <short descriptive title>
author: <agent-name>                    # lowercase, e.g. java-analyst
role: <Role Name>                        # exact casing from team config, e.g. Java Analyst
component: trino | trino-gateway | both
topics: []                               # YAML list of kebab-case tags from the canonical list in CONVENTIONS.md; use [] if none
date: YYYY-MM-DD
status: draft | peer-reviewed | approved | superseded
risk: high | medium | low                # likelihood x blast radius if the Go rewrite gets this wrong
version_pins:                            # recommended for studies/trino/** and studies/both/**, optional for trino-gateway-only studies
  trino: <commit-sha-or-tag>             # from the trino/ submodule pin at time of study
  trino-gateway: <commit-sha-or-tag>     # from the trino-gateway/ submodule pin at time of study
related-to: []                           # optional: filenames of related studies, paths relative to studies/
---

# <Title>

## Summary

2-3 sentences. What this insight is about and the single most important takeaway. If you can't say it in one paragraph, the file should probably split.

## Key Findings

- Bullet list of substantive findings.
- Cite every non-trivial claim about Java behavior with `path:line` or `path:line-range`, e.g. `trino-gateway/gateway-ha/src/main/java/.../ProxyHandler.java:42-180`.
- For QA-flavored studies, include observable signals (status codes, header names, log lines, metric names) inline rather than as a separate section.

## Behavior vs. Implementation Artifact

For each non-obvious behavior, fill in one block. Skip the section only if truly nothing applies.

### <behavior name>
- **Observed behavior:** what the Java code does, with `path:line` citation.
- **Source of behavior:** one of `protocol-required` / `gateway-design-intent` / `ops-affordance` / `defensive-historical` / `jvm-artifact` / `unclear`.
- **Rationale:** why this exists, as best understood. Cite Trino docs, a commit, or an issue if known.
- **Go obligation:** `replicate-exactly` / `replicate-intent` / `drop` / `defer-to-expert`.
- **Notes:** edge cases, version dependencies, things the Implementer should not "clean up".

## Implications for Go Rewrite

- Bullet list. What this means for scope, architecture, library choice, or test strategy.
- Keep wording implementation-language-neutral when authored by Java Analyst / Java QA / Trino Expert. Go-specific recommendations are appropriate from Architect / Go Implementer / Go QA.

## Test Strategy Hooks

- **Test level:** unit | integration | e2e | differential | load — and why this level.
- **Fixtures required:** mock Trino server capabilities, DB state, config fixtures, etc.
- **Observable signals:** what the test asserts on (response body, header, log line, metric, side effect).
- **Non-determinism risks:** timing, ordering, concurrency, network — flag anything that will cause flakes.

Implementation-team authors may write `see paired QA study <filename>` if a paired QA file covers this, or `n/a` if genuinely not applicable.

## Open Questions

- Bullet list. Tag the agent best placed to answer: `@trino-expert`, `@architect`, `@qa-tech-lead`, etc.

## Cross-references

- `[[other-study-filename.md]]` — relative to the study's own folder; broken links are fine and signal future work.
- Use the frontmatter `related-to:` array for structured links you want to be machine-queryable.
