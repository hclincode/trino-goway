// Package engine defines the RoutingMethod provider interface, the Decision and
// RouteInput types, and shared helpers available to all providers.
package engine

import "context"

// Decision is the outcome of a single RoutingMethod evaluation.
// Decided==false means "no opinion — continue to the next method".
type Decision struct {
	// RoutingGroup is the chosen group name. Meaningful only when Decided==true.
	// Empty string with Decided==true is a valid hard decision to defer — the
	// pipeline treats it as "use default group".
	RoutingGroup string
	// ExternalHeaders are headers to add/override on the proxied request.
	// Applied only when Decided==true and the map is non-nil.
	ExternalHeaders map[string]string
	// Errors are hard policy-violation messages (e.g. "user not permitted").
	// Non-empty + propagateErrors=true → HTTP 400 to the client.
	// Never use for "no rule matched" — return Decided==false instead.
	Errors []string
	// Decided is true when this method made a definitive routing call.
	// False means "no opinion"; the pipeline skips to the next method.
	Decided bool
}

// RouteInput is the normalised, provider-facing view of an inbound request.
// Built from proto via FromProto; all fields are safe to read (no nil pointers).
type RouteInput struct {
	// Source is the X-Trino-Source header value (e.g. "airflow", "superset").
	Source string
	// ClientTags is X-Trino-Client-Tags, pre-split on comma by the gateway.
	ClientTags []string
	// User is the X-Trino-User header value.
	User string
	// Catalog is the X-Trino-Catalog header value.
	Catalog string
	// Schema is the X-Trino-Schema header value.
	Schema string
	// Method is the HTTP method ("POST", "GET", …).
	Method string
	// URI is the request path + query, e.g. "/v1/statement".
	URI string
	// RemoteAddr is the client IP address.
	RemoteAddr string
	// Body is the raw SQL body (POST /v1/statement only); empty otherwise.
	Body string
	// IsNew is true for POST /v1/statement (new query submissions).
	// Providers should only make routing decisions when IsNew is true.
	IsNew bool
	// ParamMap holds URL + form parameters. Multi-valued params are comma-joined.
	ParamMap map[string]string
}

// RoutingMethod is the interface every routing provider must implement.
// Providers are registered in a Registry and composed by the Pipeline.
type RoutingMethod interface {
	// Type returns the provider type name, e.g. "expr" or "script".
	// Used by the registry for lookup and by logs/metrics for labelling.
	Type() string
	// LoadConfig parses and validates raw configuration bytes.
	// Called at startup and on every hot-reload event.
	// Must return an error without modifying the active program/script if
	// the new config is invalid (keep-last-good semantics).
	LoadConfig(raw []byte) error
	// Evaluate runs the routing logic for the given request.
	// Returns Decision.Decided==false to pass control to the next method.
	// Must never return a non-nil error — swallow errors internally, log a
	// warning, and return Decision{Decided: false}.
	Evaluate(ctx context.Context, in *RouteInput) (Decision, error)
}

// HashPct returns a deterministic 0–99 integer for s using FNV-1a mod 100.
// Available to all providers as a built-in helper for canary/blue-green splits:
//
//	hashPct(user) < 5 → "canary"
//
// The implementation is intentionally simple and stable: changing it is a
// breaking change for any rule that relies on bucket assignments.
func HashPct(s string) int {
	// FNV-1a 64-bit, then mod 100.
	const (
		fnvOffset uint64 = 14695981039346656037
		fnvPrime  uint64 = 1099511628211
	)
	h := fnvOffset
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= fnvPrime
	}
	return int(h % 100)
}
