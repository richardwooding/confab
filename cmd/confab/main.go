// Command confab is the call server. It serves the embedded web client at /
// and the WebSocket relay at /ws. The relay only ever forwards opaque
// encrypted frames between call participants — it can never read signaling
// (SDP/ICE) or chat, and media never touches it at all (WebRTC is
// peer-to-peer).
package main

import (
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
	"github.com/richardwooding/parley/dashboard"
	"github.com/richardwooding/parley/relay"
)

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
			h := w.Header()
			h.Set("Content-Encoding", e.token)
			h.Add("Vary", "Accept-Encoding")
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
	relaySrv := relay.New(relay.Options{MaxSessions: *maxSessions, MaxParticipants: maxCallSize})
	defer relaySrv.Close()
	mux.Handle("/ws", relaySrv)

	// Admin dashboard (GitHub-OAuth-gated) — only wired up when its Fly
	// secrets are present, so it's dormant locally and in the public build.
	if cfg, ok := dashboardConfigFromEnv(); ok {
		dashboard.New(cfg, relaySrv).Register(mux)
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
