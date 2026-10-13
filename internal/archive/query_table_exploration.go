package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// These are complete populations, never a page masquerading as clientRows.
// Large ledgers stay on SQL; callers narrow source WHERE before exploring.
const qtQuerySnapshotsDDL = qt.QuerySnapshotsDDL

const queryExplorationRows = 10_000
const queryExplorationBytes = 16 << 20

func (s *Server) querySnapshots(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	r = r.WithContext(ctx)
	qt.NewQuerySnapshotsHandler(pharosQuerySnapshotStore{Catalog: s.Catalog}, func(r *http.Request, dataset string, write bool) (string, error) {
		if !s.authorized(r) || !sameOrigin(r) {
			return "", errors.New("unauthorized")
		}
		schema, err := sqliteQuerySchema(strings.TrimPrefix(dataset, "pharos_"))
		if err != nil || schema.Name != dataset {
			return "", errors.New("unknown dataset")
		}
		return queryTableScope, nil
	}).ServeHTTP(w, r)
}

// Item identity is independent of the collection's filters, projection, and
// search. V2 still validates the field allowlist and applies host enrichment.
func (s *Server) getQueryItem(w http.ResponseWriter, r *http.Request, dataset string) {
	schema, err := sqliteQuerySchema(dataset)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	id := r.URL.Query().Get("id")
	if len(r.URL.Query()["id"]) != 1 || id == "" || len(id) > 10_000 {
		writeError(w, errors.New("one nonempty item ID is required"), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	request := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{
		Select: querySourceFields(schema), Where: []qt.WhereTerm{{Field: schema.IDField, Op: "=", Value: id}}, Limit: 1,
	}}
	result, err := s.executeQueryV2(ctx, dataset, nil, schema, &request, nil)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if len(result.Rows.Rows) == 0 {
		writeJSON(w, map[string]any{"row": nil}, 200)
		return
	}
	writeJSON(w, map[string]any{"row": result.Rows.Rows[0]}, 200)
}

func querySourceFields(schema qt.Schema) []string {
	names := make([]string, 0, len(schema.Fields))
	for name := range schema.Fields {
		if schema.Fields[name].Expr != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (s *Server) postQueryPopulation(w http.ResponseWriter, r *http.Request, dataset string) {
	schema, err := sqliteQuerySchema(dataset)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1_000_000)
	var input struct {
		Where []qt.WhereTerm `json:"where"`
	}
	decoder := json.NewDecoder(r.Body)
	var body json.RawMessage
	if err = decoder.Decode(&body); err != nil {
		writeError(w, err, 400)
		return
	}
	if err = requireQueryEOF(decoder); err != nil {
		writeError(w, err, 400)
		return
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		writeError(w, errors.New("expected a query object"), 400)
		return
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		writeError(w, err, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	request := qt.ServerQueryV2{Version: 2, Profile: qt.SQLiteExpressionProfile, WireQuery: qt.WireQuery{
		Select: querySourceFields(schema), Where: input.Where, Limit: queryExplorationRows,
	}}
	result, err := s.executeQueryV2(ctx, dataset, r.URL.Query(), schema, &request, nil, queryExplorationRows)
	if err != nil {
		var diagnostic *qt.PlanDiagnostic
		if errors.As(err, &diagnostic) && diagnostic.Code == "resource_limit" && diagnostic.Message == "matching population exceeds host budget" {
			err = fmt.Errorf("Narrow the source filters to at most %d rows for groups and calendar metrics.", queryExplorationRows)
		}
		writeError(w, err, 400)
		return
	}
	if result.Find != nil && (result.Find.Limited || result.Find.Search != nil && !result.Find.Search.Complete) {
		writeError(w, errors.New("The text search is incomplete. Narrow the search before exploring groups or calendar metrics."), 400)
		return
	}
	if result.Rows.Total > queryExplorationRows {
		writeError(w, fmt.Errorf("This source has %d rows. Narrow its source filters to at most %d rows for groups and calendar metrics.", result.Rows.Total, queryExplorationRows), 400)
		return
	}
	payload := map[string]any{"rows": result.Rows.Rows, "total": result.Rows.Total, "complete": true, "find": result.Find}
	encoded, err := json.Marshal(payload)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if len(encoded) > queryExplorationBytes {
		writeError(w, errors.New("This source exceeds the 16 MB exploration limit. Narrow its source filters."), 400)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(encoded)
}

func requireQueryEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one query body")
	}
	return nil
}

// Writes are serialized with other catalog metadata updates. Quota checking
// and insertion share a transaction, so concurrent saves cannot overrun it.
const querySnapshotBytes = 64 << 20
const querySnapshotCount = 10_000

type pharosQuerySnapshotStore struct{ Catalog *Catalog }

func (s pharosQuerySnapshotStore) Resolve(ctx context.Context, scope, dataset, code string) (qt.QuerySnapshot, error) {
	return (qt.SQLQuerySnapshotStore{DB: s.Catalog.DB}).Resolve(ctx, scope, dataset, code)
}
func (s pharosQuerySnapshotStore) Save(ctx context.Context, scope, dataset string, snapshot qt.QuerySnapshot) (qt.QuerySnapshot, error) {
	var stored qt.QuerySnapshot
	err := s.Catalog.writeTransaction(ctx, "query-snapshot", func(tx *sql.Tx) error {
		stored = qt.QuerySnapshot{Version: 1, Code: snapshot.Code}
		err := tx.QueryRowContext(ctx, `SELECT token,created_at FROM query_table_query_snapshots WHERE scope=? AND dataset=? AND code=?`, scope, dataset, snapshot.Code).Scan(&stored.Token, &stored.CreatedAt)
		if err == nil {
			if stored.Token != snapshot.Token {
				return qt.ErrQuerySnapshotConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count, bytes int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(token)),0) FROM query_table_query_snapshots WHERE scope=?`, scope).Scan(&count, &bytes); err != nil {
			return err
		}
		if count >= querySnapshotCount || bytes+int64(len(snapshot.Token)) > querySnapshotBytes {
			return qt.ErrQuerySnapshotQuota
		}
		stored = snapshot
		stored.CreatedAt = time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO query_table_query_snapshots(scope,dataset,code,token,created_at) VALUES(?,?,?,?,?)`, scope, dataset, stored.Code, stored.Token, stored.CreatedAt)
		return err
	})
	return stored, err
}
