package inventory

import (
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/document"
)

// windowRules reads the window rules of the legal fixtures as of 2026.
func windowRules(t *testing.T) WindowRules {
	t.Helper()
	r, err := WindowRulesAt(fixtures(t), d("2026-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCompletionWindows(t *testing.T) {
	rules := windowRules(t)
	for _, c := range []struct {
		done, tenEnd, heatStart, heatEnd string
	}{
		{"2026-06-15", "2026-06-25", "2026-10-01", "2026-10-31"}, // summer: the coming season
		{"2026-09-30", "2026-10-10", "2026-10-01", "2026-10-31"}, // the day before the season
		{"2026-10-01", "2026-10-11", "", ""},                     // in season: no heating window
		{"2026-01-31", "2026-02-10", "", ""},                     // in season across the new year
		{"2026-05-31", "2026-06-10", "", ""},                     // the season's last month
		{"2026-06-01", "2026-06-11", "2026-10-01", "2026-10-31"},
	} {
		w := CompletionWindows(d(c.done), rules)
		if w.TenDayEndsOn.String() != c.tenEnd {
			t.Errorf("%s: ten-day ends %s, want %s", c.done, w.TenDayEndsOn, c.tenEnd)
		}
		got := ""
		if w.HeatingStartsOn != nil {
			got = w.HeatingStartsOn.String() + " " + w.HeatingEndsOn.String()
		}
		if want := c.heatStart + " " + c.heatEnd; c.heatStart == "" && got != "" || c.heatStart != "" && got != want {
			t.Errorf("%s: heating window %q, want %q", c.done, got, want)
		}
	}
	// Synthetic rules, not the legal ones: a season that does not run across
	// the new year, and a two-month window ending on a short month.
	w := CompletionWindows(d("2026-12-20"), WindowRules{Days: 3, HeatingMonths: 2, SeasonStart: 1, SeasonEnd: 3})
	if w.TenDayEndsOn.String() != "2026-12-23" || w.HeatingStartsOn == nil || w.HeatingStartsOn.String() != "2027-01-01" || w.HeatingEndsOn.String() != "2027-02-28" {
		t.Errorf("non-wrapping season: %+v", w)
	}
}

func TestWindowsOpen(t *testing.T) {
	rules := windowRules(t)
	done := d("2026-06-15")
	w := CompletionWindows(done, rules)
	for _, c := range []struct {
		reason, on string
		open       bool
	}{
		{ReasonTenDay, "2026-06-14", false}, // before the entry
		{ReasonTenDay, "2026-06-15", true},
		{ReasonTenDay, "2026-06-25", true}, // last day included
		{ReasonTenDay, "2026-06-26", false},
		{ReasonHeating, "2026-09-30", false},
		{ReasonHeating, "2026-10-01", true},
		{ReasonHeating, "2026-10-31", true},
		{ReasonHeating, "2026-11-01", false},
		{"other", "2026-06-16", false},
	} {
		if got := w.Open(c.reason, done, d(c.on)); got != c.open {
			t.Errorf("%s on %s: open %v, want %v", c.reason, c.on, got, c.open)
		}
	}
	inSeason := CompletionWindows(d("2026-11-02"), rules)
	if inSeason.Open(ReasonHeating, d("2026-11-02"), d("2026-11-03")) {
		t.Error("no heating window for an entry done in season")
	}
}

func TestDecision(t *testing.T) {
	yes, no := Completion{Accepted: true}, Completion{}
	for _, c := range []struct {
		lines []Completion
		want  string
	}{{[]Completion{yes, yes}, DecisionAccepted}, {[]Completion{no}, DecisionRefused}, {[]Completion{yes, no}, DecisionPartial}} {
		if got := decision(c.lines); got != c.want {
			t.Errorf("%v: %s, want %s", c.lines, got, c.want)
		}
	}
}

func TestCompletionDocument(t *testing.T) {
	data := CompletionData{Number: "EC-X-2026-0001", IssuedOn: d("2026-06-20"), EntryNumber: "EE-X-2026-0001", EntryDoneOn: d("2026-06-15"),
		Owner: document.Party{Name: "Bailleur Test", Address: "1 rue A, Lyon"}, Tenants: []string{"Léa Martin"}, Premises: []string{"12 rue des Lilas"},
		RequestedOn: d("2026-06-18"), DecidedOn: d("2026-06-20"), Decision: decisionFR[DecisionPartial], WindowEndsOn: d("2026-06-25"),
		Lines: []CompletionLine{
			{Room: "Séjour", Label: "Parquet", Entry: "Bon état", Requested: "État d’usage", RequestedComment: "Rayure", Accepted: true},
			{Room: "Cuisine", Label: "Évier", Entry: "Bon état", Requested: "Mauvais état"}}}
	html, _, err := document.Renderer{}.Render(completionTemplate, CompletionTemplate, data, document.Footer{Number: data.Number, SnapshotHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"Complément à l’état des lieux d’entrée", "EC-X-2026-0001", "EE-X-2026-0001", "15/06/2026", "18/06/2026",
		"25/06/2026", "dix jours", "acceptée en partie", "Rayure", "Acceptée", "Refusée", "n’est pas modifié"} {
		if !strings.Contains(s, want) {
			t.Errorf("the EC document lacks %q", want)
		}
	}
	data.Heating = true
	html, _, _ = document.Renderer{}.Render(completionTemplate, CompletionTemplate, data, document.Footer{Number: data.Number, SnapshotHash: "x"})
	if !strings.Contains(string(html), "période de chauffe") {
		t.Error("a heating request names the heating season")
	}
}
