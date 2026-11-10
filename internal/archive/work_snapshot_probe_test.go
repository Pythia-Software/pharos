//go:build probe

package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

// TestWorkProductionSnapshot is an explicitly invoked, read-only-source copy
// for work verification. It uses the same online SQLite snapshot, WAL growth
// bound and shared capture locks as cloud staging. It is deliberately not a
// disaster-recovery backup: source and destination may share a drive.
//
// PHAROS_WORK_SNAPSHOT_ALLOW_LIVE=1
// PHAROS_WORK_SNAPSHOT_SOURCE=/absolute/library.toml
// PHAROS_WORK_SNAPSHOT_DESTINATION=/absolute/new/snapshot
//
//	archive.test -test.run '^TestWorkProductionSnapshot$' -test.v
//
// The normal experiment commands continue refusing live libraries. Their
// prepare command can consume this copy's completed product manifest.
func TestWorkProductionSnapshot(t *testing.T) {
	source, destination := os.Getenv("PHAROS_WORK_SNAPSHOT_SOURCE"), os.Getenv("PHAROS_WORK_SNAPSHOT_DESTINATION")
	if source == "" && destination == "" {
		t.Skip("explicit source and new destination required for a live snapshot")
	}
	if os.Getenv("PHAROS_WORK_SNAPSHOT_ALLOW_LIVE") != "1" || !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		t.Fatal("live snapshot requires explicit opt-in and absolute source/destination paths")
	}
	config, err := loadConfig(source, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planBackup(config)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Library || plan.library.ID == "" {
		t.Fatal("source must be an existing library with a recorded identity; never initialize or register it")
	}
	if err := workSnapshotDestination(plan, destination); err != nil {
		t.Fatal(err)
	}
	partial := destination + ".partial"
	if err := workSnapshotDestination(plan, partial); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingDeliveryPath(config)); !os.IsNotExist(err) {
		t.Fatal("source has a pending delivery or its state cannot be read")
	}
	if info, err := os.Stat(config.CaptureRoot); err != nil || !info.IsDir() {
		t.Fatal("source captures must exist")
	}
	// Open an existing lease read-only: never create production metadata.
	use, err := os.Open(libraryUseLock(config))
	if err != nil {
		t.Fatal(err)
	}
	defer use.Close()
	if err := filelock.Lock(use, filelock.Shared, filelock.Try); err != nil {
		t.Fatalf("source is moving, compacting or recovering: %v", err)
	}
	var required int64
	for _, item := range append(append([]backupItem{plan.catalog}, plan.trees...), plan.files...) {
		if err := filepath.WalkDir(item.Source, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path != item.Source && plan.exclude[canonical(path)] {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				required += info.Size()
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Committed pages may still live in the WAL and enlarge the snapshot.
	if info, err := os.Stat(plan.catalog.Source + "-wal"); err == nil {
		required += info.Size()
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Snapshot and live WAL share a drive. Leave room for normal writes as
	// well as the WAL growth the product snapshot explicitly permits.
	if err := ensureFree(filepath.Dir(destination), required+(20<<30)+2*backupWALGrowth); err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile(backupRecordPath(config))
	if err != nil {
		t.Fatal(err)
	}
	useDurableCatalog(t)
	if err := os.Mkdir(partial, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileDurably(filepath.Join(partial, "work-snapshot.json"), []byte("{\"version\":1,\"complete\":false}\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
	defer cancel()
	started := time.Now()
	last := ""
	catalog, _, err := cloudSnapshot(ctx, config, partial, func(phase string) {
		if phase != last {
			t.Logf("%s: %s", time.Since(started).Round(time.Second), phase)
			last = phase
		}
	})
	if err != nil {
		t.Fatalf("snapshot incomplete; retained at %s for inspection: %v", partial, err)
	}
	after, err := os.ReadFile(backupRecordPath(config))
	if err != nil || !bytes.Equal(history, after) {
		t.Fatalf("source backup history changed during snapshot: %v", err)
	}
	evidence := map[string]any{"version": 1, "complete": true, "basis": "product-online-snapshot", "source_config": source,
		"destination": destination, "catalog": catalog, "finished_at": time.Now().UTC().Format(time.RFC3339Nano),
		"seconds": time.Since(started).Seconds(), "same_drive_is_not_a_backup": true, "source_backup_history_unchanged": true,
		"manifest_host_basis": "TestMain synthetic host; source identity comes from the library metadata", "required_copy_bytes": required}
	raw, err := json.MarshalIndent(evidence, "", "  ")
	if err == nil {
		err = writeFileDurably(filepath.Join(partial, "work-snapshot.json"), append(raw, '\n'))
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(partial, destination); err != nil {
		t.Fatal(err)
	}
	t.Logf("completed consistent snapshot in %s", time.Since(started).Round(time.Second))
}

func workSnapshotDestination(plan backupPlan, destination string) error {
	if !filepath.IsAbs(destination) || canonical(destination) != destination {
		return fmt.Errorf("snapshot destination must be an absolute canonical path")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("snapshot destination must not exist")
	}
	if info, err := os.Stat(filepath.Dir(destination)); err != nil || !info.IsDir() {
		return fmt.Errorf("snapshot parent must already exist on the mounted destination drive")
	}
	for _, source := range plan.sources() {
		source = canonical(source)
		if within(destination, source) || within(source, destination) {
			return fmt.Errorf("snapshot destination overlaps source %s", source)
		}
	}
	return nil
}

func TestWorkSnapshotDestination(t *testing.T) {
	root := canonical(t.TempDir())
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	plan := backupPlan{library: backupLibrary{Path: source}, catalog: backupItem{Source: filepath.Join(source, "catalog.sqlite3")}}
	if err := workSnapshotDestination(plan, filepath.Join(root, "snapshot")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, filepath.Join(source, "snapshot"), root, "relative", filepath.Join(root, "missing", "snapshot")} {
		if err := workSnapshotDestination(plan, path); err == nil {
			t.Fatalf("unsafe destination accepted: %s", path)
		}
	}
}
