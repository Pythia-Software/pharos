package archive

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// compactFixture is a closed library catalog with messages whose rowids have
// gaps (which a renumbering VACUUM would close) and free pages left by a
// dropped table.
func compactFixture(t *testing.T) Config {
	t.Helper()
	catalog, config := testCatalog(t)
	fillCompactFixture(t, catalog.DB)
	catalog.Close()
	return config
}

func fillCompactFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	execTx(t, db,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w1','claude','local','w1','One','x')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin) VALUES('c1','w1','claude','local','c1','/a/c1.jsonl')`,
		`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<300)
		 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) SELECT 'm'||i,'c1','n'||i,'user','message','words '||hex(randomblob(200)),'h'||i FROM n`,
		`DELETE FROM messages WHERE rowid%3=0`,
		`CREATE TABLE filler(b)`, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<200) INSERT INTO filler SELECT randomblob(4000) FROM n`, `DROP TABLE filler`)
}

func compactPragma(t *testing.T, path, pragma string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value int64
	if err = db.QueryRow("PRAGMA " + pragma).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func messageRowids(t *testing.T, path string) map[int64]string {
	t.Helper()
	db, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT rowid,id FROM messages")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[int64]string{}
	for rows.Next() {
		var rowid int64
		var id string
		if err = rows.Scan(&rowid, &id); err != nil {
			t.Fatal(err)
		}
		found[rowid] = id
	}
	return found
}

func TestCompactLibraryGivesFreeSpaceBackAndKeepsEveryRow(t *testing.T) {
	config := compactFixture(t)
	path := config.CatalogPath
	rowids := messageRowids(t, path)
	free := compactPragma(t, path, "freelist_count")
	if free < 200 || len(rowids) != 200 {
		t.Fatalf("fixture: %d free pages, %d messages", free, len(rowids))
	}
	phases := []string{}
	receipt, err := CompactLibrary(t.Context(), config, func(phase string, done, total int64) {
		if len(phases) == 0 || phases[len(phases)-1] != phase {
			phases = append(phases, phase)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.FreeBytes < free*4096 || receipt.AfterBytes >= receipt.BeforeBytes {
		t.Fatalf("receipt %+v", receipt)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != receipt.AfterBytes {
		t.Fatal("catalog size", info, err)
	}
	for _, leftover := range []string{compactingSuffix, preCompactionSuffix} {
		if _, err := os.Stat(path + leftover); !os.IsNotExist(err) {
			t.Fatalf("left %s behind: %v", leftover, err)
		}
	}
	if !strings.Contains(strings.Join(phases, "|"), "Checking the compacted catalog") {
		t.Fatalf("phases %v", phases)
	}
	if compactPragma(t, path, "freelist_count") != 0 {
		t.Fatal("free pages remain")
	}
	// Rowids are kept, so the indexes keyed by them still find their rows.
	after := messageRowids(t, path)
	if len(after) != len(rowids) {
		t.Fatalf("%d messages, want %d", len(after), len(rowids))
	}
	for rowid, id := range rowids {
		if after[rowid] != id {
			t.Fatalf("rowid %d is %q, was %q", rowid, after[rowid], id)
		}
	}
	catalog := reopenCatalog(t, path)
	defer catalog.Close()
	var mode string
	var orphans int
	if err = catalog.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatal("journal mode", mode, err)
	}
	if err = catalog.DB.QueryRow(`SELECT COUNT(*) FROM text_search_documents d LEFT JOIN messages m ON m.rowid=d.message_rowid AND m.id=d.message_id
		WHERE m.id IS NULL`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatal("search documents lost their messages", orphans, err)
	}

	// A compact catalog is left as it is.
	again, err := CompactLibrary(t.Context(), config, nil)
	if err != nil || again.FreeBytes != 0 || again.AfterBytes != receipt.AfterBytes {
		t.Fatal("second compaction", again, err)
	}
}

func TestCompactLibraryLeavesTheCatalogWhenItCannotFinish(t *testing.T) {
	unchanged := func(t *testing.T, config Config, free int64) {
		t.Helper()
		if got := compactPragma(t, config.CatalogPath, "freelist_count"); got != free {
			t.Fatalf("%d free pages, want %d", got, free)
		}
		for _, leftover := range []string{compactingSuffix, compactingSuffix + "-journal"} {
			if _, err := os.Stat(config.CatalogPath + leftover); !os.IsNotExist(err) {
				t.Fatal("left the unfinished copy", leftover, err)
			}
		}
	}
	t.Run("another process has the library open", func(t *testing.T) {
		config := compactFixture(t)
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		override(t, &compactLeaseWait, 300*time.Millisecond)
		lease, err := fileLease(libraryUseLock(config), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = CompactLibrary(t.Context(), config, nil); err == nil || !strings.Contains(err.Error(), "another Pharos process") {
			t.Fatal(err)
		}
		unchanged(t, config, free)
		// A lease that ends while compacting waits, such as one MCP call's,
		// does not stop it.
		time.AfterFunc(100*time.Millisecond, func() { lease.Close() })
		if receipt, err := CompactLibrary(t.Context(), config, nil); err != nil || receipt.FreeBytes == 0 {
			t.Fatal("compaction after the lease ended", receipt, err)
		}
	})
	t.Run("no room for the copy", func(t *testing.T) {
		config := compactFixture(t)
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		override(t, &compactDiskFree, func(string) uint64 { return 0 })
		if _, err := CompactLibrary(t.Context(), config, nil); err == nil || !strings.Contains(err.Error(), "free on the library's drive") {
			t.Fatal(err)
		}
		unchanged(t, config, free)
	})
	t.Run("the copy does not match", func(t *testing.T) {
		config := compactFixture(t)
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		reads := 0
		override(t, &compactFingerprint, func(ctx context.Context, db *sql.DB) (catalogPrint, error) {
			fingerprint, err := catalogFingerprint(ctx, db)
			if reads++; reads == 2 {
				value := fingerprint.Tables["messages"]
				value[1]++
				fingerprint.Tables["messages"] = value
			}
			return fingerprint, err
		})
		if _, err := CompactLibrary(t.Context(), config, nil); err == nil || !strings.Contains(err.Error(), "messages's rowids changed") {
			t.Fatal(err)
		}
		unchanged(t, config, free)
	})
	t.Run("stopped before the copy", func(t *testing.T) {
		config := compactFixture(t)
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		ctx, cancel := context.WithCancel(t.Context())
		_, err := CompactLibrary(ctx, config, func(phase string, done, total int64) {
			if phase == "Writing the compacted catalog" {
				cancel()
			}
		})
		if err == nil {
			t.Fatal("a stopped compaction succeeded")
		}
		unchanged(t, config, free)
	})
	t.Run("stopped while writing the copy", func(t *testing.T) {
		// Enough kept data that the copy is still being written when its
		// size is first polled: the first report is the step's start, the
		// next the poll's.
		catalog, config := testCatalog(t)
		execTx(t, catalog.DB, `CREATE TABLE kept(b)`, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<15000) INSERT INTO kept SELECT randomblob(4000) FROM n`)
		fillCompactFixture(t, catalog.DB)
		catalog.Close()
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		override(t, &compactProgressEvery, 100*time.Microsecond)
		ctx, cancel := context.WithCancel(t.Context())
		reports, interrupted := 0, false
		_, err := CompactLibrary(ctx, config, func(phase string, done, total int64) {
			if phase == "Writing the compacted catalog" {
				if reports++; reports == 2 {
					interrupted = true
					cancel()
				}
			}
		})
		if !interrupted {
			t.Fatal("the copy finished before its size was polled; enlarge the fixture")
		}
		if err == nil {
			t.Fatal("a stopped compaction succeeded")
		}
		unchanged(t, config, free)
	})
	t.Run("stopped while checking the copy", func(t *testing.T) {
		config := compactFixture(t)
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		ctx, cancel := context.WithCancel(t.Context())
		reads := 0
		override(t, &compactFingerprint, func(ctx context.Context, db *sql.DB) (catalogPrint, error) {
			if reads++; reads == 2 {
				cancel()
			}
			return catalogFingerprint(ctx, db)
		})
		if _, err := CompactLibrary(ctx, config, nil); err == nil {
			t.Fatal("a stopped compaction succeeded")
		}
		unchanged(t, config, free)
	})
	t.Run("another process wrote during compaction", func(t *testing.T) {
		config := compactFixture(t)
		free := compactPragma(t, config.CatalogPath, "freelist_count")
		reads := 0
		override(t, &compactFingerprint, func(ctx context.Context, db *sql.DB) (catalogPrint, error) {
			if reads++; reads == 2 {
				if err := os.WriteFile(config.CatalogPath+"-wal", []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			return catalogFingerprint(ctx, db)
		})
		if _, err := CompactLibrary(t.Context(), config, nil); err == nil || !strings.Contains(err.Error(), "another process wrote") {
			t.Fatal(err)
		}
		if err := os.Remove(config.CatalogPath + "-wal"); err != nil {
			t.Fatal("the other process's log was removed", err)
		}
		unchanged(t, config, free)
	})
	t.Run("the new catalog does not open", func(t *testing.T) {
		config := compactFixture(t)
		free, rowids := compactPragma(t, config.CatalogPath, "freelist_count"), messageRowids(t, config.CatalogPath)
		override(t, &compactOpen, func(path string) error {
			if err := openCompacted(path); err != nil {
				t.Fatal(err)
			}
			return errors.New("refused")
		})
		if _, err := CompactLibrary(t.Context(), config, nil); err == nil || !strings.Contains(err.Error(), "original was put back") {
			t.Fatal(err)
		}
		for _, suffix := range []string{"-wal", "-shm", preCompactionSuffix} {
			if _, err := os.Stat(config.CatalogPath + suffix); !os.IsNotExist(err) {
				t.Fatalf("left %s beside the original: %v", suffix, err)
			}
		}
		unchanged(t, config, free)
		if after := messageRowids(t, config.CatalogPath); len(after) != len(rowids) {
			t.Fatalf("%d messages after putting the original back, want %d", len(after), len(rowids))
		}
	})
	t.Run("the drive has no hard links", func(t *testing.T) {
		config := compactFixture(t)
		override(t, &compactLink, func(string, string) error { return errors.ErrUnsupported })
		if receipt, err := CompactLibrary(t.Context(), config, nil); err != nil || receipt.FreeBytes == 0 {
			t.Fatal(receipt, err)
		}
		if compactPragma(t, config.CatalogPath, "freelist_count") != 0 {
			t.Fatal("free pages remain")
		}
	})
}

// What a compaction killed part way leaves goes when the library's service
// next starts (Serve), but never the only copy of the catalog.
func TestRemoveCompactionLeftovers(t *testing.T) {
	config := compactFixture(t)
	path := config.CatalogPath
	for name, body := range map[string]string{path + compactingSuffix: "copy", path + compactingSuffix + "-journal": "journal"} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(path, path+preCompactionSuffix); err != nil {
		t.Fatal(err)
	}
	rowids := messageRowids(t, path)
	removeCompactionLeftovers(path)
	if after := messageRowids(t, path); len(after) != len(rowids) {
		t.Fatal("the catalog changed")
	}
	for _, suffix := range []string{compactingSuffix, compactingSuffix + "-journal", preCompactionSuffix} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", suffix, err)
		}
	}
	// A failed rollback leaves a catalog that does not open, or none, and the
	// original only at the link.
	for _, broken := range []func() error{
		func() error { return os.WriteFile(path, []byte("not a catalog"), 0o644) },
		func() error { return os.Remove(path) },
	} {
		if err := os.WriteFile(path+preCompactionSuffix, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := broken(); err != nil {
			t.Fatal(err)
		}
		removeCompactionLeftovers(path)
		if body, err := os.ReadFile(path + preCompactionSuffix); err != nil || string(body) != "original" {
			t.Fatal("removed the only copy", err)
		}
	}
}

