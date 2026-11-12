package ledger

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// Journal labels of the FEC.
var journalLabels = map[string]string{JournalBank: "Banque", JournalGeneral: "Opérations diverses"}

// CSV writes the journal, one row per line of each entry, numbered in order
// (amounts in cents, as every export of the tool).
func CSV(entries []Entry) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"entry", "date", "journal", "kind", "source", "label", "account", "account_label", "debit_cents", "credit_cents", "document_id"})
	for n, e := range entries {
		for _, l := range e.Lines {
			_ = w.Write([]string{strconv.Itoa(n + 1), e.Date.String(), e.Journal, e.Kind, e.Source, e.Label, l.Account, l.Label,
				strconv.FormatInt(int64(l.DebitCents), 10), strconv.FormatInt(int64(l.CreditCents), 10), e.DocumentID})
		}
	}
	w.Flush()
	return b.Bytes()
}

// BalancesCSV writes each account's movements over the journal.
func BalancesCSV(balances []Balance) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"account", "label", "debit_cents", "credit_cents", "balance_cents"})
	for _, x := range balances {
		_ = w.Write([]string{x.Account, x.Label, strconv.FormatInt(int64(x.DebitCents), 10), strconv.FormatInt(int64(x.CreditCents), 10),
			strconv.FormatInt(int64(x.BalanceCents), 10)})
	}
	w.Flush()
	return b.Bytes()
}

// fecHeader is the eighteen fields of the fichier des écritures comptables,
// in their order (LPF art. A47 A-1).
var fecHeader = []string{"JournalCode", "JournalLib", "EcritureNum", "EcritureDate", "CompteNum", "CompteLib", "CompAuxNum",
	"CompAuxLib", "PieceRef", "PieceDate", "EcritureLib", "Debit", "Credit", "EcritureLet", "DateLet", "ValidDate",
	"Montantdevise", "Idevise"}

func fecDate(d period.Date) string { return fmt.Sprintf("%04d%02d%02d", d.Year, int(d.Month), d.Day) }

// fecAmount writes cents as euros with a decimal comma and no thousands
// separator.
func fecAmount(c money.Cents) string {
	neg := c < 0
	if neg {
		c = -c
	}
	s := fmt.Sprintf("%d,%02d", c/100, c%100)
	if neg {
		return "-" + s
	}
	return s
}

// fecText keeps a field on one line, without the separator, in characters
// ISO 8859-15 can hold.
var fecText = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ", "’", "'", "‘", "'", "“", "\"", "”", "\"", "…", "...", "–", "-", "—", "-", " ", " ", " ", " ")

// FEC writes the journal as a fichier des écritures comptables (LPF art.
// A47 A-1, Q-128): a header and one row per line, its eighteen fields
// separated by tabs, dates as AAAAMMJJ, amounts with a decimal comma,
// encoded in ISO 8859-15. Entries are numbered in one continuous sequence in
// date order; the piece is the movement's source; every entry is validated
// on validOn, the day the year was closed. There is no auxiliary account
// (tenants are not followed one by one yet) and no foreign currency.
func FEC(entries []Entry, validOn period.Date) ([]byte, error) {
	if validOn.IsZero() {
		return nil, fmt.Errorf("ledger: a FEC needs the closing day")
	}
	rows := [][]string{fecHeader}
	for n, e := range entries {
		for _, l := range e.Lines {
			rows = append(rows, []string{e.Journal, journalLabels[e.Journal], strconv.Itoa(n + 1), fecDate(e.Date), l.Account, l.Label, "", "",
				e.Source, fecDate(e.Date), e.Label, fecAmount(l.DebitCents), fecAmount(l.CreditCents), "", "", fecDate(validOn), "", ""})
		}
	}
	var b strings.Builder
	for _, r := range rows {
		for i := range r {
			r[i] = fecText.Replace(r[i])
		}
		b.WriteString(strings.Join(r, "\t"))
		b.WriteString("\r\n")
	}
	out, err := charmap.ISO8859_15.NewEncoder().String(b.String())
	if err != nil {
		return nil, fmt.Errorf("ledger: FEC encoding: %w", err)
	}
	return []byte(out), nil
}

// FECName is the FEC's file name: FEC and the closing day of the year
// (LPF art. A47 A-1 puts the SIREN first; a private owner has none, Q-128).
func FECName(year int) string { return fmt.Sprintf("FEC%04d1231.txt", year) }

// Piece is a supporting document behind entries of the journal.
type Piece struct {
	DocumentID string   `json:"document_id"`
	Sources    []string `json:"sources"`
}

// Pieces lists the supporting documents behind the entries, by document id,
// each with the movements it supports.
func Pieces(entries []Entry, extra map[string]string) []Piece {
	by := map[string][]string{}
	for _, e := range entries {
		if e.DocumentID != "" && !contains(by[e.DocumentID], e.Source) {
			by[e.DocumentID] = append(by[e.DocumentID], e.Source)
		}
	}
	for id, src := range extra {
		if !contains(by[id], src) {
			by[id] = append(by[id], src)
		}
	}
	out := make([]Piece, 0, len(by))
	for id, src := range by {
		sort.Strings(src)
		out = append(out, Piece{DocumentID: id, Sources: src})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].DocumentID < out[b].DocumentID })
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// PackFile is one file of the year-end pack.
type PackFile struct {
	Name string
	Data []byte
}

// Pack zips the year-end pack's files in their order, every file dated
// modified so the same year packs to the same bytes.
func Pack(files []PackFile, modified time.Time) ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, f := range files {
		w, err := z.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: modified.UTC()})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
