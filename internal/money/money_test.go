package money

import (
	"reflect"
	"testing"
)

func TestMulDivHalfUp(t *testing.T) {
	cases := []struct {
		a        Cents
		num, den int64
		want     Cents
	}{
		{85000, 17, 31, 46613}, // 46612.90
		{9000, 17, 31, 4935},   // 4935.48
		{5, 1, 2, 3},           // 2.5 rounds up
		{-5, 1, 2, -3},         // half away from zero
		{7, 1, 2, 4},           // 3.5
		{6, 1, 4, 2},           // 1.5
		{1, 1, 3, 0},           // 0.33
		{100, 0, 7, 0},         // zero numerator
		{9_000_000_000, 10000, 3, 30_000_000_000_000},
	}
	for _, c := range cases {
		got, err := MulDivHalfUp(c.a, c.num, c.den)
		if err != nil || got != c.want {
			t.Errorf("MulDivHalfUp(%d,%d,%d) = %d, %v; want %d", c.a, c.num, c.den, got, err, c.want)
		}
	}
	if _, err := MulDivHalfUp(1, 1, 0); err == nil {
		t.Error("zero denominator accepted")
	}
}

func TestMulDivFloor(t *testing.T) {
	cases := []struct {
		a        Cents
		num, den int64
		want     Cents
	}{
		{195000, 120, 365, 64109},
		{30328, 1, 12, 2527},
		{-7, 1, 2, -4},
		{8, 1, 2, 4},
	}
	for _, c := range cases {
		got, err := MulDivFloor(c.a, c.num, c.den)
		if err != nil || got != c.want {
			t.Errorf("MulDivFloor(%d,%d,%d) = %d, %v; want %d", c.a, c.num, c.den, got, err, c.want)
		}
	}
	if _, err := MulDivFloor(1, 1, -1); err == nil {
		t.Error("negative denominator accepted")
	}
}

func TestCeilToEuro(t *testing.T) {
	cases := map[Cents]Cents{0: 0, 1: 100, 99: 100, 100: 100, 16250: 16300, 16201: 16300, -150: -100, -100: -100}
	for in, want := range cases {
		if got := CeilToEuro(in); got != want {
			t.Errorf("CeilToEuro(%d) = %d; want %d", in, got, want)
		}
	}
}

func TestSplit(t *testing.T) {
	cases := []struct {
		total   Cents
		weights []int64
		want    []Cents
	}{
		{100001, []int64{5000, 5000}, []Cents{50001, 50000}},
		{100, []int64{1, 1, 1}, []Cents{34, 33, 33}},
		{10, []int64{0, 1}, []Cents{0, 10}},
		{0, []int64{3, 7}, []Cents{0, 0}},
		{1000, []int64{3333, 3333, 3334}, []Cents{333, 333, 334}},
	}
	for _, c := range cases {
		got, err := Split(c.total, c.weights)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Split(%d,%v) = %v, %v; want %v", c.total, c.weights, got, err, c.want)
		}
		if Sum(got...) != c.total {
			t.Errorf("Split(%d,%v) parts do not add up", c.total, c.weights)
		}
	}
	for _, bad := range [][]int64{{0, 0}, {-1, 2}, {}} {
		if _, err := Split(10, bad); err == nil {
			t.Errorf("Split accepted weights %v", bad)
		}
	}
	if _, err := Split(-1, []int64{1}); err == nil {
		t.Error("Split accepted a negative total")
	}
}
