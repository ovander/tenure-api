package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the deposit routes (RENT-INV-001 §6, §7, Q-115).
const (
	TypeDeductionUnjustified  = "inventory/deduction-unjustified"
	TypeGridLineMissing       = "inventory/grid-line-missing"
	TypeDeductionDocument     = "inventory/deduction-document-invalid"
	TypeSettlementUnavailable = "inventory/settlement-unavailable"
	TypeDepositSettled        = "inventory/deposit-settled"
	TypeRegularisationProof   = "inventory/regularisation-document-invalid"
	TypeArrearsUnavailable    = "inventory/arrears-unavailable"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeDeductionUnjustified:  problem.M(409, "Une retenue porte sur un élément dégradé ou manquant à la sortie, ou une clé manquante.", "A deduction is for an item worse or missing at exit, or a missing key."),
		TypeGridLineMissing:       problem.M(422, "La grille de vétusté du bail n’a pas de ligne pour cette catégorie : la retenue n’est pas calculée.", "The lease's wear grid has no line for this category: the deduction is not computed."),
		TypeDeductionDocument:     problem.M(422, "Le devis ou la facture doit être un document déposé, de classe supporting, de ce bail.", "The quote or invoice must be an uploaded document of class supporting for this lease."),
		TypeSettlementUnavailable: problem.M(409, "La restitution suit l’état des lieux de sortie émis, avec la date de remise des clés.", "The restitution follows the issued exit inventory, with the day the keys were returned."),
		TypeDepositSettled:        problem.M(409, "Le dépôt de garantie de ce bail est déjà réglé.", "The deposit of this lease is already settled."),
		TypeRegularisationProof:   problem.M(422, "La régularisation retenue doit être justifiée par un document de ce bail.", "The regularisation retained must be backed by a document of this lease."),
		TypeArrearsUnavailable:    problem.M(503, "Les impayés du bail ne peuvent pas être lus : réessayez plus tard.", "The lease's arrears cannot be read: try again later."),
	})
}

// RestitutionTemplate is the version of templates/restitution the RD
// statement renders with; a template change adds a version (tenure-api#84).
const RestitutionTemplate = "v1"

// Settlement kinds (settlement_kind).
const (
	SettlementInitial = "initial"
	SettlementRelease = "release"
)

// ArrearsFunc returns, inside the caller's scoped transaction, what a lease's
// live rent calls still owe on today. internal/app wires billing's.
type ArrearsFunc func(ctx context.Context, lease uuid.UUID, today period.Date) (money.Cents, error)

// --- Grids -------------------------------------------------------------------

// GridInput is a wear grid as the owner enters it, from a collective
// agreement or agreed by the parties (INV-OD-08: none is shipped).
type GridInput struct {
	Name   string     `json:"name"`
	Source string     `json:"source"`
	Lines  []GridLine `json:"lines"`
}

// GridView is a stored wear grid.
type GridView struct {
	ID uuid.UUID `json:"id"`
	GridInput
	Version int `json:"version"`
}

func gridView(r gridRow) (GridView, error) {
	v := GridView{ID: r.ID, Version: r.Version, GridInput: GridInput{Name: r.Name, Source: r.Source, Lines: []GridLine{}}}
	if err := json.Unmarshal(r.Lines, &v.Lines); err != nil {
		return GridView{}, fmt.Errorf("inventory: grid %s: %w", r.ID, err)
	}
	return v, nil
}

func validGrid(in GridInput) error {
	var f invariant.Fields
	if strings.TrimSpace(in.Name) == "" {
		f.Add("name", "Le nom de la grille est obligatoire.")
	}
	if strings.TrimSpace(in.Source) == "" {
		f.Add("source", "La source de la grille est obligatoire (accord collectif, accord des parties).")
	}
	if len(in.Lines) == 0 {
		f.Add("lines", "Au moins une ligne.")
	}
	seen := map[Category]bool{}
	for i, l := range in.Lines {
		pre := fmt.Sprintf("lines[%d].", i)
		if !slices.Contains(categories, l.Category) || seen[l.Category] {
			f.Add(pre+"category", "Une catégorie connue, une seule fois.")
		}
		seen[l.Category] = true
		if l.check() != nil {
			f.Add(pre+"floor_bp", "Durée, franchise et taux positifs, plancher entre 0 et 10 000.")
		}
	}
	return f.Err()
}

