package server

import "time"

// SetTimerForTest replaces the newTimer function used by gracefulStopWithTimeout
// and returns a function that restores the original. Call the restore function
// in a t.Cleanup or defer to avoid test pollution.
//
// Only for use in tests. The timer controls the graceful-stop timeout.
func SetTimerForTest(fn func(d time.Duration) <-chan time.Time) (restore func()) {
	orig := newTimer
	newTimer = fn
	return func() { newTimer = orig }
}
