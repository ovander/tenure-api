package charge

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ovander/tenure-api/internal/money"
)

// Mapping tells the syndic import which CSV columns hold what (RENT-SPEC-007
// step 1). Columns are named by their header.
type Mapping struct {
	// Delimiter is one character; ";" when empty (the usual French export).
	Delimiter string `json:"delimiter" example:";"`
	// DecimalPoint reads amounts as 1234.56; by default they read 1 234,56.
	DecimalPoint bool   `json:"decimal_point"`
	Label        string `json:"label" example:"Libellé"`
	Amount       string `json:"amount" example:"Montant"`
	// Recoverable is the syndic's recoverable amount of the line (décret
	// 87-713); a line with an empty cell is recoverable for nothing.
	Recoverable string `json:"recoverable" example:"Récupérable"`
}

// SyndicLine is one line of a syndic statement.
type SyndicLine struct {
	Row              int         `json:"row"`
	Label            string      `json:"label"`
	AmountCents      money.Cents `json:"amount_cents"`
	RecoverableCents money.Cents `json:"recoverable_cents"`
}

// ErrSyndicFile reports a statement the mapping cannot read; its message names
// the row and the column.
var ErrSyndicFile = errors.New("charge: syndic statement")

// ParseCents reads a French or plain amount as cents (money.Parse).
func ParseCents(s string, decimalPoint bool) (money.Cents, error) {
	return money.Parse(s, decimalPoint)
}

// ParseSyndic reads a syndic statement exported as CSV with a header row
// (UTF-8, an optional BOM): one line per row with a label or an amount. A
// negative amount, or a recoverable part above the amount, is refused with
// its row number, so nothing is imported from a statement it cannot read
// whole.
func ParseSyndic(data []byte, m Mapping) ([]SyndicLine, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: not UTF-8", ErrSyndicFile)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = ';'
	if m.Delimiter != "" {
		d, size := utf8.DecodeRuneInString(m.Delimiter)
		if size != len(m.Delimiter) {
			return nil, fmt.Errorf("%w: the delimiter is one character", ErrSyndicFile)
		}
		r.Comma = d
	}
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: header: %v", ErrSyndicFile, err)
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
			return 0, fmt.Errorf("%w: no column %q", ErrSyndicFile, name)
		}
		return i, nil
	}
	li, err := idx(m.Label, true)
	if err != nil {
		return nil, err
	}
	ai, err := idx(m.Amount, true)
	if err != nil {
		return nil, err
	}
	ri, err := idx(m.Recoverable, false)
	if err != nil {
		return nil, err
	}
	cell := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	var out []SyndicLine
	for row := 2; ; row++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: row %d: %v", ErrSyndicFile, row, err)
		}
		label, amountText := cell(rec, li), cell(rec, ai)
		if label == "" && amountText == "" {
			continue
		}
		amount, err := ParseCents(amountText, m.DecimalPoint)
		if err != nil {
			return nil, fmt.Errorf("%w: row %d: %v", ErrSyndicFile, row, err)
		}
		rec2, err := ParseCents(cell(rec, ri), m.DecimalPoint)
		if err != nil {
			return nil, fmt.Errorf("%w: row %d: %v", ErrSyndicFile, row, err)
		}
		switch {
		case amount < 0:
			return nil, fmt.Errorf("%w: row %d: a negative amount is a correction to enter by hand", ErrSyndicFile, row)
		case rec2 < 0 || rec2 > amount:
			return nil, fmt.Errorf("%w: row %d: the recoverable part is outside the amount", ErrSyndicFile, row)
		}
		out = append(out, SyndicLine{Row: row, Label: label, AmountCents: amount, RecoverableCents: rec2})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no line", ErrSyndicFile)
	}
	return out, nil
}