// Grids lists the scope's wear grids.
func (s Service) Grids(ctx context.Context, p *auth.Principal, requestID string) ([]GridView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []GridView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Grids(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			v, err := gridView(r)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// SaveGrid creates a grid (id nil) or replaces one at its version. A grid
// already used by a statement keeps its lines there: the RD is a snapshot.
func (s Service) SaveGrid(ctx context.Context, p *auth.Principal, requestID string, id *uuid.UUID, version int, in GridInput) (GridView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return GridView{}, err
	}
	if err := validGrid(in); err != nil {
		return GridView{}, err
	}
	raw, err := json.Marshal(in.Lines)
	if err != nil {
		return GridView{}, err
	}
	var out GridView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		now := s.now()
		r := gridRow{OwnerID: sc.OwnerID, Name: strings.TrimSpace(in.Name), Source: strings.TrimSpace(in.Source), Lines: raw,
			UpdatedAt: now, UpdatedBy: p.Sub}
		var before any
		if id == nil {
			r.ID, r.Version, r.CreatedAt, r.CreatedBy = s.NewID(), 1, now, p.Sub
			if err := s.Repo.InsertGrid(ctx, r); err != nil {
				return err
			}
		} else {
			cur, ok, err := s.Repo.Grid(ctx, *id, true)
			if err != nil {
				return err
			}
			if !ok {
				return invariant.ErrNotFound
			}
			if cur.Version != version {
				return invariant.ErrStale
			}
			if before, err = gridView(cur); err != nil {
				return err
			}
			r.ID, r.CreatedAt, r.CreatedBy = cur.ID, cur.CreatedAt, cur.CreatedBy
			if err := s.Repo.SaveGrid(ctx, r, version); err != nil {
				return err
			}
			r.Version = version + 1
		}
		if out, err = gridView(r); err != nil {
			return err
		}
		action := "inventory.grid.create"
		if id != nil {
			action = "inventory.grid.update"
		}
		return audit.Record(ctx, action, audit.Entity{Type: "vetuste_grid", ID: r.ID.String()}, before, out)
	})
	return out, err
}

// GridInputLease is the body of PUT /leases/{id}/vetuste-grid.
type GridInputLease struct {
	VetusteGridID *uuid.UUID `json:"vetuste_grid_id"`
}

// AttachGrid annexes a wear grid to a lease, or removes it, until the
// deposit is settled.
func (s Service) AttachGrid(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in GridInputLease) (DepositView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DepositView{}, err
	}
	var out DepositView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		l, ok, err := s.Repo.DepositLease(ctx, leaseID)
		if err != nil {
			return err
		}
		if !ok {
			return invariant.ErrNotFound
		}
		if sts, err := s.Repo.Settlements(ctx, leaseID); err != nil {
			return err
		} else if len(sts) > 0 {
			return invariant.New(TypeDepositSettled, leaseID.String())
		}
		if in.VetusteGridID != nil {
			if _, ok, err := s.Repo.Grid(ctx, *in.VetusteGridID, false); err != nil {
				return err
			} else if !ok {
				return invariant.Fields{{Field: "vetuste_grid_id", Message: "Grille inconnue."}}
			}
		}
		if err := s.Repo.SetLeaseGrid(ctx, leaseID, in.VetusteGridID, s.now(), p.Sub); err != nil {
			return err
		}
		if out, err = s.depositView(ctx, sc, leaseID); err != nil {
			return err
		}
		return audit.Record(ctx, "lease.vetuste_grid", audit.Entity{Type: "lease", ID: leaseID.String()},
			map[string]any{"vetuste_grid_id": l.GridID}, map[string]any{"vetuste_grid_id": in.VetusteGridID})
	})
	return out, err
}

// --- Deductions --------------------------------------------------------------

// DeductionCreate is the body of POST /leases/{id}/deposit-deductions: an exit
// item worse or missing at exit (or a missing key line), qualified by the
// owner as the tenant's, with the cost from an archived quote or invoice.
type DeductionCreate struct {
	ExitItemID  *uuid.UUID  `json:"exit_item_id,omitempty"`
	Key         *KeyRef     `json:"key,omitempty"`
	Description string      `json:"description"`
	CostCents   money.Cents `json:"cost_cents"`
	Basis       string      `json:"basis" enums:"quote,invoice"`
	DocumentID  *uuid.UUID  `json:"document_id"`
}

// KeyRef names a key line of the comparison.
type KeyRef struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
}

