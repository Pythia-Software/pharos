package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Existing dataset fixtures describe row and metric stages independently. These
// test-only builders send those inputs through the published V3 protocol.
// There is no V1/V2 execution or compatibility adapter in production.
type queryFixtureRows struct {
	qt.WireQuery
	Version           int               `json:"version"`
	Profile           string            `json:"profile"`
	ExpectedRevisions map[string]string `json:"expectedRevisions,omitempty"`
	PlanToken         string            `json:"planToken,omitempty"`
	Snapshot          string            `json:"snapshot,omitempty"`
}
type queryFixtureMetrics struct {
	Version           int               `json:"version"`
	Profile           string            `json:"profile"`
	Where             []qt.WhereTerm    `json:"where"`
	OrderBy           []qt.OrderBy      `json:"orderBy"`
	Limit             int               `json:"limit"`
	Offset            int               `json:"offset"`
	Metrics           []qt.AggSpec      `json:"metrics"`
	ExpectedRevisions map[string]string `json:"expectedRevisions,omitempty"`
	PlanToken         string            `json:"planToken,omitempty"`
	Snapshot          string            `json:"snapshot,omitempty"`
}
type queryFixtureMetricsResult struct {
	Metrics   []qt.MetricResult    `json:"metrics"`
	Execution qt.ComputedExecution `json:"execution"`
	Version   int                  `json:"version"`
}
type queryFixtureResult struct {
	Rows    *qt.QueryResultV3
	Metrics *queryFixtureMetricsResult
	Find    *libraryFindSummary
}
type queryFixtureDataset qt.QueryDatasetV3

func queryFixtureFor(_ string, schema qt.Schema) queryFixtureDataset {
	return queryFixtureDataset(queryV3Dataset(schema))
}
func fixtureQuery(rows *queryFixtureRows, metrics *queryFixtureMetrics, o qt.PlanOptions) qt.QueryRequestV3 {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	q := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: now.UnixMilli(), Rows: rows != nil}
	if rows != nil {
		q.Result = rows.WireQuery
		q.ExpectedRevisions = rows.ExpectedRevisions
		q.PlanToken = rows.PlanToken
		q.Snapshot = rows.Snapshot
	}
	if metrics != nil {
		q.Result = qt.WireQuery{Where: metrics.Where, OrderBy: metrics.OrderBy, Limit: metrics.Limit, Offset: metrics.Offset}
		q.Metrics = metrics.Metrics
		q.ExpectedRevisions = metrics.ExpectedRevisions
		q.PlanToken = metrics.PlanToken
		q.Snapshot = metrics.Snapshot
	}
	return q
}
func fixtureResult(out qt.QueryResultV3, find *libraryFindSummary, rows *queryFixtureRows, metrics *queryFixtureMetrics) queryFixtureResult {
	out.Now = 0
	result := queryFixtureResult{Find: find}
	if rows != nil {
		result.Rows = &out
	}
	if metrics != nil {
		result.Metrics = &queryFixtureMetricsResult{Metrics: out.Metrics, Execution: out.Execution, Version: 3}
	}
	return result
}
func (d queryFixtureDataset) executeFixture(ctx context.Context, db *sql.DB, rows *queryFixtureRows, metrics *queryFixtureMetrics, o qt.PlanOptions) (queryFixtureResult, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return queryFixtureResult{}, err
	}
	defer tx.Rollback()
	return d.executeFixtureIn(ctx, tx, rows, metrics, o)
}
func (d queryFixtureDataset) executeFixtureIn(ctx context.Context, tx *sql.Tx, rows *queryFixtureRows, metrics *queryFixtureMetrics, o qt.PlanOptions) (queryFixtureResult, error) {
	dataset := qt.QueryDatasetV3(d)
	if dataset.Dialect == "" {
		dataset.Dialect = "sqlite"
	}
	out, err := dataset.ExecuteIn(ctx, tx, fixtureQuery(rows, metrics, o), o)
	return fixtureResult(out, nil, rows, metrics), err
}
func (s *Server) executeQueryFixture(ctx context.Context, dataset string, params url.Values, schema qt.Schema, rows *queryFixtureRows, metrics *queryFixtureMetrics, _ ...int) (queryFixtureResult, error) {
	out, find, err := s.executeQueryV3(ctx, dataset, params, schema, fixtureQuery(rows, metrics, qt.PlanOptions{}))
	return fixtureResult(out, find, rows, metrics), err
}
func (d queryFixtureDataset) DescribeComputedPartialIn(ctx context.Context, tx *sql.Tx, ids []string, o qt.PlanOptions) (qt.ComputedDescription, error) {
	dataset := qt.QueryDatasetV3(d)
	if dataset.Dialect == "" {
		dataset.Dialect = "sqlite"
	}
	return dataset.DescribeComputedPartialIn(ctx, tx, ids, o)
}
func (d queryFixtureDataset) DescribeComputedIn(ctx context.Context, tx *sql.Tx, ids []string, o qt.PlanOptions) (qt.ComputedExecution, error) {
	dataset := qt.QueryDatasetV3(d)
	if dataset.Dialect == "" {
		dataset.Dialect = "sqlite"
	}
	description, err := dataset.DescribeComputedPartialIn(ctx, tx, ids, o)
	return description.Execution, err
}

