// Package main is the CLI entry point for the goway-migrate-config tool.
// It reads a Java trino-gateway config.yml and writes an equivalent Go trino-goway config.yml.
package main

import (
	"flag"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	input := flag.String("input", "", "path to Java trino-gateway config.yml (required)")
	output := flag.String("output", "", "path to write Go config.yml (default: stdout)")
	dryRun := flag.Bool("dry-run", false, "print result to stdout, don't write file")
	flag.Parse()

	if *input == "" {
		fmt.Fprintln(os.Stderr, "error: --input is required")
		flag.Usage()
		os.Exit(1)
	}

	javaYAML, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read %q: %v\n", *input, err)
		os.Exit(1)
	}

	cfg, warnings, err := MigrateConfig(javaYAML)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: migrate: %v\n", err)
		os.Exit(1)
	}

	out, err := marshalWithWarnings(toOutput(cfg), warnings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal: %v\n", err)
		os.Exit(1)
	}

	if *dryRun || *output == "" {
		fmt.Print(string(out))
		return
	}

	if err := os.WriteFile(*output, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write %q: %v\n", *output, err)
		os.Exit(1)
	}
}

// marshalWithWarnings serializes cfg to YAML, prepending each warning as a comment line.
func marshalWithWarnings(cfg interface{}, warnings []string) ([]byte, error) {
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("yaml marshal: %w", err)
	}

	var header []byte
	for _, w := range warnings {
		header = append(header, []byte("# WARNING: "+w+"\n")...)
	}
	if len(header) > 0 {
		header = append(header, '\n')
	}
	return append(header, body...), nil
}
