package maintenance

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Handler serves the contractor and incident routes.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// ListContractors godoc
//
//	@Summary		Contractors
//	@Description	The owner's directory of contractors, by name (RENT-SPEC-015, Q-130).
//	@Tags			maintenance
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		Contractor
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/contractors [get]
func (h Handler) ListContractors(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Contractors(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CreateContractor godoc
//
//	@Summary		Record a contractor
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			body			body		ContractorInput	true	"contractor"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	Contractor
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/contractors [post]
func (h Handler) CreateContractor(w http.ResponseWriter, r *http.Request) {
	var in ContractorInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateContractor(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateContractor godoc
//
//	@Summary		Change a contractor
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"contractor id"
//	@Param			If-Match		header		string			true	"current version"
//	@Param			body			body		ContractorInput	true	"contractor"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	Contractor
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/contractors/{id} [put]
func (h Handler) UpdateContractor(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in ContractorInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateContractor(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ListIncidents godoc
//
//	@Summary		Incidents
//	@Description	The scope's incidents and works, the latest reported first; of one asset with asset_id, in one status with status, or still open (neither invoiced nor cancelled) with open=true (RENT-SPEC-015, Q-130).
//	@Tags			maintenance
//	@Produce		json
//	@Param			asset_id		query		string	false	"asset"
//	@Param			status			query		string	false	"status"	Enums(new,quoted,scheduled,done,invoiced,cancelled)
//	@Param			open			query		bool	false	"open incidents only"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		Incident
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/incidents [get]
func (h Handler) ListIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f invariant.Fields
	var filter IncidentFilter
	if v := q.Get("asset_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			f.Add("asset_id", "Identifiant invalide.")
		}
		filter.AssetID = &id
	}
	if v := q.Get("status"); v != "" {
		if !oneOf(v, string(StatusNew), string(StatusQuoted), string(StatusScheduled), string(StatusDone), string(StatusInvoiced), string(StatusCancelled)) {
			f.Add("status", "Statut inconnu.")
		}
		filter.Status = v
	}
	switch q.Get("open") {
	case "", "false":
	case "true":
		filter.Open = true
	default:
		f.Add("open", "Attendu : true ou false.")
	}
	if err := f.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	v, err := h.Service.Incidents(r.Context(), caller(r), httpx.RequestID(r), filter)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GetIncident godoc
//
//	@Summary		An incident
//	@Tags			maintenance
//	@Produce		json
//	@Param			id				path		string	true	"incident id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	Incident
//	@Failure		400,401,403,404	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/incidents/{id} [get]
func (h Handler) GetIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Incident(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// CreateIncident godoc
//
//	@Summary		Report an incident
//	@Description	An incident reported by the tenant, or works the owner starts, on an asset, optionally under one of its leases, with photos uploaded beforehand as documents. It starts in status new.
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			body			body		IncidentInput	true	"report"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	Incident
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/incidents [post]
func (h Handler) CreateIncident(w http.ResponseWriter, r *http.Request) {
	var in IncidentInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateIncident(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateIncident godoc
//
//	@Summary		Change an incident's report
//	@Description	Replaces the report (not its asset) while the works are not done: in status new, quoted or scheduled; otherwise 409 maintenance/transition.
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"incident id"
//	@Param			If-Match		header		string			true	"current version"
//	@Param			body			body		IncidentInput	true	"report"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	Incident
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/incidents/{id} [put]
func (h Handler) UpdateIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in IncidentInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateIncident(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// Transition godoc
//
//	@Summary		Move an incident
//	@Description	quote (a contractor and the quote's amount, optionally its document), schedule (the day of the works, a contractor), done (the day they ended), invoice (the amount, the treatment, optionally the invoice's document and the day it was paid) or cancel (an optional reason). The invoice of a repair, or of an improvement of an asset whose kind tax.improvement_deductible lists (the dwellings), creates a confirmed repairs charge, deductible on line 224 of the tax summary; construction, reconstruction or enlargement, and the improvement of other premises, are capitalised on the asset and never deducted (Q-130). 409 maintenance/transition from a status that does not allow the action; 409 ledger/period-locked for a repair charged in a closed year.
//	@Tags			maintenance
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"incident id"
//	@Param			If-Match		header		string			true	"current version"
//	@Param			body			body		TransitionInput	true	"action"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	Incident
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/incidents/{id}/transitions [post]
func (h Handler) Transition(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in TransitionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Transition(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
