package loan

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// Handler serves the loan routes.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// List godoc
//
//	@Summary		Loans
//	@Description	The loans financing the scope's assets, of one asset with asset_id, each with its totals by year and without its instalments (RENT-SPEC-014, Q-126).
//	@Tags			loan
//	@Produce		json
//	@Param			asset_id		query		string	false	"asset"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		View
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/loans [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	var asset *uuid.UUID
	if v := r.URL.Query().Get("asset_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			var f invariant.Fields
			f.Add("asset_id", "Identifiant invalide.")
			httpx.Error(w, r, f)
			return
		}
		asset = &id
	}
	v, err := h.Service.List(r.Context(), caller(r), httpx.RequestID(r), asset)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// Get godoc
//
//	@Summary		A loan and its amortisation table
//	@Tags			loan
//	@Produce		json
//	@Param			id				path		string	true	"loan id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,404	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/loans/{id} [get]
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

// Create godoc
//
//	@Summary		Record a loan
//	@Description	Without instalments, the table is computed: a fixed rate, constant monthly instalments, the interest of each month the balance times the annual rate divided by twelve, to the cent half-up, the last instalment closing the balance. With instalments, it is the bank's table as typed in, checked to repay the principal (Q-126). Each instalment due in an income year puts its interest and insurance on line 250 of the tax summary.
//	@Tags			loan
//	@Accept			json
//	@Produce		json
//	@Param			body			body		Input	true	"loan"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		201				{object}	View
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/loans [post]
func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Create(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// Update godoc
//
//	@Summary		Change a loan
//	@Description	Replaces its terms and its table, computed or typed in as for POST /loans. A tax summary already issued keeps its figures.
//	@Tags			loan
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string	true	"loan id"
//	@Param			If-Match		header		string	true	"current version"
//	@Param			body			body		Input	true	"loan"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/loans/{id} [put]
func (h Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Update(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// Schedule godoc
//
//	@Summary		Preview an amortisation table
//	@Description	The table POST /loans would compute from these terms, without recording anything.
//	@Tags			loan
//	@Produce		json
//	@Param			principal_cents	query		int		true	"principal, in cents"
//	@Param			annual_rate_ppm	query		int		true	"annual rate, in parts per million"
//	@Param			term_months		query		int		true	"number of monthly instalments"
//	@Param			first_due_on	query		string	true	"first due day"	format(date)
//	@Param			insurance_cents	query		int		false	"insurance per instalment, in cents"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/loans/schedule [get]
func (h Handler) Schedule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f invariant.Fields
	num := func(key string, required bool) int64 {
		s := q.Get(key)
		if s == "" {
			if required {
				f.Add(key, "Obligatoire.")
			}
			return 0
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			f.Add(key, "Un nombre entier est attendu.")
		}
		return n
	}
	t := Terms{PrincipalCents: money.Cents(num("principal_cents", true)), AnnualRatePPM: num("annual_rate_ppm", true),
		TermMonths: int(num("term_months", true)), InsuranceCents: money.Cents(num("insurance_cents", false))}
	if d, err := period.ParseDate(q.Get("first_due_on")); err != nil {
		f.Add("first_due_on", "Une date AAAA-MM-JJ est attendue.")
	} else {
		t.FirstDueOn = d
	}
	if err := f.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if caller(r).Active == nil {
		httpx.Error(w, r, invariant.ErrForbidden)
		return
	}
	v, err := h.Service.Preview(t)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
