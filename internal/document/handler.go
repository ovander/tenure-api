package document

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

// Handler serves /documents.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// List godoc
//
//	@Summary		List the scope's documents
//	@Description	An accountant sees financial classes only.
//	@Tags			document
//	@Produce		json
//	@Param			class			query		string	false	"document class"
//	@Param			asset_id		query		string	false	"asset id"
//	@Param			lease_id		query		string	false	"lease id"
//	@Param			period			query		string	false	"period YYYY-MM"
//	@Param			year			query		int		false	"year of issue"
//	@Param			page			query		int		false	"page, from 1"
//	@Param			size			query		int		false	"page size, 1 to 100"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	httpx.List[View]
//	@Failure		400,401,403,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/documents [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := Filter{Class: q.Get("class")}
	var fields invariant.Fields
	for key, dst := range map[string]**uuid.UUID{"asset_id": &f.AssetID, "lease_id": &f.LeaseID} {
		if v := q.Get(key); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				fields.Add(key, "Identifiant invalide.")
				continue
			}
			*dst = &id
		}
	}
	if v := q.Get("period"); v != "" {
		if _, err := period.Parse(v); err != nil {
			fields.Add("period", "Période attendue au format AAAA-MM.")
		}
		f.Period = v
	}
	if v := q.Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil || y < 1970 || y > 9999 {
			fields.Add("year", "Année invalide.")
		}
		f.Year = y
	}
	if err := fields.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, total, err := h.Service.List(r.Context(), caller(r), httpx.RequestID(r), f, pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// Get godoc
//
//	@Summary	Get a document's metadata
//	@Tags		document
//	@Produce	json
//	@Param		id				path		string	true	"document id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	View
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/documents/{id} [get]
func (h Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Get(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// Content godoc
//
//	@Summary		Get a 10-minute download link to a document
//	@Description	The content is never streamed through the API; every link issued is recorded as an audit event.
//	@Tags			document
//	@Produce		json
//	@Param			id				path		string	true	"document id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	Link
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/documents/{id}/content [get]
func (h Handler) Content(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	l, err := h.Service.Content(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, l)
}
