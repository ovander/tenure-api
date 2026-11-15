package sci

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/problem"
	"github.com/ovander/tenure-api/internal/tax"
)

// Problem types of the 2072.
const (
	TypeYearOpen           = "sci/year-open"
	TypeDeclared           = "sci/declared"
	TypeYearDeclared       = "sci/year-declared"
	TypeFurnishedReceipts  = "sci/furnished-receipts"
	TypeRegisterIncomplete = "sci/register-incomplete"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeYearOpen: problem.M(409, "Cette année n’est pas close : sa 2072 se déclare après le 31 décembre.", "This year is not over: its 2072 is filed after 31 December."),
		TypeDeclared: problem.M(409, "La 2072 de cette année est déjà émise.", "This year's 2072 is already issued."),
		TypeYearDeclared: problem.M(409, "La 2072 de cette année est émise : les parts détenues cette année-là ne changent plus.",
			"This year's 2072 is issued: the parts held that year no longer change."),
		TypeFurnishedReceipts: problem.M(409, "La SCI a perçu des loyers meublés cette année : la location meublée, commerciale, peut la rendre passible de l’impôt sur les sociétés. Voyez votre expert-comptable ; la 2072 n’est pas calculée.",
			"The SCI received furnished rent this year: a commercial activity may make it liable to corporate tax. See your accountant; the 2072 is not computed."),
		TypeRegisterIncomplete: problem.M(409, "Au 31 décembre, les parts détenues ne font pas le capital de la SCI : complétez le registre des associés.",
			"On 31 December the parts held do not make up the SCI's capital: complete the register of associates."),
	})
}

// Blockers that keep a year from being issued.
const (
	BlockYearOpen           = "year_open"
	BlockFurnishedReceipts  = "furnished_receipts"
	BlockRegisterIncomplete = "register_incomplete"
	BlockDeclared           = "declared"
)

type declarationRow struct {
	ID       uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID  uuid.UUID       `gorm:"column:owner_id"`
	TaxYear  int             `gorm:"column:tax_year"`
	Result   json.RawMessage `gorm:"column:result;type:jsonb"`
	Holders  json.RawMessage `gorm:"column:holders;type:jsonb"`
	IssuedAt time.Time       `gorm:"column:issued_at"`
	IssuedBy string          `gorm:"column:issued_by"`
}

func (declarationRow) TableName() string { return "sci_declarations" }

type allocationRow struct {
	ID                 uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID            uuid.UUID `gorm:"column:owner_id"`
	DeclarationID      uuid.UUID `gorm:"column:declaration_id"`
	TaxYear            int       `gorm:"column:tax_year"`
	AssociateID        uuid.UUID `gorm:"column:associate_id"`
	AssociateName      string    `gorm:"column:associate_name"`
	PersonSub          *string   `gorm:"column:person_sub"`
	Parts              int       `gorm:"column:parts"`
	TotalParts         int       `gorm:"column:total_parts"`
	IncomeCents        int64     `gorm:"column:income_cents"`
	InterestCents      int64     `gorm:"column:interest_cents"`
	OtherExpensesCents int64     `gorm:"column:other_expenses_cents"`
	NetCents           int64     `gorm:"column:net_cents"`
}

func (allocationRow) TableName() string { return "sci_allocations" }

// Result2072 is the 2072's content of a year: the SCI's figures by nature,
// the 2044 lines they come from and their flags, and each holder's share.
type Result2072 struct {
	Figures
	Lines []tax.LineAmount `json:"lines"`
	Flags []tax.Flag       `json:"flags"`
	// FurnishedReceiptsCents is furnished rent received in the year: when
	// positive the 2072 is not issued (Q-133).
	FurnishedReceiptsCents money.Cents `json:"furnished_receipts_cents"`
}

// AllocationView is one associate's share of the year's result.
type AllocationView struct {
	AssociateID uuid.UUID `json:"associate_id"`
	Name        string    `json:"name"`
	PersonSub   *string   `json:"person_sub"`
	Parts       int       `json:"parts"`
	Figures
}

