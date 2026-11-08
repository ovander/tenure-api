package lease

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/httpx"
)

// Renewals godoc
//
//	@Summary		A lease's rent re-evaluation proposals
//	@Description	The owner's proposals of a new rent at the renewal of an unfurnished lease (RENT-SPEC-010, loi 89-462 art. 17-2, Q-120), the latest renewal first: the references, the schedule of steps, the reception, the tenant's answer and the steps applied and due.
//	@Tags			lease
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		RenewalView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/renewals [get]
func (h Handler) Renewals(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Renewals(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RenewalLetter godoc
//
//	@Summary		Prepare the proposal of a new rent at renewal (PR)
//	@Description	For an active unfurnished lease outside rent control: the proposed rent and its references (three, six in an agglomeration of more than a million inhabitants), at most the cap of a zone tendue, for the next renewal the proposal can still reach; the increase in yearly steps, thirds or sixths. A proposal not yet received is replaced. dry_run only computes.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string				true	"lease id"
//	@Param			body			body		RenewalLetterInput	true	"proposal"
//	@Param			X-Owner-Scope	header		string				false	"owner scope"
//	@Success		200				{object}	RenewalLetter
//	@Failure		400,401,403,404,409,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/renewal/letter [post]
func (h Handler) RenewalLetter(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in RenewalLetterInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RenewalLetter(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func renewalIDs(w http.ResponseWriter, r *http.Request) (lease, rid uuid.UUID, ok bool) {
	l, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "rid"))
	return l, p, ok
}

// RenewalReception godoc
//
//	@Summary		Record the reception of a rent re-evaluation proposal
//	@Description	The day the tenant received the proposal, at the latest its lead time before the term, and how it was delivered.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			rid				path		string			true	"proposal id"
//	@Param			body			body		ReceptionInput	true	"reception"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	RenewalView
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/renewals/{rid}/reception [post]
func (h Handler) RenewalReception(w http.ResponseWriter, r *http.Request) {
	id, rid, ok := renewalIDs(w, r)
	if !ok {
		return
	}
	var in ReceptionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecordReception(r.Context(), caller(r), httpx.RequestID(r), id, rid, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RenewalAnswer godoc
//
//	@Summary		Record the tenant's answer to a rent re-evaluation proposal
//	@Description	The tenant's agreement or refusal of a received proposal, on a day. A refusal leaves the rent as it is (conciliation, then the judge, Q-120).
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"lease id"
//	@Param			rid				path		string		true	"proposal id"
//	@Param			body			body		AnswerInput	true	"answer"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	RenewalView
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/renewals/{rid}/answer [post]
func (h Handler) RenewalAnswer(w http.ResponseWriter, r *http.Request) {
	id, rid, ok := renewalIDs(w, r)
	if !ok {
		return
	}
	var in AnswerInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecordAnswer(r.Context(), caller(r), httpx.RequestID(r), id, rid, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RenewalSteps godoc
//
//	@Summary		Apply the due steps of an accepted rent re-evaluation
//	@Description	Adds to the lease's rent the share of the increase of each step whose day has come, on top of any IRL revision since the letter. dry_run only computes.
//	@Tags			lease
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"lease id"
//	@Param			rid				path		string		true	"proposal id"
//	@Param			body			body		StepsInput	true	"version"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	StepsResult
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/renewals/{rid}/steps [post]
func (h Handler) RenewalSteps(w http.ResponseWriter, r *http.Request) {
	id, rid, ok := renewalIDs(w, r)
	if !ok {
		return
	}
	var in StepsInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.ApplySteps(r.Context(), caller(r), httpx.RequestID(r), id, rid, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
