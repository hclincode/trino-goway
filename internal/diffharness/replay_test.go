package diffharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunner_SimpleScenario_Pass(t *testing.T) {
	t.Parallel()

	java := newFakeGateway(t, "q1")
	goGW := newFakeGateway(t, "q1")

	s := &Scenario{
		Name: "smoke-statement",
		Steps: []Step{{
			Method: "POST",
			Path:   "/v1/statement",
			Body:   "SELECT 1",
		}},
		Diff: DiffPolicy{
			IgnoreHeaders:   []string{"Date", "Content-Length"},
			RewriteHostPort: true,
		},
	}

	r := NewRunner(Target{Name: "java", BaseURL: java.URL}, Target{Name: "go", BaseURL: goGW.URL})
	res := r.RunScenario(context.Background(), s)
	assert.Equalf(t, VerdictPass, res.Verdict,
		"expected PASS — got %s, body diff=%q, header diffs=%v",
		res.Verdict, res.BodyDiff, res.HeaderDiffs)
}

func TestRunner_DifferentBody_Fail(t *testing.T) {
	t.Parallel()

	java := newFakeGateway(t, "java-qid")
	goGW := newFakeGateway(t, "go-qid")

	s := &Scenario{
		Name:  "id-divergence",
		Steps: []Step{{Method: "POST", Path: "/v1/statement", Body: "SELECT 1"}},
		Diff:  DiffPolicy{IgnoreHeaders: []string{"Date", "Content-Length"}, RewriteHostPort: true},
	}

	r := NewRunner(Target{Name: "java", BaseURL: java.URL}, Target{Name: "go", BaseURL: goGW.URL})
	res := r.RunScenario(context.Background(), s)
	assert.Equal(t, VerdictFail, res.Verdict,
		"differing queryId should fail when not in IgnoreBodyFields")
}

func TestRunner_IgnoreBodyFields_Pass(t *testing.T) {
	t.Parallel()

	java := newFakeGateway(t, "java-qid")
	goGW := newFakeGateway(t, "go-qid")

	s := &Scenario{
		Name:  "id-ignored",
		Steps: []Step{{Method: "POST", Path: "/v1/statement", Body: "SELECT 1"}},
		Diff: DiffPolicy{
			IgnoreHeaders:    []string{"Date", "Content-Length"},
			IgnoreBodyFields: []string{"id", "nextUri"},
			RewriteHostPort:  true,
		},
	}

	r := NewRunner(Target{Name: "java", BaseURL: java.URL}, Target{Name: "go", BaseURL: goGW.URL})
	res := r.RunScenario(context.Background(), s)
	assert.Equalf(t, VerdictPass, res.Verdict,
		"id should be ignored — body diff=%q", res.BodyDiff)
}

func TestRunner_ErrorOnOneSide_VerdictError(t *testing.T) {
	t.Parallel()

	java := newFakeGateway(t, "q1")

	s := &Scenario{
		Name:  "go-unreachable",
		Steps: []Step{{Method: "POST", Path: "/v1/statement", Body: "SELECT 1"}},
	}
	r := NewRunner(
		Target{Name: "java", BaseURL: java.URL},
		Target{Name: "go", BaseURL: "http://127.0.0.1:1"}, // closed port
	)
	res := r.RunScenario(context.Background(), s)
	assert.Equal(t, VerdictError, res.Verdict)
	assert.Contains(t, res.Reason, "go errored")
}

func TestRun_ExtractAndPathFromVar(t *testing.T) {
	t.Parallel()

	var nextURICalls int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/statement":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"q1","nextUri":"%s/v1/statement/q1/1"}`, "http://"+r.Host)))
		case "/v1/statement/q1/1":
			atomic.AddInt32(&nextURICalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"q1"}`)) // no nextUri → terminal
		default:
			http.NotFound(w, r)
		}
	}))
	defer gw.Close()

	s := &Scenario{
		Name: "poll-loop",
		Steps: []Step{
			{
				Method: "POST", Path: "/v1/statement", Body: "SELECT 1",
				Extract: map[string]string{"next": "$.nextUri"},
			},
			{
				Method:      "GET",
				PathFromVar: "next",
			},
		},
	}

	vars := map[string]string{}
	resp, err := Run(context.Background(), http.DefaultClient,
		Target{Name: "x", BaseURL: gw.URL}, s, vars)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.NotEmpty(t, vars["next"], "extract should populate vars")
	assert.Equal(t, int32(1), atomic.LoadInt32(&nextURICalls))
}

func TestRun_RepeatUntilNoField(t *testing.T) {
	t.Parallel()

	var hits int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			_, _ = w.Write([]byte(fmt.Sprintf(`{"nextUri":"%s/v1/poll"}`, "http://"+r.Host)))
			return
		}
		_, _ = w.Write([]byte(`{}`)) // no nextUri → loop terminates
	}))
	defer gw.Close()

	s := &Scenario{
		Name: "poll-loop",
		Steps: []Step{{
			Method:      "GET",
			Path:        "/v1/poll",
			Extract:     map[string]string{"nextUri": "$.nextUri"},
			RepeatUntil: &RepeatPolicy{NoField: "nextUri", MaxIterations: 10},
		}},
	}

	resp, err := Run(context.Background(), http.DefaultClient,
		Target{Name: "x", BaseURL: gw.URL}, s, map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&hits), int32(3),
		"loop should have iterated until terminal response")
}

// newFakeGateway is a minimal fake that responds to POST /v1/statement with
// a fixed queryId and a nextUri pointing at its own host. The host:port WILL
// differ between two instances, which is why scenarios use RewriteHostPort.
func newFakeGateway(t *testing.T, queryID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/statement" && r.Method == "POST" {
			body, _ := json.Marshal(map[string]any{
				"id":      queryID,
				"nextUri": "http://" + r.Host + "/v1/statement/" + queryID + "/1",
				"stats":   map[string]any{"state": "QUEUED"},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}
