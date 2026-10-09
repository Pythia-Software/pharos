package querytable

import (
	"container/list"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// SQLiteScalarFunction is a deterministic, NULL-aware function to register on
// every physical connection, before opening a pool. The compiler has no driver
// dependency. Hosts adapt Call to their driver's registration API.
type SQLiteScalarFunction struct {
	Name  string
	Arity int
	Call  func([]driver.Value) (driver.Value, error)
}

const SQLiteMaxFunctionTextBytes = 1 << 20
const sqliteSafeInteger = 9007199254740991

// SQLiteFunctions returns independent function descriptors with a shared,
// concurrency-safe LRU of at most 128 compiled Go/RE2 patterns. It never changes
// SQLite's built-in functions or collations. Retain one set for the host.
func SQLiteFunctions() []SQLiteScalarFunction {
	var mu sync.Mutex
	lru := list.New()
	cache := map[string]*list.Element{}
	type entry struct {
		pattern string
		re      *regexp.Regexp
	}
	compile := func(pattern string) (*regexp.Regexp, error) {
		if len(pattern) > maxRegexBytes {
			return nil, fmt.Errorf("regex exceeds %d bytes", maxRegexBytes)
		}
		mu.Lock()
		defer mu.Unlock()
		if e := cache[pattern]; e != nil {
			lru.MoveToFront(e)
			return e.Value.(entry).re, nil
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid Go/RE2 regex: %w", err)
		}
		cache[pattern] = lru.PushFront(entry{pattern, re})
		if lru.Len() > 128 {
			e := lru.Back()
			delete(cache, e.Value.(entry).pattern)
			lru.Remove(e)
		}
		return re, nil
	}
	text := func(v driver.Value) (string, error) {
		var s string
		switch v := v.(type) {
		case string:
			s = v
		case []byte:
			s = string(v)
		case int64:
			s = strconv.FormatInt(v, 10)
		case float64:
			s = strconv.FormatFloat(v, 'g', -1, 64)
		default:
			return "", fmt.Errorf("unsupported SQLite text value %T", v)
		}
		if len(s) > SQLiteMaxFunctionTextBytes || !utf8.ValidString(s) {
			return "", fmt.Errorf("SQLite function requires valid UTF-8 of at most %d bytes", SQLiteMaxFunctionTextBytes)
		}
		return s, nil
	}
	fn := func(name string, n int, call func([]driver.Value) (driver.Value, error)) SQLiteScalarFunction {
		return SQLiteScalarFunction{Name: name, Arity: n, Call: func(a []driver.Value) (driver.Value, error) {
			if len(a) != n {
				return nil, fmt.Errorf("%s expects %d arguments", name, n)
			}
			return call(a)
		}}
	}
	regex := func(a []driver.Value, extract bool) (driver.Value, error) {
		if a[0] == nil || a[1] == nil {
			return nil, nil
		}
		pattern, err := text(a[0])
		if err != nil {
			return nil, err
		}
		re, err := compile(pattern)
		if err != nil {
			return nil, err
		}
		value, err := text(a[1])
		if err != nil {
			return nil, err
		}
		if !extract {
			if re.MatchString(value) {
				return int64(1), nil
			}
			return int64(0), nil
		}
		indexes := re.FindStringSubmatchIndex(value)
		if indexes == nil {
			return nil, nil
		}
		start, end := indexes[0], indexes[1]
		if len(indexes) > 2 {
			start, end = indexes[2], indexes[3]
		}
		if start < 0 {
			return nil, nil
		}
		return value[start:end], nil
	}
	return []SQLiteScalarFunction{
		fn("qt_lower", 1, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return nil, nil
			}
			s, e := text(a[0])
			if e != nil {
				return nil, e
			}
			return strings.ToLower(s), nil
		}),
		fn("qt_ends_with", 2, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil || a[1] == nil {
				return nil, nil
			}
			value, e := text(a[0])
			if e != nil {
				return nil, e
			}
			suffix, e := text(a[1])
			if e != nil {
				return nil, e
			}
			if strings.HasSuffix(strings.ToLower(value), strings.ToLower(suffix)) {
				return int64(1), nil
			}
			return int64(0), nil
		}),
		fn("qt_length", 1, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return nil, nil
			}
			if _, ok := a[0].(string); !ok {
				return nil, nil
			}
			s, e := text(a[0])
			if e != nil {
				return nil, e
			}
			return int64(utf8.RuneCountInString(s)), nil
		}),
		fn("qt_regexp", 2, func(a []driver.Value) (driver.Value, error) { return regex(a, false) }),
		fn("qt_regexp_extract", 2, func(a []driver.Value) (driver.Value, error) { return regex(a, true) }),
		fn("qt_array_json", 1, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return "[]", nil
			}
			s, e := text(a[0])
			if e != nil {
				return nil, e
			}
			var values []any
			if json.Unmarshal([]byte(s), &values) != nil {
				return "[]", nil
			}
			for _, v := range values {
				if _, ok := v.(string); !ok {
					return "[]", nil
				}
			}
			if values == nil {
				return "[]", nil
			}
			return s, nil
		}),
		fn("qt_datetime", 1, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return nil, nil
			}
			s, e := text(a[0])
			if e != nil {
				return nil, e
			}
			if s == "" {
				return nil, nil
			}
			t, e := sqliteParseTime(s)
			if e != nil {
				return nil, e
			}
			return t.UTC().Format("2006-01-02T15:04:05.000000000Z"), nil
		}),
		fn("qt_unix_datetime", 2, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return nil, nil
			}
			scale, ok := a[1].(int64)
			if !ok || (scale != 1 && scale != 1000) {
				return nil, fmt.Errorf("invalid unix datetime scale")
			}
			t, e := sqliteUnixTime(a[0], scale)
			if e != nil {
				return nil, e
			}
			return t.UTC().Format("2006-01-02T15:04:05.000000000Z"), nil
		}),
		fn("qt_number", 1, func(a []driver.Value) (driver.Value, error) {
			if a[0] == nil {
				return nil, nil
			}
			switch v := a[0].(type) {
			case int64:
				if v > sqliteSafeInteger || v < -sqliteSafeInteger {
					return nil, fmt.Errorf("unsafe_integer")
				}
				return v, nil
			case float64:
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, fmt.Errorf("numeric_range")
				}
				if math.Trunc(v) == v && math.Abs(v) > sqliteSafeInteger {
					return nil, fmt.Errorf("unsafe_integer")
				}
				return v, nil
			default:
				return nil, fmt.Errorf("numeric SQLite binding must contain INTEGER, REAL, or NULL, got %T", a[0])
			}
		}),
	}
}

func sqliteParseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, e := time.Parse(layout, s); e == nil {
			if t.UTC().Year() < 0 || t.UTC().Year() > 9999 {
				return time.Time{}, fmt.Errorf("datetime outside years 0000..9999")
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("not an RFC3339 datetime: %q", s)
}

func sqliteUnixTime(v any, scale int64) (time.Time, error) {
	var t time.Time
	switch n := v.(type) {
	case int64:
		if scale == 1000 {
			t = time.UnixMilli(n)
		} else {
			t = time.Unix(n, 0)
		}
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return t, fmt.Errorf("datetime out of range")
		}
		sec := math.Floor(n / float64(scale))
		if sec < -62167219200 || sec >= 253402300800 {
			return t, fmt.Errorf("datetime out of range")
		}
		nanos := math.Round((n - sec*float64(scale)) * (1e9 / float64(scale)))
		t = time.Unix(int64(sec), int64(nanos))
	default:
		return t, fmt.Errorf("datetime binding requires numeric storage")
	}
	if t.UTC().Year() < 0 || t.UTC().Year() > 9999 {
		return t, fmt.Errorf("datetime out of range")
	}
	return t, nil
}
