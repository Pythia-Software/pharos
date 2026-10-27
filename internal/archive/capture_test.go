package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func captureTestConfig(t *testing.T, sources ...SourceConfig) Config {
	t.Helper()
	root := t.TempDir()
	config := defaultConfig(filepath.Join(root, "archive.toml"))
	config.CatalogPath = filepath.Join(root, "catalog.sqlite3")
	config.ArchiveRoot = filepath.Join(root, "preserved")
	config.StagingRoot = filepath.Join(root, "staging")
	config.CaptureRoot = filepath.Join(root, "captures")
	config.Sources = sources
	return config
}

var captureClock = time.Date(2026, 9, 1, 12, 0, 0, 123456789, time.UTC)

// writeSourceFile writes content with a distinct mtime, as a tool would.
func writeSourceFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.Stat(file)
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	captureClock = captureClock.Add(time.Minute)
	stamp := captureClock
	if previous != nil && !stamp.After(previous.ModTime()) {
		stamp = previous.ModTime().Add(time.Nanosecond)
	}
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func runCapture(t *testing.T, config Config) CaptureSummary {
	t.Helper()
	summary, err := Capture(context.Background(), config, config.Sources, nil)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func captureResult(t *testing.T, summary CaptureSummary, name string) CaptureSourceResult {
	t.Helper()
	for _, result := range summary.Sources {
		if result.Name == name {
			return result
		}
	}
	t.Fatalf("no result for %s in %#v", name, summary)
	return CaptureSourceResult{}
}

func readCaptureManifest(t *testing.T, config Config, source string) captureManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(config.CaptureRoot, currentHost().ID, source, captureManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest captureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	return manifest
}

func manifestFile(t *testing.T, manifest captureManifest, captured string) *capturedFile {
	t.Helper()
	for _, file := range manifest.Files {
		if file.Captured == captured {
			return file
		}
	}
	t.Fatalf("manifest lacks %s", captured)
	return nil
}

func capturedPath(config Config, source, rel string) string {
	return filepath.Join(config.CaptureRoot, currentHost().ID, source, filepath.FromSlash(rel))
}

func readText(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCaptureCopiesWhatAdaptersReadIncrementally(t *testing.T) {
	useHost(t, "host-a")
	sources := t.TempDir()
	claude, codex := filepath.Join(sources, "claude"), filepath.Join(sources, "codex")
	writeSourceFile(t, filepath.Join(claude, "-proj", "s1.jsonl"), `{"type":"user"}`+"\n")
	writeSourceFile(t, filepath.Join(claude, "-proj", "s1", "subagents", "a1.jsonl"), `{"type":"assistant"}`+"\n")
	writeSourceFile(t, filepath.Join(claude, "-proj", "notes.txt"), "not a transcript")
	writeSourceFile(t, filepath.Join(codex, "sessions", "2026", "09", "r1.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(codex, "archived_sessions", "r2.jsonl"), "{}\n{}\n")
	writeSourceFile(t, filepath.Join(codex, "imports", "rollout-3.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(codex, "log", "other.jsonl"), "{}\n")
	config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: claude, Enabled: true}, SourceConfig{Name: "codex", Kind: "codex", Path: codex, Enabled: true})

	first := runCapture(t, config)
	if !first.OK || captureResult(t, first, "claude").FilesCopied != 2 || captureResult(t, first, "codex").FilesCopied != 3 {
		t.Fatalf("first capture: %#v", first)
	}
	for _, rel := range []string{"files/-proj/s1.jsonl", "files/-proj/s1/subagents/a1.jsonl"} {
		source := filepath.Join(claude, strings.TrimPrefix(filepath.FromSlash(rel), "files/"))
		captured := capturedPath(config, "claude", rel)
		if readText(t, captured) != readText(t, source) {
			t.Fatalf("%s differs from its source", rel)
		}
		sourceInfo, _ := os.Stat(source)
		capturedInfo, _ := os.Stat(captured)
		if !capturedInfo.ModTime().Equal(sourceInfo.ModTime()) {
			t.Fatalf("%s mtime %v, source %v", rel, capturedInfo.ModTime(), sourceInfo.ModTime())
		}
		sum := sha256.Sum256([]byte(readText(t, source)))
		if entry := manifestFile(t, readCaptureManifest(t, config, "claude"), rel); entry.SHA256 != hex.EncodeToString(sum[:]) || entry.Path != source {
			t.Fatalf("manifest entry %#v", entry)
		}
	}
	for _, ignored := range []string{"claude/files/-proj/notes.txt", "codex/files/log/other.jsonl"} {
		if _, err := os.Stat(filepath.Join(config.CaptureRoot, "host-a", filepath.FromSlash(ignored))); err == nil {
			t.Fatalf("%s is not read by its adapter and must not be captured", ignored)
		}
	}
	var host captureHostRecord
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(config.CaptureRoot, "host-a", captureHostName))), &host); err != nil || host.ID != "host-a" || host.FirstCaptureAt == "" || host.LastCaptureAt == "" {
		t.Fatalf("host.json: %#v %v", host, err)
	}
	firstManifest := readCaptureManifest(t, config, "codex")
	before := manifestFile(t, firstManifest, "files/archived_sessions/r2.jsonl").CapturedAt
	lastDataAt := firstManifest.LastDataAt
	if lastDataAt == "" {
		t.Fatal("first capture did not record when data arrived")
	}

	// Ordinary incremental capture must remain a metadata-only operation for
	// unchanged transcripts, while still retaining proof for a later manual move.
	ordinaryReads := 0
	override(t, &captureFault, func(stage, rel string) error {
		if stage == "delivery-read" {
			ordinaryReads++
			return errors.New("ordinary capture attempted a delivery transcript read")
		}
		return nil
	})
	second := runCapture(t, config)
	if codexResult := captureResult(t, second, "codex"); !second.OK || codexResult.FilesCopied != 0 || codexResult.FilesUnchanged != 3 || captureResult(t, second, "claude").FilesUnchanged != 2 {
		t.Fatalf("no-op capture copied: %#v", second)
	}
	manifest := readCaptureManifest(t, config, "codex")
	if manifestFile(t, manifest, "files/archived_sessions/r2.jsonl").CapturedAt != before || manifest.LastDataAt != lastDataAt || manifest.LastRun == nil || manifest.LastRun.FinishedAt == "" {
		t.Fatalf("no-op capture changed the manifest: %#v", manifest)
	}

	// A capture damaged or lost on the drive is copied again.
	if err := os.Remove(capturedPath(config, "codex", "files/sessions/2026/09/r1.jsonl")); err != nil {
		t.Fatal(err)
	}
	if third := runCapture(t, config); captureResult(t, third, "codex").FilesCopied != 1 {
		t.Fatalf("lost capture was not restored: %#v", third)
	}

	// Moving a configured source and editing its account must not strand its
	// capture. Even identical file metadata must acquire the new original path.
	moved := filepath.Join(t.TempDir(), "moved-claude")
	if err := os.Rename(claude, moved); err != nil {
		t.Fatal(err)
	}
	config.Sources[0].Path = moved
	for _, account := range []string{"", "changed-account"} {
		config.Sources[0].Account = account
		if run := runCapture(t, config); !run.OK {
			t.Fatalf("capture after source configuration edit: %+v", run)
		}
		manifest := readCaptureManifest(t, config, "claude")
		file := manifestFile(t, manifest, "files/-proj/s1.jsonl")
		if manifest.Source.Path != moved || manifest.Source.Account != account || file.Path != filepath.Join(moved, "-proj", "s1.jsonl") {
			t.Fatalf("capture retained stale source configuration: %+v %+v", manifest.Source, file)
		}
	}

	if ordinaryReads != 0 {
		t.Fatal("ordinary incremental capture read transcript bytes for delivery proof", ordinaryReads)
	}

	// A source with delivered evidence must still read its current bytes before
	// suppressing recapture, even when automatic delivery is no longer enabled.
	manifest = readCaptureManifest(t, config, "claude")
	if manifest.DeliveredFingerprint == "" {
		t.Fatal("ordinary capture lost the proof needed by a future manual move")
	}
	if err := durableJSON(capturedPath(config, "claude", ".delivered.json"), map[string]string{"fingerprint": manifest.DeliveredFingerprint, "destination_instance": "archive"}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	captureFault = func(stage, rel string) error {
		if stage == "delivery-read" {
			reads++
		}
		return nil
	}
	summary, err := Capture(context.Background(), config, config.Sources[:1], nil)
	if err != nil || !summary.OK || reads == 0 || summary.Sources[0].FilesSeen != 0 {
		t.Fatal("delivered source was not verified and suppressed", summary, reads, err)
	}

	// Cancel after hashing starts, before the rest of the source is read.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads = 0
	captureFault = func(stage, rel string) error {
		if stage == "delivery-read" {
			reads++
			if reads == 2 {
				cancel()
			}
		}
		return nil
	}
	summary, err = Capture(ctx, config, config.Sources[:1], nil)
	if err != nil || summary.OK || !strings.Contains(jsonText(summary), context.Canceled.Error()) {
		t.Fatal("delivery hashing ignored cancellation", summary, err)
	}
}

func TestCaptureAppendsAndPreservesRewrittenFiles(t *testing.T) {
	useHost(t, "host-a")
	claude := filepath.Join(t.TempDir(), "claude")
	file := filepath.Join(claude, "p", "s.jsonl")
	writeSourceFile(t, file, "line one\n")
	config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: claude, Enabled: true})
	runCapture(t, config)

	writeSourceFile(t, file, "line one\nline two\n")
	grown := captureResult(t, runCapture(t, config), "claude")
	entry := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/s.jsonl")
	if grown.FilesCopied != 1 || grown.VersionsPreserved != 0 || len(entry.Previous) != 0 || readText(t, capturedPath(config, "claude", entry.Captured)) != "line one\nline two\n" {
		t.Fatalf("grown file: %#v %#v", grown, entry)
	}

	writeSourceFile(t, file, "LINE ONE\nline two\n")
	rewritten := captureResult(t, runCapture(t, config), "claude")
	entry = manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/s.jsonl")
	if rewritten.VersionsPreserved != 1 || len(entry.Previous) != 1 || readText(t, capturedPath(config, "claude", entry.Previous[0].Captured)) != "line one\nline two\n" {
		t.Fatalf("rewritten file lost its earlier capture: %#v %#v", rewritten, entry)
	}
	if !strings.HasPrefix(entry.Previous[0].Captured, "history/") || readText(t, capturedPath(config, "claude", entry.Captured)) != "LINE ONE\nline two\n" {
		t.Fatalf("rewritten file: %#v", entry)
	}

	writeSourceFile(t, file, "short\n")
	runCapture(t, config)
	entry = manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/s.jsonl")
	if len(entry.Previous) != 2 || readText(t, capturedPath(config, "claude", entry.Previous[1].Captured)) != "LINE ONE\nline two\n" {
		t.Fatalf("truncated file lost its earlier capture: %#v", entry)
	}

	// A transcript can grow after its consistent prefix was captured. With no
	// SQLite inputs there is no cross-file database group to invalidate it.
	second := filepath.Join(claude, "p", "z.jsonl")
	writeSourceFile(t, second, "another session\n")
	appended := false
	override(t, &captureFault, func(stage, rel string) error {
		if stage == "copy" && rel == "files/p/z.jsonl" && !appended {
			appended = true
			writeSourceFile(t, file, "short\nnext tick\n")
		}
		return nil
	})
	if run := runCapture(t, config); !run.OK || !appended {
		t.Fatalf("append after prefix capture failed: %+v", run)
	}
	if got := readText(t, capturedPath(config, "claude", entry.Captured)); got != "short\n" {
		t.Fatalf("captured prefix changed: %q", got)
	}
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	if got := readText(t, capturedPath(config, "claude", entry.Captured)); got != "short\nnext tick\n" {
		t.Fatalf("next capture did not catch up: %q", got)
	}
}

