package portfolio

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// DiagnosticKind is a kind of mandatory diagnostic (RENT-SPEC-008: DPE, lead,
// asbestos, electricity and gas, natural-risk statement).
type DiagnosticKind string

// Diagnostic kinds.
const (
	DiagnosticDPE         DiagnosticKind = "dpe"
	DiagnosticERP         DiagnosticKind = "erp"
	DiagnosticElectricity DiagnosticKind = "electricity"
	DiagnosticGas         DiagnosticKind = "gas"
	DiagnosticLead        DiagnosticKind = "lead"
	DiagnosticAsbestos    DiagnosticKind = "asbestos"
)

// DiagnosticKinds lists every diagnostic kind.
var DiagnosticKinds = []DiagnosticKind{DiagnosticDPE, DiagnosticERP, DiagnosticElectricity, DiagnosticGas, DiagnosticLead, DiagnosticAsbestos}

// DPEClasses are the DPE energy classes, best first.
const DPEClasses = "ABCDEFG"

// Problem types of diagnostics.
const (
	TypeDiagnosticExists   = "diagnostic/already-recorded"
	TypeDiagnosticDocument = "diagnostic/document-invalid"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeDiagnosticExists:   problem.M(409, "Ce diagnostic est déjà enregistré pour ce bien ; modifiez-le.", "This diagnostic is already recorded for this asset; update it."),
		TypeDiagnosticDocument: problem.M(422, "Le document joint n’existe pas ou ne concerne pas ce bien.", "The attached document does not exist or does not concern this asset."),
	})
}

type diagnosticRow struct {
	ID                uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID           uuid.UUID  `gorm:"column:owner_id"`
	AssetID           uuid.UUID  `gorm:"column:asset_id"`
	Kind              string     `gorm:"column:kind"`
	DoneOn            *time.Time `gorm:"column:done_on"`
	ValidUntil        *time.Time `gorm:"column:valid_until"`
	Result            string     `gorm:"column:result"`
	NotRequiredReason string     `gorm:"column:not_required_reason"`
	DocumentID        *uuid.UUID `gorm:"column:document_id"`
	Version           int        `gorm:"column:version"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	CreatedBy         string     `gorm:"column:created_by"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
	UpdatedBy         string     `gorm:"column:updated_by"`
}

func (diagnosticRow) TableName() string { return "asset_diagnostics" }

// DiagnosticInput is the body of POST /assets/{id}/diagnostics and PUT
// /diagnostics/{id}. A kind the asset does not need is declared with
// not_required_reason instead of a date (Q-23).
type DiagnosticInput struct {
	Kind   DiagnosticKind `json:"kind" enums:"dpe,erp,electricity,gas,lead,asbestos"`
	DoneOn *period.Date   `json:"done_on" swaggertype:"string" format:"date"`
	// ValidUntil, when the report states one; the lease uses the earlier of
	// it and the legal validity in legal_params.
	ValidUntil *period.Date `json:"valid_until" swaggertype:"string" format:"date"`
	// Result is the energy class (A to G) for a DPE, free text otherwise.
	Result            string     `json:"result" example:"D"`
	NotRequiredReason string     `json:"not_required_reason"`
	DocumentID        *uuid.UUID `json:"document_id"`
}

