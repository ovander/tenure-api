package portfolio

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of grants.
const (
	TypeGrantExists  = "grant/already-granted"
	TypeGrantRevoked = "grant/already-revoked"
	TypeGrantSelf    = "grant/self"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeGrantExists:  problem.M(409, "Cette personne a déjà un accès en cours à ce portefeuille.", "This person already has live access to this portfolio."),
		TypeGrantRevoked: problem.M(409, "Cet accès est déjà révoqué.", "This access is already revoked."),
		TypeGrantSelf:    problem.M(422, "Un propriétaire ne s’accorde pas d’accès à lui-même.", "An owner does not grant access to themselves."),
	})
}

type grantRow struct {
	ID         uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID    uuid.UUID  `gorm:"column:owner_id"`
	GranteeSub string     `gorm:"column:grantee_sub"`
	Role       string     `gorm:"column:role"`
	GrantedAt  time.Time  `gorm:"column:granted_at"`
	RevokedAt  *time.Time `gorm:"column:revoked_at"`
	CreatedBy  string     `gorm:"column:created_by"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
	UpdatedBy  string     `gorm:"column:updated_by"`
}

func (grantRow) TableName() string { return "owner_grants" }

// GrantInput is the body of POST /grants: the Socrate subject of the person
// given access (their /me shows it, Q-92) and their role.
type GrantInput struct {
	GranteeSub string `json:"grantee_sub"`
	Role       string `json:"role" enums:"co_owner,accountant"`
}

// GrantView is a grant as the API returns it.
type GrantView struct {
	ID         uuid.UUID  `json:"id"`
	OwnerID    uuid.UUID  `json:"owner_id"`
	GranteeSub string     `json:"grantee_sub"`
	Role       string     `json:"role" enums:"co_owner,accountant"`
	GrantedAt  time.Time  `json:"granted_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func grantView(g grantRow) GrantView {
	return GrantView{ID: g.ID, OwnerID: g.OwnerID, GranteeSub: g.GranteeSub, Role: g.Role, GrantedAt: g.GrantedAt, RevokedAt: g.RevokedAt}
}

// Grants returns the grants of the scope (all of them for its owner; a
// grantee sees only their own), revoked ones included: a grant is revoked,
// never deleted (Q-05).
func (s Service) Grants(ctx context.Context, p *auth.Principal, requestID string) ([]GrantView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []GrantView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.grantsOf(ctx, sc.OwnerID)
		for _, g := range rows {
			out = append(out, grantView(g))
		}
		return err
	})
	return out, err
}

// CreateGrant gives a Socrate subject read access to the scope as co-owner or
// accountant (RENT-BE-001 §4; a step_up route), recorded in the owner's
// audit chain.
func (s Service) CreateGrant(ctx context.Context, p *auth.Principal, requestID string, in GrantInput) (GrantView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GrantView{}, err
	}
	in.GranteeSub = strings.TrimSpace(in.GranteeSub)
	var f invariant.Fields
	if in.GranteeSub == "" || len(in.GranteeSub) > 255 {
		f.Add("grantee_sub", "L’identifiant Socrate de la personne est obligatoire.")
	}
	if in.Role != string(auth.RoleCoOwner) && in.Role != string(auth.RoleAccountant) {
		f.Add("role", "Rôle attendu : co_owner ou accountant.")
	}
	if err := f.Err(); err != nil {
		return GrantView{}, err
	}
	if in.GranteeSub == p.Sub {
		return GrantView{}, invariant.New(TypeGrantSelf, "")
	}
	var out GrantView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		now := s.now()
		g := grantRow{ID: s.NewID(), OwnerID: sc.OwnerID, GranteeSub: in.GranteeSub, Role: in.Role, GrantedAt: now,
			CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if err := s.Repo.insertGrant(ctx, g); err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.New(TypeGrantExists, in.GranteeSub)
			}
			return err
		}
		out = grantView(g)
		return audit.Record(ctx, "grant.create", audit.Entity{Type: "grant", ID: g.ID.String()}, nil, out)
	})
	return out, err
}

