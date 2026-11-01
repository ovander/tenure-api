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

// Asset is a rentable unit.
type Asset struct {
	ID             string    `json:"id"`
	Kind           AssetKind `json:"kind"`
	Furnished      bool      `json:"furnished"`
	ControlledZone bool      `json:"controlled_zone"`
	// ParentAssetID links a box to the dwelling it is let with.
	ParentAssetID string `json:"parent_asset_id,omitempty"`
}

// Dwelling reports whether the asset is a dwelling (house or apartment).
func (a Asset) Dwelling() bool { return a.Kind == KindHouse || a.Kind == KindApartment }
