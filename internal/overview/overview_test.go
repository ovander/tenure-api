package overview

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func d(s string) period.Date {
	v, err := period.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func per(s string) period.Period {
	v, err := period.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

// rules loads the cockpit thresholds and the bank alert from the legal
// fixtures: no threshold is written in a test.
func rules(t *testing.T) (params.Overview, int) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "params", "2024.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := params.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Overview(d("2026-11-15"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Bank(d("2026-11-15"))
	if err != nil {
		t.Fatal(err)
	}
	return o, b.UnmatchedAlertDays
}

// base is mid-November 2026, one billable lease called, paid and receipted,
// a bank up to date: every step done.
func base(t *testing.T) Facts {
	o, alert := rules(t)
	paidAt := at("2026-11-05T09:00:00Z")
	last := d("2026-11-14")
	return Facts{
		Period: per("2026-11"), Today: d("2026-11-15"), Now: at("2026-11-15T10:00:00Z"), MonthlyRan: true,
		Billable: []Lease{{ID: "L1", AssetID: "A1", Called: true}},
		Calls: []Call{{ID: "C1", LeaseID: "L1", AssetID: "A1", Period: per("2026-11"), DueOn: d("2026-11-05"),
			TotalCents: 90000, PaidCents: 90000, PaidAt: &paidAt, Receipted: true}},
		Bank:       Bank{Accounts: 1, LastBookedOn: &last, AlertDays: alert},
		Thresholds: o,
	}
}

func step(steps []StepView, key string) StepView {
	for _, s := range steps {
		if s.Key == key {
			return s
		}
	}
	return StepView{}
}

func TestStepsOrderAndUpToDate(t *testing.T) {
	steps := Steps(base(t))
	want := []string{StepRentCalls, StepPayments, StepReceipts, StepBank, StepRecovery}
	for i, s := range steps {
		if s.Key != want[i] || s.State != StateDone {
			t.Errorf("step %d: %s %s, want %s done", i, s.Key, s.State, want[i])
		}
	}
	if !UpToDate(steps) {
		t.Error("every step done: up to date")
	}
	f := base(t)
	f.Bank.Accounts = 0
	if s := Steps(f); !UpToDate(s) || step(s, StepBank).State != StateNothing {
		t.Error("no account: nothing to do, still up to date")
	}
	f.Billable[0].Called = false
	if UpToDate(Steps(f)) {
		t.Error("a rent call missing: not up to date")
	}
}

func TestRentCallsStep(t *testing.T) {
	for _, c := range []struct {
		name    string
		mod     func(*Facts)
		state   string
		missing int64
	}{
		{"no lease", func(f *Facts) { f.Billable = nil }, StateNothing, 0},
		{"every lease called", func(*Facts) {}, StateDone, 0},
		{"missing after the 1st", func(f *Facts) { f.Billable[0].Called = false }, StateToDo, 1},
		{"missing on the 1st before the run", func(f *Facts) {
			f.Billable[0].Called, f.MonthlyRan, f.Today = false, false, d("2026-11-01")
		}, StateWaiting, 1},
		{"missing on the 1st after the run", func(f *Facts) { f.Billable[0].Called, f.Today = false, d("2026-11-01") }, StateToDo, 1},
		{"a future month", func(f *Facts) { f.Billable[0].Called, f.MonthlyRan, f.Period = false, false, per("2026-12") }, StateWaiting, 1},
		{"a past month", func(f *Facts) { f.Billable[0].Called, f.MonthlyRan, f.Period = false, false, per("2026-10") }, StateToDo, 1},
	} {
		f := base(t)
		c.mod(&f)
		s := step(Steps(f), StepRentCalls)
		if s.State != c.state || s.Counts["missing"] != c.missing || s.Counts["billable"] != int64(len(f.Billable)) {
			t.Errorf("%s: %s %v, want %s missing %d", c.name, s.State, s.Counts, c.state, c.missing)
		}
	}
}

func TestPaymentsStep(t *testing.T) {
	for _, c := range []struct {
		name  string
		mod   func(*Facts)
		state string
		want  map[string]int64
	}{
		{"nothing billed, no lease", func(f *Facts) { f.Billable, f.Calls = nil, nil }, StateNothing, map[string]int64{"called_cents": 0}},
		{"calls to come", func(f *Facts) { f.Calls = nil }, StateWaiting, map[string]int64{"called_cents": 0}},
		{"paid", func(*Facts) {}, StateDone, map[string]int64{"paid": 1, "called_cents": 90000, "received_cents": 90000}},
		{"unpaid, due day passed", func(f *Facts) { f.Calls[0].PaidCents, f.Calls[0].PaidAt = 0, nil },
			StateToDo, map[string]int64{"unpaid": 1, "late": 1, "received_cents": 0}},
		{"partly paid on its due day", func(f *Facts) {
			f.Today, f.Calls[0].PaidCents, f.Calls[0].PaidAt = d("2026-11-05"), 40000, nil
		}, StateWaiting, map[string]int64{"partly_paid": 1, "late": 0, "received_cents": 40000}},
		{"paid, but a lease still without its call", func(f *Facts) { f.Billable = append(f.Billable, Lease{ID: "L2"}) },
			StateWaiting, map[string]int64{"paid": 1}},
		{"an earlier month's call is not this month's", func(f *Facts) { f.Calls[0].Period = per("2026-10") },
			StateWaiting, map[string]int64{"called_cents": 0}},
	} {
		f := base(t)
		c.mod(&f)
		s := step(Steps(f), StepPayments)
		if s.State != c.state {
			t.Errorf("%s: %s, want %s", c.name, s.State, c.state)
		}
		for k, v := range c.want {
			if s.Counts[k] != v {
				t.Errorf("%s: %s = %d, want %d", c.name, k, s.Counts[k], v)
			}
		}
	}
}

func TestReceiptsStep(t *testing.T) {
	for _, c := range []struct {
		name  string
		mod   func(*Facts)
		state string
	}{
		{"receipted", func(*Facts) {}, StateDone},
		{"no lease", func(f *Facts) { f.Billable, f.Calls = nil, nil }, StateNothing},
		{"receipted, but a lease still without its call", func(f *Facts) { f.Billable = append(f.Billable, Lease{ID: "L2"}) }, StateWaiting},
		{"not paid yet", func(f *Facts) { f.Calls[0].PaidCents, f.Calls[0].PaidAt, f.Calls[0].Receipted = 0, nil, false }, StateWaiting},
		{"paid within the delay", func(f *Facts) {
			f.Calls[0].Receipted = false
			f.Now = f.Calls[0].PaidAt.Add(time.Duration(f.Thresholds.ReceiptDelayHours)*time.Hour - time.Minute)
		}, StateWaiting},
		{"paid, the delay run out", func(f *Facts) {
			f.Calls[0].Receipted = false
			f.Now = f.Calls[0].PaidAt.Add(time.Duration(f.Thresholds.ReceiptDelayHours) * time.Hour)
		}, StateToDo},
	} {
		f := base(t)
		c.mod(&f)
		if s := step(Steps(f), StepReceipts); s.State != c.state {
			t.Errorf("%s: %s %v, want %s", c.name, s.State, s.Counts, c.state)
		}
	}
}

func TestBankStep(t *testing.T) {
	for _, c := range []struct {
		name  string
		mod   func(*Facts)
		state string
	}{
		{"no account", func(f *Facts) { f.Bank.Accounts = 0 }, StateNothing},
		{"up to date", func(*Facts) {}, StateDone},
		{"no statement imported", func(f *Facts) { f.Bank.LastBookedOn = nil }, StateToDo},
		{"the last line at the gap's edge", func(f *Facts) {
			l := f.Today.AddDays(-f.Thresholds.StatementGapDays)
			f.Bank.LastBookedOn = &l
		}, StateDone},
		{"the last line before the gap", func(f *Facts) {
			l := f.Today.AddDays(-f.Thresholds.StatementGapDays - 1)
			f.Bank.LastBookedOn = &l
		}, StateToDo},
		{"a past month is judged at its end", func(f *Facts) {
			f.Period = per("2026-09")
			l := f.Period.Last().AddDays(-f.Thresholds.StatementGapDays)
			f.Bank.LastBookedOn = &l
		}, StateDone},
		{"an unmatched line within the alert", func(f *Facts) {
			f.Bank.Unmatched = []BankLine{{ID: "B1", BookedOn: f.Today.AddDays(-f.Bank.AlertDays), AmountCents: 100}}
		}, StateDone},
		{"an unmatched line past the alert", func(f *Facts) {
			f.Bank.Unmatched = []BankLine{{ID: "B1", BookedOn: f.Today.AddDays(-f.Bank.AlertDays - 1), AmountCents: 100}}
		}, StateToDo},
	} {
		f := base(t)
		c.mod(&f)
		if s := step(Steps(f), StepBank); s.State != c.state {
			t.Errorf("%s: %s %v, want %s", c.name, s.State, s.Counts, c.state)
		}
	}
}

func TestRecoveryStep(t *testing.T) {
	for _, c := range []struct {
		name  string
		rec   []Recovery
		state string
	}{
		{"no arrears", nil, StateDone},
		{"in arrears, the reminders running", []Recovery{{LeaseID: "L1"}}, StateWaiting},
		{"a formal step recorded, the next not due", []Recovery{{LeaseID: "L1", Formal: true}}, StateWaiting},
		{"a step due", []Recovery{{LeaseID: "L1"}, {LeaseID: "L2", StepDue: true}}, StateToDo},
	} {
		f := base(t)
		f.Recovery, f.RemindersSent = c.rec, 2
		s := step(Steps(f), StepRecovery)
		if s.State != c.state || s.Counts["in_arrears"] != int64(len(c.rec)) || s.Counts["reminders_sent"] != 2 {
			t.Errorf("%s: %s %v, want %s", c.name, s.State, s.Counts, c.state)
		}
	}
}

func kinds(as []Anomaly) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, a.Kind)
	}
	return out
}

