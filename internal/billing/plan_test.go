package billing

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func moneyOf(c int64) money.Cents { return money.Cents(c) }

func planParams(t *testing.T) params.RecoveryPlan {
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
	r, err := ps.RecoveryPlan(d(2026, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewPlan(t *testing.T) {
	r := planParams(t)
	april, may := uuid.New(), uuid.New()
	calls := []Call{{BillingDocumentID: april, DueOn: d(2026, 4, 5), TotalCents: 80000, PaidCents: 20000},
		{BillingDocumentID: may, DueOn: d(2026, 5, 5), TotalCents: 80000}}
	today := d(2026, 5, 20)
	arr := Arrears(uuid.New(), calls, today, 5)
	ok := []Instalment{{DueOn: d(2026, 6, 10), AmountCents: 70000}, {DueOn: d(2026, 7, 10), AmountCents: 70000}}
	p, err := NewPlan(arr, today, today, ok, r)
	if err != nil || p.DebtCents != 140000 || len(p.Calls) != 2 {
		t.Fatalf("plan %+v %v", p, err)
	}
	for _, c := range []struct {
		name string
		on   period.Date
		in   []Instalment
		want string
	}{
		{"short of the debt", today, ok[:1], TypePlanInvalid},
		{"days not increasing", today, []Instalment{ok[1], ok[0]}, TypePlanInvalid},
		{"before the agreement", today, []Instalment{{DueOn: d(2026, 5, 19), AmountCents: 140000}}, TypePlanInvalid},
		{"agreed in the future", today.AddDays(1), ok, TypePlanInvalid},
		{"no instalment", today, nil, TypePlanInvalid},
	} {
		var ie *invariant.Error
		if _, err := NewPlan(arr, c.on, today, c.in, r); !errors.As(err, &ie) || ie.Type != c.want {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	paid := Arrears(uuid.New(), calls[:0], today, 5)
	var ie *invariant.Error
	if _, err := NewPlan(paid, today, today, ok, r); !errors.As(err, &ie) || ie.Type != TypeRecoveryNotDue {
		t.Errorf("nothing overdue: %v", err)
	}
}

func TestPlanStatus(t *testing.T) {
	r := planParams(t)
	april, may, june := uuid.New(), uuid.New(), uuid.New()
	p := Plan{AgreedOn: d(2026, 5, 20), DebtCents: 140000, Calls: []uuid.UUID{april, may},
		Instalments: []Instalment{{DueOn: d(2026, 6, 10), AmountCents: 70000}, {DueOn: d(2026, 7, 10), AmountCents: 70000}}}
	at := func(paidApril, paidMay, paidJune int64, today period.Date) PlanState {
		calls := []Call{{BillingDocumentID: april, DueOn: d(2026, 4, 5), TotalCents: 80000, PaidCents: 20000 + moneyOf(paidApril)},
			{BillingDocumentID: may, DueOn: d(2026, 5, 5), TotalCents: 80000, PaidCents: moneyOf(paidMay)},
			{BillingDocumentID: june, DueOn: d(2026, 6, 5), TotalCents: 80000, PaidCents: moneyOf(paidJune)}}
		return PlanStatus(p, Arrears(uuid.New(), calls, today, 5), today, r)
	}
	// Before the first instalment, the current rent paid: respected.
	if s := at(0, 0, 80000, d(2026, 6, 8)); s.Status != PlanRespected || s.NextInstalment == nil || s.NextInstalment.DueOn != d(2026, 6, 10) {
		t.Errorf("before %+v", s)
	}
	// Within the grace after the first instalment's day, nothing paid yet.
	if s := at(0, 0, 80000, d(2026, 6, 10).AddDays(r.GraceDays)); s.Status != PlanRespected {
		t.Errorf("within grace %+v", s)
	}
	// Past the grace, short of the first instalment: broken.
	if s := at(0, 0, 80000, d(2026, 6, 11).AddDays(r.GraceDays)); s.Status != PlanBroken || s.Reason != PlanLateInstalment {
		t.Errorf("late %+v", s)
	}
	// The first instalment paid, the current rent not: broken.
	if s := at(60000, 10000, 0, d(2026, 6, 20)); s.Status != PlanBroken || s.Reason != PlanCurrentUnpaid || s.PaidCents != 70000 {
		t.Errorf("current unpaid %+v", s)
	}
	// Everything covered paid: completed; cancelled stays cancelled.
	if s := at(60000, 80000, 80000, d(2026, 7, 1)); s.Status != PlanCompleted {
		t.Errorf("completed %+v", s)
	}
	c := p
	cancelled := d(2026, 6, 1)
	c.CancelledOn = &cancelled
	if s := PlanStatus(c, ArrearsView{}, d(2026, 6, 2), r); s.Status != PlanCancelled {
		t.Errorf("cancelled %+v", s)
	}
}
