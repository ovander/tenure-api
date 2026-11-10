package payment

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the bank import.
const (
	TypeStatementInvalid = "payment/statement-invalid"
	TypeStatementFile    = "payment/statement-file"
	TypeLineState        = "payment/bank-line-state"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeStatementInvalid: problem.M(422, "Ce relevé ne peut pas être lu avec ce paramétrage.", "This statement cannot be read with this mapping."),
		TypeStatementFile:    problem.M(422, "Le relevé doit être un fichier CSV ou OFX déposé.", "The statement must be an uploaded CSV or OFX file."),
		TypeLineState:        problem.M(409, "Cette ligne bancaire est déjà rapprochée.", "This bank line is already matched."),
	})
}

// Statuses of a bank line (bank_lines.status).
const (
	LineUnmatched = "unmatched"
	LineMatched   = "matched"
	LineIgnored   = "ignored"
)

// AccountInput is the body of POST /bank-accounts.
type AccountInput struct {
	Label string `json:"label" example:"Compte courant"`
	// IBANLast4 is the last four characters of the IBAN, to tell accounts
	// apart; the full IBAN is never stored.
	IBANLast4 string `json:"iban_last4,omitempty" example:"0142"`
}

// AccountView is a bank account.
type AccountView struct {
	ID        uuid.UUID `json:"id"`
	Label     string    `json:"label"`
	IBANLast4 string    `json:"iban_last4,omitempty"`
	Version   int       `json:"version"`
}

// ImportInput is the body of POST /payments/import: a statement uploaded
// through POST /documents (scanned) into an account, and how to read it.
type ImportInput struct {
	AccountID  uuid.UUID   `json:"account_id"`
	DocumentID uuid.UUID   `json:"document_id"`
	Format     string      `json:"format" enums:"csv,ofx"`
	Mapping    *CSVMapping `json:"mapping,omitempty"`
	DryRun     bool        `json:"dry_run"`
}

// ImportResult is a statement imported, or previewed: its lines, and how
// many were new and how many the account already held.
type ImportResult struct {
	Lines      []BankLine `json:"lines"`
	New        int        `json:"new"`
	Duplicates int        `json:"duplicates"`
}

// LineView is a bank line as the API returns it. Stale says it is still
// unmatched past the alert delay after its booking day.
type LineView struct {
	ID          uuid.UUID   `json:"id"`
	AccountID   uuid.UUID   `json:"account_id"`
	BookedOn    period.Date `json:"booked_on" swaggertype:"string" format:"date"`
	AmountCents money.Cents `json:"amount_cents"`
	Label       string      `json:"label"`
	Reference   string      `json:"reference,omitempty"`
	Status      string      `json:"status" enums:"unmatched,matched,ignored"`
	Note        string      `json:"note,omitempty"`
	Stale       bool        `json:"stale,omitempty"`
	DocumentID  uuid.UUID   `json:"document_id"`
	Version     int         `json:"version"`
}

// LineStatusInput is the body of POST /bank-lines/{id}/status: ignore a line
// (a transfer between one's own accounts, a bank fee) or reopen it.
type LineStatusInput struct {
	Status  string `json:"status" enums:"unmatched,ignored"`
	Note    string `json:"note,omitempty"`
	Version int    `json:"version"`
}

func accountView(r accountRow) AccountView {
	return AccountView{ID: r.ID, Label: r.Label, IBANLast4: r.IBANLast4, Version: r.Version}
}

func (s Service) lineView(r lineRow, today period.Date, alert int) LineView {
	v := LineView{ID: r.ID, AccountID: r.AccountID, BookedOn: period.FromTime(r.BookedOn), AmountCents: money.Cents(r.AmountCents),
		Label: r.Label, Reference: r.Reference, Status: r.Status, Note: r.Note, DocumentID: r.ImportDocumentID, Version: r.Version}
	v.Stale = r.Status == LineUnmatched && today.After(v.BookedOn.AddDays(alert))
	return v
}

func (s Service) today() period.Date { return period.FromTime(s.Now().In(paris())) }

// Accounts lists the owner's bank accounts.
func (s Service) Accounts(ctx context.Context, p *auth.Principal, requestID string) ([]AccountView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []AccountView{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, err := s.Repo.Accounts(ctx)
		for _, r := range rows {
			out = append(out, accountView(r))
		}
		return err
	})
	return out, err
}

// CreateAccount records a bank account (RENT-SPEC-013, Q-123).
func (s Service) CreateAccount(ctx context.Context, p *auth.Principal, requestID string, in AccountInput) (AccountView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return AccountView{}, err
	}
	in.Label, in.IBANLast4 = strings.TrimSpace(in.Label), strings.ToUpper(strings.TrimSpace(in.IBANLast4))
	var f invariant.Fields
	if in.Label == "" || len(in.Label) > 100 {
		f.Add("label", "Un libellé de 1 à 100 caractères est obligatoire.")
	}
	if in.IBANLast4 != "" && !lastFour(in.IBANLast4) {
		f.Add("iban_last4", "Les quatre derniers caractères de l’IBAN, rien de plus.")
	}
	if err := f.Err(); err != nil {
		return AccountView{}, err
	}
	var out AccountView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		now := s.now()
		r := accountRow{ID: s.NewID(), OwnerID: sc.OwnerID, Label: in.Label, IBANLast4: in.IBANLast4, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		if err := s.Repo.InsertAccount(ctx, r); err != nil {
			return err
		}
		out = accountView(r)
		return audit.Record(ctx, "payment.bank_account", audit.Entity{Type: "bank_account", ID: r.ID.String()}, nil, out)
	})
	return out, err
}

