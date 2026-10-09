package querytable

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"
)

// SQLiteAggregateFunction is one independent aggregate invocation. Drivers must
// create a fresh instance for each group, call Step, then Value, and release it.
// These descriptors are ordinary aggregates, not sliding window functions.
type SQLiteAggregateFunction interface {
	Step([]driver.Value) error
	Value() (driver.Value, error)
}
type SQLiteAggregateDescriptor struct {
	Name  string
	Arity int
	New   func() SQLiteAggregateFunction
}

// SQLiteMaxMedianSamples bounds exact MEDIAN memory per group. Larger inputs
// return resource_limit rather than an approximate or truncated result.
const SQLiteMaxMedianSamples = 100000

type sqliteNumericResult struct {
	Value *float64 `json:"value"`
	Error string   `json:"error,omitempty"`
}

func sqliteNumericJSON(v *float64, e string) (driver.Value, error) {
	p, err := json.Marshal(sqliteNumericResult{v, e})
	return string(p), err
}
func sqliteFloat(v driver.Value) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
func sqliteNumericError(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e100 || n != 0 && math.Abs(n) < 1e-300 {
		return "numeric_range"
	}
	return ""
}
func sqliteUnsafe(n float64) bool { return math.Trunc(n) == n && math.Abs(n) > sqliteSafeInteger }

// Decimal rendering matches the profile's documented decimal-domain checks;
// final row arithmetic still exposes the IEEE result.
func sqliteRat(n float64) *big.Rat {
	if math.Trunc(n) == n && math.Abs(n) <= sqliteSafeInteger {
		return big.NewRat(int64(n), 1)
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(n, 'g', -1, 64))
	if !ok {
		panic("finite numeric precondition")
	}
	return r
}

var sqliteRatMax = sqliteRat(1e100)
var sqliteRatMin = sqliteRat(1e-300)

func sqliteRatError(r *big.Rat) string {
	abs := new(big.Rat).Abs(r)
	if abs.Cmp(sqliteRatMax) > 0 || r.Sign() != 0 && abs.Cmp(sqliteRatMin) < 0 {
		return "numeric_range"
	}
	return ""
}

// SQLiteV2Functions supplements SQLiteFunctions. Register both sets before
// opening connections. Ordinary formula failures are returned as error codes,
// allowing IF/COALESCE/AND/OR to preserve lazy error semantics.
func SQLiteV2Functions() []SQLiteScalarFunction {
	fn := func(name string, n int, call func([]driver.Value) (driver.Value, error)) SQLiteScalarFunction {
		return SQLiteScalarFunction{Name: name, Arity: n, Call: func(a []driver.Value) (driver.Value, error) {
			if len(a) != n {
				return nil, fmt.Errorf("%s expects %d arguments", name, n)
			}
			return call(a)
		}}
	}
	return append(sqliteDistributionFunctions(), []SQLiteScalarFunction{
		fn("qt_v2_datetime", 2, func(a []driver.Value) (driver.Value, error) {
			var value any
			code := ""
			format, ok := a[1].(string)
			if !ok {
				return nil, fmt.Errorf("invalid datetime format")
			}
			if a[0] != nil {
				var stamp time.Time
				var err error
				if format == "unix-millis" || format == "unix-seconds" {
					if _, ok := sqliteFloat(a[0]); !ok {
						code = "storage_type"
					} else {
						scale := int64(1)
						if format == "unix-millis" {
							scale = 1000
						}
						stamp, err = sqliteUnixTime(a[0], scale)
					}
				} else {
					text, ok := a[0].(string)
					if !ok {
						code = "storage_type"
					} else if text != "" {
						stamp, err = sqliteParseTime(text)
						if err == nil && format == "utc-millis" && text != stamp.UTC().Format("2006-01-02T15:04:05.000Z") {
							err = fmt.Errorf("noncanonical utc-millis storage")
						}
					} else {
						return string(`{"value":null}`), nil
					}
				}
				if err != nil {
					code = "datetime_invalid"
				}
				if code == "" {
					value = stamp.UTC().Format("2006-01-02T15:04:05.000000000Z")
				}
			}
			payload, err := json.Marshal(struct {
				Value any    `json:"value"`
				Error string `json:"error,omitempty"`
			}{value, code})
			return string(payload), err
		}),
		fn("qt_v2_field_error", 2, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return nil, nil
			}
			typ, ok := a[1].(string)
			if !ok {
				return nil, fmt.Errorf("invalid type descriptor")
			}
			switch typ {
			case "number":
				n, ok := sqliteFloat(a[0])
				if !ok {
					return "storage_type", nil
				}
				if e := sqliteNumericError(n); e != "" {
					return e, nil
				}
			case "text", "datetime":
				s, ok := a[0].(string)
				if !ok {
					return "storage_type", nil
				}
				if !utf8.ValidString(s) || len(s) > 100000 {
					return "text_range", nil
				}
			case "bool":
				n, ok := a[0].(int64)
				if !ok || n != 0 && n != 1 {
					return "storage_type", nil
				}
			default:
				return nil, fmt.Errorf("invalid type descriptor")
			}
			return nil, nil
		}),
		fn("qt_v2_unsafe", 1, func(a []driver.Value) (driver.Value, error) {
			n, ok := sqliteFloat(a[0])
			if ok && sqliteUnsafe(n) {
				return int64(1), nil
			}
			return int64(0), nil
		}),
		fn("qt_v2_arithmetic", 3, func(a []driver.Value) (driver.Value, error) {
			op, ok := a[0].(string)
			if !ok {
				return nil, fmt.Errorf("invalid arithmetic operator")
			}
			if a[1] == nil || (op == "+" || op == "-" || op == "*" || op == "/") && a[2] == nil {
				return sqliteNumericJSON(nil, "")
			}
			l, ok := sqliteFloat(a[1])
			if !ok {
				return sqliteNumericJSON(nil, "storage_type")
			}
			if e := sqliteNumericError(l); e != "" {
				return sqliteNumericJSON(nil, e)
			}
			r := float64(0)
			if a[2] != nil {
				r, ok = sqliteFloat(a[2])
				if !ok {
					return sqliteNumericJSON(nil, "storage_type")
				}
				if e := sqliteNumericError(r); e != "" {
					return sqliteNumericJSON(nil, e)
				}
			}
			exact := sqliteRat(l)
			v := l
			switch op {
			case "+":
				exact.Add(exact, sqliteRat(r))
				v = l + r
			case "-":
				exact.Sub(exact, sqliteRat(r))
				v = l - r
			case "*":
				exact.Mul(exact, sqliteRat(r))
				v = l * r
			case "/":
				if r == 0 {
					return sqliteNumericJSON(nil, "divide_by_zero")
				}
				exact.Quo(exact, sqliteRat(r))
				v = l / r
			case "ABS":
				exact.Abs(exact)
				v = math.Abs(l)
			case "unary-":
				exact.Neg(exact)
				v = -l
			case "unary+":
			default:
				return nil, fmt.Errorf("unknown arithmetic operator")
			}
			if e := sqliteRatError(exact); e != "" {
				return sqliteNumericJSON(nil, e)
			}
			if e := sqliteNumericError(v); e != "" {
				return sqliteNumericJSON(nil, e)
			}
			return sqliteNumericJSON(&v, "")
		}),
	}...)
}

