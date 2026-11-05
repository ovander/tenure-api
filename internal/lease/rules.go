package lease

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the contract, guarantee and activation rules
// (RENT-SPEC-008). Blockers of generation and activation are listed by
// GET /leases/{id}/blockers with these types; warnings never block.
const (
	TypeDepositAboveCap       = "lease/deposit-above-cap"
	TypeDepositNotAllowed     = "lease/deposit-not-allowed"
	TypeMobilityTerm          = "lease/mobility-term"
	TypeMobilityReasonMissing = "lease/mobility-reason-missing"
	TypeMobilityReasonUnknown = "lease/mobility-reason-unknown"
	TypeDiagnosticMissing     = "lease/diagnostic-missing"
	TypeDiagnosticExpired     = "lease/diagnostic-expired"
	TypeDPEClassMissing       = "lease/dpe-class-missing"
	TypeDPEClassForbidden     = "lease/dpe-class-forbidden"
	TypeOwnerIncomplete       = "lease/owner-profile-incomplete"
	TypeClauseNotVetted       = "lease/clause-not-vetted"
	TypeContractNotDraft      = "lease/contract-not-draft"
	TypeContractMissing       = "lease/contract-missing"
	TypeContractNotSigned     = "lease/contract-not-signed"
	TypeEntryInventoryMissing = "lease/entry-inventory-missing"
	TypeAmendmentInvalid      = "lease/amendment-invalid"
	TypeAmendmentState        = "lease/amendment-not-current"
	TypeSuretyWithInsurance   = "guarantee/surety-with-insurance"
	TypeVisaleWithSurety      = "guarantee/visale-with-surety"
	TypeGuarantorIncomplete   = "guarantee/guarantor-incomplete"
	TypeGuaranteeNoDocument   = "guarantee/no-document-for-kind"
	TypeGuaranteeDocument     = "guarantee/document-invalid"
	TypeInventoryExists       = "inventory/already-recorded"
	TypeInventoryDocument     = "inventory/document-invalid"
	TypeInventoryState        = "inventory/not-allowed-in-state"
	TypeInventoryItemsRetired = "inventory/items-field-retired"
	// Warnings (RENT-SPEC-008, Q-26).
	WarnBoxDeposit    = "lease/box-deposit-high"
	WarnSuretyExpired = "guarantee/surety-expired"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeDepositAboveCap:       problem.M(422, "Le dépôt de garantie dépasse le plafond légal pour ce type de bail.", "The deposit exceeds the legal cap for this lease kind."),
		TypeDepositNotAllowed:     problem.M(422, "Aucun dépôt de garantie ne peut être exigé pour ce type de bail.", "No deposit may be required for this lease kind."),
		TypeMobilityTerm:          problem.M(422, "La durée d’un bail mobilité est hors des limites légales.", "The term of a mobility lease is outside the legal limits."),
		TypeMobilityReasonMissing: problem.M(422, "Le motif du bail mobilité est obligatoire.", "The reason for a mobility lease is required."),
		TypeMobilityReasonUnknown: problem.M(422, "Motif de bail mobilité inconnu.", "Unknown mobility lease reason."),
		TypeDiagnosticMissing:     problem.M(409, "Un diagnostic obligatoire manque pour un bien du bail.", "A mandatory diagnostic is missing for an asset of the lease."),
		TypeDiagnosticExpired:     problem.M(409, "Un diagnostic d’un bien du bail n’est plus valide.", "A diagnostic of an asset of the lease is no longer valid."),
		TypeDPEClassMissing:       problem.M(409, "La classe énergétique du logement est inconnue.", "The dwelling's energy class is unknown."),
		TypeDPEClassForbidden:     problem.M(409, "La classe énergétique du logement ne permet pas de le louer.", "The dwelling's energy class does not allow it to be let."),
		TypeOwnerIncomplete:       problem.M(409, "Le profil du bailleur est incomplet (nom et adresse).", "The owner profile is incomplete (name and address)."),
		TypeClauseNotVetted:       problem.M(422, "Une clause particulière ne fait pas partie de la liste validée.", "A special clause is not in the vetted list."),
		TypeContractNotDraft:      problem.M(409, "Le contrat se génère sur un bail en brouillon ; utilisez un avenant.", "The contract is generated for a draft lease; use an amendment."),
		TypeContractMissing:       problem.M(409, "Aucun contrat n’a été généré pour la version actuelle du bail et de ses garanties.", "No contract was generated for the current version of the lease and its guarantees."),
		TypeContractNotSigned:     problem.M(409, "La copie signée du contrat n’a pas été déposée.", "The signed copy of the contract has not been uploaded."),
		TypeEntryInventoryMissing: problem.M(409, "L’état des lieux d’entrée n’a pas été déposé.", "The entry inventory has not been uploaded."),
		TypeAmendmentInvalid:      problem.M(422, "L’avenant demandé n’est pas valide.", "The requested amendment is not valid."),
		TypeAmendmentState:        problem.M(409, "Un avenant ne concerne qu’un bail en cours.", "An amendment applies only to a current lease."),
		TypeSuretyWithInsurance:   problem.M(409, "Une caution ne peut pas s’ajouter à une assurance loyers impayés, sauf pour un étudiant ou un apprenti.", "A surety cannot be combined with a rent insurance, except for a student or apprentice."),
		TypeVisaleWithSurety:      problem.M(409, "La garantie Visale remplace la caution : elles ne se cumulent pas.", "A Visale guarantee replaces a surety: they cannot be combined."),
		TypeGuarantorIncomplete:   problem.M(422, "Le garant doit avoir un nom et une adresse.", "The guarantor needs a name and an address."),
		TypeGuaranteeNoDocument:   problem.M(409, "Aucun document n’est généré pour cette garantie dans son état.", "No document is generated for this guarantee in its state."),
		TypeGuaranteeDocument:     problem.M(422, "Le document joint n’existe pas ou ne concerne pas ce bail.", "The attached document does not exist or does not concern this lease."),
		TypeInventoryExists:       problem.M(409, "Cet état des lieux est déjà enregistré.", "This inventory is already recorded."),
		TypeInventoryDocument:     problem.M(422, "L’état des lieux doit être un document déposé, de classe inventory, de ce bail.", "The inventory must be an uploaded document of class inventory for this lease."),
		TypeInventoryState:        problem.M(409, "Cet état des lieux n’est pas possible dans l’état actuel du bail.", "This inventory is not possible in the lease's current state."),
		TypeInventoryItemsRetired: problem.M(422, "Les éléments ne se déclarent plus en JSON libre : ils se saisissent dans l’état des lieux.", "Items are no longer free JSON: they are captured in the inventory."),
		WarnBoxDeposit:            problem.M(200, "Le dépôt de garantie du box dépasse deux mois de loyer.", "The box deposit exceeds two months of rent."),
		WarnSuretyExpired:         problem.M(200, "Une caution est expirée ou révoquée.", "A surety has expired or been revoked."),
	})
}

