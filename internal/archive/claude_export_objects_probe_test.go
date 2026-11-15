//go:build probe

// Read-only real-export probe. All collection/index/capture writes are under
// t.TempDir, never beside the supplied inputs or in a user's library.
//
// PHAROS_CLAUDE_EXPORT_FULL=/path/all-time.zip \
// PHAROS_CLAUDE_EXPORT_WINDOW=/path/30-day.zip \
// go test -tags probe -run '^TestClaudeSourceUnitsExportsProbe$' -v ./internal/archive
package archive

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeSourceUnitsExportsProbe(t *testing.T) {
	full, window := os.Getenv("PHAROS_CLAUDE_EXPORT_FULL"), os.Getenv("PHAROS_CLAUDE_EXPORT_WINDOW")
	if full == "" || window == "" {
		t.Skip("set explicit full/window export paths")
	}
	config := claudeImportConfig(t)
	catalog, _ := testCatalog(t)
	collect := func(from string, day int) *claudeImportJob {
		t.Helper()
		directory := t.TempDir()
		destination := filepath.Join(directory, "conversations-000.zip")
		input, err := os.Open(from)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.Create(destination)
		if err != nil {
			input.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(output, input)
		input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal(copyErr, closeErr)
		}
		stamp := time.Date(2026, 11, day, 0, 1, 0, 0, time.UTC)
		if err := os.Chtimes(destination, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(directory, "manifest.json")
		probePut(t, manifest, jsonText(claudeManifest{Version: "1.0", CreatedAt: time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), DataFiles: []claudeManifestPart{{Filename: "conversations-000.zip", Category: "conversations"}}}))
		job, err := collectClaudeExport(t.Context(), config, claudeImportRequest{Paths: []string{manifest}})
		if err != nil {
			t.Fatal("source collection failed", err)
		}
		if job.State != "collected" || job.Parts[0].Units == "" || job.Parts[0].UnitsError != "" {
			t.Fatal("source collection failed", job.State, job.Parts[0].Error, job.Parts[0].UnitsError)
		}
		config, err = LoadConfig(config.Path)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	reads := 0
	override(t, &claudeObjectReadHook, func(string) { reads++ })
	first := collect(full, 10)
	initial := ingestSource(t, catalog, config.Sources[0])
	initialReads := reads
	second := collect(window, 11)
	reads = 0
	changed := ingestSource(t, catalog, config.Sources[0])
	changedReads := reads
	collect(window, 12)
	reads = 0
	repeated := ingestSource(t, catalog, config.Sources[0])
	repeatReads := reads
	if repeated.Parsed != 0 || repeatReads != 0 {
		t.Fatal("unchanged export reread source payload", repeated, repeatReads)
	}
	var packed int64
	entries, err := os.ReadDir(filepath.Join(claudeImportRoot(config, first.Name), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		packed += info.Size()
	}
	// Destructive work is confined to the disposable library. Verify and apply
	// the same explicit plan a user can review from the CLI.
	plan, err := planClaudeCompaction(t.Context(), config, first.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Items {
		if item.Blocked != "" {
			t.Fatal("scratch replacement is ineligible", item.Blocked)
		}
	}
	run, err := applyClaudeCompaction(t.Context(), config, first.Name, plan.ID)
	if err != nil || run.State != "complete" {
		t.Fatal(run, err)
	}
	var metadata int64
	if err := filepath.WalkDir(claudeImportRoot(config, first.Name), func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(name) == ".pack" {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			metadata += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatal("scratch capture failed")
	}
	wantEvidence := catalogEvidence(t, catalog)
	wantConversations := countRows(t, catalog, "SELECT COUNT(*) FROM conversations")
	wantMessages := countRows(t, catalog, "SELECT COUNT(*) FROM messages")
	if err := os.RemoveAll(claudeImportRoot(config, first.Name)); err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := testCatalog(t)
	indexCaptures(t, rebuilt, config.CaptureRoot, "host-a")
	if countRows(t, rebuilt, "SELECT COUNT(*) FROM conversations") != wantConversations || countRows(t, rebuilt, "SELECT COUNT(*) FROM messages") != wantMessages {
		t.Fatal("offline replay differs")
	}
	if catalogEvidence(t, rebuilt) != wantEvidence {
		t.Fatal("offline source identities/content differ")
	}
	t.Logf("conversations=%d indexed_messages=%d first_parsed=%d first_object_reads=%d window_parsed=%d window_object_reads=%d repeat_parsed=%d repeat_object_reads=%d unique_pack_bytes=%d initial_zip_bytes=%d window_zip_bytes=%d metadata_bytes=%d retired_logical_bytes=%d", wantConversations, wantMessages, initial.Parsed, initialReads, changed.Parsed, changedReads, repeated.Parsed, repeatReads, packed, first.Parts[0].Size, second.Parts[0].Size, metadata, run.RetiredBytes)
}
