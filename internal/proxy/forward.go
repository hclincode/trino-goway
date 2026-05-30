package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/hclincode/trino-goway/internal/routing"
)

// handleStatement handles POST /v1/statement.
// The full upstream response is buffered so the queryId can be extracted and cached
// before the response is written to the client. Hard Invariant #3.
func (p *Proxy) handleStatement(w http.ResponseWriter, r *http.Request) {
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		p.log.Error("proxy: forward: read request body", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	input := routing.NewRouteInput(r, string(reqBody))

	result, err := p.router.Route(r.Context(), input)
	if err != nil || result.BackendURL == "" {
		p.log.Error("proxy: forward: no backend", "err", err)
		http.Error(w, "no backend available", http.StatusBadGateway)
		return
	}

	if p.cfg.Proxy.PropagateErrors && len(result.Errors) > 0 {
		http.Error(w, result.Errors[0], http.StatusBadRequest)
		return
	}

	upReq := p.buildUpstreamRequest(r.Context(), result.BackendURL, r, bytes.NewReader(reqBody))
	p.injectHeaders(upReq, r, result)

	upResp, err := p.client.Do(upReq)
	if err != nil {
		p.log.Error("proxy: forward: upstream request", "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// Buffer upstream response (bounded by responseSize).
	// +1 so we can detect an oversized body without reading the entire thing.
	limit := p.cfg.Proxy.ResponseSize.Bytes
	buf, err := io.ReadAll(io.LimitReader(upResp.Body, limit+1))
	if err != nil {
		p.log.Error("proxy: forward: read upstream body", "err", err)
		http.Error(w, "upstream read error", http.StatusBadGateway)
		return
	}
	if int64(len(buf)) > limit {
		p.log.Error("proxy: oversized /v1/statement response",
			"limit", limit, "backendURL", result.BackendURL)
		http.Error(w, "upstream response too large", http.StatusBadGateway)
		return
	}

	// Hard Invariant #3: write cache before writing response body to client.
	if queryID := extractQueryIDFromBody(buf); queryID != "" {
		p.router.WriteCache(queryID, result.BackendURL)
		if p.history != nil {
			userName := r.Header.Get("X-Trino-User")
			source := r.Header.Get("X-Trino-Source")
			if err := p.history.Insert(r.Context(), queryID, result.BackendURL, userName, source); err != nil {
				p.log.Warn("proxy: forward: record history", "err", err, "queryId", queryID)
			}
		}
	}

	copyHeaders(w.Header(), upResp.Header)
	if !p.applyCookies(w, r, result.BackendURL) {
		http.Error(w, "invalid gateway cookie", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(upResp.StatusCode)
	_, _ = w.Write(buf)
}

// handleStream handles all paths other than POST /v1/statement.
// Body is piped directly — zero buffering.
func (p *Proxy) handleStream(w http.ResponseWriter, r *http.Request) {
	input := routing.NewRouteInput(r, "")

	result, err := p.router.Route(r.Context(), input)
	if err != nil || result.BackendURL == "" {
		p.log.Error("proxy: stream: no backend", "err", err)
		http.Error(w, "no backend available", http.StatusBadGateway)
		return
	}

	upReq := p.buildUpstreamRequest(r.Context(), result.BackendURL, r, r.Body)
	p.injectHeaders(upReq, r, result)

	upResp, err := p.client.Do(upReq)
	if err != nil {
		p.log.Error("proxy: stream: upstream request", "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	copyHeaders(w.Header(), upResp.Header)
	if !p.applyCookies(w, r, result.BackendURL) {
		http.Error(w, "invalid gateway cookie", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(upResp.StatusCode)
	_, _ = io.Copy(w, upResp.Body)
}

// buildUpstreamRequest constructs the outbound request to the backend.
// Hop-by-hop headers are stripped; all other headers are copied.
func (p *Proxy) buildUpstreamRequest(ctx context.Context, backendURL string, r *http.Request, body io.Reader) *http.Request {
	target, _ := url.Parse(backendURL)
	upURL := *target
	upURL.Path = r.URL.Path
	upURL.RawQuery = r.URL.RawQuery

	upReq, _ := http.NewRequestWithContext(ctx, r.Method, upURL.String(), body)
	for k, vv := range r.Header {
		if !isHopByHop(k) {
			upReq.Header[k] = vv
		}
	}
	return upReq
}

// trinoStatementResponse is used only for queryId extraction.
type trinoStatementResponse struct {
	ID      string `json:"id"`
	NextURI string `json:"nextUri"`
}

// extractQueryIDFromBody parses the queryId from a /v1/statement JSON response.
// Returns "" if the field is absent or malformed.
// Hard Invariant #1: only reads; never rewrites the body.
func extractQueryIDFromBody(body []byte) string {
	var resp trinoStatementResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	return resp.ID
}
