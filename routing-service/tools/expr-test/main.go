// Command expr-test compiles an expr-lang routing program, evaluates it
// against a given input, and prints the result. Uses the same ExprProvider
// as the production service, so results match exactly.
//
// Usage:
//
//	expr-test <program-path> <input>
//	expr-test --program '<expr>' <input>
//	expr-test <program-path> --samples <path> [--expect <path>]
//
// arg1 (or --program): expr program source (file path or inline string)
// arg2: inline JSON or path to a .json file
//
//	{"source":"airflow","is_new":true}
//
// Exit codes: 0=ok/defer, 1=compile/validation/runtime error, 2=expectation mismatch
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolinput"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolrun"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Pre-scan for --program flag so we know if arg1 is a file path or not.
	// flags may appear before or after positional args; we parse flags first,
	// consuming --program if present, then treat the first non-flag arg as
	// the program file path (unless --program was given).
	fs := flag.NewFlagSet("expr-test", flag.ContinueOnError)
	programFlag := fs.String("program", "", "inline expr program source (mutually exclusive with arg1 file)")
	samplesPath := fs.String("samples", "", "YAML batch file of RouteInput records")
	expectPath  := fs.String("expect", "", "YAML map of {sample_id: expected_group} (requires --samples)")
	verbose     := fs.Bool("verbose", false, "print deserialized RouteInput before result")

	// If the first arg is not a flag, extract it as the program file before
	// flag.Parse so that flags may appear after it.
	var programFilePath string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && *programFlag == "" {
		programFilePath = args[0]
		rest = args[1:]
	}

	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return toolrun.ExitError
	}

	positional := fs.Args()

	// Determine the program source.
	var programSrc string
	if *programFlag != "" {
		// Inline program via --program flag.
		programSrc = *programFlag
	} else if programFilePath != "" {
		src, err := os.ReadFile(programFilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: read program file %q: %v\n", programFilePath, err)
			return toolrun.ExitError
		}
		programSrc = string(src)
	} else {
		// No program file and no --program flag.
		if len(positional) < 1 {
			fmt.Fprintln(os.Stderr, "usage: expr-test <program-path> <input>")
			fmt.Fprintln(os.Stderr, "       expr-test --program '<expr>' <input>")
			return toolrun.ExitError
		}
		src, err := os.ReadFile(positional[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: read program file %q: %v\n", positional[0], err)
			return toolrun.ExitError
		}
		programSrc = string(src)
		positional = positional[1:]
	}

	// Compile via the production provider — same path as the service.
	p := exprovider.New()
	cfg := buildConfig(programSrc)
	if err := p.LoadConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "COMPILE_ERROR: %v\n", err)
		return toolrun.ExitError
	}

	// Batch mode.
	if *samplesPath != "" {
		return runBatch(p, *samplesPath, *expectPath, *verbose)
	}

	// Single-input mode: next positional arg is the input.
	if len(positional) < 1 {
		fmt.Fprintln(os.Stderr, "usage: expr-test <program-path> <input>")
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

func runBatch(p *exprovider.Provider, samplesPath, expectPath string, verbose bool) int {
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
	yaml := "type: expr\nprogram: |\n"
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
