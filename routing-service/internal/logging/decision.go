// Package logging provides the structured per-decision logger for the
// routing-service. It enforces the PRD §7 PII rule (never log raw SQL — only a
// sha256 prefix) and the sampling policy (~10% steady-state, 100% on fallback).
package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math/rand/v2"
	"time"
)

// defaultSampleRate is the steady-state fraction of non-fallback decisions that
// are logged. Fallbacks are always logged regardless of this rate.
const defaultSampleRate = 0.10

// DecisionFields is the data logged for one Route decision.
type DecisionFields struct {
	// RuleID is the deciding method's type (e.g. "expr"), or "" on fallback.
	RuleID string
	// Source is the request's X-Trino-Source.
	Source string
	// User is the request's X-Trino-User.
	User string
	// Body is the raw SQL body. It is NEVER logged verbatim — only its
	// sha256[:8] prefix is emitted.
	Body string
	// RoutingGroup is the chosen group.
	RoutingGroup string
	// Latency is the decision wall time.
	Latency time.Duration
	// ConfigVersionHash is the active config content hash.
	ConfigVersionHash string
	// Fallback is true when no method decided (default group used).
	Fallback bool
}

// sampler decides whether a given decision should be logged.
type sampler interface {
	// sample returns true with probability rate (rate in [0,1]).
	sample(rate float64) bool
}

// randSampler uses the math/rand/v2 global source (safe for concurrent use).
type randSampler struct{}

func (randSampler) sample(rate float64) bool { return rand.Float64() < rate }

// DecisionLogger emits sampled, redacted decision logs.
type DecisionLogger struct {
	log        *slog.Logger
	sampleRate float64
	s          sampler
}

// NewDecisionLogger returns a logger sampling non-fallback decisions at ~10%.
func NewDecisionLogger(log *slog.Logger) *DecisionLogger {
	return &DecisionLogger{log: log, sampleRate: defaultSampleRate, s: randSampler{}}
}

// ShouldLog reports whether a decision should be logged: always on fallback,
// otherwise with probability sampleRate.
func (d *DecisionLogger) ShouldLog(isFallback bool) bool {
	if isFallback {
		return true
	}
	return d.s.sample(d.sampleRate)
}

// Log emits one decision record at INFO if ShouldLog allows it. Raw SQL is
// reduced to bodyHash = sha256(body)[:8]; the raw body never reaches the log.
func (d *DecisionLogger) Log(ctx context.Context, f DecisionFields) {
	if !d.ShouldLog(f.Fallback) {
		return
	}
	d.log.LogAttrs(ctx, slog.LevelInfo, "routing: decision",
		slog.String("rule_id", f.RuleID),
		slog.String("source", f.Source),
		slog.String("user", f.User),
		slog.String("body_sha256", BodyHash(f.Body)),
		slog.String("routing_group", f.RoutingGroup),
		slog.Int64("latency_ms", f.Latency.Milliseconds()),
		slog.String("config_version_hash", f.ConfigVersionHash),
		slog.Bool("fallback", f.Fallback),
	)
}

// BodyHash returns the first 8 hex chars of sha256(body), or "" for an empty
// body. This is the only representation of the SQL body permitted in logs.
func BodyHash(body string) string {
	if body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])[:8]
}
