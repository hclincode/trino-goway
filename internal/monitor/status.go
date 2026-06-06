package monitor

import "github.com/hclincode/trino-goway/internal/clusterstatus"

// TrinoStatus represents the health state of a backend cluster.
//
// It is a thin alias of clusterstatus.Status (the shared leaf enum imported by
// both monitor and clusterstats); the String()/Label() methods and member set
// live in clusterstatus. The alias keeps existing monitor.Status* consumers
// (admin, monitor internals) compiling unchanged.
type TrinoStatus = clusterstatus.Status

const (
	// StatusUnknown indicates the backend's health is not yet determined.
	StatusUnknown = clusterstatus.Unknown
	// StatusHealthy indicates the backend is reachable and not starting.
	StatusHealthy = clusterstatus.Healthy
	// StatusUnhealthy indicates the backend returned an error.
	StatusUnhealthy = clusterstatus.Unhealthy
	// StatusPending indicates the backend has been added but not yet probed, or
	// is still starting.
	StatusPending = clusterstatus.Pending
)
