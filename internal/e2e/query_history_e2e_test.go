//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// historyRecord is the wire shape returned by /trino-gateway/api/queryHistory.
// Mirrors admin.QueryDetail JSON tags.
type historyRecord struct {
	QueryID      string `json:"queryId"`
	QueryText    string `json:"queryText"`
	User         string `json:"user"`
	Source       string `json:"source"`
	BackendURL   string `json:"backendUrl"`
	CaptureTime  int64  `json:"captureTime"`
	RoutingGroup string `json:"routingGroup"`
	ExternalURL  string `json:"externalUrl"`
}

// seedHistory inserts a single row into the query_history table by connecting
// directly to the harness's Postgres container. The proxy itself never writes
// these rows, so tests must seed them to exercise the read-side endpoints.
func seedHistory(t *testing.T, dsn, queryID, userName, backendURL, source string) {
	t.Helper()

	db, err := sqlx.Open("postgres", dsn)
	require.NoError(t, err, "open postgres")
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = db.ExecContext(ctx, `
INSERT INTO query_history (query_id, backend_url, user_name, source, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (query_id) DO NOTHING`,
		queryID, backendURL, userName, source, time.Now().UTC(),
	)
	require.NoError(t, err, "seed query_history row")
}

// fetchHistory issues GET /trino-gateway/api/queryHistory and decodes the array.
func fetchHistory(t *testing.T, h *harness.Harness) []historyRecord {
	t.Helper()

	resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/api/queryHistory")
	require.NoError(t, err, "GET /trino-gateway/api/queryHistory")
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"queryHistory status: got %d", resp.StatusCode)

	var out []historyRecord
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out), "decode queryHistory body")
	return out
}

// postHistoryStatement POSTs a SELECT 1 with the given X-Trino-User and returns the
// queryId echoed by the upstream fake.
func postHistoryStatement(t *testing.T, h *harness.Harness, user string) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, h.ProxyURL+"/v1/statement", strings.NewReader("SELECT 1"))
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", user)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := h.ProxyClient().Do(req)
	require.NoError(t, err, "POST /v1/statement")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"/v1/statement status: got %d body=%s", resp.StatusCode, string(body))

	var stmt struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &stmt), "decode statement body: %s", string(body))
	require.NotEmpty(t, stmt.ID, "statement response missing id")
	return stmt.ID
}

// findByQueryID returns the first record matching queryID, or nil.
func findByQueryID(records []historyRecord, queryID string) *historyRecord {
	for i := range records {
		if records[i].QueryID == queryID {
			return &records[i]
		}
	}
	return nil
}

// TestE2E_History_RecordedAfterStatement seeds a history row keyed by a queryId
// generated through the live proxy path and verifies the row is visible via
// /trino-gateway/api/queryHistory with the recorded userName and backendUrl.
//
// The proxy does not currently write history records itself, so the seed step
// makes the test deterministic: we still drive a real /v1/statement through the
// gateway to confirm queryIds are issued, then assert the read-side wire shape.
func TestE2E_History_RecordedAfterStatement(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	queryID := postHistoryStatement(t, h, "alice")
	seedHistory(t, h.DBDSN(), queryID, "alice", fake.URL, "test")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		records := fetchHistory(t, h)
		if rec := findByQueryID(records, queryID); rec != nil {
			assert.Equal(t, "alice", rec.User, "record userName")
			assert.Containsf(t, rec.BackendURL, fake.URL,
				"backendUrl %q should contain fake URL %q", rec.BackendURL, fake.URL)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("query history did not surface queryId %q within 5s", queryID)
}

// TestE2E_History_AdminSeesAllUsers seeds rows for two distinct users and
// verifies that an ADMIN-roled caller (default harness regex grants ADMIN to
// the anonymous NOOP principal) sees both records.
func TestE2E_History_AdminSeesAllUsers(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	qidAlice := postHistoryStatement(t, h, "alice")
	qidBob := postHistoryStatement(t, h, "bob")

	seedHistory(t, h.DBDSN(), qidAlice, "alice", fake.URL, "test")
	seedHistory(t, h.DBDSN(), qidBob, "bob", fake.URL, "test")

	records := fetchHistory(t, h)

	assert.NotNilf(t, findByQueryID(records, qidAlice),
		"admin should see alice's queryId %q", qidAlice)
	assert.NotNilf(t, findByQueryID(records, qidBob),
		"admin should see bob's queryId %q", qidBob)
}

// TestE2E_History_UserScopedToOwn verifies that a non-ADMIN caller sees only
// records matching its own principal name. The harness is configured so the
// NOOP principal (name=="anonymous") gets USER but NOT ADMIN.
//
// We seed rows for "alice" and "bob". The non-ADMIN caller (principal name
// "anonymous") should see neither, confirming the server-side userName filter
// applied by admin/query.go is in effect.
func TestE2E_History_UserScopedToOwn(t *testing.T) {
	h := harness.New(t,
		harness.WithAdminRoleRegex("admin_group"), // anonymous (memberOf=="") fails
		harness.WithUserRoleRegex(".*"),           // USER role granted
	)
	fake := h.AddBackend(t, "trino-1", "default")

	qidAlice := postHistoryStatement(t, h, "alice")
	qidBob := postHistoryStatement(t, h, "bob")

	seedHistory(t, h.DBDSN(), qidAlice, "alice", fake.URL, "test")
	seedHistory(t, h.DBDSN(), qidBob, "bob", fake.URL, "test")

	records := fetchHistory(t, h)

	// Anonymous principal != alice and != bob, so neither row should be returned.
	for _, rec := range records {
		assert.NotEqualf(t, "alice", rec.User,
			"non-admin caller leaked alice's record: %+v", rec)
		assert.NotEqualf(t, "bob", rec.User,
			"non-admin caller leaked bob's record: %+v", rec)
	}
}

// TestE2E_History_Distribution seeds history rows and asserts that
// /trino-gateway/api/queryHistoryDistribution returns a JSON object containing
// at least one backendUrl → count mapping.
func TestE2E_History_Distribution(t *testing.T) {
	h := harness.New(t)
	fake := h.AddBackend(t, "trino-1", "default")

	for i := 0; i < 3; i++ {
		qid := postHistoryStatement(t, h, "alice")
		seedHistory(t, h.DBDSN(), qid, "alice", fake.URL, "test")
	}

	resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/api/queryHistoryDistribution")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var dist map[string]int64
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&dist))
	require.NotEmpty(t, dist, "distribution must contain at least one backendUrl key")

	count, ok := dist[fake.URL]
	require.Truef(t, ok, "distribution missing key for backendUrl %q (got %v)", fake.URL, keys(dist))
	assert.GreaterOrEqual(t, count, int64(3), "expected at least 3 queries for backend")
}

// TestE2E_History_ActiveBackends verifies the legacy /trino-gateway/api/activeBackends
// endpoint returns the registered backend.
func TestE2E_History_ActiveBackends(t *testing.T) {
	h := harness.New(t)
	h.AddBackend(t, "trino-1", "default")

	resp, err := h.AdminClient("").Get(h.AdminURL + "/trino-gateway/api/activeBackends")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "trino-1", "activeBackends body must contain registered backend name")
}

// keys returns the map keys for diagnostic output.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
