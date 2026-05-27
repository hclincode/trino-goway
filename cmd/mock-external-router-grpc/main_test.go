package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/hclincode/trino-goway/internal/routing/routerpb"
)

const bufSize = 1024 * 1024

func startServer(t *testing.T, group string, out io.Writer) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := newServer(group, out)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("serve returned: %v", err)
		}
	}()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.DialContext(context.Background())
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestRoute(t *testing.T) {
	tests := []struct {
		name       string
		group      string
		req        *routerpb.RouteRequest
		wantOutput []string
	}{
		{
			name:  "default group returned and request fields logged",
			group: "default",
			req: &routerpb.RouteRequest{
				Method:     "POST",
				RequestUri: "/v1/statement",
				TrinoRequestUser: &routerpb.TrinoRequestUser{
					User: "alice",
				},
			},
			wantOutput: []string{
				"\"method\": \"POST\"",
				"\"requestUri\": \"/v1/statement\"",
				"\"user\": \"alice\"",
				"/trino.gateway.v1.TrinoGatewayRouter/Route",
			},
		},
		{
			name:  "analytics group flows through",
			group: "analytics",
			req: &routerpb.RouteRequest{
				Method:     "GET",
				RequestUri: "/v1/info",
				TrinoRequestUser: &routerpb.TrinoRequestUser{
					User: "bob",
				},
			},
			wantOutput: []string{
				"\"method\": \"GET\"",
				"\"requestUri\": \"/v1/info\"",
				"\"user\": \"bob\"",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			conn := startServer(t, tc.group, &out)
			client := routerpb.NewTrinoGatewayRouterClient(conn)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resp, err := client.Route(ctx, tc.req)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if resp.GetRoutingGroup() != tc.group {
				t.Errorf("routingGroup: got %q, want %q", resp.GetRoutingGroup(), tc.group)
			}
			if len(resp.GetErrors()) != 0 {
				t.Errorf("errors: got %#v, want empty", resp.GetErrors())
			}
			if len(resp.GetExternalHeaders()) != 0 {
				t.Errorf("externalHeaders: got %#v, want empty", resp.GetExternalHeaders())
			}

			got := out.String()
			for _, want := range tc.wantOutput {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q\nfull output:\n%s", want, got)
				}
			}
		})
	}
}

func TestReflectionRegistered(t *testing.T) {
	var out bytes.Buffer
	conn := startServer(t, "default", &out)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := reflectionpb.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(ctx)
	if err != nil {
		t.Fatalf("ServerReflectionInfo: %v", err)
	}

	if err := stream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	list := resp.GetListServicesResponse()
	if list == nil {
		t.Fatalf("expected ListServicesResponse, got %#v", resp.GetMessageResponse())
	}

	var found bool
	for _, svc := range list.GetService() {
		if svc.GetName() == "trino.gateway.v1.TrinoGatewayRouter" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("TrinoGatewayRouter not in reflection ListServices: %v", list.GetService())
	}

	if err := stream.CloseSend(); err != nil {
		t.Errorf("CloseSend: %v", err)
	}
}
