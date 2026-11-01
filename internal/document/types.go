package document

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// Class is a document class (documents.class).
type Class string

// Document classes produced in slice 3; the others are added with their slice.
const (
	ClassRentCall       Class = "rent_call"
	ClassReceipt        Class = "receipt"
	ClassRegularisation Class = "regularisation"
	ClassRevisionNotice Class = "revision_notice"
	ClassContract       Class = "contract"
	ClassAmendment      Class = "amendment"
	ClassDeposit        Class = "deposit"
	ClassTaxSummary     Class = "tax_summary"
	// ClassCancellation renders a cancelling document; it is archived and
	// numbered in the class of the document it cancels (Q-49).
	ClassCancellation Class = "cancellation"
)

// Prefixes of document numbers (RENT-BE-001 §6).
var prefixes = map[Class]string{
	ClassRentCall: "AL", ClassReceipt: "QL", ClassRegularisation: "RG", ClassRevisionNotice: "RV",
	ClassContract: "CT", ClassAmendment: "AV", ClassDeposit: "DG", ClassTaxSummary: "RF",
}

// Financial classes an accountant may download (RENT-FE-001 §5).
var financial = map[Class]bool{ClassRentCall: true, ClassReceipt: true, ClassRegularisation: true,
	ClassRevisionNotice: true, ClassTaxSummary: true}

// Financial reports whether an accountant may download documents of class c.
func Financial(c Class) bool { return financial[c] }

// AssetCode is the asset segment of a document number: the first eight hex
// digits of the asset id, upper case (Q-48).
func AssetCode(asset uuid.UUID) string {
	return strings.ToUpper(strings.ReplaceAll(asset.String(), "-", "")[:8])
}

// Number formats <prefix>-<asset>-<YYYY>-<seq>, the sequence on four digits.
func Number(c Class, assetCode string, year, seq int) (string, error) {
	p, ok := prefixes[c]
	if !ok {
		return "", fmt.Errorf("document: no number prefix for class %s", c)
	}
	return fmt.Sprintf("%s-%s-%04d-%04d", p, assetCode, year, seq), nil
}

// ObjectKey is the archive key <owner_id>/<class>/<YYYY>/<number>.pdf.
func ObjectKey(owner uuid.UUID, c Class, year int, number string) string {
	return fmt.Sprintf("%s/%s/%04d/%s.pdf", owner, c, year, number)
}

// Party is a named address block.
type Party struct {
	Name    string
	Address string
}

// Line is a document line.
type Line struct {
	Label       string      `json:"label"`
	Detail      string      `json:"detail,omitempty"`
	AmountCents money.Cents `json:"amount_cents"`
}

// RentCallData is the data of a rent call (template rent_call).
type RentCallData struct {
	Number     string
	IssuedOn   period.Date
	Owner      Party
	Tenants    []string
	Assets     []string
	Period     period.Period
	DueOn      period.Date
	Lines      []Line
	TotalCents money.Cents
}

// RevisionData is the data of a revision notice (template revision_notice).
type RevisionData struct {
	Number        string
	IssuedOn      period.Date
	Owner         Party
	Tenants       []string
	Assets        []string
	RefQuarter    string
	RefValue      int64
	NewQuarter    string
	NewValue      int64
	OldRentCents  money.Cents
	NewRentCents  money.Cents
	EffectiveFrom period.Period
	CappedNote    string
}

// CancellationData is the data of a cancelling document (template cancellation).
type CancellationData struct {
	Number            string
	IssuedOn          period.Date
	Owner             Party
	Tenants           []string
	CancelledNumber   string
	CancelledIssuedOn period.Date
	Period            period.Period
	Reason            string
	Lines             []Line
	TotalCents        money.Cents
}
