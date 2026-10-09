package querytable

// schema.go — the backend projection of the JSON schema document. A FieldSpec
// is the SQL binding for one field; Schema is the per-dataset allowlist that
// Compile validates against. Built by LoadSchema from the same JSON document the
// frontend loads, so the two ends cannot drift.

import (
	"encoding/json"
	"fmt"
)

// FieldKind picks the value-coercion + operator-validation path.
type FieldKind int

const (
	FieldText FieldKind = iota
	FieldNumber
	FieldDatetime
	FieldBool
	FieldEnum
	FieldTextArray
)

func kindFromString(s string) (FieldKind, error) {
	switch s {
	case "text":
		return FieldText, nil
	case "number":
		return FieldNumber, nil
	case "datetime":
		return FieldDatetime, nil
	case "bool":
		return FieldBool, nil
	case "enum":
		return FieldEnum, nil
	case "textarray":
		return FieldTextArray, nil
	default:
		return FieldText, fmt.Errorf("unknown field type %q", s)
	}
}

// FieldSpec is one field's server binding. Expr is the server-defined SQL
// expression (from the document's bindings.postgres.expr) and is the injection
// boundary: it is never built from request input.
type FieldSpec struct {
	// ExpressionNumeric certifies a SQL numeric binding eligible for the bounded
	// v2 profile. Values outside +/-1e100 produce numeric_range, never DB overflow.
	ExpressionNumeric bool
	// SQLiteDatetimeFormat: rfc3339 (default), utc-millis, unix-seconds, or unix-millis.
	SQLiteDatetimeFormat string
	// SQLiteSortField preserves the target storage kind/format for sort.field.
	SQLiteSortField string
	// AggregateOps nil uses defaults; an empty slice disables aggregation.
	AggregateOps []string
	Groupable    *bool // nil defaults to text/enum/bool grouping in v2
	Name         string
	Kind         FieldKind
	Expr         string
	Synthetic    bool
	ServerFilter bool // false → field is client-only; Compile rejects filters on it
	// ArrayCaseSensitive preserves exact text-array membership keys. Default false.
	ArrayCaseSensitive bool
	// FilterOps is nil for the type's default matrix; a non-nil slice is the
	// field's explicit operator allowlist. An empty slice disables every op.
	FilterOps []string
	Sortable  bool
	SortExpr  string // expr to ORDER BY when it differs from Expr (FieldDef.sort.field → that field's Expr)
}

// Schema is the compiled, server-side field allowlist for one dataset.
type Schema struct {
	Name         string
	IDField      string
	Fields       map[string]FieldSpec
	DefaultSort  []OrderBy
	TiebreakSort []OrderBy
}

// ---- JSON document shapes (subset we read on the backend) -----------------

type docSQLBinding struct {
	Expr              string `json:"expr"`
	Synthetic         bool   `json:"synthetic"`
	Kind              string `json:"kind"`
	ExpressionNumeric bool   `json:"expressionNumeric"`
	DatetimeFormat    string `json:"datetimeFormat"`
}

type docField struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Type      string `json:"type"`
	Aggregate *struct {
		Enabled   *bool    `json:"enabled"`
		Measure   *bool    `json:"measure"`
		Groupable *bool    `json:"groupable"`
		Ops       []string `json:"ops"`
	} `json:"aggregate"`
	Source any `json:"source"` // "backend"|"derived" | object | absent
	Filter *struct {
		ArrayCaseSensitive bool     `json:"arrayCaseSensitive"`
		Enabled            *bool    `json:"enabled"`
		Pushdown           *bool    `json:"pushdown"`
		Ops                []string `json:"ops"`
	} `json:"filter"`
	Sort *struct {
		Enabled *bool  `json:"enabled"`
		Field   string `json:"field"`
	} `json:"sort"`
	Bindings *struct {
		Postgres *docSQLBinding `json:"postgres"`
		SQLite   *docSQLBinding `json:"sqlite"`
	} `json:"bindings"`
}

type schemaDoc struct {
	Name         string     `json:"name"`
	IDField      string     `json:"idField"`
	DefaultSort  []OrderBy  `json:"defaultSort"`
	TiebreakSort []OrderBy  `json:"tiebreakSort"`
	Fields       []docField `json:"fields"`
}

// LoadSchema parses + validates a JSON schema document (the bytes of a
// schema/*.schema.json file) into a Schema, reading the `postgres` binding for
// each backend field. Fields without a postgres binding (derived / render-only)
// are skipped — they're never filtered, sorted, or selected on the server.
func LoadSchema(doc []byte) (Schema, error) {
	return loadSQLSchema(doc, "postgres")
}

