package lease

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// TenantLink is a tenant of a lease.
type TenantLink struct {
	TenantID uuid.UUID `json:"tenant_id"`
	// Solidary defaults to true (clause de solidarité).
	Solidary *bool `json:"solidary"`
}

// RentPart is the part of the rent attributed to one asset.
type RentPart struct {
	AssetID   uuid.UUID `json:"asset_id"`
	RentCents int64     `json:"rent_cents"`
}

// Input is the body of POST and PUT /leases.
type Input struct {
	Kind           Kind           `json:"kind" enums:"unfurnished,furnished,mobility,box"`
	AssetIDs       []uuid.UUID    `json:"asset_ids"`
	Tenants        []TenantLink   `json:"tenants"`
	StartOn        period.Date    `json:"start_on" swaggertype:"string" format:"date"`
	EndOn          *period.Date   `json:"end_on" swaggertype:"string" format:"date"`
	TermMonths     int            `json:"term_months"`
	RentCents      int64          `json:"rent_cents"`
	ProvisionCents int64          `json:"provision_cents"`
	DepositCents   int64          `json:"deposit_cents"`
	DueDay         int            `json:"due_day"`
	IRLRefQuarter  string         `json:"irl_ref_quarter" example:"2025-Q2"`
	IRLRefValue    int64          `json:"irl_ref_value" example:"14668"`
	RevisionMonth  int            `json:"revision_month"`
	RentExemption  *RentExemption `json:"rent_exemption" enums:"works,under_valued"`
	RentBreakdown  []RentPart     `json:"rent_breakdown"`
	// Clauses are special clauses picked by code from the vetted list:
	// [{"code": "boiler_servicing"}] (RENT-SPEC-008).
	Clauses json.RawMessage `json:"clauses" swaggertype:"array,object"`
	// MobilityReason is required to generate a mobility lease's contract.
	MobilityReason *MobilityReason `json:"mobility_reason" enums:"vocational_training,higher_education,apprenticeship,internship,civic_service,job_transfer,temporary_assignment"`
}

