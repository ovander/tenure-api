package maintenance

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Service implements contractors and incidents (RENT-SPEC-015).
type Service struct {
	DB        *db.DB
	Repo      Repository
	Portfolio portfolio.Service
	Docs      document.Service
	Charges   charge.Service
	Now       func() time.Time
	NewID     func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// ContractorInput is the body of POST /contractors and PUT /contractors/{id}.
type ContractorInput struct {
	Name  string `json:"name" example:"Plomberie Exemple"`
	Trade string `json:"trade,omitempty" example:"plombier"`
	Email string `json:"email,omitempty" example:"contact@plomberie.example"`
	Phone string `json:"phone,omitempty" example:"04 00 00 00 00"`
	Notes string `json:"notes,omitempty"`
}

// Contractor is a contractor as the API returns it.
type Contractor struct {
	ID uuid.UUID `json:"id"`
	ContractorInput
	Version int `json:"version"`
}

func contractorView(r contractorRow) Contractor {
	return Contractor{ID: r.ID, ContractorInput: ContractorInput{Name: r.Name, Trade: r.Trade, Email: r.Email, Phone: r.Phone, Notes: r.Notes}, Version: r.Version}
}

func checkContractor(in *ContractorInput) error {
	in.Name, in.Trade, in.Email, in.Phone = strings.TrimSpace(in.Name), strings.TrimSpace(in.Trade), strings.TrimSpace(in.Email), strings.TrimSpace(in.Phone)
	var f invariant.Fields
	if in.Name == "" || len(in.Name) > 120 {
		f.Add("name", "Un nom de 1 à 120 caractères est obligatoire.")
	}
	if len(in.Trade) > 80 {
		f.Add("trade", "80 caractères au plus.")
	}
	if in.Email != "" && (len(in.Email) > 200 || !strings.Contains(in.Email, "@")) {
		f.Add("email", "Une adresse électronique valide est attendue.")
	}
	if len(in.Phone) > 40 {
		f.Add("phone", "40 caractères au plus.")
	}
	if len(in.Notes) > 4000 {
		f.Add("notes", "4 000 caractères au plus.")
	}
	return f.Err()
}

// Contractors lists the owner's contractors.
func (s Service) Contractors(ctx context.Context, p *auth.Principal, requestID string) ([]Contractor, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []Contractor{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Contractors(ctx)
		for _, r := range rows {
			out = append(out, contractorView(r))
		}
		return err
	})
	return out, err
}

// CreateContractor records a contractor.
func (s Service) CreateContractor(ctx context.Context, p *auth.Principal, requestID string, in ContractorInput) (Contractor, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Contractor{}, err
	}
	if err := checkContractor(&in); err != nil {
		return Contractor{}, err
	}
	var out Contractor
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		now := s.now()
		r := contractorRow{ID: s.NewID(), OwnerID: sc.OwnerID, Name: in.Name, Trade: in.Trade, Email: in.Email, Phone: in.Phone, Notes: in.Notes,
			CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		if err := s.Repo.InsertContractor(ctx, r); err != nil {
			return err
		}
		out = contractorView(r)
		return audit.Record(ctx, "maintenance.contractor", audit.Entity{Type: "contractor", ID: r.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateContractor replaces a contractor when version is the one read.
func (s Service) UpdateContractor(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in ContractorInput) (Contractor, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Contractor{}, err
	}
	if err := checkContractor(&in); err != nil {
		return Contractor{}, err
	}
	var out Contractor
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.Repo.Contractor(ctx, id)
		if err != nil {
			return err
		}
		r := old
		r.Name, r.Trade, r.Email, r.Phone, r.Notes, r.UpdatedAt, r.UpdatedBy = in.Name, in.Trade, in.Email, in.Phone, in.Notes, s.now(), p.Sub
		if err := s.Repo.UpdateContractor(ctx, r, version); err != nil {
			return err
		}
		r.Version = version + 1
		out = contractorView(r)
		return audit.Record(ctx, "maintenance.contractor_update", audit.Entity{Type: "contractor", ID: id.String()}, contractorView(old), out)
	})
	return out, err
}

