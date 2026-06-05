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
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
	"github.com/hclincode/trino-goway-routing-service/internal/reload"
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

	// RS-3: build the real pipeline with the configured methods.
	// No providers are registered yet (RS-4 adds expr, RS-5 adds script), so
	// the pipeline starts empty and the registry is populated as providers land.
	// An empty pipeline is valid: every request defers to cfg.DefaultRoutingGroup.
	reg := engine.NewRegistry()
	reg.Register("expr", func() engine.RoutingMethod { return exprovider.New() })
	reg.Register("script", func() engine.RoutingMethod { return scriptprovider.New() })

	var methods []engine.RoutingMethod
	for _, mc := range cfg.Methods {
		m, err := reg.Build(mc)
		if err != nil {
			log.Error("routing-service: failed to build method", "type", mc.Type, "err", err)
			os.Exit(1)
		}
		methods = append(methods, m)
	}

	pipeline := engine.NewPipeline(methods, cfg.DefaultRoutingGroup, log)
	eval := engine.NewPipelineEvaluator(pipeline)
	srv := server.New(cfg, eval, log)

	// Transition health to SERVING now that the pipeline is ready.
	srv.SetReady(pipeline.Ready())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// RS-6: hot-reload. Watch the config file and validate-before-activate on
	// every change; invalid configs keep the last-known-good pipeline live.
	watcher := reload.New(*configPath, pipeline, reg, log)
	if err := watcher.Start(ctx); err != nil {
		log.Error("routing-service: config watcher failed to start", "err", err)
		os.Exit(1)
	}
	defer watcher.Stop()

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