func TestCaptureKeepsFilesDeletedAtSource(t *testing.T) {
	useHost(t, "host-a")
	claude := filepath.Join(t.TempDir(), "claude")
	writeSourceFile(t, filepath.Join(claude, "p", "old.jsonl"), "old\n")
	writeSourceFile(t, filepath.Join(claude, "p", "new.jsonl"), "new\n")
	config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: claude, Enabled: true})
	runCapture(t, config)

	if err := os.Remove(filepath.Join(claude, "p", "old.jsonl")); err != nil {
		t.Fatal(err)
	}
	result := captureResult(t, runCapture(t, config), "claude")
	entry := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/old.jsonl")
	if result.FilesMissing != 1 || entry.MissingSince == "" || readText(t, capturedPath(config, "claude", "files/p/old.jsonl")) != "old\n" {
		t.Fatalf("deleted source file: %#v %#v", result, entry)
	}
	since := entry.MissingSince
	runCapture(t, config)
	if again := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/old.jsonl"); again.MissingSince != since {
		t.Fatalf("missing_since moved from %s to %s", since, again.MissingSince)
	}

	// A wholly unavailable source is more likely unmounted than deleted.
	if err := os.RemoveAll(claude); err != nil {
		t.Fatal(err)
	}
	gone := runCapture(t, config)
	if gone.OK || captureResult(t, gone, "claude").State != "failed" {
		t.Fatalf("unavailable source reported success: %#v", gone)
	}
	if entry := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/new.jsonl"); entry.MissingSince != "" {
		t.Fatalf("an unavailable source marked its files missing: %#v", entry)
	}
}

