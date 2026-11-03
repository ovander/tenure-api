package charge

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Handler serves /charges and /reconciliations.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

func uuidQuery(r *http.Request, key string, f *invariant.Fields) *uuid.UUID {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		f.Add(key, "Identifiant invalide.")
		return nil
	}
	return &id
}

func yearQuery(r *http.Request, f *invariant.Fields) int {
	v := r.URL.Query().Get("year")
	if v == "" {
		return 0
	}
	y, err := strconv.Atoi(v)
	if err != nil || y < 1970 || y > 9999 {
		f.Add("year", "Année invalide.")
	}
	return y
}

// ListCharges godoc
//
//	@Summary	List the scope's charges
//	@Tags		charge
//	@Produce	json
//	@Param		asset_id		query		string	false	"asset id"
//	@Param		year			query		int		false	"exercise year"
//	@Param		status			query		string	false	"draft or confirmed"
//	@Param		category		query		string	false	"charge category"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[View]
//	@Failure	400,401,403,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/charges [get]
func (h Handler) ListCharges(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	var fe invariant.Fields
	q := r.URL.Query()
	f := Filter{AssetID: uuidQuery(r, "asset_id", &fe), Year: yearQuery(r, &fe), Status: q.Get("status"), Category: q.Get("category")}
	if err := fe.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, total, err := h.Service.ListCharges(r.Context(), caller(r), httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// GetCharge godoc
//
//	@Summary	Get a charge
//	@Tags		charge
//	@Produce	json
//	@Param		id				path		string	true	"charge id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/charges/{id} [get]
func (h Handler) GetCharge(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.GetCharge(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// CreateCharge godoc
//
//	@Summary	Record a charge
//	@Tags		charge
//	@Accept		json
//	@Produce	json
//	@Param		body			body		Input	true	"charge"
//	@Param		X-Owner-Scope	header		string		false	"owner scope"
//	@Success	201				{object}	View
//	@Failure	400,401,403,415,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/charges [post]
func (h Handler) CreateCharge(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateCharge(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateCharge godoc
//
//	@Summary		Update a charge
//	@Description	Confirms an imported line or overrides its recoverable part; a charge of an issued reconciliation never changes.
//	@Tags			charge
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"charge id"
//	@Param			If-Match		header		string		true	"current version"
//	@Param			body			body		Input	true	"charge"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	View
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/charges/{id} [put]
func (h Handler) UpdateCharge(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateCharge(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ImportSyndic godoc
//
//	@Summary		Import a syndic statement into draft charges
//	@Description	The statement exported as CSV is uploaded first (POST /documents, scanned); one draft charge per line keeps the syndic's recoverable split. A statement the mapping cannot read whole imports nothing.
//	@Tags			charge
//	@Accept			json
//	@Produce		json
//	@Param			body			body		ImportInput	true	"import"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{array}		View
//	@Failure		400,401,403,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/charges/import/syndic [post]
func (h Handler) ImportSyndic(w http.ResponseWriter, r *http.Request) {
	var in ImportInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.ImportSyndic(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// ListRecons godoc
//
//	@Summary	List the scope's reconciliations
//	@Tags		charge
//	@Produce	json
//	@Param		asset_id		query		string	false	"asset id"
//	@Param		lease_id		query		string	false	"reconciliations with a line for this lease"
//	@Param		year			query		int		false	"exercise year"
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[ReconView]
//	@Failure	400,401,403,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/reconciliations [get]
func (h Handler) ListRecons(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	var fe invariant.Fields
	f := ReconFilter{AssetID: uuidQuery(r, "asset_id", &fe), LeaseID: uuidQuery(r, "lease_id", &fe), Year: yearQuery(r, &fe)}
	if err := fe.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, total, err := h.Service.ListRecons(r.Context(), caller(r), httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// GetRecon godoc
//
//	@Summary	Get a reconciliation and its lines
//	@Tags		charge
//	@Produce	json
//	@Param		id				path		string	true	"reconciliation id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	ReconView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/reconciliations/{id} [get]
func (h Handler) GetRecon(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.GetRecon(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// CreateRecon godoc
//
//	@Summary		Start the reconciliation of an asset's exercise
//	@Description	In draft; the exercise runs from the 1st of a month to the last day of a month.
//	@Tags			charge
//	@Accept			json
//	@Produce		json
//	@Param			body			body		ReconInput	true	"reconciliation"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	ReconView
//	@Failure		400,401,403,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/reconciliations [post]
func (h Handler) CreateRecon(w http.ResponseWriter, r *http.Request) {
	var in ReconInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateRecon(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// Transition godoc
//
//	@Summary		Move a reconciliation along its steps
//	@Description	allocate (again), issue (statements RG-…, postings on the next rent calls, validated provisions), settle, cancel (a correction).
//	@Tags			charge
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"reconciliation id"
//	@Param			body			body		TransitionInput	true	"action"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		200				{object}	ReconView
//	@Failure		400,401,403,404,409,412,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/reconciliations/{id}/transitions [post]
func (h Handler) Transition(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in TransitionInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.Transition(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// UpdateLine godoc
//
//	@Summary		Validate a line's provision and settlement
//	@Description	On an allocated reconciliation: the new provision to apply from the next month and how the balance is settled.
//	@Tags			charge
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"reconciliation id"
//	@Param			lineId			path		string		true	"line id"
//	@Param			If-Match		header		string		true	"current version of the line"
//	@Param			body			body		LineInput	true	"line"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	LineView
//	@Failure		400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/reconciliations/{id}/lines/{lineId} [put]
func (h Handler) UpdateLine(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	lineID, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "lineId"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in LineInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateLine(r.Context(), caller(r), httpx.RequestID(r), id, lineID, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
