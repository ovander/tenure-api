package lease

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type leaseRow struct {
	ID             uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID        uuid.UUID       `gorm:"column:owner_id"`
	Kind           string          `gorm:"column:kind"`
	State          string          `gorm:"column:state"`
	StartOn        time.Time       `gorm:"column:start_on"`
	EndOn          *time.Time      `gorm:"column:end_on"`
	TermMonths     int             `gorm:"column:term_months"`
	RentCents      int64           `gorm:"column:rent_cents"`
	ProvisionCents int64           `gorm:"column:provision_cents"`
	DepositCents   int64           `gorm:"column:deposit_cents"`
	DueDay         int             `gorm:"column:due_day"`
	IRLRefQuarter  string          `gorm:"column:irl_ref_quarter"`
	IRLRefValue    int64           `gorm:"column:irl_ref_value"`
	RevisionMonth  int             `gorm:"column:revision_month"`
	RentExemption  *string         `gorm:"column:rent_exemption"`
	RentBreakdown  json.RawMessage `gorm:"column:rent_breakdown;type:jsonb"`
	Clauses        json.RawMessage `gorm:"column:clauses;type:jsonb"`
	MobilityReason *string         `gorm:"column:mobility_reason"`
	Version        int             `gorm:"column:version"`
	CreatedAt      time.Time       `gorm:"column:created_at"`
	CreatedBy      string          `gorm:"column:created_by"`
	UpdatedAt      time.Time       `gorm:"column:updated_at"`
	UpdatedBy      string          `gorm:"column:updated_by"`
}

func (leaseRow) TableName() string { return "leases" }

type leaseAssetRow struct {
	LeaseID uuid.UUID `gorm:"column:lease_id;primaryKey"`
	AssetID uuid.UUID `gorm:"column:asset_id;primaryKey"`
	OwnerID uuid.UUID `gorm:"column:owner_id"`
}

func (leaseAssetRow) TableName() string { return "lease_assets" }

type leaseTenantRow struct {
	LeaseID  uuid.UUID `gorm:"column:lease_id;primaryKey"`
	TenantID uuid.UUID `gorm:"column:tenant_id;primaryKey"`
	OwnerID  uuid.UUID `gorm:"column:owner_id"`
	Solidary bool      `gorm:"column:solidary"`
}

func (leaseTenantRow) TableName() string { return "lease_tenants" }

