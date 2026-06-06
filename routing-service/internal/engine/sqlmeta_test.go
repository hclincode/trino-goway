package engine_test

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	"github.com/hclincode/trino-goway-routing-service/internal/sqlmeta"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// captureMethod is a RoutingMethod that records the RouteInput it sees and
// routes via a caller-supplied decide func. It lets the SQL-wiring tests assert
// on the fields the pipeline hands providers without depending on a real
// provider package.
type captureMethod struct {
	last   *engine.RouteInput
	decide func(in *engine.RouteInput) string
}

func (m *captureMethod) Type() string            { return "capture" }
func (m *captureMethod) LoadConfig(_ []byte) error { return nil }
func (m *captureMethod) Evaluate(_ context.Context, in *engine.RouteInput) (engine.Decision, error) {
	m.last = in
	g := ""
	if m.decide != nil {
		g = m.decide(in)
	}
	if g == "" {
		return engine.Decision{Decided: false}, nil
	}
	return engine.Decision{RoutingGroup: g, Decided: true}, nil
}

func newCapturePipeline(t *testing.T, m *captureMethod) *engine.Pipeline {
	t.Helper()
	log := slog.New(slog.NewTextHandler(discard{}, &slog.HandlerOptions{Level: slog.LevelError}))
	return engine.NewPipeline([]engine.RoutingMethod{m}, "default", log)
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func newSQLReq(body string, isNew bool) *pb.RouteRequest {
	return &pb.RouteRequest{
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			Body:                 body,
			IsNewQuerySubmission: isNew,
		},
		Method:     "POST",
		RequestUri: "/v1/statement",
	}
}

// TestFromProto_PrefersProtoParsedFields verifies the forward-compat path: when
// the proto carries parsed SQL fields (a future SQL-aware gateway populated
// them), FromProto uses them verbatim and HasParsedSQL reports true so the
// in-service analyzer is skipped.
func TestFromProto_PrefersProtoParsedFields(t *testing.T) {
	req := &pb.RouteRequest{
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			Body:                     "SELECT * FROM other.place.thing",
			IsNewQuerySubmission:     true,
			QueryType:                "INSERT",
			Catalogs:                 []string{"hive"},
			Schemas:                  []string{"sales"},
			CatalogSchemas:           []string{"hive.sales"},
			Tables:                   []string{"hive.sales.orders"},
			IsQueryParsingSuccessful: true,
		},
	}

	in := engine.FromProto(req)

	if !in.HasParsedSQL() {
		t.Fatalf("HasParsedSQL = false, want true (proto carried parsed fields)")
	}
	if in.QueryType != "INSERT" {
		t.Errorf("QueryType = %q, want INSERT (from proto, not derived from body)", in.QueryType)
	}
	if !in.ParseOK {
		t.Errorf("ParseOK = false, want true")
	}
	if !reflect.DeepEqual(in.Tables, []string{"hive.sales.orders"}) {
		t.Errorf("Tables = %v, want [hive.sales.orders]", in.Tables)
	}
}

// TestFromProto_EmptySQLFieldsNormalised verifies that absent proto SQL fields
// become non-nil empty slices and HasParsedSQL is false (so the analyzer runs).
func TestFromProto_EmptySQLFieldsNormalised(t *testing.T) {
	req := &pb.RouteRequest{
		TrinoQueryProperties: &pb.TrinoQueryProperties{
			Body:                 "SELECT 1",
			IsNewQuerySubmission: true,
		},
	}
	in := engine.FromProto(req)

	if in.HasParsedSQL() {
		t.Errorf("HasParsedSQL = true, want false (no proto parsed fields)")
	}
	for name, s := range map[string][]string{
		"Catalogs": in.Catalogs, "Schemas": in.Schemas,
		"CatalogSchemas": in.CatalogSchemas, "Tables": in.Tables,
	} {
		if s == nil {
			t.Errorf("%s is nil, want non-nil empty slice", name)
		}
		if len(s) != 0 {
			t.Errorf("%s = %v, want empty", name, s)
		}
	}
}

// TestEvaluator_DerivesSQLFromBody verifies the in-service analyzer fills the
// SQL fields from the body when the request is new and the proto had none — and
// that the derived fields reach the provider.
func TestEvaluator_DerivesSQLFromBody(t *testing.T) {
	m := &captureMethod{decide: func(in *engine.RouteInput) string {
		if in.QueryType == "INSERT" {
			return "etl"
		}
		if in.QueryType == "SELECT" && contains(in.Catalogs, "hive") {
			return "analytics"
		}
		return ""
	}}
	pipeline := newCapturePipeline(t, m)
	eval := engine.NewPipelineEvaluator(pipeline,
		engine.WithSQLAnalyzer(sqlmeta.NewHeuristic(0)))

	cases := []struct {
		name, body, want string
	}{
		{"insert routes to etl", "INSERT INTO hive.s.t SELECT * FROM hive.s.src", "etl"},
		{"hive select routes to analytics", "SELECT * FROM hive.sales.orders", "analytics"},
		{"non-hive select falls back to default", "SELECT * FROM pg.public.users", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := eval.EvaluateResult(context.Background(), newSQLReq(tc.body, true))
			if res.RoutingGroup != tc.want {
				t.Errorf("group = %q, want %q", res.RoutingGroup, tc.want)
			}
			if !m.last.ParseOK {
				t.Errorf("provider saw ParseOK=false; analyzer should have run")
			}
		})
	}
}

