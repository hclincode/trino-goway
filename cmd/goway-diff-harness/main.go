// Command goway-diff-harness drives differential tests between the Java
// trino-gateway and the Go trino-goway.
//
// Subcommands:
//
//	replay   Diff Go responses against committed golden recordings.
//	         Cheap (no Java required); the per-PR mode.
//	record   Run scenarios against the Java target and write/refresh goldens.
//	         Requires --java-url to point at a running Java gateway.
//	live     Run scenarios against both --java-url and --go-url and diff
//	         the live responses. Nightly mode.
//	report   Re-render an existing JSON results file as text or JSON.
//
// Run with --help on any subcommand for full flag listings.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hclincode/trino-goway/internal/diffharness"
)

const usage = `goway-diff-harness — differential testing between java trino-gateway and go trino-goway

Usage:
  goway-diff-harness <subcommand> [flags]

Subcommands:
  replay   Diff Go responses against committed golden recordings
  record   Refresh golden recordings from a Java gateway
  live     Diff live Java vs live Go responses
  report   Re-render an existing JSON results file
  help     Show this message
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch os.Args[1] {
	case "replay":
		os.Exit(runReplay(ctx, os.Args[2:]))
	case "record":
		os.Exit(runRecord(ctx, os.Args[2:]))
	case "live":
		os.Exit(runLive(ctx, os.Args[2:]))
	case "report":
		os.Exit(runReport(os.Args[2:]))
	case "help", "-h", "--help":
		fmt.Print(usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// commonFlags are shared by replay/record/live.
type commonFlags struct {
	scenariosDir string
	format       string
}

func bindCommonFlags(fs *flag.FlagSet, c *commonFlags) {
	fs.StringVar(&c.scenariosDir, "scenarios", "cmd/goway-diff-harness/testdata/scenarios",
		"directory containing scenario YAML files")
	fs.StringVar(&c.format, "format", "text", "output format: text|json")
}

func runLive(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("live", flag.ExitOnError)
	var c commonFlags
	bindCommonFlags(fs, &c)
	javaURL := fs.String("java-url", "", "base URL of the Java gateway (required)")
	goURL := fs.String("go-url", "", "base URL of the Go gateway (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *javaURL == "" || *goURL == "" {
		fmt.Fprintln(os.Stderr, "live: --java-url and --go-url are required")
		return 2
	}

	scenarios, err := loadAllScenarios(c.scenariosDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(scenarios) == 0 {
		fmt.Fprintf(os.Stderr, "no scenarios found in %s\n", c.scenariosDir)
		return 1
	}

	r := diffharness.NewRunner(
		diffharness.Target{Name: "java", BaseURL: *javaURL},
		diffharness.Target{Name: "go", BaseURL: *goURL},
	)
	results := r.RunAll(ctx, scenarios)
	return emit(results, c.format)
}

// runRecord drives every scenario against --java-url, normalizes the response,
// and writes a golden JSON file under --goldens for each. Intended for the
// nightly job that has a Java gateway bootstrapped via BootstrapContainers.
func runRecord(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	var c commonFlags
	bindCommonFlags(fs, &c)
	javaURL := fs.String("java-url", "", "base URL of the Java gateway (required)")
	goldensDir := fs.String("goldens", "cmd/goway-diff-harness/testdata/golden",
		"directory to write golden files into")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *javaURL == "" {
		fmt.Fprintln(os.Stderr, "record: --java-url is required")
		return 2
	}

	scenarios, err := loadAllScenarios(c.scenariosDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(scenarios) == 0 {
		fmt.Fprintf(os.Stderr, "no scenarios found in %s\n", c.scenariosDir)
		return 1
	}

	// Go target is unused in record mode; pass the java URL so Runner has a
	// valid Target struct.
	r := diffharness.NewRunner(
		diffharness.Target{Name: "java", BaseURL: *javaURL},
		diffharness.Target{Name: "java", BaseURL: *javaURL},
	)

	var failed int
	for _, s := range scenarios {
		g, err := r.RecordScenario(ctx, s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "record %s: %v\n", s.Name, err)
			failed++
			continue
		}
		path := diffharness.GoldenPath(*goldensDir, s.Name)
		if err := diffharness.WriteGolden(path, g); err != nil {
			fmt.Fprintf(os.Stderr, "record %s: %v\n", s.Name, err)
			failed++
			continue
		}
		fmt.Printf("recorded %s → %s\n", s.Name, path)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// runReplay diffs the Go-side response for each scenario against its committed
// golden file. Per-PR mode: no Java required.
func runReplay(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	var c commonFlags
	bindCommonFlags(fs, &c)
	goURL := fs.String("go-url", "", "base URL of the Go gateway (required)")
	goldensDir := fs.String("goldens", "cmd/goway-diff-harness/testdata/golden",
		"directory containing golden files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *goURL == "" {
		fmt.Fprintln(os.Stderr, "replay: --go-url is required")
		return 2
	}

	scenarios, err := loadAllScenarios(c.scenariosDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(scenarios) == 0 {
		fmt.Fprintf(os.Stderr, "no scenarios found in %s\n", c.scenariosDir)
		return 1
	}

	r := diffharness.NewRunner(
		diffharness.Target{Name: "go", BaseURL: *goURL},
		diffharness.Target{Name: "go", BaseURL: *goURL},
	)

	results := make([]diffharness.Result, 0, len(scenarios))
	for _, s := range scenarios {
		path := diffharness.GoldenPath(*goldensDir, s.Name)
		g, err := diffharness.ReadGolden(path)
		if err != nil {
			results = append(results, diffharness.Result{
				Scenario: s.Name,
				Verdict:  diffharness.VerdictError,
				Reason:   err.Error(),
			})
			continue
		}
		results = append(results, r.ReplayScenario(ctx, s, g))
	}
	return emit(results, c.format)
}

// runReport re-renders a JSON results file produced by an earlier run. Useful
// for converting a CI artifact to a human-readable summary without re-running
// the scenarios.
func runReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	input := fs.String("input", "", "path to a JSON results file (required)")
	format := fs.String("format", "text", "output format: text|json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "report: --input is required")
		return 2
	}

	raw, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	var doc struct {
		Results []diffharness.Result `json:"results"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintln(os.Stderr, "report: parse:", err)
		return 1
	}
	return emit(doc.Results, *format)
}

func emit(results []diffharness.Result, format string) int {
	switch format {
	case "json":
		s, err := diffharness.WriteJSON(os.Stdout, results)
		if err != nil {
			fmt.Fprintln(os.Stderr, "write json:", err)
			return 1
		}
		if s.Fail > 0 || s.Errored > 0 {
			return 1
		}
		return 0
	default:
		s := diffharness.WriteText(os.Stdout, results)
		if s.Fail > 0 || s.Errored > 0 {
			return 1
		}
		return 0
	}
}

func loadAllScenarios(dir string) ([]*diffharness.Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scenarios dir %s: %w", dir, err)
	}
	var out []*diffharness.Scenario
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !hasSuffix(name, ".yaml") && !hasSuffix(name, ".yml") {
			continue
		}
		s, err := diffharness.LoadScenario(dir + "/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
