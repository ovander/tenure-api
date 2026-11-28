package billing

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
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/notify"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the billing module.
const (
	TypeNoRevisionDue    = "billing/no-revision-due"
	TypeAlreadyCancelled = "billing/already-cancelled"
	TypeNotCancellable   = "billing/not-cancellable"
	TypeReasonRequired   = "billing/cancel-reason-required"
	TypeReceiptIssued    = "billing/receipt-issued"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeNoRevisionDue:    problem.M(409, "Aucune révision de loyer n’est due pour ce bail.", "No rent revision is due for this lease."),
		TypeAlreadyCancelled: problem.M(409, "Ce document est déjà annulé.", "This document is already cancelled."),
		TypeNotCancellable:   problem.M(409, "Ce document ne peut pas être annulé.", "This document cannot be cancelled."),
		TypeReasonRequired:   problem.M(422, "Le motif de l’annulation est obligatoire.", "A reason for the cancellation is required."),
		TypeReceiptIssued:    problem.M(409, "Une quittance a été délivrée pour cet appel de loyer ; il ne peut plus être annulé.", "A receipt was issued for this rent call; it can no longer be cancelled."),
	})
}

// Template versions in use (RENT-BE-001 §6).
const (
	RentCallTemplate     = "v1"
	RevisionTemplate     = "v1"
	CancellationTemplate = "v1"
)