// IncidentInput is the body of POST /incidents and PUT /incidents/{id}: the
// report.
type IncidentInput struct {
	AssetID    uuid.UUID   `json:"asset_id"`
	LeaseID    *uuid.UUID  `json:"lease_id,omitempty"`
	ReportedOn period.Date `json:"reported_on" swaggertype:"string" format:"date"`
	// ReportedBy is tenant or owner (works the owner starts); Channel how it
	// came: email, phone, letter, portal or visit.
	ReportedBy       string      `json:"reported_by" enums:"tenant,owner"`
	Channel          string      `json:"channel" enums:"email,phone,letter,portal,visit"`
	ReporterName     string      `json:"reporter_name,omitempty"`
	Title            string      `json:"title" example:"Fuite sous l’évier"`
	Description      string      `json:"description,omitempty"`
	Urgency          string      `json:"urgency" enums:"low,normal,urgent"`
	PhotoDocumentIDs []uuid.UUID `json:"photo_document_ids,omitempty"`
}

// TransitionInput is the body of POST /incidents/{id}/transitions: the
// action and what it records.
type TransitionInput struct {
	Action            Action       `json:"action" enums:"quote,schedule,done,invoice,cancel"`
	ContractorID      *uuid.UUID   `json:"contractor_id,omitempty"`
	QuoteCents        *money.Cents `json:"quote_cents,omitempty"`
	QuoteDocumentID   *uuid.UUID   `json:"quote_document_id,omitempty"`
	ScheduledOn       *period.Date `json:"scheduled_on,omitempty" swaggertype:"string" format:"date"`
	DoneOn            *period.Date `json:"done_on,omitempty" swaggertype:"string" format:"date"`
	InvoiceCents      *money.Cents `json:"invoice_cents,omitempty"`
	InvoiceDocumentID *uuid.UUID   `json:"invoice_document_id,omitempty"`
	// Treatment of the invoice: repair (a deductible repairs charge) or
	// improvement (capitalised, never deducted).
	Treatment string       `json:"treatment,omitempty" enums:"repair,improvement"`
	PaidOn    *period.Date `json:"paid_on,omitempty" swaggertype:"string" format:"date"`
	Reason    string       `json:"reason,omitempty"`
}

// Incident is an incident as the API returns it.
type Incident struct {
	ID uuid.UUID `json:"id"`
	IncidentInput
	Status            Status       `json:"status" enums:"new,quoted,scheduled,done,invoiced,cancelled"`
	ContractorID      *uuid.UUID   `json:"contractor_id"`
	QuoteCents        *money.Cents `json:"quote_cents"`
	QuoteDocumentID   *uuid.UUID   `json:"quote_document_id"`
	ScheduledOn       *period.Date `json:"scheduled_on" swaggertype:"string" format:"date"`
	DoneOn            *period.Date `json:"done_on" swaggertype:"string" format:"date"`
	InvoiceCents      *money.Cents `json:"invoice_cents"`
	InvoiceDocumentID *uuid.UUID   `json:"invoice_document_id"`
	Treatment         *string      `json:"treatment" enums:"repair,improvement"`
	PaidOn            *period.Date `json:"paid_on" swaggertype:"string" format:"date"`
	// ChargeID is the repairs charge a repair's invoice created.
	ChargeID     *uuid.UUID `json:"charge_id"`
	CancelReason string     `json:"cancel_reason,omitempty"`
	Version      int        `json:"version"`
}

func date(t *time.Time) *period.Date {
	if t == nil {
		return nil
	}
	d := period.FromTime(*t)
	return &d
}

func cents(v *int64) *money.Cents {
	if v == nil {
		return nil
	}
	c := money.Cents(*v)
	return &c
}

