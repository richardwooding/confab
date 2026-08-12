package proto

import (
	"encoding/hex"
	"testing"

	"github.com/richardwooding/parley/phrase"
)

// Pin the derivation under confab's label. Session IDs are shared between
// independently built clients and the hosted relay; changing this constant
// knowingly is a protocol version bump, not a refactor.
func TestSessionIDGolden(t *testing.T) {
	got := phrase.SessionID(Label, "lion-42-maple")
	if h := hex.EncodeToString(got[:]); h != "49e62cc537bf6a6032a4021da5b2e8be" {
		t.Fatalf("session-ID derivation changed: %s", h)
	}
}
