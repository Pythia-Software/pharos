package querytable

import (
	"sort"
	"strings"
)

// Reached SELECT or shownRows metric bindings that are unused by filters and
// global sorting are evaluated on the page. The source
// may be read twice within this one statement snapshot; stable unique IDs are
// required across the full authorized source, as for ordinary row execution.
func (b *planBuilder) deferRowBindings(window, late int, early map[string]bool) {
	lateAliases := []string{}
	for name := range b.dependencies {
		if alias, ok := b.sourceAliases[name]; ok && !early[alias] {
			lateAliases = append(lateAliases, alias)
		}
	}
	sort.Strings(lateAliases)
	if len(lateAliases) == 0 {
		b.stages[late].Materialized = false
		return
	}
	absent := map[string]bool{}
	for alias := range b.sourceColumns {
		if !early[alias] {
			absent[alias] = true
		}
	}
	lateSet := map[string]bool{}
	for _, alias := range lateAliases {
		lateSet[alias] = true
	}
	for i, inputs := range b.emitInputs {
		retained := []string{}
		for _, alias := range inputs {
			// Only aliases actually supplied by the window/late projection are in
			// this namespace. Host SQL may happen to contain names like r.f12.
			if absent[alias] && (i <= window || !lateSet[alias]) {
				continue
			}
			retained = append(retained, alias)
		}
		b.emitInputs[i] = retained
	}
	columns := []string{"w.*"}
	for _, alias := range lateAliases {
		columns = append(columns, b.sourceColumns[alias])
	}
	// Omit late bindings from the initial projection even though they are live
	// downstream; the late stage supplies their compiler-owned aliases.
	for alias := range absent {
		delete(b.sourceColumns, alias)
	}
	b.lateSource = true
	b.sourceFrom = "qt_source_rows"
	for {
		collision := strings.Contains(b.options.SourceSQL, b.sourceFrom)
		for _, field := range b.schema.Fields {
			collision = collision || strings.Contains(field.Expr, b.sourceFrom)
		}
		if !collision {
			break
		}
		b.sourceFrom += "_"
	}
	id := b.schema.Fields[b.schema.IDField]
	key := id.Expr
	if id.Kind == FieldText || id.Kind == FieldEnum {
		key = "(" + key + ")::text COLLATE \"C\""
	}
	b.stages[late].SQL = "SELECT " + strings.Join(columns, ",") + " FROM " + b.stages[window].Name + " AS w JOIN " + b.sourceFrom + " AS r ON " + key + " = w." + b.sourceAliases[b.schema.IDField]
	delete(b.emitInputs, late)
}
