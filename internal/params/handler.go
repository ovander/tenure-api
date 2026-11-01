package params

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/period"
)

// Handler serves /params.
type Handler struct{ Service Service }

func principalScope(r *http.Request) (*auth.Principal, bool) {
	p, ok := auth.FromContext(r.Context())
	return p, ok
}

// List godoc
//
//	@Summary	List legal parameters
//	@Tags		params
//	@Produce	json
//	@Param		key		query		string	false	"only this key"
//	@Param		as_of	query		string	false	"only the values in force on this date (YYYY-MM-DD)"
//	@Success	200		{array}		Entry
//	@Failure	400,401	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/params [get]
func (h Handler) List(w http.ResponseWriter, r *http.Request) {
	p, _ := principalScope(r)
	var asOf *period.Date
	if v := r.URL.Query().Get("as_of"); v != "" {
		d, err := period.ParseDate(v)
		if err != nil {
			httpx.Error(w, r, fieldErr("as_of", "Date attendue au format AAAA-MM-JJ."))
			return
		}
		asOf = &d
	}
	out, err := h.Service.List(r.Context(), p.DBScope(httpx.RequestID(r)), r.URL.Query().Get("key"), asOf)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if out == nil {
		out = []Entry{}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// IRLSeries godoc
//
//	@Summary	List the cached INSEE IRL values
//	@Tags		params
//	@Produce	json
//	@Success	200	{array}		IRL
//	@Failure	401	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/params/irl [get]
func (h Handler) IRLSeries(w http.ResponseWriter, r *http.Request) {
	p, _ := principalScope(r)
	out, err := h.Service.IRL(r.Context(), p.DBScope(httpx.RequestID(r)))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if out == nil {
		out = []IRL{}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// Put godoc
//
//	@Summary		Add an effective-dated value to a legal parameter
//	@Description	Admin only, step-up required. Values are appended, never edited; the effective date cannot be in the past.
//	@Tags			params
//	@Accept			json
//	@Produce		json
//	@Param			key		path		string		true	"parameter key"
//	@Param			body	body		PutInput	true	"new value"
//	@Success		201		{object}	Entry
//	@Failure		400,401,403,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/params/{key} [put]
func (h Handler) Put(w http.ResponseWriter, r *http.Request) {
	p, _ := principalScope(r)
	var in PutInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	e, err := h.Service.Put(r.Context(), p, httpx.RequestID(r), chi.URLParam(r, "key"), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, e)
}