// View is a lease as the API returns it.
type View struct {
	ID uuid.UUID `json:"id"`
	Input
	State     State     `json:"state" enums:"draft,active,notice,ended,closed"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TransitionInput is the body of POST /leases/{id}/transitions.
type TransitionInput struct {
	Action Action `json:"action" enums:"activate,notice,end,close"`
	// EndOn is required for a notice: the last day of the lease.
	EndOn *period.Date `json:"end_on" swaggertype:"string" format:"date"`
}

// GuaranteeKind is the kind of a guarantee.
type GuaranteeKind string

// Guarantee kinds (RENT-SPEC-008).
const (
	GuaranteeDeposit   GuaranteeKind = "deposit"
	GuaranteeSurety    GuaranteeKind = "surety"
	GuaranteeVisale    GuaranteeKind = "visale"
	GuaranteeInsurance GuaranteeKind = "insurance"
)

// GuaranteeStatus is the status of a guarantee.
type GuaranteeStatus string

// Guarantee statuses (RENT-SPEC-008 owner view).
const (
	StatusHeld    GuaranteeStatus = "held"
	StatusValid   GuaranteeStatus = "valid"
	StatusExpired GuaranteeStatus = "expired"
	StatusSettled GuaranteeStatus = "settled"
)

// GuaranteeInput is the body of POST /leases/{id}/guarantees and PUT
// /guarantees/{id}. The guarantor object reads name, address, legal_person
// and solidary for a surety, visa_number for Visale, insurer and policy for
// a rent insurance; the combination rules apply on every write
// (RENT-SPEC-008).
type GuaranteeInput struct {
	Kind        GuaranteeKind   `json:"kind" enums:"deposit,surety,visale,insurance"`
	Guarantor   json.RawMessage `json:"guarantor" swaggertype:"object"`
	AmountCents int64           `json:"amount_cents"`
	ValidFrom   *period.Date    `json:"valid_from" swaggertype:"string" format:"date"`
	ValidTo     *period.Date    `json:"valid_to" swaggertype:"string" format:"date"`
	Status      GuaranteeStatus `json:"status" enums:"held,valid,expired,settled"`
}

// GuaranteeView is a guarantee as the API returns it; an accountant does not
// see the guarantor.
type GuaranteeView struct {
	ID      uuid.UUID `json:"id"`
	LeaseID uuid.UUID `json:"lease_id"`
	GuaranteeInput
	DocumentID *uuid.UUID `json:"document_id"`
	Version    int        `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Service implements the lease use cases.
type Service struct {
	DB        *db.DB
	Repo      Repository
	Portfolio portfolio.Service
	Params    params.Repository
	Issuer    document.Issuer
	Now       func() time.Time
	NewID     func() uuid.UUID
}

// NewService returns a service with the real clock and UUIDv7 ids.
func NewService(d *db.DB, p portfolio.Service) Service {
	return Service{DB: d, Portfolio: p, Now: time.Now, NewID: func() uuid.UUID { return uuid.Must(uuid.NewV7()) }}
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

func view(st stored) View {
	l := st.lease
	v := View{ID: l.ID, State: State(l.State), Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt, Input: Input{
		Kind: Kind(l.Kind), StartOn: period.FromTime(l.StartOn), TermMonths: l.TermMonths, RentCents: l.RentCents,
		ProvisionCents: l.ProvisionCents, DepositCents: l.DepositCents, DueDay: l.DueDay, IRLRefQuarter: l.IRLRefQuarter,
		IRLRefValue: l.IRLRefValue, RevisionMonth: l.RevisionMonth, Clauses: l.Clauses,
		AssetIDs: []uuid.UUID{}, Tenants: []TenantLink{}, RentBreakdown: []RentPart{}}}
	if l.EndOn != nil {
		d := period.FromTime(*l.EndOn)
		v.EndOn = &d
	}
	if l.RentExemption != nil {
		e := RentExemption(*l.RentExemption)
		v.RentExemption = &e
	}
	if l.MobilityReason != nil {
		m := MobilityReason(*l.MobilityReason)
		v.MobilityReason = &m
	}
	if len(l.RentBreakdown) > 0 {
		_ = json.Unmarshal(l.RentBreakdown, &v.RentBreakdown)
	}
	for _, a := range st.assets {
		v.AssetIDs = append(v.AssetIDs, a.AssetID)
	}
	for _, t := range st.tenants {
		sol := t.Solidary
		v.Tenants = append(v.Tenants, TenantLink{TenantID: t.TenantID, Solidary: &sol})
	}
	return v
}

func validateInput(in *Input) invariant.Fields {
	var f invariant.Fields
	switch in.Kind {
	case KindUnfurnished, KindFurnished, KindMobility, KindBox:
	default:
		f.Add("kind", "Type attendu : unfurnished, furnished, mobility ou box.")
	}
	if len(in.AssetIDs) == 0 {
		f.Add("asset_ids", "Au moins un bien est obligatoire.")
	}
	if len(in.Tenants) == 0 {
		f.Add("tenants", "Au moins un locataire est obligatoire.")
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range in.AssetIDs {
		if seen[id] {
			f.Add("asset_ids", "Un bien apparaît deux fois.")
		}
		seen[id] = true
	}
	seenT := map[uuid.UUID]bool{}
	for _, t := range in.Tenants {
		if seenT[t.TenantID] {
			f.Add("tenants", "Un locataire apparaît deux fois.")
		}
		seenT[t.TenantID] = true
	}
	if in.StartOn.IsZero() {
		f.Add("start_on", "La date de début est obligatoire.")
	}
	if in.EndOn != nil && !in.StartOn.IsZero() && in.EndOn.Before(in.StartOn) {
		f.Add("end_on", "La date de fin précède la date de début.")
	}
	if in.TermMonths <= 0 {
		f.Add("term_months", "La durée doit être positive.")
	}
	if in.RentCents < 0 {
		f.Add("rent_cents", "Le loyer ne peut pas être négatif.")
	}
	if in.ProvisionCents < 0 {
		f.Add("provision_cents", "La provision ne peut pas être négative.")
	}
	if in.DepositCents < 0 {
		f.Add("deposit_cents", "Le dépôt ne peut pas être négatif.")
	}
	if in.DueDay < 1 || in.DueDay > 28 {
		f.Add("due_day", "Le jour d’échéance doit être compris entre 1 et 28.")
	}
	if y, q, ok := strings.Cut(in.IRLRefQuarter, "-Q"); !ok || len(y) != 4 || len(q) != 1 || q < "1" || q > "4" {
		f.Add("irl_ref_quarter", "Trimestre attendu au format AAAA-Qn (2025-Q2).")
	}
	if in.IRLRefValue <= 0 {
		f.Add("irl_ref_value", "L’indice de référence doit être positif (en centièmes).")
	}
	if in.RevisionMonth < 1 || in.RevisionMonth > 12 {
		f.Add("revision_month", "Le mois de révision doit être compris entre 1 et 12.")
	}
	if len(in.Clauses) == 0 {
		in.Clauses = json.RawMessage("[]")
	} else if !json.Valid(in.Clauses) {
		f.Add("clauses", "Les clauses doivent être un JSON valide.")
	}
	if in.MobilityReason != nil {
		known := false
		for _, r := range MobilityReasons {
			known = known || r == *in.MobilityReason
		}
		if !known {
			f.Add("mobility_reason", "Motif de bail mobilité inconnu.")
		}
		if in.Kind != KindMobility {
			f.Add("mobility_reason", "Un motif ne s’indique que pour un bail mobilité.")
		}
	}
	return f
}

// domain builds the calculation-engine lease, loading the assets in scope;
// unknown assets or tenants are refused.
func (s Service) domain(ctx context.Context, id uuid.UUID, in Input, state State) (Lease, []portfolio.AssetView, error) {
	assets, err := s.Portfolio.AssetsInTx(ctx, in.AssetIDs)
	if err != nil {
		return Lease{}, nil, err
	}
	var f invariant.Fields
	if len(assets) != len(in.AssetIDs) {
		f.Add("asset_ids", "Un bien indiqué n’existe pas dans ce portefeuille.")
	}
	tids := make([]uuid.UUID, len(in.Tenants))
	for i, t := range in.Tenants {
		tids[i] = t.TenantID
	}
	exist, err := s.Portfolio.TenantsExist(ctx, tids)
	if err != nil {
		return Lease{}, nil, err
	}
	for _, t := range tids {
		if !exist[t] {
			f.Add("tenants", "Un locataire indiqué n’existe pas dans ce portefeuille.")
			break
		}
	}
	if err := f.Err(); err != nil {
		return Lease{}, nil, err
	}
	boxes := 0
	for _, a := range assets {
		if a.Kind == portfolio.KindBox {
			boxes++
		}
	}
	if (in.Kind == KindBox) != (boxes == len(assets)) {
		return Lease{}, nil, invariant.New(TypeAssetsInvalid, fmt.Sprintf("kind %s with %d boxes out of %d assets", in.Kind, boxes, len(assets)))
	}
	l := Lease{ID: id.String(), Kind: in.Kind, State: state, StartOn: in.StartOn, TermMonths: in.TermMonths,
		RentCents: money.Cents(in.RentCents), ProvisionCents: money.Cents(in.ProvisionCents), DepositCents: money.Cents(in.DepositCents),
		DueDay: in.DueDay, IRLRefQuarter: in.IRLRefQuarter, IRLRefValue: in.IRLRefValue, RevisionMonth: time.Month(in.RevisionMonth)}
	if in.EndOn != nil {
		l.EndOn = *in.EndOn
	}
	if in.RentExemption != nil {
		l.RentExemption = *in.RentExemption
	}
	for _, p := range in.RentBreakdown {
		l.RentBreakdown = append(l.RentBreakdown, AssetRent{AssetID: p.AssetID.String(), RentCents: money.Cents(p.RentCents)})
	}
	for _, a := range assets {
		l.Assets = append(l.Assets, a.Domain())
	}
	return l, assets, nil
}

// check runs lease.Validate for a creation (a draft save or an activation),
// with the previous lease of a controlled-zone asset for the relocation rule.
// The first broken rule is returned.
func (s Service) check(ctx context.Context, l Lease, id uuid.UUID, assetIDs []uuid.UUID) error {
	errs, err := s.rules(ctx, l, id, assetIDs)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// rules returns every lease rule a creation breaks.
func (s Service) rules(ctx context.Context, l Lease, id uuid.UUID, assetIDs []uuid.UUID) ([]*invariant.Error, error) {
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return nil, err
	}
	c := Check{Event: EventCreation, AsOf: l.StartOn}
	if l.TenseZone() {
		prev, ok, err := s.Repo.PreviousLease(ctx, assetIDs, id, l.StartOn.Time())
		if err != nil {
			return nil, err
		}
		if ok {
			q, _ := strconv.Atoi(prev.IRLRefQuarter[len(prev.IRLRefQuarter)-1:])
			latest, found, err := s.Params.LatestIRLOfQuarter(ctx, q)
			if err != nil {
				return nil, err
			}
			if !found {
				return []*invariant.Error{invariant.New(TypeRelocationIndex, prev.IRLRefQuarter)}, nil
			}
			c.Previous = &Previous{RentCents: money.Cents(prev.RentCents), IRLRefValue: prev.IRLRefValue, IRLLatestValue: latest.Value}
		}
	}
	return Validate(l, c, ps)
}

func rowFrom(id, owner uuid.UUID, in Input) (stored, error) {
	l := leaseRow{ID: id, OwnerID: owner, Kind: string(in.Kind), StartOn: in.StartOn.Time(), TermMonths: in.TermMonths,
		RentCents: in.RentCents, ProvisionCents: in.ProvisionCents, DepositCents: in.DepositCents, DueDay: in.DueDay,
		IRLRefQuarter: in.IRLRefQuarter, IRLRefValue: in.IRLRefValue, RevisionMonth: in.RevisionMonth, Clauses: in.Clauses}
	if in.EndOn != nil {
		t := in.EndOn.Time()
		l.EndOn = &t
	}
	if in.RentExemption != nil {
		e := string(*in.RentExemption)
		l.RentExemption = &e
	}
	if in.MobilityReason != nil {
		m := string(*in.MobilityReason)
		l.MobilityReason = &m
	}
	if len(in.RentBreakdown) > 0 {
		b, err := json.Marshal(in.RentBreakdown)
		if err != nil {
			return stored{}, err
		}
		l.RentBreakdown = b
	}
	st := stored{lease: l}
	for _, a := range in.AssetIDs {
		st.assets = append(st.assets, leaseAssetRow{LeaseID: id, AssetID: a, OwnerID: owner})
	}
	for _, t := range in.Tenants {
		sol := true
		if t.Solidary != nil {
			sol = *t.Solidary
		}
		st.tenants = append(st.tenants, leaseTenantRow{LeaseID: id, TenantID: t.TenantID, OwnerID: owner, Solidary: sol})
	}
	return st, nil
}

// List returns a page of the scope's leases.
func (s Service) List(ctx context.Context, p *auth.Principal, requestID string, f Filter, offset, limit int) ([]View, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
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

// Get returns one lease of the scope.
func (s Service) Get(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.Repo.Get(ctx, id, false)
		out = view(st)
		return err
	})
	return out, err
}

// Create drafts a lease. Every lease rule is checked at save (Q-32): one
// ownership across its assets, the zone ceiling and the relocation rule.
func (s Service) Create(ctx context.Context, p *auth.Principal, requestID string, in Input) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if f := validateInput(&in); len(f) > 0 {
		return View{}, f
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		id := s.NewID()
		l, _, err := s.domain(ctx, id, in, StateDraft)
		if err != nil {
			return err
		}
		if err := s.check(ctx, l, id, in.AssetIDs); err != nil {
			return err
		}
		st, err := rowFrom(id, sc.OwnerID, in)
		if err != nil {
			return err
		}
		now := s.now()
		st.lease.State, st.lease.Version = string(StateDraft), 1
		st.lease.CreatedAt, st.lease.CreatedBy, st.lease.UpdatedAt, st.lease.UpdatedBy = now, p.Sub, now, p.Sub
		if err := s.Repo.Insert(ctx, st); err != nil {
			return err
		}
		out = view(st)
		return audit.Record(ctx, "lease.create", audit.Entity{Type: "lease", ID: id.String()}, nil, out)
	})
	return out, err
}

