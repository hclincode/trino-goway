//go:build integration

package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/testutil"
)

func TestMySQLContainer(t *testing.T) {
	db := testutil.MySQLContainer(t)

	var result int
	err := db.QueryRow("SELECT 1").Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result)
}
