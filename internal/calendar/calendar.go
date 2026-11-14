// Package calendar is the computed base of the obligations calendar
// (RENT-SPEC-012; RENT-INV-001 slice 10; Q-116): every dated obligation
// Tenure can derive from what it already holds, for the owner, the tenant
// and the asset. Build is a pure function: the caller passes the facts, the
// day and the parameter set.
package calendar

import (
	"sort"
	"time"

	"github.com/ovander/tenure-api/internal/inventory"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/maintenance"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Kinds of calendar items.
const (
	KindDiagnosticExpiry = "diagnostic_expiry"
	KindIRLRevision      = "irl_revision"
	KindNoticeEnd        = "notice_end"
	KindTermEnd          = "term_end"
	KindInsurance        = "insurance_certificate"
	KindCompletionWindow = "completion_window"
	KindDepositDeadline  = "deposit_deadline"
	KindMaintenanceDue   = "maintenance_due"
)

// Statuses of an item: due on or after today, or past and still to do.
const (
	StatusUpcoming = "upcoming"
	StatusOverdue  = "overdue"
)

// Lease is a lease as the calendar reads it.
type Lease struct {
	ID            string       `json:"id"`
	AssetIDs      []string     `json:"asset_ids"`
	Kind          string       `json:"kind"`
	State         string       `json:"state"`
	StartOn       period.Date  `json:"start_on"`
	EndOn         *period.Date `json:"end_on,omitempty"`
	TermMonths    int          `json:"term_months"`
	RevisionMonth int          `json:"revision_month,omitempty"`
	IRLRefQuarter string       `json:"irl_ref_quarter,omitempty"`
}

// Diagnostic is the latest diagnostic of a kind for an asset: its date and
// the end of validity its report states, if any. The end the calendar shows
// is lease.Expiry's (the legal validity from legal_params).
type Diagnostic struct {
	AssetID    string       `json:"asset_id"`
	Kind       string       `json:"kind"`
	DoneOn     *period.Date `json:"done_on,omitempty"`
	ValidUntil *period.Date `json:"valid_until,omitempty"`
}

// Entry is an issued entry inventory captured in Tenure.
type Entry struct {
	LeaseID string      `json:"lease_id"`
	DoneOn  period.Date `json:"done_on"`
}

// Exit is an issued exit inventory and where the deposit stands.
type Exit struct {
	LeaseID        string      `json:"lease_id"`
	KeysReturnedOn period.Date `json:"keys_returned_on"`
	Deductions     int         `json:"deductions"`
	// Settled says the first RD statement is issued.
	Settled bool `json:"settled"`
}

// Maintenance is a recurring maintenance contract of an asset and its
// latest visit (RENT-SPEC-015, Q-131).
type Maintenance struct {
	ID             string       `json:"id"`
	AssetID        string       `json:"asset_id"`
	Kind           string       `json:"kind"`
	Responsible    string       `json:"responsible"`
	IntervalMonths int          `json:"interval_months"`
	FirstDueOn     period.Date  `json:"first_due_on"`
	EndOn          *period.Date `json:"end_on,omitempty"`
	LastDoneOn     *period.Date `json:"last_done_on,omitempty"`
}

// Facts is what Build reads.
type Facts struct {
	Leases      []Lease       `json:"leases"`
	Diagnostics []Diagnostic  `json:"diagnostics"`
	Entries     []Entry       `json:"entries"`
	Exits       []Exit        `json:"exits"`
	Maintenance []Maintenance `json:"maintenance,omitempty"`
}

// Item is one dated obligation.
type Item struct {
	On      period.Date `json:"on" swaggertype:"string" format:"date"`
	Kind    string      `json:"kind" enums:"diagnostic_expiry,irl_revision,notice_end,term_end,insurance_certificate,completion_window,deposit_deadline,maintenance_due"`
	Status  string      `json:"status" enums:"upcoming,overdue"`
	AssetID string      `json:"asset_id,omitempty"`
	LeaseID string      `json:"lease_id,omitempty"`
	// Ref is what the item is about within its kind: the diagnostic kind,
	// the completion window (ten_day, heating), or the maintenance kind
	// (boiler, chimney, other).
	Ref string `json:"ref,omitempty"`
	// ContractID is the maintenance contract of a maintenance_due item.
	ContractID string `json:"contract_id,omitempty"`
	// Responsible is who must have a maintenance_due visit done: tenant or
	// owner.
	Responsible string `json:"responsible,omitempty" enums:"tenant,owner"`
}

func running(l Lease) bool { return l.State == "active" || l.State == "notice" }

// Build lists the obligations due from from to to (both included), soonest
// first, and the past ones still to do whatever their date: an expired
// diagnostic, a deposit deadline passed without its RD statement, a
// maintenance visit not recorded. A past
// date that needs nothing more is left out.
func Build(f Facts, from, to, today period.Date, ps params.Set) ([]Item, error) {
	var out []Item
	add := func(it Item, keepOverdue bool) {
		if it.On.Before(today) {
			if !keepOverdue {
				return
			}
			it.Status = StatusOverdue
			out = append(out, it)
			return
		}
		if it.On.Before(from) || it.On.After(to) {
			return
		}
		it.Status = StatusUpcoming
		out = append(out, it)
	}
	if len(f.Diagnostics) > 0 {
		rule, err := ps.Diagnostics(today)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Diagnostics {
			v := portfolio.DiagnosticView{DiagnosticInput: portfolio.DiagnosticInput{Kind: portfolio.DiagnosticKind(d.Kind), DoneOn: d.DoneOn, ValidUntil: d.ValidUntil}}
			if exp, ok := lease.Expiry(v, rule); ok {
				add(Item{On: exp, Kind: KindDiagnosticExpiry, AssetID: d.AssetID, Ref: d.Kind}, true)
			}
		}
	}
	byLease := map[string]Lease{}
	for _, l := range f.Leases {
		byLease[l.ID] = l
		asset := ""
		if len(l.AssetIDs) > 0 {
			asset = l.AssetIDs[0]
		}
		item := func(on period.Date, kind string) Item { return Item{On: on, Kind: kind, AssetID: asset, LeaseID: l.ID} }
		if l.State == "notice" && l.EndOn != nil {
			add(item(*l.EndOn, KindNoticeEnd), false)
		}
		if l.State != "active" || l.StartOn.IsZero() {
			continue
		}
		if end, ok := termEnd(l, today); ok {
			add(item(end, KindTermEnd), false)
		}
		if on, ok := nextRevision(l, today); ok {
			add(item(on, KindIRLRevision), false)
		}
		add(item(nextAnniversary(l.StartOn, today), KindInsurance), false)
	}
	for _, e := range f.Entries {
		l, ok := byLease[e.LeaseID]
		if !ok || !running(l) {
			continue
		}
		r, err := inventory.WindowRulesAt(ps, e.DoneOn)
		if err != nil {
			return nil, err
		}
		w := inventory.CompletionWindows(e.DoneOn, r)
		asset := first(l.AssetIDs)
		add(Item{On: w.TenDayEndsOn, Kind: KindCompletionWindow, AssetID: asset, LeaseID: l.ID, Ref: inventory.ReasonTenDay}, false)
		if w.HeatingEndsOn != nil {
			add(Item{On: *w.HeatingEndsOn, Kind: KindCompletionWindow, AssetID: asset, LeaseID: l.ID, Ref: inventory.ReasonHeating}, false)
		}
	}
	for _, x := range f.Exits {
		if x.Settled || x.KeysReturnedOn.IsZero() {
			continue
		}
		r, err := ps.DepositReturn(x.KeysReturnedOn)
		if err != nil {
			return nil, err
		}
		on, err := inventory.SettlementDeadline(x.KeysReturnedOn, x.Deductions > 0, r)
		if err != nil {
			return nil, err
		}
		add(Item{On: on, Kind: KindDepositDeadline, AssetID: first(byLease[x.LeaseID].AssetIDs), LeaseID: x.LeaseID}, true)
	}
	for _, m := range f.Maintenance {
		if on, ok := maintenance.NextDue(m.FirstDueOn, m.LastDoneOn, m.IntervalMonths, m.EndOn); ok {
			add(Item{On: on, Kind: KindMaintenanceDue, AssetID: m.AssetID, Ref: m.Kind, ContractID: m.ID, Responsible: m.Responsible}, true)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := out[i].On.Compare(out[j].On); c != 0 {
			return c < 0
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].LeaseID+out[i].AssetID+out[i].Ref+out[i].ContractID < out[j].LeaseID+out[j].AssetID+out[j].Ref+out[j].ContractID
	})
	if out == nil {
		out = []Item{}
	}
	return out, nil
}

func first(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// termEnd is the last day of the lease's current term, on or after today:
// the day before the start plus a whole number of terms. A lease renewed
// tacitly runs term after term; a mobility lease has one term only.
func termEnd(l Lease, today period.Date) (period.Date, bool) {
	if l.TermMonths <= 0 {
		return period.Date{}, false
	}
	for k := 1; k <= 100; k++ {
		end := l.StartOn.AddMonthsClamped(l.TermMonths * k).AddDays(-1)
		if !end.Before(today) {
			return end, true
		}
		if l.Kind == "mobility" {
			return period.Date{}, false
		}
	}
	return period.Date{}, false
}

// nextRevision is the next IRL anniversary on or after today, as billing
// reckons it (RENT-SPEC-003): the revision month, on the lease's start day
// capped to the month, at least twelve months after the start and in the
// year after the reference quarter or later.
func nextRevision(l Lease, today period.Date) (period.Date, bool) {
	if l.RevisionMonth < 1 || l.RevisionMonth > 12 || len(l.IRLRefQuarter) < 4 {
		return period.Date{}, false
	}
	refYear := 0
	for _, c := range l.IRLRefQuarter[:4] {
		if c < '0' || c > '9' {
			return period.Date{}, false
		}
		refYear = refYear*10 + int(c-'0')
	}
	at := func(y int) period.Date {
		p := period.Period{Year: y, Month: time.Month(l.RevisionMonth)}
		return period.Date{Year: y, Month: time.Month(l.RevisionMonth), Day: min(l.StartOn.Day, p.Days())}
	}
	firstOn := l.StartOn.AddMonths(12)
	y := max(firstOn.Year, refYear+1)
	ann := at(y)
	for ann.Before(firstOn) || ann.Before(today) {
		y++
		ann = at(y)
	}
	return ann, true
}

// nextAnniversary is the lease's next anniversary on or after today (the
// start day, capped to the month): the yearly insurance certificate.
func nextAnniversary(start, today period.Date) period.Date {
	at := func(y int) period.Date {
		p := period.Period{Year: y, Month: start.Month}
		return period.Date{Year: y, Month: start.Month, Day: min(start.Day, p.Days())}
	}
	y := max(today.Year, start.Year+1)
	ann := at(y)
	for ann.Before(today) {
		y++
		ann = at(y)
	}
	return ann
}
