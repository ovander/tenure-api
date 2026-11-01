package lease

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/problem"
)

// GenerateContract godoc
//
//	@Summary		Generate the contract of a draft lease
//	@Description	CT-<asset>-<YYYY>-<seq>, from the lease, its assets, owner, tenants, guarantees and diagnostics (RENT-SPEC-008). Refused with the type of the first blocker; GET /leases/{id}/blockers lists them all.
//	@Tags			lease
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		201				{object}	DocumentView
//	@Failure		401,403,404,409,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/contract [post]
func (h Handler) GenerateContract(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.GenerateContract(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// Blockers godoc
//
//	@Summary		List what blocks a lease's contract and activation
//	@Description	Generation and activation blockers of a draft lease, and the warnings of any lease (RENT-SPEC-008 owner view).
//	@Tags			lease
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	BlockersView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/blockers [get]
func (h Handler) Blockers(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Blockers(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	lang := problem.Language(httpx.Lang(r))
	for _, list := range [][]Blocker{v.Generation, v.Activation, v.Warnings} {
		for i := range list {
			if m, ok := problem.Lookup(list[i].Type); ok {
				list[i].Title = m.FR
				if lang == "en" {
					list[i].Title = m.EN
				}
			}
		}
	}
	httpx.JSON(w, http.StatusOK, v)
}

// ListLeaseDocuments godoc
//
//	@Summary	List the documents generated from a lease
//	@Tags		lease
//	@Produce	json
//	@Param		id				path		string	true	"lease id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{array}		DocumentView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases/{id}/documents [get]
func (h Handler) ListLeaseDocuments(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.LeaseDocuments(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// Amend godoc
//
//	@Summary		Amend a current lease
//	@Description	A rent change outside IRL (the deposit held never changes), a new tenant or an added box; issues the numbered amendment AV-… (RENT-SPEC-008).
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			body			body		AmendmentInput	true	"amendment"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	AmendmentResult
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/amendments [post]
func (h Handler) Amend(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in AmendmentInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Amend(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(v.Lease.Version))
	httpx.JSON(w, http.StatusCreated, v)
}

// CreateInventory godoc
//
//	@Summary		Record a lease's entry or exit inventory
//	@Description	From its uploaded document (class inventory, this lease); recorded once.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			body			body		InventoryInput	true	"inventory"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	InventoryView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/inventories [post]
func (h Handler) CreateInventory(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in InventoryInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateInventory(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// ListInventories godoc
//
//	@Summary	List a lease's inventories
//	@Tags		lease
//	@Produce	json
//	@Param		id				path		string	true	"lease id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{array}		InventoryView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/leases/{id}/inventories [get]
func (h Handler) ListInventories(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Inventories(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GuaranteeDocument godoc
//
//	@Summary		Generate a guarantee's document
//	@Description	The deposit receipt (DG) of a deposit held, or the acte de cautionnement (CA) of a surety, one per guarantor (RENT-SPEC-008).
//	@Tags			lease
//	@Produce		json
//	@Param			id				path		string	true	"guarantee id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		201				{object}	DocumentView
//	@Failure		401,403,404,409,412,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/guarantees/{id}/document [post]
func (h Handler) GuaranteeDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.GuaranteeDocument(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}
