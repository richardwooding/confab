// Command confab is the call server. It serves the embedded web client at /
// and the WebSocket relay at /ws. The relay only ever forwards opaque
// encrypted frames between call participants — it can never read signaling
// (SDP/ICE) or chat, and media never touches it at all (WebRTC is
// peer-to-peer).
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/richardwooding/confab/web"
	"github.com/richardwooding/flyaffinity"
	"github.com/richardwooding/parley/dashboard"
	"github.com/richardwooding/parley/relay"
	"github.com/richardwooding/parley/wire"
)

// clusterToken derives the internal peer-stats auth token from the dashboard
// cookie key, so every machine computes the same value without a new secret.
func clusterToken(cookieKey []byte) []byte {
	m := hmac.New(sha256.New, cookieKey)
	_, _ = m.Write([]byte("parley-cluster-stats"))
	return m.Sum(nil)
}

// maxCallSize is the participant cap per call. Full-mesh WebRTC bandwidth
// grows quadratically, so the relay clamps every session at six.
const maxCallSize = 6

// dashboardConfigFromEnv reads the admin dashboard's config from environment
// (Fly secrets). It returns ok=false when any required value is missing, so
// the dashboard stays dormant unless fully configured — the repo and local
// builds carry no secrets and expose nothing.
func dashboardConfigFromEnv() (dashboard.Config, bool) {
	id := os.Getenv("DASHBOARD_GITHUB_CLIENT_ID")
	secret := os.Getenv("DASHBOARD_GITHUB_CLIENT_SECRET")
	key := os.Getenv("DASHBOARD_COOKIE_KEY")
	base := os.Getenv("DASHBOARD_BASE_URL")
	var users []string
	for _, u := range strings.Split(os.Getenv("DASHBOARD_ALLOW"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			users = append(users, u)
		}
	}
	if id == "" || secret == "" || len(key) < 16 || base == "" || len(users) == 0 {
		return dashboard.Config{}, false
	}
	return dashboard.Config{
		ClientID: id, ClientSecret: secret, CookieKey: []byte(key),
		Allow: users, BaseURL: base, AppName: "confab",
	}, true
}

// precompressed serves an embedded asset's brotli (.br) or gzip (.gz) sibling
// when the client accepts it, else falls back to the raw FileServer. This
// keeps the server a dumb static host while shipping the smallest bytes; the
// wasm core and the whole JS/CSS/HTML shell are precompressed at build time.
func precompressed(dist fs.FS, raw http.Handler) http.HandlerFunc {
	encs := []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			raw.ServeHTTP(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		ae := r.Header.Get("Accept-Encoding")
		for _, e := range encs {
			if !strings.Contains(ae, e.token) {
				continue
			}
			b, err := fs.ReadFile(dist, p+e.ext)
			if err != nil {
				continue
			}
			// Revalidate every load against a content ETag: an unchanged asset
			// costs a cheap 304, but a deploy is picked up immediately. The
			// embedded files carry no modtime, so without this the browser can
			// heuristically cache them and serve a stale shell after a deploy.
			etag := fmt.Sprintf("\"%x\"", sha256.Sum256(b))
			h := w.Header()
			h.Set("ETag", etag)
			h.Set("Cache-Control", "no-cache")
			h.Add("Vary", "Accept-Encoding")
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			h.Set("Content-Encoding", e.token)
			h.Set("Content-Type", contentType(p))
			_, _ = w.Write(b)
			return
		}
		raw.ServeHTTP(w, r)
	}
}

// contentType maps a served path to its media type (the precompressed sibling
// hides the real extension from net/http's sniffer, so we set it explicitly).
func contentType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".wasm":
		return "application/wasm"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json", ".webmanifest":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// version is stamped by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

// displayVersion is what the UI shows: "dev" as-is, otherwise "vX.Y.Z".
func displayVersion() string {
	if version == "dev" || version == "" {
		return "dev"
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func main() {
	listen := flag.String("listen", ":8080", "address to listen on")
	maxSessions := flag.Int("max-sessions", 1000, "maximum concurrent calls")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("confab", version)
		os.Exit(0)
	}
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatalf("embedded web client: %v", err)
	}

	mux := http.NewServeMux()
	files := http.FileServerFS(dist)
	mux.Handle("/", precompressed(dist, files))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, displayVersion())
	})
	// Session-affinity routing: when running on Fly with >1 machine, pin every
	// connection for a session to one machine (its in-memory relay is
	// authoritative for that shard). Off Fly / single machine → nil-safe
	// serve-here, identical to today.
	var aff *flyaffinity.Resolver
	var router func(wire.SessionID, *http.Request) relay.RouteResult
	if mid := os.Getenv("FLY_MACHINE_ID"); mid != "" {
		aff = flyaffinity.New(os.Getenv("FLY_APP_NAME"), mid, 7*time.Second)
		router = aff.Route
		log.Printf("fly affinity routing enabled (machine %s, app %s)", mid, os.Getenv("FLY_APP_NAME"))
	}
	relaySrv := relay.New(relay.Options{
		MaxSessions: *maxSessions, MaxParticipants: maxCallSize, Router: router,
	})
	defer relaySrv.Close()
	mux.Handle("/ws", relaySrv)

	mux.HandleFunc("/whoami", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "%s %s\n", os.Getenv("FLY_MACHINE_ID"), os.Getenv("FLY_REGION"))
	})

	// Admin dashboard (GitHub-OAuth-gated) — only wired up when its Fly
	// secrets are present, so it's dormant locally and in the public build.
	if cfg, ok := dashboardConfigFromEnv(); ok {
		src := dashboard.StatsSource(relaySrv)
		// Under multi-node, aggregate every machine's shard so the dashboard
		// shows the whole cluster. Peers authenticate with a token derived
		// from the shared cookie key — no new secret.
		if aff != nil {
			token := clusterToken(cfg.CookieKey)
			mux.Handle(dashboard.InternalStatsPath(), dashboard.InternalStatsHandler(relaySrv, token))
			src = dashboard.NewAggregator(relaySrv, aff.Peers, token)
		}
		dashboard.New(cfg, src).Register(mux)
		log.Printf("admin dashboard enabled at /dashboard (%d allowed user(s))", len(cfg.Allow))
	} else {
		log.Print("admin dashboard disabled (set DASHBOARD_* env vars to enable)")
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout/ReadTimeout: /ws connections are long-lived; the
		// relay enforces its own per-frame idle deadline.
	}
	log.Printf("confab %s listening on %s", version, *listen)
	log.Fatal(srv.ListenAndServe())
}
