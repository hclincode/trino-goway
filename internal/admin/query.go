package admin

import (
	"net/http"

	"github.com/hclincode/trino-goway/internal/auth"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// QueryDetail is the wire format for query history records.
type QueryDetail struct {
	QueryID      string `json:"queryId"`
	QueryText    string `json:"queryText"`
	User         string `json:"user"`
	Source       string `json:"source"`
	BackendURL   string `json:"backendUrl"`
	CaptureTime  int64  `json:"captureTime"`  // epoch milliseconds
	RoutingGroup string `json:"routingGroup"`
	ExternalURL  string `json:"externalUrl"`
}

// queryDetailFromRecord converts a persistence.QueryRecord to a QueryDetail.
func queryDetailFromRecord(r persistence.QueryRecord) QueryDetail {
	return QueryDetail{
		QueryID:      r.QueryID,
		QueryText:    r.QueryText,
		User:         r.UserName,
		Source:       r.Source,
		BackendURL:   r.BackendURL,
		CaptureTime:  r.CreatedAt.UnixMilli(),
		RoutingGroup: r.RoutingGroup,
	}
}

// queryHistory returns recent query history.
// Non-ADMIN callers are scoped to their own username.
// GET /trino-gateway/api/queryHistory
func (a *Admin) queryHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := auth.FromContext(ctx)

	var records []persistence.QueryRecord
	var err error

	isAdmin := auth.HasRole(p, auth.RoleAdmin, a.cfg.Auth.Authorization)
	if isAdmin {
		records, err = a.cfg.History.ListRecent(ctx, 100)
	} else {
		userName := ""
		if p != nil {
			userName = p.Name
		}
		records, _, err = a.cfg.History.FindByFilter(ctx, persistence.HistoryFilter{
			UserName: userName,
			PageSize: 100,
			Page:     1,
		})
	}
	if err != nil {
		a.cfg.Log.Error("admin: query history", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	result := make([]QueryDetail, 0, len(records))
	for _, rec := range records {
		result = append(result, queryDetailFromRecord(rec))
	}
	writeJSON(w, http.StatusOK, result)
}

// legacyActiveBackends returns active backends in the legacy endpoint format.
// GET /trino-gateway/api/activeBackends
func (a *Admin) legacyActiveBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := a.cfg.Backends.ListActive(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: legacy active backends", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]ProxyBackend, 0, len(backends))
	for _, b := range backends {
		result = append(result, proxyBackendFromPersistence(b))
	}
	writeJSON(w, http.StatusOK, result)
}

// queryHistoryDistribution returns a map of backendURL → query count.
// Non-ADMIN callers are scoped to their own queries.
// GET /trino-gateway/api/queryHistoryDistribution
func (a *Admin) queryHistoryDistribution(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := auth.FromContext(ctx)

	isAdmin := auth.HasRole(p, auth.RoleAdmin, a.cfg.Auth.Authorization)

	var filter persistence.HistoryFilter
	filter.PageSize = 10000
	filter.Page = 1
	if !isAdmin && p != nil {
		filter.UserName = p.Name
	}

	records, _, err := a.cfg.History.FindByFilter(ctx, filter)
	if err != nil {
		a.cfg.Log.Error("admin: query history distribution", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	dist := make(map[string]int64)
	for _, rec := range records {
		dist[rec.BackendURL]++
	}
	writeJSON(w, http.StatusOK, dist)
}
