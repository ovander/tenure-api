package lease

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Who gives a notice (RENT-SPEC-010).
const (
	GivenByTenant = "tenant"
	GivenByOwner  = "owner"
)

// Owner notice grounds (loi 89-462 art. 15 I); the legal list lives in
// legal_params (notice.reasons).
const (
	OwnerSale         = "sale"
	OwnerRepossession = "repossession"
	OwnerLegitimate   = "legitimate"
)

// ReasonTenseZone is the tenant's reduced-notice ground of a lease in a zone
// tendue, which applies whatever the tenant writes when the asset is flagged.
const ReasonTenseZone = "tense_zone"

// How a notice reached the other party (loi 89-462 art. 15 I).
const (
	DeliveryRegistered = "registered_letter"
	DeliveryBailiff    = "bailiff"
	DeliveryHand       = "hand_delivery"
)

// The owner's answer about a protected tenant (loi 89-462 art. 15 III: over
// 65 with resources under the ceiling): not protected, or protected with a
// rehousing offer; a protected tenant without one cannot be given notice.
const (
	ProtectedNo        = "no"
	ProtectedRehousing = "rehousing_offered"
	ProtectedYes       = "yes"
)

// Problem types of a notice.
const (
	TypeNoticeInvalid       = "lease/notice-invalid"
	TypeNoticeTooLate       = "lease/notice-too-late"
	TypeNoticeNotAllowed    = "lease/notice-not-allowed"
	TypeNoticeProtected     = "lease/notice-protected-tenant"
	TypeNoticeExists        = "lease/notice-exists"
	TypeNoticeLetterMissing = "lease/notice-letter-missing"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeNoticeInvalid:       problem.M(422, "Le congé est incomplet ou son motif n’est pas reconnu.", "The notice is incomplete or its ground is not recognised."),
		TypeNoticeTooLate:       problem.M(422, "Le congé du bailleur arrive trop tard pour ce terme.", "The owner's notice arrives too late for this term."),
		TypeNoticeNotAllowed:    problem.M(422, "Ce bail ne permet pas ce congé.", "This lease does not allow this notice."),
		TypeNoticeProtected:     problem.M(422, "Un locataire protégé ne peut recevoir congé sans offre de relogement.", "A protected tenant cannot be given notice without a rehousing offer."),
		TypeNoticeExists:        problem.M(409, "Un congé est déjà enregistré pour ce bail.", "A notice is already recorded for this lease."),
		TypeNoticeLetterMissing: problem.M(422, "Le congé du bailleur doit reprendre une lettre de congé générée pour ce bail.", "The owner's notice needs a notice letter generated for this lease."),
	})
}

// Beneficiary is who takes the home back in a repossession.
type Beneficiary struct {
	Name         string `json:"name"`
	Relationship string `json:"relationship"`
	Address      string `json:"address"`
}

// NoticeGround is what a notice states: the tenant's reduced-notice ground,
// or the owner's ground with what the law requires of it.
type NoticeGround struct {
	// Reason is a reduced-notice ground of the tenant (empty for the
	// standard period), or the owner's ground: sale, repossession, legitimate.
	Reason string `json:"reason,omitempty"`
	// PriceCents and Conditions are the offer of a sale, which is worth an
	// offer to the tenant.
	PriceCents money.Cents `json:"price_cents,omitempty"`
	Conditions string      `json:"conditions,omitempty"`
	// Beneficiary is the person who takes the home back.
	Beneficiary *Beneficiary `json:"beneficiary,omitempty"`
	// Grounds states the legitimate and serious ground.
	Grounds string `json:"grounds,omitempty"`
	// Protected is the owner's answer about a protected tenant.
	Protected string `json:"protected_tenant,omitempty"`
}

// NoticeFacts is the lease a notice applies to.
type NoticeFacts struct {
	Kind       Kind
	StartOn    period.Date
	TermMonths int
	TenseZone  bool
}

// NoticeTerms is what a notice computes.
type NoticeTerms struct {
	// Months is the notice period applied.
	Months int `json:"months"`
	// EndOn is the last day of the lease.
	EndOn period.Date `json:"end_on" swaggertype:"string" format:"date"`
	// LatestReceiptOn is, for the owner, the last day the tenant may receive
	// the notice for EndOn.
	LatestReceiptOn *period.Date `json:"latest_receipt_on,omitempty" swaggertype:"string" format:"date"`
	// Reduced says a tenant's notice is the reduced one.
	Reduced bool `json:"reduced,omitempty"`
	// Warnings are what the owner should check (a zone tendue ground on an
	// asset not flagged so).
	Warnings []string `json:"warnings"`
}

// TermEnd is the last day of the term running on day: the day before the
// start plus a whole number of terms, renewed tacitly (a mobility lease has
// one term only). False without a term, or after a mobility lease's term.
func TermEnd(start period.Date, termMonths int, kind Kind, day period.Date) (period.Date, bool) {
	if termMonths <= 0 || start.IsZero() {
		return period.Date{}, false
	}
	for k := 1; k <= 200; k++ {
		end := start.AddMonthsClamped(termMonths * k).AddDays(-1)
		if !end.Before(day) {
			return end, true
		}
		if kind == KindMobility {
			return period.Date{}, false
		}
	}
	return period.Date{}, false
}

