package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	qt "github.com/Pythia-Software/query-table/backends/go"
	"modernc.org/sqlite"
)

// A private SQLite driver owns this function. Normal and already-open legacy
// connections cannot supply it. This lane is not activated until every
// protected writer participates in the complete checkpoint protocol.
const teamsProtectedWriterFunction = "pharos_teams_protected_writer_v1"

type teamsProtectedConnector struct {
	driver *sqlite.Driver
	dsn    string
}

func (c *teamsProtectedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open(c.dsn)
}
func (c *teamsProtectedConnector) Driver() driver.Driver { return c.driver }

type teamsProtectedWriter struct {
	db               *sql.DB
	mu               sync.Mutex
	requireFence     bool
	expectedInstance string
	upgradePreflight bool
	// Set only by the checkpoint protocol while it owns the writer transaction.
	// Until that protocol is implemented, no production path grants authority.
	authorized atomic.Bool
}

func teamsOpenProtectedWriter(path string) (*teamsProtectedWriter, error) {
	w := &teamsProtectedWriter{}
	d := &sqlite.Driver{}
	// Persistent computed-column and search triggers need the same Query
	// Table and lexical functions on this distinct driver instance.
	for _, fn := range append(qt.SQLiteFunctions(), qt.SQLiteV2Functions()...) {
		if err := d.RegisterDeterministicScalarFunction(fn.Name, int32(fn.Arity), func(_ *sqlite.FunctionContext, values []driver.Value) (driver.Value, error) { return fn.Call(values) }); err != nil {
			return nil, err
		}
	}
	for _, descriptor := range qt.SQLiteV2Aggregates() {
		if err := d.RegisterFunction(descriptor.Name, &sqlite.FunctionImpl{NArgs: int32(descriptor.Arity), Deterministic: true, MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) {
			return &queryTableAggregate{fn: descriptor.New()}, nil
		}}); err != nil {
			return nil, err
		}
	}
	if err := d.RegisterDeterministicScalarFunction("pharos_lexical_text", 2, func(_ *sqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
		return messageLexicalText(sqliteText(values[0]), sqliteText(values[1])), nil
	}); err != nil {
		return nil, err
	}
	text := func(args []driver.Value, i int) string { value, _ := args[i].(string); return value }
	for _, entry := range []struct {
		name  string
		arity int32
		fn    func([]driver.Value) driver.Value
	}{
		{"machine_operation_key", 4, func(a []driver.Value) driver.Value {
			return machineOperationKey(text(a, 0), text(a, 1), text(a, 2), text(a, 3))
		}},
		{"machine_operation_name", 4, func(a []driver.Value) driver.Value {
			return machineOperationName(text(a, 0), text(a, 1), text(a, 2), text(a, 3))
		}},
		{"machine_activity", 5, func(a []driver.Value) driver.Value {
			return machineActivity(text(a, 0), text(a, 1), text(a, 2), text(a, 3), text(a, 4))
		}},
		{"model_effort", 2, func(a []driver.Value) driver.Value { return modelEffort(text(a, 0), text(a, 1)) }},
		{"model_family", 1, func(a []driver.Value) driver.Value {
			if strings.TrimSpace(text(a, 0)) == "" {
				return nil
			}
			return modelFamily(text(a, 0))
		}},
	} {
		if err := d.RegisterDeterministicScalarFunction(entry.name, entry.arity, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) { return entry.fn(a), nil }); err != nil {
			return nil, err
		}
	}
	if err := d.RegisterScalarFunction(teamsProtectedWriterFunction, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		if w.authorized.Load() {
			return int64(1), nil
		}
		return nil, errTeamsProtectionContinuity
	}); err != nil {
		return nil, err
	}
	// Do not inherit the non-durable test lane. FULL and immediate locking are
	// properties of every connection opened on this private pool.
	dsn := catalogDSNWithPragmas(path, durableCatalog) + "&_txlock=immediate"
	db := sql.OpenDB(&teamsProtectedConnector{driver: d, dsn: dsn})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	w.db = db
	return w, nil
}

func teamsBeginProtectedWrite(ctx context.Context, w *teamsProtectedWriter) (*sql.Tx, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var journal string
	var syncMode int
	if err = tx.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err == nil {
		err = tx.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&syncMode)
	}
	if err != nil || !strings.EqualFold(journal, "wal") || (syncMode != 2 && syncMode != 3) {
		_ = tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("protected write requires WAL and synchronous FULL")
	}
	return tx, nil
}
