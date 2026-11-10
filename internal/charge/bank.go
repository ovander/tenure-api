package charge

import (
	"sort"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Rules a bank debit's charge suggestion rests on (RENT-SPEC-013, Q-125).
const (
	RuleStatement  = "statement"   // every unpaid charge of one statement (asset, day) adding up to the debit
	RuleAmountDate = "amount_date" // one unpaid charge of the debit's amount, near its day
)

// OpenCharge is a confirmed charge not yet paid through a bank line.
type OpenCharge struct {
	ID          uuid.UUID   `json:"id"`
	AssetID     uuid.UUID   `json:"asset_id"`
	ChargedOn   period.Date `json:"charged_on" swaggertype:"string" format:"date"`
	Label       string      `json:"label"`
	AmountCents money.Cents `json:"amount_cents"`
}

// Suggestion is one or more charges a bank debit may pay, and the rule
// it rests on.
type Suggestion struct {
	Rule    string       `json:"rule" enums:"statement,amount_date"`
	Charges []OpenCharge `json:"charges"`
}

// SuggestCharges lists the charges a bank debit may pay (RENT-SPEC-013,
// Q-125): every unpaid charge of one statement (the same asset and day, as
// a syndic import gives) whose total is the debit, when there are two or
// more; then each unpaid charge of the debit's amount. Either is charged at
// most WindowDays from the debit's day. A credit has none. Pure.
func SuggestCharges(day period.Date, amount money.Cents, open []OpenCharge, r params.BankCharges) []Suggestion {
	out := []Suggestion{}
	if amount >= 0 {
		return out
	}
	want := -amount
	near := func(c OpenCharge) bool {
		return !c.ChargedOn.Before(day.AddDays(-r.WindowDays)) && !c.ChargedOn.After(day.AddDays(r.WindowDays))
	}
	type key struct {
		asset uuid.UUID
		on    period.Date
	}
	groups := map[key][]OpenCharge{}
	var order []key
	for _, c := range open {
		if !near(c) {
			continue
		}
		k := key{c.AssetID, c.ChargedOn}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], c)
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].on.Before(order[j].on) })
	for _, k := range order {
		g := groups[k]
		var sum money.Cents
		for _, c := range g {
			sum += c.AmountCents
		}
		if len(g) > 1 && sum == want {
			out = append(out, Suggestion{Rule: RuleStatement, Charges: g})
		}
	}
	for _, k := range order {
		for _, c := range groups[k] {
			if c.AmountCents == want {
				out = append(out, Suggestion{Rule: RuleAmountDate, Charges: []OpenCharge{c}})
			}
		}
	}
	return out
}
