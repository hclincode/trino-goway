// Package reload implements the fsnotify-based config watcher for the
// routing-service. It watches the config file, debounces rapid writes, and
// atomically swaps the pipeline with a newly compiled method set on each
// valid change. Invalid configs are rejected before any swap (keep-last-good).
package reload

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

const (
	// debounceInterval is the quiet period after the last write event before a
	// reload is triggered. Rapid writes (e.g. an editor writing then chmod, or a
	// rename-into-place) are coalesced into a single reload attempt.
	debounceInterval = 100 * time.Millisecond
)

// ConfigWatcher watches a config file and hot-reloads the pipeline when it
// changes. Call Start to begin watching; call Stop to clean up. All timing and
// reload work happens on a single internally-owned goroutine so that Stop
// deterministically drains it (no goroutine leaks, no reload racing past Stop).
type ConfigWatcher struct {
	configPath string
	pipeline   *engine.Pipeline
	registry   *engine.Registry
	log        *slog.Logger

	// Metrics / observability counters. RS-9 wires these into Prometheus; in
	// RS-6 they are simple atomic counters so tests can assert on them.
	reloadSuccessTotal atomic.Int64
	reloadErrorTotal   atomic.Int64

	// reloadCount is incremented on every reload() call (success or error).
	// Debounce tests assert this is exactly 1 after a burst of writes.
	reloadCount atomic.Int64

	// lastHash is the content hash of the last successfully-applied config.
	// Used to populate the old_hash field of audit events. Empty until the
	// first successful reload.
	lastHash atomic.Pointer[string]

	// reloadHook, if non-nil, is invoked at the end of every reload() (after the
	// outcome is recorded). Tests set this to observe reload timing/coalescing.
	reloadHook func()

	// reloadSink, if non-nil, is invoked with the reload outcome (true=ok) so the
	// caller can record a metric. Keeps the reload package free of a metrics dep.
	reloadSink func(ok bool)

	// versionSink, if non-nil, is invoked with the new config hash after a
	// successful swap so the caller can update the active config version.
	versionSink func(hash string)

	// done is closed when the watcher goroutine has fully exited.
	done chan struct{}

	// watcher is the underlying fsnotify.Watcher.
	watcher *fsnotify.Watcher
}

// New returns a ConfigWatcher that watches configPath and applies valid
// configs to pipeline via registry.
func New(configPath string, pipeline *engine.Pipeline, registry *engine.Registry, log *slog.Logger) *ConfigWatcher {
	return &ConfigWatcher{
		configPath: configPath,
		pipeline:   pipeline,
		registry:   registry,
		log:        log,
		done:       make(chan struct{}),
	}
}

// ReloadSuccessTotal returns the number of successful reloads since Start.
func (w *ConfigWatcher) ReloadSuccessTotal() int64 { return w.reloadSuccessTotal.Load() }

// ReloadErrorTotal returns the number of failed reload attempts since Start.
func (w *ConfigWatcher) ReloadErrorTotal() int64 { return w.reloadErrorTotal.Load() }

// ReloadCount returns the total number of reload() invocations (success + error).
// Used by debounce tests to assert coalescing behaviour.
func (w *ConfigWatcher) ReloadCount() int64 { return w.reloadCount.Load() }

// SetReloadHook installs a function invoked at the end of every reload(). It is
// intended for tests; production code leaves it nil. Call before Start.
func (w *ConfigWatcher) SetReloadHook(fn func()) { w.reloadHook = fn }

// SetReloadSink wires a reload-outcome callback (true=ok, false=error) so the
// caller can record a metric. The reload package stays free of a metrics
// dependency; main.go adapts *metrics.Metrics into this callback. Call before Start.
func (w *ConfigWatcher) SetReloadSink(fn func(ok bool)) { w.reloadSink = fn }

// SetVersionSink wires a callback invoked with the new config hash after each
// successful swap (e.g. to update the active config-version gauge/log field).
// Call before Start.
func (w *ConfigWatcher) SetVersionSink(fn func(hash string)) { w.versionSink = fn }

// Start begins watching the config file. It returns an error if the fsnotify
// watcher cannot be initialised or if adding the config path fails. The watcher
// goroutine runs until Stop is called or ctx is cancelled.
func (w *ConfigWatcher) Start(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("reload: new watcher: %w", err)
	}
	if err := fw.Add(w.configPath); err != nil {
		_ = fw.Close()
		return fmt.Errorf("reload: watch %q: %w", w.configPath, err)
	}
	w.watcher = fw

	go w.loop(ctx)
	return nil
}

