package querytable

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DescribeComputedPartialIn works in the same authorized SQLite/PostgreSQL
// transaction as execution. The full editable catalogue remains host-owned;
// this envelope advertises only healthy transitive graphs and current revisions.
func (d QueryDatasetV3) DescribeComputedPartialIn(ctx context.Context, tx *sql.Tx, ids []string, o PlanOptions) (ComputedDescription, error) {
	out := ComputedDescription{Execution: SQLiteComputedExecution{Profile: d.Capabilities().Profile, ResolvedRevisions: map[string]string{}, Fields: map[string]SQLiteComputedFieldCapability{}}, Diagnostics: map[string]*PlanDiagnostic{}}
	if _, _, _, _, err := d.limits(); err != nil {
		return out, err
	}
	if len(ids) > 64 {
		return out, diagnostic("dependency_limit", "too many computed fields")
	}
	q := QueryRequestV3{Version: 3, Profile: out.Execution.Profile, Now: time.Now().UnixMilli()}
	options, err := d.options(ctx, tx, q, o)
	if err != nil {
		return out, err
	}
	original := options.Resolver
	var infrastructure error
	policyDataset := SQLiteV2Dataset{Schema: d.Schema, ComputedGroupable: d.ComputedGroupable}
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return ComputedDescription{}, err
		}
		id = strings.TrimPrefix(id, "@computed/")
		if !computedID.MatchString(id) {
			return out, diagnostic("definition_invalid", "invalid computed ID")
		}
		revisions := map[string]string{}
		current := options
		current.ExpectedRevisions = revisions
		current.Resolver = DefinitionResolverFunc(func(ctx context.Context, id string) (ComputedColumn, error) {
			column, e := original.ResolveComputed(ctx, id)
			if e != nil {
				var issue *PlanDiagnostic
				if !errors.As(e, &issue) {
					infrastructure = e
				}
			} else {
				revisions[id] = column.Revision
			}
			return column, e
		})
		name := "@computed/" + id
		p, e := compileRowExpressionDialect(ctx, "["+name+"]", d.Schema, current, d.Dialect == "sqlite")
		if infrastructure != nil {
			return ComputedDescription{}, infrastructure
		}
		if e != nil {
			var issue *PlanDiagnostic
			if !errors.As(e, &issue) {
				return ComputedDescription{}, e
			}
			copy := *issue
			out.Diagnostics[id] = &copy
			continue
		}
		capability := policyDataset.capabilityForPlan(name, p)
		capability.Filter = d.ComputedFilterable[id]
		out.Execution.Fields[name] = capability
		for key, revision := range p.ResolvedRevisions {
			out.Execution.ResolvedRevisions[key] = revision
		}
	}
	return out, nil
}
