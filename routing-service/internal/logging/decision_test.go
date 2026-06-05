package logging_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hclincode/trino-goway-routing-service/internal/logging"
)

// captureLogger returns a DecisionLogger writing JSON to buf.
func captureLogger(buf *bytes.Buffer) *logging.DecisionLogger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return logging.NewDecisionLogger(slog.New(h))
}

func TestLog_RedactsRawSQL(t *testing.T) {
	const sql = "SELECT * FROM secrets WHERE token = 'abc123'"
	var buf bytes.Buffer
	dl := captureLogger(&buf)

	// Fallback=true guarantees the record is emitted (100% on fallback).
	dl.Log(context.Background(), logging.DecisionFields{
		RuleID:            "",
		Source:            "airflow",
		User:              "alice",
		Body:              sql,
		RoutingGroup:      "default",
		Latency:           2 * time.Millisecond,
		ConfigVersionHash: "deadbeef",
		Fallback:          true,
	})

	out := buf.String()
	if out == "" {
		t.Fatal("expected a log record on fallback, got none")
	}
	// The raw SQL must never appear.
	if strings.Contains(out, "secrets") || strings.Contains(out, "abc123") || strings.Contains(out, "SELECT") {
		t.Fatalf("raw SQL leaked into log:\n%s", out)
	}
	// The sha256[:8] prefix must appear.
	sum := sha256.Sum256([]byte(sql))
	want := hex.EncodeToString(sum[:])[:8]
	if !strings.Contains(out, want) {
		t.Fatalf("log missing body_sha256 %q:\n%s", want, out)
	}
}

func TestLog_FieldsPresent(t *testing.T) {
	var buf bytes.Buffer
	dl := captureLogger(&buf)
	dl.Log(context.Background(), logging.DecisionFields{
		RuleID:            "expr",
		Source:            "airflow",
		User:              "alice",
		Body:              "SELECT 1",
		RoutingGroup:      "etl",
		Latency:           3 * time.Millisecond,
		ConfigVersionHash: "cafef00d",
		Fallback:          true, // force emission
	})

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	for _, k := range []string{"rule_id", "source", "user", "body_sha256", "routing_group", "latency_ms", "config_version_hash", "fallback"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("log missing field %q; got keys %v", k, keys(rec))
		}
	}
	if rec["rule_id"] != "expr" {
		t.Errorf("rule_id = %v, want expr", rec["rule_id"])
	}
	if rec["routing_group"] != "etl" {
		t.Errorf("routing_group = %v, want etl", rec["routing_group"])
	}
}

func TestShouldLog_AlwaysOnFallback(t *testing.T) {
	dl := captureLogger(&bytes.Buffer{})
	for i := 0; i < 100; i++ {
		if !dl.ShouldLog(true) {
			t.Fatal("ShouldLog(fallback=true) returned false; must always log on fallback")
		}
	}
}

func TestShouldLog_SampleRateWithinTolerance(t *testing.T) {
	dl := captureLogger(&bytes.Buffer{})
	const n = 1000
	logged := 0
	for i := 0; i < n; i++ {
		if dl.ShouldLog(false) {
			logged++
		}
	}
	// ~10% target; wide tolerance for sampling variance.
	if logged < 80 || logged > 120 {
		t.Errorf("sampled %d/%d (%.1f%%); want within 8-12%%", logged, n, float64(logged)/n*100)
	}
}

func TestBodyHash(t *testing.T) {
	if got := logging.BodyHash(""); got != "" {
		t.Errorf("BodyHash(empty) = %q, want empty", got)
	}
	sum := sha256.Sum256([]byte("x"))
	want := hex.EncodeToString(sum[:])[:8]
	if got := logging.BodyHash("x"); got != want {
		t.Errorf("BodyHash(x) = %q, want %q", got, want)
	}
	if len(logging.BodyHash("anything")) != 8 {
		t.Errorf("BodyHash must be 8 hex chars")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
