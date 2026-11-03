// Package portfolio owns the Owner, Asset, Tenant and ownership-share model
// (RENT-SPEC-002, RENT-BE-001 §1). Slice 1 holds the pure domain types the
// calculation engine reads; repositories and handlers arrive in slice 2.
package portfolio

// AssetKind is the immutable kind of an asset.
type AssetKind string

// Asset kinds (RENT-SPEC-001).
const (
	KindHouse     AssetKind = "house"
	KindApartment AssetKind = "apartment"
	KindBox       AssetKind = "box"
)

// Share is one owner's share of an asset, in basis points (10000 = 100 %).
type Share struct {
	OwnerID string `json:"owner_id"`
	BP      int64  `json:"bp"`
}

// Asset is a rentable unit.
type Asset struct {
	ID        string    `json:"id"`
	Kind      AssetKind `json:"kind"`
	Furnished bool      `json:"furnished"`
	// SurfaceHundredthsM2 is the living surface in hundredths of a square
	// metre (45,30 m² → 4530).
	SurfaceHundredthsM2 int64 `json:"surface_hundredths_m2,omitempty"`
	// ControlledZone puts the asset under a rent ceiling; RentControlZone and
	// RentControlCategory key its row in rent_control.zone_ceiling.
	ControlledZone bool `json:"controlled_zone"`
	// TenseZone puts the asset in a zone tendue, where the relocation rule
	// applies (Q-102). A controlled asset is always tense: use Tense.
	TenseZone           bool   `json:"tense_zone"`
	RentControlZone     string `json:"rent_control_zone,omitempty"`
	RentControlCategory string `json:"rent_control_category,omitempty"`
	// ParentAssetID links a box to the dwelling it is let with.
	ParentAssetID string  `json:"parent_asset_id,omitempty"`
	Shares        []Share `json:"shares,omitempty"`
}

// Tense reports whether the asset is in a zone tendue: set as such, or under
// a rent ceiling, since every rent-ceiling zone is a tense area (Q-102).
func (a Asset) Tense() bool { return a.TenseZone || a.ControlledZone }

// Dwelling reports whether the asset is a dwelling (house or apartment).
func (a Asset) Dwelling() bool { return a.Kind == KindHouse || a.Kind == KindApartment }
