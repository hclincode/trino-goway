package clusterstats

import (
	"context"
	"time"
)

// retryBackoff is the fixed delay between transport-error retries. Kept small so
// a retry still completes within the stats timeout.
const retryBackoff = 100 * time.Millisecond

// backoff waits before the next retry attempt. It returns false when no further
// attempt should be made — either the attempts are exhausted (attempt is the last
// index) or the context is done during the wait. It uses select+ctx.Done rather
// than time.Sleep so a cancelled context aborts immediately (no leaked sleeps).
func backoff(ctx context.Context, attempt, attempts int) bool {
	if attempt >= attempts-1 {
		return false
	}
	t := time.NewTimer(retryBackoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
