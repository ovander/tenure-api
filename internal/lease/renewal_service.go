package lease

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// RenewalTemplate is the version of the owner's proposal letter
// (templates/renewal).
const RenewalTemplate = "v1"

// DocRenewalProposal is the lease document kind of the proposal letter.
const DocRenewalProposal = "renewal_proposal"

// Where a re-evaluation proposal stands (lease_renewals.status).
const (
	RenewalPrepared = "prepared"
	RenewalReceived = "received"
	RenewalAccepted = "accepted"
	RenewalRefused  = "refused"
)

// RenewalLetterInput is the body of POST /leases/{id}/renewal/letter: the
// proposed rent and its references, and dry_run to preview the schedule.
type RenewalLetterInput struct {
	ReevaluationInput
	DryRun bool `json:"dry_run"`
}

// ReceptionInput is the body of POST /leases/{id}/renewals/{rid}/reception:
// the day the tenant received the proposal, and how.
type ReceptionInput struct {
	ReceivedOn  period.Date  `json:"received_on" swaggertype:"string" format:"date"`
	Delivery    string       `json:"delivery" enums:"registered_letter,bailiff,hand_delivery"`
	SentOn      *period.Date `json:"sent_on,omitempty" swaggertype:"string" format:"date"`
	TrackingRef string       `json:"tracking_ref,omitempty"`
	Version     int          `json:"version"`
}

// AnswerInput is the body of POST /leases/{id}/renewals/{rid}/answer: the
// tenant's agreement, or refusal, on a day.
type AnswerInput struct {
	Answer     string      `json:"answer" enums:"accepted,refused"`
	AnsweredOn period.Date `json:"answered_on" swaggertype:"string" format:"date"`
	Version    int         `json:"version"`
}

// StepsInput is the body of POST /leases/{id}/renewals/{rid}/steps.
type StepsInput struct {
	Version int  `json:"version"`
	DryRun  bool `json:"dry_run"`
}

// RenewalView is a re-evaluation proposal and where it stands.
type RenewalView struct {
	ID           uuid.UUID         `json:"id"`
	LeaseID      uuid.UUID         `json:"lease_id"`
	RenewalOn    period.Date       `json:"renewal_on" swaggertype:"string" format:"date"`
	Status       string            `json:"status" enums:"prepared,received,accepted,refused"`
	Proposal     ReevaluationInput `json:"proposal"`
	Terms        ReevaluationTerms `json:"terms"`
	DocumentID   uuid.UUID         `json:"document_id"`
	ReceivedOn   *period.Date      `json:"received_on,omitempty" swaggertype:"string" format:"date"`
	Delivery     string            `json:"delivery,omitempty" enums:"registered_letter,bailiff,hand_delivery"`
	TrackingRef  string            `json:"tracking_ref,omitempty"`
	AnsweredOn   *period.Date      `json:"answered_on,omitempty" swaggertype:"string" format:"date"`
	StepsApplied int               `json:"steps_applied"`
	// StepsDue is how many steps of an accepted proposal are due and not yet
	// applied to the rent, on the day read.
	StepsDue int `json:"steps_due"`
	Version  int `json:"version"`
}

// RenewalLetter is the proposal letter prepared, or previewed.
type RenewalLetter struct {
	Terms   ReevaluationTerms `json:"terms"`
	Letter  *DocumentView     `json:"letter,omitempty"`
	Renewal *RenewalView      `json:"renewal,omitempty"`
}

// StepsResult is the steps applied to the rent, or previewed.
type StepsResult struct {
	Applied   []Step      `json:"applied"`
	RentCents money.Cents `json:"rent_cents"`
	Renewal   RenewalView `json:"renewal"`
	Lease     *View       `json:"lease,omitempty"`
}

func renewalView(r renewalRow, today period.Date) RenewalView {
	v := RenewalView{ID: r.ID, LeaseID: r.LeaseID, RenewalOn: period.FromTime(r.RenewalOn), Status: r.Status, DocumentID: r.DocumentID,
		TrackingRef: r.TrackingRef, StepsApplied: r.StepsApplied, Version: r.Version}
	_ = json.Unmarshal(r.Proposal, &v.Proposal)
	_ = json.Unmarshal(r.Terms, &v.Terms)
	if r.ReceivedOn != nil {
		d := period.FromTime(*r.ReceivedOn)
		v.ReceivedOn = &d
	}
	if r.AnsweredOn != nil {
		d := period.FromTime(*r.AnsweredOn)
		v.AnsweredOn = &d
	}
	if r.Delivery != nil {
		v.Delivery = *r.Delivery
	}
	if r.Status == RenewalAccepted {
		v.StepsDue = len(dueSteps(v.Terms, r.StepsApplied, today))
	}
	return v
}

