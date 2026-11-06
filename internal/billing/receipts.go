package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/notify"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
)

// ReceiptTemplate is the template version of receipts.
const ReceiptTemplate = "v1"

var methodsFR = map[string]string{"transfer": "virement", "direct_debit": "prélèvement", "cheque": "chèque",
	"cash": "espèces", "card": "carte", "caf": "allocation logement (CAF)", "other": "autre"}

// calls returns the live rent calls of a lease with their matches, as of the
// arrears calculation.
func (s Service) calls(ctx context.Context, leaseID uuid.UUID) ([]Call, error) {
	dues, err := s.Payments.Dues(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	dueDay, err := s.Repo.DueDay(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(dues))
	for i, d := range dues {
		ids[i] = d.BillingDocumentID
	}
	reminded, err := s.Repo.RemindedOn(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Call, 0, len(dues))
	for _, d := range dues {
		p, err := period.Parse(d.Period)
		if err != nil {
			return nil, err
		}
		c := Call{BillingDocumentID: d.BillingDocumentID, Number: d.Number, Period: p,
			DueOn: period.Date{Year: p.Year, Month: p.Month, Day: min(dueDay, p.Days())}, TotalCents: money.Cents(d.TotalCents), PaidCents: money.Cents(d.PaidCents)}
		if r, ok := reminded[d.BillingDocumentID]; ok {
			c.RemindedOn = &r
		}
		out = append(out, c)
	}
	return out, nil
}

// ArrearsFor returns the arrears of a lease (GET /leases/{id}/arrears).
func (s Service) ArrearsFor(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID) (ArrearsView, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return ArrearsView{}, err
	}
	var out ArrearsView
	today := period.FromTime(s.Now().In(parisTZ()))
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		days, err := ps.Int64(params.KeyReceiptReminderDays, today)
		if err != nil {
			return err
		}
		calls, err := s.calls(ctx, leaseID)
		if err != nil {
			return err
		}
		out = Arrears(leaseID, calls, today, int(days))
		return nil
	})
	return out, err
}

// ReceiptsOwner is billing.receipts for one owner, in the job's scoped
// transaction (RENT-SPEC-003 receipt): a quittance for every live rent call
// fully paid by its matches and not yet receipted; for one partly paid, a
// partial receipt of the matches no receipt covers yet — never a quittance.
func (s Service) ReceiptsOwner(ctx context.Context, dry bool) (OwnerResult, error) {
	out := OwnerResult{}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	leases, err := s.Payments.LeasesWithDues(ctx)
	if err != nil {
		return out, err
	}
	for _, leaseID := range leases {
		if err := s.Payments.LockLease(ctx, leaseID); err != nil {
			return out, err
		}
		dues, err := s.Payments.Dues(ctx, leaseID)
		if err != nil {
			return out, err
		}
		for _, d := range dues {
			if d.PaidCents == 0 {
				continue
			}
			doc, err := s.receiptFor(ctx, leaseID, d, ps, dry)
			if err != nil {
				return out, fmt.Errorf("rent call %s: %w", d.Number, err)
			}
			if doc != nil {
				out.Issued = append(out.Issued, *doc)
			}
		}
	}
	return out, nil
}

