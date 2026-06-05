package server_test

import (
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/hclincode/trino-goway-routing-service/internal/metrics"
)

// gatherCounter returns the value of the named counter (summed across series
// that match the given labels) from m's registry. With nil labels it sums all
// series of the metric.
func gatherCounter(t *testing.T, m *metrics.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			if !labelsMatch(metric, labels) {
				continue
			}
			switch {
			case metric.GetCounter() != nil:
				total += metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				total += metric.GetGauge().GetValue()
			}
		}
	}
	return total
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
