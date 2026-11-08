//go:build integration

package itest

import "testing"

const (
	subTerms  = "9961" // owner of the terms and vacancy scenario (RENT-SPEC-010, part 12c)
	subTermsX = "9962"
)

// TestTermsAndVacancy reads a lease's terms and an asset's vacancy (Q-119):
// a furnished lease of one year renewed tacitly, then ended; a
// second lease after a gap, still running; a draft occupies nothing; the
// vacancy runs from the acquisition; another owner reads nothing.
func TestTermsAndVacancy(t *testing.T) {
	tok := token(t, subTerms)
	var owner, ten, a, l1, l2, l3 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Termes", "address": "8 rue T, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Paul Durand"}, "email": "paul@example.test"}}).json(t, &ten)
	acquired := today().AddMonthsClamped(-32)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "8 rue T, Lyon", "furnished": true, "acquired_on": acquired.String()}}).json(t, &a)
	start1, end1 := today().AddMonthsClamped(-30), today().AddMonthsClamped(-6)
	start2 := today().AddMonthsClamped(-4)
	for _, x := range []struct {
		into  *idv
		start string
	}{{&l1, start1.String()}, {&l2, start2.String()}, {&l3, today().AddMonthsClamped(1).String()}} {
		r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "furnished", "asset_ids": []string{a.ID},
			"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": x.start, "term_months": 12, "rent_cents": 70000,
			"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}})
		if r.Status != 201 {
			t.Fatalf("lease: %d %s", r.Status, r.Body)
		}
		r.json(t, x.into)
	}
	superExec(t, `UPDATE leases SET state = 'ended', end_on = $2 WHERE id = $1`, l1.ID, end1.Time())
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, l2.ID)

	var terms struct {
		Terms []struct {
			Renewal    string `json:"renewal"`
			Status     string `json:"status"`
			EndOn      string `json:"end_on"`
			EndedEarly bool   `json:"ended_early"`
		} `json:"terms"`
		Renews        bool   `json:"renews"`
		NextRenewalOn string `json:"next_renewal_on"`
	}
	call(t, "GET", "/leases/"+l1.ID+"/terms", req{tok: tok}).json(t, &terms)
	if len(terms.Terms) != 3 || terms.Terms[2].Renewal != "tacit" || !terms.Terms[2].EndedEarly || terms.Terms[2].EndOn != end1.String() || terms.NextRenewalOn != "" {
		t.Errorf("ended lease terms %+v", terms)
	}
	call(t, "GET", "/leases/"+l2.ID+"/terms", req{tok: tok}).json(t, &terms)
	if len(terms.Terms) != 1 || terms.Terms[0].Status != "current" || terms.NextRenewalOn != start2.AddMonthsClamped(12).String() {
		t.Errorf("running lease terms %+v", terms)
	}

	var vac struct {
		Since   string `json:"since"`
		Periods []struct {
			FromOn string `json:"from_on"`
			ToOn   string `json:"to_on"`
			Open   bool   `json:"open"`
		} `json:"periods"`
		DaysByYear map[string]int `json:"days_by_year"`
	}
	call(t, "GET", "/assets/"+a.ID+"/vacancies", req{tok: tok}).json(t, &vac)
	if vac.Since != acquired.String() || len(vac.Periods) != 2 || vac.Periods[0].FromOn != acquired.String() ||
		vac.Periods[1].FromOn != end1.AddDays(1).String() || vac.Periods[1].ToOn != start2.AddDays(-1).String() || vac.Periods[1].Open {
		t.Errorf("vacancy %+v", vac)
	}
	total := 0
	for _, n := range vac.DaysByYear {
		total += n
	}
	if total == 0 {
		t.Errorf("vacant days %v", vac.DaysByYear)
	}

	var other idv
	call(t, "PUT", "/me", req{tok: token(t, subTermsX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
	if r := call(t, "GET", "/assets/"+a.ID+"/vacancies", req{tok: token(t, subTermsX)}); r.Status != 404 {
		t.Errorf("another owner reads the vacancy: %d", r.Status)
	}
	if r := call(t, "GET", "/leases/"+l1.ID+"/terms", req{tok: token(t, subTermsX)}); r.Status != 404 {
		t.Errorf("another owner reads the terms: %d", r.Status)
	}
}