// MobilityReason is the tenant's reason for a bail mobilité (loi 89-462
// art. 25-12).
type MobilityReason string

// Mobility reasons.
const (
	ReasonVocationalTraining  MobilityReason = "vocational_training"
	ReasonHigherEducation     MobilityReason = "higher_education"
	ReasonApprenticeship      MobilityReason = "apprenticeship"
	ReasonInternship          MobilityReason = "internship"
	ReasonCivicService        MobilityReason = "civic_service"
	ReasonJobTransfer         MobilityReason = "job_transfer"
	ReasonTemporaryAssignment MobilityReason = "temporary_assignment"
)

// MobilityReasons lists every mobility reason.
var MobilityReasons = []MobilityReason{ReasonVocationalTraining, ReasonHigherEducation, ReasonApprenticeship, ReasonInternship,
	ReasonCivicService, ReasonJobTransfer, ReasonTemporaryAssignment}

// VettedClauses are the special clauses a contract may carry; their French
// text lives in the contract template (RENT-SPEC-008).
var VettedClauses = []string{"boiler_servicing", "chimney_sweeping", "garden_upkeep", "insurance_certificate"}

// Clause is one special clause of a lease, picked by code from VettedClauses.
type Clause struct {
	Code string `json:"code"`
}

// Clauses decodes a lease's clauses and returns the codes, or the blocker of
// the first one that is not vetted.
func Clauses(raw json.RawMessage) ([]string, *invariant.Error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var list []Clause
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, invariant.New(TypeClauseNotVetted, "clauses must be [{\"code\": …}]")
	}
	var out []string
	for _, c := range list {
		vetted := false
		for _, v := range VettedClauses {
			vetted = vetted || v == c.Code
		}
		if !vetted {
			return nil, invariant.New(TypeClauseNotVetted, c.Code)
		}
		out = append(out, c.Code)
	}
	return out, nil
}

