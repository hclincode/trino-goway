// Package clusterstatus defines the Trino backend health status enum shared by
// the health monitor and the cluster-stats collectors.
//
// It is a leaf package: it has NO internal dependencies, which lets both
// internal/monitor and internal/clusterstats import it without violating the
// one-way import direction (monitor → clusterstats; neither imports the other's
// status type directly). internal/monitor exposes TrinoStatus as a thin alias of
// clusterstatus.Status so existing consumers keep compiling.
package clusterstatus