// Migrate historical HTTP fixture inputs at construction, so their assertions
// still cover the same catalog behavior using actual V3 HTTP decoding/execution.
func queryFixtureHTTPRequest(method, target string, body io.Reader) *http.Request {
	u, err := url.Parse(target)
	if err != nil || method != http.MethodPost || !(strings.HasSuffix(u.Path, "/metrics") || strings.HasSuffix(u.Path, "/rows-v2")) {
		return httptest.NewRequest(method, target, body)
	}
	var data []byte
	if body != nil {
		data, _ = io.ReadAll(body)
	}
	var old map[string]json.RawMessage
	if json.Unmarshal(data, &old) != nil || old == nil {
		return httptest.NewRequest(method, strings.TrimSuffix(strings.TrimSuffix(target, "/metrics"), "/rows-v2")+"/query-v3", bytes.NewReader(data))
	}
	rows := strings.HasSuffix(u.Path, "/rows-v2")
	result := map[string]json.RawMessage{}
	for _, key := range []string{"select", "where", "orderBy", "limit", "offset"} {
		if value, ok := old[key]; ok {
			result[key] = value
			delete(old, key)
		}
	}
	stage, _ := json.Marshal(result)
	old["result"] = stage
	old["source"] = json.RawMessage(`{}`)
	old["rows"], _ = json.Marshal(rows)
	if _, ok := result["limit"]; !ok {
		result["limit"] = json.RawMessage(`50`)
		stage, _ = json.Marshal(result)
		old["result"] = stage
	}
	old["now"], _ = json.Marshal(time.Now().UnixMilli())
	old["version"] = json.RawMessage(`3`)
	if _, ok := old["profile"]; !ok {
		old["profile"], _ = json.Marshal(qt.SQLiteExpressionProfile)
	}
	delete(old, "diagnostics")
	data, _ = json.Marshal(old)
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/metrics"), "/rows-v2") + "/query-v3"
	return httptest.NewRequest(method, u.String(), bytes.NewReader(data))
}

func beginQueryMemory(ctx context.Context, db *sql.DB) (*sql.Tx, func() error, error) {
	return beginQueryWithTempStore(ctx, db, 2)
}
func beginQueryDisk(ctx context.Context, db *sql.DB) (*sql.Tx, func() error, error) {
	return beginQueryWithTempStore(ctx, db, 1)
}
func fixtureSourceFields(ctx context.Context, db *sql.DB, schema qt.Schema, rows *queryFixtureRows, metrics *queryFixtureMetrics, clock time.Time) ([]string, error) {
	s := &Server{Catalog: &Catalog{DB: db}}
	return s.queryV3SourceFields(ctx, schema, fixtureQuery(rows, metrics, qt.PlanOptions{Now: clock}))
}

func queryFixtureSourcePlan(ctx context.Context, schema qt.Schema, metrics queryFixtureMetrics) (qt.SourceDependencyPlan, error) {
	d := queryV3Dataset(schema)
	d.SourceSQL = (sqlDataset{}).querySourceSQL(schema)
	return d.SourcePlan(ctx, fixtureQuery(nil, &metrics, qt.PlanOptions{}), qt.PlanOptions{})
}
