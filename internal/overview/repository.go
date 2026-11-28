package overview

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/period"
)

// Repository reads the cockpit's facts in the caller's scoped transaction
// (row-level security limits every query to the scope). It only reads.
type Repository struct{}

// LeaseAssets returns each lease's main asset: the dwelling before a box,
// as billing orders them.
func (Repository) LeaseAssets(ctx context.Context) (map[uuid.UUID]uuid.UUID, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		LeaseID uuid.UUID
		AssetID uuid.UUID
	}
	if err := t.Raw(`SELECT DISTINCT ON (la.lease_id) la.lease_id, la.asset_id FROM lease_assets la
		JOIN assets a ON a.id = la.asset_id ORDER BY la.lease_id, (a.kind = 'box'), a.id`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, r := range rows {
		out[r.LeaseID] = r.AssetID
	}
	return out, nil
}

// CallState is when a rent call became fully paid (its last match) and
// whether its quittance is issued.
type CallState struct {
	ID        uuid.UUID
	LastMatch *time.Time
	Receipted bool
}

// CallStates returns the state of the given rent calls.
func (Repository) CallStates(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]CallState, error) {
	out := map[uuid.UUID]CallState{}
	if len(ids) == 0 {
		return out, nil
	}
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []CallState
	if err := t.Raw(`SELECT d.id,
		(SELECT max(m.created_at) FROM payment_matches m WHERE m.billing_document_id = d.id) AS last_match,
		EXISTS (SELECT 1 FROM billing_documents r WHERE r.kind = 'receipt' AND r.input_snapshot ->> 'rent_call_id' = d.id::text) AS receipted
		FROM billing_documents d WHERE d.id IN ?`, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// BankRow is an unmatched bank line.
type BankRow struct {
	ID          uuid.UUID
	BookedOn    time.Time
	AmountCents int64
}

// BankState returns the number of accounts, the latest booking day of an
// imported line and the unmatched lines, oldest first.
func (Repository) BankState(ctx context.Context) (int, *time.Time, []BankRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	var head []struct {
		Accounts int
		Last     *time.Time
	}
	if err := t.Raw(`SELECT (SELECT count(*) FROM bank_accounts) AS accounts, (SELECT max(booked_on) FROM bank_lines) AS last`).
		Scan(&head).Error; err != nil {
		return 0, nil, nil, err
	}
	var lines []BankRow
	if err := t.Raw(`SELECT id, booked_on, amount_cents FROM bank_lines WHERE status = 'unmatched' ORDER BY booked_on, id`).
		Scan(&lines).Error; err != nil {
		return 0, nil, nil, err
	}
	if len(head) == 0 {
		return 0, nil, lines, nil
	}
	return head[0].Accounts, head[0].Last, lines, nil
}

// RemindersSent counts the rent reminders sent from first to last.
func (Repository) RemindersSent(ctx context.Context, first, last period.Date) (int, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	var n int64
	err = t.Raw(`SELECT count(*) FROM billing_reminders WHERE sent_on BETWEEN ? AND ?`, first.Time(), last.Time()).Scan(&n).Error
	return int(n), err
}

// FinishedRuns returns the job runs (not dry) finished since since, latest
// first: the scope's and the platform's, as row-level security shows them.
func (Repository) FinishedRuns(ctx context.Context, since time.Time) ([]jobs.Run, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var runs []jobs.Run
	err = t.Where("NOT dry_run AND finished_at IS NOT NULL AND finished_at >= ?", since).Order("finished_at DESC, id").Find(&runs).Error
	return runs, err
}

// Ran says a run of job for period p (not dry) has finished.
func (Repository) Ran(ctx context.Context, job, p string) (bool, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Model(&jobs.Run{}).Where("job = ? AND period = ? AND NOT dry_run AND finished_at IS NOT NULL", job, p).Count(&n).Error
	return n > 0, err
}

// ExerciseRow is an exercise of an asset to regularise.
type ExerciseRow struct {
	AssetID      uuid.UUID
	ExerciseYear int
	EndOn        time.Time
}

// OpenExercises returns the exercises of an asset let during them (a lease
// past its draft overlapping the year) that have recoverable charges
// confirmed and no issued or settled reconciliation: the reconciliation's
// end when one is started, else 31 December of the exercise year (Q-139).
func (Repository) OpenExercises(ctx context.Context) ([]ExerciseRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []ExerciseRow
	err = t.Raw(`SELECT c.asset_id, c.exercise_year,
		coalesce((SELECT max(r.exercise_end) FROM reconciliations r WHERE r.asset_id = c.asset_id
			AND r.exercise_year = c.exercise_year AND r.state <> 'cancelled'), make_date(c.exercise_year, 12, 31)) AS end_on
		FROM (SELECT DISTINCT asset_id, exercise_year FROM charges WHERE status = 'confirmed' AND recoverable_cents > 0) c
		WHERE NOT EXISTS (SELECT 1 FROM reconciliations r WHERE r.asset_id = c.asset_id AND r.exercise_year = c.exercise_year
			AND r.state IN ('issued', 'settled'))
		AND EXISTS (SELECT 1 FROM lease_assets la JOIN leases l ON l.id = la.lease_id WHERE la.asset_id = c.asset_id
			AND l.state <> 'draft' AND l.start_on <= make_date(c.exercise_year, 12, 31)
			AND (l.end_on IS NULL OR l.end_on >= make_date(c.exercise_year, 1, 1)))
		ORDER BY end_on, c.asset_id`).Scan(&rows).Error
	return rows, err
}

// LeaseRow is a lease and its start.
type LeaseRow struct {
	ID      uuid.UUID
	StartOn time.Time
}

// ActiveWithoutEntry returns the active leases without an issued entry
// inventory.
func (Repository) ActiveWithoutEntry(ctx context.Context) ([]LeaseRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []LeaseRow
	err = t.Raw(`SELECT l.id, l.start_on FROM leases l WHERE l.state = 'active'
		AND NOT EXISTS (SELECT 1 FROM inventories i WHERE i.lease_id = l.id AND i.kind = 'entry' AND i.state = 'issued')
		ORDER BY l.start_on, l.id`).Scan(&rows).Error
	return rows, err
}
