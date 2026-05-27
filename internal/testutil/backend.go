package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeBackendConfig holds the configuration for FakeBackend.
type fakeBackendConfig struct {
	latency    time.Duration
	statusCode int
	body       string
	redirectTo string
	queryID    string
}

// FakeBackendOption configures a FakeBackend.
type FakeBackendOption func(*fakeBackendConfig)

// WithLatency injects artificial latency into every FakeBackend response.
func WithLatency(d time.Duration) FakeBackendOption {
	return func(c *fakeBackendConfig) {
		c.latency = d
	}
}

// WithStatusCode overrides the HTTP response status code for all responses.
func WithStatusCode(code int) FakeBackendOption {
	return func(c *fakeBackendConfig) {
		c.statusCode = code
	}
}

// WithBody overrides the response body for all responses.
func WithBody(body string) FakeBackendOption {
	return func(c *fakeBackendConfig) {
		c.body = body
	}
}

// WithRedirectTo makes the fake backend return a 302 redirect to the given URL.
func WithRedirectTo(url string) FakeBackendOption {
	return func(c *fakeBackendConfig) {
		c.redirectTo = url
	}
}

// WithQueryIDInResponse embeds the given queryID in the /v1/statement response.
func WithQueryIDInResponse(queryID string) FakeBackendOption {
	return func(c *fakeBackendConfig) {
		c.queryID = queryID
	}
}

// FakeBackend is a configurable fake Trino backend for testing.
type FakeBackend struct {
	// URL is the base URL of the fake backend server.
	URL string

	cfg    fakeBackendConfig
	mu     sync.Mutex
	reqs   []*http.Request
	server *httptest.Server
}

// NewFakeBackend creates a fake Trino backend httptest.Server.
// Options let tests inject latency, error responses, 3xx redirects, etc.
// Registers t.Cleanup to close the server.
func NewFakeBackend(t testing.TB, opts ...FakeBackendOption) *FakeBackend {
	t.Helper()

	cfg := fakeBackendConfig{
		queryID: "q_test_01",
	}
	for _, o := range opts {
		o(&cfg)
	}

	fb := &FakeBackend{cfg: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/info", fb.handleInfo)
	mux.HandleFunc("/v1/statement", fb.handleStatement)

	fb.server = httptest.NewServer(mux)
	fb.URL = fb.server.URL

	t.Cleanup(fb.server.Close)

	return fb
}

// Requests returns a snapshot of all HTTP requests received by the fake backend.
func (fb *FakeBackend) Requests() []*http.Request {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	out := make([]*http.Request, len(fb.reqs))
	copy(out, fb.reqs)
	return out
}

func (fb *FakeBackend) record(r *http.Request) {
	fb.mu.Lock()
	fb.reqs = append(fb.reqs, r)
	fb.mu.Unlock()
}

func (fb *FakeBackend) applyOptions(w http.ResponseWriter, r *http.Request) (handled bool) {
	if fb.cfg.latency > 0 {
		time.Sleep(fb.cfg.latency)
	}

	if fb.cfg.redirectTo != "" {
		http.Redirect(w, r, fb.cfg.redirectTo, http.StatusFound)
		return true
	}

	if fb.cfg.statusCode != 0 {
		if fb.cfg.body != "" {
			w.WriteHeader(fb.cfg.statusCode)
			_, _ = fmt.Fprint(w, fb.cfg.body)
		} else {
			w.WriteHeader(fb.cfg.statusCode)
		}
		return true
	}

	if fb.cfg.body != "" {
		_, _ = fmt.Fprint(w, fb.cfg.body)
		return true
	}

	return false
}

// handleInfo responds to GET /v1/info with {"starting":false}.
func (fb *FakeBackend) handleInfo(w http.ResponseWriter, r *http.Request) {
	fb.record(r)

	if handled := fb.applyOptions(w, r); handled {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{"starting":false}`)
}

// handleStatement responds to POST /v1/statement with a minimal Trino response JSON.
func (fb *FakeBackend) handleStatement(w http.ResponseWriter, r *http.Request) {
	fb.record(r)

	if handled := fb.applyOptions(w, r); handled {
		return
	}

	qid := fb.cfg.queryID
	nextURI := fmt.Sprintf("http://%s/v1/statement/%s/1", r.Host, qid)

	resp := struct {
		ID      string `json:"id"`
		NextURI string `json:"nextUri"`
	}{
		ID:      qid,
		NextURI: nextURI,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
