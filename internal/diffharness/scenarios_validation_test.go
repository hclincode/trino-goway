package diffharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommittedScenarios_LoadAndJustified parses every committed scenario YAML
// and enforces the discipline that EVERY diff.ignoreHeaders /
// diff.ignoreBodyFields entry is accompanied by a [JUSTIFIED] comment somewhere
// in the file. Without this, the harness can drift toward "ignore anything
// noisy" and silently pass divergent shapes.
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
			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			s, err := LoadScenario(path)
			require.NoErrorf(t, err, "load %s", name)
			assert.NotEmpty(t, s.Name)
			assert.NotEmpty(t, s.Steps)

			totalIgnores := len(s.Diff.IgnoreHeaders) + len(s.Diff.IgnoreBodyFields)
			if totalIgnores == 0 {
				return // no ignores → no justification needed
			}

			justifiedCount := strings.Count(string(raw), "[JUSTIFIED]")
			assert.GreaterOrEqualf(t, justifiedCount, 1,
				"scenario %s declares %d ignore entries but has no [JUSTIFIED] comment — every entry must carry a justification per the normalizer-minimal discipline",
				name, totalIgnores)
		})
	}
}
