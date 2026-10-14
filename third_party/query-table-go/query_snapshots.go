package querytable

import (
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Portable PostgreSQL/SQLite DDL. Snapshots never expire or cascade with named
// saved searches; the host owns retention, quotas and authorization.
const QuerySnapshotsDDL = `CREATE TABLE IF NOT EXISTS query_table_query_snapshots (
 scope text NOT NULL, dataset text NOT NULL, code text NOT NULL,
 token text NOT NULL, created_at bigint NOT NULL,
 PRIMARY KEY (scope,dataset,code)
)`

type QuerySnapshot struct {
	Version   int    `json:"version"`
	Code      string `json:"code"`
	Token     string `json:"token"`
	CreatedAt int64  `json:"createdAt"`
}
type QuerySnapshotRepository interface {
	Save(context.Context, string, string, QuerySnapshot) (QuerySnapshot, error)
	Resolve(context.Context, string, string, string) (QuerySnapshot, error)
}

// ErrQuerySnapshotQuota lets a host enforce retention/storage budgets without
// reporting an infrastructure failure. Existing immutable links stay readable.
var ErrQuerySnapshotQuota = errors.New("query snapshot storage quota exceeded")

var ErrQuerySnapshotConflict = errors.New("query snapshot code already identifies different state")
var querySnapshotCode = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// ValidateQuerySnapshot checks the portable snapshot envelope and bounds both
// encoded and expanded state. It does not authorize a scope or execute a query.
// Custom repositories must call it on direct writes as well as HTTP writes.
func ValidateQuerySnapshot(snapshot QuerySnapshot) error {
	if snapshot.Version != 1 || !querySnapshotCode.MatchString(snapshot.Code) || len(snapshot.Token) > 2*1024*1024 {
		return errors.New("invalid query snapshot")
	}
	if !strings.HasPrefix(snapshot.Token, "2j.") && !strings.HasPrefix(snapshot.Token, "2d.") {
		return errors.New("unsupported query token version")
	}
	payload, err := base64.RawURLEncoding.DecodeString(snapshot.Token[3:])
	if err != nil {
		return errors.New("invalid query token encoding")
	}
	if strings.HasPrefix(snapshot.Token, "2d.") {
		stream := flate.NewReader(bytes.NewReader(payload))
		defer stream.Close()
		payload, err = io.ReadAll(io.LimitReader(stream, 4*1024*1024+1))
		if err != nil {
			return errors.New("invalid compressed query token")
		}
	}
	if len(payload) > 4*1024*1024 {
		return errors.New("query state exceeds size limit")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(payload, &envelope) != nil || envelope == nil {
		return errors.New("invalid query state")
	}
	return nil
}

type SQLQuerySnapshotStore struct{ DB *sql.DB }

func (s SQLQuerySnapshotStore) Save(ctx context.Context, scope, dataset string, snapshot QuerySnapshot) (QuerySnapshot, error) {
	return saveQuerySnapshotIn(ctx, s.DB, scope, dataset, snapshot)
}

// SaveIn participates in a host-owned transaction. The host can serialize quota
// checks and insertion in that transaction, and owns commit/rollback. An existing
// code with the same token returns its original timestamp even at quota.
func (s SQLQuerySnapshotStore) SaveIn(ctx context.Context, tx *sql.Tx, scope, dataset string, snapshot QuerySnapshot) (QuerySnapshot, error) {
	if tx == nil {
		return QuerySnapshot{}, errors.New("nil query snapshot transaction")
	}
	return saveQuerySnapshotIn(ctx, tx, scope, dataset, snapshot)
}

type querySnapshotSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func saveQuerySnapshotIn(ctx context.Context, db querySnapshotSQL, scope, dataset string, snapshot QuerySnapshot) (QuerySnapshot, error) {
	if err := ValidateQuerySnapshot(snapshot); err != nil {
		return QuerySnapshot{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO query_table_query_snapshots(scope,dataset,code,token,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope, dataset, snapshot.Code, snapshot.Token, time.Now().UnixMilli()); err != nil {
		return QuerySnapshot{}, err
	}
	stored, err := resolveQuerySnapshotIn(ctx, db, scope, dataset, snapshot.Code)
	if err == nil && stored.Token != snapshot.Token {
		return QuerySnapshot{}, ErrQuerySnapshotConflict
	}
	return stored, err
}
func (s SQLQuerySnapshotStore) Resolve(ctx context.Context, scope, dataset, code string) (QuerySnapshot, error) {
	return resolveQuerySnapshotIn(ctx, s.DB, scope, dataset, code)
}

// ResolveIn lets quota policy recognize an idempotent save before rejecting a
// new write. The lookup uses the same authorized transaction as SaveIn.
func (s SQLQuerySnapshotStore) ResolveIn(ctx context.Context, tx *sql.Tx, scope, dataset, code string) (QuerySnapshot, error) {
	if tx == nil {
		return QuerySnapshot{}, errors.New("nil query snapshot transaction")
	}
	return resolveQuerySnapshotIn(ctx, tx, scope, dataset, code)
}

func resolveQuerySnapshotIn(ctx context.Context, db querySnapshotSQL, scope, dataset, code string) (QuerySnapshot, error) {
	snapshot := QuerySnapshot{Version: 1, Code: code}
	err := db.QueryRowContext(ctx, `SELECT token,created_at FROM query_table_query_snapshots WHERE scope=$1 AND dataset=$2 AND code=$3`, scope, dataset, code).Scan(&snapshot.Token, &snapshot.CreatedAt)
	return snapshot, err
}

// Matches httpQuerySnapshotStore. Authorize must check dataset access and write
// permission/CSRF on PUT and return a server-owned tenant/actor scope. Codes are
// lookup identifiers, not authorization. Nil authorization always denies.
func NewQuerySnapshotsHandler(repo QuerySnapshotRepository, authorize func(*http.Request, string, bool) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "method not allowed", 405)
			return
		}
		dataset, code := r.URL.Query().Get("dataset"), r.URL.Query().Get("code")
		if len(r.URL.Query()["dataset"]) != 1 || len(r.URL.Query()["code"]) != 1 || strings.TrimSpace(dataset) == "" || len(dataset) > 256 || !querySnapshotCode.MatchString(code) {
			http.Error(w, "invalid dataset or code", 400)
			return
		}
		if authorize == nil || repo == nil {
			http.Error(w, "forbidden", 403)
			return
		}
		scope, err := authorize(r, dataset, r.Method == http.MethodPut)
		if err != nil || scope == "" {
			http.Error(w, "forbidden", 403)
			return
		}
		var snapshot QuerySnapshot
		if r.Method == http.MethodPut {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024+1024))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF || snapshot.Code != code || ValidateQuerySnapshot(snapshot) != nil {
				http.Error(w, "invalid query snapshot", 400)
				return
			}
			snapshot, err = repo.Save(r.Context(), scope, dataset, snapshot)
		} else {
			snapshot, err = repo.Resolve(r.Context(), scope, dataset, code)
		}
		if errors.Is(err, ErrQuerySnapshotQuota) {
			http.Error(w, "query snapshot storage quota exceeded", http.StatusInsufficientStorage)
			return
		}
		if errors.Is(err, ErrQuerySnapshotConflict) {
			http.Error(w, "query snapshot conflict", 409)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "query snapshot unavailable", 404)
			return
		}
		if err != nil {
			http.Error(w, "query snapshot storage failed", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	})
}
