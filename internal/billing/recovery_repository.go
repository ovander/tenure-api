package billing

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
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