// DeductionView is a recorded deduction.
type DeductionView struct {
	ID          uuid.UUID   `json:"id"`
	ExitItemID  *uuid.UUID  `json:"exit_item_id,omitempty"`
	Description string      `json:"description"`
	CostCents   money.Cents `json:"cost_cents"`
	ShareBP     int64       `json:"tenant_share_bp"`
	AmountCents money.Cents `json:"amount_cents"`
	Basis       string      `json:"basis" enums:"quote,invoice"`
	DocumentID  uuid.UUID   `json:"document_id"`
}

func deductionView(r deductionRow) DeductionView {
	return DeductionView{ID: r.ID, ExitItemID: r.ExitItemID, Description: r.Description, CostCents: money.Cents(r.CostCents),
		ShareBP: r.TenantShareBP, AmountCents: money.Cents(r.AmountCents), Basis: r.Basis, DocumentID: r.DocumentID}
}

// depositFacts is a lease's deposit situation loaded in one transaction.
type depositFacts struct {
	lease       depositLease
	grid        *GridView
	exit        *capture
	comparison  *ComparisonView
	deductions  []deductionRow
	settlements []settlementRow
	rules       params.DepositReturn
	ps          params.Set
}

func (s Service) depositFacts(ctx context.Context, sc db.Scope, leaseID uuid.UUID) (depositFacts, error) {
	l, ok, err := s.Repo.DepositLease(ctx, leaseID)
	if err != nil {
		return depositFacts{}, err
	}
	if !ok {
		return depositFacts{}, invariant.ErrNotFound
	}
	f := depositFacts{lease: l}
	if l.GridID != nil {
		g, ok, err := s.Repo.Grid(ctx, *l.GridID, false)
		if err != nil {
			return depositFacts{}, err
		}
		if ok {
			v, err := gridView(g)
			if err != nil {
				return depositFacts{}, err
			}
			f.grid = &v
		}
	}
	if exitID, ok, err := s.Repo.ExitOf(ctx, leaseID); err != nil {
		return depositFacts{}, err
	} else if ok {
		c, err := s.load(ctx, sc, exitID, false)
		if err != nil {
			return depositFacts{}, err
		}
		f.exit = &c
		f.ps = c.facts.Params
		if c.head.State == StateIssued && c.entry != nil {
			cmp, err := c.comparison()
			if err != nil {
				return depositFacts{}, err
			}
			f.comparison = &cmp
		}
	}
	if f.ps.Entries() == nil {
		if f.ps, err = s.Lease.Params.Load(ctx); err != nil {
			return depositFacts{}, err
		}
	}
	if f.rules, err = f.ps.DepositReturn(s.today()); err != nil {
		return depositFacts{}, err
	}
	if f.deductions, err = s.Repo.Deductions(ctx, leaseID); err != nil {
		return depositFacts{}, err
	}
	f.settlements, err = s.Repo.Settlements(ctx, leaseID)
	return f, err
}

// keysReturned is the day the keys came back, from the issued exit.
func (f depositFacts) keysReturned() (period.Date, bool) {
	if f.exit == nil || f.exit.head.State != StateIssued || f.exit.head.KeysReturnedOn == nil {
		return period.Date{}, false
	}
	return period.FromTime(*f.exit.head.KeysReturnedOn), true
}

func (f depositFacts) settlement(kind string) (settlementRow, bool) {
	for _, r := range f.settlements {
		if r.Kind == kind {
			return r, true
		}
	}
	return settlementRow{}, false
}

