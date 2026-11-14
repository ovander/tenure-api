package maintenance

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

// ContractInput is the body of POST /maintenance-contracts and PUT
// /maintenance-contracts/{id}: a recurring maintenance of an asset.
type ContractInput struct {
	AssetID uuid.UUID `json:"asset_id"`
	// Kind is boiler (yearly servicing), chimney (sweeping) or other.
	Kind string `json:"kind" enums:"boiler,chimney,other"`
	// Fuel of a chimney, required for one: its sweeping rhythm depends on
	// it (maintenance.chimney, Q-131).
	Fuel         string     `json:"fuel,omitempty" enums:"gas,wood,pellets,oil,other"`
	Title        string     `json:"title" example:"Entretien de la chaudière"`
	ContractorID *uuid.UUID `json:"contractor_id,omitempty"`
	// IntervalMonths defaults to the legal rhythm of a boiler
	// (maintenance.recurring) or of a chimney's fuel (maintenance.chimney)
	// and may not exceed it; it is required for other.
	IntervalMonths *int `json:"interval_months,omitempty"`
	// Responsible is who must have it done, tenant or owner; it defaults to
	// the kind's (maintenance.recurring) and is required for other.
	Responsible string       `json:"responsible,omitempty" enums:"tenant,owner"`
	FirstDueOn  period.Date  `json:"first_due_on" swaggertype:"string" format:"date"`
	EndOn       *period.Date `json:"end_on,omitempty" swaggertype:"string" format:"date"`
	// DocumentID is the contract signed with the contractor, if any.
	DocumentID *uuid.UUID `json:"document_id,omitempty"`
	Notes      string     `json:"notes,omitempty"`
}

// VisitInput is the body of POST /maintenance-contracts/{id}/visits.
type VisitInput struct {
	DoneOn period.Date `json:"done_on" swaggertype:"string" format:"date"`
	// DocumentID is the certificate the contractor gave (attestation).
	DocumentID *uuid.UUID `json:"document_id,omitempty"`
	Notes      string     `json:"notes,omitempty"`
}

// Visit is a visit as the API returns it.
type Visit struct {
	ID         uuid.UUID   `json:"id"`
	DoneOn     period.Date `json:"done_on" swaggertype:"string" format:"date"`
	DocumentID *uuid.UUID  `json:"document_id"`
	Notes      string      `json:"notes,omitempty"`
}

// Contract is a maintenance contract as the API returns it.
type Contract struct {
	ID             uuid.UUID    `json:"id"`
	AssetID        uuid.UUID    `json:"asset_id"`
	Kind           string       `json:"kind" enums:"boiler,chimney,other"`
	Fuel           *string      `json:"fuel" enums:"gas,wood,pellets,oil,other"`
	Title          string       `json:"title"`
	ContractorID   *uuid.UUID   `json:"contractor_id"`
	IntervalMonths int          `json:"interval_months"`
	Responsible    string       `json:"responsible" enums:"tenant,owner"`
	FirstDueOn     period.Date  `json:"first_due_on" swaggertype:"string" format:"date"`
	EndOn          *period.Date `json:"end_on" swaggertype:"string" format:"date"`
	DocumentID     *uuid.UUID   `json:"document_id"`
	Notes          string       `json:"notes,omitempty"`
	LastDoneOn     *period.Date `json:"last_done_on" swaggertype:"string" format:"date"`
	// NextDueOn is the next visit's day; null once the contract has ended
	// before it.
	NextDueOn *period.Date `json:"next_due_on" swaggertype:"string" format:"date"`
	// Visits are the visits, the latest first (GET /maintenance-contracts/{id}
	// only).
	Visits  []Visit `json:"visits,omitempty"`
	Version int     `json:"version"`
}

func contractView(r contractRow) Contract {
	v := Contract{ID: r.ID, AssetID: r.AssetID, Kind: r.Kind, Fuel: r.Fuel, Title: r.Title, ContractorID: r.ContractorID, IntervalMonths: r.IntervalMonths,
		Responsible: r.Responsible, FirstDueOn: period.FromTime(r.FirstDueOn), EndOn: date(r.EndOn), DocumentID: r.DocumentID, Notes: r.Notes,
		LastDoneOn: date(r.LastDoneOn), Version: r.Version}
	if next, ok := NextDue(v.FirstDueOn, v.LastDoneOn, v.IntervalMonths, v.EndOn); ok {
		v.NextDueOn = &next
	}
	return v
}

