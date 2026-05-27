// Package testutil provides shared test infrastructure for trino-goway.
package testutil

import (
	"net"
	"testing"
)

// FreePort returns a random available TCP port on localhost.
// Uses net.Listen with ":0" to let the OS pick, then closes immediately.
func FreePort(t testing.TB) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testutil: FreePort: listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("testutil: FreePort: close: %v", err)
	}
	return port
}
