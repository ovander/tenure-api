package maintenance

import (
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
)

// TestNextExhaustive checks every status against every action.
func TestNextExhaustive(t *testing.T) {
	want := map[Status]map[Action]Status{
		StatusNew:       {ActionQuote: StatusQuoted, ActionSchedule: StatusScheduled, ActionCancel: StatusCancelled},
		StatusQuoted:    {ActionQuote: StatusQuoted, ActionSchedule: StatusScheduled, ActionCancel: StatusCancelled},
		StatusScheduled: {ActionSchedule: StatusScheduled, ActionDone: StatusDone, ActionCancel: StatusCancelled},
		StatusDone:      {ActionInvoice: StatusInvoiced},
		StatusInvoiced:  {},
		StatusCancelled: {},
	}
	for _, s := range Statuses {
		for _, a := range Actions {
			got, err := Next(s, a)
			to, ok := want[s][a]
			if ok {
				if err != nil || got != to {
					t.Errorf("%s from %s = %q, %v; want %q", a, s, got, err, to)
				}
				continue
			}
			var inv *invariant.Error
			if !errors.As(err, &inv) || inv.Type != TypeTransition {
				t.Errorf("%s from %s = %q, %v; want refused", a, s, got, err)
			}
		}
	}
}

func TestEditable(t *testing.T) {
	for _, s := range Statuses {
		want := s == StatusNew || s == StatusQuoted || s == StatusScheduled
		if Editable(s) != want {
			t.Errorf("Editable(%s) = %v", s, !want)
		}
	}
}

func TestDeductible(t *testing.T) {
	// A made-up rule: the kinds come from the parameter, not the code.
	rule := params.ImprovementDeductible{AssetKinds: []string{"apartment"}}
	cases := []struct {
		treatment, kind string
		want            bool
	}{
		{TreatmentRepair, "box", true},
		{TreatmentRepair, "apartment", true},
		{TreatmentImprovement, "apartment", true},
		{TreatmentImprovement, "house", false},
		{TreatmentImprovement, "box", false},
		{TreatmentConstruction, "house", false},
		{TreatmentConstruction, "box", false},
		{"unknown", "house", false},
	}
	for _, c := range cases {
		if got := Deductible(c.treatment, c.kind, rule); got != c.want {
			t.Errorf("%s on %s: %v", c.treatment, c.kind, got)
		}
	}
}