// Update replaces a draft lease's terms when version matches; a lease past
// draft changes only through transitions and, from slice 5, amendments.
func (s Service) Update(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in Input) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if f := validateInput(&in); len(f) > 0 {
		return View{}, f
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Get(ctx, id, true)
		if err != nil {
			return err
		}
		if cur.lease.Version != version {
			return invariant.ErrStale
		}
		if State(cur.lease.State) != StateDraft {
			return invariant.New(TypeNotEditable, cur.lease.State)
		}
		l, _, err := s.domain(ctx, id, in, StateDraft)
		if err != nil {
			return err
		}
		if err := s.check(ctx, l, id, in.AssetIDs); err != nil {
			return err
		}
		st, err := rowFrom(id, sc.OwnerID, in)
		if err != nil {
			return err
		}
		st.lease.State, st.lease.Version = cur.lease.State, version+1
		st.lease.CreatedAt, st.lease.CreatedBy, st.lease.UpdatedAt, st.lease.UpdatedBy = cur.lease.CreatedAt, cur.lease.CreatedBy, s.now(), p.Sub
		if err := s.Repo.Update(ctx, st, version); err != nil {
			return err
		}
		out = view(st)
		return audit.Record(ctx, "lease.update", audit.Entity{Type: "lease", ID: id.String()}, view(cur), out)
	})
	return out, err
}

