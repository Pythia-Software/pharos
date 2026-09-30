package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gbdubs/pharos/internal/querytable"
)

func (s *Server) queryTableSchema(dataset string) (querytable.Schema, error) {
	if _, ok := sqlDatasetFor(dataset); !ok && dataset != "library" && dataset != "activity" && dataset != "usage" && dataset != "writing" && dataset != "mcp_calls" && dataset != "tl1_attempts" {
		return querytable.Schema{}, fmt.Errorf("unknown query-table dataset %q", dataset)
	}
	document, err := querySchemaDocument(dataset)
	if err != nil {
		return querytable.Schema{}, err
	}
	return querytable.LoadSchema(document)
}

// librarySearch reads a Library query-table request's search: its text, and
// substring=1 to match inside words too.
func librarySearch(ctx context.Context, values url.Values) SearchOptions {
	return SearchOptions{Query: values.Get("search"), Substring: values.Get("substring") == "1", ctx: ctx}
}

// queryTableRows returns a dataset's rows. Library rows carry at least fields,
// the ones the request filters, sorts, or groups on (see libraryFields).
func (s *Server) queryTableRows(ctx context.Context, dataset string, values url.Values, fields libraryFields) ([]map[string]any, error) {
	switch dataset {
	case "library":
		rows, _, err := s.libraryRows(ctx, values, fields, nil)
		return rows, err
	case "activity":
		payload := s.activity()
		runs, _ := payload["runs"].([]map[string]any)
		for _, run := range runs {
			total := integer(run["total_sources"])
			completed := integer(run["completed_sources"])
			progress := float64(0)
			if total > 0 {
				progress = float64(completed) / float64(total) * 100
			} else if firstString(run["state"]) != "running" {
				progress = 100
			}
			run["progress"] = progress
			run["result_summary"] = syncResultSummary(run["results"])
		}
		return runs, nil
	case "usage":
		return s.Catalog.localUsageRows(ctx)
	case "writing":
		rows, _, err := s.Catalog.writingData(ctx)
		return rows, err
	case "mcp_calls":
		return s.Catalog.mcpQueryRows(ctx)
	case "tl1_attempts":
		return s.Catalog.cachedTL1AttemptRows(ctx)
	default:
		return nil, fmt.Errorf("unknown query-table dataset %q", dataset)
	}
}

func whereFields(where []querytable.WhereTerm) []string {
	fields := []string{}
	for _, term := range where {
		for _, clause := range term.Predicates() {
			fields = append(fields, clause.Field)
		}
	}
	return fields
}

func syncResultSummary(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name := firstString(item["source"])
		if failure := firstString(item["error"]); failure != "" {
			parts = append(parts, name+": "+failure)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %d workspaces, %d messages", name, integer(item["workspaces"]), integer(item["messages"])))
	}
	return strings.Join(parts, " · ")
}

func queryTableRoute(path string) (dataset, operation string, ok bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/api/query/"), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], "rows", true
	}
	if len(parts) == 2 && parts[0] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func (s *Server) getQueryTable(w http.ResponseWriter, r *http.Request, dataset, operation string) {
	if operation != "distinct" && operation != "field-stats" {
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
		return
	}
	schema, err := s.queryTableSchema(dataset)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if operation == "field-stats" {
		s.getFieldStats(w, r, dataset, schema)
		return
	}
	field := r.URL.Query().Get("field")
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			limit = parsed
		}
	}
	if table, ok := sqlDatasetFor(dataset); ok {
		if err := s.Catalog.prepareSQLDataset(r.Context(), dataset); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		result, err := table.Distinct(r.Context(), s.Catalog.DB, field, r.URL.Query().Get("q"), limit, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, result, http.StatusOK)
		return
	}
	rows, err := s.queryTableRows(r.Context(), dataset, r.URL.Query(), libraryFieldsFor(field))
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	result, err := querytable.Distinct(rows, field, r.URL.Query().Get("q"), limit, schema)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, result, http.StatusOK)
}

