package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
	"github.com/gbdubs/pharos/internal/platform/fsinfo"
	"github.com/klauspost/compress/zstd"
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

// capturedEntries is what a manifest says was captured, without its run
// record: a failed run still records itself.
func capturedEntries(t *testing.T, config Config, source string) string {
	t.Helper()
	manifest := readCaptureManifest(t, config, source)
	data, err := json.Marshal([]any{manifest.Files, manifest.Snapshots})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A capture's copy is flushed after it has the source's timestamps. A failed
// flush is a capture-drive error: the previous capture, its manifest entry
// and its history stay as they were, with no partial copy.
func TestCaptureFlushFailureKeepsThePreviousCapture(t *testing.T) {
	useHost(t, "host-a")
	claude := filepath.Join(t.TempDir(), "claude")
	file := filepath.Join(claude, "p", "s.jsonl")
	writeSourceFile(t, file, "line one\n")
	config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: claude, Enabled: true})
	runCapture(t, config)
	before := capturedEntries(t, config, "claude")
	writeSourceFile(t, file, "LINE ONE\n")
	source, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	failure := &os.PathError{Op: "fsync", Path: file, Err: syscall.EIO}
	calls := failFileSync(t, failure, func(f *os.File) bool {
		if stat, err := f.Stat(); err != nil || !stat.ModTime().Equal(source.ModTime()) {
			t.Errorf("flushed before the copy had its timestamps: %v, %v", stat, err)
		}
		return strings.HasSuffix(f.Name(), captureTempSuffix)
	})
	run := runCapture(t, config)
	result := captureResult(t, run, "claude")
	if run.OK || *calls != 1 || len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Error, "capture drive") || !strings.Contains(result.Errors[0].Error, syscall.EIO.Error()) {
		t.Fatalf("capture after %d failed flushes: %+v", *calls, result)
	}
	if after := capturedEntries(t, config, "claude"); after != before {
		t.Fatalf("a failed copy changed what the manifest records:\n%s\n%s", before, after)
	}
	if text := readText(t, capturedPath(config, "claude", "files/p/s.jsonl")); text != "line one\n" {
		t.Fatalf("previous capture replaced: %q", text)
	}
	if left := temporariesUnder(t, config.CaptureRoot); len(left) != 0 {
		t.Fatalf("partial copies left behind: %v", left)
	}
	if _, err := os.Stat(capturedPath(config, "claude", "history")); !os.IsNotExist(err) {
		t.Fatalf("a failed copy moved the previous capture to history: %v", err)
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
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous("+catalogDurability.synchronous+")")
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

// A SQLite snapshot is flushed through a read-only handle before it is kept.
// A failed flush is a capture-drive error that keeps the previous snapshot
// as the latest, adds no generation and leaves no partial snapshot.
func TestCaptureSnapshotFlushFailureKeepsThePreviousSnapshot(t *testing.T) {
	useHost(t, "host-a")
	conductor := filepath.Join(t.TempDir(), "conductor")
	if err := os.MkdirAll(conductor, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openWritableSQLite(t, filepath.Join(conductor, "conductor.db"))
	execSQL(t, db, "CREATE TABLE sessions(id TEXT PRIMARY KEY)", "CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT)", "INSERT INTO sessions VALUES('generation-1')")
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: conductor, Enabled: true})
	runCapture(t, config)
	before := capturedEntries(t, config, "conductor")
	previous := readCaptureManifest(t, config, "conductor").Snapshots[0]
	kept := readText(t, capturedPath(config, "conductor", previous.Captured))
	execSQL(t, db, "INSERT INTO sessions VALUES('generation-2')")
	calls := failFileSync(t, syscall.EINTR, func(f *os.File) bool {
		if _, err := f.Write([]byte("x")); err == nil {
			t.Error("snapshot flushed through a writable handle")
		}
		return strings.HasSuffix(f.Name(), captureTempSuffix)
	})
	run := runCapture(t, config)
	result := captureResult(t, run, "conductor")
	if run.OK || *calls != 1 || len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Error, "capture drive") || !strings.Contains(result.Errors[0].Error, syscall.EINTR.Error()) {
		t.Fatalf("capture after %d failed flushes: %+v", *calls, result)
	}
	if capturedEntries(t, config, "conductor") != before || readText(t, capturedPath(config, "conductor", previous.Captured)) != kept {
		t.Fatal("a failed snapshot replaced the previous one")
	}
	if left := temporariesUnder(t, config.CaptureRoot); len(left) != 0 {
		t.Fatalf("partial snapshots left behind: %v", left)
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
	// Only contention is a busy capture. Any other failure to take the lock
	// is returned as itself; either way the descriptor is closed, no
	// operation holds it, and the capture writes nothing.
	claude := filepath.Join(t.TempDir(), "claude")
	writeSourceFile(t, filepath.Join(claude, "p", "s.jsonl"), "{}\n")
	sources := []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Enabled: true}}
	hostDir := filepath.Join(config.CaptureRoot, currentHost().ID)
	for _, failure := range captureLockFailures() {
		t.Run("writer-"+failure.name, func(t *testing.T) {
			var files []*os.File
			override(t, &lockCaptureWriter, func(file *os.File, mode filelock.Mode, wait filelock.WaitMode) error {
				files = append(files, file)
				return failingCaptureLock(t, filelock.Exclusive, failure.err, nil)(file, mode, wait)
			})
			ctx, op := newOpRegistry("service").begin(t.Context(), OperationSpec{Kind: "capture.cli"})
			defer op.Finish(nil)
			_, captureErr := Capture(ctx, config, sources, nil)
			lock, targetErr := lockCaptureTarget(ctx, filepath.Join(hostDir, "claude"))
			if lock != nil {
				t.Fatal("a failed acquisition returned its lock")
			}
			for _, err := range []error{captureErr, targetErr} {
				if errors.Is(err, errCaptureBusy) != failure.contended || !failure.contended && !errors.Is(err, failure.err) {
					t.Fatalf("lock failure became %v", err)
				}
			}
			if len(files) != 2 || slices.ContainsFunc(files, func(file *os.File) bool { return file.Fd() != ^uintptr(0) }) {
				t.Fatalf("a failed lock was left open: %d", len(files))
			}
			if locks := op.view().Locks; len(locks) != 0 {
				t.Fatalf("a failed lock was held by the operation: %+v", locks)
			}
			for _, name := range []string{captureHostName, "claude"} {
				if _, err := os.Stat(filepath.Join(hostDir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("a capture that failed to lock wrote %s: %v", name, err)
				}
			}
			// A reader waits only for contention, until it is cancelled;
			// it never retries an unknown failure, even when run by hand.
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			phases := 0
			waitCtx = withWorkProgress(waitCtx, func(activityWork) { phases++ })
			calls := 0
			lock, err := waitCaptureLock(waitCtx, "host-a", func() (*os.File, error) {
				if calls++; calls == 2 {
					cancel()
				}
				return lockCaptureTarget(t.Context(), filepath.Join(hostDir, "claude"))
			})
			if lock != nil || failure.contended && (!errors.Is(err, context.Canceled) || calls != 2 || phases != 1) || !failure.contended && (!errors.Is(err, failure.err) || calls != 1 || phases != 0) {
				t.Fatalf("reader wait: %v after %d attempts and %d phases", err, calls, phases)
			}
		})
	}
	// A cancelled reader does not touch the lock.
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	override(t, &lockCaptureWriter, failingCaptureLock(t, filelock.Exclusive, errors.New("locked after cancellation"), nil))
	if _, err := lockCaptureTarget(cancelled, filepath.Join(hostDir, "claude")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reader: %v", err)
	}
	override(t, &lockCaptureWriter, filelock.Lock)
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
	// Status probes the lock shared: another process's capture, or a lock
	// it cannot test, is an active capture.
	if !server.captureActive() {
		t.Fatal("a held capture lock was reported idle")
	}
	session.Close()
	for _, failure := range captureLockFailures() {
		calls := 0
		override(t, &lockCaptureStatus, failingCaptureLock(t, filelock.Shared, failure.err, &calls))
		if !server.captureActive() || calls != 1 {
			t.Fatalf("status lock failure %s was reported idle", failure.name)
		}
	}
	override(t, &lockCaptureStatus, filelock.Lock)
	if server.captureActive() {
		t.Fatal("an idle capture lock was reported active")
	}
	// The probe's own shared lock ends with it.
	if session, err = beginCapture(config); err != nil {
		t.Fatalf("status probe left the capture lock held: %v", err)
	}
	session.Close()
	// This service's own running capture needs no probe, and a lock file
	// that cannot be opened is no capture.
	probes := 0
	override(t, &lockCaptureStatus, failingCaptureLock(t, filelock.Shared, errors.New("probed"), &probes))
	run := server.captures.start(config.Sources)
	if !server.captureActive() || probes != 0 {
		t.Fatalf("a running capture: probed %d times", probes)
	}
	server.captures.mu.Lock()
	run.State = "complete"
	server.captures.mu.Unlock()
	if err := os.Remove(filepath.Join(config.CaptureRoot, currentHost().ID, captureLockName)); err != nil {
		t.Fatal(err)
	}
	if server.captureActive() || probes != 0 {
		t.Fatal("a missing capture lock was reported active")
	}
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

	// The compression pass sweeps abandoned leases under every host's
	// captures (decoded copies a crashed reindex of another Mac left), keeps
	// held ones, and leaves preservation-pin readers to their capture.
	root := t.TempDir()
	other := filepath.Join(root, "host-b", "conductor")
	if err = os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	held, release, err := openReaderLease(other)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	abandoned, abandonedLock, err := lockedLease(other)
	if err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(abandoned, "history", "decoded.db"), "decoded bytes")
	abandonedLock.Close()
	pin := filepath.Join(other, ".reader-pin-x"+captureTempSuffix)
	writeSourceFile(t, filepath.Join(pin, ".reader.lock"), "")
	sweepAbandonedLeases(root)
	if _, err = os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatal("abandoned lease of another host survived the sweep", err)
	}
	for _, kept := range []string{held, pin} {
		if _, err = os.Stat(kept); err != nil {
			t.Fatal("sweep removed a lease it must keep", kept, err)
		}
	}

	// A lease staged before its flock is held has an hour's grace; an older
	// one nobody holds was abandoned between its creation and its lock.
	staged := func(age time.Duration) string {
		t.Helper()
		lease, err := os.MkdirTemp(other, leaseStagingPrefix+"*"+captureTempSuffix)
		if err != nil {
			t.Fatal(err)
		}
		writeSourceFile(t, filepath.Join(lease, "partial.db"), "staged bytes")
		at := time.Now().Add(-age)
		if err := os.Chtimes(lease, at, at); err != nil {
			t.Fatal(err)
		}
		return lease
	}
	fresh, stale, staleHeld := staged(time.Minute), staged(2*time.Hour), staged(2*time.Hour)
	staleLock, err := os.OpenFile(filepath.Join(staleHeld, ".reader.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer staleLock.Close()
	// Raw flock, as an older Pharos or the native exporter takes it.
	if err = syscall.Flock(int(staleLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleHeld, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	sweepAbandonedLeases(root)
	if _, err = os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("an abandoned staged lease survived its grace", err)
	}
	for _, kept := range []string{fresh, staleHeld, held} {
		if _, err = os.Stat(kept); err != nil {
			t.Fatal("sweep removed a staged or held lease", kept, err)
		}
	}
	staleLock.Close()
	if err = os.RemoveAll(fresh); err != nil {
		t.Fatal(err)
	}

	// Only a successful probe shows a reader is gone. Contention, however it
	// is wrapped, and every failure to test the lock keep each candidate of
	// both scavengers, preservation readers included.
	for _, failure := range captureLockFailures() {
		t.Run("probe-"+failure.name, func(t *testing.T) {
			local := t.TempDir()
			abandoned := filepath.Join(local, ".reader-gone"+captureTempSuffix)
			pinned := filepath.Join(local, ".reader-pin-gone"+captureTempSuffix)
			for _, lease := range []string{abandoned, pinned} {
				writeSourceFile(t, filepath.Join(lease, ".reader.lock"), "")
			}
			swept := filepath.Join(root, "host-c", "conductor")
			sweptLease := filepath.Join(swept, ".reader-gone"+captureTempSuffix)
			sweptStaged := filepath.Join(swept, leaseStagingPrefix+"gone"+captureTempSuffix)
			for _, lease := range []string{sweptLease, sweptStaged} {
				writeSourceFile(t, filepath.Join(lease, ".reader.lock"), "")
			}
			old := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(sweptStaged, old, old); err != nil {
				t.Fatal(err)
			}
			probes := 0
			override(t, &probeReaderLease, failingCaptureLock(t, filelock.Exclusive, failure.err, &probes))
			if active, err := preservationReaderActive(local, filepath.Join(filepath.Base(pinned), ".reader.lock")); failure.contended && (!active || err != nil) || !failure.contended && (active || !errors.Is(err, failure.err)) {
				t.Fatalf("preservation reader liveness: active=%v err=%v", active, err)
			}
			removeCaptureTemporaries(local)
			sweepAbandonedLeases(root)
			if probes < 4 {
				t.Fatalf("only %d leases were probed", probes)
			}
			for _, kept := range []string{abandoned, pinned, sweptLease, sweptStaged} {
				if _, err := os.Stat(filepath.Join(kept, ".reader.lock")); err != nil {
					t.Fatal("a scavenger removed a lease it could not prove abandoned", kept, err)
				}
			}
			if err := os.RemoveAll(filepath.Join(root, "host-c")); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A preservation reader whose lease is gone is not live; one whose lease
	// a reader holds is.
	if active, err := preservationReaderActive(t.TempDir(), ".reader-pin-x"+captureTempSuffix+"/.reader.lock"); active || err != nil {
		t.Fatalf("missing preservation reader lease: %v, %v", active, err)
	}
	pinHeld, closePin := heldPreservationReader(t, other, "held")
	if active, err := preservationReaderActive(other, filepath.Join(filepath.Base(pinHeld), ".reader.lock")); !active || err != nil {
		t.Fatalf("held preservation reader lease: %v, %v", active, err)
	}
	closePin()
	if active, err := preservationReaderActive(other, filepath.Join(filepath.Base(pinHeld), ".reader.lock")); active || err != nil {
		t.Fatalf("closed preservation reader lease: %v, %v", active, err)
	}

	// A reader that cannot take its new lease publishes neither the lease
	// nor pin metadata, and removes only what it just made.
	for _, failure := range captureLockFailures() {
		t.Run("acquire-"+failure.name, func(t *testing.T) {
			dir := t.TempDir()
			pin, _ := heldPreservationReader(t, dir, "held")
			lease, release, err := openReaderLease(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			var files []*os.File
			override(t, &lockReaderLease, func(file *os.File, mode filelock.Mode, wait filelock.WaitMode) error {
				files = append(files, file)
				return failingCaptureLock(t, filelock.Exclusive, failure.err, nil)(file, mode, wait)
			})
			c := &sourceCapture{ctx: t.Context(), dir: dir, session: &captureSession{host: Host{ID: "synthetic"}}, manifest: &captureManifest{}}
			if closeReader, err := c.pinReader(nil); closeReader != nil || !errors.Is(err, failure.err) {
				t.Fatalf("pin reader without its lease: %v", err)
			}
			if lease, lock, err := lockedLease(dir); lease != "" || lock != nil || !errors.Is(err, failure.err) {
				t.Fatalf("reader lease without its lock: %q, %v", lease, err)
			}
			if _, release, err := openReaderLease(dir); release != nil || !errors.Is(err, failure.err) {
				t.Fatalf("opened reader lease without its lock: %v", err)
			}
			if len(files) != 3 || slices.ContainsFunc(files, func(file *os.File) bool { return file.Fd() != ^uintptr(0) }) {
				t.Fatalf("a failed reader lease was left open: %d", len(files))
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			if want := []string{filepath.Base(lease), filepath.Base(pin)}; !slices.Equal(names, want) && !slices.Equal(names, []string{want[1], want[0]}) {
				t.Fatalf("after failed reader leases: %v, want only %v", names, want)
			}
		})
	}
}

// TestCrashedReaderLeaseHelperProcess is the reader of
// TestCaptureSweepsALeaseOnlyAfterItsReaderCrashes: it takes a reader lease,
// reports it and holds it until it is killed. It blocks reading stdin, which
// the parent keeps open, rather than in a way Go's deadlock detector could
// end; a parent that exits closes it, ending the helper too.
func TestCrashedReaderLeaseHelperProcess(t *testing.T) {
	dir := os.Getenv("PHAROS_TEST_READER_LEASE_DIR")
	if dir == "" {
		t.Skip("helper process")
	}
	lease, lock, err := lockedLease(dir)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	writeSourceFile(t, filepath.Join(lease, "history", "decoded.db"), "decoded bytes")
	fmt.Println("lease", lease)
	_, _ = io.Copy(io.Discard, os.Stdin)
	// The lease is the open lock file: it must not be collected, and so
	// unlocked by its finalizer, while the helper is meant to hold it.
	runtime.KeepAlive(lock)
	os.Exit(1)
}

// A reader lease in another process survives every sweep while that
// process lives, and is swept once it is killed without releasing it.
func TestCaptureSweepsALeaseOnlyAfterItsReaderCrashes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "host-b", "conductor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestCrashedReaderLeaseHelperProcess$")
	helper.Env = append(os.Environ(), "PHAROS_TEST_READER_LEASE_DIR="+dir)
	output, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Held open, unwritten, until the helper is killed.
	input, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var stderr strings.Builder
	helper.Stderr = &stderr
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- helper.Wait() }()
	killed := false
	defer func() {
		if !killed {
			_ = helper.Process.Kill()
			<-exited
		}
	}()
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "lease ") || strings.HasPrefix(line, "error ") {
				lines <- line
				return
			}
		}
		close(lines)
	}()
	var lease string
	select {
	case line, ok := <-lines:
		if !ok || !strings.HasPrefix(line, "lease ") {
			t.Fatalf("helper took no lease: %q %s", line, stderr.String())
		}
		lease = strings.TrimPrefix(line, "lease ")
	case <-time.After(20 * time.Second):
		t.Fatalf("helper took no lease: %s", stderr.String())
	}
	sweepAbandonedLeases(root)
	removeCaptureTemporaries(dir)
	if _, err := os.Stat(filepath.Join(lease, "history", "decoded.db")); err != nil {
		t.Fatal("a live reader's lease was swept", err)
	}
	killed = true
	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-exited
	sweepAbandonedLeases(root)
	if _, err := os.Stat(lease); !os.IsNotExist(err) {
		t.Fatal("a crashed reader's lease survived the sweep", err)
	}
}

// captureLockFailures are lock results each capture lock policy must tell
// apart: contention as filelock reports it, joined with its errno and
// possibly wrapped, and failures that are not contention, including a bare
// errno without the contention sentinel.
func captureLockFailures() []struct {
	name      string
	err       error
	contended bool
} {
	return []struct {
		name      string
		err       error
		contended bool
	}{
		{"contended", errors.Join(filelock.ErrContended, syscall.EWOULDBLOCK), true},
		{"wrapped-contended", fmt.Errorf("lock: %w", errors.Join(filelock.ErrContended, syscall.EAGAIN)), true},
		{"unsupported", filelock.ErrUnsupported, false},
		{"permission", fmt.Errorf("lock: %w", syscall.EACCES), false},
		{"io", syscall.EIO, false},
		{"no-locks", syscall.ENOLCK, false},
		{"interrupted", syscall.EINTR, false},
		{"bare-errno", syscall.EWOULDBLOCK, false},
	}
}

// failingCaptureLock fails every acquisition with failure, after checking
// it is a nonblocking acquisition of mode on an open file, and counts calls.
func failingCaptureLock(t *testing.T, mode filelock.Mode, failure error, calls *int) func(*os.File, filelock.Mode, filelock.WaitMode) error {
	return func(file *os.File, got filelock.Mode, wait filelock.WaitMode) error {
		if got != mode || wait != filelock.Try || file == nil || file.Fd() == ^uintptr(0) {
			t.Errorf("lock %v: mode %v, wait %v", file, got, wait)
		}
		if calls != nil {
			*calls++
		}
		return failure
	}
}

// heldPreservationReader makes a preservation reader lease in dir that this
// test holds until the returned close.
func heldPreservationReader(t *testing.T, dir, name string) (string, func()) {
	t.Helper()
	lease := filepath.Join(dir, ".reader-pin-"+name+captureTempSuffix)
	if err := os.MkdirAll(lease, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(lease, ".reader.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	if err := filelock.Lock(lock, filelock.Exclusive, filelock.Try); err != nil {
		t.Fatal(err)
	}
	return lease, func() { lock.Close() }
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

// compressGenerations runs one compression pass over this Mac's captures.
func compressGenerations(t *testing.T, config Config) []compressedGeneration {
	t.Helper()
	var done []compressedGeneration
	if err := compressCaptures(t.Context(), config, nil, func(generation compressedGeneration) { done = append(done, generation) }); err != nil {
		t.Fatal(err)
	}
	return done
}

// zstdOnDisk reports whether a file holds a zstd frame rather than its
// original bytes.
func zstdOnDisk(t *testing.T, file string) bool {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.HasPrefix(string(data), string(zstdMagic))
}

// compressionFixture is a Conductor capture with one superseded generation
// holding a session deleted at the source ("old", 2 messages) beside the
// latest snapshot ("latest", 1 message).
func compressionFixture(t *testing.T) (Config, captureTarget) {
	t.Helper()
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	exec(addConductorSession("old", 2)...)
	runCapture(t, config)
	exec("DELETE FROM session_messages", "DELETE FROM sessions")
	exec(addConductorSession("latest", 1)...)
	runCapture(t, config)
	if len(readCaptureManifest(t, config, "conductor").Snapshots[0].Generations) != 1 {
		t.Fatal("fixture needs one retained generation")
	}
	return config, captureTarget{Host: currentHost(), Source: "conductor", Dir: capturedPath(config, "conductor", "")}
}

func temporariesUnder(t *testing.T, dir string) []string {
	t.Helper()
	found := []string{}
	_ = filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(entry.Name(), captureTempSuffix) {
			found = append(found, file)
		}
		return nil
	})
	return found
}

func TestCaptureCompressesIndexedUnpinnedGenerations(t *testing.T) {
	config, target := compressionFixture(t)
	manifest := readCaptureManifest(t, config, "conductor")
	snapshot := manifest.Snapshots[0]
	generation := snapshot.Generations[0]
	file := capturedPath(config, "conductor", generation.Captured)

	// The index still needs a generation its marker lacks.
	marker := loadIndexMarker(target.Dir)
	marker.add(snapshot.Path, snapshot.CapturedAt)
	if err := marker.save(target.Dir, &manifest); err != nil {
		t.Fatal(err)
	}
	if done := compressGenerations(t, config); len(done) != 0 {
		t.Fatal("compressed a generation the index has not read", done)
	}
	marker = loadIndexMarker(target.Dir)
	marker.add(snapshot.Path, generation.CapturedAt)
	if err := marker.save(target.Dir, &manifest); err != nil {
		t.Fatal(err)
	}
	// A pin holds exact bytes.
	lock, err := lockCaptureTarget(t.Context(), target.Dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := sourceCaptureForTarget(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.pinVersion("job", "test", generation); err != nil {
		t.Fatal(err)
	}
	releaseLockFile(lock)
	if done := compressGenerations(t, config); len(done) != 0 {
		t.Fatal("compressed a pinned generation", done)
	}
	if err = releasePreservationOwner(target.Dir, "job"); err != nil {
		t.Fatal(err)
	}
	// A reader leased before compression keeps the original bytes.
	input, err := leaseCaptureForIndex(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	leased := filepath.Join(captureViewOf(input.Adapter).dir, filepath.FromSlash(generation.Captured))
	before := measureCaptures(t.Context(), config.CaptureRoot)
	plainManifest := readText(t, filepath.Join(target.Dir, captureManifestName))

	done := compressGenerations(t, config)
	if len(done) != 1 || done[0].Path != generation.Captured || done[0].Stored >= generation.Size {
		t.Fatalf("compression pass: %+v", done)
	}
	after := readCaptureManifest(t, config, "conductor")
	encoded := after.Snapshots[0].Generations[0]
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != captureGenerationsVersion || encoded.Encoding != captureEncodingZstd || encoded.StoredSize != info.Size() || encoded.Size != generation.Size || encoded.Captured != generation.Captured || !zstdOnDisk(t, file) {
		t.Fatalf("encoded generation: version %d, %+v (%d bytes on disk)", after.Version, encoded, info.Size())
	}
	if sum, size, err := fileSHA256(leased); err != nil || size != generation.Size || sum != encoded.SHA256 || zstdOnDisk(t, leased) {
		t.Fatal("a reader's lease stopped reading the original bytes", err)
	}
	// A lock-free reader that read the manifest before the compression still
	// reads the original bytes from the file under its stale, plain entry.
	if f, err := os.Open(file); err != nil {
		t.Fatal(err)
	} else {
		stale := storedAsUnlocked(f, generation)
		sum, size, err := capturedSHA256File(t.Context(), f, stale.Encoding)
		f.Close()
		if stale.Encoding != captureEncodingZstd || err != nil || size != generation.Size || sum != encoded.SHA256 {
			t.Fatal("a stale plain entry read the compressed file as plain", stale, err)
		}
	}
	// The same pair in a whole tree: a live tree validates (a compression may
	// land while it is read); a staged copy (recovery, transfer) is torn, and
	// is refused rather than published to readers that trust the entry.
	encodedManifest := readText(t, filepath.Join(target.Dir, captureManifestName))
	if err := os.WriteFile(filepath.Join(target.Dir, captureManifestName), []byte(plainManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal("live validation refused a compression that landed after its manifest read", err)
	}
	if err := validateStagedCaptureEvidence(t.Context(), config.CaptureRoot); err == nil || !strings.Contains(err.Error(), "stored compressed but its manifest records it uncompressed") {
		t.Fatal("staged validation accepted a torn copy", err)
	}
	if err := os.WriteFile(filepath.Join(target.Dir, captureManifestName), []byte(encodedManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}
	// The Storage section's history is what is on disk: the encoded size.
	history := func(sizes []captureSize) int64 {
		for _, size := range sizes {
			if size.Source == "conductor" {
				return size.History
			}
		}
		return -1
	}
	if history(before) != generation.Size || history(measureCaptures(t.Context(), config.CaptureRoot)) != encoded.StoredSize {
		t.Fatalf("storage did not shrink: %+v", measureCaptures(t.Context(), config.CaptureRoot))
	}
	if left := temporariesUnder(t, target.Dir); len(left) != 1 { // only the reader's lease
		t.Fatal("compression left temporaries", left)
	}
	if done := compressGenerations(t, config); len(done) != 0 {
		t.Fatal("compressed a generation twice", done)
	}
	// A build that cannot decode refuses the capture instead of misreading it.
	override(t, &captureManifestUnderstood, capturePlainManifestVersion)
	if _, err := loadCaptureManifest(target.Dir); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("has version %d; this build understands up to %d", captureGenerationsVersion, capturePlainManifestVersion)) {
		t.Fatal("an older build accepted an encoded capture", err)
	}
	captureManifestUnderstood = captureManifestVersion
	// Retention prunes the encoded generation by name; with no encoded entry
	// left, the manifest is readable by older builds again.
	db := openWritableSQLite(t, filepath.Join(config.Sources[0].Path, "conductor.db"))
	execSQL(t, db, "DELETE FROM sessions")
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	rotated := readCaptureManifest(t, config, "conductor")
	if rotated.Version != capturePlainManifestVersion || len(rotated.Snapshots[0].Generations) != 1 || rotated.Snapshots[0].Generations[0].Encoding != "" {
		t.Fatalf("after rotation: version %d, %+v", rotated.Version, rotated.Snapshots[0].Generations)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("indexed encoded generation was not pruned", err)
	}
}

// A kill at each step of the compression order leaves a capture that
// validates and indexes, and the next pass finishes the job.
func TestCaptureCompressionSurvivesAKillAtEachStep(t *testing.T) {
	for _, stage := range []string{"compress-encode", "compress-verify", "compress-manifest", "compress-rename"} {
		t.Run(stage, func(t *testing.T) {
			config, target := compressionFixture(t)
			catalog, _ := testCatalog(t)
			generation := readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0]
			override(t, &captureFault, func(at, rel string) error {
				if at == stage {
					panic("killed")
				}
				return nil
			})
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("the injected kill did not happen")
					}
				}()
				compressGenerations(t, config)
			}()
			captureFault = nil
			// Whenever the name may hold zstd (once the manifest committed),
			// an older build refuses the capture, also after a capture runs
			// before the next pass.
			gate := func() {
				t.Helper()
				committed := readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0].Encoding != ""
				if zstdOnDisk(t, capturedPath(config, "conductor", generation.Captured)) && !committed {
					t.Fatal("the name holds zstd under a plain entry")
				}
				captureManifestUnderstood = capturePlainManifestVersion
				_, err := loadCaptureManifest(target.Dir)
				captureManifestUnderstood = captureManifestVersion
				if committed != (err != nil) {
					t.Fatalf("older build after a kill at %s: committed %v, load error %v", stage, committed, err)
				}
			}
			gate()
			if stage == "compress-rename" {
				if run := runCapture(t, config); !run.OK {
					t.Fatal(run)
				}
				gate()
			}
			if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
				t.Fatal("killed compression left an invalid capture", err)
			}
			result := catalog.IndexCapture(t.Context(), target, nil)
			if result.Error != nil || len(result.UnavailableInputs) != 0 || conversationMessages(t, catalog, "conductor:old") != 2 || conversationMessages(t, catalog, "conductor:latest") != 1 {
				t.Fatalf("index after a killed compression: %+v", result)
			}
			if stage == "compress-encode" || stage == "compress-verify" {
				// The next capture sweeps the half-done encoding, never adopting it.
				if left := temporariesUnder(t, target.Dir); len(left) != 1 {
					t.Fatal("expected the killed encoding's temporary", left)
				}
				if run := runCapture(t, config); !run.OK {
					t.Fatal(run)
				}
				if left := temporariesUnder(t, target.Dir); len(left) != 0 || jsonText(readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0]) != jsonText(generation) {
					t.Fatal("capture adopted or kept a killed encoding", left)
				}
			}
			compressGenerations(t, config)
			encoded := readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0]
			if encoded.Encoding != captureEncodingZstd || encoded.Size != generation.Size || encoded.SHA256 == "" || !zstdOnDisk(t, capturedPath(config, "conductor", generation.Captured)) {
				t.Fatalf("next pass did not finish: %+v", encoded)
			}
			if left := temporariesUnder(t, target.Dir); len(left) != 0 {
				t.Fatal("temporaries left", left)
			}
			if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A cold latest file whose compression was killed between its manifest
	// and its rename, then rewritten at the source and captured before any
	// pass corrected the entry: the capture still sees its copy as intact, so
	// the previous version is preserved rather than lost.
	t.Run("cold file rewritten after a kill before the rename", func(t *testing.T) {
		config, catalog, _, claude := coldFilesFixture(t)
		before := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/cold-a.jsonl")
		override(t, &captureFault, func(at, rel string) error {
			if at == "compress-rename" && rel == "files/p/cold-a.jsonl" {
				panic("killed")
			}
			return nil
		})
		func() {
			defer func() { _ = recover() }()
			compressColdFiles(t, config, catalog)
		}()
		captureFault = nil
		if entry := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/cold-a.jsonl"); entry.Encoding != captureEncodingZstd || zstdOnDisk(t, capturedPath(config, "claude", entry.Captured)) {
			t.Fatal("fixture: expected an entry marked encoded over the original bytes", entry)
		}
		writeSourceFile(t, filepath.Join(claude, "p", "cold-a.jsonl"), claudeLine("cold-a", "cold-a-rewritten", "rewritten", "/proj", 59))
		if run := runCapture(t, config); !run.OK {
			t.Fatal(run)
		}
		after := manifestFile(t, readCaptureManifest(t, config, "claude"), "files/p/cold-a.jsonl")
		if after.Encoding != "" || len(after.Previous) != 1 || after.Previous[0].Size != before.Size {
			t.Fatalf("rewrite after a killed compression: %+v", after)
		}
		previous := capturedPath(config, "claude", after.Previous[0].Captured)
		if sum, size, err := fileSHA256(previous); err != nil || size != before.Size || sum != before.SHA256 {
			t.Fatal("the previous version was not preserved", err)
		}
		if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
			t.Fatal(err)
		}
	})
}

