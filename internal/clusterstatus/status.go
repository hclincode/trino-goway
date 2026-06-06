package clusterstatus

// Status represents the health state of a backend Trino cluster.
//
// Unknown is the zero value: a backend whose health has not yet been determined
// reads as Unknown until the first probe completes.
type Status int

const (
	// Unknown indicates the backend's health is not yet determined.
	Unknown Status = iota
	// Healthy indicates the backend is reachable and not starting.
	Healthy
	// Unhealthy indicates the backend returned an error.
	Unhealthy
	// Pending indicates the backend has been added but not yet probed, or is
	// still starting (Trino /v1/info reports {"starting": true}).
	Pending
)

// Label returns the admin wire-form label for the status, matching Java's
// TrinoStatus enum names ("HEALTHY", "UNHEALTHY", "PENDING", "UNKNOWN").
func (s Status) Label() string {
	switch s {
	case Healthy:
		return "HEALTHY"
	case Unhealthy:
		return "UNHEALTHY"
	case Pending:
		return "PENDING"
	default:
		return "UNKNOWN"
	}
}

// String returns the lowercase form used in logs.
func (s Status) String() string {
	switch s {
	case Healthy:
		return "healthy"
	case Unhealthy:
		return "unhealthy"
	case Pending:
		return "pending"
	default:
		return "unknown"
	}
}
