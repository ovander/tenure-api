package document

import (
	"context"
	"log/slog"
	"time"

	"github.com/ovander/tenure-api/internal/db"
)

// lockAttempts and lockBackoff bound the retries of a lock placed after the
// commit (a passing outage of the object store).
var (
	lockAttempts = 3
	lockBackoff  = time.Second
)

// Archived is an object archived by another module (an inventory photo),
// checked and relocked by archive.verify like a document.
type Archived struct {
	ID, Key, Version, SHA256 string
	CreatedAt                time.Time
}

// RecentArchived lists a module's objects archived since a time, in the
// owner scope of the transaction.
type RecentArchived func(ctx context.Context, since time.Time) ([]Archived, error)

// Archive writes data under key in the caller's scoped transaction and
// returns the object version. The object is written without retention and
// locked in compliance mode until retainUntil once the transaction commits;
// if it rolls back, the object is removed. A rollback therefore leaves no
// locked object behind: no PDF under a document number given back to the
// counter, no file without its row (RENT-BE-001 §6, #226).
//
// A lock that still fails after its retries leaves the committed document
// unlocked; it is logged as an error, which raises the alert (threat model
// A-1), and archive.verify locks it again (documents and inventory
// photos).
func Archive(ctx context.Context, store Store, key string, data []byte, contentType, sha256hex string, retainUntil time.Time) (string, error) {
	version, err := store.Stage(ctx, key, data, contentType, sha256hex)
	if err != nil {
		return "", err
	}
	if err := db.OnRollback(ctx, func(ctx context.Context) {
		if err := store.Delete(ctx, key, version); err != nil {
			slog.ErrorContext(ctx, "archive: object of a rolled-back transaction not removed", "key", key, "version", version, "error", err)
		}
	}); err != nil {
		_ = store.Delete(ctx, key, version)
		return "", err
	}
	if err := db.AfterCommit(ctx, func(ctx context.Context) {
		var err error
		for i := 0; i < lockAttempts; i++ {
			if i > 0 {
				time.Sleep(lockBackoff * time.Duration(i))
			}
			if err = store.ExtendRetention(ctx, key, version, retainUntil); err == nil {
				return
			}
		}
		slog.ErrorContext(ctx, "archive: committed object left without its compliance lock", "key", key, "version", version, "error", err)
	}); err != nil {
		return "", err
	}
	return version, nil
}
