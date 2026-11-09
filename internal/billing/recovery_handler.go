package billing

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/httpx"
)

// Recovery godoc
//
//	@Summary		The recovery of a lease's unpaid rent
//	@Description	Where the recovery stands (RENT-SPEC-011, Q-121): the arrears, the episode's first overdue due day, the steps done, the next steps with the day each falls due, the clause résolutoire's day after a commandement de payer, and the lease's natural-person sureties.
//	@Tags			billing
//	@Produce		json
//	@Param			id				path		string	true	"lease id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	RecoveryResult
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/recovery [get]
func (h Handler) Recovery(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.RecoveryFor(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RecoveryLetter godoc
//
//	@Summary		Prepare a recovery letter (MD or IC)
//	@Description	The formal notice (mise en demeure, MD) or the information of a natural-person surety (IC) of the overdue rent calls and their total. Nothing overdue is refused. dry_run only lists the debt.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string				true	"lease id"
//	@Param			body			body		RecoveryLetterInput	true	"letter"
//	@Param			X-Owner-Scope	header		string				false	"owner scope"
//	@Success		200				{object}	RecoveryLetter
//	@Failure		400,401,403,404,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/recovery/letter [post]
func (h Handler) RecoveryLetter(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in RecoveryLetterInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecoveryLetter(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RecoveryStep godoc
//
//	@Summary		Record a recovery step
//	@Description	A step done on a day, immutable: the formal notice or the surety's information on its letter, the insurer's claim with its deadline, the commandement de payer and its notice to the surety, the court referral, each with its delivery, reference and deed if uploaded. Needs something overdue on its day.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string				true	"lease id"
//	@Param			body			body		RecoveryStepInput	true	"step"
//	@Param			X-Owner-Scope	header		string				false	"owner scope"
//	@Success		200				{object}	RecoveryResult
//	@Failure		400,401,403,404,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/recovery/steps [post]
func (h Handler) RecoveryStep(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in RecoveryStepInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecordRecoveryStep(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// RecoveryPlan godoc
//
//	@Summary		Record a payment plan of overdue rent
//	@Description	The plan agreed on a day covers every rent call overdue on it and their whole debt, in instalments adding up to it on increasing days; while respected (instalments paid within their grace, the current rent paid) it suspends the recovery. One plan in force per lease. dry_run only checks it.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"lease id"
//	@Param			body			body		PlanInput	true	"plan"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	RecoveryResult
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/recovery/plans [post]
func (h Handler) RecoveryPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in PlanInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.RecordPlan(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CancelRecoveryPlan godoc
//
//	@Summary		Cancel a payment plan
//	@Description	Cancels the lease's latest payment plan on a day, which ends its suspension of the recovery. A completed or cancelled plan is refused.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"lease id"
//	@Param			pid				path		string			true	"plan id"
//	@Param			body			body		CancelPlanInput	true	"cancellation"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	RecoveryResult
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/leases/{id}/recovery/plans/{pid}/cancel [post]
func (h Handler) CancelRecoveryPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	pid, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "pid"))
	if !ok {
		return
	}
	var in CancelPlanInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CancelPlan(r.Context(), caller(r), httpx.RequestID(r), id, pid, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
