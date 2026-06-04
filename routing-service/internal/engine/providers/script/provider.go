// Package script implements the "script" routing method provider using
// Starlark (go.starlark.net). The script defines a route(req) function that
// returns a routing group name string or None/""  to defer.
//
// Sandbox: no stdlib, no load(), no file/network/os access. Only the
// RouteInput attributes and hashPct are exposed to scripts. Step limit
// (10,000) + context deadline cancel bound every evaluation.
package script

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

const (
	// maxSteps is the per-call step budget. Routing decisions are O(1) logic;
	// 10,000 steps is a 100× safety margin above any legitimate use.
	maxSteps = 10_000
	// routeFuncName is the entry-point function the script must define.
	routeFuncName = "route"
)

// compiledScript holds a compiled Starlark program and its pre-executed
// globals (which include the route function).
type compiledScript struct {
	routeFn starlark.Value // the route callable extracted from globals
}

// Provider implements engine.RoutingMethod using Starlark scripts.
type Provider struct {
	// script is an atomic pointer to the currently-active compiledScript.
	// nil means no script has been loaded; Evaluate defers in that case.
	script atomic.Pointer[compiledScript]
	// stepBudget overrides maxSteps when > 0. Used by CLI tools to adjust
	// the step limit without touching the production constant.
	stepBudget uint64
}

// New returns a new, unconfigured Provider. Register with the engine.Registry:
//
//	reg.Register("script", func() engine.RoutingMethod { return script.New() })
func New() *Provider {
	return &Provider{}
}

// SetMaxSteps overrides the per-call step budget for this provider instance.
// Only the CLI test tool (starlark-test --max-steps) should call this.
// The production server always uses the default maxSteps constant.
func (p *Provider) SetMaxSteps(n uint64) {
	atomic.StoreUint64(&p.stepBudget, n)
}

// effectiveMaxSteps returns the step budget to use for this call.
func (p *Provider) effectiveMaxSteps() uint64 {
	if n := atomic.LoadUint64(&p.stepBudget); n > 0 {
		return n
	}
	return maxSteps
}

// Type returns "script".
func (p *Provider) Type() string { return "script" }

// LoadConfig compiles and executes a Starlark script from raw YAML config
// bytes. The script must define a function named "route". Compile or execution
// errors are returned without modifying the active script (keep-last-good).
func (p *Provider) LoadConfig(raw []byte) error {
	src, err := extractSource(raw)
	if err != nil {
		return fmt.Errorf("script: LoadConfig: %w", err)
	}
	cs, err := compileAndExec(src)
	if err != nil {
		return fmt.Errorf("script: LoadConfig: %w", err)
	}
	// Swap only on success — keep-last-good semantics.
	p.script.Store(cs)
	return nil
}

// Evaluate calls route(req) in the compiled script and returns a Decision.
// Returns Decided:false when: no script loaded, route() returns None or "",
// or any error (step limit, deadline, runtime error).
func (p *Provider) Evaluate(ctx context.Context, in *engine.RouteInput) (engine.Decision, error) {
	cs := p.script.Load()
	if cs == nil {
		return engine.Decision{}, nil
	}

	thread := &starlark.Thread{
		Name: "route",
		// Disable load() — no module imports permitted.
		Load: func(_ *starlark.Thread, _ string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load() is not permitted in routing scripts")
		},
	}
	thread.SetMaxExecutionSteps(p.effectiveMaxSteps())

	// Start a goroutine that cancels the thread when ctx expires.
	// The goroutine is always started; it exits as soon as either ctx is done
	// or the done channel is closed (after Evaluate returns).
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel("context deadline exceeded")
		case <-done:
		}
	}()

	reqVal := buildReqValue(in)
	result, err := starlark.Call(thread, cs.routeFn, starlark.Tuple{reqVal}, nil)

	// Signal the cancel goroutine to exit before we return.
	close(done)

	if err != nil {
		return engine.Decision{}, fmt.Errorf("script: route(): %w", err)
	}

	switch v := result.(type) {
	case starlark.NoneType:
		return engine.Decision{Decided: false}, nil
	case starlark.String:
		s := string(v)
		if s == "" {
			return engine.Decision{Decided: false}, nil
		}
		return engine.Decision{RoutingGroup: s, Decided: true}, nil
	default:
		return engine.Decision{}, fmt.Errorf("script: route() returned %s, want string or None", result.Type())
	}
}

