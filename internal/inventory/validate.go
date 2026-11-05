package inventory

// Stage is the transition a check blocks: complete (the content) or issue
// (the signatures and the exit mentions, checked again at issue).
type Stage string

// Stages.
const (
	StageComplete Stage = "complete"
	StageIssue    Stage = "issue"
)

// Check is one broken mandatory mention or rule (décret 2016-382, RENT-INV-001
// §2 and §4), or a warning. Code is stable; Room and Item name where.
type Check struct {
	Code  string `json:"code"`
	Stage Stage  `json:"stage,omitempty"`
	Room  string `json:"room,omitempty"`
	Item  string `json:"item,omitempty"`
	Party string `json:"party,omitempty"`
}

// Checks are the blocking checks and the warnings of an inventory.
type Checks struct {
	Blocking []Check `json:"blocking"`
	Warnings []Check `json:"warnings"`
}

// Blocks reports whether a check of stage blocks; an issue is blocked by every
// blocking check.
func (c Checks) Blocks(s Stage) bool {
	for _, x := range c.Blocking {
		if s == StageIssue || x.Stage == s {
			return true
		}
	}
	return false
}

// Check codes.
const (
	CodeCoveringMissing   = "covering-not-assessed"
	CodeItemNotAssessed   = "item-not-assessed"
	CodeDamageUnexplained = "damage-unexplained"
	CodeNoKey             = "no-key-line"
	CodeNoParty           = "no-party"
	CodeNotSigned         = "party-not-signed"
	CodeNewAddressMissing = "exit-new-address-missing"
	CodeKeysReturnMissing = "exit-keys-returned-missing"
	CodeEntryDateMissing  = "exit-entry-date-missing"
	CodeDoneOnMissing     = "done-on-missing"
	CodeFurnitureMissing  = "furniture-missing"
)

// OutsideKind is the room kind of outdoor parts (garden, façades, gate):
// not a room, so the three coverings are not required there.
const OutsideKind = "outside"

// Validate returns the checks of inv, in a stable order:
//   - complete: the date; every item assessed on the scale; in every room but
//     the outdoor parts the floor, walls and ceiling assessed; a poor or broken item with a comment
//     or a photo; at least one key line;
//   - issue: at least one party, every party signed; for an exit, the
//     tenant's new address, the return of the keys and the entry's date;
//   - warning: a furnished entry with a furniture item missing (décret
//     2015-981: the lease could lose its furnished classification).
func Validate(inv Inventory) Checks {
	out := Checks{Blocking: []Check{}, Warnings: []Check{}}
	block := func(s Stage, code, room, item, party string) {
		out.Blocking = append(out.Blocking, Check{Code: code, Stage: s, Room: room, Item: item, Party: party})
	}
	if inv.DoneOn == nil {
		block(StageComplete, CodeDoneOnMissing, "", "", "")
	}
	for _, r := range inv.Rooms {
		if r.Kind == OutsideKind {
			continue
		}
		for _, c := range coverings {
			ok := false
			for _, it := range inv.Items {
				if it.RoomID == r.ID && it.Category == c && it.Condition.Valid() {
					ok = true
					break
				}
			}
			if !ok {
				block(StageComplete, CodeCoveringMissing, r.Label, string(c), "")
			}
		}
	}
	for _, it := range inv.Items {
		switch {
		case !it.Condition.Valid():
			block(StageComplete, CodeItemNotAssessed, it.RoomLabel, it.Label, "")
		case (it.Condition == Poor || it.Condition == Broken) && it.Comment == "" && it.Photos == 0:
			block(StageComplete, CodeDamageUnexplained, it.RoomLabel, it.Label, "")
		}
		if inv.Kind == Entry && inv.Furnished && it.Category == Furniture && (it.Quantity == 0 || it.Condition == Broken) {
			out.Warnings = append(out.Warnings, Check{Code: CodeFurnitureMissing, Room: it.RoomLabel, Item: it.Label})
		}
	}
	keys := 0
	for _, k := range inv.Keys {
		if k.Quantity > 0 {
			keys++
		}
	}
	if keys == 0 {
		block(StageComplete, CodeNoKey, "", "", "")
	}
	if len(inv.Parties) == 0 {
		block(StageIssue, CodeNoParty, "", "", "")
	}
	for _, p := range inv.Parties {
		if !p.Signed {
			block(StageIssue, CodeNotSigned, "", "", p.Name)
		}
	}
	if inv.Kind == Exit {
		if inv.TenantNewAddress == "" {
			block(StageIssue, CodeNewAddressMissing, "", "", "")
		}
		if inv.KeysReturnedOn == nil {
			block(StageIssue, CodeKeysReturnMissing, "", "", "")
		}
		if inv.EntryDoneOn == nil {
			block(StageIssue, CodeEntryDateMissing, "", "", "")
		}
	}
	return out
}
