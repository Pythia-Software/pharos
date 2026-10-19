package querytable

import (
	"context"
	"database/sql"
	"strconv"
)

// SQLComputedDefinitionResolver resolves canonical definitions in a host-owned
// transaction. Begin a read-only REPEATABLE READ transaction and use that same
// transaction to execute the resulting row/metric plans. Scope and Dataset must
// come from authorization, not an unvalidated request. It does not own Tx.
type SQLComputedDefinitionResolver struct {
	Tx             *sql.Tx
	Scope, Dataset string
}

func (r SQLComputedDefinitionResolver) ResolveComputed(ctx context.Context, id string) (ComputedColumn, error) {
	if r.Tx == nil || r.Scope == "" || r.Dataset == "" {
		return ComputedColumn{}, diagnostic("resolver_scope", "authorized transaction, scope and dataset required")
	}
	var c ComputedColumn
	var revision int64
	err := r.Tx.QueryRowContext(ctx, `SELECT id,label,language,language_version,source,revision FROM query_table_computed_columns WHERE scope=$1 AND dataset=$2 AND id=$3`, r.Scope, r.Dataset, id).Scan(&c.ID, &c.Label, &c.Expression.Language, &c.Expression.Version, &c.Expression.Source, &revision)
	if err == sql.ErrNoRows {
		return ComputedColumn{}, &PlanDiagnostic{Code: "definition_unavailable", Message: "computed definition missing or inaccessible", Field: "@computed/" + id}
	}
	if err != nil {
		return ComputedColumn{}, err
	}
	c.Revision = strconv.FormatInt(revision, 10)
	return c, nil
}
