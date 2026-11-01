package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// MaxUploadBytes is the size cap of an uploaded file (RENT-BE-001 §8b: 25 MB).
const MaxUploadBytes = 25 << 20

// QuarantinePrefix is the key prefix where an upload waits for its scan.
const QuarantinePrefix = "quarantine/"

// Problem types of uploads.
const (
	TypeUploadTooLarge   = "document/too-large"
	TypeTypeNotAllowed   = "document/type-not-allowed"
	TypeInfected         = "document/infected"
	TypeScanUnavailable  = "document/scan-unavailable"
	TypeClassNotUpload   = "document/class-not-uploadable"
	TypeLinkInvalid      = "document/link-invalid"
	TypeSignedTargetMiss = "document/signed-target-invalid"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeUploadTooLarge:   problem.M(413, "Le fichier dépasse 25 Mo.", "The file exceeds 25 MB."),
		TypeTypeNotAllowed:   problem.M(415, "Type de fichier refusé : PDF, JPEG, PNG, CSV ou OFX uniquement.", "File type refused: PDF, JPEG, PNG, CSV or OFX only."),
		TypeInfected:         problem.M(422, "Le fichier a été refusé par l’antivirus.", "The file was refused by the antivirus."),
		TypeScanUnavailable:  problem.M(503, "L’antivirus est indisponible ; réessayez plus tard.", "The antivirus is unavailable; try again later."),
		TypeClassNotUpload:   problem.M(422, "Cette classe de document est produite par Tenure et ne se dépose pas.", "This document class is produced by Tenure and cannot be uploaded."),
		TypeLinkInvalid:      problem.M(422, "Le bien ou le bail indiqué n’existe pas dans ce portefeuille.", "The asset or lease given does not exist in this portfolio."),
		TypeSignedTargetMiss: problem.M(422, "La copie signée doit viser un document généré de même classe et du même bail.", "A signed copy must point at a generated document of the same class and lease."),
	})
}

// Classes an owner may upload (RENT-SPEC-005: leases, amendments,
// inventories, deposit settlements, supporting documents). Generated billing
// and tax classes are produced only by Tenure.
var uploadable = map[Class]bool{ClassContract: true, ClassAmendment: true, ClassDeposit: true, ClassSurety: true,
	ClassInventory: true, ClassSupporting: true, ClassOther: true}

// Uploadable reports whether an owner may upload documents of class c.
func Uploadable(c Class) bool { return uploadable[c] }

// UploadInput describes one upload: its class, its links and the file.
type UploadInput struct {
	Class   Class
	AssetID *uuid.UUID
	LeaseID *uuid.UUID
	Period  *period.Period
	// SignsDocumentID is the generated document this file is the signed copy of.
	SignsDocumentID *uuid.UUID
	Name            string
	File            io.Reader
}

// sniff returns the stored content type and extension of an upload from its
// first bytes, never from what the client declares. CSV and OFX have no magic
// number: a text file is accepted only under one of those extensions.
func sniff(head []byte, name string) (contentType, ext string, ok bool) {
	switch detected := http.DetectContentType(head); {
	case detected == "application/pdf":
		return "application/pdf", ".pdf", true
	case detected == "image/jpeg":
		return "image/jpeg", ".jpg", true
	case detected == "image/png":
		return "image/png", ".png", true
	case strings.HasPrefix(detected, "text/plain"), strings.HasPrefix(detected, "text/xml"):
		switch strings.ToLower(path.Ext(name)) {
		case ".csv":
			return "text/csv", ".csv", true
		case ".ofx":
			return "application/x-ofx", ".ofx", true
		}
	}
	return "", "", false
}

// counter hashes, counts and keeps the first bytes of what passes through.
type counter struct {
	r    io.Reader
	h    io.Writer
	n    int64
	head []byte
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		_, _ = c.h.Write(p[:n])
		if missing := 512 - len(c.head); missing > 0 {
			c.head = append(c.head, p[:min(n, missing)]...)
		}
		c.n += int64(n)
	}
	return n, err
}

