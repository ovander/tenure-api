package billing

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/invariant"
)

type recoveryRow struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID          uuid.UUID  `gorm:"column:owner_id"`
	LeaseID          uuid.UUID  `gorm:"column:lease_id"`
	Kind             string     `gorm:"column:kind"`
	DoneOn           time.Time  `gorm:"column:done_on;type:date"`
	OutstandingCents int64      `gorm:"column:outstanding_cents"`
	GuaranteeID      *uuid.UUID `gorm:"column:guarantee_id"`
	DocumentID       *uuid.UUID `gorm:"column:document_id"`
	Delivery         *string    `gorm:"column:delivery"`
	TrackingRef      string     `gorm:"column:tracking_ref"`
	DeadlineOn       *time.Time `gorm:"column:deadline_on;type:date"`
	Note             string     `gorm:"column:note"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	CreatedBy        string     `gorm:"column:created_by"`
}

func (recoveryRow) TableName() string { return "recovery_steps" }

// RecoverySteps returns a lease's recovery steps, oldest first.
func (Repository) RecoverySteps(ctx context.Context, leaseID uuid.UUID) ([]recoveryRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []recoveryRow
	err = t.Where("lease_id = ?", leaseID).Order("done_on, created_at").Find(&rows).Error
	return rows, err
}

// InsertRecoveryStep records a recovery step.
func (Repository) InsertRecoveryStep(ctx context.Context, r recoveryRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// RecoveryDocument says whether an archive document of class recovery is
// linked to the lease, and returns its number series (MD, IC) when tenure
// prepared it, or "upload" for one uploaded.
func (Repository) RecoveryDocument(ctx context.Context, leaseID, documentID uuid.UUID) (string, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return "", false, err
	}
	var rows []struct {
		ObjectKey string `gorm:"column:object_key"`
		Source    string `gorm:"column:source"`
	}
	err = t.Table("documents").Select("object_key, source").
		Where("id = ? AND lease_id = ? AND class = 'recovery'", documentID, leaseID).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	if rows[0].Source == "upload" {
		return "upload", true, nil
	}
	series, _, _ := strings.Cut(path.Base(rows[0].ObjectKey), "-")
	return series, true, nil
}

type planRow struct {
	ID          uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID       `gorm:"column:owner_id"`
	LeaseID     uuid.UUID       `gorm:"column:lease_id"`
	AgreedOn    time.Time       `gorm:"column:agreed_on;type:date"`
	DebtCents   int64           `gorm:"column:debt_cents"`
	Calls       json.RawMessage `gorm:"column:calls;type:jsonb"`
	Instalments json.RawMessage `gorm:"column:instalments;type:jsonb"`
	DocumentID  *uuid.UUID      `gorm:"column:document_id"`
	CancelledOn *time.Time      `gorm:"column:cancelled_on;type:date"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	CreatedBy   string          `gorm:"column:created_by"`
	UpdatedAt   time.Time       `gorm:"column:updated_at"`
	UpdatedBy   string          `gorm:"column:updated_by"`
	Version     int             `gorm:"column:version"`
}

func (planRow) TableName() string { return "payment_plans" }

// Plans returns a lease's payment plans, the latest agreed first.
func (Repository) Plans(ctx context.Context, leaseID uuid.UUID) ([]planRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []planRow
	err = t.Where("lease_id = ?", leaseID).Order("agreed_on DESC, created_at DESC").Find(&rows).Error
	return rows, err
}

// InsertPlan records a payment plan.
func (Repository) InsertPlan(ctx context.Context, r planRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// CancelPlan sets a plan's cancellation day when its version is the one
// read, bumping it.
func (Repository) CancelPlan(ctx context.Context, id uuid.UUID, version int, on, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&planRow{}).Where("id = ? AND version = ? AND cancelled_on IS NULL", id, version).
		Updates(map[string]any{"cancelled_on": on, "updated_at": at, "updated_by": by, "version": version + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}
