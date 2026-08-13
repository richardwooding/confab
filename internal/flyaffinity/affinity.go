// Package flyaffinity routes relay connections so every connection for a given
// session lands on the same Fly machine (session-affinity sharding). Each
// machine's parley relay stays in-memory authoritative for its shard.
//
// Ownership is by rendezvous (highest-random-weight) hashing over the live
// machine set, discovered from Fly internal DNS. HRW moves only ~1/N sessions
// when the machine set changes (vs ~all for modulo), and needs no coordination:
// every machine computes the identical owner from the same peer set with a
// fixed, seedless hash. The Route method plugs into parley's relay.Options.Router.
package flyaffinity

import (
	"context"
	"hash/fnv"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/richardwooding/parley/relay"
	"github.com/richardwooding/parley/wire"
)

// Resolver answers ownership questions for one machine. Construct with New.
type Resolver struct {
	app  string        // FLY_APP_NAME
	self string        // FLY_MACHINE_ID ("" off Fly → always serve here)
	ttl  time.Duration // peer-cache lifetime
	res  *net.Resolver // nil → default (uses the machine's Fly resolver)

	mu        sync.Mutex
	cached    []string // machine ids, sorted; peers + self
	fetchedAt time.Time
}

// New builds a Resolver. app is FLY_APP_NAME, self is FLY_MACHINE_ID (empty
// when not running on Fly), ttl bounds how long a peer set is cached.
func New(app, self string, ttl time.Duration) *Resolver {
	return &Resolver{app: app, self: self, ttl: ttl}
}

// Route is the parley relay.Options.Router hook. It decides, before the
// WebSocket upgrade, whether this machine owns the session (serve here) or must
// hand it to the owning machine via Fly-Replay.
func (a *Resolver) Route(sid wire.SessionID, r *http.Request) relay.RouteResult {
	if a.self == "" {
		return relay.RouteResult{} // not on Fly: serve everything locally
	}
	// One-hop loop guard: Fly stamps Fly-Replay-Src on a request it already
	// replayed. Serve it here regardless of our (possibly stale) peer view.
	if r.Header.Get("Fly-Replay-Src") != "" {
		return relay.RouteResult{}
	}
	peers := a.machines(r.Context())
	if len(peers) <= 1 {
		return relay.RouteResult{} // single machine: nothing to hand off to
	}
	owner := ownerOf(sid, peers)
	if owner == "" || owner == a.self {
		return relay.RouteResult{}
	}
	return relay.RouteResult{
		Replay: true,
		Header: http.Header{"Fly-Replay": {"instance=" + owner}},
		Status: http.StatusServiceUnavailable,
	}
}

// Peers returns the base URLs of the OTHER machines (this one excluded) for the
// dashboard stats aggregator. Fly 6PN address form: http://[<id>.vm.<app>.internal]:8080.
func (a *Resolver) Peers(ctx context.Context) ([]string, error) {
	ids := a.machines(ctx)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == a.self {
			continue
		}
		out = append(out, "http://["+id+".vm."+a.app+".internal]:8080")
	}
	return out, nil
}

// machines returns the cached machine-id set (peers + self), refreshing from
// Fly DNS when the cache is older than ttl. On lookup failure it serves the
// last good set, or falls back to just self (degraded → serve-here).
func (a *Resolver) machines(ctx context.Context) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cached) > 0 && time.Since(a.fetchedAt) < a.ttl {
		return a.cached
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	res := a.res
	if res == nil {
		res = net.DefaultResolver
	}
	txts, err := res.LookupTXT(lookupCtx, "vms."+a.app+".internal")
	if err != nil {
		if len(a.cached) > 0 {
			return a.cached // serve stale rather than flap
		}
		return []string{a.self}
	}
	ids := parseVMs(txts)
	if len(ids) == 0 {
		ids = []string{a.self}
	}
	sort.Strings(ids)
	a.cached, a.fetchedAt = ids, time.Now()
	return ids
}

// parseVMs extracts machine ids from vms.<app>.internal TXT records. Fly
// returns the fleet as comma-separated entries, each "<machine_id> <region>"
// (typically a single record holding all entries), e.g.
// "811d5d2f471098 jnb,8254dea7ed4458 jnb". The id is the first whitespace
// field of each comma-separated entry.
func parseVMs(txts []string) []string {
	var ids []string
	for _, rec := range txts {
		for _, entry := range strings.Split(rec, ",") {
			if f := strings.Fields(entry); len(f) > 0 {
				ids = append(ids, f[0])
			}
		}
	}
	return ids
}

// ownerOf picks the session's owner by highest-random-weight: each machine is
// scored H(sid ∥ id) with a fixed seedless hash, highest wins (id breaks ties).
func ownerOf(sid wire.SessionID, machines []string) string {
	var best string
	var bestScore uint64
	for _, id := range machines {
		h := fnv.New64a()
		_, _ = h.Write(sid[:])
		_, _ = h.Write([]byte(id))
		score := h.Sum64()
		if best == "" || score > bestScore || (score == bestScore && id > best) {
			best, bestScore = id, score
		}
	}
	return best
}
