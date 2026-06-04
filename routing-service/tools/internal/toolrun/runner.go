// Package toolrun provides shared output formatting, batch YAML loading,
// and exit-code conventions for starlark-test and expr-test.
package toolrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolinput"
)

// ExitCode constants used by both tools.
const (
	ExitOK          = 0 // success or defer — no error
	ExitError       = 1 // compile/validation/runtime error
	ExitExpectFail  = 2 // --expect mismatch
)

// Status strings for single-input output.
const (
	StatusOK        = "OK"
	StatusDeferred  = "DEFERRED"
	StatusStepLimit = "STEP_LIMIT"
)

// Result holds the outcome of a single evaluation.
type Result struct {
	Group   string        // routing group, or "" when deferred
	Latency time.Duration
	Status  string // OK, DEFERRED, STEP_LIMIT, COMPILE_ERROR: ..., RUNTIME_ERROR: ..., ERROR: ...
}

// PrintSingle prints a single-input result in the documented format:
//
//	group:   etl
//	latency: 0.14ms
//	status:  OK
func PrintSingle(w io.Writer, r Result, verbose bool, in *engine.RouteInput) {
	if verbose && in != nil {
		_, _ = fmt.Fprintf(w, "input:\n")
		_, _ = fmt.Fprintf(w, "  source:      %q\n", in.Source)
		_, _ = fmt.Fprintf(w, "  user:        %q\n", in.User)
		_, _ = fmt.Fprintf(w, "  client_tags: %v\n", in.ClientTags)
		_, _ = fmt.Fprintf(w, "  is_new:      %v\n", in.IsNew)
		_, _ = fmt.Fprintf(w, "  catalog:     %q\n", in.Catalog)
		_, _ = fmt.Fprintf(w, "  schema:      %q\n", in.Schema)
		_, _ = fmt.Fprintf(w, "\n")
	}
	grp := r.Group
	if grp == "" {
		grp = "—" // em-dash for deferred
	}
	_, _ = fmt.Fprintf(w, "group:   %s\n", grp)
	_, _ = fmt.Fprintf(w, "latency: %s\n", fmtLatency(r.Latency))
	_, _ = fmt.Fprintf(w, "status:  %s\n", r.Status)
}

// BatchRow is one row in the batch output table.
type BatchRow struct {
	ID      string
	Group   string
	Latency time.Duration
	Status  string
}

// PrintTable prints the batch results table.
func PrintTable(w io.Writer, rows []BatchRow) {
	const (
		colSample  = 24
		colGroup   = 16
		colLatency = 10
	)
	_, _ = fmt.Fprintf(w, "%-*s %-*s %-*s %s\n",
		colSample, "SAMPLE",
		colGroup, "GROUP",
		colLatency, "LATENCY",
		"STATUS")
	for _, r := range rows {
		grp := r.Group
		if grp == "" {
			grp = "(deferred)"
		}
		_, _ = fmt.Fprintf(w, "%-*s %-*s %-*s %s\n",
			colSample, r.ID,
			colGroup, grp,
			colLatency, fmtLatency(r.Latency),
			r.Status)
	}
}

// fmtLatency formats a duration as a compact latency string.
func fmtLatency(d time.Duration) string {
	if d < time.Microsecond {
		return fmt.Sprintf("%.0fns", float64(d.Nanoseconds()))
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fµs", float64(d.Nanoseconds())/1000)
	}
	return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
}

// Evaluator is the common interface both providers satisfy for single-input
// evaluation (matches both ExprProvider and ScriptProvider's Evaluate).
type Evaluator interface {
	Evaluate(ctx context.Context, in *engine.RouteInput) (engine.Decision, error)
}

// Run calls Evaluate on the provider and wraps the result.
// It distinguishes step-limit errors from other runtime errors via the error
// message string (both Starlark and the pipeline surface them as errors).
func Run(ctx context.Context, eval Evaluator, in *engine.RouteInput) Result {
	start := time.Now()
	d, err := eval.Evaluate(ctx, in)
	latency := time.Since(start)

	if err != nil {
		msg := err.Error()
		status := "ERROR: " + msg
		// Starlark step limit surfaces as "cancelled" or "too many steps" in the error.
		if containsAny(msg, "too many steps", "cancelled", "cancel", "step") {
			status = StatusStepLimit
		}
		return Result{Latency: latency, Status: status}
	}

	if !d.Decided {
		return Result{Latency: latency, Status: StatusDeferred}
	}
	return Result{Group: d.RoutingGroup, Latency: latency, Status: StatusOK}
}

// containsAny returns true if s contains any of the substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) {
			for i := range len(s) - len(sub) + 1 {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// LoadSamples reads a YAML file containing a list of SampleRecord entries.
func LoadSamples(path string) ([]toolinput.SampleRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read samples file %q: %w", path, err)
	}
	var samples []toolinput.SampleRecord
	if err := yaml.Unmarshal(data, &samples); err != nil {
		return nil, fmt.Errorf("parse samples YAML %q: %w", path, err)
	}
	return samples, nil
}

// LoadExpect reads a YAML file containing a map of sample_id → expected_group.
func LoadExpect(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read expect file %q: %w", path, err)
	}
	var expect map[string]string
	if err := yaml.Unmarshal(data, &expect); err != nil {
		return nil, fmt.Errorf("parse expect YAML %q: %w", path, err)
	}
	return expect, nil
}
