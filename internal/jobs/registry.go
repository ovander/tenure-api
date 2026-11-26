package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// ErrChainMismatch reports a chain whose recomputed hashes differ from the
// stored ones.
var ErrChainMismatch = errors.New("audit chain mismatch")

func verifyChain(ctx context.Context, store document.Store) (any, error) {
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
	stored, err := verifyStoredHeads(ctx, store)
	if err != nil {
		return res, err
	}
	return map[string]any{"chain": res, "heads_checked": n, "stored_heads_checked": stored}, nil
}

// verifyStoredHeads reads back every version of every head object of the
// chain from the bucket, where audit.head wrote it under compliance lock,
// and checks it against the chain. It lists the bucket rather than the
// audit_chain_heads table, so that a head row deleted or rewritten in the
// database cannot hide an object from the check.
func verifyStoredHeads(ctx context.Context, store document.Store) (int, error) {
	sc, _ := db.ScopeFrom(ctx)
	prefix := "platform"
	if sc.OwnerID != uuid.Nil {
		prefix = sc.OwnerID.String()
	}
	versions, err := store.Versions(ctx, prefix+"/audit/")
	if err != nil {
		return 0, err
	}
	type stored struct {
		event int64
		hash  string
	}
	inBucket := map[string][]stored{}
	for _, v := range versions {
		obj, err := store.Open(ctx, v.Key, v.VersionID)
		if err != nil {
			return 0, err
		}
		var h struct {
			OwnerID     *string `json:"owner_id"`
			LastEventID int64   `json:"last_event_id"`
			HeadHash    string  `json:"head_hash"`
		}
		err = json.NewDecoder(io.LimitReader(obj, 1<<16)).Decode(&h)
		_ = obj.Close()
		owner := ""
		if h.OwnerID != nil {
			owner = *h.OwnerID
		}
		if err != nil || (sc.OwnerID != uuid.Nil && owner != sc.OwnerID.String()) || (sc.OwnerID == uuid.Nil && owner != "") {
			return 0, fmt.Errorf("%w: stored head %s (version %s) is not a head of this chain", ErrChainMismatch, v.Key, v.VersionID)
		}
		if err := audit.CheckStoredHead(ctx, h.LastEventID, h.HeadHash); err != nil {
			return 0, fmt.Errorf("%w: %s (version %s): %v", ErrChainMismatch, v.Key, v.VersionID, err)
		}
		inBucket[v.Key] = append(inBucket[v.Key], stored{h.LastEventID, h.HeadHash})
	}
	// Every head row has its locked object: a row whose object is missing
	// from the bucket is a checkpoint that can no longer be proved.
	rows, err := audit.HeadRows(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		found := false
		for _, s := range inBucket[r.ObjectKey] {
			if s.event == r.LastEventID && s.hash == r.HeadHash {
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("%w: stored head of %s missing from the bucket (%q)", ErrChainMismatch, r.AsOf.Format(time.DateOnly), r.ObjectKey)
		}
	}
	return len(versions), nil
}

// AuditVerify recomputes every owner's chain and the platform chain weekly and
// checks the daily heads against them, both the rows and the objects locked
// in the bucket (RENT-BE-001 §5, §8b); a mismatch fails that unit, so the run
// ends partial or failed, which alerts.
func AuditVerify(store document.Store) Job {
	return Job{
		Name:     "audit.verify",
		Schedule: "0 5 * * 0",
		Period:   WeekKey,
		PerOwner: func(ctx context.Context, _ Owner) (any, error) { return verifyChain(ctx, store) },
		Platform: func(ctx context.Context, _ Owner) (any, error) { return verifyChain(ctx, store) },
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

// VerifySample is how many archived objects archive.verify re-hashes per
// owner each week (an operational choice, not a legal constant, Q-94).
const VerifySample = 50

// ErrArchiveMismatch reports an archived object that differs from its hash.
var ErrArchiveMismatch = errors.New("archive mismatch")

// ArchiveRetention is archive.retention (RENT-BE-001 §5): on the 1st at 03:00,
// per owner, it starts the retention clock of the documents whose clock has
// started since (Q-68: a lease document once its lease ended, a supporting
// document from the end of its charges' exercise year), never earlier than
// the lock the object already carries, which is extended when the clock
// gives a later date, and lists the documents past retain_until for a
// manual review. It deletes nothing (RENT-SPEC-005, Q-95).
func ArchiveRetention(store document.Store, ps params.Repository) Job {
	return Job{
		Name:     "archive.retention",
		Schedule: "0 3 1 * *",
		Period:   MonthKey,
		PerOwner: func(ctx context.Context, o Owner) (any, error) {
			repo := document.Repository{}
			set, err := ps.Load(ctx)
			if err != nil {
				return nil, err
			}
			years, err := set.Int64(document.KeyArchiveRetention, o.Today)
			if err != nil {
				return nil, err
			}
			starts, err := repo.ClockStarts(ctx)
			if err != nil {
				return nil, err
			}
			started := 0
			for _, c := range starts {
				// retain_until is never earlier than the lock the object got
				// when archived; the lock is extended only when the clock
				// gives a later date (a compliance lock is never shortened).
				until := document.RetainUntil(c.From, years)
				locked := document.RetainUntil(c.IssuedAt.In(Paris()), years)
				extend := until.After(locked)
				if !extend {
					until = locked
				}
				if o.DryRun {
					started++
					continue
				}
				if extend {
					if err := store.ExtendRetention(ctx, c.ObjectKey, c.ObjectVersion, until.Time()); err != nil {
						return nil, err
					}
				}
				if err := repo.SetRetainUntil(ctx, c.ID, until.Time()); err != nil {
					return nil, err
				}
				started++
			}
			eligible, err := repo.PastRetention(ctx, o.Today.Time())
			if err != nil {
				return nil, err
			}
			return map[string]any{"clocks_started": started, "eligible_for_review": eligible, "deleted": 0}, nil
		},
	}
}

// ArchiveVerify is archive.verify (RENT-BE-001 §5): every Sunday at 04:00,
// per owner, it re-hashes a random sample of archived objects, and every
// document of the last eight days, against documents.sha256; a mismatch or a
// missing object fails that owner's unit, so the run ends partial or failed,
// which alerts. It also checks their compliance lock, placed after the commit
// (document.Archive), and places it again where it is missing ("relocked").
func ArchiveVerify(store document.Store, ps params.Repository, others ...document.RecentArchived) Job {
	return Job{
		Name:     "archive.verify",
		Schedule: "0 4 * * 0",
		Period:   WeekKey,
		PerOwner: func(ctx context.Context, o Owner) (any, error) {
			sample, err := document.Repository{}.Sample(ctx, VerifySample)
			if err != nil {
				return nil, err
			}
			// Every object of the last eight days is checked as well, the
			// documents and the other modules' objects (inventory photos):
			// its compliance lock is placed after its commit
			// (document.Archive), and one that failed is placed again here.
			since := o.Today.AddDays(-8).Time()
			recent, err := document.Repository{}.Recent(ctx, since)
			if err != nil {
				return nil, err
			}
			var objects []document.Archived
			for _, d := range append(sample, recent...) {
				objects = append(objects, document.Archived{ID: d.ID.String(), Key: d.ObjectKey, Version: d.ObjectVersion, SHA256: d.SHA256, CreatedAt: d.CreatedAt})
			}
			for _, list := range others {
				more, err := list(ctx, since)
				if err != nil {
					return nil, err
				}
				objects = append(objects, more...)
			}
			set, err := ps.Load(ctx)
			if err != nil {
				return nil, err
			}
			var bad, relocked []string
			seen := map[string]bool{}
			for _, d := range objects {
				if seen[d.ID] {
					continue
				}
				seen[d.ID] = true
				sum, err := hashObject(ctx, store, d.Key, d.Version)
				if err != nil || sum != d.SHA256 {
					bad = append(bad, d.ID)
					continue
				}
				created := d.CreatedAt.In(Paris())
				years, err := set.Int64(document.KeyArchiveRetention, period.FromTime(created))
				if err != nil {
					return nil, err
				}
				want := document.RetainUntil(created, years).Time()
				until, locked, err := store.Locked(ctx, d.Key, d.Version)
				if err != nil {
					return nil, err
				}
				if locked && !until.Before(want) {
					continue
				}
				if !o.DryRun {
					if err := store.ExtendRetention(ctx, d.Key, d.Version, want); err != nil {
						return nil, err
					}
				}
				relocked = append(relocked, d.ID)
			}
			res := map[string]any{"checked": len(seen), "mismatches": bad, "relocked": relocked}
			if len(bad) > 0 {
				return res, fmt.Errorf("%w: %d of %d objects", ErrArchiveMismatch, len(bad), len(seen))
			}
			return res, nil
		},
	}
}

func hashObject(ctx context.Context, store document.Store, key, version string) (string, error) {
	obj, err := store.Open(ctx, key, version)
	if err != nil {
		return "", err
	}
	defer func() { _ = obj.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, obj); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
