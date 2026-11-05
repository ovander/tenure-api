package document

import (
	"testing"
	"testing/fstest"

	"github.com/ovander/tenure-api/templates"
)

func TestCatalogueOrdersVersionsAndMarksTheLatest(t *testing.T) {
	fsys := fstest.MapFS{
		"contract/v1/document.html":        {Data: []byte("a")},
		"contract/v10/document.html":       {Data: []byte("c")},
		"contract/v2/document.html":        {Data: []byte("b")},
		"deposit_receipt/v1/document.html": {Data: []byte("d")},
		"shared/v1/style.html":             {Data: []byte("s")},
	}
	got, err := Catalogue(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "contract" || got[1].Name != "deposit_receipt" {
		t.Fatalf("templates = %+v, want contract and deposit_receipt, without shared", got)
	}
	var order []string
	for _, v := range got[0].Versions {
		order = append(order, v.Version)
		if v.Current != (v.Version == "v10") {
			t.Errorf("%s current = %v", v.Version, v.Current)
		}
	}
	if want := []string{"v1", "v2", "v10"}; len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Errorf("versions = %v, want %v", order, want)
	}
	// sha256("a")
	if got[0].Versions[0].SHA256 != "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb" {
		t.Errorf("sha256 = %s", got[0].Versions[0].SHA256)
	}
	if got[1].Class != ClassDeposit {
		t.Errorf("deposit_receipt renders class %s, want %s", got[1].Class, ClassDeposit)
	}
}

func TestCatalogueRefusesAVersionNotNamedVn(t *testing.T) {
	if _, err := Catalogue(fstest.MapFS{"contract/draft/document.html": {Data: []byte("x")}}); err == nil {
		t.Fatal("want an error for contract/draft")
	}
}

func TestEmbeddedTemplatesEachHaveACurrentVersion(t *testing.T) {
	got, err := Catalogue(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no template embedded")
	}
	for _, tv := range got {
		n := 0
		for _, v := range tv.Versions {
			if v.Current {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s has %d current versions", tv.Name, n)
		}
	}
}
