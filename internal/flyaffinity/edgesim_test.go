package flyaffinity_test

// A local, no-Fly proof of the affinity contract end to end:
//   - a relay node with a single-machine peer view serves a real session
//     (Router present, serve-here) — Host/Join pair over a live WebSocket;
//   - for that real session's SessionID, the two-machine Resolvers agree on an
//     owner, the non-owner REPLAYS to the owner (Fly-Replay: instance=<owner>),
//     and a raw ?s= dial to a non-owner node is refused with 503 (no upgrade);
//   - a request already carrying Fly-Replay-Src is served (one-hop loop guard).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/richardwooding/confab/internal/flyaffinity"
	"github.com/richardwooding/confab/internal/proto"
	"github.com/richardwooding/parley/phrase"
	"github.com/richardwooding/parley/relay"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

func relayNode(t *testing.T, self string, peers []string) *httptest.Server {
	t.Helper()
	res := flyaffinity.New("simapp", self, time.Hour)
	res.SetPeersForTest(peers)
	r := relay.New(relay.Options{Router: res.Route})
	t.Cleanup(r.Close)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func TestEdgeSimAffinityEndToEnd(t *testing.T) {
	peers := []string{"m-1", "m-2"}

	// A serve-here node (single-machine peer view) hosts a real session so we
	// have a genuine phrase/SessionID to route on.
	home := relayNode(t, "m-1", []string{"m-1"})
	url := "ws" + strings.TrimPrefix(home.URL, "http")
	host, ph, err := session.Host(context.Background(), url, proto.Options()...)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	defer func() { _ = host.Close() }()
	joiner, err := session.Join(context.Background(), url, ph, proto.Options()...)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer func() { _ = joiner.Close() }()
	sid := phrase.SessionID(proto.Label, ph)

	// Both machines' Resolvers must agree on the owner; the non-owner replays.
	owner := ""
	for _, id := range peers {
		res := flyaffinity.New("simapp", id, time.Hour)
		res.SetPeersForTest(peers)
		req, _ := http.NewRequest(http.MethodGet, "/ws", nil)
		if !res.Route(sid, req.WithContext(context.Background())).Replay {
			owner = id
		}
	}
	if owner == "" {
		t.Fatal("no machine claims ownership")
	}
	other := "m-1"
	if owner == "m-1" {
		other = "m-2"
	}
	nonOwner := flyaffinity.New("simapp", other, time.Hour)
	nonOwner.SetPeersForTest(peers)
	req, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	rr := nonOwner.Route(sid, req.WithContext(context.Background()))
	if !rr.Replay || rr.Header.Get("Fly-Replay") != "instance="+owner {
		t.Fatalf("non-owner (%s) should replay to %s, got %+v", other, owner, rr)
	}

	// A raw ?s= dial to a node that does NOT own this session is refused 503
	// (the affinity hand-off), with no upgrade.
	nonOwnerSrv := relayNode(t, other, peers)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialURL := "ws" + strings.TrimPrefix(nonOwnerSrv.URL, "http") + "?" + wire.SessionParam + "=" + sid.Hex()
	if conn, resp, derr := websocket.Dial(ctx, dialURL, nil); derr == nil {
		_ = conn.CloseNow()
		t.Fatal("dial to non-owner upgraded; expected a 503 replay")
	} else if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("non-owner dial status = %v, want 503", resp)
	}

	// Loop guard: an already-replayed request is served here even by a non-owner.
	req2, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	req2.Header.Set("Fly-Replay-Src", "instance="+owner)
	if nonOwner.Route(sid, req2.WithContext(context.Background())).Replay {
		t.Fatal("a Fly-Replay-Src request must be served (one-hop guard), not replayed again")
	}
}
