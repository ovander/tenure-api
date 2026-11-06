package app

import (
	"testing"

	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/inventory"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/tax"
	"github.com/ovander/tenure-api/templates"
)

// TestModulesRenderTheCurrentTemplate keeps GET /templates truthful (Q-106):
// the version each module renders new documents with is the latest one
// embedded, so a new version directory goes with its module's constant.
func TestModulesRenderTheCurrentTemplate(t *testing.T) {
	cat, err := document.Catalogue(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	current := map[string]string{}
	for _, tv := range cat {
		for _, v := range tv.Versions {
			if v.Current {
				current[tv.Name] = v.Version
			}
		}
	}
	used := map[string]string{
		"contract": lease.ContractTemplate, "amendment": lease.AmendmentTemplate, "deposit_receipt": lease.DepositReceiptTemplate,
		"surety": lease.SuretyTemplate, "rent_call": billing.RentCallTemplate, "revision_notice": billing.RevisionTemplate,
		"cancellation": billing.CancellationTemplate, "receipt": billing.ReceiptTemplate, "regularisation": charge.RegularisationTemplate,
		"tax_summary": tax.SummaryTemplate, "inventory": inventory.DocumentTemplate,
		"inventory_completion": inventory.CompletionTemplate,
	}
	for name, v := range used {
		if current[name] != v {
			t.Errorf("%s: module renders %s, latest embedded is %q", name, v, current[name])
		}
	}
	if len(current) != len(used) {
		t.Errorf("embedded templates %v, modules use %v: a template no module renders, or one missing here", current, used)
	}
}
