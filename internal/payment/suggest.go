package payment

import (
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Rules a suggestion rests on, strongest first (RENT-SPEC-013, Q-124).
const (
	RuleReference    = "reference"     // the rent call's number in the label or reference, the amount owed
	RuleNameAmount   = "name_amount"   // a tenant's name in the label, the amount owed
	RuleAllowance    = "allowance"     // a housing allowance (CAF, MSA) naming the tenant, at most the amount owed
	RuleAmountPeriod = "amount_period" // the amount owed, booked near the call's due day
)

var ruleRank = map[string]int{RuleReference: 0, RuleNameAmount: 1, RuleAllowance: 2, RuleAmountPeriod: 3}

// OpenCall is a live rent call with something left to pay, and what a bank
// line can be recognised by: its number and the lease's tenants.
type OpenCall struct {
	BillingDocumentID uuid.UUID   `json:"billing_document_id"`
	LeaseID           uuid.UUID   `json:"lease_id"`
	Number            string      `json:"number"`
	Period            string      `json:"period"`
	DueOn             period.Date `json:"due_on" swaggertype:"string" format:"date"`
	OutstandingCents  money.Cents `json:"outstanding_cents"`
	Tenants           []string    `json:"tenants"`
}

// Suggestion is a rent call a credit line may pay, the amount and method
// that would be recorded, and the rule it rests on.
type Suggestion struct {
	LeaseID           uuid.UUID   `json:"lease_id"`
	BillingDocumentID uuid.UUID   `json:"billing_document_id"`
	Number            string      `json:"number"`
	Period            string      `json:"period"`
	AmountCents       money.Cents `json:"amount_cents"`
	Method            Method      `json:"method" enums:"transfer,caf"`
	Rule              string      `json:"rule" enums:"reference,name_amount,allowance,amount_period"`
	Tenants           []string    `json:"tenants"`
}

// fold upper-cases a text and drops its accents and punctuation, so « Léa
// MARTIN-Durand » and « LEA MARTIN DURAND » read the same.
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	out = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToUpper(r)
		}
		return ' '
	}, out)
	return " " + strings.Join(strings.Fields(out), " ") + " "
}

// names says whether a folded label holds a tenant's name: every word of
// MinNameLength letters or more of one of the tenant's names.
func names(label string, tenants []string, minLen int) bool {
	for _, n := range tenants {
		words := strings.Fields(fold(n))
		found, any := true, false
		for _, w := range words {
			if len([]rune(w)) < minLen {
				continue
			}
			any = true
			if !strings.Contains(label, " "+w+" ") {
				found = false
				break
			}
		}
		if any && found {
			return true
		}
	}
	return false
}

// Suggest lists the rent calls a credit bank line may pay (RENT-SPEC-013,
// Q-124), strongest rule first, one suggestion per call: the call's number
// in the label or reference and the amount owed; a tenant's name and the
// amount owed; a housing allowance (a keyword of AllowanceKeywords) naming
// a tenant, for at most what the call owes; the amount owed, booked within
// WindowDays of the call's due day. A debit, or a line that fits nothing,
// has no suggestion. Pure.
func Suggest(l BankLine, calls []OpenCall, r params.BankMatching) []Suggestion {
	out := []Suggestion{}
	if l.AmountCents <= 0 {
		return out
	}
	text := fold(l.Label + " " + l.Reference)
	allowance := false
	for _, k := range r.AllowanceKeywords {
		if strings.Contains(text, fold(k)) {
			allowance = true
		}
	}
	for _, c := range calls {
		if c.OutstandingCents <= 0 {
			continue
		}
		s := Suggestion{LeaseID: c.LeaseID, BillingDocumentID: c.BillingDocumentID, Number: c.Number, Period: c.Period,
			AmountCents: l.AmountCents, Method: "transfer", Tenants: c.Tenants}
		exact := l.AmountCents == c.OutstandingCents
		named := names(text, c.Tenants, r.MinNameLength)
		switch {
		case exact && c.Number != "" && strings.Contains(text, fold(c.Number)):
			s.Rule = RuleReference
		case allowance && named && l.AmountCents <= c.OutstandingCents:
			s.Rule, s.Method = RuleAllowance, "caf"
		case exact && named:
			s.Rule = RuleNameAmount
		case exact && !l.BookedOn.Before(c.DueOn.AddDays(-r.WindowDays)) && !l.BookedOn.After(c.DueOn.AddDays(r.WindowDays)):
			s.Rule = RuleAmountPeriod
		default:
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ruleRank[out[i].Rule] != ruleRank[out[j].Rule] {
			return ruleRank[out[i].Rule] < ruleRank[out[j].Rule]
		}
		return out[i].Period < out[j].Period
	})
	return out
}

// Sure returns the one suggestion a line can be matched on without asking
// the owner: a single suggestion on the reference, the name and amount, or
// an allowance naming the tenant, and no other suggestion on the same rule.
// The amount and period rule alone is never sure. Pure.
func Sure(s []Suggestion) (Suggestion, bool) {
	if len(s) == 0 || s[0].Rule == RuleAmountPeriod {
		return Suggestion{}, false
	}
	if len(s) > 1 && s[1].Rule == s[0].Rule && s[1].LeaseID != s[0].LeaseID {
		return Suggestion{}, false
	}
	return s[0], true
}
