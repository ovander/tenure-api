package bic

import (
	"context"
	"encoding/json"
	"errors"
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

// Service serves the component grids, the fixed assets and their
// depreciation (RENT-SPEC-019, Q-132).
type Service struct {
	DB        *db.DB
	Repo      Repository
	Portfolio portfolio.Service
	Docs      document.Service
	Params    params.Repository
	Now       func() time.Time
	NewID     func() uuid.UUID
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

func fields(field, msg string) error {
	var f invariant.Fields
	f.Add(field, msg)
	return f
}

// GridInput is the body of PUT /assets/{id}/bic-grid: a new version.
type GridInput struct {
	// InServiceOn is the day the asset was first let furnished.
	InServiceOn           period.Date `json:"in_service_on" swaggertype:"string" format:"date"`
	PriceCents            money.Cents `json:"price_cents"`
	AcquisitionCostsCents money.Cents `json:"acquisition_costs_cents"`
	// CostsExpensed expenses the acquisition costs in the first year rather
	// than spreading them over the components (an option, traced).
	CostsExpensed bool   `json:"costs_expensed"`
	Lines         []Line `json:"lines"`
	// Reason says why a version changes the one before.
	Reason string `json:"reason,omitempty"`
}

// GridView is an asset's grid, its latest version, or the default proposal
// (version 0) while it has none.
type GridView struct {
	AssetID               uuid.UUID    `json:"asset_id"`
	Version               int          `json:"version"`
	Proposed              bool         `json:"proposed"`
	InServiceOn           *period.Date `json:"in_service_on" swaggertype:"string" format:"date"`
	PriceCents            money.Cents  `json:"price_cents"`
	AcquisitionCostsCents money.Cents  `json:"acquisition_costs_cents"`
	CostsExpensed         bool         `json:"costs_expensed"`
	BasisCents            money.Cents  `json:"basis_cents"`
	Lines                 []Line       `json:"lines"`
	// Bases is each line's share of the basis, in the lines' order.
	Bases     []money.Cents `json:"bases"`
	Reason    string        `json:"reason,omitempty"`
	Frozen    bool          `json:"frozen"`
	CreatedAt *time.Time    `json:"created_at,omitempty"`
}

func basis(in GridInput) money.Cents {
	if in.CostsExpensed {
		return in.PriceCents
	}
	return in.PriceCents + in.AcquisitionCostsCents
}

func gridView(r gridRow) (GridView, error) {
	v := GridView{AssetID: r.AssetID, Version: r.Version, PriceCents: money.Cents(r.PriceCents), AcquisitionCostsCents: money.Cents(r.AcquisitionCostsCents),
		CostsExpensed: r.CostsExpensed, BasisCents: money.Cents(r.BasisCents), Reason: r.Reason, Frozen: r.FrozenAt != nil}
	day := period.FromTime(r.InServiceOn)
	v.InServiceOn = &day
	created := r.CreatedAt
	v.CreatedAt = &created
	if err := json.Unmarshal(r.Lines, &v.Lines); err != nil {
		return v, err
	}
	v.Bases = Grid{BasisCents: v.BasisCents, Lines: v.Lines}.Bases()
	return v, nil
}

// proposal is the default grid of an asset (Q-132): the lines of
// bic.components, its acquisition price, the start of its first furnished
// lease, the acquisition costs treated as bic.acquisition_costs says.
func (s Service) proposal(ctx context.Context, asset uuid.UUID) (GridView, error) {
	a, err := s.Portfolio.AssetInTx(ctx, asset)
	if err != nil {
		return GridView{}, err
	}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return GridView{}, err
	}
	asOf := s.today()
	v := GridView{AssetID: asset, Proposed: true, Lines: []Line{}}
	if day, ok, err := s.Repo.FirstFurnishedLet(ctx, asset); err != nil {
		return GridView{}, err
	} else if ok {
		d := period.FromTime(day)
		v.InServiceOn, asOf = &d, d
	}
	comps, err := ps.BICComponents(asOf)
	if err != nil {
		return GridView{}, err
	}
	for _, l := range comps.Lines {
		v.Lines = append(v.Lines, Line{Component: l.Component, ShareBP: l.ShareBP, Months: l.Months})
	}
	costs, err := ps.BICAcquisitionCosts(asOf)
	if err != nil {
		return GridView{}, err
	}
	v.CostsExpensed = !costs.Capitalise
	if a.AcquisitionCents != nil {
		v.PriceCents = money.Cents(*a.AcquisitionCents)
	}
	v.BasisCents = v.PriceCents
	v.Bases = Grid{BasisCents: v.BasisCents, Lines: v.Lines}.Bases()
	return v, nil
}

func (s Service) gridInTx(ctx context.Context, asset uuid.UUID) (GridView, error) {
	r, found, err := s.Repo.Grid(ctx, asset)
	if err != nil {
		return GridView{}, err
	}
	if !found {
		return s.proposal(ctx, asset)
	}
	return gridView(r)
}

// Grid returns an asset's grid, or the default proposal while it has none.
func (s Service) Grid(ctx context.Context, p *auth.Principal, requestID string, asset uuid.UUID) (GridView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GridView{}, err
	}
	var out GridView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		out, err = s.gridInTx(ctx, asset)
		return err
	})
	return out, err
}

