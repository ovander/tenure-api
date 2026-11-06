package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/inventory"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// The inventory cases (RENT-INV-001 §10) live under
// testdata/golden/<year>/inventory/ and replay the pure functions of
// internal/inventory. The harness stands in for the repository: a seeded
// layout gets ids <asset>-r<room> and <asset>-r<room>-e<element>, and each
// inventory is kept by its id for the next steps.

type inventoryCase struct {
	Case           string                     `json:"case"`
	Description    string                     `json:"description"`
	ParamsOverride []params.Entry             `json:"params_override,omitempty"`
	Assets         []inventory.Asset          `json:"assets,omitempty"`
	Grids          map[string]*inventory.Grid `json:"grids,omitempty"`
	Steps          []inventoryStep            `json:"steps"`
}

type inventoryStep struct {
	Name       string          `json:"name"`
	Seed       *string         `json:"seed,omitempty"` // asset id
	Draft      *draftStep      `json:"draft,omitempty"`
	Capture    *captureStep    `json:"capture,omitempty"`
	Prefill    *prefillStep    `json:"prefill,omitempty"`
	Compare    *compareStep    `json:"compare,omitempty"`
	Deduct     *deductStep     `json:"deduct,omitempty"`
	Settlement *settlementStep `json:"settlement,omitempty"`
	Windows    *windowsStep    `json:"windows,omitempty"`
	Expected   json.RawMessage `json:"expected"`
}

type draftStep struct {
	ID        string   `json:"id"`
	Furnished bool     `json:"furnished,omitempty"`
	Assets    []string `json:"assets"`
}

// captureStep records what was found on site, then validates: items by
// element (<asset>-r<room>-e<element>), defaults applied to every item not
// listed.
type captureStep struct {
	ID               string               `json:"id"`
	DoneOn           *period.Date         `json:"done_on,omitempty"`
	KeysReturnedOn   *period.Date         `json:"keys_returned_on,omitempty"`
	TenantNewAddress string               `json:"tenant_new_address,omitempty"`
	Default          *inventory.Item      `json:"default,omitempty"`
	Items            map[string]itemPatch `json:"items,omitempty"`
	AddItems         []inventory.Item     `json:"add_items,omitempty"`
	Keys             []inventory.Key      `json:"keys,omitempty"`
	Parties          []inventory.Party    `json:"parties,omitempty"`
}

type itemPatch struct {
	Condition inventory.Condition `json:"condition,omitempty"`
	Works     inventory.Works     `json:"works,omitempty"`
	Quantity  *int                `json:"quantity,omitempty"`
	Comment   string              `json:"comment,omitempty"`
	Photos    int                 `json:"photos,omitempty"`
}

type prefillStep struct {
	From        string                 `json:"from"`
	ID          string                 `json:"id"`
	Completions []inventory.Completion `json:"completions,omitempty"`
}

type compareStep struct {
	Entry       string                 `json:"entry"`
	Exit        string                 `json:"exit"`
	Completions []inventory.Completion `json:"completions,omitempty"`
}

type deductStep struct {
	Input inventory.DeductionInput `json:"input"`
	Grid  string                   `json:"grid,omitempty"`
}

type settlementStep struct {
	KeysReturnedOn     period.Date               `json:"keys_returned_on"`
	DeductionRetained  bool                      `json:"deduction_retained"`
	MonthlyRentCents   money.Cents               `json:"monthly_rent_cents"`
	NewAddressGiven    bool                      `json:"new_address_given"`
	PaidOn             period.Date               `json:"paid_on"`
	AccountsApprovedOn *period.Date              `json:"accounts_approved_on,omitempty"`
	Settle             inventory.SettlementInput `json:"settle"`
}

// windowsStep computes the completion windows of an entry done on DoneOn
// and says, for each request date, whether its reason is within them.
type windowsStep struct {
	DoneOn   period.Date      `json:"done_on"`
	Requests []windowsRequest `json:"requests,omitempty"`
}

type windowsRequest struct {
	Reason string      `json:"reason"`
	On     period.Date `json:"on"`
}

type windowsResult struct {
	Windows inventory.Windows `json:"windows"`
	Open    []bool            `json:"open"`
}

type settlementResult struct {
	Deadline                period.Date          `json:"deadline"`
	Penalty                 inventory.Penalty    `json:"penalty"`
	Settlement              inventory.Settlement `json:"settlement"`
	HoldbackBalanceDeadline *period.Date         `json:"holdback_balance_deadline,omitempty"`
}