func TestApplicationCompactsALibraryWithItsServicesStopped(t *testing.T) {
	slowTest(t)
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	a, _ := lifecycleLibraries(t)
	catalog := reopenCatalog(t, a.CatalogPath)
	fillCompactFixture(t, catalog.DB)
	catalog.Close()
	app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := app.registry.register(a, "")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.startCompaction(w, map[string]any{"id": "unknown"})
	if w.Code != http.StatusBadRequest || app.busy {
		t.Fatal("compacted an unknown library", w.Code, w.Body.String())
	}
	child := lifecycleChild(t)
	app.children[id] = child
	w = httptest.NewRecorder()
	app.startCompaction(w, map[string]any{"id": id})
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-child.done:
	default:
		t.Fatal("the library's service kept running")
	}
	finished := waitFor(time.Minute, func() {
		app.operationMu.Lock()
		app.operationMu.Unlock()
	})
	app.jobMu.Lock()
	job := app.job
	app.jobMu.Unlock()
	if !finished || job["state"] != "complete" || !strings.HasPrefix(firstString(job["phase"]), "Compacted the catalog from ") {
		t.Fatal("compaction job", finished, job)
	}
	app.mu.Lock()
	busy := app.busy
	app.mu.Unlock()
	if busy || compactPragma(t, a.CatalogPath, "freelist_count") != 0 {
		t.Fatal("compaction did not finish", busy)
	}

	// A service that ignores its stop is ended, and the compaction goes on.
	override(t, &compactCloseWait, 100*time.Millisecond)
	stubborn := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	if err := stubborn.Start(); err != nil {
		t.Fatal(err)
	}
	lingering := &applicationChild{cmd: stubborn, done: make(chan struct{})}
	go func() { _ = stubborn.Wait(); close(lingering.done) }()
	t.Cleanup(func() { _ = stubborn.Process.Kill(); <-lingering.done })
	app.children[id] = lingering
	w = httptest.NewRecorder()
	app.startCompaction(w, map[string]any{"id": id})
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	if !waitFor(time.Minute, func() { app.operationMu.Lock(); app.operationMu.Unlock() }) {
		t.Fatal("the compaction did not end")
	}
	app.jobMu.Lock()
	job = app.job
	app.jobMu.Unlock()
	select {
	case <-lingering.done:
	default:
		t.Fatal("the service that ignored its stop is still running")
	}
	if job["state"] != "complete" {
		t.Fatal("job", job)
	}
	app.mu.Lock()
	busy = app.busy
	app.mu.Unlock()
	if busy {
		t.Fatal("the broker stayed busy")
	}
}
