package esign

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// Yousign is the Yousign API v3 client (OD-08). BaseURL is the API root,
// https://api.yousign.app/v3 in production or the sandbox's; the key is a
// secret from the environment (TENURE_YOUSIGN_API_KEY), never logged.
//
// A request is made with delivery mode none: Yousign sends no e-mail and
// returns each signer's link, opened on the owner's tablet for each party in
// turn (INV-OD-03). Signature fields come from the anchors printed in the
// document ({{s1|signature|…}}), one per signer in order.
type Yousign struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// maxDownload bounds the signed file read back (the PDF sent plus the
// provider's seals).
const maxDownload = 64 << 20

func (y Yousign) client() *http.Client {
	if y.HTTP != nil {
		return y.HTTP
	}
	return &http.Client{Timeout: Timeout}
}

func (y Yousign) do(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	u, err := url.JoinPath(y.BaseURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+y.APIKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := y.client().Do(req)
	if err != nil {
		return fmt.Errorf("esign: %s %s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		// The body may echo personal data: only its size is kept.
		n, _ := io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return fmt.Errorf("%w: %s %s: HTTP %d (%d bytes)", ErrProvider, method, path, res.StatusCode, n)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return nil
	}
	if b, ok := out.(*[]byte); ok {
		data, err := io.ReadAll(io.LimitReader(res.Body, maxDownload+1))
		if err != nil {
			return err
		}
		if len(data) > maxDownload {
			return fmt.Errorf("%w: download larger than %d bytes", ErrProvider, maxDownload)
		}
		*b = data
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrProvider, method, path, err)
	}
	return nil
}

func jsonBody(v any) (io.Reader, error) {
	b, err := json.Marshal(v)
	return bytes.NewReader(b), err
}

type ysID struct {
	ID string `json:"id"`
}

type ysSigner struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	SignatureLink string `json:"signature_link"`
}

type ysRequest struct {
	ID      string     `json:"id"`
	Status  string     `json:"status"`
	Signers []ysSigner `json:"signers"`
}

// Start creates the request, uploads the document (anchors parsed), adds the
// signers in order and activates it. A failure after creation cancels the
// draft at the provider, best effort.
func (y Yousign) Start(ctx context.Context, r Request) (out Started, err error) {
	if len(r.Signers) == 0 || len(r.PDF) == 0 {
		return Started{}, fmt.Errorf("esign: a request needs a document and signers")
	}
	body, err := jsonBody(map[string]any{"name": r.Name, "delivery_mode": "none", "timezone": "Europe/Paris", "external_id": r.ExternalID})
	if err != nil {
		return Started{}, err
	}
	var created ysID
	if err := y.do(ctx, http.MethodPost, "signature_requests", "application/json", body, &created); err != nil {
		return Started{}, err
	}
	if created.ID == "" {
		return Started{}, fmt.Errorf("%w: no request id", ErrProvider)
	}
	defer func() {
		if err != nil {
			_ = y.Cancel(context.WithoutCancel(ctx), created.ID)
		}
	}()
	var mp bytes.Buffer
	w := multipart.NewWriter(&mp)
	_ = w.WriteField("nature", "signable_document")
	_ = w.WriteField("parse_anchors", "true")
	fw, err := w.CreateFormFile("file", r.FileName)
	if err != nil {
		return Started{}, err
	}
	if _, err := fw.Write(r.PDF); err != nil {
		return Started{}, err
	}
	if err := w.Close(); err != nil {
		return Started{}, err
	}
	var doc ysID
	if err := y.do(ctx, http.MethodPost, "signature_requests/"+created.ID+"/documents", w.FormDataContentType(), &mp, &doc); err != nil {
		return Started{}, err
	}
	ids := map[string]string{}
	for _, s := range r.Signers {
		b, err := jsonBody(map[string]any{
			"info":                          map[string]string{"first_name": s.FirstName, "last_name": s.LastName, "email": s.Email, "locale": "fr"},
			"signature_level":               "electronic_signature",
			"signature_authentication_mode": "no_otp",
		})
		if err != nil {
			return Started{}, err
		}
		var signer ysID
		if err := y.do(ctx, http.MethodPost, "signature_requests/"+created.ID+"/signers", "application/json", b, &signer); err != nil {
			return Started{}, err
		}
		if signer.ID == "" {
			return Started{}, fmt.Errorf("%w: no signer id", ErrProvider)
		}
		ids[signer.ID] = s.Key
	}
	var active ysRequest
	if err := y.do(ctx, http.MethodPost, "signature_requests/"+created.ID+"/activate", "", nil, &active); err != nil {
		return Started{}, err
	}
	out = Started{RequestID: created.ID}
	for _, s := range active.Signers {
		key, ok := ids[s.ID]
		if !ok || !strings.HasPrefix(s.SignatureLink, "https://") {
			return Started{}, fmt.Errorf("%w: signer without a link", ErrProvider)
		}
		out.Signers = append(out.Signers, StartedSigner{Key: key, SignerID: s.ID, Link: s.SignatureLink})
	}
	if len(out.Signers) != len(r.Signers) {
		return Started{}, fmt.Errorf("%w: %d signers activated of %d", ErrProvider, len(out.Signers), len(r.Signers))
	}
	return out, nil
}

// Status reads a request and its signers.
func (y Yousign) Status(ctx context.Context, requestID string) (Status, error) {
	var r ysRequest
	if err := y.do(ctx, http.MethodGet, "signature_requests/"+requestID, "", nil, &r); err != nil {
		return Status{}, err
	}
	out := Status{}
	switch r.Status {
	case "draft", "ongoing", "approval":
		out.State = StateOngoing
	case "done":
		out.State = StateDone
	case "declined", "expired", "canceled", "deleted", "rejected":
		out.State = StateRefused
	default:
		return Status{}, fmt.Errorf("%w: request status %q", ErrProvider, r.Status)
	}
	for _, s := range r.Signers {
		out.Signers = append(out.Signers, SignerStatus{SignerID: s.ID, Signed: s.Status == "signed"})
	}
	return out, nil
}

// Download returns the signed document of a done request.
func (y Yousign) Download(ctx context.Context, requestID string) ([]byte, error) {
	var b []byte
	if err := y.do(ctx, http.MethodGet, "signature_requests/"+requestID+"/documents/download", "", nil, &b); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		return nil, fmt.Errorf("%w: the signed document is not a PDF", ErrProvider)
	}
	return b, nil
}

// Cancel cancels a pending request.
func (y Yousign) Cancel(ctx context.Context, requestID string) error {
	body, err := jsonBody(map[string]string{"reason": "contractualization_aborted"})
	if err != nil {
		return err
	}
	return y.do(ctx, http.MethodPost, "signature_requests/"+requestID+"/cancel", "application/json", body, nil)
}
