package lease

import (
	"testing"

	"github.com/ovander/tenure-api/internal/portfolio"
)

func TestStateBillable(t *testing.T) {
	want := map[State]bool{StateDraft: false, StateActive: true, StateNotice: true, StateEnded: false, StateClosed: false}
	for s, b := range want {
		if s.Billable() != b {
			t.Errorf("%s.Billable() = %v", s, !b)
		}
	}
}

func TestAssets(t *testing.T) {
	apt := portfolio.Asset{ID: "A", Kind: portfolio.KindApartment}
	box := portfolio.Asset{ID: "B", Kind: portfolio.KindBox, ParentAssetID: "A", ControlledZone: true}
	l := Lease{Assets: []portfolio.Asset{box, apt}}
	if p, ok := l.PrincipalAsset(); !ok || p.ID != "A" {
		t.Errorf("principal of apartment + box = %v", p.ID)
	}
	if !l.ControlledZone() || !l.Covers("B") || l.Covers("C") {
		t.Error("ControlledZone or Covers wrong")
	}
	if p, ok := (Lease{Assets: []portfolio.Asset{box}}).PrincipalAsset(); !ok || p.ID != "B" {
		t.Errorf("principal of a box alone = %v", p.ID)
	}
	if _, ok := (Lease{}).PrincipalAsset(); ok {
		t.Error("lease without assets has a principal asset")
	}
}
