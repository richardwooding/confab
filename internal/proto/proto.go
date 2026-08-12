// Package proto pins confab's wire-protocol domain label and session
// options. Every parley entry point that derives keys or session IDs must
// receive this label — changing it is a protocol version bump: clients on
// different labels derive different session IDs and keys and cannot talk.
package proto

import "github.com/richardwooding/parley/session"

// Label is passed via Options to every session.Host / session.Join call.
const Label = "confab/v1"

// Options is the bundle every session.Host / session.Join call must pass.
// confab uses parley's default role policy — every participant is an equal
// RoleMember; "host" exists only as parley's snapshot-server and migration
// anchor and the UI shows no role distinction.
func Options() []session.Option {
	return []session.Option{session.WithProtocol(Label)}
}
