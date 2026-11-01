package document

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// KeyArchiveRetention is the legal_params key of the retention period in years.
const KeyArchiveRetention = "archive_retention_years"

// SignedURLTTL is the lifetime of a download link (RENT-BE-001 §8b).
const SignedURLTTL = 10 * time.Minute

// Request describes a document to issue.
type Request struct {
	// Class is the archive class and the numbering class.
	Class Class
	// Template is the template class (Class, or ClassCancellation for a
	// cancelling document) and TemplateVersion its version.
	Template        Class
	TemplateVersion string
	AssetID         uuid.UUID
	LeaseID         *uuid.UUID
	Period          *period.Period
	Snapshot        any
	// Data builds the template data once the number and date are known.
	Data func(number string, issuedOn period.Date) any
}

// Issued is an archived document.
type Issued struct {
	DocumentID    uuid.UUID   `json:"document_id"`
	Number        string      `json:"number"`
	IssuedAt      time.Time   `json:"issued_at"`
	SHA256        string      `json:"sha256"`
	ObjectKey     string      `json:"object_key"`
	ObjectVersion string      `json:"object_version"`
	RetainUntil   period.Date `json:"retain_until" swaggertype:"string" format:"date"`
	SnapshotHash  string      `json:"snapshot_hash"`
	PDF           []byte      `json:"-"`
}

// Issuer runs the document pipeline (RENT-BE-001 §6) inside the caller's
// scoped transaction: number reserved, input snapshot fingerprinted, HTML
// rendered from the versioned template, PDF generated, object written under
// compliance lock and verified, metadata row inserted. Any error rolls the
// number back with the transaction. An object already written when the
// transaction then fails stays in the bucket under lock, unreferenced (Q-47).
type Issuer struct {
	Repo     Repository
	Renderer Renderer
	PDF      PDFEngine
	Store    Store
	Now      func() time.Time
	NewID    func() uuid.UUID
}

// Issue issues the document of req.
func (i Issuer) Issue(ctx context.Context, req Request, ps params.Set) (Issued, error) {
	sc, ok := db.ScopeFrom(ctx)
	if !ok || sc.OwnerID == uuid.Nil {
		return Issued{}, errors.New("document: issue needs an owner scope")
	}
	now := i.Now().UTC().Truncate(time.Microsecond)
	issuedOn := period.FromTime(now.In(paris()))
	retention, err := ps.Int64(KeyArchiveRetention, issuedOn)
	if err != nil {
		return Issued{}, err
	}
	code := AssetCode(req.AssetID)
	seq, err := i.Repo.ReserveNumber(ctx, sc.OwnerID, req.Class, code, issuedOn.Year)
	if err != nil {
		return Issued{}, fmt.Errorf("document: reserve number: %w", err)
	}
	number, err := Number(req.Class, code, issuedOn.Year, seq)
	if err != nil {
		return Issued{}, err
	}
	snapHash, err := SnapshotHash(req.Snapshot)
	if err != nil {
		return Issued{}, err
	}
	tmpl := req.Template
	if tmpl == "" {
		tmpl = req.Class
	}
	html, footer, err := i.Renderer.Render(tmpl, req.TemplateVersion, req.Data(number, issuedOn), Footer{Number: number, SnapshotHash: snapHash[:16]})
	if err != nil {
		return Issued{}, err
	}
	pdf, err := i.PDF.Convert(ctx, html, footer)
	if err != nil {
		return Issued{}, err
	}
	sum := SHA256(pdf)
	retain := RetainUntil(now.In(paris()), retention)
	key := ObjectKey(sc.OwnerID, req.Class, issuedOn.Year, number)
	version, err := i.Store.Put(ctx, key, pdf, "application/pdf", sum, retain.Time())
	if err != nil {
		return Issued{}, err
	}
	id := i.NewID()
	row := documentRow{ID: id, OwnerID: sc.OwnerID, Class: string(req.Class), AssetID: &req.AssetID, LeaseID: req.LeaseID,
		ObjectKey: key, ObjectVersion: version, SHA256: sum, SizeBytes: int64(len(pdf)),
		TemplateVersion: string(tmpl) + "/" + req.TemplateVersion, IssuedAt: &now, CreatedAt: now, CreatedBy: actor(sc),
		Source: SourceGenerated, ContentType: "application/pdf"}
	if req.Period != nil {
		p := req.Period.String()
		row.Period = &p
	}
	if !leaseBound[req.Class] {
		rt := retain.Time()
		row.RetainUntil = &rt
	}
	if err := i.Repo.Insert(ctx, row); err != nil {
		return Issued{}, fmt.Errorf("document: record %s: %w", number, err)
	}
	return Issued{DocumentID: id, Number: number, IssuedAt: now, SHA256: sum, ObjectKey: key, ObjectVersion: version,
		RetainUntil: retain, SnapshotHash: snapHash, PDF: pdf}, nil
}

