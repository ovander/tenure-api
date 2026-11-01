package jobs

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// Repository reads job_runs in the caller's scoped transaction.
type Repository struct{}

// ListRuns returns a page of runs, latest first, and the total.
func (Repository) ListRuns(ctx context.Context, offset, limit int) ([]Run, int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := tx.Model(&Run{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []Run
	err = tx.Order("started_at DESC, id").Offset(offset).Limit(limit).Find(&runs).Error
	return runs, int(total), err
}

// GetRun returns one run visible in the caller's scope.
func (Repository) GetRun(ctx context.Context, id uuid.UUID) (Run, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return Run{}, err
	}
	var runs []Run
	if err := tx.Where("id = ?", id).Limit(1).Find(&runs).Error; err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, invariant.ErrNotFound
	}
	return runs[0], nil
}

// Handler serves GET /jobs/runs with the API database role: row-level
// security shows platform runs and the runs of the caller's active scope.
type Handler struct {
	DB     *db.DB
	Runner *Runner
}

// TriggerInput is the body of POST /jobs/{name}/trigger.
type TriggerInput struct {
	DryRun bool   `json:"dry_run"`
	Period string `json:"period"`
}

// Trigger godoc
//
//	@Summary		Run a job now
//	@Description	An admin runs it for every owner; an owner for their own scope only. A dry run computes and records nothing.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Param			name			path		string			true	"job name"
//	@Param			body			body		TriggerInput	true	"options"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	Run
//	@Success		201				{object}	Run
//	@Failure		400,401,403,404,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/jobs/{name}/trigger [post]
func (h Handler) Trigger(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var in TriggerInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	opts := Options{DryRun: in.DryRun, Period: in.Period, ActorSub: p.Sub}
	switch {
	case p.Active != nil && p.Active.Role == auth.RoleOwner:
		id := p.Active.OwnerID
		opts.Owner = &id
	case p.Admin:
	default:
		problem.Write(w, r, problem.Forbidden, p.Language, "", nil)
		return
	}
	run, ran, err := h.Runner.Trigger(r.Context(), chi.URLParam(r, "name"), opts)
	switch {
	case errors.Is(err, ErrUnknownJob):
		problem.Write(w, r, problem.NotFound, p.Language, "", nil)
		return
	case errors.Is(err, ErrInvalidPeriod):
		var f invariant.Fields
		f.Add("period", "Période invalide pour ce traitement.")
		httpx.Error(w, r, f)
		return
	case err != nil:
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusOK
	if ran && !in.DryRun {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, run)
}

// ListRuns godoc
//
//	@Summary	List job runs
//	@Tags		jobs
//	@Produce	json
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[Run]
//	@Failure	400,401			{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/jobs/runs [get]
func (h Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	var runs []Run
	var total int
	err := h.DB.InScope(r.Context(), p.DBScope(httpx.RequestID(r)), func(ctx context.Context) error {
		var err error
		runs, total, err = Repository{}.ListRuns(ctx, pg.Offset(), pg.Size)
		return err
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(runs, pg, total))
}
