package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
	"github.com/gbdubs/pharos/internal/platform/fsinfo"
)

// backupFixture is a library with a catalog, captures, a host file and
// preserved data; tests treat dest as another drive.
func backupFixture(t *testing.T) (Config, string) {
	t.Helper()
	useHost(t, "host-a")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config, err := InitLibrary(filepath.Join(root, "Pharos"), func(string) string { return "uuid:TEST-VOLUME" })
	if err != nil {
		t.Fatal(err)
	}
	catalog := reopenCatalog(t, config.CatalogPath)
	if _, err := catalog.DB.Exec("INSERT INTO hosts(id,label,first_seen_at,last_seen_at) VALUES('host-z','Z','2026-01-01','2026-01-01')"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(config.CaptureRoot, "host-a")
	writeSourceFile(t, filepath.Join(host, "claude", "files", "p", "a.jsonl"), "one\n")
	writeSourceFile(t, filepath.Join(host, "claude", "files", "p", "b.jsonl"), strings.Repeat("two\n", 1000))
	writeSourceFile(t, filepath.Join(host, "claude", "manifest.json"), "{}\n")
	writeSourceFile(t, filepath.Join(host, captureLockName), "")
	writeSourceFile(t, filepath.Join(host, "claude", "files", "p", "c.jsonl"+captureTempSuffix), "half-written")
	writeSourceFile(t, filepath.Join(filepath.Dir(config.Path), "hosts", "host-a.toml"), "[[sources]]\nname = \"claude\"\n")
	writeSourceFile(t, filepath.Join(config.ArchiveRoot, "package", "x.json"), "{}")
	dest := filepath.Join(root, "Other Drive", "Pharos Backup")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	stubBackupDrives(t, filepath.Dir(dest), nil)
	return config, dest
}

// stubBackupDrives puts everything under other on another volume and drive;
// facts, if given, adjusts what either side reports.
func stubBackupDrives(t *testing.T, other string, facts func(path string, facts *volumeFacts)) {
	t.Helper()
	previous := backupVolume
	backupVolume = func(path string) (uint64, volumeFacts) {
		device, result := uint64(1), volumeFacts{PhysicalDisk: "disk4", Encryption: "filevault", Spotlight: "disabled", Location: "external"}
		if within(canonical(path), other) {
			device, result.PhysicalDisk = 2, "disk9"
		}
		if facts != nil {
			facts(canonical(path), &result)
		}
		return device, result
	}
	t.Cleanup(func() { backupVolume = previous })
}

// legacyBackupFixture is a per-user install: archive.toml and the catalog in
// a data directory, archive_root wherever archiveRoot puts it.
func legacyBackupFixture(t *testing.T, archiveRoot func(data string) string) (Config, string) {
	t.Helper()
	useHost(t, "mac-a")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "Pharos")
	config := defaultConfig(filepath.Join(data, "archive.toml"))
	config.CatalogPath = filepath.Join(data, "catalog.sqlite3")
	config.ArchiveRoot = archiveRoot(data)
	config.StagingRoot = filepath.Join(data, "staging")
	config.CaptureRoot = filepath.Join(data, "captures")
	writeSourceFile(t, config.Path, "archive_root = \"elsewhere\"\n")
	if err := os.MkdirAll(config.ArchiveRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := openTestCatalog(t, config.CatalogPath).Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "Other Drive", "Pharos Backup")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	stubBackupDrives(t, filepath.Dir(dest), nil)
	return config, dest
}

func backupFileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// checkedLock stands in for one of backup's lock calls: it checks each
// acquisition's mode and wait, keeps the file so the test can check it was
// closed, and returns what result says for that call (1-based).
func checkedLock(t *testing.T, mode filelock.Mode, wait filelock.WaitMode, files *[]*os.File, result func(call int, file *os.File) error) func(*os.File, filelock.Mode, filelock.WaitMode) error {
	return func(file *os.File, gotMode filelock.Mode, gotWait filelock.WaitMode) error {
		if gotMode != mode || gotWait != wait || file == nil || file.Fd() == ^uintptr(0) {
			t.Errorf("lock %v: mode %v, wait %v", file, gotMode, gotWait)
		}
		*files = append(*files, file)
		return result(len(*files), file)
	}
}

func requireLocksClosed(t *testing.T, files []*os.File, want int) {
	t.Helper()
	if len(files) != want {
		t.Fatalf("%d lock attempts, want %d", len(files), want)
	}
	for _, file := range files {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("%s was left open: %v", file.Name(), err)
		}
	}
}

func runBackup(t *testing.T, config Config, dest string, prune bool) BackupRun {
	t.Helper()
	job, err := prepareBackup(config, dest, prune)
	if err != nil {
		t.Fatal(err)
	}
	run := job.Run(context.Background(), nil)
	if run.State != "complete" {
		t.Fatalf("backup %s: %+v", run.State, run.Errors)
	}
	return run
}

