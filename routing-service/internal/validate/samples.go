package validate

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// sample is one record in a --samples YAML batch file. The schema matches the
// tools/internal/toolinput SampleRecord shape so the same fixtures drive both
// the CLI test tools and this validator.
type sample struct {
	ID         string            `yaml:"id"`
	Source     string            `yaml:"source"`
	User       string            `yaml:"user"`
	ClientTags []string          `yaml:"client_tags"`
	Catalog    string            `yaml:"catalog"`
	Schema     string            `yaml:"schema"`
	Method     string            `yaml:"method"`
	URI        string            `yaml:"uri"`
	RemoteAddr string            `yaml:"remote_addr"`
	Body       string            `yaml:"body"`
	IsNew      bool              `yaml:"is_new"`
	ParamMap   map[string]string `yaml:"param_map"`
}

// ToRouteInput converts a sample to the engine's RouteInput, normalising nil
// maps/slices to empty so providers see consistent zero values.
func (s sample) ToRouteInput() *engine.RouteInput {
	tags := s.ClientTags
	if tags == nil {
		tags = []string{}
	}
	pm := s.ParamMap
	if pm == nil {
		pm = map[string]string{}
	}
	return &engine.RouteInput{
		Source:     s.Source,
		User:       s.User,
		ClientTags: tags,
		Catalog:    s.Catalog,
		Schema:     s.Schema,
		Method:     s.Method,
		URI:        s.URI,
		RemoteAddr: s.RemoteAddr,
		Body:       s.Body,
		IsNew:      s.IsNew,
		ParamMap:   pm,
	}
}

// loadSamples reads a YAML file containing a list of sample records.
func loadSamples(path string) ([]sample, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read samples file %q: %w", path, err)
	}
	var samples []sample
	if err := yaml.Unmarshal(data, &samples); err != nil {
		return nil, fmt.Errorf("parse samples YAML %q: %w", path, err)
	}
	return samples, nil
}
