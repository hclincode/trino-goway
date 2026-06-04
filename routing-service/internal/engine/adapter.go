package engine

import (
	"context"

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
}

// NewPipelineEvaluator returns a PipelineEvaluator wrapping p.
func NewPipelineEvaluator(p *Pipeline) *PipelineEvaluator {
	return &PipelineEvaluator{p: p}
}

// Evaluate translates the proto request and delegates to the pipeline.
func (e *PipelineEvaluator) Evaluate(ctx context.Context, req *pb.RouteRequest) string {
	in := FromProto(req)
	return e.p.Evaluate(ctx, in)
}

// Ready delegates to the pipeline's Ready method.
func (e *PipelineEvaluator) Ready() bool {
	return e.p.Ready()
}
