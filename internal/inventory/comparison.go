package inventory

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/httpx"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// TypeNotAnExit refuses the comparison of an inventory that is not an exit
// captured from its entry.
const TypeNotAnExit = "inventory/comparison-unavailable"

func init() {
	problem.Register(map[string]problem.Message{
		TypeNotAnExit: problem.M(409, "La comparaison porte sur un état des lieux de sortie saisi à partir de l’entrée.", "The comparison is for an exit inventory captured from its entry."),
	})
}

// ComparisonView is an exit compared with its entry (RENT-INV-001 §6): each
// exit item's result, the keys, and how many results the owner must qualify
// before the settlement. The entry is corrected by its accepted completion
// lines.
type ComparisonView struct {
	Comparison
	InventoryID      uuid.UUID    `json:"inventory_id"`
	EntryInventoryID uuid.UUID    `json:"entry_inventory_id"`
	EntryDoneOn      *period.Date `json:"entry_done_on,omitempty" swaggertype:"string" format:"date"`
	ExitDoneOn       *period.Date `json:"exit_done_on,omitempty" swaggertype:"string" format:"date"`
	State            string       `json:"state" enums:"draft,scheduled,in_progress,completed,signed,issued"`
}

func (c capture) comparison() (ComparisonView, error) {
	if c.head.Kind != "exit" || c.entry == nil {
		return ComparisonView{}, invariant.New(TypeNotAnExit, c.head.Kind)
	}
	entry := c.entry.engine()
	entry.Kind = Entry
	return ComparisonView{Comparison: Compare(entry, c.completions, c.engine()), InventoryID: c.head.ID,
		EntryInventoryID: c.entry.head.ID, EntryDoneOn: dateOf(c.entry.head.DoneOn), ExitDoneOn: dateOf(c.head.DoneOn), State: c.head.State}, nil
}

// Comparison compares an exit with its entry. The co-owner reads it; the
// accountant does not.
func (s Service) Comparison(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (ComparisonView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ComparisonView{}, err
	}
	var out ComparisonView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		c, err := s.load(ctx, sc, id, false)
		if err != nil {
			return err
		}
		out, err = c.comparison()
		return err
	})
	return out, err
}

// GetComparison godoc
//
//	@Summary		An exit compared with its entry
//	@Description	Each exit item against its entry item (same, better, worse, missing, new), the keys, and the number of worse or missing results to qualify before the deposit settlement. The entry is corrected by its accepted completion lines. Not for an accountant.
//	@Tags			inventory
//	@Produce		json
//	@Param			id				path		string	true	"exit inventory id"
//	@Param			X-Owner-Scope	header		string	false	"owner scope"
//	@Success		200				{object}	ComparisonView
//	@Failure		401,403,404,409	{object}	problem.Body
//	@Security		BearerAuth
//	@Router			/inventories/{id}/comparison [get]
func (h Handler) GetComparison(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.UUIDParam(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	v, err := h.Service.Comparison(r.Context(), caller(r), httpx.RequestID(r), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}
