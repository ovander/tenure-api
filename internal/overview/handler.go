package overview

import (
	"net/http"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Handler serves the month cockpit.
type Handler struct {
	Service Service
}

// Month godoc
//
//	@Summary		The month cockpit
//	@Description	Where a period stands and what is wrong (RENT-FE-001 §4b, Q-139), read only, for every role of the scope. Five steps in order (rent_calls, payments, receipts, bank, recovery), each with its state and counts; the anomalies by severity (blocking, to_act, to_watch), then since when. The rent calls, payments and receipts are the period's; the bank, the recovery and the anomalies stand as of today, except the missing rent calls, which are the period's. The thresholds come from legal_params (overview.cockpit). The period is the current month in Europe/Paris by default.
//	@Tags			overview
//	@Produce		json
//	@Param			period			query		string	false	"period, YYYY-MM"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/overview/month [get]
func (h Handler) Month(w http.ResponseWriter, r *http.Request) {
	var per *period.Period
	if v := r.URL.Query().Get("period"); v != "" {
		p, err := period.Parse(v)
		if err != nil {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "period invalide (AAAA-MM)", nil)
			return
		}
		per = &p
	}
	p, _ := auth.FromContext(r.Context())
	v, err := h.Service.Month(r.Context(), p, httpx.RequestID(r), per)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
