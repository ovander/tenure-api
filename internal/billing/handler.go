package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/period"
)

// DocView is a billing document as the API returns it.
type DocView struct {
	ID          uuid.UUID       `json:"id"`
	LeaseID     uuid.UUID       `json:"lease_id"`
	Kind        Kind            `json:"kind" enums:"rent_call,receipt,regularisation,revision_notice,cancellation"`
	Period      string          `json:"period"`
	Generation  int             `json:"generation"`
	Number      string          `json:"number"`
	Lines       []document.Line `json:"lines"`
	TotalCents  int64           `json:"total_cents"`
	IssuedAt    time.Time       `json:"issued_at"`
	CancelsID   *uuid.UUID      `json:"cancels_id"`
	CancelledBy *uuid.UUID      `json:"cancelled_by"`
	DocumentID  uuid.UUID       `json:"document_id"`
}

func docView(d docRow) DocView {
	v := DocView{ID: d.ID, LeaseID: d.LeaseID, Kind: Kind(d.Kind), Period: d.Period, Generation: d.Generation, Number: d.Number,
		TotalCents: d.TotalCents, IssuedAt: d.IssuedAt, CancelsID: d.CancelsID, CancelledBy: d.CancelledBy, DocumentID: d.DocumentID,
		Lines: []document.Line{}}
	_ = json.Unmarshal(d.Lines, &v.Lines)
	return v
}

// List returns a page of the scope's billing documents.
func (s Service) List(ctx context.Context, p *auth.Principal, requestID string, f DocFilter, offset, limit int) ([]DocView, int, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []DocView
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.List(ctx, f, offset, limit)
		for _, r := range rows {
			out = append(out, docView(r))
		}
		total = n
		return err
	})
	return out, total, err
}

// Get returns one billing document of the scope.
func (s Service) Get(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (DocView, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return DocView{}, err
	}
	var out DocView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Get(ctx, id)
		out = docView(r)
		return err
	})
	return out, err
}

// Handler serves /billing and POST /leases/{id}/revision.
type Handler struct {
	Service Service
	Runner  *jobs.Runner
}

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// RunInput is the body of POST /billing/runs.
type RunInput struct {
	Period string `json:"period" example:"2025-04"`
	DryRun bool   `json:"dry_run"`
}

// RunView is a billing run: the job run and its fees, computed (dry run) or
// issued by this run; empty when an earlier run is returned unchanged.
type RunView struct {
	Run    jobs.Run `json:"run"`
	DryRun bool     `json:"dry_run"`
	Fees   []Fee    `json:"fees"`
}

