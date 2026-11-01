package params

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/period"
)

// INSEE fetches the IRL series from the INSEE BDM SDMX service (RENT-SPEC-006
// external data). Only the observations matter: each <Obs> element carries
// TIME_PERIOD ("2025-Q2") and OBS_VALUE ("146.68").
type INSEE struct {
	URL    string
	Client *http.Client
}

var quarterRe = regexp.MustCompile(`^[0-9]{4}-Q[1-4]$`)

// Hundredths parses a decimal index such as "146.68" into hundredths exactly.
func Hundredths(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("params: index %q has more than two decimals", s)
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("params: index %q: %w", s, err)
	}
	f := int64(0)
	if frac != "" {
		f, err = strconv.ParseInt((frac + "0")[:2], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("params: index %q: %w", s, err)
		}
	}
	if w <= 0 {
		return 0, fmt.Errorf("params: index %q is not positive", s)
	}
	return w*100 + f, nil
}

// ParseSDMX extracts the quarterly observations of an SDMX document.
func ParseSDMX(r io.Reader) (map[string]int64, error) {
	dec := xml.NewDecoder(r)
	out := map[string]int64{}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("params: sdmx: %w", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok || el.Name.Local != "Obs" {
			continue
		}
		var q, v string
		for _, a := range el.Attr {
			switch a.Name.Local {
			case "TIME_PERIOD":
				q = a.Value
			case "OBS_VALUE":
				v = a.Value
			}
		}
		if !quarterRe.MatchString(q) || v == "" {
			continue
		}
		n, err := Hundredths(v)
		if err != nil {
			return nil, err
		}
		out[q] = n
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("params: sdmx document holds no quarterly observation")
	}
	return out, nil
}

// Fetch returns the published series.
func (s INSEE) Fetch(ctx context.Context) (map[string]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/xml")
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("params: insee: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("params: insee: status %d", res.StatusCode)
	}
	return ParseSDMX(io.LimitReader(res.Body, 10<<20))
}

// EstimatedPublication is the publication date recorded for an IRL value
// fetched from INSEE, whose SDMX series gives none: the 15th of the month after
// the quarter, INSEE's usual calendar, and never after today (Q-55).
func EstimatedPublication(quarter string, today period.Date) period.Date {
	y, _ := strconv.Atoi(quarter[:4])
	q, _ := strconv.Atoi(quarter[6:])
	d := period.Date{Year: y, Month: time.Month(3*q + 1), Day: 15}
	if q == 4 {
		d = period.Date{Year: y + 1, Month: time.January, Day: 15}
	}
	if d.After(today) {
		return today
	}
	return d
}

// InsertIRL caches an index value; an existing quarter is left unchanged and
// reported as not inserted (a correction is a reviewed override, Q-53).
func (Repository) InsertIRL(ctx context.Context, quarter string, value int64, published time.Time, source string) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	res := tx.Exec(`INSERT INTO irl_index (quarter, value, published_on, source) VALUES (?, ?, ?, ?) ON CONFLICT (quarter) DO NOTHING`,
		quarter, value, published, source)
	return res.RowsAffected == 1, res.Error
}
