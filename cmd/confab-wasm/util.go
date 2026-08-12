//go:build js && wasm

package main

import (
	"strconv"

	"github.com/richardwooding/parley/wire"
)

func jsUint(id wire.ParticipantID) string { return strconv.FormatUint(uint64(id), 10) }

func wireID(id uint32) wire.ParticipantID { return wire.ParticipantID(id) }
