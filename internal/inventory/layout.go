package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the layout routes.
const (
	TypeLayoutExists = "inventory/layout-exists"
	TypeNoTemplate   = "inventory/no-template"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeLayoutExists: problem.M(409, "Ce bien a déjà un plan de pièces : modifiez-le plutôt que de le recréer.", "This asset already has a layout: edit it instead of seeding it again."),
		TypeNoTemplate:   problem.M(409, "Aucun modèle d’état des lieux n’existe pour ce type de bien.", "No inventory template exists for this kind of asset."),
	})
}

// Product limits of a layout (not legal values): enough for any dwelling,
// small enough to keep a PUT bounded.
const (
	maxRooms           = 60
	maxElementsPerRoom = 80
)

// The room kinds and element categories the schema accepts.
var (
	roomKinds  = []string{"entry", "living", "kitchen", "bedroom", "bathroom", "wc", "storage", "outside", "garage", "box", "other"}
	categories = []Category{Floor, Walls, Ceiling, Joinery, Electrical, Plumbing, Heating, Appliance, Furniture, Other}
)

// Service serves the layouts and templates (RENT-INV-001 §4, §7).
type Service struct {
	DB        *db.DB
	Repo      Repository
	Portfolio portfolio.Service
	Photos    PhotoStore
	Now       func() time.Time
	NewID     func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

// scoped refuses a caller without a scope, and an accountant: the layout and
// the inventories are not theirs to read (RENT-INV-001 §8).
func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil || p.Active.Role == auth.RoleAccountant {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// ElementView is an element of a room as the API returns it.
type ElementView struct {
	ID          uuid.UUID    `json:"id"`
	Position    int          `json:"position"`
	Category    Category     `json:"category" enums:"floor,walls,ceiling,joinery,electrical,plumbing,heating,appliance,furniture,other"`
	Label       string       `json:"label"`
	Quantity    int          `json:"quantity"`
	InstalledOn *period.Date `json:"installed_on,omitempty" swaggertype:"string" format:"date"`
	Version     int          `json:"version"`
}

// RoomView is a room of the layout with its elements.
type RoomView struct {
	ID                  uuid.UUID     `json:"id"`
	Position            int           `json:"position"`
	Kind                string        `json:"kind" enums:"entry,living,kitchen,bedroom,bathroom,wc,storage,outside,garage,box,other"`
	Label               string        `json:"label"`
	SurfaceHundredthsM2 *int64        `json:"surface_hundredths_m2,omitempty"`
	Version             int           `json:"version"`
	Elements            []ElementView `json:"elements"`
}

// LayoutView is an asset's layout. Version 0: nothing seeded or saved yet.
type LayoutView struct {
	AssetID uuid.UUID  `json:"asset_id"`
	Version int        `json:"version"`
	Rooms   []RoomView `json:"rooms"`
}

// ElementInput is an element of a layout PUT; ID names an existing element,
// absent for a new one.
type ElementInput struct {
	ID          *uuid.UUID   `json:"id,omitempty"`
	Category    Category     `json:"category"`
	Label       string       `json:"label"`
	Quantity    int          `json:"quantity"`
	InstalledOn *period.Date `json:"installed_on,omitempty" swaggertype:"string" format:"date"`
}

// RoomInput is a room of a layout PUT, in its new position.
type RoomInput struct {
	ID                  *uuid.UUID     `json:"id,omitempty"`
	Kind                string         `json:"kind"`
	Label               string         `json:"label"`
	SurfaceHundredthsM2 *int64         `json:"surface_hundredths_m2,omitempty"`
	Elements            []ElementInput `json:"elements"`
}

// LayoutInput is the body of PUT /assets/{id}/layout: the whole layout in
// order. A room or element left out is removed; an inventory that copied it
// keeps its own label.
type LayoutInput struct {
	Rooms []RoomInput `json:"rooms"`
}

// SeedInput is the body of POST /assets/{id}/layout/seed: what the template
// needs to know of the asset. Furnished defaults to the asset's own flag.
type SeedInput struct {
	Bedrooms  int             `json:"bedrooms"`
	Features  map[string]bool `json:"features,omitempty"`
	Furnished *bool           `json:"furnished,omitempty"`
}

// TemplateView is an inventory template the scope can read.
type TemplateView struct {
	ID        uuid.UUID       `json:"id"`
	AssetKind string          `json:"asset_kind" enums:"house,apartment,box"`
	Version   int             `json:"version"`
	System    bool            `json:"system"`
	Source    string          `json:"source,omitempty"`
	Structure json.RawMessage `json:"structure" swaggertype:"object"`
}

func view(assetID uuid.UUID, l layoutRow, found bool, rooms []roomRow, els []elementRow) LayoutView {
	out := LayoutView{AssetID: assetID, Rooms: []RoomView{}}
	if found {
		out.Version = l.Version
	}
	byRoom := map[uuid.UUID][]ElementView{}
	for _, e := range els {
		ev := ElementView{ID: e.ID, Position: e.Position, Category: Category(e.Category), Label: e.Label, Quantity: e.Quantity, Version: e.Version}
		if e.InstalledOn != nil {
			d := period.FromTime(*e.InstalledOn)
			ev.InstalledOn = &d
		}
		byRoom[e.RoomID] = append(byRoom[e.RoomID], ev)
	}
	for _, r := range rooms {
		rv := RoomView{ID: r.ID, Position: r.Position, Kind: r.Kind, Label: r.Label, SurfaceHundredthsM2: r.Surface, Version: r.Version,
			Elements: byRoom[r.ID]}
		if rv.Elements == nil {
			rv.Elements = []ElementView{}
		}
		out.Rooms = append(out.Rooms, rv)
	}
	return out
}

// Layout returns an asset's layout.
func (s Service) Layout(ctx context.Context, p *auth.Principal, requestID string, assetID uuid.UUID) (LayoutView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return LayoutView{}, err
	}
	var out LayoutView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Portfolio.AssetInTx(ctx, assetID); err != nil {
			return err
		}
		l, found, rooms, els, err := s.Repo.Layout(ctx, assetID)
		out = view(assetID, l, found, rooms, els)
		return err
	})
	return out, err
}

func contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// validate checks a layout PUT field by field; today bounds installed_on.
func validate(in LayoutInput, today period.Date) invariant.Fields {
	var f invariant.Fields
	if len(in.Rooms) > maxRooms {
		f.Add("rooms", fmt.Sprintf("Au plus %d pièces.", maxRooms))
	}
	for i, r := range in.Rooms {
		at := fmt.Sprintf("rooms[%d]", i)
		if !contains(roomKinds, r.Kind) {
			f.Add(at+".kind", "Type de pièce inconnu.")
		}
		if strings.TrimSpace(r.Label) == "" {
			f.Add(at+".label", "Le nom de la pièce est obligatoire.")
		}
		if r.SurfaceHundredthsM2 != nil && *r.SurfaceHundredthsM2 <= 0 {
			f.Add(at+".surface_hundredths_m2", "La surface doit être positive.")
		}
		if len(r.Elements) > maxElementsPerRoom {
			f.Add(at+".elements", fmt.Sprintf("Au plus %d éléments par pièce.", maxElementsPerRoom))
		}
		for j, e := range r.Elements {
			eat := fmt.Sprintf("%s.elements[%d]", at, j)
			if !contains(categories, e.Category) {
				f.Add(eat+".category", "Catégorie inconnue.")
			}
			if strings.TrimSpace(e.Label) == "" {
				f.Add(eat+".label", "Le nom de l’élément est obligatoire.")
			}
			if e.Quantity < 0 {
				f.Add(eat+".quantity", "La quantité ne peut pas être négative.")
			}
			if e.InstalledOn != nil && e.InstalledOn.After(today) {
				f.Add(eat+".installed_on", "La date de pose est dans le futur.")
			}
		}
	}
	return f
}

