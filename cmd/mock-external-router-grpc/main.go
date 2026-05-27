// Command mock-external-router-grpc is a development tool that mimics an
// external gRPC routing service for trino-goway. It implements the
// TrinoGatewayRouter service, pretty-prints each RouteRequest, and returns a
// fixed routing group.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/hclincode/trino-goway/internal/routing/routerpb"
)

func main() {
	addr := flag.String("addr", ":9001", "address to listen on")
	group := flag.String("group", "default", "routing group to return in responses")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Error("failed to listen", "addr", *addr, "error", err)
		os.Exit(1)
	}

	srv := newServer(*group, os.Stdout)

	logger.Info("mock external gRPC router listening", "addr", *addr, "group", *group)
	if err := srv.Serve(lis); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func newServer(group string, out io.Writer) *grpc.Server {
	s := grpc.NewServer()
	routerpb.RegisterTrinoGatewayRouterServer(s, &mockRouter{group: group, out: out})
	reflection.Register(s)
	return s
}

type mockRouter struct {
	routerpb.UnimplementedTrinoGatewayRouterServer
	group string
	out   io.Writer
}

func (m *mockRouter) Route(_ context.Context, req *routerpb.RouteRequest) (*routerpb.RouteResponse, error) {
	marshaller := protojson.MarshalOptions{Multiline: true, Indent: "  "}
	body, err := marshaller.Marshal(req)
	if err != nil {
		fmt.Fprintf(m.out, "failed to marshal RouteRequest: %v\n", err)
	} else {
		ts := time.Now().UTC().Format(time.RFC3339)
		fmt.Fprintf(m.out, "%s  /trino.gateway.v1.TrinoGatewayRouter/Route\n%s\n\n", ts, body)
	}

	return &routerpb.RouteResponse{
		RoutingGroup:    m.group,
		Errors:          []string{},
		ExternalHeaders: map[string]string{},
	}, nil
}
