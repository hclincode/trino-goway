package testutil

import (
	"testing"

	"go.uber.org/goleak"
)

// VerifyTestMain wraps goleak.VerifyTestMain with trino-goway known goroutine filters.
// Call as: func TestMain(m *testing.M) { testutil.VerifyTestMain(m) }
func VerifyTestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// testcontainers-go and the underlying Docker/Moby client leave behind
		// some long-lived goroutines that are not owned by test code.
		goleak.IgnoreTopFunction("github.com/testcontainers/testcontainers-go.(*Reaper).Connect.func1"),
		goleak.IgnoreTopFunction("github.com/testcontainers/testcontainers-go/internal/core/network.(*network).cleanup"),
		goleak.IgnoreTopFunction("github.com/moby/moby/client.(*Client).NegotiateAPIVersion"),

		// Standard library and runtime goroutines that are sometimes reported
		// in short-lived test processes.
		goleak.IgnoreTopFunction("os/signal.loop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
	)
}
