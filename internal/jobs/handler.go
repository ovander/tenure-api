package jobs

import (
	"context"
	"net/http"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/httpx"
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

// Handler serves GET /jobs/runs with the API database role: row-level
// security shows platform runs and the runs of the caller's active scope.
type Handler struct{ DB *db.DB }

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
