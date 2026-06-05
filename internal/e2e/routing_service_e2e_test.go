//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hclincode/trino-goway/internal/e2e/harness"
	"github.com/hclincode/trino-goway/internal/e2e/harness/routingadmin"
)

// routingServiceExprProgram is the inline expr routing rule exercised by these
// tests. It mirrors the PRD §6.2 worked example (a subset):
//
//   - X-Trino-Source: airflow              => "etl"
//   - X-Trino-Client-Tags: tier=premium    => "premium"
//   - otherwise                            => "" (defer; service applies its
//     defaultRoutingGroup, here "default")
const routingServiceExprProgram = `request.source == "airflow" ? "etl"
  : "tier=premium" in request.client_tags ? "premium"
  : ""`

// routingServiceExternalTimeout is the gateway's routing.external.timeout for
// these tests. It is generous (relative to the harness 500ms default) so the
// first gRPC call — which establishes the lazy grpc.NewClient connection to the
// freshly-launched routing-service subprocess — does not time out and fall back.
// It is sized to absorb CPU/IO scheduling spikes when several heavy E2E cases
// (each a Postgres container + multiple subprocesses) run back-to-back; a
// too-tight value makes the non-default-routing assertions flaky under load.
const routingServiceExternalTimeout = 5 * time.Second

// fleet bundles the three group backends a test routes across.
type fleet struct {
	etl     *trinoFakeByGroup
	premium *trinoFakeByGroup
	deflt   *trinoFakeByGroup
}

// trinoFakeByGroup wraps a backend fake so test assertions read clearly.
type trinoFakeByGroup struct {
	fake interface{ QueryIDs() []string }
}

func (b *trinoFakeByGroup) hits() int { return len(b.fake.QueryIDs()) }

// startGatewayWithRoutingService boots a real routing-service with the shared
// expr rule and a gateway pointed at it over gRPC, registers backends for the
// etl, premium, and default groups, and warms the gateway→service gRPC path so
// the first asserted request routes deterministically.
func startGatewayWithRoutingService(t *testing.T) (*harness.Harness, *harness.RoutingService, fleet) {
	t.Helper()

	rs := harness.StartRoutingService(t, "default", harness.ExprMethod(routingServiceExprProgram))
	h := harness.New(t,
		harness.WithExternalGRPCRouter(rs.GRPCAddr),
		harness.WithExternalTimeout(routingServiceExternalTimeout),
	)

	f := fleet{
		etl:     &trinoFakeByGroup{fake: h.AddBackend(t, "etl-backend", "etl")},
		premium: &trinoFakeByGroup{fake: h.AddBackend(t, "premium-backend", "premium")},
		deflt:   &trinoFakeByGroup{fake: h.AddBackend(t, "default-backend", "default")},
	}

	warmRoutingPath(t, h, f.etl)
	return h, rs, f
}

