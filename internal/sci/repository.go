package sci

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Repository runs the sci module's SQL in the caller's scoped transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

type associateRow struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID   uuid.UUID `gorm:"column:owner_id"`
	Name      string    `gorm:"column:name"`
	PersonSub *string   `gorm:"column:person_sub"`
	Version   int       `gorm:"column:version"`
	CreatedAt time.Time `gorm:"column:created_at"`
	CreatedBy string    `gorm:"column:created_by"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`
}

func (associateRow) TableName() string { return "sci_associates" }

type movementRow struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID  `gorm:"column:owner_id"`
	OnDate        time.Time  `gorm:"column:on_date;type:date"`
	Reason        string     `gorm:"column:reason"`
	Parts         int        `gorm:"column:parts"`
	FromHoldingID *uuid.UUID `gorm:"column:from_holding_id"`
	ToAssociateID uuid.UUID  `gorm:"column:to_associate_id"`
	Note          string     `gorm:"column:note"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	CreatedBy     string     `gorm:"column:created_by"`
}

func (movementRow) TableName() string { return "sci_movements" }

type holdingRow struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID  `gorm:"column:owner_id"`
	AssociateID uuid.UUID  `gorm:"column:associate_id"`
	Parts       int        `gorm:"column:parts"`
	FromOn      time.Time  `gorm:"column:from_on;type:date"`
	OpenedBy    uuid.UUID  `gorm:"column:opened_by"`
	ClosedOn    *time.Time `gorm:"column:closed_on;type:date"`
	ClosedBy    *uuid.UUID `gorm:"column:closed_by"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	CreatedBy   string     `gorm:"column:created_by"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
	UpdatedBy   string     `gorm:"column:updated_by"`
}

func (holdingRow) TableName() string { return "sci_holdings" }

// Capital returns the scope's capital in parts; ok is false when the scope is
// not an SCI's.
func (Repository) Capital(ctx context.Context, owner uuid.UUID) (int, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return 0, false, err
	}
	var rows []struct{ CapitalParts *int }
	if err := t.Table("owners").Select("capital_parts").Where("id = ? AND kind = 'sci'", owner).Limit(1).Scan(&rows).Error; err != nil {
		return 0, false, err
	}
	if len(rows) == 0 || rows[0].CapitalParts == nil {
		return 0, false, nil
	}
	return *rows[0].CapitalParts, true, nil
}

// LockRegister serialises the writes to the scope's register, by a
// transaction-scoped advisory lock.
func (Repository) LockRegister(ctx context.Context, owner uuid.UUID) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Exec("SELECT pg_advisory_xact_lock(hashtext('sci-register:' || ?::text))", owner.String()).Error
}

// Associates lists the scope's associates in register order.
func (Repository) Associates(ctx context.Context) ([]associateRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []associateRow
	err = t.Order("created_at, id").Find(&rows).Error
	return rows, err
}

// Associate returns one associate of the scope.
func (Repository) Associate(ctx context.Context, id uuid.UUID) (associateRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return associateRow{}, err
	}
	var rows []associateRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return associateRow{}, err
	}
	if len(rows) == 0 {
		return associateRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertAssociate records an associate.
func (Repository) InsertAssociate(ctx context.Context, a associateRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&a).Error
}

// UpdateAssociate changes an associate when the version matches.
func (Repository) UpdateAssociate(ctx context.Context, a associateRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&associateRow{}).Where("id = ? AND version = ?", a.ID, version).Updates(map[string]any{
		"name": a.Name, "person_sub": a.PersonSub, "version": version + 1, "updated_at": a.UpdatedAt, "updated_by": a.UpdatedBy})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Holdings lists the scope's holdings, oldest first.
func (Repository) Holdings(ctx context.Context) ([]holdingRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []holdingRow
	err = t.Order("from_on, created_at, id").Find(&rows).Error
	return rows, err
}

// Holding returns one holding of the scope. Writes to the register are
// serialised by LockRegister; a row lock would hide a closed holding, which
// the closing policy no longer lets tenure_api update.
func (Repository) Holding(ctx context.Context, id uuid.UUID) (holdingRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return holdingRow{}, err
	}
	var rows []holdingRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return holdingRow{}, err
	}
	if len(rows) == 0 {
		return holdingRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertMovement records a movement.
func (Repository) InsertMovement(ctx context.Context, m movementRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&m).Error
}

// InsertHolding records a holding.
func (Repository) InsertHolding(ctx context.Context, h holdingRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&h).Error
}

// CloseHolding closes a holding that is still open.
func (Repository) CloseHolding(ctx context.Context, h holdingRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&holdingRow{}).Where("id = ? AND closed_on IS NULL", h.ID).Updates(map[string]any{
		"closed_on": h.ClosedOn, "closed_by": h.ClosedBy, "updated_at": h.UpdatedAt, "updated_by": h.UpdatedBy})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Movements lists the scope's movements, oldest first.
func (Repository) Movements(ctx context.Context) ([]movementRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []movementRow
	err = t.Order("on_date, created_at, id").Find(&rows).Error
	return rows, err
}

// LiveGrantee returns the Socrate subject of a live grant of the scope.
func (Repository) LiveGrantee(ctx context.Context, owner, grant uuid.UUID) (string, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return "", false, err
	}
	var subs []string
	if err := t.Table("owner_grants").Where("id = ? AND owner_id = ? AND revoked_at IS NULL", grant, owner).
		Limit(1).Pluck("grantee_sub", &subs).Error; err != nil {
		return "", false, err
	}
	if len(subs) == 0 {
		return "", false, nil
	}
	return subs[0], true, nil
}

// Declaration returns the scope's 2072 of a year, if issued.
func (Repository) Declaration(ctx context.Context, year int) (declarationRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return declarationRow{}, false, err
	}
	var rows []declarationRow
	if err := t.Where("tax_year = ?", year).Limit(1).Find(&rows).Error; err != nil {
		return declarationRow{}, false, err
	}
	if len(rows) == 0 {
		return declarationRow{}, false, nil
	}
	return rows[0], true, nil
}

// DeclaredYears lists the years of the scope's issued 2072s, latest first.
func (Repository) DeclaredYears(ctx context.Context) ([]int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var years []int
	err = t.Model(&declarationRow{}).Order("tax_year DESC").Pluck("tax_year", &years).Error
	return years, err
}

// LastDeclaredYear is the latest year of an issued 2072, if any.
func (r Repository) LastDeclaredYear(ctx context.Context) (int, bool, error) {
	years, err := r.DeclaredYears(ctx)
	if err != nil || len(years) == 0 {
		return 0, false, err
	}
	return years[0], true, nil
}

// Allocations returns a declaration's shares, in register order.
func (Repository) Allocations(ctx context.Context, declaration uuid.UUID) ([]allocationRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []allocationRow
	err = t.Table("sci_allocations AS al").Select("al.*").
		Joins("JOIN sci_associates a ON a.id = al.associate_id").
		Where("al.declaration_id = ?", declaration).Order("a.created_at, a.id").Find(&rows).Error
	return rows, err
}

// InsertDeclaration records a 2072.
func (Repository) InsertDeclaration(ctx context.Context, d declarationRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&d).Error
}

// InsertAllocation records an associate's share of a 2072.
func (Repository) InsertAllocation(ctx context.Context, a allocationRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&a).Error
}

// EntityName returns the SCI's name.
func (Repository) EntityName(ctx context.Context, owner uuid.UUID) (string, error) {
	t, err := tx(ctx)
	if err != nil {
		return "", err
	}
	var names []string
	err = t.Table("owners").Where("id = ?", owner).Limit(1).Pluck("legal_name", &names).Error
	if err != nil || len(names) == 0 {
		return "", err
	}
	return names[0], nil
}
