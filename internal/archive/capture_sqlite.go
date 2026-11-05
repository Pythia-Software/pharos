package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	sqlite "modernc.org/sqlite"
)

func (c *sourceCapture) captureDatabases(items []captureItem, present map[string]bool) error {
	for _, item := range items {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		rel := path.Join(captureFilesDir, item.rel)
		state, err := sqliteDependencyState(item.path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				c.fail(item.path, err)
				present[rel] = true
			}
			continue
		}
		present[rel] = true
		c.result.DatabasesSeen++
		previous := c.snapshots[rel]
		if previous != nil {
			previous.MissingSince = ""
			if previous.Path == item.path && (c.session.recoveryID == "" || previous.RecoveryID == c.session.recoveryID) && previous.Source == state && c.intactSnapshot(previous) {
				c.result.SnapshotsUnchanged++
				// SQLite record capture exports every database on every run
				// (each group names this run's revisions), so a compressed
				// latest snapshot is restored to its original bytes once
				// rather than decoded on every capture.
				if c.source.Options["sqlite_record_capture"] == true && previous.Encoding != "" {
					if err := c.restorePlainSnapshot(previous); err != nil {
						// A full or failing drive stops the source, as
						// snapshot() does, rather than decoding the rest.
						var destination destinationError
						if errors.As(err, &destination) {
							return err
						}
						c.fail(item.path, err)
						continue
					}
				}
				if err := c.exportSnapshot(previous); err != nil {
					c.fail(item.path, err)
				}
				continue
			}
		}
		snapshot, err := c.snapshot(item, rel, state, previous)
		if err != nil {
			var destination destinationError
			if errors.As(err, &destination) {
				return err
			}
			c.fail(item.path, err)
			continue
		}
		c.snapshots[rel] = snapshot
		if err := c.exportSnapshot(snapshot); err != nil {
			c.fail(item.path, err)
		}
		snapshot.Generations = c.retain(item.path, snapshot.Generations)
		c.result.SnapshotsTaken++
		c.result.SnapshotBytes += snapshot.Size
		c.report()
		if err := c.commit(false); err != nil {
			return err
		}
		c.removePruned(false)
	}
	return nil
}

func sqliteState(file string) (sqliteSourceState, error) {
	info, err := os.Stat(file)
	if err != nil {
		return sqliteSourceState{}, err
	}
	if !info.Mode().IsRegular() {
		return sqliteSourceState{}, fs.ErrNotExist
	}
	state := sqliteSourceState{Size: info.Size(), MTimeNS: info.ModTime().UnixNano()}
	if wal, err := os.Stat(file + "-wal"); err == nil {
		state.WALSize, state.WALMTimeNS = wal.Size(), wal.ModTime().UnixNano()
	} else if !errors.Is(err, os.ErrNotExist) {
		return sqliteSourceState{}, err
	}
	return state, nil
}

