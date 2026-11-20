package csvx

import (
	"bytes"
	"testing"
)

func TestCell(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"Loyer":                   "Loyer",
		"=HYPERLINK(\"x\",\"y\")": "'=HYPERLINK(\"x\",\"y\")",
		"+33 6":                   "'+33 6",
		"-1234":                   "-1234",
		"+12":                     "+12",
		"- travaux":               "'- travaux",
		"@SUM(A1)":                "'@SUM(A1)",
		"\tcmd":                   "'\tcmd",
		"\rcmd":                   "'\rcmd",
		"2026-06-30":              "2026-06-30",
		"{\"a\":1}":               "{\"a\":1}",
	} {
		if got := Cell(in); got != want {
			t.Errorf("Cell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriter(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	_ = w.Write([]string{"=1+1", "-500", "Dupont, Marie"})
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "'=1+1,-500,\"Dupont, Marie\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