// depositKey is the legal_params key of the deposit cap of a lease kind; a
// box let alone has none (Q-26).
func depositKey(k Kind) (string, bool) {
	switch k {
	case KindUnfurnished:
		return params.KeyDepositMaxUnfurnished, true
	case KindFurnished:
		return params.KeyDepositMaxFurnished, true
	case KindMobility:
		return params.KeyDepositMaxMobility, true
	}
	return "", false
}

// DepositCap returns the highest deposit of a lease of kind k at rent (rent
// excluding charges): deposit_max_months × rent; capped is false for a box
// let alone, which has no legal cap (Q-26).
func DepositCap(k Kind, rent money.Cents, asOf period.Date, ps params.Set) (limit money.Cents, capped bool, err error) {
	key, ok := depositKey(k)
	if !ok {
		return 0, false, nil
	}
	months, err := ps.Int64(key, asOf)
	if err != nil {
		return 0, false, err
	}
	return rent * money.Cents(months), true, nil
}

// CheckDeposit returns the rule a deposit of amount breaks for a lease of
// kind k at rent, or nil (RENT-SPEC-008: ≤ 1 month unfurnished, ≤ 2 months
// furnished, none for a mobility lease, from legal_params).
func CheckDeposit(k Kind, rent, amount money.Cents, asOf period.Date, ps params.Set) (*invariant.Error, error) {
	limit, capped, err := DepositCap(k, rent, asOf, ps)
	if err != nil || !capped || amount <= limit {
		return nil, err
	}
	if limit == 0 {
		return invariant.New(TypeDepositNotAllowed, fmt.Sprintf("deposit %d for a %s lease", amount, k)), nil
	}
	return invariant.New(TypeDepositAboveCap, fmt.Sprintf("deposit %d above cap %d", amount, limit)), nil
}

// GuaranteeFact is what the combination rules read of a guarantee.
type GuaranteeFact struct {
	Kind        GuaranteeKind
	Status      GuaranteeStatus
	AmountCents money.Cents
}

// live reports whether a guarantee is in force (held or valid).
func (g GuaranteeFact) live() bool { return g.Status == StatusHeld || g.Status == StatusValid }

