package maintenance

import (
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
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
