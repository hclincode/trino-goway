package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolrun"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---- helpers ----

func writeScript(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "route.star")
	if err := os.WriteFile(f, []byte(content), 0600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return f
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "starlark-test")
	cmd := exec.Command("go", "build", "-o", bin,
		"github.com/hclincode/trino-goway-routing-service/tools/starlark-test")
	cmd.Dir = findModRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build starlark-test: %v\n%s", err, out)
	}
	return bin
}

func findModRoot(t *testing.T) string {
	t.Helper()
	// Walk up from the test file location until we find go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func runBin(t *testing.T, bin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out.String(), errb.String(), ee.ExitCode()
		}
	}
	return out.String(), errb.String(), 0
}

// ---- exit-code matrix ----

func TestExitCode_ValidScript_MatchingInput_OK(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  if req.source == "airflow":
    return "etl"
  return None
`)
	stdout, _, code := runBin(t, bin, script, `{"source":"airflow","is_new":true}`)
	if code != toolrun.ExitOK {
		t.Errorf("exit code = %d, want %d", code, toolrun.ExitOK)
	}
	if !strings.Contains(stdout, "group:   etl") {
		t.Errorf("stdout %q: missing 'group:   etl'", stdout)
	}
	if !strings.Contains(stdout, "status:  OK") {
		t.Errorf("stdout %q: missing 'status:  OK'", stdout)
	}
}

func TestExitCode_ValidScript_NonMatchingInput_Deferred(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  if req.source == "airflow":
    return "etl"
  return None
`)
	stdout, _, code := runBin(t, bin, script, `{"source":"superset","is_new":true}`)
	if code != toolrun.ExitOK {
		t.Errorf("exit code = %d, want %d", code, toolrun.ExitOK)
	}
	if !strings.Contains(stdout, "status:  DEFERRED") {
		t.Errorf("stdout %q: missing 'status:  DEFERRED'", stdout)
	}
}

func TestExitCode_StepLimitScript_Error(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  x = 0
  for i in range(1000000000):
    x = x + 1
  return "etl"
`)
	start := time.Now()
	stdout, _, code := runBin(t, bin, script, `{"is_new":true}`)
	elapsed := time.Since(start)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (step limit)", code, toolrun.ExitError)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("step-limit script took %v in subprocess, want < 500ms", elapsed)
	}
	// Assert the output contains the STEP_LIMIT status string.
	if !strings.Contains(stdout, toolrun.StatusStepLimit) {
		t.Errorf("stdout %q: missing %q status", stdout, toolrun.StatusStepLimit)
	}
}

func TestExitCode_MaxStepsFlag_StepLimit(t *testing.T) {
	// --max-steps 1 forces step limit on any non-trivial script.
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  x = 0
  for i in range(100):
    x = x + 1
  return "etl"
`)
	stdout, _, code := runBin(t, bin, script, "--max-steps", "1", `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (--max-steps 1 step limit)", code, toolrun.ExitError)
	}
	if !strings.Contains(stdout, toolrun.StatusStepLimit) {
		t.Errorf("stdout %q: missing %q status", stdout, toolrun.StatusStepLimit)
	}
}

func TestExitCode_MissingRouteFunc_Error(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, "x = 1\n")
	_, _, code := runBin(t, bin, script, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (missing route func)", code, toolrun.ExitError)
	}
}

func TestExitCode_SyntaxError_Error(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, "not valid starlark ???\n")
	_, _, code := runBin(t, bin, script, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (syntax error)", code, toolrun.ExitError)
	}
}

func TestExitCode_Batch_AllMatch_OK(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  if req.source == "airflow":
    return "etl"
  return None
`)
	samplesFile := filepath.Join(t.TempDir(), "samples.yaml")
	mustWriteFile(t, samplesFile, []byte(`
- id: s1
  source: airflow
  is_new: true
- id: s2
  source: other
  is_new: true
`))
	expectFile := filepath.Join(t.TempDir(), "expect.yaml")
	mustWriteFile(t, expectFile, []byte(`s1: etl
s2: ""
`))
	_, _, code := runBin(t, bin, script, "--samples", samplesFile, "--expect", expectFile)
	if code != toolrun.ExitOK {
		t.Errorf("exit code = %d, want %d (all match)", code, toolrun.ExitOK)
	}
}

