package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var queryIDPattern = regexp.MustCompile(`^\d+_\d+_[0-9a-f]{4}_trino$`)

func TestTrinoFake_PostStatement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "simple select", body: `{"query":"SELECT 1"}`},
		{name: "empty body", body: ""},
		{name: "larger query", body: `{"query":"SELECT * FROM big_table WHERE x = 'y'"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := NewTrinoFake(t)

			resp, err := http.Post(f.URL+"/v1/statement", "application/json", strings.NewReader(tc.body))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

			var body struct {
				ID      string `json:"id"`
				NextURI string `json:"nextUri"`
				InfoURI string `json:"infoUri"`
				Stats   struct {
					State string `json:"state"`
				} `json:"stats"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

			assert.Regexp(t, queryIDPattern, body.ID, "queryId should match <ms>_<seq>_<4hex>_trino")
			assert.Contains(t, body.NextURI, "/v1/query/"+body.ID+"/1")
			assert.Contains(t, body.InfoURI, "/v1/queryinfo/"+body.ID)
			assert.Equal(t, "QUEUED", body.Stats.State)

			ids := f.QueryIDs()
			require.Len(t, ids, 1)
			assert.Equal(t, body.ID, ids[0])
		})
	}
}

func TestTrinoFake_QueryIDsAreUnique(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)

	const calls = 5
	seen := make(map[string]struct{}, calls)
	for i := 0; i < calls; i++ {
		resp, err := http.Post(f.URL+"/v1/statement", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	for _, id := range f.QueryIDs() {
		seen[id] = struct{}{}
	}
	assert.Len(t, seen, calls, "every POST should yield a unique queryId")
}

func TestTrinoFake_StickyGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hits int
	}{
		{name: "single hit", hits: 1},
		{name: "three hits", hits: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := NewTrinoFake(t)
			queryID := postStatement(t, f)

			for i := 0; i < tc.hits; i++ {
				resp, err := http.Get(f.URL + "/v1/query/" + queryID + "/" + itoa(i+1))
				require.NoError(t, err)

				assert.Equal(t, http.StatusOK, resp.StatusCode)

				var body struct {
					ID    string `json:"id"`
					Stats struct {
						State string `json:"state"`
					} `json:"stats"`
				}
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
				resp.Body.Close()

				assert.Equal(t, queryID, body.ID)
				assert.Equal(t, "FINISHED", body.Stats.State)
			}

			assert.Equal(t, tc.hits, f.HitCount(queryID))
		})
	}
}

func TestTrinoFake_HEADProbe_Known(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)
	queryID := postStatement(t, f)

	req, err := http.NewRequest(http.MethodHead, f.URL+"/v1/query/"+queryID, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, f.HeadProbes(queryID))
}

func TestTrinoFake_HEADProbe_Unknown(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)
	const unknown = "9999999999999_99_dead_trino"

	req, err := http.NewRequest(http.MethodHead, f.URL+"/v1/query/"+unknown, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, 1, f.HeadProbes(unknown))
}

func TestTrinoFake_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		count int
	}{
		{name: "single cancel", count: 1},
		{name: "two cancels", count: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := NewTrinoFake(t)

			want := make([]string, 0, tc.count)
			for i := 0; i < tc.count; i++ {
				qid := postStatement(t, f)
				want = append(want, qid)

				req, err := http.NewRequest(http.MethodDelete, f.URL+"/v1/query/"+qid, nil)
				require.NoError(t, err)

				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				resp.Body.Close()

				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}

			assert.Equal(t, want, f.Cancellations())
		})
	}
}

func TestTrinoFake_SetStarting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		starting     bool
		wantStarting bool
	}{
		{name: "default not starting", starting: false, wantStarting: false},
		{name: "starting true", starting: true, wantStarting: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := NewTrinoFake(t)
			f.SetStarting(tc.starting)

			resp, err := http.Get(f.URL + "/v1/info")
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusOK, resp.StatusCode)

			var body struct {
				Starting bool `json:"starting"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

			assert.Equal(t, tc.wantStarting, body.Starting)
		})
	}
}

func TestTrinoFake_NextURIUsesHostHeader(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)

	req, err := http.NewRequest(http.MethodPost, f.URL+"/v1/statement", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Host = "example.com"

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var body struct {
		ID      string `json:"id"`
		NextURI string `json:"nextUri"`
		InfoURI string `json:"infoUri"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	assert.Contains(t, body.NextURI, "http://example.com/")
	assert.Contains(t, body.InfoURI, "http://example.com/")
}

func TestTrinoFake_ReceivedHeaders(t *testing.T) {
	t.Parallel()

	f := NewTrinoFake(t)

	req, err := http.NewRequest(http.MethodPost, f.URL+"/v1/statement", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("X-Trino-User", "alice")
	req.Header.Set("X-Custom", "v1")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var body struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	got := f.ReceivedHeaders(body.ID)
	require.NotNil(t, got)
	assert.Equal(t, "alice", got.Get("X-Trino-User"))
	assert.Equal(t, "v1", got.Get("X-Custom"))

	assert.Nil(t, f.ReceivedHeaders("never_seen_qid"), "unknown queryId should yield nil headers")
}

// postStatement issues a POST /v1/statement and returns the generated queryId.
func postStatement(t testing.TB, f *TrinoFake) string {
	t.Helper()

	resp, err := http.Post(f.URL+"/v1/statement", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close()

	var body struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.ID)
	return body.ID
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for i > 0 {
		pos--
		digits[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(digits[pos:])
}
