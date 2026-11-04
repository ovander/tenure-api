package lease

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// fixtures loads every legal parameter fixture: no legal constant is written
// in these tests (CLAUDE.md).
func fixtures(t *testing.T) params.Set {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "params", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("no fixtures", err)
	}
	var rs []io.Reader
	for _, f := range files {
		r, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		rs = append(rs, r)
	}
	ps, err := params.Load(rs...)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func has(errs []*invariant.Error, typ string) bool {
	for _, e := range errs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

var start = period.NewDate(2026, 3, 1)

func months(t *testing.T, ps params.Set, key string) money.Cents {
	t.Helper()
	m, err := ps.Int64(key, start)
	if err != nil {
		t.Fatal(err)
	}
	return money.Cents(m)
}

func TestCheckDeposit(t *testing.T) {
	ps := fixtures(t)
	const rent = money.Cents(80000)
	cases := []struct {
		kind   Kind
		amount money.Cents
		want   string
	}{
		{KindUnfurnished, rent * months(t, ps, params.KeyDepositMaxUnfurnished), ""},
		{KindUnfurnished, rent*months(t, ps, params.KeyDepositMaxUnfurnished) + 1, TypeDepositAboveCap},
		{KindFurnished, rent * months(t, ps, params.KeyDepositMaxFurnished), ""},
		{KindFurnished, rent*months(t, ps, params.KeyDepositMaxFurnished) + 1, TypeDepositAboveCap},
		{KindMobility, 0, ""},
		{KindMobility, 1, TypeDepositNotAllowed},
		// A box let alone has no legal cap (Q-26).
		{KindBox, rent * 12, ""},
	}
	for _, c := range cases {
		e, err := CheckDeposit(c.kind, rent, c.amount, start, ps)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if e != nil {
			got = e.Type
		}
		if got != c.want {
			t.Errorf("%s deposit %d: %q; want %q", c.kind, c.amount, got, c.want)
		}
	}
}

func TestValidateDepositAndMobilityTerm(t *testing.T) {
	ps := fixtures(t)
	r, err := ps.MobilityTerm(start)
	if err != nil {
		t.Fatal(err)
	}
	base := Lease{Kind: KindMobility, StartOn: start, RentCents: 70000, TermMonths: r.Max, Assets: []portfolio.Asset{flat("A", false, 0, o1)}}
	for _, c := range []struct {
		name string
		mod  func(*Lease)
		want string
	}{
		{"mobility at the longest term", func(*Lease) {}, ""},
		{"mobility above the longest term", func(l *Lease) { l.TermMonths = r.Max + 1 }, TypeMobilityTerm},
		{"mobility below the shortest term", func(l *Lease) { l.TermMonths = r.Min - 1 }, TypeMobilityTerm},
		{"mobility with a deposit", func(l *Lease) { l.DepositCents = 1 }, TypeDepositNotAllowed},
		{"unfurnished deposit above the cap", func(l *Lease) {
			l.Kind, l.TermMonths = KindUnfurnished, 36
			l.DepositCents = l.RentCents*months(t, ps, params.KeyDepositMaxUnfurnished) + 1
		}, TypeDepositAboveCap},
	} {
		l := base
		c.mod(&l)
		errs, err := Validate(l, Check{Event: EventCreation, AsOf: start}, ps)
		if err != nil {
			t.Fatal(err)
		}
		if (c.want == "" && len(errs) > 0) || (c.want != "" && !has(errs, c.want)) {
			t.Errorf("%s: %v; want %q", c.name, types(errs), c.want)
		}
	}
	// A rent change (renewal check) never re-checks a deposit already held.
	l := base
	l.Kind, l.TermMonths, l.DepositCents = KindUnfurnished, 36, base.RentCents
	l.RentCents /= 2
	errs, err := Validate(l, Check{Event: EventRenewal, AsOf: start}, ps)
	if err != nil || len(errs) > 0 {
		t.Errorf("renewal re-checked the deposit: %v %v", types(errs), err)
	}
}

func TestCheckGuarantees(t *testing.T) {
	ps := fixtures(t)
	l := Lease{Kind: KindUnfurnished, StartOn: start, RentCents: 80000}
	surety := GuaranteeFact{Kind: GuaranteeSurety, Status: StatusValid, AmountCents: 1000000}
	insurance := GuaranteeFact{Kind: GuaranteeInsurance, Status: StatusValid}
	visale := GuaranteeFact{Kind: GuaranteeVisale, Status: StatusValid}
	deposit := func(c money.Cents) GuaranteeFact {
		return GuaranteeFact{Kind: GuaranteeDeposit, Status: StatusHeld, AmountCents: c}
	}
	maxDeposit := l.RentCents * months(t, ps, params.KeyDepositMaxUnfurnished)
	cases := []struct {
		name         string
		gs           []GuaranteeFact
		students     bool
		checkDeposit bool
		want         []string
	}{
		{"surety alone", []GuaranteeFact{surety}, false, false, nil},
		{"surety with insurance", []GuaranteeFact{surety, insurance}, false, false, []string{TypeSuretyWithInsurance}},
		{"surety with insurance, students", []GuaranteeFact{surety, insurance}, true, false, nil},
		{"insurance with an expired surety", []GuaranteeFact{insurance, {Kind: GuaranteeSurety, Status: StatusExpired}}, false, false, nil},
		{"visale with surety", []GuaranteeFact{visale, surety}, false, false, []string{TypeVisaleWithSurety}},
		{"visale alone", []GuaranteeFact{visale, deposit(maxDeposit)}, false, true, nil},
		{"deposit at the cap", []GuaranteeFact{deposit(maxDeposit)}, false, true, nil},
		{"deposit above the cap", []GuaranteeFact{deposit(maxDeposit + 1)}, false, true, []string{TypeDepositAboveCap}},
		{"two deposits above the cap together", []GuaranteeFact{deposit(maxDeposit), deposit(1)}, false, true, []string{TypeDepositAboveCap}},
		{"a deposit held is not re-checked", []GuaranteeFact{deposit(maxDeposit + 1)}, false, false, nil},
		{"a settled deposit does not count", []GuaranteeFact{{Kind: GuaranteeDeposit, Status: StatusSettled, AmountCents: maxDeposit}, deposit(maxDeposit)}, false, true, nil},
	}
	for _, c := range cases {
		errs, err := CheckGuarantees(l, c.gs, c.students, c.checkDeposit, ps)
		if err != nil {
			t.Fatal(err)
		}
		got := types(errs)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v; want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v; want %v", c.name, got, c.want)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestCheckDiagnostics(t *testing.T) {
	ps := fixtures(t)
	on := period.NewDate(2026, 11, 1)
	rule, err := ps.Diagnostics(on)
	if err != nil {
		t.Fatal(err)
	}
	minClass, err := ps.MinDPEClass(on)
	if err != nil {
		t.Fatal(err)
	}
	flatID, boxID := uuid.New(), uuid.New()
	flatAsset := portfolio.AssetView{ID: flatID, AssetInput: portfolio.AssetInput{Kind: portfolio.KindApartment}}
	boxAsset := portfolio.AssetView{ID: boxID, AssetInput: portfolio.AssetInput{Kind: portfolio.KindBox}}
	// fresh returns a record of kind done one month ago, valid by its rule.
	fresh := func(asset uuid.UUID, kind string) portfolio.DiagnosticView {
		d := portfolio.DiagnosticView{AssetID: asset, DiagnosticInput: portfolio.DiagnosticInput{Kind: portfolio.DiagnosticKind(kind), DoneOn: ptr(on.AddMonths(-1))}}
		if kind == "dpe" {
			d.Result = minClass
		}
		return d
	}
	complete := func() []portfolio.DiagnosticView {
		var out []portfolio.DiagnosticView
		for _, k := range rule.Required["dwelling"] {
			out = append(out, fresh(flatID, k))
		}
		for _, k := range rule.Required["box"] {
			out = append(out, fresh(boxID, k))
		}
		return out
	}
	replace := func(ds []portfolio.DiagnosticView, d portfolio.DiagnosticView) []portfolio.DiagnosticView {
		for i := range ds {
			if ds[i].AssetID == d.AssetID && ds[i].Kind == d.Kind {
				ds[i] = d
			}
		}
		return ds
	}
	erpMonths := rule.ValidityMonths["erp"]
	worst := string(portfolio.DPEClasses[len(portfolio.DPEClasses)-1])
	cases := []struct {
		name string
		ds   func() []portfolio.DiagnosticView
		want string
	}{
		{"complete and valid", complete, ""},
		{"missing DPE", func() []portfolio.DiagnosticView { return complete()[1:] }, TypeDiagnosticMissing},
		{"ERP on its last valid day", func() []portfolio.DiagnosticView {
			d := fresh(flatID, "erp")
			d.DoneOn = ptr(on.AddMonths(-erpMonths).AddDays(1))
			return replace(complete(), d)
		}, ""},
		{"ERP past its validity", func() []portfolio.DiagnosticView {
			d := fresh(flatID, "erp")
			d.DoneOn = ptr(on.AddMonths(-erpMonths))
			return replace(complete(), d)
		}, TypeDiagnosticExpired},
		{"report validity earlier than the legal one", func() []portfolio.DiagnosticView {
			d := fresh(flatID, "electricity")
			d.ValidUntil = ptr(on.AddDays(-1))
			return replace(complete(), d)
		}, TypeDiagnosticExpired},
		{"DPE made before the reform", func() []portfolio.DiagnosticView {
			d := fresh(flatID, "dpe")
			d.DoneOn = ptr(rule.DPEMinDoneOn.AddDays(-1))
			return replace(complete(), d)
		}, TypeDiagnosticExpired},
		{"lead declared not required", func() []portfolio.DiagnosticView {
			d := portfolio.DiagnosticView{AssetID: flatID, DiagnosticInput: portfolio.DiagnosticInput{Kind: "lead", NotRequiredReason: "immeuble construit en 1975"}}
			return replace(complete(), d)
		}, ""},
		{"DPE class below the minimum", func() []portfolio.DiagnosticView {
			d := fresh(flatID, "dpe")
			d.Result = worst
			return replace(complete(), d)
		}, TypeDPEClassForbidden},
		{"box without its ERP", func() []portfolio.DiagnosticView {
			var out []portfolio.DiagnosticView
			for _, d := range complete() {
				if d.AssetID != boxID {
					out = append(out, d)
				}
			}
			return out
		}, TypeDiagnosticMissing},
	}
	for _, c := range cases {
		errs, err := CheckDiagnostics([]portfolio.AssetView{flatAsset, boxAsset}, c.ds(), on, ps)
		if err != nil {
			t.Fatal(err)
		}
		if (c.want == "" && len(errs) > 0) || (c.want != "" && !has(errs, c.want)) {
			t.Errorf("%s: %v; want %q", c.name, types(errs), c.want)
		}
	}
	// A blocker names its asset and diagnostic as a subject, so the owner
	// reads an address and a diagnostic, never an id (Q-105).
	missing, _ := CheckDiagnostics([]portfolio.AssetView{flatAsset, boxAsset}, complete()[1:], on, ps)
	dpe := invariant.Subject{AssetID: flatAsset.ID.String(), Diagnostic: "dpe"}
	if len(missing) != 2 || missing[0].Type != TypeDiagnosticMissing || missing[0].Subject != dpe ||
		missing[1].Type != TypeDPEClassMissing || missing[1].Subject != dpe {
		t.Errorf("missing DPE subjects: %+v", missing)
	}
	late := fresh(flatID, "electricity")
	late.ValidUntil = ptr(on.AddDays(-1))
	expired, _ := CheckDiagnostics([]portfolio.AssetView{flatAsset, boxAsset}, replace(complete(), late), on, ps)
	if len(expired) != 1 || expired[0].Subject != (invariant.Subject{AssetID: flatAsset.ID.String(), Diagnostic: "electricity", ValidUntil: on.AddDays(-1).String()}) {
		t.Errorf("expired electricity subject: %+v", expired)
	}

	// The worse of the asset's recorded class and the DPE's result counts.
	ds := complete()
	worse := flatAsset
	worse.DPEClass = ptr(worst)
	if errs, _ := CheckDiagnostics([]portfolio.AssetView{worse, boxAsset}, ds, on, ps); !has(errs, TypeDPEClassForbidden) {
		t.Errorf("asset class %s ignored: %v", worst, types(errs))
	}
}

func TestClauses(t *testing.T) {
	codes, e := Clauses(json.RawMessage(`[{"code":"boiler_servicing"},{"code":"garden_upkeep"}]`))
	if e != nil || len(codes) != 2 {
		t.Errorf("vetted: %v %v", codes, e)
	}
	for _, bad := range []string{`[{"code":"no_pets"}]`, `["free text"]`, `{"code":"boiler_servicing"}`} {
		if _, e := Clauses(json.RawMessage(bad)); e == nil || e.Type != TypeClauseNotVetted {
			t.Errorf("%s accepted", bad)
		}
	}
	if codes, e := Clauses(json.RawMessage(`[]`)); e != nil || len(codes) != 0 {
		t.Errorf("empty: %v %v", codes, e)
	}
}

func TestWarnings(t *testing.T) {
	ps := fixtures(t)
	m := months(t, ps, params.KeyDepositWarningBox)
	box := Lease{Kind: KindBox, StartOn: start, RentCents: 10000, DepositCents: 10000 * m}
	if w, _ := Warnings(box, nil, nil, start, ps); len(w) != 0 {
		t.Errorf("box at the warning level: %v", types(w))
	}
	box.DepositCents++
	if w, _ := Warnings(box, nil, nil, start, ps); !has(w, WarnBoxDeposit) {
		t.Errorf("box above the warning level: %v", types(w))
	}
	l := Lease{Kind: KindUnfurnished, StartOn: start, RentCents: 80000}
	past := start.AddDays(-1)
	w, _ := Warnings(l, []GuaranteeFact{{Kind: GuaranteeSurety, Status: StatusValid}}, []*period.Date{&past}, start, ps)
	if !has(w, WarnSuretyExpired) {
		t.Errorf("expired surety: %v", types(w))
	}
}