// RevokeGrant ends a grant (DELETE /grants/{id}, a step_up route): revoked_at
// is set and the row kept; the grantee's next request no longer resolves the
// scope (403).
func (s Service) RevokeGrant(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (GrantView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GrantView{}, err
	}
	var out GrantView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		g, err := s.Repo.grant(ctx, id, sc.OwnerID)
		if err != nil {
			return err
		}
		if g.RevokedAt != nil {
			return invariant.New(TypeGrantRevoked, id.String())
		}
		before := grantView(g)
		now := s.now()
		g.RevokedAt, g.UpdatedAt, g.UpdatedBy = &now, now, p.Sub
		if err := s.Repo.revokeGrant(ctx, g); err != nil {
			return err
		}
		out = grantView(g)
		return audit.Record(ctx, "grant.revoke", audit.Entity{Type: "grant", ID: id.String()}, before, out)
	})
	return out, err
}

func (Repository) grantsOf(ctx context.Context, owner uuid.UUID) ([]grantRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []grantRow
	err = t.Where("owner_id = ?", owner).Order("granted_at, id").Find(&rows).Error
	return rows, err
}

func (Repository) grantsReceived(ctx context.Context, sub string) ([]grantRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []grantRow
	err = t.Where("grantee_sub = ?", sub).Order("granted_at, id").Find(&rows).Error
	return rows, err
}

func (Repository) grant(ctx context.Context, id, owner uuid.UUID) (grantRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return grantRow{}, err
	}
	var rows []grantRow
	if err := t.Where("id = ? AND owner_id = ?", id, owner).Limit(1).Find(&rows).Error; err != nil {
		return grantRow{}, err
	}
	if len(rows) == 0 {
		return grantRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

func (Repository) insertGrant(ctx context.Context, g grantRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&g).Error
}

func (Repository) revokeGrant(ctx context.Context, g grantRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Model(&grantRow{}).Where("id = ? AND revoked_at IS NULL", g.ID).Updates(map[string]any{
		"revoked_at": g.RevokedAt, "updated_at": g.UpdatedAt, "updated_by": g.UpdatedBy}).Error
}

// DataExport is GET /me/export: what Tenure holds about the caller as a
// person (RGPD art. 15 and 20, RENT-SPEC-022): their owner profile, the
// grants they gave and the grants they received (Q-93). Tenant data is
// exported on a tenant's request through the owner, not here.
type DataExport struct {
	ExportedAt     time.Time   `json:"exported_at"`
	Sub            string      `json:"sub"`
	Owner          *OwnerView  `json:"owner"`
	GrantsGiven    []GrantView `json:"grants_given"`
	GrantsReceived []GrantView `json:"grants_received"`
}

// Export returns the caller's data-subject export, recorded in their audit
// chain as me.export when they have an owner profile.
func (s Service) Export(ctx context.Context, p *auth.Principal, requestID string) (DataExport, error) {
	out := DataExport{ExportedAt: s.now(), Sub: p.Sub, GrantsGiven: []GrantView{}, GrantsReceived: []GrantView{}}
	err := s.DB.InScope(ctx, db.Scope{ActorSub: p.Sub, Role: "self", RequestID: requestID}, func(ctx context.Context) error {
		o, ok, err := s.Repo.OwnerBySub(ctx, p.Sub)
		if err != nil {
			return err
		}
		if ok {
			v := ownerView(o)
			out.Owner = &v
		}
		rows, err := s.Repo.grantsReceived(ctx, p.Sub)
		for _, g := range rows {
			out.GrantsReceived = append(out.GrantsReceived, grantView(g))
		}
		return err
	})
	if err != nil || out.Owner == nil {
		return out, err
	}
	err = s.DB.InScope(ctx, db.Scope{OwnerID: out.Owner.ID, ActorSub: p.Sub, Role: string(auth.RoleOwner), RequestID: requestID}, func(ctx context.Context) error {
		rows, err := s.Repo.grantsOf(ctx, out.Owner.ID)
		if err != nil {
			return err
		}
		for _, g := range rows {
			out.GrantsGiven = append(out.GrantsGiven, grantView(g))
		}
		return audit.Record(ctx, "me.export", audit.Entity{Type: "owner", ID: out.Owner.ID.String()}, nil,
			map[string]any{"grants_given": len(out.GrantsGiven), "grants_received": len(out.GrantsReceived)})
	})
	return out, err
}
