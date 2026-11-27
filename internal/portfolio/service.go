package portfolio

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the portfolio module.
const (
	TypeKindImmutable     = "asset/kind-immutable"
	TypeParentInvalid     = "asset/parent-invalid"
	TypeSharesInvalid     = "asset/shares-invalid"
	TypeShareOwnerUnknown = "asset/share-owner-unknown"
	TypeOwnerExists       = "owner/already-exists"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeKindImmutable:     problem.M(409, "Le type d’un bien ne peut pas être modifié.", "An asset's kind cannot be changed."),
		TypeParentInvalid:     problem.M(422, "Un box ne peut être rattaché qu’à un logement de votre portefeuille.", "A box can only be attached to a dwelling of your portfolio."),
		TypeSharesInvalid:     problem.M(422, "Les quotes-parts doivent totaliser 100 %.", "Ownership shares must add up to 100 %."),
		TypeShareOwnerUnknown: problem.M(422, "Un copropriétaire indiqué n’existe pas.", "A co-owner given does not exist."),
		TypeOwnerExists:       problem.M(409, "Ce compte a déjà un profil de propriétaire.", "This account already has an owner profile."),
	})
}

// Languages a profile may choose.
var languages = map[string]bool{"fr": true, "en": true}

// ShareView is one ownership share, in basis points.
type ShareView struct {
	OwnerID uuid.UUID `json:"owner_id"`
	BP      int64     `json:"bp"`
}

// AssetInput is the body of POST and PUT /assets.
type AssetInput struct {
	Kind                AssetKind `json:"kind" enums:"house,apartment,box"`
	Address             string    `json:"address"`
	Lot                 string    `json:"lot"`
	SurfaceHundredthsM2 *int64    `json:"surface_hundredths_m2"`
	Furnished           bool      `json:"furnished"`
	DPEClass            *string   `json:"dpe_class"`
	ControlledZone      bool      `json:"controlled_zone"`
	// TenseZone is the zone tendue (Q-102); it is stored and returned as
	// true whenever ControlledZone is set.
	TenseZone           bool   `json:"tense_zone"`
	RentControlZone     string `json:"rent_control_zone"`
	RentControlCategory string `json:"rent_control_category"`
	// CollectiveBuilding is an asset in a building under a syndic: its
	// annual accounts gate the deposit holdback (RENT-INV-001 §6).
	CollectiveBuilding bool         `json:"collective_building"`
	AcquiredOn         *period.Date `json:"acquired_on" swaggertype:"string" format:"date"`
	AcquisitionCents   *int64       `json:"acquisition_cents"`
	ParentAssetID      *uuid.UUID   `json:"parent_asset_id"`
	// Shares defaults to 100 % for the owner of the scope.
	Shares []ShareView `json:"shares"`
}

