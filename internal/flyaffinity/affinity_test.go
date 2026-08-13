package flyaffinity

import (
	"context"
	"net/http"
	"testing"

	"github.com/richardwooding/parley/wire"
)

// --- determinism: same sid + peers → same owner, regardless of process ---
func TestOwnerDeterministic(t *testing.T) {
	peers := []string{"m-aaa", "m-bbb", "m-ccc"}
	sid := wire.SessionID{1, 2, 3, 4, 5}
	first := ownerOf(sid, peers)
	for i := 0; i < 1000; i++ {
		if ownerOf(sid, peers) != first {
			t.Fatal("ownerOf is not deterministic")
		}
	}
	if first == "" {
		t.Fatal("no owner chosen")
	}
}

// --- HRW minimal reshuffle: growing 2→3 moves ~1/3, not ~2/3 (modulo would) ---
func TestOwnerMinimalReshuffle(t *testing.T) {
	two := []string{"m-aaa", "m-bbb"}
	three := []string{"m-aaa", "m-bbb", "m-ccc"}
	const N = 6000
	moved := 0
	for i := 0; i < N; i++ {
		var sid wire.SessionID
		sid[0], sid[1] = byte(i), byte(i>>8)
		if ownerOf(sid, two) != ownerOf(sid, three) {
			moved++
		}
	}
	frac := float64(moved) / N
	// HRW target ≈ 1/3 (only sessions the new machine wins move). Allow slack.
	if frac < 0.25 || frac > 0.42 {
		t.Fatalf("reshuffle fraction %.3f, want ≈0.33 (HRW); modulo would be ≈0.67", frac)
	}
}

func newRoute(self string, peers []string) *Resolver {
	r := New("myapp", self, 1<<62) // huge ttl so the cache never refreshes
	if peers != nil {
		r.cached = peers
		// fetchedAt zero + huge ttl: time.Since(0) < ttl is true, cache used.
	}
	return r
}

func TestRouteServeCases(t *testing.T) {
	req := func(hdr http.Header) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "/ws", nil)
		if hdr != nil {
			r.Header = hdr
		}
		return r.WithContext(context.Background())
	}
	sid := wire.SessionID{9}

	// Off Fly (self=="") → serve.
	if newRoute("", nil).Route(sid, req(nil)).Replay {
		t.Fatal("off-Fly should serve here")
	}
	// Single machine → serve.
	if newRoute("m-aaa", []string{"m-aaa"}).Route(sid, req(nil)).Replay {
		t.Fatal("single machine should serve here")
	}
	// Already replayed (Fly-Replay-Src present) → serve.
	r := newRoute("m-aaa", []string{"m-aaa", "m-zzz"})
	if r.Route(sid, req(http.Header{"Fly-Replay-Src": {"instance=m-zzz"}})).Replay {
		t.Fatal("replayed request should serve here (loop guard)")
	}
}

func TestRouteReplaysToOwner(t *testing.T) {
	peers := []string{"m-aaa", "m-bbb", "m-ccc"}
	sid := wire.SessionID{42}
	owner := ownerOf(sid, peers)
	// Pick a self that is NOT the owner.
	var notOwner string
	for _, p := range peers {
		if p != owner {
			notOwner = p
			break
		}
	}
	r := newRoute(notOwner, peers)
	req, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	res := r.Route(sid, req.WithContext(context.Background()))
	if !res.Replay {
		t.Fatal("non-owner should replay")
	}
	if got := res.Header.Get("Fly-Replay"); got != "instance="+owner {
		t.Fatalf("Fly-Replay = %q, want instance=%s", got, owner)
	}
	// And the owner itself serves.
	if newRoute(owner, peers).Route(sid, req.WithContext(context.Background())).Replay {
		t.Fatal("owner should serve here")
	}
}

// parseVMs must handle Fly's real vms.<app>.internal TXT shape: comma-separated
// entries, each "<machine_id> <region>", usually in one record.
func TestParseVMs(t *testing.T) {
	cases := []struct {
		name string
		txts []string
		want []string
	}{
		{"single record, two entries",
			[]string{"811d5d2f471098 jnb,8254dea7ed4458 jnb"},
			[]string{"811d5d2f471098", "8254dea7ed4458"}},
		{"one machine",
			[]string{"aaa111 jnb"},
			[]string{"aaa111"}},
		{"separate records",
			[]string{"aaa111 jnb", "bbb222 ord"},
			[]string{"aaa111", "bbb222"}},
		{"empty", nil, nil},
	}
	for _, tc := range cases {
		got := parseVMs(tc.txts)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}
