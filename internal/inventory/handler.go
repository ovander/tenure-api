package inventory

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// Handler serves the asset layouts, the inventory templates and the inventory photos.
type Handler struct{ Service Service }

func caller(r *http.Request) *auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// GetLayout godoc
//
//	@Summary		An asset's layout: its rooms and their elements
//	@Description	Version 0 until the layout is seeded or saved. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"asset id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	LayoutView
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/layout [get]
func (h Handler) GetLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Layout(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// PutLayout godoc
//
//	@Summary		Replace an asset's layout
//	@Description	The whole layout in order, with If-Match on its version (W/"0" before the first save). A room or element left out is removed; ids keep past inventories linked.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"asset id"
//	@Param			If-Match		header		string		true	"layout version, W/\"n\""
//	@Param			body			body		LayoutInput	true	"layout"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		200				{object}	LayoutView
//	@Failure		400,401,403,404,412,415,422,428	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/layout [put]
func (h Handler) PutLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	version, ok := httpx.IfMatch(w, r)
	if !ok {
		return
	}
	var in LayoutInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SaveLayout(r.Context(), caller(r), httpx.RequestID(r), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusOK, v.Version, v)
}

// SeedLayout godoc
//
//	@Summary		Seed an asset's first layout from a template
//	@Description	From the latest template of the asset's kind (RENT-INV-001 §4): bedrooms numbered, rooms needing an absent feature left out, the furniture of a furnished dwelling added. Refused when the asset has rooms.
//	@Tags			inventory
//	@Accept			json
//	@Produce		json
//	@Param			id				path		string		true	"asset id"
//	@Param			body			body		SeedInput	true	"what the template needs"
//	@Param			X-Owner-Scope	header		string		false	"owner scope"
//	@Success		201				{object}	LayoutView
//	@Failure		400,401,403,404,409,412,415,422	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/assets/{id}/layout/seed [post]
func (h Handler) SeedLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var in SeedInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	v, err := h.Service.SeedLayout(r.Context(), caller(r), httpx.RequestID(r), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.Resource(w, http.StatusCreated, v.Version, v)
}

// ListTemplates godoc
//
//	@Summary		The inventory templates the scope can read
//	@Description	The system templates and the owner's own, newest version first within each asset kind. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{array}		TemplateView
//	@Failure		401,403			{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventory-templates [get]
func (h Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	v, err := h.Service.Templates(r.Context(), caller(r), httpx.RequestID(r))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// PhotoBodyLimit is the body cap of a photo upload: the file cap plus room
// for the multipart framing and the form fields.
const PhotoBodyLimit = document.MaxUploadBytes + 64<<10

// UploadPhoto godoc
//
//	@Summary		Add a photo to an inventory item or meter
//	@Description	multipart/form-data: item_id or meter_id first, the file last. The upload waits in quarantine while the antivirus scans it; a JPEG or PNG is then turned upright, stripped of every metadata (EXIF, location) and re-encoded to JPEG, and the cleaned file is archived under compliance lock. 25 MB at most. Not on an issued inventory; owner only.
//	@Tags			inventory
//	@Accept			multipart/form-data
//	@Produce		json
//	@Param			id				path		string	true	"inventory id"
//	@Param			item_id			formData	string	false	"inventory item id"
//	@Param			meter_id		formData	string	false	"inventory meter id"
//	@Param			file			formData	file	true	"JPEG or PNG"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		201				{object}	PhotoView
//	@Failure		400,401,403,404,409,413,415,422,503	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/photos [post]
func (h Handler) UploadPhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		problem.Write(w, r, problem.UnsupportedMedia, httpx.Lang(r), "multipart/form-data attendu", nil)
		return
	}
	in := PhotoInput{InventoryID: id}
	var fields invariant.Fields
	for {
		part, err := mr.NextPart()
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				httpx.Error(w, r, invariant.New(document.TypeUploadTooLarge, ""))
				return
			}
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "le champ file est obligatoire et vient en dernier", nil)
			return
		}
		name := part.FormName()
		if name == "file" {
			in.File = part
			break
		}
		raw, err := io.ReadAll(io.LimitReader(part, 1<<10))
		if err != nil {
			problem.Write(w, r, problem.BadRequest, httpx.Lang(r), "champ illisible", nil)
			return
		}
		switch name {
		case "item_id", "meter_id":
			v, err := uuid.Parse(string(raw))
			if err != nil {
				fields.Add(name, "Identifiant invalide.")
				continue
			}
			if name == "item_id" {
				in.ItemID = &v
			} else {
				in.MeterID = &v
			}
		default:
			fields.Add(name, "Champ inconnu.")
		}
	}
	if err := fields.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	v, err := h.Service.UploadPhoto(r.Context(), caller(r), httpx.RequestID(r), in)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = invariant.New(document.TypeUploadTooLarge, "")
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// PhotoContent godoc
//
//	@Summary		Get a 10-minute link to an inventory photo
//	@Description	The photo is never streamed through the API; the link is shown inline and every link issued is recorded as an audit event. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"inventory id"
//	@Param			photoId			path		string	true	"photo id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	document.Link
//	@Failure		401,403,404		{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/photos/{photoId}/content [get]
func (h Handler) PhotoContent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	photo, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "photoId"))
	if !ok {
		return
	}
	l, err := h.Service.PhotoContent(r.Context(), caller(r), httpx.RequestID(r), id, photo)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, l)
}
