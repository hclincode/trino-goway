package diffharness

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommittedScenarios_LoadAndJustified parses every committed scenario YAML
// and enforces the normalizer-minimal discipline at PER-ENTRY granularity:
// EVERY individual diff.ignoreHeaders / diff.ignoreBodyFields list item must
// carry an inline `# [JUSTIFIED] <reason>` comment on its own line. A file-level
// `[JUSTIFIED]` count is too weak — it stays green if a future author appends an
// unjustified ignore as long as one old marker survives (qa-tech-lead F1). Tying
// each justification to its exact entry makes the validator un-gameable by stale
// tokens and forces a fresh rationale whenever the ignore set grows.
//
// Uses ../../cmd/goway-diff-harness/testdata/scenarios via a relative path so
// the validation lives with the scenario format, not with the CLI.
func TestCommittedScenarios_LoadAndJustified(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "cmd", "goway-diff-harness", "testdata", "scenarios")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "at least one committed scenario expected")

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, name)

			s, err := LoadScenario(path)
			require.NoErrorf(t, err, "load %s", name)
			assert.NotEmpty(t, s.Name)
			assert.NotEmpty(t, s.Steps)

			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			unjustified := unjustifiedIgnoreEntries(string(raw))
			assert.Emptyf(t, unjustified,
				"scenario %s: every ignoreHeaders/ignoreBodyFields entry must carry an inline "+
					"`# [JUSTIFIED] <reason>` comment; these do not: %v",
				name, unjustified)
		})
	}
}

// unjustifiedIgnoreEntries scans the raw scenario YAML and returns the list
// items under diff.ignoreHeaders / diff.ignoreBodyFields that lack an inline
// [JUSTIFIED] comment. It is a line-oriented scan (not a YAML decode) because
// the justification lives in comments, which the YAML decoder discards.
//
// State machine: it only treats `- item` lines as ignore entries while it is
// inside the diff: mapping AND under an ignoreHeaders:/ignoreBodyFields: key, so
// list items elsewhere (e.g. steps:) are never required to be justified.
func unjustifiedIgnoreEntries(raw string) []string {
	const (
		secNone = iota
		secDiff
		secIgnore
	)
	section := secNone
	ignoreKeyIndent := -1 // indent of the active ignore* key; entries are deeper

	var missing []string
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // blank or full-line comment
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))

		// A top-level key (no indent) that is not `diff:` ends the diff section.
		if indent == 0 {
			if trimmed == "diff:" {
				section = secDiff
			} else {
				section = secNone
			}
			ignoreKeyIndent = -1
			continue
		}

		if section == secNone {
			continue
		}

		// A list item under an active ignore key is an ignore entry to validate.
		if section == secIgnore && strings.HasPrefix(trimmed, "- ") && indent > ignoreKeyIndent {
			if !strings.Contains(line, "[JUSTIFIED]") {
				missing = append(missing, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			}
			continue
		}

		// Otherwise this is a key line inside diff:. Entering an ignore* key opens
		// the ignore list; any other key (e.g. rewriteHostPort:) closes it.
		if isIgnoreKey(trimmed) {
			section = secIgnore
			ignoreKeyIndent = indent
		} else {
			section = secDiff
			ignoreKeyIndent = -1
		}
	}
	return missing
}

// isIgnoreKey reports whether a trimmed YAML line opens an ignoreHeaders or
// ignoreBodyFields sequence.
func isIgnoreKey(trimmed string) bool {
	return trimmed == "ignoreHeaders:" || trimmed == "ignoreBodyFields:"
}