// SQLiteV2Aggregates uses exact decimal accumulators for SUM/AVG, independent of
// visitation order and SQLite's integer overflow. SUM conservatively rejects an
// absolute input sum above MAX_SAFE_INTEGER. MEDIAN uses exact linear midpoint.
// Box plots use bounded exact samples; histograms retain shared-edge bin counters.
func SQLiteV2Aggregates() []SQLiteAggregateDescriptor {
	out := []SQLiteAggregateDescriptor{}
	for _, op := range []string{"sum", "avg", "median"} {
		op := op
		out = append(out, SQLiteAggregateDescriptor{Name: "qt_v2_" + op, Arity: 1, New: func() SQLiteAggregateFunction {
			return &sqliteV2Aggregate{op: op}
		}})
	}
	return append(out, sqliteDistributionAggregates()...)
}

type sqliteV2Aggregate struct {
	op                     string
	n                      int64
	sum, abs               *big.Rat
	integerSum, integerAbs int64
	samples                []float64
	err                    string
}

func (a *sqliteV2Aggregate) Step(values []driver.Value) error {
	if len(values) != 1 {
		return fmt.Errorf("aggregate expects one argument")
	}
	if values[0] == nil || a.err != "" {
		return nil
	}
	n, ok := sqliteFloat(values[0])
	if !ok {
		a.err = "storage_type"
		return nil
	}
	if e := sqliteNumericError(n); e != "" {
		a.err = e
		return nil
	}
	if sqliteUnsafe(n) {
		a.err = "unsafe_integer"
		return nil
	}
	a.n++
	if a.op == "median" {
		if a.n > SQLiteMaxMedianSamples {
			a.err = "resource_limit"
			a.samples = nil
			return nil
		}
		a.samples = append(a.samples, n)
	} else {
		// Safe integral populations need no per-sample rational allocation.
		// Promote before the absolute bound is exceeded, also for AVG, so
		// large/cancelling populations keep exact semantics and error priority.
		if a.sum == nil && math.Trunc(n) == n && math.Abs(n) <= float64(sqliteSafeInteger-a.integerAbs) {
			a.integerSum += int64(n)
			a.integerAbs += int64(math.Abs(n))
			return nil
		}
		if a.sum == nil {
			a.sum = big.NewRat(a.integerSum, 1)
			a.abs = big.NewRat(a.integerAbs, 1)
		}
		r := sqliteRat(n)
		a.sum.Add(a.sum, r)
		a.abs.Add(a.abs, new(big.Rat).Abs(r))
	}
	return nil
}
func (a *sqliteV2Aggregate) Value() (driver.Value, error) {
	if a.err != "" {
		return sqliteNumericJSON(nil, a.err)
	}
	if a.n == 0 {
		return sqliteNumericJSON(nil, "")
	}
	if a.op != "median" && a.sum == nil {
		a.sum = big.NewRat(a.integerSum, 1)
		a.abs = big.NewRat(a.integerAbs, 1)
	}
	var exact *big.Rat
	switch a.op {
	case "median":
		sort.Float64s(a.samples)
		i := len(a.samples) / 2
		exact = sqliteRat(a.samples[i])
		if len(a.samples)%2 == 0 {
			exact.Add(exact, sqliteRat(a.samples[i-1]))
			exact.Quo(exact, big.NewRat(2, 1))
		}
	case "avg":
		exact = new(big.Rat).Quo(a.sum, big.NewRat(a.n, 1))
	case "sum":
		if a.abs.Cmp(big.NewRat(sqliteSafeInteger, 1)) > 0 {
			return sqliteNumericJSON(nil, "unsafe_integer")
		}
		exact = a.sum
	}
	if e := sqliteRatError(exact); e != "" {
		return sqliteNumericJSON(nil, e)
	}
	n, _ := exact.Float64()
	if e := sqliteNumericError(n); e != "" {
		return sqliteNumericJSON(nil, e)
	}
	if sqliteUnsafe(n) {
		return sqliteNumericJSON(nil, "unsafe_integer")
	}
	return sqliteNumericJSON(&n, "")
}