// YearView is GET /sci/result: the year's 2072, as declared when it is, else
// computed now; and what keeps it from being issued.
type YearView struct {
	Year         int              `json:"year"`
	Declared     bool             `json:"declared"`
	IssuedAt     *time.Time       `json:"issued_at"`
	CapitalParts int              `json:"capital_parts"`
	HeldParts    int              `json:"held_parts"`
	Result       Result2072       `json:"result"`
	Allocations  []AllocationView `json:"allocations"`
	Blockers     []string         `json:"blockers" enums:"year_open,furnished_receipts,register_incomplete,declared"`
}

// DeclarationInput is the body of POST /sci/declarations.
type DeclarationInput struct {
	Year int `json:"year"`
}

func (s Service) yearInTx(ctx context.Context, owner uuid.UUID, year int) (YearView, error) {
	v := YearView{Year: year, Allocations: []AllocationView{}, Blockers: []string{}}
	capital, _, err := s.Repo.Capital(ctx, owner)
	if err != nil {
		return v, err
	}
	v.CapitalParts = capital
	if d, ok, err := s.Repo.Declaration(ctx, year); err != nil {
		return v, err
	} else if ok {
		v.Declared, v.IssuedAt = true, &d.IssuedAt
		if err := json.Unmarshal(d.Result, &v.Result); err != nil {
			return v, err
		}
		rows, err := s.Repo.Allocations(ctx, d.ID)
		if err != nil {
			return v, err
		}
		for _, a := range rows {
			v.HeldParts = a.TotalParts
			v.Allocations = append(v.Allocations, AllocationView{AssociateID: a.AssociateID, Name: a.AssociateName, PersonSub: a.PersonSub, Parts: a.Parts,
				Figures: Figures{IncomeCents: money.Cents(a.IncomeCents), InterestCents: money.Cents(a.InterestCents),
					OtherExpensesCents: money.Cents(a.OtherExpensesCents), NetCents: money.Cents(a.NetCents)}})
		}
		v.Blockers = append(v.Blockers, BlockDeclared)
		return v, nil
	}
	sum, err := s.Tax.EstimateInTx(ctx, year)
	if err != nil {
		return v, err
	}
	for _, o := range sum.Owners {
		if o.OwnerID != owner.String() {
			continue
		}
		v.Result.Lines = o.Totals
		v.Result.IncomeCents, v.Result.InterestCents, v.Result.OtherExpensesCents = o.Reel.IncomeCents, o.Reel.InterestCents, o.Reel.OtherExpensesCents
		v.Result.NetCents = o.Reel.IncomeCents - o.Reel.InterestCents - o.Reel.OtherExpensesCents
		v.Result.FurnishedReceiptsCents = o.FurnishedGrossReceiptsCents
	}
	if v.Result.Lines == nil {
		v.Result.Lines = []tax.LineAmount{}
	}
	v.Result.Flags = sum.Flags
	if v.Result.Flags == nil {
		v.Result.Flags = []tax.Flag{}
	}
	as, err := s.Repo.Associates(ctx)
	if err != nil {
		return v, err
	}
	rows, err := s.Repo.Holdings(ctx)
	if err != nil {
		return v, err
	}
	hs := make([]Holding, len(rows))
	for i, h := range rows {
		hs[i] = holdingOf(h)
	}
	by, held := PartsOn(hs, params.TaxYearEnd(year))
	v.HeldParts = held
	var holders []Holder
	names := map[uuid.UUID]associateRow{}
	for _, a := range as {
		names[a.ID] = a
		holders = append(holders, Holder{Associate: a.ID, Parts: by[a.ID]})
	}
	if held > 0 {
		alloc, err := Allocate(v.Result.Figures, holders)
		if err != nil {
			return v, err
		}
		for _, a := range alloc {
			r := names[a.Associate]
			v.Allocations = append(v.Allocations, AllocationView{AssociateID: a.Associate, Name: r.Name, PersonSub: r.PersonSub, Parts: a.Parts, Figures: a.Figures})
		}
	}
	if year >= s.today().Year {
		v.Blockers = append(v.Blockers, BlockYearOpen)
	}
	if v.Result.FurnishedReceiptsCents > 0 {
		v.Blockers = append(v.Blockers, BlockFurnishedReceipts)
	}
	if held != capital {
		v.Blockers = append(v.Blockers, BlockRegisterIncomplete)
	}
	return v, nil
}

func checkYear(year int) error {
	if year < 1970 || year > 9999 {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		return f.Err()
	}
	return nil
}