// A pass remembers what it decided not to compress, so it does not read the
// same large generation again every pass; one generation's problem does not
// keep the others uncompressed; and a capture that starts mid-encode leaves
// the encoding alone.
func TestCaptureCompressionRemembersWhatItLeft(t *testing.T) {
	useHost(t, "host-a")
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true})
	config.CaptureGenerations = 3
	for _, id := range []string{"a", "b", "c", "d"} {
		exec(addConductorSession(id, 1)...)
		runCapture(t, config)
	}
	target := captureTarget{Host: currentHost(), Source: "conductor", Dir: capturedPath(config, "conductor", "")}
	manifest, err := loadCaptureManifest(target.Dir)
	if err != nil || len(manifest.Snapshots[0].Generations) != 3 {
		t.Fatal("fixture needs three generations", err)
	}
	generations := manifest.Snapshots[0].Generations
	// The bytes of one disagree with its recorded digest: evidence trouble.
	manifest.Snapshots[0].Generations[0].SHA256 = strings.Repeat("0", 64)
	if err = writeCaptureManifest(target.Dir, manifest); err != nil {
		t.Fatal(err)
	}
	// Another does not shrink (stand-in bytes of the recorded size).
	noise := make([]byte, generations[1].Size)
	if _, err = rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(capturedPath(config, "conductor", generations[1].Captured), noise, 0o600); err != nil {
		t.Fatal(err)
	}
	encodes := 0
	override(t, &captureFault, func(stage, rel string) error {
		if stage == "compress-encode" {
			encodes++
			// A capture starting now does not sweep the encoding in progress.
			if run := runCapture(t, config); !run.OK {
				t.Fatal(run)
			}
		}
		return nil
	})
	var done []compressedGeneration
	err = compressCaptures(t.Context(), config, nil, func(g compressedGeneration) { done = append(done, g) })
	if err == nil || !strings.Contains(err.Error(), "does not match its manifest digest") || len(done) != 1 || done[0].Path != generations[2].Captured || encodes != 3 {
		t.Fatalf("first pass: %v, compressed %+v, %d encodes", err, done, encodes)
	}
	outcomes := loadCompressionOutcomes(target.Dir)
	if outcomes.Entries[generations[0].Captured].Outcome != "mismatch" || outcomes.Entries[generations[1].Captured].Outcome != "incompressible" {
		t.Fatalf("outcomes: %+v", outcomes.Entries)
	}
	done = nil
	if err = compressCaptures(t.Context(), config, nil, func(g compressedGeneration) { done = append(done, g) }); err != nil || len(done) != 0 || encodes != 3 {
		t.Fatalf("second pass read settled generations again: %v, %+v, %d encodes", err, done, encodes)
	}
}

