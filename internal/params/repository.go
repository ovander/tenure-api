package params

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/period"
)

type paramRow struct {
	Key           string          `gorm:"column:key"`
	EffectiveFrom time.Time       `gorm:"column:effective_from"`
	Value         json.RawMessage `gorm:"column:value;type:jsonb"`
	Source        string          `gorm:"column:source"`
	SourceURL     string          `gorm:"column:source_url"`
	CreatedBy     string          `gorm:"column:created_by"`
}

func (paramRow) TableName() string { return "legal_params" }

// IRL is one cached INSEE IRL value, in hundredths.
type IRL struct {
	Quarter     string      `json:"quarter" gorm:"column:quarter"`
	Value       int64       `json:"value" gorm:"column:value"`
	PublishedOn period.Date `json:"published_on" gorm:"-" swaggertype:"string" format:"date"`
	Published   time.Time   `json:"-" gorm:"column:published_on"`
}

// Repository reads and appends legal_params and reads irl_index. It runs in
// the caller's scoped transaction.
type Repository struct{}

// Load returns every legal parameter as a Set.
func (Repository) Load(ctx context.Context) (Set, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return Set{}, err
	}
	var rows []paramRow
	if err := tx.Order("key, effective_from").Find(&rows).Error; err != nil {
		return Set{}, err
	}
	entries := make([]Entry, len(rows))
	for i, r := range rows {
		entries[i] = Entry{Key: r.Key, EffectiveFrom: period.FromTime(r.EffectiveFrom), Value: r.Value, Source: r.Source, SourceURL: r.SourceURL}
	}
	return New(entries...)
}

// Insert appends one entry.
func (Repository) Insert(ctx context.Context, e Entry, by string) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Create(&paramRow{Key: e.Key, EffectiveFrom: e.EffectiveFrom.Time(), Value: e.Value,
		Source: e.Source, SourceURL: e.SourceURL, CreatedBy: by}).Error
}

// IRLSeries returns the cached IRL values, latest first.
func (Repository) IRLSeries(ctx context.Context) ([]IRL, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []IRL
	if err := tx.Table("irl_index").Order("quarter DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	for i := range out {
		out[i].PublishedOn = period.FromTime(out[i].Published)
	}
	return out, nil
}

// LatestIRLOfQuarter returns the latest published value of quarter number q
// (1 to 4), the "current IRL" of a lease referenced on that quarter.
func (Repository) LatestIRLOfQuarter(ctx context.Context, q int) (IRL, bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return IRL{}, false, err
	}
	var out []IRL
	if err := tx.Table("irl_index").Where("right(quarter, 1) = ?", q).Order("quarter DESC").Limit(1).Find(&out).Error; err != nil {
		return IRL{}, false, err
	}
	if len(out) == 0 {
		return IRL{}, false, nil
	}
	out[0].PublishedOn = period.FromTime(out[0].Published)
	return out[0], true, nil
}