// Upload runs the upload pipeline (RENT-BE-001 §8b): the file streams to the
// quarantine prefix (capped at 25 MB, hashed while it streams), the
// antivirus scans it, its type is sniffed, and only a clean file is copied under
// compliance lock to the archive prefix and recorded with its audit event. A
// refused file is deleted from quarantine; an infected one is also logged and
// recorded as a document.reject audit event. No scanner answer refuses the
// upload (fail closed). The quarantined object is always deleted.
func (s Service) Upload(ctx context.Context, p *auth.Principal, requestID string, in UploadInput) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if !uploadable[in.Class] {
		return View{}, invariant.New(TypeClassNotUpload, string(in.Class))
	}
	if in.SignsDocumentID != nil && in.LeaseID == nil {
		return View{}, invariant.New(TypeSignedTargetMiss, "a signed copy needs its lease")
	}
	if s.Scanner == nil {
		return View{}, invariant.New(TypeScanUnavailable, "no scanner configured")
	}
	id := s.NewID()
	qkey := QuarantinePrefix + sc.OwnerID.String() + "/" + id.String()
	h := sha256.New()
	c := &counter{r: io.LimitReader(in.File, MaxUploadBytes+1), h: h}
	qversion, err := s.Store.PutQuarantine(ctx, qkey, c)
	defer func() {
		// Deleting the quarantined object is best effort: the prefix holds no
		// lock, and a leftover is never referenced.
		if qversion != "" {
			if err := s.Store.Delete(context.WithoutCancel(ctx), qkey, qversion); err != nil {
				slog.WarnContext(ctx, "quarantine delete failed", "key", qkey, "error", err)
			}
		}
	}()
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return View{}, invariant.New(TypeUploadTooLarge, "")
		}
		return View{}, err
	}
	if c.n > MaxUploadBytes {
		return View{}, invariant.New(TypeUploadTooLarge, fmt.Sprintf("more than %d bytes", MaxUploadBytes))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	verdict, err := s.scan(ctx, qkey, qversion)
	if err != nil {
		slog.ErrorContext(ctx, "upload scan failed", "error", err, "request_id", requestID)
		return View{}, invariant.New(TypeScanUnavailable, "")
	}
	if !verdict.Clean {
		slog.WarnContext(ctx, "upload rejected by antivirus", "owner_id", sc.OwnerID, "sub", p.Sub,
			"signature", verdict.Signature, "sha256", sum, "size", c.n, "request_id", requestID)
		if err := s.DB.InScope(ctx, sc, func(ctx context.Context) error {
			return audit.Record(ctx, "document.reject", audit.Entity{Type: "upload", ID: id.String()}, nil,
				map[string]any{"class": in.Class, "sha256": sum, "size_bytes": c.n, "signature": verdict.Signature})
		}); err != nil {
			return View{}, err
		}
		return View{}, invariant.New(TypeInfected, verdict.Signature)
	}
	// The type is checked after the scan, so that every infected file is
	// logged, whatever it claims to be.
	contentType, ext, ok := sniff(c.head, in.Name)
	if !ok || c.n == 0 {
		return View{}, invariant.New(TypeTypeNotAllowed, http.DetectContentType(c.head))
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkLinks(ctx, in); err != nil {
			return err
		}
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		now := s.Now().UTC().Truncate(time.Microsecond)
		today := period.FromTime(now.In(paris()))
		years, err := ps.Int64(KeyArchiveRetention, today)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s/%s/%04d/%s%s", sc.OwnerID, in.Class, today.Year, id, ext)
		version, err := s.Store.CopyLocked(ctx, qkey, qversion, key, contentType, sum, RetainUntil(now.In(paris()), years).Time())
		if err != nil {
			return err
		}
		row := documentRow{ID: id, OwnerID: sc.OwnerID, Class: string(in.Class), AssetID: in.AssetID, LeaseID: in.LeaseID,
			ObjectKey: key, ObjectVersion: version, SHA256: sum, SizeBytes: c.n, IssuedAt: &now, CreatedAt: now, CreatedBy: p.Sub,
			Source: SourceUpload, ContentType: contentType, OriginalName: cleanName(in.Name), SignsDocumentID: in.SignsDocumentID}
		if in.Period != nil {
			ps := in.Period.String()
			row.Period = &ps
		}
		// The retention clock of an upload starts at a later event (lease
		// end, tax year of the charge): retain_until stays unset, so it is
		// never eligible for deletion, until archive.retention sets it (Q-68).
		if err := s.Repo.Insert(ctx, row); err != nil {
			return err
		}
		out = view(row)
		return audit.Record(ctx, "document.upload", audit.Entity{Type: "document", ID: id.String()}, nil, out)
	})
	return out, err
}

// ReadInTx returns, inside the current transaction, the metadata and the
// content of an archived document of the scope, refusing one above max bytes
// (the charge module reads an uploaded syndic statement this way).
func (s Service) ReadInTx(ctx context.Context, id uuid.UUID, max int64) (View, []byte, error) {
	d, err := s.Repo.Get(ctx, id)
	if err != nil {
		return View{}, nil, err
	}
	if d.SizeBytes > max {
		return View{}, nil, invariant.New(TypeUploadTooLarge, d.ID.String())
	}
	obj, err := s.Store.Open(ctx, d.ObjectKey, d.ObjectVersion)
	if err != nil {
		return View{}, nil, err
	}
	defer func() { _ = obj.Close() }()
	b, err := io.ReadAll(io.LimitReader(obj, max+1))
	if err != nil {
		return View{}, nil, err
	}
	if SHA256(b) != d.SHA256 {
		return View{}, nil, fmt.Errorf("%w: %s", ErrChecksum, d.ObjectKey)
	}
	return view(d), b, nil
}

func (s Service) scan(ctx context.Context, key, version string) (Verdict, error) {
	obj, err := s.Store.Open(ctx, key, version)
	if err != nil {
		return Verdict{}, err
	}
	defer func() { _ = obj.Close() }()
	return s.Scanner.Scan(ctx, obj)
}

func (s Service) checkLinks(ctx context.Context, in UploadInput) error {
	ok, err := s.Repo.LinksExist(ctx, in.AssetID, in.LeaseID)
	if err != nil {
		return err
	}
	if !ok {
		return invariant.New(TypeLinkInvalid, "")
	}
	if in.SignsDocumentID == nil {
		return nil
	}
	target, err := s.Repo.Get(ctx, *in.SignsDocumentID)
	if errors.Is(err, invariant.ErrNotFound) {
		return invariant.New(TypeSignedTargetMiss, in.SignsDocumentID.String())
	}
	if err != nil {
		return err
	}
	if target.Source != SourceGenerated || Class(target.Class) != in.Class || target.LeaseID == nil || *target.LeaseID != *in.LeaseID {
		return invariant.New(TypeSignedTargetMiss, in.SignsDocumentID.String())
	}
	return nil
}

// cleanName keeps the base name of what the client sent, without control
// characters, at most 200 bytes; it is metadata only, never a key.
func cleanName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	var b bytes.Buffer
	for _, r := range name {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := b.String()
	for len(out) > 200 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	if out == "." || out == "/" {
		return ""
	}
	return out
}
