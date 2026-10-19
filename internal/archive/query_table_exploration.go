package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

// Immutable query configuration is stored independently of dataset contents.
const qtQuerySnapshotsDDL = qt.QuerySnapshotsDDL

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

func requireQueryEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one query body")
	}
	return nil
}

// Writes are serialized with other catalog metadata updates. Quota checking
// and insertion share a transaction, so concurrent saves cannot overrun it.
const querySnapshotBytes = 64 << 20

// querySnapshotCount is a var so that tests can reach it with a few rows.
var querySnapshotCount int64 = 10_000

type pharosQuerySnapshotStore struct{ Catalog *Catalog }

func (s pharosQuerySnapshotStore) Resolve(ctx context.Context, scope, dataset, code string) (qt.QuerySnapshot, error) {
	return (qt.SQLQuerySnapshotStore{DB: s.Catalog.reader()}).Resolve(ctx, scope, dataset, code)
}
func (s pharosQuerySnapshotStore) Save(ctx context.Context, scope, dataset string, snapshot qt.QuerySnapshot) (qt.QuerySnapshot, error) {
	if err := qt.ValidateQuerySnapshot(snapshot); err != nil {
		return qt.QuerySnapshot{}, err
	}
	store := qt.SQLQuerySnapshotStore{DB: s.Catalog.DB}
	var stored qt.QuerySnapshot
	err := s.Catalog.writeTransaction(ctx, "query-snapshot", func(tx *sql.Tx) error {
		var err error
		stored, err = store.ResolveIn(ctx, tx, scope, dataset, snapshot.Code)
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
		stored, err = store.SaveIn(ctx, tx, scope, dataset, snapshot)
		return err
	})
	return stored, err
}