// SaveLayout replaces an asset's layout with in, checked against version
// (If-Match; 0 for an asset without a layout). Rooms and elements keep their
// ids, so the items of past inventories stay linked to them; an id that is not
// of this asset is refused.
func (s Service) SaveLayout(ctx context.Context, p *auth.Principal, requestID string, assetID uuid.UUID, version int, in LayoutInput) (LayoutView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return LayoutView{}, err
	}
	today := period.FromTime(s.Now().In(paris()))
	if f := validate(in, today); len(f) > 0 {
		return LayoutView{}, f
	}
	var out LayoutView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Portfolio.AssetInTx(ctx, assetID); err != nil {
			return err
		}
		l, found, rooms, els, err := s.Repo.Layout(ctx, assetID)
		if err != nil {
			return err
		}
		before := view(assetID, l, found, rooms, els)
		if before.Version != version {
			return invariant.ErrStale
		}
		out, err = s.apply(ctx, sc.OwnerID, p.Sub, assetID, rooms, els, in)
		if err != nil {
			return err
		}
		if err := s.Repo.BumpLayout(ctx, layoutRow{AssetID: assetID, OwnerID: sc.OwnerID, CreatedAt: s.now(), CreatedBy: p.Sub,
			UpdatedAt: s.now(), UpdatedBy: p.Sub}, version); err != nil {
			return err
		}
		out.Version = version + 1
		return audit.Record(ctx, "layout.update", audit.Entity{Type: "asset", ID: assetID.String()}, before, out)
	})
	return out, err
}

// apply diffs in against the stored rooms and elements and writes the change.
func (s Service) apply(ctx context.Context, owner uuid.UUID, sub string, assetID uuid.UUID, rooms []roomRow, els []elementRow, in LayoutInput) (LayoutView, error) {
	now := s.now()
	oldRooms := map[uuid.UUID]roomRow{}
	for _, r := range rooms {
		oldRooms[r.ID] = r
	}
	oldEls := map[uuid.UUID]elementRow{}
	for _, e := range els {
		oldEls[e.ID] = e
	}
	var f invariant.Fields
	var insR, updR []roomRow
	var insE, updE []elementRow
	keptR, keptE := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for i, r := range in.Rooms {
		row := roomRow{OwnerID: owner, AssetID: assetID, Position: i + 1, Kind: r.Kind, Label: strings.TrimSpace(r.Label),
			Surface: r.SurfaceHundredthsM2, UpdatedAt: now, UpdatedBy: sub}
		if r.ID != nil {
			old, ok := oldRooms[*r.ID]
			if !ok || keptR[*r.ID] {
				f.Add(fmt.Sprintf("rooms[%d].id", i), "Pièce inconnue pour ce bien.")
				continue
			}
			keptR[*r.ID] = true
			row.ID, row.CreatedAt, row.CreatedBy, row.Version = old.ID, old.CreatedAt, old.CreatedBy, old.Version
			if old.Position != row.Position || old.Kind != row.Kind || old.Label != row.Label || !sameInt(old.Surface, row.Surface) {
				row.Version++
				updR = append(updR, row)
			}
		} else {
			row.ID, row.CreatedAt, row.CreatedBy, row.Version = s.NewID(), now, sub, 1
			insR = append(insR, row)
		}
		for j, e := range r.Elements {
			erow := elementRow{OwnerID: owner, RoomID: row.ID, Position: j + 1, Category: string(e.Category), Label: strings.TrimSpace(e.Label),
				Quantity: e.Quantity, UpdatedAt: now, UpdatedBy: sub}
			if e.InstalledOn != nil {
				t := e.InstalledOn.Time()
				erow.InstalledOn = &t
			}
			if e.ID != nil {
				old, ok := oldEls[*e.ID]
				if !ok || keptE[*e.ID] {
					f.Add(fmt.Sprintf("rooms[%d].elements[%d].id", i, j), "Élément inconnu pour ce bien.")
					continue
				}
				keptE[*e.ID] = true
				erow.ID, erow.CreatedAt, erow.CreatedBy, erow.Version = old.ID, old.CreatedAt, old.CreatedBy, old.Version
				if old.RoomID != erow.RoomID || old.Position != erow.Position || old.Category != erow.Category || old.Label != erow.Label ||
					old.Quantity != erow.Quantity || !sameTime(old.InstalledOn, erow.InstalledOn) {
					erow.Version++
					updE = append(updE, erow)
				}
			} else {
				erow.ID, erow.CreatedAt, erow.CreatedBy, erow.Version = s.NewID(), now, sub, 1
				insE = append(insE, erow)
			}
		}
	}
	if len(f) > 0 {
		return LayoutView{}, f
	}
	var delR, delE []uuid.UUID
	for _, r := range rooms {
		if !keptR[r.ID] {
			delR = append(delR, r.ID)
		}
	}
	for _, e := range els {
		if !keptE[e.ID] && keptR[e.RoomID] {
			delE = append(delE, e.ID) // a removed room takes its elements with it
		}
	}
	if err := s.Repo.SaveRooms(ctx, insR, updR, delR, insE, updE, delE); err != nil {
		return LayoutView{}, err
	}
	_, found, nr, ne, err := s.Repo.Layout(ctx, assetID)
	if err != nil {
		return LayoutView{}, err
	}
	return view(assetID, layoutRow{}, found, nr, ne), nil
}

