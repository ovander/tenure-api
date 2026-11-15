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
			Kind      string
		}
		if err := tx.Raw("SELECT id, legal_name, language, kind FROM owners WHERE socrate_sub = ?", sub).Scan(&own).Error; err != nil {
			return err
		}
		for _, o := range own {
			scopes = append(scopes, Scope{OwnerID: o.ID, Role: RoleOwner, LegalName: o.LegalName, Kind: o.Kind})
			lang = o.Language
		}
		var granted []struct {
			OwnerID   uuid.UUID
			Role      string
			LegalName string
			Kind      string
		}
		if err := tx.Raw(`SELECT g.owner_id, g.role, o.legal_name, o.kind FROM owner_grants g JOIN owners o ON o.id = g.owner_id
			WHERE g.grantee_sub = ? AND g.revoked_at IS NULL ORDER BY o.legal_name, g.owner_id`, sub).Scan(&granted).Error; err != nil {
			return err
		}
		for _, g := range granted {
			sc := Scope{OwnerID: g.OwnerID, Role: Role(g.Role), LegalName: g.LegalName, Kind: g.Kind}
			// A manager has the owner's rights, in an SCI only (Q-133);
			// tenure_scope_role answers the same in the database.
			if sc.Role == RoleManager {
				if sc.Kind != KindSCI {
					continue
				}
				sc.Role, sc.Manager = RoleOwner, true
			}
			scopes = append(scopes, sc)
		}
		return nil
	})
	return scopes, lang, err
}
