package admin

import (
	"net/http"
	"time"

	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// Result is the envelope for /webapp/* responses.
type Result[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

func resultOK[T any](data T) Result[T] {
	return Result[T]{Code: 200, Msg: "Successful.", Data: data}
}

func resultErr(msg string) Result[any] {
	return Result[any]{Code: 500, Msg: msg, Data: nil}
}

// TableData is the paginated result wrapper.
type TableData[T any] struct {
	Total int64 `json:"total"`
	Rows  []T   `json:"rows"`
}

// RoutingRule is a routing rule object.
type RoutingRule struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Priority    int      `json:"priority"`
	Actions     []string `json:"actions"`
	Condition   string   `json:"condition"`
}

// DistributionResponse carries gateway distribution stats.
type DistributionResponse struct {
	TotalBackendCount     int                    `json:"totalBackendCount"`
	OnlineBackendCount    int                    `json:"onlineBackendCount"`
	OfflineBackendCount   int                    `json:"offlineBackendCount"`
	HealthyBackendCount   int                    `json:"healthyBackendCount"`
	UnhealthyBackendCount int                    `json:"unhealthyBackendCount"`
	TotalQueryCount       int64                  `json:"totalQueryCount"`
	AvgQueryCountMinute   float64                `json:"averageQueryCountMinute"`
	AvgQueryCountSecond   float64                `json:"averageQueryCountSecond"`
	StartTime             string                 `json:"startTime"`        // ISO-8601 with ms
	DistributionChart     []ChartPoint           `json:"distributionChart"`
	LineChart             map[string][]TimePoint `json:"lineChart"`
}

// ChartPoint is one point in the distribution chart.
type ChartPoint struct {
	BackendURL string `json:"backendUrl"`
	Name       string `json:"name"`
	QueryCount int64  `json:"queryCount"`
}

// TimePoint is one sample in the line chart.
type TimePoint struct {
	EpochMillis int64  `json:"epochMillis"`
	BackendURL  string `json:"backendUrl"`
	Name        string `json:"name"`
	QueryCount  int64  `json:"queryCount"`
}

// UIConfiguration holds front-end feature flags.
type UIConfiguration struct {
	AuthType string `json:"authType"`
}

// webappGetAllBackends returns all backends with live status.
// POST /webapp/getAllBackends
func (a *Admin) webappGetAllBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := a.cfg.Backends.List(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: webapp get all backends", "err", err)
		writeJSON(w, http.StatusOK, resultErr("failed to list backends"))
		return
	}
	result := make([]BackendResponse, 0, len(backends))
	for _, b := range backends {
		result = append(result, a.backendResponseFromPersistence(b))
	}
	writeJSON(w, http.StatusOK, resultOK(result))
}

// FindQueryHistoryRequest is the request body for findQueryHistory.
type FindQueryHistoryRequest struct {
	UserName   string `json:"userName"`
	BackendURL string `json:"backendUrl"`
	QueryID    string `json:"queryId"`
	Source     string `json:"source"`
	Page       int    `json:"page"`
	PageSize   int    `json:"pageSize"`
}

// webappFindQueryHistory returns paginated query history.
// Non-ADMIN callers have their username forced.
// POST /webapp/findQueryHistory
func (a *Admin) webappFindQueryHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := auth.FromContext(ctx)

	var req FindQueryHistoryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusOK, resultErr("bad request"))
		return
	}

	isAdmin := auth.HasRole(p, auth.RoleAdmin, a.cfg.Auth.Authorization)
	if !isAdmin {
		// Force user filter for non-admin callers.
		if p != nil {
			req.UserName = p.Name
		}
	}

	filter := persistence.HistoryFilter{
		UserName:   req.UserName,
		BackendURL: req.BackendURL,
		QueryID:    req.QueryID,
		Source:     req.Source,
		Page:       req.Page,
		PageSize:   req.PageSize,
	}

	records, total, err := a.cfg.History.FindByFilter(ctx, filter)
	if err != nil {
		a.cfg.Log.Error("admin: webapp find query history", "err", err)
		writeJSON(w, http.StatusOK, resultErr("failed to query history"))
		return
	}

	rows := make([]QueryDetail, 0, len(records))
	for _, rec := range records {
		rows = append(rows, queryDetailFromRecord(rec))
	}
	writeJSON(w, http.StatusOK, resultOK(TableData[QueryDetail]{Total: total, Rows: rows}))
}