// Delivery is one e-mail of an issued document.
type Delivery struct {
	ID       uuid.UUID `json:"id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Status   string    `json:"status"`
	to       string
	language string
}

// IssuedDoc is a billing document issued by a run or an action.
type IssuedDoc struct {
	BillingDocumentID uuid.UUID  `json:"billing_document_id"`
	DocumentID        uuid.UUID  `json:"document_id"`
	Kind              Kind       `json:"kind"`
	Number            string     `json:"number"`
	LeaseID           uuid.UUID  `json:"lease_id"`
	Period            string     `json:"period"`
	TotalCents        int64      `json:"total_cents"`
	SHA256            string     `json:"sha256"`
	Deliveries        []Delivery `json:"deliveries"`
	pdf               []byte
	mail              notify.DocumentMail
}

// RevisionResult is a revision computed by a run.
type RevisionResult struct {
	LeaseID  uuid.UUID `json:"lease_id"`
	Due      Due       `json:"due"`
	Revision Revision  `json:"revision"`
	Applied  bool      `json:"applied"`
}

// OwnerResult is the outcome of one owner's run.
type OwnerResult struct {
	Period    string           `json:"period"`
	Fees      []Fee            `json:"fees,omitempty"`
	Revisions []RevisionResult `json:"revisions,omitempty"`
	Issued    []IssuedDoc      `json:"issued,omitempty"`
	Skipped   []uuid.UUID      `json:"already_issued,omitempty"`
}

// Service issues billing documents. Its methods that take no principal run
// inside the caller's scoped transaction (a job's owner iteration); the others
// open their own with the API role.
type Service struct {
	DB       *db.DB // API role
	Lease    lease.Service
	Repo     Repository
	Params   params.Repository
	Issuer   document.Issuer
	Docs     document.Repository
	Payments payment.Repository
	Mailer   notify.Mailer
	Now      func() time.Time
	NewID    func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func actorOf(ctx context.Context) string {
	sc, _ := db.ScopeFrom(ctx)
	if sc.ActorSub != "" {
		return sc.ActorSub
	}
	return "system:" + sc.Role
}

func feeLines(f Fee) []document.Line {
	var out []document.Line
	for _, l := range f.Lines {
		switch l.Kind {
		case LineRent:
			d := ""
			if f.Prorated {
				d = fmt.Sprintf("prorata %d/%d jours", f.OccupiedDays, f.DaysInPeriod)
			}
			out = append(out, document.Line{Kind: string(l.Kind), Label: "Loyer hors charges", Detail: d, AmountCents: l.AmountCents})
		case LineProvision:
			d := ""
			if f.Prorated {
				d = fmt.Sprintf("prorata %d/%d jours", f.OccupiedDays, f.DaysInPeriod)
			}
			out = append(out, document.Line{Kind: string(l.Kind), Label: "Provision sur charges", Detail: d, AmountCents: l.AmountCents})
		case LineRegularisation:
			d := l.Reference
			if l.Of > 0 {
				d = fmt.Sprintf("%s, échéance %d/%d", l.Reference, l.Instalment, l.Of)
			}
			out = append(out, document.Line{Kind: string(l.Kind), Label: "Régularisation des charges", Detail: d, AmountCents: l.AmountCents})
		}
	}
	return out
}

func names(r Record) []string {
	var out []string
	for _, t := range r.Tenants {
		out = append(out, strings.Join(t.Names, ", "))
	}
	return out
}

func addresses(r Record) []string {
	var out []string
	for _, a := range r.Assets {
		label := a.Address
		if a.Lot != "" {
			label += " — lot " + a.Lot
		}
		if a.Kind == "box" {
			label = "Box : " + label
		}
		out = append(out, label)
	}
	return out
}

func principal(r Record) uuid.UUID {
	if len(r.Assets) == 0 {
		return uuid.Nil
	}
	return r.Assets[0].ID // dwellings sort first
}

// deliveries records one pending e-mail per tenant with an address and a
// skipped one for a tenant without (Q-24).
func (s Service) deliveries(ctx context.Context, docID uuid.UUID, r Record) ([]Delivery, error) {
	sc, _ := db.ScopeFrom(ctx)
	var out []Delivery
	for _, t := range r.Tenants {
		d := Delivery{ID: s.NewID(), TenantID: t.ID, Status: "pending", to: t.Email, language: t.Language}
		if t.Email == "" {
			d.Status = "skipped"
		}
		now := s.now()
		if err := s.Docs.InsertDelivery(ctx, document.DeliveryRow(d.ID, sc.OwnerID, docID, t.Email, d.Status, now)); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (s Service) record(ctx context.Context, kind Kind, leaseID uuid.UUID, p string, gen int, iss document.Issued,
	lines []document.Line, total money.Cents, cancels *uuid.UUID, snapshot any) (docRow, error) {
	sc, _ := db.ScopeFrom(ctx)
	lj, err := json.Marshal(lines)
	if err != nil {
		return docRow{}, err
	}
	sj, err := json.Marshal(snapshot)
	if err != nil {
		return docRow{}, err
	}
	row := docRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, Kind: string(kind), Period: p, Generation: gen,
		Number: iss.Number, Lines: lj, TotalCents: int64(total), IssuedAt: iss.IssuedAt, CancelsID: cancels,
		DocumentID: iss.DocumentID, InputSnapshot: sj, CreatedBy: actorOf(ctx)}
	if err := s.Repo.Insert(ctx, row); err != nil {
		return docRow{}, err
	}
	return row, audit.Record(ctx, "billing."+string(kind)+".issue", audit.Entity{Type: "billing_document", ID: row.ID.String()}, nil,
		map[string]any{"number": iss.Number, "lease_id": leaseID, "period": p, "generation": gen, "total_cents": total,
			"document_id": iss.DocumentID, "sha256": iss.SHA256, "cancels_id": cancels})
}

// revise applies the revision due on today to l, unless dry, issuing the
// revision notice. It returns the lease as revised (or unchanged).
func (s Service) revise(ctx context.Context, l lease.Lease, v lease.View, r Record, today period.Date,
	series map[string]Index, ps params.Set, dry bool) (lease.Lease, *RevisionResult, *IssuedDoc, error) {
	due, ok, err := DueRevision(l, func(q string) (Index, bool) { i, ok := series[q]; return i, ok }, today)
	if err != nil || !ok {
		return l, nil, nil, err
	}
	rev, err := Revise(l, due.Index, due.Anniversary, due.From, ps)
	if err != nil {
		return l, nil, nil, err
	}
	res := &RevisionResult{LeaseID: v.ID, Due: due, Revision: rev}
	if dry {
		return rev.Apply(l), res, nil, nil
	}
	if err := s.Lease.ApplyRevisionInTx(ctx, v, int64(rev.Apply(l).RentCents), rev.NewRefQuarter, rev.NewRefValue, actorOf(ctx)); err != nil {
		return l, nil, nil, err
	}
	res.Applied = true
	if rev.Forfeited {
		return rev.Apply(l), res, nil, nil
	}
	live, gen, err := s.Repo.Live(ctx, v.ID, KindRevisionNotice, due.From.String())
	if err != nil || live != nil {
		return rev.Apply(l), res, nil, err
	}
	note := ""
	switch rev.CappedBy {
	case params.KeyIRLCap:
		note = "La variation de l’indice est plafonnée par la loi en vigueur à la date de révision."
	case params.KeyRentControlZoneCeiling:
		note = "Le loyer révisé est limité au loyer de référence majoré de la zone d’encadrement."
	}
	from := due.From
	data := func(number string, issued period.Date) any {
		return document.RevisionData{Number: number, IssuedOn: issued, Owner: document.Party{Name: r.OwnerName, Address: r.OwnerAddress},
			Tenants: names(r), Assets: addresses(r), RefQuarter: l.IRLRefQuarter, RefValue: l.IRLRefValue,
			NewQuarter: rev.NewRefQuarter, NewValue: rev.NewRefValue, OldRentCents: rev.OldRentCents, NewRentCents: rev.NewRentCents,
			EffectiveFrom: from, CappedNote: note}
	}
	snapshot := map[string]any{"lease": v, "due": due, "revision": rev, "template": "revision_notice/" + RevisionTemplate}
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRevisionNotice, TemplateVersion: RevisionTemplate,
		AssetID: principal(r), LeaseID: &v.ID, Period: &from, Snapshot: snapshot, Data: data}, ps)
	if err != nil {
		return l, nil, nil, err
	}
	lines := []document.Line{{Label: "Loyer hors charges révisé", AmountCents: rev.NewRentCents}}
	row, err := s.record(ctx, KindRevisionNotice, v.ID, from.String(), gen+1, iss, lines, rev.NewRentCents, nil, snapshot)
	if err != nil {
		return l, nil, nil, err
	}
	dels, err := s.deliveries(ctx, iss.DocumentID, r)
	if err != nil {
		return l, nil, nil, err
	}
	doc := &IssuedDoc{BillingDocumentID: row.ID, DocumentID: iss.DocumentID, Kind: KindRevisionNotice, Number: iss.Number, LeaseID: v.ID,
		Period: from.String(), TotalCents: int64(rev.NewRentCents), SHA256: iss.SHA256, Deliveries: dels, pdf: iss.PDF,
		mail: notify.DocumentMail{Kind: "revision_notice", Number: iss.Number, Owner: r.OwnerName}}
	return rev.Apply(l), res, doc, nil
}

// MonthlyOwner is billing.monthly for one owner, in the job's scoped
// transaction (RENT-SPEC-003 monthly run): for every active or notice lease
// occupied in the period, apply the revision due, then compute the fee and —
// unless a live rent call exists or this is a dry run — issue it. Dry run
// computes the same fees without writing.
func (s Service) MonthlyOwner(ctx context.Context, p period.Period, today period.Date, dry bool) (OwnerResult, error) {
	out := OwnerResult{Period: p.String()}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	series, err := s.Repo.IRLSeries(ctx)
	if err != nil {
		return out, err
	}
	ids, err := s.Repo.BillableLeaseIDs(ctx, p)
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		l, v, err := s.Lease.DomainInTx(ctx, id)
		if err != nil {
			return out, fmt.Errorf("lease %s: %w", id, err)
		}
		r, err := s.Repo.Record(ctx, id)
		if err != nil {
			return out, err
		}
		live, gen, err := s.Repo.Live(ctx, id, KindRentCall, p.String())
		if err != nil {
			return out, err
		}
		if live != nil {
			out.Skipped = append(out.Skipped, id)
			continue
		}
		l, rr, rdoc, err := s.revise(ctx, l, v, r, today, series, ps, dry)
		if err != nil {
			return out, fmt.Errorf("lease %s revision: %w", id, err)
		}
		if rr != nil && rr.Due.From.Compare(p) > 0 {
			// The revision applies from a later period: bill this one at the
			// current rent.
			l.RentCents = money.Cents(v.RentCents)
		}
		if rr != nil {
			out.Revisions = append(out.Revisions, *rr)
		}
		if rdoc != nil {
			out.Issued = append(out.Issued, *rdoc)
		}
		opts, post, err := s.regularisationFor(ctx, id, p)
		if err != nil {
			return out, fmt.Errorf("lease %s regularisation: %w", id, err)
		}
		fee, err := ComputeFee(l, p, ps, opts)
		if errors.Is(err, ErrNotBillable) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("lease %s fee: %w", id, err)
		}
		if dry {
			out.Fees = append(out.Fees, fee)
			continue
		}
		doc, err := s.issueRentCall(ctx, l, v, r, p, fee, ps, gen+1, rr)
		if err != nil {
			return out, fmt.Errorf("lease %s rent call: %w", id, err)
		}
		if err := s.carry(ctx, post, doc.BillingDocumentID, fee); err != nil {
			return out, fmt.Errorf("lease %s regularisation: %w", id, err)
		}
		out.Fees = append(out.Fees, fee)
		out.Issued = append(out.Issued, doc)
	}
	return out, nil
}

func (s Service) issueRentCall(ctx context.Context, l lease.Lease, v lease.View, r Record, p period.Period, fee Fee,
	ps params.Set, gen int, rr *RevisionResult) (IssuedDoc, error) {
	lines := feeLines(fee)
	due := period.Date{Year: p.Year, Month: p.Month, Day: min(l.DueDay, p.Days())}
	data := func(number string, issued period.Date) any {
		return document.RentCallData{Number: number, IssuedOn: issued, Owner: document.Party{Name: r.OwnerName, Address: r.OwnerAddress},
			Tenants: names(r), Assets: addresses(r), Period: p, DueOn: due, Lines: lines, TotalCents: fee.TotalCents}
	}
	snapshot := map[string]any{"lease": v, "fee": fee, "period": p, "rent_cents_applied": l.RentCents, "revision": rr,
		"template": "rent_call/" + RentCallTemplate, "generation": gen}
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRentCall, TemplateVersion: RentCallTemplate,
		AssetID: principal(r), LeaseID: &v.ID, Period: &p, Snapshot: snapshot, Data: data}, ps)
	if err != nil {
		return IssuedDoc{}, err
	}
	row, err := s.record(ctx, KindRentCall, v.ID, p.String(), gen, iss, lines, fee.TotalCents, nil, snapshot)
	if err != nil {
		return IssuedDoc{}, err
	}
	dels, err := s.deliveries(ctx, iss.DocumentID, r)
	if err != nil {
		return IssuedDoc{}, err
	}
	return IssuedDoc{BillingDocumentID: row.ID, DocumentID: iss.DocumentID, Kind: KindRentCall, Number: iss.Number, LeaseID: v.ID,
		Period: p.String(), TotalCents: int64(fee.TotalCents), SHA256: iss.SHA256, Deliveries: dels, pdf: iss.PDF,
		mail: notify.DocumentMail{Kind: "rent_call", Number: iss.Number, Period: document.PeriodFR(p), Owner: r.OwnerName}}, nil
}

// RevisionsOwner applies every revision due on today for one owner, in the
// job's scoped transaction (irl.fetch, RENT-BE-001 §5).
func (s Service) RevisionsOwner(ctx context.Context, today period.Date, dry bool) (OwnerResult, error) {
	out := OwnerResult{}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	series, err := s.Repo.IRLSeries(ctx)
	if err != nil {
		return out, err
	}
	ids, err := s.Repo.LeaseIDsInState(ctx, "active", "notice")
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		l, v, err := s.Lease.DomainInTx(ctx, id)
		if err != nil {
			return out, err
		}
		r, err := s.Repo.Record(ctx, id)
		if err != nil {
			return out, err
		}
		_, rr, doc, err := s.revise(ctx, l, v, r, today, series, ps, dry)
		if err != nil {
			return out, fmt.Errorf("lease %s revision: %w", id, err)
		}
		if rr != nil {
			out.Revisions = append(out.Revisions, *rr)
		}
		if doc != nil {
			out.Issued = append(out.Issued, *doc)
		}
	}
	return out, nil
}

// RevisionsDueInTx lists the IRL revisions due on today and not applied, as
// billing.revisions would apply them, for any role of the scope: it reads
// the leases without locking them and writes nothing (the month cockpit).
func (s Service) RevisionsDueInTx(ctx context.Context, today period.Date) ([]RevisionResult, error) {
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return nil, err
	}
	series, err := s.Repo.IRLSeries(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.Repo.LeaseIDsInState(ctx, "active", "notice")
	if err != nil {
		return nil, err
	}
	var out []RevisionResult
	for _, id := range ids {
		l, v, err := s.Lease.DomainReadInTx(ctx, id)
		if err != nil {
			return nil, err
		}
		_, rr, _, err := s.revise(ctx, l, v, Record{}, today, series, ps, true)
		if err != nil {
			return nil, fmt.Errorf("lease %s revision: %w", id, err)
		}
		if rr != nil {
			out = append(out, *rr)
		}
	}
	return out, nil
}

// StatementDoc prepares the delivery of a document another module issued for
// a lease (the regularisation statement of the charge module), inside the
// current transaction: one pending delivery per tenant with an e-mail. The
// caller sends it with Deliver after its commit.
func (s Service) StatementDoc(ctx context.Context, leaseID uuid.UUID, kind Kind, iss document.Issued, periodLabel string) (IssuedDoc, error) {
	r, err := s.Repo.Record(ctx, leaseID)
	if err != nil {
		return IssuedDoc{}, err
	}
	dels, err := s.deliveries(ctx, iss.DocumentID, r)
	if err != nil {
		return IssuedDoc{}, err
	}
	return IssuedDoc{DocumentID: iss.DocumentID, Kind: kind, Number: iss.Number, LeaseID: leaseID, Period: periodLabel,
		SHA256: iss.SHA256, Deliveries: dels, pdf: iss.PDF,
		mail: notify.DocumentMail{Kind: string(kind), Number: iss.Number, Period: periodLabel, Owner: r.OwnerName}}, nil
}

// Deliver sends the e-mails of documents committed in scope sc, recording each
// outcome with an audit event (RENT-BE-001 §6). It runs after the commit, so
// no e-mail ever carries a rolled-back document; a failed send is recorded and
// left for a later retry, it does not fail the run.
func (s Service) Deliver(ctx context.Context, d *db.DB, sc db.Scope, docs []IssuedDoc) []Delivery {
	var out []Delivery
	for _, doc := range docs {
		for _, del := range doc.Deliveries {
			if del.Status != "pending" {
				out = append(out, del)
				continue
			}
			m := doc.mail
			m.Language = del.language
			if m.Period == "" {
				m.Period = doc.Period
			}
			status, errText := "sent", ""
			if err := s.Mailer.Send(ctx, notify.Compose(m, del.to, doc.pdf)); err != nil {
				status, errText = "failed", err.Error()
			}
			err := d.InScope(ctx, sc, func(ctx context.Context) error {
				if err := s.Docs.SetDelivery(ctx, del.ID, status, errText, s.now()); err != nil {
					return err
				}
				return audit.Record(ctx, "document.deliver", audit.Entity{Type: "document_delivery", ID: del.ID.String()}, nil,
					map[string]any{"document_id": doc.DocumentID, "tenant_id": del.TenantID, "status": status})
			})
			if err != nil {
				status = "failed"
			}
			del.Status = status
			out = append(out, del)
		}
	}
	return out
}

func ownerScope(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// CancelInput is the body of POST /billing/documents/{id}/cancel.
type CancelInput struct {
	Reason string `json:"reason"`
}

// CancelResult is a cancellation and the rent call that replaces it.
type CancelResult struct {
	Cancellation IssuedDoc  `json:"cancellation"`
	Reissued     *IssuedDoc `json:"reissued"`
}

// Cancel cancels a live rent call and reissues it (RENT-SPEC-005, RENT-FE-001
// flow 5): a cancelling document is issued, then — when the lease is still
// billed for the period — a new rent call computed from the lease as it is now,
// as the next generation, in the same transaction. Revision notices are
// corrected by amendment (slice 5), not cancelled.
func (s Service) Cancel(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in CancelInput) (CancelResult, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return CancelResult{}, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" {
		return CancelResult{}, invariant.New(TypeReasonRequired, "reason")
	}
	var out IssuedDoc
	var reissued *IssuedDoc
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if cur.Kind != string(KindRentCall) {
			return invariant.New(TypeNotCancellable, cur.Number)
		}
		if cur.CancelledBy != nil {
			return invariant.New(TypeAlreadyCancelled, cur.Number)
		}
		if err := s.Payments.LockLease(ctx, cur.LeaseID); err != nil {
			return err
		}
		receipts, err := s.Repo.ReceiptsOf(ctx, cur.ID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(receipts, func(r docRow) bool { return r.Kind == string(KindReceipt) }) {
			return receiptedError(cur.Number)
		}
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		r, err := s.Repo.Record(ctx, cur.LeaseID)
		if err != nil {
			return err
		}
		var orig []document.Line
		if err := json.Unmarshal(cur.Lines, &orig); err != nil {
			return err
		}
		lines := make([]document.Line, len(orig))
		for i, l := range orig {
			lines[i] = document.Line{Kind: l.Kind, Label: l.Label, Detail: l.Detail, AmountCents: -l.AmountCents}
		}
		per, err := period.Parse(cur.Period)
		if err != nil {
			return err
		}
		class := document.ClassRentCall
		total := -money.Cents(cur.TotalCents)
		data := func(number string, issued period.Date) any {
			return document.CancellationData{Number: number, IssuedOn: issued, Owner: document.Party{Name: r.OwnerName, Address: r.OwnerAddress},
				Tenants: names(r), CancelledNumber: cur.Number, CancelledIssuedOn: period.FromTime(cur.IssuedAt), Period: per,
				Reason: in.Reason, Lines: lines, TotalCents: total}
		}
		snapshot := map[string]any{"cancels": cur.ID, "cancels_number": cur.Number, "reason": in.Reason, "template": "cancellation/" + CancellationTemplate}
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: class, Template: document.ClassCancellation, TemplateVersion: CancellationTemplate,
			AssetID: principal(r), LeaseID: &cur.LeaseID, Period: &per, Snapshot: snapshot, Data: data}, ps)
		if err != nil {
			return err
		}
		row, err := s.record(ctx, KindCancellation, cur.LeaseID, cur.Period, 1, iss, lines, total, &cur.ID, snapshot)
		if err != nil {
			return err
		}
		dels, err := s.deliveries(ctx, iss.DocumentID, r)
		if err != nil {
			return err
		}
		out = IssuedDoc{BillingDocumentID: row.ID, DocumentID: iss.DocumentID, Kind: KindCancellation, Number: iss.Number,
			LeaseID: cur.LeaseID, Period: cur.Period, TotalCents: int64(total), SHA256: iss.SHA256, Deliveries: dels, pdf: iss.PDF,
			mail: notify.DocumentMail{Kind: "cancellation", Number: iss.Number, Period: document.PeriodFR(per), Owner: r.OwnerName}}

		l, v, err := s.Lease.DomainInTx(ctx, cur.LeaseID)
		if err != nil {
			return err
		}
		// A revision issued since takes effect after this period: the call is
		// reissued at the rent in force in it, as the monthly run billed it.
		if old, ok, err := s.Repo.RentBefore(ctx, cur.LeaseID, cur.Period); err != nil {
			return err
		} else if ok {
			l.RentCents = money.Cents(old)
		}
		opts, post, err := s.regularisationFor(ctx, cur.LeaseID, per)
		if err != nil {
			return err
		}
		fee, err := ComputeFee(l, per, ps, opts)
		if errors.Is(err, ErrNotBillable) {
			// Nothing to reissue: the payments become unapplied again.
			return s.moveMatches(ctx, cur.ID, cur.ID, 0)
		}
		if err != nil {
			return err
		}
		_, gen, err := s.Repo.Live(ctx, cur.LeaseID, KindRentCall, cur.Period)
		if err != nil {
			return err
		}
		doc, err := s.issueRentCall(ctx, l, v, r, per, fee, ps, gen+1, nil)
		if err != nil {
			return err
		}
		reissued = &doc
		if err := s.carry(ctx, post, doc.BillingDocumentID, fee); err != nil {
			return err
		}
		return s.moveMatches(ctx, cur.ID, doc.BillingDocumentID, fee.TotalCents)
	})
	if err != nil {
		return CancelResult{}, err
	}
	docs := []IssuedDoc{out}
	if reissued != nil {
		docs = append(docs, *reissued)
	}
	dels := s.Deliver(ctx, s.DB, sc, docs)
	out.Deliveries = dels[:len(out.Deliveries)]
	if reissued != nil {
		reissued.Deliveries = dels[len(out.Deliveries):]
	}
	return CancelResult{Cancellation: out, Reissued: reissued}, nil
}

// ReviseNow applies the revision due today on one lease (POST
// /leases/{id}/revision), or computes it with dry.
func (s Service) ReviseNow(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, dry bool) (RevisionResult, *IssuedDoc, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return RevisionResult{}, nil, err
	}
	var res RevisionResult
	var doc *IssuedDoc
	today := period.FromTime(s.Now().In(parisTZ()))
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		series, err := s.Repo.IRLSeries(ctx)
		if err != nil {
			return err
		}
		l, v, err := s.Lease.DomainInTx(ctx, id)
		if err != nil {
			return err
		}
		if !l.State.Billable() {
			return invariant.New(TypeNoRevisionDue, "lease not active")
		}
		r, err := s.Repo.Record(ctx, id)
		if err != nil {
			return err
		}
		_, rr, d, err := s.revise(ctx, l, v, r, today, series, ps, dry)
		if err != nil {
			return err
		}
		if rr == nil {
			return invariant.New(TypeNoRevisionDue, id.String())
		}
		res, doc = *rr, d
		return nil
	})
	if err != nil {
		return RevisionResult{}, nil, err
	}
	if doc != nil {
		doc.Deliveries = s.Deliver(ctx, s.DB, sc, []IssuedDoc{*doc})
	}
	return res, doc, nil
}

func parisTZ() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}
