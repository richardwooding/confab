// call.js — the WebRTC layer. Owns getUserMedia, one RTCPeerConnection per
// remote peer (full mesh, ≤5 remotes), the MDN perfect-negotiation pattern,
// and the video grid. Zero protocol: signaling travels through the WASM core
// (app.js routes rtc.signal events here and sends ours back). Pairing rule:
// the newcomer initiates (I offer to every existing peer, i.e. every id
// lower than mine); the higher id of each pair is the polite peer — the
// newcomer both initiates and yields on glare.
(() => {
  "use strict";

  const ICE = [
    { urls: "stun:stun.cloudflare.com:3478" },
    { urls: "stun:stun.l.google.com:19302" },
  ];

  let send = () => {};       // set by init: (to, kind, payload)
  let onCount = () => {};    // set by init: (remotePeers)
  let selfId = 0;
  let local = null;          // MediaStream (may be null: recvonly join)
  const peers = new Map();   // id -> peer

  const $ = (id) => document.getElementById(id);

  // --- media ------------------------------------------------------------

  async function acquire() {
    const want = {
      video: { width: { ideal: 1280 }, height: { ideal: 720 }, facingMode: "user" },
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    };
    try {
      local = await navigator.mediaDevices.getUserMedia(want);
      return "av";
    } catch {
      try {
        local = await navigator.mediaDevices.getUserMedia({ audio: want.audio });
        return "audio";
      } catch {
        local = null;
        return "none";
      }
    }
  }

  function setEnabled(kind, on) {
    if (!local) return false;
    for (const t of (kind === "audio" ? local.getAudioTracks() : local.getVideoTracks())) {
      t.enabled = on;
    }
    return true;
  }

  function stopLocal() {
    if (local) for (const t of local.getTracks()) t.stop();
    local = null;
  }

  // --- tiles ------------------------------------------------------------

  function tile(id, label, muted) {
    const el = document.createElement("div");
    el.className = "tile";
    el.id = "tile-" + id;
    el.innerHTML = '<video autoplay playsinline' + (muted ? " muted" : "") + '></video>' +
      '<span class="tag"></span><span class="badge" hidden></span>';
    el.querySelector(".tag").textContent = label;
    $("grid").appendChild(el);
    sizeGrid();
    return el;
  }

  function sizeGrid() {
    const n = $("grid").childElementCount;
    $("grid").dataset.n = Math.min(n, 6);
  }

  function badge(id, text, retry) {
    const el = document.getElementById("tile-" + id);
    if (!el) return;
    const b = el.querySelector(".badge");
    if (!text) { b.hidden = true; return; }
    b.hidden = false;
    b.textContent = text;
    b.onclick = null;
    if (retry) {
      b.textContent = text + " — tap to retry";
      b.style.cursor = "pointer";
      b.onclick = retry;
    }
  }

  // --- peers (perfect negotiation, MDN pattern) ---------------------------

  function bitrateFor(remotes) {
    if (remotes <= 2) return 900_000;
    if (remotes <= 4) return 500_000;
    return 350_000;
  }

  function applyBitrates() {
    const cap = bitrateFor(peers.size);
    for (const p of peers.values()) {
      for (const s of p.pc.getSenders()) {
        if (!s.track || s.track.kind !== "video") continue;
        const params = s.getParameters();
        params.encodings = params.encodings?.length ? params.encodings : [{}];
        params.encodings[0].maxBitrate = cap;
        s.setParameters(params).catch(() => {});
      }
    }
  }

  function newPeer(id, label) {
    const p = {
      pc: new RTCPeerConnection({ iceServers: ICE }),
      polite: selfId > id, // the newcomer (higher id) yields on glare
      makingOffer: false,
      ignoreOffer: false,
      settingRemoteAnswer: false,
      el: tile(id, label || "#" + id, false),
    };
    const pc = p.pc;
    if (local) for (const t of local.getTracks()) pc.addTrack(t, local);
    else { pc.addTransceiver("audio", { direction: "recvonly" }); pc.addTransceiver("video", { direction: "recvonly" }); }

    pc.ontrack = ({ streams }) => {
      const v = p.el.querySelector("video");
      if (streams[0] && v.srcObject !== streams[0]) v.srcObject = streams[0];
    };
    pc.onnegotiationneeded = async () => {
      try {
        p.makingOffer = true;
        await pc.setLocalDescription();
        send(id, "offer", JSON.stringify(pc.localDescription));
      } catch { /* peer gone mid-negotiation */ } finally {
        p.makingOffer = false;
      }
    };
    pc.onicecandidate = ({ candidate }) => send(id, "ice", JSON.stringify(candidate));
    pc.onconnectionstatechange = () => {
      const st = pc.connectionState;
      if (st === "connected") badge(id, "");
      else if (st === "connecting") badge(id, "connecting…");
      else if (st === "disconnected") badge(id, "reconnecting…");
      else if (st === "failed") badge(id, "failed", () => retryPeer(id, label));
    };
    peers.set(id, p);
    applyBitrates();
    onCount(peers.size);
    return p;
  }

  function retryPeer(id, label) {
    const p = peers.get(id);
    if (!p) return;
    p.pc.restartIce();
    setTimeout(() => {
      const cur = peers.get(id);
      if (!cur || cur.pc.connectionState !== "failed") return;
      send(id, "bye", "");
      dropPeer(id);
      newPeer(id, label); // fresh pc; onnegotiationneeded sends a new offer
    }, 10_000);
  }

  function dropPeer(id) {
    const p = peers.get(id);
    if (!p) return;
    p.pc.close();
    p.el.remove();
    peers.delete(id);
    sizeGrid();
    applyBitrates();
    onCount(peers.size);
  }

  async function onSignal(from, kind, payload, label) {
    if (kind === "bye") { dropPeer(from); return; }
    const p = peers.get(from) || newPeer(from, label); // elder side: lazy create
    const pc = p.pc;
    try {
      if (kind === "ice") {
        const cand = JSON.parse(payload);
        try { await pc.addIceCandidate(cand); } catch (err) { if (!p.ignoreOffer) throw err; }
        return;
      }
      const desc = JSON.parse(payload);
      const readyForOffer = !p.makingOffer &&
        (pc.signalingState === "stable" || p.settingRemoteAnswer);
      const collision = desc.type === "offer" && !readyForOffer;
      p.ignoreOffer = !p.polite && collision;
      if (p.ignoreOffer) return;
      p.settingRemoteAnswer = desc.type === "answer";
      await pc.setRemoteDescription(desc); // implicit rollback for the polite peer
      p.settingRemoteAnswer = false;
      if (desc.type === "offer") {
        await pc.setLocalDescription();
        send(from, "answer", JSON.stringify(pc.localDescription));
      }
    } catch (err) {
      console.warn("rtc: negotiation with", from, "failed:", err);
    }
  }

  // --- public surface -----------------------------------------------------

  window.confabCall = {
    acquire,          // -> "av" | "audio" | "none"
    previewInto(v) { if (local) v.srcObject = local; },
    hasVideo: () => !!local && local.getVideoTracks().length > 0,
    setMic: (on) => setEnabled("audio", on),
    setCam: (on) => setEnabled("video", on),

    init(id, sendFn, countFn) {
      selfId = id;
      send = sendFn;
      onCount = countFn;
      // Self tile: mirrored, muted, always first.
      const el = tile("self", "you", true);
      el.classList.add("self");
      if (local) el.querySelector("video").srcObject = local;
    },

    // Roster diff drives the mesh: I initiate to existing (lower-id) peers;
    // later joiners initiate to me; departures tear down.
    roster(ids, names) {
      for (const id of ids) {
        if (id === selfId || peers.has(id)) continue;
        if (id < selfId) newPeer(id, names[id]); // onnegotiationneeded → offer
      }
      for (const id of [...peers.keys()]) {
        if (!ids.includes(id)) dropPeer(id);
      }
      for (const [id, p] of peers) {
        const tag = p.el.querySelector(".tag");
        if (names[id]) tag.textContent = names[id];
      }
    },

    signal: onSignal,

    resumed() { // relay came back: kick any pc the outage broke
      for (const p of peers.values()) {
        if (p.pc.connectionState === "failed" || p.pc.connectionState === "disconnected") {
          p.pc.restartIce();
        }
      }
    },

    teardown() {
      for (const id of [...peers.keys()]) dropPeer(id);
      const self = document.getElementById("tile-self");
      if (self) self.remove();
      stopLocal();
    },
  };
})();
