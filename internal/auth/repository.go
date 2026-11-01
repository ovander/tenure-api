package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/db"
)

// Repository resolves scopes from owners and owner_grants. It runs in a
// transaction that carries only the caller's subject, so row-level security
// shows it the caller's own owner row and the grants made to the caller.
type Repository struct{ DB *db.DB }

// Resolve implements Resolver.
func (r Repository) Resolve(ctx context.Context, sub string) ([]Scope, string, error) {
	var scopes []Scope
	lang := "fr"
	err := r.DB.InScope(ctx, db.Scope{ActorSub: sub, Role: "resolve"}, func(ctx context.Context) error {
		tx, err := db.Tx(ctx)
		if err != nil {
			return err
		}
		var own []struct {
			ID        uuid.UUID
			LegalName string
			Language  string
		}
		if err := tx.Raw("SELECT id, legal_name, language FROM owners WHERE socrate_sub = ?", sub).Scan(&own).Error; err != nil {
			return err
		}
		for _, o := range own {
			scopes = append(scopes, Scope{OwnerID: o.ID, Role: RoleOwner, LegalName: o.LegalName})
			lang = o.Language
		}
		var granted []struct {
			OwnerID   uuid.UUID
			Role      string
			LegalName string
		}
		if err := tx.Raw(`SELECT g.owner_id, g.role, o.legal_name FROM owner_grants g JOIN owners o ON o.id = g.owner_id
			WHERE g.grantee_sub = ? AND g.revoked_at IS NULL ORDER BY o.legal_name, g.owner_id`, sub).Scan(&granted).Error; err != nil {
			return err
		}
		for _, g := range granted {
			scopes = append(scopes, Scope{OwnerID: g.OwnerID, Role: Role(g.Role), LegalName: g.LegalName})
		}
		return nil
	})
	return scopes, lang, err
}
