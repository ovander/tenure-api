package charge

import (
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
)

// Every state × every action, exhaustively (RENT-BE-001 §10).
func TestNextExhaustive(t *testing.T) {
	want := map[State]map[Action]State{
		StateDraft:     {ActionAllocate: StateAllocated},
		StateAllocated: {ActionAllocate: StateAllocated, ActionIssue: StateIssued},
		StateIssued:    {ActionSettle: StateSettled, ActionCancel: StateCancelled},
		StateSettled:   {ActionCancel: StateCancelled},
		StateCancelled: {},
	}
	n := 0
	for _, s := range States {
		for _, a := range Actions {
			n++
			got, err := Next(s, a)
			if to, ok := want[s][a]; ok {
				if err != nil || got != to {
					t.Errorf("%s + %s = %s, %v; want %s", s, a, got, err, to)
				}
				continue
			}
			if !invariant.Is(err, TypeInvalidTransition) || got != "" {
				t.Errorf("%s + %s = %s, %v; want %s", s, a, got, err, TypeInvalidTransition)
			}
		}
	}
	if n != len(States)*len(Actions) {
		t.Fatal("not exhaustive")
	}
}

func TestProvisionsCollected(t *testing.T) {
	cases := []struct {
		name  string
		calls []PaidCall
		want  money.Cents
	}{
		{"fully paid", []PaidCall{{TotalCents: 90000, ProvisionCents: 10000, MatchedCents: 90000}}, 10000},
		{"unpaid call counts nothing", []PaidCall{{TotalCents: 90000, ProvisionCents: 10000}}, 0},
		{"half paid, in proportion", []PaidCall{{TotalCents: 90000, ProvisionCents: 10000, MatchedCents: 45000}}, 5000},
		// 10000 × 1 / 3 = 3333,33… → 3334: rounding favours the tenant.
		{"rounded up", []PaidCall{{TotalCents: 30000, ProvisionCents: 10000, MatchedCents: 10000}}, 3334},
		{"never above the provision line", []PaidCall{{TotalCents: 90000, ProvisionCents: 10000, MatchedCents: 95000}}, 10000},
		{"a call without provision", []PaidCall{{TotalCents: 80000, MatchedCents: 80000}}, 0},
		{"rent calls, not calls issued: two of twelve paid", append(make([]PaidCall, 0, 12),
			PaidCall{TotalCents: 90000, ProvisionCents: 10000, MatchedCents: 90000},
			PaidCall{TotalCents: 90000, ProvisionCents: 10000, MatchedCents: 90000},
			PaidCall{TotalCents: 90000, ProvisionCents: 10000}), 20000},
	}
	for _, c := range cases {
		got, err := ProvisionsCollected(c.calls)
		if err != nil || got != c.want {
			t.Errorf("%s: %d, %v; want %d", c.name, got, err, c.want)
		}
	}
}
