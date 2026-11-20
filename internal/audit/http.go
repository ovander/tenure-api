package audit

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/csvx"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

// Filter narrows an event list (GET /audit/events?entity_type=&entity_id=&from=&to=).
type Filter struct {
	EntityType string
	EntityID   string
	From, To   *period.Date // inclusive, Europe/Paris days
}

// Service reads the scope's audit trail.
type Service struct{ DB *db.DB }

func (s Service) list(ctx context.Context, f Filter, offset, limit int) ([]Event, int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	sc, _ := db.ScopeFrom(ctx)
	q := tx.Model(&Event{}).Where("owner_id = ?", sc.OwnerID)
	if f.EntityType != "" {
		q = q.Where("entity_type = ?", f.EntityType)
	}
	if f.EntityID != "" {
		q = q.Where("entity_id = ?", f.EntityID)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	if f.From != nil {
		q = q.Where("occurred_at >= ?", time.Date(f.From.Year, f.From.Month, f.From.Day, 0, 0, 0, 0, paris))
	}
	if f.To != nil {
		q = q.Where("occurred_at < ?", time.Date(f.To.Year, f.To.Month, f.To.Day+1, 0, 0, 0, 0, paris))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []Event
	q = q.Order("id")
	if limit > 0 {
		q = q.Offset(offset).Limit(limit)
	}
	err = q.Find(&out).Error
	return out, int(total), err
}

// List returns a page of the scope's events (owner and accountant, Q-37).
func (s Service) List(ctx context.Context, p *auth.Principal, requestID string, f Filter, offset, limit int) ([]Event, int, error) {
	if p.Active == nil {
		return nil, 0, invariant.ErrForbidden
	}
	var out []Event
	var total int
	err := s.DB.InScope(ctx, p.DBScope(requestID), func(ctx context.Context) error {
		var err error
		out, total, err = s.list(ctx, f, offset, limit)
		return err
	})
	return out, total, err
}

// Export returns every matching event and records the export itself as an
// audit event (RENT-SPEC-022: exports are covered actions).
func (s Service) Export(ctx context.Context, p *auth.Principal, requestID string, f Filter) ([]Event, error) {
	if p.Active == nil {
		return nil, invariant.ErrForbidden
	}
	var out []Event
	err := s.DB.InScope(ctx, p.DBScope(requestID), func(ctx context.Context) error {
		var err error
		if out, _, err = s.list(ctx, f, 0, 0); err != nil {
			return err
		}
		return Record(ctx, "audit.export", Entity{Type: "audit_trail", ID: p.Active.OwnerID.String()}, nil,
			map[string]any{"events": len(out), "entity_type": f.EntityType, "entity_id": f.EntityID})
	})
	return out, err
}

// Handler serves /audit.
type Handler struct{ Service Service }

func filter(w http.ResponseWriter, r *http.Request) (Filter, bool) {
	q := r.URL.Query()
	f := Filter{EntityType: q.Get("entity_type"), EntityID: q.Get("entity_id")}
	var fields invariant.Fields
	for key, dst := range map[string]**period.Date{"from": &f.From, "to": &f.To} {
		if v := q.Get(key); v != "" {
			d, err := period.ParseDate(v)
			if err != nil {
				fields.Add(key, "Date attendue au format AAAA-MM-JJ.")
				continue
			}
			*dst = &d
		}
	}
	if err := fields.Err(); err != nil {
		httpx.Error(w, r, err)
		return f, false
	}
	return f, true
}

// List godoc
//
//	@Summary	List the scope's audit events, with their chain hashes
//	@Tags		audit
//	@Produce	json
//	@Param		entity_type		query		string	false	"entity type, e.g. lease"
//	@Param		entity_id		query		string	false	"entity id"
//	@Param		from			query		string	false	"first day (YYYY-MM-DD)"
//	@Param		to				query		string	false	"last day (YYYY-MM-DD)"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[Event]
//	@Failure	400,401,403,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/audit/events [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	f, ok := filter(w, r)
	if !ok {
		return
	}
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	items, total, err := h.Service.List(r.Context(), p, httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// ExportCSV godoc
//
//	@Summary		Export the scope's audit events as CSV
//	@Description	Step-up required; the export is itself recorded as an audit event.
//	@Tags			audit
//	@Produce		text/csv
//	@Param			entity_type		query		string	false	"entity type"
//	@Param			entity_id		query		string	false	"entity id"
//	@Param			from			query		string	false	"first day (YYYY-MM-DD)"
//	@Param			to				query		string	false	"last day (YYYY-MM-DD)"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{string}	string	"CSV"
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/audit/events.csv [get]
func (h Handler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	f, ok := filter(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	events, err := h.Service.Export(r.Context(), p, httpx.RequestID(r), f)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-events.csv"`)
	cw := csvx.NewWriter(w)
	_ = cw.Write([]string{"id", "occurred_at", "actor_sub", "scope", "action", "entity_type", "entity_id", "request_id", "before", "after", "prev_hash", "hash"})
	for _, e := range events {
		_ = cw.Write([]string{strconv.FormatInt(e.ID, 10), e.OccurredAt.UTC().Format(time.RFC3339Nano), e.ActorSub, e.Scope, e.Action,
			e.EntityType, e.EntityID, e.RequestID, string(e.Before), string(e.After), e.PrevHash, e.Hash})
	}
	cw.Flush()
}
