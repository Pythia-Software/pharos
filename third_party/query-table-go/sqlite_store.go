package querytable

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// SQLiteComputedColumnsDDL stores canonical definitions. Hosts opt into v2
// execution separately by registering the v2 descriptors and executor.
// Requires SQLite 3.38+ (RETURNING).
const SQLiteComputedColumnsDDL = `CREATE TABLE IF NOT EXISTS query_table_computed_columns (
 scope TEXT NOT NULL,
 dataset TEXT NOT NULL,
 id TEXT NOT NULL,
 label TEXT NOT NULL,
 language TEXT NOT NULL,
 language_version INTEGER NOT NULL,
 source TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 PRIMARY KEY (scope,dataset,id)
) STRICT`

// SQLiteComputedColumnStore implements ComputedColumnRepository. Hosts supply
// a configured database and authorize the scope through NewComputedColumnsHandler.
// Saves use an atomic revision predicate; concurrent edits cannot overwrite one
// another. SQLITE_BUSY is a database failure, never a revision conflict.
type SQLiteComputedColumnStore struct{ DB *sql.DB }

func (s SQLiteComputedColumnStore) List(ctx context.Context, scope, dataset string) ([]ComputedColumn, error) {
	if s.DB == nil {
		return nil, errors.New("nil SQLite database")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,label,language,language_version,source,revision FROM query_table_computed_columns WHERE scope=? AND dataset=? ORDER BY label COLLATE BINARY,id COLLATE BINARY`, scope, dataset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComputedColumn{}
	for rows.Next() {
		var c ComputedColumn
		var revision int64
		if err = rows.Scan(&c.ID, &c.Label, &c.Expression.Language, &c.Expression.Version, &c.Expression.Source, &revision); err != nil {
			return nil, err
		}
		c.Revision = strconv.FormatInt(revision, 10)
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s SQLiteComputedColumnStore) Save(ctx context.Context, scope, dataset string, request SaveComputedColumnRequest) (ComputedColumn, error) {
	if s.DB == nil {
		return ComputedColumn{}, errors.New("nil SQLite database")
	}
	return sqliteSaveComputed(ctx, s.DB, scope, dataset, request)
}

// SaveIn lets a host validate the resulting canonical graph in the same
// transaction before committing. The caller owns commit/rollback.
func (s SQLiteComputedColumnStore) SaveIn(ctx context.Context, tx *sql.Tx, scope, dataset string, request SaveComputedColumnRequest) (ComputedColumn, error) {
	if tx == nil {
		return ComputedColumn{}, errors.New("nil SQLite transaction")
	}
	return sqliteSaveComputed(ctx, tx, scope, dataset, request)
}
func sqliteSaveComputed(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope, dataset string, request SaveComputedColumnRequest) (ComputedColumn, error) {
	c := request.Column
	if err := validateComputed(c); err != nil {
		return ComputedColumn{}, err
	}
	var row *sql.Row
	if request.ExpectedRevision == nil {
		row = db.QueryRowContext(ctx, `INSERT INTO query_table_computed_columns (scope,dataset,id,label,language,language_version,source) VALUES (?,?,?,?,?,?,?) ON CONFLICT DO NOTHING RETURNING revision`, scope, dataset, c.ID, c.Label, c.Expression.Language, c.Expression.Version, c.Expression.Source)
	} else {
		revision, err := strconv.ParseInt(*request.ExpectedRevision, 10, 64)
		if err != nil || revision < 1 {
			return ComputedColumn{}, ErrComputedConflict
		}
		row = db.QueryRowContext(ctx, `UPDATE query_table_computed_columns SET label=?,language=?,language_version=?,source=?,revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE scope=? AND dataset=? AND id=? AND revision=? RETURNING revision`, c.Label, c.Expression.Language, c.Expression.Version, c.Expression.Source, scope, dataset, c.ID, revision)
	}
	var revision int64
	if err := row.Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return ComputedColumn{}, ErrComputedConflict
	} else if err != nil {
		return ComputedColumn{}, err
	}
	c.Revision = strconv.FormatInt(revision, 10)
	return c, nil
}

// SQLiteComputedDefinitionResolver uses the same caller-owned transaction as
// v2 execution. Scope/Dataset must come from host authorization.
type SQLiteComputedDefinitionResolver struct {
	Tx             *sql.Tx
	Scope, Dataset string
}

func (r SQLiteComputedDefinitionResolver) ResolveComputed(ctx context.Context, id string) (ComputedColumn, error) {
	// SQLite accepts PostgreSQL-style numbered $ parameters as well. All SQL in
	// this resolver is portable and only its transaction semantics differ.
	return (SQLComputedDefinitionResolver{Tx: r.Tx, Scope: r.Scope, Dataset: r.Dataset}).ResolveComputed(ctx, id)
}