func incidentView(r incidentRow) Incident {
	v := Incident{ID: r.ID, IncidentInput: IncidentInput{AssetID: r.AssetID, LeaseID: r.LeaseID, ReportedOn: period.FromTime(r.ReportedOn),
		ReportedBy: r.ReportedBy, Channel: r.Channel, ReporterName: r.ReporterName, Title: r.Title, Description: r.Description, Urgency: r.Urgency,
		PhotoDocumentIDs: []uuid.UUID{}}, Status: Status(r.Status), ContractorID: r.ContractorID, QuoteCents: cents(r.QuoteCents),
		QuoteDocumentID: r.QuoteDocumentID, ScheduledOn: date(r.ScheduledOn), DoneOn: date(r.DoneOn), InvoiceCents: cents(r.InvoiceCents),
		InvoiceDocumentID: r.InvoiceDocumentID, Treatment: r.Treatment, PaidOn: date(r.PaidOn), ChargeID: r.ChargeID, CancelReason: r.CancelReason,
		Version: r.Version}
	for _, id := range r.PhotoDocumentIDs {
		if u, err := uuid.Parse(id); err == nil {
			v.PhotoDocumentIDs = append(v.PhotoDocumentIDs, u)
		}
	}
	return v
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func checkReport(in *IncidentInput) error {
	in.Title, in.ReporterName = strings.TrimSpace(in.Title), strings.TrimSpace(in.ReporterName)
	if in.Urgency == "" {
		in.Urgency = "normal"
	}
	var f invariant.Fields
	if in.AssetID == uuid.Nil {
		f.Add("asset_id", "Le bien concerné est obligatoire.")
	}
	if in.ReportedOn.IsZero() {
		f.Add("reported_on", "La date du signalement est obligatoire.")
	}
	if !oneOf(in.ReportedBy, "tenant", "owner") {
		f.Add("reported_by", "Attendu : tenant ou owner.")
	}
	if !oneOf(in.Channel, "email", "phone", "letter", "portal", "visit") {
		f.Add("channel", "Attendu : email, phone, letter, portal ou visit.")
	}
	if in.Title == "" || len(in.Title) > 200 {
		f.Add("title", "Un intitulé de 1 à 200 caractères est obligatoire.")
	}
	if len(in.Description) > 4000 {
		f.Add("description", "4 000 caractères au plus.")
	}
	if !oneOf(in.Urgency, "low", "normal", "urgent") {
		f.Add("urgency", "Attendu : low, normal ou urgent.")
	}
	if len(in.PhotoDocumentIDs) > 20 {
		f.Add("photo_document_ids", "20 photos au plus.")
	}
	return f.Err()
}

// documentsExist checks that each document is the scope's.
func (s Service) documentsExist(ctx context.Context, field string, ids ...*uuid.UUID) error {
	for _, id := range ids {
		if id == nil {
			continue
		}
		if _, err := s.Docs.Repo.Get(ctx, *id); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				var f invariant.Fields
				f.Add(field, "Ce document n’existe pas dans ce portefeuille.")
				return f
			}
			return err
		}
	}
	return nil
}

func (s Service) checkLinks(ctx context.Context, in IncidentInput) error {
	if _, err := s.Portfolio.AssetInTx(ctx, in.AssetID); err != nil {
		if errors.Is(err, invariant.ErrNotFound) {
			var f invariant.Fields
			f.Add("asset_id", "Ce bien n’existe pas dans ce portefeuille.")
			return f
		}
		return err
	}
	if in.LeaseID != nil {
		ok, err := s.Repo.LeaseOfAsset(ctx, *in.LeaseID, in.AssetID)
		if err != nil {
			return err
		}
		if !ok {
			var f invariant.Fields
			f.Add("lease_id", "Ce bail ne loue pas ce bien.")
			return f
		}
	}
	for i := range in.PhotoDocumentIDs {
		if err := s.documentsExist(ctx, "photo_document_ids", &in.PhotoDocumentIDs[i]); err != nil {
			return err
		}
	}
	return nil
}

