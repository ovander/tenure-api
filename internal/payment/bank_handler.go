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
