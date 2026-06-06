package clusterstatus

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStatus_StringRoundTrip pins the admin wire-form Label() and the lowercase
// String() for every member, including UNKNOWN as the zero value.
func TestStatus_StringRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    Status
		wantLabel string
		wantStr   string
	}{
		{name: "healthy", status: Healthy, wantLabel: "HEALTHY", wantStr: "healthy"},
		{name: "unhealthy", status: Unhealthy, wantLabel: "UNHEALTHY", wantStr: "unhealthy"},
		{name: "pending", status: Pending, wantLabel: "PENDING", wantStr: "pending"},
		{name: "unknown", status: Unknown, wantLabel: "UNKNOWN", wantStr: "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.wantLabel, tc.status.Label())
			assert.Equal(t, tc.wantStr, tc.status.String())
		})
	}
}

// TestStatus_UnknownIsZeroValue guards the choice-(a) invariant that a not-yet-probed
// backend reads as UNKNOWN, so an unobserved default ClusterStats reports UNKNOWN.
func TestStatus_UnknownIsZeroValue(t *testing.T) {
	t.Parallel()

	var zero Status
	assert.Equal(t, Unknown, zero)
	assert.Equal(t, "UNKNOWN", zero.Label())
	assert.Equal(t, "unknown", zero.String())
}

// TestStatus_UnmappedLabelFallsBackToUnknown ensures the Label()/String() default arms
// collapse any out-of-range value to UNKNOWN rather than mislabeling it.
func TestStatus_UnmappedLabelFallsBackToUnknown(t *testing.T) {
	t.Parallel()

	bogus := Status(99)
	assert.Equal(t, "UNKNOWN", bogus.Label())
	assert.Equal(t, "unknown", bogus.String())
}
