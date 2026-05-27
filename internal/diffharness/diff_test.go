package diffharness

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiff_IdenticalResponses_Pass(t *testing.T) {
	t.Parallel()
	r := Response{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"id":"q1"}`),
	}
	got := Diff(r, r, DiffPolicy{})
	assert.Equal(t, VerdictPass, got.Verdict)
	assert.True(t, got.StatusMatch)
	assert.Empty(t, got.HeaderDiffs)
	assert.Empty(t, got.BodyDiff)
}

func TestDiff_StatusMismatch_Fail(t *testing.T) {
	t.Parallel()
	got := Diff(
		Response{StatusCode: 200, Body: []byte(`{}`)},
		Response{StatusCode: 500, Body: []byte(`{}`)},
		DiffPolicy{},
	)
	assert.Equal(t, VerdictFail, got.Verdict)
	assert.False(t, got.StatusMatch)
}

func TestDiff_HeaderJavaOnly_Fail(t *testing.T) {
	t.Parallel()
	got := Diff(
		Response{StatusCode: 200, Headers: http.Header{"X-Java": {"v"}}, Body: []byte(`{}`)},
		Response{StatusCode: 200, Headers: http.Header{}, Body: []byte(`{}`)},
		DiffPolicy{},
	)
	assert.Equal(t, VerdictFail, got.Verdict)
	require.Len(t, got.HeaderDiffs, 1)
	assert.Equal(t, "X-Java", got.HeaderDiffs[0].Name)
	assert.Equal(t, "java-only", got.HeaderDiffs[0].Reason)
}

func TestDiff_HeaderGoOnly_Fail(t *testing.T) {
	t.Parallel()
	got := Diff(
		Response{StatusCode: 200, Headers: http.Header{}, Body: []byte(`{}`)},
		Response{StatusCode: 200, Headers: http.Header{"X-Go": {"v"}}, Body: []byte(`{}`)},
		DiffPolicy{},
	)
	require.Len(t, got.HeaderDiffs, 1)
	assert.Equal(t, "go-only", got.HeaderDiffs[0].Reason)
}

func TestDiff_HeaderValueMatchUnordered_Pass(t *testing.T) {
	t.Parallel()
	// Multi-valued header in different order on each side; should match.
	got := Diff(
		Response{StatusCode: 200, Headers: http.Header{"X-Multi": {"a", "b"}}, Body: []byte(`{}`)},
		Response{StatusCode: 200, Headers: http.Header{"X-Multi": {"b", "a"}}, Body: []byte(`{}`)},
		DiffPolicy{},
	)
	assert.Equal(t, VerdictPass, got.Verdict)
}

func TestDiff_BodyJSONStructurallyEqual_OrderIndependent(t *testing.T) {
	t.Parallel()
	// Different key order but structurally identical.
	got := Diff(
		Response{StatusCode: 200, Body: []byte(`{"id":"q1","nextUri":"x"}`)},
		Response{StatusCode: 200, Body: []byte(`{"nextUri":"x","id":"q1"}`)},
		DiffPolicy{},
	)
	assert.Equal(t, VerdictPass, got.Verdict, "JSON object key order must not matter")
}

func TestDiff_BodyDiffers_Fail(t *testing.T) {
	t.Parallel()
	got := Diff(
		Response{StatusCode: 200, Body: []byte(`{"id":"q1"}`)},
		Response{StatusCode: 200, Body: []byte(`{"id":"q2"}`)},
		DiffPolicy{},
	)
	assert.Equal(t, VerdictFail, got.Verdict)
	assert.NotEmpty(t, got.BodyDiff)
}

func TestWriteText_RendersPassAndFail(t *testing.T) {
	t.Parallel()
	results := []Result{
		{Scenario: "scn-a", StatusMatch: true, JavaStatus: 200, GoStatus: 200, Verdict: VerdictPass},
		{
			Scenario: "scn-b", JavaStatus: 200, GoStatus: 500,
			Verdict: VerdictFail,
			HeaderDiffs: []HeaderDiff{
				{Name: "X-Go", Go: []string{"v"}, Reason: "go-only"},
			},
			BodyDiff: "- foo\n+ bar",
		},
	}
	var buf bytes.Buffer
	s := WriteText(&buf, results)

	assert.Equal(t, 1, s.Pass)
	assert.Equal(t, 1, s.Fail)
	out := buf.String()
	assert.Contains(t, out, "=== scn-a ===")
	assert.Contains(t, out, "result:   PASS")
	assert.Contains(t, out, "=== scn-b ===")
	assert.Contains(t, out, "result:   FAIL")
	assert.Contains(t, out, "+ X-Go: v [go-only]")
	assert.Contains(t, out, "PASS 1 / FAIL 1 / SKIP 0 / ERROR 0")
}

func TestWriteJSON_ValidJSON(t *testing.T) {
	t.Parallel()
	results := []Result{
		{Scenario: "scn-a", Verdict: VerdictPass, StatusMatch: true, JavaStatus: 200, GoStatus: 200},
	}
	var buf bytes.Buffer
	s, err := WriteJSON(&buf, results)
	require.NoError(t, err)
	assert.Equal(t, 1, s.Pass)
	assert.True(t, strings.Contains(buf.String(), `"scenario": "scn-a"`),
		"json output should include scenario name field")
}
