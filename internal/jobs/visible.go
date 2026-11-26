package jobs

import (
	"encoding/json"

	"github.com/google/uuid"
)

// Visible returns run as the caller may see it. A run of an owner scope is
// only listed to that scope (row-level security), and an admin sees every run
// whole. A platform run (no owner), which row-level security shows to every
// caller, carries the result of each owner it iterated: the caller keeps only
// the result of their active scope (none without one), the platform unit's
// outcome, and the counts and status of what they keep, so that one owner
// never reads another's documents, leases or amounts.
func Visible(run Run, active *uuid.UUID, admin bool) Run {
	if admin || run.OwnerID != nil {
		return run
	}
	var s struct {
		Platform *ownerResult  `json:"platform"`
		Owners   []ownerResult `json:"owners"`
	}
	out := map[string]any{}
	units, failures := 0, 0
	if err := json.Unmarshal(run.Summary, &s); err == nil {
		if s.Platform != nil {
			units++
			if !s.Platform.OK {
				failures++
			}
			out["platform"] = map[string]any{"ok": s.Platform.OK}
		}
		mine := []ownerResult{}
		if active != nil {
			for _, o := range s.Owners {
				if o.OwnerID == active.String() {
					mine = append(mine, o)
					units++
					if !o.OK {
						failures++
					}
				}
			}
		}
		if s.Owners != nil {
			out["owners"] = mine
		}
	}
	out["units"], out["failures"] = units, failures
	run.Summary, _ = json.Marshal(out)
	// A run that never reached its units (its check or the owner list
	// failed) keeps its status; the reason, which may name data, is dropped.
	if units > 0 || s.Owners != nil {
		switch {
		case units > 0 && failures == units:
			run.Status = StatusFailed
		case failures > 0:
			run.Status = StatusPartial
		default:
			run.Status = StatusSucceeded
		}
	}
	return run
}
