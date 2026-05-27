package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	tests := []struct {
		name           string
		group          string
		path           string
		body           string
		wantGroup      string
		wantInOutput   string
		wantPrettyJSON bool
	}{
		{
			name:           "valid JSON body is pretty-printed",
			group:          "default",
			path:           "/route",
			body:           `{"method":"POST","requestURI":"/v1/statement"}`,
			wantGroup:      "default",
			wantInOutput:   "\"method\": \"POST\"",
			wantPrettyJSON: true,
		},
		{
			name:         "non-JSON body returns 200 with raw output",
			group:        "default",
			path:         "/route",
			body:         "not json at all",
			wantGroup:    "default",
			wantInOutput: "not json at all",
		},
		{
			name:         "group flag wires through to response",
			group:        "analytics",
			path:         "/route",
			body:         `{"x":1}`,
			wantGroup:    "analytics",
			wantInOutput: "\"x\": 1",
		},
		{
			name:         "arbitrary path is accepted",
			group:        "default",
			path:         "/some/other/path",
			body:         `{}`,
			wantGroup:    "default",
			wantInOutput: "POST /some/other/path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			handler, err := newHandler(tc.group, &out)
			if err != nil {
				t.Fatalf("newHandler: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type: got %q, want application/json", got)
			}

			var resp struct {
				RoutingGroup    string            `json:"routingGroup"`
				Errors          []string          `json:"errors"`
				ExternalHeaders map[string]string `json:"externalHeaders"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v (body=%q)", err, rec.Body.String())
			}
			if resp.RoutingGroup != tc.wantGroup {
				t.Errorf("routingGroup: got %q, want %q", resp.RoutingGroup, tc.wantGroup)
			}
			if resp.Errors == nil || len(resp.Errors) != 0 {
				t.Errorf("errors: got %#v, want []", resp.Errors)
			}
			if resp.ExternalHeaders == nil || len(resp.ExternalHeaders) != 0 {
				t.Errorf("externalHeaders: got %#v, want {}", resp.ExternalHeaders)
			}

			if !strings.Contains(out.String(), tc.wantInOutput) {
				t.Errorf("output missing %q\nfull output:\n%s", tc.wantInOutput, out.String())
			}
		})
	}
}

func TestHandlerViaHTTPTestServer(t *testing.T) {
	var out bytes.Buffer
	handler, err := newHandler("analytics", &out)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/anything", "application/json", strings.NewReader(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `"routingGroup":"analytics"`) {
		t.Errorf("body missing routing group: %s", body)
	}
}
