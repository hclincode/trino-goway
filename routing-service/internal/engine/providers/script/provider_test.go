package script_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// makeConfig returns YAML config bytes with an inline program.
func makeConfig(program string) []byte {
	yaml := "type: script\nprogram: |\n"
	lines := splitLines(program)
	for _, l := range lines {
		yaml += "  " + l + "\n"
	}
	return []byte(yaml)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func newProvider(t *testing.T, program string) *scriptprovider.Provider {
	t.Helper()
	p := scriptprovider.New()
	if err := p.LoadConfig(makeConfig(program)); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return p
}

func eval(t *testing.T, p *scriptprovider.Provider, in *engine.RouteInput) engine.Decision {
	t.Helper()
	d, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Logf("Evaluate returned error (may be expected): %v", err)
	}
	return d
}

// --- Type ---

func TestProvider_Type(t *testing.T) {
	if scriptprovider.New().Type() != "script" {
		t.Errorf("Type() = %q, want %q", scriptprovider.New().Type(), "script")
	}
}

// --- None/""→defer ---

func TestEvaluate_NoneReturn_Defers(t *testing.T) {
	p := newProvider(t, "def route(req):\n  return None\n")
	d := eval(t, p, &engine.RouteInput{IsNew: true})
	if d.Decided {
		t.Errorf("None return: Decided=true, want false")
	}
}

func TestEvaluate_EmptyStringReturn_Defers(t *testing.T) {
	p := newProvider(t, "def route(req):\n  return \"\"\n")
	d := eval(t, p, &engine.RouteInput{IsNew: true})
	if d.Decided {
		t.Errorf("empty string return: Decided=true, want false")
	}
}

func TestEvaluate_NoProgram_Defers(t *testing.T) {
	p := scriptprovider.New()
	d, err := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	if err != nil {
		t.Errorf("no program: unexpected error: %v", err)
	}
	if d.Decided {
		t.Error("no program: Decided=true, want false")
	}
}

// --- Routing decisions ---

func TestEvaluate_SimpleRoute(t *testing.T) {
	p := newProvider(t, `
def route(req):
  if req.source == "airflow":
    return "etl"
  return None
`)
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
		if d.Decided != tc.decided || d.RoutingGroup != tc.group {
			t.Errorf("source=%q: got {Decided:%v, Group:%q}, want {%v, %q}",
				tc.source, d.Decided, d.RoutingGroup, tc.decided, tc.group)
		}
	}
}

func TestEvaluate_ClientTags(t *testing.T) {
	p := newProvider(t, `
def route(req):
  if "tier=premium" in req.client_tags:
    return "premium"
  return None
`)
	with := &engine.RouteInput{ClientTags: []string{"tier=premium"}, IsNew: true}
	without := &engine.RouteInput{ClientTags: []string{"tier=free"}, IsNew: true}
	empty := &engine.RouteInput{IsNew: true}

	if d := eval(t, p, with); !d.Decided || d.RoutingGroup != "premium" {
		t.Errorf("with tag: %+v", d)
	}
	if d := eval(t, p, without); d.Decided {
		t.Errorf("wrong tag: expected defer, got %+v", d)
	}
	if d := eval(t, p, empty); d.Decided {
		t.Errorf("no tags: expected defer, got %+v", d)
	}
}

func TestEvaluate_IsNew_Accessible(t *testing.T) {
	p := newProvider(t, `
def route(req):
  if req.is_new:
    return "new"
  return "old"
`)
	newReq := &engine.RouteInput{IsNew: true}
	oldReq := &engine.RouteInput{IsNew: false}

	if d := eval(t, p, newReq); !d.Decided || d.RoutingGroup != "new" {
		t.Errorf("is_new=true: %+v", d)
	}
	if d := eval(t, p, oldReq); !d.Decided || d.RoutingGroup != "old" {
		t.Errorf("is_new=false: %+v", d)
	}
}

// --- hashPct determinism ---

