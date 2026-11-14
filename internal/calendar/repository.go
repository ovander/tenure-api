package calendar

import (
	"context"
	"strings"
	"time"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/period"
)

// Repository reads the calendar's facts, in the scope's transaction (RLS
// filters every table on the owner). It writes nothing.
type Repository struct{}

type leaseRow struct {
	ID            string     `gorm:"column:id"`
	Kind          string     `gorm:"column:kind"`
	State         string     `gorm:"column:state"`
	StartOn       time.Time  `gorm:"column:start_on"`
	EndOn         *time.Time `gorm:"column:end_on"`
	TermMonths    int        `gorm:"column:term_months"`
	RevisionMonth int        `gorm:"column:revision_month"`
	IRLRefQuarter string     `gorm:"column:irl_ref_quarter"`
	Assets        string     `gorm:"column:assets"`
}

type diagnosticRow struct {
	AssetID    string     `gorm:"column:asset_id"`
	Kind       string     `gorm:"column:kind"`
	DoneOn     *time.Time `gorm:"column:done_on"`
	ValidUntil *time.Time `gorm:"column:valid_until"`
}

type entryRow struct {
	LeaseID string    `gorm:"column:lease_id"`
	DoneOn  time.Time `gorm:"column:done_on"`
}

type exitRow struct {
	LeaseID        string    `gorm:"column:lease_id"`
	KeysReturnedOn time.Time `gorm:"column:keys_returned_on"`
	Deductions     int       `gorm:"column:deductions"`
	Settled        bool      `gorm:"column:settled"`
}

func dateOf(t *time.Time) *period.Date {
	if t == nil {
		return nil
	}
	d := period.FromTime(*t)
	return &d
}

// Facts loads the scope's leases (not drafts), the latest diagnostic of each
// kind per asset, the issued entries captured in Tenure, and the issued
// exits with their keys date and where the deposit stands, and the
// maintenance contracts with their latest visit.
func (Repository) Facts(ctx context.Context) (Facts, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return Facts{}, err
	}
	f := Facts{Leases: []Lease{}, Diagnostics: []Diagnostic{}, Entries: []Entry{}, Exits: []Exit{}}
	var ls []leaseRow
	if err := t.Raw(`SELECT l.id::text, l.kind, l.state, l.start_on, l.end_on, l.term_months, l.revision_month, l.irl_ref_quarter,
			coalesce((SELECT string_agg(la.asset_id::text, ',' ORDER BY la.asset_id) FROM lease_assets la WHERE la.lease_id = l.id), '') AS assets
		FROM leases l WHERE l.state <> 'draft' ORDER BY l.start_on, l.id`).Scan(&ls).Error; err != nil {
		return Facts{}, err
	}
	for _, r := range ls {
		f.Leases = append(f.Leases, Lease{ID: r.ID, AssetIDs: splitIDs(r.Assets), Kind: r.Kind, State: r.State, StartOn: period.FromTime(r.StartOn),
			EndOn: dateOf(r.EndOn), TermMonths: r.TermMonths, RevisionMonth: r.RevisionMonth, IRLRefQuarter: r.IRLRefQuarter})
	}
	var ds []diagnosticRow
	if err := t.Raw(`SELECT DISTINCT ON (asset_id, kind) asset_id::text, kind, done_on, valid_until FROM asset_diagnostics
		ORDER BY asset_id, kind, done_on DESC NULLS LAST, created_at DESC`).Scan(&ds).Error; err != nil {
		return Facts{}, err
	}
	for _, r := range ds {
		f.Diagnostics = append(f.Diagnostics, Diagnostic{AssetID: r.AssetID, Kind: r.Kind, DoneOn: dateOf(r.DoneOn), ValidUntil: dateOf(r.ValidUntil)})
	}
	var es []entryRow
	if err := t.Raw(`SELECT lease_id::text, done_on FROM inventories
		WHERE kind = 'entry' AND state = 'issued' AND mode <> 'upload' AND done_on IS NOT NULL ORDER BY done_on`).Scan(&es).Error; err != nil {
		return Facts{}, err
	}
	for _, r := range es {
		f.Entries = append(f.Entries, Entry{LeaseID: r.LeaseID, DoneOn: period.FromTime(r.DoneOn)})
	}
	var xs []exitRow
	if err := t.Raw(`SELECT i.lease_id::text, i.keys_returned_on,
			(SELECT count(*) FROM deposit_deductions dd WHERE dd.lease_id = i.lease_id) AS deductions,
			EXISTS (SELECT 1 FROM deposit_settlements s WHERE s.lease_id = i.lease_id AND s.kind = 'initial') AS settled
		FROM inventories i WHERE i.kind = 'exit' AND i.state = 'issued' AND i.keys_returned_on IS NOT NULL ORDER BY i.keys_returned_on`).Scan(&xs).Error; err != nil {
		return Facts{}, err
	}
	for _, r := range xs {
		f.Exits = append(f.Exits, Exit{LeaseID: r.LeaseID, KeysReturnedOn: period.FromTime(r.KeysReturnedOn), Deductions: r.Deductions, Settled: r.Settled})
	}
	var ms []maintenanceRow
	if err := t.Raw(`SELECT c.id::text, c.asset_id::text, c.kind, c.responsible, c.interval_months, c.first_due_on, c.end_on,
			(SELECT max(v.done_on) FROM maintenance_visits v WHERE v.contract_id = c.id) AS last_done_on
		FROM maintenance_contracts c ORDER BY c.first_due_on, c.id`).Scan(&ms).Error; err != nil {
		return Facts{}, err
	}
	for _, r := range ms {
		f.Maintenance = append(f.Maintenance, Maintenance{ID: r.ID, AssetID: r.AssetID, Kind: r.Kind, Responsible: r.Responsible,
			IntervalMonths: r.IntervalMonths, FirstDueOn: period.FromTime(r.FirstDueOn), EndOn: dateOf(r.EndOn), LastDoneOn: dateOf(r.LastDoneOn)})
	}
	return f, nil
}

type maintenanceRow struct {
	ID             string     `gorm:"column:id"`
	AssetID        string     `gorm:"column:asset_id"`
	Kind           string     `gorm:"column:kind"`
	Responsible    string     `gorm:"column:responsible"`
	IntervalMonths int        `gorm:"column:interval_months"`
	FirstDueOn     time.Time  `gorm:"column:first_due_on"`
	EndOn          *time.Time `gorm:"column:end_on"`
	LastDoneOn     *time.Time `gorm:"column:last_done_on"`
}

func splitIDs(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}
