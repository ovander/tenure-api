package inventory

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
)

// Handler serves the asset layouts and the inventory templates.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// GetLayout godoc
//
//	@Summary		An asset's layout: its rooms and their elements
//	@Description	Version 0 until the layout is seeded or saved. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"asset id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	LayoutView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/layout [get]
func (h Handler) GetLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Layout(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutLayout godoc
//
//	@Summary		Replace an asset's layout
//	@Description	The whole layout in order, with If-Match on its version (W/"0" before the first save). A room or element left out is removed; ids keep past inventories linked.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"asset id"
//	@Param			If-Match		header		string		true	"layout version, W/\"n\""
//	@Param			body			body		LayoutInput	true	"layout"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	LayoutView
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/layout [put]
func (h Handler) PutLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in LayoutInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveLayout(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// SeedLayout godoc
//
//	@Summary		Seed an asset's first layout from a template
//	@Description	From the latest template of the asset's kind (RENT-INV-001 §4): bedrooms numbered, rooms needing an absent feature left out, the furniture of a furnished dwelling added. Refused when the asset has rooms.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"asset id"
//	@Param			body			body		SeedInput	true	"what the template needs"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	LayoutView
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/layout/seed [post]
func (h Handler) SeedLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in SeedInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SeedLayout(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// ListTemplates godoc
//
//	@Summary		The inventory templates the scope can read
//	@Description	The system templates and the owner's own, newest version first within each asset kind. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		TemplateView
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventory-templates [get]
func (h Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Templates(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
