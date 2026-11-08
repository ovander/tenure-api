package lease

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type noticeRow struct {
	ID          uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID       `gorm:"column:owner_id"`
	LeaseID     uuid.UUID       `gorm:"column:lease_id"`
	GivenBy     string          `gorm:"column:given_by"`
	Reason      string          `gorm:"column:reason"`
	ReceivedOn  time.Time       `gorm:"column:received_on;type:date"`
	Months      int             `gorm:"column:months"`
	EndOn       time.Time       `gorm:"column:end_on;type:date"`
	Delivery    string          `gorm:"column:delivery"`
	SentOn      *time.Time      `gorm:"column:sent_on;type:date"`
	TrackingRef string          `gorm:"column:tracking_ref"`
	Ground      json.RawMessage `gorm:"column:ground;type:jsonb"`
	DocumentID  *uuid.UUID      `gorm:"column:document_id"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	CreatedBy   string          `gorm:"column:created_by"`
}

func (noticeRow) TableName() string { return "lease_notices" }

// Notice returns a lease's notice, if one is recorded.
func (Repository) Notice(ctx context.Context, leaseID uuid.UUID) (noticeRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return noticeRow{}, false, err
	}
	var rows []noticeRow
	if err := t.Where("lease_id = ?", leaseID).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return noticeRow{}, false, err
	}
	return rows[0], true, nil
}

// InsertNotice records a notice.
func (Repository) InsertNotice(ctx context.Context, r noticeRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// LeaseDocumentOf returns the lease document of a kind that holds an archive
// document, if it belongs to the lease.
func (Repository) LeaseDocumentOf(ctx context.Context, leaseID, documentID uuid.UUID, kind string) (leaseDocumentRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return leaseDocumentRow{}, false, err
	}
	var rows []leaseDocumentRow
	err = t.Where("lease_id = ? AND document_id = ? AND kind = ?", leaseID, documentID, kind).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return leaseDocumentRow{}, false, err
	}
	return rows[0], true, nil
}

// UploadOnLease reports whether an archive document is an upload linked to
// the lease.
func (Repository) UploadOnLease(ctx context.Context, leaseID, documentID uuid.UUID) (bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Table("documents").Where("id = ? AND lease_id = ? AND source = 'upload'", documentID, leaseID).Count(&n).Error
	return n > 0, err
}

type occupancyRow struct {
	StartOn time.Time  `gorm:"column:start_on"`
	EndOn   *time.Time `gorm:"column:end_on"`
}

// Occupancies returns the start and end of every lease of an asset that is
// not a draft.
func (Repository) Occupancies(ctx context.Context, asset uuid.UUID) ([]occupancyRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []occupancyRow
	err = t.Table("leases l").Select("l.start_on, l.end_on").Joins("JOIN lease_assets la ON la.lease_id = l.id").
		Where("la.asset_id = ? AND l.state <> 'draft'", asset).Order("l.start_on, l.id").Find(&rows).Error
	return rows, err
}
