package tax

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/problem"
)

// Handler serves /tax/summaries.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// List godoc
//
//	@Summary	List the scope's tax summaries
//	@Tags		tax
//	@Produce	json
//	@Param		year			query		int		false	"income year"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[View]
//	@Failure	400,401,403		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/tax/summaries [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	year := 0
	if v := r.URL.Query().Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "year invalide", nil)
			return
		}
		year = y
	}
	items, total, err := h.Service.List(r.Context(), caller(r), httpx.RequestID(r), year, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// Create godoc
//
//	@Summary		Draft or issue an income year's tax summary
//	@Description	Without a regime, (re)computes the year as a draft with micro-foncier and régime réel side by side; with a regime, issues it: RF-… PDF and CSV of the lines archived, summary frozen. A step-up route (fresh MFA authentication).
//	@Tags			tax
//	@Accept			json
//	@Produce		json
//	@Param			body			body		IssueInput	true	"year and regime"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/tax/summaries [post]
func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in IssueInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Create(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// Get godoc
//
//	@Summary	Get a tax summary
//	@Tags		tax
//	@Produce	json
//	@Param		id				path		string	true	"summary id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/tax/summaries/{id} [get]
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Get(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// LinesCSV godoc
//
//	@Summary		Export a tax summary's lines as CSV
//	@Description	One row per owner, asset and line with its source ids, then totals, flags and supporting documents. An export: step-up required, recorded in the audit trail.
//	@Tags			tax
//	@Produce		text/csv
//	@Param			id				path		string	true	"summary id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{string}	string	"CSV"
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/tax/summaries/{id}/lines.csv [get]
func (h Handler) LinesCSV(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	b, err := h.Service.ExportCSV(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lignes-2044.csv"`)
	_, _ = w.Write(b)
}
