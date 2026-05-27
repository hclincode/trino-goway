// Package diffharness drives differential testing between the Java
// trino-gateway and the Go trino-goway. Scenarios describe HTTP request
// sequences to replay at both gateways; responses are normalized and diffed.
//
// The package is consumed by the cmd/goway-diff-harness binary and by e2e
// tests. It does not import any gateway implementation directly — both targets
// are passed in as URLs so the same harness can run live or replay modes.
package diffharness
