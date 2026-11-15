package portfolio

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of entities (an SCI as an owner scope, Q-133).
const (
	TypeEntityIS     = "entity/is-unsupported"
	TypeEntityNotSCI = "entity/not-sci"
	TypeGrantManager = "grant/manager-sci-only"
	TypeLastManager  = "grant/last-manager"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeEntityIS: problem.M(422, "Une SCI soumise à l’impôt sur les sociétés n’est pas prise en charge : seule la SCI à l’impôt sur le revenu l’est.",
			"An SCI under corporate tax is not supported: only an SCI at the income tax is."),
		TypeEntityNotSCI: problem.M(409, "Cette action ne concerne qu’une SCI : choisissez le périmètre de la SCI.",
			"This action concerns an SCI only: choose the SCI's scope."),
		TypeGrantManager: problem.M(422, "Le rôle de gérant n’existe que dans une SCI.", "The manager role exists in an SCI only."),
		TypeLastManager:  problem.M(409, "Une SCI garde au moins un gérant : nommez-en un autre avant de révoquer celui-ci.", "An SCI keeps at least one manager: appoint another before revoking this one."),
	})
}

// TaxRegimeIR is the only tax regime of an SCI Tenure supports: the income
// tax (CGI art. 8, Q-133).
const TaxRegimeIR = "ir"

// EntityInput is the body of POST /entities and PUT /entity: an SCI at the
// income tax. Its capital in parts is set at creation and does not change
// (an increase or a reduction of capital is not built, Q-133).
type EntityInput struct {
	LegalName    string       `json:"legal_name"`
	Address      string       `json:"address"`
	Siren        string       `json:"siren"`
	TaxRegime    string       `json:"tax_regime" enums:"ir"`
	RegimeFrom   *period.Date `json:"regime_from" swaggertype:"string" format:"date"`
	CapitalParts int          `json:"capital_parts"`
	Language     string       `json:"language" enums:"fr,en"`
}

