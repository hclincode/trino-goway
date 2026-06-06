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

	// SQL-aware routing fields (UC-RTG-04). Supply these directly to test content
	// rules offline; the CLI tools evaluate the provider against the given input
	// and do not run the in-service analyzer themselves.
	QueryType      string   `json:"query_type"`
	QueryCategory  string   `json:"query_category"`
	Catalogs       []string `json:"catalogs"`
	Schemas        []string `json:"schemas"`
	CatalogSchemas []string `json:"catalog_schemas"`
	Tables         []string `json:"tables"`
	ParseOK        bool     `json:"parse_ok"`
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
		Source:         ji.Source,
		User:           ji.User,
		ClientTags:     ji.ClientTags,
		Catalog:        ji.Catalog,
		Schema:         ji.Schema,
		Method:         ji.Method,
		URI:            ji.URI,
		RemoteAddr:     ji.RemoteAddr,
		Body:           ji.Body,
		IsNew:          ji.IsNew,
		ParamMap:       ji.ParamMap,
		QueryType:      ji.QueryType,
		QueryCategory:  ji.QueryCategory,
		Catalogs:       nilToEmpty(ji.Catalogs),
		Schemas:        nilToEmpty(ji.Schemas),
		CatalogSchemas: nilToEmpty(ji.CatalogSchemas),
		Tables:         nilToEmpty(ji.Tables),
		ParseOK:        ji.ParseOK,
	}, nil
}

// nilToEmpty normalises a nil slice to a non-nil empty slice.
func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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

	// SQL-aware routing fields (UC-RTG-04); see jsonInput.
	QueryType      string   `yaml:"query_type"`
	QueryCategory  string   `yaml:"query_category"`
	Catalogs       []string `yaml:"catalogs"`
	Schemas        []string `yaml:"schemas"`
	CatalogSchemas []string `yaml:"catalog_schemas"`
	Tables         []string `yaml:"tables"`
	ParseOK        bool     `yaml:"parse_ok"`
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
		Source:         s.Source,
		User:           s.User,
		ClientTags:     tags,
		Catalog:        s.Catalog,
		Schema:         s.Schema,
		Method:         s.Method,
		URI:            s.URI,
		RemoteAddr:     s.RemoteAddr,
		Body:           s.Body,
		IsNew:          s.IsNew,
		ParamMap:       pm,
		QueryType:      s.QueryType,
		QueryCategory:  s.QueryCategory,
		Catalogs:       nilToEmpty(s.Catalogs),
		Schemas:        nilToEmpty(s.Schemas),
		CatalogSchemas: nilToEmpty(s.CatalogSchemas),
		Tables:         nilToEmpty(s.Tables),
		ParseOK:        s.ParseOK,
	}
}
