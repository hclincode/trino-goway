package validate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/validate"
)

// --- fixtures ---

// validConfig routes source=="a" to the given group via an expr method.
func validConfig(group string) string {
	return "" +
		"addr: \":9001\"\n" +
		"defaultRoutingGroup: default\n" +
		"methods:\n" +
		"  - type: expr\n" +
		"    program: 'request.source == \"a\" ? \"" + group + "\" : \"\"'\n"
}

// configNoMethods is a valid pure-default config (every request → default).
const configNoMethods = "addr: \":9001\"\ndefaultRoutingGroup: default\n"

// badExprConfig has an expr program that returns an int — rejected by the
// AsKind(String) compile check.
const badExprConfig = "" +
	"addr: \":9001\"\n" +
	"defaultRoutingGroup: default\n" +
	"methods:\n" +
	"  - type: expr\n" +
	"    program: '42'\n"

// unknownTypeConfig references a method type the registry does not know.
const unknownTypeConfig = "" +
	"addr: \":9001\"\n" +
	"defaultRoutingGroup: default\n" +
	"methods:\n" +
	"  - type: nope\n" +
	"    program: 'whatever'\n"

// invalidYAML is not parseable as the Config struct.
const invalidYAML = "addr: \":9001\"\ndefaultRoutingGroup: default\nmethods: [this is: not valid"

// missingDefaultGroup fails Validate() (empty defaultRoutingGroup).
const missingDefaultGroup = "addr: \":9001\"\ndefaultRoutingGroup: \"\"\n"

const samplesYAML = "" +
	"- id: hit-a\n" +
	"  source: a\n" +
	"  is_new: true\n" +
	"- id: miss\n" +
	"  source: zzz\n" +
	"  is_new: true\n"

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// writeIn writes content into dir (so multiple files share a tmp dir).
func writeIn(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func run(opts validate.Options) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = validate.Run(&out, &errOut, opts)
	return code, out.String(), errOut.String()
}

// --- validity (no samples) ---

func TestRun_ValidConfig_ExitsOK(t *testing.T) {
	cfg := writeTemp(t, "config.yaml", validConfig("group-a"))
	code, stdout, _ := run(validate.Options{ConfigPath: cfg})
	if code != validate.ExitOK {
		t.Fatalf("exit = %d, want %d", code, validate.ExitOK)
	}
	if strings.TrimSpace(stdout) != "OK" {
		t.Errorf("stdout = %q, want %q", stdout, "OK")
	}
}

func TestRun_PureDefaultConfig_ExitsOK(t *testing.T) {
	cfg := writeTemp(t, "config.yaml", configNoMethods)
	code, _, _ := run(validate.Options{ConfigPath: cfg})
	if code != validate.ExitOK {
		t.Fatalf("exit = %d, want %d", code, validate.ExitOK)
	}
}

func TestRun_InvalidConfigs_ExitInvalid(t *testing.T) {
	cases := []struct {
		name    string
		content string
		errSub  string // substring expected in stderr
	}{
		{"bad-expr", badExprConfig, "methods[0]"},
		{"unknown-type", unknownTypeConfig, "unknown method type"},
		{"invalid-yaml", invalidYAML, "unmarshal"},
		{"missing-default-group", missingDefaultGroup, "defaultRoutingGroup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeTemp(t, "config.yaml", tc.content)
			code, stdout, stderr := run(validate.Options{ConfigPath: cfg})
			if code != validate.ExitInvalid {
				t.Fatalf("exit = %d, want %d (stderr=%q)", code, validate.ExitInvalid, stderr)
			}
			if strings.Contains(stdout, "OK") {
				t.Errorf("stdout unexpectedly contains OK: %q", stdout)
			}
			if !strings.Contains(stderr, tc.errSub) {
				t.Errorf("stderr = %q, want substring %q", stderr, tc.errSub)
			}
		})
	}
}

func TestRun_MissingConfigFile_ExitInvalid(t *testing.T) {
	code, _, stderr := run(validate.Options{ConfigPath: "/no/such/file.yaml"})
	if code != validate.ExitInvalid {
		t.Fatalf("exit = %d, want %d", code, validate.ExitInvalid)
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("stderr = %q, want an error message", stderr)
	}
}

func TestRun_EmptyConfigPath_ExitInvalid(t *testing.T) {
	code, _, stderr := run(validate.Options{ConfigPath: ""})
	if code != validate.ExitInvalid {
		t.Fatalf("exit = %d, want %d", code, validate.ExitInvalid)
	}
	if !strings.Contains(stderr, "--config is required") {
		t.Errorf("stderr = %q, want --config required", stderr)
	}
}

// --- samples table ---

