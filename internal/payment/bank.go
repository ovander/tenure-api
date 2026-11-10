package payment

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// BankLine is one line of a bank statement as read (RENT-SPEC-013): a credit
// is positive, a debit negative. Key identifies it in its account, so a
// statement imported twice adds nothing (Q-123).
type BankLine struct {
	Row         int         `json:"row"`
	BookedOn    period.Date `json:"booked_on" swaggertype:"string" format:"date"`
	AmountCents money.Cents `json:"amount_cents"`
	Label       string      `json:"label"`
	Reference   string      `json:"reference,omitempty"`
	Key         string      `json:"key"`
}

// CSVMapping tells the CSV import which columns hold what, by header. The
// amount is one signed column, or a debit and a credit column.
type CSVMapping struct {
	// Delimiter is one character; ";" when empty (the usual French export).
	Delimiter string `json:"delimiter" example:";"`
	// DecimalPoint reads amounts as 1234.56; by default they read 1 234,56.
	DecimalPoint bool   `json:"decimal_point"`
	Date         string `json:"date" example:"Date"`
	// DateFormat is a Go layout; 02/01/2006 when empty.
	DateFormat string `json:"date_format,omitempty" example:"02/01/2006"`
	Amount     string `json:"amount,omitempty" example:"Montant"`
	Debit      string `json:"debit,omitempty" example:"Débit"`
	Credit     string `json:"credit,omitempty" example:"Crédit"`
	Label      string `json:"label" example:"Libellé"`
	Reference  string `json:"reference,omitempty"`
}

// ErrStatement reports a statement that cannot be read whole; its message
// names the row or the element.
var ErrStatement = errors.New("payment: bank statement")

// text decodes a statement: UTF-8 (an optional BOM dropped), or else
// Windows-1252, which French banks still export.
func text(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data)
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(data)
	if err != nil {
		return string(bytes.ToValidUTF8(data, []byte("?")))
	}
	return string(out)
}

// keys gives each line its key: the bank's own id when it has one (OFX
// FITID), else a hash of its day, amount and label with its rank among the
// identical lines of the statement, so two equal lines on one day stay two.
func keys(lines []BankLine, ids []string) {
	seen := map[string]int{}
	for i := range lines {
		if ids != nil && ids[i] != "" {
			lines[i].Key = "fitid:" + ids[i]
			continue
		}
		base := fmt.Sprintf("%s|%d|%s", lines[i].BookedOn, lines[i].AmountCents, strings.ToUpper(strings.Join(strings.Fields(lines[i].Label), " ")))
		seen[base]++
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", base, seen[base])))
		lines[i].Key = "hash:" + hex.EncodeToString(sum[:16])
	}
}