// Transition moves a lease along its lifecycle (RENT-SPEC-002) after the
// blockers of the action:
//   - activate: one current lease per asset (assets locked), every blocker of
//     contract generation as of today (lease rules, guarantees, diagnostics),
//     a contract of the current lease version and guarantees, its signed copy
//     and the entry inventory (RENT-SPEC-008, Q-22);
//   - notice: an end date on or after the start;
//   - end: the end date of the notice;
//   - close: every deposit settled.
func (s Service) Transition(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in TransitionInput) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Get(ctx, id, true)
		if err != nil {
			return err
		}
		before := view(cur)
		to, err := Next(State(cur.lease.State), in.Action)
		if err != nil {
			return err
		}
		next := cur
		switch in.Action {
		case ActionActivate:
			ids := before.AssetIDs
			if err := s.Repo.LockAssets(ctx, ids); err != nil {
				return err
			}
			others, err := s.Repo.CurrentLeasesOn(ctx, ids, id)
			if err != nil {
				return err
			}
			if len(others) > 0 {
				return invariant.New(TypeOneActivePerAsset, others[0].String())
			}
			f, err := s.facts(ctx, sc, id, false)
			if err != nil {
				return err
			}
			gen, err := s.generationBlockers(ctx, f)
			if err != nil {
				return err
			}
			act, err := s.activationBlockers(ctx, f)
			if err != nil {
				return err
			}
			if all := append(gen, act...); len(all) > 0 {
				return all[0]
			}
		case ActionNotice:
			if in.EndOn == nil || in.EndOn.Before(period.FromTime(cur.lease.StartOn)) {
				return invariant.New(TypeNoticeEndRequired, "end_on")
			}
			t := in.EndOn.Time()
			next.lease.EndOn = &t
		case ActionEnd:
			if cur.lease.EndOn == nil {
				return invariant.New(TypeNoticeEndRequired, "end_on")
			}
		case ActionClose:
			gs, err := s.Repo.Guarantees(ctx, id)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(gs, func(g guaranteeRow) bool {
				return g.Kind == string(GuaranteeDeposit) && g.Status != string(StatusSettled)
			}) {
				return invariant.New(TypeDepositNotSettled, id.String())
			}
		}
		next.lease.State, next.lease.Version = string(to), cur.lease.Version+1
		next.lease.UpdatedAt, next.lease.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.SetState(ctx, next.lease); err != nil {
			return err
		}
		out = view(next)
		return audit.Record(ctx, "lease."+string(in.Action), audit.Entity{Type: "lease", ID: id.String()}, before, out)
	})
	return out, err
}

