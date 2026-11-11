package archive

import (
	"fmt"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// keepStatementJournalsInMemory raises the size at which SQLite moves a
// statement journal from memory to a temporary file, from 64 KiB to 64 MiB.
// A write inside a savepoint journals every page it changes; ingest does that
// for each record, and past the limit each page costs two writes to a file
// that is deleted when the transaction ends. Tests' transactions are small
// enough to stay in memory. The setting is process-wide and can only change
// while SQLite is shut down, so this runs before any connection opens.
func keepStatementJournalsInMemory() error {
	tls := libc.NewTLS()
	defer tls.Close()
	if rc := sqlite3.Xsqlite3_shutdown(tls); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite3_shutdown: %d", rc)
	}
	arguments := tls.Alloc(16)
	defer tls.Free(16)
	if rc := sqlite3.Xsqlite3_config(tls, sqlite3.SQLITE_CONFIG_STMTJRNL_SPILL, libc.VaList(arguments, int32(64<<20))); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite3_config(SQLITE_CONFIG_STMTJRNL_SPILL): %d", rc)
	}
	if rc := sqlite3.Xsqlite3_initialize(tls); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite3_initialize: %d", rc)
	}
	return nil
}