func TestAnomalyRules(t *testing.T) {
	for _, c := range []struct {
		name string
		mod  func(*Facts)
		want []string
	}{
		{"nothing wrong", func(*Facts) {}, []string{}},
		{"a rent call missing", func(f *Facts) { f.Billable[0].Called = false }, []string{KindRentCallMissing}},
		{"a rent call missing before the run", func(f *Facts) {
			f.Billable[0].Called, f.MonthlyRan, f.Today = false, false, d("2026-11-01")
		}, []string{}},
		{"a future month's call is not missing", func(f *Facts) { f.Billable[0].Called, f.MonthlyRan, f.Period = false, false, per("2026-12") }, []string{}},
		{"a failed run in the window", func(f *Facts) {
			f.Jobs = []JobRun{{ID: "J1", Job: "billing.receipts", Status: "failed", FinishedAt: f.Today.AddDays(-f.Thresholds.JobWindowDays).Time()}}
		}, []string{KindJobFailed}},
		{"a partial run", func(f *Facts) {
			f.Jobs = []JobRun{{ID: "J1", Job: "billing.monthly", Status: "partial", FinishedAt: f.Now}}
		}, []string{KindJobFailed}},
		{"a failed run before the window", func(f *Facts) {
			f.Jobs = []JobRun{{ID: "J1", Job: "billing.receipts", Status: "failed", FinishedAt: f.Today.AddDays(-f.Thresholds.JobWindowDays - 1).Time()}}
		}, []string{}},
		{"a run that succeeded", func(f *Facts) {
			f.Jobs = []JobRun{{ID: "J1", Job: "billing.monthly", Status: "succeeded", FinishedAt: f.Now}}
		}, []string{}},
		{"overdue past the grace", func(f *Facts) {
			f.Calls[0].PaidCents, f.Calls[0].PaidAt = 0, nil
			f.Today = f.Calls[0].DueOn.AddDays(f.Thresholds.OverdueGraceDays + 1)
		}, []string{KindPaymentOverdue}},
		{"unpaid within the grace", func(f *Facts) {
			f.Calls[0].PaidCents, f.Calls[0].PaidAt = 0, nil
			f.Today = f.Calls[0].DueOn.AddDays(f.Thresholds.OverdueGraceDays)
		}, []string{}},
		{"a stale bank line", func(f *Facts) {
			f.Bank.Unmatched = []BankLine{{ID: "B1", BookedOn: f.Today.AddDays(-f.Bank.AlertDays - 1), AmountCents: 100}}
		}, []string{KindBankLineStale}},
		{"a receipt late", func(f *Facts) {
			f.Calls[0].Receipted = false
			f.Now = f.Calls[0].PaidAt.Add(time.Duration(f.Thresholds.ReceiptDelayHours) * time.Hour)
		}, []string{KindReceiptMissing}},
		{"an IRL revision due", func(f *Facts) { f.Revisions = []Dated{{LeaseID: "L1", On: d("2026-11-01")}} }, []string{KindIRLRevisionDue}},
		{"an exercise past the delay", func(f *Facts) {
			f.Exercises = []Dated{{AssetID: "A1", Ref: "2025", On: f.Today.AddDays(-f.Thresholds.RegularisationAfterDays - 1)}}
		}, []string{KindRegularisationDue}},
		{"an exercise within the delay", func(f *Facts) {
			f.Exercises = []Dated{{AssetID: "A1", Ref: "2025", On: f.Today.AddDays(-f.Thresholds.RegularisationAfterDays)}}
		}, []string{}},
		{"an entry inventory missing", func(f *Facts) { f.MissingInventory = []Dated{{LeaseID: "L1", On: d("2026-01-01")}} }, []string{KindInventoryMissing}},
		{"a diagnostic at the horizon", func(f *Facts) {
			f.Diagnostics = []Dated{{AssetID: "A1", Ref: "dpe", On: f.Today.AddDays(f.Thresholds.ExpiringDays)}}
		}, []string{KindDiagnosticExpiring}},
		{"a diagnostic past the horizon", func(f *Facts) {
			f.Diagnostics = []Dated{{AssetID: "A1", Ref: "dpe", On: f.Today.AddDays(f.Thresholds.ExpiringDays + 1)}}
		}, []string{}},
		{"a diagnostic expired", func(f *Facts) { f.Diagnostics = []Dated{{AssetID: "A1", Ref: "dpe", On: d("2026-01-01")}} }, []string{KindDiagnosticExpiring}},
		{"a deposit deadline passed", func(f *Facts) { f.DepositDeadlines = []Dated{{LeaseID: "L1", On: d("2026-11-01")}} }, []string{KindDepositDeadline}},
	} {
		f := base(t)
		c.mod(&f)
		got := kinds(Anomalies(f))
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
			}
		}
	}
}

