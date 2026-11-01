package lease

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/problem"
)

// Handler serves /leases and /guarantees.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// List godoc
//
//	@Summary	List the scope's leases
//	@Tags		lease
//	@Produce	json
//	@Param		state			query		string	false	"draft, active, notice, ended or closed"
//	@Param		asset_id		query		string	false	"leases over this asset"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[View]
//	@Failure	400,401,403		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	var f Filter
	q := r.URL.Query()
	if s := q.Get("state"); s != "" {
		valid := false
		for _, x := range States {
			valid = valid || string(x) == s
		}
		if !valid {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "état inconnu", nil)
			return
		}
		f.State = s
	}
	if a := q.Get("asset_id"); a != "" {
		id, err := uuid.Parse(a)
		if err != nil {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "asset_id invalide", nil)
			return
		}
		f.AssetID = &id
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
//	@Summary	Get a lease
//	@Tags		lease
//	@Produce	json
//	@Param		id				path		string	true	"lease id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases/{id} [get]
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
//	@Summary		Draft a lease
//	@Description	Checks every lease rule at save: one ownership across the assets, zone ceiling, relocation rule.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			body			body		Input	true	"lease"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		201				{object}	View
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases [post]
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
//	@Summary	Update a draft lease
//	@Tags		lease
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string	true	"lease id"
//	@Param		If-Match		header		string	true	"current version"
//	@Param		body			body		Input	true	"lease"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases/{id} [put]
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

// Transition godoc
//
//	@Summary	Move a lease along its lifecycle (activate, notice, end, close)
//	@Tags		lease
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string			true	"lease id"
//	@Param		body			body		TransitionInput	true	"transition"
//	@Param		X-Owner-Scope	header		string			false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	400,401,403,404,409,415,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases/{id}/transitions [post]
func (h Handler) Transition(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in TransitionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Transition(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ListGuarantees godoc
//
//	@Summary		List a lease's guarantees
//	@Description	An accountant does not see the guarantor.
//	@Tags			lease
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		GuaranteeView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/guarantees [get]
func (h Handler) ListGuarantees(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	out, err := h.Service.Guarantees(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// CreateGuarantee godoc
//
//	@Summary	Add a guarantee to a lease
//	@Tags		lease
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string			true	"lease id"
//	@Param		body			body		GuaranteeInput	true	"guarantee"
//	@Param		X-Owner-Scope	header		string			false	"owner scope"
//	@Success	201				{object}	GuaranteeView
//	@Failure	400,401,403,404,409,415,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases/{id}/guarantees [post]
func (h Handler) CreateGuarantee(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in GuaranteeInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateGuarantee(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateGuarantee godoc
//
//	@Summary	Update a guarantee
//	@Tags		lease
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string			true	"guarantee id"
//	@Param		If-Match		header		string			true	"current version"
//	@Param		body			body		GuaranteeInput	true	"guarantee"
//	@Param		X-Owner-Scope	header		string			false	"owner scope"
//	@Success	200				{object}	GuaranteeView
//	@Failure	400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/guarantees/{id} [put]
func (h Handler) UpdateGuarantee(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in GuaranteeInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateGuarantee(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
