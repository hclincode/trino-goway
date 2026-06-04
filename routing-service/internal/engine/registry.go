package engine

import (
	"fmt"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
)

// Factory is a function that constructs a new, unconfigured RoutingMethod.
type Factory func() RoutingMethod

// Registry maps method type names to their provider factories.
// Register all providers at program startup (typically in main.go init or
// explicit calls before starting the server). Attempting to register a
// duplicate type panics — that is a programmer error caught at startup.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register associates typeName with factory. Panics if typeName is already
// registered (fail-loud on misconfiguration at startup).
func (r *Registry) Register(typeName string, factory Factory) {
	if _, exists := r.factories[typeName]; exists {
		panic(fmt.Sprintf("engine: registry: duplicate type %q", typeName))
	}
	r.factories[typeName] = factory
}

// Build constructs and configures a RoutingMethod for the given MethodConfig.
// Returns an error if the type is unknown or if LoadConfig fails.
func (r *Registry) Build(cfg config.MethodConfig) (RoutingMethod, error) {
	factory, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("engine: registry: unknown method type %q", cfg.Type)
	}
	m := factory()
	raw, err := methodConfigBytes(cfg)
	if err != nil {
		return nil, fmt.Errorf("engine: registry: marshal config for %q: %w", cfg.Type, err)
	}
	if err := m.LoadConfig(raw); err != nil {
		return nil, fmt.Errorf("engine: registry: LoadConfig for %q: %w", cfg.Type, err)
	}
	return m, nil
}

// methodConfigBytes serialises the method-specific fields of a MethodConfig
// into a YAML byte slice for passing to LoadConfig.
// Only the fields a provider cares about (type, program, file, refresh) are
// included; the provider is responsible for parsing this slice.
func methodConfigBytes(cfg config.MethodConfig) ([]byte, error) {
	// Simple hand-rolled YAML: avoids pulling in yaml.Marshal for a tiny struct.
	// Format matches what providers expect:
	//   type: expr
	//   program: |
	//     <source>
	//   file: /path/to/file
	//   refresh: 30s
	var buf []byte
	buf = appendYAMLStr(buf, "type", cfg.Type)
	if cfg.Program != "" {
		buf = appendYAMLBlock(buf, "program", cfg.Program)
	}
	if cfg.File != "" {
		buf = appendYAMLStr(buf, "file", cfg.File)
	}
	if cfg.Refresh.D > 0 {
		buf = appendYAMLStr(buf, "refresh", cfg.Refresh.D.String())
	}
	return buf, nil
}

func appendYAMLStr(buf []byte, key, val string) []byte {
	buf = append(buf, key+": "...)
	buf = append(buf, val...)
	buf = append(buf, '\n')
	return buf
}

func appendYAMLBlock(buf []byte, key, val string) []byte {
	buf = append(buf, key+": |\n"...)
	// Indent each line of the block scalar by two spaces.
	for len(val) > 0 {
		var line string
		if i := indexByte(val, '\n'); i >= 0 {
			line = val[:i+1]
			val = val[i+1:]
		} else {
			line = val + "\n"
			val = ""
		}
		buf = append(buf, "  "...)
		buf = append(buf, line...)
	}
	return buf
}

func indexByte(s string, b byte) int {
	for i := range len(s) {
		if s[i] == b {
			return i
		}
	}
	return -1
}