func sameInt(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func sameTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

// SeedLayout builds an asset's first layout from the latest template of its
// kind (the owner's own when it has one, else the system's): refused when
// the asset already has rooms.
func (s Service) SeedLayout(ctx context.Context, p *auth.Principal, requestID string, assetID uuid.UUID, in SeedInput) (LayoutView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return LayoutView{}, err
	}
	var f invariant.Fields
	if in.Bedrooms < 0 || in.Bedrooms > maxRooms {
		f.Add("bedrooms", fmt.Sprintf("Entre 0 et %d chambres.", maxRooms))
	}
	for k := range in.Features {
		if k != "cellar" && k != "garage" && k != "garden" {
			f.Add("features", "Attendu : cellar, garage ou garden.")
		}
	}
	if len(f) > 0 {
		return LayoutView{}, f
	}
	var out LayoutView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		a, err := s.Portfolio.AssetInTx(ctx, assetID)
		if err != nil {
			return err
		}
		l, found, rooms, _, err := s.Repo.Layout(ctx, assetID)
		if err != nil {
			return err
		}
		if len(rooms) > 0 {
			return invariant.New(TypeLayoutExists, assetID.String())
		}
		tpls, err := s.Repo.Templates(ctx)
		if err != nil {
			return err
		}
		var tpl *Template
		for _, row := range tpls {
			if row.AssetKind == string(a.Kind) {
				var t Template
				if err := json.Unmarshal(row.Structure, &t); err != nil {
					return fmt.Errorf("inventory: template %s: %w", row.ID, err)
				}
				tpl = &t
				break // owner rows first, then the newest version
			}
		}
		if tpl == nil {
			return invariant.New(TypeNoTemplate, string(a.Kind))
		}
		furnished := a.Furnished
		if in.Furnished != nil {
			furnished = *in.Furnished
		}
		seeded, err := Seed(*tpl, Asset{ID: assetID.String(), Kind: string(a.Kind), Bedrooms: in.Bedrooms, Features: in.Features, Furnished: furnished})
		if err != nil {
			return err
		}
		body := LayoutInput{Rooms: make([]RoomInput, 0, len(seeded))}
		for _, r := range seeded {
			ri := RoomInput{Kind: r.Kind, Label: r.Label, Elements: make([]ElementInput, 0, len(r.Elements))}
			for _, e := range r.Elements {
				ri.Elements = append(ri.Elements, ElementInput{Category: e.Category, Label: e.Label, Quantity: e.Quantity})
			}
			body.Rooms = append(body.Rooms, ri)
		}
		version := 0
		if found {
			version = l.Version
		}
		before := view(assetID, l, found, nil, nil)
		if out, err = s.apply(ctx, sc.OwnerID, p.Sub, assetID, nil, nil, body); err != nil {
			return err
		}
		if err := s.Repo.BumpLayout(ctx, layoutRow{AssetID: assetID, OwnerID: sc.OwnerID, CreatedAt: s.now(), CreatedBy: p.Sub,
			UpdatedAt: s.now(), UpdatedBy: p.Sub}, version); err != nil {
			return err
		}
		out.Version = version + 1
		return audit.Record(ctx, "layout.seed", audit.Entity{Type: "asset", ID: assetID.String()}, before, out)
	})
	return out, err
}

// Templates lists the templates the scope can read.
func (s Service) Templates(ctx context.Context, p *auth.Principal, requestID string) ([]TemplateView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []TemplateView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Templates(ctx)
		for _, r := range rows {
			out = append(out, TemplateView{ID: r.ID, AssetKind: r.AssetKind, Version: r.Version, System: r.OwnerID == nil, Source: r.Source, Structure: r.Structure})
		}
		return err
	})
	return out, err
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}
