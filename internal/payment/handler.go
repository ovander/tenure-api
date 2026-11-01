package payment

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Handler serves /payments.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// List godoc
//
//	@Summary	List the scope's payments
//	@Tags		payment
//	@Produce	json
//	@Param		lease_id		query		string	false	"lease id"
//	@Param		unmatched		query		bool	false	"only payments with an unapplied part"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[View]
//	@Failure	400,401,403,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/payments [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := Filter{Unmatched: q.Get("unmatched") == "true"}
	if v := q.Get("lease_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			var fe invariant.Fields
			fe.Add("lease_id", "Identifiant invalide.")
			httpx.Error(w, r, fe)
			return
		}
		f.LeaseID = &id
	}
	items, total, err := h.Service.List(r.Context(), caller(r), httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// Get godoc
//
//	@Summary	Get a payment and its matches
//	@Tags		payment
//	@Produce	json
//	@Param		id				path		string	true	"payment id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/payments/{id} [get]
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
//	@Summary		Record a payment
//	@Description	With auto_match, the payment is applied at once to the lease's oldest unpaid rent calls.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			body			body		Input	true	"payment"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		201				{object}	View
//	@Failure		400,401,403,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/payments [post]
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
//	@Summary		Update a payment
//	@Description	Refused once a receipt covers the payment (for its amount and date), and below what is already applied.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string	true	"payment id"
//	@Param			If-Match		header		string	true	"current version"
//	@Param			body			body		Input	true	"payment"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/payments/{id} [put]
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

// Match godoc
//
//	@Summary		Apply a payment to rent calls
//	@Description	Explicit matches (split across several rent calls allowed), or auto to apply the unapplied part to the oldest unpaid rent calls.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"payment id"
//	@Param			body			body		MatchInput	true	"matches"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/payments/{id}/matches [post]
func (h Handler) Match(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in MatchInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Match(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
