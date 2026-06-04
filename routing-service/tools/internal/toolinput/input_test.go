package toolinput_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolinput"
)

func TestParse_InlineJSON(t *testing.T) {
	in, err := toolinput.Parse(`{"source":"airflow","user":"alice","is_new":true}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.Source != "airflow" {
		t.Errorf("Source = %q, want %q", in.Source, "airflow")
	}
	if in.User != "alice" {
		t.Errorf("User = %q, want %q", in.User, "alice")
	}
	if !in.IsNew {
		t.Error("IsNew = false, want true")
	}
}

func TestParse_InlineJSON_AllFields(t *testing.T) {
	in, err := toolinput.Parse(`{
		"source":"s","user":"u","client_tags":["a","b"],
		"catalog":"c","schema":"sc","method":"POST","uri":"/v1","remote_addr":"1.2.3.4",
		"body":"SELECT 1","is_new":true,"param_map":{"k":"v"}
	}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.Source != "s" || in.Catalog != "c" || in.Schema != "sc" {
		t.Errorf("unexpected fields: %+v", in)
	}
	if len(in.ClientTags) != 2 || in.ClientTags[0] != "a" {
		t.Errorf("ClientTags = %v", in.ClientTags)
	}
	if in.ParamMap["k"] != "v" {
		t.Errorf("ParamMap = %v", in.ParamMap)
	}
}

func TestParse_EmptyJSON_ZeroValues(t *testing.T) {
	in, err := toolinput.Parse(`{}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.Source != "" || in.IsNew || len(in.ClientTags) != 0 {
		t.Errorf("unexpected non-zero: %+v", in)
	}
}

func TestParse_FromFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(f, []byte(`{"source":"dbt","is_new":false}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, err := toolinput.Parse(f)
	if err != nil {
		t.Fatalf("Parse file: %v", err)
	}
	if in.Source != "dbt" {
		t.Errorf("Source = %q, want dbt", in.Source)
	}
}

func TestParse_MissingFile_Error(t *testing.T) {
	_, err := toolinput.Parse("/no/such/file.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestParse_InvalidJSON_Error(t *testing.T) {
	_, err := toolinput.Parse(`{not valid json}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParse_NilClientTags_NormalisedToEmpty(t *testing.T) {
	in, err := toolinput.Parse(`{"source":"x"}`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.ClientTags == nil {
		t.Error("ClientTags is nil, want empty non-nil slice")
	}
}

func TestSampleRecord_ToRouteInput(t *testing.T) {
	s := toolinput.SampleRecord{
		ID:         "test",
		Source:     "airflow",
		User:       "alice",
		ClientTags: []string{"t1"},
		IsNew:      true,
	}
	in := s.ToRouteInput()
	if in.Source != "airflow" || in.User != "alice" || !in.IsNew {
		t.Errorf("ToRouteInput: %+v", in)
	}
	if len(in.ClientTags) != 1 || in.ClientTags[0] != "t1" {
		t.Errorf("ClientTags: %v", in.ClientTags)
	}
}

func TestSampleRecord_ToRouteInput_NilTagsNormalised(t *testing.T) {
	s := toolinput.SampleRecord{ID: "x"}
	in := s.ToRouteInput()
	if in.ClientTags == nil {
		t.Error("ClientTags is nil after ToRouteInput")
	}
	if in.ParamMap == nil {
		t.Error("ParamMap is nil after ToRouteInput")
	}
}
