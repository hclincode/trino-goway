package reload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
	exprovider "github.com/hclincode/trino-goway-routing-service/internal/engine/providers/expr"
	"github.com/hclincode/trino-goway-routing-service/internal/reload"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// --- helpers ---

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newRegistry() *engine.Registry {
	reg := engine.NewRegistry()
	reg.Register("expr", func() engine.RoutingMethod { return exprovider.New() })
	return reg
}

// configForGroup returns a config that routes source=="a" to the given group.
func configForGroup(group string) string {
	return "" +
		"addr: \":9001\"\n" +
		"defaultRoutingGroup: default\n" +
		"methods:\n" +
		"  - type: expr\n" +
		"    program: 'request.source == \"a\" ? \"" + group + "\" : \"\"'\n"
}

// invalidConfig returns a config whose expr program fails to compile (returns
// an int, which the AsKind(String) check rejects at LoadConfig time).
func invalidConfig() string {
	return "" +
		"addr: \":9001\"\n" +
		"defaultRoutingGroup: default\n" +
		"methods:\n" +
		"  - type: expr\n" +
		"    program: '42'\n"
}

// writeFile writes content to path, replacing any existing file.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// routeA evaluates the pipeline for source=="a" and returns the routing group.
func routeA(p *engine.Pipeline) string {
	return p.Evaluate(context.Background(), &engine.RouteInput{Source: "a", IsNew: true})
}

// waitFor polls cond until true or the deadline elapses; fails the test on timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, msg)
}

// startWatcher writes the initial config, builds a pipeline from it, and starts
// a watcher. It returns the pipeline, watcher, and config path. The watcher is
// stopped via t.Cleanup.
func startWatcher(t *testing.T, initial string) (*engine.Pipeline, *reload.ConfigWatcher, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, initial)

	reg := newRegistry()
	// Build the initial pipeline from the on-disk config via the production path
	// (config.Load + registry.Build) so the watcher starts from a known-good
	// live state exactly as main.go would.
	p := buildPipeline(t, reg, path)

	w := reload.New(path, p, reg, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		cancel()
		t.Fatalf("watcher start: %v", err)
	}
	t.Cleanup(func() {
		w.Stop()
		cancel()
	})
	return p, w, path
}

// buildPipeline loads the config at path and builds a live pipeline from it via
// the production registry path, mirroring how main.go constructs the initial
// pipeline before the watcher takes over.
func buildPipeline(t *testing.T, reg *engine.Registry, path string) *engine.Pipeline {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("initial config.Load: %v", err)
	}
	methods := make([]engine.RoutingMethod, 0, len(cfg.Methods))
	for _, mc := range cfg.Methods {
		m, err := reg.Build(mc)
		if err != nil {
			t.Fatalf("initial registry.Build(%q): %v", mc.Type, err)
		}
		methods = append(methods, m)
	}
	return engine.NewPipeline(methods, cfg.DefaultRoutingGroup, discardLogger())
}

// --- tests ---

func TestWatcher_ValidConfigRoutes(t *testing.T) {
	p, _, _ := startWatcher(t, configForGroup("group-a"))
	if got := routeA(p); got != "group-a" {
		t.Fatalf("initial route = %q, want %q", got, "group-a")
	}
}

func TestWatcher_InvalidReloadKeepsLastGood(t *testing.T) {
	p, w, path := startWatcher(t, configForGroup("group-a"))
	if got := routeA(p); got != "group-a" {
		t.Fatalf("initial route = %q, want %q", got, "group-a")
	}

	errsBefore := w.ReloadErrorTotal()
	writeFile(t, path, invalidConfig())

	// Wait for the (failed) reload to register.
	waitFor(t, 2*time.Second,
		func() bool { return w.ReloadErrorTotal() == errsBefore+1 },
		"reload error counter to increment by 1")

	// Pipeline must still serve the last-known-good route.
	if got := routeA(p); got != "group-a" {
		t.Fatalf("after invalid reload, route = %q, want last-good %q", got, "group-a")
	}
	if w.ReloadSuccessTotal() != 0 {
		t.Fatalf("ReloadSuccessTotal = %d, want 0 (no valid reload happened)", w.ReloadSuccessTotal())
	}
}

func TestWatcher_ValidReloadSwapsPipeline(t *testing.T) {
	p, w, path := startWatcher(t, configForGroup("group-a"))
	if got := routeA(p); got != "group-a" {
		t.Fatalf("initial route = %q, want %q", got, "group-a")
	}

	successBefore := w.ReloadSuccessTotal()
	writeFile(t, path, configForGroup("group-b"))

	waitFor(t, 2*time.Second,
		func() bool { return routeA(p) == "group-b" },
		"pipeline to route to group-b after valid reload")

	if w.ReloadSuccessTotal() != successBefore+1 {
		t.Fatalf("ReloadSuccessTotal = %d, want %d", w.ReloadSuccessTotal(), successBefore+1)
	}
}