func TestCaptureResumesAfterInterruption(t *testing.T) {
	useHost(t, "host-a")
	claude := filepath.Join(t.TempDir(), "claude")
	for index := range 10 {
		writeSourceFile(t, filepath.Join(claude, "p", fmt.Sprintf("s%02d.jsonl", index)), strings.Repeat(fmt.Sprintf("line %d\n", index), 100))
	}
	config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: claude, Enabled: true})
	previousInterval := captureCheckpointInterval
	captureCheckpointInterval = 0
	t.Cleanup(func() { captureCheckpointInterval = previousInterval; captureFault = nil })

	// A kill mid-run: nothing after the fault runs, as if the process died.
	copies := 0
	captureFault = func(stage, rel string) error {
		if copies++; copies == 6 {
			panic("killed")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the injected kill did not happen")
			}
		}()
		runCapture(t, config)
	}()
	manifest := readCaptureManifest(t, config, "claude")
	if len(manifest.Files) != 5 || manifest.LastRun == nil || manifest.LastRun.FinishedAt != "" {
		t.Fatalf("killed run left %d files, last run %#v", len(manifest.Files), manifest.LastRun)
	}
	// What a kill mid-copy leaves behind, for a file no later run rewrites.
	stale := capturedPath(config, "claude", "files/p/orphan.jsonl") + captureTempSuffix
	writeSourceFile(t, stale, "half written")

	// An unplug mid-run: the drive fails and the run reports it.
	copies = 0
	captureFault = func(stage, rel string) error {
		if copies++; copies == 3 {
			return errors.New("device not configured")
		}
		return nil
	}
	failed := runCapture(t, config)
	result := captureResult(t, failed, "claude")
	manifest = readCaptureManifest(t, config, "claude")
	if failed.OK || result.State != "failed" || result.FilesCopied != 2 || len(manifest.Files) != 7 || len(manifest.LastRun.Errors) == 0 {
		t.Fatalf("failed run: %#v, manifest has %d files", result, len(manifest.Files))
	}

	captureFault = nil
	resumed := runCapture(t, config)
	result = captureResult(t, resumed, "claude")
	if !resumed.OK || result.FilesCopied != 3 || result.FilesUnchanged != 7 {
		t.Fatalf("resumed run: %#v", result)
	}
	if len(readCaptureManifest(t, config, "claude").Files) != 10 {
		t.Fatal("resumed run did not complete the manifest")
	}
	_ = filepath.WalkDir(filepath.Join(config.CaptureRoot, "host-a"), func(file string, entry os.DirEntry, err error) error {
		if strings.HasSuffix(file, captureTempSuffix) {
			t.Errorf("temporary file left behind: %s", file)
		}
		return nil
	})
	for index := range 10 {
		rel := fmt.Sprintf("files/p/s%02d.jsonl", index)
		if readText(t, capturedPath(config, "claude", rel)) != strings.Repeat(fmt.Sprintf("line %d\n", index), 100) {
			t.Fatalf("%s is wrong after resuming", rel)
		}
	}
}

func openWritableSQLite(t *testing.T, file string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func execSQL(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func TestCaptureSnapshotsSQLiteConsistentlyWhileWritten(t *testing.T) {
	slowTest(t)
	useHost(t, "host-a")
	conductor := filepath.Join(t.TempDir(), "com.conductor.app")
	if err := os.MkdirAll(conductor, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openWritableSQLite(t, filepath.Join(conductor, "conductor.db"))
	execSQL(t, db, "CREATE TABLE sessions(id TEXT PRIMARY KEY, message_count INTEGER)", "CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT, body TEXT)",
		"INSERT INTO sessions VALUES('s1',0)")
	body := strings.Repeat("x", 4000)
	commit := func() error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec("INSERT INTO messages(session_id,body) VALUES('s1',?)", body); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE sessions SET message_count=message_count+1 WHERE id='s1'"); err != nil {
			return err
		}
		return tx.Commit()
	}
	// What commit would write 5000 times, in one transaction.
	seed := seedTx(t, db)
	for range 5000 {
		if _, err := seed.Exec("INSERT INTO messages(session_id,body) VALUES('s1',?)", body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec("UPDATE sessions SET message_count=5000 WHERE id='s1'"); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, seed)
	cache := openWritableSQLite(t, filepath.Join(conductor, "cache.db"))
	execSQL(t, cache, "CREATE TABLE kv(k TEXT, v TEXT)")

	var commits atomic.Int64
	stop, done := make(chan struct{}), make(chan error)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := commit(); err != nil {
				done <- err
				return
			}
			commits.Add(1)
		}
	}()
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: conductor, Enabled: true})
	started := commits.Load()
	summary := runCapture(t, config)
	during := commits.Load() - started
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("writer failed during the snapshot: %v", err)
	}
	result := captureResult(t, summary, "conductor")
	if !summary.OK || result.SnapshotsTaken != 1 || result.DatabasesSeen != 1 {
		t.Fatalf("snapshot run: %#v", summary)
	}
	if during == 0 {
		t.Log("the writer did not commit during the capture; consistency was not exercised under load")
	}
	snapshotFile := capturedPath(config, "conductor", "files/conductor.db")
	if _, err := os.Stat(capturedPath(config, "conductor", "files/cache.db")); err == nil {
		t.Fatal("a database without sessions and messages was captured")
	}
	snapshot, err := openReadOnlySQLite(snapshotFile)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var counted, messages int
	var mode, integrity string
	if err := snapshot.QueryRow("SELECT message_count,(SELECT COUNT(*) FROM messages) FROM sessions").Scan(&counted, &messages); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if counted != messages || messages < 5000 || mode != "delete" || integrity != "ok" {
		t.Fatalf("snapshot: message_count=%d messages=%d journal=%s integrity=%s (writer committed %d times during capture)", counted, messages, mode, integrity, during)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(snapshotFile + suffix); err == nil {
			t.Fatalf("snapshot has a %s file", suffix)
		}
	}
	manifest := readCaptureManifest(t, config, "conductor")
	if len(manifest.Snapshots) != 1 || manifest.Snapshots[0].Method != "sqlite-backup" || manifest.Snapshots[0].Source.Size == 0 || len(manifest.Files) != 0 {
		t.Fatalf("manifest: %#v", manifest)
	}
}

