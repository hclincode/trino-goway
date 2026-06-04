package expr_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// makeConfig returns YAML config bytes in the format produced by
// engine.Registry.methodConfigBytes, with an inline program.
func makeConfig(program string) []byte {
	yaml := "type: expr\nprogram: |\n"
	for _, line := range splitLines(program) {
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

func newProvider(t *testing.T, program string) *exprovider.Provider {
	t.Helper()
	p := exprovider.New()
	if err := p.LoadConfig(makeConfig(program)); err != nil {
		t.Fatalf("LoadConfig(%q): %v", program, err)
	}
	return p
}

func eval(t *testing.T, p *exprovider.Provider, in *engine.RouteInput) engine.Decision {
	t.Helper()
	d, err := p.Evaluate(context.Background(), in)
	if err != nil {
		// Provider may return a non-nil error on runtime failure; that is OK — pipeline
		// handles it. For tests that don't expect errors we still return the decision.
		t.Logf("Evaluate returned error (may be expected): %v", err)
	}
	return d
}

// --- Type() ---

func TestProvider_Type(t *testing.T) {
	p := exprovider.New()
	if p.Type() != "expr" {
		t.Errorf("Type() = %q, want %q", p.Type(), "expr")
	}
}

// --- Compile errors ---

func TestLoadConfig_SyntaxError_ReturnsError(t *testing.T) {
	p := exprovider.New()
	err := p.LoadConfig(makeConfig("source === invalid ??? "))
	if err == nil {
		t.Fatal("LoadConfig with syntax error: expected error, got nil")
	}
}

func TestLoadConfig_TypeMismatch_ReturnsError(t *testing.T) {
	// Program returns int, not string — should be rejected.
	p := exprovider.New()
	err := p.LoadConfig(makeConfig("42"))
	if err == nil {
		t.Fatal("LoadConfig with int-returning program: expected error (type mismatch), got nil")
	}
}

func TestLoadConfig_BoolReturn_ReturnsError(t *testing.T) {
	p := exprovider.New()
	err := p.LoadConfig(makeConfig("true"))
	if err == nil {
		t.Fatal("LoadConfig with bool-returning program: expected error, got nil")
	}
}

func TestLoadConfig_UnknownVariable_ReturnsError(t *testing.T) {
	// Reference an undefined variable — compile should reject it.
	p := exprovider.New()
	err := p.LoadConfig(makeConfig("unknownVar == \"x\" ? \"y\" : \"\""))
	if err == nil {
		t.Fatal("LoadConfig with undefined variable: expected error, got nil")
	}
}

// --- Keep-last-good ---

func TestLoadConfig_KeepLastGood_OnFailedReload(t *testing.T) {
	// Step 1: load a valid program that routes airflow→etl.
	p := newProvider(t, `request.source == "airflow" ? "etl" : ""`)

	in := &engine.RouteInput{Source: "airflow", IsNew: true}
	d := eval(t, p, in)
	if !d.Decided || d.RoutingGroup != "etl" {
		t.Fatalf("before bad reload: got %+v, want etl", d)
	}

	// Step 2: attempt to load an invalid program — must return error.
	err := p.LoadConfig(makeConfig("this is not valid expr ??? "))
	if err == nil {
		t.Fatal("bad reload: expected error, got nil")
	}

	// Step 3: old program still serves.
	d2 := eval(t, p, in)
	if !d2.Decided || d2.RoutingGroup != "etl" {
		t.Errorf("after bad reload: got %+v, want old program still serving etl", d2)
	}
}

// --- Routing decisions ---

func TestEvaluate_EmptyProgram_ReturnsDefer(t *testing.T) {
	p := newProvider(t, `""`)
	d := eval(t, p, &engine.RouteInput{IsNew: true})
	if d.Decided {
		t.Errorf("empty-string program: Decided = true, want false")
	}
}

func TestEvaluate_NoProgram_ReturnsDefer(t *testing.T) {
	// LoadConfig never called — Evaluate must not panic and must return defer.
	p := exprovider.New()
	d, err := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if err != nil {
		t.Errorf("no program: unexpected error %v", err)
	}
	if d.Decided {
		t.Error("no program: Decided = true, want false")
	}
}

func TestEvaluate_SimpleRoute_Decided(t *testing.T) {
	p := newProvider(t, `request.source == "airflow" ? "etl" : ""`)

	cases := []struct {
		source  string
		decided bool
		group   string
	}{
		{"airflow", true, "etl"},
		{"superset", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		d := eval(t, p, &engine.RouteInput{Source: tc.source, IsNew: true})
		if d.Decided != tc.decided {
			t.Errorf("source=%q: Decided=%v, want %v", tc.source, d.Decided, tc.decided)
		}
		if d.RoutingGroup != tc.group {
			t.Errorf("source=%q: group=%q, want %q", tc.source, d.RoutingGroup, tc.group)
		}
	}
}

func TestEvaluate_ClientTag_Matching(t *testing.T) {
	p := newProvider(t, `"tier=premium" in request.client_tags ? "premium" : ""`)

	withTag := &engine.RouteInput{ClientTags: []string{"tier=premium"}, IsNew: true}
	without := &engine.RouteInput{ClientTags: []string{"tier=free"}, IsNew: true}
	empty := &engine.RouteInput{IsNew: true}

	if d := eval(t, p, withTag); !d.Decided || d.RoutingGroup != "premium" {
		t.Errorf("with tag: %+v", d)
	}
	if d := eval(t, p, without); d.Decided {
		t.Errorf("wrong tag: expected defer, got %+v", d)
	}
	if d := eval(t, p, empty); d.Decided {
		t.Errorf("no tags: expected defer, got %+v", d)
	}
}

func TestEvaluate_HashPct_Deterministic(t *testing.T) {
	// hashPct is accessible in the program and returns consistent results.
	p := newProvider(t, `hashPct(request.user) < 5 ? "canary" : "prod"`)

	in := &engine.RouteInput{User: "alice@example.com", IsNew: true}
	first := eval(t, p, in)
	if !first.Decided {
		t.Fatal("hashPct program: expected Decided=true")
	}

	// Same input must always map to the same bucket.
	for range 20 {
		d := eval(t, p, in)
		if d.RoutingGroup != first.RoutingGroup {
			t.Fatalf("hashPct non-deterministic: got %q then %q", first.RoutingGroup, d.RoutingGroup)
		}
	}
}

func TestEvaluate_HashPct_ReachableInEnv(t *testing.T) {
	// Verify hashPct can be called with various strings without compile error.
	p := newProvider(t, `hashPct(request.user) < 100 ? "yes" : "no"`)
	d := eval(t, p, &engine.RouteInput{User: "bob", IsNew: true})
	if !d.Decided {
		t.Error("hashPct callable: expected Decided=true for < 100")
	}
	if d.RoutingGroup != "yes" {
		t.Errorf("hashPct callable: group = %q, want yes", d.RoutingGroup)
	}
}

// --- PRD §6.2 worked example ---

func TestEvaluate_PRD_WorkedExample(t *testing.T) {
	// This is the full worked example from PRD §6.2.
	program := `request.source == "airflow" ? "etl"
  : request.source == "superset" ? (hashPct(request.user) < 5 ? "interactive-canary" : "interactive")
  : "tier=premium" in request.client_tags ? "premium"
  : hasSuffix(request.user, "@analytics.acme.com") ? "etl-" + split(split(request.user, "@")[1], ".")[0]
  : ""`

	p := newProvider(t, program)

	cases := []struct {
		name    string
		in      *engine.RouteInput
		wantGrp string // "" means defer
	}{
		{
			name:    "airflow→etl",
			in:      &engine.RouteInput{Source: "airflow", User: "pipe@x.com", IsNew: true},
			wantGrp: "etl",
		},
		{
			name:    "superset→interactive (non-canary bucket)",
			in:      &engine.RouteInput{Source: "superset", User: "canonical-non-canary-user-xyz", IsNew: true},
			wantGrp: "interactive", // hashPct("canonical-non-canary-user-xyz") >= 5
		},
		{
			name:    "premium tag→premium",
			in:      &engine.RouteInput{ClientTags: []string{"tier=premium"}, IsNew: true},
			wantGrp: "premium",
		},
		{
			name:    "analytics domain→computed etl group",
			in:      &engine.RouteInput{User: "alice@analytics.acme.com", IsNew: true},
			wantGrp: "etl-analytics",
		},
		{
			name:    "no match→defer",
			in:      &engine.RouteInput{Source: "dbt", User: "bob@other.com", IsNew: true},
			wantGrp: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := eval(t, p, tc.in)
			if tc.wantGrp == "" {
				if d.Decided {
					t.Errorf("expected defer, got group %q", d.RoutingGroup)
				}
			} else {
				if !d.Decided {
					t.Errorf("expected group %q, got defer", tc.wantGrp)
				} else if d.RoutingGroup != tc.wantGrp {
					t.Errorf("group = %q, want %q", d.RoutingGroup, tc.wantGrp)
				}
			}
		})
	}
}

// --- Field accessibility ---

func TestEvaluate_AllRouteInputFields_Accessible(t *testing.T) {
	// Verify every RouteInput field is accessible without a compile error.
	programs := []string{
		`request.source == "" ? "x" : ""`,
		`len(request.client_tags) == 0 ? "x" : ""`,
		`request.user == "" ? "x" : ""`,
		`request.catalog == "" ? "x" : ""`,
		`request.schema == "" ? "x" : ""`,
		`request.method == "" ? "x" : ""`,
		`request.uri == "" ? "x" : ""`,
		`request.remote_addr == "" ? "x" : ""`,
		`request.body == "" ? "x" : ""`,
		`request.is_new ? "x" : ""`,
		`request.param_map == nil ? "x" : ""`,
	}
	for _, prog := range programs {
		p := exprovider.New()
		if err := p.LoadConfig(makeConfig(prog)); err != nil {
			t.Errorf("program %q: unexpected compile error: %v", prog, err)
		}
	}
}

// --- File-based source ---

func TestLoadConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "route.expr")
	if err := os.WriteFile(path, []byte(`request.source == "airflow" ? "etl" : ""`), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := exprovider.New()
	cfg := []byte("type: expr\nfile: " + path + "\n")
	if err := p.LoadConfig(cfg); err != nil {
		t.Fatalf("LoadConfig from file: %v", err)
	}

	d := eval(t, p, &engine.RouteInput{Source: "airflow", IsNew: true})
	if !d.Decided || d.RoutingGroup != "etl" {
		t.Errorf("file-based program: got %+v, want etl", d)
	}
}

func TestLoadConfig_MissingFile_ReturnsError(t *testing.T) {
	p := exprovider.New()
	cfg := []byte("type: expr\nfile: /no/such/file.expr\n")
	if err := p.LoadConfig(cfg); err == nil {
		t.Fatal("LoadConfig with missing file: expected error, got nil")
	}
}

func TestLoadConfig_NoSourceNorFile_ReturnsError(t *testing.T) {
	p := exprovider.New()
	cfg := []byte("type: expr\n")
	if err := p.LoadConfig(cfg); err == nil {
		t.Fatal("LoadConfig with no program or file: expected error, got nil")
	}
}

// --- PRD §6.2 verbatim compile check ---

// TestEvaluate_PRDExample_CompilesAsWritten verifies that the exact expr program
// from PRD §6.2 compiles and routes correctly. This is the authoritative proof
// that the documented operator contract works verbatim — if this test fails, the
// field naming contract is broken.
func TestEvaluate_PRDExample_CompilesAsWritten(t *testing.T) {
	// Exact string from PRD §6.2 — do NOT reformat or rename fields.
	prdExprProgram := `request.source == "airflow" ? "etl"
  : request.source == "superset" ? (hashPct(request.user) < 5 ? "interactive-canary" : "interactive")
  : "tier=premium" in request.client_tags ? "premium"
  : hasSuffix(request.user, "@analytics.acme.com") ? "etl-" + split(split(request.user, "@")[1], ".")[0]
  : ""`

	p := exprovider.New()
	if err := p.LoadConfig(makeConfig(prdExprProgram)); err != nil {
		t.Fatalf("PRD §6.2 expr example failed to compile: %v\n"+
			"This means the operator-facing field contract is broken.", err)
	}

	cases := []struct {
		source string
		user   string
		tags   []string
		want   string
	}{
		{"airflow", "pipe@x.com", nil, "etl"},
		{"superset", "canonical-non-canary-user-xyz", nil, "interactive"},
		{"", "", []string{"tier=premium"}, "premium"},
		{"", "alice@analytics.acme.com", nil, "etl-analytics"},
		{"dbt", "bob@other.com", nil, ""},
	}
	for _, tc := range cases {
		in := &engine.RouteInput{Source: tc.source, User: tc.user, ClientTags: tc.tags, IsNew: true}
		d := eval(t, p, in)
		if tc.want == "" {
			if d.Decided {
				t.Errorf("source=%q user=%q: expected defer, got group %q", tc.source, tc.user, d.RoutingGroup)
			}
		} else {
			if !d.Decided || d.RoutingGroup != tc.want {
				t.Errorf("source=%q user=%q tags=%v: group=%q decided=%v, want %q",
					tc.source, tc.user, tc.tags, d.RoutingGroup, d.Decided, tc.want)
			}
		}
	}
}