func TestEvaluate_HashPct_Deterministic(t *testing.T) {
	p := newProvider(t, `
def route(req):
  if hashPct(req.user) < 5:
    return "canary"
  return "prod"
`)
	in := &engine.RouteInput{User: "alice@example.com", IsNew: true}
	first := eval(t, p, in)
	if !first.Decided {
		t.Fatal("hashPct: expected Decided=true")
	}
	for range 20 {
		d := eval(t, p, in)
		if d.RoutingGroup != first.RoutingGroup {
			t.Fatalf("hashPct non-deterministic: got %q then %q", first.RoutingGroup, d.RoutingGroup)
		}
	}
}

// --- Keep-last-good ---

func TestLoadConfig_KeepLastGood_OnFailedReload(t *testing.T) {
	p := newProvider(t, `
def route(req):
  return "etl"
`)
	in := &engine.RouteInput{IsNew: true}
	if d := eval(t, p, in); !d.Decided || d.RoutingGroup != "etl" {
		t.Fatalf("before bad reload: %+v", d)
	}

	// Attempt to load an invalid script.
	err := p.LoadConfig(makeConfig("this is not valid starlark ??? }}"))
	if err == nil {
		t.Fatal("bad reload: expected error, got nil")
	}

	// Old script still serves.
	if d := eval(t, p, in); !d.Decided || d.RoutingGroup != "etl" {
		t.Errorf("after bad reload: %+v, want old script serving etl", d)
	}
}

func TestLoadConfig_MissingRouteFunc_ReturnsError(t *testing.T) {
	p := scriptprovider.New()
	err := p.LoadConfig(makeConfig("x = 1\n"))
	if err == nil {
		t.Fatal("no route func: expected error, got nil")
	}
}

// --- Step limit: must return in <5ms ---

func TestEvaluate_StepLimit_ReturnsUnder5ms(t *testing.T) {
	// Infinite loop script — step limit must fire within 5ms.
	p := newProvider(t, `
def route(req):
  x = 0
  for i in range(1000000000):
    x = x + 1
  return "etl"
`)
	in := &engine.RouteInput{IsNew: true}
	start := time.Now()
	d, err := p.Evaluate(context.Background(), in)
	elapsed := time.Since(start)

	if elapsed > 5*time.Millisecond {
		t.Errorf("step limit took %v, want < 5ms", elapsed)
	}
	if d.Decided {
		t.Errorf("step limit: Decided=true, want false")
	}
	// err is expected (step limit fires)
	_ = err
}

// --- Deadline propagation: cancel goroutine must not leak ---

func TestEvaluate_ContextDeadline_CancelGoroutineClean(t *testing.T) {
	// Use a script that loops; cancel via a very short deadline.
	p := newProvider(t, `
def route(req):
  x = 0
  for i in range(1000000000):
    x = x + 1
  return "etl"
`)
	in := &engine.RouteInput{IsNew: true}

	// The cancel goroutine must exit cleanly. goleak in TestMain will catch any leak.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	d, _ := p.Evaluate(ctx, in)
	if d.Decided {
		t.Error("deadline: Decided=true, want false")
	}

	// Give the cancel goroutine a moment to drain.
	time.Sleep(10 * time.Millisecond)
	// goleak in TestMain asserts no leaks after all tests.
}

// --- Sandbox negative tests ---

func TestSandbox_Load_Rejected(t *testing.T) {
	// load() must fail at compile/exec time.
	p := scriptprovider.New()
	err := p.LoadConfig(makeConfig(`
load("os", "getenv")
def route(req):
  return None
`))
	if err == nil {
		t.Fatal("load(): expected error, got nil")
	}
}

func TestSandbox_OpenBuiltin_NotDefined(t *testing.T) {
	// open() is not in the Starlark universe — should fail at exec time.
	p := scriptprovider.New()
	err := p.LoadConfig(makeConfig(`
def route(req):
  f = open("/etc/passwd")
  return None
`))
	if err == nil {
		// open is undefined — if it compiled, Evaluate would fail.
		// Either way the program should not succeed.
		d, evalErr := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
		if evalErr == nil && d.Decided {
			t.Error("open() was callable and returned a group — sandbox breach")
		}
	}
	// If err != nil, the sandbox correctly rejected the script.
}

