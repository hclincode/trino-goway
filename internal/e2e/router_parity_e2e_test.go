//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
)

// TestE2E_RouterParity_MockVsRealService asserts that the gateway treats any
// conformant TrinoGatewayRouter interchangeably: for an equivalent rule, the
// same request routed through cmd/mock-external-router-grpc (a fixed-group mock)
// and through the real routing-service (a real expr rule) yields the identical
// routing group.
//
// The "equivalent rule" is the airflow→etl mapping. The mock is configured to
// return "etl" unconditionally; the real service is configured with the expr
// rule `request.source == "airflow" ? "etl" : ""`. An X-Trino-Source=airflow
// request must therefore land on the etl backend in BOTH setups — proving the
// gateway's routing outcome is independent of which conformant gRPC router
// serves it.
func TestE2E_RouterParity_MockVsRealService(t *testing.T) {
	const wantGroup = "etl"

	hdr := http.Header{}
	hdr.Set("X-Trino-Source", "airflow")

	// --- Path A: gateway → mock router (fixed group "etl") -------------------
	mockGroup := routeThroughMock(t, wantGroup, hdr)

	// --- Path B: gateway → real routing-service (airflow→etl expr rule) ------
	realGroup := routeThroughRealService(t, hdr)

	// Parity: both conformant routers steer the identical request to the same
	// group, so the gateway's observable routing outcome matches.
	assert.Equal(t, wantGroup, mockGroup, "mock router must route airflow→etl")
	assert.Equal(t, wantGroup, realGroup, "real routing-service must route airflow→etl")
	assert.Equal(t, mockGroup, realGroup,
		"gateway must treat mock and real routers interchangeably (identical routingGroup)")
}

// routeThroughMock boots a gateway pointed at the fixed-group mock router,
// registers etl + default backends, and returns the group whose backend
// received the request ("etl" or "default").
func routeThroughMock(t *testing.T, fixedGroup string, hdr http.Header) string {
	t.Helper()

	mock := harness.StartMockGRPCRouter(t, fixedGroup)
	h := harness.New(t,
		harness.WithExternalGRPCRouter(mock.GRPCAddr),
		harness.WithExternalTimeout(routingServiceExternalTimeout),
	)
	etl := &trinoFakeByGroup{fake: h.AddBackend(t, "etl-backend", "etl")}
	deflt := &trinoFakeByGroup{fake: h.AddBackend(t, "default-backend", "default")}

	// Warm the lazy gRPC channel: the mock always returns etl, so an etl hit
	// proves the gateway↔mock path is live (the cold-start fallback would target
	// the gateway defaultGroup "default", not "etl").
	warmRoutingPath(t, h, etl)

	etlBefore := etl.hits()
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "mock path status=%d body=%s", resp.StatusCode, string(body))

	return whichGroupGotHit(etl, etlBefore, deflt)
}

// routeThroughRealService boots a gateway pointed at the real routing-service
// (airflow→etl expr rule), registers etl + default backends, and returns the
// group whose backend received the request.
func routeThroughRealService(t *testing.T, hdr http.Header) string {
	t.Helper()

	rs := harness.StartRoutingService(t, "default", harness.ExprMethod(routingServiceExprProgram))
	h := harness.New(t,
		harness.WithExternalGRPCRouter(rs.GRPCAddr),
		harness.WithExternalTimeout(routingServiceExternalTimeout),
	)
	etl := &trinoFakeByGroup{fake: h.AddBackend(t, "etl-backend", "etl")}
	deflt := &trinoFakeByGroup{fake: h.AddBackend(t, "default-backend", "default")}

	warmRoutingPath(t, h, etl)

	etlBefore := etl.hits()
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "real path status=%d body=%s", resp.StatusCode, string(body))

	return whichGroupGotHit(etl, etlBefore, deflt)
}

// whichGroupGotHit returns "etl" if the etl backend's hit count grew past
// etlBefore, otherwise "default". Used to read back the gateway's routing
// decision via which group's backend received the POST.
func whichGroupGotHit(etl *trinoFakeByGroup, etlBefore int, deflt *trinoFakeByGroup) string {
	if etl.hits() > etlBefore {
		return "etl"
	}
	if deflt.hits() > 0 {
		return "default"
	}
	return ""
}
