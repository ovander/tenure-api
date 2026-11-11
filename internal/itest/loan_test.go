//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/tax"
)

const (
	subLoan  = "9975" // owner of the loans scenario (RENT-SPEC-014, part 15a)
	subLoanX = "9976"
)

type loanView struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	TermMonths    int    `json:"term_months"`
	InterestCents int64  `json:"interest_cents"`
	Version       int    `json:"version"`
	Years         []struct {
		Year           int   `json:"year"`
		Instalments    int   `json:"instalments"`
		InterestCents  int64 `json:"interest_cents"`
		InsuranceCents int64 `json:"insurance_cents"`
		RemainingCents int64 `json:"remaining_cents"`
	} `json:"years"`
	Instalments []struct {
		Seq            int   `json:"seq"`
		PrincipalCents int64 `json:"principal_cents"`
		InterestCents  int64 `json:"interest_cents"`
		RemainingCents int64 `json:"remaining_cents"`
	} `json:"instalments"`
}

// TestLoans records a loan computed from its terms, changes it into the
// bank's typed-in table, and reads its 2025 interest and insurance on line
// 250 of the tax summary, the interest typed in by hand set aside (Q-126);
// another owner reads none of its rows.
func TestLoans(t *testing.T) {
	tok, tokX := token(t, subLoan), token(t, subLoanX)
	fresh := func() string { return token(t, subLoan, stepUp(time.Minute)) }
	var owner, other, a idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Emprunt", "address": "3 rue E, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokX, body: map[string]any{"legal_name": "Bailleur X", "address": "4 rue X"}}).json(t, &other)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "3 rue E, Lyon"}}).json(t, &a)

	computed := map[string]any{"asset_id": a.ID, "label": "Prêt bancaire", "lender": "Banque Exemple", "principal_cents": 12000000,
		"annual_rate_ppm": 30000, "term_months": 12, "first_due_on": "2025-01-15", "insurance_cents": 1000}
	var l loanView
	audited(t, seen, "POST /loans", 201, func() resp {
		return call(t, "POST", "/loans", req{tok: tok, body: computed})
	}).json(t, &l)
	if l.Source != "computed" || len(l.Instalments) != 12 || l.Instalments[0].InterestCents != 30000 || l.Instalments[11].RemainingCents != 0 {
		t.Fatalf("computed loan %+v", l)
	}
	if len(l.Years) != 1 || l.Years[0].Year != 2025 || l.Years[0].InsuranceCents != 12000 || l.Years[0].InterestCents != l.InterestCents {
		t.Errorf("years %+v", l.Years)
	}
	var preview loanView
	call(t, "GET", "/loans/schedule?principal_cents=12000000&annual_rate_ppm=30000&term_months=12&first_due_on=2025-01-15&insurance_cents=1000",
		req{tok: tok}).json(t, &preview)
	if preview.InterestCents != l.InterestCents || len(preview.Instalments) != 12 {
		t.Errorf("preview %+v", preview)
	}
	if r := call(t, "POST", "/loans", req{tok: tokX, body: computed}); r.Status != 422 {
		t.Errorf("another owner's asset: %d %s", r.Status, r.Body)
	}
	short := map[string]any{"asset_id": a.ID, "label": "Prêt", "principal_cents": 100000,
		"instalments": []map[string]any{{"due_on": "2025-02-01", "principal_cents": 60000, "interest_cents": 100}}}
	if r := call(t, "POST", "/loans", req{tok: tok, body: short}); r.problemType(t) != "loan/schedule-invalid" {
		t.Errorf("a table short of the principal: %d %s", r.Status, r.Body)
	}

	typed := map[string]any{"asset_id": a.ID, "label": "Prêt bancaire", "principal_cents": 12000000,
		"instalments": []map[string]any{
			{"due_on": "2025-06-15", "principal_cents": 0, "interest_cents": 30000, "insurance_cents": 1000},
			{"due_on": "2025-12-15", "principal_cents": 6000000, "interest_cents": 30000, "insurance_cents": 1000},
			{"due_on": "2026-06-15", "principal_cents": 6000000, "interest_cents": 15000, "insurance_cents": 1000},
		}}
	if r := call(t, "PUT", "/loans/"+l.ID, req{tok: tok, ifMatch: etag(2), body: typed}); r.Status != 412 {
		t.Errorf("stale version: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "PUT /loans/{id}", 200, func() resp {
		return call(t, "PUT", "/loans/"+l.ID, req{tok: tok, ifMatch: etag(1), body: typed})
	}).json(t, &l)
	if l.Source != "imported" || l.TermMonths != 3 || l.Version != 2 || len(l.Instalments) != 3 || l.Instalments[1].RemainingCents != 6000000 {
		t.Fatalf("typed loan %+v", l)
	}
	if n := superCount(t, "SELECT count(*) FROM loan_instalments WHERE loan_id = $1", l.ID); n != 3 {
		t.Errorf("instalments stored: %d", n)
	}
	var list []loanView
	call(t, "GET", "/loans?asset_id="+a.ID, req{tok: tok}).json(t, &list)
	if len(list) != 1 || list[0].ID != l.ID || len(list[0].Instalments) != 0 || len(list[0].Years) != 2 {
		t.Errorf("list %+v", list)
	}

	// Interest typed in by hand for the same asset and year is set aside.
	if r := call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a.ID, "charged_on": "2025-12-31", "paid_on": "2025-12-31",
		"category": "loan_interest", "amount_cents": 99900, "recoverable_cents": 0}}); r.Status != 201 {
		t.Fatalf("charge: %d %s", r.Status, r.Body)
	}
	var draft tax.View
	audited(t, seen, "POST /tax/summaries", 200, func() resp {
		return call(t, "POST", "/tax/summaries", req{tok: fresh(), body: map[string]any{"year": 2025}})
	}).json(t, &draft)
	if len(draft.Summary.Owners) != 1 {
		t.Fatalf("draft %+v", draft)
	}
	var on250 int64
	var sources []string
	for _, x := range draft.Summary.Owners[0].Totals {
		if x.Line == "250" {
			on250, sources = int64(x.AmountCents), x.Sources
		}
	}
	// 2025: interest 300 + 300 €, insurance 10 + 10 €.
	if on250 != 62000 || len(sources) != 4 {
		t.Errorf("line 250 %d from %v", on250, sources)
	}
	superseded := 0
	for _, f := range draft.Summary.Flags {
		if f.Kind == tax.FlagSupersededByLoan {
			superseded++
		}
	}
	if superseded != 1 || draft.Inputs.LoanInstalments != 2 {
		t.Errorf("flags %+v inputs %+v", draft.Summary.Flags, draft.Inputs)
	}

	rlsZeroRows(t, owner.ID, other.ID, subLoan, subLoanX, a.ID, map[string]string{"loans": "owner_id = $1", "loan_instalments": "owner_id = $1"})
}
