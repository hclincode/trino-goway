package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	"github.com/hclincode/trino-goway-routing-service/tools/internal/toolrun"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ---- helpers ----

func writeProgram(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "route.expr")
	if err := os.WriteFile(f, []byte(content), 0600); err != nil {
		t.Fatalf("write program: %v", err)
	}
	return f
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "expr-test")
	cmd := exec.Command("go", "build", "-o", bin,
		"github.com/hclincode/trino-goway-routing-service/tools/expr-test")
	cmd.Dir = findModRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build expr-test: %v\n%s", err, out)
	}
	return bin
}

func findModRoot(t *testing.T) string {
	t.Helper()
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

func TestExitCode_ValidProgram_MatchingInput_OK(t *testing.T) {
	bin := buildBinary(t)
	prog := writeProgram(t, `request.source == "airflow" ? "etl" : ""`)
	stdout, _, code := runBin(t, bin, prog, `{"source":"airflow","is_new":true}`)
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

func TestExitCode_ValidProgram_NonMatchingInput_Deferred(t *testing.T) {
	bin := buildBinary(t)
	prog := writeProgram(t, `request.source == "airflow" ? "etl" : ""`)
	stdout, _, code := runBin(t, bin, prog, `{"source":"superset","is_new":true}`)
	if code != toolrun.ExitOK {
		t.Errorf("exit code = %d, want %d", code, toolrun.ExitOK)
	}
	if !strings.Contains(stdout, "status:  DEFERRED") {
		t.Errorf("stdout %q: missing 'status:  DEFERRED'", stdout)
	}
}

func TestExitCode_CompileError_Error(t *testing.T) {
	bin := buildBinary(t)
	prog := writeProgram(t, "42") // int, not string — type mismatch
	_, stderr, code := runBin(t, bin, prog, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (compile error)", code, toolrun.ExitError)
	}
	if !strings.Contains(stderr, "COMPILE_ERROR") {
		t.Errorf("stderr %q: missing 'COMPILE_ERROR'", stderr)
	}
}

func TestExitCode_TypeMismatch_CompileError(t *testing.T) {
	bin := buildBinary(t)
	prog := writeProgram(t, "true") // bool, not string
	_, stderr, code := runBin(t, bin, prog, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (type mismatch)", code, toolrun.ExitError)
	}
	if !strings.Contains(stderr, "COMPILE_ERROR") {
		t.Errorf("stderr %q: missing 'COMPILE_ERROR'", stderr)
	}
}

func TestExitCode_RuntimeError_RuntimeErrorStatus(t *testing.T) {
	// An out-of-bounds array access is a runtime error (caught by safeEvaluate).
	// The output must contain RUNTIME_ERROR: and exit 1.
	bin := buildBinary(t)
	// client_tags is empty, so index [999] panics at runtime.
	prog := writeProgram(t, `request.client_tags[999]`)
	stdout, _, code := runBin(t, bin, prog, `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (runtime error)", code, toolrun.ExitError)
	}
	if !strings.Contains(stdout, toolrun.StatusRuntimeError) {
		t.Errorf("stdout %q: missing %q status prefix", stdout, toolrun.StatusRuntimeError)
	}
}

func TestExitCode_InlineProgram_OK(t *testing.T) {
	bin := buildBinary(t)
	stdout, _, code := runBin(t, bin,
		"--program", `request.source == "airflow" ? "etl" : ""`,
		`{"source":"airflow","is_new":true}`)
	if code != toolrun.ExitOK {
		t.Errorf("exit code = %d, want %d (inline program)", code, toolrun.ExitOK)
	}
	if !strings.Contains(stdout, "group:   etl") {
		t.Errorf("stdout %q: missing 'group:   etl'", stdout)
	}
}

func TestExitCode_Batch_AllMatch_OK(t *testing.T) {
	bin := buildBinary(t)
	prog := writeProgram(t, `request.source == "airflow" ? "etl" : ""`)
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
	_, _, code := runBin(t, bin, prog, "--samples", samplesFile, "--expect", expectFile)
	if code != toolrun.ExitOK {
		t.Errorf("exit code = %d, want %d (all match)", code, toolrun.ExitOK)
	}
}

func TestExitCode_Batch_Mismatch_ExpectFail(t *testing.T) {
	bin := buildBinary(t)
	prog := writeProgram(t, `"etl"`) // always returns etl
	samplesFile := filepath.Join(t.TempDir(), "samples.yaml")
	mustWriteFile(t, samplesFile, []byte(`
- id: s1
  is_new: true
`))
	expectFile := filepath.Join(t.TempDir(), "expect.yaml")
	mustWriteFile(t, expectFile, []byte("s1: wrong-group\n"))
	_, _, code := runBin(t, bin, prog, "--samples", samplesFile, "--expect", expectFile)
	if code != toolrun.ExitExpectFail {
		t.Errorf("exit code = %d, want %d (expectation mismatch)", code, toolrun.ExitExpectFail)
	}
}

func TestExitCode_MissingFile_Error(t *testing.T) {
	bin := buildBinary(t)
	_, _, code := runBin(t, bin, "/no/such/file.expr", `{"is_new":true}`)
	if code != toolrun.ExitError {
		t.Errorf("exit code = %d, want %d (missing file)", code, toolrun.ExitError)
	}
}

// ---- output == production provider ----

func TestOutput_MatchesProductionProvider(t *testing.T) {
	programSrc := `request.source == "airflow" ? "etl"
  : request.source == "superset" ? "interactive"
  : ""`

	bin := buildBinary(t)
	progFile := writeProgram(t, programSrc)

	cases := []struct {
		input string
		in    *engine.RouteInput
	}{
		{`{"source":"airflow","is_new":true}`, &engine.RouteInput{Source: "airflow", IsNew: true}},
		{`{"source":"superset","is_new":true}`, &engine.RouteInput{Source: "superset", IsNew: true}},
		{`{"source":"dbt","is_new":true}`, &engine.RouteInput{Source: "dbt", IsNew: true}},
	}

	// Build the production provider.
	p := exprovider.New()
	cfg := makeConfig(programSrc)
	if err := p.LoadConfig(cfg); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	for _, tc := range cases {
		d, err := p.Evaluate(context.Background(), tc.in)
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", tc.input, err)
		}
		wantGroup := d.RoutingGroup

		stdout, _, code := runBin(t, bin, progFile, tc.input)
		if wantGroup == "" {
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

// makeConfig wraps a source string into the YAML format LoadConfig expects.
func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write file %q: %v", path, err)
	}
}

func makeConfig(src string) []byte {
	yaml := "type: expr\nprogram: |\n"
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
