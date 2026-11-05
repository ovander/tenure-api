package inventory

import (
	"fmt"
	"sort"
	"strconv"
)

// Template is a system or owner template for an asset kind (RENT-INV-001 §4,
// inventory_templates). Its rooms are seeded in order; Furnished adds the
// furniture of a furnished dwelling (décret 2015-981) to the rooms of each
// kind. Labels are French: they are printed on the inventory.
type Template struct {
	AssetKind string                       `json:"asset_kind"`
	Version   int                          `json:"version"`
	Rooms     []TemplateRoom               `json:"rooms"`
	Furnished map[string][]TemplateElement `json:"furnished,omitempty"`
}

// TemplateRoom is a room of a template. Repeat "bedroom" seeds one room per
// bedroom of the asset, numbered; When names an asset feature the room needs
// ("cellar", "garage", "garden").
type TemplateRoom struct {
	Kind     string            `json:"kind"`
	Label    string            `json:"label"`
	Repeat   string            `json:"repeat,omitempty"`
	When     string            `json:"when,omitempty"`
	Elements []TemplateElement `json:"elements"`
}

// TemplateElement is an element of a template room. A furnished element
// that Replaces the label of a base element takes its place (the hob of a
// furnished kitchen is the décret's « plaques de cuisson », not a second one).
type TemplateElement struct {
	Category Category `json:"category"`
	Label    string   `json:"label"`
	Quantity int      `json:"quantity,omitempty"`
	Replaces string   `json:"replaces,omitempty"`
}

// furnish adds extra to base, each extra element replacing the base element
// it names, in place.
func furnish(base, extra []TemplateElement) []TemplateElement {
	out := append([]TemplateElement{}, base...)
	for _, e := range extra {
		done := false
		if e.Replaces != "" {
			for i := range out {
				if out[i].Label == e.Replaces {
					out[i] = TemplateElement{Category: e.Category, Label: e.Label, Quantity: e.Quantity}
					done = true
					break
				}
			}
		}
		if !done {
			out = append(out, TemplateElement{Category: e.Category, Label: e.Label, Quantity: e.Quantity})
		}
	}
	return out
}

// Asset is what Seed needs to know of an asset.
type Asset struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Bedrooms  int             `json:"bedrooms"`
	Features  map[string]bool `json:"features,omitempty"`
	Furnished bool            `json:"furnished,omitempty"`
}

// Seed builds an asset's layout from a template of its kind: rooms in the
// template's order, positions from 1, bedrooms repeated and numbered, rooms
// that need an absent feature left out, and the furniture added when the
// asset is let furnished; the furniture of a room kind the asset lacks (the
// bedroom of a studio) goes to its living room. The owner then edits the
// layout once.
func Seed(t Template, a Asset) ([]Room, error) {
	if t.AssetKind != a.Kind {
		return nil, fmt.Errorf("inventory: template for %s, asset is %s", t.AssetKind, a.Kind)
	}
	if a.Bedrooms < 0 {
		return nil, fmt.Errorf("inventory: negative bedroom count")
	}
	seeded := map[string]bool{}
	for _, tr := range t.Rooms {
		if (tr.When == "" || a.Features[tr.When]) && (tr.Repeat != "bedroom" || a.Bedrooms > 0) {
			seeded[tr.Kind] = true
		}
	}
	var out []Room
	add := func(tr TemplateRoom, label string) {
		r := Room{AssetID: a.ID, Position: len(out) + 1, Kind: tr.Kind, Label: label}
		els := tr.Elements
		if a.Furnished {
			els = furnish(els, t.Furnished[tr.Kind])
			if tr.Kind == "living" {
				kinds := make([]string, 0, len(t.Furnished))
				for k := range t.Furnished {
					kinds = append(kinds, k)
				}
				sort.Strings(kinds)
				for _, k := range kinds {
					if !seeded[k] {
						els = furnish(els, t.Furnished[k])
					}
				}
			}
		}
		for i, e := range els {
			if e.Label == "" || e.Category == "" {
				continue
			}
			r.Elements = append(r.Elements, Element{Position: i + 1, Category: e.Category, Label: e.Label, Quantity: e.Quantity})
		}
		out = append(out, r)
	}
	for _, tr := range t.Rooms {
		if tr.When != "" && !a.Features[tr.When] {
			continue
		}
		switch tr.Repeat {
		case "":
			add(tr, tr.Label)
		case "bedroom":
			for i := 1; i <= a.Bedrooms; i++ {
				label := tr.Label
				if a.Bedrooms > 1 {
					label += " " + strconv.Itoa(i)
				}
				add(tr, label)
			}
		default:
			return nil, fmt.Errorf("inventory: unknown repeat %q", tr.Repeat)
		}
	}
	return out, nil
}

// Draft builds the items of a new entry inventory from the layouts of the
// lease's assets: a box let with a dwelling is a room block of the same
// inventory, never a document of its own. Item and room ids are derived from
// the element and room ids the caller gives the layout.
func Draft(id string, furnished bool, layouts ...[]Room) Inventory {
	inv := Inventory{ID: id, Kind: Entry, Furnished: furnished, Rooms: []Room{}, Items: []Item{}, Keys: []Key{}}
	for _, rooms := range layouts {
		for _, r := range rooms {
			room := Room{ID: r.ID, AssetID: r.AssetID, Position: len(inv.Rooms) + 1, Kind: r.Kind, Label: r.Label}
			inv.Rooms = append(inv.Rooms, room)
			for _, e := range r.Elements {
				inv.Items = append(inv.Items, Item{ID: id + "/" + e.ID, RoomID: r.ID, RoomLabel: r.Label, ElementID: e.ID,
					Category: e.Category, Label: e.Label, Quantity: e.Quantity})
			}
		}
	}
	return inv
}
