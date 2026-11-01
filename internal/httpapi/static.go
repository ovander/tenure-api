package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/ovander/tenure-api/internal/problem"
)

// webCSP is the policy of the embedded web app: its own scripts, styles,
// images and API calls only, no inline script, no framing (RENT-BE-001 §8b).
// index.html also allows the styles that carry its per-response nonce:
// PrimeVue's theme is written into <style> elements at run time (Q-96,
// tenure-web Q-W1). The API keeps backendkit's default-src 'none'.
func webCSP(nonce string) string {
	style := "style-src 'self'; "
	if nonce != "" {
		style = "style-src 'self' 'nonce-" + nonce + "'; "
	}
	return "default-src 'self'; script-src 'self'; " + style + "img-src 'self' data:; font-src 'self'; " +
		"connect-src 'self'; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'none'"
}

// noncePlaceholder is what tenure-web's index.html holds where tenure writes
// the nonce (<meta name="csp-nonce" content="__CSP_NONCE__">).
const noncePlaceholder = "__CSP_NONCE__"

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

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
		data, err := fs.ReadFile(files, name)
		if err != nil {
			notFound(w, r)
			return
		}
		nonce := ""
		if name == "index.html" {
			if nonce, err = newNonce(); err != nil {
				problem.Write(w, r, problem.Internal, "", "", nil)
				return
			}
			data = bytes.ReplaceAll(data, []byte(noncePlaceholder), []byte(nonce))
		}
		h := w.Header()
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		h.Set("Content-Length", strconv.Itoa(len(data)))
		h.Set("Content-Security-Policy", webCSP(nonce))
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	}
}

// problemNotFound is the 404 of every path no route and no file answers.
func problemNotFound(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, r, problem.NotFound, "", "", nil)
}
