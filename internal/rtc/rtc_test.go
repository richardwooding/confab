package rtc

import (
	"strings"
	"testing"

	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/wire"
)

// capture records SendTo calls so tests can assert routing without a relay.
type capture struct {
	to      wire.ParticipantID
	service string
	body    []byte
}

func (c *capture) Broadcast(serviceID string, body []byte) error {
	c.service, c.body = serviceID, body
	return nil
}

func (c *capture) SendTo(to wire.ParticipantID, serviceID string, body []byte) error {
	c.to, c.service, c.body = to, serviceID, body
	return nil
}

func attach(t *testing.T, self wire.ParticipantID) (*Service, *capture, *[]any) {
	t.Helper()
	s := New()
	cap := &capture{}
	var events []any
	s.Attach(service.Context{Send: cap, Emit: func(e any) { events = append(events, e) }, Self: self, HostID: 1})
	return s, cap, &events
}

func TestSendRoutesPeerDirected(t *testing.T) {
	s, cap, _ := attach(t, 1)
	if err := s.Send(2, "offer", `{"sdp":"..."}`); err != nil {
		t.Fatal(err)
	}
	if cap.to != 2 || cap.service != ID {
		t.Fatalf("routed to %d/%s, want 2/%s", cap.to, cap.service, ID)
	}
}

func TestSendValidation(t *testing.T) {
	s, _, _ := attach(t, 1)
	if err := s.Send(2, "bogus", "x"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := s.Send(0, "offer", "x"); err == nil {
		t.Fatal("recipient 0 accepted")
	}
	if err := s.Send(1, "offer", "x"); err == nil {
		t.Fatal("self-send accepted")
	}
	if err := s.Send(2, "ice", strings.Repeat("x", maxPayload+1)); err == nil {
		t.Fatal("oversized payload accepted")
	}
}

// The payload must pass through byte-identically — the Go layer never
// normalizes or parses SDP.
func TestOpaquePassthrough(t *testing.T) {
	sender, cap, _ := attach(t, 1)
	payload := `{"type":"offer","sdp":"v=0\r\no=- 42 2 IN IP4 127.0.0.1\r\n"}`
	if err := sender.Send(2, "offer", payload); err != nil {
		t.Fatal(err)
	}
	receiver, _, events := attach(t, 2)
	if err := receiver.HandleFrame(1, cap.body); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 {
		t.Fatalf("%d events, want 1", len(*events))
	}
	sig := (*events)[0].(Signal)
	if sig.From != 1 || sig.Kind != "offer" || sig.Payload != payload {
		t.Fatalf("got %+v", sig)
	}
}

// Unknown wire kinds from newer peers are ignored, not errors.
func TestUnknownKindIgnored(t *testing.T) {
	s, _, events := attach(t, 2)
	body, err := wire.Marshal(signal{Kind: 99, Payload: "future"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HandleFrame(1, body); err != nil {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatal("unknown kind emitted an event")
	}
}
