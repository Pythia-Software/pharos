package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testDurability skips the drive flushes that durableCatalog makes every
// commit wait for. A test process that dies still leaves its commits in the
// OS cache, so only a power loss could tell the difference. The tests of
// durability itself call useDurableCatalog.
var testDurability = catalogPragmas{synchronous: "OFF"}

// TestMain keeps every test, and every process a test starts, out of this
// Mac's real support directory: detecting the host writes host.json there.
// Helper processes share their parent's. It also keeps tests off this Mac's
// tools: no test may reach GitHub, ask ioreg or scutil who the Mac is, run
// diskutil, mdutil or hdiutil, or read the user's git configuration. Tests of
// those lookups opt back in.
func TestMain(m *testing.M) {
	support := os.Getenv("PHAROS_TEST_SUPPORT_DIR")
	owned := support == ""
	if owned {
		dir, err := os.MkdirTemp("", "pharos-test-support-")
		if err != nil {
			panic(err)
		}
		support = dir
		os.Setenv("PHAROS_TEST_SUPPORT_DIR", support)
		// Helper processes inherit these, and whatever a test sets for them.
		os.Setenv("PHAROS_HOST_ID", "host_pharos_test")
		os.Setenv("PHAROS_HOST_LABEL", "Pharos Test Mac")
		os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	}
	os.Setenv("PHAROS_SUPPORT_DIR", support)
	// Tests of the lookup point githubCLI at a stub.
	githubCLI, githubPause = "pharos-test-no-gh", time.Millisecond
	// Tests of the drive checks stub the tools (see stubDriveTool).
	driveTool = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("%s does not run in tests", name)
	}
	catalogDurability = testDurability
	// New libraries and audit references start as copies of the template.
	newCatalog = func(path string) (*Catalog, error) {
		if _, err := os.Lstat(path); err == nil {
			return OpenCatalog(path)
		}
		return openTemplateCatalog(path)
	}
	code := m.Run()
	if owned {
		os.RemoveAll(support)
	}
	os.Exit(code)
}

// slowTest skips one of the slowest tests in go test -short, the fast tier.
// The full run, go test ./..., keeps it.
func slowTest(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow: runs without -short")
	}
}

// useDurableCatalog gives the catalogs t opens the service's durability.
func useDurableCatalog(t testing.TB) {
	t.Helper()
	override(t, &catalogDurability, durableCatalog)
}

// override sets a package variable, such as a limit a test lowers so that a
// few rows exceed it, until t ends.
func override[T any](t testing.TB, variable *T, value T) {
	t.Helper()
	previous := *variable
	*variable = value
	t.Cleanup(func() { *variable = previous })
}

