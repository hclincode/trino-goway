package monitor

// TrinoStatus represents the health state of a backend cluster.
type TrinoStatus int

const (
	// StatusUnknown indicates the backend's health is not yet determined.
	StatusUnknown TrinoStatus = iota
	// StatusHealthy indicates the backend is reachable and not starting.
	StatusHealthy
	// StatusUnhealthy indicates the backend returned an error or is still starting.
	StatusUnhealthy
	// StatusPending indicates the backend has been added but not yet probed.
	StatusPending
)

// String returns a human-readable representation of the TrinoStatus.
func (s TrinoStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusUnhealthy:
		return "unhealthy"
	case StatusPending:
		return "pending"
	default:
		return "unknown"
	}
}
