# CLAUDE.md

## What this is

confab is croc-style pairing for video calls: a host gets a code phrase,
others join with it. Built on github.com/richardwooding/parley — the phrase
seeds a PAKE handshake and ALL signaling (WebRTC SDP/ICE) plus chat travels
end-to-end encrypted through a blind relay. Media is full-mesh peer-to-peer
WebRTC (DTLS-SRTP), never touching the server. Calls cap at 6 (relay
enforces MaxParticipants).

## Commands

```sh
make test     # go test -race ./...
make wasm     # build browser core into web/dist (+ gloam assets + wasm_exec.js)
make serve    # make wasm && go run ./cmd/confab → http://localhost:8080
make lint     # go vet + golangci-lint
GOOS=js GOARCH=wasm go build -o /dev/null ./cmd/confab-wasm   # WASM check (CI runs this)
```

## Architecture and invariants

- **The relay is blind** (parley/relay at /ws). It sees session IDs and
  opaque frames. There is no server-side call logic anywhere; media never
  reaches the server at all.
- **Zero `syscall/js` outside `cmd/confab-wasm`.** internal/{proto,rtc}
  compile natively AND to WASM; internal/integration exercises the exact
  code the browser runs against a real relay.
- **Protocol label**: every session.Host/Join passes `proto.Options()`
  (label "confab/v1", parley default roles — everyone is an equal Member;
  the UI shows no role distinction). internal/proto's golden test pins the
  session-ID derivation.
- **rtc service** (internal/rtc): opaque peer-directed signal router —
  kinds offer/answer/ice/bye, payloads never parsed in Go, no snapshot
  (late joiners initiate fresh connections). Kind 5 reserved for
  media-state fanout.
- **JS owns WebRTC**: cmd/confab-wasm bridges exactly two functions
  (`confab_send` UI→core, `confabOnEvent` core→UI, JSON at the bridge
  only); web/src/call.js holds RTCPeerConnection/mesh/perfect-negotiation
  (newcomer initiates, higher id is polite), app.js is the router. The JS
  layer never implements protocol.
- **Relay drops don't touch media**: on session.reconnecting the JS layer
  leaves peer connections alone (P2P survives); on session.resumed it
  restartIce()s any failed ones.
- **web/dist is generated** (`make wasm`), embedded via web/embed.go.
  gloam.css/gloam.js are vendored in web/src and refreshed by
  .github/workflows/gloam-sync.yml — don't hand-edit them.

## Releasing and deploy

Tag push (vX.Y.Z) triggers goreleaser: binaries + ghcr.io/richardwooding/
confab image (ko, web client embedded). Hosted at confab.fly.dev — exactly
ONE always-on machine (in-memory relay; never enable auto-stop or scale
past 1). Deploys drop relay connections mid-call: media keeps flowing P2P
and clients auto-resume signaling (reconnect = resume).
