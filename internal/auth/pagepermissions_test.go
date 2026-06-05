package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/auth"
)

func TestResolvePagePermissions(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		perms map[string]string
		want  []string
	}{
		{
			name:  "no roles → empty",
			roles: nil,
			perms: map[string]string{"USER": "dashboard"},
			want:  []string{},
		},
		{
			name:  "no config → empty (unrestricted)",
			roles: []string{"USER"},
			perms: nil,
			want:  []string{},
		},
		{
			name:  "single role maps to its pages",
			roles: []string{"USER"},
			perms: map[string]string{"USER": "dashboard_history"},
			want:  []string{"dashboard", "history"},
		},
		{
			name:  "union across roles, deduplicated",
			roles: []string{"ADMIN", "USER"},
			perms: map[string]string{"ADMIN": "dashboard_cluster", "USER": "dashboard_history"},
			want:  []string{"cluster", "dashboard", "history"},
		},
		{
			name:  "any unrestricted role short-circuits to all pages",
			roles: []string{"ADMIN", "USER"},
			perms: map[string]string{"USER": "dashboard"}, // ADMIN absent
			want:  []string{},
		},
		{
			name:  "empty page segments are skipped",
			roles: []string{"USER"},
			perms: map[string]string{"USER": "dashboard__history_"},
			want:  []string{"dashboard", "history"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := auth.ResolvePagePermissions(tc.roles, tc.perms)
			assert.NotNil(t, got, "result must never be nil")
			// Order-independent: the union dedup order is an implementation detail.
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}
