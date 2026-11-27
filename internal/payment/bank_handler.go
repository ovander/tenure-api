package payment

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// ListAccounts godoc
//
//	@Summary		The owner's bank accounts
//	@Description	Each with its label and the last four IBAN characters; the full IBAN is never stored (RENT-SPEC-013, Q-123).
//	@Tags			payment
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		AccountView
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-accounts [get]
func (h Handler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Accounts(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CreateAccount godoc
//
//	@Summary		Record a bank account
//	@Description	A label, and the last four IBAN characters to tell accounts apart.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			body			body		AccountInput	true	"account"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	AccountView
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-accounts [post]
func (h Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var in AccountInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateAccount(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// Import godoc
//
//	@Summary		Import a bank statement (CSV or OFX)
//	@Description	A statement uploaded through POST /documents into an account: OFX (version 1 or 2), or CSV read with its mapping (delimiter, decimal point, date column and format, a signed amount or debit and credit columns, label, reference). A statement that cannot be read whole imports nothing; a line the account already holds is counted, not added (Q-123). dry_run only reads it.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			body			body		ImportInput	true	"statement"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	ImportResult
//	@Failure		400,401,403,404,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/payments/import [post]
func (h Handler) Import(w http.ResponseWriter, r *http.Request) {
	var in ImportInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Import(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// ListLines godoc
//
//	@Summary		Bank lines
//	@Description	The imported bank lines, the latest booked first, by account and status; an unmatched line past the alert delay is flagged stale.
//	@Tags			payment
//	@Produce		json
//	@Param			account_id		query		string	false	"bank account"
//	@Param			status			query		string	false	"unmatched, matched or ignored"
//	@Param			page			query		int		false	"page, from 1"
//	@Param			size			query		int		false	"page size, 1 to 100"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	httpx.List[LineView]
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines [get]
func (h Handler) ListLines(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var f LineFilter
	var fe invariant.Fields
	if v := q.Get("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fe.Add("account_id", "Identifiant invalide.")
		}
		f.AccountID = &id
	}
	switch s := q.Get("status"); s {
	case "", LineUnmatched, LineMatched, LineIgnored:
		f.Status = s
	default:
		fe.Add("status", "Attendu : unmatched, matched ou ignored.")
	}
	if err := fe.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, total, err := h.Service.Lines(r.Context(), caller(r), httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// SetLineStatus godoc
//
//	@Summary		Ignore or reopen a bank line
//	@Description	Ignore an unmatched line (a transfer between one's own accounts, a bank fee), with a note, or reopen an ignored one. A matched line is refused.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"bank line id"
//	@Param			body			body		LineStatusInput	true	"status"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	LineView
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines/{id}/status [post]
func (h Handler) SetLineStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in LineStatusInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SetLineStatus(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// Suggestions godoc
//
//	@Summary		The rent calls a bank credit may pay
//	@Description	Strongest rule first (Q-124): the rent call's number and the amount owed; a tenant's name and the amount owed; a housing allowance (CAF, MSA) naming a tenant, for at most the amount owed; the amount owed, booked near the call's due day. A debit, or a line already matched or ignored, has none.
//	@Tags			payment
//	@Produce		json
//	@Param			id				path		string	true	"bank line id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		Suggestion
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines/{id}/suggestions [get]
func (h Handler) Suggestions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Suggestions(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// MatchLine godoc
//
//	@Summary		Match a bank credit to rent
//	@Description	Records the credit as payments of the leases it is split across (a third party may pay; one line may pay several leases), adding up to its amount; each is paid on the line's day and applied to the lease's oldest unpaid rent calls. The line becomes matched.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"bank line id"
//	@Param			body			body		MatchLineInput	true	"allocations"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	MatchLineResult
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines/{id}/match [post]
func (h Handler) MatchLine(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in MatchLineInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.MatchLine(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// PaymentSuggestions godoc
//
//	@Summary		The payments already recorded that a bank credit may be
//	@Description	Payments entered by hand that name no bank line yet, of at most the credit's amount, paid within bank.matching's window of the line's day; the exact amount first, then the nearest day. Linking one instead of matching the line to rent keeps a payment from being recorded twice. A debit, or a line already matched or ignored, has none.
//	@Tags			payment
//	@Produce		json
//	@Param			id				path		string	true	"bank line id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		View
//	@Failure		400,401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines/{id}/payment-suggestions [get]
func (h Handler) PaymentSuggestions(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.PaymentSuggestions(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// LinkPayments godoc
//
//	@Summary		Link a bank credit to payments already recorded
//	@Description	The credit is payments the owner already entered by hand: payments of the scope that name no bank line, adding up to exactly its amount, now name it, and no payment is recorded. The line becomes matched.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string				true	"bank line id"
//	@Param			body			body		LinkPaymentsInput	true	"payments"
//	@Param			X-Owner-Scope	header		string				false	"owner scope"
//	@Success		200				{object}	MatchLineResult
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines/{id}/payments [post]
func (h Handler) LinkPayments(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in LinkPaymentsInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.LinkPayments(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// AutoMatch godoc
//
//	@Summary		Match the sure bank credits
//	@Description	Matches, without asking, every unmatched credit with one sure suggestion (the rent call's number, the tenant's name and amount, or an allowance naming the tenant); the amount-and-period rule alone and any ambiguity stay for the owner. dry_run only lists them.
//	@Tags			payment
//	@Accept			json
//	@Produce		json
//	@Param			body			body		AutoMatchInput	true	"account, dry run"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	AutoMatchResult
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bank-lines/auto [post]
func (h Handler) AutoMatch(w http.ResponseWriter, r *http.Request) {
	var in AutoMatchInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.AutoMatch(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
