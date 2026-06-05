// Package metrics owns the gateway's Prometheus registry and exposition handler.
//
// The package deliberately avoids prometheus.DefaultRegisterer: callers construct
// a Registry with New, register collectors against it explicitly, and the
// composition root injects it where metrics are recorded. This keeps metric
// ownership visible at the wiring site and lets tests run with an isolated registry.
package metrics