type guaranteeRow struct {
	ID          uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID       `gorm:"column:owner_id"`
	LeaseID     uuid.UUID       `gorm:"column:lease_id"`
	Kind        string          `gorm:"column:kind"`
	Guarantor   json.RawMessage `gorm:"column:guarantor;type:jsonb"`
	AmountCents int64           `gorm:"column:amount_cents"`
	ValidFrom   *time.Time      `gorm:"column:valid_from"`
	ValidTo     *time.Time      `gorm:"column:valid_to"`
	Status      string          `gorm:"column:status"`
	DocumentID  *uuid.UUID      `gorm:"column:document_id"`
	Version     int             `gorm:"column:version"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	CreatedBy   string          `gorm:"column:created_by"`
	UpdatedAt   time.Time       `gorm:"column:updated_at"`
	UpdatedBy   string          `gorm:"column:updated_by"`
}

func (guaranteeRow) TableName() string { return "guarantees" }

// stored is a lease with its links.
type stored struct {
	lease   leaseRow
	assets  []leaseAssetRow
	tenants []leaseTenantRow
}

// Repository stores leases, their links and guarantees, under the caller's
// row-level-security scope.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

// Filter narrows a lease list.
type Filter struct {
	State   string
	AssetID *uuid.UUID
}

// List returns a page of leases and the total.
func (r Repository) List(ctx context.Context, f Filter, offset, limit int) ([]stored, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&leaseRow{})
	if f.State != "" {
		q = q.Where("state = ?", f.State)
	}
	if f.AssetID != nil {
		q = q.Where("id IN (SELECT lease_id FROM lease_assets WHERE asset_id = ?)", *f.AssetID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []leaseRow
	if err := q.Order("start_on DESC, id").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out, err := r.links(ctx, rows)
	return out, int(total), err
}

func (Repository) links(ctx context.Context, rows []leaseRow) ([]stored, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, l := range rows {
		ids[i] = l.ID
	}
	var as []leaseAssetRow
	var ts []leaseTenantRow
	if len(ids) > 0 {
		if err := t.Where("lease_id IN ?", ids).Order("asset_id").Find(&as).Error; err != nil {
			return nil, err
		}
		if err := t.Where("lease_id IN ?", ids).Order("tenant_id").Find(&ts).Error; err != nil {
			return nil, err
		}
	}
	out := make([]stored, len(rows))
	for i, l := range rows {
		out[i].lease = l
		for _, a := range as {
			if a.LeaseID == l.ID {
				out[i].assets = append(out[i].assets, a)
			}
		}
		for _, x := range ts {
			if x.LeaseID == l.ID {
				out[i].tenants = append(out[i].tenants, x)
			}
		}
	}
	return out, nil
}

// Get returns a lease, locking its row when forUpdate.
func (r Repository) Get(ctx context.Context, id uuid.UUID, forUpdate bool) (stored, error) {
	t, err := tx(ctx)
	if err != nil {
		return stored{}, err
	}
	q := t.Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []leaseRow
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return stored{}, err
	}
	if len(rows) == 0 {
		return stored{}, invariant.ErrNotFound
	}
	out, err := r.links(ctx, rows)
	if err != nil {
		return stored{}, err
	}
	return out[0], nil
}

// Insert creates a lease and its links.
func (Repository) Insert(ctx context.Context, s stored) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Create(&s.lease).Error; err != nil {
		return err
	}
	if err := t.Create(&s.assets).Error; err != nil {
		return err
	}
	return t.Create(&s.tenants).Error
}

// Update replaces a lease's terms and links when version matches.
func (Repository) Update(ctx context.Context, s stored, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	l := s.lease
	res := t.Model(&leaseRow{}).Where("id = ? AND version = ?", l.ID, version).Updates(map[string]any{
		"kind": l.Kind, "start_on": l.StartOn, "end_on": l.EndOn, "term_months": l.TermMonths,
		"rent_cents": l.RentCents, "provision_cents": l.ProvisionCents, "deposit_cents": l.DepositCents,
		"due_day": l.DueDay, "irl_ref_quarter": l.IRLRefQuarter, "irl_ref_value": l.IRLRefValue,
		"revision_month": l.RevisionMonth, "rent_exemption": l.RentExemption, "rent_breakdown": l.RentBreakdown,
		"clauses": l.Clauses, "mobility_reason": l.MobilityReason, "version": version + 1, "updated_at": l.UpdatedAt, "updated_by": l.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	if err := t.Where("lease_id = ?", l.ID).Delete(&leaseAssetRow{}).Error; err != nil {
		return err
	}
	if err := t.Where("lease_id = ?", l.ID).Delete(&leaseTenantRow{}).Error; err != nil {
		return err
	}
	if err := t.Create(&s.assets).Error; err != nil {
		return err
	}
	return t.Create(&s.tenants).Error
}

// SetState records a transition, bumping the version.
func (Repository) SetState(ctx context.Context, l leaseRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Model(&leaseRow{}).Where("id = ?", l.ID).Updates(map[string]any{
		"state": l.State, "end_on": l.EndOn, "version": l.Version, "updated_at": l.UpdatedAt, "updated_by": l.UpdatedBy,
	}).Error
}

// LockAssets takes a row lock on the assets, serialising concurrent
// activations of leases over them.
func (Repository) LockAssets(ctx context.Context, ids []uuid.UUID) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	var got []uuid.UUID
	return t.Table("assets").Where("id IN ?", ids).Clauses(clause.Locking{Strength: "UPDATE"}).Pluck("id", &got).Error
}

// CurrentLeasesOn returns the ids of active or notice leases, other than
// exclude, covering any of the assets.
func (Repository) CurrentLeasesOn(ctx context.Context, assets []uuid.UUID, exclude uuid.UUID) ([]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.Table("leases l").Joins("JOIN lease_assets la ON la.lease_id = l.id").
		Where("la.asset_id IN ? AND l.state IN ('active', 'notice') AND l.id <> ?", assets, exclude).
		Distinct().Pluck("l.id", &ids).Error
	return ids, err
}

// PreviousLease returns the latest non-draft lease, other than exclude,
// started before start on any of the assets: the "previous lease" of the
// relocation rule.
func (Repository) PreviousLease(ctx context.Context, assets []uuid.UUID, exclude uuid.UUID, start time.Time) (leaseRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return leaseRow{}, false, err
	}
	var rows []leaseRow
	err = t.Table("leases l").Select("l.*").Joins("JOIN lease_assets la ON la.lease_id = l.id").
		Where("la.asset_id IN ? AND l.state <> 'draft' AND l.id <> ? AND l.start_on < ?", assets, exclude, start).
		Order("l.start_on DESC").Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return leaseRow{}, false, err
	}
	return rows[0], true, nil
}

// Guarantees returns the guarantees of a lease.
func (Repository) Guarantees(ctx context.Context, leaseID uuid.UUID) ([]guaranteeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []guaranteeRow
	err = t.Where("lease_id = ?", leaseID).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// Guarantee returns one guarantee.
func (Repository) Guarantee(ctx context.Context, id uuid.UUID) (guaranteeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return guaranteeRow{}, err
	}
	var rows []guaranteeRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return guaranteeRow{}, err
	}
	if len(rows) == 0 {
		return guaranteeRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertGuarantee creates a guarantee.
func (Repository) InsertGuarantee(ctx context.Context, g guaranteeRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&g).Error
}

// UpdateGuarantee updates a guarantee when version matches.
func (Repository) UpdateGuarantee(ctx context.Context, g guaranteeRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&guaranteeRow{}).Where("id = ? AND version = ?", g.ID, version).Updates(map[string]any{
		"kind": g.Kind, "guarantor": g.Guarantor, "amount_cents": g.AmountCents, "valid_from": g.ValidFrom,
		"valid_to": g.ValidTo, "status": g.Status, "document_id": g.DocumentID,
		"version": version + 1, "updated_at": g.UpdatedAt, "updated_by": g.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// ApplyRevision sets the revised rent and reference index when version
// matches, bumping it.
func (Repository) ApplyRevision(ctx context.Context, id uuid.UUID, version int, rentCents int64, quarter string, value int64, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&leaseRow{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{
		"rent_cents": rentCents, "irl_ref_quarter": quarter, "irl_ref_value": value,
		"version": version + 1, "updated_at": at, "updated_by": by,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}