// AddDeduction records a deduction on a flagged exit item or a missing key.
// The tenant's share comes from the lease's grid for the item's category and
// age (Deduct); without a grid the whole cost, which the owner justifies.
func (s Service) AddDeduction(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in DeductionCreate) (DeductionView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DeductionView{}, err
	}
	var f invariant.Fields
	if strings.TrimSpace(in.Description) == "" {
		f.Add("description", "La description de la retenue est obligatoire.")
	} else if tooLong(in.Description, maxComment) {
		f.Add("description", fmt.Sprintf("Au plus %d caractères.", maxComment))
	}
	if in.CostCents <= 0 {
		f.Add("cost_cents", "Le coût du devis ou de la facture est obligatoire.")
	}
	if in.Basis != "quote" && in.Basis != "invoice" {
		f.Add("basis", "Justificatif attendu : quote (devis) ou invoice (facture).")
	}
	if in.DocumentID == nil {
		f.Add("document_id", "Le devis ou la facture est obligatoire.")
	}
	if (in.ExitItemID == nil) == (in.Key == nil) {
		f.Add("exit_item_id", "Un élément de la sortie ou une clé manquante.")
	}
	if err := f.Err(); err != nil {
		return DeductionView{}, err
	}
	var out DeductionView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		df, err := s.depositFacts(ctx, sc, leaseID)
		if err != nil {
			return err
		}
		if len(df.settlements) > 0 {
			return invariant.New(TypeDepositSettled, leaseID.String())
		}
		if df.comparison == nil {
			return invariant.New(TypeSettlementUnavailable, "no issued exit compared with its entry")
		}
		d, ok, err := s.Repo.DocumentInfo(ctx, *in.DocumentID)
		if err != nil {
			return err
		}
		if !ok || d.Class != string(document.ClassSupporting) || d.Source != document.SourceUpload || d.LeaseID == nil || *d.LeaseID != leaseID {
			return invariant.New(TypeDeductionDocument, in.DocumentID.String())
		}
		ded := Deduction{ShareBP: fullBP, AmountCents: in.CostCents}
		if in.ExitItemID != nil {
			res, ok := flaggedItem(*df.comparison, in.ExitItemID.String())
			if !ok {
				return invariant.New(TypeDeductionUnjustified, in.ExitItemID.String())
			}
			for _, r := range df.deductions {
				if r.ExitItemID != nil && *r.ExitItemID == *in.ExitItemID {
					return invariant.Fields{{Field: "exit_item_id", Message: "Cet élément a déjà une retenue."}}
				}
			}
			if ded, err = s.deduct(ctx, df, res, in.CostCents); err != nil {
				return err
			}
		} else if !missingKey(*df.comparison, *in.Key) {
			return invariant.New(TypeDeductionUnjustified, in.Key.Kind+" "+in.Key.Destination)
		}
		now := s.now()
		r := deductionRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, ExitItemID: in.ExitItemID, Description: strings.TrimSpace(in.Description),
			CostCents: int64(in.CostCents), TenantShareBP: ded.ShareBP, AmountCents: int64(ded.AmountCents), Basis: in.Basis, DocumentID: *in.DocumentID,
			Version: 1, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub}
		if err := s.Repo.InsertDeduction(ctx, r); err != nil {
			return err
		}
		out = deductionView(r)
		return audit.Record(ctx, "deposit.deduction.create", audit.Entity{Type: "deposit_deduction", ID: r.ID.String()}, nil,
			map[string]any{"lease_id": leaseID, "deduction": out, "age_years": ded.AgeYears, "grid_line": ded.Line})
	})
	return out, err
}

func flaggedItem(c ComparisonView, exitItem string) (ItemResult, bool) {
	for _, r := range c.Items {
		if r.ExitItemID == exitItem && (r.Result == Worse || r.Result == Missing) {
			return r, true
		}
	}
	return ItemResult{}, false
}

func missingKey(c ComparisonView, k KeyRef) bool {
	for _, r := range c.Keys {
		if r.Kind == k.Kind && r.Destination == k.Destination && r.Result == Missing {
			return true
		}
	}
	return false
}

// deduct applies the grid to a flagged item: its age from the layout
// element's installation, or the lease start, to the exit's date.
func (s Service) deduct(ctx context.Context, df depositFacts, res ItemResult, cost money.Cents) (Deduction, error) {
	in := DeductionInput{Category: res.Category, CostCents: cost, LeaseStart: period.FromTime(df.lease.StartOn)}
	if df.exit.head.DoneOn != nil {
		in.ExitOn = period.FromTime(*df.exit.head.DoneOn)
	}
	for _, it := range df.exit.items {
		if it.ID.String() == res.ExitItemID && it.ElementID != nil {
			on, err := s.Repo.InstalledOn(ctx, *it.ElementID)
			if err != nil {
				return Deduction{}, err
			}
			if on != nil {
				d := period.FromTime(*on)
				in.InstalledOn = &d
			}
		}
	}
	var g *Grid
	if df.grid != nil {
		g = &Grid{Name: df.grid.Name, Source: df.grid.Source, Lines: df.grid.Lines}
	}
	d, err := Deduct(in, g)
	if errors.Is(err, ErrNoGridLine) {
		return Deduction{}, invariant.New(TypeGridLineMissing, string(res.Category))
	}
	return d, err
}

