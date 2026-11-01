package document

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// ExportTimeout bounds the build of one export (RENT-SPEC-002 performance: a
// year in under 60 seconds; the bound leaves room for a large history).
const ExportTimeout = 10 * time.Minute

// TypeExportInvalid is the problem type of an export with neither or both of
// a year and an asset.
const TypeExportInvalid = "document/export-invalid"

func init() {
	problem.Register(map[string]problem.Message{
		TypeExportInvalid: problem.M(422, "Indiquez une année ou un bien, pas les deux.", "Give a year or an asset, not both."),
	})
}

// ExportInput is the body of POST /documents/export.
type ExportInput struct {
	Year    *int       `json:"year"`
	AssetID *uuid.UUID `json:"asset_id"`
}

// ExportView is an export and, once ready, its download link.
type ExportView struct {
	ID         uuid.UUID  `json:"id"`
	State      string     `json:"state" enums:"pending,ready,failed"`
	Year       *int       `json:"year"`
	AssetID    *uuid.UUID `json:"asset_id"`
	Documents  int        `json:"documents"`
	SizeBytes  int64      `json:"size_bytes"`
	SHA256     string     `json:"sha256"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Link       *Link      `json:"link,omitempty"`
}

func exportView(e exportRow) ExportView {
	return ExportView{ID: e.ID, State: e.State, Year: e.Year, AssetID: e.AssetID, Documents: e.Documents, SizeBytes: e.SizeBytes,
		SHA256: e.SHA256, Error: e.Error, CreatedAt: e.CreatedAt, FinishedAt: e.FinishedAt}
}

// CreateExport records an export of a year or of an asset's whole history
// (RENT-SPEC-005) and builds it after the commit: a ZIP of the documents the
// caller may read in the scope (financial classes only for an accountant)
// with an index CSV. It is a step_up route and its request is an audit event
// document.export.
func (s Service) CreateExport(ctx context.Context, p *auth.Principal, requestID string, in ExportInput) (ExportView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ExportView{}, err
	}
	if (in.Year == nil) == (in.AssetID == nil) || (in.Year != nil && (*in.Year < 1970 || *in.Year > 9999)) {
		return ExportView{}, invariant.New(TypeExportInvalid, "")
	}
	f := ExportFilter{AssetID: in.AssetID}
	if in.Year != nil {
		f.Year = *in.Year
	}
	if p.Active.Role == auth.RoleAccountant {
		for c := range financial {
			f.Classes = append(f.Classes, string(c))
		}
	}
	var out ExportView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if in.AssetID != nil {
			ok, err := s.Repo.LinksExist(ctx, in.AssetID, nil)
			if err != nil {
				return err
			}
			if !ok {
				return invariant.New(TypeLinkInvalid, in.AssetID.String())
			}
		}
		e := exportRow{ID: s.NewID(), OwnerID: sc.OwnerID, RequestedBy: p.Sub, Role: string(p.Active.Role), Year: in.Year,
			AssetID: in.AssetID, State: "pending", CreatedAt: s.Now().UTC().Truncate(time.Microsecond)}
		if err := s.Repo.InsertExport(ctx, e); err != nil {
			return err
		}
		out = exportView(e)
		return audit.Record(ctx, "document.export", audit.Entity{Type: "export", ID: e.ID.String()}, nil, out)
	})
	if err != nil {
		return ExportView{}, err
	}
	go s.buildExport(context.WithoutCancel(ctx), sc, out.ID, f)
	return out, nil
}

// buildExport reads the export's documents in one transaction, streams the
// ZIP to <owner_id>/exports/<id>.zip outside the locked archive (Q-91), then
// records the outcome, ready or failed, with its audit event.
func (s Service) buildExport(ctx context.Context, sc db.Scope, id uuid.UUID, f ExportFilter) {
	ctx, cancel := context.WithTimeout(ctx, ExportTimeout)
	defer cancel()
	var docs []documentRow
	err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		docs, err = s.Repo.ExportDocuments(ctx, f)
		return err
	})
	e := exportRow{ID: id, State: "ready"}
	if err == nil {
		e.ObjectKey = fmt.Sprintf("%s/exports/%s.zip", sc.OwnerID, id)
		e.ObjectVersion, e.SHA256, e.SizeBytes, err = s.writeZip(ctx, e.ObjectKey, docs)
		e.Documents = len(docs)
	}
	if err != nil {
		slog.ErrorContext(ctx, "export failed", "export_id", id, "error", err)
		e = exportRow{ID: id, State: "failed", Error: "export impossible"}
	}
	now := s.Now().UTC().Truncate(time.Microsecond)
	e.FinishedAt = &now
	if err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.Repo.FinishExport(ctx, e); err != nil {
			return err
		}
		return audit.Record(ctx, "document.export."+e.State, audit.Entity{Type: "export", ID: id.String()}, nil,
			map[string]any{"documents": e.Documents, "sha256": e.SHA256, "size_bytes": e.SizeBytes})
	}); err != nil {
		slog.ErrorContext(ctx, "export not recorded", "export_id", id, "error", err)
	}
}

// writeZip streams the documents and their index into the object store and
// returns the object version, its SHA-256 and its size. Each archived object
// is checked against its recorded hash on the way.
func (s Service) writeZip(ctx context.Context, key string, docs []documentRow) (string, string, int64, error) {
	pr, pw := io.Pipe()
	h := sha256.New()
	counted := &countingWriter{w: io.MultiWriter(pw, h)}
	go func() {
		zw := zip.NewWriter(counted)
		err := func() error {
			idx, err := zw.Create("index.csv")
			if err != nil {
				return err
			}
			cw := csv.NewWriter(idx)
			_ = cw.Write([]string{"file", "id", "class", "source", "asset_id", "lease_id", "period", "issued_at", "sha256", "size_bytes", "retain_until"})
			for _, d := range docs {
				name := path.Join(d.Class, d.ID.String()+"-"+path.Base(d.ObjectKey))
				row := []string{name, d.ID.String(), d.Class, d.Source, uuidText(d.AssetID), uuidText(d.LeaseID), text(d.Period),
					d.IssuedAt.UTC().Format(time.RFC3339), d.SHA256, strconv.FormatInt(d.SizeBytes, 10), ""}
				if d.RetainUntil != nil {
					row[10] = d.RetainUntil.Format(time.DateOnly)
				}
				_ = cw.Write(row)
			}
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
			for _, d := range docs {
				if err := s.copyInto(ctx, zw, path.Join(d.Class, d.ID.String()+"-"+path.Base(d.ObjectKey)), d); err != nil {
					return err
				}
			}
			return zw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()
	version, err := s.Store.PutUnlocked(ctx, key, "application/zip", pr)
	_ = pr.Close()
	if err != nil {
		return "", "", 0, err
	}
	return version, hex.EncodeToString(h.Sum(nil)), counted.n, nil
}

func (s Service) copyInto(ctx context.Context, zw *zip.Writer, name string, d documentRow) error {
	obj, err := s.Store.Open(ctx, d.ObjectKey, d.ObjectVersion)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Close() }()
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), obj); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != d.SHA256 {
		return fmt.Errorf("%w: %s", ErrChecksum, d.ObjectKey)
	}
	return nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func uuidText(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// GetExport returns an export of the caller (the owner sees every export of
// the scope) and, once ready, a 10-minute link to it, recorded as an audit
// event document.download.
func (s Service) GetExport(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (ExportView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ExportView{}, err
	}
	var out ExportView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		e, err := s.Repo.Export(ctx, id)
		if err != nil {
			return err
		}
		out = exportView(e)
		if e.State != "ready" {
			return nil
		}
		u, err := s.Store.PresignGet(ctx, e.ObjectKey, e.ObjectVersion, SignedURLTTL, map[string][]string{
			"response-content-disposition": {fmt.Sprintf(`attachment; filename="export-%s.zip"`, e.ID)},
			"response-content-type":        {"application/zip"},
			"x-tenure-sub":                 {p.Sub},
			"x-tenure-export":              {e.ID.String()},
		})
		if err != nil {
			return err
		}
		out.Link = &Link{URL: u.String(), ExpiresAt: s.Now().UTC().Add(SignedURLTTL).Truncate(time.Second)}
		return audit.Record(ctx, "document.download", audit.Entity{Type: "export", ID: e.ID.String()}, nil,
			map[string]any{"object_key": e.ObjectKey, "expires_at": out.Link.ExpiresAt})
	})
	return out, err
}