// DiagnosticView is a diagnostic as the API returns it.
type DiagnosticView struct {
	ID      uuid.UUID `json:"id"`
	AssetID uuid.UUID `json:"asset_id"`
	DiagnosticInput
	// ExpiresOn is the last day the diagnostic is valid, when it expires:
	// the earlier of valid_until and its legal validity from legal_params
	// (an ERP is valid 6 months whatever its report says), as the lease
	// blockers and the calendar read it.
	ExpiresOn *period.Date `json:"expires_on,omitempty" swaggertype:"string" format:"date"`
	Version   int          `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// Expiry returns the last day a diagnostic is valid, and false when it does
// not expire: the earlier of the date its report states and its legal
// validity from legal_params; a DPE made before dpe_min_done_on is void.
func Expiry(d DiagnosticInput, rule params.Diagnostics) (period.Date, bool) {
	if d.DoneOn == nil {
		return period.Date{}, false
	}
	if d.Kind == DiagnosticDPE && !rule.DPEMinDoneOn.IsZero() && d.DoneOn.Before(rule.DPEMinDoneOn) {
		return d.DoneOn.AddDays(-1), true
	}
	var exp period.Date
	expires := false
	if m := rule.ValidityMonths[string(d.Kind)]; m > 0 {
		exp, expires = d.DoneOn.AddMonths(m).AddDays(-1), true
	}
	if d.ValidUntil != nil && (!expires || d.ValidUntil.Before(exp)) {
		exp, expires = *d.ValidUntil, true
	}
	return exp, expires
}

// withExpiry sets each view's ExpiresOn, with the diagnostics rule in force
// today.
func (s Service) withExpiry(ctx context.Context, vs []DiagnosticView) error {
	if len(vs) == 0 {
		return nil
	}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return err
	}
	rule, err := ps.Diagnostics(period.FromTime(s.Now().In(paris())))
	if err != nil {
		return err
	}
	for i := range vs {
		vs[i].ExpiresOn = nil
		if exp, ok := Expiry(vs[i].DiagnosticInput, rule); ok {
			vs[i].ExpiresOn = &exp
		}
	}
	return nil
}

func diagnosticView(d diagnosticRow) DiagnosticView {
	v := DiagnosticView{ID: d.ID, AssetID: d.AssetID, Version: d.Version, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		DiagnosticInput: DiagnosticInput{Kind: DiagnosticKind(d.Kind), Result: d.Result, NotRequiredReason: d.NotRequiredReason,
			DocumentID: d.DocumentID}}
	if d.DoneOn != nil {
		x := period.FromTime(*d.DoneOn)
		v.DoneOn = &x
	}
	if d.ValidUntil != nil {
		x := period.FromTime(*d.ValidUntil)
		v.ValidUntil = &x
	}
	return v
}

func (s Service) validateDiagnostic(in *DiagnosticInput) invariant.Fields {
	var f invariant.Fields
	known := false
	for _, k := range DiagnosticKinds {
		known = known || k == in.Kind
	}
	if !known {
		f.Add("kind", "Type attendu : dpe, erp, electricity, gas, lead ou asbestos.")
	}
	in.Result, in.NotRequiredReason = strings.TrimSpace(in.Result), strings.TrimSpace(in.NotRequiredReason)
	today := period.FromTime(s.Now().In(paris()))
	switch {
	case in.NotRequiredReason != "" && in.DoneOn != nil:
		f.Add("not_required_reason", "Un diagnostic réalisé ne peut pas être déclaré non requis.")
	case in.NotRequiredReason == "" && in.DoneOn == nil:
		f.Add("done_on", "La date du diagnostic est obligatoire, sauf s’il est déclaré non requis.")
	}
	if in.DoneOn != nil && today.Before(*in.DoneOn) {
		f.Add("done_on", "La date du diagnostic est dans le futur.")
	}
	if in.ValidUntil != nil && in.DoneOn != nil && in.ValidUntil.Before(*in.DoneOn) {
		f.Add("valid_until", "La fin de validité précède la date du diagnostic.")
	}
	if in.Kind == DiagnosticDPE && in.DoneOn != nil {
		in.Result = strings.ToUpper(in.Result)
		if len(in.Result) != 1 || !strings.Contains(DPEClasses, in.Result) {
			f.Add("result", "La classe énergétique du DPE est attendue (A à G).")
		}
	}
	return f
}

func diagnosticRowFrom(in DiagnosticInput) diagnosticRow {
	d := diagnosticRow{Kind: string(in.Kind), Result: in.Result, NotRequiredReason: in.NotRequiredReason, DocumentID: in.DocumentID}
	if in.DoneOn != nil {
		t := in.DoneOn.Time()
		d.DoneOn = &t
	}
	if in.ValidUntil != nil {
		t := in.ValidUntil.Time()
		d.ValidUntil = &t
	}
	return d
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

// Diagnostics returns the diagnostics of an asset.
func (s Service) Diagnostics(ctx context.Context, p *auth.Principal, requestID string, assetID uuid.UUID) ([]DiagnosticView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []DiagnosticView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Asset(ctx, assetID); err != nil {
			return err
		}
		rows, err := s.Repo.Diagnostics(ctx, []uuid.UUID{assetID})
		if err != nil {
			return err
		}
		for _, d := range rows {
			out = append(out, diagnosticView(d))
		}
		return s.withExpiry(ctx, out)
	})
	return out, err
}

// DiagnosticsInTx returns the diagnostics of the assets inside the current
// transaction (used by the lease service for its blockers).
func (s Service) DiagnosticsInTx(ctx context.Context, assetIDs []uuid.UUID) ([]DiagnosticView, error) {
	rows, err := s.Repo.Diagnostics(ctx, assetIDs)
	if err != nil {
		return nil, err
	}
	out := make([]DiagnosticView, 0, len(rows))
	for _, d := range rows {
		out = append(out, diagnosticView(d))
	}
	return out, s.withExpiry(ctx, out)
}

func (s Service) checkDiagnosticDocument(ctx context.Context, assetID uuid.UUID, doc *uuid.UUID) error {
	if doc == nil {
		return nil
	}
	ok, err := s.Repo.DocumentOfAsset(ctx, *doc, assetID)
	if err != nil {
		return err
	}
	if !ok {
		return invariant.New(TypeDiagnosticDocument, doc.String())
	}
	return nil
}

// CreateDiagnostic records a diagnostic of an asset; one per kind (Q-23).
func (s Service) CreateDiagnostic(ctx context.Context, p *auth.Principal, requestID string, assetID uuid.UUID, in DiagnosticInput) (DiagnosticView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DiagnosticView{}, err
	}
	if f := s.validateDiagnostic(&in); len(f) > 0 {
		return DiagnosticView{}, f
	}
	var out DiagnosticView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Asset(ctx, assetID); err != nil {
			return err
		}
		if err := s.checkDiagnosticDocument(ctx, assetID, in.DocumentID); err != nil {
			return err
		}
		now := s.now()
		d := diagnosticRowFrom(in)
		d.ID, d.OwnerID, d.AssetID, d.Version = s.NewID(), sc.OwnerID, assetID, 1
		d.CreatedAt, d.CreatedBy, d.UpdatedAt, d.UpdatedBy = now, p.Sub, now, p.Sub
		if err := s.Repo.InsertDiagnostic(ctx, d); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeDiagnosticExists, string(in.Kind))
			}
			return err
		}
		out = diagnosticView(d)
		if err := audit.Record(ctx, "diagnostic.create", audit.Entity{Type: "diagnostic", ID: d.ID.String()}, nil, out); err != nil {
			return err
		}
		vs := []DiagnosticView{out}
		if err := s.withExpiry(ctx, vs); err != nil {
			return err
		}
		out = vs[0]
		return nil
	})
	return out, err
}

// UpdateDiagnostic replaces a diagnostic when version matches; its kind and
// asset never change.
func (s Service) UpdateDiagnostic(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in DiagnosticInput) (DiagnosticView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DiagnosticView{}, err
	}
	if f := s.validateDiagnostic(&in); len(f) > 0 {
		return DiagnosticView{}, f
	}
	var out DiagnosticView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Diagnostic(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return invariant.ErrStale
		}
		if string(in.Kind) != cur.Kind {
			var f invariant.Fields
			f.Add("kind", "Le type d’un diagnostic ne change pas ; enregistrez-en un autre.")
			return f
		}
		if err := s.checkDiagnosticDocument(ctx, cur.AssetID, in.DocumentID); err != nil {
			return err
		}
		before := diagnosticView(cur)
		d := diagnosticRowFrom(in)
		d.ID, d.OwnerID, d.AssetID, d.Version = id, cur.OwnerID, cur.AssetID, version+1
		d.CreatedAt, d.CreatedBy, d.UpdatedAt, d.UpdatedBy = cur.CreatedAt, cur.CreatedBy, s.now(), p.Sub
		if err := s.Repo.UpdateDiagnostic(ctx, d, version); err != nil {
			return err
		}
		out = diagnosticView(d)
		if err := audit.Record(ctx, "diagnostic.update", audit.Entity{Type: "diagnostic", ID: id.String()}, before, out); err != nil {
			return err
		}
		vs := []DiagnosticView{out}
		if err := s.withExpiry(ctx, vs); err != nil {
			return err
		}
		out = vs[0]
		return nil
	})
	return out, err
}

// --- repository -----------------------------------------------------------------

// Diagnostics returns the diagnostics of the assets.
func (Repository) Diagnostics(ctx context.Context, assetIDs []uuid.UUID) ([]diagnosticRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []diagnosticRow
	err = t.Where("asset_id IN ?", assetIDs).Order("asset_id, kind").Find(&rows).Error
	return rows, err
}

// Diagnostic returns one diagnostic, locked for the transaction.
func (Repository) Diagnostic(ctx context.Context, id uuid.UUID) (diagnosticRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return diagnosticRow{}, err
	}
	var rows []diagnosticRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return diagnosticRow{}, err
	}
	if len(rows) == 0 {
		return diagnosticRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertDiagnostic creates a diagnostic.
func (Repository) InsertDiagnostic(ctx context.Context, d diagnosticRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&d).Error
}

// UpdateDiagnostic updates a diagnostic when version matches.
func (Repository) UpdateDiagnostic(ctx context.Context, d diagnosticRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&diagnosticRow{}).Where("id = ? AND version = ?", d.ID, version).Updates(map[string]any{
		"done_on": d.DoneOn, "valid_until": d.ValidUntil, "result": d.Result, "not_required_reason": d.NotRequiredReason,
		"document_id": d.DocumentID, "version": version + 1, "updated_at": d.UpdatedAt, "updated_by": d.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// DocumentOfAsset reports whether a document of the scope is linked to the
// asset, or to no asset at all.
func (Repository) DocumentOfAsset(ctx context.Context, doc, asset uuid.UUID) (bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Table("documents").Where("id = ? AND (asset_id IS NULL OR asset_id = ?)", doc, asset).Count(&n).Error
	return n > 0, err
}
