package document

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/ovander/tenure-api/internal/invariant"
)

// ErrPDFUnavailable reports a PDF engine that could not render now: gotenberg
// answered 503 (its API timeout, typically while it starts or restarts
// Chromium or LibreOffice) or 429 (queue full), or could not be reached. It
// wraps invariant.ErrUnavailable, so the request fails closed with 503
// http/unavailable: nothing is issued and the document number is given back
// with the rolled-back transaction (Q-100).
var ErrPDFUnavailable = fmt.Errorf("document: pdf engine unavailable: %w", invariant.ErrUnavailable)

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

// Convert implements PDFEngine.
func (g Gotenberg) Convert(ctx context.Context, html, footer []byte) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, content := range map[string][]byte{"index.html": html, "footer.html": footer} {
		fw, err := w.CreateFormFile("files", name)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(content); err != nil {
			return nil, err
		}
	}
	for k, v := range map[string]string{
		"paperWidth": "8.27", "paperHeight": "11.69", "marginTop": "0.79", "marginBottom": "0.87",
		"marginLeft": "0.71", "marginRight": "0.71", "printBackground": "true", "pdfa": "PDF/A-2b",
	} {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.URL, "/")+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	c := g.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// The caller gave up; the engine is not to blame.
			return nil, fmt.Errorf("document: pdf engine: %w", err)
		}
		return nil, fmt.Errorf("%w: %w", ErrPDFUnavailable, err)
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(res.Body, 50<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		err := fmt.Errorf("status %d: %s", res.StatusCode, bytes.TrimSpace(out[:min(len(out), 300)]))
		if res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("%w: %w", ErrPDFUnavailable, err)
		}
		return nil, fmt.Errorf("document: pdf engine: %w", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		return nil, fmt.Errorf("document: pdf engine returned no PDF")
	}
	return out, nil
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
