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

// Document classes (documents.class).
const (
	ClassRentCall       Class = "rent_call"
	ClassReceipt        Class = "receipt"
	ClassRegularisation Class = "regularisation"
	ClassRevisionNotice Class = "revision_notice"
	ClassContract       Class = "contract"
	ClassAmendment      Class = "amendment"
	ClassDeposit        Class = "deposit"
	ClassTaxSummary     Class = "tax_summary"
	ClassInventory      Class = "inventory"
	ClassSupporting     Class = "supporting"
	ClassOther          Class = "other"
	ClassSurety         Class = "surety"
	// ClassCancellation renders a cancelling document; it is archived and
	// numbered in the class of the document it cancels (Q-49).
	ClassCancellation Class = "cancellation"
)

// Prefixes of document numbers (RENT-BE-001 §6).
var prefixes = map[Class]string{
	ClassRentCall: "AL", ClassReceipt: "QL", ClassRegularisation: "RG", ClassRevisionNotice: "RV",
	ClassContract: "CT", ClassAmendment: "AV", ClassDeposit: "DG", ClassTaxSummary: "RF", ClassSurety: "CA",
}

// Sources of a document (documents.source).
const (
	SourceGenerated = "generated"
	SourceUpload    = "upload"
)

// leaseBound classes keep their archive from the lease's end (RENT-SPEC-005):
// their retain_until stays unset until the lease ends (Q-68).
var leaseBound = map[Class]bool{ClassContract: true, ClassAmendment: true, ClassDeposit: true, ClassSurety: true,
	ClassInventory: true}

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
	// Kind is the fee line kind (rent, provision, regularisation) when the
	// line comes from a fee.
	Kind        string      `json:"kind,omitempty"`
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

// PaymentLine is a payment shown on a receipt.
type PaymentLine struct {
	PaidOn      period.Date
	Method      string
	AmountCents money.Cents
}

// ReceiptData is the data of a quittance or, with Partial, a partial receipt
// (template receipt). A quittance states rent and charges separately
// (RENT-SPEC-003, loi 89-462 art. 21).
type ReceiptData struct {
	Number         string
	IssuedOn       period.Date
	Owner          Party
	Tenants        []string
	Assets         []string
	Period         period.Period
	RentCallNumber string
	Partial        bool
	Lines          []Line
	TotalCents     money.Cents
	RentCents      money.Cents
	ChargesCents   money.Cents
	Payments       []PaymentLine
	PaidCents      money.Cents
	// ReceivedCents is what this partial receipt acknowledges.
	ReceivedCents    money.Cents
	OutstandingCents money.Cents
}

// ContractTenant is a tenant named in a contract.
type ContractTenant struct {
	Name     string
	Solidary bool
}

// ContractAsset is a let asset described in a contract.
type ContractAsset struct {
	Kind    string // house, apartment or box
	Address string
	Lot     string
	// SurfaceHundredthsM2 is the living area; zero for a box or when unknown.
	SurfaceHundredthsM2 int64
	DPEClass            string
}

// ContractDiagnostic is one line of the diagnostics file annexed to a contract.
type ContractDiagnostic struct {
	Asset             string
	Kind              string
	DoneOn            *period.Date
	ValidUntil        *period.Date
	Result            string
	NotRequiredReason string
}

// ContractGuarantee is a guarantee stated in a contract.
type ContractGuarantee struct {
	Kind        string // deposit, surety, visale or insurance
	Guarantor   string
	AmountCents money.Cents
	ValidTo     *period.Date
}

// ContractData is the data of a lease contract (template contract, one
// variant per lease kind, RENT-SPEC-008).
type ContractData struct {
	Number         string
	IssuedOn       period.Date
	Kind           string // unfurnished, furnished, mobility or box
	Owner          Party
	Tenants        []ContractTenant
	Assets         []ContractAsset
	StartOn        period.Date
	TermMonths     int
	RentCents      money.Cents
	ProvisionCents money.Cents
	DepositCents   money.Cents
	DueDay         int
	IRLRefQuarter  string
	IRLRefValue    int64
	RevisionMonth  int
	MobilityReason string
	// Controlled is set for a rent-controlled dwelling: the contract states
	// the ceiling and, when known, the previous tenant's rent (art. 3).
	Controlled        bool
	CeilingCents      money.Cents
	PreviousRentCents *money.Cents
	Guarantees        []ContractGuarantee
	Clauses           []string
	Diagnostics       []ContractDiagnostic
	Copropriete       bool
}