func (s Service) receiptFor(ctx context.Context, leaseID uuid.UUID, d payment.Due, ps params.Set, dry bool) (*IssuedDoc, error) {
	prior, err := s.Repo.ReceiptsOf(ctx, d.BillingDocumentID)
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	for _, r := range prior {
		if r.Kind == string(KindReceipt) {
			return nil, nil // quittance already issued
		}
		var snap struct {
			MatchIDs []string `json:"match_ids"`
		}
		_ = json.Unmarshal(r.InputSnapshot, &snap)
		for _, id := range snap.MatchIDs {
			covered[id] = true
		}
	}
	matches, err := s.Payments.MatchesOfDocument(ctx, d.BillingDocumentID)
	if err != nil {
		return nil, err
	}
	full := d.PaidCents >= d.TotalCents
	var news []payment.MatchRow
	for _, m := range matches {
		if !covered[m.ID.String()] {
			news = append(news, m)
		}
	}
	if !full && len(news) == 0 {
		return nil, nil
	}
	shown := news
	if full {
		shown = matches // the quittance lists every payment of the period
	}
	call, err := s.Repo.Get(ctx, d.BillingDocumentID)
	if err != nil {
		return nil, err
	}
	var lines []document.Line
	if err := json.Unmarshal(call.Lines, &lines); err != nil {
		return nil, err
	}
	var rent, charges money.Cents
	for _, l := range lines {
		if l.Kind == string(LineRent) {
			rent += l.AmountCents
		} else {
			charges += l.AmountCents
		}
	}
	pids := make([]uuid.UUID, 0, len(shown))
	for _, m := range shown {
		pids = append(pids, m.PaymentID)
	}
	info, err := s.Payments.Info(ctx, pids)
	if err != nil {
		return nil, err
	}
	var pays []document.PaymentLine
	var received money.Cents
	ids := make([]string, 0, len(matches))
	for _, m := range shown {
		pi := info[m.PaymentID]
		pays = append(pays, document.PaymentLine{PaidOn: period.FromTime(pi.PaidOn), Method: methodsFR[pi.Method], AmountCents: money.Cents(m.AmountCents)})
		received += money.Cents(m.AmountCents)
	}
	for _, m := range matches {
		ids = append(ids, m.ID.String())
	}
	slices.Sort(ids)
	if dry {
		kind := KindPartialReceipt
		if full {
			kind = KindReceipt
		}
		return &IssuedDoc{Kind: kind, LeaseID: leaseID, Period: d.Period, TotalCents: int64(received)}, nil
	}
	r, err := s.Repo.Record(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	per, err := period.Parse(d.Period)
	if err != nil {
		return nil, err
	}
	kind, gen := KindPartialReceipt, 0
	if full {
		kind, gen = KindReceipt, call.Generation
	} else {
		n, err := s.Repo.CountKind(ctx, leaseID, KindPartialReceipt, d.Period)
		if err != nil {
			return nil, err
		}
		gen = n + 1
	}
	data := func(number string, issued period.Date) any {
		return document.ReceiptData{Number: number, IssuedOn: issued, Owner: document.Party{Name: r.OwnerName, Address: r.OwnerAddress},
			Tenants: names(r), Assets: addresses(r), Period: per, RentCallNumber: call.Number, Partial: !full, Lines: lines,
			TotalCents: money.Cents(d.TotalCents), RentCents: rent, ChargesCents: charges, Payments: pays,
			PaidCents: money.Cents(d.PaidCents), ReceivedCents: received, OutstandingCents: money.Cents(d.TotalCents - d.PaidCents)}
	}
	snapshot := map[string]any{"rent_call_id": d.BillingDocumentID, "rent_call_number": call.Number, "match_ids": ids,
		"paid_cents": d.PaidCents, "total_cents": d.TotalCents, "partial": !full, "template": "receipt/" + ReceiptTemplate}
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassReceipt, TemplateVersion: ReceiptTemplate,
		AssetID: principal(r), LeaseID: &leaseID, Period: &per, Snapshot: snapshot, Data: data}, ps)
	if err != nil {
		return nil, err
	}
	total := money.Cents(d.TotalCents)
	if !full {
		total = received
	}
	row, err := s.record(ctx, kind, leaseID, d.Period, gen, iss, lines, total, nil, snapshot)
	if err != nil {
		return nil, err
	}
	dels, err := s.deliveries(ctx, iss.DocumentID, r)
	if err != nil {
		return nil, err
	}
	return &IssuedDoc{BillingDocumentID: row.ID, DocumentID: iss.DocumentID, Kind: kind, Number: iss.Number, LeaseID: leaseID,
		Period: d.Period, TotalCents: int64(total), SHA256: iss.SHA256, Deliveries: dels, pdf: iss.PDF,
		mail: notify.DocumentMail{Kind: string(kind), Number: iss.Number, Period: document.PeriodFR(per), Owner: r.OwnerName}}, nil
}

// ReminderDue is a reminder to send, computed by billing.reminders.
type ReminderDue struct {
	LeaseID           uuid.UUID   `json:"lease_id"`
	BillingDocumentID uuid.UUID   `json:"billing_document_id"`
	Number            string      `json:"number"`
	Period            string      `json:"period"`
	OutstandingCents  money.Cents `json:"outstanding_cents"`
	tenants           []Tenant
	owner             string
	dueOn             period.Date
}

