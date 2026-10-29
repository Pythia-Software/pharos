package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Compacting the catalog gives the drive back the free pages deleted data
// leaves inside the file (removing tool output from the word index frees about
// 30 GB on a large library). SQLite reuses free pages for new data, so this is
// a one-time, explicit step, never automatic.
//
// It runs in the broker with the library's services stopped and its use lock
// held exclusively, so no other process has the catalog open. It writes a
// compacted copy beside the catalog with VACUUM INTO, rather than VACUUM in
// place: in place, VACUUM writes the whole database through the WAL as well as
// to a temporary file, and holds the writer throughout. The copy is checked
// (quick_check, every table's rows and, where SQLite assigns them, rowids)
// before it atomically replaces the catalog, so a failure at any step leaves
// the original untouched. VACUUM may renumber the rowids of tables without an
// INTEGER PRIMARY KEY, and tool_command_fts and text_search_documents depend
// on those of tool_calls and messages.

const (
	compactingSuffix    = ".compacting"
	preCompactionSuffix = ".pre-compaction"
	compactMarginBytes  = 1 << 30
)

// compactProgressEvery is how often the copy's size is reported; tests
// shorten it.
var compactProgressEvery = 2 * time.Second

// compactSpaceNeeded is the free space a copy of live bytes needs: VACUUM
// INTO can write a little more than the pages it keeps.
func compactSpaceNeeded(live int64) int64 {
	return live + live/10 + compactMarginBytes
}

// compactLeaseWait is how long compacting waits for other openers' leases
// to end; tests shorten it.
var compactLeaseWait = 10 * time.Second

// compactDiskFree is the free space on path's volume; tests replace it.
var compactDiskFree = func(path string) uint64 {
	_, free := diskUsage(path)
	return free
}

// compactFingerprint reads what the check compares, compactLink keeps the
// old catalog during the swap and compactOpen checks the new one; tests
// replace them.
var (
	compactFingerprint = catalogFingerprint
	compactLink        = os.Link
	compactOpen        = openCompacted
)

// compactionReceipt is what a compaction did.
type compactionReceipt struct {
	BeforeBytes int64         `json:"before_bytes"`
	AfterBytes  int64         `json:"after_bytes"`
	FreeBytes   int64         `json:"free_bytes"`
	Duration    time.Duration `json:"duration"`
}

// CompactLibrary compacts config's catalog. progress reports each step and,
// while the copy is written, how much of it is done.
func CompactLibrary(ctx context.Context, config Config, progress func(phase string, done, total int64)) (compactionReceipt, error) {
	started := time.Now()
	var receipt compactionReceipt
	report := func(phase string, done, total int64) {
		if progress != nil {
			progress(phase, done, total)
		}
	}
	if err := config.checkLibrary(libraryVolumeIdentity); err != nil {
		return receipt, err
	}
	if err := checkCatalogCompatibility(config.CatalogPath); err != nil {
		return receipt, err
	}
	if _, err := os.Stat(pendingDeliveryPath(config)); err == nil {
		return receipt, fmt.Errorf("this library is finishing a verified move; resume it before compacting")
	}
	lease, err := compactLease(ctx, config)
	if err != nil {
		return receipt, err
	}
	defer lease.Close()
	path := canonical(config.CatalogPath)
	staging, previous := path+compactingSuffix, path+preCompactionSuffix
	removeCompactionLeftovers(path)

	report("Preparing the catalog", 0, 0)
	source, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		return receipt, err
	}
	source.SetMaxOpenConns(1)
	defer func() {
		if source != nil {
			source.Close()
		}
	}()
	// The copy reads the WAL too, but the swap needs it empty: a log left
	// beside the new file would be replayed into it.
	var busy, frames, copied int64
	if err = source.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &copied); err != nil {
		return receipt, err
	}
	if busy != 0 {
		return receipt, fmt.Errorf("the catalog's change log could not be folded in; close other Pharos processes and compact again")
	}
	var pageSize, pages, free int64
	for pragma, into := range map[string]*int64{"page_size": &pageSize, "page_count": &pages, "freelist_count": &free} {
		if err = source.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(into); err != nil {
			return receipt, err
		}
	}
	receipt.BeforeBytes, receipt.FreeBytes = pages*pageSize, free*pageSize
	receipt.AfterBytes = receipt.BeforeBytes
	if free == 0 {
		receipt.Duration = time.Since(started)
		return receipt, nil
	}
	live := (pages - free) * pageSize
	if need := compactSpaceNeeded(live); compactDiskFree(filepath.Dir(path)) < uint64(need) {
		return receipt, fmt.Errorf("compacting needs %s free on the library's drive for the new copy; free up space and compact again", humanBytes(need))
	}

	report("Recording what the catalog holds", 0, 0)
	before, err := compactFingerprint(ctx, source)
	if err != nil {
		return receipt, err
	}
	report("Writing the compacted catalog", 0, live)
	if err = writeCompacted(ctx, source, staging, live, report); err != nil {
		removeStaging(staging)
		return receipt, err
	}
	if err = source.Close(); err != nil {
		removeStaging(staging)
		return receipt, err
	}
	source = nil
	if err = verifyCompacted(ctx, staging, before, report); err != nil {
		removeStaging(staging)
		return receipt, err
	}
	report("Replacing the catalog", 0, 0)
	if err = swapCompacted(path, staging, previous); err != nil {
		removeStaging(staging)
		return receipt, err
	}
	if info, statErr := os.Stat(path); statErr == nil {
		receipt.AfterBytes = info.Size()
	}
	receipt.Duration = time.Since(started)
	return receipt, nil
}