// dueSteps are the steps of a schedule after the first applied ones whose
// day has come.
func dueSteps(t ReevaluationTerms, applied int, today period.Date) []Step {
	var out []Step
	for k := applied; k < len(t.Steps); k++ {
		if t.Steps[k].From.After(today) {
			break
		}
		out = append(out, t.Steps[k])
	}
	return out
}

func reevaluationFacts(l Lease) ReevaluationFacts {
	f := ReevaluationFacts{Kind: l.Kind, StartOn: l.StartOn, TermMonths: l.TermMonths, RentCents: l.RentCents,
		TenseZone: l.TenseZone(), ControlledZone: l.ControlledZone()}
	if !l.EndOn.IsZero() {
		e := l.EndOn
		f.EndOn = &e
	}
	return f
}

// renewalLetterData is what templates/renewal renders.
type renewalLetterData struct {
	Number            string
	IssuedOn          period.Date
	Owner             document.Party
	Tenants           []string
	Premises          []string
	ContractNumber    string
	StartOn           period.Date
	LeadMonths        int
	ProposedRentCents money.Cents
	References        []Reference
	Terms             ReevaluationTerms
}

// Renewals returns a lease's re-evaluation proposals, the latest renewal
// first (RENT-SPEC-010, Q-120).
func (s Service) Renewals(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) ([]RenewalView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []RenewalView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Get(ctx, id, false); err != nil {
			return err
		}
		rows, err := s.Repo.Renewals(ctx, id)
		if err != nil {
			return err
		}
		today := s.today()
		for _, r := range rows {
			out = append(out, renewalView(r, today))
		}
		return nil
	})
	return out, err
}

// RenewalLetter prepares the owner's proposal of a new rent (PR-<asset>-
// <YYYY>-<seq>) for the next renewal of an active unfurnished lease the
// proposal can still reach (loi 89-462 art. 17-2, Q-120): the references,
// the cap of a zone tendue and the yearly steps. A proposal not yet received
// is replaced by a new letter; one received is final. A dry run only
// computes.
func (s Service) RenewalLetter(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in RenewalLetterInput) (RenewalLetter, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return RenewalLetter{}, err
	}
	for i := range in.References {
		in.References[i].Address = strings.TrimSpace(in.References[i].Address)
		in.References[i].Description = strings.TrimSpace(in.References[i].Description)
	}
	var out RenewalLetter
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		f, err := s.facts(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if State(f.st.lease.State) != StateActive {
			return invariant.New(TypeInvalidTransition, "renewal proposal from "+f.st.lease.State)
		}
		r, err := f.ps.Reevaluation(f.today)
		if err != nil {
			return err
		}
		terms, err := Reevaluate(reevaluationFacts(f.lease), in.ReevaluationInput, period.Date{}, f.today, r)
		if err != nil {
			return err
		}
		out.Terms = terms
		prev, exists, err := s.Repo.RenewalFor(ctx, id, terms.RenewalOn.Time())
		if err != nil {
			return err
		}
		if exists && prev.Status != RenewalPrepared {
			return invariant.New(TypeReevaluationExists, prev.ID.String())
		}
		if in.DryRun {
			return nil
		}
		contract := ""
		if ct, ok, err := s.Repo.LatestLeaseDocument(ctx, id, DocContract); err != nil {
			return err
		} else if ok {
			contract = ct.Number
		}
		data := renewalLetterData{Owner: document.Party{Name: f.owner.LegalName, Address: f.owner.Address},
			Tenants: tenantNames(f.tenants), Premises: assetLabels(f.assets), ContractNumber: contract,
			StartOn: f.lease.StartOn, LeadMonths: r.LeadMonths, ProposedRentCents: in.ProposedRentCents,
			References: in.References, Terms: terms}
		snap := map[string]any{"template": "renewal/" + RenewalTemplate, "lease": f.view, "proposal": in.ReevaluationInput, "terms": terms}
		lid := id
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRenewal, TemplateVersion: RenewalTemplate,
			AssetID: principalID(f.lease), LeaseID: &lid, Snapshot: snap,
			Data: func(number string, issued period.Date) any {
				data.Number, data.IssuedOn = number, issued
				return data
			}}, f.ps)
		if err != nil {
			return err
		}
		doc, err := s.recordLeaseDocument(ctx, sc, id, DocRenewalProposal, f.st.lease.Version, nil, iss, snap, p.Sub)
		if err != nil {
			return err
		}
		proposal, err := json.Marshal(in.ReevaluationInput)
		if err != nil {
			return err
		}
		tj, err := json.Marshal(terms)
		if err != nil {
			return err
		}
		now := s.now()
		var before any
		row := renewalRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: id, RenewalOn: terms.RenewalOn.Time(), Status: RenewalPrepared,
			Proposal: proposal, Terms: tj, DocumentID: iss.DocumentID, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		if exists {
			before = renewalView(prev, f.today)
			row.ID, row.CreatedAt, row.CreatedBy, row.Version = prev.ID, prev.CreatedAt, prev.CreatedBy, prev.Version+1
			err = s.Repo.UpdateRenewal(ctx, row, prev.Version)
		} else {
			err = s.Repo.InsertRenewal(ctx, row)
		}
		if err != nil {
			return err
		}
		v := renewalView(row, f.today)
		out.Letter, out.Renewal = &doc, &v
		return audit.Record(ctx, "lease.renewal_letter", audit.Entity{Type: "lease_renewal", ID: row.ID.String()}, before, out)
	})
	return out, err
}

