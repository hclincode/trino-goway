package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hclincode/trino-goway/internal/clusterstats"
	"github.com/hclincode/trino-goway/internal/monitor"
	"github.com/hclincode/trino-goway/internal/persistence"
)

// ProxyBackend is the wire format for backend objects in REST responses.
type ProxyBackend struct {
	Name         string `json:"name"`
	ProxyTo      string `json:"proxyTo"`
	ExternalURL  string `json:"externalUrl"`
	Active       bool   `json:"active"`
	RoutingGroup string `json:"routingGroup"`
}

// BackendResponse extends ProxyBackend with live cluster stats.
type BackendResponse struct {
	ProxyBackend
	Queued  int    `json:"queued"`
	Running int    `json:"running"`
	Status  string `json:"status"` // "HEALTHY", "UNHEALTHY", "PENDING"
}

// ClusterStatsResponse is the M7 public backend-state wire shape, matching Java's
// ClusterStats record field names exactly. Emitted by GET
// /api/public/backends/{name}/state (UC-ADM-14).
//
// userQueuedCount carries ,omitempty so an uncollected backend (UI_API not in
// use, or not-yet-collected) emits null/absent rather than {}, matching Java's
// null default (choice b).
type ClusterStatsResponse struct {
	ClusterID         string         `json:"clusterId"`
	RunningQueryCount int            `json:"runningQueryCount"`
	QueuedQueryCount  int            `json:"queuedQueryCount"`
	NumWorkerNodes    int            `json:"numWorkerNodes"`
	TrinoStatus       string         `json:"trinoStatus"`
	ProxyTo           string         `json:"proxyTo"`
	ExternalURL       string         `json:"externalUrl"`
	RoutingGroup      string         `json:"routingGroup"`
	UserQueuedCount   map[string]int `json:"userQueuedCount,omitempty"`
}

// clusterStatsResponseFromPersistence maps a persistence.Backend plus a collected
// ClusterStats to the M7 wire shape. Counts/worker-count/per-user breakdown come
// from cs; trinoStatus comes from the live monitor verdict; proxyTo/externalUrl/
// routingGroup are populated from persistence (choice b) — even for an
// uncollected backend whose cs carries empty identity fields.
func (a *Admin) clusterStatsResponseFromPersistence(b persistence.Backend, cs clusterstats.ClusterStats) ClusterStatsResponse {
	status := a.cfg.Monitor.Status(b.URL)
	return ClusterStatsResponse{
		ClusterID:         b.Name,
		RunningQueryCount: cs.RunningQueryCount,
		QueuedQueryCount:  cs.QueuedQueryCount,
		NumWorkerNodes:    cs.NumWorkerNodes,
		TrinoStatus:       trinoStatusLabel(status),
		ProxyTo:           b.URL,
		ExternalURL:       externalURLOrProxyTo(b.ExternalURL, b.URL),
		RoutingGroup:      b.RoutingGroup,
		UserQueuedCount:   cs.UserQueuedCount,
	}
}

// proxyBackendFromPersistence maps a persistence.Backend to a ProxyBackend.
// externalUrl falls back to the proxyTo URL when unset, matching Java's
// ProxyBackendConfiguration.getExternalUrl().
func proxyBackendFromPersistence(b persistence.Backend) ProxyBackend {
	return ProxyBackend{
		Name:         b.Name,
		ProxyTo:      b.URL,
		ExternalURL:  externalURLOrProxyTo(b.ExternalURL, b.URL),
		Active:       b.Active,
		RoutingGroup: b.RoutingGroup,
	}
}

// externalURLOrProxyTo returns externalURL when set, otherwise proxyTo.
// Java's getExternalUrl() returns getProxyTo() when externalUrl is null, so the
// field is always populated on the wire.
func externalURLOrProxyTo(externalURL, proxyTo string) string {
	if externalURL == "" {
		return proxyTo
	}
	return externalURL
}