func applyReport(r *incidentRow, in IncidentInput) {
	r.AssetID, r.LeaseID, r.ReportedOn, r.ReportedBy, r.Channel = in.AssetID, in.LeaseID, in.ReportedOn.Time(), in.ReportedBy, in.Channel
	r.ReporterName, r.Title, r.Description, r.Urgency = in.ReporterName, in.Title, in.Description, in.Urgency
	r.PhotoDocumentIDs = db.TextArray{}
	for _, id := range in.PhotoDocumentIDs {
		r.PhotoDocumentIDs = append(r.PhotoDocumentIDs, id.String())
	}
}

// Incidents lists the scope's incidents.
func (s Service) Incidents(ctx context.Context, p *auth.Principal, requestID string, f IncidentFilter) ([]Incident, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []Incident{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Incidents(ctx, f)
		for _, r := range rows {
			out = append(out, incidentView(r))
		}
		return err
	})
	return out, err
}

// Incident returns one incident.
func (s Service) Incident(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (Incident, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Incident{}, err
	}
	var out Incident
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Incident(ctx, id, false)
		out = incidentView(r)
		return err
	})
	return out, err
}

// CreateIncident records an incident reported (RENT-SPEC-015, Q-130).
func (s Service) CreateIncident(ctx context.Context, p *auth.Principal, requestID string, in IncidentInput) (Incident, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Incident{}, err
	}
	if err := checkReport(&in); err != nil {
		return Incident{}, err
	}
	var out Incident
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkLinks(ctx, in); err != nil {
			return err
		}
		now := s.now()
		r := incidentRow{ID: s.NewID(), OwnerID: sc.OwnerID, Status: string(StatusNew), CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		applyReport(&r, in)
		if err := s.Repo.InsertIncident(ctx, r); err != nil {
			return err
		}
		out = incidentView(r)
		return audit.Record(ctx, "maintenance.incident", audit.Entity{Type: "incident", ID: r.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateIncident replaces an incident's report while its works are not done
// (Editable), when version is the one read.
func (s Service) UpdateIncident(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in IncidentInput) (Incident, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Incident{}, err
	}
	if err := checkReport(&in); err != nil {
		return Incident{}, err
	}
	var out Incident
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.Repo.Incident(ctx, id, true)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		if !Editable(Status(old.Status)) {
			return invariant.New(TypeTransition, "the report of "+old.Status+" works no longer changes")
		}
		if in.AssetID != old.AssetID {
			var f invariant.Fields
			f.Add("asset_id", "Le bien d’un incident ne change pas.")
			return f
		}
		if err := s.checkLinks(ctx, in); err != nil {
			return err
		}
		r := old
		applyReport(&r, in)
		r.UpdatedAt, r.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.SaveIncident(ctx, r, version); err != nil {
			return err
		}
		r.Version = version + 1
		out = incidentView(r)
		return audit.Record(ctx, "maintenance.incident_update", audit.Entity{Type: "incident", ID: id.String()}, incidentView(old), out)
	})
	return out, err
}

func timeOf(d *period.Date) *time.Time {
	if d == nil {
		return nil
	}
	t := d.Time()
	return &t
}