// SaveGrid records the next version of an asset's grid when version is the
// one read (0 for the first). A version is never changed; one a declaration
// used stays as it was (Q-132).
func (s Service) SaveGrid(ctx context.Context, p *auth.Principal, requestID string, asset uuid.UUID, version int, in GridInput) (GridView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GridView{}, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	var f invariant.Fields
	if in.InServiceOn.IsZero() {
		f.Add("in_service_on", "Le jour de la première mise en location meublée est obligatoire.")
	}
	if in.PriceCents < 0 {
		f.Add("price_cents", "Le prix ne peut pas être négatif.")
	}
	if in.AcquisitionCostsCents < 0 {
		f.Add("acquisition_costs_cents", "Les frais ne peuvent pas être négatifs.")
	}
	if err := (Grid{BasisCents: basis(in), Lines: in.Lines}).Validate(); err != nil {
		f.Add("lines", "Chaque composant a une quote-part et une durée positives, une seule fois, et les quotes-parts font 100 %.")
	}
	if version > 0 && in.Reason == "" {
		f.Add("reason", "Dites pourquoi la grille change.")
	}
	if len(in.Reason) > 1024 {
		f.Add("reason", "1 024 caractères au plus.")
	}
	if err := f.Err(); err != nil {
		return GridView{}, err
	}
	var out GridView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Portfolio.AssetInTx(ctx, asset); err != nil {
			return err
		}
		old, err := s.gridInTx(ctx, asset)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		lines, err := json.Marshal(in.Lines)
		if err != nil {
			return err
		}
		r := gridRow{ID: s.NewID(), OwnerID: sc.OwnerID, AssetID: asset, Version: version + 1, InServiceOn: in.InServiceOn.Time(),
			PriceCents: int64(in.PriceCents), AcquisitionCostsCents: int64(in.AcquisitionCostsCents), CostsExpensed: in.CostsExpensed,
			BasisCents: int64(basis(in)), Lines: lines, Reason: in.Reason, CreatedAt: s.now(), CreatedBy: p.Sub}
		if err := s.Repo.InsertGrid(ctx, r); err != nil {
			return err
		}
		out, err = gridView(r)
		if err != nil {
			return err
		}
		var before any
		if !old.Proposed {
			before = old
		}
		return audit.Record(ctx, "bic.grid", audit.Entity{Type: "asset", ID: asset.String()}, before, out)
	})
	return out, err
}

