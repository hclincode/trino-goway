// Package toolinput parses a RouteInput from a JSON string or file path.
// Both starlark-test and expr-test accept arg2 as either an inline JSON object
// or a path to a .json file containing the same object.
package toolinput

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// jsonInput is the JSON representation of a routing request.
// All fields are optional; zero-values are used when absent.
type jsonInput struct {
	Source     string            `json:"source"`
	User       string            `json:"user"`
	ClientTags []string          `json:"client_tags"`
	Catalog    string            `json:"catalog"`
	Schema     string            `json:"schema"`
	Method     string            `json:"method"`
	URI        string            `json:"uri"`
	RemoteAddr string            `json:"remote_addr"`
	Body       string            `json:"body"`
	IsNew      bool              `json:"is_new"`
	ParamMap   map[string]string `json:"param_map"`
}

// Parse parses a RouteInput from s, which is either:
//   - an inline JSON object string: `{"source":"airflow","is_new":true}`
//   - a path to a .json file containing the same object
func Parse(s string) (*engine.RouteInput, error) {
	var data []byte

	// Detect file vs inline: if s looks like a file path (starts with ./, /,
	// or ends with .json) and the file exists, read it.
	if looksLikeFile(s) {
		b, err := os.ReadFile(s)
		if err != nil {
			return nil, fmt.Errorf("read input file %q: %w", s, err)
		}
		data = b
	} else {
		data = []byte(s)
	}

	var ji jsonInput
	if err := json.Unmarshal(data, &ji); err != nil {
		return nil, fmt.Errorf("parse input JSON: %w", err)
	}

	if ji.ClientTags == nil {
		ji.ClientTags = []string{}
	}
	if ji.ParamMap == nil {
		ji.ParamMap = map[string]string{}
	}

	return &engine.RouteInput{
		Source:     ji.Source,
		User:       ji.User,
		ClientTags: ji.ClientTags,
		Catalog:    ji.Catalog,
		Schema:     ji.Schema,
		Method:     ji.Method,
		URI:        ji.URI,
		RemoteAddr: ji.RemoteAddr,
		Body:       ji.Body,
		IsNew:      ji.IsNew,
		ParamMap:   ji.ParamMap,
	}, nil
}

// looksLikeFile returns true when s appears to be a file path rather than
// an inline JSON string.
func looksLikeFile(s string) bool {
	s = strings.TrimSpace(s)
	// An inline JSON object always starts with '{'.
	if strings.HasPrefix(s, "{") {
		return false
	}
	// Treat anything that doesn't start with '{' as a potential file path.
	return true
}

// SamplesYAML is a batch of named RouteInput records from a YAML file.
type SamplesYAML struct {
	Samples []SampleRecord
}

// SampleRecord is one entry in a --samples YAML file.
type SampleRecord struct {
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

// ToRouteInput converts a SampleRecord to a RouteInput.
func (s SampleRecord) ToRouteInput() *engine.RouteInput {
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