// warmRoutingPath issues airflow-tagged requests until one lands on the etl
// backend. Because the gateway's cold-start fallback targets its defaultGroup
// ("default"), an etl hit proves the gateway↔routing-service gRPC channel is
// live and the airflow→etl rule was actually evaluated by the service — not the
// fallback. The etl backend's warm-up hits are not asserted by callers (tests
// capture per-group baselines after warm-up and assert deltas).
func warmRoutingPath(t *testing.T, h *harness.Harness, etl *trinoFakeByGroup) {
	t.Helper()
	hdr := http.Header{}
	hdr.Set("X-Trino-Source", "airflow")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		before := etl.hits()
		resp, body := postStatement(t, h, "SELECT 1", hdr)
		_ = resp.Body.Close()
		require.Equalf(t, http.StatusOK, resp.StatusCode, "warm-up status=%d body=%s", resp.StatusCode, string(body))
		if etl.hits() > before {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("routing-service: gateway→service gRPC path did not warm within 15s")
}

// TestE2E_RoutingService_SourceAirflowToETL asserts that X-Trino-Source=airflow
// is steered to the etl group by a real expr rule evaluated in the
// routing-service subprocess.
func TestE2E_RoutingService_SourceAirflowToETL(t *testing.T) {
	h, _, f := startGatewayWithRoutingService(t)

	etlBefore, premiumBefore, defaultBefore := f.etl.hits(), f.premium.hits(), f.deflt.hits()

	hdr := http.Header{}
	hdr.Set("X-Trino-Source", "airflow")
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	assert.Equal(t, 1, f.etl.hits()-etlBefore, "airflow source must route to etl backend")
	assert.Equal(t, 0, f.premium.hits()-premiumBefore, "premium backend must not receive the query")
	assert.Equal(t, 0, f.deflt.hits()-defaultBefore, "default backend must not receive the query")
}

// TestE2E_RoutingService_ClientTagPremium asserts that the client tag
// tier=premium routes to the premium group via the real expr rule.
func TestE2E_RoutingService_ClientTagPremium(t *testing.T) {
	h, _, f := startGatewayWithRoutingService(t)

	etlBefore, premiumBefore, defaultBefore := f.etl.hits(), f.premium.hits(), f.deflt.hits()

	hdr := http.Header{}
	hdr.Set("X-Trino-Client-Tags", "tier=premium,region=us")
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	assert.Equal(t, 1, f.premium.hits()-premiumBefore, "tier=premium tag must route to premium backend")
	assert.Equal(t, 0, f.etl.hits()-etlBefore, "etl backend must not receive the query")
	assert.Equal(t, 0, f.deflt.hits()-defaultBefore, "default backend must not receive the query")
}

// TestE2E_RoutingService_DeferToDefault asserts that a request matching no rule
// (the expr program returns "") defers to the routing-service's
// defaultRoutingGroup, which the gateway honours by routing to the default
// group.
func TestE2E_RoutingService_DeferToDefault(t *testing.T) {
	h, _, f := startGatewayWithRoutingService(t)

	etlBefore, premiumBefore, defaultBefore := f.etl.hits(), f.premium.hits(), f.deflt.hits()

	// No X-Trino-Source / tags => expr defers => service default "default".
	resp, body := postStatement(t, h, "SELECT 1", nil)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	assert.Equal(t, 1, f.deflt.hits()-defaultBefore, "unmatched request must route to default backend")
	assert.Equal(t, 0, f.etl.hits()-etlBefore)
	assert.Equal(t, 0, f.premium.hits()-premiumBefore)
}

// TestE2E_RoutingService_DownFallsBackToDefaultGroup asserts that when the
// routing-service is unreachable, the gateway falls back to its own
// routing.defaultGroup ("default") and no request is dropped.
func TestE2E_RoutingService_DownFallsBackToDefaultGroup(t *testing.T) {
	rs := harness.StartRoutingService(t, "default", harness.ExprMethod(routingServiceExprProgram))
	h := harness.New(t,
		harness.WithExternalGRPCRouter(rs.GRPCAddr),
		harness.WithExternalTimeout(routingServiceExternalTimeout),
	)
	deflt := h.AddBackend(t, "default-backend", "default")

	// Kill the routing-service so the gateway's gRPC dial fails on Route.
	rs.Stop(t)

	// A request that WOULD have matched the etl rule must still succeed via the
	// gateway's defaultGroup fallback (the routing service is down). There is no
	// etl backend registered, so a non-fallback route would 502; success proves
	// the request landed on the default backend.
	hdr := http.Header{}
	hdr.Set("X-Trino-Source", "airflow")
	resp, body := postStatement(t, h, "SELECT 1", hdr)
	defer resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"routing-service down must fall back to defaultGroup, got status=%d body=%s",
		resp.StatusCode, string(body))

	assert.Equal(t, 1, len(deflt.QueryIDs()), "request must land on the default backend when routing-service is down")
}

// TestE2E_RoutingService_KillSwitchChangesRouting asserts that disabling the
// expr method via the RoutingServiceAdmin kill-switch takes effect on the next
// request: an airflow request first routes to etl, and after DisableMethod the
// same request defers to the service default ("default").
func TestE2E_RoutingService_KillSwitchChangesRouting(t *testing.T) {
	h, rs, f := startGatewayWithRoutingService(t)

	hdr := http.Header{}
	hdr.Set("X-Trino-Source", "airflow")

	etlBefore, defaultBefore := f.etl.hits(), f.deflt.hits()

	// Before the kill-switch: airflow => etl.
	resp1, body1 := postStatement(t, h, "SELECT 1", hdr)
	defer resp1.Body.Close()
	require.Equalf(t, http.StatusOK, resp1.StatusCode, "status=%d body=%s", resp1.StatusCode, string(body1))
	require.Equal(t, 1, f.etl.hits()-etlBefore, "pre-kill-switch: airflow must route to etl")

	// Disable the expr method. The pipeline now has no methods, so every request
	// defers to the service defaultRoutingGroup ("default").
	rs.DisableMethod(t, "expr")
	requireMethodDisabled(t, rs, "expr")

	// After the kill-switch: the same airflow request defers to default.
	resp2, body2 := postStatement(t, h, "SELECT 1", hdr)
	defer resp2.Body.Close()
	require.Equalf(t, http.StatusOK, resp2.StatusCode, "status=%d body=%s", resp2.StatusCode, string(body2))

	assert.Equal(t, 1, f.etl.hits()-etlBefore, "post-kill-switch: etl must not receive a second query")
	assert.Equal(t, 1, f.deflt.hits()-defaultBefore, "post-kill-switch: airflow must defer to default")
}

// requireMethodDisabled polls ListDisabled until the named method appears, so
// the subsequent request observes the kill-switch state deterministically.
func requireMethodDisabled(t *testing.T, rs *harness.RoutingService, methodType string) {
	t.Helper()
	client := rs.AdminClient(t)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		resp, err := client.ListDisabled(ctx, &routingadmin.ListDisabledRequest{})
		cancel()
		require.NoError(t, err, "ListDisabled")
		if contains(resp.GetDisabled(), methodType) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("routing-service: method %q not reported disabled within 5s", methodType)
}
