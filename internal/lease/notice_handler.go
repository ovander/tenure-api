package lease

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
)

// Notice godoc
//
//	@Summary		A lease's recorded notice
//	@Description	The notice that ends the lease (RENT-SPEC-010): who gave it, its ground, when it was received and how, and the end it set. Not for an accountant.
//	@Tags			lease
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	NoticeView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/notice [get]
func (h Handler) Notice(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Notice(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GiveNotice godoc
//
//	@Summary		Record a notice and put the lease under notice
//	@Description	The tenant's notice, with its reduced-notice ground if any, or the owner's on the letter generated for the lease, received on a date: tenure computes the end (Q-118) and the lease moves to notice. One notice per lease; dry_run only computes.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"lease id"
//	@Param			body			body		NoticeInput	true	"notice"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	NoticeResult
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/notice [post]
func (h Handler) GiveNotice(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in NoticeInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.GiveNotice(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// NoticeLetter godoc
//
//	@Summary		Prepare the owner's notice letter (CG)
//	@Description	For an active unfurnished or furnished lease, on a legal ground (sale with its price, repossession with its beneficiary, legitimate ground), never to a protected tenant without a rehousing offer: the letter for the end of the next term it can still reach, with the last day the tenant must receive it. dry_run only computes the term.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string				true	"lease id"
//	@Param			body			body		NoticeLetterInput	true	"ground"
//	@Param			X-Owner-Scope	header		string				false	"owner scope"
//	@Success		200				{object}	NoticeLetter
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/notice/letter [post]
func (h Handler) NoticeLetter(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in NoticeLetterInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.NoticeLetter(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
