package diffharness

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Scenario describes a request sequence to replay at both gateways and the
// diff policy to apply when comparing their responses.
type Scenario struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Steps       []Step     `yaml:"steps"`
	Diff        DiffPolicy `yaml:"diff"`
}

// Step is one HTTP request in a scenario.
//
// Path resolution order:
//  1. If PathFromVar is set, look it up in the per-run variable map (populated
//     by a prior step's Extract); the value is the full URL or path.
//  2. Else use Path verbatim, joined to the gateway base URL.
type Step struct {
	Method      string            `yaml:"method"`
	Path        string            `yaml:"path,omitempty"`
	PathFromVar string            `yaml:"pathFromVar,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	Body        string            `yaml:"body,omitempty"`
	Extract     map[string]string `yaml:"extract,omitempty"`
	RepeatUntil *RepeatPolicy     `yaml:"repeatUntil,omitempty"`
}

// RepeatPolicy bounds a step's repetition. When NoField is set, the step
// repeats GETs of the same URL until the named JSON field is absent from the
// body. MaxIterations caps the loop regardless.
type RepeatPolicy struct {
	NoField       string `yaml:"noField,omitempty"`
	MaxIterations int    `yaml:"maxIterations,omitempty"`
}

// DiffPolicy controls how the captured Java/Go responses are normalized
// before structural comparison.
//
// Intentionally small. Per qa-tech-lead's normalizer caution
// ("if the normalizer strips a header that's actually load-bearing, we'll
// pass differential and break clients"), every entry added here must carry a
// justification comment in the scenario YAML that uses it.
type DiffPolicy struct {
	IgnoreHeaders    []string `yaml:"ignoreHeaders,omitempty"`
	IgnoreBodyFields []string `yaml:"ignoreBodyFields,omitempty"`
	RewriteHostPort  bool     `yaml:"rewriteHostPort,omitempty"`
}

// LoadScenario reads a scenario YAML file from disk.
func LoadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse scenario %s: %w", path, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("scenario %s: missing required field 'name'", path)
	}
	if len(s.Steps) == 0 {
		return nil, fmt.Errorf("scenario %s: must declare at least one step", path)
	}
	return &s, nil
}
