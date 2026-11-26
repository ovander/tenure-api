package charge

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

func fixtures(t *testing.T) params.Set {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "params", "2025.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := params.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	// The fixtures start in 2025; these cases issue 2023 statements in 2024,
	// so the same value is back-dated for the test only.
	s, err = s.With(params.Entry{Key: params.KeyRegularisationSpread, EffectiveFrom: d(2014, 3, 27),
		Value: []byte("12"), Source: "test only: 2025 fixture value back-dated"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Int64(params.KeyRegularisationInstalments, d(2025, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.With(params.Entry{Key: params.KeyRegularisationInstalments, EffectiveFrom: d(2014, 3, 27),
		Value: []byte(fmt.Sprint(n)), Source: "test only: 2025 fixture value back-dated"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func d(y int, m time.Month, day int) period.Date { return period.NewDate(y, m, day) }

func mkLease(id string, start, end period.Date) lease.Lease {
	return lease.Lease{ID: id, State: lease.StateActive, StartOn: start, EndOn: end,
		Assets: []portfolio.Asset{{ID: "A2", Kind: portfolio.KindApartment}}}
}

// The 2023 exercise of A2: 195 000 recoverable (180 000 + TEOM 15 000).
func charges2023() []Charge {
	return []Charge{
		{ID: "c1", AssetID: "A2", Category: CategoryCoproStatement, AmountCents: 180000, ChargedOn: d(2023, 12, 31), RecoverableBP: 10000},
		{ID: "c2", AssetID: "A2", Category: CategoryCoproStatement, AmountCents: 60000, ChargedOn: d(2023, 12, 31), RecoverableBP: 0},
		{ID: "c3", AssetID: "A2", Category: CategoryTEOM, AmountCents: 15000, ChargedOn: d(2023, 10, 15), RecoverableBP: 10000},
		// Provisional calls are replaced by the statement: ignored here.
		{ID: "c4", AssetID: "A2", Category: CategoryCoproProvision, AmountCents: 50000, ChargedOn: d(2023, 1, 1), RecoverableBP: 6000},
	}
}

func TestAllocateWithVacancy(t *testing.T) {
	ex := Exercise{AssetID: "A2", Start: d(2023, 1, 1), End: d(2023, 12, 31), IssuedOn: d(2024, 3, 10)}
	leases := []lease.Lease{
		mkLease("new", d(2023, 6, 1), period.Date{}),
		mkLease("old", d(2022, 5, 1), d(2023, 4, 30)),
	}
	a, err := Allocate(charges2023(), leases, ex, map[string]money.Cents{"old": 48000, "new": 84000}, fixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.RecoverableCents != 195000 || a.NonRecoverableCents != 60000 || a.ExerciseDays != 365 || a.VacancyDays != 31 {
		t.Fatalf("totals %+v", a)
	}
	// floor(195000×120/365) = 64109, floor(195000×214/365) = 114328.
	want := []Line{
		{LeaseID: "old", Days: 120, AllocatedCents: 64109, ProvisionsCents: 48000, BalanceCents: 16109},
		{LeaseID: "new", Days: 214, AllocatedCents: 114328, ProvisionsCents: 84000, BalanceCents: 30328, NewProvisionCents: 16300},
	}
	for i, w := range want {
		g := a.Lines[i]
		if g.LeaseID != w.LeaseID || g.Days != w.Days || g.AllocatedCents != w.AllocatedCents ||
			g.BalanceCents != w.BalanceCents || g.NewProvisionCents != w.NewProvisionCents || g.Spread {
			t.Errorf("line %d = %+v; want %+v", i, g, w)
		}
	}
	if a.VacancyCents != 16563 || money.Sum(a.Lines[0].AllocatedCents, a.Lines[1].AllocatedCents, a.VacancyCents) != 195000 {
		t.Errorf("vacancy %d does not close the total", a.VacancyCents)
	}
}

func TestAllocateLateSpread(t *testing.T) {
	ps := fixtures(t)
	leases := []lease.Lease{mkLease("new", d(2023, 6, 1), period.Date{})}
	onTime := Exercise{AssetID: "A2", Start: d(2023, 1, 1), End: d(2023, 12, 31), IssuedOn: d(2024, 12, 31)}
	late := onTime
	late.IssuedOn = d(2025, 1, 1)
	a, err := Allocate(charges2023(), leases, onTime, map[string]money.Cents{"new": 84000}, ps)
	if err != nil || a.Lines[0].Spread {
		t.Fatalf("issued exactly 12 months after: spread %v, %v", a.Lines, err)
	}
	a, err = Allocate(charges2023(), leases, late, map[string]money.Cents{"new": 84000}, ps)
	if err != nil {
		t.Fatal(err)
	}
	n, err := ps.Int64(params.KeyRegularisationInstalments, late.IssuedOn)
	if err != nil {
		t.Fatal(err)
	}
	ln := a.Lines[0]
	if !ln.Spread || int64(len(ln.Instalments)) != n || money.Sum(ln.Instalments...) != ln.BalanceCents ||
		ln.Instalments[0] != 2527 || ln.Instalments[11] != 2531 {
		t.Errorf("spread %+v", ln)
	}
	credit, err := Allocate(charges2023(), leases, late, map[string]money.Cents{"new": 200000}, ps)
	if err != nil || credit.Lines[0].Spread || credit.Lines[0].BalanceCents >= 0 {
		t.Errorf("a credit is never spread: %+v, %v", credit.Lines, err)
	}
}

// An exercise ending on 29 February is late from 28 February of the year
// the spread delay ends in, not from 1 March (the month count is clamped).
func TestAllocateLateFromLeapDay(t *testing.T) {
	ps := fixtures(t)
	leases := []lease.Lease{mkLease("new", d(2023, 3, 1), period.Date{})}
	after, err := ps.Int64(params.KeyRegularisationSpread, d(2025, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	ex := Exercise{AssetID: "A2", Start: d(2023, 3, 1), End: d(2024, 2, 29)}
	limit := ex.End.AddMonthsClamped(int(after))
	ex.IssuedOn = limit.AddDays(1)
	a, err := Allocate(charges2023(), leases, ex, map[string]money.Cents{"new": 84000}, ps)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Lines[0].Spread {
		t.Errorf("issued on %s, the day after %s: not spread", ex.IssuedOn, limit)
	}
}

func TestAllocateNoVacancyLargestRemainder(t *testing.T) {
	ex := Exercise{AssetID: "A2", Start: d(2023, 1, 1), End: d(2023, 12, 31), IssuedOn: d(2024, 3, 1)}
	leases := []lease.Lease{
		mkLease("a", d(2022, 1, 1), d(2023, 4, 30)), // 120 days
		mkLease("b", d(2023, 5, 1), period.Date{}),  // 245 days
	}
	cs := []Charge{{ID: "c", AssetID: "A2", Category: CategoryRecoverableUtilities, AmountCents: 100, ChargedOn: d(2023, 6, 1), RecoverableBP: 10000}}
	a, err := Allocate(cs, leases, ex, nil, fixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	// 100 × 120/365 = 32.88, 100 × 245/365 = 67.12 → 33 and 67.
	if a.VacancyDays != 0 || a.VacancyCents != 0 || a.Lines[0].AllocatedCents != 33 || a.Lines[1].AllocatedCents != 67 {
		t.Errorf("got %+v", a)
	}
	if a.Lines[0].NewProvisionCents != 0 || a.Lines[1].NewProvisionCents != 100 { // 100 ÷ 12 → 9 cents → 1 €
		t.Errorf("provisions %d %d", a.Lines[0].NewProvisionCents, a.Lines[1].NewProvisionCents)
	}
}

func TestAllocateRejects(t *testing.T) {
	ps := fixtures(t)
	ex := Exercise{AssetID: "A2", Start: d(2023, 1, 1), End: d(2023, 12, 31), IssuedOn: d(2024, 3, 1)}
	other := []Charge{{ID: "x", AssetID: "B9", Category: CategoryTEOM, AmountCents: 1, ChargedOn: d(2023, 1, 1)}}
	outside := []Charge{{ID: "x", AssetID: "A2", Category: CategoryTEOM, AmountCents: 1, ChargedOn: d(2024, 1, 1)}}
	badShare := []Charge{{ID: "x", AssetID: "A2", Category: CategoryTEOM, AmountCents: 1, ChargedOn: d(2023, 1, 1), RecoverableBP: 10001}}
	overlap := []lease.Lease{mkLease("a", d(2023, 1, 1), period.Date{}), mkLease("b", d(2023, 6, 1), period.Date{})}
	for name, c := range map[string]struct {
		cs []Charge
		ls []lease.Lease
		ex Exercise
	}{
		"other asset":        {other, nil, ex},
		"outside exercise":   {outside, nil, ex},
		"share above 100 %":  {badShare, nil, ex},
		"overlapping leases": {nil, overlap, ex},
		"no issue date":      {nil, nil, Exercise{AssetID: "A2", Start: d(2023, 1, 1), End: d(2023, 12, 31)}},
	} {
		if _, err := Allocate(c.cs, c.ls, c.ex, nil, ps); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