// roomView is how a seeded room appears in a golden file: its label, kind
// and element labels, so a changed template shows as a readable diff.
type roomView struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Elements []string `json:"elements"`
}

func loadTemplates(t *testing.T) map[string]inventory.Template {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "inventory", "templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Source    string               `json:"source"`
		Templates []inventory.Template `json:"templates"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	out := map[string]inventory.Template{}
	for _, tp := range f.Templates {
		out[tp.AssetKind] = tp
	}
	return out
}

func TestInventoryGolden(t *testing.T) {
	years, err := os.ReadDir(filepath.Join(root, "golden"))
	if err != nil {
		t.Fatal(err)
	}
	templates := loadTemplates(t)
	n := 0
	for _, y := range years {
		year, err := strconv.Atoi(y.Name())
		if !y.IsDir() || err != nil {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(root, "golden", y.Name(), "inventory", "*.json"))
		sort.Strings(files)
		for _, f := range files {
			n++
			t.Run(y.Name()+"/inventory/"+filepath.Base(f), func(t *testing.T) { runInventoryCase(t, f, year, templates) })
		}
	}
	if n == 0 {
		t.Fatal("no inventory golden case found")
	}
}

type inventoryState struct {
	c         inventoryCase
	ps        params.Set
	templates map[string]inventory.Template
	layouts   map[string][]inventory.Room
	invs      map[string]inventory.Inventory
}

func runInventoryCase(t *testing.T, path string, year int, templates map[string]inventory.Template) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c inventoryCase
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	st := &inventoryState{c: c, ps: loadParams(t, year, c.ParamsOverride), templates: templates,
		layouts: map[string][]inventory.Room{}, invs: map[string]inventory.Inventory{}}
	changed := false
	for i := range c.Steps {
		s := &c.Steps[i]
		got, err := st.run(s)
		if err != nil {
			t.Fatalf("step %q: %v", s.Name, err)
		}
		b, err := json.MarshalIndent(got, "      ", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if !bytes.Equal(normalise(t, s.Expected), normalise(t, b)) {
				changed = true
			}
			s.Expected = b
			continue
		}
		if len(s.Expected) == 0 {
			t.Errorf("step %q has no expected result; run with -update and review", s.Name)
			continue
		}
		if want, have := normalise(t, s.Expected), normalise(t, b); !bytes.Equal(want, have) {
			t.Errorf("step %q differs from the golden file\nwant %s\n got %s", s.Name, want, have)
		}
	}
	if *update && changed {
		out, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
	}
}

func (st *inventoryState) asset(id string) (inventory.Asset, error) {
	for _, a := range st.c.Assets {
		if a.ID == id {
			return a, nil
		}
	}
	return inventory.Asset{}, fmt.Errorf("unknown asset %s", id)
}

func (st *inventoryState) run(s *inventoryStep) (any, error) {
	switch {
	case s.Seed != nil:
		a, err := st.asset(*s.Seed)
		if err != nil {
			return nil, err
		}
		tp, ok := st.templates[a.Kind]
		if !ok {
			return nil, fmt.Errorf("no template for %s", a.Kind)
		}
		rooms, err := inventory.Seed(tp, a)
		if err != nil {
			return nil, err
		}
		views := []roomView{}
		for i := range rooms {
			rooms[i].ID = fmt.Sprintf("%s-r%d", a.ID, rooms[i].Position)
			v := roomView{ID: rooms[i].ID, Kind: rooms[i].Kind, Label: rooms[i].Label, Elements: []string{}}
			for j := range rooms[i].Elements {
				rooms[i].Elements[j].ID = fmt.Sprintf("%s-e%d", rooms[i].ID, rooms[i].Elements[j].Position)
				rooms[i].Elements[j].RoomID = rooms[i].ID
				v.Elements = append(v.Elements, rooms[i].Elements[j].Label)
			}
			views = append(views, v)
		}
		st.layouts[a.ID] = rooms
		return views, nil
	case s.Draft != nil:
		var layouts [][]inventory.Room
		for _, id := range s.Draft.Assets {
			l, ok := st.layouts[id]
			if !ok {
				return nil, fmt.Errorf("asset %s not seeded", id)
			}
			layouts = append(layouts, l)
		}
		inv := inventory.Draft(s.Draft.ID, s.Draft.Furnished, layouts...)
		st.invs[inv.ID] = inv
		return map[string]int{"rooms": len(inv.Rooms), "items": len(inv.Items)}, nil
	case s.Capture != nil:
		inv, ok := st.invs[s.Capture.ID]
		if !ok {
			return nil, fmt.Errorf("unknown inventory %s", s.Capture.ID)
		}
		cp := s.Capture
		if cp.DoneOn != nil {
			inv.DoneOn = cp.DoneOn
		}
		if cp.KeysReturnedOn != nil {
			inv.KeysReturnedOn = cp.KeysReturnedOn
		}
		if cp.TenantNewAddress != "" {
			inv.TenantNewAddress = cp.TenantNewAddress
		}
		for i := range inv.Items {
			it := &inv.Items[i]
			if cp.Default != nil {
				if cp.Default.Condition != "" {
					it.Condition, it.ToCheck = cp.Default.Condition, false
				}
				if cp.Default.Works != "" && (it.Category == inventory.Appliance || it.Category == inventory.Electrical || it.Category == inventory.Plumbing) {
					it.Works = cp.Default.Works
				}
			}
			p, ok := cp.Items[it.ElementIDOr()]
			if !ok {
				continue
			}
			if p.Condition != "" {
				it.Condition = p.Condition
			}
			if p.Works != "" {
				it.Works = p.Works
			}
			if p.Quantity != nil {
				it.Quantity = *p.Quantity
			}
			if p.Comment != "" {
				it.Comment = p.Comment
			}
			it.Photos += p.Photos
			it.ToCheck = false
		}
		inv.Items = append(inv.Items, cp.AddItems...)
		if cp.Keys != nil {
			inv.Keys = cp.Keys
		}
		if cp.Parties != nil {
			inv.Parties = cp.Parties
		}
		st.invs[inv.ID] = inv
		return inventory.Validate(inv), nil
	case s.Prefill != nil:
		src, ok := st.invs[s.Prefill.From]
		if !ok {
			return nil, fmt.Errorf("unknown inventory %s", s.Prefill.From)
		}
		inv := inventory.Prefill(s.Prefill.ID, src, s.Prefill.Completions)
		st.invs[inv.ID] = inv
		toCheck := 0
		for _, it := range inv.Items {
			if it.ToCheck {
				toCheck++
			}
		}
		return map[string]any{"kind": inv.Kind, "items": len(inv.Items), "to_check": toCheck, "keys": inv.Keys,
			"entry_done_on": inv.EntryDoneOn}, nil
	case s.Compare != nil:
		e, ok1 := st.invs[s.Compare.Entry]
		x, ok2 := st.invs[s.Compare.Exit]
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("unknown inventory")
		}
		cmp := inventory.Compare(e, s.Compare.Completions, x)
		// Only what differs: a golden of every same item would hide the point.
		changed := []inventory.ItemResult{}
		for _, r := range cmp.Items {
			if r.Result != inventory.Same {
				changed = append(changed, r)
			}
		}
		cmp.Items = changed
		return cmp, nil
	case s.Deduct != nil:
		var g *inventory.Grid
		if s.Deduct.Grid != "" {
			var ok bool
			if g, ok = st.c.Grids[s.Deduct.Grid]; !ok {
				return nil, fmt.Errorf("unknown grid %s", s.Deduct.Grid)
			}
		}
		return inventory.Deduct(s.Deduct.Input, g)
	case s.Windows != nil:
		r, err := inventory.WindowRulesAt(st.ps, s.Windows.DoneOn)
		if err != nil {
			return nil, err
		}
		w := inventory.CompletionWindows(s.Windows.DoneOn, r)
		out := windowsResult{Windows: w, Open: []bool{}}
		for _, q := range s.Windows.Requests {
			out.Open = append(out.Open, w.Open(q.Reason, s.Windows.DoneOn, q.On))
		}
		return out, nil
	case s.Settlement != nil:
		ss := s.Settlement
		r, err := st.ps.DepositReturn(ss.KeysReturnedOn)
		if err != nil {
			return nil, err
		}
		var out settlementResult
		if out.Deadline, err = inventory.SettlementDeadline(ss.KeysReturnedOn, ss.DeductionRetained, r); err != nil {
			return nil, err
		}
		if out.Penalty, err = inventory.LatePenalty(out.Deadline, ss.PaidOn, ss.MonthlyRentCents, ss.NewAddressGiven, r); err != nil {
			return nil, err
		}
		if out.Settlement, err = inventory.Settle(ss.Settle, r); err != nil {
			return nil, err
		}
		if ss.AccountsApprovedOn != nil {
			d, err := inventory.HoldbackBalanceDeadline(*ss.AccountsApprovedOn, r)
			if err != nil {
				return nil, err
			}
			out.HoldbackBalanceDeadline = &d
		}
		return out, nil
	}
	return nil, fmt.Errorf("step has no calculation")
}
