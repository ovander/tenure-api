package inventory

// Result is the outcome of comparing an exit item with its entry item
// (RENT-INV-001 §6). Tenure never charges a result on its own: the owner
// qualifies a worse or missing item before any deduction.
type Result string

// Comparison results.
const (
	Same    Result = "same"
	Better  Result = "better"
	Worse   Result = "worse"
	Missing Result = "missing"
	NewItem Result = "new"
)

// State is an item's condition, works and quantity on one side.
type State struct {
	Condition Condition `json:"condition,omitempty"`
	Works     Works     `json:"works,omitempty"`
	Quantity  int       `json:"quantity,omitempty"`
	Comment   string    `json:"comment,omitempty"`
}

// ItemResult is one exit item against its entry item. Steps is how many steps
// down the scale it went (negative when better).
type ItemResult struct {
	ExitItemID  string   `json:"exit_item_id"`
	EntryItemID string   `json:"entry_item_id,omitempty"`
	Room        string   `json:"room"`
	Label       string   `json:"label"`
	Category    Category `json:"category"`
	Result      Result   `json:"result"`
	Entry       *State   `json:"entry,omitempty"`
	Exit        State    `json:"exit"`
	Steps       int      `json:"steps"`
}

// KeyResult is one key line, matched on kind and destination.
type KeyResult struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
	Entry       int    `json:"entry"`
	Exit        int    `json:"exit"`
	Result      Result `json:"result"`
}

// Comparison is the result of Compare.
type Comparison struct {
	Items []ItemResult `json:"items"`
	Keys  []KeyResult  `json:"keys"`
	// Flagged counts the worse and missing results: what the owner must
	// qualify before the settlement.
	Flagged int `json:"flagged"`
}

// Compare compares an exit with its entry, the entry corrected by its
// accepted completion lines:
//   - missing: fewer pieces of furniture, or fewer keys, than at entry;
//   - worse: lower on the scale, or works ok at entry and ko at exit;
//   - better: higher on the scale, or works ko at entry and ok at exit;
//   - same: neither;
//   - new: an exit item with no entry item, informative only.
//
// Exit items keep their order; keys follow the entry's order, then the keys
// found only at exit.
func Compare(entry Inventory, completions []Completion, exit Inventory) Comparison {
	accepted := acceptedLines(completions)
	byID := map[string]Item{}
	for _, it := range entry.Items {
		byID[it.ID] = applyCompletion(it, accepted[it.ID])
	}
	out := Comparison{Items: []ItemResult{}, Keys: []KeyResult{}}
	for _, x := range exit.Items {
		r := ItemResult{ExitItemID: x.ID, EntryItemID: x.EntryItemID, Room: x.RoomLabel, Label: x.Label, Category: x.Category,
			Exit: State{Condition: x.Condition, Works: x.Works, Quantity: x.Quantity, Comment: x.Comment}}
		e, ok := byID[x.EntryItemID]
		if x.EntryItemID == "" || !ok {
			r.Result = NewItem
			out.Items = append(out.Items, r)
			continue
		}
		r.Entry = &State{Condition: e.Condition, Works: e.Works, Quantity: e.Quantity, Comment: e.Comment}
		r.Steps = e.Condition.rank() - x.Condition.rank()
		switch {
		case x.Quantity < e.Quantity:
			r.Result = Missing
		case r.Steps > 0 || (e.Works == WorksOK && x.Works == WorksKO):
			r.Result = Worse
		case r.Steps < 0 || (e.Works == WorksKO && x.Works == WorksOK):
			r.Result = Better
		default:
			r.Result = Same
		}
		if r.Result == Worse || r.Result == Missing {
			out.Flagged++
		}
		out.Items = append(out.Items, r)
	}
	type k struct{ kind, dest string }
	exitKeys := map[k]int{}
	var exitOrder []k
	for _, x := range exit.Keys {
		id := k{x.Kind, x.Destination}
		if _, seen := exitKeys[id]; !seen {
			exitOrder = append(exitOrder, id)
		}
		exitKeys[id] += x.Quantity
	}
	done := map[k]bool{}
	entryKeys := map[k]int{}
	var entryOrder []k
	for _, x := range entry.Keys {
		id := k{x.Kind, x.Destination}
		if _, seen := entryKeys[id]; !seen {
			entryOrder = append(entryOrder, id)
		}
		entryKeys[id] += x.Quantity
	}
	for _, id := range entryOrder {
		r := KeyResult{Kind: id.kind, Destination: id.dest, Entry: entryKeys[id], Exit: exitKeys[id]}
		switch {
		case r.Exit < r.Entry:
			r.Result = Missing
			out.Flagged++
		case r.Exit > r.Entry:
			r.Result = Better
		default:
			r.Result = Same
		}
		done[id] = true
		out.Keys = append(out.Keys, r)
	}
	for _, id := range exitOrder {
		if !done[id] {
			out.Keys = append(out.Keys, KeyResult{Kind: id.kind, Destination: id.dest, Exit: exitKeys[id], Result: NewItem})
		}
	}
	return out
}
