// Package document produces, numbers, archives and serves documents
// (RENT-SPEC-005, RENT-BE-001 §6): a document is rendered from a versioned
// French template, converted to PDF/A-2b by the PDF engine, written once to
// the object store under compliance lock with its SHA-256 verified, and
// recorded with its metadata in the same transaction as its number.
package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/templates"
)

var frMonths = [...]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août",
	"septembre", "octobre", "novembre", "décembre"}

// Euros formats cents the French way: « 1 234,56 € » (narrow no-break space
// between thousands, no-break space before the sign, « − » for a negative).
func Euros(c money.Cents) string {
	neg := c < 0
	if neg {
		c = -c
	}
	whole, frac := int64(c)/100, int64(c)%100
	s := fmt.Sprint(whole)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	out := fmt.Sprintf("%s,%02d €", b.String(), frac)
	if neg {
		out = "−" + out
	}
	return out
}

// DateFR formats a date as 02/01/2006.
func DateFR(d period.Date) string { return fmt.Sprintf("%02d/%02d/%04d", d.Day, int(d.Month), d.Year) }

// PeriodFR formats a period as « mars 2025 ».
func PeriodFR(p period.Period) string { return fmt.Sprintf("%s %d", frMonths[p.Month-1], p.Year) }

// Index100 formats an index in hundredths as « 146,68 ».
func Index100(v int64) string { return fmt.Sprintf("%d,%02d", v/100, v%100) }

var funcs = template.FuncMap{
	"euros":       Euros,
	"date":        DateFR,
	"periodLabel": PeriodFR,
	"index100":    Index100,
}

// Renderer renders the embedded templates.
type Renderer struct{}

// Footer is the data of the page footer: the number and the fingerprint of
// the input snapshot (Q-46: a PDF cannot carry its own hash).
type Footer struct {
	Number       string
	SnapshotHash string
}

// Render returns the HTML of class's template version with data, and its
// footer.
func (Renderer) Render(class Class, version string, data any, f Footer) (html, footer []byte, err error) {
	t, err := template.New("document.html").Funcs(funcs).ParseFS(templates.FS,
		string(class)+"/"+version+"/document.html", "shared/v1/style.html")
	if err != nil {
		return nil, nil, fmt.Errorf("document: template %s/%s: %w", class, version, err)
	}
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "document.html", data); err != nil {
		return nil, nil, fmt.Errorf("document: render %s: %w", class, err)
	}
	ft, err := template.ParseFS(templates.FS, "shared/v1/footer.html")
	if err != nil {
		return nil, nil, err
	}
	var fb bytes.Buffer
	if err := ft.Execute(&fb, f); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), fb.Bytes(), nil
}

// SnapshotHash returns the hex SHA-256 of the canonical JSON of the input
// snapshot; its first 16 characters are printed in the footer.
func SnapshotHash(snapshot any) (string, error) {
	b, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// SHA256 returns the hex SHA-256 of b.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RetainUntil returns the end of retention of a document issued on issued:
// years later, the same day (RENT-SPEC-005).
func RetainUntil(issued time.Time, years int64) period.Date {
	return period.FromTime(issued).AddMonths(int(12 * years))
}
