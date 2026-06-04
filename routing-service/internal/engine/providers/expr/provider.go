// Package expr implements the "expr" routing method provider using the
// expr-lang/expr expression language.
//
// An expr program is a single expression that returns a routing group name
// (non-empty string) or "" to defer to the next method. The program is
// compiled at LoadConfig time with full type-checking; a compile failure
// leaves the previously-compiled program live (keep-last-good semantics).
package expr

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"

	exprlib "github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// routeEnv is the type-checked environment struct passed to expr.Compile and
// expr.Run. All fields exposed to the expression are declared here so that
// the compiler can enforce type safety and reject invalid field accesses.
type routeEnv struct {
	// Request holds the routing request fields.
	Request requestFields
}

// requestFields mirrors engine.RouteInput and is the struct accessible as
// "request" inside an expr program.
type requestFields struct {
	Source     string
	ClientTags []string
	User       string
	Catalog    string
	Schema     string
	Method     string
	URI        string
	RemoteAddr string
	Body       string
	IsNew      bool
	ParamMap   map[string]string
}

// Provider implements engine.RoutingMethod using expr-lang/expr.
type Provider struct {
	// prog is the currently-active compiled program. nil means no program has
	// been loaded yet; Evaluate returns Decided:false in that case.
	prog atomic.Pointer[vm.Program]
}

// New returns a new, unconfigured Provider. Register with the engine.Registry
// under the type name "expr":
//
//	reg.Register("expr", func() engine.RoutingMethod { return expr.New() })
func New() *Provider {
	return &Provider{}
}

// Type returns "expr".
func (p *Provider) Type() string { return "expr" }

// LoadConfig compiles the expression source from raw YAML config bytes.
// The config format is:
//
//	type: expr
//	program: |
//	  source == "airflow" ? "etl" : ""
//
// or:
//
//	type: expr
//	file: /path/to/program.expr
//
// Compile errors are returned without modifying the active program
// (keep-last-good). The program must be a string-returning expression;
// a type mismatch (e.g. returning int) is rejected at compile time.
func (p *Provider) LoadConfig(raw []byte) error {
	src, err := extractSource(raw)
	if err != nil {
		return fmt.Errorf("expr: LoadConfig: %w", err)
	}
	prog, err := compile(src)
	if err != nil {
		return fmt.Errorf("expr: LoadConfig: compile: %w", err)
	}
	// Swap only on success — keep-last-good semantics.
	p.prog.Store(prog)
	return nil
}

// Evaluate runs the compiled program against the request and returns a
// Decision. Returns Decided:false (defer) when:
//   - no program has been loaded yet
//   - the program returns ""
//   - expr.Run returns an error (including type panics)
func (p *Provider) Evaluate(_ context.Context, in *engine.RouteInput) (engine.Decision, error) {
	prog := p.prog.Load()
	if prog == nil {
		// No program loaded yet — defer.
		return engine.Decision{}, nil
	}

	env := buildEnv(in)
	result, err := exprlib.Run(prog, env)
	if err != nil {
		// Runtime error — treat as defer, log via the pipeline's safeEvaluate.
		return engine.Decision{}, fmt.Errorf("expr: Run: %w", err)
	}

	group, ok := result.(string)
	if !ok {
		// Should not happen: compile enforces AsKind(String). Guard anyway.
		return engine.Decision{}, fmt.Errorf("expr: result is %T, want string", result)
	}

	if group == "" {
		return engine.Decision{Decided: false}, nil
	}
	return engine.Decision{RoutingGroup: group, Decided: true}, nil
}

// compile parses and type-checks an expression source string.
// The program must return a string (routing group name or "").
func compile(src string) (*vm.Program, error) {
	return exprlib.Compile(src,
		exprlib.Env(routeEnv{}),
		exprlib.AsKind(reflect.String),
		// Register hashPct as a known function for type-checking.
		exprlib.Function("hashPct",
			func(params ...any) (any, error) {
				if len(params) != 1 {
					return nil, fmt.Errorf("hashPct: expected 1 argument, got %d", len(params))
				}
				s, ok := params[0].(string)
				if !ok {
					return nil, fmt.Errorf("hashPct: argument must be string, got %T", params[0])
				}
				return engine.HashPct(s), nil
			},
			new(func(string) int), // type signature for the compiler
		),
	)
}

// buildEnv constructs the expr evaluation environment from a RouteInput.
func buildEnv(in *engine.RouteInput) routeEnv {
	tags := in.ClientTags
	if tags == nil {
		tags = []string{}
	}
	pm := in.ParamMap
	if pm == nil {
		pm = map[string]string{}
	}
	return routeEnv{
		Request: requestFields{
			Source:     in.Source,
			ClientTags: tags,
			User:       in.User,
			Catalog:    in.Catalog,
			Schema:     in.Schema,
			Method:     in.Method,
			URI:        in.URI,
			RemoteAddr: in.RemoteAddr,
			Body:       in.Body,
			IsNew:      in.IsNew,
			ParamMap:   pm,
		},
	}
}

// extractSource parses the YAML config bytes produced by engine.Registry.methodConfigBytes
// and returns the program source string. Supports both inline program and file
// source (for file: the file is read at this point).
func extractSource(raw []byte) (string, error) {
	// Parse the hand-rolled YAML from methodConfigBytes.
	// Format:
	//   type: expr
	//   program: |
	//     <source lines>
	// or:
	//   type: expr
	//   file: /path
	lines := splitLines(string(raw))
	var program, file string
	inBlock := false
	var blockLines []string

	for _, line := range lines {
		if inBlock {
			// Block scalar continuation: lines indented with 2 spaces.
			if len(line) >= 2 && line[0] == ' ' && line[1] == ' ' {
				blockLines = append(blockLines, line[2:])
				continue
			}
			// Non-indented line ends the block.
			inBlock = false
			program = joinLines(blockLines)
		}
		key, val := parseYAMLLine(line)
		switch key {
		case "program":
			if val == "|" {
				inBlock = true
				blockLines = nil
			} else {
				program = val
			}
		case "file":
			file = val
		}
	}
	if inBlock {
		program = joinLines(blockLines)
	}

	if program != "" {
		return program, nil
	}
	if file != "" {
		src, err := readFile(file)
		if err != nil {
			return "", fmt.Errorf("read file %q: %w", file, err)
		}
		return src, nil
	}
	return "", fmt.Errorf("config must specify program or file")
}

// splitLines splits s on newlines, preserving empty lines.
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

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}

// parseYAMLLine splits "key: value" into key and value. Returns "", "" for
// lines that don't match (comments, blank lines, block continuations).
func parseYAMLLine(line string) (key, val string) {
	for i := range len(line) {
		if line[i] == ':' {
			k := line[:i]
			v := ""
			if i+1 < len(line) {
				v = line[i+1:]
				// Trim leading space after colon.
				if len(v) > 0 && v[0] == ' ' {
					v = v[1:]
				}
			}
			return k, v
		}
	}
	return "", ""
}
