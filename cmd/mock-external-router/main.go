// Command mock-external-router is a development tool that mimics an external
// HTTP routing service for trino-goway. It accepts POST requests on any path,
// pretty-prints the request body, and responds with a fixed routing group.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	port := flag.Int("port", 9000, "port to listen on")
	group := flag.String("group", "default", "routing group to return in responses")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	handler, err := newHandler(*group, os.Stdout)
	if err != nil {
		logger.Error("failed to build handler", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/", handler)

	addr := fmt.Sprintf(":%d", *port)
	logger.Info("mock external router listening", "addr", addr, "group", *group)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func newHandler(group string, out io.Writer) (http.Handler, error) {
	resp := struct {
		RoutingGroup    string            `json:"routingGroup"`
		Errors          []string          `json:"errors"`
		ExternalHeaders map[string]string `json:"externalHeaders"`
	}{
		RoutingGroup:    group,
		Errors:          []string{},
		ExternalHeaders: map[string]string{},
	}
	respBody, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			fmt.Fprintf(out, "failed to read request body: %v\n", err)
			return
		}
		defer r.Body.Close()

		ts := time.Now().UTC().Format(time.RFC3339)
		fmt.Fprintf(out, "%s  %s %s\n", ts, r.Method, r.URL.Path)

		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			fmt.Fprintln(out, pretty.String())
		} else {
			fmt.Fprintln(out, string(body))
		}
		fmt.Fprintln(out)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}), nil
}
