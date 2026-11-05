package inventory

// Prefill builds the draft that follows source (RENT-INV-001 §5):
//   - from an entry, the exit: every entry item, corrected by the accepted
//     completion lines, linked by EntryItemID and marked to check, so the
//     owner only touches what changed; keys are copied as they were handed
//     over, to be counted again; meters keep their kind and number, without a
//     reading;
//   - from the last exit, the next lease's entry: the exit conditions, with no
//     link to the previous lease and nothing marked to check.
//
// id is the new inventory's id; item ids are derived from it.
func Prefill(id string, source Inventory, completions []Completion) Inventory {
	next := Entry
	if source.Kind == Entry {
		next = Exit
	}
	out := Inventory{ID: id, Kind: next, Furnished: source.Furnished, Rooms: append([]Room{}, source.Rooms...),
		Items: []Item{}, Keys: []Key{}}
	if next == Exit {
		out.EntryDoneOn = source.DoneOn
	}
	accepted := acceptedLines(completions)
	for _, it := range source.Items {
		n := it
		n.ID = id + "/" + it.ElementIDOr()
		n.Photos = 0
		if next == Exit {
			n = applyCompletion(n, accepted[it.ID])
			n.EntryItemID = it.ID
			n.ToCheck = true
		} else {
			n.EntryItemID = ""
			n.ToCheck = false
		}
		out.Items = append(out.Items, n)
	}
	out.Keys = append(out.Keys, source.Keys...)
	for _, m := range source.Meters {
		out.Meters = append(out.Meters, Meter{Kind: m.Kind, MeterNumber: m.MeterNumber, Unit: m.Unit})
	}
	return out
}

// ElementIDOr is the element id of an item, or its own id for an item added
// on site, which has no element.
func (it Item) ElementIDOr() string {
	if it.ElementID != "" {
		return it.ElementID
	}
	return it.ID
}

func acceptedLines(cs []Completion) map[string]Completion {
	out := map[string]Completion{}
	for _, c := range cs {
		if c.Accepted {
			out[c.EntryItemID] = c
		}
	}
	return out
}

// applyCompletion corrects an entry item with an accepted completion line:
// the issued entry is never edited, so the corrected state lives only in the
// comparison and the exit pre-fill.
func applyCompletion(it Item, c Completion) Item {
	if c.EntryItemID == "" {
		return it
	}
	if c.Condition != "" {
		it.Condition = c.Condition
	}
	if c.Works != "" {
		it.Works = c.Works
	}
	if c.Comment != "" {
		it.Comment = c.Comment
	}
	return it
}
