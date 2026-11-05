package inventory

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the capture routes (RENT-INV-001 §7).
const (
	TypeLayoutMissing    = "inventory/layout-missing"
	TypeNotEditable      = "inventory/not-editable"
	TypeTransition       = "inventory/transition-invalid"
	TypeMentionMissing   = "inventory/mandatory-mention-missing"
	TypeSignatureMissing = "inventory/signature-missing"
	TypeMeterHasPhotos   = "inventory/meter-has-photos"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeLayoutMissing:    problem.M(422, "Un bien du bail n’a pas encore de plan de pièces : créez-le avant l’état des lieux.", "An asset of the lease has no layout yet: create it before the inventory."),
		TypeNotEditable:      problem.M(409, "Cet état des lieux ne se modifie pas à cette étape.", "This inventory cannot be changed at this stage."),
		TypeTransition:       problem.M(409, "Cette étape n’est pas possible depuis l’état actuel de l’état des lieux.", "This step is not possible from the inventory's current state."),
		TypeMentionMissing:   problem.M(422, "Des mentions obligatoires manquent à l’état des lieux.", "Mandatory mentions are missing from the inventory."),
		TypeSignatureMissing: problem.M(422, "Toutes les parties n’ont pas encore signé l’état des lieux.", "Not every party has signed the inventory yet."),
		TypeMeterHasPhotos:   problem.M(409, "Ce compteur a des photos archivées : il ne peut pas être retiré.", "This meter has archived photos: it cannot be removed."),
	})
}

// Product limits of a capture (not legal values).
const (
	maxItems       = 5000
	maxMeters      = 30
	maxKeys        = 50
	maxLabel       = 200
	maxComment     = 1500
	maxPlace       = 300
	maxObservation = 4000
)

var (
	meterKinds = []string{"elec_peak", "elec_offpeak", "elec_base", "gas", "water_cold", "water_hot", "heating_energy"}
	keyKinds   = []string{"key", "badge", "remote", "card", "code"}
	reading    = regexp.MustCompile(`^([0-9]+([.,][0-9]+)?)?$`)
)

// Inventory states (RENT-INV-001 §5).
const (
	StateDraft      = "draft"
	StateScheduled  = "scheduled"
	StateInProgress = "in_progress"
	StateCompleted  = "completed"
	StateSigned     = "signed"
	StateIssued     = "issued"
)

// Transition actions.
const (
	ActionSchedule = "schedule"
	ActionStart    = "start"
	ActionComplete = "complete"
	ActionReopen   = "reopen"
	ActionIssue    = "issue"
)

// transitions maps an action to the state it leaves and the state it enters.
var transitions = map[string][2]string{
	ActionSchedule: {StateDraft, StateScheduled},
	ActionStart:    {StateScheduled, StateInProgress},
	ActionComplete: {StateInProgress, StateCompleted},
	ActionReopen:   {StateCompleted, StateInProgress},
	ActionIssue:    {StateSigned, StateIssued},
}

// What a state lets the owner change (RENT-INV-001 §5): the header (date,
// place, observations) until completed, the items and photos on site (and
// in a draft), the meters and keys until completed.
func headerEditable(state string) bool {
	return state == StateDraft || state == StateScheduled || state == StateInProgress
}

func itemsEditable(state string) bool { return state == StateDraft || state == StateInProgress }

func editError(state string) error {
	if state == StateIssued {
		return invariant.New(TypeIssuedImmutable, "")
	}
	return invariant.New(TypeNotEditable, state)
}

// ItemView is an item of a captured inventory.
type ItemView struct {
	ID        uuid.UUID   `json:"id"`
	Position  int         `json:"position"`
	Category  Category    `json:"category" enums:"floor,walls,ceiling,joinery,electrical,plumbing,heating,appliance,furniture,other"`
	Label     string      `json:"label"`
	Condition Condition   `json:"condition,omitempty" enums:"new,good,fair,poor,broken"`
	Works     Works       `json:"works,omitempty" enums:"ok,ko,n/a"`
	Quantity  int         `json:"quantity"`
	Comment   string      `json:"comment"`
	ToCheck   bool        `json:"to_check"`
	ElementID *uuid.UUID  `json:"element_id,omitempty"`
	Photos    []uuid.UUID `json:"photos"`
	Version   int         `json:"version"`
}

// CaptureRoom is a room of a captured inventory: its label and kind as
// copied from the layout, and its items in order.
type CaptureRoom struct {
	RoomID *uuid.UUID `json:"room_id,omitempty"`
	Label  string     `json:"label"`
	Kind   string     `json:"kind,omitempty"`
	Items  []ItemView `json:"items"`
}

