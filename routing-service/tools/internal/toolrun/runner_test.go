package toolrun_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolrun"
)

// stubEval is a minimal Evaluator for testing.
type stubEval struct {
	group   string
	decided bool
	err     error
	delay   time.Duration
}

func (s *stubEval) Evaluate(_ context.Context, _ *engine.RouteInput) (engine.Decision, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.err != nil {
		return engine.Decision{}, s.err
	}
	return engine.Decision{RoutingGroup: s.group, Decided: s.decided}, nil
}

func TestRun_Decided(t *testing.T) {
	eval := &stubEval{group: "etl", decided: true}
	r := toolrun.Run(context.Background(), eval, &engine.RouteInput{})
	if r.Status != toolrun.StatusOK {
		t.Errorf("Status = %q, want %q", r.Status, toolrun.StatusOK)
	}
	if r.Group != "etl" {
		t.Errorf("Group = %q, want %q", r.Group, "etl")
	}
}

func TestRun_Deferred(t *testing.T) {
	eval := &stubEval{decided: false}
	r := toolrun.Run(context.Background(), eval, &engine.RouteInput{})
	if r.Status != toolrun.StatusDeferred {
		t.Errorf("Status = %q, want %q", r.Status, toolrun.StatusDeferred)
	}
}

func TestRun_RuntimeError_HasPrefix(t *testing.T) {
	// A non-step-limit runtime error must produce "RUNTIME_ERROR: <msg>".
	eval := &stubEval{err: context.DeadlineExceeded}
	r := toolrun.Run(context.Background(), eval, &engine.RouteInput{})
	want := toolrun.StatusRuntimeError + ": " + context.DeadlineExceeded.Error()
	if r.Status != want {
		t.Errorf("Status = %q, want %q", r.Status, want)
	}
}

func TestPrintSingle_OK(t *testing.T) {
	var buf bytes.Buffer
	r := toolrun.Result{Group: "etl", Latency: 100 * time.Microsecond, Status: toolrun.StatusOK}
	toolrun.PrintSingle(&buf, r, false, nil)
	out := buf.String()
	if !strings.Contains(out, "group:   etl") {
		t.Errorf("output %q: missing group line", out)
	}
	if !strings.Contains(out, "status:  OK") {
		t.Errorf("output %q: missing status line", out)
	}
}

func TestPrintSingle_Deferred_ShowsDash(t *testing.T) {
	var buf bytes.Buffer
	r := toolrun.Result{Group: "", Latency: 50 * time.Microsecond, Status: toolrun.StatusDeferred}
	toolrun.PrintSingle(&buf, r, false, nil)
	out := buf.String()
	if !strings.Contains(out, "group:   —") {
		t.Errorf("output %q: missing em-dash for deferred", out)
	}
}

func TestPrintSingle_Verbose(t *testing.T) {
	var buf bytes.Buffer
	r := toolrun.Result{Group: "etl", Status: toolrun.StatusOK}
	in := &engine.RouteInput{Source: "airflow", User: "alice"}
	toolrun.PrintSingle(&buf, r, true, in)
	out := buf.String()
	if !strings.Contains(out, "airflow") {
		t.Errorf("verbose output %q: missing source", out)
	}
}

func TestRun_StepLimitError_Status(t *testing.T) {
	// Simulate a step-limit error that contains "too many steps".
	import_errors_new := func(s string) error { return &errString{s} }
	eval := &stubEval{err: import_errors_new("starlark: too many steps")}
	r := toolrun.Run(context.Background(), eval, &engine.RouteInput{})
	if r.Status != toolrun.StatusStepLimit {
		t.Errorf("Status = %q, want %q", r.Status, toolrun.StatusStepLimit)
	}
}

type errString struct{ s string }
func (e *errString) Error() string { return e.s }

func TestLoadSamples_ValidYAML(t *testing.T) {
	f := filepath.Join(t.TempDir(), "samples.yaml")
	if err := os.WriteFile(f, []byte(`
- id: s1
  source: airflow
  is_new: true
- id: s2
  source: superset
`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	samples, err := toolrun.LoadSamples(f)
	if err != nil {
		t.Fatalf("LoadSamples: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("len = %d, want 2", len(samples))
	}
	if samples[0].ID != "s1" || samples[0].Source != "airflow" {
		t.Errorf("samples[0] = %+v", samples[0])
	}
}

func TestLoadSamples_MissingFile_Error(t *testing.T) {
	_, err := toolrun.LoadSamples("/no/such/file.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadExpect_ValidYAML(t *testing.T) {
	f := filepath.Join(t.TempDir(), "expect.yaml")
	if err := os.WriteFile(f, []byte("s1: etl\ns2: batch\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	expect, err := toolrun.LoadExpect(f)
	if err != nil {
		t.Fatalf("LoadExpect: %v", err)
	}
	if expect["s1"] != "etl" || expect["s2"] != "batch" {
		t.Errorf("expect = %v", expect)
	}
}

func TestLoadExpect_MissingFile_Error(t *testing.T) {
	_, err := toolrun.LoadExpect("/no/such/file.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	rows := []toolrun.BatchRow{
		{ID: "s1", Group: "etl", Latency: 10 * time.Microsecond, Status: toolrun.StatusOK},
		{ID: "s2", Group: "", Latency: 8 * time.Microsecond, Status: toolrun.StatusDeferred},
	}
	toolrun.PrintTable(&buf, rows)
	out := buf.String()
	if !strings.Contains(out, "SAMPLE") {
		t.Errorf("table %q: missing header", out)
	}
	if !strings.Contains(out, "s1") || !strings.Contains(out, "etl") {
		t.Errorf("table %q: missing row s1/etl", out)
	}
	if !strings.Contains(out, "(deferred)") {
		t.Errorf("table %q: missing deferred row", out)
	}
}