func sqliteDependencyState(file string) (sqliteSourceState, error) {
	state, err := sqliteState(file)
	if err != nil {
		return state, err
	}
	info, err := os.Stat(file)
	if err != nil {
		return state, err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		state.Identity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	if state.WALSize > 0 {
		wal, err := os.Stat(file + "-wal")
		if err != nil {
			return state, err
		}
		if stat, ok := wal.Sys().(*syscall.Stat_t); ok {
			state.WALIdentity = fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
		}
	}
	return state, nil
}

// restorePlainSnapshot replaces an encoded latest snapshot with its original
// bytes, under the capture lock this writer holds: decoded and verified into
// a temporary, flushed, renamed over the name, then recorded plain at the
// next commit. A kill before that commit leaves an entry marked encoded over
// the original bytes, which every reader reads as plain (storedAs).
func (c *sourceCapture) restorePlainSnapshot(s *capturedSnapshot) error {
	file := filepath.Join(c.dir, filepath.FromSlash(s.Captured))
	v := c.storedSnapshot(s)
	if v.Encoding == "" {
		s.Encoding, s.StoredSize = "", 0
		return nil
	}
	if captureFault != nil {
		if err := captureFault("restore-plain", s.Captured); err != nil {
			return toDestination(err)
		}
	}
	if err := c.ensureSpace(v.Size); err != nil {
		return err
	}
	temporary := file + captureTempSuffix
	if err := materializeCaptured(c.ctx, file, temporary, v); err != nil {
		os.Remove(temporary)
		return err
	}
	out, err := os.Open(temporary)
	if err == nil {
		err = out.Sync() // F_FULLFSYNC before the original encoding is replaced
		out.Close()
	}
	if err == nil {
		err = os.Rename(temporary, file)
	}
	if err != nil {
		os.Remove(temporary)
		return toDestination(err)
	}
	syncDir(filepath.Dir(file))
	s.Encoding, s.StoredSize = "", 0
	return nil
}

// storedSnapshot is a latest snapshot's entry as its copy is actually stored
// (storedAs: a compression killed before its rename left it plain).
func (c *sourceCapture) storedSnapshot(s *capturedSnapshot) capturedVersion {
	return storedAs(filepath.Join(c.dir, filepath.FromSlash(s.Captured)), s.version())
}

// intactSnapshot is intact for a latest snapshot at the size it is stored at
// and its recorded captured mtime, which compression keeps. The file is
// opened only when an encoded entry's file has the original size.
func (c *sourceCapture) intactSnapshot(s *capturedSnapshot) bool {
	size := s.version().storedSize()
	if s.Encoding != "" {
		if info, err := os.Stat(filepath.Join(c.dir, filepath.FromSlash(s.Captured))); err == nil && info.Size() == s.Size && s.Size != s.StoredSize {
			size = c.storedSnapshot(s).storedSize()
		}
	}
	return c.intact(s.Captured, size, s.CapturedMTimeNS)
}

// snapshot replaces a database's latest snapshot, keeping the previous one as
// a generation. Retention keeps the newest capture_snapshot_generations.
func (c *sourceCapture) snapshot(item captureItem, rel string, state sqliteSourceState, previous *capturedSnapshot) (*capturedSnapshot, error) {
	if err := checkNativeSnapshotPause(c.dir); err != nil {
		return nil, err
	}
	if captureFault != nil {
		if err := captureFault("snapshot", rel); err != nil {
			return nil, toDestination(err)
		}
	}
	if err := reserveCaptureAcquisition(c.dir, state.Size+state.WALSize); err != nil {
		return nil, err
	}
	if err := c.ensureSpace(state.Size + state.WALSize); err != nil {
		return nil, err
	}
	destination := filepath.Join(c.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return nil, toDestination(err)
	}
	temporary := destination + captureTempSuffix
	os.Remove(temporary)
	// A kind whose database also holds credentials is copied row by row,
	// only what it keeps: what is never written cannot be left behind by a
	// capture that stops part way.
	copySnapshot := snapshotSQLite
	if keep := captureTables(c.source.Kind); keep != nil {
		copySnapshot = func(ctx context.Context, src, dst string) error { return copySQLiteRows(ctx, src, dst, keep) }
	}
	if err := copySnapshot(c.ctx, item.path, temporary); err != nil {
		os.Remove(temporary)
		return nil, err
	}
	if captureFault != nil {
		if err := captureFault("snapshot-written", rel); err != nil {
			os.Remove(temporary)
			return nil, toDestination(err)
		}
	}
	info, err := syncPath(temporary)
	if err != nil {
		os.Remove(temporary)
		return nil, toDestination(err)
	}
	snapshot := &capturedSnapshot{RecoveryID: c.session.recoveryID, Path: item.path, Captured: rel, Size: info.Size(), CapturedMTimeNS: info.ModTime().UnixNano(), CapturedAt: now(), Method: "sqlite-backup", Source: state}
	if previous != nil {
		snapshot.Generations = slices.Clone(previous.Generations)
		if c.session.generations > 0 || c.session.preserveUnindexed || c.source.Options["sqlite_record_capture"] == true {
			// An encoded (idle) latest snapshot moves into history encoded,
			// with the digest recorded when it was compressed.
			stored := c.storedSnapshot(previous)
			version := capturedVersion{Captured: previous.Captured, Size: previous.Size, CapturedAt: previous.CapturedAt, Source: stored.Source, Encoding: stored.Encoding, StoredSize: stored.StoredSize, SHA256: previous.SHA256}
			intact := c.intactSnapshot(previous)
			kept, err := c.preserve(version, intact)
			if err != nil && intact {
				os.Remove(temporary)
				return nil, err
			}
			// Otherwise an interrupted rotation may already have replaced the
			// latest, leaving its predecessor in history to adopt.
			if err == nil {
				snapshot.Generations = append([]capturedVersion{kept}, withoutVersion(snapshot.Generations, kept.Captured)...)
			}
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		os.Remove(temporary)
		return nil, toDestination(err)
	}
	snapshot.Generations = c.retain(item.path, snapshot.Generations)
	return snapshot, nil
}

// retain keeps ordinary generations plus every pinned or unverified input.
// An indexed marker is never a preservation receipt. Unique evidence is not
// evicted to make room; the acquisition space/capacity gate stops the writer.
func (c *sourceCapture) retain(sourcePath string, generations []capturedVersion) []capturedVersion {
	if c.session.recoveryID != "" || c.session.preserveUnindexed {
		return generations
	}
	kept := make([]capturedVersion, 0, len(generations))
	extra := 0
	for i, old := range generations {
		pinned, err := pinnedPath(c.dir, old.Captured)
		if i < c.session.generations || err != nil || pinned {
			kept = append(kept, old)
			continue
		}
		if c.source.Options["sqlite_record_capture"] == true {
			if !c.canPruneSnapshot(old.Captured) {
				kept = append(kept, old)
				continue
			}
		} else if c.indexed != nil && c.indexed.exists && !c.indexed.has(sourcePath, old.CapturedAt) {
			if extra < c.session.generations {
				kept = append(kept, old)
				extra++
				continue
			}
			c.result.Warnings = append(c.result.Warnings, captureError{Path: old.Captured, Error: "pruned a database snapshot that was never indexed; sessions deleted at the source before it was superseded may be lost"})
		}
		c.prune = append(c.prune, old.Captured)
	}
	return kept
}

// captureTable is what a capture keeps of a table: the columns, as
// expressions over the source's (each in place of the column it is
// named for), "" for each as it is; and the rows, "" for all of them.
type captureTable struct{ columns, where string }

// captureTables are the tables a capture of a source kind keeps, or nil to
// keep them all. A kind whose database also holds credentials keeps only the
// tables, or rows, its adapter reads.
func captureTables(kind string) map[string]captureTable {
	switch strings.ToLower(kind) {
	case "opencode":
		keep := map[string]captureTable{}
		for table := range opencodeCaptureTables {
			keep[table] = captureTable{}
		}
		return keep
	case "cursor":
		return cursorCaptureTables
	case "cursor-cli":
		return cursorCLICaptureTables
	case "zed":
		return zedCaptureTables
	}
	return nil
}

// copySQLiteRows writes to dst a copy of src holding only the tables keep
// names, with their indexes, and of each only the rows and columns it keeps,
// read in one transaction so the copy is consistent while the owning app
// keeps committing. Rows keep their rowids.
func copySQLiteRows(ctx context.Context, src, dst string, keep map[string]captureTable) error {
	for attempt := 0; ; attempt++ {
		err := copySQLiteRowsOnce(ctx, src, dst, keep)
		if err == nil || attempt >= 3 || !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "busy") {
			return err
		}
		os.Remove(dst)
		time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
	}
}

func copySQLiteRowsOnce(ctx context.Context, src, dst string, keep map[string]captureTable) error {
	source, err := openSourceSQLite(src)
	if err != nil {
		return err
	}
	defer source.Close()
	read, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer read.Rollback()
	schema, err := queryMapsContext(ctx, read, `SELECT type, name, tbl_name, sql FROM sqlite_schema WHERE sql IS NOT NULL AND type IN ('table','index') AND name NOT LIKE 'sqlite_%' ORDER BY type='index', name`)
	if err != nil {
		return err
	}
	var encoding string
	var user, application int64
	for pragma, value := range map[string]any{"encoding": &encoding, "user_version": &user, "application_id": &application} {
		if err := read.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(value); err != nil {
			return err
		}
	}
	// The copy is a temporary file until it is fsynced and renamed, so
	// SQLite need not journal or sync it.
	target, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dst}).String()+"?_pragma=journal_mode(off)&_pragma=synchronous(off)")
	if err != nil {
		return err
	}
	defer target.Close()
	target.SetMaxOpenConns(1)
	if _, err := target.ExecContext(ctx, fmt.Sprintf("PRAGMA encoding='%s'; PRAGMA user_version=%d; PRAGMA application_id=%d", encoding, user, application)); err != nil {
		return err
	}
	write, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer write.Rollback()
	indexes := []string{}
	for _, row := range schema {
		table, statement := firstString(row["tbl_name"]), firstString(row["sql"])
		spec, kept := keep[table]
		if !kept {
			continue
		}
		if firstString(row["type"]) == "index" {
			indexes = append(indexes, statement)
			continue
		}
		if _, err := write.ExecContext(ctx, statement); err != nil {
			return err
		}
		if err := copySQLiteTable(ctx, read, write, table, spec); err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
	}
	for _, statement := range indexes {
		if _, err := write.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return write.Commit()
}