// Stop closes the fsnotify watcher and waits for the watcher goroutine to exit.
// It is safe to call Stop more than once; subsequent calls block until the
// goroutine has exited (which the first call guarantees).
func (w *ConfigWatcher) Stop() {
	// Closing the fsnotify watcher closes its Events/Errors channels, which the
	// loop detects and returns. The reload timer (if armed) is stopped by loop
	// on its way out, so no reload runs after Stop returns.
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
	<-w.done
}

// loop is the sole watcher goroutine. It owns the debounce timer and performs
// reloads inline, so a single happens-before from channel-close to goroutine
// exit covers all reload work — Stop drains everything cleanly.
func (w *ConfigWatcher) loop(ctx context.Context) {
	defer close(w.done)

	// timer is created stopped; we drive it with a never-firing initial deadline
	// and Reset on each write event. timerC is nil until the timer is armed so
	// the select doesn't busy-fire on a drained channel.
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	var timerC <-chan time.Time
	defer timer.Stop()

	arm := func() {
		timer.Reset(debounceInterval)
		timerC = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
				arm()
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// fsnotify errors are non-fatal; log and continue.
			w.log.Warn("reload: fsnotify error", "err", err)
		case <-timerC:
			timerC = nil
			w.reload()
		}
	}
}

// reload reads and validates the new config, builds (compiles) every method,
// and only on full success atomically swaps the pipeline. On any failure the
// live pipeline is left untouched (last-known-good) and an error audit event is
// emitted.
func (w *ConfigWatcher) reload() {
	defer func() {
		w.reloadCount.Add(1)
		if w.reloadHook != nil {
			w.reloadHook()
		}
	}()

	oldHash := w.oldHash()

	// 1. Read the raw bytes once: used both for parsing and for the new hash so
	// the audit event hash matches exactly what was evaluated.
	data, readErr := os.ReadFile(w.configPath)
	if readErr != nil {
		w.fail(oldHash, "", fmt.Sprintf("read config: %v", readErr))
		return
	}
	newHash := hashBytes(data)

	// 2. Parse + validate the config.
	cfg, err := config.Load(w.configPath)
	if err != nil {
		w.fail(oldHash, newHash, fmt.Sprintf("config parse failed: %v", err))
		return
	}

	// 3. Build (and thereby compile/validate) every method. Any failure aborts
	// the whole reload before any swap.
	newMethods := make([]engine.RoutingMethod, 0, len(cfg.Methods))
	for _, mc := range cfg.Methods {
		m, err := w.registry.Build(mc)
		if err != nil {
			w.fail(oldHash, newHash, fmt.Sprintf("method %q build failed: %v", mc.Type, err))
			return
		}
		newMethods = append(newMethods, m)
	}

	// 4. All methods validated — atomically swap the live pipeline.
	w.pipeline.Swap(newMethods)
	w.lastHash.Store(&newHash)
	w.reloadSuccessTotal.Add(1)
	w.emitAudit("ok", oldHash, newHash, fmt.Sprintf("%d method(s)", len(newMethods)), "")
	if w.reloadSink != nil {
		w.reloadSink(true)
	}
	if w.versionSink != nil {
		w.versionSink(newHash)
	}
	w.log.Info("reload: config applied", "methods", len(newMethods), "hash", newHash)
}

// fail records a failed reload: increments the error counter, emits an error
// audit event, and logs — all without touching the live pipeline.
func (w *ConfigWatcher) fail(oldHash, newHash, errMsg string) {
	w.reloadErrorTotal.Add(1)
	w.emitAudit("error", oldHash, newHash, "", errMsg)
	if w.reloadSink != nil {
		w.reloadSink(false)
	}
	w.log.Error("reload: keeping last-known-good", "old_hash", oldHash, "new_hash", newHash, "err", errMsg)
}

// oldHash returns the hash of the last successfully-applied config, or "" if
// none has been applied yet.
func (w *ConfigWatcher) oldHash() string {
	if p := w.lastHash.Load(); p != nil {
		return *p
	}
	return ""
}

// emitAudit logs a structured audit event for a reload outcome. RS-9 will also
// route this to an audit sink/metric; for RS-6 a structured slog event carrying
// timestamp, old/new config hash, and outcome is the contract.
func (w *ConfigWatcher) emitAudit(result, oldHash, newHash, summary, errMsg string) {
	w.log.Info("reload: audit",
		"trigger", "file_change",
		"result", result,
		"old_hash", oldHash,
		"new_hash", newHash,
		"summary", summary,
		"err", errMsg,
		"timestamp", time.Now().UTC().Format(time.RFC3339Nano),
	)
}

// hashBytes returns the first 8 hex chars of the SHA-256 of data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:4])
}
