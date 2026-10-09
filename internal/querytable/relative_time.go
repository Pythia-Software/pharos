package querytable

import (
	"time"

	shared "github.com/Pythia-Software/query-table/backends/go"
)

// ResolveRelativeWhere preserves the saved operands while executing every
// datetime clause against the same clock, including OR groups.
func ResolveRelativeWhere(where []WhereTerm, schema Schema, clock time.Time) ([]WhereTerm, error) {
	out := append([]WhereTerm{}, where...)
	resolve := func(c WhereClause) (WhereClause, error) {
		if schema.Fields[c.Field].Kind == Datetime && c.Op != "is_null" && c.Op != "is_not_null" {
			value, err := shared.ResolveRelativeDatetime(c.Value, clock)
			if err != nil {
				return c, err
			}
			c.Value = value
		}
		return c, nil
	}
	for i, term := range out {
		if term.IsGroup() {
			out[i].Any = append([]WhereClause{}, term.Any...)
			for j, c := range term.Any {
				next, err := resolve(c)
				if err != nil {
					return nil, err
				}
				out[i].Any[j] = next
			}
		} else {
			c, err := resolve(term.Literal())
			if err != nil {
				return nil, err
			}
			out[i].Value = c.Value
		}
	}
	return out, nil
}
