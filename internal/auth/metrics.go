package auth

// Metrics records authentication metrics. Defined here (consumer owns the
// interface) per project conventions; nil-safe via noopMetrics so call sites
// never nil-check.
type Metrics interface {
	// AuthRequest records one authentication decision: authType is "oidc",
	// "ldap", or "noop"; result is "allow" or "deny".
	AuthRequest(authType, result string)
	// JWKSRefresh records a JWKS refresh attempt: result is "success" or "error".
	JWKSRefresh(result string)
	// JWKSKeys sets the number of keys currently loaded from the JWKS endpoint.
	JWKSKeys(n int)
}

// Auth metric label values shared by middleware and the metrics implementation.
const (
	TypeOIDC = "oidc"
	TypeLDAP = "ldap"
	TypeNoop = "noop"

	ResultAllow = "allow"
	ResultDeny  = "deny"

	JWKSResultSuccess = "success"
	JWKSResultError   = "error"
)

// noopMetrics is the nil-safe default Metrics used when none is injected.
type noopMetrics struct{}

func (noopMetrics) AuthRequest(string, string) {}
func (noopMetrics) JWKSRefresh(string)         {}
func (noopMetrics) JWKSKeys(int)               {}

// orNoop returns m, or a no-op Metrics when m is nil.
func orNoop(m Metrics) Metrics {
	if m == nil {
		return noopMetrics{}
	}
	return m
}

// metricsFromVariadic resolves an optional Metrics argument to a non-nil value.
func metricsFromVariadic(ms []Metrics) Metrics {
	if len(ms) > 0 {
		return orNoop(ms[0])
	}
	return noopMetrics{}
}