// EntityView is an SCI's profile.
type EntityView struct {
	ID uuid.UUID `json:"id"`
	EntityInput
	Kind      string    `json:"kind" enums:"sci"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func entityView(o ownerRow) EntityView {
	v := EntityView{ID: o.ID, Kind: o.Kind, Version: o.Version, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
		EntityInput: EntityInput{LegalName: o.LegalName, Address: o.Address, Siren: o.Siren, Language: o.Language}}
	if o.TaxRegime != nil {
		v.TaxRegime = *o.TaxRegime
	}
	if o.RegimeFrom != nil {
		d := period.FromTime(*o.RegimeFrom)
		v.RegimeFrom = &d
	}
	if o.CapitalParts != nil {
		v.CapitalParts = *o.CapitalParts
	}
	return v
}

func validEntity(in *EntityInput) error {
	in.LegalName, in.Address, in.Siren = strings.TrimSpace(in.LegalName), strings.TrimSpace(in.Address), strings.ReplaceAll(strings.TrimSpace(in.Siren), " ", "")
	if in.Language == "" {
		in.Language = "fr"
	}
	if in.TaxRegime == "is" {
		return invariant.New(TypeEntityIS, "")
	}
	var f invariant.Fields
	if in.LegalName == "" {
		f.Add("legal_name", "La dénomination de la SCI est obligatoire.")
	}
	if in.Siren != "" && !nineDigits(in.Siren) {
		f.Add("siren", "Le SIREN compte neuf chiffres.")
	}
	if in.TaxRegime != TaxRegimeIR {
		f.Add("tax_regime", "Régime attendu : ir (impôt sur le revenu).")
	}
	if in.CapitalParts <= 0 || in.CapitalParts > math.MaxInt32 {
		f.Add("capital_parts", "Le nombre de parts du capital est un entier positif.")
	}
	if !languages[in.Language] {
		f.Add("language", "Langue attendue : fr ou en.")
	}
	return f.Err()
}

func nineDigits(s string) bool {
	if len(s) != 9 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// CreateEntity creates an SCI at the income tax (POST /entities, a step_up
// route, Q-133). The SCI is an owner scope of its own with no Socrate
// subject; its creator becomes its first manager, by a grant recorded in the
// same transaction, in the SCI's own audit chain.
func (s Service) CreateEntity(ctx context.Context, p *auth.Principal, requestID string, in EntityInput) (EntityView, error) {
	if err := validEntity(&in); err != nil {
		return EntityView{}, err
	}
	now := s.now()
	regime, parts := TaxRegimeIR, in.CapitalParts
	row := ownerRow{ID: s.NewID(), Kind: auth.KindSCI, LegalName: in.LegalName, Address: in.Address, Siren: in.Siren, Language: in.Language,
		TaxRegime: &regime, CapitalParts: &parts, Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
	if in.RegimeFrom != nil {
		t := in.RegimeFrom.Time()
		row.RegimeFrom = &t
	}
	out := entityView(row)
	err := s.DB.InScope(ctx, db.Scope{OwnerID: row.ID, ActorSub: p.Sub, Role: string(auth.RoleOwner), RequestID: requestID}, func(ctx context.Context) error {
		if err := s.Repo.InsertOwner(ctx, row); err != nil {
			return err
		}
		g := grantRow{ID: s.NewID(), OwnerID: row.ID, GranteeSub: p.Sub, Role: string(auth.RoleManager), GrantedAt: now,
			CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if err := s.Repo.insertGrant(ctx, g); err != nil {
			return err
		}
		if err := audit.Record(ctx, "entity.create", audit.Entity{Type: "owner", ID: row.ID.String()}, nil, out); err != nil {
			return err
		}
		return audit.Record(ctx, "grant.create", audit.Entity{Type: "grant", ID: g.ID.String()}, nil, grantView(g))
	})
	return out, err
}

// Entity returns the active SCI scope's profile (GET /entity); 409
// entity/not-sci in a person's scope.
func (s Service) Entity(ctx context.Context, p *auth.Principal, requestID string) (EntityView, error) {
	sc, err := entityScope(p, requestID)
	if err != nil {
		return EntityView{}, err
	}
	var out EntityView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		o, err := s.Repo.Owner(ctx, sc.OwnerID)
		out = entityView(o)
		return err
	})
	return out, err
}

// UpdateEntity changes the active SCI's profile (PUT /entity, If-Match). Its
// tax regime and its capital in parts do not change.
func (s Service) UpdateEntity(ctx context.Context, p *auth.Principal, requestID string, version int, in EntityInput) (EntityView, error) {
	sc, err := entityScope(p, requestID)
	if err != nil {
		return EntityView{}, err
	}
	var out EntityView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, err := s.Repo.Owner(ctx, sc.OwnerID)
		if err != nil {
			return err
		}
		if cur.CapitalParts != nil && in.CapitalParts == 0 {
			in.CapitalParts = *cur.CapitalParts
		}
		if err := validEntity(&in); err != nil {
			return err
		}
		if cur.CapitalParts == nil || in.CapitalParts != *cur.CapitalParts {
			var f invariant.Fields
			f.Add("capital_parts", "Le capital de la SCI ne change pas ici : une augmentation ou une réduction de capital n’est pas prise en charge.")
			return f.Err()
		}
		if cur.Version != version {
			return invariant.ErrStale
		}
		before := entityView(cur)
		cur.LegalName, cur.Address, cur.Siren, cur.Language = in.LegalName, in.Address, in.Siren, in.Language
		cur.RegimeFrom = nil
		if in.RegimeFrom != nil {
			t := in.RegimeFrom.Time()
			cur.RegimeFrom = &t
		}
		cur.UpdatedAt, cur.UpdatedBy, cur.Version = s.now(), p.Sub, version+1
		if err := s.Repo.UpdateEntity(ctx, cur, version); err != nil {
			return err
		}
		out = entityView(cur)
		return audit.Record(ctx, "entity.update", audit.Entity{Type: "owner", ID: cur.ID.String()}, before, out)
	})
	return out, err
}

func entityScope(p *auth.Principal, requestID string) (db.Scope, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return sc, err
	}
	if !p.Active.IsSCI() {
		return sc, invariant.New(TypeEntityNotSCI, "")
	}
	return sc, nil
}
