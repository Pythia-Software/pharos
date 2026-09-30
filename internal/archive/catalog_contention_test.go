package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIngestRetriesCatalogWriterContention(t *testing.T) {
	catalog, _ := testCatalog(t)
	// Give ingestion one connection with a short timeout so this test can
	// exercise retries without waiting for the production five seconds.
	catalog.DB.SetMaxOpenConns(1)
	catalog.DB.SetMaxIdleConns(1)
	if _, err := catalog.DB.Exec("PRAGMA busy_timeout=20"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(path, []byte(`{"workspaces":[{"id":"work","title":"Test","conversations":[{"id":"thread","provider":"codex","messages":[{"id":"one","role":"user","text":"hello"}]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(SourceConfig{Name: "fixture", Kind: "canonical", Path: path, Account: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+catalog.Path+"?_pragma=busy_timeout(20)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var held *sql.Tx
	waiting := false
	released := make(chan error, 1)
	result := catalog.IngestContext(context.Background(), adapter, func(phase string, _, _, _, _, _ int) {
		if phase == "waiting for catalog" {
			waiting = true
		}
		if phase != "indexing" || held != nil {
			return
		}
		var err error
		held, err = other.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := held.Exec("INSERT INTO meta(key,value) VALUES('contention-test','1')"); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			released <- held.Commit()
		}()
	})
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if result.Error != nil || result.Workspaces != 1 || !waiting {
		t.Fatalf("ingest after transient writer conflict: %+v, waiting=%t", result, waiting)
	}
	var count int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM workspaces WHERE source_kind='canonical'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("workspaces=%d, error=%v", count, err)
	}
}

func TestCatalogWriterSummaryNamesLocalWriter(t *testing.T) {
	catalog, _ := testCatalog(t)
	tx, finish, err := catalog.beginTrackedWrite(context.Background(), "test-writer")
	if err != nil {
		t.Fatal(err)
	}
	if summary := catalog.writerSummary(); !strings.Contains(summary, "test-writer") {
		t.Fatalf("missing active writer: %s", summary)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	finish()
	if summary := catalog.writerSummary(); !strings.Contains(summary, "active_writers=[none tracked") || !strings.Contains(summary, "another Mac") {
		t.Fatalf("stale or misleading writer summary: %s", summary)
	}
}
