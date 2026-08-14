// confab service worker: the app shell is served NETWORK-FIRST so an online user
// always runs current code (the cache is only an offline fallback). This avoids
// stale/mixed asset versions — e.g. a fresh index.html paired with an old app.js
// — which can leave new UI wired to missing handlers. The relay WebSocket is
// never intercepted. No push handling — confab has none.
const CACHE = "confab-shell-v2";
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

// Network-first: fresh when online (updates the cache), fall back to cache
// (then the shell root) only when offline.
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

self.addEventListener("fetch", (ev) => {
  const url = new URL(ev.request.url);
  if (ev.request.method !== "GET" || url.origin !== location.origin) return;
  if (url.pathname === "/ws") return; // the relay socket is sacred
  ev.respondWith(networkFirst(ev.request)); // whole shell is network-first
});
