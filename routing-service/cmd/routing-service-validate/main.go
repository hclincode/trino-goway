// Command routing-service-validate is a dry-run CLI that validates a
// routing-service config without serving traffic, and optionally evaluates it
// against sample requests and diffs the routing outcomes against a baseline.
//
// It loads the config through the exact production path (config.Load +
// registry.Build + engine.Pipeline), so a passing validation means the running
// service would load the same config successfully.
//
// Usage:
//
//	routing-service-validate --config config.yaml
//	routing-service-validate --config config.yaml --samples samples.yaml
//	routing-service-validate --config new.yaml --samples samples.yaml \
//	    --diff --baseline current.yaml
//
// Exit codes:
//
//	0  config valid (and, in --diff mode, no routes changed)
//	1  config invalid: parse / validate / compile error
//	2  --diff detected at least one changed routing outcome (CI gate)
package main

import (
	"flag"
	"os"

	"github.com/hclincode/trino-goway-routing-service/internal/validate"
)

func main() {
	configPath := flag.String("config", "", "path to the config YAML to validate (required)")
	samplesPath := flag.String("samples", "", "YAML batch of sample RouteInput records to route")
	diff := flag.Bool("diff", false, "diff routing outcomes against --baseline (requires --samples)")
	baselinePath := flag.String("baseline", "", "baseline config to diff against (requires --diff)")
	flag.Parse()

	os.Exit(validate.Run(os.Stdout, os.Stderr, validate.Options{
		ConfigPath:   *configPath,
		SamplesPath:  *samplesPath,
		Diff:         *diff,
		BaselinePath: *baselinePath,
	}))
}
