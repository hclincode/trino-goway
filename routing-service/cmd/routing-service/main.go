// Command routing-service is the standalone gRPC routing service for trino-goway.
// It implements the TrinoGatewayRouter contract and selects routing groups for
// new Trino query submissions.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/server"
)

func main() {
	configPath := flag.String("config", "", "path to config YAML file (required)")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "routing-service: --config is required")
		os.Exit(1)
	}

	level := parseLogLevel(*logLevel)
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("routing-service: config load failed", "err", err)
		os.Exit(1)
	}

	log.Info("routing-service: starting",
		"addr", cfg.Addr,
		"metricsAddr", cfg.MetricsAddr,
		"defaultGroup", cfg.DefaultRoutingGroup,
		"methodCount", len(cfg.Methods),
	)

	// RS-2 stub: use the stub evaluator. RS-3 wires the real pipeline.
	eval := server.NewStubEvaluator(nil)
	srv := server.New(cfg, eval, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		log.Error("routing-service: server error", "err", err)
		os.Exit(1)
	}
	log.Info("routing-service: stopped")
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