// ItemInput is the body of POST /assets/{id}/fixed-assets and PUT
// /fixed-assets/{id}: furniture or works.
type ItemInput struct {
	Kind string `json:"kind" enums:"furniture,works"`
	// Works is the nature of works: repair, improvement or construction.
	Works       string      `json:"works,omitempty" enums:"repair,improvement,construction"`
	Label       string      `json:"label"`
	CostCents   money.Cents `json:"cost_cents"`
	InServiceOn period.Date `json:"in_service_on" swaggertype:"string" format:"date"`
	// Months defaults to bic.furniture for furniture; required for works.
	Months *int `json:"months,omitempty"`
	// Treatment forces asset or expense against the rule (Q-132); the
	// reason is then required and traced.
	Treatment      string       `json:"treatment,omitempty" enums:"asset,expense"`
	OverrideReason string       `json:"override_reason,omitempty"`
	DocumentID     *uuid.UUID   `json:"document_id,omitempty"`
	DisposedOn     *period.Date `json:"disposed_on,omitempty" swaggertype:"string" format:"date"`
}

// ItemView is an item as the API returns it.
type ItemView struct {
	ID          uuid.UUID   `json:"id"`
	AssetID     uuid.UUID   `json:"asset_id"`
	Kind        string      `json:"kind" enums:"furniture,works"`
	Works       *string     `json:"works" enums:"repair,improvement,construction"`
	Label       string      `json:"label"`
	CostCents   money.Cents `json:"cost_cents"`
	InServiceOn period.Date `json:"in_service_on" swaggertype:"string" format:"date"`
	Months      int         `json:"months"`
	Treatment   string      `json:"treatment" enums:"asset,expense"`
	// Suggested is what the rule gives without an override.
	Suggested      string       `json:"suggested" enums:"asset,expense"`
	Overridden     bool         `json:"overridden"`
	OverrideReason string       `json:"override_reason,omitempty"`
	DocumentID     *uuid.UUID   `json:"document_id"`
	DisposedOn     *period.Date `json:"disposed_on" swaggertype:"string" format:"date"`
	Version        int          `json:"version"`
}

func itemView(r itemRow, suggested string) ItemView {
	v := ItemView{ID: r.ID, AssetID: r.AssetID, Kind: r.Kind, Works: r.Works, Label: r.Label, CostCents: money.Cents(r.CostCents),
		InServiceOn: period.FromTime(r.InServiceOn), Months: r.Months, Treatment: r.Treatment, Suggested: suggested, Overridden: r.Overridden,
		OverrideReason: r.OverrideReason, DocumentID: r.DocumentID, Version: r.Version}
	if r.DisposedOn != nil {
		d := period.FromTime(*r.DisposedOn)
		v.DisposedOn = &d
	}
	return v
}

func works(r itemRow) string {
	if r.Works == nil {
		return ""
	}
	return *r.Works
}

