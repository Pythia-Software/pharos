package archive

import (
	"context"
	"fmt"
	"strings"

	"github.com/gbdubs/pharos/internal/querytable"
)

// The message summary strip asks for ten scalar totals over the same source.
// Read that population once instead of repeating its joins for every category.
func (d sqlDataset) writingMessageSummary(ctx context.Context, q queryer, request querytable.AggregationRequest, schema querytable.Schema) (querytable.AggregationResult, bool, error) {
	if len(request.Aggregations) < 2 {
		return querytable.AggregationResult{}, false, nil
	}
	expressions := []string{"COUNT(*) AS n"}
	fields := whereFields(request.Where)
	for i, spec := range request.Aggregations {
		if len(spec.GroupBy) > 0 || spec.Op != "count" && spec.Op != "sum" {
			return querytable.AggregationResult{}, false, nil
		}
		value := "COUNT(*)"
		if spec.ID == "" {
			return querytable.AggregationResult{}, true, fmt.Errorf("aggregation id is required")
		}
		if spec.Field != "" {
			field, ok := schema.Fields[spec.Field]
			if !ok || spec.Op == "sum" && field.Kind != querytable.Number {
				return querytable.AggregationResult{}, false, nil
			}
			expr, err := d.expr(spec.Field)
			if err != nil {
				return querytable.AggregationResult{}, true, err
			}
			fields = append(fields, spec.Field)
			if spec.Op == "sum" {
				value = "SUM(" + expr + ")"
			} else {
				value = "COUNT(NULLIF(" + expr + ",''))"
			}
		} else if spec.Op != "count" {
			return querytable.AggregationResult{}, false, nil
		}
		expressions = append(expressions, fmt.Sprintf("%s AS v%d", value, i))
	}
	if err := schema.ValidateQuery(querytable.Query{Where: request.Where, Limit: 1}); err != nil {
		return querytable.AggregationResult{}, true, err
	}
	where, args, err := d.where(request.Where, schema)
	if err != nil {
		return querytable.AggregationResult{}, true, err
	}
	projected := d.project(fields)
	rows, err := queryMapsContext(ctx, q, "SELECT "+strings.Join(expressions, ",")+" "+projected.from+where, args...)
	if err != nil {
		return querytable.AggregationResult{}, true, err
	}
	out := querytable.AggregationResult{Metrics: []querytable.Metric{}}
	for i, spec := range request.Aggregations {
		value := rows[0][fmt.Sprintf("v%d", i)]
		if spec.Op == "sum" && value != nil {
			if n, ok := number(value); ok {
				value = n
			}
		}
		out.Metrics = append(out.Metrics, querytable.Metric{ID: spec.ID, Buckets: []querytable.Bucket{{Keys: []any{}, Value: value, Count: int(integer(rows[0]["n"]))}}})
	}
	return out, true, nil
}
