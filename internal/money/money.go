// Package money holds the integer-cent amount type and the rounding helpers
// every calculation uses (RENT-BE-001 §7). There is no floating point in this
// package: products are computed exactly with math/big before rounding.
package money

import (
	"errors"
	"math/big"
)

// Cents is an amount of money in euro cents. Negative values are credits.
type Cents int64

// ErrZeroDenominator is returned by the division helpers for a zero or
// negative denominator.
var ErrZeroDenominator = errors.New("money: denominator must be positive")

// MulDivHalfUp returns a × num ÷ den rounded to the nearest cent, halves away
// from zero, so a credit rounds like the matching debit (OPEN-QUESTIONS Q-18).
// den must be positive.
func MulDivHalfUp(a Cents, num, den int64) (Cents, error) {
	if den <= 0 {
		return 0, ErrZeroDenominator
	}
	p := new(big.Int).Mul(big.NewInt(int64(a)), big.NewInt(num))
	neg := p.Sign() < 0
	p.Abs(p)
	d := big.NewInt(den)
	q, r := new(big.Int).QuoRem(p, d, new(big.Int))
	if new(big.Int).Lsh(r, 1).Cmp(d) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return Cents(q.Int64()), nil
}

// MulDivFloor returns a × num ÷ den rounded towards minus infinity. den must
// be positive.
func MulDivFloor(a Cents, num, den int64) (Cents, error) {
	if den <= 0 {
		return 0, ErrZeroDenominator
	}
	p := new(big.Int).Mul(big.NewInt(int64(a)), big.NewInt(num))
	q, m := new(big.Int).DivMod(p, big.NewInt(den), new(big.Int)) // Euclidean: m >= 0
	_ = m
	return Cents(q.Int64()), nil
}

// CeilToEuro rounds c up to the next whole euro (a multiple of 100 cents);
// whole euros are returned unchanged.
func CeilToEuro(c Cents) Cents {
	r := c % 100
	if r == 0 {
		return c
	}
	if r > 0 {
		return c - r + 100
	}
	return c - r
}

// Split divides total among weights in proportion, by largest remainder, so
// that the parts always add up to total exactly. Ties on the remainder go to
// the lower index. Weights must be non-negative with a positive sum; total
// must be non-negative.
func Split(total Cents, weights []int64) ([]Cents, error) {
	if total < 0 {
		return nil, errors.New("money: Split of a negative total")
	}
	var sum int64
	for _, w := range weights {
		if w < 0 {
			return nil, errors.New("money: negative weight")
		}
		sum += w
	}
	if sum <= 0 {
		return nil, ErrZeroDenominator
	}
	parts := make([]Cents, len(weights))
	rems := make([]*big.Int, len(weights))
	var given Cents
	for i, w := range weights {
		p := new(big.Int).Mul(big.NewInt(int64(total)), big.NewInt(w))
		q, r := new(big.Int).QuoRem(p, big.NewInt(sum), new(big.Int))
		parts[i] = Cents(q.Int64())
		rems[i] = r
		given += parts[i]
	}
	for left := total - given; left > 0; left-- {
		best := -1
		for i := range rems {
			if weights[i] == 0 {
				continue
			}
			if best == -1 || rems[i].Cmp(rems[best]) > 0 {
				best = i
			}
		}
		parts[best]++
		rems[best] = big.NewInt(-1) // one extra cent per part at most
	}
	return parts, nil
}

// Sum returns the sum of amounts.
func Sum(amounts ...Cents) Cents {
	var s Cents
	for _, a := range amounts {
		s += a
	}
	return s
}