// A SQLite-record generation waits for its export receipt without being read
// again each pass, while a receipt that does not verify (perhaps a passing
// failure) is retried a bounded number of times.
func TestCaptureCompressionWaitsForVerifiedRecordExports(t *testing.T) {
	setup := func(t *testing.T) (Config, capturedVersion, string) {
		t.Helper()
		useHost(t, "host-a")
		dir := filepath.Join(t.TempDir(), "conductor")
		exec := conductorFixture(t, dir)
		config := captureTestConfig(t, SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}})
		exec(addConductorSession("a", 1)...)
		runCapture(t, config)
		exec(addConductorSession("b", 1)...)
		runCapture(t, config)
		generation := readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0]
		receipt := capturedPath(config, "conductor", filepath.Join(recordDir, "receipts", hashBytes([]byte(generation.Captured))+".json"))
		if _, err := os.Stat(receipt); err != nil {
			t.Fatal("fixture generation has no export receipt", err)
		}
		return config, generation, receipt
	}
	pass := func(t *testing.T, config Config) (int, error) {
		t.Helper()
		compressed := 0
		err := compressCaptures(t.Context(), config, nil, func(compressedGeneration) { compressed++ })
		return compressed, err
	}
	t.Run("no receipt yet", func(t *testing.T) {
		config, generation, receipt := setup(t)
		valid := readText(t, receipt)
		if err := os.Remove(receipt); err != nil {
			t.Fatal(err)
		}
		if n, err := pass(t, config); n != 0 || err != nil {
			t.Fatal("generation without a receipt:", n, err)
		}
		target := capturedPath(config, "conductor", "")
		if !loadCompressionOutcomes(target).settled(target, generation) {
			t.Fatal("a generation awaiting its receipt is read again every pass")
		}
		writeSourceFile(t, receipt, valid)
		if n, err := pass(t, config); n != 1 || err != nil {
			t.Fatal("generation whose receipt appeared:", n, err)
		}
	})
	t.Run("receipt that does not verify", func(t *testing.T) {
		config, generation, receipt := setup(t)
		valid := readText(t, receipt)
		writeSourceFile(t, receipt, `{"version":1}`)
		target := capturedPath(config, "conductor", "")
		defer func() {
			if t.Failed() {
				return
			}
			// A new receipt that verifies is a new chance.
			writeSourceFile(t, receipt, valid)
			if n, err := pass(t, config); n != 1 || err != nil {
				t.Fatal("generation whose receipt was replaced by one that verifies:", n, err)
			}
		}()
		for attempt := 1; attempt <= captureCompressAttempts; attempt++ {
			if n, err := pass(t, config); n != 0 || err == nil || !strings.Contains(err.Error(), "does not verify") {
				t.Fatal("attempt", attempt, n, err)
			}
			if got := loadCompressionOutcomes(target).Entries[generation.Captured]; got.Outcome != "failed" || got.Failures != attempt {
				t.Fatalf("attempt %d recorded %+v", attempt, got)
			}
		}
		if n, err := pass(t, config); n != 0 || err != nil {
			t.Fatal("unverifiable receipt retried past its attempts:", n, err)
		}
		// A different receipt within the same mtime second (HFS+) is still a
		// new receipt: identified by content, not mtime.
		info, err := os.Stat(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(receipt, []byte(`{"version":1,"input":"other"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.Chtimes(receipt, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if n, err := pass(t, config); n != 0 || err == nil {
			t.Fatal("a replaced receipt with the same mtime was not tried again:", n, err)
		}
	})
}

// coldFilesFixture is a Claude capture, seen unchanged by a second capture
// and indexed, whose transcripts are cold except recent.jsonl.
func coldFilesFixture(t *testing.T) (Config, *Catalog, captureTarget, string) {
	t.Helper()
	useHost(t, "host-a")
	override(t, &compressMinBytes, 1)
	catalog, _ := testCatalog(t)
	claude := filepath.Join(t.TempDir(), "claude")
	for index, name := range []string{"cold-a", "cold-b"} {
		lines := ""
		for line := range 40 {
			lines += claudeLine(name, fmt.Sprintf("%s-%d", name, line), strings.Repeat("compressible text ", 20), "/proj", index*40+line%60)
		}
		writeSourceFile(t, filepath.Join(claude, "p", name+".jsonl"), lines)
	}
	writeSourceFile(t, filepath.Join(claude, "p", "recent.jsonl"), claudeLine("recent", "recent-1", strings.Repeat("recent text ", 20), "/proj", 1))
	recent := time.Now()
	if err := os.Chtimes(filepath.Join(claude, "p", "recent.jsonl"), recent, recent); err != nil {
		t.Fatal(err)
	}
	writeSourceFile(t, filepath.Join(claude, "p", "sessions-index.json"), `{"entries":[]}`)
	config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: claude, Account: "local", Enabled: true})
	runCapture(t, config)
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	// The second capture finds the transcripts unchanged.
	runCapture(t, config)
	return config, catalog, captureTarget{Host: currentHost(), Source: "claude", Dir: capturedPath(config, "claude", "")}, claude
}

func compressColdFiles(t *testing.T, config Config, catalog *Catalog) []string {
	t.Helper()
	compressed := []string{}
	if err := compressCaptures(t.Context(), config, catalog, func(g compressedGeneration) { compressed = append(compressed, g.Path) }); err != nil {
		t.Fatal(err)
	}
	sort.Strings(compressed)
	return compressed
}

func TestCaptureCompressesColdFiles(t *testing.T) {
	config, catalog, target, claude := coldFilesFixture(t)
	plan, err := planCapture(context.Background(), config.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := func() string {
		t.Helper()
		c, err := sourceCaptureForTarget(t.Context(), target)
		if err != nil {
			t.Fatal(err)
		}
		value, err := captureDeliveryFingerprint(t.Context(), config.Sources[0], plan, c)
		if err != nil || value == "" {
			t.Fatal("delivery fingerprint", value, err)
		}
		return value
	}
	delivered := fingerprint()
	// The source fingerprint read through the capture matches the live
	// source's, before and after compression.
	sourceFingerprint := func() string {
		t.Helper()
		adapter, _, _, err := openCaptureAdapter(target)
		if err != nil {
			t.Fatal(err)
		}
		value, err := adapter.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	live, err := (&jsonlAdapter{baseAdapter: baseAdapter{config: config.Sources[0]}, provider: "claude"}).Fingerprint()
	if err != nil || sourceFingerprint() != live {
		t.Fatal("capture fingerprint differs from the live source's before compression", err)
	}

	// Off, or without the index's record, nothing latest is compressed.
	off := config
	off.CaptureCompressAfterDays = 0
	if done := compressColdFiles(t, off, catalog); len(done) != 0 {
		t.Fatal("compressed latest files with capture_compress_after_days = 0", done)
	}
	if done := compressColdFiles(t, config, nil); len(done) != 0 {
		t.Fatal("compressed latest files the index has not recorded", done)
	}
	// After an extractor change the next index parses every part again, so
	// nothing is cold until it has.
	if err := catalog.advanceSyncGeneration(t.Context(), "host-a", "claude"); err != nil {
		t.Fatal(err)
	}
	if done := compressColdFiles(t, config, catalog); len(done) != 0 {
		t.Fatal("compressed latest files recorded by an earlier extractor", done)
	}
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	// Another Mac's index of this capture records the same parts with
	// "@offhost": the same bytes, so they count as indexed too.
	if _, err := catalog.DB.Exec(`UPDATE source_item_states SET extractor=replace(extractor,'@generation:','@offhost@generation:') WHERE host_id='host-a' AND source_name='claude'`); err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string][2]string{
		"claude:v17":                      {"claude:v17", "claude:v17@offhost"},
		"claude:v17@offhost@generation:1": {"claude:v17@generation:1", "claude:v17@offhost@generation:1"},
		"claude:v13-names@generation:2":   {"claude:v13-names@generation:2", "claude:v13-names@offhost@generation:2"},
	} {
		if own, offHost := extractorVariants(version); own != want[0] || offHost != want[1] {
			t.Errorf("extractorVariants(%q) = %q, %q", version, own, offHost)
		}
	}
	done := compressColdFiles(t, config, catalog)
	if want := []string{"files/p/cold-a.jsonl", "files/p/cold-b.jsonl"}; jsonText(done) != jsonText(want) {
		t.Fatalf("compressed %v, want %v", done, want)
	}
	manifest := readCaptureManifest(t, config, "claude")
	for _, file := range manifest.Files {
		encoded := file.Encoding == captureEncodingZstd
		if encoded != slices.Contains(done, file.Captured) || encoded && (!zstdOnDisk(t, capturedPath(config, "claude", file.Captured)) || file.StoredSize >= file.Size) {
			t.Fatalf("%s stored as %q (%d of %d bytes)", file.Captured, file.Encoding, file.StoredSize, file.Size)
		}
	}
	if manifest.Version != captureFilesVersion {
		t.Fatal("manifest with encoded files is not the version older builds refuse", manifest.Version)
	}
	// A build that reads only encoded generations (phase 1) refuses it.
	override(t, &captureManifestUnderstood, captureGenerationsVersion)
	_, err = loadCaptureManifest(target.Dir)
	captureManifestUnderstood = captureManifestVersion
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("has version %d; this build understands up to %d", captureFilesVersion, captureGenerationsVersion)) {
		t.Fatal("a generations-only build accepted encoded latest files", err)
	}
	// Delivery fingerprints come from the manifest's original-byte fields.
	if fingerprint() != delivered {
		t.Fatal("compression changed the delivery fingerprint")
	}
	if sourceFingerprint() != live {
		t.Fatal("compression changed the capture's source fingerprint")
	}
	if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}
	// An unchanged source leaves the encoded copies alone: they are intact by
	// their stored size.
	if result := captureResult(t, runCapture(t, config), "claude"); result.FilesCopied != 0 || result.FilesUnchanged != 4 {
		t.Fatalf("capture over unchanged cold files: %+v", result)
	}
	// A cold file that changes again is recopied in full and uncompressed;
	// a rewritten one keeps its encoded copy in history.
	appendSourceFile(t, filepath.Join(claude, "p", "cold-a.jsonl"), claudeLine("cold-a", "cold-a-new", "more", "/proj", 59))
	writeSourceFile(t, filepath.Join(claude, "p", "cold-b.jsonl"), claudeLine("cold-b", "cold-b-new", "rewritten", "/proj", 59))
	writeSourceFile(t, filepath.Join(claude, "p", "recent.jsonl"), claudeLine("recent", "recent-2", strings.Repeat("rewritten recent ", 20), "/proj", 2))
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	manifest = readCaptureManifest(t, config, "claude")
	a, b, recent := manifestFile(t, manifest, "files/p/cold-a.jsonl"), manifestFile(t, manifest, "files/p/cold-b.jsonl"), manifestFile(t, manifest, "files/p/recent.jsonl")
	if a.Encoding != "" || b.Encoding != "" || len(a.Previous) != 0 || len(b.Previous) != 1 || b.Previous[0].Encoding != captureEncodingZstd || len(recent.Previous) != 1 || recent.Previous[0].Encoding != "" {
		t.Fatalf("after changes: a %+v, b %+v, recent %+v", a, b, recent)
	}
	if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
		t.Fatal(err)
	}
	// Superseded versions are cold by definition, whatever their age.
	if done := compressColdFiles(t, config, nil); jsonText(done) != jsonText([]string{recent.Previous[0].Captured}) {
		t.Fatal("superseded version compression:", done)
	}
	indexCaptures(t, catalog, config.CaptureRoot, "host-a")
	if conversationMessages(t, catalog, "cold-a") != 41 || conversationMessages(t, catalog, "recent") == 0 {
		t.Fatal("index after cold files changed", conversationMessages(t, catalog, "cold-a"))
	}
}

// Only transcripts the index parses when they themselves change are stored
// encoded; files every plan reads stay plain.
func TestCaptureCompressesOnlyAllowlistedLatestFiles(t *testing.T) {
	for _, test := range []struct {
		kind, rel string
		want      bool
	}{
		{"claude", "files/p/session.jsonl", true},
		{"claude", "files/p/sessions-index.json", false},
		{"codex", "files/sessions/2026/09/01/rollout-2026-09-01T00-00-00-x.jsonl", true},
		{"codex", "files/session_index.jsonl", false},
		{"codex", "files/history.jsonl", false},
		{"antigravity", "files/brain/x/.system_generated/logs/transcript_full.jsonl", true},
		{"antigravity", "files/annotations/x.pbtxt", false},
		{"antigravity", "files/cli-logs/cli-1.log", false},
		{"tl1", "files/transcripts/task/1-claude.jsonl", false},
		{"conductor", "files/conductor.db", false},
		{"claude", "history/20260901T000000.000000000Z/p/session.jsonl", false},
	} {
		if got := compressibleLatest(test.kind, test.rel); got != test.want {
			t.Errorf("%s %s: %v, want %v", test.kind, test.rel, got, test.want)
		}
	}
	// Retained-input sources keep the original inode in their durable
	// revisions, so compressing their latest files would save nothing.
	file := &capturedFile{Path: "/p/a.jsonl", Captured: "files/p/a.jsonl", Size: 1 << 20, MTimeNS: 1, CapturedAt: "2026-09-01T00:00:00Z", MissingSince: "2026-09-02T00:00:00Z"}
	policy := compressPolicy{afterDays: 14, now: time.Now(), indexed: func(string, string, int64, int64) bool { return true }}
	manifest := &captureManifest{Source: captureSourceInfo{Name: "claude", Kind: "claude"}, Files: []*capturedFile{file}}
	if !coldLatest(t.TempDir(), manifest, file, policy) {
		t.Fatal("a cold, indexed transcript was not eligible")
	}
	manifest.RetainedInputCapture = true
	if coldLatest(t.TempDir(), manifest, file, policy) {
		t.Fatal("a retained-input source's latest file was eligible")
	}
}

// addEncodedClaudeFixture adds a Claude source to config whose capture holds
// an encoded latest transcript and an encoded earlier version, compressed as
// a pass would once the index had recorded them. It returns the source.
func addEncodedClaudeFixture(t *testing.T, config *Config) SourceConfig {
	t.Helper()
	override(t, &compressMinBytes, 1)
	claude := filepath.Join(t.TempDir(), "claude")
	lines := ""
	for line := range 40 {
		lines += claudeLine("kept", fmt.Sprintf("kept-%d", line), strings.Repeat("kept text ", 20), "/proj", line)
	}
	writeSourceFile(t, filepath.Join(claude, "p", "kept.jsonl"), lines)
	writeSourceFile(t, filepath.Join(claude, "p", "rewritten.jsonl"), lines)
	source := SourceConfig{Name: "claude", Kind: "claude", Path: claude, Account: "local", Enabled: true}
	config.Sources = append(config.Sources, source)
	only := *config
	only.Sources = []SourceConfig{source}
	runCapture(t, only)
	writeSourceFile(t, filepath.Join(claude, "p", "rewritten.jsonl"), claudeLine("rewritten", "rewritten-1", "new", "/proj", 1))
	runCapture(t, only)
	runCapture(t, only)
	target := captureTarget{Host: currentHost(), Source: "claude", Dir: capturedPath(only, "claude", "")}
	policy := compressPolicy{afterDays: 1, now: time.Now(), indexed: func(string, string, int64, int64) bool { return true }}
	if err := compressSourceFiles(t.Context(), target, policy, nil); err != nil {
		t.Fatal(err)
	}
	manifest := readCaptureManifest(t, only, "claude")
	kept, rewritten := manifestFile(t, manifest, "files/p/kept.jsonl"), manifestFile(t, manifest, "files/p/rewritten.jsonl")
	if kept.Encoding != captureEncodingZstd || len(rewritten.Previous) != 1 || rewritten.Previous[0].Encoding != captureEncodingZstd {
		t.Fatalf("fixture: kept %+v, rewritten %+v", kept, rewritten)
	}
	return source
}

// assertEncodedClaudeFixture checks a copy of that capture under root: the
// entries are still encoded and decode to their recorded bytes.
func assertEncodedClaudeFixture(t *testing.T, root, host string) {
	t.Helper()
	dir := filepath.Join(root, host, "claude")
	manifest, err := loadCaptureManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range manifest.Files {
		for _, v := range append([]capturedVersion{file.version()}, file.Previous...) {
			if v.Encoding == "" {
				continue
			}
			sum, size, err := capturedSHA256(t.Context(), filepath.Join(dir, filepath.FromSlash(v.Captured)), v.Encoding)
			if err != nil || size != v.Size || sum != v.SHA256 {
				t.Fatalf("%s did not round-trip: %v", v.Captured, err)
			}
			checked++
		}
	}
	if checked < 2 {
		t.Fatalf("copy holds %d encoded entries, want at least 2", checked)
	}
}

// A capture's errors keep only the files they name from counting as seen
// unchanged; an error naming no file keeps them all.
func TestCaptureColdFilesNeedACleanLastRun(t *testing.T) {
	policy := compressPolicy{afterDays: 14, now: time.Now(), indexed: func(string, string, int64, int64) bool { return true }}
	file := &capturedFile{Path: "/p/a.jsonl", Captured: "files/p/a.jsonl", Size: 1 << 20, MTimeNS: 1, CapturedAt: "2026-09-01T00:00:00Z"}
	for _, test := range []struct {
		errors []captureError
		want   bool
	}{
		{nil, true},
		{[]captureError{{Path: "/p/other.jsonl", Error: "unreadable"}}, true},
		{[]captureError{{Path: "/p/a.jsonl", Error: "unreadable"}}, false},
		{[]captureError{{Error: "capture drive: device not configured"}}, false},
	} {
		manifest := &captureManifest{Source: captureSourceInfo{Name: "claude", Kind: "claude"}, Files: []*capturedFile{file},
			LastRun: &captureRunRecord{StartedAt: "2026-09-02T00:00:00Z", FinishedAt: "2026-09-02T00:01:00Z", Errors: test.errors}}
		if got := coldLatest(t.TempDir(), manifest, file, policy); got != test.want {
			t.Errorf("last run errors %v: cold %v, want %v", test.errors, got, test.want)
		}
	}
}

// A durable files-only inventory is identified by what it holds: compressing
// an earlier version of a file neither re-identifies nor restamps it.
func TestDurableInputsIgnoreCompressionElsewhere(t *testing.T) {
	useHost(t, "host-a")
	override(t, &compressMinBytes, 1)
	claude := filepath.Join(t.TempDir(), "claude")
	lines := strings.Repeat(claudeLine("s", "u", strings.Repeat("text ", 40), "/proj", 1), 20)
	writeSourceFile(t, filepath.Join(claude, "p", "s.jsonl"), lines)
	source := SourceConfig{Name: "claude", Kind: "claude", Path: claude, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
	config := captureTestConfig(t, source)
	runCapture(t, config)
	writeSourceFile(t, filepath.Join(claude, "p", "s.jsonl"), claudeLine("s", "v", "rewritten", "/proj", 2))
	runCapture(t, config)
	target := captureTarget{Host: currentHost(), Source: "claude", Dir: capturedPath(config, "claude", "")}
	freeze := func() {
		t.Helper()
		lock, err := lockCaptureTarget(t.Context(), target.Dir)
		if err != nil {
			t.Fatal(err)
		}
		defer releaseLockFile(lock)
		input, err := freezeCaptureInput(t.Context(), target, source, true)
		if err != nil {
			t.Fatal(err)
		}
		input.Close()
	}
	freeze()
	if err := compressSourceFiles(t.Context(), target, compressPolicy{}, nil); err != nil {
		t.Fatal(err)
	}
	if manifest := readCaptureManifest(t, config, "claude"); manifest.Version != captureFilesVersion || manifest.Files[0].Previous[0].Encoding != captureEncodingZstd {
		t.Fatal("fixture: the earlier version was not compressed")
	}
	freeze()
	revisions, err := os.ReadDir(filepath.Join(target.Dir, "inputs"))
	if err != nil || len(revisions) != 1 {
		t.Fatalf("compressing an earlier version re-identified the inventory: %d revisions, %v", len(revisions), err)
	}
	inventory, err := loadCaptureManifest(filepath.Join(target.Dir, "inputs", revisions[0].Name()))
	if err != nil || inventory.Version != capturePlainManifestVersion {
		t.Fatal("files-only inventory stamped with another entry's version", inventory.Version, err)
	}
}

// loadManifestForTest loads a capture manifest or fails the test.
func loadManifestForTest(t *testing.T, dir string) *captureManifest {
	t.Helper()
	manifest, err := loadCaptureManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

// Rotation never leaves the bytes it supersedes linked nowhere: a file
// already at the history name is kept only if it holds those bytes (a pin's
// copy); anything else is moved aside, kept as evidence that planning reads
// as an ordinary orphan, and capture keeps working.
func TestCapturePreserveVerifiesWhatIsAlreadyAtTheName(t *testing.T) {
	var encoded bytes.Buffer
	writer, err := zstd.NewWriter(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte(strings.Repeat("not this database ", 50)))
	writer.Close()
	for _, test := range []struct {
		name     string
		junk     func(size int64) []byte
		noLinks  bool
		keepsPin bool
	}{
		{name: "wrong-size junk", junk: func(int64) []byte { return []byte("junk") }},
		{name: "same-size junk", junk: func(size int64) []byte { return make([]byte, size) }},
		{name: "zstd with the wrong digest", junk: func(int64) []byte { return encoded.Bytes() }},
		{name: "wrong-size junk without hard links", junk: func(int64) []byte { return []byte("junk") }, noLinks: true},
		{name: "a pin's copy without hard links", noLinks: true, keepsPin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			useHost(t, "host-a")
			_, config := testCatalog(t)
			config.CaptureRoot = filepath.Join(t.TempDir(), "captures")
			dir := filepath.Join(t.TempDir(), "conductor")
			exec := conductorFixture(t, dir)
			exec(addConductorSession("first", 1)...)
			config.Sources = []SourceConfig{{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}}
			runCapture(t, config)
			latest := readCaptureManifest(t, config, "conductor").Snapshots[0]
			original, size, err := fileSHA256(capturedPath(config, "conductor", latest.Captured))
			if err != nil {
				t.Fatal(err)
			}
			stamp, _ := time.Parse(time.RFC3339Nano, latest.CapturedAt)
			name := path.Join(captureHistoryDir, stamp.UTC().Format(captureStampLayout), "conductor.db")
			at := capturedPath(config, "conductor", name)
			var junk []byte
			if test.keepsPin {
				// A copy made where hard links are unavailable: same bytes,
				// another inode.
				if junk, err = os.ReadFile(capturedPath(config, "conductor", latest.Captured)); err != nil {
					t.Fatal(err)
				}
			} else {
				junk = test.junk(size)
			}
			writeSourceFile(t, at, string(junk))
			before, _ := os.Stat(at)
			if test.noLinks {
				override(t, &historyLinkFile, func(string, string) error { return errors.New("operation not supported") })
			}
			exec(addConductorSession("second", 1)...)
			if run := runCapture(t, config); !run.OK {
				t.Fatal(run)
			}
			generation := readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0]
			if sum, n, err := fileSHA256(at); generation.Captured != name || generation.Encoding != "" || err != nil || n != size || sum != original {
				t.Fatalf("the superseded snapshot did not survive at %s: %+v", name, generation)
			}
			conflict := at + ".conflict-1"
			if test.keepsPin {
				if after, _ := os.Stat(at); !os.SameFile(before, after) {
					t.Fatal("rotation replaced a pin's copy")
				}
				if _, err := os.Stat(conflict); !os.IsNotExist(err) {
					t.Fatal("a matching copy was moved aside", err)
				}
			} else if kept, err := os.ReadFile(conflict); err != nil || string(kept) != string(junk) {
				t.Fatal("what was at the name was not kept aside", err)
			}
			if err := validateCaptureEvidence(t.Context(), config.CaptureRoot); err != nil {
				t.Fatal(err)
			}
			plan, err := planHistory(t.Context(), config, historyScope{Host: "host-a", Source: "conductor"})
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range plan.Units {
				if u.Gap != "" && u.Path != "" {
					t.Fatal("planning found a failing unit", u)
				}
			}
			exec(addConductorSession("third", 1)...)
			if run := runCapture(t, config); !run.OK {
				t.Fatal("a later capture failed", run)
			}
		})
	}
}

func TestCaptureSpaceGuards(t *testing.T) {
	dir := t.TempDir()
	margin := uint64(captureFreeMargin)
	t.Run("a capture write", func(t *testing.T) {
		c := &sourceCapture{dir: dir}
		asked := fakeCapacity(t, map[string]fsinfo.Space{dir: {AvailableBytes: margin + 4096}}, nil)
		if err := c.ensureSpace(4096); err != nil {
			t.Fatal("exactly the margin left:", err)
		}
		var destination destinationError
		if err := c.ensureSpace(4097); !errors.As(err, &destination) {
			t.Fatal(err)
		}
		if err := c.ensureSpace(-1); !errors.As(err, &destination) || !errors.Is(err, fsinfo.ErrRange) {
			t.Fatal(err)
		}
		asked = fakeCapacity(t, nil, map[string]error{dir: syscall.EIO})
		if err := c.ensureSpace(1); !errors.As(err, &destination) || !errors.Is(err, syscall.EIO) {
			t.Fatal("an unknown capacity:", err)
		}
		if err := c.ensureSpace(0); err != nil || len(*asked) != 1 {
			t.Fatal("writing nothing asked for the capacity:", err, *asked)
		}
	})
	t.Run("a compressed file", func(t *testing.T) {
		var out bytes.Buffer
		w := &spaceCheckedWriter{w: &out, dir: dir}
		asked := fakeCapacity(t, map[string]fsinfo.Space{dir: {AvailableBytes: margin + 8}}, nil)
		if n, err := w.Write([]byte("abcd")); n != 4 || err != nil || len(*asked) != 1 {
			t.Fatal("the first write:", n, err, *asked)
		}
		if n, err := w.Write([]byte("efgh")); n != 4 || err != nil {
			t.Fatal(n, err)
		}
		// 64 MiB pending is always checked, and the margin refuses it.
		checks := len(*asked)
		w.pending = 64<<20 - 4
		if _, err := w.Write([]byte("ijkl")); err == nil || len(*asked) != checks+1 || out.String() != "abcdefgh" {
			t.Fatal("a write past the margin:", err, *asked, out.String())
		}
		if _, err := w.Write([]byte("m")); err == nil || out.String() != "abcdefgh" {
			t.Fatal("a write after the refusal:", err, out.String())
		}
		// An unknown capacity refuses the write and every later one,
		// keeping what was pending.
		out.Reset()
		w = &spaceCheckedWriter{w: &out, dir: dir}
		fakeCapacity(t, map[string]fsinfo.Space{dir: {AvailableBytes: 1 << 50}}, map[string]error{dir: syscall.EIO})
		if n, err := w.Write([]byte("abcd")); n != 0 || !errors.Is(err, syscall.EIO) || w.pending != 4 {
			t.Fatal(n, err, w.pending)
		}
		fakeCapacity(t, map[string]fsinfo.Space{dir: {AvailableBytes: 1 << 50}}, nil)
		if n, err := w.Write([]byte("efgh")); n != 0 || !errors.Is(err, syscall.EIO) || out.Len() != 0 {
			t.Fatal("a write after the failed check:", n, err, out.Len())
		}
	})
}

func TestSpaceNeededRefusesWraparound(t *testing.T) {
	for _, c := range []struct {
		parts []int64
		want  uint64
		err   bool
	}{
		{nil, 0, false},
		{[]int64{0, captureFreeMargin}, uint64(captureFreeMargin), false},
		{[]int64{math.MaxInt64, math.MaxInt64}, math.MaxUint64 - 1, false},
		{[]int64{math.MaxInt64, math.MaxInt64, 2}, 0, true},
		{[]int64{5, -1}, 0, true},
	} {
		got, err := spaceNeeded(c.parts...)
		if got != c.want || (err != nil) != c.err || err != nil && !errors.Is(err, fsinfo.ErrRange) {
			t.Errorf("spaceNeeded(%v) = %d, %v", c.parts, got, err)
		}
	}
	if displayBytes(math.MaxUint64) != math.MaxInt64 || displayBytes(7) != 7 {
		t.Fatal("displayBytes")
	}
}
