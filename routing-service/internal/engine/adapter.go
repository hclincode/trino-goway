package engine

import (
	"context"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/sqlmeta"
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// PipelineEvaluator wraps a Pipeline to satisfy the server.Evaluator interface.
// It translates the proto RouteRequest into a RouteInput before calling the pipeline.
//
// server.Evaluator is defined as:
//
//	Evaluate(ctx context.Context, req *pb.RouteRequest) string
//	Ready() bool
//
// This adapter is the bridge between the gRPC layer (RS-2) and the engine
// layer (RS-3). Wire it in cmd/routing-service/main.go.
type PipelineEvaluator struct {
	p *Pipeline
	// analyzer derives SQL-aware fields (UC-RTG-04) from the request body when
	// the proto did not already carry parsed fields. Never nil after
	// NewPipelineEvaluator (defaults to sqlmeta.Noop, i.e. SQL parsing off).
	analyzer sqlmeta.SQLAnalyzer
	// sqlObs, when non-nil, is invoked once per analysis with the outcome so the
	// server can record metrics without engine depending on the metrics package
	// (no globals). result ∈ "ok"|"empty"|"error" mirrors the SQL-parse counter.
	sqlObs func(result string, dur time.Duration, truncated bool)
}

// NewPipelineEvaluator returns a PipelineEvaluator wrapping p. By default SQL
// analysis is disabled (a no-op analyzer); enable it with WithSQLAnalyzer.
func NewPipelineEvaluator(p *Pipeline, opts ...EvaluatorOption) *PipelineEvaluator {
	e := &PipelineEvaluator{p: p, analyzer: sqlmeta.Noop{}}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// EvaluatorOption configures a PipelineEvaluator.
type EvaluatorOption func(*PipelineEvaluator)

// WithSQLAnalyzer injects the SQL analyzer used to derive UC-RTG-04 fields from
// the request body. Pass sqlmeta.NewHeuristic(...) to enable content routing, or
// omit (the default sqlmeta.Noop) to keep SQL parsing off.
func WithSQLAnalyzer(a sqlmeta.SQLAnalyzer) EvaluatorOption {
	return func(e *PipelineEvaluator) {
		if a != nil {
			e.analyzer = a
		}
	}
}

// WithSQLObserver registers a callback invoked once per in-service SQL analysis
// with the outcome ("ok"|"empty"|"error") and whether the body was truncated.
// Used by the server to record SQL-parse metrics without a metrics dependency
// in the engine package (no globals).
func WithSQLObserver(obs func(result string, dur time.Duration, truncated bool)) EvaluatorOption {
	return func(e *PipelineEvaluator) { e.sqlObs = obs }
}

// Evaluate translates the proto request and delegates to the pipeline.
func (e *PipelineEvaluator) Evaluate(ctx context.Context, req *pb.RouteRequest) string {
	in := e.inputFromProto(req)
	return e.p.Evaluate(ctx, in)
}

// EvaluateResult translates the proto request and returns the observability-rich
// result (group + deciding method type + outcome flags) for RS-9 metrics/logs.
// It also attaches the PII-safe SQL-analysis summary (UC-RTG-04) for the
// decision log.
func (e *PipelineEvaluator) EvaluateResult(ctx context.Context, req *pb.RouteRequest) EvalResult {
	in := e.inputFromProto(req)
	res := e.p.EvaluateResult(ctx, in)
	res.SQL = summarize(in)
	return res
}

// inputFromProto builds the RouteInput and fills the UC-RTG-04 SQL fields in
// service when appropriate. FromProto stays pure (no analyzer dependency); the
// analysis happens here, at the boundary where the analyzer is injected.
//
// The analyzer fires only when: the request is a new submission, the body is
// non-empty, and the proto did not already carry parsed fields (forward-compat
// with a future SQL-aware gateway). Otherwise the proto-provided (or empty)
// fields are used as-is.
func (e *PipelineEvaluator) inputFromProto(req *pb.RouteRequest) *RouteInput {
	in := FromProto(req)
	// Forward-compat: when the proto already carried a parsed QueryType (a future
	// SQL-aware gateway), derive the coarse category from it so rules on
	// query_category work without re-parsing. The proto has no category field.
	if in.HasParsedSQL() && in.QueryType != "" && in.QueryCategory == "" {
		in.QueryCategory = sqlmeta.CategoryForType(in.QueryType)
	}
	e.fillSQLMeta(in)
	return in
}

// fillSQLMeta runs the injected analyzer over in.Body when content analysis is
// warranted, populating the SQL-aware fields. Never errors — a parse miss leaves
// the fields empty with ParseOK=false (fail-safe).
func (e *PipelineEvaluator) fillSQLMeta(in *RouteInput) {
	if !in.IsNew || in.Body == "" || in.HasParsedSQL() {
		return
	}

	var (
		meta      sqlmeta.QueryMeta
		truncated bool
	)
	start := time.Now()
	if h, ok := e.analyzer.(*sqlmeta.Heuristic); ok {
		meta, truncated = h.AnalyzeWithTruncation(in.Body, in.Catalog, in.Schema)
	} else {
		meta = e.analyzer.Analyze(in.Body, in.Catalog, in.Schema)
	}
	dur := time.Since(start)

	in.QueryType = meta.QueryType
	in.QueryCategory = meta.Category
	in.Catalogs = meta.Catalogs
	in.Schemas = meta.Schemas
	in.CatalogSchemas = meta.CatalogSchemas
	in.Tables = meta.Tables
	in.ParseOK = meta.ParseOK

	if e.sqlObs != nil {
		e.sqlObs(sqlResult(meta), dur, truncated)
	}
}

// sqlResult classifies a QueryMeta for the SQL-parse metric label:
//   - "ok"    — analysis recognised the statement (ParseOK)
//   - "empty" — analysis ran but recognised nothing (parse miss / non-SQL)
//
// The heuristic never errors, so "error" is reserved for a future backend that
// can fail; it is emitted by that backend's own observer wiring.
func sqlResult(meta sqlmeta.QueryMeta) string {
	if meta.ParseOK {
		return "ok"
	}
	return "empty"
}

// Ready delegates to the pipeline's Ready method.
func (e *PipelineEvaluator) Ready() bool {
	return e.p.Ready()
}
