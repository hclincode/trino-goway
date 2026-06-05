package admin_test

import (
	"net/http"
	"testing"

	"github.com/hclincode/trino-goway/internal/admin"
	"github.com/hclincode/trino-goway/internal/metrics"
)

func TestAdmin_MetricsRoute(t *testing.T) {
	bs := newFakeBackendStore()
	hs := &fakeHistoryStore{}
	sp := newFakeStatusProvider()

	t.Run("enabled serves 200 OpenMetrics", func(t *testing.T) {
		reg := metrics.New()
		cfg := adminCfgNoAuth(bs, hs, sp)
		cfg.Metrics = admin.MetricsConfig{
			Enabled: true,
			Path:    "/metrics",
			Handler: reg.Handler(),
		}
		a := admin.New(cfg)

		rec := do(a, http.MethodGet, "/metrics", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("metrics enabled: want 200, got %d", rec.Code)
		}
	})

	t.Run("disabled returns 404", func(t *testing.T) {
		reg := metrics.New()
		cfg := adminCfgNoAuth(bs, hs, sp)
		cfg.Metrics = admin.MetricsConfig{
			Enabled: false,
			Path:    "/metrics",
			Handler: reg.Handler(),
		}
		a := admin.New(cfg)

		rec := do(a, http.MethodGet, "/metrics", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("metrics disabled: want 404, got %d", rec.Code)
		}
	})

	t.Run("nil handler returns 404 even when enabled", func(t *testing.T) {
		cfg := adminCfgNoAuth(bs, hs, sp)
		cfg.Metrics = admin.MetricsConfig{
			Enabled: true,
			Path:    "/metrics",
			Handler: nil,
		}
		a := admin.New(cfg)

		rec := do(a, http.MethodGet, "/metrics", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("metrics nil handler: want 404, got %d", rec.Code)
		}
	})
}
