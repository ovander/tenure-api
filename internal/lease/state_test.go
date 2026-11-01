package lease

import (
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
)

// Every state × every action, exhaustively (RENT-BE-001 §10).
func TestNextExhaustive(t *testing.T) {
	want := map[State]map[Action]State{
		StateDraft:  {ActionActivate: StateActive},
		StateActive: {ActionNotice: StateNotice},
		StateNotice: {ActionEnd: StateEnded},
		StateEnded:  {ActionClose: StateClosed},
		StateClosed: {},
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
	if n != len(States)*len(Actions) || n != 20 {
		t.Errorf("covered %d pairs", n)
	}
	if _, err := Next("draft", "teleport"); !invariant.Is(err, TypeInvalidTransition) {
		t.Error("unknown action accepted")
	}
}
