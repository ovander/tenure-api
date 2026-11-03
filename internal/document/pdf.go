package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// TypePDFUnavailable is the problem of a PDF engine that cannot render now:
// unreachable, timing out, or answering 502, 503 or 504 (fail closed, Q-101).
const TypePDFUnavailable = "document/pdf-unavailable"

func init() {
	problem.Register(map[string]problem.Message{
		TypePDFUnavailable: problem.M(503, "Le moteur PDF est indisponible ; réessayez plus tard.", "The PDF engine is unavailable; try again later."),
	})
}

// GotenbergFlags are the flags gotenberg runs with, wherever it runs
// (integration suite, docker-compose.dev.yml, deploy/sidecars.compose.yml):
// Chromium and LibreOffice start at boot instead of on the first conversion,
// and gotenberg's own limit stays under the client's 60 s so that a slow
// conversion ends in its 503, not in a client timeout (Q-101). The start
// timeouts are raised from 20 s per engine and 30 s for the API: with
// auto-start, a slow boot past them makes gotenberg exit instead of serving
// (Q-103).
var GotenbergFlags = []string{"gotenberg", "--chromium-auto-start=true", "--libreoffice-auto-start=true", "--api-timeout=50s",
	"--chromium-start-timeout=60s", "--libreoffice-start-timeout=60s", "--api-start-timeout=90s"}

// pdfRetryDelay is the wait before the one retry of a conversion gotenberg
// refused with 503 (busy or restarting Chromium or LibreOffice).
var pdfRetryDelay = time.Second

// PDFEngine converts HTML to PDF.
type PDFEngine interface {
	Convert(ctx context.Context, html, footer []byte) ([]byte, error)
}

// Gotenberg converts through gotenberg's Chromium route to PDF/A-2b on A4
// (OD-03, BE-OD-02); the fonts are embedded by the PDF/A conversion.
type Gotenberg struct {
	URL    string
	Client *http.Client
}

// Convert implements PDFEngine. A conversion is pure, so one refused with 503
// is retried once; an engine still unavailable gives TypePDFUnavailable, any
// other failure an internal error.
func (g Gotenberg) Convert(ctx context.Context, html, footer []byte) ([]byte, error) {
	out, status, err := g.convert(ctx, html, footer)
	if status == http.StatusServiceUnavailable {
		select {
		case <-ctx.Done():
		case <-time.After(pdfRetryDelay):
			out, status, err = g.convert(ctx, html, footer)
		}
	}
	if err != nil && unavailable(status, err) {
		return nil, fmt.Errorf("%w: %v", invariant.New(TypePDFUnavailable, ""), err)
	}
	return out, err
}

// unavailable tells a passing outage (no answer, a timeout, a gateway or
// overload status) from a conversion the engine refused.
func unavailable(status int, err error) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case 0:
		return !errors.Is(err, context.Canceled) && !errors.Is(err, errNoPDF)
	}
	return false
}

var errNoPDF = errors.New("document: pdf engine returned no PDF")

func (g Gotenberg) convert(ctx context.Context, html, footer []byte) ([]byte, int, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, content := range map[string][]byte{"index.html": html, "footer.html": footer} {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			return nil, -1, err
		}
		if _, err := fw.Write(content); err != nil {
			return nil, -1, err
		}
	}
	for k, v := range map[string]string{
		"paperWidth": "8.27", "paperHeight": "11.69", "marginTop": "0.79", "marginBottom": "0.87",
		"marginLeft": "0.71", "marginRight": "0.71", "printBackground": "true", "pdfa": "PDF/A-2b",
	} {
		if err := w.WriteField(k, v); err != nil {
			return nil, -1, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, -1, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.URL, "/")+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, -1, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	c := g.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	// Status 0 below means no answer: unreachable or timed out.
	res, err := c.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("document: pdf engine: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(res.Body, 50<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("document: pdf engine: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("document: pdf engine: status %d: %s", res.StatusCode, bytes.TrimSpace(out[:min(len(out), 300)]))
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, -1, errNoPDF
	}
	return out, res.StatusCode, nil
}

// Ping checks the PDF engine answers (/readyz).
func (g Gotenberg) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.URL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("pdf engine health: status %d", res.StatusCode)
	}
	return nil
}