// compactLease takes the library's use lock exclusively. While the service
// is stopped, an agent's MCP call opens the catalog itself under a shared
// lease for the call alone, so a busy lock is retried for a few seconds; a
// Terminal command holds it until it ends.
func compactLease(ctx context.Context, config Config) (*os.File, error) {
	deadline := time.Now().Add(compactLeaseWait)
	for {
		lease, err := fileLease(libraryUseLock(config), true)
		if err == nil {
			return lease, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another Pharos process has this library open, such as a Terminal command or an agent's long Pharos MCP call; let it finish and compact again")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// removeStaging removes an unfinished copy and the rollback journal VACUUM
// INTO keeps beside it while it writes.
func removeStaging(staging string) {
	_ = os.Remove(staging)
	_ = os.Remove(staging + "-journal")
}

// removeCompactionLeftovers removes what a compaction killed part way left
// beside the catalog at path: an unfinished copy, or the link to the catalog
// a finished swap replaced. Call it under the library's use lease, which a
// running compaction holds exclusively. The link goes only once path opens as
// a catalog: then it is path itself (killed before the swap) or the original
// a checked and flushed copy replaced. If path does not open, the swap's
// rollback failed and the link may be the only copy of the original.
func removeCompactionLeftovers(path string) {
	removeStaging(path + compactingSuffix)
	if _, err := os.Stat(path + preCompactionSuffix); err == nil && checkCatalogCompatibility(path) == nil {
		_ = os.Remove(path + preCompactionSuffix)
	}
}

// writeCompacted writes source's compacted copy to staging and makes it
// durable, reporting the copy's size as it grows.
func writeCompacted(ctx context.Context, source *sql.DB, staging string, live int64, report func(string, int64, int64)) error {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(compactProgressEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// SQLite creates the copy at its first write, after its page
				// cache fills.
				written := int64(0)
				if info, err := os.Stat(staging); err == nil {
					written = min(info.Size(), live)
				}
				report("Writing the compacted catalog", written, live)
			}
		}
	}()
	_, err := source.ExecContext(ctx, "VACUUM INTO ?", staging)
	// No size report may land after the next step's.
	close(done)
	<-stopped
	if err != nil {
		return err
	}
	// os.File.Sync is F_FULLFSYNC: the copy is on the drive before it
	// replaces the catalog.
	return syncFile(staging)
}

// verifyCompacted checks the copy at staging against before, the source's
// fingerprint.
func verifyCompacted(ctx context.Context, staging string, before catalogPrint, report func(string, int64, int64)) error {
	report("Checking the compacted catalog", 0, 0)
	// VACUUM INTO writes a rollback-journal database, which opens read only
	// without a -shm beside it.
	compacted, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: staging}).String()+"?mode=ro")
	if err != nil {
		return err
	}
	defer compacted.Close()
	compacted.SetMaxOpenConns(1)
	rows, err := compacted.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	problems := []string{}
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			rows.Close()
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("the compacted copy failed its check, so the catalog was left as it was: %s", strings.Join(problems[:min(len(problems), 3)], "; "))
	}
	after, err := compactFingerprint(ctx, compacted)
	if err != nil {
		return err
	}
	if difference := before.difference(after); difference != "" {
		return fmt.Errorf("the compacted copy does not match the catalog (%s), so the catalog was left as it was", difference)
	}
	return nil
}