// AmendmentData is the data of an amendment (template amendment).
type AmendmentData struct {
	Number         string
	IssuedOn       period.Date
	Owner          Party
	Tenants        []string
	Assets         []string
	ContractNumber string
	LeaseStartOn   period.Date
	Kind           string // rent, tenant_add or asset_add
	EffectiveOn    period.Date
	OldRentCents   money.Cents
	NewRentCents   money.Cents
	TenantName     string
	AssetLabel     string
	Reason         string
}

// DepositReceiptData is the data of a deposit receipt (template
// deposit_receipt).
type DepositReceiptData struct {
	Number         string
	IssuedOn       period.Date
	Owner          Party
	Tenants        []string
	Assets         []string
	ContractNumber string
	AmountCents    money.Cents
	ReceivedOn     period.Date
	RentCents      money.Cents
	Furnished      bool
}

// SuretyData is the data of an acte de cautionnement (template surety), one
// per guarantor (RENT-SPEC-008).
type SuretyData struct {
	Number         string
	IssuedOn       period.Date
	Owner          Party
	Tenants        []string
	Assets         []string
	ContractNumber string
	Guarantor      Party
	LegalPerson    bool
	Solidary       bool
	MaxAmountCents money.Cents
	ValidTo        *period.Date
	StartOn        period.Date
	TermMonths     int
	RentCents      money.Cents
	ProvisionCents money.Cents
	IRLRefQuarter  string
	RevisionMonth  int
}

// RegularisationCharge is one charge line of a regularisation statement.
type RegularisationCharge struct {
	Category         string
	Label            string
	ChargedOn        period.Date
	AmountCents      money.Cents
	RecoverableCents money.Cents
}

// RegularisationData is the data of a charge regularisation statement, one
// per lease (template regularisation, RENT-SPEC-007 step 6).
type RegularisationData struct {
	Number           string
	IssuedOn         period.Date
	Owner            Party
	Tenants          []string
	Asset            string
	ExerciseStart    period.Date
	ExerciseEnd      period.Date
	Charges          []RegularisationCharge
	TotalCents       money.Cents
	RecoverableCents money.Cents
	ExerciseDays     int
	LeaseDays        int
	AllocatedCents   money.Cents
	ProvisionsCents  money.Cents
	BalanceCents     money.Cents
	// Settlement says how the balance is settled: fee (next rent calls),
	// deposit, direct.
	Settlement  string
	Spread      bool
	Instalments []money.Cents
	FirstPeriod period.Period
	// NewProvisionCents is the provision applied from the next month, when
	// the owner validated one.
	NewProvisionCents money.Cents
	SupportingUntil   period.Date
}

// TaxLine is one form line of a tax summary.
type TaxLine struct {
	Line        string
	AmountCents money.Cents
}

// TaxAsset is one asset of a tax summary, for the owner's share.
type TaxAsset struct {
	Label              string
	Furnished          bool
	Lines              []TaxLine
	NetCents           money.Cents
	GrossReceiptsCents money.Cents
}

// TaxSummaryData is the data of the annual tax summary (template
// tax_summary): both regimes side by side, the one issued, and the
// supporting documents (RENT-SPEC-004).
type TaxSummaryData struct {
	Number    string
	IssuedOn  period.Date
	Owner     Party
	TaxYear   int
	Millesime int
	Regime    string // micro or reel
	Assets    []TaxAsset
	Totals    []TaxLine
	// Micro-foncier check.
	MicroEligible       bool
	MicroGrossCents     money.Cents
	MicroAllowanceCents money.Cents
	MicroTaxableCents   money.Cents
	// Régime réel.
	ReelIncomeCents     money.Cents
	ReelExpensesCents   money.Cents
	ReelNetCents        money.Cents
	ReelTaxableCents    money.Cents
	GlobalImputable     money.Cents
	CarriedForward      []TaxDeficit
	FurnishedGrossCents money.Cents
	LockYears           int64
	Flags               int
	Supporting          []string
}

// TaxDeficit is a deficit carried forward.
type TaxDeficit struct {
	Year           int
	RemainingCents money.Cents
}
