package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/params"
)

// ErrChainMismatch reports a chain whose recomputed hashes differ from the
// stored ones.
var ErrChainMismatch = errors.New("audit chain mismatch")

func verifyChain(ctx context.Context) (any, error) {
	res, err := audit.Verify(ctx)
	if err != nil {
		return nil, err
	}
	if !res.OK {
		return res, fmt.Errorf("%w at event %d: %s", ErrChainMismatch, res.BadID, res.Reason)
	}
	n, err := audit.VerifyHeads(ctx)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrChainMismatch, err)
	}
	return map[string]any{"chain": res, "heads_checked": n}, nil
}

// AuditVerify recomputes every owner's chain and the platform chain weekly and
// checks the stored daily heads against them (RENT-BE-001 §5, §8b); a mismatch
// fails that unit, so the run ends partial or failed, which alerts.
func AuditVerify() Job {
	return Job{
		Name:     "audit.verify",
		Schedule: "0 5 * * 0",
		Period:   WeekKey,
		PerOwner: func(ctx context.Context, _ Owner) (any, error) { return verifyChain(ctx) },
		Platform: func(ctx context.Context, _ Owner) (any, error) { return verifyChain(ctx) },
	}
}

// AuditHead writes each chain's head daily to <owner_id>/audit/<date>.json
// (platform/audit/<date>.json for the platform chain) under compliance lock,
// and records it in audit_chain_heads (RENT-BE-001 §5, §8b).
func AuditHead(store document.Store, ps params.Repository) Job {
	write := func(ctx context.Context, o Owner) (any, error) {
		id, hash, ok, err := audit.Head(ctx)
		if err != nil || !ok {
			return map[string]any{"events": 0}, err
		}
		set, err := ps.Load(ctx)
		if err != nil {
			return nil, err
		}
		years, err := set.Int64(document.KeyArchiveRetention, o.Today)
		if err != nil {
			return nil, err
		}
		sc, _ := db.ScopeFrom(ctx)
		prefix := "platform"
		var owner any
		if o.ID != [16]byte{} {
			prefix, owner = o.ID.String(), o.ID.String()
		}
		key := fmt.Sprintf("%s/audit/%s.json", prefix, o.Today)
		body, _ := json.Marshal(map[string]any{"owner_id": owner, "as_of": o.Today.String(), "last_event_id": id, "head_hash": hash})
		if o.DryRun {
			return map[string]any{"object_key": key, "head_hash": hash}, nil
		}
		retain := o.Today.AddMonths(int(12 * years)).Time()
		if _, err := store.Put(ctx, key, body, "application/json", document.SHA256(body), retain); err != nil {
			return nil, err
		}
		h := audit.ChainHead{AsOf: o.Today.Time(), LastEventID: id, HeadHash: hash, ObjectKey: key}
		if sc.OwnerID != [16]byte{} {
			oid := sc.OwnerID
			h.OwnerID = &oid
		}
		if err := audit.InsertHead(ctx, h); err != nil {
			return nil, err
		}
		return map[string]any{"object_key": key, "last_event_id": id, "head_hash": hash}, nil
	}
	return Job{
		Name:     "audit.head",
		Schedule: "0 2 * * *",
		Period:   DayKey,
		PerOwner: write,
		Platform: write,
	}
}
