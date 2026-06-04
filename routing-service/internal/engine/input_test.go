package engine_test

import (
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

func TestFromProto_FullyPopulated(t *testing.T) {
	req := &pb.RouteRequest{
		TrinoSource: "airflow",
		ClientTags:  []string{"tag-a", "tag-b"},
		Method:      "POST",
		RequestUri:  "/v1/statement",
		RemoteAddr:  "10.0.0.1:1234",
		TrinoRequestUser: &pb.TrinoRequestUser{
			User: "alice",
		},
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			DefaultCatalog:       "hive",
			DefaultSchema:        "analytics",
			Body:                 "SELECT 1",
			IsNewQuerySubmission: true,
		},
	}

	in := engine.FromProto(req)

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"Source", in.Source, "airflow"},
		{"User", in.User, "alice"},
		{"Catalog", in.Catalog, "hive"},
		{"Schema", in.Schema, "analytics"},
		{"Body", in.Body, "SELECT 1"},
		{"IsNew", in.IsNew, true},
		{"Method", in.Method, "POST"},
		{"URI", in.URI, "/v1/statement"},
		{"RemoteAddr", in.RemoteAddr, "10.0.0.1:1234"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if len(in.ClientTags) != 2 || in.ClientTags[0] != "tag-a" || in.ClientTags[1] != "tag-b" {
		t.Errorf("ClientTags = %v, want [tag-a tag-b]", in.ClientTags)
	}
}

func TestFromProto_NilTrinoRequestUser(t *testing.T) {
	req := &pb.RouteRequest{
		TrinoSource:      "superset",
		TrinoRequestUser: nil, // must not panic
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			IsNewQuerySubmission: true,
		},
	}
	in := engine.FromProto(req)
	if in.User != "" {
		t.Errorf("User = %q, want empty when TrinoRequestUser is nil", in.User)
	}
}

func TestFromProto_NilTrinoQueryProperties(t *testing.T) {
	req := &pb.RouteRequest{
		TrinoSource:          "dbt",
		TrinoQueryProperties: nil, // must not panic
	}
	in := engine.FromProto(req)
	if in.Catalog != "" {
		t.Errorf("Catalog = %q, want empty when TrinoQueryProperties is nil", in.Catalog)
	}
	if in.Schema != "" {
		t.Errorf("Schema = %q, want empty", in.Schema)
	}
	if in.Body != "" {
		t.Errorf("Body = %q, want empty", in.Body)
	}
	if in.IsNew != false {
		t.Errorf("IsNew = %v, want false", in.IsNew)
	}
}

func TestFromProto_NilRequest(t *testing.T) {
	// Must not panic on nil input.
	in := engine.FromProto(nil)
	if in == nil {
		t.Fatal("FromProto(nil) returned nil, want empty RouteInput")
	}
	if in.Source != "" || in.User != "" || in.IsNew {
		t.Errorf("FromProto(nil) returned non-zero fields: %+v", in)
	}
}

func TestFromProto_IsNew_False(t *testing.T) {
	req := &pb.RouteRequest{
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			IsNewQuerySubmission: false,
		},
	}
	in := engine.FromProto(req)
	if in.IsNew {
		t.Error("IsNew = true, want false")
	}
}

func TestFromProto_ClientTags_NilNormalisedToEmpty(t *testing.T) {
	req := &pb.RouteRequest{
		// ClientTags not set → proto returns nil slice.
	}
	in := engine.FromProto(req)
	if in.ClientTags == nil {
		t.Error("ClientTags is nil, want empty non-nil slice")
	}
	// Safe to range over without nil check.
	count := 0
	for range in.ClientTags {
		count++
	}
	if count != 0 {
		t.Errorf("ClientTags len = %d, want 0", count)
	}
}

func TestFromProto_ParamMap_NilNormalisedToEmpty(t *testing.T) {
	req := &pb.RouteRequest{}
	in := engine.FromProto(req)
	if in.ParamMap == nil {
		t.Error("ParamMap is nil, want empty non-nil map")
	}
}

func TestFromProto_ParamMap_Values(t *testing.T) {
	req := &pb.RouteRequest{
		ParameterMap: map[string]string{"k": "v", "x": "y"},
	}
	in := engine.FromProto(req)
	if in.ParamMap["k"] != "v" {
		t.Errorf("ParamMap[k] = %q, want %q", in.ParamMap["k"], "v")
	}
}