// AssetView is an asset as the API returns it.
type AssetView struct {
	ID uuid.UUID `json:"id"`
	AssetInput
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Domain returns the calculation-engine view of the asset.
func (a AssetView) Domain() Asset {
	d := Asset{ID: a.ID.String(), Kind: a.Kind, Furnished: a.Furnished, ControlledZone: a.ControlledZone,
		TenseZone: a.TenseZone || a.ControlledZone, RentControlZone: a.RentControlZone, RentControlCategory: a.RentControlCategory}
	if a.SurfaceHundredthsM2 != nil {
		d.SurfaceHundredthsM2 = *a.SurfaceHundredthsM2
	}
	if a.ParentAssetID != nil {
		d.ParentAssetID = a.ParentAssetID.String()
	}
	for _, s := range a.Shares {
		d.Shares = append(d.Shares, Share{OwnerID: s.OwnerID.String(), BP: s.BP})
	}
	return d
}

// TenantInput is the body of POST and PUT /tenants.
type TenantInput struct {
	Names    []string `json:"names"`
	Email    string   `json:"email"`
	Phone    string   `json:"phone"`
	Language string   `json:"language" enums:"fr,en"`
	// Occupation records a student or apprentice, who may have a surety
	// alongside a rent insurance (loi 89-462 art. 22-1).
	Occupation *Occupation `json:"occupation" enums:"student,apprentice,other"`
}

// Occupation is a tenant's occupation, as far as the guarantee rules need it.
type Occupation string

// Occupations.
const (
	OccupationStudent    Occupation = "student"
	OccupationApprentice Occupation = "apprentice"
	OccupationOther      Occupation = "other"
)

// TenantView is a tenant as the API returns it. An accountant sees names
// only (RENT-BE-001 §4).
type TenantView struct {
	ID uuid.UUID `json:"id"`
	TenantInput
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OwnerInput is the body of PUT /me.
type OwnerInput struct {
	LegalName string `json:"legal_name"`
	Address   string `json:"address"`
	TaxID     string `json:"tax_id"`
	Language  string `json:"language" enums:"fr,en"`
}

// OwnerView is the caller's owner profile.
type OwnerView struct {
	ID uuid.UUID `json:"id"`
	OwnerInput
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MeView is GET /me: the caller, their roles and scopes.
type MeView struct {
	Sub         string       `json:"sub"`
	Admin       bool         `json:"admin"`
	Language    string       `json:"language"`
	Scopes      []auth.Scope `json:"scopes"`
	ActiveScope *auth.Scope  `json:"active_scope"`
	Owner       *OwnerView   `json:"owner"`
}

// Service implements the portfolio use cases.
type Service struct {
	DB   *db.DB
	Repo Repository
	// Params reads the diagnostics rule (a diagnostic's expiry).
	Params params.Repository
	Now    func() time.Time
	// NewID returns a new UUIDv7.
	NewID func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

// NewService returns a service with the real clock and UUIDv7 ids.
func NewService(d *db.DB) Service {
	return Service{DB: d, Now: time.Now, NewID: func() uuid.UUID { return uuid.Must(uuid.NewV7()) }}
}

func ownerView(o ownerRow) OwnerView {
	return OwnerView{ID: o.ID, OwnerInput: OwnerInput{LegalName: o.LegalName, Address: o.Address, TaxID: o.TaxID, Language: o.Language},
		Version: o.Version, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
}

// Me returns the caller's profile and scopes.
func (s Service) Me(ctx context.Context, p *auth.Principal, requestID string) (MeView, error) {
	v := MeView{Sub: p.Sub, Admin: p.Admin, Language: p.Language, Scopes: p.Scopes, ActiveScope: p.Active}
	if v.Scopes == nil {
		v.Scopes = []auth.Scope{}
	}
	err := s.DB.InScope(ctx, db.Scope{ActorSub: p.Sub, Role: "self", RequestID: requestID}, func(ctx context.Context) error {
		o, ok, err := s.Repo.OwnerBySub(ctx, p.Sub)
		if ok {
			ov := ownerView(o)
			v.Owner = &ov
		}
		return err
	})
	return v, err
}

// PutMe creates the caller's owner profile, or updates it when version is
// set (Q-06, Q-35). The profile is a business record linked to the Socrate
// identity by socrate_sub; it is not an account and holds no credential. The
// write and its audit event run in the owner's own scope, which starts the
// owner's audit chain.
func (s Service) PutMe(ctx context.Context, p *auth.Principal, requestID string, version *int, in OwnerInput) (OwnerView, error) {
	in.LegalName, in.Address, in.TaxID = strings.TrimSpace(in.LegalName), strings.TrimSpace(in.Address), strings.TrimSpace(in.TaxID)
	if in.Language == "" {
		in.Language = "fr"
	}
	var f invariant.Fields
	if in.LegalName == "" {
		f.Add("legal_name", "Le nom est obligatoire.")
	}
	if !languages[in.Language] {
		f.Add("language", "Langue attendue : fr ou en.")
	}
	if err := f.Err(); err != nil {
		return OwnerView{}, err
	}
	var cur ownerRow
	var exists bool
	err := s.DB.InScope(ctx, db.Scope{ActorSub: p.Sub, Role: "self", RequestID: requestID}, func(ctx context.Context) error {
		var err error
		cur, exists, err = s.Repo.OwnerBySub(ctx, p.Sub)
		return err
	})
	if err != nil {
		return OwnerView{}, err
	}
	switch {
	case !exists && version != nil:
		return OwnerView{}, invariant.ErrNotFound
	case exists && version == nil:
		return OwnerView{}, invariant.New(TypeOwnerExists, "use If-Match to update the profile")
	}
	now := s.now()
	if !exists {
		row := ownerRow{ID: s.NewID(), SocrateSub: &p.Sub, Kind: auth.KindPerson, LegalName: in.LegalName, Address: in.Address, TaxID: in.TaxID,
			Language: in.Language, Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		out := ownerView(row)
		err := s.DB.InScope(ctx, db.Scope{OwnerID: row.ID, ActorSub: p.Sub, Role: string(auth.RoleOwner), RequestID: requestID}, func(ctx context.Context) error {
			if err := s.Repo.InsertOwner(ctx, row); err != nil {
				if db.IsUniqueViolation(err) {
					return invariant.New(TypeOwnerExists, "created concurrently")
				}
				return err
			}
			return audit.Record(ctx, "owner.create", audit.Entity{Type: "owner", ID: row.ID.String()}, nil, out)
		})
		return out, err
	}
	before := ownerView(cur)
	cur.LegalName, cur.Address, cur.TaxID, cur.Language = in.LegalName, in.Address, in.TaxID, in.Language
	cur.UpdatedAt, cur.UpdatedBy, cur.Version = now, p.Sub, *version+1
	out := ownerView(cur)
	err = s.DB.InScope(ctx, db.Scope{OwnerID: cur.ID, ActorSub: p.Sub, Role: string(auth.RoleOwner), RequestID: requestID}, func(ctx context.Context) error {
		if err := s.Repo.UpdateOwner(ctx, cur, *version); err != nil {
			return err
		}
		return audit.Record(ctx, "owner.update", audit.Entity{Type: "owner", ID: cur.ID.String()}, before, out)
	})
	return out, err
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// --- Assets -------------------------------------------------------------------

func assetView(a assetRow, shares []shareRow) AssetView {
	v := AssetView{ID: a.ID, Version: a.Version, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, AssetInput: AssetInput{
		Kind: AssetKind(a.Kind), Address: a.Address, Lot: a.Lot, Furnished: a.Furnished, DPEClass: a.DPEClass,
		ControlledZone: a.ControlledZone, TenseZone: a.TenseZone || a.ControlledZone, RentControlZone: a.RentControlZone, RentControlCategory: a.RentControlCategory,
		CollectiveBuilding: a.CollectiveBuilding, AcquisitionCents: a.AcquisitionCents, ParentAssetID: a.ParentAssetID, Shares: []ShareView{}}}
	if a.Surface != nil {
		n := int64(*a.Surface)
		v.SurfaceHundredthsM2 = &n
	}
	if a.AcquiredOn != nil {
		d := period.FromTime(*a.AcquiredOn)
		v.AcquiredOn = &d
	}
	for _, sh := range shares {
		v.Shares = append(v.Shares, ShareView{OwnerID: sh.OwnerID, BP: int64(sh.SharePct)})
	}
	return v
}

// ListAssets returns a page of the scope's assets.
func (s Service) ListAssets(ctx context.Context, p *auth.Principal, requestID string, offset, limit int) ([]AssetView, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []AssetView
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.ListAssets(ctx, offset, limit)
		if err != nil {
			return err
		}
		total = n
		out, err = s.withShares(ctx, rows)
		return err
	})
	return out, total, err
}

func (s Service) withShares(ctx context.Context, rows []assetRow) ([]AssetView, error) {
	ids := make([]uuid.UUID, len(rows))
	for i, a := range rows {
		ids[i] = a.ID
	}
	shares, err := s.Repo.Shares(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]AssetView, len(rows))
	for i, a := range rows {
		out[i] = assetView(a, shares[a.ID])
	}
	return out, nil
}

// GetAsset returns one asset of the scope.
func (s Service) GetAsset(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (AssetView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return AssetView{}, err
	}
	var out AssetView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		out, err = s.AssetInTx(ctx, id)
		return err
	})
	return out, err
}

// AssetsInTx returns the assets with ids inside the current transaction (used
// by the lease service); unknown ids are left out.
func (s Service) AssetsInTx(ctx context.Context, ids []uuid.UUID) ([]AssetView, error) {
	rows, err := s.Repo.Assets(ctx, ids)
	if err != nil {
		return nil, err
	}
	return s.withShares(ctx, rows)
}

// AllAssetsInTx returns every asset of the scope inside the current
// transaction (the dashboard lists them, RENT-SPEC-016).
func (s Service) AllAssetsInTx(ctx context.Context) ([]AssetView, error) {
	rows, _, err := s.Repo.ListAssets(ctx, 0, 100000)
	if err != nil {
		return nil, err
	}
	return s.withShares(ctx, rows)
}

// AssetInTx returns one asset inside the current transaction.
func (s Service) AssetInTx(ctx context.Context, id uuid.UUID) (AssetView, error) {
	out, err := s.AssetsInTx(ctx, []uuid.UUID{id})
	if err != nil {
		return AssetView{}, err
	}
	if len(out) == 0 {
		return AssetView{}, invariant.ErrNotFound
	}
	return out[0], nil
}

func validateAsset(in *AssetInput, owner uuid.UUID) invariant.Fields {
	var f invariant.Fields
	in.Address = strings.TrimSpace(in.Address)
	switch in.Kind {
	case KindHouse, KindApartment, KindBox:
	default:
		f.Add("kind", "Type attendu : house, apartment ou box.")
	}
	if in.Address == "" {
		f.Add("address", "L’adresse est obligatoire.")
	}
	if in.SurfaceHundredthsM2 != nil && *in.SurfaceHundredthsM2 <= 0 {
		f.Add("surface_hundredths_m2", "La surface doit être positive.")
	}
	if in.DPEClass != nil && !strings.Contains("ABCDEFG", *in.DPEClass) || in.DPEClass != nil && len(*in.DPEClass) != 1 {
		f.Add("dpe_class", "Classe DPE attendue : A à G.")
	}
	if in.AcquisitionCents != nil && *in.AcquisitionCents < 0 {
		f.Add("acquisition_cents", "Le prix d’acquisition ne peut pas être négatif.")
	}
	if in.ParentAssetID != nil && in.Kind != KindBox {
		f.Add("parent_asset_id", "Seul un box peut être rattaché à un logement.")
	}
	dwelling := in.Kind == KindHouse || in.Kind == KindApartment
	if in.ControlledZone && dwelling && (in.RentControlZone == "" || in.RentControlCategory == "") {
		f.Add("rent_control_zone", "Zone et catégorie d’encadrement obligatoires en zone encadrée.")
	}
	if len(in.Shares) == 0 {
		in.Shares = []ShareView{{OwnerID: owner, BP: 10000}}
	}
	var sum int64
	seen := map[uuid.UUID]bool{}
	for _, sh := range in.Shares {
		if sh.BP <= 0 || seen[sh.OwnerID] {
			f.Add("shares", "Chaque copropriétaire apparaît une fois avec une quote-part positive.")
			break
		}
		seen[sh.OwnerID] = true
		sum += sh.BP
	}
	if sum != 10000 && len(f) == 0 {
		f.Add("shares", "Les quotes-parts doivent totaliser 100 %.")
	}
	return f
}

func assetRowFrom(in AssetInput) assetRow {
	a := assetRow{Kind: string(in.Kind), Address: in.Address, Lot: in.Lot, Furnished: in.Furnished, DPEClass: in.DPEClass,
		ControlledZone: in.ControlledZone, TenseZone: in.TenseZone || in.ControlledZone, RentControlZone: in.RentControlZone, RentControlCategory: in.RentControlCategory,
		CollectiveBuilding: in.CollectiveBuilding, AcquisitionCents: in.AcquisitionCents, ParentAssetID: in.ParentAssetID}
	if in.SurfaceHundredthsM2 != nil {
		h := db.Hundredths(*in.SurfaceHundredthsM2)
		a.Surface = &h
	}
	if in.AcquiredOn != nil {
		t := in.AcquiredOn.Time()
		a.AcquiredOn = &t
	}
	return a
}

func shareRows(id uuid.UUID, in []ShareView) []shareRow {
	out := make([]shareRow, len(in))
	for i, sh := range in {
		out[i] = shareRow{AssetID: id, OwnerID: sh.OwnerID, SharePct: db.Hundredths(sh.BP)}
	}
	return out
}

func (s Service) checkParent(ctx context.Context, parent *uuid.UUID) error {
	if parent == nil {
		return nil
	}
	pa, err := s.Repo.Asset(ctx, *parent)
	if errors.Is(err, invariant.ErrNotFound) || err == nil && pa.Kind == string(KindBox) {
		return invariant.New(TypeParentInvalid, parent.String())
	}
	return err
}

func mapShareFK(err error) error {
	if err != nil && strings.Contains(err.Error(), "asset_shares_owner_id_fkey") {
		return invariant.New(TypeShareOwnerUnknown, "")
	}
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return invariant.New(TypeShareOwnerUnknown, "")
	}
	return err
}

// CreateAsset adds an asset to the scope.
func (s Service) CreateAsset(ctx context.Context, p *auth.Principal, requestID string, in AssetInput) (AssetView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return AssetView{}, err
	}
	if f := validateAsset(&in, sc.OwnerID); len(f) > 0 {
		return AssetView{}, f
	}
	var out AssetView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkParent(ctx, in.ParentAssetID); err != nil {
			return err
		}
		now := s.now()
		a := assetRowFrom(in)
		a.ID, a.OwnerID, a.Version, a.CreatedAt, a.CreatedBy, a.UpdatedAt, a.UpdatedBy = s.NewID(), sc.OwnerID, 1, now, p.Sub, now, p.Sub
		shares := shareRows(a.ID, in.Shares)
		if err := s.Repo.InsertAsset(ctx, a, shares); err != nil {
			return mapShareFK(err)
		}
		out = assetView(a, shares)
		return audit.Record(ctx, "asset.create", audit.Entity{Type: "asset", ID: a.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateAsset replaces an asset's attributes when version matches; its kind is
// immutable (RENT-SPEC-002).
func (s Service) UpdateAsset(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in AssetInput) (AssetView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return AssetView{}, err
	}
	if f := validateAsset(&in, sc.OwnerID); len(f) > 0 {
		return AssetView{}, f
	}
	var out AssetView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		before, err := s.AssetInTx(ctx, id)
		if err != nil {
			return err
		}
		if before.Kind != in.Kind {
			return invariant.New(TypeKindImmutable, string(before.Kind))
		}
		if before.Version != version {
			return invariant.ErrStale
		}
		if in.ParentAssetID != nil && *in.ParentAssetID == id {
			return invariant.New(TypeParentInvalid, "self")
		}
		if err := s.checkParent(ctx, in.ParentAssetID); err != nil {
			return err
		}
		a := assetRowFrom(in)
		a.ID, a.OwnerID, a.CreatedAt, a.CreatedBy = id, sc.OwnerID, before.CreatedAt, ""
		a.UpdatedAt, a.UpdatedBy, a.Version = s.now(), p.Sub, version+1
		shares := shareRows(id, in.Shares)
		if err := s.Repo.UpdateAsset(ctx, a, version, shares); err != nil {
			return mapShareFK(err)
		}
		out = assetView(a, shares)
		return audit.Record(ctx, "asset.update", audit.Entity{Type: "asset", ID: id.String()}, before, out)
	})
	return out, err
}