// getFieldStats answers GET /api/query/{dataset}/field-stats?fields=a,b with
// dataset-wide stats for the column picker. Like distinct values, they ignore
// the table's filters but follow the Library's semantic ?search= result set.
func (s *Server) getFieldStats(w http.ResponseWriter, r *http.Request, dataset string, schema querytable.Schema) {
	names := []string{}
	for name := range strings.SplitSeq(r.URL.Query().Get("fields"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	fields, err := querytable.StatFields(names, schema)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	var result map[string]querytable.FieldStat
	if table, ok := sqlDatasetFor(dataset); ok {
		if err := s.Catalog.prepareSQLDataset(r.Context(), dataset); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		result, err = table.FieldStats(r.Context(), s.Catalog.DB, fields, schema)
	} else {
		var rows []map[string]any
		if rows, err = s.queryTableRows(r.Context(), dataset, r.URL.Query(), libraryFieldsFor(fields...)); err == nil {
			result, err = querytable.FieldStats(rows, fields, schema)
		}
	}
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, result, http.StatusOK)
}

func (s *Server) postQueryTable(w http.ResponseWriter, r *http.Request, dataset, operation string) {
	schema, err := s.queryTableSchema(dataset)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1_000_000))
	if table, ok := sqlDatasetFor(dataset); ok {
		s.postSQLQueryTable(w, r, dataset, operation, table, schema, decoder)
		return
	}
	switch {
	case dataset == "writing" && operation == "series":
		var request writingSeriesRequest
		if err := decoder.Decode(&request); err != nil {
			writeError(w, fmt.Errorf("series json: %w", err), http.StatusBadRequest)
			return
		}
		value, err := s.Catalog.writingSeries(r.Context(), request, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, value, http.StatusOK)
	case operation == "rows":
		var query querytable.Query
		if err := decoder.Decode(&query); err != nil {
			writeError(w, fmt.Errorf("query json: %w", err), http.StatusBadRequest)
			return
		}
		if query.Limit == 0 {
			query.Limit = 100
		}
		// Only the page is shown, so it alone is completed with selected fields.
		needed := whereFields(query.Where)
		for _, order := range query.OrderBy {
			needed = append(needed, order.Field)
		}
		var rows []map[string]any
		var evidence map[string]*searchEvidence
		var find *libraryFindSet
		var err error
		if dataset == "library" {
			rows, find, err = s.libraryRows(r.Context(), r.URL.Query(), libraryFieldsFor(needed...), &evidence)
		} else {
			rows, err = s.queryTableRows(r.Context(), dataset, r.URL.Query(), libraryFieldsFor(needed...))
		}
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		result, err := querytable.Apply(rows, query, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		if dataset == "library" {
			if result.Rows, err = s.Catalog.libraryPage(r.Context(), result.Rows); err != nil {
				writeError(w, err, http.StatusInternalServerError)
				return
			}
			// explain=1 adds each row's "why" (see explainLibraryRows).
			if r.URL.Query().Get("explain") == "1" && len(evidence) > 0 {
				if err := s.Catalog.explainLibraryRows(r.Context(), result.Rows, r.URL.Query().Get("search"), evidence); err != nil {
					writeError(w, err, http.StatusInternalServerError)
					return
				}
			}
			if find != nil {
				find.attach(result.Rows)
				writeJSON(w, struct {
					querytable.Result
					Find libraryFindSummary `json:"find"`
				}{result, libraryFindSummary{Workspaces: len(find.matches), Limited: find.limited}}, http.StatusOK)
				return
			}
		}
		writeJSON(w, result, http.StatusOK)
	case operation == "aggregations":
		var request querytable.AggregationRequest
		if err := decoder.Decode(&request); err != nil {
			writeError(w, fmt.Errorf("aggregation json: %w", err), http.StatusBadRequest)
			return
		}
		needed := whereFields(request.Where)
		for _, aggregation := range request.Aggregations {
			if aggregation.Field != "" {
				needed = append(needed, aggregation.Field)
			}
			needed = append(needed, aggregation.GroupBy...)
		}
		rows, err := s.queryTableRows(r.Context(), dataset, r.URL.Query(), libraryFieldsFor(needed...))
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		result, err := querytable.Aggregate(rows, request, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		if dataset == "usage" {
			sortUsageTimeBuckets(request, result)
		}
		writeJSON(w, result, http.StatusOK)
	default:
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
	}
}

// prepareSQLDataset brings the derived table an SQL dataset reads up to date:
// the tool rollup for tool use, and for user messages, a classification that
// is missing or by older rules (see ensureAuthorship).
func (c *Catalog) prepareSQLDataset(ctx context.Context, dataset string) error {
	if dataset == "writing_messages" {
		c.ensureAuthorship()
		return nil
	}
	return c.currentToolRollup(ctx)
}

// postSQLQueryTable answers row and aggregation requests for SQL-backed
// datasets, which filter, sort, page, and group in SQLite.
func (s *Server) postSQLQueryTable(w http.ResponseWriter, r *http.Request, dataset, operation string, table sqlDataset, schema querytable.Schema, decoder *json.Decoder) {
	if err := s.Catalog.prepareSQLDataset(r.Context(), dataset); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if dataset == "tool_calls" {
		ready, err := s.Catalog.toolSearchReady(r.Context())
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		table.toolSearchIndexed = ready
	}
	switch operation {
	case "rows":
		var query querytable.Query
		if err := decoder.Decode(&query); err != nil {
			writeError(w, fmt.Errorf("query json: %w", err), http.StatusBadRequest)
			return
		}
		if query.Limit == 0 {
			query.Limit = 100
		}
		result, err := table.Rows(r.Context(), s.Catalog.DB, query, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		switch dataset {
		case "tool_calls":
			err = s.Catalog.priceToolCalls(result.Rows)
		case "writing_messages":
			err = s.Catalog.attachAuthoredText(r.Context(), result.Rows)
		}
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, result, http.StatusOK)
	case "aggregations":
		var request querytable.AggregationRequest
		if err := decoder.Decode(&request); err != nil {
			writeError(w, fmt.Errorf("aggregation json: %w", err), http.StatusBadRequest)
			return
		}
		result, err := table.Aggregate(r.Context(), s.Catalog.DB, request, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		sortUsageTimeBuckets(request, result)
		writeJSON(w, result, http.StatusOK)
	default:
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
	}
}
