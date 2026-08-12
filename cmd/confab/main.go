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
	"github.com/richardwooding/parley/relay"
)

// maxCallSize is the participant cap per call. Full-mesh WebRTC bandwidth
// grows quadratically, so the relay clamps every session at six.
const maxCallSize = 6

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
