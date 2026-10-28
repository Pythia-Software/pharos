package archive

import (
	"context"
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
	if err := snapshotSQLite(c.ctx, item.path, temporary); err != nil {
		os.Remove(temporary)
		return nil, err
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
				snapshot.Generations = append([]capturedVersion{kept}, snapshot.Generations...)
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
	db, err := openReadOnlySQLite(src)
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
