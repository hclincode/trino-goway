package routing

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/hclincode/trino-goway/internal/config"
	pb "github.com/hclincode/trino-goway/internal/routing/routerpb"
)

// externalGRPCSelector calls the external gRPC routing service to select a routing group.
// Fallback semantics are identical to the HTTP transport: any error returns ("", nil, err).
type externalGRPCSelector struct {
	cfg    config.ExternalConfig
	client pb.TrinoGatewayRouterClient
}

// newExternalGRPCSelector dials the gRPC address and returns a selector.
// Returns nil if GRPCAddr is not configured.
func newExternalGRPCSelector(cfg config.ExternalConfig) (*externalGRPCSelector, error) {
	if cfg.GRPCAddr == "" {
		return nil, nil
	}
	conn, err := grpc.NewClient(cfg.GRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("routing: grpc: dial %q: %w", cfg.GRPCAddr, err)
	}
	return &externalGRPCSelector{
		cfg:    cfg,
		client: pb.NewTrinoGatewayRouterClient(conn),
	}, nil
}

// selectGroup calls the gRPC Route RPC and returns routing group, headers, and errors.
// Returns ("", nil, nil, err) on any failure — the caller falls back to default.
func (s *externalGRPCSelector) selectGroup(ctx context.Context, req *RouteInput) (routingGroup string, externalHeaders map[string]string, errors []string, err error) {
	if s == nil || s.client == nil {
		return "", nil, nil, nil
	}

	timeout := s.cfg.Timeout.D
	if timeout == 0 {
		timeout = defaultExternalTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pbReq := buildProtoRequest(req)
	resp, err := s.client.Route(callCtx, pbReq)
	if err != nil {
		return "", nil, nil, fmt.Errorf("routing: grpc: route: %w", err)
	}

	headers := resp.GetExternalHeaders()
	if headers == nil {
		headers = map[string]string{}
	}
	return resp.GetRoutingGroup(), headers, resp.GetErrors(), nil
}

// buildProtoRequest constructs a RouteRequest proto from a RouteInput.
func buildProtoRequest(req *RouteInput) *pb.RouteRequest {
	errMsg := "trino-parser not available in Go v1"
	catalog := req.Header("X-Trino-Catalog")
	schema := req.Header("X-Trino-Schema")

	qp := &pb.TrinoQueryProperties{
		Body:                     req.Body,
		QueryType:                "",
		ResourceGroupQueryType:   "",
		DefaultCatalog:           catalog,
		DefaultSchema:            schema,
		Catalogs:                 []string{},
		Schemas:                  []string{},
		CatalogSchemas:           []string{},
		Tables:                   []string{},
		IsNewQuerySubmission:     req.Method == "POST",
		IsQueryParsingSuccessful: false,
		ErrorMessage:             errMsg,
	}

	rtu := &pb.TrinoRequestUser{
		User: req.Header("X-Trino-User"),
	}

	// Comma-join multi-valued parameters per architect ruling on map<string, string>.
	paramMap := make(map[string]string, len(req.Parameters))
	for k, vals := range req.Parameters {
		paramMap[k] = strings.Join(vals, ",")
	}

	return &pb.RouteRequest{
		TrinoQueryProperties: qp,
		TrinoRequestUser:     rtu,
		ContentType:          "application/json",
		RemoteUser:           req.RemoteUser,
		Method:               req.Method,
		RequestUri:           req.RequestURI,
		QueryString:          req.QueryString,
		RemoteAddr:           req.RemoteAddr,
		RemoteHost:           req.RemoteHost,
		ParameterMap:         paramMap,
	}
}
