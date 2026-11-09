package billing

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func recoveryParams(t *testing.T) params.Recovery {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "params", "*.json"))
	var rs []io.Reader
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fh.Close() }()
		rs = append(rs, fh)
	}
	ps, err := params.Load(rs...)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ps.Recovery(d(2026, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func kinds(v RecoveryView) []string {
	out := []string{}
	for _, n := range v.Next {
		out = append(out, n.Kind)
	}
	return out
}

func TestRecovery(t *testing.T) {
	r := recoveryParams(t)
	calls := []Call{
		{BillingDocumentID: uuid.New(), Period: p(2026, 3), DueOn: d(2026, 3, 5), TotalCents: 80000, PaidCents: 80000},
		{BillingDocumentID: uuid.New(), Period: p(2026, 4), DueOn: d(2026, 4, 5), TotalCents: 80000, PaidCents: 20000},
		{BillingDocumentID: uuid.New(), Period: p(2026, 5), DueOn: d(2026, 5, 5), TotalCents: 80000},
	}
	today := d(2026, 5, 10)
	arr := Arrears(uuid.New(), calls, today, 5)
	g := uuid.New()
	facts := RecoveryFacts{Sureties: []uuid.UUID{g}, Insured: true, TermStartOn: d(2025, 1, 1)}

	// Nothing done: the formal notice, the surety and the insurer are due
	// from the oldest overdue due day.
	v, err := Recovery(arr, nil, facts, today, r)
	if err != nil {
		t.Fatal(err)
	}
	since := d(2026, 4, 5)
	if v.Since == nil || *v.Since != since || v.ResolutionOn != nil {
		t.Fatalf("since %+v", v)
	}
	want := map[string]period.Date{StepFormalNotice: since.AddDays(r.FormalNoticeAfterDays), StepSuretyNotice: since.AddMonthsClamped(r.SuretyNoticeAfterMonths),
		StepInsuranceClaim: since.AddDays(r.InsuranceClaimAfterDays)}
	if len(v.Next) != 3 {
		t.Fatalf("next %v", kinds(v))
	}
	for _, n := range v.Next {
		if n.DueOn != want[n.Kind] || n.Overdue != today.After(n.DueOn) {
			t.Errorf("%s due %s overdue %v", n.Kind, n.DueOn, n.Overdue)
		}
		if n.Kind == StepSuretyNotice && (n.GuaranteeID == nil || *n.GuaranteeID != g) {
			t.Errorf("surety notice names its guarantee %+v", n)
		}
	}
	for i := 1; i < len(v.Next); i++ {
		if v.Next[i].DueOn.Before(v.Next[i-1].DueOn) {
			t.Errorf("next not by day %v", v.Next)
		}
	}

	// The formal notice, the surety informed and the claim done: the
	// commandement follows the notice.
	fn := d(2026, 4, 25)
	steps := []RecoveryStep{{Kind: StepFormalNotice, DoneOn: fn}, {Kind: StepSuretyNotice, DoneOn: d(2026, 5, 5), GuaranteeID: &g},
		{Kind: StepInsuranceClaim, DoneOn: d(2026, 5, 2)}}
	v, _ = Recovery(arr, steps, facts, today, r)
	if k := kinds(v); len(k) != 1 || k[0] != StepCommandement || v.Next[0].DueOn != fn.AddDays(r.CommandementAfterDays) {
		t.Errorf("after the notice %v %+v", k, v.Next)
	}

	// The commandement: its notice to the surety, then the court once the
	// clause résolutoire's delay has run, six weeks for a recent term.
	cmd := d(2026, 5, 12)
	steps = append(steps, RecoveryStep{Kind: StepCommandement, DoneOn: cmd})
	v, _ = Recovery(arr, steps, facts, d(2026, 5, 20), r)
	if v.ResolutionOn == nil || *v.ResolutionOn != cmd.AddDays(7*r.ResolutionWeeks) {
		t.Fatalf("resolution %+v", v.ResolutionOn)
	}
	if k := kinds(v); len(k) != 2 || k[0] != StepSuretyCommandement || k[1] != StepCourt || v.Next[1].DueOn != v.ResolutionOn.AddDays(1) {
		t.Errorf("after the commandement %v", k)
	}
	// A term begun before the 2023 law keeps the former delay in months.
	old := facts
	old.TermStartOn = d(2022, 1, 1)
	v, _ = Recovery(arr, steps, old, d(2026, 5, 20), r)
	if v.ResolutionOn == nil || *v.ResolutionOn != cmd.AddMonthsClamped(r.FormerResolutionMonths) {
		t.Errorf("former delay %+v", v.ResolutionOn)
	}

	// Steps of an earlier episode do not count; no surety, no insurer: only
	// the formal notice.
	early := []RecoveryStep{{Kind: StepFormalNotice, DoneOn: d(2025, 11, 20)}}
	v, _ = Recovery(arr, early, RecoveryFacts{TermStartOn: d(2025, 1, 1)}, today, r)
	if k := kinds(v); len(k) != 1 || k[0] != StepFormalNotice || len(v.Steps) != 1 {
		t.Errorf("earlier episode %v", k)
	}

	// Nothing overdue: no episode, the history kept.
	paid := Arrears(uuid.New(), calls[:1], today, 5)
	v, _ = Recovery(paid, steps, facts, today, r)
	if v.Since != nil || len(v.Next) != 0 || len(v.Steps) != len(steps) {
		t.Errorf("no arrears %+v", v)
	}
}