// resolve checks an item and gives its months, its suggested treatment and
// the one retained (Q-132).
func (s Service) resolve(ctx context.Context, in *ItemInput) (int, string, string, error) {
	in.Label, in.OverrideReason = strings.TrimSpace(in.Label), strings.TrimSpace(in.OverrideReason)
	var f invariant.Fields
	switch in.Kind {
	case KindFurniture:
		if in.Works != "" {
			f.Add("works", "La nature des travaux ne vaut que pour des travaux.")
		}
	case KindWorks:
		if in.Works != WorksRepair && in.Works != WorksImprovement && in.Works != WorksConstruction {
			f.Add("works", "Attendu : repair, improvement ou construction.")
		}
		if in.Months == nil {
			f.Add("months", "La durée d’amortissement des travaux est obligatoire.")
		}
	default:
		f.Add("kind", "Attendu : furniture ou works.")
	}
	if in.Label == "" || len(in.Label) > 200 {
		f.Add("label", "Un intitulé de 1 à 200 caractères est obligatoire.")
	}
	if in.CostCents < 0 {
		f.Add("cost_cents", "Le coût ne peut pas être négatif.")
	}
	if in.InServiceOn.IsZero() {
		f.Add("in_service_on", "Le jour de mise en service est obligatoire.")
	}
	if in.Months != nil && (*in.Months < 1 || *in.Months > 1200) {
		f.Add("months", "Une durée de 1 à 1 200 mois est attendue.")
	}
	if in.Treatment != "" && in.Treatment != TreatmentAsset && in.Treatment != TreatmentExpense {
		f.Add("treatment", "Attendu : asset ou expense.")
	}
	if in.DisposedOn != nil && in.DisposedOn.Before(in.InServiceOn) {
		f.Add("disposed_on", "La sortie ne peut précéder la mise en service.")
	}
	if err := f.Err(); err != nil {
		return 0, "", "", err
	}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return 0, "", "", err
	}
	threshold, err := ps.BICSmallEquipment(in.InServiceOn)
	if err != nil {
		return 0, "", "", err
	}
	months := 0
	if in.Months != nil {
		months = *in.Months
	} else {
		fu, err := ps.BICFurniture(in.InServiceOn)
		if err != nil {
			return 0, "", "", err
		}
		months = fu.Months
	}
	suggested := Capitalise(in.Kind, in.Works, in.CostCents, threshold.ThresholdCents, "")
	treatment := suggested
	if in.Treatment != "" && in.Treatment != suggested {
		if in.OverrideReason == "" {
			return 0, "", "", fields("override_reason", "Dites pourquoi vous forcez ce traitement.")
		}
		treatment = in.Treatment
	}
	if in.DocumentID != nil {
		if _, err := s.Docs.Repo.Get(ctx, *in.DocumentID); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				return 0, "", "", fields("document_id", "Ce document n’existe pas dans ce portefeuille.")
			}
			return 0, "", "", err
		}
	}
	return months, suggested, treatment, nil
}

func apply(r *itemRow, in ItemInput, months int, suggested, treatment string) {
	r.Kind, r.Label, r.CostCents, r.InServiceOn, r.Months = in.Kind, in.Label, int64(in.CostCents), in.InServiceOn.Time(), months
	r.Works = nil
	if in.Works != "" {
		w := in.Works
		r.Works = &w
	}
	r.Treatment, r.Overridden, r.OverrideReason = treatment, treatment != suggested, ""
	if r.Overridden {
		r.OverrideReason = in.OverrideReason
	}
	r.DocumentID, r.DisposedOn = in.DocumentID, nil
	if in.DisposedOn != nil {
		t := in.DisposedOn.Time()
		r.DisposedOn = &t
	}
}

func (s Service) suggestedFor(ctx context.Context, r itemRow) (string, error) {
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return "", err
	}
	th, err := ps.BICSmallEquipment(period.FromTime(r.InServiceOn))
	if err != nil {
		return "", err
	}
	return Capitalise(r.Kind, works(r), money.Cents(r.CostCents), th.ThresholdCents, ""), nil
}

// Items lists an asset's furniture and works.
func (s Service) Items(ctx context.Context, p *auth.Principal, requestID string, asset uuid.UUID) ([]ItemView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []ItemView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Portfolio.AssetInTx(ctx, asset); err != nil {
			return err
		}
		rows, err := s.Repo.Items(ctx, asset)
		if err != nil {
			return err
		}
		for _, r := range rows {
			sug, err := s.suggestedFor(ctx, r)
			if err != nil {
				return err
			}
			out = append(out, itemView(r, sug))
		}
		return nil
	})
	return out, err
}