// Year returns the year's 2072 (GET /sci/result?year=): as declared, or
// computed now from the SCI's own receipts, charges and loan interest with
// the rules of the 2044 réel and split by the parts held on 31 December.
func (s Service) Year(ctx context.Context, p *auth.Principal, requestID string, year int) (YearView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return YearView{}, err
	}
	if err := checkYear(year); err != nil {
		return YearView{}, err
	}
	var out YearView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		out, err = s.yearInTx(ctx, sc.OwnerID, year)
		return err
	})
	return out, err
}

// Declarations lists the SCI's issued 2072s, latest first.
func (s Service) Declarations(ctx context.Context, p *auth.Principal, requestID string) ([]YearView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []YearView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		years, err := s.Repo.DeclaredYears(ctx)
		if err != nil {
			return err
		}
		for _, y := range years {
			v, err := s.yearInTx(ctx, sc.OwnerID, y)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// Issue issues the year's 2072 (POST /sci/declarations, a step_up route,
// Q-133): once, after the year is over, when the parts held on 31 December
// make up the capital and the SCI received no furnished rent. Its figures
// and each associate's share are stored as they are; the register is locked
// for that year from then on.
func (s Service) Issue(ctx context.Context, p *auth.Principal, requestID string, in DeclarationInput) (YearView, error) {
	sc, err := scope(p, requestID)
	if err != nil {
		return YearView{}, err
	}
	if err := checkYear(in.Year); err != nil {
		return YearView{}, err
	}
	if in.Year >= s.today().Year {
		return YearView{}, invariant.New(TypeYearOpen, strconv.Itoa(in.Year))
	}
	var out YearView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.Repo.LockRegister(ctx, sc.OwnerID); err != nil {
			return err
		}
		v, err := s.yearInTx(ctx, sc.OwnerID, in.Year)
		if err != nil {
			return err
		}
		for _, b := range v.Blockers {
			switch b {
			case BlockDeclared:
				return invariant.New(TypeDeclared, strconv.Itoa(in.Year))
			case BlockYearOpen:
				return invariant.New(TypeYearOpen, strconv.Itoa(in.Year))
			case BlockFurnishedReceipts:
				return invariant.New(TypeFurnishedReceipts, "")
			case BlockRegisterIncomplete:
				return invariant.New(TypeRegisterIncomplete, "")
			}
		}
		now := s.now()
		result, err := json.Marshal(v.Result)
		if err != nil {
			return err
		}
		holders := make([]Holder, len(v.Allocations))
		for i, a := range v.Allocations {
			holders[i] = Holder{Associate: a.AssociateID, Parts: a.Parts}
		}
		hb, err := json.Marshal(holders)
		if err != nil {
			return err
		}
		d := declarationRow{ID: s.NewID(), OwnerID: sc.OwnerID, TaxYear: in.Year, Result: result, Holders: hb, IssuedAt: now, IssuedBy: p.Sub}
		if err := s.Repo.InsertDeclaration(ctx, d); err != nil {
			return err
		}
		for _, a := range v.Allocations {
			r := allocationRow{ID: s.NewID(), OwnerID: sc.OwnerID, DeclarationID: d.ID, TaxYear: in.Year, AssociateID: a.AssociateID,
				AssociateName: a.Name, PersonSub: a.PersonSub, Parts: a.Parts, TotalParts: v.HeldParts,
				IncomeCents: int64(a.IncomeCents), InterestCents: int64(a.InterestCents), OtherExpensesCents: int64(a.OtherExpensesCents), NetCents: int64(a.NetCents)}
			if err := s.Repo.InsertAllocation(ctx, r); err != nil {
				return err
			}
		}
		v.Declared, v.IssuedAt = true, &now
		v.Blockers = []string{BlockDeclared}
		out = v
		return audit.Record(ctx, "sci.declaration", audit.Entity{Type: "sci_declaration", ID: d.ID.String()}, nil, out)
	})
	return out, err
}

// lockedYear refuses a movement dated in a year whose 2072 is issued.
func (s Service) lockedYear(ctx context.Context, year int) error {
	last, ok, err := s.Repo.LastDeclaredYear(ctx)
	if err != nil {
		return err
	}
	if ok && year <= last {
		return invariant.New(TypeYearDeclared, strconv.Itoa(last))
	}
	return nil
}