func TestAnomalyOrderAndFields(t *testing.T) {
	f := base(t)
	f.Calls[0].PaidCents, f.Calls[0].PaidAt = 30000, nil
	f.Billable = append(f.Billable, Lease{ID: "L2", AssetID: "A2"})
	f.Diagnostics = []Dated{{AssetID: "A1", Ref: "dpe", On: f.Today.AddDays(10)}}
	f.Revisions = []Dated{{LeaseID: "L1", AssetID: "A1", On: d("2026-10-20")}}
	f.Jobs = []JobRun{{ID: "J1", Job: "billing.receipts", Status: "failed", FinishedAt: f.Now}}
	got := Anomalies(f)
	want := []struct {
		kind, severity string
		since          string
	}{
		{KindRentCallMissing, SeverityBlocking, "2026-11-01"},
		{KindJobFailed, SeverityBlocking, "2026-11-15"},
		{KindIRLRevisionDue, SeverityToAct, "2026-10-20"},
		{KindPaymentOverdue, SeverityToAct, "2026-11-05"},
		{KindDiagnosticExpiring, SeverityToWatch, "2026-11-25"},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Severity != w.severity || got[i].Since.String() != w.since {
			t.Errorf("%d: %+v, want %+v", i, got[i], w)
		}
	}
	overdue := got[3]
	if overdue.AmountCents != money.Cents(60000) || overdue.DocumentID != "C1" || overdue.LeaseID != "L1" || overdue.Days != 10 {
		t.Errorf("payment_overdue: %+v", overdue)
	}
	if got[4].Days != -10 || got[4].Ref != "dpe" {
		t.Errorf("a day to come counts negative: %+v", got[4])
	}
	if got[0].LeaseID != "L2" || got[1].Ref != "billing.receipts" {
		t.Errorf("what each concerns: %+v %+v", got[0], got[1])
	}
}