func TestSandbox_ImportKeyword_Rejected(t *testing.T) {
	// "import" is not Starlark syntax.
	p := scriptprovider.New()
	err := p.LoadConfig(makeConfig(`
import sys
def route(req):
  return None
`))
	if err == nil {
		t.Fatal("import keyword: expected compile error, got nil")
	}
}

func TestSandbox_HugeList_StepLimitFires(t *testing.T) {
	// Build a large list via an explicit loop — step limit fires before it grows big.
	p := newProvider(t, `
def route(req):
  x = []
  for i in range(1000000000):
    x.append(i)
  return "x"
`)
	start := time.Now()
	d, _ := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	elapsed := time.Since(start)
	if d.Decided {
		t.Error("huge list loop: Decided=true, want false (step limit)")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("huge list loop took %v, want < 100ms", elapsed)
	}
}

func TestSandbox_LargeDictMutation_StepLimitFires(t *testing.T) {
	// Large dict mutation — step limit should fire.
	p := newProvider(t, `
def route(req):
  x = {}
  for i in range(10000000):
    x[str(i)] = i
  return "x"
`)
	start := time.Now()
	d, _ := p.Evaluate(context.Background(), &engine.RouteInput{IsNew: true})
	elapsed := time.Since(start)
	if d.Decided {
		t.Error("large dict: Decided=true, want false (step limit)")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("large dict took %v, want < 100ms", elapsed)
	}
}

// --- PRD §6.2 worked example ---

func TestEvaluate_PRD_WorkedExample(t *testing.T) {
	p := newProvider(t, `
def route(req):
  if req.source == "airflow":
    return "etl"
  if req.source == "superset":
    if hashPct(req.user) < 5:
      return "interactive-canary"
    return "interactive"
  if "tier=premium" in req.client_tags:
    return "premium"
  if req.user.endswith("@analytics.acme.com"):
    parts = req.user.split("@")
    domain_parts = parts[1].split(".")
    return "etl-" + domain_parts[0]
  return None
`)
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
			wantGrp: "interactive",
		},
		{
			name:    "premium tag→premium",
			in:      &engine.RouteInput{ClientTags: []string{"tier=premium"}, IsNew: true},
			wantGrp: "premium",
		},
		{
			name:    "analytics domain→etl-analytics",
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
				if !d.Decided || d.RoutingGroup != tc.wantGrp {
					t.Errorf("group = %q (decided=%v), want %q", d.RoutingGroup, d.Decided, tc.wantGrp)
				}
			}
		})
	}
}

// --- File-based source ---

func TestLoadConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "route.star")
	content := `
def route(req):
  if req.source == "airflow":
    return "etl"
  return None
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	p := scriptprovider.New()
	cfg := []byte("type: script\nfile: " + path + "\n")
	if err := p.LoadConfig(cfg); err != nil {
		t.Fatalf("LoadConfig from file: %v", err)
	}
	d := eval(t, p, &engine.RouteInput{Source: "airflow", IsNew: true})
	if !d.Decided || d.RoutingGroup != "etl" {
		t.Errorf("file-based script: got %+v", d)
	}
}

func TestLoadConfig_MissingFile_ReturnsError(t *testing.T) {
	p := scriptprovider.New()
	if err := p.LoadConfig([]byte("type: script\nfile: /no/such/file.star\n")); err == nil {
		t.Fatal("missing file: expected error, got nil")
	}
}

func TestLoadConfig_NoSourceNorFile_ReturnsError(t *testing.T) {
	p := scriptprovider.New()
	if err := p.LoadConfig([]byte("type: script\n")); err == nil {
		t.Fatal("no source: expected error, got nil")
	}
}
