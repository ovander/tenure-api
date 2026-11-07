//go:build integration

package itest

import (
	"testing"
)

const (
	subCalendar  = "9955" // owner of the calendar scenario (RENT-SPEC-012 base)
	subCalendarX = "9956"
)

type calendarView struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Items []struct {
		On      string `json:"on"`
		Kind    string `json:"kind"`
		Status  string `json:"status"`
		AssetID string `json:"asset_id"`
		LeaseID string `json:"lease_id"`
		Ref     string `json:"ref"`
	} `json:"items"`
}

func (v calendarView) has(kind, ref string) bool {
	for _, it := range v.Items {
		if it.Kind == kind && (ref == "" || it.Ref == ref) {
			return true
		}
	}
	return false
}

// TestCalendar reads the computed obligations calendar (RENT-SPEC-012 base,
// RENT-INV-001 slice 10, Q-116) on a real lease: a diagnostic due within the
// range, the entry's completion window, the insurance anniversary; then, the
// lease under notice and its exit issued, the end of the notice and the
// deposit deadline, gone once the RD statement is issued; one asset; ranges
// refused; another owner reads nothing.
func TestCalendar(t *testing.T) {
	tok := token(t, subCalendar)
	ps := fixtureParams(t)
	var owner, ten idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Calendrier", "address": "9 rue K, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}, "email": "lea@example.test"}}).json(t, &ten)
	lease, entry := completedEntry(t, tok, ten.ID, "9 rue K, Lyon")
	var asset string
	if err := env.super.QueryRow(`SELECT asset_id FROM lease_assets WHERE lease_id = $1`, lease).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	rule, _ := ps.Diagnostics(today())
	months := rule.ValidityMonths["erp"]
	done := today().AddMonths(-months).AddDays(20)
	if r := call(t, "POST", "/assets/"+asset+"/diagnostics", req{tok: tok, body: map[string]any{"kind": "erp", "done_on": done.String()}}); r.Status != 201 {
		t.Fatalf("diagnostic: %d %s", r.Status, r.Body)
	}
	superExec(t, `UPDATE leases SET state = 'active', start_on = current_date - 300 WHERE id = $1`, lease)

	var v calendarView
	call(t, "GET", "/calendar", req{tok: tok}).json(t, &v)
	if v.From != today().String() || v.To != today().AddDays(90).String() {
		t.Errorf("default range %s..%s", v.From, v.To)
	}
	if !v.has("diagnostic_expiry", "erp") || !v.has("insurance_certificate", "") {
		t.Errorf("active lease %+v", v.Items)
	}

	// The entry issued, then the lease under notice with its exit issued.
	issuedExit(t, tok, "Bailleur Calendrier", lease, entry)
	v = calendarView{}
	call(t, "GET", "/calendar", req{tok: tok}).json(t, &v)
	if !v.has("completion_window", "ten_day") || !v.has("notice_end", "") || !v.has("deposit_deadline", "") || v.has("insurance_certificate", "") {
		t.Errorf("under notice %+v", v.Items)
	}
	for _, it := range v.Items {
		if it.Status != "upcoming" || it.AssetID != asset {
			t.Errorf("item %+v", it)
		}
	}
	if r := call(t, "POST", "/leases/"+lease+"/deposit-settlement", req{tok: tok, body: map[string]any{}}); r.Status != 201 {
		t.Fatalf("settle: %d %s", r.Status, r.Body)
	}
	v = calendarView{}
	call(t, "GET", "/calendar", req{tok: tok}).json(t, &v)
	if v.has("deposit_deadline", "") {
		t.Errorf("deadline kept once the RD is issued %+v", v.Items)
	}

	// One asset, a range, and refused ranges.
	v = calendarView{}
	call(t, "GET", "/calendar?asset_id="+lease, req{tok: tok}).json(t, &v)
	if len(v.Items) != 0 {
		t.Errorf("another asset's items %+v", v.Items)
	}
	v = calendarView{}
	call(t, "GET", "/calendar?from="+today().AddDays(1).String()+"&to="+today().AddDays(2).String(), req{tok: tok}).json(t, &v)
	if v.has("diagnostic_expiry", "") {
		t.Errorf("an item out of range %+v", v.Items)
	}
	if r := call(t, "GET", "/calendar?from=2026-13-01", req{tok: tok}); r.Status != 400 {
		t.Errorf("bad date: %d", r.Status)
	}
	if r := call(t, "GET", "/calendar?from="+today().String()+"&to="+today().AddDays(-1).String(), req{tok: tok}); r.Status != 422 {
		t.Errorf("to before from: %d", r.Status)
	}

	var other idv
	call(t, "PUT", "/me", req{tok: token(t, subCalendarX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
	v = calendarView{}
	call(t, "GET", "/calendar", req{tok: token(t, subCalendarX)}).json(t, &v)
	if len(v.Items) != 0 {
		t.Errorf("another owner's calendar %+v", v.Items)
	}
}