func lastFour(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// Import reads a bank statement uploaded as CSV or OFX into an account
// (RENT-SPEC-013, Q-123): a statement the format or mapping cannot read
// whole imports nothing; a line the account already holds (same key) is
// counted, not added again. A dry run only reads it.
func (s Service) Import(ctx context.Context, p *auth.Principal, requestID string, in ImportInput) (ImportResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ImportResult{}, err
	}
	if in.Format != "csv" && in.Format != "ofx" {
		var f invariant.Fields
		f.Add("format", "Attendu : csv ou ofx.")
		return ImportResult{}, f
	}
	if in.Format == "csv" && in.Mapping == nil {
		var f invariant.Fields
		f.Add("mapping", "Un relevé CSV a besoin de son paramétrage.")
		return ImportResult{}, f
	}
	out := ImportResult{Lines: []BankLine{}}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if _, err := s.Repo.Account(ctx, in.AccountID); err != nil {
			return err
		}
		meta, data, err := s.Docs.ReadInTx(ctx, in.DocumentID, document.MaxUploadBytes)
		if errors.Is(err, invariant.ErrNotFound) {
			return invariant.New(TypeStatementFile, in.DocumentID.String())
		}
		if err != nil {
			return err
		}
		want := map[string]string{"csv": "text/csv", "ofx": "application/x-ofx"}[in.Format]
		if meta.Source != document.SourceUpload || meta.ContentType != want {
			return invariant.New(TypeStatementFile, meta.ContentType)
		}
		var lines []BankLine
		if in.Format == "csv" {
			lines, err = ParseCSV(data, *in.Mapping)
		} else {
			lines, err = ParseOFX(data)
		}
		if err != nil {
			return invariant.New(TypeStatementInvalid, err.Error())
		}
		keys := make([]string, len(lines))
		for i, l := range lines {
			keys[i] = l.Key
		}
		held, err := s.Repo.LineKeys(ctx, in.AccountID, keys)
		if err != nil {
			return err
		}
		now := s.now()
		var rows []lineRow
		for _, l := range lines {
			if held[l.Key] {
				out.Duplicates++
				continue
			}
			held[l.Key] = true
			out.Lines = append(out.Lines, l)
			rows = append(rows, lineRow{ID: s.NewID(), OwnerID: sc.OwnerID, AccountID: in.AccountID, BookedOn: l.BookedOn.Time(),
				AmountCents: int64(l.AmountCents), Label: l.Label, Reference: l.Reference, LineKey: l.Key, ImportDocumentID: in.DocumentID,
				Status: LineUnmatched, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1})
		}
		out.New = len(rows)
		if in.DryRun {
			return nil
		}
		if err := s.Repo.InsertLines(ctx, rows); err != nil {
			return err
		}
		return audit.Record(ctx, "payment.bank_import", audit.Entity{Type: "bank_account", ID: in.AccountID.String()}, nil,
			map[string]any{"document_id": in.DocumentID, "format": in.Format, "mapping": in.Mapping, "new": out.New, "duplicates": out.Duplicates})
	})
	return out, err
}

// Lines lists bank lines, the latest booked first, each flagged stale while
// unmatched past bank.reconciliation.unmatched_alert_days.
func (s Service) Lines(ctx context.Context, p *auth.Principal, requestID string, f LineFilter, offset, limit int) ([]LineView, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	out := []LineView{}
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		today := s.today()
		b, err := ps.Bank(today)
		if err != nil {
			return err
		}
		rows, n, err := s.Repo.Lines(ctx, f, offset, limit)
		if err != nil {
			return err
		}
		total = n
		for _, r := range rows {
			out = append(out, s.lineView(r, today, b.UnmatchedAlertDays))
		}
		return nil
	})
	return out, total, err
}

// SetLineStatus ignores an unmatched bank line or reopens an ignored one; a
// matched line is refused.
func (s Service) SetLineStatus(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in LineStatusInput) (LineView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return LineView{}, err
	}
	in.Note = strings.TrimSpace(in.Note)
	var f invariant.Fields
	if in.Status != LineUnmatched && in.Status != LineIgnored {
		f.Add("status", "Attendu : unmatched ou ignored.")
	}
	if len(in.Note) > 500 {
		f.Add("note", "Au plus 500 caractères.")
	}
	if err := f.Err(); err != nil {
		return LineView{}, err
	}
	var out LineView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Line(ctx, id)
		if err != nil {
			return err
		}
		if r.Status == LineMatched {
			return invariant.New(TypeLineState, id.String())
		}
		if r.Version != in.Version {
			return invariant.ErrStale
		}
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		today := s.today()
		b, err := ps.Bank(today)
		if err != nil {
			return err
		}
		before := s.lineView(r, today, b.UnmatchedAlertDays)
		if err := s.Repo.SetLineStatus(ctx, id, in.Version, in.Status, in.Note, s.now(), p.Sub); err != nil {
			return err
		}
		r.Status, r.Note, r.Version = in.Status, in.Note, r.Version+1
		out = s.lineView(r, today, b.UnmatchedAlertDays)
		return audit.Record(ctx, "payment.bank_line", audit.Entity{Type: "bank_line", ID: id.String()}, before, out)
	})
	return out, err
}