// ParseCSV reads a bank statement exported as CSV with a header row. A row
// with neither a date nor an amount is skipped (a bank's title or total
// line); any other row the mapping cannot read refuses the whole statement,
// with its row number. Pure.
func ParseCSV(data []byte, m CSVMapping) ([]BankLine, error) {
	r := csv.NewReader(strings.NewReader(text(data)))
	r.Comma = ';'
	if m.Delimiter != "" {
		d, size := utf8.DecodeRuneInString(m.Delimiter)
		if size != len(m.Delimiter) {
			return nil, fmt.Errorf("%w: the delimiter is one character", ErrStatement)
		}
		r.Comma = d
	}
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: header: %v", ErrStatement, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	idx := func(name string, required bool) (int, error) {
		if name == "" && !required {
			return -1, nil
		}
		i, ok := col[name]
		if !ok {
			return 0, fmt.Errorf("%w: no column %q", ErrStatement, name)
		}
		return i, nil
	}
	if (m.Amount == "") == (m.Debit == "" && m.Credit == "") {
		return nil, fmt.Errorf("%w: map the amount, or the debit and credit columns", ErrStatement)
	}
	di, err := idx(m.Date, true)
	if err != nil {
		return nil, err
	}
	li, err := idx(m.Label, true)
	if err != nil {
		return nil, err
	}
	ai, err := idx(m.Amount, false)
	if err != nil {
		return nil, err
	}
	dbi, err := idx(m.Debit, false)
	if err != nil {
		return nil, err
	}
	cri, err := idx(m.Credit, false)
	if err != nil {
		return nil, err
	}
	ri, err := idx(m.Reference, false)
	if err != nil {
		return nil, err
	}
	layout := m.DateFormat
	if layout == "" {
		layout = "02/01/2006"
	}
	cell := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	var out []BankLine
	for row := 2; ; row++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: row %d: %v", ErrStatement, row, err)
		}
		ds := cell(rec, di)
		var amount money.Cents
		if ai >= 0 {
			if amount, err = money.Parse(cell(rec, ai), m.DecimalPoint); err != nil {
				return nil, fmt.Errorf("%w: row %d: %v", ErrStatement, row, err)
			}
		} else {
			debit, err := money.Parse(cell(rec, dbi), m.DecimalPoint)
			if err != nil {
				return nil, fmt.Errorf("%w: row %d: %v", ErrStatement, row, err)
			}
			credit, err := money.Parse(cell(rec, cri), m.DecimalPoint)
			if err != nil {
				return nil, fmt.Errorf("%w: row %d: %v", ErrStatement, row, err)
			}
			amount = abs(credit) - abs(debit)
		}
		if ds == "" && amount == 0 {
			continue
		}
		t, err := time.Parse(layout, ds)
		if err != nil {
			return nil, fmt.Errorf("%w: row %d: date %q", ErrStatement, row, ds)
		}
		if amount == 0 {
			return nil, fmt.Errorf("%w: row %d: no amount", ErrStatement, row)
		}
		out = append(out, BankLine{Row: row, BookedOn: period.FromTime(t), AmountCents: amount, Label: cell(rec, li), Reference: cell(rec, ri)})
	}
	keys(out, nil)
	return out, nil
}

func abs(c money.Cents) money.Cents {
	if c < 0 {
		return -c
	}
	return c
}

var (
	ofxTrn = regexp.MustCompile(`(?is)<STMTTRN>(.*?)(?:</STMTTRN>|<STMTTRN>|</BANKTRANLIST>)`)
	ofxTag = func(tag string) *regexp.Regexp { return regexp.MustCompile(`(?i)<` + tag + `>\s*([^<\r\n]*)`) }
	ofxDT  = ofxTag("DTPOSTED")
	ofxAmt = ofxTag("TRNAMT")
	ofxID  = ofxTag("FITID")
	ofxNm  = ofxTag("NAME")
	ofxMm  = ofxTag("MEMO")
	ofxRef = ofxTag("CHECKNUM")
)

// ParseOFX reads a bank statement in OFX, version 1 (SGML, leaf elements not
// closed) or 2 (XML): one line per STMTTRN, its day from DTPOSTED, its
// signed amount from TRNAMT, its label from NAME and MEMO, its key from
// FITID. A transaction it cannot read refuses the whole statement. Pure.
func ParseOFX(data []byte) ([]BankLine, error) {
	s := text(data)
	blocks := ofxTrn.FindAllStringSubmatchIndex(s, -1)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%w: no STMTTRN", ErrStatement)
	}
	get := func(re *regexp.Regexp, b string) string {
		if m := re.FindStringSubmatch(b); m != nil {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	out := make([]BankLine, 0, len(blocks))
	ids := make([]string, 0, len(blocks))
	for n, ix := range blocks {
		b := s[ix[2]:ix[3]]
		dt := get(ofxDT, b)
		if len(dt) < 8 {
			return nil, fmt.Errorf("%w: transaction %d: DTPOSTED %q", ErrStatement, n+1, dt)
		}
		t, err := time.Parse("20060102", dt[:8])
		if err != nil {
			return nil, fmt.Errorf("%w: transaction %d: DTPOSTED %q", ErrStatement, n+1, dt)
		}
		raw := get(ofxAmt, b)
		amount, err := money.Parse(strings.ReplaceAll(raw, ",", "."), true)
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("%w: transaction %d: TRNAMT %q", ErrStatement, n+1, raw)
		}
		label := strings.TrimSpace(strings.Join([]string{get(ofxNm, b), get(ofxMm, b)}, " "))
		out = append(out, BankLine{Row: n + 1, BookedOn: period.FromTime(t), AmountCents: amount, Label: label, Reference: get(ofxRef, b)})
		ids = append(ids, get(ofxID, b))
	}
	keys(out, ids)
	return out, nil
}