// --- Tenants ------------------------------------------------------------------

func tenantView(t tenantRow, role auth.Role) TenantView {
	v := TenantView{ID: t.ID, Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		TenantInput: TenantInput{Names: []string(t.Names), Email: t.Email, Phone: t.Phone, Language: t.Language}}
	if t.Occupation != nil {
		o := Occupation(*t.Occupation)
		v.Occupation = &o
	}
	if role == auth.RoleAccountant {
		v.Email, v.Phone, v.Language, v.Occupation = "", "", "", nil
	}
	return v
}

func validateTenant(in *TenantInput) invariant.Fields {
	var f invariant.Fields
	names := in.Names[:0:0]
	for _, n := range in.Names {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	in.Names = names
	if len(in.Names) == 0 {
		f.Add("names", "Au moins un nom est obligatoire.")
	}
	if in.Email != "" && !strings.Contains(in.Email, "@") {
		f.Add("email", "Adresse e-mail invalide.")
	}
	if in.Language == "" {
		in.Language = "fr"
	}
	if !languages[in.Language] {
		f.Add("language", "Langue attendue : fr ou en.")
	}
	if in.Occupation != nil {
		switch *in.Occupation {
		case OccupationStudent, OccupationApprentice, OccupationOther:
		default:
			f.Add("occupation", "Valeur attendue : student, apprentice ou other.")
		}
	}
	return f
}

func occupationCol(o *Occupation) *string {
	if o == nil {
		return nil
	}
	s := string(*o)
	return &s
}

// TenantsInTx returns the tenants with ids inside the current transaction
// (used by the lease service for documents and guarantee rules).
func (s Service) TenantsInTx(ctx context.Context, ids []uuid.UUID) ([]TenantView, error) {
	rows, err := s.Repo.Tenants(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]TenantView, 0, len(rows))
	for _, t := range rows {
		out = append(out, tenantView(t, auth.RoleOwner))
	}
	return out, nil
}