// copySQLiteTable copies a table's kept rows and columns, with their rowids
// when it has them.
func copySQLiteTable(ctx context.Context, read, write *sql.Tx, table string, spec captureTable) error {
	quoted, err := quoteRecordIdentifier(table)
	if err != nil {
		return err
	}
	info, err := queryMapsContext(ctx, read, "SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		return err
	}
	names := []string{}
	for _, column := range info {
		name, err := quoteRecordIdentifier(firstString(column["name"]))
		if err != nil {
			return err
		}
		names = append(names, name)
	}
	columns := defaultString(spec.columns, strings.Join(names, ", "))
	where := ""
	if spec.where != "" {
		where = " WHERE " + spec.where
	}
	// A table without a rowid has none to keep.
	rowid := "rowid, "
	if _, err := read.ExecContext(ctx, "SELECT rowid FROM "+quoted+" LIMIT 0"); err != nil {
		rowid = ""
	}
	rows, err := read.QueryContext(ctx, "SELECT "+rowid+columns+" FROM "+quoted+where)
	if err != nil {
		return err
	}
	defer rows.Close()
	width := len(names)
	if rowid != "" {
		width++
	}
	insert, err := write.PrepareContext(ctx, "INSERT INTO "+quoted+"("+rowid+strings.Join(names, ", ")+") VALUES("+placeholders(width)+")")
	if err != nil {
		return err
	}
	defer insert.Close()
	values := make([]any, width)
	pointers := make([]any, width)
	for index := range values {
		pointers[index] = &values[index]
	}
	for count := 0; rows.Next(); count++ {
		if count%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		if _, err := insert.ExecContext(ctx, values...); err != nil {
			return err
		}
	}
	return rows.Err()
}

