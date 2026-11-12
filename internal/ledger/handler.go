package ledger

import (
	"net/http"
	"strconv"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Handler serves the journal routes.
type Handler struct{ Service Service }

// Get godoc
//
//	@Summary		The owner's journal of a year
//	@Description	Every movement of the year posted on a cash basis, in double entry, to the accounts of the chart in legal_params: rent received (rent, provisions, unapplied), charges paid (their recoverable part to the provisions account), loan instalments, deposits received (a liability) and deposit statements; and the balance of each account. closed says the year's tax summary is issued: what it was computed from no longer changes (Q-127).
//	@Tags			ledger
//	@Produce		json
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/ledger [get]
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		httpx.Error(w, r, f)
		return
	}
	p, _ := auth.FromContext(r.Context())
	v, err := h.Service.Year(r.Context(), p, httpx.RequestID(r), year)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