// swapCompacted replaces path with staging. path names a catalog at every
// moment: the old one until the rename, the new one after. previous keeps a
// link to the old catalog until the new one has opened, and puts it back if
// it does not.
func swapCompacted(path, staging, previous string) error {
	// The last connection closed and removed an empty log; a log left now
	// belongs to another opener and would be replayed into the new file.
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 0 {
		return fmt.Errorf("another process wrote to the catalog during compaction; the catalog was left as it was")
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	linked := compactLink(path, previous) == nil
	if err := os.Rename(staging, path); err != nil {
		if linked {
			_ = os.Remove(previous)
		}
		return err
	}
	syncDir(filepath.Dir(path))
	if err := compactOpen(path); err != nil {
		// The new file's log and index go with it; the original's were
		// removed before the swap.
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
		if linked && os.Rename(previous, path) == nil {
			syncDir(filepath.Dir(path))
			return fmt.Errorf("the compacted catalog did not open, so the original was put back: %w", err)
		}
		return fmt.Errorf("the compacted catalog did not open: %w", err)
	}
	if linked {
		_ = os.Remove(previous)
	}
	return nil
}

// openCompacted puts the new catalog back in WAL mode, as the service opens
// it, and checks that it opens.
func openCompacted(path string) error {
	db, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var mode string
	if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return fmt.Errorf("journal mode is %s", mode)
	}
	return checkCatalogCompatibility(path)
}

// catalogPrint is what a compaction must keep: the schema, every table's row
// count and, for each table whose rowids SQLite assigns, their sum and
// largest.
type catalogPrint struct {
	Schema []string
	Tables map[string][3]int64
}

func (p catalogPrint) difference(other catalogPrint) string {
	if strings.Join(p.Schema, "\n") != strings.Join(other.Schema, "\n") {
		return "the schema differs"
	}
	for name, value := range p.Tables {
		if got, ok := other.Tables[name]; !ok {
			return name + " is missing"
		} else if got[0] != value[0] {
			return fmt.Sprintf("%s has %d rows instead of %d", name, got[0], value[0])
		} else if got != value {
			return name + "'s rowids changed"
		}
	}
	if len(other.Tables) != len(p.Tables) {
		return "the tables differ"
	}
	return ""
}

// catalogFingerprint reads db's catalogPrint. Rowids are summed modulo a
// prime, since FTS5's segment rowids would overflow a plain sum.
func catalogFingerprint(ctx context.Context, db *sql.DB) (catalogPrint, error) {
	fingerprint := catalogPrint{Tables: map[string][3]int64{}}
	rows, err := db.QueryContext(ctx, "SELECT type||' '||name||' '||COALESCE(sql,'') FROM sqlite_schema ORDER BY type,name")
	if err != nil {
		return fingerprint, err
	}
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			rows.Close()
			return fingerprint, err
		}
		fingerprint.Schema = append(fingerprint.Schema, line)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return fingerprint, err
	}
	tables, err := queryMapsContext(ctx, db, "SELECT name,wr FROM pragma_table_list WHERE schema='main' AND type IN ('table','shadow') AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return fingerprint, err
	}
	for _, table := range tables {
		name := firstString(table["name"])
		assigned := integer(table["wr"]) == 0
		if assigned {
			alias, err := rowidAlias(ctx, db, name)
			if err != nil {
				return fingerprint, err
			}
			assigned = !alias
		}
		var value [3]int64
		statement := "SELECT COUNT(*),0,0 FROM " + sqlIdentifier(name)
		if assigned {
			statement = "SELECT COUNT(*),COALESCE(SUM(rowid%1000000007),0),COALESCE(MAX(rowid),0) FROM " + sqlIdentifier(name)
		}
		if err = db.QueryRowContext(ctx, statement).Scan(&value[0], &value[1], &value[2]); err != nil {
			return fingerprint, err
		}
		fingerprint.Tables[name] = value
	}
	return fingerprint, nil
}

// rowidAlias reports whether table's rowid is its INTEGER PRIMARY KEY, which
// VACUUM keeps.
func rowidAlias(ctx context.Context, db *sql.DB, table string) (bool, error) {
	columns, err := queryMapsContext(ctx, db, "SELECT type,pk FROM pragma_table_info(?) WHERE pk>0", table)
	if err != nil {
		return false, err
	}
	return len(columns) == 1 && strings.EqualFold(firstString(columns[0]["type"]), "INTEGER"), nil
}