// DeleteDeduction removes a deduction while the deposit is not settled.
func (s Service) DeleteDeduction(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) error {
	sc, err := scoped(p, requestID)
	if err != nil {
		return err
	}
	return s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, ok, err := s.Repo.Deduction(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return invariant.ErrNotFound
		}
		if sts, err := s.Repo.Settlements(ctx, r.LeaseID); err != nil {
			return err
		} else if len(sts) > 0 {
			return invariant.New(TypeDepositSettled, r.LeaseID.String())
		}
		if err := s.Repo.DeleteDeduction(ctx, id); err != nil {
			return err
		}
		return audit.Record(ctx, "deposit.deduction.delete", audit.Entity{Type: "deposit_deduction", ID: id.String()}, deductionView(r), nil)
	})
}

// --- Settlement --------------------------------------------------------------

// SettleInput is the body of POST /leases/{id}/deposit-settlement. The first
// statement settles the deposit; in a collective building whose accounts are
// not yet approved it holds back a share, released by a second statement
// once accounts_approved_on is known, net of the regularisation backed by
// its document.
type SettleInput struct {
	// DryRun computes the statement without issuing it.
	DryRun                   bool         `json:"dry_run,omitempty"`
	Release                  bool         `json:"release,omitempty"`
	AccountsApprovedOn       *period.Date `json:"accounts_approved_on,omitempty" swaggertype:"string" format:"date"`
	RegularisationCents      money.Cents  `json:"regularisation_cents,omitempty"`
	RegularisationDocumentID *uuid.UUID   `json:"regularisation_document_id,omitempty"`
}

// StatementLine is a deduction as the statement lists it.
type StatementLine struct {
	DeductionView
	Room      string `json:"room,omitempty"`
	Label     string `json:"label,omitempty"`
	Entry     *State `json:"entry,omitempty"`
	Exit      *State `json:"exit,omitempty"`
	Document  string `json:"document_sha256,omitempty"`
	GridLabel string `json:"grid,omitempty"`
}

// Statement is a restitution statement's figures, previewed or issued.
type Statement struct {
	Kind           string          `json:"kind" enums:"initial,release"`
	SettledOn      period.Date     `json:"settled_on" swaggertype:"string" format:"date"`
	KeysReturnedOn *period.Date    `json:"keys_returned_on,omitempty" swaggertype:"string" format:"date"`
	Deadline       period.Date     `json:"deadline" swaggertype:"string" format:"date"`
	DepositCents   money.Cents     `json:"deposit_cents"`
	Lines          []StatementLine `json:"lines"`
	Settlement
	PenaltyMonths int         `json:"penalty_months"`
	PenaltyCents  money.Cents `json:"penalty_cents"`
	// TotalCents is the balance plus the late penalty: what is paid back to
	// the tenant (negative: what the tenant still owes).
	TotalCents       money.Cents `json:"total_cents"`
	NewAddressGiven  bool        `json:"new_address_given"`
	RegularisationOf *uuid.UUID  `json:"regularisation_document_id,omitempty"`
	DocumentID       *uuid.UUID  `json:"document_id,omitempty"`
}

// DepositView is where a lease's deposit stands: its grid, the exit's
// flagged items, the deductions, a preview of the next statement when one
// can be issued, and the statements issued.
type DepositView struct {
	LeaseID            uuid.UUID       `json:"lease_id"`
	DepositCents       money.Cents     `json:"deposit_cents"`
	RentCents          money.Cents     `json:"rent_cents"`
	CollectiveBuilding bool            `json:"collective_building"`
	Grid               *GridView       `json:"grid,omitempty"`
	ExitInventoryID    *uuid.UUID      `json:"exit_inventory_id,omitempty"`
	Flagged            []ItemResult    `json:"flagged"`
	MissingKeys        []KeyResult     `json:"missing_keys"`
	Deductions         []DeductionView `json:"deductions"`
	Next               string          `json:"next" enums:"wait_exit,initial,release,done"`
	Preview            *Statement      `json:"preview,omitempty"`
	Statements         []Statement     `json:"statements"`
}

// Deposit returns where a lease's deposit stands. The co-owner reads it;
// the accountant reads the RD documents only (INV-OD-11).
func (s Service) Deposit(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID) (DepositView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return DepositView{}, err
	}
	var out DepositView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		out, err = s.depositView(ctx, sc, leaseID)
		return err
	})
	return out, err
}

