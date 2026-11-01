package httpapi

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/ovander/tenure-api/internal/problem"
)

// webCSP is the policy of the embedded web app: its own scripts, styles,
// images and API calls only, no inline script, no framing (RENT-BE-001 §8b).
// The API keeps backendkit's default-src 'none'.
const webCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// staticApp serves the embedded tenure-web bundle for GET and HEAD outside
// /api and /bff: a file that exists is served as is (hashed assets under
// assets/ cached for a year), a path with an extension that does not exist is
// a 404, and any other path is a route of the single-page app and gets
// index.html, never cached. Everything else falls through to notFound.
func staticApp(files fs.FS, notFound http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if files == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			p == "/api" || strings.HasPrefix(p, "/api/") || p == "/bff" || strings.HasPrefix(p, "/bff/") {
			notFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+p), "/")
		if name == "" {
			name = "index.html"
		}
		if st, err := fs.Stat(files, name); err != nil || st.IsDir() {
			if path.Ext(name) != "" {
				notFound(w, r)
				return
			}
			name = "index.html"
		}
		f, err := files.Open(name)
		if err != nil {
			notFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()
		h := w.Header()
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		h.Set("Content-Security-Policy", webCSP)
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, f)
		}
	}
}

// problemNotFound is the 404 of every path no route and no file answers.
func problemNotFound(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, r, problem.NotFound, "", "", nil)
}
