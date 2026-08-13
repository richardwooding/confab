package flyaffinity

import "time"

// SetPeersForTest pins the machine set so ownership is deterministic in tests,
// bypassing DNS. The huge fetchedAt keeps the cache valid.
func (a *Resolver) SetPeersForTest(ids []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cached = ids
	a.fetchedAt = time.Now().Add(1000 * time.Hour)
}
