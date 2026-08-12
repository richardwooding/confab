// confab service worker: fast repeat loads, never stale deploys, and the
// relay WebSocket is never intercepted. No push handling — confab has none.
const CACHE = "confab-shell-v1";
const SHELL = [
  "/", "/index.html", "/app.js", "/call.js", "/style.css",
  "/gloam.css", "/gloam.js", "/wasm_exec.js", "/manifest.json", "/favicon.svg",
];

self.addEventListener("install", (ev) => {
  ev.waitUntil(
    caches.open(CACHE)
      .then((c) => Promise.allSettled(SHELL.map((u) => c.add(u))))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (ev) => {
  ev.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

async function networkFirst(req) {
  try {
    const res = await fetch(req);
    const c = await caches.open(CACHE);
    c.put(req, res.clone());
    return res;
  } catch {
    return (await caches.match(req)) || (await caches.match("/"));
  }
}

async function staleWhileRevalidate(req) {
  const cached = await caches.match(req);
  const fetching = fetch(req)
    .then(async (res) => {
      const c = await caches.open(CACHE);
      c.put(req, res.clone());
      return res;
    })
    .catch(() => cached);
  return cached || fetching;
}

self.addEventListener("fetch", (ev) => {
  const url = new URL(ev.request.url);
  if (ev.request.method !== "GET" || url.origin !== location.origin) return;
  if (url.pathname === "/ws") return; // the relay socket is sacred
  if (ev.request.mode === "navigate" || url.pathname === "/confab.wasm") {
    ev.respondWith(networkFirst(ev.request));
    return;
  }
  ev.respondWith(staleWhileRevalidate(ev.request));
});