// snapshotSQLite writes a transactionally consistent copy of src to dst with
// SQLite's online backup API from a read-only, query-only connection. It was
// measured faster and lighter than VACUUM INTO, which also needs a connection
// that is not query-only.
func snapshotSQLite(ctx context.Context, src, dst string) error {
	for attempt := 0; ; attempt++ {
		err := backupSQLite(ctx, src, dst)
		if err == nil || attempt >= 3 || !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "busy") {
			return err
		}
		os.Remove(dst)
		time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
	}
}

// backupStepPages bounds how long a snapshot runs before it notices that
// its context has ended: 4096 pages is 16 MB at SQLite's default page size.
var backupStepPages int32 = 4096

func backupSQLite(ctx context.Context, src, dst string) error {
	db, err := openSourceSQLite(src)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Every step copies pages from the read transaction held here, so the
	// snapshot is consistent while the owning app keeps committing (WAL does
	// not block it). Without it each step would take a new read transaction,
	// and any foreign commit between steps would restart the copy.
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var tables int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema").Scan(&tables); err != nil {
		return err
	}
	// The snapshot is a temporary file until it is fsynced and renamed, so
	// SQLite need not journal or sync it.
	target := (&url.URL{Scheme: "file", Path: dst}).String() + "?_pragma=journal_mode(off)&_pragma=synchronous(off)"
	return conn.Raw(func(driverConn any) error {
		source, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("the SQLite driver cannot make online backups")
		}
		backup, err := source.NewBackup(target)
		if err != nil {
			return err
		}
		// Stepping in chunks lets a release (before an eject) stop a
		// multi-gigabyte snapshot promptly.
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				backup.Finish()
				return err
			}
			if more, err = backup.Step(backupStepPages); err != nil {
				backup.Finish()
				return err
			}
		}
		snapshot, err := backup.Commit()
		if err != nil {
			return err
		}
		defer snapshot.Close()
		// The copy inherits the source's WAL flag; a snapshot is one
		// self-contained file that opens read-only without -wal/-shm files.
		exec, ok := snapshot.(driver.ExecerContext)
		if !ok {
			return errors.New("the SQLite driver cannot configure the snapshot")
		}
		_, err = exec.ExecContext(ctx, "PRAGMA journal_mode=DELETE", nil)
		return err
	})
}