// seedTx begins a transaction for seeding a fixture, so that its rows cost one
// commit rather than one each; commitSeed commits it. A test that fails
// before then rolls it back.
func seedTx(t testing.TB, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func commitSeed(t testing.TB, tx *sql.Tx) {
	t.Helper()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// execTx runs statements in one transaction.
func execTx(t testing.TB, db *sql.DB, statements ...string) {
	t.Helper()
	tx := seedTx(t, db)
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	commitSeed(t, tx)
}

// testConfig is a configuration whose every path is under a new temporary
// directory. The catalog path is not created.
func testConfig(t testing.TB) Config {
	t.Helper()
	root := t.TempDir()
	config := defaultConfig(filepath.Join(root, "archive.toml"))
	config.CatalogPath = filepath.Join(root, "catalog.sqlite3")
	config.ArchiveRoot = filepath.Join(root, "archive")
	config.StagingRoot = filepath.Join(root, "staging")
	// The default is this Mac's own captures, which tests must neither read
	// nor write.
	config.CaptureRoot = filepath.Join(root, "captures")
	config.APIToken = "test-token"
	if err := os.MkdirAll(config.ArchiveRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return config
}

// testCatalog is an empty, initialized catalog in a new directory. It is a
// copy of one initialized once per process (see templateCatalog), so a test
// does not pay for Initialize. Tests whose subject is Initialize or a
// migration use freshCatalog.
func testCatalog(t testing.TB) (*Catalog, Config) {
	t.Helper()
	config := testConfig(t)
	return openTestCatalog(t, config.CatalogPath), config
}

// freshCatalog is testCatalog through OpenCatalog, for tests of what
// Initialize does on a new catalog.
func freshCatalog(t testing.TB) (*Catalog, Config) {
	t.Helper()
	config := testConfig(t)
	catalog, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	closeAtEnd(t, catalog)
	return catalog, config
}

// openTestCatalog writes the template catalog to path and opens it, closing it
// when t ends.
func openTestCatalog(t testing.TB, path string) *Catalog {
	t.Helper()
	catalog, err := openTemplateCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	closeAtEnd(t, catalog)
	return catalog
}

// closeAtEnd closes catalog when t ends, once the work it started in the
// background without a server, such as an authorship rebuild a page asked
// for, has finished: otherwise that work could run into a later test, and
// reopen files in t's directory while it is being removed. A server's work
// is waited for by testServer.
func closeAtEnd(t testing.TB, catalog *Catalog) {
	t.Helper()
	t.Cleanup(func() {
		if !waitFor(time.Minute, catalog.unowned.Wait) {
			t.Error("the catalog's background work did not finish within a minute of the test ending")
		}
		catalog.Close()
	})
}

// reopenCatalog is OpenCatalog for an existing catalog, such as a new
// library's, in a test that is not about Initialize. When this build has
// initialized the catalog, only the host step of Initialize runs, as in
// openTemplateCatalog; the rest would change nothing. The caller closes it.
func reopenCatalog(t testing.TB, path string) *Catalog {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	catalog, err := openCatalog(path, func(c *Catalog) error {
		if current, err := c.initializedByThisBuild(); err != nil || !current {
			return c.initializeLocked(true)
		}
		return c.migrateHosts()
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

var catalogTemplate struct {
	once  sync.Once
	bytes []byte
	err   error
}

// templateCatalog initializes a catalog once per process and returns its
// file. It is built under a fallback host, which Initialize records nowhere,
// so it belongs to no host: openTemplateCatalog registers the test's.
func templateCatalog() ([]byte, error) {
	catalogTemplate.once.Do(func() {
		catalogTemplate.bytes, catalogTemplate.err = buildTemplateCatalog()
	})
	return catalogTemplate.bytes, catalogTemplate.err
}

func buildTemplateCatalog() ([]byte, error) {
	dir, err := os.MkdirTemp("", "pharos-catalog-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "catalog.sqlite3")
	previous := currentHost
	currentHost = func() Host { return Host{ID: "host_template", Label: "Template", Fallback: true} }
	catalog, err := OpenCatalog(path)
	currentHost = previous
	if err != nil {
		return nil, err
	}
	if err := catalog.Close(); err != nil {
		return nil, err
	}
	// Close checkpointed everything into the file.
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 0 {
		return nil, fmt.Errorf("the template catalog left %d bytes in its WAL", info.Size())
	}
	return os.ReadFile(path)
}

// openTemplateCatalog opens a copy of the template at path the way
// OpenCatalog opens a catalog this build has initialized, except that only
// the host step of Initialize runs: it is the one part that depends on the
// test (see useHost).
func openTemplateCatalog(path string) (*Catalog, error) {
	template, err := templateCatalog()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, template, 0o644); err != nil {
		return nil, err
	}
	return openCatalog(path, func(c *Catalog) error {
		if err := c.migrateHosts(); err != nil {
			return err
		}
		// A new catalog's revision is random; keep copies of the template apart.
		_, err := c.DB.Exec("UPDATE meta SET value=hex(randomblob(16)) WHERE key='tool_rollup_full_revision'")
		return err
	})
}

// testServer is NewServer for a test. When the test ends it stops the server's
// background work (rebuilds, syncs, drive checks) and waits for it, so none of
// it runs into a later test. The catalog stays open for its owner to close.
func testServer(t testing.TB, config Config, catalog *Catalog) *Server {
	t.Helper()
	server := NewServer(config, catalog)
	t.Cleanup(func() { settleServer(t, server) })
	return server
}

// settleServer cancels server's background work and waits for it and for
// requests in flight.
func settleServer(t testing.TB, server *Server) {
	t.Helper()
	server.beginStop()
	if !waitFor(time.Minute, func() { server.life.requests.Wait(); server.life.workers.Wait() }) {
		t.Error("the server's background work did not stop within a minute of the test ending")
	}
}

// waitFor runs wait and reports whether it returned within timeout.
func waitFor(timeout time.Duration, wait func()) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
