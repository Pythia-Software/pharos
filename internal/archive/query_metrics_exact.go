package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"math/big"
	"strconv"
)

// exactSum adds numbers as the adapter's SUM does (qt_v2_sum and sqliteRat
// in third_party/query-table-go/sqlite_v2_functions.go): exactly, reading an
// integer as itself and any other value as the decimal its shortest
// representation spells. The total is rounded to a float64 once, at the end.
// add and total report false wherever the adapter would report an error
// instead of a value, so the caller can fall through to it.
type exactSum struct {
	values int64
	// While every value is an integer, the sum and absolute sum themselves.
	integer, integerAbs int64
	// Otherwise each value m×10^e is added to the parts for e.
	parts, absParts map[int]*decimalPart
}

type decimalPart struct {
	small int64
	large *big.Int
}

const safeInteger = 1<<53 - 1

// add adds a value of a number field, as the adapter reads it: an integer or
// a float64, finite, between 1e-300 and 1e100 in magnitude or zero, and not
// an integer beyond JavaScript's safe integers.
func (s *exactSum) add(value any, digits []byte) ([]byte, bool) {
	var n float64
	switch v := value.(type) {
	case int64:
		n = float64(v)
	case int:
		n = float64(v)
	case float64:
		n = v
	default:
		return digits, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e100 || n != 0 && math.Abs(n) < 1e-300 || math.Trunc(n) == n && math.Abs(n) > safeInteger {
		return digits, false
	}
	s.values++
	if s.parts == nil && math.Trunc(n) == n && math.Abs(n) <= float64(safeInteger-s.integerAbs) {
		s.integer += int64(n)
		s.integerAbs += int64(math.Abs(n))
		return digits, true
	}
	if s.parts == nil {
		s.parts, s.absParts = map[int]*decimalPart{}, map[int]*decimalPart{}
		addDecimal(s.parts, s.integer, 0)
		addDecimal(s.absParts, s.integerAbs, 0)
	}
	if math.Trunc(n) == n {
		addDecimal(s.parts, int64(n), 0)
		addDecimal(s.absParts, int64(math.Abs(n)), 0)
		return digits, true
	}
	// The shortest digits, as strconv.FormatFloat(n, 'g', -1, 64) spells them,
	// in the form [-]d.ddde±x: at most 17 digits, so the mantissa fits an int64.
	digits = strconv.AppendFloat(digits[:0], n, 'e', -1, 64)
	at := bytes.IndexByte(digits, 'e')
	exponent, _ := strconv.Atoi(string(digits[at+1:]))
	var mantissa int64
	fraction := -1 // digits after the point, once it is passed
	for _, digit := range bytes.TrimPrefix(digits[:at], []byte("-")) {
		if digit == '.' {
			fraction = 0
			continue
		}
		mantissa = mantissa*10 + int64(digit-'0')
		if fraction >= 0 {
			fraction++
		}
	}
	exponent -= max(fraction, 0)
	if n < 0 {
		addDecimal(s.parts, -mantissa, exponent)
	} else {
		addDecimal(s.parts, mantissa, exponent)
	}
	addDecimal(s.absParts, mantissa, exponent)
	return digits, true
}

func addDecimal(parts map[int]*decimalPart, mantissa int64, exponent int) {
	part := parts[exponent]
	if part == nil {
		part = &decimalPart{}
		parts[exponent] = part
	}
	if mantissa > 0 && part.small > math.MaxInt64-mantissa || mantissa < 0 && part.small < math.MinInt64-mantissa {
		if part.large == nil {
			part.large = new(big.Int)
		}
		part.large.Add(part.large, big.NewInt(part.small))
		part.small = 0
	}
	part.small += mantissa
}

func decimalRat(parts map[int]*decimalPart) *big.Rat {
	least := 0
	first := true
	for exponent := range parts {
		if first || exponent < least {
			least, first = exponent, false
		}
	}
	total := new(big.Int)
	for exponent, part := range parts {
		value := big.NewInt(part.small)
		if part.large != nil {
			value.Add(value, part.large)
		}
		total.Add(total, value.Mul(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent-least)), nil)))
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(least, -least))), nil)
	if least < 0 {
		return new(big.Rat).SetFrac(total, scale)
	}
	return new(big.Rat).SetInt(total.Mul(total, scale))
}

var (
	exactSafe = big.NewRat(safeInteger, 1)
	exactMax  = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(100), nil))
	exactMin  = new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(300), nil))
)

// total is the adapter's SUM: nil with no values, else the exact sum rounded
// once to a float64, or an int64 while every value was an integer.
func (s *exactSum) total() (any, bool) {
	if s.values == 0 {
		return nil, true
	}
	if s.parts == nil {
		return s.integer, true
	}
	sum, abs := decimalRat(s.parts), decimalRat(s.absParts)
	magnitude := new(big.Rat).Abs(sum)
	if abs.Cmp(exactSafe) > 0 || magnitude.Cmp(exactMax) > 0 || sum.Sign() != 0 && magnitude.Cmp(exactMin) < 0 {
		return nil, false
	}
	n, _ := sum.Float64()
	if math.Trunc(n) == n && math.Abs(n) > safeInteger {
		return nil, false
	}
	return n, true
}

// adapterNumbers returns values as the adapter does: it hands each sum to
// SQLite as JSON ({"value":n}) and reads it back with json_extract.
func adapterNumbers(ctx context.Context, tx *sql.Tx, values []float64) ([]any, error) {
	type result struct {
		Value float64 `json:"value"`
	}
	payload := make([]result, len(values))
	for i, value := range values {
		payload[i].Value = value
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT json_extract(value,'$.value') FROM json_each(?) ORDER BY key", string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]any, 0, len(values))
	for rows.Next() {
		var value any
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if n, ok := value.(int64); ok {
			value = float64(n)
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
