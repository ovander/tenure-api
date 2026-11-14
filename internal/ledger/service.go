package ledger

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
	"github.com/ovander/tenure-api/internal/tax"
)

// TypeChartIncomplete is a journal refused for a chart of accounts that
// misses a role or a charge category.
const TypeChartIncomplete = "ledger/chart-incomplete"

func init() {
	problem.Register(map[string]problem.Message{
		TypeChartIncomplete: problem.M(422, "Le plan de comptes ne couvre pas tous les mouvements de cette année.",
			"The chart of accounts does not cover every movement of this year."),
	})
}

// Service serves the owner's journal.
type Service struct {
	DB     *db.DB
	Repo   Repository
	Params params.Repository
	Now    func() time.Time
}

// View is the journal of a calendar year.
type View struct {
	Year int `json:"year"`
	// Closed says the year's tax summary is issued: its figures no longer
	// change (Q-127).
	Closed   bool      `json:"closed"`
	Entries  []Entry   `json:"entries"`
	Balances []Balance `json:"balances"`
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// statementFigures are the figures of a deposit statement the journal
// posts (inventory.Statement as issued).
type statementFigures struct {
	DepositCents        money.Cents `json:"deposit_cents"`
	DeductionsCents     money.Cents `json:"deductions_cents"`
	ArrearsCents        money.Cents `json:"arrears_cents"`
	RegularisationCents money.Cents `json:"regularisation_cents"`
	HoldbackCents       money.Cents `json:"holdback_cents"`
	PenaltyCents        money.Cents `json:"penalty_cents"`
	TotalCents          money.Cents `json:"total_cents"`
}

// inputs gathers the movements of [from, to].
func (s Service) inputs(ctx context.Context, from, to time.Time) (Inputs, error) {
	var in Inputs
	principal, err := tax.Repository{}.PrincipalAssets(ctx)
	if err != nil {
		return in, err
	}
	assetOf := func(lease uuid.UUID) string {
		if a, ok := principal[lease]; ok {
			return a.String()
		}
		return ""
	}
	ps, ms, err := s.Repo.Payments(ctx, from, to)
	if err != nil {
		return in, err
	}
	byPayment := map[uuid.UUID][]matchRow{}
	for _, m := range ms {
		byPayment[m.PaymentID] = append(byPayment[m.PaymentID], m)
	}
	for _, p := range ps {
		lp := Payment{ID: p.ID.String(), LeaseID: p.LeaseID.String(), AssetID: assetOf(p.LeaseID), PaidOn: period.FromTime(p.PaidOn), AmountCents: money.Cents(p.AmountCents)}
		for _, m := range byPayment[p.ID] {
			var lines []document.Line
			if err := json.Unmarshal(m.Lines, &lines); err != nil {
				return in, err
			}
			var rent, prov money.Cents
			for _, l := range lines {
				switch l.Kind {
				case string(payment.LineRent):
					rent += l.AmountCents
				case string(payment.LineProvision):
					prov += l.AmountCents
				}
			}
			// The same split as the tax summary (Q-65), so the rent account
			// is line 211's figure.
			parts, err := tax.SplitMatch(money.Cents(m.AmountCents), rent, prov, money.Cents(m.TotalCents))
			if err != nil {
				return in, err
			}
			for _, x := range parts {
				switch x.Line {
				case payment.LineRent:
					lp.RentCents += x.AmountCents
				case payment.LineProvision:
					lp.ProvisionCents += x.AmountCents
				default:
					lp.RegularisationCents += x.AmountCents
				}
			}
		}
		in.Payments = append(in.Payments, lp)
	}
	cs, err := s.Repo.Charges(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, c := range cs {
		in.Charges = append(in.Charges, Charge{ID: c.ID.String(), AssetID: c.AssetID.String(), Category: c.Category,
			PaidOn: period.FromTime(c.PaidOn), AmountCents: money.Cents(c.AmountCents), RecoverableCents: money.Cents(c.RecoverableCents),
			DocumentID: idString(c.DocumentID)})
	}
	is, err := s.Repo.Instalments(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, i := range is {
		in.Instalments = append(in.Instalments, Instalment{LoanID: i.LoanID.String(), AssetID: i.AssetID.String(), Seq: i.Seq, DueOn: period.FromTime(i.DueOn),
			PrincipalCents: money.Cents(i.PrincipalCents), InterestCents: money.Cents(i.InterestCents), InsuranceCents: money.Cents(i.InsuranceCents),
			DocumentID: idString(i.DocumentID)})
	}
	ds, err := s.Repo.Deposits(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, d := range ds {
		in.Deposits = append(in.Deposits, Deposit{ID: d.ID.String(), LeaseID: d.LeaseID.String(), AssetID: assetOf(d.LeaseID), ReceivedOn: period.FromTime(d.ReceivedOn),
			AmountCents: money.Cents(d.AmountCents)})
	}
	ss, err := s.Repo.Settlements(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, x := range ss {
		var f statementFigures
		if err := json.Unmarshal(x.Figures, &f); err != nil {
			return in, fmt.Errorf("ledger: settlement %s: %w", x.ID, err)
		}
		in.Settlements = append(in.Settlements, Settlement{ID: x.ID.String(), LeaseID: x.LeaseID.String(), AssetID: assetOf(x.LeaseID), SettledOn: period.FromTime(x.SettledOn),
			ReleasedCents: f.DepositCents - f.HoldbackCents, DeductionsCents: f.DeductionsCents, ArrearsCents: f.ArrearsCents,
			RegularisationCents: f.RegularisationCents, PenaltyCents: f.PenaltyCents, TotalCents: f.TotalCents, DocumentID: x.DocumentID.String()})
	}
	ims, err := s.Repo.Improvements(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, x := range ims {
		in.Improvements = append(in.Improvements, Improvement{IncidentID: x.ID.String(), AssetID: x.AssetID.String(), Title: x.Title,
			PaidOn: period.FromTime(x.PaidOn), AmountCents: money.Cents(x.InvoiceCents), DocumentID: idString(x.InvoiceDocumentID)})
	}
	return in, nil
}

func idString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// Year returns the journal of a calendar year (RENT-SPEC-014, Q-127), the
// chart of accounts being the one effective on its 31 December.
func (s Service) Year(ctx context.Context, p *auth.Principal, requestID string, year int) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if err := checkYear(year); err != nil {
		return View{}, err
	}
	out := View{Year: year}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		y, err := s.year(ctx, year)
		out = y.View
		return err
	})
	return out, err
}

// JournalInTx posts the journal of a calendar year in the caller's scoped
// transaction (the dashboard reads it, RENT-SPEC-016).
func (s Service) JournalInTx(ctx context.Context, year int) (View, error) {
	if err := checkYear(year); err != nil {
		return View{}, err
	}
	y, err := s.year(ctx, year)
	return y.View, err
}

// yearData is a year's journal with what its exports need.
type yearData struct {
	View
	issued issuedRow
}

func checkYear(year int) error {
	if year < 1970 || year > 9999 {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		return f
	}
	return nil
}

// year posts the journal of a calendar year in the scope's transaction.
func (s Service) year(ctx context.Context, year int) (yearData, error) {
	from, to := period.NewDate(year, time.January, 1), period.NewDate(year, time.December, 31)
	out := yearData{View: View{Year: year}}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	chart, err := Chart(ps, to)
	if err != nil {
		return out, err
	}
	in, err := s.inputs(ctx, from.Time(), to.Time())
	if err != nil {
		return out, err
	}
	out.Entries, out.Balances, err = Journal(in, chart, from, to)
	if errors.Is(err, ErrChart) {
		return out, invariant.New(TypeChartIncomplete, err.Error())
	}
	if err != nil {
		return out, err
	}
	out.issued, out.Closed, err = s.Repo.Issued(ctx, year)
	return out, err
}

// Export kinds.
const (
	ExportJournal = "journal"
	ExportFEC     = "fec"
	ExportPack    = "pack"
)

// TypeYearOpen is a FEC asked for a year not yet closed: a FEC holds
// validated entries only (Q-128).
const TypeYearOpen = "ledger/year-open"

func init() {
	problem.Register(map[string]problem.Message{
		TypeYearOpen: problem.M(409, "Le FEC n’est produit que pour une année close, une fois son récapitulatif fiscal émis.",
			"The FEC is produced only for a closed year, once its tax summary is issued."),
	})
}

// File is an export's content.
type File struct {
	Name        string
	ContentType string
	Data        []byte
}

// Export produces the journal CSV, the FEC or the year-end pack of a year
// (RENT-SPEC-014, Q-128), recording the export with its digest.
func (s Service) Export(ctx context.Context, p *auth.Principal, requestID string, year int, kind string) (File, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return File{}, err
	}
	if err := checkYear(year); err != nil {
		return File{}, err
	}
	var out File
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		y, err := s.year(ctx, year)
		if err != nil {
			return err
		}
		switch kind {
		case ExportJournal:
			out = File{Name: fmt.Sprintf("journal-%d.csv", year), ContentType: "text/csv; charset=utf-8", Data: CSV(y.Entries)}
		case ExportFEC:
			if !y.Closed {
				return invariant.New(TypeYearOpen, strconv.Itoa(year))
			}
			b, err := FEC(y.Entries, period.FromTime(y.issued.IssuedAt.In(paris())))
			if err != nil {
				return err
			}
			out = File{Name: FECName(year), ContentType: "text/plain; charset=iso-8859-15", Data: b}
		case ExportPack:
			b, err := s.pack(ctx, y)
			if err != nil {
				return err
			}
			out = File{Name: fmt.Sprintf("cloture-%d.zip", year), ContentType: "application/zip", Data: b}
		default:
			return fmt.Errorf("ledger: unknown export %q", kind)
		}
		return audit.Record(ctx, "ledger.export", audit.Entity{Type: "ledger", ID: strconv.Itoa(year)}, nil,
			map[string]any{"year": year, "kind": kind, "closed": y.Closed, "sha256": document.SHA256(out.Data)})
	})
	return out, err
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

// pack builds the year-end pack (Q-128): the journal, the balances, the FEC
// when the year is closed, the 2044 lines of its issued tax summary, the
// index of the supporting documents and a note saying what is in it.
func (s Service) pack(ctx context.Context, y yearData) ([]byte, error) {
	files := []PackFile{{Name: fmt.Sprintf("journal-%d.csv", y.Year), Data: CSV(y.Entries)},
		{Name: fmt.Sprintf("soldes-%d.csv", y.Year), Data: BalancesCSV(y.Balances)}}
	extra := map[string]string{}
	modified := time.Date(y.Year, time.December, 31, 0, 0, 0, 0, time.UTC)
	note := []string{fmt.Sprintf("Clôture de l’exercice %d, produite par Tenure.", y.Year), "",
		fmt.Sprintf("journal-%d.csv : le journal en partie double, en trésorerie, montants en centimes.", y.Year),
		fmt.Sprintf("soldes-%d.csv : les mouvements et le solde de chaque compte.", y.Year)}
	if y.Closed {
		b, err := FEC(y.Entries, period.FromTime(y.issued.IssuedAt.In(paris())))
		if err != nil {
			return nil, err
		}
		var sum tax.Summary
		if err := json.Unmarshal(y.issued.Summary, &sum); err != nil {
			return nil, err
		}
		lines, err := tax.CSV(y.Year, sum, nil)
		if err != nil {
			return nil, err
		}
		files = append(files, PackFile{Name: FECName(y.Year), Data: b}, PackFile{Name: fmt.Sprintf("lignes-2044-%d.csv", y.Year), Data: lines})
		note = append(note, FECName(y.Year)+" : le fichier des écritures comptables (LPF art. A47 A-1), validé le jour de l’émission du récapitulatif fiscal.",
			fmt.Sprintf("lignes-2044-%d.csv : les lignes de la déclaration 2044 telles qu’émises.", y.Year))
		for _, d := range []*uuid.UUID{y.issued.DocumentID, y.issued.CSVDocumentID} {
			if d != nil {
				extra[d.String()] = fmt.Sprintf("tax_summary:%d", y.Year)
			}
		}
		modified = y.issued.IssuedAt
	} else {
		note = append(note, "L’année n’est pas close : son récapitulatif fiscal n’est pas émis, il n’y a ni FEC ni lignes 2044, et les chiffres peuvent encore changer.")
	}
	idx, err := s.index(ctx, Pieces(y.Entries, extra))
	if err != nil {
		return nil, err
	}
	files = append(files, PackFile{Name: fmt.Sprintf("pieces-%d.csv", y.Year), Data: idx})
	note = append(note, fmt.Sprintf("pieces-%d.csv : les pièces justificatives archivées et les mouvements qu’elles justifient ; elles se téléchargent depuis les documents.", y.Year), "")
	files = append(files, PackFile{Name: "LISEZMOI.txt", Data: []byte(strings.Join(note, "\r\n"))})
	return Pack(files, modified)
}

// index writes the supporting documents index.
func (s Service) index(ctx context.Context, pieces []Piece) ([]byte, error) {
	ids := make([]uuid.UUID, 0, len(pieces))
	for _, p := range pieces {
		if id, err := uuid.Parse(p.DocumentID); err == nil {
			ids = append(ids, id)
		}
	}
	docs, err := s.Repo.Documents(ctx, ids)
	if err != nil {
		return nil, err
	}
	by := map[string]documentRow{}
	for _, d := range docs {
		by[d.ID.String()] = d
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"document_id", "class", "name", "sha256", "size_bytes", "archived_at", "sources"})
	for _, p := range pieces {
		d, ok := by[p.DocumentID]
		if !ok {
			continue
		}
		name := d.OriginalName
		if name == "" {
			name = path.Base(d.ObjectKey)
		}
		_ = w.Write([]string{p.DocumentID, d.Class, name, d.SHA256, strconv.FormatInt(d.SizeBytes, 10), d.CreatedAt.UTC().Format(time.RFC3339),
			strings.Join(p.Sources, " ")})
	}
	w.Flush()
	return b.Bytes(), nil
}