func TestWatcher_ConcurrentTrafficDuringReload(t *testing.T) {
	p, _, path := startWatcher(t, configForGroup("group-a"))

	const goroutines = 10
	const callsEach = 100

	var wg sync.WaitGroup
	var badGroup atomic.Int64
	// Acceptable groups across the reload window: the old group, the new group,
	// or the default. A nil/empty result would prove the atomic swap exposed a
	// torn state.
	ok := map[string]bool{"group-a": true, "group-b": true, "default": true}

	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < callsEach; i++ {
				got := routeA(p)
				if !ok[got] {
					badGroup.Add(1)
				}
			}
		}()
	}

	close(start)
	// Trigger a valid reload mid-traffic.
	writeFile(t, path, configForGroup("group-b"))
	wg.Wait()

	if n := badGroup.Load(); n != 0 {
		t.Fatalf("%d Evaluate calls returned an unexpected/torn group during reload", n)
	}
}

func TestWatcher_DebounceCoalescesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, configForGroup("group-a"))

	reg := newRegistry()
	p := buildPipeline(t, reg, path)

	w := reload.New(path, p, reg, discardLogger())

	var hookCalls atomic.Int64
	lastHook := make(chan struct{}, 64)
	w.SetReloadHook(func() {
		hookCalls.Add(1)
		select {
		case lastHook <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		cancel()
		t.Fatalf("watcher start: %v", err)
	}
	t.Cleanup(func() {
		w.Stop()
		cancel()
	})

	// 5 rapid writes well within the 100ms debounce window.
	for i := 0; i < 5; i++ {
		writeFile(t, path, configForGroup("group-b"))
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for at least one reload to fire.
	select {
	case <-lastHook:
	case <-time.After(2 * time.Second):
		t.Fatal("no reload fired within 2s")
	}

	// Allow plenty of settle time (well past the 100ms debounce window); assert
	// no further reloads occurred — the burst must coalesce to exactly one reload().
	time.Sleep(300 * time.Millisecond)

	if got := w.ReloadCount(); got != 1 {
		t.Fatalf("ReloadCount = %d, want exactly 1 (debounce should coalesce the burst)", got)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("reload hook calls = %d, want exactly 1", got)
	}
}

// safeBuf is a goroutine-safe bytes.Buffer; the watcher logs from its own
// goroutine while the test reads, so writes and reads must be synchronised.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// auditEvents parses captured JSON slog lines and returns those that are reload
// audit events ({"msg":"reload: audit", ...}).
func auditEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // non-JSON line (shouldn't happen with JSON handler)
		}
		if m["msg"] == "reload: audit" {
			out = append(out, m)
		}
	}
	return out
}

func TestWatcher_AuditEventsOnReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeFile(t, path, configForGroup("group-a"))

	reg := newRegistry()
	p := buildPipeline(t, reg, path)

	var sink safeBuf
	log := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelInfo}))
	w := reload.New(path, p, reg, log)

	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		cancel()
		t.Fatalf("watcher start: %v", err)
	}
	t.Cleanup(func() {
		w.Stop()
		cancel()
	})

	// 1. Valid reload → expect an audit event {result: "ok"} with a new_hash.
	writeFile(t, path, configForGroup("group-b"))
	waitFor(t, 2*time.Second,
		func() bool { return w.ReloadSuccessTotal() == 1 },
		"one successful reload")

	// 2. Invalid reload → expect an audit event {result: "error"} that carries the
	// prior config's hash as old_hash (last-known-good provenance).
	writeFile(t, path, invalidConfig())
	waitFor(t, 2*time.Second,
		func() bool { return w.ReloadErrorTotal() == 1 },
		"one failed reload")

	// Give the watcher goroutine a moment to flush the final audit log line.
	waitFor(t, 2*time.Second,
		func() bool { return len(auditEvents(t, sink.String())) >= 2 },
		"two audit events captured")

	events := auditEvents(t, sink.String())
	var ok, errEv map[string]any
	for _, e := range events {
		switch e["result"] {
		case "ok":
			ok = e
		case "error":
			errEv = e
		}
	}
	if ok == nil {
		t.Fatalf("no audit event with result=ok; events=%v", events)
	}
	if errEv == nil {
		t.Fatalf("no audit event with result=error; events=%v", events)
	}

	// The ok event must carry trigger, a non-empty new_hash, and a timestamp.
	if ok["trigger"] != "file_change" {
		t.Errorf("ok audit trigger = %v, want file_change", ok["trigger"])
	}
	if s, _ := ok["new_hash"].(string); s == "" {
		t.Errorf("ok audit new_hash is empty, want the applied config hash")
	}
	if s, _ := ok["timestamp"].(string); s == "" {
		t.Errorf("ok audit timestamp is empty")
	}

	// The error event must reference the last-known-good config as old_hash —
	// which equals the ok event's new_hash (the config we successfully applied).
	okHash, _ := ok["new_hash"].(string)
	if oldHash, _ := errEv["old_hash"].(string); oldHash != okHash {
		t.Errorf("error audit old_hash = %q, want last-good hash %q", oldHash, okHash)
	}
}
