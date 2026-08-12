//go:build js && wasm

// Command confab-wasm is the browser core: it owns the parley session
// (pairing, crypto, reconnect), the chat and rtc services, and exposes
// exactly two functions to JavaScript — window.confab_send(json) for
// UI→core commands and window.confabOnEvent(json) for core→UI events.
// JSON at the bridge only; CBOR on the wire. The JS layer owns all WebRTC
// objects and rendering — it never implements protocol.
//
// This is the ONLY file in the module allowed to import syscall/js: the
// rest compiles natively too, so the integration tests exercise the exact
// code the browser runs.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"syscall/js"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/richardwooding/confab/internal/proto"
	"github.com/richardwooding/confab/internal/rtc"
	"github.com/richardwooding/parley/service"
	"github.com/richardwooding/parley/service/chat"
	"github.com/richardwooding/parley/session"
)

// command is the single UI→core message shape; unused fields stay empty.
type command struct {
	Type    string `json:"type"`
	Phrase  string `json:"phrase,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	To      uint32 `json:"to,omitempty"`      // rtc.signal recipient
	Kind    string `json:"kind,omitempty"`    // offer|answer|ice|bye
	Payload string `json:"payload,omitempty"` // opaque SDP/ICE JSON
}

// app is the core's mutable state: one live session at a time, guarded by a
// generation counter so a superseded session's goroutines retire quietly.
type app struct {
	mu     sync.Mutex
	gen    int
	client *session.Client
	mux    *service.Mux
	chat   *chat.Service
	rtc    *rtc.Service
}

var current app

func main() {
	js.Global().Set("confab_send", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 {
			return nil
		}
		go dispatch(args[0].String())
		return nil
	}))
	emit("core.ready", map[string]any{})
	select {}
}

func emit(typ string, fields map[string]any) {
	fields["type"] = typ
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	js.Global().Call("confabOnEvent", string(b))
}

func emitError(msg string) { emit("error", map[string]any{"message": msg}) }

var commands = map[string]func(command){
	"create":     func(c command) { create(c.Name) },
	"join":       func(c command) { join(c.Phrase, c.Name) },
	"leave":      func(command) { leave() },
	"chat.say":   func(c command) { say(c.Text) },
	"rtc.signal": func(c command) { rtcSignal(c.To, c.Kind, c.Payload) },
}

func dispatch(raw string) {
	var cmd command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		emitError("bad command: " + err.Error())
		return
	}
	h, ok := commands[cmd.Type]
	if !ok {
		emitError("unknown command " + cmd.Type)
		return
	}
	h(cmd)
}

// relayURL derives the WebSocket endpoint from the page's own origin.
func relayURL() string {
	loc := js.Global().Get("location")
	scheme := "ws"
	if loc.Get("protocol").String() == "https:" {
		scheme = "wss"
	}
	return scheme + "://" + loc.Get("host").String() + "/ws"
}

func shareURL(phrase string) string {
	loc := js.Global().Get("location")
	return loc.Get("protocol").String() + "//" + loc.Get("host").String() + "/#" + phrase
}

func create(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, phrase, err := session.Host(ctx, relayURL(), proto.Options()...)
	if err != nil {
		emitError("couldn't start a call: " + err.Error())
		return
	}
	start(client, name)
	url := shareURL(phrase)
	png, err := qrcode.Encode(url, qrcode.Medium, 220)
	qr := ""
	if err == nil {
		qr = base64.StdEncoding.EncodeToString(png)
	}
	emit("session.created", map[string]any{
		"phrase": phrase, "url": url, "qr": qr, "self": uint32(client.Self()),
	})
}

func join(phrase, name string) {
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		emitError("enter a code phrase")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := session.Join(ctx, relayURL(), phrase, proto.Options()...)
	if err != nil {
		msg := "couldn't join: " + err.Error()
		switch {
		case strings.Contains(err.Error(), "not found"):
			msg = "no call with that phrase — check for typos"
		case strings.Contains(err.Error(), "unwrap"):
			msg = "wrong phrase"
		case strings.Contains(err.Error(), "full"):
			msg = "this call is full (6 people max)"
		}
		emitError(msg)
		return
	}
	start(client, name)
	emit("session.joined", map[string]any{"self": uint32(client.Self())})
}

// start wires a fresh session into the app under a new generation.
func start(client *session.Client, name string) {
	ch := chat.New()
	rt := rtc.New()
	mux := service.NewMux(client, service.WithServices(ch, rt))
	if name = strings.TrimSpace(name); name != "" {
		mux.SetName(name)
	}
	mux.SetReconnectable()

	closePrev()
	current.mu.Lock()
	current.gen++
	myGen := current.gen
	current.client = client
	current.mux = mux
	current.chat = ch
	current.rtc = rt
	current.mu.Unlock()
	go pump(mux, myGen)
}

// closePrev retires any live session: bump the generation FIRST so a running
// pump sees itself superseded, then tear down.
func closePrev() {
	current.mu.Lock()
	current.gen++
	client := current.client
	mux := current.mux
	current.client, current.mux, current.chat, current.rtc = nil, nil, nil, nil
	current.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if mux != nil {
		mux.Close()
	}
}

func leave() { closePrev() }

func isCurrent(gen int) bool {
	current.mu.Lock()
	defer current.mu.Unlock()
	return gen == current.gen
}

// pump drains the mux's merged event stream into JS events until the
// session ends or is superseded.
func pump(mux *service.Mux, gen int) {
	for ev := range mux.Events() {
		switch e := ev.(type) {
		case service.Roster:
			members := map[string]string{}
			for id := range e.Members {
				members[jsUint(id)] = e.Names[id]
			}
			emit("roster", map[string]any{"members": members})
		case chat.Message:
			emit("chat.msg", map[string]any{"from": uint32(e.From), "text": e.Text})
		case rtc.Signal:
			emit("rtc.signal", map[string]any{
				"from": uint32(e.From), "kind": e.Kind, "payload": e.Payload,
			})
		case service.Promoted:
			emit("session.promoted", map[string]any{"self": uint32(e.Self)})
		case service.ServiceError:
			emitError(e.Service + ": " + e.Err.Error())
		case service.SessionEvent:
			if closed, ok := e.Event.(session.Closed); ok {
				if !pumpClosed(mux, gen, closed.Reason) {
					return
				}
			}
		}
	}
}

// pumpClosed handles a session drop: reconnect network losses, surface
// everything else. Reports whether the pump should keep running (resumed).
func pumpClosed(mux *service.Mux, gen int, reason string) bool {
	if !isCurrent(gen) {
		return false // superseded by leave() or a new session
	}
	if reason == "connection lost" && reconnectNet(mux, gen) {
		return true
	}
	if isCurrent(gen) {
		emit("session.closed", map[string]any{"reason": reason})
	}
	return false
}

// reconnectNet retries the relay connection with capped backoff, resuming
// the same session (same participant id, same keys) on success. Media keeps
// flowing peer-to-peer throughout — only signaling and chat pause.
func reconnectNet(mux *service.Mux, gen int) bool {
	emit("session.reconnecting", map[string]any{})
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 40; attempt++ {
		if !isCurrent(gen) {
			return false
		}
		current.mu.Lock()
		client := current.client
		current.mu.Unlock()
		if client == nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := client.Reconnect(ctx)
		cancel()
		if err == nil {
			mux.Rebind(client)
			emit("session.resumed", map[string]any{})
			return true
		}
		time.Sleep(backoff)
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
	return false
}

func say(text string) {
	current.mu.Lock()
	ch := current.chat
	current.mu.Unlock()
	if ch == nil {
		emitError("not in a call")
		return
	}
	if err := ch.Say(text); err != nil {
		emitError(err.Error())
	}
}

func rtcSignal(to uint32, kind, payload string) {
	current.mu.Lock()
	rt := current.rtc
	current.mu.Unlock()
	if rt == nil {
		emitError("not in a call")
		return
	}
	if err := rt.Send(wireID(to), kind, payload); err != nil {
		emitError(err.Error())
	}
}
