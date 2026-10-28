package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

func (s *Server) queryTableSchema(dataset string) (querytable.Schema, error) {
	if _, ok := sqlDatasetFor(dataset); !ok && dataset != "library" && dataset != "activity" && dataset != "usage" && dataset != "conversation_activity" && dataset != "writing" && dataset != "mcp_calls" && dataset != "tl1_attempts" && dataset != "findings" && dataset != "parallelism" {
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
func (s *Server) queryTableRows(ctx context.Context, dataset string, values url.Values, fields libraryFields, where ...[]querytable.WhereTerm) ([]map[string]any, error) {
	record, err := s.recordSearch(ctx, dataset, values)
	if err != nil {
		return nil, err
	}
	defer record.close()
	if record != nil && fields != nil {
		fields = maps.Clone(fields)
		fields[record.field] = true
	}
	rows, err := s.queryTableSourceRows(ctx, dataset, values, fields, where...)
	if record == nil || err != nil {
		return rows, err
	}
	filtered := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row[record.field] == nil {
			continue
		}
		matched, e := record.matches(ctx, firstString(row[record.field]))
		if e != nil {
			return nil, e
		}
		if matched {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}

func (s *Server) queryTableSourceRows(ctx context.Context, dataset string, values url.Values, fields libraryFields, where ...[]querytable.WhereTerm) ([]map[string]any, error) {
	switch dataset {
	case "library":
		rows, _, err := s.libraryRows(ctx, values, fields, nil, where...)
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
	default:
		rows, ok, err := s.Catalog.datasetRows(ctx, dataset, fields, where...)
		if !ok {
			return nil, fmt.Errorf("unknown query-table dataset %q", dataset)
		}
		return rows, err
	}
}

// datasetRows returns the rows of a dataset the catalog holds in memory;
// ok is false for the others (the Library, activity and SQL datasets).
func (c *Catalog) datasetRows(ctx context.Context, dataset string, fields libraryFields, where ...[]querytable.WhereTerm) ([]map[string]any, bool, error) {
	var rows []map[string]any
	var err error
	switch dataset {
	case "usage":
		if !usageNeedsLineage(fields) {
			rows, err = c.localUsageBaseRows(ctx, false)
		} else {
			rows, err = c.localUsageRows(ctx)
		}
	case "conversation_activity":
		rows, err = c.conversationActivityRows(ctx)
	case "writing":
		rows, _, err = c.writingData(ctx)
	case "mcp_calls":
		rows, err = c.mcpQueryRows(ctx)
	case "tl1_attempts":
		rows, err = c.cachedTL1AttemptRows(ctx)
	case "findings":
		rows, err = c.findingQueryRows(ctx)
	case "parallelism":
		rows, err = c.parallelismQueryRows(ctx, fields, where...)
	default:
		return nil, false, nil
	}
	return rows, true, err
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
	if operation == "item" {
		s.getQueryItem(w, r, dataset)
		return
	}
	if operation == "capabilities" {
		s.getQueryCapabilities(w, r, dataset)
		return
	}
	if operation != "distinct" && operation != "field-stats" {
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
		return
	}
	// Keep one low-priority scan active, with separate queue and execution
	// budgets. A cold scan gets the normal query deadline after acquiring a slot.
	ctx, release, err := s.beginQueryMetadata(r.Context())
	if err != nil {
		writeError(w, err, http.StatusRequestTimeout)
		return
	}
	defer release()
	r = r.WithContext(ctx)

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
		record, e := s.recordSearch(r.Context(), dataset, r.URL.Query())
		if e != nil {
			writeError(w, e, http.StatusBadRequest)
			return
		}
		defer record.close()
		table, e = record.table(table)
		if e != nil {
			writeError(w, e, http.StatusBadRequest)
			return
		}
		if err := s.Catalog.prepareSQLDataset(r.Context(), dataset); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		if dataset == "writing_messages" {
			var e error
			table, _, e = s.Catalog.messageFindDataset(r.Context(), r.URL.Query(), table)
			if e != nil {
				writeError(w, e, http.StatusBadRequest)
				return
			}
		}
		result, err := table.Distinct(r.Context(), s.Catalog.reader(), field, r.URL.Query().Get("q"), limit, schema)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, result, http.StatusOK)
		return
	}
	// Metadata ignores the table's filters, so its Library search has a
	// different workspace scope. It cannot resume the filtered table's cursor.
	values := r.URL.Query()
	values.Del("find_cursor")
	rows, err := s.queryTableRows(r.Context(), dataset, values, queryFieldsFor(dataset, field))
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
// the table's filters but follow the Library's semantic ?search= and keyword
// find= result sets. Keyword metadata starts its own batch without a table cursor.
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
		record, e := s.recordSearch(r.Context(), dataset, r.URL.Query())
		if e != nil {
			writeError(w, e, http.StatusBadRequest)
			return
		}
		defer record.close()
		table, e = record.table(table)
		if e != nil {
			writeError(w, e, http.StatusBadRequest)
			return
		}
		if err := s.Catalog.prepareSQLDataset(r.Context(), dataset); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		if dataset == "writing_messages" {
			table, _, err = s.Catalog.messageFindDataset(r.Context(), r.URL.Query(), table)
			if err != nil {
				writeError(w, err, http.StatusBadRequest)
				return
			}
		}
		result, err = table.FieldStats(r.Context(), s.Catalog.reader(), fields, schema)
	} else {
		var rows []map[string]any
		values := r.URL.Query()
		values.Del("find_cursor")
		if rows, err = s.queryTableRows(r.Context(), dataset, values, queryFieldsFor(dataset, fields...)); err == nil {
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
	if operation == "query-v3" {
		s.postQueryV3(w, r, dataset)
		return
	}
	if r.URL.Query().Get("record_search") != "" {
		writeError(w, fmt.Errorf("record keyword search requires query-v3"), http.StatusBadRequest)
		return
	}
	if operation == "population" {
		writeError(w, fmt.Errorf("complete-population downloads are disabled; use server query execution"), http.StatusGone)
		return
	}
	// The legacy grouped route is gone. A page loaded before an upgrade can
	// still call it; reloading picks up /metrics.
	if operation == "aggregations" {
		writeError(w, fmt.Errorf("this version of Pharos no longer serves this request; reload the page"), http.StatusGone)
		return
	}
	if operation == "metrics" || operation == "rows-v2" {
		s.postQueryV2(w, r, dataset, operation)
		return
	}
	schema, err := s.queryTableSchema(dataset)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1_000_000)
	var body json.RawMessage
	if err := decodeQueryBody(r, &body); err != nil {
		writeError(w, err, 400)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if table, ok := sqlDatasetFor(dataset); ok {
		s.postSQLQueryTable(w, r, dataset, operation, table, schema, decoder)
		return
	}
	switch {
	case dataset == "usage" && operation == "carbon":
		var request carbonSeriesRequest
		if err := decoder.Decode(&request); err != nil {
			writeError(w, fmt.Errorf("carbon json: %w", err), http.StatusBadRequest)
			return
		}
		value, err := s.Catalog.carbonSeries(r.Context(), request)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, value, http.StatusOK)
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
			rows, find, err = s.libraryRows(r.Context(), r.URL.Query(), libraryFieldsFor(needed...), &evidence, query.Where)
		} else {
			needed = append(needed, query.Select...)
			fields := queryFieldsFor(dataset, needed...)
			// An omitted selection requests complete legacy Usage rows.
			if dataset == "usage" && len(query.Select) == 0 {
				fields = nil
			}
			rows, err = s.queryTableRows(r.Context(), dataset, r.URL.Query(), fields)
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
				if err := s.Catalog.attachLibraryFind(r.Context(), find, result.Rows); err != nil {
					writeError(w, err, http.StatusInternalServerError)
					return
				}
				writeJSON(w, struct {
					querytable.Result
					Find libraryFindSummary `json:"find"`
				}{result, find.summary()}, http.StatusOK)
				return
			}
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

// postSQLQueryTable answers legacy row requests for SQL-backed datasets,
// which filter, sort and page in SQLite.
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
		result, err := table.Rows(r.Context(), s.Catalog.reader(), query, schema)
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
	default:
		writeJSON(w, map[string]any{"error": "not found"}, http.StatusNotFound)
	}
}

func (s *Server) beginQueryMetadata(parent context.Context) (context.Context, func(), error) {
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	waiting, cancelWait := context.WithTimeout(parent, 10*time.Second)
	defer cancelWait()
	select {
	case s.queryMetadata <- struct{}{}:
	case <-waiting.Done():
		return nil, nil, waiting.Err()
	}
	execution, cancel := context.WithTimeout(parent, queryTableDeadline)
	return execution, func() { cancel(); <-s.queryMetadata }, nil
}
