package sci

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

// Handler serves the register routes of an SCI.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// GetRegister godoc
//
//	@Summary		The SCI's register
//	@Description	Its capital in parts, the parts held on the day asked (today by default, Europe/Paris) and its associates with their holdings, in register order (Q-133). 409 entity/not-sci in a person's scope.
//	@Tags			sci
//	@Produce		json
//	@Param			on				query		string	false	"day (YYYY-MM-DD)"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	Register
//	@Failure		401,403,409,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/associates [get]
func (h Handler) GetRegister(w http.ResponseWriter, r *http.Request) {
	var on *period.Date
	if v := r.URL.Query().Get("on"); v != "" {
		d, err := period.ParseDate(v)
		if err != nil {
			var f invariant.Fields
			f.Add("on", "Date attendue : AAAA-MM-JJ.")
			httpx.Error(w, r, f.Err())
			return
		}
		on = &d
	}
	v, err := h.Service.Register(r.Context(), caller(r), httpx.RequestID(r), on)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CreateAssociate godoc
//
//	@Summary		Add an associate
//	@Description	A name only, or a Tenure person: link self (the caller) or the grant_id of a live grant of the SCI (422 sci/link-grant otherwise; 409 sci/associate-linked when that person is already an associate). Parts come by movements.
//	@Tags			sci
//	@Accept			json
//	@Produce		json
//	@Param			body			body		AssociateInput	true	"associate"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	AssociateView
//	@Failure		400,401,403,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/associates [post]
func (h Handler) CreateAssociate(w http.ResponseWriter, r *http.Request) {
	var in AssociateInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateAssociate(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateAssociate godoc
//
//	@Summary		Change an associate
//	@Description	Their name or link; their holdings change only by movements.
//	@Tags			sci
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"associate id"
//	@Param			If-Match		header		string			true	"current version"
//	@Param			body			body		AssociateInput	true	"associate"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	AssociateView
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/associates/{id} [put]
func (h Handler) UpdateAssociate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in AssociateInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateAssociate(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ListMovements godoc
//
//	@Summary		The SCI's movements of parts
//	@Description	Oldest first, each with the holdings it opened and closed.
//	@Tags			sci
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		MovementView
//	@Failure		401,403,409		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/sci/movements [get]
func (h Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Movements(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RecordMovement godoc
//
//	@Summary		Record a movement of parts
//	@Description	A subscription gives new parts within the capital (422 sci/capital-exceeded beyond it). A transfer, a gift or a succession moves parts of an open holding to another associate on its day: the holding closes that day, the receiver's opens that day, and what the giver keeps opens again that day (409 sci/holding-closed, 422 sci/holding-parts, 422 sci/same-associate). A movement is not undone (Q-133).
//	@Tags			sci
//	@Accept			json
//	@Produce		json
//	@Param			body			body		MovementInput	true	"movement"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	MovementView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/sci/movements [post]
func (h Handler) RecordMovement(w http.ResponseWriter, r *http.Request) {
	var in MovementInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecordMovement(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}
