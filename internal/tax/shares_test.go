package tax

import (
	"testing"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
)

func TestWithShares(t *testing.T) {
	ps := fixtures(t)
	threshold, err := ps.Int64(params.KeyMicroFoncierThreshold, params.TaxYearEnd(2025))
	if err != nil {
		t.Fatal(err)
	}
	globalCap, err := ps.Int64(params.KeyDeficitGlobalCap, params.TaxYearEnd(2025))
	if err != nil {
		t.Fatal(err)
	}
	own := func(income, interest, other money.Cents, furnished bool) Summary {
		return Summary{Year: 2025, Owners: []OwnerSummary{{OwnerID: "p", Assets: []AssetLines{{AssetID: "a", Furnished: furnished}},
			Reel: Reel{IncomeCents: income, InterestCents: interest, OtherExpensesCents: other, NetCents: income - interest - other}}}}
	}
	share := func(income, interest, other money.Cents) SCIShare {
		return SCIShare{SCIName: "SCI", Parts: 1, TotalParts: 2, IncomeCents: income, InterestCents: interest, OtherExpensesCents: other,
			NetCents: income - interest - other}
	}

	// No share: the summary is unchanged.
	s := own(1000, 0, 0, false)
	if got, err := WithShares(s, "p", nil, nil, 2025, ps, nil); err != nil || got.Owners[0].Reel.NetCents != 1000 || got.Owners[0].SCIShares != nil {
		t.Fatalf("no share: %+v %v", got, err)
	}

	// A direct unfurnished asset and a profitable share: both in the réel;
	// the micro open while the gross stays under the threshold.
	half := money.Cents(threshold / 2)
	got, err := WithShares(own(half, 0, 100, false), "p", []SCIShare{share(half, 50, 25)}, nil, 2025, ps, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := got.Owners[0]
	if o.Reel.IncomeCents != 2*half || o.Reel.InterestCents != 50 || o.Reel.OtherExpensesCents != 125 || o.Reel.NetCents != 2*half-175 ||
		o.Reel.TaxableCents != 2*half-175 || len(o.SCIShares) != 1 {
		t.Errorf("réel %+v", o.Reel)
	}
	if !o.Micro.Eligible || o.Micro.GrossCents != 2*half {
		t.Errorf("micro with a direct asset %+v", o.Micro)
	}
	// Over the threshold with the share: the micro closes.
	got, _ = WithShares(own(half, 0, 0, false), "p", []SCIShare{share(half+1000, 0, 0)}, nil, 2025, ps, nil)
	if got.Owners[0].Micro.Eligible {
		t.Errorf("over the threshold: %+v", got.Owners[0].Micro)
	}

	// Shares only: a summary of its own, and no micro.
	got, err = WithShares(Summary{Year: 2025}, "q", []SCIShare{share(1000, 0, 0)}, nil, 2025, ps, nil)
	if err != nil || len(got.Owners) != 1 || got.Owners[0].OwnerID != "q" || got.Owners[0].Micro.Eligible || got.Owners[0].Reel.NetCents != 1000 {
		t.Errorf("shares only %+v %v", got, err)
	}
	// A furnished asset is not a direct unfurnished one.
	got, _ = WithShares(own(0, 0, 0, true), "p", []SCIShare{share(1000, 0, 0)}, nil, 2025, ps, nil)
	if got.Owners[0].Micro.Eligible {
		t.Errorf("a furnished asset only: %+v", got.Owners[0].Micro)
	}

	// A share in deficit: the owner's own deficit rules on the whole, the
	// interest carried forward, the rest against global income up to the cap.
	got, err = WithShares(own(1000, 0, 0, false), "p", []SCIShare{share(0, 3000, money.Cents(globalCap)+500)}, nil, 2025, ps,
		map[string][]Deficit{"p": {{Year: 2023, RemainingCents: 700}}})
	if err != nil {
		t.Fatal(err)
	}
	r := got.Owners[0].Reel
	if r.NetCents != 1000-3000-money.Cents(globalCap)-500 || r.GlobalImputableCents != money.Cents(globalCap) {
		t.Errorf("deficit %+v", r)
	}
	var carried money.Cents
	for _, d := range r.CarriedForward {
		carried += d.RemainingCents
	}
	// interest beyond income (2000) and what passes the cap (500) carry
	// forward with the prior deficit of 2023 (700).
	if carried != 2000+500+700 {
		t.Errorf("carried %d %+v", carried, r.CarriedForward)
	}
}

func TestWithSharesPartsLoans(t *testing.T) {
	ps := fixtures(t)
	sh := []SCIShare{{SCIName: "SCI", Parts: 1, TotalParts: 2, IncomeCents: 5000, NetCents: 5000}}
	loans := []PartsLoan{{LoanID: "l1", Label: "Prêt parts", SCIName: "SCI", InterestCents: 1200, InsuranceCents: 100}}
	got, err := WithShares(Summary{Year: 2025}, "p", sh, loans, 2025, ps, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Owners[0].Reel
	if r.InterestCents != 1300 || r.NetCents != 3700 || len(got.Owners[0].PartsLoans) != 1 {
		t.Errorf("a loan for parts with its share %+v", r)
	}
	// The loan's interest beyond the share's income is carried forward, as
	// a loan's interest is.
	got, err = WithShares(Summary{Year: 2025}, "p", nil, []PartsLoan{{LoanID: "l1", InterestCents: 900}}, 2025, ps, nil)
	if err != nil {
		t.Fatal(err)
	}
	r = got.Owners[0].Reel
	if r.NetCents != -900 || r.GlobalImputableCents != 0 || len(r.CarriedForward) != 1 || r.CarriedForward[0].RemainingCents != 900 {
		t.Errorf("a loan for parts without income %+v", r)
	}
}
