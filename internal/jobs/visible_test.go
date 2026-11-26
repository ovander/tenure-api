package jobs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestVisible(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	summary, err := json.Marshal(map[string]any{
		"platform": ownerResult{OK: true, Result: map[string]any{"index": 14567}},
		"owners": []ownerResult{
			{OwnerID: a.String(), OK: true, Result: map[string]any{"total_cents": 120000}},
			{OwnerID: b.String(), OK: false, Error: "lease L1 of owner b"},
		},
		"units": 3, "failures": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	platform := Run{Job: "billing.monthly", Status: StatusPartial, Summary: summary}
	read := func(r Run) (owners []ownerResult, units, failures int, platformOK *bool) {
		var s struct {
			Platform *struct {
				OK     bool `json:"ok"`
				Result any  `json:"result"`
			} `json:"platform"`
			Owners   []ownerResult `json:"owners"`
			Units    int           `json:"units"`
			Failures int           `json:"failures"`
		}
		if err := json.Unmarshal(r.Summary, &s); err != nil {
			t.Fatal(err)
		}
		if s.Platform != nil {
			if s.Platform.Result != nil {
				t.Errorf("the platform unit's result is shown: %s", r.Summary)
			}
			platformOK = &s.Platform.OK
		}
		return s.Owners, s.Units, s.Failures, platformOK
	}

	// An admin sees the run whole.
	if got := Visible(platform, nil, true); string(got.Summary) != string(summary) || got.Status != StatusPartial {
		t.Errorf("admin: %s %s", got.Summary, got.Status)
	}
	// Owner a sees their own result only; b's failure is not theirs.
	got := Visible(platform, &a, false)
	owners, units, failures, ok := read(got)
	if len(owners) != 1 || owners[0].OwnerID != a.String() || units != 2 || failures != 0 || ok == nil || !*ok || got.Status != StatusSucceeded {
		t.Errorf("owner a: %s %s", got.Summary, got.Status)
	}
	if strings.Contains(string(got.Summary), b.String()) || strings.Contains(string(got.Summary), "owner b") {
		t.Errorf("owner a reads something of owner b: %s", got.Summary)
	}
	// Owner b sees their failure.
	got = Visible(platform, &b, false)
	if owners, _, failures, _ = read(got); len(owners) != 1 || failures != 1 || got.Status != StatusPartial {
		t.Errorf("owner b: %s %s", got.Summary, got.Status)
	}
	// A caller with no scope sees no owner at all.
	got = Visible(platform, nil, false)
	if owners, units, _, _ = read(got); len(owners) != 0 || units != 1 {
		t.Errorf("no scope: %s", got.Summary)
	}
	// An owner's own run is left as row-level security served it.
	own := Run{Job: "billing.monthly", OwnerID: &a, Status: StatusSucceeded, Summary: summary}
	if got := Visible(own, &a, false); string(got.Summary) != string(summary) {
		t.Errorf("own run changed: %s", got.Summary)
	}
	// A run stopped before its units keeps its status, not its reason.
	check, _ := json.Marshal(map[string]any{"check": "owner b has no lease"})
	got = Visible(Run{Status: StatusFailed, Summary: check}, &a, false)
	if got.Status != StatusFailed || string(got.Summary) != `{"failures":0,"units":0}` {
		t.Errorf("stopped run: %s %s", got.Summary, got.Status)
	}
}
