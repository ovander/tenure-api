package lease

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

var zeroDate period.Date

func refs(n int, rent money.Cents) []Reference {
	out := make([]Reference, n)
	for i := range out {
		out[i] = Reference{Address: fmt.Sprintf("%d rue de l’Exemple, 69000 Lyon", i+1), RentCents: rent}
	}
	return out
}

func reevalParams(t *testing.T) params.Reevaluation {
	t.Helper()
	r, err := fixtures(t).Reevaluation(day("2026-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReevaluateRefusals(t *testing.T) {
	r := reevalParams(t)
	base := ReevaluationFacts{Kind: KindUnfurnished, StartOn: day("2024-01-01"), TermMonths: 36, RentCents: 80000}
	ok := ReevaluationInput{ProposedRentCents: 84000, References: refs(r.MinReferences, 90000)}
	today := day("2026-03-01")
	with := func(m func(*ReevaluationFacts, *ReevaluationInput)) (ReevaluationFacts, ReevaluationInput) {
		f, in := base, ok
		in.References = append([]Reference(nil), ok.References...)
		m(&f, &in)
		return f, in
	}
	for _, c := range []struct {
		name     string
		mod      func(*ReevaluationFacts, *ReevaluationInput)
		received string
		want     string
	}{
		{"furnished", func(f *ReevaluationFacts, _ *ReevaluationInput) { f.Kind = KindFurnished }, "", TypeReevaluationNotAllowed},
		{"ended", func(f *ReevaluationFacts, _ *ReevaluationInput) { f.EndOn = dp("2026-12-31") }, "", TypeReevaluationNotAllowed},
		{"controlled", func(f *ReevaluationFacts, _ *ReevaluationInput) { f.ControlledZone = true }, "", TypeReevaluationControlled},
		{"few references", func(_ *ReevaluationFacts, in *ReevaluationInput) { in.References = in.References[1:] }, "", TypeReevaluationInvalid},
		{"large agglomeration", func(_ *ReevaluationFacts, in *ReevaluationInput) { in.LargeAgglomeration = true }, "", TypeReevaluationInvalid},
		{"blank address", func(_ *ReevaluationFacts, in *ReevaluationInput) { in.References[0].Address = " " }, "", TypeReevaluationInvalid},
		{"not an increase", func(_ *ReevaluationFacts, in *ReevaluationInput) { in.ProposedRentCents = 80000 }, "", TypeReevaluationInvalid},
		{"too late", func(*ReevaluationFacts, *ReevaluationInput) {}, "2026-07-01", TypeReevaluationTooLate},
		{"tense zone above cap", func(f *ReevaluationFacts, in *ReevaluationInput) { f.TenseZone = true; in.ProposedRentCents = 86000 }, "", TypeReevaluationAboveCap},
		{"tense zone references below", func(f *ReevaluationFacts, in *ReevaluationInput) {
			f.TenseZone = true
			in.References = refs(r.MinReferences, 79000)
		}, "", TypeReevaluationAboveCap},
	} {
		f, in := with(c.mod)
		received := zeroDate
		if c.received != "" {
			received = day(c.received)
		}
		_, err := Reevaluate(f, in, received, today, r)
		var ie *invariant.Error
		if !errors.As(err, &ie) || ie.Type != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
}

func TestReevaluateSchedule(t *testing.T) {
	r := reevalParams(t)
	f := ReevaluationFacts{Kind: KindUnfurnished, StartOn: day("2024-01-01"), TermMonths: 36, RentCents: 80000}
	in := ReevaluationInput{ProposedRentCents: 84001, References: refs(r.MinReferences, 90000)}
	got, err := Reevaluate(f, in, zeroDate, day("2026-03-01"), r)
	if err != nil {
		t.Fatal(err)
	}
	if got.RenewalOn != day("2027-01-01") || got.LatestReceiptOn != day("2026-06-30") || len(got.Steps) != r.StepsShort {
		t.Fatalf("short schedule %+v", got)
	}
	if last := got.Steps[len(got.Steps)-1]; last.RentCents != in.ProposedRentCents || got.Steps[1].From != day("2028-01-01") {
		t.Errorf("steps %+v", got.Steps)
	}
	for i := 1; i < len(got.Steps); i++ {
		if got.Steps[i].RentCents <= got.Steps[i-1].RentCents {
			t.Errorf("steps not increasing %+v", got.Steps)
		}
	}
	// Past the lead time, with no reception day, the proposal aims at the
	// next renewal.
	got, err = Reevaluate(f, in, zeroDate, day("2026-08-01"), r)
	if err != nil || got.RenewalOn != day("2030-01-01") {
		t.Errorf("next renewal %+v %v", got, err)
	}
	// An increase above the threshold, or a long term, spreads in sixths.
	big := in
	big.ProposedRentCents = f.RentCents + money.Cents(int64(f.RentCents)*(r.IncreaseThresholdBP+1)/10000) + 1
	if got, err = Reevaluate(f, big, zeroDate, day("2026-03-01"), r); err != nil || len(got.Steps) != r.StepsLong {
		t.Errorf("above threshold %+v %v", got, err)
	}
	long := f
	long.TermMonths = r.LongTermMonths
	if got, err = Reevaluate(long, in, zeroDate, day("2026-03-01"), r); err != nil || len(got.Steps) != r.StepsLong {
		t.Errorf("long term %+v %v", got, err)
	}
	// In a zone tendue the proposal may reach the cap, not beyond.
	tense := f
	tense.TenseZone = true
	got, err = Reevaluate(tense, ReevaluationInput{ProposedRentCents: 85000, References: refs(r.MinReferences, 90000)}, zeroDate, day("2026-03-01"), r)
	if err != nil || got.CapCents == nil || *got.CapCents < 85000 {
		t.Errorf("tense zone at cap %+v %v", got, err)
	}
}
