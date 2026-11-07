package document

import "testing"

func TestNumberOf(t *testing.T) {
	cases := []struct {
		name string
		row  documentRow
		want string
	}{
		{"issued", documentRow{Source: SourceGenerated, TemplateVersion: "inventory/v3", ObjectKey: "o/inventory/2026/EE-0A1B2C3D-2026-0001.pdf"}, "EE-0A1B2C3D-2026-0001"},
		{"attached file", documentRow{Source: SourceGenerated, ObjectKey: "o/tax_summary/2026/RF-0A1B2C3D-2025-0001-lines.csv"}, ""},
		{"upload", documentRow{Source: "upload", ObjectKey: "o/inventory/2026/0190aa00-0000-7000-8000-000000000001.pdf"}, ""},
	}
	for _, c := range cases {
		if got := numberOf(c.row); got != c.want {
			t.Errorf("%s: numberOf = %q, want %q", c.name, got, c.want)
		}
		if got := view(c.row).Number; got != c.want {
			t.Errorf("%s: view number = %q, want %q", c.name, got, c.want)
		}
	}
}
