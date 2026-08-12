// app.js — the shell router. Owns views, state, chat UI, and the bridge to
// the WASM core (window.confab_send / window.confabOnEvent). WebRTC lives in
// call.js; this file only routes rtc.signal events to it. The JS layer never
// implements protocol.
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const views = { home: $("view-home"), preflight: $("view-preflight"), call: $("view-call") };

  const state = {
    self: 0,
    names: {},          // idString -> name
    memberIds: [],      // numeric ids incl. self
    phrase: "",
    url: "",
    mode: "",           // "create" | "join" (what preflight leads to)
    inCall: false,
    chatOpen: false,
    unread: 0,
    mic: true,
    cam: true,
  };

  function show(name) {
    for (const [k, v] of Object.entries(views)) v.classList.toggle("hidden", k !== name);
  }

  function send(obj) {
    if (window.confab_send) window.confab_send(JSON.stringify(obj));
  }

  let toastTimer = null;
  function toast(msg) {
    const el = $("toast");
    el.textContent = msg;
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.hidden = true; }, 4000);
  }

  function displayName(id) {
    return state.names[String(id)] || "#" + id;
  }

  // ---- navigation: home → preflight → call, mirrored on browser Back ----
  function pushCall() {
    if (!state.inCall) history.pushState({ k: "call" }, "");
    state.inCall = true;
  }
  function leaveToHome() {
    // Reload is the bulletproof reset: tears down the core, all peer
    // connections, and drops any invite #phrase.
    location.replace(location.pathname);
  }
  window.addEventListener("popstate", () => {
    if (!state.inCall) return; // hash-only navigations are handled below
    if (!confirm("Leave the call?")) {
      history.pushState({ k: "call" }, "");
      return;
    }
    leaveToHome();
  });

  // Opening an invite link while the app is already loaded is a
  // same-document navigation (only the #phrase changes): route it to
  // preflight exactly like a fresh load would.
  window.addEventListener("hashchange", () => {
    const invite = decodeURIComponent(location.hash.slice(1)).trim();
    if (!state.inCall && invite) {
      $("join-phrase").value = invite;
      preflight("join");
    }
  });

  // ---- preflight --------------------------------------------------------
  async function preflight(mode) {
    state.mode = mode;
    show("preflight");
    $("preflight-title").textContent = mode === "create" ? "Start your call" : "Ready to join?";
    $("btn-go").textContent = mode === "create" ? "Start call" : "Join call";
    $("pf-name").value = $("name").value || localStorage.getItem("confab-name") || "";
    const ctx = $("preflight-context");
    if (mode === "create") {
      ctx.textContent = "You'll get a fresh phrase to share.";
    } else {
      ctx.textContent = "Joining ";
      const ph = document.createElement("span");
      ph.className = "phrase-inline";
      ph.textContent = phraseValue();
      ctx.appendChild(ph);
    }
    const got = await window.confabCall.acquire();
    const status = $("preview-status");
    if (got === "av") {
      status.textContent = "camera and microphone ready";
      window.confabCall.previewInto($("preview"));
    } else if (got === "audio") {
      status.textContent = "no camera — you'll join with audio only";
    } else {
      status.textContent = "no mic or camera — you'll join to watch and chat";
    }
  }

  $("pf-mic").addEventListener("click", () => toggleMedia("mic", $("pf-mic")));
  $("pf-cam").addEventListener("click", () => toggleMedia("cam", $("pf-cam")));
  $("ctl-mic").addEventListener("click", () => toggleMedia("mic", $("ctl-mic")));
  $("ctl-cam").addEventListener("click", () => toggleMedia("cam", $("ctl-cam")));

  function toggleMedia(kind, btn) {
    const on = !(kind === "mic" ? state.mic : state.cam);
    if (kind === "mic") { state.mic = on; window.confabCall.setMic(on); }
    else { state.cam = on; window.confabCall.setCam(on); }
    for (const b of [kind === "mic" ? $("pf-mic") : $("pf-cam"), kind === "mic" ? $("ctl-mic") : $("ctl-cam")]) {
      b.setAttribute("aria-pressed", String(on));
      b.classList.toggle("off", !on);
    }
  }

  $("btn-go").addEventListener("click", () => {
    const name = $("pf-name").value.trim();
    localStorage.setItem("confab-name", name);
    if (state.mode === "create") send({ type: "create", name });
    else send({ type: "join", phrase: phraseValue(), name });
    $("btn-go").disabled = true;
  });
  $("btn-back-home").addEventListener("click", () => leaveToHome());

  // ---- call view ---------------------------------------------------------
  function enterCall(self) {
    state.self = self;
    show("call");
    pushCall();
    window.confabCall.init(
      self,
      (to, kind, payload) => send({ type: "rtc.signal", to, kind, payload }),
      () => updateCount(),
    );
    updateCount();
    $("btn-go").disabled = false;
  }

  function updateCount() {
    $("count").textContent = state.memberIds.length + "/6";
    $("invite-card").hidden = state.memberIds.length > 1 || !state.phrase;
  }

  $("ctl-leave").addEventListener("click", () => {
    if (confirm("Leave the call?")) {
      send({ type: "leave" });
      window.confabCall.teardown();
      leaveToHome();
    }
  });
  $("ctl-invite").addEventListener("click", () => {
    $("invite-card").hidden = !$("invite-card").hidden;
  });
  $("btn-copy-link").addEventListener("click", () => copy(state.url, "link copied"));
  $("btn-copy-phrase").addEventListener("click", () => copy(state.phrase, "phrase copied"));

  function copy(text, note) {
    navigator.clipboard.writeText(text).then(() => toast(note), () => toast("copy failed — select it manually"));
  }

  // ---- chat ---------------------------------------------------------------
  $("ctl-chat").addEventListener("click", () => {
    state.chatOpen = !state.chatOpen;
    $("chat-pane").hidden = !state.chatOpen;
    $("ctl-chat").setAttribute("aria-expanded", String(state.chatOpen));
    if (state.chatOpen) {
      state.unread = 0;
      $("chat-unread").hidden = true;
      $("chat-input").focus();
    }
  });
  $("chat-form").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const text = $("chat-input").value.trim();
    if (!text) return;
    send({ type: "chat.say", text });
    $("chat-input").value = "";
  });

  function chatLine(from, text) {
    const li = document.createElement("p");
    const who = document.createElement("b");
    who.textContent = from === state.self ? "you" : displayName(from);
    li.appendChild(who);
    li.appendChild(document.createTextNode(" " + text));
    $("chat-log").appendChild(li);
    $("chat-log").scrollTop = $("chat-log").scrollHeight;
    if (!state.chatOpen && from !== state.self) {
      state.unread++;
      $("chat-unread").textContent = String(state.unread);
      $("chat-unread").hidden = false;
    }
  }

  // ---- core events ---------------------------------------------------------
  const handlers = {
    "core.ready"(e) {
      $("btn-action").disabled = false;
      $("home-status").textContent = "";
      const invite = decodeURIComponent(location.hash.slice(1)).trim();
      if (invite) {
        // Invitees never see the start/join choice: straight to preflight.
        $("join-phrase").value = invite;
        preflight("join");
      }
    },
    "error"(e) {
      toast(e.message);
      $("btn-go").disabled = false;
    },
    "session.created"(e) {
      state.phrase = e.phrase;
      state.url = e.url;
      $("invite-phrase").textContent = e.phrase;
      if (e.qr) $("invite-qr").src = "data:image/png;base64," + e.qr;
      enterCall(e.self);
    },
    "session.joined"(e) {
      enterCall(e.self);
    },
    "roster"(e) {
      state.names = e.members;
      state.memberIds = Object.keys(e.members).map(Number);
      updateCount();
      const names = {};
      for (const [id, n] of Object.entries(e.members)) if (n) names[Number(id)] = n;
      window.confabCall.roster(state.memberIds, names);
    },
    "chat.msg"(e) { chatLine(e.from, e.text); },
    "rtc.signal"(e) {
      window.confabCall.signal(e.from, e.kind, e.payload, displayName(e.from));
    },
    "session.reconnecting"() { $("reconnect-banner").hidden = false; },
    "session.resumed"() {
      $("reconnect-banner").hidden = true;
      toast("signaling restored");
      window.confabCall.resumed();
    },
    "session.promoted"() { /* invisible to the UI by design */ },
    "session.closed"(e) {
      toast("call ended" + (e.reason ? ": " + e.reason : ""));
      window.confabCall.teardown();
      setTimeout(leaveToHome, 1500);
    },
  };

  window.confabOnEvent = (raw) => {
    let e;
    try { e = JSON.parse(raw); } catch { return; }
    const h = handlers[e.type];
    if (h) h(e);
  };

  // ---- home wiring -----------------------------------------------------------
  // phraseValue reads the live field, trimmed; pasting a full invite URL
  // still joins — everything after the last "#" is the phrase.
  function phraseValue() {
    let v = $("join-phrase").value.trim();
    const hash = v.lastIndexOf("#");
    if (hash !== -1) v = v.slice(hash + 1).trim();
    return v;
  }

  // The phrase field is the mode switch: one primary action, no wrong button.
  function syncAction() {
    $("btn-action").textContent = phraseValue() ? "Join this call" : "▶ Start a new call";
  }

  $("name").value = localStorage.getItem("confab-name") || "";
  $("join-phrase").addEventListener("input", syncAction);
  syncAction();
  $("btn-action").addEventListener("click", () => {
    // Mode is computed at click time from the live field, never cached.
    preflight(phraseValue() ? "join" : "create");
  });
  $("join-phrase").addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") $("btn-action").click();
  });

  fetch("/version").then((r) => r.text()).then((v) => { $("version-badge").textContent = "confab " + v; }).catch(() => {});

  // ---- boot the core ----------------------------------------------------------
  (async () => {
    if (typeof Go === "undefined") {
      $("home-status").textContent = "wasm_exec.js missing — run `make wasm`";
      return;
    }
    try {
      const go = new Go();
      const result = await WebAssembly.instantiateStreaming(fetch("confab.wasm"), go.importObject);
      go.run(result.instance);
    } catch (err) {
      $("home-status").textContent = "couldn't load the core: " + err;
    }
  })();

  // Best-effort clean goodbye on tab close/navigation: a graceful session
  // Close removes us from the call immediately instead of after the relay's
  // 30s reconnect grace. Fires-and-forgets into the core; if the page dies
  // first, the grace window covers it.
  window.addEventListener("pagehide", () => send({ type: "leave" }));

  if ("serviceWorker" in navigator) {
    window.addEventListener("load", () => {
      navigator.serviceWorker.register("service-worker.js").catch(() => {});
    });
  }
})();
