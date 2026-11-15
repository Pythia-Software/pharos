package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

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
// search. V3 validates the field allowlist and applies host enrichment.
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
	request := qt.QueryRequestV3{Version: 3, Profile: qt.SQLiteExpressionProfile, Now: time.Now().UnixMilli(), Rows: true, Result: qt.WireQuery{
		Select: querySourceFields(schema), Where: []qt.WhereTerm{{Field: schema.IDField, Op: "=", Value: id}}, Limit: 1,
	}}
	result, _, err := s.executeQueryV3(ctx, dataset, url.Values{"carbon_settings": r.URL.Query()["carbon_settings"], "parallelism_settings": r.URL.Query()["parallelism_settings"]}, schema, request)
	if err != nil {
		writeError(w, err, 400)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if len(result.Rows) == 0 {
		body := map[string]any{"row": nil}
		if dataset == "agent_events" || dataset == "agent_event_messages" || dataset == "agent_event_rates" {
			// A saved event whose conversation is withheld until its events are
			// rebuilt (withheldAgentEvents) is pending, not gone.
			pending, err := agentEventItemPending(ctx, s.Catalog.reader(), dataset, id)
			if err != nil {
				writeError(w, err, 400)
				return
			}
			coverage, err := agentEventsCoverage(ctx, s.Catalog.reader())
			if err != nil {
				writeError(w, err, 400)
				return
			}
			body["pending"], body["coverage"] = pending, coverage
		}
		writeJSON(w, body, 200)
		return
	}
	row := result.Rows[0]
	if dataset == "tool_calls" {
		detail, detailErr := s.Catalog.toolCallDetail(ctx, id)
		if detailErr != nil {
			writeError(w, detailErr, 400)
			return
		}
		// Preserve canonical wire types and enrich only on explicit inspection.
		for _, name := range []string{"input_text", "input_text_bytes", "input_text_truncated", "result_text", "result_text_bytes", "result_text_truncated", "result_details", "commands", "urls", "request", "next_request"} {
			row[name] = detail[name]
		}
	}
	writeJSON(w, map[string]any{"row": row}, 200)
}

// agentEventItemPending is whether an agent-event item the dataset leaves out
// is still stored, with its conversation withheld (agentEventsWithheld) until
// the ordinary events run rebuilds it.
func agentEventItemPending(ctx context.Context, q queryer, dataset, id string) (bool, error) {
	present, err := agentEventsLedgerPresent(ctx, q)
	if err != nil || !present {
		return false, err
	}
	var rows []map[string]any
	switch dataset {
	case "agent_events":
		event, parseErr := strconv.ParseInt(strings.TrimPrefix(id, "e"), 10, 64)
		if parseErr != nil || !strings.HasPrefix(id, "e") {
			return false, nil
		}
		rows, err = queryMapsContext(ctx, q, `SELECT conversation_id FROM agent_events WHERE id=? AND probability>=?`, event, agentEventThreshold)
	case "agent_event_messages":
		rows, err = queryMapsContext(ctx, q, `SELECT conversation_id FROM agent_event_messages WHERE message_id=?`, id)
	case "agent_event_rates":
		// A rate row is message_id:family (agentEventRateDataset).
		cut := strings.LastIndex(id, ":")
		if cut < 0 {
			return false, nil
		}
		rows, err = queryMapsContext(ctx, q, `SELECT conversation_id FROM agent_event_facts WHERE message_id=?`, id[:cut])
	}
	if err != nil || len(rows) == 0 {
		return false, err
	}
	withheld, err := agentEventsWithheld(ctx, q)
	return slices.Contains(withheld, firstString(rows[0]["conversation_id"])), err
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