// OwnerInTx returns the profile of the scope's owner inside the current
// transaction.
func (s Service) OwnerInTx(ctx context.Context, id uuid.UUID) (OwnerView, error) {
	o, err := s.Repo.Owner(ctx, id)
	if err != nil {
		return OwnerView{}, err
	}
	return ownerView(o), nil
}

// ListTenants returns a page of the scope's tenants.
func (s Service) ListTenants(ctx context.Context, p *auth.Principal, requestID string, offset, limit int) ([]TenantView, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []TenantView
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.ListTenants(ctx, offset, limit)
		for _, t := range rows {
			out = append(out, tenantView(t, p.Active.Role))
		}
		total = n
		return err
	})
	return out, total, err
}

// GetTenant returns one tenant of the scope.
func (s Service) GetTenant(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (TenantView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return TenantView{}, err
	}
	var out TenantView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Tenants(ctx, []uuid.UUID{id})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return invariant.ErrNotFound
		}
		out = tenantView(rows[0], p.Active.Role)
		return nil
	})
	return out, err
}

// TenantsExist reports which of ids are tenants of the current transaction's
// scope (used by the lease service).
func (s Service) TenantsExist(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := s.Repo.Tenants(ctx, ids)
	out := map[uuid.UUID]bool{}
	for _, t := range rows {
		out[t.ID] = true
	}
	return out, err
}