// resolve checks a contract and fills its interval and who is responsible
// from the kind's legal rhythm (Q-131).
func (s Service) resolve(ctx context.Context, in ContractInput) (int, string, error) {
	in.Title = strings.TrimSpace(in.Title)
	var f invariant.Fields
	if in.AssetID == uuid.Nil {
		f.Add("asset_id", "Le bien est obligatoire.")
	}
	if !oneOf(in.Kind, KindBoiler, KindChimney, KindOther) {
		f.Add("kind", "Attendu : boiler, chimney ou other.")
	}
	if in.Title == "" || len(in.Title) > 200 {
		f.Add("title", "Un intitulé de 1 à 200 caractères est obligatoire.")
	}
	if in.FirstDueOn.IsZero() {
		f.Add("first_due_on", "La date de la première visite est obligatoire.")
	}
	if in.EndOn != nil && !in.FirstDueOn.IsZero() && in.EndOn.Before(in.FirstDueOn) {
		f.Add("end_on", "La fin ne peut précéder la première visite.")
	}
	switch {
	case in.Kind == KindChimney && in.Fuel == "":
		f.Add("fuel", "Le combustible est obligatoire pour un ramonage.")
	case in.Kind == KindChimney && !oneOf(in.Fuel, "gas", "wood", "pellets", "oil", "other"):
		f.Add("fuel", "Attendu : gas, wood, pellets, oil ou other.")
	case in.Kind != KindChimney && in.Fuel != "":
		f.Add("fuel", "Le combustible ne vaut que pour un ramonage.")
	}
	if in.Responsible != "" && !oneOf(in.Responsible, "tenant", "owner") {
		f.Add("responsible", "Attendu : tenant ou owner.")
	}
	if len(in.Notes) > 4000 {
		f.Add("notes", "4 000 caractères au plus.")
	}
	if err := f.Err(); err != nil {
		return 0, "", err
	}
	interval, responsible := 0, in.Responsible
	if in.IntervalMonths != nil {
		interval = *in.IntervalMonths
	}
	if in.Kind != KindOther {
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return 0, "", err
		}
		rec, err := ps.MaintenanceRecurring(in.FirstDueOn)
		if err != nil {
			return 0, "", err
		}
		rule, ok := rec.Kinds[in.Kind]
		if !ok || rule.IntervalMonths <= 0 {
			return 0, "", errors.New("maintenance: no legal rhythm for " + in.Kind)
		}
		if in.Kind == KindChimney {
			ch, err := ps.MaintenanceChimney(in.FirstDueOn)
			if err != nil {
				return 0, "", err
			}
			if rule.IntervalMonths = ch.Months(in.Fuel); rule.IntervalMonths <= 0 {
				return 0, "", errors.New("maintenance: no sweeping rhythm for " + in.Fuel)
			}
		}
		if interval == 0 {
			interval = rule.IntervalMonths
		}
		if interval > rule.IntervalMonths {
			f.Add("interval_months", "La loi impose une visite tous les "+strconv.Itoa(rule.IntervalMonths)+" mois au plus.")
		}
		if responsible == "" {
			responsible = rule.Responsible
		}
	}
	if interval < 1 || interval > 120 {
		f.Add("interval_months", "Un intervalle de 1 à 120 mois est obligatoire.")
	}
	if responsible == "" {
		f.Add("responsible", "Précisez qui fait faire l’entretien : tenant ou owner.")
	}
	if err := f.Err(); err != nil {
		return 0, "", err
	}
	if _, err := s.Portfolio.AssetInTx(ctx, in.AssetID); err != nil {
		if errors.Is(err, invariant.ErrNotFound) {
			f.Add("asset_id", "Ce bien n’existe pas dans ce portefeuille.")
			return 0, "", f
		}
		return 0, "", err
	}
	if in.ContractorID != nil {
		if _, err := s.Repo.Contractor(ctx, *in.ContractorID); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				return 0, "", invariant.New(TypeContractor, in.ContractorID.String())
			}
			return 0, "", err
		}
	}
	if err := s.documentsExist(ctx, "document_id", in.DocumentID); err != nil {
		return 0, "", err
	}
	return interval, responsible, nil
}

func applyContract(r *contractRow, in ContractInput, interval int, responsible string) {
	r.AssetID, r.Kind, r.Title, r.ContractorID = in.AssetID, in.Kind, strings.TrimSpace(in.Title), in.ContractorID
	r.Fuel = nil
	if in.Fuel != "" {
		fuel := in.Fuel
		r.Fuel = &fuel
	}
	r.IntervalMonths, r.Responsible, r.FirstDueOn, r.EndOn = interval, responsible, in.FirstDueOn.Time(), timeOf(in.EndOn)
	r.DocumentID, r.Notes = in.DocumentID, in.Notes
}

