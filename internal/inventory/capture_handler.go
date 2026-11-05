package inventory

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
)

// GetCapture godoc
//
//	@Summary		An inventory as captured
//	@Description	Header, rooms and items, meters, keys, the parties who sign and the open checks. The ETag is the inventory's version. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"inventory id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	CaptureView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id} [get]
func (h Handler) GetCapture(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Capture(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutCapture godoc
//
//	@Summary		Replace an inventory's header
//	@Description	The date it is done (never in the future), the appointment, the place and the general observations, with If-Match on the inventory's version. Until the inventory is completed.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"inventory id"
//	@Param			If-Match		header		string		true	"inventory version, W/\"n\""
//	@Param			body			body		HeaderInput	true	"header"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	CaptureView
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id} [put]
func (h Handler) PutCapture(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in HeaderInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveHeader(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutItem godoc
//
//	@Summary		Record an item as found
//	@Description	Condition on the scale, works, quantity and comment, with If-Match on the item's version; clears « to check ». In a draft or on site.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"inventory id"
//	@Param			itemId			path		string		true	"item id"
//	@Param			If-Match		header		string		true	"item version, W/\"n\""
//	@Param			body			body		ItemInput	true	"item"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	ItemView
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/items/{itemId} [put]
func (h Handler) PutItem(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	item, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "itemId"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in ItemInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveItem(r.Context(), caller(r), httpx.RequestID(r), id, item, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PostItem godoc
//
//	@Summary		Add an item found on site
//	@Description	In a room of the inventory (room_id or its label) or a new room. In a draft or on site.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"inventory id"
//	@Param			body			body		NewItemInput	true	"item"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	ItemView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/items [post]
func (h Handler) PostItem(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in NewItemInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.AddItem(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// PutMeters godoc
//
//	@Summary		Replace an inventory's meter readings
//	@Description	The whole list, with If-Match on the inventory's version; an id keeps a meter and its photos, and a meter with photos cannot be removed. Until completed.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"inventory id"
//	@Param			If-Match		header		string		true	"inventory version, W/\"n\""
//	@Param			body			body		MetersInput	true	"meters"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	CaptureView
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/meters [put]
func (h Handler) PutMeters(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in MetersInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveMeters(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutKeys godoc
//
//	@Summary		Replace an inventory's keys and access devices
//	@Description	The whole list, with If-Match on the inventory's version. Until completed.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"inventory id"
//	@Param			If-Match		header		string		true	"inventory version, W/\"n\""
//	@Param			body			body		KeysInput	true	"keys"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	CaptureView
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/keys [put]
func (h Handler) PutKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in KeysInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveKeys(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PostTransition godoc
//
//	@Summary		Move an inventory along its lifecycle
//	@Description	schedule (with scheduled_at and place), start (the date becomes today unless set), complete (the mandatory mentions checked), reopen, issue (a signed inventory into its EE document). With dry_run the answer is a TransitionResult (allowed, checks) and nothing changes.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"inventory id"
//	@Param			body			body		TransitionInput	true	"transition"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	CaptureView
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/transitions [post]
func (h Handler) PostTransition(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in TransitionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, dry, err := h.Service.Transition(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if dry != nil {
		httpx.JSON(w, http.StatusOK, dry)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
