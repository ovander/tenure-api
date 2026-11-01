package payment

import "testing"

func TestMatched(t *testing.T) {
	p := Payment{AmountCents: 100000, Matches: []Match{
		{Line: LineRent, AmountCents: 85000}, {Line: LineProvision, AmountCents: 9000}, {Line: LineRent, AmountCents: 1000},
	}}
	if p.Matched(LineRent) != 86000 || p.Matched(LineProvision) != 9000 || p.Matched(LineRegularisation) != 0 || p.Unmatched() != 5000 {
		t.Errorf("rent %d provision %d unmatched %d", p.Matched(LineRent), p.Matched(LineProvision), p.Unmatched())
	}
}
