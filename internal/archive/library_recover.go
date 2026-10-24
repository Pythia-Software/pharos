package archive

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// RecoverLibrary publishes a separate, inactive candidate. It never opens a
// backup for writing and never reuses the original writable-instance identity.
func RecoverLibrary(ctx context.Context, backup, destination string, progress func(string)) (Config, error) {
	backup, destination = canonical(expandPath(backup)), canonical(expandPath(destination))
	if !filepath.IsAbs(destination) || destination == "/" || within(destination, backup) || within(backup, destination) {
		return Config{}, fmt.Errorf("choose a separate, new library folder")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("recovery destination already exists or is inaccessible: %s", destination)
	}
	var manifest backupManifest
	b, err := os.ReadFile(filepath.Join(backup, backupManifestName))
	var lock *os.File
	libraryCopy := os.IsNotExist(err)
	if libraryCopy {
		// A Finder copy has no backup manifest and intentionally fails the
		// writable-instance guard. Recovery reads that copy under an exclusive
		// use lease, snapshots SQLite including its WAL, and gives the candidate
		// a fresh identity. Absolute references outside the copy are refused.
		config, e := LoadConfig(filepath.Join(backup, libraryConfigName))
		if e != nil {
			return Config{}, e
		}
		if !config.Library {
			return Config{}, fmt.Errorf("use a complete backup to recover a legacy installation")
		}
		relative := func(path string) (string, error) {
			rel, e := filepath.Rel(backup, canonical(path))
			if e != nil || !filepath.IsLocal(rel) {
				return "", fmt.Errorf("copied library references files outside its folder; recover from a complete backup instead")
			}
			return filepath.ToSlash(rel), nil
		}
		rel, e := relative(config.CatalogPath)
		if e != nil {
			return Config{}, e
		}
		if _, e = os.Stat(config.CatalogPath); e != nil {
			return Config{}, e
		}
		manifest = backupManifest{Version: backupManifestVersion, Complete: true, Catalog: &backupCatalog{Path: rel}}
		for _, item := range []backupItem{{Name: "captures", Source: config.CaptureRoot}, {Name: "preserved", Source: config.ArchiveRoot}, {Name: "hosts", Source: filepath.Join(backup, hostsDirName)}, {Name: "library_identity", Source: descriptorPath(config)}} {
			item.Path, e = relative(item.Source)
			if e != nil {
				return Config{}, e
			}
			if _, e = os.Stat(item.Source); e == nil {
				manifest.Items = append(manifest.Items, item)
			} else if !os.IsNotExist(e) {
				return Config{}, e
			}
		}
		if e = checkCatalogCompatibility(config.CatalogPath); e != nil {
			return Config{}, e
		}
		lock, err = fileLease(libraryUseLock(config), true)
	} else {
		if err != nil {
			return Config{}, err
		}
		if err = json.Unmarshal(b, &manifest); err != nil {
			return Config{}, err
		}
		lock, err = os.Open(filepath.Join(backup, backupLockName))
		if err == nil {
			err = syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		}
	}
	if lock != nil {
		defer lock.Close()
	}
	if err != nil {
		return Config{}, fmt.Errorf("cannot lock recovery source; wait for active operations: %w", err)
	}
	if manifest.Version != backupManifestVersion || !manifest.Complete || len(manifest.Missing) > 0 || manifest.Catalog == nil {
		return Config{}, fmt.Errorf("recovery requires a complete, supported backup; this backup is partial or unfinished")
	}
	safe := func(rel string) (string, error) {
		if !filepath.IsLocal(rel) || strings.Contains(rel, "\\") {
			return "", fmt.Errorf("unsafe path in backup: %q", rel)
		}
		path := filepath.Join(backup, filepath.FromSlash(rel))
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		if canonical(path) != path || !within(resolved, backup) {
			return "", fmt.Errorf("backup path contains a symbolic link: %s", rel)
		}
		return path, nil
	}
	catalogPath, err := safe(manifest.Catalog.Path)
	if err != nil {
		return Config{}, err
	}
	if err = checkCatalogCompatibility(catalogPath); err != nil {
		return Config{}, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".pharos-recovery-")
	if err != nil {
		return Config{}, err
	}
	report := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	report("Restoring a separate candidate; automatic capture is paused")
	// A failed candidate is retained for inspection, never published as usable.
	configPath, err := writeLibraryConfig(stage, libraryVolumeIdentity, defaultLibrarySettings)
	if err != nil {
		return Config{}, err
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		return Config{}, err
	}
	if err = os.MkdirAll(filepath.Dir(config.CatalogPath), 0o700); err != nil {
		return Config{}, err
	}
	if libraryCopy {
		err = ensureCatalogWorkingSpace(filepath.Dir(config.CatalogPath), catalogPath)
		if err == nil {
			err = snapshotSQLite(ctx, catalogPath, config.CatalogPath)
		}
	} else {
		_, err = copyVerified(ctx, catalogPath, config.CatalogPath)
	}
	if err != nil {
		return Config{}, err
	}
	for _, item := range manifest.Items {
		var target string
		switch item.Name {
		case "captures":
			target = config.CaptureRoot
		case "preserved":
			target = config.ArchiveRoot
		case "hosts":
			target = filepath.Join(stage, hostsDirName)
		default:
			continue
		}
		path, err := safe(item.Path)
		if err != nil {
			if os.IsNotExist(err) && item.Files == 0 && item.Bytes == 0 {
				if err = os.MkdirAll(target, 0o700); err != nil {
					return Config{}, err
				}
				continue
			}
			return Config{}, err
		}
		if _, err = retainedTree(ctx, path, target, "", false); err != nil {
			return Config{}, err
		}
	}
	catalog, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		return Config{}, err
	}
	var integrity string
	err = catalog.DB.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity)
	if err == nil && integrity != "ok" {
		err = fmt.Errorf("backup catalog did not pass its integrity check: %s", integrity)
	}
	if err == nil {
		// Progress is valid only while the indexed/delivered payload still
		// exists. Reset both proofs and parser shortcuts so explicitly resumed
		// capture can recover surviving originals on the same Mac.
		_, err = catalog.DB.ExecContext(ctx, `DELETE FROM sync_settings; DELETE FROM automatic_source_states; DELETE FROM sync_deferred; DELETE FROM sync_recovery_jobs;
 DELETE FROM cloud_backup_schedules;
 DELETE FROM library_delivery_checkpoints; DELETE FROM library_workspace_delivery_checkpoints;
 DELETE FROM source_states; DELETE FROM source_record_states; DELETE FROM source_item_states; DELETE FROM source_terminal_states; DELETE FROM sync_generations;`)
	}
	if closeErr := catalog.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Config{}, err
	}
	descriptor, err := ensureLibraryDescriptor(config, filepath.Base(destination))
	if err != nil {
		return Config{}, err
	}
	descriptor.RecoveredFrom = manifest.Library.ID
	descriptor.RecoveryPoint = manifest.FinishedAt
	for _, item := range manifest.Items {
		if item.Name == "library_identity" {
			path, e := safe(item.Path)
			if e != nil {
				return Config{}, e
			}
			data, e := os.ReadFile(path)
			if e != nil {
				return Config{}, e
			}
			var prior LibraryDescriptor
			if e = json.Unmarshal(data, &prior); e != nil {
				return Config{}, e
			}
			if prior.Version > libraryFormatVersion {
				return Config{}, fmt.Errorf("this library copy requires a newer Pharos")
			}
			descriptor.RecoveredFrom = prior.ID
		}
	}
	if err = durableJSON(descriptorPath(config), descriptor); err != nil {
		return Config{}, err
	}
	if err = validateCaptureEvidence(config.CaptureRoot); err != nil {
		return Config{}, err
	}
	report("Validating recovered history and publishing candidate")
	if err = flushTree(stage); err != nil {
		return Config{}, err
	}
	if err = os.Rename(stage, destination); err != nil {
		return Config{}, err
	}
	if err = syncDirectory(filepath.Dir(destination)); err != nil {
		return Config{}, err
	}
	return LoadConfig(filepath.Join(destination, libraryConfigName))
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func validateCaptureEvidence(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link in capture evidence: %s", path)
		}
		if entry.IsDir() || entry.Name() != captureManifestName {
			return nil
		}
		manifest, err := loadCaptureManifest(filepath.Dir(path))
		if err != nil {
			return err
		}
		for _, file := range manifest.Files {
			if !filepath.IsLocal(file.Captured) {
				return fmt.Errorf("unsafe captured path")
			}
			digest, size, err := fileDigest(filepath.Join(filepath.Dir(path), filepath.FromSlash(file.Captured)))
			if err != nil || size != file.Size || (file.SHA256 != "" && digest != file.SHA256) {
				return fmt.Errorf("capture evidence failed verification: %s", file.Captured)
			}
		}
		for _, snapshot := range manifest.Snapshots {
			if !filepath.IsLocal(snapshot.Captured) {
				return fmt.Errorf("unsafe captured snapshot path")
			}
			info, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(snapshot.Captured)))
			if err != nil || info.Size() != snapshot.Size {
				return fmt.Errorf("captured snapshot failed verification: %s", snapshot.Captured)
			}
		}
		return nil
	})
}

func flushTree(root string) error {
	dirs := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		return syncFile(path)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = syncDirectory(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func runLibraryRecoverCLI(args []string) error {
	f := flag.NewFlagSet("library-recover", flag.ContinueOnError)
	backup := f.String("backup", "", "complete backup folder")
	destination := f.String("to", "", "new candidate folder")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *backup == "" || *destination == "" {
		return fmt.Errorf("usage: pharos library-recover --backup DIR --to NEW_DIR")
	}
	config, err := RecoverLibrary(context.Background(), *backup, *destination, func(s string) { fmt.Fprintln(os.Stderr, s) })
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"config": config.Path, "capture_enabled": false})
}