// RemindersOwner is billing.reminders for one owner, in the job's scoped
// transaction: every live rent call still unpaid on its due day plus
// receipt_due_days_before_reminder, and not reminded yet (RENT-SPEC-003).
// Nothing is written here: the e-mails and their records follow the commit.
func (s Service) RemindersOwner(ctx context.Context, today period.Date) ([]ReminderDue, error) {
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return nil, err
	}
	days, err := ps.Int64(params.KeyReceiptReminderDays, today)
	if err != nil {
		return nil, err
	}
	leases, err := s.Payments.LeasesWithDues(ctx)
	if err != nil {
		return nil, err
	}
	var out []ReminderDue
	for _, leaseID := range leases {
		calls, err := s.calls(ctx, leaseID)
		if err != nil {
			return nil, err
		}
		var r *Record
		for _, it := range Arrears(leaseID, calls, today, int(days)).Items {
			if !it.ReminderDue {
				continue
			}
			if r == nil {
				rec, err := s.Repo.Record(ctx, leaseID)
				if err != nil {
					return nil, err
				}
				r = &rec
			}
			out = append(out, ReminderDue{LeaseID: leaseID, BillingDocumentID: it.BillingDocumentID, Number: it.Number,
				Period: it.Period.String(), OutstandingCents: it.OutstandingCents, tenants: r.Tenants, owner: r.OwnerName, dueOn: it.DueOn})
		}
	}
	return out, nil
}

// SendReminders e-mails the reminders computed for one owner and records each
// (one per rent call, with its audit event) in scope sc.
func (s Service) SendReminders(ctx context.Context, d *db.DB, sc db.Scope, today period.Date, due []ReminderDue) []ReminderDue {
	var sent []ReminderDue
	for _, rd := range due {
		recipients, failures := 0, 0
		for _, t := range rd.tenants {
			if t.Email == "" {
				continue
			}
			recipients++
			per, _ := period.Parse(rd.Period)
			m := notify.ComposeReminder(notify.Reminder{Number: rd.Number, Period: document.PeriodFR(per), DueOn: document.DateFR(rd.dueOn),
				Outstanding: document.Euros(rd.OutstandingCents), Owner: rd.owner, Language: t.Language}, t.Email)
			if err := s.Mailer.Send(ctx, m); err != nil {
				failures++
			}
		}
		err := d.InScope(ctx, sc, func(ctx context.Context) error {
			row := ReminderRow{ID: s.NewID(), OwnerID: sc.OwnerID, BillingDocumentID: rd.BillingDocumentID, SentOn: today.Time(),
				OutstandingCents: int64(rd.OutstandingCents), Recipients: recipients, Failures: failures, CreatedAt: s.now()}
			if err := s.Repo.InsertReminder(ctx, row); err != nil {
				return err
			}
			return audit.Record(ctx, "billing.reminder", audit.Entity{Type: "billing_document", ID: rd.BillingDocumentID.String()}, nil,
				map[string]any{"number": rd.Number, "outstanding_cents": rd.OutstandingCents, "recipients": recipients, "failures": failures})
		})
		if err == nil {
			sent = append(sent, rd)
		}
	}
	return sent
}

// moveMatches re-points the payments matched to a cancelled rent call to its
// reissue, up to the reissue's total; what the reissue cannot take becomes
// unapplied again (Q-63).
func (s Service) moveMatches(ctx context.Context, from, to uuid.UUID, total money.Cents) error {
	ms, err := s.Payments.MatchesOfDocument(ctx, from)
	if err != nil {
		return err
	}
	room := int64(total)
	for _, m := range ms {
		amount := min(m.AmountCents, max(room, 0))
		if err := s.Payments.MoveMatch(ctx, m.ID, to, amount); err != nil {
			return err
		}
		room -= amount
	}
	return nil
}

// errReceipted is returned when a cancelled rent call already has a quittance.
func receiptedError(number string) error { return invariant.New(TypeReceiptIssued, number) }

// OutstandingInTx is, inside the caller's scoped transaction, what the
// lease's live rent calls still owe (total − matched): the arrears the
// deposit settlement deducts (RENT-INV-001 §6). internal/app hands it to the
// inventory module, which does not import billing.
func (s Service) OutstandingInTx(ctx context.Context, leaseID uuid.UUID, today period.Date) (money.Cents, error) {
	calls, err := s.calls(ctx, leaseID)
	if err != nil {
		return 0, err
	}
	return Arrears(leaseID, calls, today, 0).OutstandingCents, nil
}