// --- Guarantees -----------------------------------------------------------------

func guaranteeView(g guaranteeRow, role auth.Role) GuaranteeView {
	v := GuaranteeView{ID: g.ID, LeaseID: g.LeaseID, DocumentID: g.DocumentID, Version: g.Version, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
		GuaranteeInput: GuaranteeInput{Kind: GuaranteeKind(g.Kind), Guarantor: g.Guarantor, AmountCents: g.AmountCents, Status: GuaranteeStatus(g.Status)}}
	if g.ValidFrom != nil {
		d := period.FromTime(*g.ValidFrom)
		v.ValidFrom = &d
	}
	if g.ValidTo != nil {
		d := period.FromTime(*g.ValidTo)
		v.ValidTo = &d
	}
	if role == auth.RoleAccountant {
		v.Guarantor = json.RawMessage("{}")
	}
	return v
}

func validateGuarantee(in *GuaranteeInput) invariant.Fields {
	var f invariant.Fields
	switch in.Kind {
	case GuaranteeDeposit, GuaranteeSurety, GuaranteeVisale, GuaranteeInsurance:
	default:
		f.Add("kind", "Type attendu : deposit, surety, visale ou insurance.")
	}
	if in.Status == "" {
		in.Status = StatusHeld
	}
	switch in.Status {
	case StatusHeld, StatusValid, StatusExpired, StatusSettled:
	default:
		f.Add("status", "Statut attendu : held, valid, expired ou settled.")
	}
	if in.AmountCents < 0 {
		f.Add("amount_cents", "Le montant ne peut pas être négatif.")
	}
	if len(in.Guarantor) == 0 {
		in.Guarantor = json.RawMessage("{}")
	} else {
		var obj map[string]any
		if json.Unmarshal(in.Guarantor, &obj) != nil {
			f.Add("guarantor", "Le garant doit être un objet JSON.")
		}
	}
	if in.ValidFrom != nil && in.ValidTo != nil && in.ValidTo.Before(*in.ValidFrom) {
		f.Add("valid_to", "La fin de validité précède son début.")
	}
	if in.Kind == GuaranteeSurety {
		if g := guarantorOf(in.Guarantor); g.Name == "" || g.Address == "" {
			f.Add("guarantor", "Une caution doit avoir un nom et une adresse.")
		}
	}
	return f
}

