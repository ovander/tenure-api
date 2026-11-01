package tax

import (
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/payment"
)

// SplitMatch splits an amount matched to a rent call between its lines in
// proportion to their amounts (OPEN-QUESTIONS Q-65): the rent part is
// matched × rent ÷ total, rounded half-up and never above the rent line; the
// provision part likewise; the rest is the regularisation part (which a
// negative credit line makes larger than the regularisation line itself, so
// that a credit never reduces the rent received). A call with no positive
// total carries no rent.
func SplitMatch(matched, rent, provision, total money.Cents) ([]payment.Match, error) {
	if matched == 0 {
		return nil, nil
	}
	if total <= 0 {
		return []payment.Match{{Line: payment.LineRegularisation, AmountCents: matched}}, nil
	}
	part := func(line money.Cents) (money.Cents, error) {
		if line <= 0 {
			return 0, nil
		}
		v, err := money.MulDivHalfUp(matched, int64(line), int64(total))
		if err != nil {
			return 0, err
		}
		return max(0, min(v, line)), nil
	}
	r, err := part(rent)
	if err != nil {
		return nil, err
	}
	p, err := part(provision)
	if err != nil {
		return nil, err
	}
	var out []payment.Match
	if r != 0 {
		out = append(out, payment.Match{Line: payment.LineRent, AmountCents: r})
	}
	if p != 0 {
		out = append(out, payment.Match{Line: payment.LineProvision, AmountCents: p})
	}
	if rest := matched - r - p; rest != 0 {
		out = append(out, payment.Match{Line: payment.LineRegularisation, AmountCents: rest})
	}
	return out, nil
}
