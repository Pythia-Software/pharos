package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

var teamsUsageV2MaxRows = 10000

const teamsUsageV2MaxFacts = 100000

// teamsOwnedUsageFact is an internal input whose caller has already verified
// source ownership, child/anchor assignments and protection in this same
// transaction. The production acquisition path remains fail-closed.
type teamsOwnedUsageFact struct {
	ID                               string
	At                               time.Time
	ModelFamily, Project, Protection string
	Total                            int64
	CostMicros                       *int64
	PriceAssumed                     bool
}
type teamsUsageV2Result struct {
	Rows           []map[string]any
	MissingPrice   bool
	AssumedPrice   bool
	ProjectOmitted bool
}

func teamsUsageV2Schema() qt.Schema {
	groupable := true
	field := func(name string, kind qt.FieldKind) qt.FieldSpec {
		return qt.FieldSpec{Name: name, Kind: kind, Expr: `r."` + name + `"`, ServerFilter: true, Sortable: true, Groupable: &groupable, ExpressionNumeric: kind == qt.FieldNumber}
	}
	fields := map[string]qt.FieldSpec{}
	for _, x := range []struct {
		name string
		kind qt.FieldKind
	}{{"id", qt.FieldText}, {"first_usage_at", qt.FieldDatetime}, {"day", qt.FieldText}, {"model_family", qt.FieldEnum}, {"project_name", qt.FieldText}, {"total_tokens", qt.FieldNumber}, {"cost_usd", qt.FieldNumber}, {"priced_count", qt.FieldNumber}, {"eligible_count", qt.FieldNumber}, {"assumed_count", qt.FieldNumber}} {
		fields[x.name] = field(x.name, x.kind)
	}
	return qt.Schema{Name: "teams_certified_usage_v2", IDField: "id", Fields: fields}
}
func teamsUsageV2Request(plan teamsUsageQueryV2, now time.Time) qt.QueryRequestV3 {
	summaries := make([]qt.SummaryColumn, 0, len(plan.Metrics)+2)
	selects := append([]string{}, plan.GroupBy...)
	for _, m := range plan.Metrics {
		summaries = append(summaries, qt.SummaryColumn{ID: "q1_metric_" + m.ID, Op: "sum", Field: m.Field})
		selects = append(selects, "q1_metric_"+m.ID)
	}
	summaries = append(summaries, qt.SummaryColumn{ID: "_q_eligible", Op: "sum", Field: "eligible_count"}, qt.SummaryColumn{ID: "_q_priced", Op: "sum", Field: "priced_count"}, qt.SummaryColumn{ID: "_q_assumed", Op: "sum", Field: "assumed_count"})
	selects = append(selects, "_q_eligible", "_q_priced", "_q_assumed")
	return qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: now.UnixMilli(), Rows: true,
		Source:   qt.WireQuery{Where: []qt.WhereTerm{{Field: "first_usage_at", Op: ">=", Value: plan.Start.Format(time.RFC3339Nano)}, {Field: "first_usage_at", Op: "<", Value: plan.End.Format(time.RFC3339Nano)}}},
		Grouping: &qt.GroupingDefinition{Keys: append([]string{}, plan.GroupBy...), Summaries: summaries}, Result: qt.WireQuery{Select: selects, Limit: teamsUsageV2MaxRows}}
}

