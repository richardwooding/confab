// Package rtc is confab's WebRTC signaling service: an opaque, peer-directed
// router for SDP offers/answers and ICE candidates over the encrypted parley
// session. The Go side never parses a payload — WebRTC lives entirely in the
// browser; this service only guarantees that signals travel end-to-end
// encrypted and arrive attributed to an authenticated participant. The relay
// sees none of it (SDP and ICE leak IPs and media topology).
//
// The service is deliberately ephemeral: no state, no snapshot. A late
// joiner initiates fresh peer connections, so there is nothing to catch up.
package rtc

import (
	"fmt"

	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/wire"
)

// ID is the service identifier on the wire.
const ID = "rtc"

// maxPayload keeps one signal well inside parley's 64KiB frame budget; a
// full SDP with bundled m-lines is single-digit KiB.
const maxPayload = 32 * 1024

// Wire kinds. Kind 5 is reserved for media-state fanout (mute/camera
// badges) post-MVP.
const (
	kindOffer  uint8 = 1
	kindAnswer uint8 = 2
	kindICE    uint8 = 3
	kindBye    uint8 = 4 // explicit per-pair hangup (retry flow); the roster covers full leaves
)

type signal struct {
	Kind    uint8  `cbor:"1,keyasint"`
	Payload string `cbor:"2,keyasint,omitempty"` // opaque JSON; never parsed here
}

// Signal is the mux event handed to the UI layer for every incoming
// signaling message. Payload is untrusted JSON: feed it only to
// setRemoteDescription/addIceCandidate, never the DOM.
type Signal struct {
	From    wire.ParticipantID
	Kind    string // "offer" | "answer" | "ice" | "bye"
	Payload string
}

var kinds = map[string]uint8{
	"offer": kindOffer, "answer": kindAnswer, "ice": kindICE, "bye": kindBye,
}

var kindNames = map[uint8]string{
	kindOffer: "offer", kindAnswer: "answer", kindICE: "ice", kindBye: "bye",
}

// Service implements service.Service. It is stateless beyond its Context.
type Service struct {
	service.Base
}

// New constructs the signaling service.
func New() *Service { return &Service{} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.SetContext(ctx) }

// Send routes one signaling message to a single peer. Signaling is always
// peer-directed — never broadcast — so a mesh renegotiation between two
// participants is invisible to the rest of the call.
func (s *Service) Send(to wire.ParticipantID, kind, payload string) error {
	k, ok := kinds[kind]
	if !ok {
		return fmt.Errorf("rtc: unknown kind %q", kind)
	}
	if len(payload) > maxPayload {
		return fmt.Errorf("rtc: payload exceeds %d bytes", maxPayload)
	}
	if to == 0 || to == s.Ctx().Self {
		return fmt.Errorf("rtc: bad recipient %d", to)
	}
	body, err := wire.Marshal(signal{Kind: k, Payload: payload})
	if err != nil {
		return err
	}
	return s.Ctx().Send.SendTo(to, ID, body)
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[signal](body)
	if err != nil {
		return fmt.Errorf("rtc: %w", err)
	}
	name, ok := kindNames[m.Kind]
	if !ok {
		return nil // forward compat: a newer peer sent a kind we don't know
	}
	if len(m.Payload) > maxPayload {
		return fmt.Errorf("rtc: oversized signal from %d", from)
	}
	s.Ctx().Emit(Signal{From: from, Kind: name, Payload: m.Payload})
	return nil
}

// Snapshot is nil by design: signaling is ephemeral and late joiners
// initiate fresh peer connections.
func (s *Service) Snapshot() ([]byte, error) { return nil, nil }

// Restore is a no-op for the same reason.
func (s *Service) Restore([]byte) error { return nil }