func (s Service) depositView(ctx context.Context, sc db.Scope, leaseID uuid.UUID) (DepositView, error) {
	df, err := s.depositFacts(ctx, sc, leaseID)
	if err != nil {
		return DepositView{}, err
	}
	v := DepositView{LeaseID: leaseID, DepositCents: money.Cents(df.lease.DepositCents), RentCents: money.Cents(df.lease.RentCents),
		CollectiveBuilding: df.lease.Collective, Grid: df.grid, Flagged: []ItemResult{}, MissingKeys: []KeyResult{},
		Deductions: []DeductionView{}, Statements: []Statement{}}
	if df.exit != nil {
		v.ExitInventoryID = &df.exit.head.ID
	}
	if df.comparison != nil {
		for _, r := range df.comparison.Items {
			if r.Result == Worse || r.Result == Missing {
				v.Flagged = append(v.Flagged, r)
			}
		}
		for _, k := range df.comparison.Keys {
			if k.Result == Missing {
				v.MissingKeys = append(v.MissingKeys, k)
			}
		}
	}
	for _, r := range df.deductions {
		v.Deductions = append(v.Deductions, deductionView(r))
	}
	for _, r := range df.settlements {
		var st Statement
		if err := json.Unmarshal(r.Figures, &st); err != nil {
			return DepositView{}, fmt.Errorf("inventory: settlement %s: %w", r.ID, err)
		}
		d := r.DocumentID
		st.DocumentID = &d
		v.Statements = append(v.Statements, st)
	}
	v.Next = next(df)
	if v.Next == SettlementInitial || v.Next == SettlementRelease {
		st, err := s.statement(ctx, df, SettleInput{Release: v.Next == SettlementRelease})
		if err == nil {
			v.Preview = &st
		} else if !isInvariant(err) {
			return DepositView{}, err
		}
	}
	return v, nil
}

func isInvariant(err error) bool {
	var ie *invariant.Error
	var f invariant.Fields
	return errors.As(err, &ie) || errors.As(err, &f)
}

// next is the statement to issue: none before the issued exit with its keys
// date; the first; the release of a holdback; or none left.
func next(df depositFacts) string {
	first, ok := df.settlement(SettlementInitial)
	switch {
	case !ok:
		if _, has := df.keysReturned(); !has {
			return "wait_exit"
		}
		return SettlementInitial
	case first.HoldbackCents > 0:
		if _, released := df.settlement(SettlementRelease); !released {
			return SettlementRelease
		}
	}
	return "done"
}

// statement computes a statement as of today (RENT-INV-001 §6).
func (s Service) statement(ctx context.Context, df depositFacts, in SettleInput) (Statement, error) {
	keys, ok := df.keysReturned()
	if !ok {
		return Statement{}, invariant.New(TypeSettlementUnavailable, "no issued exit with its keys date")
	}
	today := s.today()
	newAddress := strings.TrimSpace(df.exit.head.TenantNewAddress) != ""
	st := Statement{SettledOn: today, KeysReturnedOn: &keys, DepositCents: money.Cents(df.lease.DepositCents), Lines: []StatementLine{},
		NewAddressGiven: newAddress, RegularisationOf: in.RegularisationDocumentID}
	if in.RegularisationCents < 0 {
		return Statement{}, invariant.Fields{{Field: "regularisation_cents", Message: "La régularisation ne peut pas être négative."}}
	}
	if in.AccountsApprovedOn != nil && today.Before(*in.AccountsApprovedOn) {
		return Statement{}, invariant.Fields{{Field: "accounts_approved_on", Message: "L’approbation des comptes ne peut pas être dans le futur."}}
	}
	if !in.Release {
		if _, done := df.settlement(SettlementInitial); done {
			return Statement{}, invariant.New(TypeDepositSettled, df.lease.ID.String())
		}
		st.Kind = SettlementInitial
		var amounts []money.Cents
		for _, r := range df.deductions {
			amounts = append(amounts, money.Cents(r.AmountCents))
			st.Lines = append(st.Lines, statementLine(df, r))
		}
		if s.Arrears == nil {
			return Statement{}, invariant.New(TypeArrearsUnavailable, "")
		}
		arrears, err := s.Arrears(ctx, df.lease.ID, today)
		if err != nil {
			return Statement{}, err
		}
		approved := in.AccountsApprovedOn != nil
		set, err := Settle(SettlementInput{DepositCents: st.DepositCents, Deductions: amounts, ArrearsCents: arrears,
			RegularisationCents: in.RegularisationCents, CollectiveBuilding: df.lease.Collective, AccountsApproved: approved}, df.rules)
		if err != nil {
			return Statement{}, err
		}
		if set.HoldbackCents > 0 && in.RegularisationCents > 0 {
			return Statement{}, invariant.Fields{{Field: "regularisation_cents", Message: "Sans comptes approuvés, la provision retenue tient lieu de régularisation."}}
		}
		st.Settlement = set
		if st.Deadline, err = SettlementDeadline(keys, len(amounts) > 0, df.rules); err != nil {
			return Statement{}, err
		}
	} else {
		first, ok := df.settlement(SettlementInitial)
		if !ok || first.HoldbackCents == 0 {
			return Statement{}, invariant.New(TypeSettlementUnavailable, "no holdback to release")
		}
		if _, done := df.settlement(SettlementRelease); done {
			return Statement{}, invariant.New(TypeDepositSettled, df.lease.ID.String())
		}
		st.Kind = SettlementRelease
		st.DepositCents = money.Cents(first.HoldbackCents)
		st.Settlement = Settlement{RegularisationCents: in.RegularisationCents, BalanceCents: money.Cents(first.HoldbackCents) - in.RegularisationCents}
		if in.AccountsApprovedOn == nil {
			// A preview before the approval is entered: due a month after
			// today at the earliest.
			st.Deadline = today.AddMonthsClamped(df.rules.HoldbackBalanceMonths)
		} else {
			d, err := HoldbackBalanceDeadline(*in.AccountsApprovedOn, df.rules)
			if err != nil {
				return Statement{}, err
			}
			st.Deadline = d
		}
	}
	pen, err := LatePenalty(st.Deadline, today, money.Cents(df.lease.RentCents), newAddress, df.rules)
	if err != nil {
		return Statement{}, err
	}
	st.PenaltyMonths, st.PenaltyCents = pen.StartedMonths, pen.AmountCents
	st.TotalCents = st.BalanceCents + st.PenaltyCents
	return st, nil
}

