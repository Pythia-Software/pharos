package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"github.com/gbdubs/pharos/internal/querytable"
	"github.com/gbdubs/pharos/schemas"
)

// The generated definitions share dashboard measures with the frontend. Each
// source is grouped before joining: joining messages onto usage would multiply
// both word counts and costs. Metric populations remain independent.
type scatterMetricDefinition struct {
	Dataset string            `json:"dataset"`
	Keys    map[string]string `json:"keys"`
	Time    struct {
		First string `json:"first"`
		Last  string `json:"last"`
	} `json:"time"`
	Where   []querytable.WhereTerm `json:"where"`
	Summary qt.SummaryColumn       `json:"summary"`
	Params  map[string]string      `json:"params"`
}

var scatterDefinitions = func() map[string]scatterMetricDefinition {
	data, err := schemas.Documents.ReadFile("scatter-metrics.json")
	if err != nil {
		panic(err)
	}
	var definitions map[string]scatterMetricDefinition
	if err := json.Unmarshal(data, &definitions); err != nil {
		panic(err)
	}
	return definitions
}()
var scatterMaxEntities = querytable.MaxLimit

func (s *Server) scatterRows(ctx context.Context, fields libraryFields, filters ...[]querytable.WhereTerm) ([]map[string]any, error) {
	unit := "conversation"
	entity := ""
	var window []querytable.WhereTerm
	// Only simple conjunctive time bounds are pushed into source datasets. All
	// other filters (including OR and NOT) still execute on the joined points.
	for _, terms := range filters {
		for _, term := range terms {
			if term.Field == "id" && term.Op == "=" && !term.Negated {
				if kind, key, ok := strings.Cut(term.Value, ":"); ok {
					unit, entity = kind, key
				}
			}
			if term.Field == "entity_id" && term.Op == "=" && !term.Negated {
				entity = term.Value
			}
			if term.Field == "point_kind" && term.Op == "=" && !term.Negated {
				unit = term.Value
			}
			if (term.Field == "first_at" || term.Field == "last_at") && !term.Negated && slices.Contains([]string{">", ">=", "<", "<="}, term.Op) {
				window = append(window, term)
			}
		}
	}
	if !slices.Contains([]string{"conversation", "work", "agent_session", "message"}, unit) {
		return nil, fmt.Errorf("unknown scatter point type %q", unit)
	}
	ids := []string{}
	for id := range scatterDefinitions {
		if fields == nil || fields[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) > 16 && entity == "" {
		return nil, fmt.Errorf("choose up to 16 scatter metrics per query")
	}
	// Metadata-only requests still have a population; never load every metric.
	if len(ids) == 0 {
		ids = []string{"human_typed", "tokens_cost"}
	}
	points := map[string]map[string]any{}
	for _, id := range ids {
		definition := scatterDefinitions[id]
		key := definition.Keys[unit]
		if key == "" {
			continue
		}
		schema, err := sqliteQuerySchema(definition.Dataset)
		if err != nil {
			return nil, err
		}
		where := slices.Clone(definition.Where)
		if entity != "" {
			where = append(where, querytable.WhereTerm{Field: key, Op: "=", Value: entity})
		}
		where = append(where, querytable.WhereTerm{Field: key, Op: "is_not_null"}, querytable.WhereTerm{Field: key, Op: "!=", Value: ""})
		for _, term := range window {
			if term.Field == "first_at" {
				term.Field = definition.Time.First
			} else {
				term.Field = definition.Time.Last
			}
			where = append(where, term)
		}
		summary := definition.Summary
		summaries := []qt.SummaryColumn{summary, {ID: "first_at", Op: "min", Field: definition.Time.First}, {ID: "last_at", Op: "max", Field: definition.Time.Last}}
		selected := []string{key, "value", "first_at", "last_at"}
		// Representative text is display metadata, never part of the join key.
		for _, field := range []string{"title", "provider", "repository_name", "project_name", "model"} {
			if _, ok := schema.Fields[field]; !ok {
				continue
			}
			summaries = append(summaries, qt.SummaryColumn{ID: "meta_" + field, Op: "row", Field: field})
			selected = append(selected, "meta_"+field)
			if field != "title" {
				summaries = append(summaries, qt.SummaryColumn{ID: "distinct_" + field, Op: "count_distinct", Field: field})
				selected = append(selected, "distinct_"+field)
			}
		}
		request := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true,
			Source: qt.WireQuery{Where: where}, Grouping: &qt.GroupingDefinition{Keys: []string{key}, Summaries: summaries, Representative: &qt.RepresentativeRow{OrderBy: []qt.OrderBy{{Field: schema.IDField, Dir: "asc"}}}},
			Result: qt.WireQuery{Select: selected, OrderBy: []qt.OrderBy{{Field: key, Dir: "asc"}}, Limit: scatterMaxEntities}}
		params := url.Values{}
		for name, value := range definition.Params {
			params.Set(name, value)
		}
		if definition.Dataset == "parallelism" {
			// Point identity requires conversation counting; keep the user's
			// saved activity gap instead of inventing a separate estimate.
			params.Set("parallelism_settings", fmt.Sprintf(`{"counting":"agent","gap":%d}`, s.Catalog.parallelismSettings(ctx).gap))
		}
		result, _, err := s.executeQueryV3(ctx, definition.Dataset, params, schema, request)
		if err != nil {
			return nil, fmt.Errorf("scatter metric %s: %w", id, err)
		}
		if result.Total > int64(len(result.Rows)) {
			// A diagnostic reaches the page as its message; a plain error
			// would read as the query failing.
			return nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeResourceLimit, Message: fmt.Sprintf("This range has more than %d points to plot; choose a shorter range", scatterMaxEntities)}
		}
		for _, row := range result.Rows {
			entity := firstString(row[key])
			if entity == "" {
				continue
			}
			point := points[entity]
			if point == nil {
				point = map[string]any{"id": unit + ":" + entity, "entity_id": entity, "point_kind": unit}
				points[entity] = point
			}
			if value := row["value"]; value != nil {
				if _, bad := value.(map[string]any); bad {
					return nil, fmt.Errorf("scatter metric %s could not be computed", id)
				}
				point[id] = value
			}
			for _, field := range []string{"title", "provider", "repository_name", "project_name", "model"} {
				text := firstString(row["meta_"+field])
				if text == "" {
					continue
				}
				distinct, _ := number(row["distinct_"+field])
				if field != "title" && (distinct > 1 || point[field] != nil && point[field] != text) {
					text = "Multiple"
				}
				if point[field] == nil || field != "title" {
					point[field] = text
				}
			}
			for _, field := range []string{"first_at", "last_at"} {
				text := firstString(row[field])
				if text == "" {
					continue
				}
				previous := firstString(point[field])
				if previous == "" || field == "first_at" && text < previous || field == "last_at" && text > previous {
					point[field] = text
				}
			}
		}
	}
	rows := make([]map[string]any, 0, len(points))
	for _, point := range points {
		if point["title"] == nil {
			point["title"] = point["entity_id"]
		}
		rows = append(rows, point)
	}
	slices.SortFunc(rows, func(a, b map[string]any) int {
		return strings.Compare(firstString(a["entity_id"]), firstString(b["entity_id"]))
	})
	return rows, nil
}
