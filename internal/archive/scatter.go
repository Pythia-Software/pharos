package archive

import (
	"context"
	"encoding/json"
	"errors"
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

// A source grouping returns at most one page of entities per query, so each
// metric reads its entities a page at a time. The joined points must still
// fit the outer grouping over the scatter dataset; beyond that the request
// asks for a narrower range. Tests lower both to keep fixtures tiny.
var scatterPageSize = querytable.MaxLimit
var scatterMaxEntities = queryV3MaxGroups

// afterScatterPage runs between two pages of one metric; tests change the
// catalog there.
var afterScatterPage = func() {}

// scatterReadAttempts bounds restarts of the complete join when its source
// generation changes. Partial pages and values from earlier metrics are discarded.
const scatterReadAttempts = 3

var errScatterSourceChanged = errors.New("scatter source changed")

func scatterTooManyPoints() error {
	// A diagnostic reaches the page as its message; a plain error would read
	// as the query failing.
	return &qt.PlanDiagnostic{Code: qt.ErrorCodeResourceLimit, Message: fmt.Sprintf("This range has more than %s points to plot; choose a shorter range", formatCount(scatterMaxEntities))}
}

func (s *Server) scatterRows(ctx context.Context, fields libraryFields, filters ...[]querytable.WhereTerm) ([]map[string]any, error) {
	// Each source owns its preparation and transaction lifetimes. Accept their
	// independent reads only if the whole catalog generation stayed unchanged;
	// an overlap key alone cannot detect changed values or balanced edits.
	now := time.Now().UnixMilli()
	for range scatterReadAttempts {
		before, err := readCacheGenerations(ctx, s.Catalog.reader())
		if err != nil {
			return nil, err
		}
		rows, readErr := s.scatterRowsAt(ctx, fields, now, before.all, filters...)
		if errors.Is(readErr, errScatterSourceChanged) {
			continue
		}
		after, err := readCacheGenerations(ctx, s.Catalog.reader())
		if err != nil {
			return nil, err
		}
		if before.all == after.all {
			return rows, readErr
		}
	}
	return nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeUnavailable, Message: "The library kept changing while this plot was read; try again in a moment"}
}

// scatterSourceSchema authorizes only our trusted, fixed scatter formulas to
// measure complete message text. The public Messages schema still disallows
// text aggregates, so neither the basic metric picker nor grouped columns
// offer an invalid SUM(text), and field statistics never scan full bodies.
func scatterSourceSchema(dataset string) (qt.Schema, error) {
	schema, err := sqliteQuerySchema(dataset)
	if err == nil && dataset == "messages" {
		text := schema.Fields["text"]
		text.AggregateOps = []string{"sum"}
		schema.Fields["text"] = text
	}
	return schema, err
}

func (s *Server) scatterRowsAt(ctx context.Context, fields libraryFields, now, generation int64, filters ...[]querytable.WhereTerm) ([]map[string]any, error) {
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
		schema, err := scatterSourceSchema(definition.Dataset)
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
		request := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: now, Rows: true,
			Source: qt.WireQuery{Where: where}, Grouping: &qt.GroupingDefinition{Keys: []string{key}, Summaries: summaries, Representative: &qt.RepresentativeRow{OrderBy: []qt.OrderBy{{Field: schema.IDField, Dir: "asc"}}}},
			Result: qt.WireQuery{Select: selected, OrderBy: []qt.OrderBy{{Field: key, Dir: "asc"}}, Limit: scatterPageSize}}
		params := url.Values{}
		for name, value := range definition.Params {
			params.Set(name, value)
		}
		if definition.Dataset == "parallelism" {
			// Point identity requires conversation counting; keep the user's
			// saved activity gap instead of inventing a separate estimate.
			params.Set("parallelism_settings", fmt.Sprintf(`{"counting":"agent","gap":%d}`, s.Catalog.parallelismSettings(ctx).gap))
		}
		rows, err := s.scatterSourceRows(ctx, id, definition.Dataset, params, schema, request, generation)
		if err != nil {
			return nil, err
		}
		if err := scatterMerge(points, unit, id, key, rows); err != nil {
			return nil, err
		}
		if len(points) > scatterMaxEntities {
			return nil, scatterTooManyPoints()
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

// scatterMerge adds one page of a metric's grouped rows to the points, keyed
// by recorded identity, with each point's representative display metadata.
func scatterMerge(points map[string]map[string]any, unit, id, key string, rows []map[string]any) error {
	for _, row := range rows {
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
				return fmt.Errorf("scatter metric %s could not be computed", id)
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
	return nil
}

// scatterSourceRows pages one metric within the complete join's generation.
// A changed generation stops immediately, before shifted offsets can be joined.
func (s *Server) scatterSourceRows(ctx context.Context, id, dataset string, params url.Values, schema qt.Schema, request qt.QueryRequestV3, generation int64) ([]map[string]any, error) {
	var rows []map[string]any
	for page := 0; ; page++ {
		request.Result.Offset = len(rows)
		if page > 0 {
			afterScatterPage()
		}
		result, _, err := s.executeQueryV3(ctx, dataset, params, schema, request)
		after, generationErr := readCacheGenerations(ctx, s.Catalog.reader())
		if generationErr != nil {
			return nil, generationErr
		}
		if after.all != generation {
			return nil, errScatterSourceChanged
		}
		if err != nil {
			var diagnostic *qt.PlanDiagnostic
			if errors.As(err, &diagnostic) && diagnostic.Code == qt.ErrorCodeResourceLimit {
				// A source can exceed its budget before it returns Total. Preserve
				// the reason without calling a byte/population limit a point count.
				issue := *diagnostic
				issue.Message += "; choose a shorter range"
				return nil, &issue
			}
			return nil, fmt.Errorf("scatter metric %s: %w", id, err)
		}
		if result.Total > int64(scatterMaxEntities) {
			return nil, scatterTooManyPoints()
		}
		rows = append(rows, result.Rows...)
		if int64(len(rows)) >= result.Total {
			return rows, nil
		}
		if len(result.Rows) == 0 {
			return nil, &qt.PlanDiagnostic{Code: qt.ErrorCodeUnavailable, Message: "Scatter points could not be read completely; try again"}
		}
	}
}
