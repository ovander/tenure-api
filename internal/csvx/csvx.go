// Package csvx writes the CSV exports a person opens in a spreadsheet
// (threat model T15, A-2): a text cell that a spreadsheet would read as a
// formula is written with a leading apostrophe, so that a tenant's name or a
// syndic's label such as =HYPERLINK(…) stays text on the accountant's
// machine. Integers, negative amounts included, are written as they are. The
// FEC is not written here: its format is fixed by LPF art. A47 A-1.
package csvx

import (
	"encoding/csv"
	"io"
	"strconv"
)

// Writer is a csv.Writer whose cells are neutralised by Cell.
type Writer struct{ w *csv.Writer }

// NewWriter returns a Writer writing to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: csv.NewWriter(w)} }

// Write writes one record, each cell through Cell.
func (w *Writer) Write(record []string) error {
	out := make([]string, len(record))
	for i, c := range record {
		out[i] = Cell(c)
	}
	return w.w.Write(out)
}

// Flush writes any buffered data.
func (w *Writer) Flush() { w.w.Flush() }

// Error reports an error of a previous Write or Flush.
func (w *Writer) Error() error { return w.w.Error() }

// Cell returns c, prefixed with an apostrophe when it starts with a
// character a spreadsheet takes as the start of a formula (=, +, -, @, tab,
// carriage return) and is not an integer.
func Cell(c string) string {
	if c == "" {
		return c
	}
	switch c[0] {
	case '=', '+', '-', '@', '\t', '\r':
		if _, err := strconv.ParseInt(c, 10, 64); err == nil {
			return c
		}
		return "'" + c
	}
	return c
}