// MeterView is a meter reading of a captured inventory.
type MeterView struct {
	ID          uuid.UUID   `json:"id"`
	Kind        string      `json:"kind" enums:"elec_peak,elec_offpeak,elec_base,gas,water_cold,water_hot,heating_energy"`
	MeterNumber string      `json:"meter_number"`
	Reading     string      `json:"reading"`
	Unit        string      `json:"unit"`
	Photos      []uuid.UUID `json:"photos"`
}

// KeyView is a line of keys and access devices.
type KeyView struct {
	Kind        string `json:"kind" enums:"key,badge,remote,card,code"`
	Destination string `json:"destination"`
	Quantity    int    `json:"quantity"`
	Comment     string `json:"comment"`
}

// CaptureView is an inventory captured in Tenure, as the capture screen
// reads it: the header, the rooms and items, meters, keys, the parties who
// sign and the checks still open.
type CaptureView struct {
	ID           uuid.UUID     `json:"id"`
	LeaseID      uuid.UUID     `json:"lease_id"`
	Kind         string        `json:"kind" enums:"entry,exit"`
	Mode         string        `json:"mode" enums:"amicable,bailiff,upload"`
	State        string        `json:"state" enums:"draft,scheduled,in_progress,completed,signed,issued"`
	DoneOn       *period.Date  `json:"done_on,omitempty" swaggertype:"string" format:"date"`
	ScheduledAt  *time.Time    `json:"scheduled_at,omitempty"`
	Place        string        `json:"place"`
	Observations string        `json:"observations"`
	DocumentID   *uuid.UUID    `json:"document_id,omitempty"`
	Version      int           `json:"version"`
	Rooms        []CaptureRoom `json:"rooms"`
	Meters       []MeterView   `json:"meters"`
	Keys         []KeyView     `json:"keys"`
	Parties      []Party       `json:"parties"`
	Checks       Checks        `json:"checks"`
}

// HeaderInput is the body of PUT /inventories/{id}: the whole header.
type HeaderInput struct {
	DoneOn       *period.Date `json:"done_on,omitempty" swaggertype:"string" format:"date"`
	ScheduledAt  *time.Time   `json:"scheduled_at,omitempty"`
	Place        string       `json:"place"`
	Observations string       `json:"observations"`
}

// ItemInput is the body of PUT /inventories/{id}/items/{itemId}: the item as
// found. An empty condition leaves it unassessed.
type ItemInput struct {
	Condition Condition `json:"condition,omitempty" enums:"new,good,fair,poor,broken"`
	Works     Works     `json:"works,omitempty" enums:"ok,ko,n/a"`
	Quantity  int       `json:"quantity"`
	Comment   string    `json:"comment"`
}

// NewItemInput is the body of POST /inventories/{id}/items: an item found on
// site, in a room of the inventory (room_id, or the room's label) or in a new
// one (room_label and room_kind).
type NewItemInput struct {
	RoomID    *uuid.UUID `json:"room_id,omitempty"`
	RoomLabel string     `json:"room_label"`
	RoomKind  string     `json:"room_kind,omitempty" enums:"entry,living,kitchen,bedroom,bathroom,wc,storage,outside,garage,box,other"`
	Category  Category   `json:"category" enums:"floor,walls,ceiling,joinery,electrical,plumbing,heating,appliance,furniture,other"`
	Label     string     `json:"label"`
	Quantity  int        `json:"quantity"`
}

// MeterInput is a meter of PUT /inventories/{id}/meters; ID keeps an
// existing meter (and its photos).
type MeterInput struct {
	ID          *uuid.UUID `json:"id,omitempty"`
	Kind        string     `json:"kind" enums:"elec_peak,elec_offpeak,elec_base,gas,water_cold,water_hot,heating_energy"`
	MeterNumber string     `json:"meter_number"`
	Reading     string     `json:"reading" example:"12345,6"`
	Unit        string     `json:"unit"`
}

// MetersInput is the body of PUT /inventories/{id}/meters.
type MetersInput struct {
	Meters []MeterInput `json:"meters"`
}

// KeysInput is the body of PUT /inventories/{id}/keys.
type KeysInput struct {
	Keys []KeyView `json:"keys"`
}

