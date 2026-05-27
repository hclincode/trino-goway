package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/hclincode/trino-goway/internal/config"
)

// routingGroupExternalBody is the JSON request body sent to the external HTTP routing service.
// Field names match the Java RoutingGroupExternalBody record exactly (camelCase).
type routingGroupExternalBody struct {
	TrinoQueryProperties *trinoQueryProperties `json:"trinoQueryProperties"`
	TrinoRequestUser     *trinoRequestUser     `json:"trinoRequestUser"`
	ContentType          string                `json:"contentType"`
	RemoteUser           *string               `json:"remoteUser"`
	Method               string                `json:"method"`
	RequestURI           string                `json:"requestURI"`
	QueryString          *string               `json:"queryString"`
	Session              interface{}           `json:"session"`
	RemoteAddr           *string               `json:"remoteAddr"`
	RemoteHost           *string               `json:"remoteHost"`
	Parameters           map[string][]string   `json:"parameters"`
}

type trinoQueryProperties struct {
	Body                    string   `json:"body"`
	QueryType               string   `json:"queryType"`
	ResourceGroupQueryType  string   `json:"resourceGroupQueryType"`
	DefaultCatalog          *string  `json:"defaultCatalog"`
	DefaultSchema           *string  `json:"defaultSchema"`
	Catalogs                []string `json:"catalogs"`
	Schemas                 []string `json:"schemas"`
	CatalogSchemas          []string `json:"catalogSchemas"`
	Tables                  []string `json:"tables"`
	IsNewQuerySubmission    bool     `json:"isNewQuerySubmission"`
	IsQueryParsingSuccessful bool    `json:"isQueryParsingSuccessful"`
	ErrorMessage            *string  `json:"errorMessage"`
}

type trinoRequestUser struct {
	User     string  `json:"user"`
	UserInfo *string `json:"userInfo"`
}

// externalRouterResponse is the JSON response from the external HTTP routing service.
type externalRouterResponse struct {
	RoutingGroup    *string           `json:"routingGroup"`
	Errors          []string          `json:"errors"`
	ExternalHeaders map[string]string `json:"externalHeaders"`
}

// externalHTTPSelector calls the external HTTP routing endpoint to select a routing group.
type externalHTTPSelector struct {
	cfg    config.ExternalConfig
	client *http.Client
}

func newExternalHTTPSelector(cfg config.ExternalConfig, client *http.Client) *externalHTTPSelector {
	return &externalHTTPSelector{cfg: cfg, client: client}
}

// selectGroup POSTs the request metadata to the external routing URL and returns the response.
// Returns ("", nil, nil) if the URL is not configured.
// Returns ("", nil, err) on any transport or parse failure — the caller falls back to default.
func (s *externalHTTPSelector) selectGroup(ctx context.Context, req *RouteInput) (routingGroup string, externalHeaders map[string]string, errors []string, err error) {
	if s.cfg.URL == "" {
		return "", nil, nil, nil
	}

	body := buildExternalBody(req)
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, nil, fmt.Errorf("routing: http: marshal body: %w", err)
	}

	timeout := s.cfg.Timeout.D
	if timeout == 0 {
		timeout = defaultExternalTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, s.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return "", nil, nil, fmt.Errorf("routing: http: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	// Forward inbound headers to the routing service, excluding excludeHeaders and
	// Content-Length (which is invalid after body substitution).
	excluded := buildExcludeSet(s.cfg.ExcludeHeaders)
	excluded["Content-Length"] = true
	for k, vv := range req.Headers() {
		if !excluded[http.CanonicalHeaderKey(k)] {
			for _, v := range vv {
				httpReq.Header.Add(k, v)
			}
		}
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", nil, nil, fmt.Errorf("routing: http: execute: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", nil, nil, fmt.Errorf("routing: http: non-200 status %d", resp.StatusCode)
	}

	var result externalRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, nil, fmt.Errorf("routing: http: decode response: %w", err)
	}

	group := ""
	if result.RoutingGroup != nil {
		group = *result.RoutingGroup
	}
	headers := result.ExternalHeaders
	if headers == nil {
		headers = map[string]string{}
	}
	return group, headers, result.Errors, nil
}

// buildExternalBody constructs the routing request body from a RouteInput.
// SQL-analysis fields are always empty in Go v1 (no trino-parser).
func buildExternalBody(req *RouteInput) routingGroupExternalBody {
	var qp *trinoQueryProperties
	// Always populate trinoQueryProperties with best-effort v1 data.
	errMsg := "trino-parser not available in Go v1"
	catalog := req.Header("X-Trino-Catalog")
	schema := req.Header("X-Trino-Schema")
	qp = &trinoQueryProperties{
		Body:                    req.Body,
		QueryType:               "",
		ResourceGroupQueryType:  "",
		DefaultCatalog:          nilIfEmpty(catalog),
		DefaultSchema:           nilIfEmpty(schema),
		Catalogs:                []string{},
		Schemas:                 []string{},
		CatalogSchemas:          []string{},
		Tables:                  []string{},
		IsNewQuerySubmission:    req.Method == http.MethodPost,
		IsQueryParsingSuccessful: false,
		ErrorMessage:            &errMsg,
	}

	var rtu *trinoRequestUser
	if u := req.Header("X-Trino-User"); u != "" {
		rtu = &trinoRequestUser{User: u}
	}

	remoteUser := nilIfEmpty(req.RemoteUser)
	queryString := nilIfEmpty(req.QueryString)
	remoteAddr := nilIfEmpty(req.RemoteAddr)
	remoteHost := nilIfEmpty(req.RemoteHost)

	params := req.Parameters
	if params == nil {
		params = map[string][]string{}
	}

	return routingGroupExternalBody{
		TrinoQueryProperties: qp,
		TrinoRequestUser:     rtu,
		ContentType:          "application/json",
		RemoteUser:           remoteUser,
		Method:               req.Method,
		RequestURI:           req.RequestURI,
		QueryString:          queryString,
		Session:              nil,
		RemoteAddr:           remoteAddr,
		RemoteHost:           remoteHost,
		Parameters:           params,
	}
}

// buildExcludeSet converts a slice of header names to a canonical-cased lookup set.
func buildExcludeSet(headers []string) map[string]bool {
	m := make(map[string]bool, len(headers))
	for _, h := range headers {
		m[http.CanonicalHeaderKey(h)] = true
	}
	return m
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