// teamsExecuteUsageV2In is the local Query Table engine seam. Its caller must
// already have verified source/ownership/protection in this same transaction;
// the only production entry remains closed by teamsRequireTypedUsageEligibility.
// No cached or unrestricted Usage source is read here.
func teamsExecuteUsageV2In(ctx context.Context, tx *sql.Tx, plan teamsUsageQueryV2, facts []teamsOwnedUsageFact, now time.Time) (out teamsUsageV2Result, err error) {
	if tx == nil || len(facts) > teamsUsageV2MaxFacts || !plan.valid() {
		return out, errTeamsQueryUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, queryTableDeadline)
	defer cancel()
	// Project values become visible only if an owned non-private fact in the
	// exact time/filter population witnesses that value. This SQL has no source
	// outside the already-scoped temp relation.
	source := `WITH witnesses AS (SELECT DISTINCT project_name FROM teams_q1_scoped_usage WHERE protection='non_private' AND project_name IS NOT NULL) SELECT s.id,s.first_usage_at,s.day,s.model_family,s.project_name,s.total_tokens,s.cost_usd,s.priced_count,s.eligible_count,s.assumed_count FROM teams_q1_scoped_usage s WHERE s.project_name IS NOT NULL AND EXISTS(SELECT 1 FROM witnesses w WHERE w.project_name=s.project_name)`
	d := qt.QueryDatasetV3{Schema: teamsUsageV2Schema(), SourceSQL: source, Scope: "teams", Dataset: "certified_usage_v2", Dialect: "sqlite", MaxRows: teamsUsageV2MaxRows, MaxGroups: teamsUsageV2MaxRows, MaxPopulation: teamsUsageV2MaxFacts, MaxStagingBytes: 64 << 20}
	request := teamsUsageV2Request(plan, now)
	dependencies, err := d.SourcePlan(ctx, request, qt.PlanOptions{Identity: "teams:certified_usage_v2"})
	if err != nil {
		return out, err
	}
	allowed := map[string]bool{"id": true, "first_usage_at": true, "day": true, "model_family": true, "project_name": true, "total_tokens": true, "cost_usd": true, "priced_count": true, "eligible_count": true, "assumed_count": true}
	for _, name := range dependencies.BaseFields {
		if !allowed[name] {
			return out, errTeamsQueryUnsupported
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE teams_q1_scoped_usage (id TEXT PRIMARY KEY,first_usage_at TEXT NOT NULL,day TEXT NOT NULL,model_family TEXT NOT NULL,project_name TEXT,protection TEXT NOT NULL,total_tokens INTEGER NOT NULL,cost_usd INTEGER,priced_count INTEGER NOT NULL,eligible_count INTEGER NOT NULL,assumed_count INTEGER NOT NULL)`); err != nil {
		return out, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, e := tx.ExecContext(cleanupCtx, `DROP TABLE IF EXISTS teams_q1_scoped_usage`); e != nil {
			out = teamsUsageV2Result{}
			err = errors.Join(err, e)
		}
	}()
	var stagedBytes int64
	for _, f := range facts {
		stagedBytes += int64(len(f.ID) + len(f.ModelFamily) + len(f.Project) + len(f.Protection) + 96)
		if stagedBytes > 64<<20 || len(f.ID) > 128 || len(f.ModelFamily) > 128 || len(f.Project) > 512 || !utf8.ValidString(f.ID) || !utf8.ValidString(f.ModelFamily) || !utf8.ValidString(f.Project) {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		if (f.ModelFamily != "family_a" && f.ModelFamily != "family_b" && f.ModelFamily != "unknown") || strings.ContainsAny(f.Project, "\x00\u2028\u2029") {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		if f.ID == "" || f.At.IsZero() || f.ModelFamily == "" || f.Total < 0 || f.Total > teamsMaxExactInteger || (f.CostMicros != nil && (*f.CostMicros < 0 || *f.CostMicros > teamsMaxExactInteger)) {
			return teamsUsageV2Result{}, errTeamsSourceUnavailable
		}
		switch f.Protection {
		case "non_private", "explicit_private", "inherited_private", "flagged", "unknown":
		default:
			return teamsUsageV2Result{}, errTeamsSourceUnavailable
		}
		// Time restriction precedes project witness or grouping.
		at := f.At.UTC()
		if at.Before(plan.Start) || !at.Before(plan.End) {
			continue
		}
		day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		var project any
		if f.Project != "" {
			project = f.Project
		}
		var cost any
		priced := 0
		assumed := 0
		if f.PriceAssumed && f.CostMicros == nil {
			return teamsUsageV2Result{}, errTeamsSourceUnavailable
		}
		if f.PriceAssumed {
			assumed = 1
		}
		if f.CostMicros != nil {
			cost = *f.CostMicros
			priced = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams_q1_scoped_usage(id,first_usage_at,day,model_family,project_name,protection,total_tokens,cost_usd,priced_count,eligible_count,assumed_count) VALUES(?,?,?,?,?,?,?,?,?,1,?)`, f.ID, at.Format(time.RFC3339Nano), day, f.ModelFamily, project, f.Protection, f.Total, cost, priced, assumed); err != nil {
			return teamsUsageV2Result{}, err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_q1_scoped_usage s WHERE s.project_name IS NULL OR NOT EXISTS(SELECT 1 FROM teams_q1_scoped_usage w WHERE w.protection='non_private' AND w.project_name=s.project_name))`).Scan(&out.ProjectOmitted); err != nil {
		return teamsUsageV2Result{}, err
	}
	var witnessed int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams_q1_scoped_usage s WHERE s.project_name IS NOT NULL AND EXISTS(SELECT 1 FROM teams_q1_scoped_usage w WHERE w.protection='non_private' AND w.project_name=s.project_name)`).Scan(&witnessed); err != nil {
		return out, err
	}
	result, err := d.ExecuteIn(ctx, tx, request, qt.PlanOptions{Identity: "teams:certified_usage_v2"})
	if err != nil {
		return out, err
	}
	if result.Total != int64(len(result.Rows)) {
		return out, errTeamsQueryUnsupported
	}
	var represented int64
	for _, row := range result.Rows {
		eligible, ok := teamsExactResultInt(row["_q_eligible"])
		if !ok {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		represented += eligible
		if represented > witnessed {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		priced, ok := teamsExactResultInt(row["_q_priced"])
		if !ok || priced > eligible {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		assumed, ok := teamsExactResultInt(row["_q_assumed"])
		if !ok || assumed > priced {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		if assumed > 0 {
			out.AssumedPrice = true
		}
		projected := make(map[string]any, len(plan.GroupBy)+len(plan.Metrics))
		for _, dimension := range plan.GroupBy {
			projected[dimension] = row[dimension]
		}
		for _, metric := range plan.Metrics {
			alias := "q1_metric_" + metric.ID
			value := row[alias]
			if metric.Field == "cost_usd" && priced != eligible {
				projected[metric.ID] = nil
				out.MissingPrice = true
				continue
			}
			v, valid := teamsExactResultInt(value)
			if !valid {
				return teamsUsageV2Result{}, errTeamsQueryUnsupported
			}
			projected[metric.ID] = v
		}
		out.Rows = append(out.Rows, projected)
	}
	if represented != witnessed {
		return teamsUsageV2Result{}, errTeamsQueryUnsupported
	}
	// Wire keys use canonical JSON arrays and bytewise order, independent of
	// locale or the Query Table executor's incidental grouping order.
	type keyedRow struct {
		key []byte
		row map[string]any
	}
	ordered := make([]keyedRow, 0, len(out.Rows))
	seen := map[string]bool{}
	for _, row := range out.Rows {
		key, e := teamsUsageV2Key(row)
		if e != nil {
			return teamsUsageV2Result{}, e
		}
		if seen[string(key)] {
			return teamsUsageV2Result{}, errTeamsQueryUnsupported
		}
		seen[string(key)] = true
		ordered = append(ordered, keyedRow{key, row})
	}
	sort.Slice(ordered, func(i, j int) bool { return bytes.Compare(ordered[i].key, ordered[j].key) < 0 })
	for i, row := range ordered {
		out.Rows[i] = row.row
	}
	return out, nil
}
func teamsUsageV2Key(row map[string]any) ([]byte, error) {
	day, a := row["day"].(string)
	model, b := row["model_family"].(string)
	project, c := row["project_name"].(string)
	if !a || !b || !c || !utf8.ValidString(project) || strings.ContainsAny(project, "\x00\u2028\u2029") {
		return nil, errTeamsQueryUnsupported
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([]string{day, model, project}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// teamsDecodeUsageV2Key accepts only the canonical byte form. Comparing the
// decoded and re-encoded form also rejects unpaired JSON surrogate escapes,
// which encoding/json would otherwise silently replace with U+FFFD.
func teamsDecodeUsageV2Key(raw []byte) ([]string, error) {
	if !utf8.Valid(raw) {
		return nil, errTeamsQueryUnsupported
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) != 3 {
		return nil, errTeamsQueryUnsupported
	}
	canonical, err := teamsUsageV2Key(map[string]any{"day": values[0], "model_family": values[1], "project_name": values[2]})
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, errTeamsQueryUnsupported
	}
	return values, nil
}
func teamsExactResultInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, v >= 0 && v <= teamsMaxExactInteger
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > float64(teamsMaxExactInteger) || math.Trunc(v) != v {
			return 0, false
		}
		return int64(v), true
	default:
		return 0, false
	}
}

func (c *Catalog) teamsExecuteUsageV2(ctx context.Context, plan teamsUsageQueryV2) (teamsUsageV2Result, error) {
	// There is no production bridge from unsigned/local candidate facts to typed
	// assignments yet. A server-authorized adapter must replace this closed gate.
	if err := teamsRequireTypedUsageEligibility(); err != nil {
		return teamsUsageV2Result{}, err
	}
	return teamsUsageV2Result{}, errTeamsSourceUnavailable
}
