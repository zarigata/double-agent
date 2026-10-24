package api

import (
	"embed"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// webAssets holds the Web UI of the control plane.
//
// The interface is embedded so that the daemon stays a single binary that can
// be dropped on a machine without a package manager.
//
//go:embed web
var webAssets embed.FS

// handleStatic serves the Web UI and the certificate authority.
//
// An unknown path that is not part of the API is answered with the entry
// point of the interface: the Web UI is a single page application and owns
// its own routes.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErrorStatus(w, r, http.StatusMethodNotAllowed, CodeInvalidRequest,
			"the method is not allowed for this endpoint", nil)
		return
	}
	if r.URL.Path == "/ca.crt" {
		s.serveCA(w, r)
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, r, CodeNotFound, "the endpoint does not exist", nil)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	data, err := webAssets.ReadFile(path.Join("web", name))
	if err != nil {
		data, err = webAssets.ReadFile("web/index.html")
		if err != nil {
			writeError(w, r, CodeInternal, "the web interface is not available", nil)
			return
		}
		name = "index.html"
	}
	header := w.Header()
	header.Set("Content-Type", contentTypeFor(name))
	header.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// serveCA sends the certificate authority of the node.
//
// A browser needs it to trust the local certificate of the daemon.
func (s *Server) serveCA(w http.ResponseWriter, r *http.Request) {
	caPath := s.cfg.Listen.TLS.CAFile
	if caPath == "" {
		caPath = filepath.Join(s.cfg.PKIPath(), "ca.crt")
	}
	data, err := os.ReadFile(caPath)
	if err != nil {
		writeError(w, r, CodeNotFound, "the certificate authority is not available", nil)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "application/x-x509-ca-cert")
	header.Set("Content-Disposition", `attachment; filename="double-agent-ca.crt"`)
	header.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// contentTypeFor guesses the content type of an embedded file.
func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".map":
		return "application/json"
	default:
		if guessed := mime.TypeByExtension(filepath.Ext(name)); guessed != "" {
			return guessed
		}
		return "application/octet-stream"
	}
}