func TestBackupCopiesTheLibraryIncrementally(t *testing.T) {
	t.Run("catalog-progress", func(t *testing.T) {
		override(t, &backupStepPages, int32(1))
		config, dest := backupFixture(t)
		job, err := prepareBackup(config, dest, false)
		if err != nil {
			t.Fatal(err)
		}
		_, parent := newOpRegistry("service").begin(context.Background(), OperationSpec{Kind: "background.update", Trigger: triggerUser})
		defer parent.Finish(nil)
		parent.Plan(backgroundSteps([]string{"backup"})...)
		parent.Step("Back up", "", -1)
		_, child := parent.Child(OperationSpec{Kind: "backup.local", Trigger: triggerChained})
		defer child.Finish(nil)
		inventory, partial := false, false
		run := job.Run(child.Context(), func(run BackupRun) {
			mirrorBackup(child, run)
			if run.Phase != "catalog" || run.BytesTotal <= 0 {
				return
			}
			inventory = inventory || run.BytesCopied == 0
			partial = partial || run.BytesCopied > 0 && run.BytesCopied < run.BytesTotal
			step := operationStep(child.view())
			if step["unit"] != "byte" || step["done"] != float64(run.BytesCopied) || step["total"] != float64(run.BytesTotal) {
				t.Fatalf("catalog progress did not reach the background operation: %+v, run=%+v", step, run)
			}
		})
		if run.State != "complete" || !inventory || !partial || run.BytesCopied != run.BytesTotal || run.BytesCopied <= run.CatalogBytes {
			t.Fatalf("backup progress or final byte accounting: inventory=%v partial=%v run=%+v", inventory, partial, run)
		}
		if step := parent.view().Step; step.Index != 1 || len(parent.view().Plan) != 1 {
			t.Fatalf("snapshot progress advanced the planned task: %+v", step)
		}
	})
	t.Run("hard-linked-inputs", func(t *testing.T) {
		config, dest := backupFixture(t)
		source := filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "b.jsonl")
		alias := filepath.Join(config.CaptureRoot, "host-a", "claude", "inputs", "revision", "files", "b.jsonl")
		if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(source, alias); err != nil {
			t.Fatal(err)
		}
		lease := filepath.Join(config.CaptureRoot, "host-a", "claude", ".reader-active"+captureTempSuffix, "files", "reader.jsonl")
		writeSourceFile(t, lease, "temporary reader")
		runBackup(t, config, dest, false)
		a, err := os.Stat(filepath.Join(dest, "captures", "host-a", "claude", "files", "p", "b.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(filepath.Join(dest, "captures", "host-a", "claude", "inputs", "revision", "files", "b.jsonl"))
		if err != nil || !os.SameFile(a, b) {
			t.Fatal("backup duplicated a retained inode", err)
		}
		original, _ := os.Stat(source)
		if os.SameFile(a, original) {
			t.Fatal("backup links to the original drive")
		}
		if _, err := os.Stat(filepath.Join(dest, "captures", "host-a", "claude", filepath.Base(filepath.Dir(filepath.Dir(lease))))); !os.IsNotExist(err) {
			t.Fatal("backup included a reader lease", err)
		}
	})
	config, dest := backupFixture(t)
	first := runBackup(t, config, dest, false)
	// Config and descriptor, 2 captures and manifest, host settings, preserved evidence.
	if first.FilesCopied != 7 || !first.CatalogCopied || first.FilesUnchanged != 0 {
		t.Fatalf("first backup: %+v", first)
	}
	source := filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "b.jsonl")
	copied := filepath.Join(dest, "captures", "host-a", "claude", "files", "p", "b.jsonl")
	sourceInfo, _ := os.Stat(source)
	copiedInfo, err := os.Stat(copied)
	if err != nil || readText(t, copied) != readText(t, source) || !copiedInfo.ModTime().Equal(sourceInfo.ModTime()) || copiedInfo.Mode().Perm() != 0o600 {
		t.Fatalf("copy of b.jsonl: %v %v", copiedInfo, err)
	}
	for _, skipped := range []string{"captures/host-a/" + captureLockName, "captures/host-a/claude/files/p/c.jsonl" + captureTempSuffix, "staging"} {
		if _, err := os.Stat(filepath.Join(dest, skipped)); err == nil {
			t.Errorf("%s was backed up", skipped)
		}
	}
	for _, kept := range []string{"library.toml", libraryDescriptorName, "hosts/host-a.toml", "preserved/package/x.json", "captures/host-a/claude/manifest.json"} {
		if _, err := os.Stat(filepath.Join(dest, kept)); err != nil {
			t.Errorf("%s missing from the backup: %v", kept, err)
		}
	}
	snapshot, err := openReadOnlySQLite(filepath.Join(dest, "catalog", "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	var label, mode string
	if err := snapshot.QueryRow("SELECT label FROM hosts WHERE id='host-z'").Scan(&label); err != nil || label != "Z" {
		t.Fatalf("catalog snapshot: %q %v", label, err)
	}
	if err := snapshot.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("snapshot journal mode %q %v", mode, err)
	}
	snapshot.Close()
	var manifest backupManifest
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(dest, backupManifestName))), &manifest); err != nil || !manifest.Complete || manifest.Catalog == nil || manifest.Library.VolumeID != "uuid:TEST-VOLUME" {
		t.Fatalf("manifest: %+v %v", manifest, err)
	}
	history := loadBackupHistory(config)
	if len(history.Backups) != 1 || history.Backups[0].Destination != dest || history.Backups[0].BytesTotal != first.BytesTotal {
		t.Fatalf("backups.json: %+v", history)
	}

	// Nothing changed: nothing is copied, not even the catalog.
	second := runBackup(t, config, dest, false)
	if second.FilesCopied != 0 || second.CatalogCopied || second.FilesUnchanged != first.FilesCopied || second.BytesTotal != first.BytesTotal {
		t.Fatalf("unchanged backup: %+v", second)
	}

	// Changes are copied; a file gone from the library stays in the backup.
	writeSourceFile(t, source, strings.Repeat("two\n", 1001))
	writeSourceFile(t, filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "d.jsonl"), "new\n")
	if err := os.Remove(filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "a.jsonl")); err != nil {
		t.Fatal(err)
	}
	third := runBackup(t, config, dest, false)
	if third.FilesCopied != 2 || third.FilesPruned != 0 || readText(t, copied) != readText(t, source) {
		t.Fatalf("changed backup: %+v", third)
	}
	gone := filepath.Join(dest, "captures", "host-a", "claude", "files", "p", "a.jsonl")
	if _, err := os.Stat(gone); err != nil {
		t.Fatal("a file deleted from the library was deleted from the backup without --prune")
	}
	// A leftover temporary is ours to prune too.
	writeSourceFile(t, filepath.Join(dest, "preserved", "stale"+backupTempSuffix), "x")
	fourth := runBackup(t, config, dest, true)
	if fourth.FilesPruned != 2 || fourth.FilesCopied != 0 {
		t.Fatalf("pruned backup: %+v", fourth)
	}
	if _, err := os.Stat(gone); err == nil {
		t.Fatal("--prune kept a file deleted from the library")
	}
	if len(loadBackupHistory(config).Backups) != 4 {
		t.Fatal("not every backup was recorded")
	}
}

