package inventory

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
)

// GetCompletions godoc
//
//	@Summary		An entry's completion windows and requests
//	@Description	The windows (loi 89-462 art. 3-2): ten days after the entry, and the first month of the heating season when the entry was done outside it; which are open today; every request with its lines and, once decided, its EC document. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"entry inventory id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	CompletionsView
//	@Failure		401,403,404,409	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/completions [get]
func (h Handler) GetCompletions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Completions(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// PostCompletion godoc
//
//	@Summary		Record a tenant's request to complete an issued entry
//	@Description	The reason (ten_day or heating), the day the request was made, within its window, the tenant's letter when uploaded (POST /documents, class inventory, this lease), and one line per entry item with the state asked for. A heating request covers heating items only. It waits for the owner's decision.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"entry inventory id"
//	@Param			body			body		CompletionInput	true	"request"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	CompletionView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/completions [post]
func (h Handler) PostCompletion(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in CompletionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RequestCompletion(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// PutCompletion godoc
//
//	@Summary		Decide a completion request
//	@Description	Every line accepted or refused, with If-Match on the request's version; issues the EC document. The issued entry is not edited: the accepted lines complete it in the exit pre-fill and the comparison. A decided request no longer changes.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"completion request id"
//	@Param			If-Match		header		string			true	"request version, W/\"n\""
//	@Param			body			body		DecisionInput	true	"decision"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	CompletionView
//	@Failure		400,401,403,404,409,412,415,422,428,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/completions/{id} [put]
func (h Handler) PutCompletion(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in DecisionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.DecideCompletion(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
