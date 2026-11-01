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

// ListDiagnostics godoc
//
//	@Summary	List an asset's diagnostics
//	@Tags		portfolio
//	@Produce	json
//	@Param		id				path		string	true	"asset id"
//	@Param		X-Owner-Scope	header		string	false	"owner scope"
//	@Success	200				{array}		DiagnosticView
//	@Failure	401,403,404		{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/assets/{id}/diagnostics [get]
func (h Handler) ListDiagnostics(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Diagnostics(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CreateDiagnostic godoc
//
//	@Summary		Record an asset's diagnostic
//	@Description	One record per kind; a kind the asset does not need is declared with not_required_reason (Q-23).
//	@Tags			portfolio
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string			true	"asset id"
//	@Param			body			body		DiagnosticInput	true	"diagnostic"
//	@Param			X-Owner-Scope	header		string			false	"owner scope"
//	@Success		201				{object}	DiagnosticView
//	@Failure		400,401,403,404,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/diagnostics [post]
func (h Handler) CreateDiagnostic(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in DiagnosticInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateDiagnostic(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// UpdateDiagnostic godoc
//
//	@Summary	Update a diagnostic
//	@Tags		portfolio
//	@Accept		json
//	@Produce	json
//	@Param		id				path		string			true	"diagnostic id"
//	@Param		If-Match		header		string			true	"current version"
//	@Param		body			body		DiagnosticInput	true	"diagnostic"
//	@Param		X-Owner-Scope	header		string			false	"owner scope"
//	@Success	200				{object}	DiagnosticView
//	@Failure	400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security	BearerAuth
//	@Router		/diagnostics/{id} [put]
func (h Handler) UpdateDiagnostic(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in DiagnosticInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.UpdateDiagnostic(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// ListGrants godoc
//
//	@Summary		List the scope's grants
//	@Description	Revoked grants included: a grant is revoked, never deleted. A grantee sees only their own.
//	@Tags			portfolio
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		GrantView
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/grants [get]
func (h Handler) ListGrants(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Grants(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// CreateGrant godoc
//
//	@Summary		Give a Socrate subject read access to the portfolio
//	@Description	As co-owner (reads everything) or accountant (financial data, no tenant contact data). A step-up route.
//	@Tags			portfolio
//	@Accept			json
//	@Produce		json
//	@Param			body			body		GrantInput	true	"grant"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	GrantView
//	@Failure		400,401,403,409,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/grants [post]
func (h Handler) CreateGrant(w http.ResponseWriter, r *http.Request) {
	var in GrantInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.CreateGrant(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// RevokeGrant godoc
//
//	@Summary		Revoke a grant
//	@Description	Sets revoked_at; the grantee's next request no longer reaches the scope. A step-up route.
//	@Tags			portfolio
//	@Produce		json
//	@Param			id				path		string	true	"grant id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	GrantView
//	@Failure		401,403,404,409	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/grants/{id} [delete]
func (h Handler) RevokeGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.RevokeGrant(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// ExportMe godoc
//
//	@Summary		Export the caller's personal data (RGPD)
//	@Description	The owner profile and the grants given and received. A step-up route, recorded in the audit trail.
//	@Tags			portfolio
//	@Produce		json
//	@Success		200	{object}	DataExport
//	@Failure		401	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/me/export [get]
func (h Handler) ExportMe(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Export(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="tenure-mes-donnees.json"`)
	httpx.JSON(w, http.StatusOK, v)
}
