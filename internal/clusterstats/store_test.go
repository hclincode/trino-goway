package clusterstats

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hclincode/trino-goway/internal/clusterstatus"
)

// TestStatsStore_KeyedByName asserts the store keys by backend NAME (not URL):
// observing and reading back uses the ClusterID/name, and an URL is never a key.
func TestStatsStore_KeyedByName(t *testing.T) {
	t.Parallel()

	s := NewStatsStore()
	s.ObserveStats(map[string]ClusterStats{
		"be-a": {ClusterID: "be-a", RunningQueryCount: 5, QueuedQueryCount: 2, TrinoStatus: clusterstatus.Healthy},
		"be-b": {ClusterID: "be-b", RunningQueryCount: 1},
	})

	a := s.Stats("be-a")
	assert.Equal(t, "be-a", a.ClusterID)
	assert.Equal(t, 5, a.RunningQueryCount)
	assert.Equal(t, 2, a.QueuedQueryCount)
	assert.Equal(t, clusterstatus.Healthy, a.TrinoStatus)

	b := s.Stats("be-b")
	assert.Equal(t, 1, b.RunningQueryCount)

	// A proxyTo URL is not a key — only the name is.
	assert.Equal(t, ClusterStats{ClusterID: "http://backend/a"}, s.Stats("http://backend/a"))
}

// TestStatsStore_UnobservedDefaultFromPersistence pins choice (b): for a backend
// never observed (and before the first ObserveStats), the store returns a zero
// ClusterStats with ClusterID set to the name — counts 0, TrinoStatus UNKNOWN,
// UserQueuedCount nil. The admin boundary fills proxyTo/externalUrl/routingGroup
// from persistence; the store itself leaves them empty.
func TestStatsStore_UnobservedDefaultFromPersistence(t *testing.T) {
	t.Parallel()

	t.Run("before first observe", func(t *testing.T) {
		t.Parallel()
		s := NewStatsStore()
		cs := s.Stats("never-seen")

		assert.Equal(t, "never-seen", cs.ClusterID)
		assert.Zero(t, cs.RunningQueryCount)
		assert.Zero(t, cs.QueuedQueryCount)
		assert.Zero(t, cs.NumWorkerNodes)
		assert.Equal(t, clusterstatus.Unknown, cs.TrinoStatus)
		assert.Nil(t, cs.UserQueuedCount)
		assert.Empty(t, cs.ProxyTo)
		assert.Empty(t, cs.ExternalURL)
		assert.Empty(t, cs.RoutingGroup)
	})

	t.Run("name absent from a populated snapshot", func(t *testing.T) {
		t.Parallel()
		s := NewStatsStore()
		s.ObserveStats(map[string]ClusterStats{"observed": {ClusterID: "observed", RunningQueryCount: 3}})

		cs := s.Stats("absent")
		assert.Equal(t, ClusterStats{ClusterID: "absent"}, cs)
	})
}

// TestStatsStore_ObserveReplacesSnapshot verifies each ObserveStats fully replaces
// the prior snapshot (single swap), so a name dropped from a later tick reverts to
// the uncollected default.
func TestStatsStore_ObserveReplacesSnapshot(t *testing.T) {
	t.Parallel()

	s := NewStatsStore()
	s.ObserveStats(map[string]ClusterStats{"be-a": {ClusterID: "be-a", RunningQueryCount: 5}})
	assert.Equal(t, 5, s.Stats("be-a").RunningQueryCount)

	s.ObserveStats(map[string]ClusterStats{"be-b": {ClusterID: "be-b", RunningQueryCount: 1}})
	assert.Equal(t, ClusterStats{ClusterID: "be-a"}, s.Stats("be-a"), "be-a dropped from the new snapshot")
	assert.Equal(t, 1, s.Stats("be-b").RunningQueryCount)
}

// TestStatsStore_ConcurrentReadWrite exercises the atomic snapshot swap under
// concurrent readers and writers; run with -race to catch any data race.
func TestStatsStore_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()

	s := NewStatsStore()
	const goroutines = 8
	const iterations = 1000

	var wg sync.WaitGroup

	// Writers swap the snapshot repeatedly.
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				s.ObserveStats(map[string]ClusterStats{
					"be-a": {ClusterID: "be-a", RunningQueryCount: seed + i},
				})
			}
		}(w)
	}

	// Readers continuously read; the result must always be a complete value.
	for r := 0; r < goroutines; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				cs := s.Stats("be-a")
				// ClusterID is either the observed "be-a" or the uncollected default
				// (also "be-a"); never a torn read.
				assert.Equal(t, "be-a", cs.ClusterID)
			}
		}()
	}

	wg.Wait()
}
