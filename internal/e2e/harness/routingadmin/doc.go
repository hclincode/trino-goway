// Package routingadmin holds the RoutingServiceAdmin gRPC client stubs used by
// the E2E suite to drive the routing-service kill-switch (DisableMethod /
// EnableMethod / ListDisabled).
//
// The two .pb.go files in this package are copied verbatim from the
// routing-service module's routerpb package (github.com/hclincode/
// trino-goway-routing-service, proto/admin.proto), with only the Go package
// name changed to routingadmin. They are vendored — rather than taking a
// cross-module dependency — because the admin contract lives in a separate Go
// module and the gateway must not depend on it at build time. The wire format
// (service trino.gateway.v1.RoutingServiceAdmin) is identical, so the stubs
// interoperate with the running routing-service binary.
//
// If proto/admin.proto changes upstream, regenerate there and re-copy here
// (see internal/e2e/harness/routing_service.go for the source of truth).
package routingadmin
