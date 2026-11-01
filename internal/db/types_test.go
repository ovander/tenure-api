package db

import (
	"reflect"
	"testing"
)

func TestHundredths(t *testing.T) {
	for in, want := range map[string]Hundredths{"45.30": 4530, "45.3": 4530, "100": 10000, "0.05": 5, "-1.50": -150} {
		var h Hundredths
		if err := h.Scan(in); err != nil || h != want {
			t.Errorf("Scan(%q) = %d, %v", in, h, err)
		}
	}
	for h, want := range map[Hundredths]string{4530: "45.30", 5: "0.05", -150: "-1.50", 10000: "100.00"} {
		if v, _ := h.Value(); v != want {
			t.Errorf("Value(%d) = %v", h, v)
		}
	}
	var h Hundredths
	if h.Scan("abc") == nil {
		t.Error("invalid numeric accepted")
	}
}

func TestTextArray(t *testing.T) {
	cases := map[string]TextArray{
		`{}`:                         {},
		`{Dupont,"Jean-Luc Martin"}`: {"Dupont", "Jean-Luc Martin"},
		`{"a \"b\"","c\\d"}`:         {`a "b"`, `c\d`},
	}
	for in, want := range cases {
		var a TextArray
		if err := a.Scan(in); err != nil || !reflect.DeepEqual(a, want) {
			t.Errorf("Scan(%q) = %q, %v", in, a, err)
		}
	}
	v, _ := TextArray{`a "b"`, "c,d"}.Value()
	var back TextArray
	if err := back.Scan(v); err != nil || !reflect.DeepEqual(back, TextArray{`a "b"`, "c,d"}) {
		t.Errorf("round trip %v → %q, %v", v, back, err)
	}
}