func TestCaptureRetainsSnapshotGenerations(t *testing.T) {
	useHost(t, "host-a")
	conductor := filepath.Join(t.TempDir(), "conductor")
	if err := os.MkdirAll(conductor, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openWritableSQLite(t, filepath.Join(conductor, "conductor.db"))
	execSQL(t, db, "CREATE TABLE sessions(id TEXT PRIMARY KEY)", "CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT)", "INSERT INTO sessions VALUES('generation-1')")
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: conductor, Enabled: true})
	sessionsIn := func(rel string) string {
		t.Helper()
		snapshot, err := openReadOnlySQLite(capturedPath(config, "conductor", rel))
		if err != nil {
			t.Fatal(err)
		}
		defer snapshot.Close()
		var ids string
		if err := snapshot.QueryRow("SELECT group_concat(id) FROM (SELECT id FROM sessions ORDER BY id)").Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	runCapture(t, config)
	// A session deleted at the source survives in the previous generation.
	execSQL(t, db, "DELETE FROM sessions", "INSERT INTO sessions VALUES('generation-2')")
	runCapture(t, config)
	snapshot := readCaptureManifest(t, config, "conductor").Snapshots[0]
	if len(snapshot.Generations) != 1 || sessionsIn(snapshot.Generations[0].Captured) != "generation-1" || sessionsIn(snapshot.Captured) != "generation-2" {
		t.Fatalf("second snapshot: %#v", snapshot)
	}
	first := snapshot.Generations[0].Captured

	execSQL(t, db, "INSERT INTO sessions VALUES('generation-3')")
	runCapture(t, config)
	snapshot = readCaptureManifest(t, config, "conductor").Snapshots[0]
	if len(snapshot.Generations) != 1 || sessionsIn(snapshot.Generations[0].Captured) != "generation-2" {
		t.Fatalf("third snapshot: %#v", snapshot)
	}
	if _, err := os.Stat(capturedPath(config, "conductor", first)); err == nil {
		t.Fatalf("generation beyond capture_snapshot_generations was kept: %s", first)
	}

	unchanged := captureResult(t, runCapture(t, config), "conductor")
	if unchanged.SnapshotsTaken != 0 || unchanged.SnapshotsUnchanged != 1 {
		t.Fatalf("unchanged database was snapshotted again: %#v", unchanged)
	}
	if _, err := os.Stat(capturedPath(config, "conductor", recordDir)); !os.IsNotExist(err) {
		t.Fatal("default capture exported SQLite rows", err)
	}

	config.CaptureGenerations = 0
	execSQL(t, db, "INSERT INTO sessions VALUES('generation-4')")
	runCapture(t, config)
	if snapshot := readCaptureManifest(t, config, "conductor").Snapshots[0]; len(snapshot.Generations) != 0 || sessionsIn(snapshot.Captured) != "generation-2,generation-3,generation-4" {
		t.Fatalf("with no generations: %#v", snapshot)
	}
	if entries, err := os.ReadDir(capturedPath(config, "conductor", captureHistoryDir)); err == nil && len(entries) > 0 {
		t.Fatalf("history kept %d entries with no generations configured", len(entries))
	}
	nativeConfig := config
	// A pending library must keep every unindexed generation, even when its
	// ordinary retention is zero and indexing has never completed once.
	// Protection survives a fresh config load and ordinary capture, without
	// relying on the application worker's runtime-only PendingCapture flag.
	pending := testLibrary(t, filepath.Join(t.TempDir(), "pending"))
	if err := protectPendingEvidence(pending); err != nil {
		t.Fatal(err)
	}
	config = pending
	config.Sources = []SourceConfig{{Name: "conductor", Kind: "conductor", Path: conductor, Enabled: true}}
	config.CaptureGenerations = 0
	runCapture(t, config)
	for _, id := range []string{"pending-1", "pending-2", "pending-3"} {
		execSQL(t, db, "DELETE FROM sessions", "INSERT INTO sessions VALUES('"+id+"')")
		runCapture(t, config)
	}
	snapshot = readCaptureManifest(t, config, "conductor").Snapshots[0]
	if len(snapshot.Generations) != 3 || sessionsIn(snapshot.Generations[2].Captured) != "generation-2,generation-3,generation-4" {
		t.Fatal("pending retention lost history before its first index", snapshot)
	}

	config = nativeConfig
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "moved-conductor")
	if err := os.Rename(conductor, moved); err != nil {
		t.Fatal(err)
	}
	config.Sources[0].Path, config.Sources[0].Account = moved, "changed-account"
	if run := runCapture(t, config); !run.OK {
		t.Fatalf("native capture after configuration edit: %+v", run)
	}
	manifest := readCaptureManifest(t, config, "conductor")
	if manifest.Source.Account != "changed-account" || manifest.Snapshots[0].Path != filepath.Join(moved, "conductor.db") || sessionsIn(manifest.Snapshots[0].Captured) != "pending-3" {
		t.Fatalf("native snapshot kept stale configuration: %+v", manifest)
	}
}