// checkGuarantee applies the combination rules to the lease's guarantees
// with g added, or replacing the guarantee replaced (RENT-SPEC-008).
func (s Service) checkGuarantee(ctx context.Context, sc db.Scope, leaseID uuid.UUID, replaced *uuid.UUID, g guaranteeRow, checkDeposit bool) error {
	f, err := s.facts(ctx, sc, leaseID, false)
	if err != nil {
		return err
	}
	var rows []guaranteeRow
	for _, x := range f.guarantees {
		if replaced == nil || x.ID != *replaced {
			rows = append(rows, x)
		}
	}
	rows = append(rows, g)
	errs, err := CheckGuarantees(f.lease, guaranteeFacts(rows), studentsOnly(f.tenants), checkDeposit, f.ps)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func guaranteeRowFrom(in GuaranteeInput) guaranteeRow {
	g := guaranteeRow{Kind: string(in.Kind), Guarantor: in.Guarantor, AmountCents: in.AmountCents, Status: string(in.Status)}
	if in.ValidFrom != nil {
		t := in.ValidFrom.Time()
		g.ValidFrom = &t
	}
	if in.ValidTo != nil {
		t := in.ValidTo.Time()
		g.ValidTo = &t
	}
	return g
}

// Guarantees returns the guarantees of a lease.
func (s Service) Guarantees(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID) ([]GuaranteeView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []GuaranteeView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Get(ctx, leaseID, false); err != nil {
			return err
		}
		rows, err := s.Repo.Guarantees(ctx, leaseID)
		for _, g := range rows {
			out = append(out, guaranteeView(g, p.Active.Role))
		}
		return err
	})
	return out, err
}

// CreateGuarantee adds a guarantee to a lease that is not closed.
func (s Service) CreateGuarantee(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in GuaranteeInput) (GuaranteeView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GuaranteeView{}, err
	}
	if f := validateGuarantee(&in); len(f) > 0 {
		return GuaranteeView{}, f
	}
	var out GuaranteeView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		l, err := s.Repo.Get(ctx, leaseID, true)
		if err != nil {
			return err
		}
		if State(l.lease.State) == StateClosed {
			return invariant.New(TypeGuaranteeClosed, leaseID.String())
		}
		g := guaranteeRowFrom(in)
		if err := s.checkGuarantee(ctx, sc, leaseID, nil, g, in.Kind == GuaranteeDeposit); err != nil {
			return err
		}
		now := s.now()
		g.ID, g.OwnerID, g.LeaseID, g.Version = s.NewID(), sc.OwnerID, leaseID, 1
		g.CreatedAt, g.CreatedBy, g.UpdatedAt, g.UpdatedBy = now, p.Sub, now, p.Sub
		if err := s.Repo.InsertGuarantee(ctx, g); err != nil {
			return err
		}
		out = guaranteeView(g, auth.RoleOwner)
		return audit.Record(ctx, "guarantee.create", audit.Entity{Type: "guarantee", ID: g.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateGuarantee updates a guarantee when version matches.
func (s Service) UpdateGuarantee(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in GuaranteeInput) (GuaranteeView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GuaranteeView{}, err
	}
	if f := validateGuarantee(&in); len(f) > 0 {
		return GuaranteeView{}, f
	}
	var out GuaranteeView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Guarantee(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return invariant.ErrStale
		}
		l, err := s.Repo.Get(ctx, cur.LeaseID, true)
		if err != nil {
			return err
		}
		if State(l.lease.State) == StateClosed {
			return invariant.New(TypeGuaranteeClosed, cur.LeaseID.String())
		}
		before := guaranteeView(cur, auth.RoleOwner)
		g := guaranteeRowFrom(in)
		// A deposit already held is checked again only when it grows: a rent
		// change never changes it (RENT-SPEC-008).
		grows := in.Kind == GuaranteeDeposit && (cur.Kind != string(GuaranteeDeposit) || in.AmountCents > cur.AmountCents)
		if err := s.checkGuarantee(ctx, sc, cur.LeaseID, &id, g, grows); err != nil {
			return err
		}
		g.ID, g.OwnerID, g.LeaseID, g.DocumentID, g.Version = id, cur.OwnerID, cur.LeaseID, cur.DocumentID, version+1
		g.CreatedAt, g.CreatedBy, g.UpdatedAt, g.UpdatedBy = cur.CreatedAt, cur.CreatedBy, s.now(), p.Sub
		if err := s.Repo.UpdateGuarantee(ctx, g, version); err != nil {
			return err
		}
		out = guaranteeView(g, auth.RoleOwner)
		return audit.Record(ctx, "guarantee.update", audit.Entity{Type: "guarantee", ID: id.String()}, before, out)
	})
	return out, err
}