// Contracts lists the scope's maintenance contracts, of one asset when
// asset is set, each with its last and next visit.
func (s Service) Contracts(ctx context.Context, p *auth.Principal, requestID string, asset *uuid.UUID) ([]Contract, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []Contract{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Contracts(ctx, asset)
		for _, r := range rows {
			out = append(out, contractView(r))
		}
		return err
	})
	return out, err
}

// Contract returns a maintenance contract and its visits.
func (s Service) Contract(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (Contract, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Contract{}, err
	}
	var out Contract
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		out, err = s.contractInTx(ctx, id, false)
		return err
	})
	return out, err
}

func (s Service) contractInTx(ctx context.Context, id uuid.UUID, forUpdate bool) (Contract, error) {
	r, err := s.Repo.Contract(ctx, id, forUpdate)
	if err != nil {
		return Contract{}, err
	}
	v := contractView(r)
	vs, err := s.Repo.Visits(ctx, id)
	if err != nil {
		return Contract{}, err
	}
	v.Visits = []Visit{}
	for _, x := range vs {
		v.Visits = append(v.Visits, Visit{ID: x.ID, DoneOn: period.FromTime(x.DoneOn), DocumentID: x.DocumentID, Notes: x.Notes})
	}
	return v, nil
}

// CreateContract records a recurring maintenance (RENT-SPEC-015, Q-131).
func (s Service) CreateContract(ctx context.Context, p *auth.Principal, requestID string, in ContractInput) (Contract, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Contract{}, err
	}
	var out Contract
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		interval, responsible, err := s.resolve(ctx, in)
		if err != nil {
			return err
		}
		now := s.now()
		r := contractRow{ID: s.NewID(), OwnerID: sc.OwnerID, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		applyContract(&r, in, interval, responsible)
		if err := s.Repo.InsertContract(ctx, r); err != nil {
			return err
		}
		out = contractView(r)
		return audit.Record(ctx, "maintenance.contract", audit.Entity{Type: "maintenance_contract", ID: r.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateContract replaces a recurring maintenance (not its asset) when
// version is the one read.
func (s Service) UpdateContract(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in ContractInput) (Contract, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Contract{}, err
	}
	var out Contract
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.Repo.Contract(ctx, id, true)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		if in.AssetID != old.AssetID {
			var f invariant.Fields
			f.Add("asset_id", "Le bien d’un contrat ne change pas.")
			return f
		}
		interval, responsible, err := s.resolve(ctx, in)
		if err != nil {
			return err
		}
		r := old
		applyContract(&r, in, interval, responsible)
		r.UpdatedAt, r.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateContract(ctx, r, version); err != nil {
			return err
		}
		r.Version = version + 1
		out = contractView(r)
		return audit.Record(ctx, "maintenance.contract_update", audit.Entity{Type: "maintenance_contract", ID: id.String()}, contractView(old), out)
	})
	return out, err
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

// RecordVisit records a visit done on a contract, with its certificate,
// when version is the contract's one read: the next visit is due its
// interval after the latest one. A visit is never changed.
func (s Service) RecordVisit(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in VisitInput) (Contract, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Contract{}, err
	}
	var f invariant.Fields
	today := period.FromTime(s.Now().In(paris()))
	if in.DoneOn.IsZero() {
		f.Add("done_on", "La date de la visite est obligatoire.")
	} else if today.Before(in.DoneOn) {
		f.Add("done_on", "Une visite à venir ne s’enregistre pas encore.")
	}
	if len(in.Notes) > 4000 {
		f.Add("notes", "4 000 caractères au plus.")
	}
	if err := f.Err(); err != nil {
		return Contract{}, err
	}
	var out Contract
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.contractInTx(ctx, id, true)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		if err := s.documentsExist(ctx, "document_id", in.DocumentID); err != nil {
			return err
		}
		now := s.now()
		v := visitRow{ID: s.NewID(), OwnerID: sc.OwnerID, ContractID: id, DoneOn: in.DoneOn.Time(), DocumentID: in.DocumentID,
			Notes: strings.TrimSpace(in.Notes), CreatedAt: now, CreatedBy: p.Sub}
		if err := s.Repo.InsertVisit(ctx, v); err != nil {
			return err
		}
		if err := s.Repo.TouchContract(ctx, id, version, now, p.Sub); err != nil {
			return err
		}
		out, err = s.contractInTx(ctx, id, false)
		if err != nil {
			return err
		}
		return audit.Record(ctx, "maintenance.visit", audit.Entity{Type: "maintenance_contract", ID: id.String()}, old, out)
	})
	return out, err
}
