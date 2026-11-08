package lease

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/invariant"
)

type renewalRow struct {
	ID           uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID      uuid.UUID       `gorm:"column:owner_id"`
	LeaseID      uuid.UUID       `gorm:"column:lease_id"`
	RenewalOn    time.Time       `gorm:"column:renewal_on;type:date"`
	Status       string          `gorm:"column:status"`
	Proposal     json.RawMessage `gorm:"column:proposal;type:jsonb"`
	Terms        json.RawMessage `gorm:"column:terms;type:jsonb"`
	DocumentID   uuid.UUID       `gorm:"column:document_id"`
	ReceivedOn   *time.Time      `gorm:"column:received_on;type:date"`
	Delivery     *string         `gorm:"column:delivery"`
	TrackingRef  string          `gorm:"column:tracking_ref"`
	AnsweredOn   *time.Time      `gorm:"column:answered_on;type:date"`
	StepsApplied int             `gorm:"column:steps_applied"`
	CreatedAt    time.Time       `gorm:"column:created_at"`
	CreatedBy    string          `gorm:"column:created_by"`
	UpdatedAt    time.Time       `gorm:"column:updated_at"`
	UpdatedBy    string          `gorm:"column:updated_by"`
	Version      int             `gorm:"column:version"`
}

func (renewalRow) TableName() string { return "lease_renewals" }

// Renewals returns a lease's re-evaluation proposals, the latest renewal
// first.
func (Repository) Renewals(ctx context.Context, leaseID uuid.UUID) ([]renewalRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []renewalRow
	err = t.Where("lease_id = ?", leaseID).Order("renewal_on DESC, created_at DESC").Find(&rows).Error
	return rows, err
}

// Renewal returns one proposal of a lease, locked for update when lock is
// set.
func (Repository) Renewal(ctx context.Context, leaseID, id uuid.UUID, lock bool) (renewalRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return renewalRow{}, err
	}
	q := t.Where("lease_id = ? AND id = ?", leaseID, id)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []renewalRow
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return renewalRow{}, err
	}
	if len(rows) == 0 {
		return renewalRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// RenewalFor returns the proposal of a lease for a renewal day, if any,
// locked for update.
func (Repository) RenewalFor(ctx context.Context, leaseID uuid.UUID, renewalOn time.Time) (renewalRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return renewalRow{}, false, err
	}
	var rows []renewalRow
	err = t.Where("lease_id = ? AND renewal_on = ?", leaseID, renewalOn).Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return renewalRow{}, false, err
	}
	return rows[0], true, nil
}

// InsertRenewal records a proposal.
func (Repository) InsertRenewal(ctx context.Context, r renewalRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// UpdateRenewal saves a proposal when its version is the one read, bumping
// it.
func (Repository) UpdateRenewal(ctx context.Context, r renewalRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&renewalRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"status": r.Status, "proposal": r.Proposal, "terms": r.Terms, "document_id": r.DocumentID,
		"received_on": r.ReceivedOn, "delivery": r.Delivery, "tracking_ref": r.TrackingRef, "answered_on": r.AnsweredOn,
		"steps_applied": r.StepsApplied, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy, "version": version + 1,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}
