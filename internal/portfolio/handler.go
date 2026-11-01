package portfolio

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
)

// Handler serves /me, /assets and /tenants.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// GetMe godoc
//
//	@Summary	The caller, their roles and owner scopes
//	@Tags		portfolio
//	@Produce	json
//	@Success	200	{object}	MeView
//	@Failure	401	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/me [get]
func (h Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Me(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if v.Owner != nil {
		w.Header().Set("ETag", httpx.ETag(v.Owner.Version))
	}
	httpx.JSON(w, http.StatusOK, v)
}

// PutMe godoc
//
//	@Summary		Create or update the caller's owner profile
//	@Description	Without If-Match, creates the profile linked to the caller's Socrate subject; with If-Match, updates it.
//	@Tags			portfolio
//	@Accept			json
//	@Produce		json
//	@Param			If-Match	header		string		false	"version to update"
//	@Param			body		body		OwnerInput	true	"profile"
//	@Success		200			{object}	OwnerView
//	@Success		201			{object}	OwnerView
//	@Failure		400,401,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/me [put]
func (h Handler) PutMe(w http.ResponseWriter, r *http.Request) {
	var version *int
	if r.Header.Get("If-Match") != "" {
		v, ok := httpx.IfMatch(w, r)
		if !ok {
			return
		}
		version = &v
	}
	var in OwnerInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.PutMe(r.Context(), caller(r), httpx.RequestID(r), version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusOK
	if version == nil {
		status = http.StatusCreated
	}
	httpx.Resource(w, status, v.Version, v)
}

// ListAssets godoc
//
//	@Summary	List the scope's assets
//	@Tags		portfolio
//	@Produce	json
//	@Param		page			query		int		false	"page, from 1"
//	@Param		size			query		int		false	"page size, 1 to 100"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	httpx.List[AssetView]
//	@Failure	400,401,403		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/assets [get]
func (h Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	items, total, err := h.Service.ListAssets(r.Context(), caller(r), httpx.RequestID(r), pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// GetAsset godoc
//
//	@Summary	Get an asset
//	@Tags		portfolio
//	@Produce	json
//	@Param		id				path		string	true	"asset id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	AssetView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/assets/{id} [get]
func (h Handler) GetAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.GetAsset(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// CreateAsset godoc
//
//	@Summary	Add an asset
//	@Tags		portfolio
//	@Accept		json
//	@Produce	json
//	@Param		body			body		AssetInput	true	"asset"
//	@Param		X-Owner-Scope	header		string		false	"owner scope"
//	@Success	201				{object}	AssetView
//	@Failure	400,401,403,415,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/assets [post]
func (h Handler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	var in AssetInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateAsset(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateAsset godoc
//
//	@Summary	Update an asset
//	@Tags		portfolio
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string		true	"asset id"
//	@Param		If-Match		header		string		true	"current version"
//	@Param		body			body		AssetInput	true	"asset"
//	@Param		X-Owner-Scope	header		string		false	"owner scope"
//	@Success	200				{object}	AssetView
//	@Failure	400,401,403,404,409,412,415,422,428	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/assets/{id} [put]
func (h Handler) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in AssetInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateAsset(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ListTenants godoc
//
//	@Summary		List the scope's tenants
//	@Description	An accountant sees names only.
//	@Tags			portfolio
//	@Produce		json
//	@Param			page			query		int		false	"page, from 1"
//	@Param			size			query		int		false	"page size, 1 to 100"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	httpx.List[TenantView]
//	@Failure		400,401,403		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/tenants [get]
func (h Handler) ListTenants(w http.ResponseWriter, r *http.Request) {
	pg, ok := httpx.ParsePage(w, r)
	if !ok {
		return
	}
	items, total, err := h.Service.ListTenants(r.Context(), caller(r), httpx.RequestID(r), pg.Offset(), pg.Size)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.NewList(items, pg, total))
}

// GetTenant godoc
//
//	@Summary	Get a tenant
//	@Tags		portfolio
//	@Produce	json
//	@Param		id				path		string	true	"tenant id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{object}	TenantView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/tenants/{id} [get]
func (h Handler) GetTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.GetTenant(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// CreateTenant godoc
//
//	@Summary	Add a tenant
//	@Tags		portfolio
//	@Accept		json
//	@Produce	json
//	@Param		body			body		TenantInput	true	"tenant"
//	@Param		X-Owner-Scope	header		string		false	"owner scope"
//	@Success	201				{object}	TenantView
//	@Failure	400,401,403,415,422	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/tenants [post]
func (h Handler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	var in TenantInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateTenant(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateTenant godoc
//
//	@Summary	Update a tenant
//	@Tags		portfolio
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string		true	"tenant id"
//	@Param		If-Match		header		string		true	"current version"
//	@Param		body			body		TenantInput	true	"tenant"
//	@Param		X-Owner-Scope	header		string		false	"owner scope"
//	@Success	200				{object}	TenantView
//	@Failure	400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/tenants/{id} [put]
func (h Handler) UpdateTenant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in TenantInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateTenant(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}
