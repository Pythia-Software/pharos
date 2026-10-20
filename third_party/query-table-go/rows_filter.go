package querytable

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrRowsUnsupported asks the host to stage its rows and use ExecuteV2In.
// No partial execution result accompanies this error. Test with errors.Is.
var ErrRowsUnsupported = errors.New("in-memory rows unsupported")

// RowFilter is an immutable, reusable SQLite-compatible predicate. The zero
// value matches every row. Match can return ErrRowsUnsupported for a Go value
// or storage representation that cannot be evaluated exactly.
type RowFilter struct{ terms [][]rowPredicate }
type rowPredicate struct {
	clause       WhereClause
	field        FieldSpec
	value        any
	op, constant string
	re           *regexp.Regexp
}

// CompileRowFilter compiles the same base-field predicates as CompileSQLiteWhere.
// Values are keyed by schema field name; SQL bindings must be direct references
// to that column (optionally qualified by r). Computed/SQL expressions require
// fallback. Missing keys and nil are NULL; see ExecuteV2Rows for storage types.
func CompileRowFilter(terms []WhereTerm, s Schema, o SQLiteOptions) (RowFilter, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	for _, term := range terms {
		for _, c := range term.Predicates() {
			if strings.HasPrefix(c.Field, "@computed/") {
				return RowFilter{}, fmt.Errorf("%w: computed filter %s", ErrRowsUnsupported, c.Field)
			}
		}
	}
	// Use the SQL compiler as the authoritative validation boundary, including
	// validation of alternatives in OR groups containing a no-op.
	if _, err := CompileSQLiteWhere(terms, s, o); err != nil {
		return RowFilter{}, err
	}
	var out RowFilter
	for _, term := range terms {
		var group []rowPredicate
		noOp := false
		for _, c := range term.Predicates() {
			f := s.Fields[c.Field]
			sql, _, _ := sqliteLiteral(c, s, o.Now)
			if sql == "" {
				noOp = true
				continue
			}
			p := rowPredicate{clause: c, field: f, op: c.Op, value: c.Value}
			switch c.Op {
			case "=", "!=", ">", ">=", "<", "<=":
				_, p.value, p.op, p.constant, _ = sqliteComparison(c, f, o.Now)
				if b, ok := p.value.(bool); ok {
					if b {
						p.value = int64(1)
					} else {
						p.value = int64(0)
					}
				}
			case "length_gt", "length_lt", "length_eq":
				// The SQL compiler already checked canonical nonnegative integer syntax.
				_, args, _ := sqliteLiteral(c, s, o.Now)
				p.value = args[0]
			case "matches_regex", "not_matches_regex":
				if !validRowFunctionText(c.Value) {
					return RowFilter{}, fmt.Errorf("%w: regex text", ErrRowsUnsupported)
				}
				p.re, _ = sqlitePattern(c.Value)
			case "contains", "starts_with", "ends_with", "includes":
				if !validRowFunctionText(c.Value) {
					return RowFilter{}, fmt.Errorf("%w: filter text", ErrRowsUnsupported)
				}
				if c.Op != "includes" || !f.ArrayCaseSensitive {
					p.value = strings.ToLower(c.Value)
				}
			}
			group = append(group, p)
		}
		if noOp {
			continue
		}
		for _, p := range group {
			if !rowFieldSupported(p.clause.Field, p.field) {
				return RowFilter{}, fmt.Errorf("%w: filter binding %s", ErrRowsUnsupported, p.clause.Field)
			}
		}
		out.terms = append(out.terms, group)
	}
	return out, nil
}

var rowIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_$]*$`)

func rowFieldSupported(name string, f FieldSpec) bool {
	if strings.HasPrefix(name, "@computed/") || f.Synthetic || f.ValueErrorExpr != "" {
		return false
	}
	expr := strings.TrimSpace(f.Expr)
	columns := []string{sqliteIdent(name)}
	if rowIdentifier.MatchString(name) {
		columns = append(columns, name)
	}
	for _, column := range columns {
		if expr == "r."+column || expr == `"r".`+column || expr == sqliteIdent(name) {
			return true
		}
	}
	if rowIdentifier.MatchString(name) && expr == name {
		switch strings.ToLower(name) {
		case "null", "true", "false", "current_date", "current_time", "current_timestamp":
			return false
		}
		return true
	}
	return false
}

// Match returns whether the row would pass SQLite's WHERE. Runtime fallback
// errors must be handled before using any selected rows.
func (f RowFilter) Match(row map[string]any) (bool, error) {
	match := true
	for _, term := range f.terms {
		anyMatch := false
		for i := range term {
			p := &term[i]
			yes, err := p.match(row[p.clause.Field])
			if err != nil {
				return false, err
			}
			anyMatch = anyMatch || yes
		}
		match = match && anyMatch
	}
	return match, nil
}

// rowStorage implements the documented untyped SQL parameter binding. NaN is
// stored as NULL by SQLite. Other Go types intentionally require fallback.
func rowStorage(v any, f *FieldSpec) (any, error) {
	switch n := v.(type) {
	case nil, int64, string:
	case float64:
		if math.IsNaN(n) {
			return nil, nil
		}
	case bool:
		if f.Kind != FieldBool {
			return nil, fmt.Errorf("%w: %T in field %s", ErrRowsUnsupported, v, f.Name)
		}
		if n {
			return int64(1), nil
		}
		return int64(0), nil
	default:
		return nil, fmt.Errorf("%w: %T in field %s", ErrRowsUnsupported, v, f.Name)
	}
	if f.Kind == FieldDatetime && v == "" {
		return nil, nil
	}
	return v, nil
}
func validRowFunctionText(s string) bool {
	return len(s) <= SQLiteMaxFunctionTextBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func rowArray(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok || !validRowFunctionText(s) {
		return nil, fmt.Errorf("%w: array storage", ErrRowsUnsupported)
	}
	// Decode into any first: []string would silently accept JSON null elements.
	var a []any
	if json.Unmarshal([]byte(s), &a) != nil {
		return nil, nil
	}
	result := make([]string, 0, len(a))
	for _, v := range a {
		text, ok := v.(string)
		if !ok {
			return nil, nil
		}
		result = append(result, text)
	}
	// qt_array_json passes the original JSON to SQLite's json_each, which
	// decodes unpaired surrogates differently from encoding/json. Validate the
	// original escapes so Go's replacement runes cannot change membership.
	if err := validJSONSurrogates(s); err != nil {
		return nil, fmt.Errorf("%w: array Unicode escapes: %v", ErrRowsUnsupported, err)
	}
	return result, nil
}

func (p *rowPredicate) match(raw any) (bool, error) {
	v, err := rowStorage(raw, &p.field)
	if err != nil {
		return false, err
	}
	null := v == nil
	if p.field.Kind == FieldText || p.field.Kind == FieldEnum || p.field.Kind == FieldDatetime {
		null = null || v == ""
	}
	var array []string
	if p.field.Kind == FieldTextArray {
		array, err = rowArray(v)
		if err != nil {
			return false, err
		}
		null = len(array) == 0
	}
	// truth=-1 is SQL UNKNOWN. Negation must preserve UNKNOWN until WHERE.
	truth := -1
	yes := false
	switch p.clause.Op {
	case "is_null":
		yes = null
		truth = 0
	case "is_not_null":
		yes = !null
		truth = 0
	case "=", "!=", ">", ">=", "<", "<=":
		if p.constant != "" {
			yes = p.constant == "1"
			truth = 0
			break
		}
		if p.field.Kind == FieldDatetime && (p.field.SQLiteDatetimeFormat != "utc-millis" && p.field.SQLiteDatetimeFormat != "unix-millis" && p.field.SQLiteDatetimeFormat != "unix-seconds") && v != nil {
			s, ok := v.(string)
			if !ok || !validRowFunctionText(s) {
				return false, fmt.Errorf("%w: datetime storage", ErrRowsUnsupported)
			}
			t, e := sqliteParseTime(s)
			if e != nil {
				return false, fmt.Errorf("%w: %v", ErrRowsUnsupported, e)
			}
			v = t.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
		if (p.field.Kind == FieldText || p.field.Kind == FieldEnum) && v == nil {
			v = ""
		}
		if v == nil {
			if p.clause.Op == "!=" {
				yes = true
				truth = 0
			}
			break
		}
		cmp := rowCompare(v, p.value)
		switch p.op {
		case "=":
			yes = cmp == 0
		case "!=":
			yes = cmp != 0
		case ">":
			yes = cmp > 0
		case ">=":
			yes = cmp >= 0
		case "<":
			yes = cmp < 0
		case "<=":
			yes = cmp <= 0
		}
		truth = 0
	case "includes":
		for _, s := range array {
			if !p.field.ArrayCaseSensitive {
				if !validRowFunctionText(s) {
					return false, fmt.Errorf("%w: array element", ErrRowsUnsupported)
				}
				s = strings.ToLower(s)
			}
			yes = yes || s == p.value
		}
		truth = 0
	default:
		if v == nil {
			break
		}
		s, ok := v.(string)
		if !ok {
			return false, fmt.Errorf("%w: text predicate storage", ErrRowsUnsupported)
		}
		if !validRowFunctionText(s) {
			return false, fmt.Errorf("%w: text predicate storage", ErrRowsUnsupported)
		}
		truth = 0
		switch p.clause.Op {
		case "contains":
			yes = strings.Contains(strings.ToLower(s), p.value.(string))
		case "starts_with":
			yes = strings.HasPrefix(strings.ToLower(s), p.value.(string))
		case "ends_with":
			yes = strings.HasSuffix(strings.ToLower(s), p.value.(string))
		case "matches_regex":
			yes = p.re.MatchString(s)
		case "not_matches_regex":
			yes = !p.re.MatchString(s)
		case "length_gt":
			yes = int64(utf8.RuneCountInString(s)) > p.value.(int64)
		case "length_lt":
			yes = int64(utf8.RuneCountInString(s)) < p.value.(int64)
		case "length_eq":
			yes = int64(utf8.RuneCountInString(s)) == p.value.(int64)
		}
	}
	if p.clause.Negated {
		return truth != -1 && !yes && !null, nil
	}
	return truth != -1 && yes, nil
}

// rowCompare follows SQLite storage classes for untyped columns: numeric <
// text. NULL placement is handled by the caller. Mixed large INTEGER/REAL
// comparisons use exact binary rationals to avoid rounding away int64 bits.
func rowCompare(a, b any) int {
	if v, ok := a.(bool); ok {
		a = int64(0)
		if v {
			a = int64(1)
		}
	}
	if v, ok := b.(bool); ok {
		b = int64(0)
		if v {
			b = int64(1)
		}
	}

	as, at := a.(string)
	bs, bt := b.(string)
	if at {
		if bt {
			return strings.Compare(as, bs)
		}
		return 1
	}
	if bt {
		return -1
	}
	ai, ia := a.(int64)
	bi, ib := b.(int64)
	if ia && ib {
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
		return 0
	}
	af, _ := sqliteFloat(a)
	bf, _ := sqliteFloat(b)
	if (ia && math.Abs(af) > sqliteSafeInteger || ib && math.Abs(bf) > sqliteSafeInteger) && !math.IsInf(af, 0) && !math.IsInf(bf, 0) {
		ar, br := new(big.Rat), new(big.Rat)
		if ia {
			ar.SetInt64(ai)
		} else {
			ar.SetFloat64(af)
		}
		if ib {
			br.SetInt64(bi)
		} else {
			br.SetFloat64(bf)
		}
		return ar.Cmp(br)
	}
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	return 0
}

// SQLiteRowValues returns SQL parameters for an untyped staging table in the
// requested column order. It shares ExecuteV2Rows' storage contract, including
// bool binding, NaN-to-NULL and empty-datetime-to-NULL normalization. The host
// owns table creation, column quoting, authorization, insertion and cleanup.
func SQLiteRowValues(row map[string]any, s Schema, columns []string) ([]any, error) {
	values := make([]any, len(columns))
	for i, name := range columns {
		f, ok := s.Fields[name]
		if !ok {
			return nil, fmt.Errorf("unknown staging field %q", name)
		}
		v, err := rowStorage(row[name], &f)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	return values, nil
}