// backendResponseFromPersistence maps a persistence.Backend to a BackendResponse
// with live status and live queued/running counts. Counts come from the stats
// store keyed by backend NAME when a StatsProvider is configured; otherwise they
// are 0 (INFO_API parity).
func (a *Admin) backendResponseFromPersistence(b persistence.Backend) BackendResponse {
	status := a.cfg.Monitor.Status(b.URL)
	resp := BackendResponse{
		ProxyBackend: proxyBackendFromPersistence(b),
		Status:       trinoStatusLabel(status),
	}
	if a.cfg.Stats != nil {
		cs := a.cfg.Stats.Stats(b.Name)
		resp.Queued = cs.QueuedQueryCount
		resp.Running = cs.RunningQueryCount
	}
	return resp
}

// trinoStatusLabel converts a TrinoStatus to the admin wire string, delegating to
// the shared enum so Unknown maps to "UNKNOWN" (no longer collapsed to PENDING).
func trinoStatusLabel(s monitor.TrinoStatus) string {
	return s.Label()
}

// persistenceBackendFromProxy maps a ProxyBackend to a persistence.Backend, preserving timestamps.
func persistenceBackendFromProxy(pb ProxyBackend) persistence.Backend {
	now := time.Now().UTC()
	return persistence.Backend{
		Name:         pb.Name,
		URL:          pb.ProxyTo,
		ExternalURL:  pb.ExternalURL,
		RoutingGroup: pb.RoutingGroup,
		Active:       pb.Active,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// decodeJSON decodes the request body into v.
func decodeJSON(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("admin: read body: %w", err)
	}
	return json.Unmarshal(body, v)
}

// ---- /api/public/* endpoints (no auth) ----

// listPublicBackends returns all active backends as ProxyBackend array.
// GET /api/public/backends
func (a *Admin) listPublicBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := a.cfg.Backends.List(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: list public backends", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]ProxyBackend, 0, len(backends))
	for _, b := range backends {
		result = append(result, proxyBackendFromPersistence(b))
	}
	writeJSON(w, http.StatusOK, result)
}

// getPublicBackend returns a single backend by name.
// GET /api/public/backends/{name}
func (a *Admin) getPublicBackend(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	backends, err := a.cfg.Backends.List(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: get public backend", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	for _, b := range backends {
		if b.Name == name {
			writeJSON(w, http.StatusOK, proxyBackendFromPersistence(b))
			return
		}
	}
	http.NotFound(w, r)
}

// getPublicBackendState returns the M7 ClusterStats wire shape for a backend
// (UC-ADM-14). Counts are live under UI_API/METRICS collectors and 0 under
// INFO_API; proxyTo/externalUrl/routingGroup are always populated from
// persistence, even for a not-yet-collected backend (choice b). 404 on missing.
// GET /api/public/backends/{name}/state
func (a *Admin) getPublicBackendState(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	backends, err := a.cfg.Backends.List(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: get public backend state", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	for _, b := range backends {
		if b.Name == name {
			var cs clusterstats.ClusterStats
			if a.cfg.Stats != nil {
				cs = a.cfg.Stats.Stats(b.Name)
			}
			resp := a.clusterStatsResponseFromPersistence(b, cs)
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	http.NotFound(w, r)
}

// ---- /gateway/* endpoints (API role) ----

// handleGatewayPing returns a plain "ok" response.
// GET /gateway
func (a *Admin) handleGatewayPing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, "ok")
}

// listAllBackends returns all backends.
// GET /gateway/backend/all
func (a *Admin) listAllBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := a.cfg.Backends.List(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: list all backends", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]ProxyBackend, 0, len(backends))
	for _, b := range backends {
		result = append(result, proxyBackendFromPersistence(b))
	}
	writeJSON(w, http.StatusOK, result)
}

// listActiveBackends returns only active backends.
// GET /gateway/backend/active
func (a *Admin) listActiveBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := a.cfg.Backends.ListActive(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: list active backends", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]ProxyBackend, 0, len(backends))
	for _, b := range backends {
		result = append(result, proxyBackendFromPersistence(b))
	}
	writeJSON(w, http.StatusOK, result)
}

// activateBackend sets a backend to active.
// POST /gateway/backend/activate/{name}
func (a *Admin) activateBackend(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := a.cfg.Backends.SetActive(r.Context(), name, true); err != nil {
		a.cfg.Log.Error("admin: activate backend", "name", name, "err", err)
		http.Error(w, fmt.Sprintf("backend %q not found or error", name), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// deactivateBackend sets a backend to inactive.
// POST /gateway/backend/deactivate/{name}
func (a *Admin) deactivateBackend(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := a.cfg.Backends.SetActive(r.Context(), name, false); err != nil {
		a.cfg.Log.Error("admin: deactivate backend", "name", name, "err", err)
		http.Error(w, fmt.Sprintf("backend %q not found or error", name), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// addBackend inserts a new backend.
// POST /gateway/backend/modify/add
func (a *Admin) addBackend(w http.ResponseWriter, r *http.Request) {
	var pb ProxyBackend
	if err := decodeJSON(r, &pb); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b := persistenceBackendFromProxy(pb)
	if err := a.cfg.Backends.Upsert(r.Context(), b); err != nil {
		a.cfg.Log.Error("admin: add backend", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pb)
}

// updateBackend updates an existing backend.
// POST /gateway/backend/modify/update
func (a *Admin) updateBackend(w http.ResponseWriter, r *http.Request) {
	var pb ProxyBackend
	if err := decodeJSON(r, &pb); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b := persistenceBackendFromProxy(pb)
	if err := a.cfg.Backends.Upsert(r.Context(), b); err != nil {
		a.cfg.Log.Error("admin: update backend", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pb)
}

// deleteBackend removes a backend by name (body is raw string, not JSON).
// POST /gateway/backend/modify/delete
func (a *Admin) deleteBackend(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<10))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(string(body))
	if name == "" {
		http.Error(w, "bad request: empty name", http.StatusBadRequest)
		return
	}
	if err := a.cfg.Backends.Delete(r.Context(), name); err != nil {
		a.cfg.Log.Error("admin: delete backend", "name", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- /entity/* endpoints (ADMIN role) ----

// listEntityTypes returns the supported entity types.
// GET /entity
func (a *Admin) listEntityTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []string{"GATEWAY_BACKEND"})
}

// upsertEntity upserts an entity and updates the in-memory monitor state.
// POST /entity?entityType=GATEWAY_BACKEND
func (a *Admin) upsertEntity(w http.ResponseWriter, r *http.Request) {
	entityType := r.URL.Query().Get("entityType")
	if entityType != "GATEWAY_BACKEND" {
		http.Error(w, fmt.Sprintf("unknown entity type: %q", entityType), http.StatusInternalServerError)
		return
	}

	var pb ProxyBackend
	if err := decodeJSON(r, &pb); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	b := persistenceBackendFromProxy(pb)
	if err := a.cfg.Backends.Upsert(r.Context(), b); err != nil {
		a.cfg.Log.Error("admin: upsert entity", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Update in-memory monitor state immediately.
	if a.cfg.StatusMut != nil {
		if pb.Active {
			a.cfg.StatusMut.SetBackendStatus(pb.ProxyTo, monitor.StatusPending)
		} else {
			a.cfg.StatusMut.SetBackendStatus(pb.ProxyTo, monitor.StatusUnhealthy)
		}
	}

	writeJSON(w, http.StatusOK, pb)
}

// listEntities returns all entities of the given type.
// GET /entity/{entityType}
func (a *Admin) listEntities(w http.ResponseWriter, r *http.Request) {
	entityType := chi.URLParam(r, "entityType")
	if entityType != "GATEWAY_BACKEND" {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	backends, err := a.cfg.Backends.List(r.Context())
	if err != nil {
		a.cfg.Log.Error("admin: list entities", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]ProxyBackend, 0, len(backends))
	for _, b := range backends {
		result = append(result, proxyBackendFromPersistence(b))
	}
	writeJSON(w, http.StatusOK, result)
}
