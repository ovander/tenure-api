package maintenance

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// ListContracts godoc
//
//	@Summary		Maintenance contracts
//	@Description	The scope's recurring maintenance (boiler, chimney, other), of one asset with asset_id, each with its latest visit and the next one due (RENT-SPEC-015, Q-131).
//	@Tags			maintenance
//	@Produce		json
//	@Param			asset_id		query		string	false	"asset"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		Contract
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/maintenance-contracts [get]
func (h Handler) ListContracts(w http.ResponseWriter, r *http.Request) {
	var asset *uuid.UUID
	if v := r.URL.Query().Get("asset_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			var f invariant.Fields
			f.Add("asset_id", "Identifiant invalide.")
			httpx.Error(w, r, f)
			return
		}
		asset = &id
	}
	v, err := h.Service.Contracts(r.Context(), caller(r), httpx.RequestID(r), asset)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GetContract godoc
//
//	@Summary		A maintenance contract and its visits
//	@Tags			maintenance
//	@Produce		json
//	@Param			id				path		string	true	"contract id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	Contract
//	@Failure		400,401,403,404	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/maintenance-contracts/{id} [get]
func (h Handler) GetContract(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Contract(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// CreateContract godoc
//
//	@Summary		Record a maintenance contract
//	@Description	A recurring maintenance of an asset. For a boiler or a chimney, the interval and who must have it done default to the legal rhythm in legal_params (maintenance.recurring; for a chimney, by its fuel, required, maintenance.chimney), and a longer interval is refused; for other, both are required. The first visit is due on first_due_on, each next one its interval after the latest visit; the obligations calendar shows it (maintenance_due).
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			body			body		ContractInput	true	"contract"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	Contract
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/maintenance-contracts [post]
func (h Handler) CreateContract(w http.ResponseWriter, r *http.Request) {
	var in ContractInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateContract(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateContract godoc
//
//	@Summary		Change a maintenance contract
//	@Description	Replaces it, not its asset, checked as for POST /maintenance-contracts; set end_on to end it.
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"contract id"
//	@Param			If-Match		header		string			true	"current version"
//	@Param			body			body		ContractInput	true	"contract"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	Contract
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/maintenance-contracts/{id} [put]
func (h Handler) UpdateContract(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in ContractInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateContract(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// RecordVisit godoc
//
//	@Summary		Record a maintenance visit
//	@Description	A visit done (not in the future), with the certificate the contractor gave, uploaded beforehand as a document. The next visit is due its interval after the latest one. A visit is never changed.
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"contract id"
//	@Param			If-Match		header		string		true	"contract's current version"
//	@Param			body			body		VisitInput	true	"visit"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	Contract
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/maintenance-contracts/{id}/visits [post]
func (h Handler) RecordVisit(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in VisitInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecordVisit(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
