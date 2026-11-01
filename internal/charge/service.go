package charge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// RegularisationTemplate is the version of the statement template.
const RegularisationTemplate = "v1"

// Problem types of the charge module.
const (
	TypeChargeReconciled  = "charge/reconciled"
	TypeImportInvalid     = "charge/import-invalid"
	TypeDocumentInvalid   = "charge/document-invalid"
	TypeReconExists       = "reconciliation/already-exists"
	TypeExerciseInvalid   = "reconciliation/exercise-invalid"
	TypeDraftCharges      = "reconciliation/draft-charges"
	TypeNotSettled        = "reconciliation/not-settled"
	TypeLineNotEditable   = "reconciliation/line-not-editable"
	TypeSettlementInvalid = "reconciliation/settlement-invalid"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeInvalidTransition: problem.M(409, "Cette étape n’est pas possible dans l’état actuel de la régularisation.", "This step is not possible in the reconciliation's current state."),
		TypeChargeReconciled:  problem.M(409, "Cette charge figure dans une régularisation émise ; elle ne se modifie plus.", "This charge is part of an issued reconciliation; it can no longer change."),
		TypeImportInvalid:     problem.M(422, "Le relevé du syndic ne peut pas être lu avec ce paramétrage.", "The syndic statement cannot be read with this mapping."),
		TypeDocumentInvalid:   problem.M(422, "Le document indiqué n’existe pas ou n’est pas un relevé CSV déposé.", "The document given does not exist or is not an uploaded CSV statement."),
		TypeReconExists:       problem.M(409, "Une régularisation existe déjà pour ce bien et cet exercice.", "A reconciliation already exists for this asset and exercise."),
		TypeExerciseInvalid:   problem.M(422, "L’exercice commence un 1er du mois et finit un dernier jour du mois.", "The exercise starts on the 1st of a month and ends on a last day of a month."),
		TypeDraftCharges:      problem.M(409, "Des charges importées restent à valider pour cet exercice.", "Imported charges are still to be confirmed for this exercise."),
		TypeNotSettled:        problem.M(409, "Des soldes de régularisation restent à reporter sur les avis d’échéance.", "Some regularisation balances are still to be carried on rent calls."),
		TypeLineNotEditable:   problem.M(409, "Une ligne ne se modifie que sur une régularisation répartie, avant émission.", "A line can only change on an allocated reconciliation, before it is issued."),
		TypeSettlementInvalid: problem.M(422, "Ce mode de règlement n’est pas possible pour ce bail.", "This settlement is not possible for this lease."),
	})
}

// Settlement modes of a balance.
const (
	SettleFee     = "fee"
	SettleDeposit = "deposit"
	SettleDirect  = "direct"
	SettleNone    = "none"
)

// Categories lists every charge category.
var Categories = []Category{CategoryCoproProvision, CategoryCoproStatement, CategoryCoproRegularisationPrior, CategoryPropertyTax,
	CategoryTEOM, CategoryInsurance, CategoryRepairs, CategoryManagement, CategoryLoanInterest, CategoryLoanInsurance,
	CategoryRecoverableUtilities, CategoryIndemnity, CategoryOwnerExpenseChargedToTenant, CategoryUnrecoveredAtClose}

