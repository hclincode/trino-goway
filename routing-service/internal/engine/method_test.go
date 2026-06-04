package engine_test

import (
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

func TestHashPct_Deterministic(t *testing.T) {
	// Same input must always produce the same output.
	inputs := []string{"alice", "bob", "airflow", "", "superset", "a-very-long-user-name@example.com"}
	for _, s := range inputs {
		first := engine.HashPct(s)
		for i := range 10 {
			got := engine.HashPct(s)
			if got != first {
				t.Errorf("HashPct(%q) call %d = %d, want %d (not deterministic)", s, i, got, first)
			}
		}
	}
}

func TestHashPct_Range(t *testing.T) {
	// Result must always be in [0, 99].
	for _, s := range []string{"a", "b", "abc", "", "xyz123", "user@domain.com"} {
		got := engine.HashPct(s)
		if got < 0 || got > 99 {
			t.Errorf("HashPct(%q) = %d, want 0–99", s, got)
		}
	}
}

func TestHashPct_Distribution(t *testing.T) {
	// Over 1000 distinct inputs, no bucket should be hit more than 5× the
	// expected average (i.e. no bucket gets more than 5% of all inputs when
	// we expect ~1% per bucket). This is a loose sanity check, not a
	// statistical test.
	const n = 1000
	counts := make([]int, 100)
	for i := range n {
		// Generate distinct string inputs by formatting the index.
		s := string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('0'+i/676%10))
		counts[engine.HashPct(s)]++
	}
	maxAllowed := n / 100 * 5 // 5× the expected 10 per bucket
	for bucket, count := range counts {
		if count > maxAllowed {
			t.Errorf("HashPct: bucket %d got %d hits (>%d), distribution looks skewed", bucket, count, maxAllowed)
		}
	}
}