// DomainInTx returns, inside the current transaction, the calculation-engine
// lease of id and its API view; the lease row is locked for the transaction.
func (s Service) DomainInTx(ctx context.Context, id uuid.UUID) (Lease, View, error) {
	st, err := s.Repo.Get(ctx, id, true)
	if err != nil {
		return Lease{}, View{}, err
	}
	v := view(st)
	l, _, err := s.domain(ctx, id, v.Input, State(st.lease.State))
	return l, v, err
}

// SetProvisionInTx records a new monthly provision validated by the owner
// after a charge reconciliation (RENT-SPEC-007 step 8), inside the current
// transaction, with its audit event lease.provision; it applies from the next
// rent call issued.
func (s Service) SetProvisionInTx(ctx context.Context, id uuid.UUID, cents int64, by string) error {
	st, err := s.Repo.Get(ctx, id, true)
	if err != nil {
		return err
	}
	before := map[string]any{"provision_cents": st.lease.ProvisionCents, "version": st.lease.Version}
	if err := s.Repo.SetProvision(ctx, id, st.lease.Version, cents, s.now(), by); err != nil {
		return err
	}
	return audit.Record(ctx, "lease.provision", audit.Entity{Type: "lease", ID: id.String()}, before,
		map[string]any{"provision_cents": cents, "version": st.lease.Version + 1})
}

// DepositHeldInTx reports whether the lease still holds a deposit (held or
// valid), against which a regularisation balance can be settled.
func (s Service) DepositHeldInTx(ctx context.Context, id uuid.UUID) (bool, error) {
	gs, err := s.Repo.Guarantees(ctx, id)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(gs, func(g guaranteeRow) bool {
		return g.Kind == string(GuaranteeDeposit) && (g.Status == string(StatusHeld) || g.Status == string(StatusValid))
	}), nil
}

// RevisionApplied is the audit view of an applied revision.
type RevisionApplied struct {
	RentCents     int64  `json:"rent_cents"`
	IRLRefQuarter string `json:"irl_ref_quarter"`
	IRLRefValue   int64  `json:"irl_ref_value"`
	Version       int    `json:"version"`
}

// ApplyRevisionInTx records a computed revision on the lease, inside the
// current transaction, with its audit event lease.revise.
func (s Service) ApplyRevisionInTx(ctx context.Context, v View, rentCents int64, quarter string, value int64, by string) error {
	before := RevisionApplied{RentCents: v.RentCents, IRLRefQuarter: v.IRLRefQuarter, IRLRefValue: v.IRLRefValue, Version: v.Version}
	after := RevisionApplied{RentCents: rentCents, IRLRefQuarter: quarter, IRLRefValue: value, Version: v.Version + 1}
	if err := s.Repo.ApplyRevision(ctx, v.ID, v.Version, rentCents, quarter, value, s.now(), by); err != nil {
		return err
	}
	return audit.Record(ctx, "lease.revise", audit.Entity{Type: "lease", ID: v.ID.String()}, before, after)
}
