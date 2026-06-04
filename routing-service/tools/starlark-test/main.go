// Command starlark-test loads a Starlark routing script, builds the request
// context from a given input, runs route(req), and prints the execution result.
// It uses the same ScriptProvider as the production service, so results match
// exactly what the service would return.
//
// Usage:
//
//	starlark-test <script-path> <input>
//	starlark-test <script-path> --samples <path> [--expect <path>]
//
// arg1: path to the .star file (must define def route(req):)
// arg2: inline JSON or path to a .json file
//
//	{"source":"airflow","user":"alice","is_new":true}
//
// Exit codes: 0=ok/defer, 1=error, 2=expectation mismatch
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolinput"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolrun"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// arg1 must be the script path (first non-flag argument). Extract it before
	// flag.Parse so that flags may appear after the script path in any order.
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: starlark-test <script-path> <input>")
		fmt.Fprintln(os.Stderr, "       starlark-test <script-path> --samples <path>")
		return toolrun.ExitError
	}
	scriptPath := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("starlark-test", flag.ContinueOnError)
	samplesPath := fs.String("samples", "", "YAML batch file of RouteInput records")
	expectPath  := fs.String("expect", "", "YAML map of {sample_id: expected_group} (requires --samples)")
	verbose     := fs.Bool("verbose", false, "print deserialized RouteInput before result")
	maxSteps    := fs.Uint64("max-steps", 0, "override step budget (default: production value 10000)")

	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return toolrun.ExitError
	}

	positional := fs.Args()
	// scriptPath already consumed above.

	// Load and compile the script via the production provider.
	p := scriptprovider.New()
	if *maxSteps > 0 {
		p.SetMaxSteps(*maxSteps)
	}
	src, err := os.ReadFile(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read script %q: %v\n", scriptPath, err)
		return toolrun.ExitError
	}
	cfg := buildConfig(string(src))
	if err := p.LoadConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return toolrun.ExitError
	}

	// Batch mode: --samples provided.
	if *samplesPath != "" {
		return runBatch(p, *samplesPath, *expectPath, *verbose)
	}

	// Single-input mode: arg2 is the first remaining positional after script path.
	if len(positional) < 1 {
		fmt.Fprintln(os.Stderr, "usage: starlark-test <script-path> <input>")
		return toolrun.ExitError
	}
	in, err := toolinput.Parse(positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return toolrun.ExitError
	}

	r := toolrun.Run(context.Background(), p, in)
	toolrun.PrintSingle(os.Stdout, r, *verbose, in)

	if r.Status != toolrun.StatusOK && r.Status != toolrun.StatusDeferred {
		return toolrun.ExitError
	}
	return toolrun.ExitOK
}

func runBatch(p *scriptprovider.Provider, samplesPath, expectPath string, verbose bool) int {
	samples, err := toolrun.LoadSamples(samplesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return toolrun.ExitError
	}

	var expect map[string]string
	if expectPath != "" {
		expect, err = toolrun.LoadExpect(expectPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return toolrun.ExitError
		}
	}

	rows := make([]toolrun.BatchRow, 0, len(samples))
	mismatches := 0

	for _, s := range samples {
		in := s.ToRouteInput()
		r := toolrun.Run(context.Background(), p, in)
		if verbose {
			toolrun.PrintSingle(os.Stdout, r, true, in)
		}
		row := toolrun.BatchRow{
			ID:      s.ID,
			Group:   r.Group,
			Latency: r.Latency,
			Status:  r.Status,
		}
		if expect != nil {
			if wantGroup, ok := expect[s.ID]; ok {
				if r.Group != wantGroup {
					row.Status = fmt.Sprintf("MISMATCH (want %q)", wantGroup)
					mismatches++
				}
			}
		}
		rows = append(rows, row)
	}

	toolrun.PrintTable(os.Stdout, rows)

	if mismatches > 0 {
		fmt.Fprintf(os.Stderr, "\n%d expectation(s) failed\n", mismatches)
		return toolrun.ExitExpectFail
	}
	return toolrun.ExitOK
}

// buildConfig wraps a source string into the YAML format LoadConfig expects.
func buildConfig(src string) []byte {
	yaml := "type: script\nprogram: |\n"
	for _, line := range splitLines(src) {
		yaml += "  " + line + "\n"
	}
	return []byte(yaml)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