// TransitionInput is the body of POST /inventories/{id}/transitions.
// schedule may carry the date and place; dry_run returns the checks only.
type TransitionInput struct {
	Action      string     `json:"action" enums:"schedule,start,complete,reopen,issue"`
	DryRun      bool       `json:"dry_run,omitempty"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	Place       *string    `json:"place,omitempty"`
}

// TransitionResult answers a dry run: whether the action would pass, and the
// checks.
type TransitionResult struct {
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
	Checks  Checks `json:"checks"`
}

// capture is an inventory loaded for a service call.
type capture struct {
	head   captureRow
	items  []itemRow
	meters []meterRow
	keys   []keyRow
	photos []photoRow
	sigs   []signatureRow
	facts  lease.InventoryFacts
}

func (s Service) load(ctx context.Context, sc db.Scope, id uuid.UUID, lock bool) (capture, error) {
	h, ok, err := s.Repo.Capture(ctx, id, lock)
	if err != nil {
		return capture{}, err
	}
	if !ok {
		return capture{}, invariant.ErrNotFound
	}
	c := capture{head: h}
	if c.items, err = s.Repo.Items(ctx, id); err != nil {
		return capture{}, err
	}
	if c.meters, err = s.Repo.Meters(ctx, id); err != nil {
		return capture{}, err
	}
	if c.keys, err = s.Repo.Keys(ctx, id); err != nil {
		return capture{}, err
	}
	if c.photos, err = s.Repo.Photos(ctx, id); err != nil {
		return capture{}, err
	}
	if c.sigs, err = s.Repo.Signatures(ctx, id); err != nil {
		return capture{}, err
	}
	c.facts, err = s.Lease.InventoryFactsInTx(ctx, sc, h.LeaseID)
	return c, err
}

// captured refuses an inventory that is not captured in Tenure (an upload).
func captured(h captureRow) error {
	if h.Mode == "upload" {
		return invariant.New(TypeNotEditable, "upload")
	}
	return nil
}

func dateOf(t *time.Time) *period.Date {
	if t == nil {
		return nil
	}
	d := period.FromTime(*t)
	return &d
}

func roomKey(it itemRow) string {
	if it.RoomID != nil {
		return it.RoomID.String()
	}
	return "label:" + it.RoomLabel
}

// engine turns a capture into what the pure functions read.
func (c capture) engine() Inventory {
	inv := Inventory{ID: c.head.ID.String(), Kind: Kind(c.head.Kind), Furnished: c.facts.Kind == lease.KindFurnished,
		DoneOn: dateOf(c.head.DoneOn), KeysReturnedOn: dateOf(c.head.KeysReturnedOn), TenantNewAddress: c.head.TenantNewAddress,
		Rooms: []Room{}, Items: []Item{}, Keys: []Key{}, Meters: []Meter{}, Parties: c.parties()}
	photos := map[uuid.UUID]int{}
	for _, p := range c.photos {
		if p.ItemID != nil {
			photos[*p.ItemID]++
		}
	}
	seen := map[string]bool{}
	for _, it := range c.items {
		k := roomKey(it)
		if !seen[k] {
			seen[k] = true
			kind := ""
			if it.RoomKind != nil {
				kind = *it.RoomKind
			}
			inv.Rooms = append(inv.Rooms, Room{ID: k, Position: len(inv.Rooms) + 1, Kind: kind, Label: it.RoomLabel})
		}
		item := Item{ID: it.ID.String(), RoomID: k, RoomLabel: it.RoomLabel, Category: Category(it.Category), Label: it.Label,
			Quantity: it.Quantity, Comment: it.Comment, Photos: photos[it.ID], ToCheck: it.ToCheck}
		if it.Condition != nil {
			item.Condition = Condition(*it.Condition)
		}
		if it.Works != nil {
			item.Works = Works(*it.Works)
		}
		inv.Items = append(inv.Items, item)
	}
	for _, k := range c.keys {
		inv.Keys = append(inv.Keys, Key{Kind: k.Kind, Destination: k.Destination, Quantity: k.Quantity, Comment: k.Comment})
	}
	for _, m := range c.meters {
		inv.Meters = append(inv.Meters, Meter{Kind: m.Kind, MeterNumber: m.MeterNumber, Reading: m.Reading, Unit: m.Unit})
	}
	return inv
}

// parties are who sign (décret 2016-382): the owner and every tenant of the
// lease; a party has signed when a signature of that party carries its name.
func (c capture) parties() []Party {
	signed := func(party, name string) bool {
		for _, s := range c.sigs {
			if s.Party == party && s.SignerName == name {
				return true
			}
		}
		return false
	}
	out := []Party{{Party: "owner", Name: c.facts.Owner.Name, Signed: signed("owner", c.facts.Owner.Name)}}
	for _, t := range c.facts.Tenants {
		out = append(out, Party{Party: "tenant", Name: t, Signed: signed("tenant", t)})
	}
	return out
}

func (c capture) view() CaptureView {
	v := CaptureView{ID: c.head.ID, LeaseID: c.head.LeaseID, Kind: c.head.Kind, Mode: c.head.Mode, State: c.head.State,
		DoneOn: dateOf(c.head.DoneOn), ScheduledAt: c.head.ScheduledAt, Place: c.head.Place, Observations: c.head.Observations,
		DocumentID: c.head.DocumentID, Version: c.head.Version, Rooms: []CaptureRoom{}, Meters: []MeterView{}, Keys: []KeyView{},
		Parties: c.parties(), Checks: Validate(c.engine())}
	byItem, byMeter := map[uuid.UUID][]uuid.UUID{}, map[uuid.UUID][]uuid.UUID{}
	for _, p := range c.photos {
		switch {
		case p.ItemID != nil:
			byItem[*p.ItemID] = append(byItem[*p.ItemID], p.ID)
		case p.MeterID != nil:
			byMeter[*p.MeterID] = append(byMeter[*p.MeterID], p.ID)
		}
	}
	index := map[string]int{}
	for _, it := range c.items {
		k := roomKey(it)
		i, ok := index[k]
		if !ok {
			kind := ""
			if it.RoomKind != nil {
				kind = *it.RoomKind
			}
			i = len(v.Rooms)
			index[k] = i
			v.Rooms = append(v.Rooms, CaptureRoom{RoomID: it.RoomID, Label: it.RoomLabel, Kind: kind, Items: []ItemView{}})
		}
		v.Rooms[i].Items = append(v.Rooms[i].Items, itemView(it, byItem[it.ID]))
	}
	for _, m := range c.meters {
		ph := byMeter[m.ID]
		if ph == nil {
			ph = []uuid.UUID{}
		}
		v.Meters = append(v.Meters, MeterView{ID: m.ID, Kind: m.Kind, MeterNumber: m.MeterNumber, Reading: m.Reading, Unit: m.Unit, Photos: ph})
	}
	for _, k := range c.keys {
		v.Keys = append(v.Keys, KeyView{Kind: k.Kind, Destination: k.Destination, Quantity: k.Quantity, Comment: k.Comment})
	}
	return v
}

func itemView(it itemRow, photos []uuid.UUID) ItemView {
	if photos == nil {
		photos = []uuid.UUID{}
	}
	v := ItemView{ID: it.ID, Position: it.Position, Category: Category(it.Category), Label: it.Label, Quantity: it.Quantity,
		Comment: it.Comment, ToCheck: it.ToCheck, ElementID: it.ElementID, Photos: photos, Version: it.Version}
	if it.Condition != nil {
		v.Condition = Condition(*it.Condition)
	}
	if it.Works != nil {
		v.Works = Works(*it.Works)
	}
	return v
}

// Capture returns an inventory as captured (an upload has no rooms). The
// co-owner reads it; the accountant does not (RENT-INV-001 §8).
func (s Service) Capture(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (CaptureView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CaptureView{}, err
	}
	var out CaptureView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, false)
		if err != nil {
			return err
		}
		out = c.view()
		return nil
	})
	return out, err
}

func tooLong(s string, n int) bool { return utf8.RuneCountInString(s) > n }

// SaveHeader replaces the header of a captured inventory (If-Match on its
// version): the date it is done, the appointment and place, the
// observations. The date is never in the future.
func (s Service) SaveHeader(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in HeaderInput) (CaptureView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CaptureView{}, err
	}
	var f invariant.Fields
	if in.DoneOn != nil && s.today().Before(*in.DoneOn) {
		f.Add("done_on", "La date de l’état des lieux est dans le futur.")
	}
	if tooLong(in.Place, maxPlace) {
		f.Add("place", fmt.Sprintf("Au plus %d caractères.", maxPlace))
	}
	if tooLong(in.Observations, maxObservation) {
		f.Add("observations", fmt.Sprintf("Au plus %d caractères.", maxObservation))
	}
	if err := f.Err(); err != nil {
		return CaptureView{}, err
	}
	var out CaptureView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		if !headerEditable(c.head.State) {
			return editError(c.head.State)
		}
		if c.head.Version != version {
			return invariant.ErrStale
		}
		before := header(c.head)
		h := c.head
		h.Place, h.Observations, h.ScheduledAt = strings.TrimSpace(in.Place), in.Observations, utcPtr(in.ScheduledAt)
		h.DoneOn = nil
		if in.DoneOn != nil {
			t := in.DoneOn.Time()
			h.DoneOn = &t
		}
		if err := s.touch(ctx, &h, p.Sub); err != nil {
			return err
		}
		c.head = h
		out = c.view()
		return audit.Record(ctx, "inventory.update", audit.Entity{Type: "inventory", ID: id.String()}, before, header(h))
	})
	return out, err
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC().Truncate(time.Second)
	return &u
}

// header is the audited part of an inventory header.
func header(h captureRow) map[string]any {
	return map[string]any{"state": h.State, "done_on": dateOf(h.DoneOn), "scheduled_at": h.ScheduledAt, "place": h.Place,
		"observations": h.Observations, "document_id": h.DocumentID, "version": h.Version}
}

// touch saves h at its version + 1 and moves it there.
func (s Service) touch(ctx context.Context, h *captureRow, by string) error {
	now := s.now()
	h.UpdatedAt, h.UpdatedBy = &now, &by
	if err := s.Repo.SaveHeader(ctx, *h, h.Version); err != nil {
		return err
	}
	h.Version++
	return nil
}

func validItem(f *invariant.Fields, prefix string, in ItemInput) {
	if in.Condition != "" && !in.Condition.Valid() {
		f.Add(prefix+"condition", "État attendu : new, good, fair, poor ou broken.")
	}
	if in.Works != "" && in.Works != WorksOK && in.Works != WorksKO && in.Works != WorksNA {
		f.Add(prefix+"works", "Valeur attendue : ok, ko ou n/a.")
	}
	if in.Quantity < 0 {
		f.Add(prefix+"quantity", "La quantité ne peut pas être négative.")
	}
	if tooLong(in.Comment, maxComment) {
		f.Add(prefix+"comment", fmt.Sprintf("Au plus %d caractères.", maxComment))
	}
}

// SaveItem records an item as found (If-Match on the item's version): its
// condition, whether it works, its quantity and a comment. Confirming an item
// clears its « to check » mark.
func (s Service) SaveItem(ctx context.Context, p *auth.Principal, requestID string, id, itemID uuid.UUID, version int, in ItemInput) (ItemView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ItemView{}, err
	}
	var f invariant.Fields
	validItem(&f, "", in)
	if err := f.Err(); err != nil {
		return ItemView{}, err
	}
	var out ItemView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		var it *itemRow
		for i := range c.items {
			if c.items[i].ID == itemID {
				it = &c.items[i]
			}
		}
		if it == nil {
			return invariant.ErrNotFound
		}
		if !itemsEditable(c.head.State) {
			return editError(c.head.State)
		}
		if it.Version != version {
			return invariant.ErrStale
		}
		before := itemView(*it, nil)
		next := *it
		next.Condition, next.Works = nil, nil
		if in.Condition != "" {
			v := string(in.Condition)
			next.Condition = &v
		}
		if in.Works != "" {
			v := string(in.Works)
			next.Works = &v
		}
		next.Quantity, next.Comment, next.ToCheck = in.Quantity, strings.TrimSpace(in.Comment), false
		next.UpdatedAt, next.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.SaveItem(ctx, next, version); err != nil {
			return err
		}
		next.Version++
		var photos []uuid.UUID
		for _, ph := range c.photos {
			if ph.ItemID != nil && *ph.ItemID == itemID {
				photos = append(photos, ph.ID)
			}
		}
		out = itemView(next, photos)
		before.Photos, before.Version = nil, it.Version
		return audit.Record(ctx, "inventory.item.update", audit.Entity{Type: "inventory", ID: id.String()},
			map[string]any{"item": before}, map[string]any{"item": out})
	})
	return out, err
}

// AddItem records an item found on site, after the last item of its room
// (or at the end, in a new room).
func (s Service) AddItem(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in NewItemInput) (ItemView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ItemView{}, err
	}
	var f invariant.Fields
	if !contains(categories, in.Category) {
		f.Add("category", "Catégorie inconnue.")
	}
	in.Label, in.RoomLabel = strings.TrimSpace(in.Label), strings.TrimSpace(in.RoomLabel)
	if in.Label == "" || tooLong(in.Label, maxLabel) {
		f.Add("label", fmt.Sprintf("Le libellé est obligatoire, %d caractères au plus.", maxLabel))
	}
	if in.RoomID == nil && (in.RoomLabel == "" || tooLong(in.RoomLabel, maxLabel)) {
		f.Add("room_label", "La pièce est obligatoire.")
	}
	if in.RoomKind != "" && !contains(roomKinds, in.RoomKind) {
		f.Add("room_kind", "Type de pièce inconnu.")
	}
	if in.Quantity < 0 {
		f.Add("quantity", "La quantité ne peut pas être négative.")
	}
	if err := f.Err(); err != nil {
		return ItemView{}, err
	}
	var out ItemView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		if !itemsEditable(c.head.State) {
			return editError(c.head.State)
		}
		if len(c.items) >= maxItems {
			return invariant.Fields{{Field: "label", Message: fmt.Sprintf("Au plus %d éléments.", maxItems)}}
		}
		now := s.now()
		row := itemRow{ID: s.NewID(), OwnerID: sc.OwnerID, InventoryID: id, Category: string(in.Category), Label: in.Label,
			Quantity: in.Quantity, Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		// The room: an existing one by id or label takes its label and kind;
		// otherwise a new room at the end.
		last := -1
		for i, it := range c.items {
			if (in.RoomID != nil && it.RoomID != nil && *it.RoomID == *in.RoomID) ||
				(in.RoomID == nil && it.RoomLabel == in.RoomLabel) {
				last = i
			}
		}
		if last >= 0 {
			r := c.items[last]
			row.RoomID, row.RoomLabel, row.RoomKind = r.RoomID, r.RoomLabel, r.RoomKind
		} else if in.RoomID != nil {
			return invariant.Fields{{Field: "room_id", Message: "Aucune pièce de cet état des lieux n’a cet identifiant."}}
		} else {
			row.RoomLabel = in.RoomLabel
			if in.RoomKind != "" {
				k := in.RoomKind
				row.RoomKind = &k
			}
		}
		// Positions are unique: shift the items after the room's last one.
		pos := len(c.items) + 1
		if last >= 0 && last < len(c.items)-1 {
			pos = c.items[last].Position + 1
			if err := s.Repo.ShiftItems(ctx, id, pos); err != nil {
				return err
			}
		}
		row.Position = pos
		if err := s.Repo.InsertItems(ctx, []itemRow{row}); err != nil {
			return err
		}
		out = itemView(row, nil)
		return audit.Record(ctx, "inventory.item.add", audit.Entity{Type: "inventory", ID: id.String()}, nil,
			map[string]any{"item": out, "room_label": row.RoomLabel})
	})
	return out, err
}

// SaveMeters replaces the meters of a captured inventory (If-Match on the
// inventory's version). A meter that has photos cannot be removed.
func (s Service) SaveMeters(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in MetersInput) (CaptureView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CaptureView{}, err
	}
	var f invariant.Fields
	if len(in.Meters) > maxMeters {
		f.Add("meters", fmt.Sprintf("Au plus %d compteurs.", maxMeters))
	}
	for i, m := range in.Meters {
		k := fmt.Sprintf("meters[%d].", i)
		if !contains(meterKinds, m.Kind) {
			f.Add(k+"kind", "Type de compteur inconnu.")
		}
		if !reading.MatchString(strings.TrimSpace(m.Reading)) {
			f.Add(k+"reading", "Index attendu : des chiffres, avec une virgule décimale au besoin.")
		}
		if tooLong(m.MeterNumber, 64) || tooLong(m.Unit, 16) {
			f.Add(k+"meter_number", "Numéro ou unité trop long.")
		}
	}
	if err := f.Err(); err != nil {
		return CaptureView{}, err
	}
	var out CaptureView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		if !headerEditable(c.head.State) {
			return editError(c.head.State)
		}
		if c.head.Version != version {
			return invariant.ErrStale
		}
		existing := map[uuid.UUID]bool{}
		for _, m := range c.meters {
			existing[m.ID] = true
		}
		now := s.now()
		keep, kept := []meterRow{}, map[uuid.UUID]bool{}
		for i, m := range in.Meters {
			row := meterRow{ID: s.NewID(), OwnerID: sc.OwnerID, InventoryID: id, Kind: m.Kind, MeterNumber: strings.TrimSpace(m.MeterNumber),
				Reading: strings.TrimSpace(m.Reading), Unit: strings.TrimSpace(m.Unit), CreatedAt: now.Add(time.Duration(i) * time.Microsecond), CreatedBy: p.Sub}
			if m.ID != nil {
				if !existing[*m.ID] || kept[*m.ID] {
					return invariant.Fields{{Field: fmt.Sprintf("meters[%d].id", i), Message: "Aucun compteur de cet état des lieux n’a cet identifiant."}}
				}
				row.ID = *m.ID
				kept[*m.ID] = true
			}
			keep = append(keep, row)
		}
		for _, ph := range c.photos {
			if ph.MeterID != nil && !kept[*ph.MeterID] {
				return invariant.New(TypeMeterHasPhotos, ph.MeterID.String())
			}
		}
		before := c.view().Meters
		if err := s.Repo.ReplaceMeters(ctx, id, keep, existing); err != nil {
			return err
		}
		if err := s.touch(ctx, &c.head, p.Sub); err != nil {
			return err
		}
		if c.meters, err = s.Repo.Meters(ctx, id); err != nil {
			return err
		}
		out = c.view()
		return audit.Record(ctx, "inventory.meters.update", audit.Entity{Type: "inventory", ID: id.String()},
			map[string]any{"meters": before}, map[string]any{"meters": out.Meters, "version": out.Version})
	})
	return out, err
}

// SaveKeys replaces the key lines of a captured inventory (If-Match on the
// inventory's version).
func (s Service) SaveKeys(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in KeysInput) (CaptureView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CaptureView{}, err
	}
	var f invariant.Fields
	if len(in.Keys) > maxKeys {
		f.Add("keys", fmt.Sprintf("Au plus %d lignes.", maxKeys))
	}
	for i, k := range in.Keys {
		pre := fmt.Sprintf("keys[%d].", i)
		if !contains(keyKinds, k.Kind) {
			f.Add(pre+"kind", "Type attendu : key, badge, remote, card ou code.")
		}
		if d := strings.TrimSpace(k.Destination); d == "" || tooLong(d, maxLabel) {
			f.Add(pre+"destination", "Ce que la clé ouvre est obligatoire.")
		}
		if k.Quantity < 0 {
			f.Add(pre+"quantity", "La quantité ne peut pas être négative.")
		}
		if tooLong(k.Comment, maxComment) {
			f.Add(pre+"comment", fmt.Sprintf("Au plus %d caractères.", maxComment))
		}
	}
	if err := f.Err(); err != nil {
		return CaptureView{}, err
	}
	var out CaptureView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		if !headerEditable(c.head.State) {
			return editError(c.head.State)
		}
		if c.head.Version != version {
			return invariant.ErrStale
		}
		before := c.view().Keys
		now := s.now()
		rows := []keyRow{}
		for i, k := range in.Keys {
			rows = append(rows, keyRow{ID: s.NewID(), OwnerID: sc.OwnerID, InventoryID: id, Kind: k.Kind, Destination: strings.TrimSpace(k.Destination),
				Quantity: k.Quantity, Comment: strings.TrimSpace(k.Comment), CreatedAt: now.Add(time.Duration(i) * time.Microsecond), CreatedBy: p.Sub})
		}
		if err := s.Repo.ReplaceKeys(ctx, id, rows); err != nil {
			return err
		}
		if err := s.touch(ctx, &c.head, p.Sub); err != nil {
			return err
		}
		c.keys = rows
		out = c.view()
		return audit.Record(ctx, "inventory.keys.update", audit.Entity{Type: "inventory", ID: id.String()},
			map[string]any{"keys": before}, map[string]any{"keys": out.Keys, "version": out.Version})
	})
	return out, err
}

// Transition moves a captured inventory along its lifecycle (RENT-INV-001
// §5): schedule (with the date and place), start on site (the date of the
// inventory becomes today unless set), complete when the content checks
// pass, reopen a completed one, issue a signed one into its EE document.
// sign comes with the signatures (inventory slice 6). A dry run returns the
// checks without changing anything.
func (s Service) Transition(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in TransitionInput) (CaptureView, *TransitionResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return CaptureView{}, nil, err
	}
	move, ok := transitions[in.Action]
	if !ok {
		return CaptureView{}, nil, invariant.Fields{{Field: "action", Message: "Étape attendue : schedule, start, complete, reopen ou issue."}}
	}
	if in.Place != nil && tooLong(*in.Place, maxPlace) {
		return CaptureView{}, nil, invariant.Fields{{Field: "place", Message: fmt.Sprintf("Au plus %d caractères.", maxPlace)}}
	}
	var out CaptureView
	var dry *TransitionResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, true)
		if err != nil {
			return err
		}
		if err := captured(c.head); err != nil {
			return err
		}
		h := c.head
		if in.ScheduledAt != nil {
			h.ScheduledAt = utcPtr(in.ScheduledAt)
		}
		if in.Place != nil {
			h.Place = strings.TrimSpace(*in.Place)
		}
		if in.Action == ActionStart && h.DoneOn == nil {
			t := s.today().Time()
			h.DoneOn = &t
		}
		c.head = h
		checks := Validate(c.engine())
		blocked := s.blocked(in.Action, h, checks)
		if in.DryRun {
			dry = &TransitionResult{Action: in.Action, Allowed: c.head.State == move[0] && blocked == nil, Checks: checks}
			return nil
		}
		if c.head.State != move[0] {
			if c.head.State == StateIssued {
				return invariant.New(TypeIssuedImmutable, "")
			}
			return invariant.New(TypeTransition, in.Action+" from "+c.head.State)
		}
		if blocked != nil {
			return blocked
		}
		before := header(c.head)
		before["state"] = move[0]
		if in.Action == ActionIssue {
			doc, err := s.issueEntry(ctx, c)
			if err != nil {
				return err
			}
			c.head.DocumentID, c.head.TemplateVersion = &doc, "inventory/"+EntryTemplate
		}
		c.head.State = move[1]
		if err := s.touch(ctx, &c.head, p.Sub); err != nil {
			return err
		}
		out = c.view()
		after := header(c.head)
		after["action"] = in.Action
		return audit.Record(ctx, "inventory.transition", audit.Entity{Type: "inventory", ID: id.String()}, before, after)
	})
	return out, dry, err
}

// blocked is the error an action meets on the checks, nil when it may pass.
func (s Service) blocked(action string, h captureRow, checks Checks) error {
	var f invariant.Fields
	switch action {
	case ActionSchedule:
		if h.ScheduledAt == nil {
			f.Add("scheduled_at", "La date et l’heure du rendez-vous sont obligatoires.")
		}
		if h.Place == "" {
			f.Add("place", "Le lieu du rendez-vous est obligatoire.")
		}
		return f.Err()
	case ActionComplete:
		if h.DoneOn != nil && s.today().Before(period.FromTime(*h.DoneOn)) {
			f.Add("done_on", "La date de l’état des lieux est dans le futur.")
			return f.Err()
		}
		if checks.Blocks(StageComplete) {
			return invariant.New(TypeMentionMissing, codes(checks, StageComplete))
		}
	case ActionIssue:
		if checks.Blocks(StageComplete) {
			return invariant.New(TypeMentionMissing, codes(checks, StageComplete))
		}
		if checks.Blocks(StageIssue) {
			return invariant.New(TypeSignatureMissing, codes(checks, StageIssue))
		}
	}
	return nil
}

// codes lists the check codes of a stage, for logs and tests (no labels: they
// may name people).
func codes(c Checks, s Stage) string {
	var out []string
	for _, x := range c.Blocking {
		if x.Stage == s {
			out = append(out, x.Code)
		}
	}
	return strings.Join(out, ",")
}

func (s Service) today() period.Date { return period.FromTime(s.Now().In(paris())) }

// Drafter drafts the items of a captured entry inventory from the layouts of
// the lease's assets, inside the lease's transaction (lease.Drafter).
type Drafter struct {
	Repo  Repository
	NewID func() uuid.UUID
}

// DraftEntryInTx implements lease.Drafter: every element of every room of
// each asset's layout, in the lease's asset order, becomes an unassessed
// item, its room's label and kind copied (Draft). An asset without a layout
// is refused (inventory/layout-missing): the owner seeds it first (S15a).
func (d Drafter) DraftEntryInTx(ctx context.Context, r lease.DraftRequest) (int, error) {
	var layouts [][]Room
	kinds := map[string]string{}
	elements := map[string]elementRow{}
	for _, a := range r.AssetIDs {
		_, _, rooms, els, err := d.Repo.Layout(ctx, a)
		if err != nil {
			return 0, err
		}
		if len(rooms) == 0 {
			return 0, invariant.New(TypeLayoutMissing, a.String())
		}
		var layout []Room
		for _, rm := range rooms {
			room := Room{ID: rm.ID.String(), AssetID: a.String(), Position: rm.Position, Kind: rm.Kind, Label: rm.Label}
			kinds[room.ID] = rm.Kind
			for _, e := range els {
				if e.RoomID == rm.ID {
					room.Elements = append(room.Elements, Element{ID: e.ID.String(), RoomID: room.ID, Position: e.Position,
						Category: Category(e.Category), Label: e.Label, Quantity: e.Quantity})
					elements[e.ID.String()] = e
				}
			}
			layout = append(layout, room)
		}
		layouts = append(layouts, layout)
	}
	inv := Draft(r.InventoryID.String(), r.Furnished, layouts...)
	rows := make([]itemRow, 0, len(inv.Items))
	for i, it := range inv.Items {
		e := elements[it.ElementID]
		room, elem := e.RoomID, e.ID
		kind := kinds[it.RoomID]
		rows = append(rows, itemRow{ID: d.NewID(), OwnerID: r.OwnerID, InventoryID: r.InventoryID, RoomID: &room, ElementID: &elem,
			RoomLabel: it.RoomLabel, RoomKind: &kind, Position: i + 1, Category: string(it.Category), Label: it.Label,
			Quantity: it.Quantity, Version: 1, CreatedAt: r.At, CreatedBy: r.Actor, UpdatedAt: r.At, UpdatedBy: r.Actor})
	}
	return len(rows), d.Repo.InsertItems(ctx, rows)
}