// latestReceipt is the last day an owner's notice may be received for a
// term ending on end: the day before end+1 day minus the lead time, the
// stricter reading of « au moins six mois avant le terme » (Q-118).
func latestReceipt(end period.Date, months int) period.Date {
	return end.AddDays(1).AddMonthsClamped(-months).AddDays(-1)
}

// noticeEnd is the last day of a period of months running from receivedOn:
// the day before the same day of the month, months later; when that month is
// too short for the day, its last day (Q-118).
func noticeEnd(receivedOn period.Date, months int) period.Date {
	same := receivedOn.AddMonthsClamped(months)
	if same.Day != receivedOn.Day {
		return same
	}
	return same.AddDays(-1)
}

// TenantNotice computes a tenant's notice received on receivedOn (RENT-SPEC-010,
// loi 89-462 art. 15 I, 25-8, 25-12): the period by lease kind, reduced on
// an unfurnished lease for a listed ground or a zone tendue; the lease ends
// on noticeEnd (Q-118). A box
// is not under loi 89-462: its notice is recorded with an end date by hand.
func TenantNotice(f NoticeFacts, g NoticeGround, receivedOn period.Date, r params.NoticeRules) (NoticeTerms, error) {
	t := NoticeTerms{Warnings: []string{}}
	switch f.Kind {
	case KindFurnished:
		t.Months = r.Tenant.Furnished
	case KindMobility:
		t.Months = r.Tenant.Mobility
	case KindUnfurnished:
		t.Months = r.Tenant.Unfurnished
		if g.Reason != "" && !slices.Contains(r.Reasons.TenantReduced, g.Reason) {
			return NoticeTerms{}, invariant.New(TypeNoticeInvalid, "reason "+g.Reason)
		}
		if g.Reason != "" || f.TenseZone {
			t.Months, t.Reduced = r.Tenant.Reduced, true
		}
		if g.Reason == ReasonTenseZone && !f.TenseZone {
			t.Warnings = append(t.Warnings, "tense_zone_not_flagged")
		}
	default:
		return NoticeTerms{}, invariant.New(TypeNoticeNotAllowed, string(f.Kind))
	}
	if receivedOn.IsZero() || receivedOn.Before(f.StartOn) || t.Months <= 0 {
		return NoticeTerms{}, invariant.New(TypeNoticeInvalid, "received_on")
	}
	t.EndOn = noticeEnd(receivedOn, t.Months)
	return t, nil
}

// OwnerNotice computes an owner's notice (RENT-SPEC-010, loi 89-462 art. 15
// I and III, 25-8): only for an unfurnished or furnished lease, on one of
// the legal grounds with what each requires, never to a protected tenant
// without a rehousing offer, for the end of the current term when it is
// received in time. With receivedOn zero it gives the next term the notice
// can still reach, and the last day to deliver it, as a letter is prepared.
func OwnerNotice(f NoticeFacts, g NoticeGround, receivedOn, today period.Date, r params.NoticeRules) (NoticeTerms, error) {
	t := NoticeTerms{Warnings: []string{}}
	switch f.Kind {
	case KindUnfurnished:
		t.Months = r.Owner.Unfurnished
	case KindFurnished:
		t.Months = r.Owner.Furnished
	default:
		return NoticeTerms{}, invariant.New(TypeNoticeNotAllowed, string(f.Kind))
	}
	if err := ownerGround(g, r); err != nil {
		return NoticeTerms{}, err
	}
	on := receivedOn
	if on.IsZero() {
		on = today
	}
	if on.Before(f.StartOn) || t.Months <= 0 {
		return NoticeTerms{}, invariant.New(TypeNoticeInvalid, "received_on")
	}
	end, ok := TermEnd(f.StartOn, f.TermMonths, f.Kind, on)
	if !ok {
		return NoticeTerms{}, invariant.New(TypeNoticeNotAllowed, "no term")
	}
	latest := latestReceipt(end, t.Months)
	if on.After(latest) {
		next, _ := TermEnd(f.StartOn, f.TermMonths, f.Kind, end.AddDays(1))
		if !receivedOn.IsZero() {
			return NoticeTerms{}, invariant.New(TypeNoticeTooLate, fmt.Sprintf("received %s after %s; next term ends %s", receivedOn, latest, next))
		}
		end, latest = next, latestReceipt(next, t.Months)
	}
	t.EndOn, t.LatestReceiptOn = end, &latest
	return t, nil
}

func ownerGround(g NoticeGround, r params.NoticeRules) error {
	if !slices.Contains(r.Reasons.Owner, g.Reason) {
		return invariant.New(TypeNoticeInvalid, "reason "+g.Reason)
	}
	switch g.Protected {
	case ProtectedNo, ProtectedRehousing:
	case ProtectedYes:
		return invariant.New(TypeNoticeProtected, "protected_tenant")
	default:
		return invariant.New(TypeNoticeInvalid, "protected_tenant")
	}
	switch g.Reason {
	case OwnerSale:
		if g.PriceCents <= 0 {
			return invariant.New(TypeNoticeInvalid, "price_cents")
		}
	case OwnerRepossession:
		b := g.Beneficiary
		if b == nil || strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.Address) == "" ||
			!slices.Contains(r.Reasons.RepossessionBeneficiaries, b.Relationship) {
			return invariant.New(TypeNoticeInvalid, "beneficiary")
		}
	case OwnerLegitimate:
		if strings.TrimSpace(g.Grounds) == "" {
			return invariant.New(TypeNoticeInvalid, "grounds")
		}
	}
	return nil
}
