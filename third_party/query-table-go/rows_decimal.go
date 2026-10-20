package querytable

import (
	"database/sql/driver"
	"math"
	"math/big"
	"strconv"
)

// Keep common decimal populations (money and token counts) in scaled integers.
// Promotion is exact and happens before either coefficient can overflow. The
// shared SQLite accumulator remains authoritative for final rounding, bounds
// and uncommon scales; there is no per-input binary floating-point addition.
type rowDecimalAggregate struct {
	n        int64
	sum, abs int64
	scale    int
	slow     *sqliteV2Aggregate
}

var rowPowersOfTen = [...]int64{1, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000, 1000000000, 10000000000, 100000000000, 1000000000000, 10000000000000, 100000000000000, 1000000000000000, 10000000000000000, 100000000000000000, 1000000000000000000}

func rowDecimalParts(n float64) (int64, int, bool) {
	if math.Trunc(n) == n {
		return int64(n), 0, true
	} // caller checks safe integers
	var buf [32]byte
	text := strconv.AppendFloat(buf[:0], n, 'g', -1, 64)
	coefficient := int64(0)
	scale := 0
	negative, fraction := false, false
	i := 0
	if text[0] == '-' {
		negative = true
		i++
	}
	for ; i < len(text) && text[i] != 'e'; i++ {
		if text[i] == '.' {
			fraction = true
			continue
		}
		coefficient = coefficient*10 + int64(text[i]-'0')
		if fraction {
			scale++
		}
	}
	if i < len(text) {
		i++
		sign := 1
		if text[i] == '-' {
			sign = -1
			i++
		} else if text[i] == '+' {
			i++
		}
		exponent := 0
		for ; i < len(text); i++ {
			exponent = exponent*10 + int(text[i]-'0')
		}
		scale -= sign * exponent
	}
	if negative {
		coefficient = -coefficient
	}
	return coefficient, scale, scale >= 0 && scale < len(rowPowersOfTen)
}
func (a *rowDecimalAggregate) add(n float64) {
	if a.slow != nil {
		_ = a.slow.Step([]driver.Value{n})
		return
	}
	coefficient, scale, ok := rowDecimalParts(n)
	if ok {
		// Only the absolute accumulator needs an overflow guard: it also bounds
		// the signed sum, including populations with large cancellation.
		magnitude := coefficient
		if magnitude < 0 {
			magnitude = -magnitude
		}
		sum, abs := a.sum, a.abs
		target := a.scale
		if scale > target {
			factor := rowPowersOfTen[scale-target]
			if abs > math.MaxInt64/factor {
				ok = false
			} else {
				sum *= factor
				abs *= factor
				target = scale
			}
		} else if scale < target {
			factor := rowPowersOfTen[target-scale]
			if magnitude > math.MaxInt64/factor {
				ok = false
			} else {
				coefficient *= factor
				magnitude *= factor
			}
		}
		if ok && abs <= math.MaxInt64-magnitude {
			a.n++
			a.sum = sum + coefficient
			a.abs = abs + magnitude
			a.scale = target
			return
		}
	}
	a.slow = a.accumulator("")
	_ = a.slow.Step([]driver.Value{n})
}
func (a *rowDecimalAggregate) accumulator(op string) *sqliteV2Aggregate {
	return &sqliteV2Aggregate{op: op, n: a.n, sum: big.NewRat(a.sum, rowPowersOfTen[a.scale]), abs: big.NewRat(a.abs, rowPowersOfTen[a.scale])}
}
func (a *rowDecimalAggregate) value(op string) (driver.Value, error) {
	if a.slow != nil {
		a.slow.op = op
		return a.slow.Value()
	}
	return a.accumulator(op).Value()
}