// compileAndExec parses, compiles, and executes a Starlark source string in a
// restricted environment. Returns a compiledScript if the script defines a
// callable "route" function.
func compileAndExec(src string) (*compiledScript, error) {
	// Use file options that match the legacy Starlark spec (no load-as-module).
	opts := syntax.LegacyFileOptions()

	// predeclared contains the names available at compile time for type checking.
	predeclared := starlark.StringDict{
		"hashPct": starlark.NewBuiltin("hashPct", builtinHashPct),
	}

	// Compile in a scratch thread with no step limit — compilation is fast and
	// happens only on the cold path (LoadConfig).
	compileThread := &starlark.Thread{
		Name: "compile",
		Load: func(_ *starlark.Thread, _ string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load() is not permitted in routing scripts")
		},
	}

	globals, err := starlark.ExecFileOptions(opts, compileThread, "<script>", src, predeclared)
	if err != nil {
		return nil, fmt.Errorf("compile/exec: %w", err)
	}

	routeFn, ok := globals[routeFuncName]
	if !ok {
		return nil, fmt.Errorf("script must define a function named %q", routeFuncName)
	}
	if _, callable := routeFn.(starlark.Callable); !callable {
		return nil, fmt.Errorf("%q is not callable (got %s)", routeFuncName, routeFn.Type())
	}

	// Freeze the route function so it is safe to share across goroutines.
	routeFn.Freeze()

	return &compiledScript{routeFn: routeFn}, nil
}

// builtinHashPct is the Starlark callable for hashPct(s string) int.
// It delegates to engine.HashPct — same FNV-1a deterministic helper as expr.
func builtinHashPct(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(kwargs) > 0 {
		return nil, fmt.Errorf("hashPct: unexpected keyword argument")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("hashPct: expected 1 positional argument, got %d", len(args))
	}
	s, ok := starlark.AsString(args[0])
	if !ok {
		return nil, fmt.Errorf("hashPct: argument must be string, got %s", args[0].Type())
	}
	return starlark.MakeInt(engine.HashPct(s)), nil
}

// buildReqValue wraps a RouteInput as a frozen Starlark struct value.
// Fields mirror the expr provider's requestFields exactly so scripts and
// expressions use the same field names.
func buildReqValue(in *engine.RouteInput) starlark.Value {
	tags := make([]starlark.Value, len(in.ClientTags))
	for i, t := range in.ClientTags {
		tags[i] = starlark.String(t)
	}

	paramDict := starlark.NewDict(len(in.ParamMap))
	for k, v := range in.ParamMap {
		_ = paramDict.SetKey(starlark.String(k), starlark.String(v))
	}

	s := starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"source":      starlark.String(in.Source),
		"client_tags": starlark.NewList(tags),
		"user":        starlark.String(in.User),
		"catalog":     starlark.String(in.Catalog),
		"schema":      starlark.String(in.Schema),
		"method":      starlark.String(in.Method),
		"uri":         starlark.String(in.URI),
		"remote_addr": starlark.String(in.RemoteAddr),
		"body":        starlark.String(in.Body),
		"is_new":      starlark.Bool(in.IsNew),
		"param_map":   paramDict,
	})
	s.Freeze()
	return s
}

// extractSource parses the YAML config bytes produced by
// engine.Registry.methodConfigBytes and returns the program source.
// Supports both inline program and file source.
func extractSource(raw []byte) (string, error) {
	lines := splitLines(string(raw))
	var program, file string
	inBlock := false
	var blockLines []string

	for _, line := range lines {
		if inBlock {
			if len(line) >= 2 && line[0] == ' ' && line[1] == ' ' {
				blockLines = append(blockLines, line[2:])
				continue
			}
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
		data, err := readFile(file)
		if err != nil {
			return "", fmt.Errorf("read file %q: %w", file, err)
		}
		return data, nil
	}
	return "", fmt.Errorf("config must specify program or file")
}

// splitLines, joinLines, parseYAMLLine mirror the expr provider's helpers
// to parse methodConfigBytes YAML output.
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

func parseYAMLLine(line string) (key, val string) {
	for i := range len(line) {
		if line[i] == ':' {
			k := line[:i]
			v := ""
			if i+1 < len(line) {
				v = line[i+1:]
				if len(v) > 0 && v[0] == ' ' {
					v = v[1:]
				}
			}
			return k, v
		}
	}
	return "", ""
}