// Service implements the charges ledger and the reconciliation.
type Service struct {
	DB      *db.DB
	Repo    Repository
	Lease   lease.Service
	Billing billing.Service
	Docs    document.Service
	Issuer  document.Issuer
	Params  params.Repository
	Now     func() time.Time
	NewID   func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func (s Service) today() period.Date {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		loc = time.UTC
	}
	return period.FromTime(s.Now().In(loc))
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

func actorOf(ctx context.Context) string {
	sc, _ := db.ScopeFrom(ctx)
	if sc.ActorSub != "" {
		return sc.ActorSub
	}
	return "system:" + sc.Role
}

// --- charges ledger --------------------------------------------------------------

// Input is the body of POST and PUT /charges.
type Input struct {
	AssetID   uuid.UUID    `json:"asset_id"`
	ChargedOn period.Date  `json:"charged_on" swaggertype:"string" format:"date"`
	PaidOn    *period.Date `json:"paid_on" swaggertype:"string" format:"date"`
	Category  Category     `json:"category" enums:"copro_provision,copro_statement,copro_regularisation_prior,property_tax,teom,insurance,repairs,management,loan_interest,loan_insurance,recoverable_utilities,indemnity,owner_expense_charged_to_tenant,unrecovered_at_close"`
	Label     string       `json:"label"`
	// AmountCents is the whole charge; RecoverableCents the part recoverable
	// from the tenant (décret 87-713), 0 to the amount.
	AmountCents      int64 `json:"amount_cents"`
	RecoverableCents int64 `json:"recoverable_cents"`
	// ExerciseYear defaults to the year of charged_on.
	ExerciseYear int        `json:"exercise_year"`
	DocumentID   *uuid.UUID `json:"document_id"`
	// Status is draft or confirmed (default); only confirmed charges are
	// reconciled.
	Status string `json:"status" enums:"draft,confirmed"`
}

// View is a charge as the API returns it.
type View struct {
	ID uuid.UUID `json:"id"`
	Input
	// SyndicRecoverableCents is the syndic's own split of an imported line,
	// kept beside an owner's override.
	SyndicRecoverableCents *int64     `json:"syndic_recoverable_cents"`
	Source                 string     `json:"source" enums:"manual,syndic,invoice"`
	ReconciliationID       *uuid.UUID `json:"reconciliation_id"`
	Version                int        `json:"version"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

func chargeView(c chargeRow) View {
	v := View{ID: c.ID, SyndicRecoverableCents: c.SyndicRecoverableCents, Source: c.Source, ReconciliationID: c.ReconciliationID,
		Version: c.Version, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Input: Input{AssetID: c.AssetID, ChargedOn: period.FromTime(c.ChargedOn), Category: Category(c.Category), Label: c.Label,
			AmountCents: c.AmountCents, RecoverableCents: c.RecoverableCents, ExerciseYear: c.ExerciseYear, DocumentID: c.DocumentID, Status: c.Status}}
	if c.PaidOn != nil {
		d := period.FromTime(*c.PaidOn)
		v.PaidOn = &d
	}
	return v
}

func validateCharge(in *Input) invariant.Fields {
	var f invariant.Fields
	if !slices.Contains(Categories, in.Category) {
		f.Add("category", "Catégorie de charge inconnue.")
	}
	if in.ChargedOn.IsZero() {
		f.Add("charged_on", "La date de la charge est obligatoire.")
	}
	if in.AmountCents < 0 {
		f.Add("amount_cents", "Le montant ne peut pas être négatif.")
	}
	if in.RecoverableCents < 0 || in.RecoverableCents > in.AmountCents {
		f.Add("recoverable_cents", "La part récupérable est comprise entre 0 et le montant.")
	}
	if in.ExerciseYear == 0 {
		in.ExerciseYear = in.ChargedOn.Year
	}
	if in.Status == "" {
		in.Status = "confirmed"
	}
	if in.Status != "draft" && in.Status != "confirmed" {
		f.Add("status", "Statut attendu : draft ou confirmed.")
	}
	in.Label = strings.TrimSpace(in.Label)
	return f
}

func (s Service) checkLinks(ctx context.Context, asset uuid.UUID, doc *uuid.UUID) error {
	if _, err := s.Lease.Portfolio.AssetInTx(ctx, asset); err != nil {
		if errors.Is(err, invariant.ErrNotFound) {
			var f invariant.Fields
			f.Add("asset_id", "Ce bien n’existe pas dans ce portefeuille.")
			return f
		}
		return err
	}
	if doc != nil {
		if _, err := s.Docs.Repo.Get(ctx, *doc); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				return invariant.New(TypeDocumentInvalid, doc.String())
			}
			return err
		}
	}
	return nil
}

// ListCharges returns a page of the scope's charges.
func (s Service) ListCharges(ctx context.Context, p *auth.Principal, requestID string, f Filter, offset, limit int) ([]View, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []View
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.ListCharges(ctx, f, offset, limit)
		for _, r := range rows {
			out = append(out, chargeView(r))
		}
		total = n
		return err
	})
	return out, total, err
}

// GetCharge returns one charge.
func (s Service) GetCharge(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.Repo.Charge(ctx, id)
		out = chargeView(c)
		return err
	})
	return out, err
}

// CreateCharge records a charge in the ledger (source manual).
func (s Service) CreateCharge(ctx context.Context, p *auth.Principal, requestID string, in Input) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if f := validateCharge(&in); len(f) > 0 {
		return View{}, f
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkLinks(ctx, in.AssetID, in.DocumentID); err != nil {
			return err
		}
		now := s.now()
		c := chargeRowFrom(in)
		c.ID, c.OwnerID, c.Source, c.Version = s.NewID(), sc.OwnerID, "manual", 1
		c.CreatedAt, c.CreatedBy, c.UpdatedAt, c.UpdatedBy = now, p.Sub, now, p.Sub
		if err := s.Repo.InsertCharges(ctx, []chargeRow{c}); err != nil {
			return err
		}
		out = chargeView(c)
		return audit.Record(ctx, "charge.create", audit.Entity{Type: "charge", ID: c.ID.String()}, nil, out)
	})
	return out, err
}

func chargeRowFrom(in Input) chargeRow {
	c := chargeRow{AssetID: in.AssetID, ChargedOn: in.ChargedOn.Time(), Category: string(in.Category), Label: in.Label,
		AmountCents: in.AmountCents, RecoverableCents: in.RecoverableCents, ExerciseYear: in.ExerciseYear, DocumentID: in.DocumentID, Status: in.Status}
	if in.PaidOn != nil {
		t := in.PaidOn.Time()
		c.PaidOn = &t
	}
	return c
}

// UpdateCharge replaces a charge when version matches: confirming an imported
// line or overriding the syndic's recoverable split, which the audit trail
// records with the split before (RENT-SPEC-007 step 1). A charge used by an
// issued reconciliation never changes, nor does its asset.
func (s Service) UpdateCharge(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in Input) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if f := validateCharge(&in); len(f) > 0 {
		return View{}, f
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Charge(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return invariant.ErrStale
		}
		if cur.ReconciliationID != nil {
			return invariant.New(TypeChargeReconciled, cur.ReconciliationID.String())
		}
		if in.AssetID != cur.AssetID {
			var f invariant.Fields
			f.Add("asset_id", "Le bien d’une charge ne change pas.")
			return f
		}
		if err := s.checkLinks(ctx, in.AssetID, in.DocumentID); err != nil {
			return err
		}
		before := chargeView(cur)
		c := chargeRowFrom(in)
		c.ID, c.OwnerID, c.Source, c.SyndicRecoverableCents = id, cur.OwnerID, cur.Source, cur.SyndicRecoverableCents
		c.CreatedAt, c.CreatedBy, c.UpdatedAt, c.UpdatedBy, c.Version = cur.CreatedAt, cur.CreatedBy, s.now(), p.Sub, version+1
		if err := s.Repo.UpdateCharge(ctx, c, version); err != nil {
			return err
		}
		out = chargeView(c)
		return audit.Record(ctx, "charge.update", audit.Entity{Type: "charge", ID: id.String()}, before, out)
	})
	return out, err
}

// ImportInput is the body of POST /charges/import/syndic: the syndic's
// statement exported as CSV and uploaded through POST /documents (scanned,
// class supporting), and how to read it.
type ImportInput struct {
	AssetID uuid.UUID `json:"asset_id"`
	// DocumentID is the uploaded CSV.
	DocumentID uuid.UUID `json:"document_id"`
	// StatementDocumentID is the statement's PDF, attached to every line.
	StatementDocumentID *uuid.UUID  `json:"statement_document_id"`
	ChargedOn           period.Date `json:"charged_on" swaggertype:"string" format:"date"`
	ExerciseYear        int         `json:"exercise_year"`
	Mapping             Mapping     `json:"mapping"`
}

// ImportSyndic creates one draft charge (copro_statement, source syndic) per
// line of a syndic statement, with the syndic's own recoverable split kept
// beside the recoverable part the owner may override before confirming
// (RENT-SPEC-007 step 1). A statement the mapping cannot read whole imports
// nothing.
func (s Service) ImportSyndic(ctx context.Context, p *auth.Principal, requestID string, in ImportInput) ([]View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	var f invariant.Fields
	if in.ChargedOn.IsZero() {
		f.Add("charged_on", "La date du relevé est obligatoire.")
	}
	if in.ExerciseYear == 0 {
		in.ExerciseYear = in.ChargedOn.Year
	}
	if err := f.Err(); err != nil {
		return nil, err
	}
	out := []View{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkLinks(ctx, in.AssetID, in.StatementDocumentID); err != nil {
			return err
		}
		meta, data, err := s.Docs.ReadInTx(ctx, in.DocumentID, document.MaxUploadBytes)
		if errors.Is(err, invariant.ErrNotFound) {
			return invariant.New(TypeDocumentInvalid, in.DocumentID.String())
		}
		if err != nil {
			return err
		}
		if meta.Source != document.SourceUpload || meta.ContentType != "text/csv" {
			return invariant.New(TypeDocumentInvalid, meta.ContentType)
		}
		lines, err := ParseSyndic(data, in.Mapping)
		if err != nil {
			return invariant.New(TypeImportInvalid, err.Error())
		}
		doc := in.StatementDocumentID
		if doc == nil {
			doc = &in.DocumentID
		}
		now := s.now()
		rows := make([]chargeRow, len(lines))
		for i, l := range lines {
			syndic := int64(l.RecoverableCents)
			rows[i] = chargeRow{ID: s.NewID(), OwnerID: sc.OwnerID, AssetID: in.AssetID, ChargedOn: in.ChargedOn.Time(),
				Category: string(CategoryCoproStatement), Label: l.Label, AmountCents: int64(l.AmountCents),
				RecoverableCents: int64(l.RecoverableCents), SyndicRecoverableCents: &syndic, ExerciseYear: in.ExerciseYear,
				DocumentID: doc, Source: "syndic", Status: "draft", Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
			out = append(out, chargeView(rows[i]))
		}
		if err := s.Repo.InsertCharges(ctx, rows); err != nil {
			return err
		}
		return audit.Record(ctx, "charge.import", audit.Entity{Type: "document", ID: in.DocumentID.String()}, nil,
			map[string]any{"asset_id": in.AssetID, "mapping": in.Mapping, "charges": out})
	})
	return out, err
}

// --- reconciliation ----------------------------------------------------------------

// ReconInput is the body of POST /reconciliations: one asset and exercise.
type ReconInput struct {
	AssetID       uuid.UUID   `json:"asset_id"`
	ExerciseStart period.Date `json:"exercise_start" swaggertype:"string" format:"date"`
	ExerciseEnd   period.Date `json:"exercise_end" swaggertype:"string" format:"date"`
}

// Totals are the figures of an allocation.
type Totals struct {
	ExerciseDays        int         `json:"exercise_days"`
	TotalCents          money.Cents `json:"total_cents"`
	RecoverableCents    money.Cents `json:"recoverable_cents"`
	NonRecoverableCents money.Cents `json:"non_recoverable_cents"`
	VacancyDays         int         `json:"vacancy_days"`
	VacancyCents        money.Cents `json:"vacancy_cents"`
	// Cancellations are the numbers of the documents that cancelled the
	// statements.
	Cancellations []string `json:"cancellations,omitempty"`
}

// LineView is the reconciliation of one lease.
type LineView struct {
	ID                     uuid.UUID     `json:"id"`
	LeaseID                uuid.UUID     `json:"lease_id"`
	Days                   int           `json:"days"`
	AllocatedCents         int64         `json:"allocated_cents"`
	ProvisionsCents        int64         `json:"provisions_cents"`
	BalanceCents           int64         `json:"balance_cents"`
	Spread                 bool          `json:"spread"`
	Instalments            []money.Cents `json:"instalments"`
	ProposedProvisionCents int64         `json:"proposed_provision_cents"`
	NewProvisionCents      int64         `json:"new_provision_cents"`
	ApplyProvision         bool          `json:"apply_provision"`
	Settlement             string        `json:"settlement" enums:"fee,deposit,direct,none"`
	DocumentID             *uuid.UUID    `json:"document_id"`
	Number                 string        `json:"number"`
	Version                int           `json:"version"`
}

// ReconView is a reconciliation as the API returns it.
type ReconView struct {
	ID                     uuid.UUID   `json:"id"`
	AssetID                uuid.UUID   `json:"asset_id"`
	ExerciseYear           int         `json:"exercise_year"`
	ExerciseStart          period.Date `json:"exercise_start" swaggertype:"string" format:"date"`
	ExerciseEnd            period.Date `json:"exercise_end" swaggertype:"string" format:"date"`
	State                  State       `json:"state" enums:"draft,allocated,issued,settled,cancelled"`
	Totals                 Totals      `json:"totals"`
	ChargeIDs              []uuid.UUID `json:"charge_ids"`
	IssuedAt               *time.Time  `json:"issued_at"`
	CorrectsID             *uuid.UUID  `json:"corrects_id"`
	CancellationDocumentID *uuid.UUID  `json:"cancellation_document_id"`
	Lines                  []LineView  `json:"lines"`
	Version                int         `json:"version"`
	CreatedAt              time.Time   `json:"created_at"`
	UpdatedAt              time.Time   `json:"updated_at"`
}

func lineView(l lineRow) LineView {
	v := LineView{ID: l.ID, LeaseID: l.LeaseID, Days: l.Days, AllocatedCents: l.AllocatedCents, ProvisionsCents: l.ProvisionsCents,
		BalanceCents: l.BalanceCents, Spread: l.Spread, ProposedProvisionCents: l.ProposedProvisionCents, NewProvisionCents: l.NewProvisionCents,
		ApplyProvision: l.ApplyProvision, Settlement: l.Settlement, DocumentID: l.DocumentID, Number: l.Number, Version: l.Version,
		Instalments: []money.Cents{}}
	_ = json.Unmarshal(l.Instalments, &v.Instalments)
	return v
}

func reconView(r reconRow, lines []lineRow) ReconView {
	v := ReconView{ID: r.ID, AssetID: r.AssetID, ExerciseYear: r.ExerciseYear, ExerciseStart: period.FromTime(r.ExerciseStart),
		ExerciseEnd: period.FromTime(r.ExerciseEnd), State: State(r.State), IssuedAt: r.IssuedAt, CorrectsID: r.CorrectsID,
		CancellationDocumentID: r.CancellationDocumentID, Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		ChargeIDs: []uuid.UUID{}, Lines: []LineView{}}
	_ = json.Unmarshal(r.Totals, &v.Totals)
	_ = json.Unmarshal(r.ChargeIDs, &v.ChargeIDs)
	for _, l := range lines {
		v.Lines = append(v.Lines, lineView(l))
	}
	return v
}

// ListRecons returns a page of the scope's reconciliations, without lines.
func (s Service) ListRecons(ctx context.Context, p *auth.Principal, requestID string, f ReconFilter, offset, limit int) ([]ReconView, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []ReconView
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.ListRecons(ctx, f, offset, limit)
		for _, r := range rows {
			out = append(out, reconView(r, nil))
		}
		total = n
		return err
	})
	return out, total, err
}

// GetRecon returns one reconciliation with its lines.
func (s Service) GetRecon(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (ReconView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ReconView{}, err
	}
	var out ReconView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Recon(ctx, id, false)
		if err != nil {
			return err
		}
		lines, err := s.Repo.Lines(ctx, id)
		out = reconView(r, lines)
		return err
	})
	return out, err
}

// CreateRecon starts the reconciliation of an asset's exercise, in draft. An
// exercise runs from the 1st of a month to the last day of a month (Q-78);
// the exercise year is the year it ends. A new reconciliation of an exercise
// whose previous one was cancelled corrects it.
func (s Service) CreateRecon(ctx context.Context, p *auth.Principal, requestID string, in ReconInput) (ReconView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ReconView{}, err
	}
	if in.ExerciseStart.IsZero() || in.ExerciseEnd.IsZero() || in.ExerciseStart.Day != 1 || in.ExerciseEnd != in.ExerciseEnd.Period().Last() ||
		in.ExerciseEnd.Before(in.ExerciseStart) {
		return ReconView{}, invariant.New(TypeExerciseInvalid, fmt.Sprintf("%s to %s", in.ExerciseStart, in.ExerciseEnd))
	}
	var out ReconView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkLinks(ctx, in.AssetID, nil); err != nil {
			return err
		}
		prev, _, err := s.Repo.ListRecons(ctx, ReconFilter{AssetID: &in.AssetID, Year: in.ExerciseEnd.Year}, 0, 1)
		if err != nil {
			return err
		}
		now := s.now()
		r := reconRow{ID: s.NewID(), OwnerID: sc.OwnerID, AssetID: in.AssetID, ExerciseYear: in.ExerciseEnd.Year,
			ExerciseStart: in.ExerciseStart.Time(), ExerciseEnd: in.ExerciseEnd.Time(), State: string(StateDraft),
			Totals: json.RawMessage("{}"), ChargeIDs: json.RawMessage("[]"), Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if len(prev) > 0 && prev[0].State == string(StateCancelled) {
			r.CorrectsID = &prev[0].ID
		}
		if err := s.Repo.InsertRecon(ctx, r); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeReconExists, in.AssetID.String())
			}
			return err
		}
		out = reconView(r, nil)
		return audit.Record(ctx, "reconciliation.create", audit.Entity{Type: "reconciliation", ID: r.ID.String()}, nil, out)
	})
	return out, err
}

// allocation is a computed reconciliation with what issuing it needs.
type allocation struct {
	alloc   Allocation
	charges []chargeRow
	leases  map[string]lease.Lease
	views   map[string]lease.View
	lines   []lineRow
	totals  Totals
	ps      params.Set
}

// compute runs the reconciliation of one asset's exercise as of today
// (RENT-SPEC-007 steps 2 to 5 and 8): the confirmed charges of the
// exercise, the leases that covered the asset, the provisions collected from
// matched payments, then Allocate. Choices the owner made on a previous
// allocation's lines (provision, settlement) are kept, lease by lease.
func (s Service) compute(ctx context.Context, r reconRow, keep []lineRow) (allocation, error) {
	var out allocation
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	out.ps = ps
	rows, err := s.Repo.ExerciseCharges(ctx, r.AssetID, r.ExerciseStart, r.ExerciseEnd)
	if err != nil {
		return out, err
	}
	var charges []Charge
	for _, c := range rows {
		if c.ReconciliationID != nil && *c.ReconciliationID != r.ID {
			continue // already reconciled by another exercise
		}
		if c.Status == "draft" {
			return out, invariant.New(TypeDraftCharges, c.ID.String())
		}
		rec := money.Cents(c.RecoverableCents)
		charges = append(charges, Charge{ID: c.ID.String(), AssetID: c.AssetID.String(), Category: Category(c.Category),
			AmountCents: money.Cents(c.AmountCents), ChargedOn: period.FromTime(c.ChargedOn), RecoverableAmountCents: &rec})
		out.charges = append(out.charges, c)
	}
	ids, err := s.Repo.LeasesOnAsset(ctx, r.AssetID, r.ExerciseStart, r.ExerciseEnd)
	if err != nil {
		return out, err
	}
	start, end := period.FromTime(r.ExerciseStart), period.FromTime(r.ExerciseEnd)
	out.leases, out.views = map[string]lease.Lease{}, map[string]lease.View{}
	var leases []lease.Lease
	provisions := map[string]money.Cents{}
	for _, id := range ids {
		l, v, err := s.Lease.DomainInTx(ctx, id)
		if err != nil {
			return out, err
		}
		leases = append(leases, l)
		out.leases[l.ID], out.views[l.ID] = l, v
		if l.Kind == lease.KindMobility {
			continue // flat-rate charges, never regularised (Q-76)
		}
		calls, err := s.Repo.PaidCalls(ctx, id, start.Period().String(), end.Period().String())
		if err != nil {
			return out, err
		}
		if provisions[l.ID], err = ProvisionsCollected(calls); err != nil {
			return out, err
		}
	}
	ex := Exercise{AssetID: r.AssetID.String(), Start: start, End: end, IssuedOn: s.today()}
	out.alloc, err = Allocate(charges, leases, ex, provisions, ps)
	if err != nil {
		return out, err
	}
	out.totals = Totals{ExerciseDays: out.alloc.ExerciseDays, RecoverableCents: out.alloc.RecoverableCents,
		NonRecoverableCents: out.alloc.NonRecoverableCents, VacancyDays: out.alloc.VacancyDays, VacancyCents: out.alloc.VacancyCents}
	out.totals.TotalCents = out.totals.RecoverableCents + out.totals.NonRecoverableCents
	prior := map[uuid.UUID]lineRow{}
	for _, k := range keep {
		prior[k.LeaseID] = k
	}
	now, by := s.now(), actorOf(ctx)
	for _, ln := range out.alloc.Lines {
		l := out.leases[ln.LeaseID]
		lid, _ := uuid.Parse(ln.LeaseID)
		row := lineRow{ID: s.NewID(), OwnerID: r.OwnerID, ReconciliationID: r.ID, LeaseID: lid, Days: ln.Days,
			AllocatedCents: int64(ln.AllocatedCents), ProvisionsCents: int64(ln.ProvisionsCents), BalanceCents: int64(ln.BalanceCents),
			Spread: ln.Spread, ProposedProvisionCents: int64(ln.NewProvisionCents), NewProvisionCents: int64(ln.NewProvisionCents),
			Version: 1, CreatedAt: now, CreatedBy: by, UpdatedAt: now, UpdatedBy: by}
		row.Instalments, _ = json.Marshal(ln.Instalments)
		if ln.Instalments == nil {
			row.Instalments = json.RawMessage("[]")
		}
		switch {
		case l.Kind == lease.KindMobility:
			row.BalanceCents, row.ProvisionsCents, row.Spread, row.Instalments = 0, 0, false, json.RawMessage("[]")
			row.ProposedProvisionCents, row.NewProvisionCents, row.Settlement = 0, 0, SettleNone
		case row.BalanceCents == 0:
			row.Settlement = SettleNone
		case l.State.Billable():
			row.Settlement = SettleFee
		default:
			held, err := s.Lease.DepositHeldInTx(ctx, lid)
			if err != nil {
				return out, err
			}
			row.Settlement = SettleDirect
			if held {
				row.Settlement = SettleDeposit
			}
		}
		if p, ok := prior[lid]; ok && l.Kind != lease.KindMobility {
			row.ApplyProvision, row.NewProvisionCents = p.ApplyProvision, p.NewProvisionCents
			if row.BalanceCents != 0 && p.Settlement != SettleNone {
				row.Settlement = p.Settlement
			}
		}
		out.lines = append(out.lines, row)
	}
	return out, nil
}

// TransitionInput is the body of POST /reconciliations/{id}/transitions.
type TransitionInput struct {
	Action Action `json:"action" enums:"allocate,issue,settle,cancel"`
	// Reason is stated on the cancelling documents.
	Reason string `json:"reason"`
}

// Transition moves a reconciliation along draft → allocated → issued →
// settled, or cancels an issued one (RENT-SPEC-007):
//   - allocate computes (again) the allocation and its lines;
//   - issue recomputes it as of today, issues one statement RG-… per lease
//     (none for a mobility lease), posts each balance for the next rent calls
//     (in 12 instalments when late), applies the provisions the owner
//     validated and freezes the charges; the statements are e-mailed after
//     the commit;
//   - settle needs every posting carried by a live rent call;
//   - cancel issues a cancelling document per statement, voids the postings
//     not yet carried and reverses the ones carried, and releases the charges
//     for a corrected reconciliation.
func (s Service) Transition(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in TransitionInput) (ReconView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ReconView{}, err
	}
	var out ReconView
	var mail []billing.IssuedDoc
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Recon(ctx, id, true)
		if err != nil {
			return err
		}
		lines, err := s.Repo.Lines(ctx, id)
		if err != nil {
			return err
		}
		before := reconView(r, lines)
		to, err := Next(State(r.State), in.Action)
		if err != nil {
			return err
		}
		switch in.Action {
		case ActionAllocate:
			if lines, err = s.allocate(ctx, &r, lines); err != nil {
				return err
			}
		case ActionIssue:
			if lines, mail, err = s.issue(ctx, sc, &r, lines); err != nil {
				return err
			}
		case ActionSettle:
			ps, err := s.Repo.Postings(ctx, id)
			if err != nil {
				return err
			}
			for _, x := range ps {
				if !x.Carried {
					return invariant.New(TypeNotSettled, x.Reference)
				}
			}
		case ActionCancel:
			if mail, err = s.cancel(ctx, sc, &r, lines, strings.TrimSpace(in.Reason)); err != nil {
				return err
			}
		}
		r.State, r.UpdatedAt, r.UpdatedBy = string(to), s.now(), p.Sub
		if err := s.Repo.UpdateRecon(ctx, r, before.Version); err != nil {
			return err
		}
		r.Version = before.Version + 1
		out = reconView(r, lines)
		return audit.Record(ctx, "reconciliation."+string(in.Action), audit.Entity{Type: "reconciliation", ID: id.String()}, before, out)
	})
	if err == nil && len(mail) > 0 {
		s.Billing.Deliver(ctx, s.DB, sc, mail)
	}
	return out, err
}

func (s Service) allocate(ctx context.Context, r *reconRow, keep []lineRow) ([]lineRow, error) {
	a, err := s.compute(ctx, *r, keep)
	if err != nil {
		return nil, err
	}
	if err := s.Repo.ReplaceLines(ctx, r.ID, a.lines); err != nil {
		return nil, err
	}
	r.Totals, _ = json.Marshal(a.totals)
	r.ChargeIDs, _ = json.Marshal(a.alloc.ChargeIDs)
	return a.lines, nil
}

func (s Service) issue(ctx context.Context, sc db.Scope, r *reconRow, keep []lineRow) ([]lineRow, []billing.IssuedDoc, error) {
	a, err := s.compute(ctx, *r, keep)
	if err != nil {
		return nil, nil, err
	}
	if err := s.Repo.ReplaceLines(ctx, r.ID, a.lines); err != nil {
		return nil, nil, err
	}
	asset, err := s.Lease.Portfolio.AssetInTx(ctx, r.AssetID)
	if err != nil {
		return nil, nil, err
	}
	assetLabel := asset.Address
	if asset.Lot != "" {
		assetLabel += ", lot " + asset.Lot
	}
	today := s.today()
	months, err := a.ps.Int64(params.KeySupportingDocsMonths, today)
	if err != nil {
		return nil, nil, err
	}
	first := today.Period().Next()
	var charges []document.RegularisationCharge
	var chargeIDs []uuid.UUID
	for _, c := range a.charges {
		if !Category(c.Category).Reconciled() {
			continue
		}
		charges = append(charges, document.RegularisationCharge{Category: c.Category, Label: c.Label, ChargedOn: period.FromTime(c.ChargedOn),
			AmountCents: money.Cents(c.AmountCents), RecoverableCents: money.Cents(c.RecoverableCents)})
		chargeIDs = append(chargeIDs, c.ID)
	}
	exEnd := period.FromTime(r.ExerciseEnd)
	var mail []billing.IssuedDoc
	var postings []PostingRow
	now, by := s.now(), actorOf(ctx)
	for i := range a.lines {
		ln := &a.lines[i]
		l := a.leases[ln.LeaseID.String()]
		if l.Kind == lease.KindMobility {
			continue
		}
		rec, err := s.Billing.Repo.Record(ctx, ln.LeaseID)
		if err != nil {
			return nil, nil, err
		}
		var tenants []string
		for _, t := range rec.Tenants {
			tenants = append(tenants, strings.Join(t.Names, " et "))
		}
		var inst []money.Cents
		_ = json.Unmarshal(ln.Instalments, &inst)
		data := document.RegularisationData{Owner: document.Party{Name: rec.OwnerName, Address: rec.OwnerAddress}, Tenants: tenants,
			Asset: assetLabel, ExerciseStart: period.FromTime(r.ExerciseStart), ExerciseEnd: exEnd, Charges: charges,
			TotalCents: a.totals.TotalCents, RecoverableCents: a.totals.RecoverableCents, ExerciseDays: a.totals.ExerciseDays,
			LeaseDays: ln.Days, AllocatedCents: money.Cents(ln.AllocatedCents), ProvisionsCents: money.Cents(ln.ProvisionsCents),
			BalanceCents: money.Cents(ln.BalanceCents), Settlement: ln.Settlement, Spread: ln.Spread, Instalments: inst,
			FirstPeriod: first, SupportingUntil: today.AddMonths(int(months))}
		if ln.ApplyProvision && l.State.Billable() {
			data.NewProvisionCents = money.Cents(ln.NewProvisionCents)
		}
		lid := ln.LeaseID
		exPeriod := exEnd.Period()
		snap := map[string]any{"template": "regularisation/" + RegularisationTemplate, "reconciliation_id": r.ID, "line": lineView(*ln),
			"totals": a.totals, "allocation": a.alloc, "charges": charges, "lease": a.views[ln.LeaseID.String()]}
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRegularisation, TemplateVersion: RegularisationTemplate,
			AssetID: r.AssetID, LeaseID: &lid, Period: &exPeriod, Snapshot: snap,
			Data: func(number string, issued period.Date) any {
				data.Number, data.IssuedOn = number, issued
				return data
			}}, a.ps)
		if err != nil {
			return nil, nil, err
		}
		ln.DocumentID, ln.Number, ln.UpdatedAt = &iss.DocumentID, iss.Number, now
		if err := s.Repo.UpdateLine(ctx, *ln, ln.Version); err != nil {
			return nil, nil, err
		}
		ln.Version++
		if ln.Settlement == SettleFee && ln.BalanceCents != 0 {
			if ln.Spread {
				for k, amount := range inst {
					p := first
					for range k {
						p = p.Next()
					}
					postings = append(postings, PostingRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: lid, ReconciliationID: r.ID, LineID: ln.ID,
						Reference: iss.Number, Instalment: k + 1, Of: len(inst), AmountCents: int64(amount), DueFrom: p.String(),
						CreatedAt: now, CreatedBy: by})
				}
			} else {
				postings = append(postings, PostingRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: lid, ReconciliationID: r.ID, LineID: ln.ID,
					Reference: iss.Number, AmountCents: ln.BalanceCents, DueFrom: first.String(), CreatedAt: now, CreatedBy: by})
			}
		}
		if ln.ApplyProvision && l.State.Billable() && ln.NewProvisionCents != int64(l.ProvisionCents) {
			if err := s.Lease.SetProvisionInTx(ctx, lid, ln.NewProvisionCents, by); err != nil {
				return nil, nil, err
			}
		}
		doc, err := s.Billing.StatementDoc(ctx, lid, billing.KindRegularisation, iss, fmt.Sprintf("exercice %d", r.ExerciseYear))
		if err != nil {
			return nil, nil, err
		}
		mail = append(mail, doc)
	}
	if err := s.Repo.InsertPostings(ctx, postings); err != nil {
		return nil, nil, err
	}
	rid := r.ID
	if err := s.Repo.FreezeCharges(ctx, chargeIDs, &rid, now, by); err != nil {
		return nil, nil, err
	}
	r.Totals, _ = json.Marshal(a.totals)
	r.ChargeIDs, _ = json.Marshal(a.alloc.ChargeIDs)
	r.IssuedAt = &now
	return a.lines, mail, nil
}

func (s Service) cancel(ctx context.Context, sc db.Scope, r *reconRow, lines []lineRow, reason string) ([]billing.IssuedDoc, error) {
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return nil, err
	}
	if reason == "" {
		reason = "rectification du décompte"
	}
	var totals Totals
	_ = json.Unmarshal(r.Totals, &totals)
	exEnd := period.FromTime(r.ExerciseEnd).Period()
	issuedOn := period.FromTime(r.IssuedAt.In(time.UTC))
	var mail []billing.IssuedDoc
	for _, ln := range lines {
		if ln.DocumentID == nil {
			continue
		}
		rec, err := s.Billing.Repo.Record(ctx, ln.LeaseID)
		if err != nil {
			return nil, err
		}
		var tenants []string
		for _, t := range rec.Tenants {
			tenants = append(tenants, strings.Join(t.Names, " et "))
		}
		lid, number := ln.LeaseID, ln.Number
		data := document.CancellationData{Owner: document.Party{Name: rec.OwnerName, Address: rec.OwnerAddress}, Tenants: tenants,
			CancelledNumber: number, CancelledIssuedOn: issuedOn, Period: exEnd, Reason: reason,
			Lines:      []document.Line{{Label: "Solde de régularisation des charges", Detail: number, AmountCents: -money.Cents(ln.BalanceCents)}},
			TotalCents: -money.Cents(ln.BalanceCents)}
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRegularisation, Template: document.ClassCancellation,
			TemplateVersion: billing.CancellationTemplate, AssetID: r.AssetID, LeaseID: &lid, Period: &exEnd,
			Snapshot: map[string]any{"cancels": number, "reconciliation_id": r.ID, "reason": reason, "line": lineView(ln)},
			Data: func(n string, issued period.Date) any {
				data.Number, data.IssuedOn = n, issued
				return data
			}}, ps)
		if err != nil {
			return nil, err
		}
		totals.Cancellations = append(totals.Cancellations, iss.Number)
		if r.CancellationDocumentID == nil {
			r.CancellationDocumentID = &iss.DocumentID
		}
		doc, err := s.Billing.StatementDoc(ctx, lid, billing.KindCancellation, iss, fmt.Sprintf("exercice %d", r.ExerciseYear))
		if err != nil {
			return nil, err
		}
		mail = append(mail, doc)
	}
	posts, err := s.Repo.Postings(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	now, by := s.now(), actorOf(ctx)
	next := s.today().Period().Next()
	var void []uuid.UUID
	var reverse []PostingRow
	for _, x := range posts {
		if !x.Carried {
			void = append(void, x.ID)
			continue
		}
		reverse = append(reverse, PostingRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: x.LeaseID, ReconciliationID: r.ID,
			LineID: x.LineID, Reference: "annulation " + x.Reference, AmountCents: -x.AmountCents, DueFrom: next.String(),
			CreatedAt: now, CreatedBy: by})
	}
	if err := s.Repo.VoidPostings(ctx, void, now); err != nil {
		return nil, err
	}
	if err := s.Repo.InsertPostings(ctx, reverse); err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	_ = json.Unmarshal(r.ChargeIDs, &ids)
	if err := s.Repo.FreezeCharges(ctx, ids, nil, now, by); err != nil {
		return nil, err
	}
	r.Totals, _ = json.Marshal(totals)
	return mail, nil
}

// LineInput is the body of PUT /reconciliations/{id}/lines/{lineId}: the
// owner's validation of the new provision (RENT-SPEC-007 step 8) and the
// settlement of the balance.
type LineInput struct {
	ApplyProvision    bool   `json:"apply_provision"`
	NewProvisionCents *int64 `json:"new_provision_cents"`
	Settlement        string `json:"settlement" enums:"fee,deposit,direct"`
}

// UpdateLine records the owner's choices on a line of an allocated
// reconciliation, when version matches: the provision to apply from the next
// month (the proposal by default) and how the balance is settled — on the
// next rent calls for a lease still billed, against the deposit it still
// holds, or directly.
func (s Service) UpdateLine(ctx context.Context, p *auth.Principal, requestID string, id, lineID uuid.UUID, version int, in LineInput) (LineView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return LineView{}, err
	}
	var out LineView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Recon(ctx, id, true)
		if err != nil {
			return err
		}
		if State(r.State) != StateAllocated {
			return invariant.New(TypeLineNotEditable, r.State)
		}
		lines, err := s.Repo.Lines(ctx, id)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(lines, func(l lineRow) bool { return l.ID == lineID })
		if i < 0 {
			return invariant.ErrNotFound
		}
		cur := lines[i]
		if cur.Version != version {
			return invariant.ErrStale
		}
		if cur.Settlement == SettleNone && (in.Settlement != "" || in.ApplyProvision) && cur.ProposedProvisionCents == 0 && cur.BalanceCents == 0 {
			return invariant.New(TypeSettlementInvalid, "nothing to settle on this line")
		}
		l, _, err := s.Lease.DomainInTx(ctx, cur.LeaseID)
		if err != nil {
			return err
		}
		next := cur
		next.ApplyProvision = in.ApplyProvision
		if in.NewProvisionCents != nil {
			if *in.NewProvisionCents < 0 {
				var f invariant.Fields
				f.Add("new_provision_cents", "La provision ne peut pas être négative.")
				return f
			}
			next.NewProvisionCents = *in.NewProvisionCents
		}
		if in.ApplyProvision && (!l.State.Billable() || l.Kind == lease.KindMobility) {
			return invariant.New(TypeSettlementInvalid, "no provision to set on a lease no longer billed")
		}
		if in.Settlement != "" && in.Settlement != cur.Settlement {
			switch in.Settlement {
			case SettleFee:
				if !l.State.Billable() {
					return invariant.New(TypeSettlementInvalid, "the lease is no longer billed")
				}
			case SettleDeposit:
				held, err := s.Lease.DepositHeldInTx(ctx, cur.LeaseID)
				if err != nil {
					return err
				}
				if !held {
					return invariant.New(TypeSettlementInvalid, "no deposit held")
				}
			case SettleDirect:
			default:
				return invariant.New(TypeSettlementInvalid, in.Settlement)
			}
			if cur.BalanceCents == 0 || l.Kind == lease.KindMobility {
				return invariant.New(TypeSettlementInvalid, "no balance to settle")
			}
			next.Settlement = in.Settlement
		}
		next.UpdatedAt, next.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateLine(ctx, next, version); err != nil {
			return err
		}
		next.Version = version + 1
		out = lineView(next)
		return audit.Record(ctx, "reconciliation.line", audit.Entity{Type: "reconciliation_line", ID: lineID.String()}, lineView(cur), out)
	})
	return out, err
}