func TestBackupRefusesUnsafeDestinations(t *testing.T) {
	config, dest := backupFixture(t)
	library := filepath.Dir(config.Path)
	cases := map[string]string{
		filepath.Join(library, "backup"): "overlaps",
		library:                          "overlaps",
		filepath.Join(filepath.Dir(dest), "missing", "deeper"): "unavailable",
		"": "required",
	}
	for destination, problem := range cases {
		if _, err := prepareBackup(config, destination, false); err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("backup to %q: %v, want %q", destination, err, problem)
		}
	}
	writeSourceFile(t, filepath.Join(dest, "notes.txt"), "mine")
	if _, err := prepareBackup(config, dest, true); err == nil || !strings.Contains(err.Error(), "neither empty nor a Pharos backup") {
		t.Fatalf("non-empty destination: %v", err)
	}
	os.Remove(filepath.Join(dest, "notes.txt"))
	writeSourceFile(t, filepath.Join(dest, ".DS_Store"), "finder")
	runBackup(t, config, dest, false)
	// Another library may not take over this backup.
	record := backupRecordPath(config)
	saved := readText(t, record)
	writeSourceFile(t, record, strings.Replace(saved, `"library_id": "`, `"library_id": "other-`, 1))
	if _, err := prepareBackup(config, dest, true); err == nil || !strings.Contains(err.Error(), "another library") {
		t.Fatalf("foreign library: %v", err)
	}
	writeSourceFile(t, record, saved)
	for _, relative := range []string{"relative/path", "./x", "x"} {
		if _, err := prepareBackup(config, relative, false); err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Errorf("relative destination %q: %v", relative, err)
		}
	}

	// Without the stand-in drives, a folder beside the library is refused.
	backupVolume = func(path string) (uint64, volumeFacts) {
		device, _ := deviceOf(nearestExisting(path))
		return device, volumeFacts{}
	}
	if _, err := prepareBackup(config, filepath.Join(filepath.Dir(dest), "Second"), false); err == nil || !strings.Contains(err.Error(), "same volume") {
		t.Fatalf("same-volume destination: %v", err)
	}
	backupVolume = func(path string) (uint64, volumeFacts) {
		device, _ := deviceOf(nearestExisting(path))
		if within(canonical(path), filepath.Dir(dest)) {
			device++
		}
		return device, volumeFacts{PhysicalDisk: "disk4"}
	}
	if _, err := prepareBackup(config, filepath.Join(filepath.Dir(dest), "Second"), false); err == nil || !strings.Contains(err.Error(), "same physical drive (disk4)") {
		t.Fatalf("same-drive destination: %v", err)
	}
}

