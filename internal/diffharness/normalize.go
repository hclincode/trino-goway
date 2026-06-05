package diffharness

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HostNormalizationToken is the sentinel substituted for the gateway's
// host:port during normalization. Both sides normalize to this token, so a
// passing diff means structural equivalence after the host has been factored
// out.
const HostNormalizationToken = "<GATEWAY>"

// Response captures one side's HTTP response for diffing.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Normalize applies the scenario's DiffPolicy to a captured response.
// gatewayHost is the host:port the request was sent to (so it can be rewritten
// to HostNormalizationToken).
//
// Mutates a copy; the input is not modified.
func Normalize(r Response, policy DiffPolicy, gatewayHost string) Response {
	out := Response{
		StatusCode: r.StatusCode,
		Headers:    cloneHeaders(r.Headers),
		Body:       append([]byte(nil), r.Body...),
	}

	for _, name := range policy.IgnoreHeaders {
		out.Headers.Del(name)
	}

	if policy.RewriteHostPort && gatewayHost != "" {
		// Rewrite in headers (Location, Link, Set-Cookie sometimes carry it).
		for k, vv := range out.Headers {
			rewritten := make([]string, len(vv))
			for i, v := range vv {
				rewritten[i] = strings.ReplaceAll(v, gatewayHost, HostNormalizationToken)
			}
			out.Headers[k] = rewritten
		}
		// And in the body.
		out.Body = []byte(strings.ReplaceAll(string(out.Body), gatewayHost, HostNormalizationToken))
	}

	if len(policy.IgnoreBodyFields) > 0 {
		out.Body = stripJSONFields(out.Body, policy.IgnoreBodyFields)
	}

	return out
}

// stripJSONFields removes the given dotted-path fields from a JSON body.
// If the body is not valid JSON, returns it unchanged — leaving the diff
// to fail loudly rather than silently corrupting.
//
// Supports nested paths via "." separators (e.g. "stats.processedRows") and
// descends into JSON arrays: a dotted path is applied to every element of any
// array encountered along the way, so an array-of-objects body (e.g.
// /gateway/backend/all → [{...},{...}]) has the named field stripped from each
// element. Wildcards are NOT supported; keep the scenario explicit.
func stripJSONFields(body []byte, fields []string) []byte {
	if len(body) == 0 {
		return body
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	for _, field := range fields {
		doc = deleteJSONPath(doc, strings.Split(field, "."))
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// deleteJSONPath walks node along path and deletes the final key. It descends
// into both objects and arrays: when node is an array, the (remaining) path is
// applied to each element; when node is an object, the path's head key is
// followed. Returns the (possibly modified) root.
func deleteJSONPath(node any, path []string) any {
	if len(path) == 0 || node == nil {
		return node
	}
	switch n := node.(type) {
	case []any:
		// Apply the path to every element so list endpoints normalize too.
		for i, elem := range n {
			n[i] = deleteJSONPath(elem, path)
		}
		return n
	case map[string]any:
		if len(path) == 1 {
			delete(n, path[0])
			return n
		}
		if child, ok := n[path[0]]; ok {
			n[path[0]] = deleteJSONPath(child, path[1:])
		}
		return n
	default:
		// Scalar (or unsupported) node: nothing to delete here.
		return node
	}
}

func cloneHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		cp := make([]string, len(vv))
		copy(cp, vv)
		out[k] = cp
	}
	return out
}
