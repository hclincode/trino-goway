// Command routing-service is the standalone gRPC routing service for trino-goway.
// It implements the TrinoGatewayRouter contract and selects routing groups for
// new Trino query submissions.
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	scriptprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/script"
	"github.com/hclincode/trino-goway-routing-service/internal/metrics"
	"github.com/hclincode/trino-goway-routing-service/internal/sqlmeta"
	"github.com/hclincode/trino-goway-routing-service/internal/reload"
	"github.com/hclincode/trino-goway-routing-service/internal/server"
	"github.com/hclincode/trino-goway-routing-service/internal/tracing"
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
		"adminAddr", cfg.AdminAddr,
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// RS-9: observability. Own Prometheus registry (no global) + OTel tracing
	// (optional; disabled when cfg.TracingEndpoint is empty).
	m := metrics.New()

	// RS-16/17: SQL-aware routing (UC-RTG-04). When enabled, inject the
	// best-effort heuristic analyzer plus a metrics observer; when disabled,
	// a no-op analyzer leaves the SQL fields empty (header/source routing only).
	evalOpts := []engine.EvaluatorOption{}
	if cfg.SQLParsing.Enabled {
		log.Info("routing-service: SQL-aware routing enabled",
			"maxBodyBytes", cfg.SQLParsing.MaxBodyBytes)
		evalOpts = append(evalOpts,
			engine.WithSQLAnalyzer(sqlmeta.NewHeuristic(cfg.SQLParsing.MaxBodyBytes)),
			engine.WithSQLObserver(func(result string, dur time.Duration, truncated bool) {
				m.RecordSQLParse(metrics.SQLParseResult(result), dur, truncated)
			}),
		)
	}
	eval := engine.NewPipelineEvaluator(pipeline, evalOpts...)
	tp, prop, tpShutdown, err := tracing.Init(ctx, tracing.Config{
		Endpoint: cfg.TracingEndpoint,
		Insecure: true,
	})
	if err != nil {
		log.Error("routing-service: tracing init failed", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tpShutdown(shutCtx)
	}()

	srv := server.New(cfg, eval, log,
		server.WithMetrics(m),
		server.WithTracing(tp, prop),
	)

	// Seed the active config version (for decision logs + the config_version gauge).
	srv.SetConfigVersion(configHash(*configPath))

	// Transition health to SERVING now that the pipeline is ready.
	srv.SetReady(pipeline.Ready())

	// Serve /metrics on its own port (cfg.MetricsAddr), separate from the
	// data-plane and admin listeners.
	metricsSrv := metrics.NewServer(m)
	metricsErr := make(chan error, 1)
	go func() { metricsErr <- metricsSrv.Start(ctx, cfg.MetricsAddr) }()

	// RS-6: hot-reload. Watch the config file and validate-before-activate on
	// every change; invalid configs keep the last-known-good pipeline live.
	// Wire reload outcomes + the active config version into metrics (RS-9).
	watcher := reload.New(*configPath, pipeline, reg, log)
	watcher.SetReloadSink(func(ok bool) {
		if ok {
			m.RecordReload(metrics.ReloadOK)
		} else {
			m.RecordReload(metrics.ReloadError)
		}
	})
	watcher.SetVersionSink(srv.SetConfigVersion)
	if err := watcher.Start(ctx); err != nil {
		log.Error("routing-service: config watcher failed to start", "err", err)
		os.Exit(1)
	}
	defer watcher.Stop()

	// RS-8: kill-switch. Serve the RoutingServiceAdmin control plane on a
	// SEPARATE listener (cfg.AdminAddr) so it can be firewalled to platform
	// operators. It drives the pipeline's atomic Disable/Enable; changes take
	// effect on the next Route call without a restart.
	admin := server.NewAdmin(pipeline, log)
	// Keep the method_disabled gauge in sync with kill-switch state.
	allMethodTypes := make([]string, 0, len(cfg.Methods))
	for _, mc := range cfg.Methods {
		allMethodTypes = append(allMethodTypes, mc.Type)
	}
	admin.SetOnChange(func(disabled []string) { m.SyncDisabled(allMethodTypes, disabled) })
	adminErr := make(chan error, 1)
	go func() { adminErr <- admin.Start(ctx, cfg.AdminAddr) }()
	defer admin.Stop()

	if err := srv.Start(ctx); err != nil {
		log.Error("routing-service: server error", "err", err)
		os.Exit(1)
	}
	if err := <-adminErr; err != nil {
		log.Error("routing-service: admin server error", "err", err)
		os.Exit(1)
	}
	if err := <-metricsErr; err != nil {
		log.Error("routing-service: metrics server error", "err", err)
		os.Exit(1)
	}
	log.Info("routing-service: stopped")
}

// configHash returns the first 8 hex chars of the SHA-256 of the config file,
// or "unknown" if it cannot be read. Matches the reload watcher's hashing so
// the startup version and reload versions are comparable.
func configHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:4])
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
