package routing

// RouterMetrics records routing-path metrics. Defined here (consumer owns the
// interface) per project conventions; nil-safe via the noopMetrics fallback so
// call sites never nil-check.
type RouterMetrics interface {
	// RouterCall records one external routing call: transport is "http" or "grpc";
	// outcome is "ok", "error", "timeout", or "fallback".
	RouterCall(transport, outcome string, seconds float64)
	// CacheEvent records a sticky-routing cache lookup: event is "hit" or "miss".
	CacheEvent(event string)
	// RecoveryStep records a recovery-chain step taken: step is "history",
	// "probe", or "default".
	RecoveryStep(step string)
	// KillQueryRoute records a KILL QUERY regex-routed request.
	KillQueryRoute()
}

// Router metric label values, shared by the router and the metrics implementation.
const (
	TransportHTTP = "http"
	TransportGRPC = "grpc"

	RouterOutcomeOK       = "ok"
	RouterOutcomeError    = "error"
	RouterOutcomeTimeout  = "timeout"
	RouterOutcomeFallback = "fallback"

	CacheEventHit  = "hit"
	CacheEventMiss = "miss"

	RecoveryStepHistory = "history"
	RecoveryStepProbe   = "probe"
	RecoveryStepDefault = "default"
)

// noopMetrics is the nil-safe default RouterMetrics used when none is injected.
type noopMetrics struct{}

func (noopMetrics) RouterCall(string, string, float64) {}
func (noopMetrics) CacheEvent(string)                  {}
func (noopMetrics) RecoveryStep(string)                {}
func (noopMetrics) KillQueryRoute()                    {}
