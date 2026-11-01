package db

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

// Hundredths maps a numeric(…, 2) column to an integer number of hundredths
// (45,30 m² → 4530; a 50,00 % share → 5000 bp), so no float touches a surface
// or a share. A NULL column scans as 0.
type Hundredths int64

// Value implements driver.Valuer.
func (h Hundredths) Value() (driver.Value, error) {
	sign := ""
	v := int64(h)
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100), nil
}

// Scan implements sql.Scanner.
func (h *Hundredths) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*h = 0
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	case int64:
		*h = Hundredths(v * 100)
		return nil
	default:
		return fmt.Errorf("db: cannot scan %T into Hundredths", src)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	frac = (frac + "00")[:2]
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("db: invalid numeric %q", s)
	}
	n := w*100 + f
	if neg {
		n = -n
	}
	*h = Hundredths(n)
	return nil
}

// TextArray maps a text[] column.
type TextArray []string

// Value implements driver.Valuer with a PostgreSQL array literal.
func (a TextArray) Value() (driver.Value, error) {
	if a == nil {
		return "{}", nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range a {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Scan implements sql.Scanner for a one-dimensional text array literal.
func (a *TextArray) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("db: cannot scan %T into TextArray", src)
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return fmt.Errorf("db: invalid array %q", s)
	}
	s = s[1 : len(s)-1]
	out := []string{}
	for len(s) > 0 {
		var item strings.Builder
		if s[0] == '"' {
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				item.WriteByte(s[i])
			}
			s = s[min(i+1, len(s)):]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			item.WriteString(s[:end])
			s = s[end:]
		}
		out = append(out, item.String())
		s = strings.TrimPrefix(s, ",")
	}
	*a = out
	return nil
}