func TestCaptureSeparatesHosts(t *testing.T) {
	root := t.TempDir()
	config := captureTestConfig(t)
	for _, host := range []string{"host-a", "host-b"} {
		useHost(t, host)
		claude := filepath.Join(root, host, "claude")
		writeSourceFile(t, filepath.Join(claude, "p", host+".jsonl"), host+"\n")
		config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Enabled: true}}
		if summary := runCapture(t, config); !summary.OK || summary.Root != filepath.Join(config.CaptureRoot, host) {
			t.Fatalf("%s: %#v", host, summary)
		}
	}
	for _, host := range []string{"host-a", "host-b"} {
		dir := filepath.Join(config.CaptureRoot, host)
		if readText(t, filepath.Join(dir, "claude", "files", "p", host+".jsonl")) != host+"\n" {
			t.Fatalf("%s capture missing", host)
		}
		entries, _ := os.ReadDir(filepath.Join(dir, "claude", "files", "p"))
		var manifest captureManifest
		_ = json.Unmarshal([]byte(readText(t, filepath.Join(dir, "claude", captureManifestName))), &manifest)
		if len(entries) != 1 || manifest.Host.ID != host || len(manifest.Files) != 1 || !strings.Contains(readText(t, filepath.Join(dir, captureHostName)), `"id": "`+host+`"`) {
			t.Fatalf("%s captures mix hosts: %v %#v", host, entries, manifest)
		}
	}
}

func TestCaptureTL1MapsRegistryPaths(t *testing.T) {
	useHost(t, "host-a")
	root := t.TempDir()
	database := filepath.Join(root, "tl1", "project.db")
	transcripts := filepath.Join(root, "tl1", "project", "transcripts")
	repository := filepath.Join(root, "repo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(repository, "tl1.json"), "{}")
	writeSourceFile(t, filepath.Join(transcripts, "task-1", "attempt.jsonl"), "{}\n")
	db := openWritableSQLite(t, database)
	execSQL(t, db, "CREATE TABLE tasks(id TEXT)", "CREATE TABLE task_attempts(id TEXT, task_id TEXT, attempt_number INTEGER)")
	registry := filepath.Join(root, "tl1", "registry.json")
	rows := []map[string]string{
		{"installation_id": "abc123", "db_path": database, "config_path": filepath.Join(repository, "tl1.json"), "code_repo": repository, "transcripts_dir": transcripts, "project_name": "project"},
		{"installation_id": "gone", "db_path": filepath.Join(root, "missing.db"), "config_path": filepath.Join(repository, "tl1.json"), "code_repo": repository},
	}
	data, _ := json.Marshal(map[string]any{"installations": rows})
	writeSourceFile(t, registry, string(data))
	config := captureTestConfig(t, SourceConfig{Name: "tl1", Kind: "tl1", Path: registry, Enabled: true})

	summary := runCapture(t, config)
	result := captureResult(t, summary, "tl1")
	if !summary.OK || result.FilesCopied != 2 || result.SnapshotsTaken != 1 {
		t.Fatalf("tl1 capture: %#v", summary)
	}
	manifest := readCaptureManifest(t, config, "tl1")
	if len(manifest.Installations) != 1 {
		t.Fatalf("installations: %#v", manifest.Installations)
	}
	installation := manifest.Installations[0]
	if installation.CapturedDB != "files/installations/abc123/project.db" || installation.CapturedTranscripts != "files/installations/abc123/transcripts" || installation.DBPath != database {
		t.Fatalf("installation mapping: %#v", installation)
	}
	mapped := map[string]string{}
	for _, mapping := range manifest.PathMap {
		mapped[mapping.Original] = mapping.Captured
	}
	if mapped[registry] != "files/registry.json" || mapped[database] != installation.CapturedDB || mapped[transcripts] != installation.CapturedTranscripts {
		t.Fatalf("path map: %#v", manifest.PathMap)
	}
	for _, rel := range []string{"files/registry.json", "files/installations/abc123/transcripts/task-1/attempt.jsonl", installation.CapturedDB} {
		if _, err := os.Stat(capturedPath(config, "tl1", rel)); err != nil {
			t.Fatalf("%s not captured: %v", rel, err)
		}
	}

	// An installation leaving the registry keeps its captures and mapping.
	data, _ = json.Marshal(map[string]any{"installations": rows[1:]})
	writeSourceFile(t, registry, string(data))
	runCapture(t, config)
	manifest = readCaptureManifest(t, config, "tl1")
	if len(manifest.Installations) != 1 || manifest.Installations[0].MissingSince == "" || len(manifest.PathMap) != 3 || manifest.Snapshots[0].MissingSince == "" {
		t.Fatalf("unregistered installation: %#v %#v", manifest.Installations, manifest.PathMap)
	}
	if _, err := os.Stat(capturedPath(config, "tl1", installation.CapturedDB)); err != nil {
		t.Fatalf("unregistered installation's snapshot was removed: %v", err)
	}
}

func TestCaptureRootDefaultsAndGuards(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) Config {
		t.Helper()
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		config, err := LoadConfig(file)
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	if config := write("library.toml", "library = true\ncatalog_path = \"catalog/catalog.sqlite3\"\n"); config.CaptureRoot != filepath.Join(dir, "captures") || config.CaptureGenerations != 1 {
		t.Fatalf("library capture root: %s", config.CaptureRoot)
	}
	if config := write("archive.toml", "data_dir = \"data\"\n"); config.CaptureRoot != filepath.Join(dir, "data", "captures") {
		t.Fatalf("legacy capture root: %s", config.CaptureRoot)
	}
	if config := write("custom.toml", "capture_root = \"raw\"\ncapture_snapshot_generations = 3\n"); config.CaptureRoot != filepath.Join(dir, "raw") || config.CaptureGenerations != 3 {
		t.Fatalf("configured capture root: %s %d", config.CaptureRoot, config.CaptureGenerations)
	}

	useHost(t, "host-a")
	config := captureTestConfig(t)
	config.CaptureRoot = filepath.Join(dir, "unmounted-volume", "captures")
	if _, err := beginCapture(config); err == nil {
		t.Fatal("a capture root whose drive is absent was created")
	}
	if _, err := os.Stat(filepath.Join(dir, "unmounted-volume")); err == nil {
		t.Fatal("the missing mount point was recreated")
	}
	config = captureTestConfig(t)
	session, err := beginCapture(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beginCapture(config); !errors.Is(err, errCaptureBusy) {
		t.Fatalf("second concurrent capture: %v", err)
	}
	session.Close()
	if session, err := beginCapture(config); err != nil {
		t.Fatalf("capture lock was not released: %v", err)
	} else {
		session.Close()
	}
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint("reader-wait-cancel=", cancelled), func(t *testing.T) {
			held, err := beginCapture(config)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			// A wait says whose capture it waits for.
			phases := make(chan string, 4)
			ctx = withWorkProgress(ctx, func(work activityWork) { phases <- work.Phase })
			busy := make(chan struct{}, 1)
			result := make(chan error, 1)
			go func() {
				lock, err := waitCaptureLock(ctx, "Grady's MacBook", func() (*os.File, error) {
					lock, err := lockCaptureTarget(ctx, filepath.Join(config.CaptureRoot, currentHost().ID, "reader"))
					if errors.Is(err, errCaptureBusy) {
						select {
						case busy <- struct{}{}:
						default:
						}
					}
					return lock, err
				})
				if lock != nil {
					lock.Close()
				}
				result <- err
			}()
			select {
			case <-busy:
			case <-ctx.Done():
				t.Fatal("reader did not encounter the held capture lock")
			}
			if cancelled {
				cancel()
			} else {
				held.Close()
			}
			err = <-result
			if cancelled && !errors.Is(err, context.Canceled) || !cancelled && err != nil {
				t.Fatal("reader lock wait", err)
			}
			if len(phases) != 1 || <-phases != "waiting for the capture on Grady's MacBook to finish" {
				t.Fatal("the wait was not reported once with the capturing Mac")
			}
		})
	}
	if _, err := captureSources(config, []string{"nope"}); err == nil {
		t.Fatal("unknown source accepted")
	}
}