// LoadSQLiteSchema reads only bindings.sqlite; it never falls back to Postgres SQL.
func LoadSQLiteSchema(doc []byte) (Schema, error) {
	return loadSQLSchema(doc, "sqlite")
}

func loadSQLSchema(doc []byte, dialect string) (Schema, error) {
	var d schemaDoc
	if err := json.Unmarshal(doc, &d); err != nil {
		return Schema{}, fmt.Errorf("schema json: %w", err)
	}
	if d.Name == "" {
		return Schema{}, fmt.Errorf("schema: missing name")
	}
	if d.IDField == "" {
		return Schema{}, fmt.Errorf("schema: missing idField")
	}

	s := Schema{
		Name:         d.Name,
		IDField:      d.IDField,
		Fields:       make(map[string]FieldSpec, len(d.Fields)),
		DefaultSort:  d.DefaultSort,
		TiebreakSort: d.TiebreakSort,
	}

	binding := func(f docField) *docSQLBinding {
		if f.Bindings == nil {
			return nil
		}
		if dialect == "sqlite" {
			return f.Bindings.SQLite
		}
		return f.Bindings.Postgres
	}
	seen := map[string]bool{}
	// First pass: collect Expr per field so sort.field can reference another
	// field's expression.
	exprByName := map[string]string{}
	for _, f := range d.Fields {
		if dialect == "sqlite" {
			if f.Name == "" || seen[f.Name] {
				return Schema{}, fmt.Errorf("duplicate or empty SQLite field %q", f.Name)
			}
			seen[f.Name] = true
		}
		if b := binding(f); b != nil {
			exprByName[f.Name] = b.Expr
		}
	}

	for _, f := range d.Fields {
		if binding(f) == nil {
			continue // derived / client-only field
		}
		pg := binding(f)
		if pg.Expr == "" {
			return Schema{}, fmt.Errorf("field %q: empty %s expr", f.Name, dialect)
		}

		kindStr := pg.Kind
		if kindStr == "" {
			kindStr = f.Type
		}
		kind, err := kindFromString(kindStr)
		if err != nil {
			return Schema{}, fmt.Errorf("field %q: %w", f.Name, err)
		}

		serverFilter := true
		var filterOps []string
		if f.Filter != nil {
			if f.Filter.Enabled != nil {
				serverFilter = *f.Filter.Enabled
			}
			if f.Filter.Pushdown != nil {
				serverFilter = serverFilter && *f.Filter.Pushdown
			}
			if f.Filter.Ops != nil {
				filterOps = make([]string, len(f.Filter.Ops))
				copy(filterOps, f.Filter.Ops)
			}
		}

		sortable := true
		sortExpr := pg.Expr
		sqliteSortField := ""
		if f.Sort != nil {
			if f.Sort.Enabled != nil {
				sortable = *f.Sort.Enabled
			}
			if f.Sort.Field != "" {
				if e, ok := exprByName[f.Sort.Field]; ok {
					sortExpr = e
					if dialect == "sqlite" {
						sqliteSortField = f.Sort.Field
					}
				} else if dialect == "sqlite" {
					return Schema{}, fmt.Errorf("field %q has unbound SQLite sort target %q", f.Name, f.Sort.Field)
				}
			}
		}

		var groupable *bool
		var aggregateOps []string
		if f.Aggregate != nil {
			groupable = f.Aggregate.Groupable
			aggregateOps = f.Aggregate.Ops
			if (f.Aggregate.Enabled != nil && !*f.Aggregate.Enabled) || (f.Aggregate.Measure != nil && !*f.Aggregate.Measure) {
				aggregateOps = []string{}
			}
		}
		s.Fields[f.Name] = FieldSpec{
			ExpressionNumeric:    pg.ExpressionNumeric,
			SQLiteDatetimeFormat: pg.DatetimeFormat,
			SQLiteSortField:      sqliteSortField,
			Groupable:            groupable,
			AggregateOps:         aggregateOps,
			Name:                 f.Name,
			Kind:                 kind,
			Expr:                 pg.Expr,
			Synthetic:            pg.Synthetic,
			ServerFilter:         serverFilter,
			FilterOps:            filterOps,
			ArrayCaseSensitive:   f.Filter != nil && f.Filter.ArrayCaseSensitive,
			Sortable:             sortable,
			SortExpr:             sortExpr,
		}
	}
	if dialect == "sqlite" {
		if err := validateSQLiteSchema(s); err != nil {
			return Schema{}, err
		}
	}
	return s, nil
}