// CreateRun godoc
//
//	@Summary		Run the monthly billing of the scope for a period
//	@Description	dry_run returns the computed fees without issuing; otherwise rent calls are issued, archived and e-mailed, and fees lists those issued by this run. A second run of the same period issues only the rent calls still missing (a lease activated since); while one runs, it is returned (200).
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			body			body		RunInput	true	"period"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	RunView
//	@Success		201				{object}	RunView
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/billing/runs [post]
func (h Handler) CreateRun(w http.ResponseWriter, r *http.Request) {
	p := caller(r)
	var in RunInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	if _, err := period.Parse(in.Period); err != nil {
		var f invariant.Fields
		f.Add("period", "Période attendue au format AAAA-MM.")
		httpx.Error(w, r, f)
		return
	}
	owner := p.Active.OwnerID
	run, ran, err := h.Runner.Trigger(r.Context(), "billing.monthly", jobs.Options{Owner: &owner, DryRun: in.DryRun, Period: in.Period, ActorSub: p.Sub, Rerun: true})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	out := RunView{Run: run, DryRun: in.DryRun, Fees: []Fee{}}
	if in.DryRun || ran {
		var sum struct {
			Owners []struct {
				Result OwnerResult `json:"result"`
				Error  string      `json:"error"`
			} `json:"owners"`
		}
		_ = json.Unmarshal(run.Summary, &sum)
		for _, o := range sum.Owners {
			if o.Error == "" { // a failed owner's transaction rolled back
				out.Fees = append(out.Fees, o.Result.Fees...)
			}
		}
	}
	status := http.StatusOK
	if ran && !in.DryRun {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

// GetRun godoc
//
//	@Summary	Get a billing run
//	@Tags		billing
//	@Produce	json
//	@Param		id				path		string	true	"run id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	jobs.Run
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/billing/runs/{id} [get]
func (h Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p := caller(r)
	var run jobs.Run
	err := h.Service.DB.InScope(r.Context(), p.DBScope(httpx.RequestID(r)), func(ctx context.Context) error {
		var err error
		run, err = jobs.Repository{}.GetRun(ctx, id)
		return err
	})
	if err == nil && (run.Job != "billing.monthly" || run.OwnerID == nil || *run.OwnerID != p.Active.OwnerID) {
		err = invariant.ErrNotFound
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

// ListDocuments godoc
//
//	@Summary	List the scope's billing documents
//	@Tags		billing
//	@Produce	json
//	@Param		period			query		string	false	"period YYYY-MM"
//	@Param		lease_id		query		string	false	"lease id"
//	@Param		kind			query		string	false	"rent_call, revision_notice, cancellation…"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[DocView]
//	@Failure	400,401,403,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/billing/documents [get]
func (h Handler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := DocFilter{Period: q.Get("period"), Kind: q.Get("kind")}
	var fields invariant.Fields
	if f.Period != "" {
		if _, err := period.Parse(f.Period); err != nil {
			fields.Add("period", "Période attendue au format AAAA-MM.")
		}
	}
	if v := q.Get("lease_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fields.Add("lease_id", "Identifiant invalide.")
		}
		f.LeaseID = &id
	}
	if err := fields.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, total, err := h.Service.List(r.Context(), caller(r), httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// GetDocument godoc
//
//	@Summary	Get a billing document
//	@Tags		billing
//	@Produce	json
//	@Param		id				path		string	true	"billing document id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	DocView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/billing/documents/{id} [get]
func (h Handler) GetDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Get(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// Cancel godoc
//
//	@Summary		Cancel a rent call and reissue it
//	@Description	Issues a cancelling document and, when the lease is still billed for the period, a new rent call; both are archived and e-mailed.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"billing document id"
//	@Param			body			body		CancelInput	true	"reason"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	CancelResult
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/billing/documents/{id}/cancel [post]
func (h Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in CancelInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	res, err := h.Service.Cancel(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

// RevisionInput is the body of POST /leases/{id}/revision.
type RevisionInput struct {
	DryRun bool `json:"dry_run"`
}

// RevisionView is the outcome of POST /leases/{id}/revision.
type RevisionView struct {
	Revision RevisionResult `json:"revision"`
	Notice   *IssuedDoc     `json:"notice"`
}

// Revise godoc
//
//	@Summary		Apply the IRL revision due today on a lease
//	@Description	Applies the revision from its first billing period (never retroactively) and issues the revision notice; dry_run only computes it.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			body			body		RevisionInput	true	"options"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	RevisionView
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/revision [post]
func (h Handler) Revise(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in RevisionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	res, doc, err := h.Service.ReviseNow(r.Context(), caller(r), httpx.RequestID(r), id, in.DryRun)
	if err != nil {
		var inv *invariant.Error
		if errors.As(err, &inv) {
			httpx.Error(w, r, inv)
			return
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, RevisionView{Revision: res, Notice: doc})
}

// Arrears godoc
//
//	@Summary		The arrears of a lease
//	@Description	Every live rent call with something left to pay, overdue after its due day; no penalty is computed.
//	@Tags			billing
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	ArrearsView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/arrears [get]
func (h Handler) Arrears(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.ArrearsFor(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
