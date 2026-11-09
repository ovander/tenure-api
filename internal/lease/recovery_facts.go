package lease

import (
	"context"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/period"
)

// Surety is a natural-person surety of a lease, whom the recovery of unpaid
// rent informs (RENT-SPEC-011).
type Surety struct {
	GuaranteeID uuid.UUID `json:"guarantee_id"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
}

// RecoveryParties is what the recovery of unpaid rent reads of a lease
// (internal/billing, Q-121): the parties of its letters, its natural-person
// sureties in force, whether an insurer or Visale covers it, and the first
// day of its current term.
type RecoveryParties struct {
	State          State
	AssetID        uuid.UUID
	Owner          document.Party
	Tenants        []string
	Premises       []string
	ContractNumber string
	StartOn        period.Date
	Sureties       []Surety
	Insured        bool
	TermStartOn    period.Date
}

// RecoveryPartiesInTx reads a lease's recovery parties inside the caller's
// scoped transaction.
func (s Service) RecoveryPartiesInTx(ctx context.Context, sc db.Scope, id uuid.UUID, today period.Date) (RecoveryParties, error) {
	f, err := s.facts(ctx, sc, id, false)
	if err != nil {
		return RecoveryParties{}, err
	}
	out := RecoveryParties{State: State(f.st.lease.State), AssetID: principalID(f.lease), Owner: document.Party{Name: f.owner.LegalName, Address: f.owner.Address},
		Tenants: tenantNames(f.tenants), Premises: assetLabels(f.assets), StartOn: f.lease.StartOn}
	if ct, ok, err := s.Repo.LatestLeaseDocument(ctx, id, DocContract); err != nil {
		return RecoveryParties{}, err
	} else if ok {
		out.ContractNumber = ct.Number
	}
	inForce := func(g guaranteeRow) bool {
		if g.Status != string(StatusValid) && g.Status != string(StatusHeld) {
			return false
		}
		return g.ValidTo == nil || !period.FromTime(*g.ValidTo).Before(today)
	}
	for _, g := range f.guarantees {
		if !inForce(g) {
			continue
		}
		switch GuaranteeKind(g.Kind) {
		case GuaranteeSurety:
			if who := guarantorOf(g.Guarantor); !who.LegalPerson {
				out.Sureties = append(out.Sureties, Surety{GuaranteeID: g.ID, Name: who.Name, Address: who.Address})
			}
		case GuaranteeInsurance, GuaranteeVisale:
			out.Insured = true
		}
	}
	lt, err := f.ps.LeaseTerm(f.lease.StartOn)
	if err != nil {
		return RecoveryParties{}, err
	}
	tf := TermFacts{StartOn: f.lease.StartOn, TermMonths: f.lease.TermMonths, Renews: RenewsTacitly(f.lease.Kind, f.lease.TermMonths, lt.FurnishedStudent)}
	if !f.lease.EndOn.IsZero() {
		e := f.lease.EndOn
		tf.EndOn = &e
	}
	out.TermStartOn = f.lease.StartOn
	for _, t := range Terms(tf, today).Terms {
		if !t.StartOn.After(today) {
			out.TermStartOn = t.StartOn
		}
	}
	return out, nil
}
