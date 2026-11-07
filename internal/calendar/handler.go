package calendar

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Handler serves the calendar routes.
type Handler struct {
	Service Service
}

// Get godoc
//
//	@Summary		The obligations calendar
//	@Description	Every dated obligation Tenure derives from what it holds (RENT-SPEC-012, computed base): diagnostics' end of validity, IRL revisions, ends of notice and of term, the yearly home insurance certificate, the entry inventory's completion windows, the deposit restitution deadline. From from to to (today and the next 90 days by default, three years at most), always with the overdue ones; for one asset with asset_id. Not for an accountant.
//	@Tags			calendar
//	@Produce		json
//	@Param			from			query		string	false	"first day, YYYY-MM-DD"
//	@Param			to				query		string	false	"last day, YYYY-MM-DD"
//	@Param			asset_id		query		string	false	"one asset"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/calendar [get]
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	day := func(name string) (*period.Date, bool) {
		v := q.Get(name)
		if v == "" {
			return nil, true
		}
		d, err := period.ParseDate(v)
		if err != nil {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), name+" invalide (AAAA-MM-JJ)", nil)
			return nil, false
		}
		return &d, true
	}
	from, ok := day("from")
	if !ok {
		return
	}
	to, ok := day("to")
	if !ok {
		return
	}
	asset := q.Get("asset_id")
	if asset != "" {
		if _, err := uuid.Parse(asset); err != nil {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "asset_id invalide", nil)
			return
		}
	}
	p, _ := auth.FromContext(r.Context())
	v, err := h.Service.Calendar(r.Context(), p, httpx.RequestID(r), from, to, asset)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