// TestEvaluator_AnalyzerSkippedWhenNotNew verifies analysis only fires on new
// submissions: a non-new request leaves the SQL fields empty even with a body.
func TestEvaluator_AnalyzerSkippedWhenNotNew(t *testing.T) {
	m := &captureMethod{decide: func(in *engine.RouteInput) string {
		if in.QueryType == "INSERT" {
			return "etl"
		}
		return ""
	}}
	pipeline := newCapturePipeline(t, m)
	eval := engine.NewPipelineEvaluator(pipeline,
		engine.WithSQLAnalyzer(sqlmeta.NewHeuristic(0)))

	res := eval.EvaluateResult(context.Background(), newSQLReq("INSERT INTO hive.s.t VALUES (1)", false))
	if res.RoutingGroup != "default" {
		t.Errorf("group = %q, want default (analysis must not fire on non-new)", res.RoutingGroup)
	}
	if m.last.QueryType != "" || m.last.ParseOK {
		t.Errorf("provider saw QueryType=%q ParseOK=%v, want empty/false on non-new",
			m.last.QueryType, m.last.ParseOK)
	}
}

// TestEvaluator_NoopAnalyzerLeavesParseOKFalse verifies that with SQL parsing
// off (default Noop analyzer) the fields stay empty and rules fall back.
func TestEvaluator_NoopAnalyzerLeavesParseOKFalse(t *testing.T) {
	m := &captureMethod{decide: func(in *engine.RouteInput) string {
		if in.ParseOK {
			return "parsed"
		}
		return ""
	}}
	pipeline := newCapturePipeline(t, m)
	eval := engine.NewPipelineEvaluator(pipeline) // no analyzer → Noop

	res := eval.EvaluateResult(context.Background(), newSQLReq("SELECT * FROM hive.s.t", true))
	if res.RoutingGroup != "default" {
		t.Errorf("group = %q, want default (parse_ok must be false with parsing off)", res.RoutingGroup)
	}
	if res.SQL.ParseOK || m.last.ParseOK {
		t.Errorf("ParseOK = true, want false with Noop analyzer")
	}
}

// TestEvaluator_SQLSummaryForDecisionLog verifies the PII-safe summary attached
// to EvalResult: type/category + counts, never identifiers.
func TestEvaluator_SQLSummaryForDecisionLog(t *testing.T) {
	pipeline := newCapturePipeline(t, &captureMethod{}) // always defer → default
	eval := engine.NewPipelineEvaluator(pipeline,
		engine.WithSQLAnalyzer(sqlmeta.NewHeuristic(0)))

	body := "SELECT * FROM hive.sales.orders o JOIN hive.sales.customers c ON o.id=c.id"
	res := eval.EvaluateResult(context.Background(), newSQLReq(body, true))

	if res.SQL.QueryType != "SELECT" {
		t.Errorf("SQL.QueryType = %q, want SELECT", res.SQL.QueryType)
	}
	if res.SQL.QueryCategory != sqlmeta.CategoryRead {
		t.Errorf("SQL.QueryCategory = %q, want READ", res.SQL.QueryCategory)
	}
	if !res.SQL.ParseOK {
		t.Errorf("SQL.ParseOK = false, want true")
	}
	if res.SQL.TableCount != 2 {
		t.Errorf("SQL.TableCount = %d, want 2", res.SQL.TableCount)
	}
	if res.SQL.CatalogCount != 1 {
		t.Errorf("SQL.CatalogCount = %d, want 1", res.SQL.CatalogCount)
	}
}

// TestEvaluator_SQLObserverInvoked verifies the observer fires with the result
// class so the server can record SQL-parse metrics without a metrics dep here.
func TestEvaluator_SQLObserverInvoked(t *testing.T) {
	pipeline := newCapturePipeline(t, &captureMethod{})

	var gotResult string
	var gotCalls int
	eval := engine.NewPipelineEvaluator(pipeline,
		engine.WithSQLAnalyzer(sqlmeta.NewHeuristic(0)),
		engine.WithSQLObserver(func(result string, _ time.Duration, _ bool) {
			gotResult = result
			gotCalls++
		}),
	)

	eval.EvaluateResult(context.Background(), newSQLReq("SELECT 1", true))
	if gotCalls != 1 {
		t.Fatalf("observer calls = %d, want 1", gotCalls)
	}
	if gotResult != "ok" {
		t.Errorf("observer result = %q, want ok", gotResult)
	}

	// A non-SQL body → "empty".
	eval.EvaluateResult(context.Background(), newSQLReq("not sql here", true))
	if gotResult != "empty" {
		t.Errorf("observer result = %q, want empty for non-SQL body", gotResult)
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
