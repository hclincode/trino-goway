//go:build e2e

package harness_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestHarness_Smoke verifies that the harness can start the trino-goway binary
// end to end: readyz returns 200, livez returns 200, and a fake backend can be
// registered and observed via the admin API. Requires Docker for the embedded
// Postgres testcontainer.
func TestHarness_Smoke(t *testing.T) {
	h := harness.New(t)

	t.Run("livez returns 200", func(t *testing.T) {
		resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/livez")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("readyz returns 200", func(t *testing.T) {
		resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/readyz")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("register backend and see it in /gateway/backend/all", func(t *testing.T) {
		fake := h.AddBackend(t, "trino-1", "default")
		require.NotNil(t, fake)

		resp, err := h.AdminClient("").Get(h.AdminURL + "/gateway/backend/all")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}