// RecordReception records the day the tenant received a proposal, which
// must be at the latest its lead time before the term it aims at, and how
// it was delivered.
func (s Service) RecordReception(ctx context.Context, p *auth.Principal, requestID string, id, rid uuid.UUID, in ReceptionInput) (RenewalView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return RenewalView{}, err
	}
	var fs invariant.Fields
	if in.ReceivedOn.IsZero() {
		fs.Add("received_on", "La date de réception est obligatoire.")
	}
	switch in.Delivery {
	case DeliveryRegistered, DeliveryBailiff, DeliveryHand:
	default:
		fs.Add("delivery", "Attendu : registered_letter, bailiff ou hand_delivery.")
	}
	if in.SentOn != nil && in.SentOn.After(in.ReceivedOn) {
		fs.Add("sent_on", "L’envoi ne peut pas suivre la réception.")
	}
	in.TrackingRef = strings.TrimSpace(in.TrackingRef)
	if len(in.TrackingRef) > 100 {
		fs.Add("tracking_ref", "Au plus 100 caractères.")
	}
	if err := fs.Err(); err != nil {
		return RenewalView{}, err
	}
	var out RenewalView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		f, err := s.facts(ctx, sc, id, true)
		if err != nil {
			return err
		}
		row, err := s.Repo.Renewal(ctx, id, rid, true)
		if err != nil {
			return err
		}
		if row.Version != in.Version {
			return invariant.ErrStale
		}
		if row.Status != RenewalPrepared {
			return invariant.New(TypeReevaluationState, row.Status)
		}
		if in.ReceivedOn.After(f.today) {
			return invariant.New(TypeReevaluationInvalid, "received_on after today")
		}
		before := renewalView(row, f.today)
		r, err := f.ps.Reevaluation(in.ReceivedOn)
		if err != nil {
			return err
		}
		// The proposal is checked as the letter states it: the rent of
		// that day.
		facts := reevaluationFacts(f.lease)
		facts.RentCents = before.Terms.CurrentRentCents
		terms, err := Reevaluate(facts, before.Proposal, in.ReceivedOn, f.today, r)
		if err != nil {
			return err
		}
		if terms.RenewalOn != before.RenewalOn {
			return invariant.New(TypeReevaluationTooLate, "received "+in.ReceivedOn.String()+" for "+before.RenewalOn.String())
		}
		on, d := in.ReceivedOn.Time(), in.Delivery
		row.Status, row.ReceivedOn, row.Delivery, row.TrackingRef = RenewalReceived, &on, &d, in.TrackingRef
		row.UpdatedAt, row.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateRenewal(ctx, row, in.Version); err != nil {
			return err
		}
		row.Version++
		out = renewalView(row, f.today)
		return audit.Record(ctx, "lease.renewal_reception", audit.Entity{Type: "lease_renewal", ID: rid.String()}, before, out)
	})
	return out, err
}

