//go:build probe

package archive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs classification and its write plan without writing a catalog. Only an
// explicitly named backup is accepted, opened immutable and read-only; no
// Initialize, migration, checkpoint, or source-file backfill runs.
//
//	PHAROS_AUTHORSHIP_BACKUP='/path/to/backup/catalog.sqlite3' \
//	  go test -tags probe -run '^TestAuthorshipBackupProbe$' -v -timeout 30m ./internal/archive
func TestAuthorshipBackupProbe(t *testing.T) {
	path := os.Getenv("PHAROS_AUTHORSHIP_BACKUP")
	if path == "" {
		t.Skip("set PHAROS_AUTHORSHIP_BACKUP to an immutable backup")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(path), "backup") || strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe requires a backup, never the live catalog")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1&mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &Catalog{DB: db}
	want, built, err := c.authorshipGenerations()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stored_current=%v want=%s built=%s", want == built, want, built)
	started := time.Now()
	inputs, launches, err := c.authorshipInputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("inputs=%d launches=%d inputs_seconds=%.3f", len(inputs), len(launches), time.Since(started).Seconds())
	started = time.Now()
	var results []authorResult
	classifier := newClassifier(inputs)
	if os.Getenv("PHAROS_AUTHORSHIP_PROBE_REFERENCE") == "1" {
		results, err = classifyAuthorshipUnpaged(t.Context(), db, inputs, classifier)
	} else {
		results, err = classifyAuthorship(t.Context(), db, inputs, classifier)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("classify_seconds=%.3f candidates=%d lines=%d", time.Since(started).Seconds(), len(classifier.userText.seen), len(classifier.lines))
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for _, result := range results {
		if err := encoder.Encode(result); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("classification_sha256=%x", digest.Sum(nil))
	suffix := 0
	since := time.Now().Add(-time.Hour)
	for _, input := range inputs {
		if !input.SentAt.Before(since) {
			suffix++
		}
	}
	t.Logf("one_hour_watermark=%s suffix_messages=%d (backup time coverage only; no persisted incremental checkpoint)", formatTime(since), suffix)
	started = time.Now()
	plan, err := c.planAuthorship(t.Context(), results, launches)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, change := range plan {
		counts[change.table]++
	}
	t.Logf("plan_seconds=%.3f would_write=%v", time.Since(started).Seconds(), counts)
}

// An opt-in fixture measurement of the real persisted-state path. It reports
// work counts and timings separately from the immutable-backup measurement;
// fixture timings must not be extrapolated to production.
//
//	PHAROS_AUTHORSHIP_INCREMENTAL_PROBE=1 go test -tags probe \
//	  -run '^TestAuthorshipIncrementalWorkProbe$' -v ./internal/archive
func TestAuthorshipIncrementalWorkProbe(t *testing.T) {
	if os.Getenv("PHAROS_AUTHORSHIP_INCREMENTAL_PROBE") != "1" {
		t.Skip("set PHAROS_AUTHORSHIP_INCREMENTAL_PROBE=1")
	}
	for _, history := range []int{100, 1000} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			c, _ := testCatalog(t)
			tx := seedTx(t, c.DB)
			for _, sql := range []string{
				`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-09-01')`,
				`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('c','w','codex','local','c','codex')`,
			} {
				if _, err := tx.Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			text := "Please inspect the parser carefully and explain how the grammar handles all supported input documents without losing their original meaning"
			for i := range history {
				id := fmt.Sprint(i)
				if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES(?,'c',?,'user','message',?,?,?)`, id, id, fmt.Sprintf("Case %d. %s", i, text), formatTime(authorAt(i)), id); err != nil {
					t.Fatal(err)
				}
			}
			commitSeed(t, tx)
			want, _, _ := c.authorshipGenerations()
			before := time.Now()
			if err := c.RebuildAuthorship(t.Context(), want); err != nil {
				t.Fatal(err)
			}
			full := time.Since(before)
			if _, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('append','c','append','user','message',?,?,?)`, text, formatTime(authorAt(history+1)), "append"); err != nil {
				t.Fatal(err)
			}
			want, _, _ = c.authorshipGenerations()
			classified, mode := 0, ""
			ctx := withWorkProgress(t.Context(), func(work activityWork) {
				if work.Phase == "classifying authorship" {
					classified = int(work.Total)
				}
				if strings.HasPrefix(work.Phase, "authorship mode:") {
					mode = work.Phase
				}
			})
			before = time.Now()
			if err := c.RebuildAuthorship(ctx, want); err != nil {
				t.Fatal(err)
			}
			incremental := time.Since(before)
			if classified != 1 || mode != "authorship mode: suffix" {
				t.Fatalf("history=%d classified=%d mode=%s", history, classified, mode)
			}
			t.Logf("fixture history=%d classified=%d full_seconds=%.3f incremental_seconds=%.3f", history, classified, full.Seconds(), incremental.Seconds())
		})
	}
}
