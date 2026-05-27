package diffharness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// goldenFileVersion is the on-disk version tag for golden files. Bump when
// the schema changes in a way that pre-bumped goldens must be re-recorded.
const goldenFileVersion = 1

// Golden is the on-disk representation of a recorded Java-side normalized
// response. The harness reads these in replay mode and diffs the live Go-side
// response against them.
//
// Headers is the post-normalization header set (after IgnoreHeaders +
// RewriteHostPort). Body is the post-normalization body bytes (after
// IgnoreBodyFields + RewriteHostPort). Storing the normalized form means
// replay does not need to re-apply the policy to the Java side — only to the
// Go side.
type Golden struct {
	Version    int                 `json:"version"`
	Scenario   string              `json:"scenario"`
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       json.RawMessage     `json:"body"`
}

// toResponse rehydrates a Golden into a Response for diffing. The body is the
// raw bytes from the JSON document (already normalized).
func (g Golden) toResponse() Response {
	h := make(http.Header, len(g.Headers))
	for k, vv := range g.Headers {
		h[k] = append([]string(nil), vv...)
	}
	body := []byte(g.Body)
	if len(body) == 0 || string(body) == "null" {
		body = nil
	}
	return Response{
		StatusCode: g.StatusCode,
		Headers:    h,
		Body:       body,
	}
}

// newGolden builds a Golden from a normalized Response. The body may be empty
// or non-JSON; we store it as a JSON string in that case so the file is always
// valid JSON.
func newGolden(scenario string, r Response) Golden {
	headers := make(map[string][]string, len(r.Headers))
	for k, vv := range r.Headers {
		headers[k] = append([]string(nil), vv...)
	}

	var body json.RawMessage
	if len(r.Body) == 0 {
		body = json.RawMessage(`null`)
	} else if json.Valid(r.Body) {
		body = append(json.RawMessage(nil), r.Body...)
	} else {
		// Wrap as a JSON string so the golden file is always parseable.
		quoted, _ := json.Marshal(string(r.Body))
		body = quoted
	}
	return Golden{
		Version:    goldenFileVersion,
		Scenario:   scenario,
		StatusCode: r.StatusCode,
		Headers:    headers,
		Body:       body,
	}
}

// WriteGolden serializes g to disk at path with indentation suitable for diff
// review. Creates the parent directory if missing.
func WriteGolden(path string, g Golden) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("golden: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("golden: marshal: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("golden: write %s: %w", path, err)
	}
	return nil
}

// ReadGolden loads a Golden from disk and validates its version.
func ReadGolden(path string) (Golden, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Golden{}, fmt.Errorf("golden: read %s: %w", path, err)
	}
	var g Golden
	if err := json.Unmarshal(raw, &g); err != nil {
		return Golden{}, fmt.Errorf("golden: parse %s: %w", path, err)
	}
	if g.Version != goldenFileVersion {
		return Golden{}, fmt.Errorf("golden: %s: version %d (expected %d) — re-record",
			path, g.Version, goldenFileVersion)
	}
	return g, nil
}

// GoldenPath returns the conventional path for a scenario's golden file under
// the given directory.
func GoldenPath(dir, scenarioName string) string {
	return filepath.Join(dir, scenarioName+".json")
}
