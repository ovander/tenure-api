package inventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the photo routes.
const (
	TypePhotoNotImage   = "inventory/photo-not-image"
	TypePhotoTarget     = "inventory/photo-target-invalid"
	TypeIssuedImmutable = "inventory/issued-immutable"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypePhotoNotImage:   problem.M(422, "La photo doit être une image JPEG ou PNG de taille raisonnable.", "The photo must be a JPEG or PNG image of a reasonable size."),
		TypePhotoTarget:     problem.M(422, "La photo doit se rattacher à un élément ou à un compteur de cet état des lieux.", "The photo must belong to an item or a meter of this inventory."),
		TypeIssuedImmutable: problem.M(409, "Cet état des lieux est émis : il ne peut plus être modifié.", "This inventory is issued: it can no longer be changed."),
	})
}

// PhotoStore is what the photo pipeline needs of the archive and the antivirus.
type PhotoStore struct {
	Store   document.Store
	Scanner document.Scanner
	Params  params.Repository
}

// PhotoView is an archived inventory photo. SHA256 is the cleaned file's,
// the one archived; the upload's own hash is kept in the audit event only.
type PhotoView struct {
	ID          uuid.UUID  `json:"id"`
	InventoryID uuid.UUID  `json:"inventory_id"`
	ItemID      *uuid.UUID `json:"item_id,omitempty"`
	MeterID     *uuid.UUID `json:"meter_id,omitempty"`
	SHA256      string     `json:"sha256"`
	Width       int        `json:"width"`
	Height      int        `json:"height"`
	CreatedAt   time.Time  `json:"created_at"`
}

// PhotoInput is a photo upload: the file and what it shows.
type PhotoInput struct {
	InventoryID uuid.UUID
	ItemID      *uuid.UUID
	MeterID     *uuid.UUID
	File        io.Reader
}

func photoView(r photoRow) PhotoView {
	return PhotoView{ID: r.ID, InventoryID: r.InventoryID, ItemID: r.ItemID, MeterID: r.MeterID, SHA256: r.SHA256, Width: r.Width,
		Height: r.Height, CreatedAt: r.CreatedAt}
}

// UploadPhoto archives an inventory photo (RENT-INV-001 §3, §7): the upload
// waits in quarantine (capped at document.MaxUploadBytes, hashed as it
// streams) while the antivirus scans it (fail closed); a clean JPEG or PNG is
// then decoded, turned upright, stripped of every metadata and re-encoded,
// and the cleaned file is hashed and written under compliance lock to
// <owner>/inventory/<lease>/<inventory>/<photo>.jpg. Its row and its audit
// event inventory.photo.add, with both hashes, are written in one
// transaction. An issued inventory takes no photo. The quarantined object is
// always deleted.
func (s Service) UploadPhoto(ctx context.Context, p *auth.Principal, requestID string, in PhotoInput) (PhotoView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return PhotoView{}, err
	}
	if (in.ItemID == nil) == (in.MeterID == nil) {
		return PhotoView{}, invariant.New(TypePhotoTarget, "one of item_id or meter_id")
	}
	if s.Photos.Scanner == nil {
		return PhotoView{}, invariant.New(document.TypeScanUnavailable, "no scanner configured")
	}
	// Fail fast on a wrong target, before the upload is streamed.
	var lease uuid.UUID
	if err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		lease, err = s.photoTarget(ctx, in)
		return err
	}); err != nil {
		return PhotoView{}, err
	}
	id := s.NewID()
	qkey := document.QuarantinePrefix + sc.OwnerID.String() + "/" + id.String()
	h := sha256.New()
	limited := io.LimitReader(in.File, document.MaxUploadBytes+1)
	var raw bytes.Buffer
	qversion, err := s.Photos.Store.PutQuarantine(ctx, qkey, io.TeeReader(io.TeeReader(limited, h), &raw))
	defer func() {
		if qversion != "" {
			if err := s.Photos.Store.Delete(context.WithoutCancel(ctx), qkey, qversion); err != nil {
				slog.WarnContext(ctx, "quarantine delete failed", "key", qkey, "error", err)
			}
		}
	}()
	if err != nil {
		return PhotoView{}, err
	}
	if raw.Len() > document.MaxUploadBytes {
		return PhotoView{}, invariant.New(document.TypeUploadTooLarge, fmt.Sprintf("more than %d bytes", document.MaxUploadBytes))
	}
	original := hex.EncodeToString(h.Sum(nil))
	verdict, err := s.scanQuarantined(ctx, qkey, qversion)
	if err != nil {
		slog.ErrorContext(ctx, "photo scan failed", "error", err, "request_id", requestID)
		return PhotoView{}, invariant.New(document.TypeScanUnavailable, "")
	}
	if !verdict.Clean {
		slog.WarnContext(ctx, "photo rejected by antivirus", "owner_id", sc.OwnerID, "sub", p.Sub, "signature", verdict.Signature,
			"sha256", original, "size", raw.Len(), "request_id", requestID)
		if err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
			return audit.Record(ctx, "inventory.photo.reject", audit.Entity{Type: "inventory", ID: in.InventoryID.String()}, nil,
				map[string]any{"sha256": original, "size_bytes": raw.Len(), "signature": verdict.Signature})
		}); err != nil {
			return PhotoView{}, err
		}
		return PhotoView{}, invariant.New(document.TypeInfected, verdict.Signature)
	}
	cleaned, w, hgt, err := CleanPhoto(raw.Bytes())
	if errors.Is(err, ErrNotPhoto) {
		return PhotoView{}, invariant.New(TypePhotoNotImage, "")
	}
	if err != nil {
		return PhotoView{}, err
	}
	sum := sha256.Sum256(cleaned)
	cleanHash := hex.EncodeToString(sum[:])
	var out PhotoView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.photoTarget(ctx, in); err != nil {
			return err
		}
		ps, err := s.Photos.Params.Load(ctx)
		if err != nil {
			return err
		}
		now := s.now()
		today := period.FromTime(now.In(paris()))
		years, err := ps.Int64(document.KeyArchiveRetention, today)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s/inventory/%s/%s/%s.jpg", sc.OwnerID, lease, in.InventoryID, id)
		version, err := document.Archive(ctx, s.Photos.Store, key, cleaned, "image/jpeg", cleanHash, document.RetainUntil(now.In(paris()), years).Time())
		if err != nil {
			return err
		}
		row := photoRow{ID: id, OwnerID: sc.OwnerID, InventoryID: in.InventoryID, ItemID: in.ItemID, MeterID: in.MeterID, ObjectKey: key,
			ObjectVersion: version, SHA256: cleanHash, OriginalSHA256: original, Width: w, Height: hgt, CreatedAt: now, CreatedBy: p.Sub}
		if err := s.Repo.InsertPhoto(ctx, row); err != nil {
			return err
		}
		out = photoView(row)
		return audit.Record(ctx, "inventory.photo.add", audit.Entity{Type: "inventory", ID: in.InventoryID.String()}, nil,
			map[string]any{"photo": out, "original_sha256": original, "original_size_bytes": raw.Len(), "size_bytes": len(cleaned)})
	})
	return out, err
}