// fields checks what an action needs and writes it on r (Q-130).
func (s Service) fields(ctx context.Context, r *incidentRow, in TransitionInput) error {
	var f invariant.Fields
	needContractor := func() {
		if in.ContractorID == nil && r.ContractorID == nil {
			f.Add("contractor_id", "L’artisan est obligatoire.")
		}
	}
	switch in.Action {
	case ActionQuote:
		needContractor()
		if in.QuoteCents == nil || *in.QuoteCents <= 0 {
			f.Add("quote_cents", "Le montant du devis doit être positif.")
		}
	case ActionSchedule:
		needContractor()
		if in.ScheduledOn == nil {
			f.Add("scheduled_on", "La date d’intervention est obligatoire.")
		} else if in.ScheduledOn.Before(period.FromTime(r.ReportedOn)) {
			f.Add("scheduled_on", "L’intervention ne peut précéder le signalement.")
		}
	case ActionDone:
		if in.DoneOn == nil {
			f.Add("done_on", "La date de fin des travaux est obligatoire.")
		} else if in.DoneOn.Before(period.FromTime(r.ReportedOn)) {
			f.Add("done_on", "Les travaux ne peuvent finir avant le signalement.")
		}
	case ActionInvoice:
		if in.InvoiceCents == nil || *in.InvoiceCents <= 0 {
			f.Add("invoice_cents", "Le montant de la facture doit être positif.")
		}
		if !oneOf(in.Treatment, TreatmentRepair, TreatmentImprovement) {
			f.Add("treatment", "Attendu : repair (réparation, déductible) ou improvement (amélioration, immobilisée).")
		}
	case ActionCancel:
		if len(in.Reason) > 500 {
			f.Add("reason", "500 caractères au plus.")
		}
	}
	if err := f.Err(); err != nil {
		return err
	}
	if in.ContractorID != nil {
		if _, err := s.Repo.Contractor(ctx, *in.ContractorID); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				return invariant.New(TypeContractor, in.ContractorID.String())
			}
			return err
		}
		r.ContractorID = in.ContractorID
	}
	if err := s.documentsExist(ctx, "quote_document_id", in.QuoteDocumentID); err != nil {
		return err
	}
	if err := s.documentsExist(ctx, "invoice_document_id", in.InvoiceDocumentID); err != nil {
		return err
	}
	switch in.Action {
	case ActionQuote:
		v := int64(*in.QuoteCents)
		r.QuoteCents, r.QuoteDocumentID = &v, in.QuoteDocumentID
	case ActionSchedule:
		r.ScheduledOn = timeOf(in.ScheduledOn)
	case ActionDone:
		r.DoneOn = timeOf(in.DoneOn)
	case ActionInvoice:
		v := int64(*in.InvoiceCents)
		t := in.Treatment
		r.InvoiceCents, r.InvoiceDocumentID, r.Treatment, r.PaidOn = &v, in.InvoiceDocumentID, &t, timeOf(in.PaidOn)
	case ActionCancel:
		r.CancelReason = strings.TrimSpace(in.Reason)
	}
	return nil
}

// Transition moves an incident (RENT-SPEC-015, Q-130), when version is the
// one read. A repair's invoice creates a confirmed repairs charge of its
// amount, charged on the day the works were done, paid on paid_on, with the
// invoice as its document: deductible on line 224, matched to its bank
// debit like any charge, refused in a closed year (Q-127). An improvement's
// invoice creates no charge: it is capitalised on the asset, never
// deducted, and the owner's journal posts it when paid.
func (s Service) Transition(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in TransitionInput) (Incident, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Incident{}, err
	}
	var out Incident
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.Repo.Incident(ctx, id, true)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		to, err := Next(Status(old.Status), in.Action)
		if err != nil {
			return err
		}
		r := old
		if err := s.fields(ctx, &r, in); err != nil {
			return err
		}
		if in.Action == ActionInvoice && in.Treatment == TreatmentRepair {
			doc := r.InvoiceDocumentID
			c, err := s.Charges.CreateInTx(ctx, sc.OwnerID, p.Sub, "invoice", charge.Input{AssetID: r.AssetID, ChargedOn: period.FromTime(*r.DoneOn),
				PaidOn: date(r.PaidOn), Category: charge.CategoryRepairs, Label: "Travaux : " + r.Title, AmountCents: *r.InvoiceCents,
				DocumentID: doc, Status: "confirmed"})
			if err != nil {
				return err
			}
			r.ChargeID = &c.ID
		}
		r.Status, r.UpdatedAt, r.UpdatedBy = string(to), s.now(), p.Sub
		if err := s.Repo.SaveIncident(ctx, r, version); err != nil {
			return err
		}
		r.Version = version + 1
		out = incidentView(r)
		return audit.Record(ctx, "maintenance.incident_"+string(in.Action), audit.Entity{Type: "incident", ID: id.String()}, incidentView(old), out)
	})
	return out, err
}