func TestExitCode_Batch_Mismatch_ExpectFail(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  return "etl"
`)
	samplesFile := filepath.Join(t.TempDir(), "samples.yaml")
	mustWriteFile(t, samplesFile, []byte(`
- id: s1
  is_new: true
`))
	expectFile := filepath.Join(t.TempDir(), "expect.yaml")
	mustWriteFile(t, expectFile, []byte("s1: wrong-group\n"))
	_, _, code := runBin(t, bin, script, "--samples", samplesFile, "--expect", expectFile)
	if code != toolrun.ExitExpectFail {
		t.Errorf("exit code = %d, want %d (expectation mismatch)", code, toolrun.ExitExpectFail)
	}
}

// ---- output == production provider ----

func TestOutput_MatchesProductionProvider(t *testing.T) {
	// Run the same input through the production provider directly and compare
	// the routing group to what the CLI tool prints.
	scriptContent := `
def route(req):
  if req.source == "airflow":
    return "etl"
  if req.source == "superset":
    return "interactive"
  return None
`
	bin := buildBinary(t)
	scriptFile := writeScript(t, scriptContent)

	cases := []struct {
		input string
		in    *engine.RouteInput
	}{
		{`{"source":"airflow","is_new":true}`, &engine.RouteInput{Source: "airflow", IsNew: true}},
		{`{"source":"superset","is_new":true}`, &engine.RouteInput{Source: "superset", IsNew: true}},
		{`{"source":"dbt","is_new":true}`, &engine.RouteInput{Source: "dbt", IsNew: true}},
	}

	// Build the production provider once.
	p := scriptprovider.New()
	cfg := makeConfig(scriptContent)
	if err := p.LoadConfig(cfg); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	for _, tc := range cases {
		// Get the production provider result.
		d, err := p.Evaluate(context.Background(), tc.in)
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", tc.input, err)
		}
		wantGroup := d.RoutingGroup // "" if deferred

		// Get the CLI tool result.
		stdout, _, code := runBin(t, bin, scriptFile, tc.input)
		if tc.in.Source == "dbt" {
			// Deferred — code 0 expected.
			if code != toolrun.ExitOK {
				t.Errorf("input %q: exit code = %d, want 0", tc.input, code)
			}
			if !strings.Contains(stdout, "status:  DEFERRED") {
				t.Errorf("input %q: want DEFERRED in output, got %q", tc.input, stdout)
			}
		} else {
			if code != toolrun.ExitOK {
				t.Errorf("input %q: exit code = %d, want 0", tc.input, code)
			}
			expected := "group:   " + wantGroup
			if !strings.Contains(stdout, expected) {
				t.Errorf("input %q: want %q in output, got %q", tc.input, expected, stdout)
			}
		}
	}
}

// ---- optional sandbox extras ----

func TestExitCode_InfiniteRecursion_Error(t *testing.T) {
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  return route(req)
`)
	_, _, code := runBin(t, bin, script, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (infinite recursion)", code, toolrun.ExitError)
	}
}

func TestExitCode_WrongReturnType_RuntimeError(t *testing.T) {
	// route() returns int instead of string|None → RUNTIME_ERROR: status.
	bin := buildBinary(t)
	script := writeScript(t, `
def route(req):
  return 42
`)
	stdout, _, code := runBin(t, bin, script, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (wrong return type)", code, toolrun.ExitError)
	}
	if !strings.Contains(stdout, toolrun.StatusRuntimeError) {
		t.Errorf("stdout %q: missing %q status prefix", stdout, toolrun.StatusRuntimeError)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write file %q: %v", path, err)
	}
}

// makeConfig mirrors the starlark-test buildConfig function for direct provider tests.
func makeConfig(src string) []byte {
	yaml := "type: script\nprogram: |\n"
	for _, line := range splitLines(src) {
		yaml += "  " + line + "\n"
	}
	return []byte(yaml)
}

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
