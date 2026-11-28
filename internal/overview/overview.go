// Package overview is the month cockpit (RENT-FE-001 §4b, RENT-BE-001 §3
// GET /overview/month, Q-139): where a period stands, step by step, and the
// anomalies of the scope. Steps and Anomalies are pure functions over the
// facts the repository reads: the caller passes the period, the day, the
// instant and the thresholds.
package overview

import (
	"sort"
	"time"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Step keys, in the cockpit's order.
const (
	StepRentCalls = "rent_calls"
	StepPayments  = "payments"
	StepReceipts  = "receipts"
	StepBank      = "bank"
	StepRecovery  = "recovery"
)

// Step states.
const (
	StateDone    = "done"
	StateToDo    = "to_do"
	StateWaiting = "waiting"
	StateNothing = "nothing"
)

// Anomaly kinds.
const (
	KindRentCallMissing    = "rent_call_missing"
	KindJobFailed          = "job_failed"
	KindPaymentOverdue     = "payment_overdue"
	KindBankLineStale      = "bank_line_stale"
	KindReceiptMissing     = "receipt_missing"
	KindIRLRevisionDue     = "irl_revision_due"
	KindRegularisationDue  = "regularisation_due"
	KindInventoryMissing   = "inventory_missing"
	KindDiagnosticExpiring = "diagnostic_expiring"
	KindDepositDeadline    = "deposit_deadline"
)

// Severities, most urgent first.
const (
	SeverityBlocking = "blocking"
	SeverityToAct    = "to_act"
	SeverityToWatch  = "to_watch"
)

var severityRank = map[string]int{SeverityBlocking: 0, SeverityToAct: 1, SeverityToWatch: 2}

// Lease is a lease billable in the period: running (active or under notice)
// and occupied during it.
type Lease struct {
	ID      string `json:"id"`
	AssetID string `json:"asset_id,omitempty"`
	// Called says a live rent call of the period exists.
	Called bool `json:"called,omitempty"`
}

// Call is a live rent call, any period, as the cockpit reads it.
type Call struct {
	ID         string        `json:"id"`
	LeaseID    string        `json:"lease_id,omitempty"`
	AssetID    string        `json:"asset_id,omitempty"`
	Period     period.Period `json:"period"`
	DueOn      period.Date   `json:"due_on"`
	TotalCents money.Cents   `json:"total_cents"`
	PaidCents  money.Cents   `json:"paid_cents"`
	// PaidAt is when the call became fully paid (its last match), nil while
	// it is not.
	PaidAt *time.Time `json:"paid_at,omitempty"`
	// Receipted says the call's quittance is issued.
	Receipted bool `json:"receipted,omitempty"`
}

func (c Call) paid() bool { return c.PaidCents >= c.TotalCents }

// BankLine is an unmatched bank line.
type BankLine struct {
	ID          string      `json:"id"`
	BookedOn    period.Date `json:"booked_on"`
	AmountCents money.Cents `json:"amount_cents"`
}

// Bank is where the bank reconciliation stands.
type Bank struct {
	Accounts int `json:"accounts"`
	// LastBookedOn is the latest booking day of an imported line, nil with
	// none.
	LastBookedOn *period.Date `json:"last_booked_on,omitempty"`
	Unmatched    []BankLine   `json:"unmatched,omitempty"`
	// AlertDays is bank.reconciliation's unmatched_alert_days.
	AlertDays int `json:"-"`
}

// Recovery is where the recovery of a lease in arrears stands.
type Recovery struct {
	LeaseID string `json:"lease_id,omitempty"`
	AssetID string `json:"asset_id,omitempty"`
	// Formal says a recovery step (formal notice, commandement…) is recorded
	// in the current episode.
	Formal bool `json:"formal,omitempty"`
	// StepDue says a recovery step is due by today and not done, the
	// escalation not suspended by a respected payment plan.
	StepDue bool `json:"step_due,omitempty"`
}

// JobRun is a finished job run as the scope sees it.
type JobRun struct {
	ID         string    `json:"id"`
	Job        string    `json:"job"`
	Status     string    `json:"status"`
	FinishedAt time.Time `json:"finished_at"`
}

// Dated is an item the other modules already computed, with its day: an IRL
// revision due, an exercise ended, a diagnostic's expiry, a deposit's legal
// deadline, an active lease without its entry inventory (the lease's start).
type Dated struct {
	LeaseID    string      `json:"lease_id,omitempty"`
	AssetID    string      `json:"asset_id,omitempty"`
	DocumentID string      `json:"document_id,omitempty"`
	Ref        string      `json:"ref,omitempty"`
	On         period.Date `json:"on"`
}

// Facts is what Steps and Anomalies read.
type Facts struct {
	Period period.Period `json:"period"`
	// Today is the day in Europe/Paris, Now the instant.
	Today period.Date `json:"today"`
	Now   time.Time   `json:"now"`
	// MonthlyRan says billing.monthly finished a run for the period.
	MonthlyRan bool    `json:"monthly_ran,omitempty"`
	Billable   []Lease `json:"billable,omitempty"`
	// Calls is every live rent call of the scope.
	Calls []Call `json:"calls,omitempty"`
	Bank  Bank   `json:"bank"`
	// Recovery is every lease in arrears (a call overdue).
	Recovery []Recovery `json:"recovery,omitempty"`
	// RemindersSent counts the reminders sent in the period.
	RemindersSent int      `json:"reminders_sent"`
	Jobs          []JobRun `json:"jobs,omitempty"`
	Revisions     []Dated  `json:"revisions,omitempty"`
	// Exercises are the exercises of leased assets with charges and no
	// issued reconciliation, On their end.
	Exercises        []Dated         `json:"exercises,omitempty"`
	MissingInventory []Dated         `json:"missing_inventory,omitempty"`
	Diagnostics      []Dated         `json:"diagnostics,omitempty"`
	DepositDeadlines []Dated         `json:"deposit_deadlines,omitempty"`
	Thresholds       params.Overview `json:"-"`
}

// StepView is one step of the cockpit, with its counts (keys per step:
// RENT-BE-001 §3) and, for the bank, the last booking day.
type StepView struct {
	Key    string           `json:"key" enums:"rent_calls,payments,receipts,bank,recovery"`
	State  string           `json:"state" enums:"done,to_do,waiting,nothing"`
	Counts map[string]int64 `json:"counts"`
	On     *period.Date     `json:"on,omitempty" swaggertype:"string" format:"date"`
}

// Anomaly is something wrong, with what it concerns and since when. Days is
// the number of days from Since to today: positive once past, negative for a
// day still to come (an expiry, a deadline).
type Anomaly struct {
	Kind        string      `json:"kind" enums:"rent_call_missing,job_failed,payment_overdue,bank_line_stale,receipt_missing,irl_revision_due,regularisation_due,inventory_missing,diagnostic_expiring,deposit_deadline"`
	Severity    string      `json:"severity" enums:"blocking,to_act,to_watch"`
	LeaseID     string      `json:"lease_id,omitempty"`
	AssetID     string      `json:"asset_id,omitempty"`
	DocumentID  string      `json:"document_id,omitempty"`
	Ref         string      `json:"ref,omitempty"`
	AmountCents money.Cents `json:"amount_cents,omitempty"`
	Since       period.Date `json:"since" swaggertype:"string" format:"date"`
	Days        int         `json:"days"`
}

// Phase says where the period lies against today.
func phase(f Facts) int {
	cur := period.Period{Year: f.Today.Year, Month: f.Today.Month}
	return f.Period.Compare(cur)
}

// rentCallsDue says the period's rent calls should be issued by now: the
// period's monthly run finished, or the period started before today.
func rentCallsDue(f Facts) bool {
	return phase(f) <= 0 && (f.MonthlyRan || f.Today.After(f.Period.First()))
}

func daysFrom(since, today period.Date) int { return period.DaysInclusive(since, today) - 1 }

func signedDays(on, today period.Date) int {
	if on.After(today) {
		return -daysFrom(today, on)
	}
	return daysFrom(on, today)
}

func overdue(c Call, f Facts) bool {
	return !c.paid() && f.Today.After(c.DueOn.AddDays(f.Thresholds.OverdueGraceDays))
}

func receiptLate(c Call, f Facts) bool {
	return c.paid() && !c.Receipted && c.PaidAt != nil &&
		!f.Now.Before(c.PaidAt.Add(time.Duration(f.Thresholds.ReceiptDelayHours)*time.Hour))
}

func bankStale(l BankLine, b Bank, today period.Date) bool {
	return today.After(l.BookedOn.AddDays(b.AlertDays))
}

// bankBehind says no line is booked in the last StatementGapDays before the
// period's end (today for the current or a future period).
func bankBehind(f Facts) bool {
	ref := f.Period.Last()
	if phase(f) >= 0 {
		ref = f.Today
	}
	return f.Bank.LastBookedOn == nil || f.Bank.LastBookedOn.Before(ref.AddDays(-f.Thresholds.StatementGapDays))
}

// Steps returns the five steps of the period, in order (RENT-FE-001 §4b).
// The rent calls, payments and receipts are the period's; the bank and the
// recovery are where they stand today.
func Steps(f Facts) []StepView {
	return []StepView{rentCalls(f), payments(f), receipts(f), bank(f), recovery(f)}
}

func rentCalls(f Facts) StepView {
	var issued int64
	for _, l := range f.Billable {
		if l.Called {
			issued++
		}
	}
	n := int64(len(f.Billable))
	s := StepView{Key: StepRentCalls, Counts: map[string]int64{"billable": n, "issued": issued, "missing": n - issued}}
	switch {
	case n == 0:
		s.State = StateNothing
	case issued == n:
		s.State = StateDone
	case rentCallsDue(f):
		s.State = StateToDo
	default:
		s.State = StateWaiting
	}
	return s
}

// allCalled says every billable lease has its rent call of the period.
func (f Facts) allCalled() bool {
	for _, l := range f.Billable {
		if !l.Called {
			return false
		}
	}
	return true
}

func (f Facts) periodCalls() []Call {
	var out []Call
	for _, c := range f.Calls {
		if c.Period == f.Period {
			out = append(out, c)
		}
	}
	return out
}

func payments(f Facts) StepView {
	var called, received, paid, partly, unpaid, late int64
	calls := f.periodCalls()
	for _, c := range calls {
		called += int64(c.TotalCents)
		received += int64(min(c.PaidCents, c.TotalCents))
		switch {
		case c.paid():
			paid++
		case c.PaidCents > 0:
			partly++
		default:
			unpaid++
		}
		if !c.paid() && f.Today.After(c.DueOn) {
			late++
		}
	}
	s := StepView{Key: StepPayments, Counts: map[string]int64{"called_cents": called, "received_cents": received,
		"paid": paid, "partly_paid": partly, "unpaid": unpaid, "late": late}}
	switch {
	case len(calls) == 0 && len(f.Billable) == 0:
		s.State = StateNothing
	case len(calls) > 0 && paid == int64(len(calls)) && f.allCalled():
		s.State = StateDone
	case late > 0:
		s.State = StateToDo
	default:
		s.State = StateWaiting
	}
	return s
}

func receipts(f Facts) StepView {
	var paid, issued, late int64
	calls := f.periodCalls()
	for _, c := range calls {
		if !c.paid() {
			continue
		}
		paid++
		if c.Receipted {
			issued++
		} else if receiptLate(c, f) {
			late++
		}
	}
	s := StepView{Key: StepReceipts, Counts: map[string]int64{"paid": paid, "issued": issued, "missing": paid - issued}}
	switch {
	case len(calls) == 0 && len(f.Billable) == 0:
		s.State = StateNothing
	case paid > 0 && issued == paid && paid == int64(len(calls)) && f.allCalled():
		s.State = StateDone
	case late > 0:
		s.State = StateToDo
	default:
		// A receipt not issued yet within the delay, or calls not paid yet.
		s.State = StateWaiting
	}
	return s
}

func bank(f Facts) StepView {
	var stale, oldest int64
	for _, l := range f.Bank.Unmatched {
		if bankStale(l, f.Bank, f.Today) {
			stale++
		}
		oldest = max(oldest, int64(daysFrom(l.BookedOn, f.Today)))
	}
	s := StepView{Key: StepBank, On: f.Bank.LastBookedOn, Counts: map[string]int64{"accounts": int64(f.Bank.Accounts),
		"unmatched": int64(len(f.Bank.Unmatched)), "stale": stale, "oldest_unmatched_days": oldest}}
	switch {
	case f.Bank.Accounts == 0:
		s.State = StateNothing
	case stale > 0 || bankBehind(f):
		s.State = StateToDo
	default:
		s.State = StateDone
	}
	return s
}

func recovery(f Facts) StepView {
	var formal, due int64
	for _, r := range f.Recovery {
		if r.Formal {
			formal++
		}
		if r.StepDue {
			due++
		}
	}
	s := StepView{Key: StepRecovery, Counts: map[string]int64{"in_arrears": int64(len(f.Recovery)),
		"reminders_sent": int64(f.RemindersSent), "formal": formal, "due": due}}
	switch {
	case len(f.Recovery) == 0:
		s.State = StateDone
	case due > 0:
		s.State = StateToDo
	default:
		s.State = StateWaiting
	}
	return s
}

// UpToDate says every step is done or has nothing to do.
func UpToDate(steps []StepView) bool {
	for _, s := range steps {
		if s.State != StateDone && s.State != StateNothing {
			return false
		}
	}
	return true
}

// Anomalies returns what is wrong as of today, by severity, then since when
// (oldest first), then kind and what it concerns (RENT-FE-001 §4b). Only the
// missing rent calls are the period's; the rest is the scope's today.
func Anomalies(f Facts) []Anomaly {
	out := []Anomaly{}
	add := func(a Anomaly) {
		a.Days = signedDays(a.Since, f.Today)
		out = append(out, a)
	}
	if rentCallsDue(f) {
		for _, l := range f.Billable {
			if !l.Called {
				add(Anomaly{Kind: KindRentCallMissing, Severity: SeverityBlocking, LeaseID: l.ID, AssetID: l.AssetID, Since: f.Period.First()})
			}
		}
	}
	window := f.Today.AddDays(-f.Thresholds.JobWindowDays)
	for _, j := range f.Jobs {
		on := period.FromTime(j.FinishedAt)
		if (j.Status == "failed" || j.Status == "partial") && !on.Before(window) {
			add(Anomaly{Kind: KindJobFailed, Severity: SeverityBlocking, Ref: j.Job, Since: on})
		}
	}
	for _, c := range f.Calls {
		if overdue(c, f) {
			add(Anomaly{Kind: KindPaymentOverdue, Severity: SeverityToAct, LeaseID: c.LeaseID, AssetID: c.AssetID, DocumentID: c.ID,
				AmountCents: c.TotalCents - c.PaidCents, Since: c.DueOn})
		}
		if receiptLate(c, f) {
			add(Anomaly{Kind: KindReceiptMissing, Severity: SeverityToAct, LeaseID: c.LeaseID, AssetID: c.AssetID, DocumentID: c.ID,
				AmountCents: c.TotalCents, Since: period.FromTime(*c.PaidAt)})
		}
	}
	for _, l := range f.Bank.Unmatched {
		if bankStale(l, f.Bank, f.Today) {
			add(Anomaly{Kind: KindBankLineStale, Severity: SeverityToAct, Ref: l.ID, AmountCents: l.AmountCents, Since: l.BookedOn})
		}
	}
	dated := func(kind, severity string, items []Dated, keep func(Dated) bool) {
		for _, d := range items {
			if keep(d) {
				add(Anomaly{Kind: kind, Severity: severity, LeaseID: d.LeaseID, AssetID: d.AssetID, DocumentID: d.DocumentID, Ref: d.Ref, Since: d.On})
			}
		}
	}
	always := func(Dated) bool { return true }
	dated(KindIRLRevisionDue, SeverityToAct, f.Revisions, always)
	regularise := f.Today.AddDays(-f.Thresholds.RegularisationAfterDays)
	dated(KindRegularisationDue, SeverityToAct, f.Exercises, func(d Dated) bool { return d.On.Before(regularise) })
	dated(KindInventoryMissing, SeverityToAct, f.MissingInventory, always)
	horizon := f.Today.AddDays(f.Thresholds.ExpiringDays)
	ahead := func(d Dated) bool { return !d.On.After(horizon) }
	dated(KindDiagnosticExpiring, SeverityToWatch, f.Diagnostics, ahead)
	dated(KindDepositDeadline, SeverityToWatch, f.DepositDeadlines, ahead)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] < severityRank[b.Severity]
		}
		if c := a.Since.Compare(b.Since); c != 0 {
			return c < 0
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.LeaseID+a.AssetID+a.DocumentID+a.Ref < b.LeaseID+b.AssetID+b.DocumentID+b.Ref
	})
	return out
}
