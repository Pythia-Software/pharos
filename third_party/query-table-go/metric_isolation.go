package querytable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Request-level diagnostics describe the request, handshake, view or host
// configuration rather than one metric clause. They still fail the batch.
var requestDiagnostics = map[string]bool{
	"version": true, "query": true, "query_required": true, "residual_query": true, "profile_required": true, "profile_mismatch": true,
	"scope": true, "duplicate_metric": true, "view_mismatch": true, "filter": true, "sort": true, "window_required": true,
	"definition_changed": true, "resolver_required": true, "resolver_scope": true, "dataset": true,
	"snapshot_transaction_required": true, "snapshot_validation_required": true, "plan_token_validation_required": true,
	"source_required": true, "schema": true, "schema_limit": true, "identity_required": true, "duplicate_identity": true,
}

// metricClauseError reports whether err belongs to one metric clause, and the
// message to return for it. Cancellation and request-level diagnostics fail the
// request. Statement failures (for example a host statement timeout) are
// isolated but not exposed verbatim, matching the v3 HTTP handler.
func metricClauseError(ctx context.Context, err error) (string, bool) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	var issue *PlanDiagnostic
	if errors.As(err, &issue) {
		if requestDiagnostics[issue.Code] {
			return "", false
		}
		return issue.Message, true
	}
	return "could not execute metric", true
}

func failedMetric(id, scope, message string) SQLiteMetricV2 {
	if scope == "" {
		scope = "allMatching"
	}
	return SQLiteMetricV2{ID: id, Scope: scope, Buckets: []SQLiteMetricV2Bucket{}, Error: message}
}

// isolateMetric runs one metric inside a savepoint so a failed PostgreSQL
// statement does not abort the transaction for later metrics or the commit.
// Clause failures become that metric's error. Cancellation, request-level
// diagnostics and a savepoint that cannot be restored fail the request.
func isolateMetric(ctx context.Context, tx *sql.Tx, id, scope string, run func() (SQLiteMetricV2, error)) (SQLiteMetricV2, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT qt_metric"); err != nil {
		return SQLiteMetricV2{}, err
	}
	metric, err := run()
	if err == nil {
		if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT qt_metric"); err != nil {
			return SQLiteMetricV2{}, err
		}
		return metric, nil
	}
	message, clause := metricClauseError(ctx, err)
	if !clause {
		return SQLiteMetricV2{}, err
	}
	if _, rollback := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT qt_metric"); rollback != nil {
		return SQLiteMetricV2{}, errors.Join(err, rollback)
	}
	if _, release := tx.ExecContext(ctx, "RELEASE SAVEPOINT qt_metric"); release != nil {
		return SQLiteMetricV2{}, errors.Join(err, release)
	}
	return failedMetric(id, scope, message), nil
}

// sampleBudgetMessage names the sampled reduction and the host budget.
func sampleBudgetMessage(spec AggSpec, limit int) string {
	what := "MEDIAN"
	if spec.Distribution != nil {
		what = "The " + spec.Distribution.Kind + " distribution"
	}
	message := fmt.Sprintf("%s exceeded the %s-sample budget", what, groupedThousands(limit))
	if len(spec.GroupBy) > 0 {
		message += " for one or more groups"
	}
	return message
}

func groupedThousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
