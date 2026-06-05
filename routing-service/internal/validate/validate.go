// Package validate implements the dry-run logic behind the
// routing-service-validate CLI: it loads a config exactly as the running
// service would (config.Load + registry.Build + engine.Pipeline), optionally
// evaluates it against a batch of sample requests, and optionally diffs the
// routing outcomes against a baseline config.
//
// Keeping the logic here (rather than in package main) makes it unit-testable
// and keeps cmd/routing-service-validate a thin wrapper.
package validate

import (
	"context"
	"fmt"
	"io"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
)

// Exit codes (mirrors the CLI contract).
const (
	// ExitOK means the config is valid and (in diff mode) no routes changed.
	ExitOK = 0
	// ExitInvalid means the config failed to parse, validate, or compile.
	ExitInvalid = 1
	// ExitDiff means --diff detected at least one changed routing outcome.
	ExitDiff = 2
)

// Options configures a single Run.
type Options struct {
	// ConfigPath is the config under test (required).
	ConfigPath string
	// SamplesPath, if set, is a YAML batch of sample RouteInput records to route.
	SamplesPath string
	// Diff enables comparison against BaselinePath. Requires BaselinePath and
	// SamplesPath.
	Diff bool
	// BaselinePath is the baseline config to diff against when Diff is true.
	BaselinePath string
}

// NewRegistry builds a registry with the production providers registered. It is
// the same provider set main.go wires, so validation matches what the service
// would load.
func NewRegistry() *engine.Registry {
	reg := engine.NewRegistry()
	reg.Register("expr", func() engine.RoutingMethod { return exprovider.New() })
	reg.Register("script", func() engine.RoutingMethod { return scriptprovider.New() })
	return reg
}

// Run executes the dry-run described by opts, writing human-readable output to
// out and errors to errOut, and returns the process exit code.
//
// Behaviour:
//   - Always: load+validate ConfigPath via the production path. On failure,
//     print the precise error and return ExitInvalid.
//   - Without SamplesPath: print "OK" and return ExitOK.
//   - With SamplesPath: route every sample and print a table
//     (SAMPLE | INPUT | GROUP). GROUP is "—" when the pipeline defers.
//   - With Diff: also load the baseline config, route the same samples through
//     it, and mark rows where the group changed; return ExitDiff if any did.
func Run(out, errOut io.Writer, opts Options) int {
	if opts.ConfigPath == "" {
		_, _ = fmt.Fprintln(errOut, "error: --config is required")
		return ExitInvalid
	}

	reg := NewRegistry()

	pipeline, err := buildPipeline(reg, opts.ConfigPath)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return ExitInvalid
	}

	// No samples: this is a pure validity check.
	if opts.SamplesPath == "" {
		_, _ = fmt.Fprintln(out, "OK")
		return ExitOK
	}

	samples, err := loadSamples(opts.SamplesPath)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "error: %v\n", err)
		return ExitInvalid
	}

	// Diff mode: build the baseline pipeline up front so a broken baseline is a
	// config error (ExitInvalid), not a spurious diff.
	var baseline *engine.Pipeline
	if opts.Diff {
		if opts.BaselinePath == "" {
			_, _ = fmt.Fprintln(errOut, "error: --diff requires --baseline")
			return ExitInvalid
		}
		baseline, err = buildPipeline(reg, opts.BaselinePath)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "error: baseline: %v\n", err)
			return ExitInvalid
		}
	}

	rows := make([]row, 0, len(samples))
	changed := 0
	for _, s := range samples {
		in := s.ToRouteInput()
		newGroup := pipeline.Evaluate(context.Background(), in)
		r := row{
			id:       s.ID,
			summary:  inputSummary(s.Source, s.User),
			newGroup: newGroup,
		}
		if baseline != nil {
			r.oldGroup = baseline.Evaluate(context.Background(), in)
			if r.oldGroup != r.newGroup {
				r.changed = true
				changed++
			}
		}
		rows = append(rows, r)
	}

	printTable(out, rows, baseline != nil)

	if changed > 0 {
		_, _ = fmt.Fprintf(errOut, "\n%d route(s) changed vs baseline\n", changed)
		return ExitDiff
	}
	return ExitOK
}

// buildPipeline loads the config at path via the production path and returns a
// live pipeline. Any parse/validate/compile error is returned to the caller.
func buildPipeline(reg *engine.Registry, path string) (*engine.Pipeline, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	methods := make([]engine.RoutingMethod, 0, len(cfg.Methods))
	for i, mc := range cfg.Methods {
		m, err := reg.Build(mc)
		if err != nil {
			return nil, fmt.Errorf("methods[%d] (%s): %w", i, mc.Type, err)
		}
		methods = append(methods, m)
	}
	// Discard logger: the CLI surfaces decisions through the table, not logs.
	return engine.NewPipeline(methods, cfg.DefaultRoutingGroup, discardLogger()), nil
}
