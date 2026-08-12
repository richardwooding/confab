# confab

Video calls that pair like [croc](https://github.com/schollz/croc) sends
files: the host gets a code phrase (`lion-42-maple`), everyone else joins
with it. No accounts, no links that outlive the call, no server that can
watch.

confab is built on [parley](https://github.com/richardwooding/parley) —
phrase-paired, end-to-end-encrypted group sessions over a blind relay.
parley carries the WebRTC *signaling* (SDP offers/answers and ICE
candidates travel as encrypted frames the relay cannot read — signaling
leaks IPs and media topology, so it deserves E2EE too). Media never touches
the server at all: calls are full-mesh peer-to-peer WebRTC, encrypted
DTLS-SRTP between browsers.

- **Up to 6 people** per call (full mesh; the relay enforces the cap)
- **Inline chat** on the same encrypted session, with history for late joiners
- **Reconnect-friendly**: if the relay connection drops, your call keeps
  running — media is peer-to-peer — and chat resumes when the relay returns
- **One binary** serves the web app and the relay; self-host it anywhere

## Run it

```sh
make serve          # build the browser core and serve on :8080
```

## Known limitations

- STUN-only NAT traversal (Cloudflare/Google public STUN). Most peers
  connect directly; symmetric-NAT pairs (some corporate networks) will not
  — those tiles show "failed". A TURN relay is the planned fix.
- Mesh bandwidth grows with participants; sender bitrates are tiered down
  as the call grows.

## Provenance

The session core (parley) and this app's architecture were extracted from
[kibitz](https://github.com/richardwooding/kibitz), the E2EE board-game
table. UI in the [gloam](https://github.com/richardwooding/gloam) design
system.

MIT licensed.