// CheckGuarantees returns every combination rule the lease's guarantees
// break (RENT-SPEC-008): a surety cannot be combined with a rent insurance
// unless every tenant is a student or an apprentice; a Visale guarantee
// replaces a surety. When checkDeposit is set, the deposits in force must
// also respect the cap at the lease's rent; it is not set when a deposit
// already held is left as it is, since a rent change never changes it.
func CheckGuarantees(l Lease, gs []GuaranteeFact, studentsOnly, checkDeposit bool, ps params.Set) ([]*invariant.Error, error) {
	var out []*invariant.Error
	has := map[GuaranteeKind]bool{}
	var deposits money.Cents
	for _, g := range gs {
		if g.live() {
			has[g.Kind] = true
			if g.Kind == GuaranteeDeposit {
				deposits += g.AmountCents
			}
		}
	}
	if has[GuaranteeSurety] && has[GuaranteeInsurance] && !studentsOnly {
		out = append(out, invariant.New(TypeSuretyWithInsurance, ""))
	}
	if has[GuaranteeSurety] && has[GuaranteeVisale] {
		out = append(out, invariant.New(TypeVisaleWithSurety, ""))
	}
	if checkDeposit {
		e, err := CheckDeposit(l.Kind, l.RentCents, deposits, l.StartOn, ps)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// Family is the diagnostics family of an asset: "box" or "dwelling".
func Family(a portfolio.AssetView) string {
	if a.Kind == portfolio.KindBox {
		return "box"
	}
	return "dwelling"
}

// Expiry returns the last day a diagnostic is valid, and false when it does
// not expire: the earlier of the date its report states and its legal
// validity from legal_params; a DPE made before dpe_min_done_on is void.
func Expiry(d portfolio.DiagnosticView, rule params.Diagnostics) (period.Date, bool) {
	if d.DoneOn == nil {
		return period.Date{}, false
	}
	if d.Kind == portfolio.DiagnosticDPE && !rule.DPEMinDoneOn.IsZero() && d.DoneOn.Before(rule.DPEMinDoneOn) {
		return d.DoneOn.AddDays(-1), true
	}
	var exp period.Date
	expires := false
	if m := rule.ValidityMonths[string(d.Kind)]; m > 0 {
		exp, expires = d.DoneOn.AddMonths(m).AddDays(-1), true
	}
	if d.ValidUntil != nil && (!expires || d.ValidUntil.Before(exp)) {
		exp, expires = *d.ValidUntil, true
	}
	return exp, expires
}

// worse returns the worse of two DPE classes ("" when unknown).
func worse(a, b string) string {
	if strings.Index(portfolio.DPEClasses, a) > strings.Index(portfolio.DPEClasses, b) {
		return a
	}
	return b
}

// CheckDiagnostics returns the diagnostic blockers of the lease's assets on
// date on (RENT-SPEC-008: a missing mandatory diagnostic, one past its
// validity, a dwelling without a DPE class or below the minimum class to be
// let). A kind the owner declared not required is accepted (Q-23).
func CheckDiagnostics(assets []portfolio.AssetView, diags []portfolio.DiagnosticView, on period.Date, ps params.Set) ([]*invariant.Error, error) {
	rule, err := ps.Diagnostics(on)
	if err != nil {
		return nil, err
	}
	minClass, err := ps.MinDPEClass(on)
	if err != nil {
		return nil, err
	}
	var out []*invariant.Error
	for _, a := range assets {
		byKind := map[portfolio.DiagnosticKind]portfolio.DiagnosticView{}
		for _, d := range diags {
			if d.AssetID == a.ID {
				byKind[d.Kind] = d
			}
		}
		for _, k := range rule.Required[Family(a)] {
			d, ok := byKind[portfolio.DiagnosticKind(k)]
			switch {
			case !ok:
				out = append(out, invariant.New(TypeDiagnosticMissing, fmt.Sprintf("asset %s: %s", a.ID, k)).
					About(invariant.Subject{AssetID: a.ID.String(), Diagnostic: k}))
				continue
			case d.NotRequiredReason != "":
				continue
			}
			if exp, expires := Expiry(d, rule); expires && exp.Before(on) {
				out = append(out, invariant.New(TypeDiagnosticExpired, fmt.Sprintf("asset %s: %s valid until %s", a.ID, k, exp)).
					About(invariant.Subject{AssetID: a.ID.String(), Diagnostic: k, ValidUntil: exp.String()}))
			}
		}
		if Family(a) != "dwelling" {
			continue
		}
		class := ""
		if d, ok := byKind[portfolio.DiagnosticDPE]; ok && d.NotRequiredReason == "" {
			class = d.Result
		}
		if a.DPEClass != nil && *a.DPEClass != "" {
			class = worse(class, strings.ToUpper(*a.DPEClass))
		}
		switch {
		case class == "":
			out = append(out, invariant.New(TypeDPEClassMissing, a.ID.String()).
				About(invariant.Subject{AssetID: a.ID.String(), Diagnostic: string(portfolio.DiagnosticDPE)}))
		case strings.Index(portfolio.DPEClasses, class) > strings.Index(portfolio.DPEClasses, minClass):
			out = append(out, invariant.New(TypeDPEClassForbidden, fmt.Sprintf("asset %s: class %s, minimum %s", a.ID, class, minClass)).
				About(invariant.Subject{AssetID: a.ID.String(), Diagnostic: string(portfolio.DiagnosticDPE)}))
		}
	}
	return out, nil
}

// Warnings returns what the owner view flags without blocking: a box deposit
// above deposit_warning_months.box months of rent (Q-26), and a surety
// expired or revoked (RENT-SPEC-008).
func Warnings(l Lease, gs []GuaranteeFact, validTo []*period.Date, today period.Date, ps params.Set) ([]*invariant.Error, error) {
	var out []*invariant.Error
	if l.Kind == KindBox {
		months, err := ps.Int64(params.KeyDepositWarningBox, l.StartOn)
		if err != nil {
			return nil, err
		}
		if l.DepositCents > l.RentCents*money.Cents(months) {
			out = append(out, invariant.New(WarnBoxDeposit, fmt.Sprintf("deposit %d above %d months of rent", l.DepositCents, months)))
		}
	}
	for i, g := range gs {
		if g.Kind != GuaranteeSurety {
			continue
		}
		if g.Status == StatusExpired || (i < len(validTo) && validTo[i] != nil && validTo[i].Before(today)) {
			out = append(out, invariant.New(WarnSuretyExpired, ""))
		}
	}
	return out, nil
}
