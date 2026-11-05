package inventory

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
)

// GetPreview godoc
//
//	@Summary		The completed inventory as a PDF to sign
//	@Description	Unnumbered and not archived, marked as a draft: printed to be signed by hand, then uploaded as the signed copy. Only once the capture is completed.
//	@Tags			inventory
//	@Produce		application/pdf
//	@Param			id				path	string	true	"inventory id"
//	@Param			X-Owner-Scope	header	string	false	"owner scope"
//	@Success		200				{file}	file
//	@Failure		401,403,404,409,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/preview [get]
func (h Handler) GetPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	pdf, err := h.Service.Preview(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/pdf")
	hd.Set("Content-Disposition", `inline; filename="etat-des-lieux-a-signer.pdf"`)
	hd.Set("Content-Length", strconv.Itoa(len(pdf)))
	hd.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// GetSignatures godoc
//
//	@Summary		An inventory's parties, signatures and electronic request
//	@Description	The signing links of a pending request are shown to the owner only. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"inventory id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	SignaturesView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/signatures [get]
func (h Handler) GetSignatures(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Signatures(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, v)
}

// PostSignatures godoc
//
//	@Summary		Sign a completed inventory
//	@Description	wet_scan: the signed copy (uploaded through POST /documents, class inventory, this lease) and the parties who signed it, with their reservations. esign: a request to the signature provider for every party still to sign, each with an e-mail; the answer carries one link per party, opened on the tablet in turn. When the last party has signed, the inventory is issued into its EE document.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"inventory id"
//	@Param			body			body		SignInput	true	"signatures"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	SignaturesView
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/signatures [post]
func (h Handler) PostSignatures(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in SignInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Sign(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, v)
}

// PostSignaturesRefresh godoc
//
//	@Summary		Read the pending electronic request at the provider
//	@Description	Records each party who has signed; once all have, archives the signed file as evidence and issues the EE document. Called after each party signs on the tablet; no webhook.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"inventory id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	SignaturesView
//	@Failure		401,403,404,409,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/signatures/refresh [post]
func (h Handler) PostSignaturesRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Refresh(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, v)
}

// PostSignaturesCancel godoc
//
//	@Summary		Cancel the pending electronic request
//	@Description	At the provider and in Tenure, so the parties may sign by hand instead.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"inventory id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	SignaturesView
//	@Failure		401,403,404,409,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/signatures/cancel [post]
func (h Handler) PostSignaturesCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.CancelRequest(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
