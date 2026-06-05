package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus/collectors"
)

// RegisterRuntime registers the Go runtime and process collectors on the registry.
//
// These are the Go-native equivalent of the JVM and process metrics the Java
// trino-gateway exposed (and which this rewrite deliberately drops along with the
// JVM): the Go collector emits goroutine/thread counts, GC pause and cycle stats,
// and heap allocation/object gauges (go_goroutines, go_gc_*, go_memstats_*); the
// process collector emits process CPU seconds, resident/virtual memory, open file
// descriptors, and process start time (process_*).
func (r *Registry) RegisterRuntime() error {
	if err := r.reg.Register(collectors.NewGoCollector()); err != nil {
		return fmt.Errorf("metrics: register go collector: %w", err)
	}
	if err := r.reg.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return fmt.Errorf("metrics: register process collector: %w", err)
	}
	return nil
}