// webappGetDistribution returns distribution statistics.
// POST /webapp/getDistribution
func (a *Admin) webappGetDistribution(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	backends, err := a.cfg.Backends.List(ctx)
	if err != nil {
		a.cfg.Log.Error("admin: webapp get distribution", "err", err)
		writeJSON(w, http.StatusOK, resultErr("failed to list backends"))
		return
	}

	var total, online, offline, healthy, unhealthy int
	dist := make(map[string]int64)
	chart := make([]ChartPoint, 0, len(backends))

	for _, b := range backends {
		total++
		st := a.cfg.Monitor.Status(b.URL)
		if b.Active {
			online++
		} else {
			offline++
		}
		switch st {
		case monitor.StatusHealthy:
			healthy++
		default:
			unhealthy++
		}
		dist[b.URL] = 0
		chart = append(chart, ChartPoint{
			BackendURL: b.URL,
			Name:       b.Name,
			QueryCount: 0,
		})
	}

	// Get recent records for distribution chart counts.
	records, _, err := a.cfg.History.FindByFilter(ctx, persistence.HistoryFilter{PageSize: 10000, Page: 1})
	if err == nil {
		for _, rec := range records {
			if _, ok := dist[rec.BackendURL]; ok {
				dist[rec.BackendURL]++
			}
		}
		// Update chart counts.
		for i := range chart {
			chart[i].QueryCount = dist[chart[i].BackendURL]
		}
	}

	var totalQueryCount int64
	for _, c := range dist {
		totalQueryCount += c
	}

	uptime := time.Since(a.cfg.StartTime)
	var avgPerMinute, avgPerSecond float64
	if uptime.Seconds() > 0 {
		avgPerSecond = float64(totalQueryCount) / uptime.Seconds()
		avgPerMinute = avgPerSecond * 60
	}

	resp := DistributionResponse{
		TotalBackendCount:     total,
		OnlineBackendCount:    online,
		OfflineBackendCount:   offline,
		HealthyBackendCount:   healthy,
		UnhealthyBackendCount: unhealthy,
		TotalQueryCount:       totalQueryCount,
		AvgQueryCountMinute:   avgPerMinute,
		AvgQueryCountSecond:   avgPerSecond,
		StartTime:             a.cfg.StartTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		DistributionChart:     chart,
		LineChart:             map[string][]TimePoint{},
	}
	writeJSON(w, http.StatusOK, resultOK(resp))
}

// webappGetUIConfig returns the UI configuration.
// POST /webapp/getUIConfiguration
func (a *Admin) webappGetUIConfig(w http.ResponseWriter, r *http.Request) {
	cfg := UIConfiguration{
		AuthType: a.cfg.Auth.Type,
	}
	writeJSON(w, http.StatusOK, resultOK(cfg))
}

// webappGetRoutingRules returns routing rules (v1: always empty list).
// POST /webapp/getRoutingRules
func (a *Admin) webappGetRoutingRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, resultOK([]RoutingRule{}))
}

// webappUpdateRoutingRules updates routing rules (v1: not fully implemented).
// POST /webapp/updateRoutingRules
func (a *Admin) webappUpdateRoutingRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, resultOK([]RoutingRule{}))
}

// webappSaveBackend saves a new backend.
// POST /webapp/saveBackend
func (a *Admin) webappSaveBackend(w http.ResponseWriter, r *http.Request) {
	var pb ProxyBackend
	if err := decodeJSON(r, &pb); err != nil {
		writeJSON(w, http.StatusOK, resultErr("bad request"))
		return
	}
	b := persistenceBackendFromProxy(pb)
	if err := a.cfg.Backends.Upsert(r.Context(), b); err != nil {
		a.cfg.Log.Error("admin: webapp save backend", "err", err)
		writeJSON(w, http.StatusOK, resultErr("failed to save backend"))
		return
	}
	writeJSON(w, http.StatusOK, resultOK(pb))
}

// webappUpdateBackend updates an existing backend.
// POST /webapp/updateBackend
func (a *Admin) webappUpdateBackend(w http.ResponseWriter, r *http.Request) {
	var pb ProxyBackend
	if err := decodeJSON(r, &pb); err != nil {
		writeJSON(w, http.StatusOK, resultErr("bad request"))
		return
	}
	b := persistenceBackendFromProxy(pb)
	if err := a.cfg.Backends.Upsert(r.Context(), b); err != nil {
		a.cfg.Log.Error("admin: webapp update backend", "err", err)
		writeJSON(w, http.StatusOK, resultErr("failed to update backend"))
		return
	}
	writeJSON(w, http.StatusOK, resultOK(pb))
}

// webappDeleteBackend deletes a backend by name (takes full ProxyBackend, uses only name).
// POST /webapp/deleteBackend
func (a *Admin) webappDeleteBackend(w http.ResponseWriter, r *http.Request) {
	var pb ProxyBackend
	if err := decodeJSON(r, &pb); err != nil {
		writeJSON(w, http.StatusOK, resultErr("bad request"))
		return
	}
	if err := a.cfg.Backends.Delete(r.Context(), pb.Name); err != nil {
		a.cfg.Log.Error("admin: webapp delete backend", "name", pb.Name, "err", err)
		writeJSON(w, http.StatusOK, resultErr("failed to delete backend"))
		return
	}
	writeJSON(w, http.StatusOK, resultOK(true))
}