// RecordAnswer records the tenant's agreement or refusal of a received
// proposal. A refusal leaves the rent as it is: the parties may refer to
// the commission départementale de conciliation, then the judge (Q-120).
func (s Service) RecordAnswer(ctx context.Context, p *auth.Principal, requestID string, id, rid uuid.UUID, in AnswerInput) (RenewalView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return RenewalView{}, err
	}
	var fs invariant.Fields
	if in.Answer != RenewalAccepted && in.Answer != RenewalRefused {
		fs.Add("answer", "Attendu : accepted ou refused.")
	}
	if in.AnsweredOn.IsZero() {
		fs.Add("answered_on", "La date de la réponse est obligatoire.")
	}
	if err := fs.Err(); err != nil {
		return RenewalView{}, err
	}
	var out RenewalView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Get(ctx, id, true); err != nil {
			return err
		}
		row, err := s.Repo.Renewal(ctx, id, rid, true)
		if err != nil {
			return err
		}
		if row.Version != in.Version {
			return invariant.ErrStale
		}
		if row.Status != RenewalReceived {
			return invariant.New(TypeReevaluationState, row.Status)
		}
		today := s.today()
		if in.AnsweredOn.After(today) || in.AnsweredOn.Before(period.FromTime(*row.ReceivedOn)) {
			return invariant.New(TypeReevaluationInvalid, "answered_on")
		}
		before := renewalView(row, today)
		on := in.AnsweredOn.Time()
		row.Status, row.AnsweredOn, row.UpdatedAt, row.UpdatedBy = in.Answer, &on, s.now(), p.Sub
		if err := s.Repo.UpdateRenewal(ctx, row, in.Version); err != nil {
			return err
		}
		row.Version++
		out = renewalView(row, today)
		return audit.Record(ctx, "lease.renewal_answer", audit.Entity{Type: "lease_renewal", ID: rid.String()}, before, out)
	})
	return out, err
}

// ApplySteps applies to the lease's rent the steps of an accepted proposal
// whose day has come: each adds its share of the increase to the rent as it
// stands, so an IRL revision since the letter is kept (loi 89-462 art.
// 17-2: the increase applies without prejudice to the yearly revision). A
// dry run only computes.
func (s Service) ApplySteps(ctx context.Context, p *auth.Principal, requestID string, id, rid uuid.UUID, in StepsInput) (StepsResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return StepsResult{}, err
	}
	var out StepsResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.Repo.Get(ctx, id, true)
		if err != nil {
			return err
		}
		if ls := State(st.lease.State); ls != StateActive && ls != StateNotice {
			return invariant.New(TypeInvalidTransition, "renewal steps on "+st.lease.State)
		}
		row, err := s.Repo.Renewal(ctx, id, rid, true)
		if err != nil {
			return err
		}
		if row.Version != in.Version {
			return invariant.ErrStale
		}
		if row.Status != RenewalAccepted {
			return invariant.New(TypeReevaluationState, row.Status)
		}
		today := s.today()
		before := renewalView(row, today)
		due := dueSteps(before.Terms, row.StepsApplied, today)
		if len(due) == 0 {
			return invariant.New(TypeReevaluationNoStepDue, rid.String())
		}
		lv := view(st)
		prev := before.Terms.CurrentRentCents
		if row.StepsApplied > 0 {
			prev = before.Terms.Steps[row.StepsApplied-1].RentCents
		}
		rent := money.Cents(lv.RentCents)
		for _, d := range due {
			rent += d.RentCents - prev
			prev = d.RentCents
		}
		out.Applied, out.RentCents = due, rent
		if in.DryRun {
			out.Renewal = before
			return nil
		}
		if err := s.Repo.ApplyRevision(ctx, id, st.lease.Version, int64(rent), lv.IRLRefQuarter, lv.IRLRefValue, s.now(), p.Sub); err != nil {
			return err
		}
		row.StepsApplied += len(due)
		row.UpdatedAt, row.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateRenewal(ctx, row, in.Version); err != nil {
			return err
		}
		row.Version++
		out.Renewal = renewalView(row, today)
		after, err := s.Repo.Get(ctx, id, false)
		if err != nil {
			return err
		}
		av := view(after)
		out.Lease = &av
		return audit.Record(ctx, "lease.renewal_steps", audit.Entity{Type: "lease", ID: id.String()},
			map[string]any{"rent_cents": lv.RentCents, "steps_applied": before.StepsApplied}, out)
	})
	return out, err
}