func statementLine(df depositFacts, r deductionRow) StatementLine {
	l := StatementLine{DeductionView: deductionView(r)}
	if r.ExitItemID != nil && df.comparison != nil {
		for _, it := range df.comparison.Items {
			if it.ExitItemID == r.ExitItemID.String() {
				exit := it.Exit
				l.Room, l.Label, l.Entry, l.Exit = it.Room, it.Label, it.Entry, &exit
				if df.grid != nil && r.TenantShareBP < fullBP {
					l.GridLabel = df.grid.Name
				}
			}
		}
	}
	return l
}

// Settle issues the next restitution statement (dry_run: computes it only).
// The first one lists every deduction, the arrears, the holdback in a
// collective building whose accounts are not approved, the late penalty
// owed and the total; the second releases the holdback. Each is issued as
// an RD document in the same transaction, and the deductions are frozen.
func (s Service) Settle(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in SettleInput) (Statement, error) {
	dryRun := in.DryRun
	sc, err := scoped(p, requestID)
	if err != nil {
		return Statement{}, err
	}
	if in.Release && in.AccountsApprovedOn == nil && !dryRun {
		return Statement{}, invariant.Fields{{Field: "accounts_approved_on", Message: "La date d’approbation des comptes est obligatoire pour libérer la provision."}}
	}
	// A dry run previews the figures; the regularisation's document is
	// required to issue them.
	if in.RegularisationCents > 0 && in.RegularisationDocumentID == nil && !dryRun {
		return Statement{}, invariant.New(TypeRegularisationProof, "missing")
	}
	var out Statement
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		df, err := s.depositFacts(ctx, sc, leaseID)
		if err != nil {
			return err
		}
		if in.RegularisationDocumentID != nil {
			d, ok, err := s.Repo.DocumentInfo(ctx, *in.RegularisationDocumentID)
			if err != nil {
				return err
			}
			// The first statement deducts arrears already: a billed RG is in
			// them, so only an unbilled regularisation (its supporting
			// document) is retained there; the release takes either.
			if !ok || d.LeaseID == nil || *d.LeaseID != leaseID ||
				(d.Class != string(document.ClassSupporting) && (!in.Release || d.Class != string(document.ClassRegularisation))) {
				return invariant.New(TypeRegularisationProof, in.RegularisationDocumentID.String())
			}
		}
		st, err := s.statement(ctx, df, in)
		if err != nil {
			return err
		}
		if dryRun {
			out = st
			return nil
		}
		docID, err := s.issueRestitution(ctx, sc, df, st)
		if err != nil {
			return err
		}
		st.DocumentID = &docID
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		r := settlementRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, Kind: st.Kind, SettledOn: st.SettledOn.Time(), Deadline: st.Deadline.Time(),
			Figures: raw, BalanceCents: int64(st.TotalCents), HoldbackCents: int64(st.HoldbackCents), DocumentID: docID, CreatedAt: s.now(), CreatedBy: p.Sub}
		if err := s.Repo.InsertSettlement(ctx, r); err != nil {
			return err
		}
		out = st
		return audit.Record(ctx, "deposit.settle", audit.Entity{Type: "lease", ID: leaseID.String()}, nil, st)
	})
	return out, err
}