func TestBackupWaitsForARunningCapture(t *testing.T) {
	config, dest := backupFixture(t)
	lock, err := os.OpenFile(filepath.Join(config.CaptureRoot, "host-a", captureLockName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	job, err := prepareBackup(config, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	const waitingPhase = "waiting for a capture to finish"
	waiting := make(chan struct{})
	notices, last := 0, ""
	done := make(chan BackupRun)
	go func() {
		done <- job.Run(context.Background(), func(run BackupRun) {
			// Later progress reports repeat the phase; count entries into it.
			if run.Phase == waitingPhase && last != waitingPhase {
				if notices++; notices == 1 {
					close(waiting)
				}
			}
			last = run.Phase
		})
	}()
	select {
	case <-waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("the backup did not wait for the capture lock")
	}
	if _, err := os.Stat(filepath.Join(dest, "captures", "host-a")); err == nil {
		t.Fatal("captures were copied while a capture held the lock")
	}
	lock.Close()
	if run := <-done; run.State != "complete" || notices != 1 {
		t.Fatalf("backup after the capture (%d waiting notices): %+v", notices, run)
	}
	if _, err := os.Stat(filepath.Join(dest, "captures", "host-a", "claude", "manifest.json")); err != nil {
		t.Fatal(err)
	}

	// The capture lock is a shared, nonblocking acquisition. Only the
	// adapter's contention waits; any other failure leaves the lock's state
	// unknown, so that host is skipped while the rest of the backup goes on.
	host := filepath.Join(config.CaptureRoot, "host-a")
	direct := func(ctx context.Context) (*backupJob, *int) {
		job, err := prepareBackup(config, dest, false)
		if err != nil {
			t.Fatal(err)
		}
		notices := 0
		job.ctx, job.progress = ctx, func(run BackupRun) {
			if run.Phase == waitingPhase {
				notices++
			}
		}
		return job, &notices
	}
	for _, failure := range captureLockFailures() {
		t.Run(failure.name, func(t *testing.T) {
			var files []*os.File
			override(t, &lockBackupCaptures, checkedLock(t, filelock.Shared, filelock.Try, &files, func(call int, file *os.File) error {
				switch {
				case call == 1:
					return failure.err
				case !failure.contended:
					// End the wait a regression would start, rather than hang.
					t.Errorf("retried after %v", failure.err)
				}
				return filelock.Lock(file, filelock.Shared, filelock.Try)
			}))
			job, notices := direct(context.Background())
			unlock, err := job.lockCaptures(host)
			if failure.contended {
				if err != nil || len(files) != 2 || *notices != 1 {
					t.Fatalf("after contention: %v, %d attempts, %d notices", err, len(files), *notices)
				}
				if _, err := files[0].Stat(); err != nil {
					t.Fatal("the capture lock was released before the copy:", err)
				}
				unlock()
				requireLocksClosed(t, files, 2)
				return
			}
			if unlock != nil || err != failure.err || *notices != 0 {
				t.Fatalf("lock failure: %v, %d notices", err, *notices)
			}
			requireLocksClosed(t, files, 1)

			files = nil
			skipped := filepath.Join(filepath.Dir(dest), "Skipped "+failure.name)
			job, err = prepareBackup(config, skipped, false)
			if err != nil {
				t.Fatal(err)
			}
			run := job.Run(context.Background(), nil)
			if run.State != "failed" || len(run.Errors) != 1 || run.Errors[0].Error != failure.err.Error() || filepath.Base(run.Errors[0].Path) != "host-a" {
				t.Fatalf("backup beside an unknown capture lock: %+v", run)
			}
			if backupFileExists(t, filepath.Join(skipped, "captures", "host-a")) || !backupFileExists(t, filepath.Join(skipped, "hosts", "host-a.toml")) {
				t.Fatal("the host was copied, or the rest of the backup was not")
			}
			requireLocksClosed(t, files, 1)
		})
	}
	t.Run("cancelled while waiting", func(t *testing.T) {
		contended := errors.Join(filelock.ErrContended, syscall.EWOULDBLOCK)
		var files []*os.File
		override(t, &lockBackupCaptures, checkedLock(t, filelock.Shared, filelock.Try, &files, func(int, *os.File) error { return contended }))
		ctx, cancel := context.WithCancel(context.Background())
		job, notices := direct(ctx)
		job.progress = func(run BackupRun) {
			if run.Phase == waitingPhase {
				*notices++
				cancel()
			}
		}
		if unlock, err := job.lockCaptures(host); unlock != nil || !errors.Is(err, context.Canceled) || *notices != 1 {
			t.Fatalf("cancelled wait: %v, %d notices", err, *notices)
		}
		requireLocksClosed(t, files, 1)

		// A pre-cancelled wait still makes its first attempt, and a lock it
		// gets is kept.
		files = nil
		override(t, &lockBackupCaptures, checkedLock(t, filelock.Shared, filelock.Try, &files, func(_ int, file *os.File) error {
			return filelock.Lock(file, filelock.Shared, filelock.Try)
		}))
		job, notices = direct(ctx)
		unlock, err := job.lockCaptures(host)
		if err != nil || len(files) != 1 || *notices != 0 {
			t.Fatalf("pre-cancelled acquisition: %v, %d attempts, %d notices", err, len(files), *notices)
		}
		unlock()
		requireLocksClosed(t, files, 1)

		// A backup cancelled while it waits stops rather than skipping.
		files = nil
		override(t, &lockBackupCaptures, checkedLock(t, filelock.Shared, filelock.Try, &files, func(int, *os.File) error { return contended }))
		cancelled := filepath.Join(filepath.Dir(dest), "Cancelled")
		job, err = prepareBackup(config, cancelled, false)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		run := job.Run(ctx, func(run BackupRun) {
			if run.Phase == waitingPhase {
				cancel()
			}
		})
		if run.State != "cancelled" || backupFileExists(t, filepath.Join(cancelled, "captures", "host-a")) {
			t.Fatalf("backup cancelled while waiting: %+v", run)
		}
		requireLocksClosed(t, files, 1)
	})
	t.Run("missing lock", func(t *testing.T) {
		override(t, &lockBackupCaptures, func(file *os.File, _ filelock.Mode, _ filelock.WaitMode) error {
			t.Errorf("locked %s", file.Name())
			return nil
		})
		if err := os.Remove(filepath.Join(host, captureLockName)); err != nil {
			t.Fatal(err)
		}
		job, notices := direct(context.Background())
		if unlock, err := job.lockCaptures(host); unlock == nil || err != nil || *notices != 0 {
			t.Fatalf("missing capture lock: %v", err)
		} else {
			unlock()
		}
		if run := runBackup(t, config, filepath.Join(filepath.Dir(dest), "Unlocked"), false); run.State != "complete" {
			t.Fatalf("backup without a capture lock: %+v", run)
		}
		if backupFileExists(t, filepath.Join(host, captureLockName)) {
			t.Fatal("the backup created a capture lock")
		}
	})
}

func TestBackupRefusesADestinationInUse(t *testing.T) {
	config, dest := backupFixture(t)
	runBackup(t, config, dest, false)
	lock, err := os.OpenFile(filepath.Join(dest, backupLockName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	job, err := prepareBackup(config, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if run := job.Run(context.Background(), nil); run.State != "failed" || len(run.Errors) != 1 || run.Errors[0].Error != errBackupDestinationBusy.Error() {
		t.Fatalf("second backup to a destination in use: %+v", run)
	}

	// Only the adapter's contention is another backup; any other failure is
	// the destination's, with its cause. Neither names the library nor
	// writes the destination.
	for _, failure := range captureLockFailures() {
		t.Run(failure.name, func(t *testing.T) {
			fresh := filepath.Join(filepath.Dir(dest), "Fresh "+failure.name)
			job, err := prepareBackup(config, fresh, false)
			if err != nil {
				t.Fatal(err)
			}
			// Without an id the job would record one once it held the lock.
			job.ctx, job.plan.library.ID = context.Background(), ""
			override(t, &lockBackupHistory, func(file *os.File, _ filelock.Mode, _ filelock.WaitMode) error {
				t.Errorf("recorded the library's id without the destination: %s", file.Name())
				return nil
			})
			var files []*os.File
			override(t, &lockBackupDestination, checkedLock(t, filelock.Exclusive, filelock.Try, &files, func(int, *os.File) error { return failure.err }))
			err = job.execute()
			if failure.contended {
				if err != errBackupDestinationBusy {
					t.Fatalf("contention: %v", err)
				}
			} else if !errors.As(err, new(backupDestinationError)) || !errors.Is(err, failure.err) || errors.Is(err, errBackupDestinationBusy) {
				t.Fatalf("lock failure: %v", err)
			}
			requireLocksClosed(t, files, 1)
			entries, err := os.ReadDir(fresh)
			if err != nil || len(entries) != 1 || entries[0].Name() != backupLockName {
				t.Fatalf("a refused backup wrote its destination: %v %v", entries, err)
			}
		})
	}
}

func TestBackupCancelledRecordsNothing(t *testing.T) {
	config, dest := backupFixture(t)
	job, err := prepareBackup(config, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if run := job.Run(ctx, nil); run.State != "cancelled" {
		t.Fatalf("cancelled backup: %+v", run)
	}
	if len(loadBackupHistory(config).Backups) != 0 {
		t.Fatal("a cancelled backup was recorded")
	}
}

func TestBackupAPIRunsInBackground(t *testing.T) {
	config, dest := backupFixture(t)
	server := probeServer(t, config.Path)
	if code, body := probeCall(t, server, http.MethodPost, "/api/backup", `{"destination":"relative/path"}`); code != http.StatusBadRequest {
		t.Fatalf("relative destination: %d %v", code, body)
	}
	code, body := probeCall(t, server, http.MethodPost, "/api/backup", fmt.Sprintf(`{"destination":%q}`, dest))
	if code != http.StatusAccepted || body["ok"] != true {
		t.Fatalf("start: %d %v", code, body)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, status := probeCall(t, server, http.MethodGet, "/api/backup", "")
		if status["active"] == false {
			run, _ := status["run"].(map[string]any)
			if run["state"] != "complete" || status["last_success"] == nil {
				t.Fatalf("backup status: %v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup still running: %v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, body := probeCall(t, server, http.MethodPost, "/api/backup/cancel", ""); body["cancelled"] != false {
		t.Fatalf("cancel with nothing running: %v", body)
	}
	_, health := probeCall(t, server, http.MethodGet, "/api/health", "")
	if drive, ok := health["drive"].(map[string]any); !ok || drive["status"] == nil {
		t.Fatalf("health has no drive report: %v", health["drive"])
	}
}

// Item 1: a symlinked archive_root or hosts/ is backed up as what it points
// to, and a listing that comes back empty never prunes an earlier backup.
func TestBackupFollowsSymlinkedRootsAndNeverPrunesAnEmptyListing(t *testing.T) {
	config, dest := backupFixture(t)
	library := filepath.Dir(config.Path)
	elsewhere := filepath.Join(filepath.Dir(library), "Elsewhere")
	for _, name := range []string{"preserved", "hosts"} {
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(library, name), filepath.Join(elsewhere, name)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(elsewhere, name), filepath.Join(library, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink inside a tree is not followed, and its earlier copy is kept.
	writeSourceFile(t, filepath.Join(elsewhere, "linked.json"), "{}")
	if err := os.Symlink(filepath.Join(elsewhere, "linked.json"), filepath.Join(elsewhere, "preserved", "link.json")); err != nil {
		t.Fatal(err)
	}
	first := runBackup(t, config, dest, true)
	for _, kept := range []string{"preserved/package/x.json", "hosts/host-a.toml"} {
		if !backupFileExists(t, filepath.Join(dest, kept)) {
			t.Fatalf("%s is missing from the backup: %+v", kept, first)
		}
	}
	if first.FilesPruned != 0 || !strings.Contains(strings.Join(first.Warnings, " "), "symbolic links") {
		t.Fatalf("first backup: %+v", first)
	}
	writeSourceFile(t, filepath.Join(dest, "preserved", "link.json"), "an earlier copy")
	second := runBackup(t, config, dest, true)
	if second.FilesPruned != 0 || second.FilesCopied != 0 || !backupFileExists(t, filepath.Join(dest, "preserved", "link.json")) {
		t.Fatalf("second backup: %+v", second)
	}
	// archive_root now lists nothing (an unmounted drive's empty mount
	// point, say): its backup is kept, with a warning.
	if err := os.RemoveAll(filepath.Join(elsewhere, "preserved")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(elsewhere, "preserved"), 0o755); err != nil {
		t.Fatal(err)
	}
	third := runBackup(t, config, dest, true)
	if third.FilesPruned != 0 || !backupFileExists(t, filepath.Join(dest, "preserved", "package", "x.json")) || !strings.Contains(strings.Join(third.Warnings, " "), "was not pruned") {
		t.Fatalf("empty listing: %+v", third)
	}
}

// Item 2: HFS+ stores names decomposed. A copy whose name the destination
// stores differently from the library's is still the same file to --prune.
func TestBackupPruneKeepsFilesTheDestinationNamesDifferently(t *testing.T) {
	config, dest := backupFixture(t)
	composed := []string{"café.json", "한국어.json"}
	decomposed := []string{"café.json", "한국어.json"}
	for _, name := range composed {
		writeSourceFile(t, filepath.Join(config.ArchiveRoot, name), name)
	}
	runBackup(t, config, dest, false)
	// What an HFS+ destination would have stored.
	for index := range composed {
		temporary := filepath.Join(dest, "preserved", "renaming")
		if err := os.Rename(filepath.Join(dest, "preserved", composed[index]), temporary); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temporary, filepath.Join(dest, "preserved", decomposed[index])); err != nil {
			t.Fatal(err)
		}
	}
	run := runBackup(t, config, dest, true)
	if run.FilesPruned != 0 || run.FilesCopied != 0 {
		t.Fatalf("prune removed or recopied files it had just accounted for: %+v", run)
	}
	for _, name := range composed {
		if readText(t, filepath.Join(dest, "preserved", name)) != name {
			t.Fatalf("%s is gone from the backup", name)
		}
	}
}

// Item 3: a destination belongs to one library. A per-user install on
// another Mac is another library, even with the same catalog path and a copy
// of backups.json; a portable library stays itself on every Mac.
func TestBackupIdentityTiesADestinationToOneLibrary(t *testing.T) {
	config, dest := legacyBackupFixture(t, func(data string) string { return filepath.Join(data, "preserved") })
	runBackup(t, config, dest, false)
	id := loadBackupHistory(config).LibraryID
	if id == "" {
		t.Fatal("the first backup did not give the library an id")
	}
	var manifest backupManifest
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(dest, backupManifestName))), &manifest); err != nil || manifest.Library.ID != id || manifest.Library.HostID != "mac-a" {
		t.Fatalf("manifest library: %+v %v", manifest.Library, err)
	}
	useHost(t, "mac-b")
	if _, err := prepareBackup(config, dest, true); err == nil || !strings.Contains(err.Error(), "another library") {
		t.Fatalf("another Mac's install with a copied backups.json: %v", err)
	}
	os.Remove(backupRecordPath(config))
	if _, err := prepareBackup(config, dest, true); err == nil || !strings.Contains(err.Error(), "another library") {
		t.Fatalf("another Mac's install: %v", err)
	}

	library, libraryDest := backupFixture(t)
	runBackup(t, library, libraryDest, false)
	useHost(t, "host-b")
	if run := runBackup(t, library, libraryDest, true); run.State != "complete" {
		t.Fatalf("the same library on another Mac: %+v", run)
	}
}

// Item 4: with archive_root being the catalog's own directory, the tree
// copies neither the live catalog nor backup internals, and prune spares
// the snapshot, the manifest and the lock.
func TestBackupKeepsTheLiveCatalogOutOfTrees(t *testing.T) {
	config, dest := legacyBackupFixture(t, func(data string) string { return data })
	writeSourceFile(t, filepath.Join(config.ArchiveRoot, "package", "a.json"), "{}")
	writeSourceFile(t, filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "x.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(config.StagingRoot, "partial.zip"), "zip")
	catalog := reopenCatalog(t, config.CatalogPath)
	defer catalog.Close()
	if _, err := catalog.DB.Exec("INSERT INTO hosts(id,label,first_seen_at,last_seen_at) VALUES('live','Live','2026-01-01','2026-01-01')"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		runBackup(t, config, dest, true)
	}
	preserved := filepath.Join(dest, "preserved")
	if !backupFileExists(t, filepath.Join(preserved, "package", "a.json")) || !backupFileExists(t, filepath.Join(dest, "captures", "host-a", "claude", "files", "x.jsonl")) {
		t.Fatal("the archive or captures are missing from the backup")
	}
	for _, name := range []string{"catalog.sqlite3", "catalog.sqlite3-wal", "catalog.sqlite3-shm", "backups.json", backupRecordLockName, "captures", "staging", "archive.toml"} {
		if backupFileExists(t, filepath.Join(preserved, name)) {
			t.Errorf("preserved/%s was copied", name)
		}
	}
	for _, name := range []string{backupManifestName, backupLockName, "catalog.sqlite3", "archive.toml"} {
		if !backupFileExists(t, filepath.Join(dest, name)) {
			t.Errorf("%s is missing after a pruned backup", name)
		}
	}
	snapshot, err := openReadOnlySQLite(filepath.Join(dest, "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var label, mode string
	if err := snapshot.QueryRow("SELECT label FROM hosts WHERE id='live'").Scan(&label); err != nil || label != "Live" {
		t.Fatalf("snapshot: %q %v", label, err)
	}
	if err := snapshot.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("snapshot journal mode %q %v", mode, err)
	}
}

// Item 5: a destination on a disk image stored on the library's drive is on
// that drive, and so is a library on an image stored on the destination's.
func TestBackupRefusesDiskImagesOnTheSameDrive(t *testing.T) {
	config, dest := backupFixture(t)
	library := filepath.Dir(config.Path)
	image := filepath.Join(filepath.Dir(library), "Images", "Backup.sparsebundle")
	stubBackupDrives(t, filepath.Dir(dest), func(path string, facts *volumeFacts) {
		if within(path, filepath.Dir(dest)) {
			facts.Location, facts.ImagePath, facts.PhysicalDisk = "disk-image", image, "disk12"
		}
	})
	if _, err := prepareBackup(config, dest, false); err == nil || !strings.Contains(err.Error(), "disk image ("+image+") stored on the same drive") {
		t.Fatalf("backup onto an image on the library drive: %v", err)
	}
	other := filepath.Join(filepath.Dir(dest), "library.sparsebundle")
	stubBackupDrives(t, filepath.Dir(dest), func(path string, facts *volumeFacts) {
		if within(path, library) || path == library {
			facts.Location, facts.ImagePath, facts.PhysicalDisk = "disk-image", other, "disk13"
		}
	})
	if _, err := prepareBackup(config, dest, false); err == nil || !strings.Contains(err.Error(), "is on a disk image ("+other+")") {
		t.Fatalf("library on an image on the destination drive: %v", err)
	}
}

// Item 6: exFAT, HFS+ and FAT store mtimes coarsely; the backup stays
// incremental there, and still copies a real change.
func TestBackupIsIncrementalOnCoarseTimestamps(t *testing.T) {
	config, dest := backupFixture(t)
	runBackup(t, config, dest, false)
	for _, granularity := range []time.Duration{10 * time.Millisecond, time.Second, 2 * time.Second} {
		_ = filepath.WalkDir(filepath.Join(dest, "captures"), func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.Type().IsRegular() {
				info, _ := entry.Info()
				rounded := info.ModTime().Truncate(granularity)
				os.Chtimes(path, rounded, rounded)
			}
			return nil
		})
		if run := runBackup(t, config, dest, true); run.FilesCopied != 0 || run.FilesPruned != 0 {
			t.Fatalf("%v timestamps: %+v", granularity, run)
		}
	}
	source := filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "a.jsonl")
	writeSourceFile(t, source, "ONE\n") // same size, a later mtime
	if run := runBackup(t, config, dest, false); run.FilesCopied != 1 {
		t.Fatalf("changed file: %+v", run)
	}
}

// Item 6: a missing part makes the backup partial, and says so.
func TestBackupWithAMissingPartIsPartial(t *testing.T) {
	config, dest := backupFixture(t)
	runBackup(t, config, dest, false)
	if err := os.RemoveAll(config.ArchiveRoot); err != nil {
		t.Fatal(err)
	}
	job, err := prepareBackup(config, dest, true)
	if err != nil {
		t.Fatal(err)
	}
	run := job.Run(context.Background(), nil)
	if run.State != "partial" || len(run.Missing) != 1 || run.Missing[0] != config.ArchiveRoot {
		t.Fatalf("backup without archive_root: %+v", run)
	}
	if !backupFileExists(t, filepath.Join(dest, "preserved", "package", "x.json")) {
		t.Fatal("the missing part's earlier backup was deleted")
	}
	history := loadBackupHistory(config)
	if !history.Backups[0].Partial {
		t.Fatalf("history: %+v", history.Backups[0])
	}
	if check := backupCheck(config, history, volumeFacts{}); check.Status != "warning" || !strings.Contains(check.Summary, "partial") {
		t.Fatalf("backup check after a partial backup: %+v", check)
	}
	if rate, _ := (backupHistory{Backups: []backupRecord{{FinishedAt: now(), Partial: true, BytesTotal: 1}, {FinishedAt: time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano), BytesTotal: 5e9}}}).growth(); rate != 0 {
		t.Fatalf("growth counted a partial backup: %v", rate)
	}
}

// Item 6: concurrent records all land in backups.json.
func TestBackupRecordsAreNotLost(t *testing.T) {
	config, _ := backupFixture(t)
	var wait sync.WaitGroup
	for index := range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := recordBackup(config, backupRecord{Destination: fmt.Sprint(index), FinishedAt: now()}); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if history := loadBackupHistory(config); len(history.Backups) != 20 {
		t.Fatalf("%d of 20 records kept", len(history.Backups))
	}

	// One blocking exclusive acquisition: a record waits for another holder,
	// here a raw flock, and lands once it lets go.
	holder, err := os.OpenFile(filepath.Join(filepath.Dir(backupRecordPath(config)), backupRecordLockName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	var files []*os.File
	blocking := make(chan struct{})
	override(t, &lockBackupHistory, checkedLock(t, filelock.Exclusive, filelock.Wait, &files, func(_ int, file *os.File) error {
		close(blocking)
		return filelock.Lock(file, filelock.Exclusive, filelock.Wait)
	}))
	done := make(chan error)
	go func() { done <- recordBackup(config, backupRecord{Destination: "after the holder", FinishedAt: now()}) }()
	<-blocking
	select {
	case err := <-done:
		t.Fatalf("recorded past another holder: %v", err)
	default:
	}
	holder.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if history := loadBackupHistory(config); len(history.Backups) != 21 || history.Backups[0].Destination != "after the holder" {
		t.Fatalf("record after the holder: %+v", history.Backups)
	}
	requireLocksClosed(t, files, 1)

	// A failed acquisition is returned as itself, never as busy, before
	// the update sees or writes the history.
	before := readText(t, backupRecordPath(config))
	for _, failure := range captureLockFailures() {
		t.Run(failure.name, func(t *testing.T) {
			var files []*os.File
			override(t, &lockBackupHistory, checkedLock(t, filelock.Exclusive, filelock.Wait, &files, func(int, *os.File) error { return failure.err }))
			if _, err := updateBackupHistory(config, func(*backupHistory) bool {
				t.Error("updated the history without its lock")
				return true
			}); err != failure.err {
				t.Fatalf("history lock failure: %v", err)
			}
			if err := recordBackup(config, backupRecord{Destination: "unlocked", FinishedAt: now()}); err != failure.err {
				t.Fatalf("record: %v", err)
			}
			if id, err := libraryID(config); id != "" || err != failure.err {
				t.Fatalf("library id: %q %v", id, err)
			}
			if readText(t, backupRecordPath(config)) != before {
				t.Fatal("the history changed without its lock")
			}
			requireLocksClosed(t, files, 3)
		})
	}
}

// Item 6: credentials for other services stay out of the backup.
func TestBackupLeavesOtherServicesCredentialsOut(t *testing.T) {
	config, dest := legacyBackupFixture(t, func(data string) string { return filepath.Join(data, "preserved") })
	writeSourceFile(t, config.Path, "api_token = \"local-token\"\ngithub_token = \"ghp_secret\" # mine\ntl1_token = 'tl1-secret'\narchive_root = \"x\"\n\n[[sources]]\nname = \"github_token\"\n")
	runBackup(t, config, dest, false)
	copied := readText(t, filepath.Join(dest, "archive.toml"))
	if strings.Contains(copied, "ghp_secret") || strings.Contains(copied, "tl1-secret") || !strings.Contains(copied, `api_token = "local-token"`) ||
		!strings.Contains(copied, "# github_token was left out of this backup") || !strings.Contains(copied, "name = \"github_token\"") {
		t.Fatalf("backed-up configuration:\n%s", copied)
	}
	if run := runBackup(t, config, dest, false); run.FilesCopied != 0 {
		t.Fatalf("an unchanged configuration was copied again: %+v", run)
	}
}

// Item 6: while a writer keeps committing, a snapshot stops once the WAL it
// pins grows past the bound, rather than letting it grow without limit.
func TestBackupBoundsTheCatalogWAL(t *testing.T) {
	slowTest(t)
	config, dest := backupFixture(t)
	previousGrowth, previousStep, previousPoll := backupWALGrowth, backupStepPages, backupWALPoll
	backupWALGrowth, backupStepPages, backupWALPoll = 64<<10, 1, 5*time.Millisecond
	t.Cleanup(func() { backupWALGrowth, backupStepPages, backupWALPoll = previousGrowth, previousStep, previousPoll })
	catalog := reopenCatalog(t, config.CatalogPath)
	defer catalog.Close()
	if _, err := catalog.DB.Exec("CREATE TABLE filler(body BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<4000) INSERT INTO filler SELECT randomblob(8000) FROM n"); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var commits atomic.Int64
	var writeErr atomic.Value
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := catalog.DB.Exec("INSERT INTO filler VALUES(randomblob(8000))"); err != nil {
				writeErr.Store(err)
			} else {
				commits.Add(1)
			}
		}
	}()
	job, err := prepareBackup(config, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	run := job.Run(context.Background(), nil)
	close(stop)
	<-done
	if run.State != "failed" || len(run.Errors) == 0 || !strings.Contains(run.Errors[0].Error, "WAL grew") || run.CatalogWALPeak <= backupWALGrowth {
		t.Fatalf("backup under a writer (%d commits, error %v): %+v", commits.Load(), writeErr.Load(), run)
	}
	if backupFileExists(t, filepath.Join(dest, "catalog", "catalog.sqlite3"+backupTempSuffix)) {
		t.Fatal("the abandoned snapshot was left behind")
	}
}

// Raw revisions and persistent pin ownership are ordinary capture-tree files.
// A restored library must replay them without either the native DB or its
// source host, including after every native generation has disappeared.
func addSQLiteRecordBackupFixture(t *testing.T, config *Config) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("durable-backup", 2)...)
	config.Sources = []SourceConfig{{Name: "records", Kind: "conductor", Path: dir, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}}
	if run := runCapture(t, *config); !run.OK {
		t.Fatal(run)
	}
	return dir
}
func TestBackupRestoresSQLiteRecordReplay(t *testing.T) {
	config, dest := backupFixture(t)
	original := addSQLiteRecordBackupFixture(t, &config)
	// A compressed latest transcript and earlier version are backed up as
	// stored and recover as valid evidence.
	addEncodedClaudeFixture(t, &config)
	runBackup(t, config, dest, false)
	assertEncodedClaudeFixture(t, filepath.Join(dest, "captures"), "host-a")
	recovered, err := RecoverLibrary(t.Context(), dest, filepath.Join(t.TempDir(), "recovered"), nil)
	if err != nil {
		t.Fatal("recovery of a backup with compressed captures", err)
	}
	assertEncodedClaudeFixture(t, recovered.CaptureRoot, "host-a")
	os.RemoveAll(original)
	restored := config
	restored.CaptureRoot = filepath.Join(dest, "captures")
	os.RemoveAll(capturedPath(restored, "records", "files"))
	os.RemoveAll(capturedPath(restored, "records", "history"))
	catalog, _ := testCatalog(t)
	recordTestReplay(t, restored, "records", catalog)
	if conversationMessages(t, catalog, "conductor:durable-backup") != 2 {
		t.Fatal("backup lost durable SQLite evidence")
	}
}

// A backup taken while a compression is half done (its encoding still in its
// lease, or its manifest committed but the file not yet renamed) recovers to
// a library whose capture validates and still yields the session only the
// generation holds.
func TestBackupTakenMidCompressionRestoresAValidCapture(t *testing.T) {
	for _, stage := range []string{"compress-verify", "compress-rename"} {
		t.Run(stage, func(t *testing.T) {
			config, dest := backupFixture(t)
			dir := filepath.Join(t.TempDir(), "conductor")
			exec := conductorFixture(t, dir)
			config.Sources = []SourceConfig{{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}}
			exec(addConductorSession("old", 2)...)
			runCapture(t, config)
			exec("DELETE FROM session_messages", "DELETE FROM sessions")
			exec(addConductorSession("latest", 1)...)
			runCapture(t, config)
			override(t, &captureFault, func(at, rel string) error {
				if at == stage {
					panic("killed")
				}
				return nil
			})
			func() {
				defer func() { _ = recover() }()
				compressGenerations(t, config)
			}()
			captureFault = nil
			runBackup(t, config, dest, false)
			if left := temporariesUnder(t, filepath.Join(dest, "captures")); len(left) != 0 {
				t.Fatal("backup copied a compression's temporary", left)
			}
			// Restore through the recovery entry point, which validates every
			// captured file before publishing the library.
			restored, err := RecoverLibrary(t.Context(), dest, filepath.Join(t.TempDir(), "restored"), nil)
			if err != nil {
				t.Fatal("recovery of a backup taken mid-compression", err)
			}
			if err := validateCaptureEvidence(t.Context(), restored.CaptureRoot); err != nil {
				t.Fatal("restored capture is invalid", err)
			}
			catalog, _ := testCatalog(t)
			target := captureTarget{Host: currentHost(), Source: "conductor", Dir: capturedPath(restored, "conductor", "")}
			if result := catalog.IndexCapture(t.Context(), target, nil); result.Error != nil || conversationMessages(t, catalog, "conductor:old") != 2 {
				t.Fatalf("restored capture: %+v", result)
			}
		})
	}
}

// A backup's copy is flushed after it has its timestamps and before its name.
// A failed copy is never flushed; a failed flush is a destination error that
// keeps the previous backup's file and leaves no partial copy or link.
func TestBackupCopyFileFlushesBeforePublishing(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "dest", "file")
	writeSourceFile(t, source, "new bytes")
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	newJob := func(ctx context.Context) *backupJob {
		return &backupJob{ctx: ctx, buffer: make([]byte, 4)}
	}
	unchanged := func(t *testing.T, j *backupJob) {
		t.Helper()
		if text := readText(t, target); text != "old bytes" {
			t.Fatalf("previous backup changed: %q", text)
		}
		if backupFileExists(t, target+backupTempSuffix) || len(j.links) != 0 {
			t.Fatalf("failed copy left a partial copy or link: %v", j.links)
		}
	}
	for _, failure := range fileSyncFailures() {
		t.Run(failure.name, func(t *testing.T) {
			writeSourceFile(t, target, "old bytes")
			j := newJob(t.Context())
			calls := failFileSync(t, failure.err, func(file *os.File) bool {
				if stat, err := file.Stat(); err != nil || !stat.ModTime().Equal(info.ModTime()) {
					t.Errorf("flushed before the copy had its timestamps: %v, %v", stat, err)
				}
				return file.Name() == target+backupTempSuffix
			})
			err := j.copyFile(source, target, info)
			if !errors.As(err, new(backupDestinationError)) || !errors.Is(err, failure.is) || *calls != 1 {
				t.Fatalf("copy = %v after %d flushes; want a destination error for %v once", err, *calls, failure.is)
			}
			unchanged(t, j)
		})
	}
	t.Run("copy failure", func(t *testing.T) {
		calls := failFileSync(t, errors.New("flushed a failed copy"), func(*os.File) bool { return true })
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		for _, test := range []struct {
			name  string
			ctx   context.Context
			size  int64
			error error
		}{
			{"cancelled", cancelled, info.Size(), context.Canceled},
			{"source shrank", t.Context(), info.Size() + 1, errBackupSourceShrank},
		} {
			writeSourceFile(t, target, "old bytes")
			j := newJob(test.ctx)
			err := j.copyFile(source, target, sizedInfo{info, test.size})
			if !errors.Is(err, test.error) || errors.As(err, new(backupDestinationError)) || *calls != 0 {
				t.Fatalf("%s: copy = %v after %d flushes", test.name, err, *calls)
			}
			unchanged(t, j)
		}
	})
	t.Run("success", func(t *testing.T) {
		writeSourceFile(t, target, "old bytes")
		j := newJob(t.Context())
		calls := failFileSync(t, nil, func(file *os.File) bool {
			if text := readText(t, target); text != "old bytes" {
				t.Errorf("flushed after publishing: %q", text)
			}
			return false
		})
		if err := j.copyFile(source, target, info); err != nil || *calls != 0 {
			t.Fatal(err)
		}
		copied, err := os.Stat(target)
		if err != nil || readText(t, target) != "new bytes" || !copied.ModTime().Equal(info.ModTime()) || len(j.links) != 1 {
			t.Fatalf("copy: %v, %v, links %v", copied, err, j.links)
		}
	})
}

// sizedInfo is a file's info with another size, as if it shrank afterwards.
type sizedInfo struct {
	os.FileInfo
	size int64
}

func (s sizedInfo) Size() int64 { return s.size }

// A failed flush of the catalog snapshot or a copied file fails the backup
// before anything is published: the previous backup's files, manifest and
// history stay as they were.
func TestBackupFlushFailureKeepsThePreviousBackup(t *testing.T) {
	readManifest := func(t *testing.T, dest string) backupManifest {
		t.Helper()
		var manifest backupManifest
		if err := json.Unmarshal([]byte(readText(t, filepath.Join(dest, backupManifestName))), &manifest); err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	for _, test := range []struct {
		name string
		file func(manifest backupManifest) string
	}{
		{"catalog snapshot", func(manifest backupManifest) string { return manifest.Catalog.Path }},
		{"copied file", func(backupManifest) string { return "captures/host-a/claude/files/p/b.jsonl" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, dest := backupFixture(t)
			runBackup(t, config, dest, false)
			previous := readManifest(t, dest)
			target := filepath.Join(dest, filepath.FromSlash(test.file(previous)))
			before, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			// Change both, so that either is copied again.
			writeSourceFile(t, filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "b.jsonl"), "rewritten\n")
			catalog := reopenCatalog(t, config.CatalogPath)
			if _, err := catalog.DB.Exec("INSERT INTO hosts(id,label,first_seen_at,last_seen_at) VALUES('host-y','Y','2026-01-01','2026-01-01')"); err != nil {
				t.Fatal(err)
			}
			if err := catalog.Close(); err != nil {
				t.Fatal(err)
			}
			failure := syscall.EIO
			calls := failFileSync(t, failure, func(file *os.File) bool { return file.Name() == target+backupTempSuffix })
			job, err := prepareBackup(config, dest, false)
			if err != nil {
				t.Fatal(err)
			}
			run := job.Run(t.Context(), nil)
			if run.State != "failed" || *calls != 1 || len(run.Errors) == 0 || !strings.Contains(run.Errors[0].Error, failure.Error()) {
				t.Fatalf("backup after %d failed flushes: %+v", *calls, run)
			}
			if after, err := os.ReadFile(target); err != nil || string(after) != string(before) {
				t.Fatalf("previous backup's %s changed: %v", filepath.Base(target), err)
			}
			manifest := readManifest(t, dest)
			if manifest.Complete || manifest.FinishedAt != "" || len(loadBackupHistory(config).Backups) != 1 {
				t.Fatalf("a failed backup was recorded as finished: %+v", manifest)
			}
			if test.name == "catalog snapshot" && *manifest.Catalog != *previous.Catalog {
				t.Fatalf("the unflushed snapshot was recorded: %+v", manifest.Catalog)
			}
			err = filepath.WalkDir(dest, func(path string, entry os.DirEntry, err error) error {
				if err == nil && strings.HasSuffix(path, backupTempSuffix) {
					t.Errorf("partial copy left behind: %s", path)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnsureFreeKeepsItsMargin(t *testing.T) {
	dir := t.TempDir()
	asked := fakeCapacity(t, map[string]fsinfo.Space{dir: {TotalBytes: 1 << 40, AvailableBytes: uint64(captureFreeMargin) + 4096}}, nil)
	if err := ensureFree(dir, 4096); err != nil {
		t.Fatal("exactly the margin left:", err)
	}
	var destination backupDestinationError
	if err := ensureFree(dir, 4097); !errors.As(err, &destination) || !strings.Contains(err.Error(), "only 1.1 GB is free") {
		t.Fatal(err)
	}
	if err := ensureFree(dir, -1); !errors.As(err, &destination) || !errors.Is(err, fsinfo.ErrRange) {
		t.Fatal(err)
	}
	// An unknown capacity refuses the write as a destination problem and
	// keeps its cause; it is not an empty drive.
	fakeCapacity(t, nil, map[string]error{dir: syscall.EIO})
	err := ensureFree(dir, 1)
	if !errors.As(err, &destination) || !errors.Is(err, syscall.EIO) || strings.Contains(err.Error(), "0 B") {
		t.Fatal(err)
	}
	// Writing nothing needs no reading.
	*asked = nil
	if err := ensureFree(dir, 0); err != nil {
		t.Fatal(err)
	}
	if total, free := diskUsage(dir); total != 0 || free != 0 {
		t.Fatalf("the display reading of an unknown capacity: %d, %d", total, free)
	}
}
