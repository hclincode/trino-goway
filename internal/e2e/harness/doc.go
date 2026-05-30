//go:build e2e

// Package harness boots a real trino-goway binary as a subprocess wired against
// a Postgres testcontainer and fake Trino backends. E2E tests drive the gateway
// purely through its HTTP interface using the typed clients returned by Harness.
package harness