// RestitutionData is the data of templates/restitution/v1.
type RestitutionData struct {
	Number      string
	IssuedOn    period.Date
	Release     bool
	Owner       document.Party
	Tenants     []string
	Premises    []string
	ExitNumber  string
	Statement   Statement
	FirstNumber string
	Lines       []RestitutionLine
	// KeysReturnedOn is the start of the restitution clock; Owed says the
	// total is the tenant's debt, TotalAbs its amount either way.
	KeysReturnedOn period.Date
	Owed           bool
	TotalAbs       money.Cents
}

// RestitutionLine is a deduction line of the RD document, in French.
type RestitutionLine struct {
	Item, Entry, Exit, Description, Basis, Document, Grid string
	CostCents, AmountCents                                money.Cents
	SharePct                                              string
}

// sharePct prints a share in basis points as a French percentage (2 decimals).
func sharePct(bp int64) string {
	return strings.Replace(fmt.Sprintf("%d,%02d %%", bp/100, bp%100), ",00 %", " %", 1)
}

func (s Service) issueRestitution(ctx context.Context, sc db.Scope, df depositFacts, st Statement) (uuid.UUID, error) {
	facts, err := s.Lease.InventoryFactsInTx(ctx, sc, df.lease.ID)
	if err != nil {
		return uuid.Nil, err
	}
	d := RestitutionData{Release: st.Kind == SettlementRelease, Owner: facts.Owner, Tenants: facts.Tenants, Statement: st,
		Owed: st.TotalCents < 0, TotalAbs: max(st.TotalCents, -st.TotalCents)}
	if st.KeysReturnedOn != nil {
		d.KeysReturnedOn = *st.KeysReturnedOn
	}
	for _, a := range facts.Assets {
		d.Premises = append(d.Premises, a.Label)
	}
	number := func(id *uuid.UUID) (string, error) {
		if id == nil {
			return "", nil
		}
		di, ok, err := s.Repo.DocumentInfo(ctx, *id)
		if err != nil || !ok {
			return "", err
		}
		return strings.TrimSuffix(path.Base(di.ObjectKey), ".pdf"), nil
	}
	if d.ExitNumber, err = number(df.exit.head.DocumentID); err != nil {
		return uuid.Nil, err
	}
	if first, ok := df.settlement(SettlementInitial); ok {
		if d.FirstNumber, err = number(&first.DocumentID); err != nil {
			return uuid.Nil, err
		}
	}
	basisFR := map[string]string{"quote": "devis", "invoice": "facture"}
	for _, l := range st.Lines {
		rl := RestitutionLine{Item: strings.TrimPrefix(l.Room+" · "+l.Label, " · "), Description: l.Description, Basis: basisFR[l.Basis],
			CostCents: l.CostCents, AmountCents: l.AmountCents, SharePct: sharePct(l.ShareBP), Grid: l.GridLabel}
		if l.Entry != nil {
			rl.Entry = stateFR(*l.Entry)
		}
		if l.Exit != nil {
			rl.Exit = stateFR(*l.Exit)
		}
		if di, ok, err := s.Repo.DocumentInfo(ctx, l.DocumentID); err != nil {
			return uuid.Nil, err
		} else if ok {
			rl.Document = di.SHA256
		}
		if rl.Item == "" {
			rl.Item = "Clés et moyens d’accès"
		}
		d.Lines = append(d.Lines, rl)
	}
	lid := df.lease.ID
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRestitution, Template: document.ClassRestitution,
		TemplateVersion: RestitutionTemplate, AssetID: facts.Principal, LeaseID: &lid, Snapshot: d,
		Data: func(n string, issued period.Date) any {
			d.Number, d.IssuedOn = n, issued
			return d
		}}, facts.Params)
	if err != nil {
		return uuid.Nil, err
	}
	return iss.DocumentID, nil
}
