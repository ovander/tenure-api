package ledger

import (
	"net/http"
	"strconv"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Handler serves the journal routes.
type Handler struct{ Service Service }

// Get godoc
//
//	@Summary		The owner's journal of a year
//	@Description	Every movement of the year posted on a cash basis, in double entry, to the accounts of the chart in legal_params: rent received (rent, provisions, unapplied), charges paid (their recoverable part to the provisions account), loan instalments, deposits received (a liability) and deposit statements; and the balance of each account. closed says the year's tax summary is issued: what it was computed from no longer changes (Q-127).
//	@Tags			ledger
//	@Produce		json
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/ledger [get]
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		httpx.Error(w, r, f)
		return
	}
	p, _ := auth.FromContext(r.Context())
	v, err := h.Service.Year(r.Context(), p, httpx.RequestID(r), year)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (h Handler) export(w http.ResponseWriter, r *http.Request, kind string) {
	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		httpx.Error(w, r, f)
		return
	}
	p, _ := auth.FromContext(r.Context())
	f, err := h.Service.Export(r.Context(), p, httpx.RequestID(r), year, kind)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+f.Name+`"`)
	_, _ = w.Write(f.Data)
}

// JournalCSV godoc
//
//	@Summary		The year's journal as CSV
//	@Description	One row per line of each entry, numbered, amounts in cents (Q-128). Requires a recent multi-factor login; the export is audited.
//	@Tags			ledger
//	@Produce		text/csv
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{file}		file
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/ledger/journal.csv [get]
func (h Handler) JournalCSV(w http.ResponseWriter, r *http.Request) { h.export(w, r, ExportJournal) }

// FEC godoc
//
//	@Summary		The year's fichier des écritures comptables
//	@Description	The eighteen fields of LPF art. A47 A-1, tab-separated, ISO 8859-15, dates AAAAMMJJ, amounts with a decimal comma; only for a closed year, validated the day its tax summary was issued (Q-128). Requires a recent multi-factor login; the export is audited.
//	@Tags			ledger
//	@Produce		plain
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{file}		file
//	@Failure		400,401,403,409,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/ledger/fec.txt [get]
func (h Handler) FEC(w http.ResponseWriter, r *http.Request) { h.export(w, r, ExportFEC) }

// Pack godoc
//
//	@Summary		The year-end pack
//	@Description	A ZIP of the journal, the balances, the index of the supporting documents and a note; for a closed year, the FEC and the issued 2044 lines too (Q-128). Requires a recent multi-factor login; the export is audited.
//	@Tags			ledger
//	@Produce		application/zip
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{file}		file
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/ledger/pack.zip [get]
func (h Handler) Pack(w http.ResponseWriter, r *http.Request) { h.export(w, r, ExportPack) }
