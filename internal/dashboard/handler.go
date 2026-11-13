package dashboard

import (
	"net/http"
	"strconv"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Handler serves the dashboard routes.
type Handler struct{ Service Service }

func year(w http.ResponseWriter, r *http.Request) (int, bool) {
	y, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		httpx.Error(w, r, f)
		return 0, false
	}
	return y, true
}

// Get godoc
//
//	@Summary		The dashboard of a year
//	@Description	Per asset: rent, other income, expenses, net, gross and net yield on the acquisition price (a year's worth), vacant and occupied days, provisions received against recoverable charges paid, arrears aged by dashboard.aging. For the portfolio: the same summed, the bank's movements and the income and expenses by month, the year to date against the same days of the previous year, and the tax estimate under both regimes when the year has its 2044 map. Every figure comes from the owner's journal, the tax summary's split (RENT-SPEC-016, Q-129).
//	@Tags			dashboard
//	@Produce		json
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/dashboard [get]
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	y, ok := year(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	v, err := h.Service.Year(r.Context(), p, httpx.RequestID(r), y)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// AssetsCSV godoc
//
//	@Summary		The dashboard's assets as CSV
//	@Description	One row per asset, then the portfolio; amounts in cents, yields in basis points (Q-129). Requires a recent multi-factor login; the export is audited.
//	@Tags			dashboard
//	@Produce		text/csv
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{file}		file
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/dashboard/assets.csv [get]
func (h Handler) AssetsCSV(w http.ResponseWriter, r *http.Request) {
	y, ok := year(w, r)
	if !ok {
		return
	}
	p, _ := auth.FromContext(r.Context())
	b, err := h.Service.Export(r.Context(), p, httpx.RequestID(r), y)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tableau-de-bord-`+strconv.Itoa(y)+`.csv"`)
	_, _ = w.Write(b)
}
