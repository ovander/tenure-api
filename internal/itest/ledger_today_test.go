//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/period"
)

// TestLedgerInstalmentsToDate: the journal of the current year posts the
// loan instalments due up to today, not those still to come (#258).
func TestLedgerInstalmentsToDate(t *testing.T) {
	tok := token(t, "itest-ledger-today")
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Échéances", "address": "4 rue E, Lyon"}})
	var a idv
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "4 rue E, Lyon"}}).json(t, &a)
	now := today()
	first := period.NewDate(now.Year, time.January, 1)
	if r := call(t, "POST", "/loans", req{tok: tok, body: map[string]any{"asset_id": a.ID, "label": "Prêt fictif", "principal_cents": 1200000,
		"annual_rate_ppm": 24000, "term_months": 24, "first_due_on": first.String()}}); r.Status != 201 {
		t.Fatalf("loan: %d %s", r.Status, r.Body)
	}
	var j ledger.View
	call(t, "GET", "/ledger?year="+first.String()[:4], req{tok: tok}).json(t, &j)
	n := 0
	for _, e := range j.Entries {
		if e.Kind != "instalment" {
			continue
		}
		n++
		if now.Before(e.Date) {
			t.Errorf("instalment of %s posted on %s", e.Date, now)
		}
	}
	if want := int(now.Month); n != want {
		t.Errorf("%d instalments posted; want %d (due on the 1st, up to %s)", n, want, now)
	}
}
