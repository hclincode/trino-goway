// Package clusterstats collects live per-backend cluster statistics (queued and
// running query counts, worker-node count, per-user queued breakdown) for
// UC-MON-02 and the M7 public backend-state wire shape.
//
// A config-selected Collector (NOOP, INFO_API, UI_API, METRICS) is driven by the
// health monitor on its existing probe tick and publishes one name-keyed
// snapshot per cycle into a StatsStore, which the admin layer reads from.
//
// Import direction is one-way: monitor → clusterstats. This package must NOT
// import internal/monitor. The shared status enum lives in the dependency-free
// internal/clusterstatus leaf package, imported by both.
package clusterstats
