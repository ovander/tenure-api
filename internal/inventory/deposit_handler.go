package inventory

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
)

// GetGrids godoc
//
//	@Summary		The scope's wear grids
//	@Description	Grids entered by the owner from a collective agreement or agreed by the parties (INV-OD-08: none is shipped). Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			X-Owner-Scope	header	string	false	"owner scope"
//	@Success		200				{array}	GridView
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/vetuste-grids [get]
func (h Handler) GetGrids(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Grids(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// PostGrid godoc
//
//	@Summary		Record a wear grid
//	@Description	Per category: theoretical lifetime, franchise, yearly flat reduction and floor, rates in basis points (décret 2016-382 art. 4).
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			body			body		GridInput	true	"grid"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	GridView
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/vetuste-grids [post]
func (h Handler) PostGrid(w http.ResponseWriter, r *http.Request) {
	var in GridInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveGrid(r.Context(), caller(r), httpx.RequestID(r), nil, 0, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// PutGrid godoc
//
//	@Summary		Replace a wear grid
//	@Description	With If-Match on its version. A statement already issued keeps the grid as it was (snapshot).
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"grid id"
//	@Param			If-Match		header		string		true	"grid version, W/\"n\""
//	@Param			body			body		GridInput	true	"grid"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	GridView
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/vetuste-grids/{id} [put]
func (h Handler) PutGrid(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in GridInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveGrid(r.Context(), caller(r), httpx.RequestID(r), &id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutLeaseGrid godoc
//
//	@Summary		Annex a wear grid to a lease
//	@Description	Or remove it (null), until the deposit is settled.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			body			body		GridInputLease	true	"grid"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	DepositView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/vetuste-grid [put]
func (h Handler) PutLeaseGrid(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in GridInputLease
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.AttachGrid(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GetDeposit godoc
//
//	@Summary		Where a lease's deposit stands
//	@Description	The grid, the exit items worse or missing and the missing keys, the deductions, the next statement (wait_exit, initial, release, done) previewed as of today, and the statements issued. Not for an accountant, who reads the RD documents.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	DepositView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/deposit [get]
func (h Handler) GetDeposit(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Deposit(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GetDeductions godoc
//
//	@Summary		A lease's deposit deductions
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path	string	true	"lease id"
//	@Param			X-Owner-Scope	header	string	false	"owner scope"
//	@Success		200				{array}	DeductionView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/deposit-deductions [get]
func (h Handler) GetDeductions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Deposit(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v.Deductions)
}

// PostDeduction godoc
//
//	@Summary		Retain a deduction from the deposit
//	@Description	On an exit item worse or missing at exit (exit_item_id), or a missing key line (key), qualified by the owner as the tenant's; the cost from a quote or invoice uploaded through POST /documents (class supporting, this lease). The tenant's share comes from the lease's wear grid for the item's category and age; without a grid, the whole cost.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			body			body		DeductionCreate	true	"deduction"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	DeductionView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/deposit-deductions [post]
func (h Handler) PostDeduction(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in DeductionCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.AddDeduction(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// DeleteDeduction godoc
//
//	@Summary		Remove a deduction
//	@Description	Until the deposit is settled.
//	@Tags			inventory
//	@Param			id				path	string	true	"deduction id"
//	@Param			X-Owner-Scope	header	string	false	"owner scope"
//	@Success		204
//	@Failure		401,403,404,409	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/deposit-deductions/{id} [delete]
func (h Handler) DeleteDeduction(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.Service.DeleteDeduction(r.Context(), caller(r), httpx.RequestID(r), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PostSettlement godoc
//
//	@Summary		Issue the deposit restitution statement
//	@Description	The first statement (RD) deducts the deductions and the unpaid rent calls, holds back a share in a collective building whose accounts are not approved (accounts_approved_on absent), adds the late penalty owed, and is due from the return of the keys; release: true issues the second, releasing the holdback net of the regularisation backed by its document, due from the approval. dry_run computes it only. The deductions no longer change once the first is issued.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"lease id"
//	@Param			body			body		SettleInput	true	"settlement"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	Statement
//	@Success		201				{object}	Statement
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/deposit-settlement [post]
func (h Handler) PostSettlement(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in SettleInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Settle(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusCreated
	if in.DryRun {
		status = http.StatusOK
	}
	httpx.JSON(w, status, v)
}
