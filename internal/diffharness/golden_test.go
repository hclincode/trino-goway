package diffharness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGolden_RoundTrip_JSONBody(t *testing.T) {
	t.Parallel()

	resp := Response{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"q1","nextUri":"<GATEWAY>/v1/statement/q1/1"}`),
	}
	g := newGolden("seam1-body-passthrough", resp)
	assert.Equal(t, goldenFileVersion, g.Version)
	assert.JSONEq(t, string(resp.Body), string(g.Body))

	path := filepath.Join(t.TempDir(), "g.json")
	require.NoError(t, WriteGolden(path, g))

	loaded, err := ReadGolden(path)
	require.NoError(t, err)
	assert.Equal(t, g.Scenario, loaded.Scenario)
	assert.Equal(t, g.StatusCode, loaded.StatusCode)
	assert.JSONEq(t, string(g.Body), string(loaded.Body))

	rehydrated := loaded.toResponse()
	assert.Equal(t, 200, rehydrated.StatusCode)
	assert.Equal(t, "application/json", rehydrated.Headers.Get("Content-Type"))
	assert.JSONEq(t, string(resp.Body), string(rehydrated.Body))
}

func TestGolden_NonJSONBodyStoredAsString(t *testing.T) {
	t.Parallel()

	resp := Response{StatusCode: 503, Body: []byte("upstream unreachable")}
	g := newGolden("error-passthrough", resp)

	// Body must be a JSON-encoded string so the file is valid JSON.
	var s string
	require.NoError(t, json.Unmarshal(g.Body, &s))
	assert.Equal(t, "upstream unreachable", s)
}

func TestGolden_EmptyBodyIsNull(t *testing.T) {
	t.Parallel()
	g := newGolden("204-no-content", Response{StatusCode: 204})
	assert.Equal(t, "null", string(g.Body))
	r := g.toResponse()
	assert.Nil(t, r.Body)
}

func TestReadGolden_VersionMismatch(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":999,"scenario":"x"}`), 0o644))

	_, err := ReadGolden(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 999")
}

func TestReadGolden_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := ReadGolden(filepath.Join(t.TempDir(), "missing.json"))
	require.Error(t, err)
}

func TestGoldenPath_JoinsCleanly(t *testing.T) {
	t.Parallel()
	assert.Equal(t, filepath.Join("a", "b", "scenario.json"),
		GoldenPath(filepath.Join("a", "b"), "scenario"))
}

func TestRunner_RecordAndReplay_RoundTrip(t *testing.T) {
	t.Parallel()

	// Java side: stable response we can record.
	java := newFakeGateway(t, "q1")
	// Go side: same payload after ignoring id+nextUri → replay should PASS.
	goGW := newFakeGateway(t, "q1")

	s := &Scenario{
		Name:  "seam1-body-passthrough",
		Steps: []Step{{Method: "POST", Path: "/v1/statement", Body: "SELECT 1"}},
		Diff: DiffPolicy{
			IgnoreHeaders:    []string{"Date", "Content-Length"},
			IgnoreBodyFields: []string{"id", "nextUri"},
			RewriteHostPort:  true,
		},
	}

	r := NewRunner(Target{Name: "java", BaseURL: java.URL}, Target{Name: "go", BaseURL: goGW.URL})

	g, err := r.RecordScenario(context.Background(), s)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), s.Name+".json")
	require.NoError(t, WriteGolden(path, g))

	loaded, err := ReadGolden(path)
	require.NoError(t, err)

	res := r.ReplayScenario(context.Background(), s, loaded)
	assert.Equalf(t, VerdictPass, res.Verdict,
		"expected PASS — got %s, body diff=%q", res.Verdict, res.BodyDiff)
}

func TestRunner_ReplayScenario_NameMismatch(t *testing.T) {
	t.Parallel()

	goGW := newFakeGateway(t, "q1")
	r := NewRunner(Target{Name: "x", BaseURL: goGW.URL}, Target{Name: "x", BaseURL: goGW.URL})

	g := Golden{Version: goldenFileVersion, Scenario: "other"}
	res := r.ReplayScenario(context.Background(), &Scenario{
		Name:  "mine",
		Steps: []Step{{Method: "GET", Path: "/v1/statement"}},
	}, g)
	assert.Equal(t, VerdictError, res.Verdict)
	assert.Contains(t, res.Reason, "does not match")
}

func TestRunner_ReplayScenario_GoUnreachable(t *testing.T) {
	t.Parallel()

	g := Golden{Version: goldenFileVersion, Scenario: "x", StatusCode: 200, Body: json.RawMessage(`null`)}
	r := NewRunner(
		Target{Name: "java", BaseURL: "http://127.0.0.1:1"},
		Target{Name: "go", BaseURL: "http://127.0.0.1:1"},
	)
	res := r.ReplayScenario(context.Background(), &Scenario{
		Name:  "x",
		Steps: []Step{{Method: "GET", Path: "/v1/statement"}},
	}, g)
	assert.Equal(t, VerdictError, res.Verdict)
	assert.Contains(t, res.Reason, "go errored")
}

func TestRunner_RecordScenario_JavaUnreachable(t *testing.T) {
	t.Parallel()

	r := NewRunner(
		Target{Name: "java", BaseURL: "http://127.0.0.1:1"},
		Target{Name: "go", BaseURL: "http://127.0.0.1:1"},
	)
	_, err := r.RecordScenario(context.Background(), &Scenario{
		Name:  "x",
		Steps: []Step{{Method: "GET", Path: "/v1/statement"}},
	})
	require.Error(t, err)
}

// Sanity check: a recorded golden against a divergent Go side fails on replay.
func TestRunner_Replay_DivergentBody_Fail(t *testing.T) {
	t.Parallel()

	java := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"shape":"java"}`)
	}))
	defer java.Close()
	goGW := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"shape":"go"}`)
	}))
	defer goGW.Close()

	s := &Scenario{
		Name:  "shape-mismatch",
		Steps: []Step{{Method: "GET", Path: "/"}},
	}
	r := NewRunner(Target{Name: "java", BaseURL: java.URL}, Target{Name: "go", BaseURL: goGW.URL})

	g, err := r.RecordScenario(context.Background(), s)
	require.NoError(t, err)

	res := r.ReplayScenario(context.Background(), s, g)
	assert.Equal(t, VerdictFail, res.Verdict)
	assert.NotEmpty(t, res.BodyDiff)
}
