package bic

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
)

// Handler serves the furnished-letting routes (RENT-SPEC-019, Q-132).
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// GetGrid godoc
//
//	@Summary		An asset's component grid
//	@Description	The latest version of the asset's grid for furnished letting under the réel; while it has none, the default proposal (version 0, proposed): the lines of bic.components, the acquisition price, the start of its first furnished lease, the acquisition costs as bic.acquisition_costs says (RENT-SPEC-019, Q-132).
//	@Tags			bic
//	@Produce		json
//	@Param			id				path		string	true	"asset id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	GridView
//	@Failure		400,401,403,404	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/bic-grid [get]
func (h Handler) GetGrid(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Grid(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutGrid godoc
//
//	@Summary		Record a version of an asset's component grid
//	@Description	Records the next version (If-Match: the version read, 0 for the first). The shares add up to 100 % (10 000 basis points); a line of 0 months is not depreciated (the land). The basis is the price with the acquisition costs unless they are expensed. A version is never changed; a change after the first needs its reason.
//	@Tags			bic
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"asset id"
//	@Param			If-Match		header		string		true	"current version (0 for none)"
//	@Param			body			body		GridInput	true	"grid"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	GridView
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/bic-grid [put]
func (h Handler) PutGrid(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatchOrZero(w, r)
	if !ok {
		return
	}
	var in GridInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveGrid(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ListItems godoc
//
//	@Summary		An asset's furniture and works
//	@Tags			bic
//	@Produce		json
//	@Param			id				path		string	true	"asset id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		ItemView
//	@Failure		400,401,403,404	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/fixed-assets [get]
func (h Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Items(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CreateItem godoc
//
//	@Summary		Record furniture or works on an asset
//	@Description	Expense or fixed asset (Q-132): works of repair are an expense; improvement and construction works are fixed assets; furniture under bic.small_equipment is expensed. A treatment forced against the rule needs its reason, traced. Furniture takes bic.furniture's months unless given; works need theirs.
//	@Tags			bic
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"asset id"
//	@Param			body			body		ItemInput	true	"item"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	ItemView
//	@Failure		400,401,403,404,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/fixed-assets [post]
func (h Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in ItemInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateItem(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateItem godoc
//
//	@Summary		Change furniture or works
//	@Description	Replaces it, checked as for POST /assets/{id}/fixed-assets; set disposed_on when it leaves the asset.
//	@Tags			bic
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"item id"
//	@Param			If-Match		header		string		true	"current version"
//	@Param			body			body		ItemInput	true	"item"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	ItemView
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/fixed-assets/{id} [put]
func (h Handler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in ItemInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateItem(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// GetDepreciation godoc
//
//	@Summary		An asset's depreciation for a year
//	@Description	Straight-line by days from the day first let furnished (an item: put in service), each line closed on its last day, cents half-up (Q-132): one row per component of the latest grid, then per capitalised item; with the cost of the items expensed that year.
//	@Tags			bic
//	@Produce		json
//	@Param			id				path		string	true	"asset id"
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	DepreciationView
//	@Failure		400,401,403,404,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/depreciation [get]
func (h Handler) GetDepreciation(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	year, _ := strconv.Atoi(r.URL.Query().Get("year"))
	v, err := h.Service.Depreciation(r.Context(), caller(r), httpx.RequestID(r), id, year)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// GetResult godoc
//
//	@Summary		The BIC result of furnished letting for a year
//	@Description	The scope's owner's furnished letting under the réel (RENT-SPEC-019, Q-132), their shares applied. Receipts are rent, provisions, regularisation and what a deposit statement kept. Charges are the other charges paid (recoverable included), the loan interest and insurance, and the furniture expensed. The depreciation deducted is capped at receipts less those charges (CGI art. 39 C, II); the excess is carried to the next years without time limit. The years from the first one in service are chained to carry it. The micro-BIC figure (bic.micro) and the lower regime are given beside.
//	@Tags			bic
//	@Produce		json
//	@Param			year			query		int		true	"calendar year"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	ResultView
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bic/result [get]
func (h Handler) GetResult(w http.ResponseWriter, r *http.Request) {
	year, _ := strconv.Atoi(r.URL.Query().Get("year"))
	v, err := h.Service.Result(r.Context(), caller(r), httpx.RequestID(r), year)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// ListDeclarations godoc
//
//	@Summary		The BIC declarations issued
//	@Description	The scope's owner's declarations of furnished letting, by year, with their figures as issued (Q-132).
//	@Tags			bic
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		Declaration
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bic/declarations [get]
func (h Handler) ListDeclarations(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Declarations(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// IssueDeclaration godoc
//
//	@Summary		Issue a year's BIC declaration
//	@Description	Issues the year under the regime chosen (step_up): its figures, computed from the declarations before it, are stored as they are, and the grid versions they used are frozen; a frozen grid takes no new version (409 bic/grid-frozen). A year is declared once (409 bic/declared), after the years before it (409 bic/declaration-order); the micro is open up to its receipts threshold (422 bic/micro-closed). Under the micro, the year carries no depreciation of its own and passes the earlier carry on (Q-132).
//	@Tags			bic
//	@Accept			json
//	@Produce		json
//	@Param			body			body		DeclarationInput	true	"year and regime"
//	@Param			X-Owner-Scope	header		string				false	"owner scope"
//	@Success		201				{object}	Declaration
//	@Failure		400,401,403,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/bic/declarations [post]
func (h Handler) IssueDeclaration(w http.ResponseWriter, r *http.Request) {
	var in DeclarationInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Issue(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}
