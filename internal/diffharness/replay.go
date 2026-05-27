package diffharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Target is one gateway the harness sends requests to.
type Target struct {
	Name    string // "java" or "go"
	BaseURL string // e.g. "http://127.0.0.1:34567"
}

// Host returns the host:port portion of BaseURL for normalization.
func (t Target) Host() string {
	u, err := url.Parse(t.BaseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// defaultStepTimeout caps any single HTTP step. Scenarios with long polls
// should set their own context via RunWithContext.
const defaultStepTimeout = 30 * time.Second

// Run executes the scenario against a single target and returns the FINAL
// response (the last step's response). Intermediate responses are used only
// for Extract — they are not part of the diff.
//
// vars is mutated as steps run (Extract writes to it). Pass an empty map per
// run; do not share across targets.
func Run(ctx context.Context, client *http.Client, target Target, scenario *Scenario, vars map[string]string) (Response, error) {
	if vars == nil {
		vars = make(map[string]string)
	}
	var last Response
	for i, step := range scenario.Steps {
		resp, err := executeStep(ctx, client, target, step, vars)
		if err != nil {
			return Response{}, fmt.Errorf("scenario %s step %d (%s %s): %w",
				scenario.Name, i, step.Method, step.Path, err)
		}
		applyExtract(resp.Body, step.Extract, vars)
		last = resp

		if step.RepeatUntil != nil {
			final, err := runRepeat(ctx, client, target, step, vars, resp)
			if err != nil {
				return Response{}, fmt.Errorf("scenario %s step %d repeat: %w",
					scenario.Name, i, err)
			}
			last = final
		}
	}
	return last, nil
}

// runRepeat loops the given step until RepeatUntil's termination condition is
// met or MaxIterations is reached.
func runRepeat(ctx context.Context, client *http.Client, target Target, step Step, vars map[string]string, first Response) (Response, error) {
	maxIter := step.RepeatUntil.MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	current := first
	for i := 0; i < maxIter; i++ {
		if step.RepeatUntil.NoField != "" && !bodyHasField(current.Body, step.RepeatUntil.NoField) {
			return current, nil
		}
		// Re-execute with the same step config; PathFromVar will be re-resolved
		// from vars so a fresh nextUri is picked up.
		resp, err := executeStep(ctx, client, target, step, vars)
		if err != nil {
			return current, err
		}
		applyExtract(resp.Body, step.Extract, vars)
		current = resp
	}
	return current, nil
}

func executeStep(ctx context.Context, client *http.Client, target Target, step Step, vars map[string]string) (Response, error) {
	urlStr, err := resolveURL(target, step, vars)
	if err != nil {
		return Response{}, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, defaultStepTimeout)
	defer cancel()

	var body io.Reader
	if step.Body != "" {
		body = strings.NewReader(step.Body)
	}
	req, err := http.NewRequestWithContext(reqCtx, step.Method, urlStr, body)
	if err != nil {
		return Response{}, err
	}
	for k, v := range step.Headers {
		req.Header.Set(k, v)
	}

	httpResp, err := client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("read body: %w", err)
	}
	return Response{
		StatusCode: httpResp.StatusCode,
		Headers:    httpResp.Header.Clone(),
		Body:       raw,
	}, nil
}

func resolveURL(target Target, step Step, vars map[string]string) (string, error) {
	if step.PathFromVar != "" {
		v, ok := vars[step.PathFromVar]
		if !ok {
			return "", fmt.Errorf("pathFromVar %q not set by any prior extract", step.PathFromVar)
		}
		// If the variable already encodes a full URL pointing at the original
		// gateway, replace its host with this target's host so the same step
		// can be replayed against both sides.
		if u, err := url.Parse(v); err == nil && u.Scheme != "" && u.Host != "" {
			tu, err := url.Parse(target.BaseURL)
			if err != nil {
				return "", err
			}
			u.Scheme = tu.Scheme
			u.Host = tu.Host
			return u.String(), nil
		}
		return strings.TrimRight(target.BaseURL, "/") + "/" + strings.TrimLeft(v, "/"), nil
	}
	return strings.TrimRight(target.BaseURL, "/") + "/" + strings.TrimLeft(step.Path, "/"), nil
}

// applyExtract pulls JSONPath-ish fields out of body and writes them to vars.
// Supports two forms today: "$.field" (top-level) and "$.a.b" (nested).
// Wildcards and array indexing are intentionally NOT supported; scenarios
// stay narrow.
func applyExtract(body []byte, extract map[string]string, vars map[string]string) {
	if len(extract) == 0 {
		return
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return
	}
	for name, path := range extract {
		v := lookupJSONPath(doc, strings.TrimPrefix(path, "$."))
		if v == "" {
			continue
		}
		vars[name] = v
	}
}

func lookupJSONPath(node any, path string) string {
	parts := strings.Split(path, ".")
	cur := node
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	switch v := cur.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		return ""
	}
}

func bodyHasField(body []byte, field string) bool {
	if !bytes.Contains(body, []byte(`"`+field+`"`)) {
		return false
	}
	// Confirm structurally — substring match alone could trigger on a nested
	// string value happening to contain the field name.
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	return lookupJSONPath(doc, field) != ""
}
