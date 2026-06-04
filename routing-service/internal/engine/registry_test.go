package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hclincode/trino-goway-routing-service/internal/config"
	"github.com/hclincode/trino-goway-routing-service/internal/engine"
)

// errLoadMethod always fails LoadConfig.
type errLoadMethod struct{ typeName string }

func (m *errLoadMethod) Type() string { return m.typeName }
func (m *errLoadMethod) LoadConfig(_ []byte) error {
	return errors.New("simulated LoadConfig failure")
}
func (m *errLoadMethod) Evaluate(_ context.Context, _ *engine.RouteInput) (engine.Decision, error) {
	return engine.Decision{Decided: true, RoutingGroup: "ok"}, nil
}

// okMethod always succeeds at LoadConfig and returns a fixed group.
type okMethod struct{ typeName string }

func (m *okMethod) Type() string           { return m.typeName }
func (m *okMethod) LoadConfig(_ []byte) error { return nil }
func (m *okMethod) Evaluate(_ context.Context, _ *engine.RouteInput) (engine.Decision, error) {
	return engine.Decision{Decided: true, RoutingGroup: "ok"}, nil
}

func TestRegistry_Build_UnknownType_ReturnsError(t *testing.T) {
	r := engine.NewRegistry()
	// Register nothing; build an unknown type.
	cfg := config.MethodConfig{Type: "no-such-type", Program: "x"}
	_, err := r.Build(cfg)
	if err == nil {
		t.Fatal("Build: expected error for unknown type, got nil")
	}
}

func TestRegistry_Build_LoadConfigFailure_PropagatesError(t *testing.T) {
	r := engine.NewRegistry()
	r.Register("fail-load", func() engine.RoutingMethod { return &errLoadMethod{typeName: "fail-load"} })

	cfg := config.MethodConfig{Type: "fail-load", Program: "irrelevant"}
	_, err := r.Build(cfg)
	if err == nil {
		t.Fatal("Build: expected error when LoadConfig fails, got nil")
	}
}

func TestRegistry_Build_Success(t *testing.T) {
	r := engine.NewRegistry()
	r.Register("ok", func() engine.RoutingMethod { return &okMethod{typeName: "ok"} })

	cfg := config.MethodConfig{Type: "ok", Program: "x"}
	m, err := r.Build(cfg)
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	if m.Type() != "ok" {
		t.Errorf("method.Type() = %q, want %q", m.Type(), "ok")
	}
}

func TestRegistry_Register_DuplicatePanics(t *testing.T) {
	r := engine.NewRegistry()
	r.Register("dup", func() engine.RoutingMethod { return &okMethod{typeName: "dup"} })

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("Register duplicate: expected panic, got none")
		}
	}()
	// Second registration of the same type must panic.
	r.Register("dup", func() engine.RoutingMethod { return &okMethod{typeName: "dup"} })
}

func TestRegistry_Build_WithFileConfig(t *testing.T) {
	// Verify Build succeeds with a File-based method config (exercises the
	// methodConfigBytes YAML serialisation path for the file field).
	r := engine.NewRegistry()
	r.Register("file-method", func() engine.RoutingMethod { return &okMethod{typeName: "file-method"} })

	cfg := config.MethodConfig{Type: "file-method", File: "/some/path.star"}
	m, err := r.Build(cfg)
	if err != nil {
		t.Fatalf("Build with file config: unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("Build with file config: returned nil method")
	}
}