func TestCaptureAPIRunsInBackgroundAndReportsSafeToUnplug(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	claude := filepath.Join(t.TempDir(), "claude")
	writeSourceFile(t, filepath.Join(claude, "p", "s.jsonl"), "{}\n")
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Enabled: true}}
	server := testServer(t, config, catalog)
	release := make(chan struct{})
	captureFault = func(stage, rel string) error { <-release; return nil }
	t.Cleanup(func() { captureFault = nil })
	call := func(method, body string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(method, "/api/capture", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		value := map[string]any{}
		_ = json.Unmarshal(response.Body.Bytes(), &value)
		return response.Code, value
	}
	if code, _ := call(http.MethodPost, `{"sources":["nope"]}`); code != http.StatusBadRequest {
		t.Fatalf("unknown source: %d", code)
	}
	if code, body := call(http.MethodPost, `{}`); code != http.StatusAccepted || body["run"] == nil {
		t.Fatalf("start: %d %#v", code, body)
	}
	if code, body := call(http.MethodGet, ""); code != http.StatusOK || body["active"] != true || body["safe_to_unplug"] != false {
		t.Fatalf("status while running: %d %#v", code, body)
	}
	if code, _ := call(http.MethodPost, `{}`); code != http.StatusConflict {
		t.Fatalf("second capture: %d", code)
	}
	if _, err := Capture(context.Background(), config, config.Sources, nil); !errors.Is(err, errCaptureBusy) {
		t.Fatalf("CLI capture during an API capture: %v", err)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body := call(http.MethodGet, "")
		run, _ := body["run"].(map[string]any)
		if run["state"] == "complete" && body["safe_to_unplug"] == true {
			if run["files_copied"] != float64(1) || run["completed_sources"] != float64(1) {
				t.Fatalf("finished run: %#v", run)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capture did not finish: %#v", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCaptureAPIReleasesItsLockWhenAStopBeginsFirst(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	claude := filepath.Join(t.TempDir(), "claude")
	writeSourceFile(t, filepath.Join(claude, "p", "s.jsonl"), "{}\n")
	config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
	config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Enabled: true}}
	server := testServer(t, config, catalog)
	// The request was admitted; then a release begins before its worker starts.
	server.beginStop()
	response := httptest.NewRecorder()
	server.startCapture(response, map[string]any{})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("capture after a stop began: %d %s", response.Code, response.Body.String())
	}
	if runs := server.captures.list(); len(runs) != 1 || runs[0].State != "failed" || runs[0].CompletedAt == nil {
		t.Fatalf("runs %#v", runs)
	}
	if server.captureActive() {
		t.Fatal("capture still reported active")
	}
	session, err := beginCapture(config)
	if err != nil {
		t.Fatalf("capture lock left held: %v", err)
	}
	session.Close()
}

func TestCaptureExports(t *testing.T) {
	useHost(t, "host-a")
	root := t.TempDir()
	chatgpt, canonical, single := filepath.Join(root, "chatgpt"), filepath.Join(root, "canonical"), filepath.Join(root, "release.json")
	writeSourceFile(t, filepath.Join(chatgpt, "conversations.json"), "[]")
	writeSourceFile(t, filepath.Join(chatgpt, "user.json"), "{}")
	writeSourceFile(t, filepath.Join(canonical, "a", "one.json"), "{}")
	writeSourceFile(t, filepath.Join(canonical, "notes.md"), "")
	writeSourceFile(t, single, "{}")
	config := captureTestConfig(t, SourceConfig{Name: "chatgpt", Kind: "chatgpt-export", Path: chatgpt, Enabled: true},
		SourceConfig{Name: "canonical", Kind: "canonical", Path: canonical, Enabled: true}, SourceConfig{Name: "release", Kind: "tl1-export", Path: single, Enabled: true})
	summary := runCapture(t, config)
	if !summary.OK {
		t.Fatalf("export capture: %#v", summary)
	}
	for source, want := range map[string]string{"chatgpt": "files/conversations.json", "canonical": "files/a/one.json", "release": "files/release.json"} {
		manifest := readCaptureManifest(t, config, source)
		if len(manifest.Files) != 1 || manifest.Files[0].Captured != want {
			t.Fatalf("%s captured %#v", source, manifest.Files)
		}
	}
}