// CreateItem records furniture or works on an asset (Q-132).
func (s Service) CreateItem(ctx context.Context, p *auth.Principal, requestID string, asset uuid.UUID, in ItemInput) (ItemView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ItemView{}, err
	}
	var out ItemView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Portfolio.AssetInTx(ctx, asset); err != nil {
			return err
		}
		months, sug, treatment, err := s.resolve(ctx, &in)
		if err != nil {
			return err
		}
		now := s.now()
		r := itemRow{ID: s.NewID(), OwnerID: sc.OwnerID, AssetID: asset, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		apply(&r, in, months, sug, treatment)
		if err := s.Repo.InsertItem(ctx, r); err != nil {
			return err
		}
		out = itemView(r, sug)
		return audit.Record(ctx, "bic.fixed_asset", audit.Entity{Type: "fixed_asset", ID: r.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateItem replaces an item (not its asset) when version is the one read.
func (s Service) UpdateItem(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in ItemInput) (ItemView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ItemView{}, err
	}
	var out ItemView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.Repo.Item(ctx, id)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		months, sug, treatment, err := s.resolve(ctx, &in)
		if err != nil {
			return err
		}
		oldSug, err := s.suggestedFor(ctx, old)
		if err != nil {
			return err
		}
		r := old
		apply(&r, in, months, sug, treatment)
		r.UpdatedAt, r.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateItem(ctx, r, version); err != nil {
			return err
		}
		r.Version = version + 1
		out = itemView(r, sug)
		return audit.Record(ctx, "bic.fixed_asset_update", audit.Entity{Type: "fixed_asset", ID: id.String()}, itemView(old, oldSug), out)
	})
	return out, err
}

// DepreciationView is an asset's depreciation for a year.
type DepreciationView struct {
	AssetID uuid.UUID `json:"asset_id"`
	Year    int       `json:"year"`
	// GridVersion is the grid used, 0 when the asset has none yet (only its
	// items are depreciated).
	GridVersion int         `json:"grid_version"`
	Rows        []Row       `json:"rows"`
	TotalCents  money.Cents `json:"total_cents"`
	// ExpensedCents is the cost of the items expensed, put in service in the
	// year.
	ExpensedCents money.Cents `json:"expensed_cents"`
}

// Depreciation computes an asset's depreciation for a calendar year from
// its latest grid and its capitalised items (Q-132).
func (s Service) Depreciation(ctx context.Context, p *auth.Principal, requestID string, asset uuid.UUID, year int) (DepreciationView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DepreciationView{}, err
	}
	if year < 1970 || year > 9999 {
		return DepreciationView{}, fields("year", "Une année sur quatre chiffres est attendue.")
	}
	var out DepreciationView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Portfolio.AssetInTx(ctx, asset); err != nil {
			return err
		}
		var err error
		out, err = s.depreciationInTx(ctx, asset, year)
		return err
	})
	return out, err
}

func (s Service) depreciationInTx(ctx context.Context, asset uuid.UUID, year int) (DepreciationView, error) {
	out := DepreciationView{AssetID: asset, Year: year}
	err := func() error {
		var g *Grid
		gr, found, err := s.Repo.Grid(ctx, asset)
		if err != nil {
			return err
		}
		if found {
			v, err := gridView(gr)
			if err != nil {
				return err
			}
			g = &Grid{InServiceOn: *v.InServiceOn, BasisCents: v.BasisCents, Lines: v.Lines}
			out.GridVersion = v.Version
		}
		rows, err := s.Repo.Items(ctx, asset)
		if err != nil {
			return err
		}
		var items []Item
		for _, r := range rows {
			if r.Treatment == TreatmentExpense {
				if r.InServiceOn.Year() == year {
					out.ExpensedCents += money.Cents(r.CostCents)
				}
				continue
			}
			it := Item{ID: r.ID.String(), Label: r.Label, CostCents: money.Cents(r.CostCents), InServiceOn: period.FromTime(r.InServiceOn), Months: r.Months}
			if r.DisposedOn != nil {
				d := period.FromTime(*r.DisposedOn)
				it.DisposedOn = &d
			}
			items = append(items, it)
		}
		out.Rows, err = Depreciation(g, items, year)
		out.TotalCents = Total(out.Rows)
		return err
	}()
	return out, err
}
