package diffharness

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize_IgnoreHeaders_Drops(t *testing.T) {
	t.Parallel()

	in := Response{
		StatusCode: 200,
		Headers: http.Header{
			"Date":         {"Wed, 27 May 2026 03:00:00 GMT"},
			"Content-Type": {"application/json"},
		},
		Body: []byte(`{"id":"q1"}`),
	}
	got := Normalize(in, DiffPolicy{IgnoreHeaders: []string{"Date"}}, "")
	assert.Empty(t, got.Headers.Get("Date"))
	assert.Equal(t, "application/json", got.Headers.Get("Content-Type"))
}

func TestNormalize_RewriteHostPort_BodyAndHeaders(t *testing.T) {
	t.Parallel()

	gatewayHost := "127.0.0.1:34567"
	in := Response{
		StatusCode: 200,
		Headers: http.Header{
			"Location": {"http://" + gatewayHost + "/v1/statement/queued/q1"},
		},
		Body: []byte(`{"nextUri":"http://` + gatewayHost + `/v1/statement/queued/q1/1"}`),
	}
	got := Normalize(in, DiffPolicy{RewriteHostPort: true}, gatewayHost)

	assert.Contains(t, got.Headers.Get("Location"), HostNormalizationToken)
	assert.NotContains(t, got.Headers.Get("Location"), gatewayHost)
	assert.Contains(t, string(got.Body), HostNormalizationToken)
	assert.NotContains(t, string(got.Body), gatewayHost)
}

func TestNormalize_IgnoreBodyFields_StripsNested(t *testing.T) {
	t.Parallel()

	in := Response{
		Body: []byte(`{"id":"q1","stats":{"processedRows":42,"elapsedTimeMillis":99}}`),
	}
	got := Normalize(in, DiffPolicy{IgnoreBodyFields: []string{"stats.processedRows"}}, "")
	require.Contains(t, string(got.Body), `"id":"q1"`)
	assert.NotContains(t, string(got.Body), `processedRows`,
		"stats.processedRows should be stripped")
	assert.Contains(t, string(got.Body), `elapsedTimeMillis`,
		"only the named field should be stripped")
}

func TestNormalize_NonJSONBody_LeavesUnchanged(t *testing.T) {
	t.Parallel()

	in := Response{Body: []byte("plain text body, not json")}
	got := Normalize(in, DiffPolicy{IgnoreBodyFields: []string{"anything"}}, "")
	assert.Equal(t, in.Body, got.Body,
		"non-JSON body must pass through untouched so the diff fails loudly")
}

func TestNormalize_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	original := Response{
		Headers: http.Header{"Date": {"original"}},
		Body:    []byte(`{"id":"q1"}`),
	}
	_ = Normalize(original, DiffPolicy{IgnoreHeaders: []string{"Date"}}, "")
	assert.Equal(t, "original", original.Headers.Get("Date"),
		"Normalize must not mutate its input")
}

func TestStripJSONFields_MalformedJSON_Passthrough(t *testing.T) {
	t.Parallel()

	body := []byte("{not valid json")
	got := stripJSONFields(body, []string{"id"})
	assert.Equal(t, body, got)
}

func TestStripJSONFields_EmptyBody_Passthrough(t *testing.T) {
	t.Parallel()

	got := stripJSONFields(nil, []string{"id"})
	assert.Nil(t, got)
}

// TestStripJSONFields_TopLevelArray strips a field from every element of a
// top-level JSON array body (e.g. /gateway/backend/all → [{...},{...}]). Before
// Task #23 this silently no-op'd because deleteJSONPath only handled maps.
func TestStripJSONFields_TopLevelArray(t *testing.T) {
	t.Parallel()

	body := []byte(`[{"a":1,"keep":2},{"a":3,"keep":4}]`)
	got := stripJSONFields(body, []string{"a"})
	assert.JSONEq(t, `[{"keep":2},{"keep":4}]`, string(got),
		"the named field must be stripped from every array element; other fields preserved")
}

// TestStripJSONFields_NestedArray strips a dotted path that descends through an
// object into an array of objects (stats.subStages → [{...}]).
func TestStripJSONFields_NestedArray(t *testing.T) {
	t.Parallel()

	body := []byte(`{"stats":{"subStages":[{"t":1,"keep":9},{"t":2,"keep":8}]}}`)
	got := stripJSONFields(body, []string{"stats.subStages.t"})
	assert.JSONEq(t, `{"stats":{"subStages":[{"keep":9},{"keep":8}]}}`, string(got),
		"dotted path must descend object→array and strip the field from each element")
}

// TestStripJSONFields_MapUnchanged is a regression guard that the array support
// did not alter the existing object-body behavior.
func TestStripJSONFields_MapUnchanged(t *testing.T) {
	t.Parallel()

	body := []byte(`{"id":"q1","stats":{"processedRows":42,"elapsedTimeMillis":99}}`)
	got := stripJSONFields(body, []string{"stats.processedRows"})
	assert.JSONEq(t, `{"id":"q1","stats":{"elapsedTimeMillis":99}}`, string(got),
		"object-body stripping must be unchanged by the array support")
}

func TestLoadScenario_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/scenario.yaml"
	yaml := `
name: smoke
description: simple body passthrough
steps:
  - method: POST
    path: /v1/statement
    headers:
      X-Trino-User: diff-harness
    body: "SELECT 1"
diff:
  ignoreHeaders: [Date]
  rewriteHostPort: true
`
	require.NoError(t, writeFile(t, path, yaml))

	s, err := LoadScenario(path)
	require.NoError(t, err)
	assert.Equal(t, "smoke", s.Name)
	require.Len(t, s.Steps, 1)
	assert.Equal(t, "POST", s.Steps[0].Method)
	assert.Equal(t, "diff-harness", s.Steps[0].Headers["X-Trino-User"])
	assert.True(t, s.Diff.RewriteHostPort)
	assert.Contains(t, s.Diff.IgnoreHeaders, "Date")
}

func TestLoadScenario_MissingName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/bad.yaml"
	require.NoError(t, writeFile(t, path, `steps: [{method: GET, path: /}]`))
	_, err := LoadScenario(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field 'name'")
}

func TestLoadScenario_NoSteps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/bad.yaml"
	require.NoError(t, writeFile(t, path, `name: x`))
	_, err := LoadScenario(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one step")
}

// writeFile is a small helper that fails the test on error.
func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return writeFileBytes(path, []byte(strings.TrimSpace(content)+"\n"))
}