// CreateTenant adds a tenant to the scope.
func (s Service) CreateTenant(ctx context.Context, p *auth.Principal, requestID string, in TenantInput) (TenantView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return TenantView{}, err
	}
	if f := validateTenant(&in); len(f) > 0 {
		return TenantView{}, f
	}
	var out TenantView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		now := s.now()
		t := tenantRow{ID: s.NewID(), OwnerID: sc.OwnerID, Names: in.Names, Email: in.Email, Phone: in.Phone, Language: in.Language,
			Occupation: occupationCol(in.Occupation), Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if err := s.Repo.InsertTenant(ctx, t); err != nil {
			return err
		}
		out = tenantView(t, auth.RoleOwner)
		return audit.Record(ctx, "tenant.create", audit.Entity{Type: "tenant", ID: t.ID.String()}, nil, out)
	})
	return out, err
}

// UpdateTenant replaces a tenant's attributes when version matches.
func (s Service) UpdateTenant(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in TenantInput) (TenantView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return TenantView{}, err
	}
	if f := validateTenant(&in); len(f) > 0 {
		return TenantView{}, f
	}
	var out TenantView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Tenants(ctx, []uuid.UUID{id})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return invariant.ErrNotFound
		}
		cur := rows[0]
		if cur.Version != version {
			return invariant.ErrStale
		}
		before := tenantView(cur, auth.RoleOwner)
		cur.Names, cur.Email, cur.Phone, cur.Language = in.Names, in.Email, in.Phone, in.Language
		cur.Occupation = occupationCol(in.Occupation)
		cur.UpdatedAt, cur.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateTenant(ctx, cur, version); err != nil {
			return err
		}
		cur.Version = version + 1
		out = tenantView(cur, auth.RoleOwner)
		return audit.Record(ctx, "tenant.update", audit.Entity{Type: "tenant", ID: id.String()}, before, out)
	})
	return out, err
}