// Opt-in filesystem imports parse the durable inventory, even when the
// source disappears or grows between publication and the first parser read.
func TestIngestPreservesBeforeParsing(t *testing.T) {
	t.Run("default-off", func(t *testing.T) {
		catalog, _ := testCatalog(t)
		source := stableCodexSource(t, 1)
		if result := ingestSource(t, catalog, source); result.Error != nil || result.Revision != "" {
			t.Fatal(result)
		}
		appendSourceFile(t, source.Path, codexLines("another-session", 1))
		if result := ingestSource(t, catalog, source); result.Error != nil || result.Revision != "" {
			t.Fatal(result)
		}
		moved := filepath.Join(t.TempDir(), "rollout-moved.jsonl")
		if err := os.Rename(source.Path, moved); err != nil {
			t.Fatal(err)
		}
		source.Path, source.Account = moved, "changed-account"
		if result := ingestSource(t, catalog, source); result.Error != nil || result.Workspaces == 0 {
			t.Fatal("configuration edit blocked indexing", result)
		}
		if _, err := os.Stat(catalog.captureRootPath()); !os.IsNotExist(err) {
			t.Fatal("default indexing created captures", err)
		}
		config := Config{CaptureRoot: catalog.captureRootPath(), Sources: []SourceConfig{source}}
		if run := runCapture(t, config); !run.OK {
			t.Fatal(run)
		}
		target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
		if result := catalog.IndexCapture(t.Context(), target, nil); result.Error != nil || result.Revision != "" {
			t.Fatal("default captured index published an experimental inventory", result)
		}
		if _, err := os.Stat(filepath.Join(target.Dir, "inputs")); !os.IsNotExist(err) {
			t.Fatal("default captured index retained duplicate inputs", err)
		}
	})
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint("removed=", removed), func(t *testing.T) {
			useHost(t, "host-a")
			catalog, _ := testCatalog(t)
			root := filepath.Join(t.TempDir(), "claude")
			file := filepath.Join(root, "project", "s.jsonl")
			first := claudeLine("s", "u1", "retained first", "/project", 1)
			writeSourceFile(t, file, first)
			index := filepath.Join(filepath.Dir(file), "sessions-index.json")
			writeSourceFile(t, index, `{"entries":[{"sessionId":"s","customTitle":"Preserved title"}]}`)
			source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
			adapter, _ := MakeAdapter(source)
			changed := false
			result := catalog.Ingest(adapter, func(phase string, _, _, _, _, _ int) {
				if phase != "discovering inputs" || changed {
					return
				}
				changed = true
				if removed {
					if err := os.RemoveAll(root); err != nil {
						t.Fatal(err)
					}
				} else {
					appendSourceFile(t, file, claudeLine("s", "u2", "later live append", "/project", 2))
					writeSourceFile(t, index, `{"entries":[{"sessionId":"s","customTitle":"Later title"}]}`)
				}
			})
			if result.Error != nil || !changed || result.Revision == "" || conversationMessages(t, catalog, "s") != 1 || workspaceTitle(t, catalog, "s") != "Preserved title" {
				t.Fatalf("acquired revision: %+v", result)
			}
			dir := filepath.Join(catalog.captureRootPath(), "host-a", source.Name, "inputs", result.Revision)
			retained, _, _, err := openCaptureAdapter(captureTarget{Host: currentHost(), Source: source.Name, Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if countRows(t, catalog, "SELECT COUNT(*) FROM copy_evidence WHERE revision=?", result.Revision) != 2 {
				t.Fatal("catalog has no exact retained identity")
			}
			if removed {
				// A parser upgrade uses the same retained bytes without the source.
				ctx := withIngestMode(t.Context(), ingestMode{force: true})
				if replay := catalog.IngestContext(ctx, retained, nil); replay.Error != nil {
					t.Fatal(replay.Error)
				}
			} else if next := ingestSource(t, catalog, source); next.Messages != 2 || conversationMessages(t, catalog, "s") != 2 {
				t.Fatalf("next revision: %+v", next)
			}
		})
	}
}

