package dashboard

import (
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func chart() params.LedgerChart {
	return params.LedgerChart{Accounts: map[string]params.LedgerAccount{
		ledger.RoleRent: {Code: "706000"}, ledger.RoleProvisions: {Code: "419800"}, ledger.RoleBank: {Code: "512000"}, ledger.RoleLoanInterest: {Code: "661100"}}}
}

func TestBuckets(t *testing.T) {
	b, total, err := buckets([]int{30, 60, 90}, []Overdue{{DaysLate: 0, OutstandingCents: 999}, {DaysLate: 30, OutstandingCents: 1}, {DaysLate: 31, OutstandingCents: 2},
		{DaysLate: 90, OutstandingCents: 4}, {DaysLate: 91, OutstandingCents: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if total != 15 || b[0].Cents != 1 || b[1].Cents != 2 || b[2].Cents != 4 || b[3].Cents != 8 || b[3].ToDays != nil || b[0].FromDays != 1 || b[1].FromDays != 31 {
		t.Fatalf("buckets %+v total %d", b, total)
	}
	if _, _, err := buckets([]int{60, 30}, nil); err == nil {
		t.Fatal("decreasing bounds")
	}
}

func TestYield(t *testing.T) {
	base := money.Cents(1_000_000)
	if y, _ := yield(50_000, &base, 365, 365); y == nil || *y != 500 {
		t.Fatalf("full year %v", y)
	}
	if y, _ := yield(25_000, &base, 182, 365); y == nil || *y != 501 {
		t.Fatalf("half year %v", y)
	}
	zero := money.Cents(0)
	for _, b := range []*money.Cents{nil, &zero} {
		if y, _ := yield(50_000, b, 365, 365); y != nil {
			t.Fatalf("no base %v", y)
		}
	}
	if y, _ := yield(50_000, &base, 0, 365); y != nil {
		t.Fatalf("no day %v", y)
	}
}

func TestBuildFutureAndChart(t *testing.T) {
	d, err := Build(nil, nil, chart(), []AssetInput{{AssetID: "A", Label: "A"}}, 2027, period.NewDate(2026, 6, 1), params.DashboardAging{BucketsDays: []int{30}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Months) != 0 || d.Assets[0].Days != 0 || d.Assets[0].GrossYieldBP != nil || d.Partial {
		t.Fatalf("future year %+v", d)
	}
	bad := chart()
	delete(bad.Accounts, ledger.RoleBank)
	if _, err := Build(nil, nil, bad, nil, 2026, period.NewDate(2026, 6, 1), params.DashboardAging{}); !errors.Is(err, ledger.ErrChart) {
		t.Fatalf("incomplete chart %v", err)
	}
}