func actor(sc db.Scope) string {
	if sc.ActorSub != "" {
		return sc.ActorSub
	}
	return "system:" + sc.Role
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

// View is document metadata as the API returns it.
type View struct {
	ID              uuid.UUID  `json:"id"`
	Class           Class      `json:"class"`
	AssetID         *uuid.UUID `json:"asset_id"`
	LeaseID         *uuid.UUID `json:"lease_id"`
	Period          *string    `json:"period"`
	SHA256          string     `json:"sha256"`
	SizeBytes       int64      `json:"size_bytes"`
	TemplateVersion string     `json:"template_version"`
	IssuedAt        *time.Time `json:"issued_at"`
	// RetainUntil is unset while the retention clock has not started (a
	// lease document before the lease ends, Q-68): never eligible for deletion.
	RetainUntil     *period.Date `json:"retain_until" swaggertype:"string" format:"date"`
	Source          string       `json:"source" enums:"generated,upload"`
	ContentType     string       `json:"content_type"`
	OriginalName    string       `json:"original_name"`
	SignsDocumentID *uuid.UUID   `json:"signs_document_id"`
}

func view(d documentRow) View {
	v := View{ID: d.ID, Class: Class(d.Class), AssetID: d.AssetID, LeaseID: d.LeaseID, Period: d.Period, SHA256: d.SHA256,
		SizeBytes: d.SizeBytes, TemplateVersion: d.TemplateVersion, IssuedAt: d.IssuedAt, Source: d.Source,
		ContentType: contentTypeOf(d), OriginalName: d.OriginalName, SignsDocumentID: d.SignsDocumentID}
	if d.RetainUntil != nil {
		r := period.FromTime(*d.RetainUntil)
		v.RetainUntil = &r
	}
	return v
}

// Link is a short-lived download link.
type Link struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Service serves document metadata, download links and uploads with the API
// role.
type Service struct {
	DB      *db.DB
	Repo    Repository
	Store   Store
	Scanner Scanner
	Params  params.Repository
	Now     func() time.Time
	NewID   func() uuid.UUID
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// List returns a page of the scope's documents; an accountant sees financial
// classes only.
func (s Service) List(ctx context.Context, p *auth.Principal, requestID string, f Filter, offset, limit int) ([]View, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	if p.Active.Role == auth.RoleAccountant {
		for c := range financial {
			f.Classes = append(f.Classes, string(c))
		}
	}
	var out []View
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.List(ctx, f, offset, limit)
		for _, r := range rows {
			out = append(out, view(r))
		}
		total = n
		return err
	})
	return out, total, err
}

// Get returns one document of the scope.
func (s Service) Get(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		d, err := s.Repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if p.Active.Role == auth.RoleAccountant && !Financial(Class(d.Class)) {
			return invariant.ErrNotFound
		}
		out = view(d)
		return nil
	})
	return out, err
}

// Content returns a 10-minute link to the document's archived version and
// records the download as an audit event (RENT-BE-001 §8b). The link is
// issued only to a caller who may read the document, and its signature
// covers the caller's subject and the document id (Q-50).
func (s Service) Content(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (Link, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Link{}, err
	}
	var out Link
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		d, err := s.Repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if p.Active.Role == auth.RoleAccountant && !Financial(Class(d.Class)) {
			return invariant.ErrNotFound
		}
		name := d.ID.String()
		if n := lastSegment(d.ObjectKey); n != "" {
			name = n
		}
		u, err := s.Store.PresignGet(ctx, d.ObjectKey, d.ObjectVersion, SignedURLTTL, url.Values{
			"response-content-disposition": {fmt.Sprintf(`attachment; filename="%s"`, name)},
			"response-content-type":        {contentTypeOf(d)},
			"x-tenure-sub":                 {p.Sub},
			"x-tenure-document":            {d.ID.String()},
		})
		if err != nil {
			return err
		}
		out = Link{URL: u.String(), ExpiresAt: s.Now().UTC().Add(SignedURLTTL).Truncate(time.Second)}
		return audit.Record(ctx, "document.download", audit.Entity{Type: "document", ID: d.ID.String()}, nil,
			map[string]any{"object_key": d.ObjectKey, "expires_at": out.ExpiresAt})
	})
	return out, err
}

func contentTypeOf(d documentRow) string {
	if d.ContentType == "" {
		return "application/pdf"
	}
	return d.ContentType
}

func lastSegment(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[i+1:]
		}
	}
	return key
}
