package admin

import "net/http"

// handleLivez always returns 200 "ok".
// GET /trino-gateway/livez
func (a *Admin) handleLivez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz returns 200 "ok" once SetReady has been called, 503 otherwise.
// GET /trino-gateway/readyz
func (a *Admin) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !a.ready.Load() {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
