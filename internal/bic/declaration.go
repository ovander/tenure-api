package bic

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
	"github.com/ovander/tenure-api/internal/tax"
)

// Problem types of the bic module.
const (
	TypeGridFrozen = "bic/grid-frozen"
	TypeDeclared   = "bic/declared"
	TypeOrder      = "bic/declaration-order"
	TypeMicroShut  = "bic/micro-closed"
	TypeYearOpen   = "bic/year-open"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeGridFrozen: problem.M(409, "Cette grille a servi à une déclaration : elle ne change plus. La révision du plan d’amortissement n’est pas encore prise en charge.",
			"This grid was used by a declaration: it no longer changes. Revising the depreciation plan is not supported yet."),
		TypeDeclared: problem.M(409, "La déclaration de cette année est déjà émise : elle ne change plus.",
			"This year's declaration is already issued: it no longer changes."),
		TypeOrder: problem.M(409, "Une année postérieure est déjà déclarée : les années se déclarent dans l’ordre.",
			"A later year is already declared: years are declared in order."),
		TypeMicroShut: problem.M(422, "Les recettes dépassent le seuil du micro-BIC : seul le régime réel est ouvert.",
			"The receipts exceed the micro-BIC threshold: only the réel is open."),
		TypeYearOpen: problem.M(409, "Cette année n’est pas close : sa déclaration se fait après le 31 décembre.",
			"This year is not over: it is declared after 31 December."),
	})
}

// DeclarationInput is the body of POST /bic/declarations.
type DeclarationInput struct {
	Year   int    `json:"year"`
	Regime string `json:"regime" enums:"reel,micro"`
}

// GridUse is a grid version a declaration used.
type GridUse struct {
	AssetID uuid.UUID `json:"asset_id"`
	GridID  uuid.UUID `json:"grid_id"`
	Version int       `json:"version"`
}

// Declaration is a declaration as the API returns it: its figures as they
// were issued.
type Declaration struct {
	ID       uuid.UUID   `json:"id"`
	Year     int         `json:"year"`
	Regime   string      `json:"regime" enums:"reel,micro"`
	Result   YearResult  `json:"result"`
	Assets   []AssetYear `json:"assets"`
	Grids    []GridUse   `json:"grids"`
	IssuedAt time.Time   `json:"issued_at"`
}

func declarationView(r declarationRow) (Declaration, error) {
	d := Declaration{ID: r.ID, Year: r.TaxYear, Regime: r.Regime, IssuedAt: r.IssuedAt}
	if err := json.Unmarshal(r.Result, &d.Result); err != nil {
		return d, err
	}
	if err := json.Unmarshal(r.Assets, &d.Assets); err != nil {
		return d, err
	}
	err := json.Unmarshal(r.Grids, &d.Grids)
	return d, err
}

func (s Service) declarationsInTx(ctx context.Context) (map[int]Declaration, error) {
	rows, err := s.Repo.Declarations(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int]Declaration{}
	for _, r := range rows {
		d, err := declarationView(r)
		if err != nil {
			return nil, err
		}
		out[d.Year] = d
	}
	return out, nil
}

// Declarations lists the scope's owner's declarations, by year.
func (s Service) Declarations(ctx context.Context, p *auth.Principal, requestID string) ([]Declaration, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []Declaration{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Declarations(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			d, err := declarationView(r)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// Issue issues the declaration of a year under the regime chosen
// (RENT-SPEC-019, Q-132): its figures are computed from the declarations
// before it, stored as they are, and the grid versions they used are
// frozen. A year is declared once, after the years before it; the micro is
// open only up to its receipts threshold.
func (s Service) Issue(ctx context.Context, p *auth.Principal, requestID string, in DeclarationInput) (Declaration, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return Declaration{}, err
	}
	if in.Year < 1970 || in.Year > 9999 {
		return Declaration{}, fields("year", "Une année sur quatre chiffres est attendue.")
	}
	if in.Regime != RegimeReel && in.Regime != RegimeMicro {
		return Declaration{}, fields("regime", "Attendu : reel ou micro.")
	}
	if p.Active.IsSCI() {
		return Declaration{}, invariant.New(tax.TypePersonOnly, "")
	}
	if in.Year >= s.today().Year {
		// Its receipts and depreciation are not final (#258).
		return Declaration{}, invariant.New(TypeYearOpen, strconv.Itoa(in.Year))
	}
	var out Declaration
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		done, err := s.declarationsInTx(ctx)
		if err != nil {
			return err
		}
		if _, ok := done[in.Year]; ok {
			return invariant.New(TypeDeclared, "")
		}
		for y := range done {
			if y > in.Year {
				return invariant.New(TypeOrder, "")
			}
		}
		view, err := s.resultInTx(ctx, sc.OwnerID, in.Year, in.Regime, done)
		if err != nil {
			return err
		}
		if in.Regime == RegimeMicro && !view.Result.Micro.Eligible {
			return invariant.New(TypeMicroShut, "")
		}
		now := s.now()
		var grids []GridUse
		for _, a := range view.Assets {
			id, found, err := s.Repo.GridID(ctx, a.AssetID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if err := s.Repo.FreezeGrid(ctx, id, now); err != nil {
				return err
			}
			grids = append(grids, GridUse{AssetID: a.AssetID, GridID: id, Version: a.GridVersion})
		}
		if grids == nil {
			grids = []GridUse{}
		}
		res := view.Result
		res.Declared = true
		resJSON, err := json.Marshal(res)
		if err != nil {
			return err
		}
		assets, err := json.Marshal(view.Assets)
		if err != nil {
			return err
		}
		gridJSON, err := json.Marshal(grids)
		if err != nil {
			return err
		}
		r := declarationRow{ID: s.NewID(), OwnerID: sc.OwnerID, TaxYear: in.Year, Regime: in.Regime, Result: resJSON, Assets: assets,
			Grids: gridJSON, IssuedAt: now, IssuedBy: p.Sub}
		if err := s.Repo.InsertDeclaration(ctx, r); err != nil {
			return err
		}
		out, err = declarationView(r)
		if err != nil {
			return err
		}
		return audit.Record(ctx, "bic.declaration", audit.Entity{Type: "bic_declaration", ID: r.ID.String()}, nil, out)
	})
	return out, err
}
