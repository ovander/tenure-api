// Package inventory is the inventory engine of RENT-INV-001 (phase 2): the
// pure functions that seed an asset's layout from a template, pre-fill an
// inventory from the previous one, check its mandatory mentions, compare an
// exit with its entry, and compute deposit deductions and restitution
// deadlines. Nothing here does I/O: no database, clock, network or
// environment. The caller passes the dates, the parameter set and the rows.
// Amounts are integer cents and rates integer basis points (10 000 bp =
// 100 %), rounded through internal/money.
package inventory

import (
	"github.com/ovander/tenure-api/internal/period"
)

// Kind is an entry or exit inventory.
type Kind string

// Inventory kinds.
const (
	Entry Kind = "entry"
	Exit  Kind = "exit"
)

// Condition is the state of an item on the ordinal scale of RENT-INV-001 §4
// (INV-OD-04, proposed).
type Condition string

// The condition scale, best first.
const (
	New    Condition = "new"
	Good   Condition = "good"
	Fair   Condition = "fair"
	Poor   Condition = "poor"
	Broken Condition = "broken"
)

// rank orders the scale: higher is better; 0 for no condition.
func (c Condition) rank() int {
	switch c {
	case New:
		return 5
	case Good:
		return 4
	case Fair:
		return 3
	case Poor:
		return 2
	case Broken:
		return 1
	}
	return 0
}

// Valid reports whether c is on the scale.
func (c Condition) Valid() bool { return c.rank() > 0 }

// Works says whether an appliance, electrical or plumbing item works,
// recorded apart from its condition.
type Works string

// Works values.
const (
	WorksOK Works = "ok"
	WorksKO Works = "ko"
	WorksNA Works = "n/a"
)

// Category is what an element is (asset_elements.category).
type Category string

// Element categories.
const (
	Floor      Category = "floor"
	Walls      Category = "walls"
	Ceiling    Category = "ceiling"
	Joinery    Category = "joinery"
	Electrical Category = "electrical"
	Plumbing   Category = "plumbing"
	Heating    Category = "heating"
	Appliance  Category = "appliance"
	Furniture  Category = "furniture"
	Other      Category = "other"
)

// coverings are the three categories décret 2016-382 requires for every
// room: the state of floor, wall and ceiling coverings.
var coverings = []Category{Floor, Walls, Ceiling}

// Room is one room of an asset's layout, with its elements, or of an
// inventory, without them.
type Room struct {
	ID       string    `json:"id,omitempty"`
	AssetID  string    `json:"asset_id,omitempty"`
	Position int       `json:"position"`
	Kind     string    `json:"kind"`
	Label    string    `json:"label"`
	Elements []Element `json:"elements,omitempty"`
}

// Element is one element of a room in an asset's layout. InstalledOn, when
// known, gives the wear-and-tear age.
type Element struct {
	ID          string       `json:"id,omitempty"`
	RoomID      string       `json:"room_id,omitempty"`
	Position    int          `json:"position"`
	Category    Category     `json:"category"`
	Label       string       `json:"label"`
	Quantity    int          `json:"quantity,omitempty"`
	InstalledOn *period.Date `json:"installed_on,omitempty"`
}

// Item is one line of an inventory: an element of a room as found. Label is
// copied from the element so a later layout edit never changes an issued
// inventory. EntryItemID links an exit item to its entry item.
type Item struct {
	ID          string    `json:"id"`
	RoomID      string    `json:"room_id"`
	RoomLabel   string    `json:"room_label"`
	ElementID   string    `json:"element_id,omitempty"`
	Category    Category  `json:"category"`
	Label       string    `json:"label"`
	Condition   Condition `json:"condition,omitempty"`
	Works       Works     `json:"works,omitempty"`
	Quantity    int       `json:"quantity,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Photos      int       `json:"photos,omitempty"`
	EntryItemID string    `json:"entry_item_id,omitempty"`
	// ToCheck marks a pre-filled item not yet confirmed on site.
	ToCheck bool `json:"to_check,omitempty"`
}

// Key is one line of keys and access devices.
type Key struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
	Quantity    int    `json:"quantity"`
	Comment     string `json:"comment,omitempty"`
}

// Meter is one meter reading. Reading is a decimal string as read on the
// meter, never a float.
type Meter struct {
	Kind        string `json:"kind"`
	MeterNumber string `json:"meter_number,omitempty"`
	Reading     string `json:"reading,omitempty"`
	Unit        string `json:"unit,omitempty"`
}

// Party is who must sign (owner, each co-tenant, a representative), and
// whether they have.
type Party struct {
	Party  string `json:"party"`
	Name   string `json:"name"`
	Signed bool   `json:"signed"`
}

// Inventory is the content of an inventory as the engine sees it.
type Inventory struct {
	ID               string       `json:"id"`
	Kind             Kind         `json:"kind"`
	Furnished        bool         `json:"furnished,omitempty"`
	DoneOn           *period.Date `json:"done_on,omitempty"`
	KeysReturnedOn   *period.Date `json:"keys_returned_on,omitempty"`
	TenantNewAddress string       `json:"tenant_new_address,omitempty"`
	EntryDoneOn      *period.Date `json:"entry_done_on,omitempty"`
	Rooms            []Room       `json:"rooms"`
	Items            []Item       `json:"items"`
	Keys             []Key        `json:"keys"`
	Meters           []Meter      `json:"meters,omitempty"`
	Parties          []Party      `json:"parties,omitempty"`
}

// Completion is an accepted or refused line of a completion request on an
// issued entry (10-day or heating window): it corrects one entry item.
type Completion struct {
	EntryItemID string    `json:"entry_item_id"`
	Condition   Condition `json:"condition,omitempty"`
	Works       Works     `json:"works,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Accepted    bool      `json:"accepted"`
}