func TestIngestAcquisitionFailureDoesNotAdvanceCheckpoints(t *testing.T) {
	for _, stage := range []string{"copy", "input-publish"} {
		t.Run(stage, func(t *testing.T) {
			useHost(t, "host-a")
			catalog, _ := testCatalog(t)
			source := SourceConfig{Name: "claude", Kind: "claude", Path: filepath.Join(t.TempDir(), "s.jsonl"), Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
			writeSourceFile(t, source.Path, claudeLine("s", "u1", "first", "/p", 1))
			ingestSource(t, catalog, source)
			before, err := queryMaps(catalog.DB, "SELECT * FROM source_item_states")
			if err != nil {
				t.Fatal(err)
			}
			appendSourceFile(t, source.Path, claudeLine("s", "u2", "pending", "/p", 2))
			override(t, &captureFault, func(point, _ string) error {
				if point == stage {
					return errors.New("injected acquisition failure")
				}
				return nil
			})
			adapter, _ := MakeAdapter(source)
			result := catalog.Ingest(adapter, nil)
			if result.Error == nil || !result.Pending || conversationMessages(t, catalog, "s") != 1 {
				t.Fatalf("failed acquisition published content: %+v", result)
			}
			after, _ := queryMaps(catalog.DB, "SELECT * FROM source_item_states")
			if jsonText(before) != jsonText(after) {
				t.Fatal("failed acquisition advanced parts")
			}
			status := catalog.inputPreservationStatus(t.Context(), "host-a", source.Name)
			if status["pending_newer_inputs"] != true || status["error"] == nil {
				t.Fatalf("missing pending failure: %#v", status)
			}
		})
	}
}

func TestIngestResumesDurableInputAfterCancellation(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	source := SourceConfig{Name: "claude", Kind: "claude", Path: filepath.Join(t.TempDir(), "s.jsonl"), Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
	writeSourceFile(t, source.Path, claudeLine("s", "u1", "survives cancellation", "/p", 1))
	adapter, _ := MakeAdapter(source)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := catalog.IngestContext(ctx, adapter, func(phase string, _, _, _, _, _ int) {
		if phase == "discovering inputs" {
			cancel()
		}
	})
	if result.Error == nil || result.Revision == "" || countRows(t, catalog, "SELECT COUNT(*) FROM source_item_states") != 0 || countRows(t, catalog, "SELECT COUNT(*) FROM conversations") != 0 {
		t.Fatalf("cancelled acquisition: %+v", result)
	}
	if err := os.Remove(source.Path); err != nil {
		t.Fatal(err)
	}
	target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(catalog.captureRootPath(), "host-a", source.Name)}
	replay := catalog.IndexCapture(t.Context(), target, nil)
	if replay.Error != nil || conversationMessages(t, catalog, "s") != 1 {
		t.Fatalf("offline resume: %+v", replay)
	}
}

func TestCaptureCleansOnlyAbandonedReaderLeases(t *testing.T) {
	dir := t.TempDir()
	lease, err := os.MkdirTemp(dir, ".reader-*"+captureTempSuffix)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(lease, "snapshot.db")
	writeSourceFile(t, file, "retained native bytes")
	lock, err := os.OpenFile(filepath.Join(lease, ".reader.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	removeCaptureTemporaries(dir)
	if _, err = os.Stat(file); err != nil {
		t.Fatal("active reader lost its snapshot", err)
	}
	lock.Close()
	removeCaptureTemporaries(dir)
	if _, err = os.Stat(lease); !os.IsNotExist(err) {
		t.Fatalf("abandoned native lease remains: %v", err)
	}
}

func TestFreezeRetainedInputsWithoutHardLinks(t *testing.T) {
	for _, durable := range []bool{true, false} {
		t.Run(fmt.Sprint("durable=", durable), func(t *testing.T) {
			useHost(t, "host-a")
			catalog, _ := testCatalog(t)
			root := t.TempDir()
			source := SourceConfig{Name: "source", Kind: "claude", Path: root, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
			var change func()
			if durable {
				file := filepath.Join(root, "project", "s.jsonl")
				writeSourceFile(t, file, claudeLine("s", "u1", "first", "/project", 1))
				change = func() { appendSourceFile(t, file, claudeLine("s", "u2", "later", "/project", 2)) }
			} else {
				source.Kind = "conductor"
				exec := conductorFixture(t, root)
				exec(addConductorSession("s", 1)...)
				updates := 0
				change = func() {
					updates++
					exec(fmt.Sprintf("UPDATE session_messages SET content='later-%d' WHERE session_id='s'", updates))
				}
			}
			config := captureTestConfig(t, source)
			if run := runCapture(t, config); !run.OK {
				t.Fatal(run)
			}
			// Populate a native generation too, so the temporary reader must
			// lease latest and historical snapshots on the linkless volume.
			if !durable {
				change()
				if run := runCapture(t, config); !run.OK {
					t.Fatal(run)
				}
			}
			override(t, &retainedLinkFile, func(string, string) error { return syscall.ENOTSUP })
			target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, "host-a", source.Name)}
			lock, err := lockCaptureTarget(t.Context(), target.Dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if durable {
				// Simulate a crash after copying but before the inventory commit.
				originalFault := captureFault
				override(t, &captureFault, func(stage, path string) error {
					if stage == "input-publish" {
						return errors.New("interrupted inventory publication")
					}
					return nil
				})
				if _, err = freezeCaptureInput(t.Context(), target, source, durable); err == nil {
					t.Fatal("publication fault missed")
				}
				captureFault = originalFault
			}
			input, err := freezeCaptureInput(t.Context(), target, source, durable)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			view := captureViewOf(input.Adapter)
			check := func(rel string) {
				t.Helper()
				original, copied := filepath.Join(target.Dir, rel), filepath.Join(view.dir, rel)
				a, err := os.Stat(original)
				if err != nil {
					t.Fatal(err)
				}
				b, err := os.Stat(copied)
				if err != nil || os.SameFile(a, b) || !a.ModTime().Equal(b.ModTime()) {
					t.Fatal("reader did not preserve independent bytes and mtime", rel, err)
				}
				h1, n1, e1 := fileSHA256(original)
				h2, n2, e2 := fileSHA256(copied)
				if e1 != nil || e2 != nil || h1 != h2 || n1 != n2 {
					t.Fatal("reader copy changed input", rel, e1, e2)
				}
			}
			for _, f := range view.manifest.Files {
				check(f.Captured)
			}
			for _, snapshot := range view.manifest.Snapshots {
				check(snapshot.Captured)
				for _, generation := range snapshot.Generations {
					check(generation.Captured)
				}
			}
			again, err := freezeCaptureInput(t.Context(), target, source, durable)
			if err != nil {
				t.Fatal("could not reuse copied inventory", err)
			}
			again.Close()
			lock.Close()
			change()
			if run := runCapture(t, config); !run.OK {
				t.Fatal(run)
			}
			if err = os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			result := catalog.IngestContext(t.Context(), input.Adapter, nil)
			id, body := "s", "first"
			if !durable {
				id, body = "conductor:s", "later-1"
				if len(view.manifest.Snapshots) == 0 || len(view.manifest.Snapshots[0].Generations) == 0 {
					t.Fatal("native fixture lacks a historical snapshot")
				}
			}
			var text string
			if err = catalog.DB.QueryRow("SELECT text FROM messages WHERE kind='message' LIMIT 1").Scan(&text); err != nil {
				t.Fatal(err)
			}
			if result.Error != nil || conversationMessages(t, catalog, id) != 1 || text != body {
				t.Fatalf("rotated reader failed without live sources: %+v, text %q", result, text)
			}
		})
	}
}

func TestFreezeRetainedCopyFailureDoesNotPublish(t *testing.T) {
	for _, mode := range []string{"cancel", "corrupt", "reserve"} {
		t.Run(mode, func(t *testing.T) {
			useHost(t, "host-a")
			source := SourceConfig{Name: "claude", Kind: "claude", Path: filepath.Join(t.TempDir(), "s.jsonl"), Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
			writeSourceFile(t, source.Path, claudeLine("s", "u1", "first", "/project", 1))
			config := captureTestConfig(t, source)
			if run := runCapture(t, config); !run.OK {
				t.Fatal(run)
			}
			target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, "host-a", source.Name)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			override(t, &retainedLinkFile, func(input, _ string) error {
				if mode == "cancel" {
					cancel()
				} else if mode == "corrupt" {
					if err := os.WriteFile(input, []byte("corrupt"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return syscall.ENOTSUP
			})
			if mode == "reserve" {
				if err := savePreservationPins(target.Dir, preservationPins{Version: 1, Gates: map[string]int64{"fixture": 0}}); err != nil {
					t.Fatal(err)
				}
			}
			lock, err := lockCaptureTarget(t.Context(), target.Dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if _, err = freezeCaptureInput(ctx, target, source, true); err == nil {
				t.Fatal("copy failure published an inventory")
			}
			err = filepath.WalkDir(filepath.Join(target.Dir, "inputs"), func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() && (entry.Name() == captureManifestName || strings.HasSuffix(entry.Name(), captureTempSuffix)) {
					t.Fatal("failed copy left published inventory or partial bytes", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