// photoTarget checks that the inventory exists, takes photos at its stage (a
// draft, or on site), and holds the item or meter; it returns the
// inventory's lease.
func (s Service) photoTarget(ctx context.Context, in PhotoInput) (uuid.UUID, error) {
	inv, ok, err := s.Repo.Inventory(ctx, in.InventoryID)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, invariant.ErrNotFound
	}
	if !itemsEditable(inv.State) {
		return uuid.Nil, editError(inv.State)
	}
	var belongs bool
	if in.ItemID != nil {
		belongs, err = s.Repo.ItemOf(ctx, in.InventoryID, *in.ItemID)
	} else {
		belongs, err = s.Repo.MeterOf(ctx, in.InventoryID, *in.MeterID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !belongs {
		return uuid.Nil, invariant.New(TypePhotoTarget, "not of this inventory")
	}
	return inv.LeaseID, nil
}

func (s Service) scanQuarantined(ctx context.Context, key, version string) (document.Verdict, error) {
	r, err := s.Photos.Store.Open(ctx, key, version)
	if err != nil {
		return document.Verdict{}, err
	}
	defer func() { _ = r.Close() }()
	return s.Photos.Scanner.Scan(ctx, r)
}

// PhotoContent returns a short-lived link to a photo, shown inline (an
// <img>), and records the view as an audit event, as a document download is
// (RENT-BE-001 §8b). The link's signature covers the caller and the photo.
func (s Service) PhotoContent(ctx context.Context, p *auth.Principal, requestID string, inventoryID, photoID uuid.UUID) (document.Link, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return document.Link{}, err
	}
	var out document.Link
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		ph, ok, err := s.Repo.Photo(ctx, photoID)
		if err != nil {
			return err
		}
		if !ok || ph.InventoryID != inventoryID {
			return invariant.ErrNotFound
		}
		u, err := s.Photos.Store.PresignGet(ctx, ph.ObjectKey, ph.ObjectVersion, document.SignedURLTTL, url.Values{
			"response-content-disposition": {"inline"},
			"response-content-type":        {"image/jpeg"},
			"x-tenure-sub":                 {p.Sub},
			"x-tenure-photo":               {ph.ID.String()},
		})
		if err != nil {
			return err
		}
		out = document.Link{URL: u.String(), ExpiresAt: s.Now().UTC().Add(document.SignedURLTTL).Truncate(time.Second)}
		return audit.Record(ctx, "inventory.photo.view", audit.Entity{Type: "inventory", ID: inventoryID.String()}, nil,
			map[string]any{"photo_id": ph.ID, "expires_at": out.ExpiresAt})
	})
	return out, err
}
