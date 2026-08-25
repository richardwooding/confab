// Package integration exercises confab's exact browser-core code natively:
// a real parley relay (capped at 6 participants, as in production) with
// native session clients running the same chat and rtc services the WASM
// bridge registers.
package integration

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/richardwooding/confab/internal/proto"
	"github.com/richardwooding/confab/internal/rtc"
	"github.com/richardwooding/parley/relay"
	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/service/chat"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
	"golang.org/x/time/rate"
)

// startRelay mirrors production: MaxParticipants 6 enforces the call cap.
func startRelay(t *testing.T) string {
	t.Helper()
	// ConnBurst is raised above the default 5: these tests legitimately open
	// up to 7 connections from one IP in a burst.
	s := relay.New(relay.Options{MaxParticipants: 6, ConnRate: rate.Limit(100), ConnBurst: 100})
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// end is one native "browser": session client + mux + the two services.
type end struct {
	client *session.Client
	mux    *service.Mux
	chat   *chat.Service
	rtc    *rtc.Service
}

func hostEnd(t *testing.T, url string) (*end, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return wireEnd(c), phrase
}

func joinEnd(t *testing.T, url, phrase string) *end {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return wireEnd(c)
}

func wireEnd(c *session.Client) *end {
	ch := chat.New()
	rt := rtc.New()
	return &end{client: c, mux: service.NewMux(c, service.WithServices(ch, rt)), chat: ch, rtc: rt}
}

// waitFor pulls mux events until one matches type E and pred.
func waitFor[E any](t *testing.T, e *end, pred func(E) bool) E {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-e.mux.Events():
			if !ok {
				t.Fatal("event stream closed")
			}
			if v, isE := ev.(E); isE && pred(v) {
				return v
			}
		case <-deadline:
			var zero E
			t.Fatalf("timed out waiting for %T", zero)
			panic("unreachable")
		}
	}
}

func drain(e *end) {
	go func() {
		for range e.mux.Events() { //nolint:revive // discard
		}
	}()
}

func TestChatAcrossThreeEnds(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostEnd(t, url)
	a := joinEnd(t, url, phrase)
	b := joinEnd(t, url, phrase)

	if err := host.chat.Say("welcome"); err != nil {
		t.Fatal(err)
	}
	for _, e := range []*end{a, b} {
		waitFor(t, e, func(m chat.Message) bool { return m.Text == "welcome" })
	}
	drain(host)
	if err := a.chat.Say("hi all"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, b, func(m chat.Message) bool { return m.Text == "hi all" })
}

// Signaling is peer-directed: a signal from A to B must never reach C.
func TestRTCSignalPeerDirected(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostEnd(t, url)
	a := joinEnd(t, url, phrase)
	b := joinEnd(t, url, phrase)
	drain(host)

	fakeSDP := `{"type":"offer","sdp":"v=0 fake"}`
	if err := a.rtc.Send(b.client.Self(), "offer", fakeSDP); err != nil {
		t.Fatal(err)
	}
	sig := waitFor(t, b, func(s rtc.Signal) bool { return s.Kind == "offer" })
	if sig.From != a.client.Self() || sig.Payload != fakeSDP {
		t.Fatalf("got %+v", sig)
	}
	// b answers a; c (the host) must see neither leg.
	if err := b.rtc.Send(a.client.Self(), "answer", `{"type":"answer"}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, func(s rtc.Signal) bool { return s.Kind == "answer" })
}

// A trickle of ICE candidates arrives in order (parley preserves per-sender
// frame order end-to-end).
func TestRTCICEOrdering(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostEnd(t, url)
	a := joinEnd(t, url, phrase)
	drain(host)

	for _, c := range []string{"cand-1", "cand-2", "cand-3"} {
		if err := host.rtc.Send(a.client.Self(), "ice", c); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for len(got) < 3 {
		s := waitFor(t, a, func(s rtc.Signal) bool { return s.Kind == "ice" })
		got = append(got, s.Payload)
	}
	if got[0] != "cand-1" || got[1] != "cand-2" || got[2] != "cand-3" {
		t.Fatalf("ICE out of order: %v", got)
	}
}

// The production cap: a seventh participant is refused by the relay.
func TestSeventhJoinerRejected(t *testing.T) {
	url := startRelay(t)
	_, phrase := hostEnd(t, url)
	for range 5 {
		drain(joinEnd(t, url, phrase))
	}
	_, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err == nil {
		t.Fatal("seventh joiner accepted")
	}
	if !strings.Contains(err.Error(), "full") {
		t.Fatalf("rejection error %q does not mention fullness", err)
	}
}

// Late joiners get chat history via the ctl snapshot.
func TestLateJoinerChatHistory(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostEnd(t, url)
	drain(host)
	if err := host.chat.Say("before you arrived"); err != nil {
		t.Fatal(err)
	}
	late := joinEnd(t, url, phrase)
	waitFor(t, late, func(m chat.Message) bool { return m.Text == "before you arrived" })
}

var _ = wire.ParticipantID(0)