func TestRun_Samples_PrintsTable(t *testing.T) {
	dir := t.TempDir()
	cfg := writeIn(t, dir, "config.yaml", validConfig("group-a"))
	samples := writeIn(t, dir, "samples.yaml", samplesYAML)

	code, stdout, stderr := run(validate.Options{ConfigPath: cfg, SamplesPath: samples})
	if code != validate.ExitOK {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, validate.ExitOK, stderr)
	}
	// Header + both sample rows present.
	for _, want := range []string{"SAMPLE", "GROUP", "hit-a", "group-a", "miss", "default"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestRun_Samples_MissingFile_ExitInvalid(t *testing.T) {
	cfg := writeTemp(t, "config.yaml", validConfig("group-a"))
	code, _, stderr := run(validate.Options{ConfigPath: cfg, SamplesPath: "/no/such/samples.yaml"})
	if code != validate.ExitInvalid {
		t.Fatalf("exit = %d, want %d", code, validate.ExitInvalid)
	}
	if !strings.Contains(stderr, "samples") {
		t.Errorf("stderr = %q, want samples read error", stderr)
	}
}

func TestRun_Samples_BadYAML_ExitInvalid(t *testing.T) {
	dir := t.TempDir()
	cfg := writeIn(t, dir, "config.yaml", validConfig("group-a"))
	samples := writeIn(t, dir, "samples.yaml", "- id: x\n  source: [unclosed")
	code, _, stderr := run(validate.Options{ConfigPath: cfg, SamplesPath: samples})
	if code != validate.ExitInvalid {
		t.Fatalf("exit = %d, want %d", code, validate.ExitInvalid)
	}
	if !strings.Contains(stderr, "samples") {
		t.Errorf("stderr = %q, want samples parse error", stderr)
	}
}

// --- diff mode ---

func TestRun_Diff_NoChange_ExitOK(t *testing.T) {
	dir := t.TempDir()
	cfg := writeIn(t, dir, "config.yaml", validConfig("group-a"))
	baseline := writeIn(t, dir, "baseline.yaml", validConfig("group-a"))
	samples := writeIn(t, dir, "samples.yaml", samplesYAML)

	code, stdout, stderr := run(validate.Options{
		ConfigPath: cfg, SamplesPath: samples, Diff: true, BaselinePath: baseline,
	})
	if code != validate.ExitOK {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, validate.ExitOK, stderr)
	}
	// Diff table has OLD and NEW columns; no CHANGED markers when identical.
	for _, want := range []string{"OLD", "NEW"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff table missing %q\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "CHANGED") {
		t.Errorf("unexpected CHANGED marker when routes are identical:\n%s", stdout)
	}
}

func TestRun_Diff_RouteChanged_ExitDiff(t *testing.T) {
	dir := t.TempDir()
	cfg := writeIn(t, dir, "config.yaml", validConfig("group-b"))     // new: a → group-b
	baseline := writeIn(t, dir, "baseline.yaml", validConfig("group-a")) // old: a → group-a
	samples := writeIn(t, dir, "samples.yaml", samplesYAML)

	code, stdout, stderr := run(validate.Options{
		ConfigPath: cfg, SamplesPath: samples, Diff: true, BaselinePath: baseline,
	})
	if code != validate.ExitDiff {
		t.Fatalf("exit = %d, want %d (ExitDiff)", code, validate.ExitDiff)
	}
	if !strings.Contains(stdout, "CHANGED") {
		t.Errorf("expected a CHANGED marker on the diffed row:\n%s", stdout)
	}
	if !strings.Contains(stderr, "route(s) changed") {
		t.Errorf("stderr = %q, want a changed-routes summary", stderr)
	}
	// The unchanged "miss" sample (default in both) must NOT be marked changed:
	// exactly one CHANGED occurrence (the hit-a row).
	if n := strings.Count(stdout, "CHANGED"); n != 1 {
		t.Errorf("CHANGED count = %d, want 1\n%s", n, stdout)
	}
}

func TestRun_Diff_WithoutBaseline_ExitInvalid(t *testing.T) {
	dir := t.TempDir()
	cfg := writeIn(t, dir, "config.yaml", validConfig("group-a"))
	samples := writeIn(t, dir, "samples.yaml", samplesYAML)
	code, _, stderr := run(validate.Options{ConfigPath: cfg, SamplesPath: samples, Diff: true})
	if code != validate.ExitInvalid {
		t.Fatalf("exit = %d, want %d", code, validate.ExitInvalid)
	}
	if !strings.Contains(stderr, "--diff requires --baseline") {
		t.Errorf("stderr = %q, want --baseline required", stderr)
	}
}

func TestRun_Diff_BrokenBaseline_ExitInvalid(t *testing.T) {
	dir := t.TempDir()
	cfg := writeIn(t, dir, "config.yaml", validConfig("group-a"))
	baseline := writeIn(t, dir, "baseline.yaml", badExprConfig)
	samples := writeIn(t, dir, "samples.yaml", samplesYAML)
	code, _, stderr := run(validate.Options{
		ConfigPath: cfg, SamplesPath: samples, Diff: true, BaselinePath: baseline,
	})
	if code != validate.ExitInvalid {
		t.Fatalf("exit = %d, want %d (a broken baseline is a config error, not a diff)", code, validate.ExitInvalid)
	}
	if !strings.Contains(stderr, "baseline") {
		t.Errorf("stderr = %q, want baseline error", stderr)
	}
}
