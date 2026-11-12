// Package golden replays the golden cases under testdata/golden/<year>/
// through the calculation engine (RENT-BE-001 §10). Each case file holds the
// inputs (leases, charges, payments, owner shares, parameter overrides) and a
// list of steps — fee, revise, allocate, validate, tax — each with its
// expected result.
// A case of year Y only sees parameters effective on or before 31 December Y.
//
// Regenerate the expected results with:
//
//	go test ./internal/golden -update
//
// and review the diff: a changed figure is a behaviour change.
package golden

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/tax"
)

var update = flag.Bool("update", false, "rewrite the expected results of the golden files")

var root = filepath.Join("..", "..", "testdata")

type caseFile struct {
	Case           string            `json:"case"`
	Description    string            `json:"description"`
	ParamsOverride []params.Entry    `json:"params_override,omitempty"`
	Leases         []lease.Lease     `json:"leases"`
	Charges        []charge.Charge   `json:"charges,omitempty"`
	Payments       []payment.Payment `json:"payments,omitempty"`
	Assets         []tax.AssetShares `json:"assets,omitempty"`
	// Loans are the recorded loans' instalments the tax steps read
	// (RENT-SPEC-014, Q-126).
	Loans []tax.LoanInstalment `json:"loans,omitempty"`
	Steps []step               `json:"steps"`
}

type step struct {
	Name     string          `json:"name"`
	Fee      *feeStep        `json:"fee,omitempty"`
	Revise   *reviseStep     `json:"revise,omitempty"`
	Allocate *allocateStep   `json:"allocate,omitempty"`
	Tax      *taxStep        `json:"tax,omitempty"`
	Validate *validateStep   `json:"validate,omitempty"`
	Expected json.RawMessage `json:"expected"`
}

type feeStep struct {
	LeaseID        string                  `json:"lease_id"`
	Period         period.Period           `json:"period"`
	Regularisation *billing.Regularisation `json:"regularisation,omitempty"`
}

type reviseStep struct {
	LeaseID     string        `json:"lease_id"`
	Index       billing.Index `json:"index"`
	Anniversary period.Date   `json:"anniversary"`
	From        period.Period `json:"from"`
	// Apply updates the case's lease with the result for the next steps.
	Apply bool `json:"apply"`
}

type allocateStep struct {
	Exercise   charge.Exercise        `json:"exercise"`
	Provisions map[string]money.Cents `json:"provisions"`
}

type validateStep struct {
	LeaseID  string          `json:"lease_id"`
	Event    lease.Event     `json:"event"`
	AsOf     period.Date     `json:"as_of"`
	Previous *lease.Previous `json:"previous,omitempty"`
}

type taxStep struct {
	Year  int                      `json:"year"`
	Prior map[string][]tax.Deficit `json:"prior,omitempty"`
}

func loadParams(t *testing.T, year int, override []params.Entry) params.Set {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "params", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no parameter fixtures: %v", err)
	}
	var all []params.Entry
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		set, err := params.Load(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		all = append(all, set.Entries()...)
	}
	set, err := params.New(all...)
	if err != nil {
		t.Fatal(err)
	}
	set = set.Until(period.NewDate(year, 12, 31))
	set, err = set.With(override...)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestGolden(t *testing.T) {
	years, err := os.ReadDir(filepath.Join(root, "golden"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, y := range years {
		year, err := strconv.Atoi(y.Name())
		if !y.IsDir() || err != nil {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(root, "golden", y.Name(), "*.json"))
		sort.Strings(files)
		for _, f := range files {
			n++
			t.Run(y.Name()+"/"+filepath.Base(f), func(t *testing.T) { runCase(t, f, year) })
		}
	}
	if n == 0 {
		t.Fatal("no golden case found")
	}
}

func runCase(t *testing.T, path string, year int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c caseFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ps := loadParams(t, year, c.ParamsOverride)
	leases := map[string]lease.Lease{}
	for _, l := range c.Leases {
		leases[l.ID] = l
	}
	changed := false
	for i := range c.Steps {
		s := &c.Steps[i]
		got, err := runStep(s, leases, c, ps)
		if err != nil {
			t.Fatalf("step %q: %v", s.Name, err)
		}
		b, err := json.MarshalIndent(got, "      ", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if !bytes.Equal(normalise(t, s.Expected), normalise(t, b)) {
				changed = true
			}
			s.Expected = b
			continue
		}
		if len(s.Expected) == 0 {
			t.Errorf("step %q has no expected result; run with -update and review", s.Name)
			continue
		}
		if want, have := normalise(t, s.Expected), normalise(t, b); !bytes.Equal(want, have) {
			t.Errorf("step %q differs from the golden file\nwant %s\n got %s", s.Name, want, have)
		}
	}
	if *update && changed {
		out, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
	}
}

func normalise(t *testing.T, b []byte) []byte {
	t.Helper()
	if len(b) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return out
}

func runStep(s *step, leases map[string]lease.Lease, c caseFile, ps params.Set) (any, error) {
	switch {
	case s.Fee != nil:
		l, ok := leases[s.Fee.LeaseID]
		if !ok {
			return nil, fmt.Errorf("unknown lease %s", s.Fee.LeaseID)
		}
		return billing.ComputeFee(l, s.Fee.Period, ps, billing.FeeOptions{Regularisation: s.Fee.Regularisation})
	case s.Revise != nil:
		l, ok := leases[s.Revise.LeaseID]
		if !ok {
			return nil, fmt.Errorf("unknown lease %s", s.Revise.LeaseID)
		}
		r, err := billing.Revise(l, s.Revise.Index, s.Revise.Anniversary, s.Revise.From, ps)
		if err == nil && s.Revise.Apply {
			leases[l.ID] = r.Apply(l)
		}
		return r, err
	case s.Allocate != nil:
		var cs []charge.Charge
		for _, ch := range c.Charges {
			if ch.AssetID == s.Allocate.Exercise.AssetID {
				cs = append(cs, ch)
			}
		}
		return charge.Allocate(cs, c.Leases, s.Allocate.Exercise, s.Allocate.Provisions, ps)
	case s.Validate != nil:
		l, ok := leases[s.Validate.LeaseID]
		if !ok {
			return nil, fmt.Errorf("unknown lease %s", s.Validate.LeaseID)
		}
		errs, err := lease.Validate(l, lease.Check{Event: s.Validate.Event, AsOf: s.Validate.AsOf, Previous: s.Validate.Previous}, ps)
		if errs == nil {
			errs = []*invariant.Error{}
		}
		return errs, err
	case s.Tax != nil:
		charges, flags := tax.WithLoans(c.Charges, c.Loans, s.Tax.Year)
		sum, err := tax.Lines(c.Payments, charges, c.Assets, s.Tax.Year, ps, s.Tax.Prior)
		sum.Flags = append(sum.Flags, flags...)
		return sum, err
	}
	return nil, fmt.Errorf("step has no calculation")
}

// TestYearIsolation proves that a parameter dated after a case's year cannot
// reach it: a 2026 value contradicting 2025 is invisible to Until(2025-12-31).
func TestYearIsolation(t *testing.T) {
	set := loadParams(t, 2025, nil)
	for _, e := range set.Entries() {
		if e.EffectiveFrom.Year > 2025 {
			t.Errorf("2025 case sees %s effective %s", e.Key, e.EffectiveFrom)
		}
	}
}
