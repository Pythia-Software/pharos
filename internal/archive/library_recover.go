package archive

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// RecoverLibrary publishes a separate, inactive candidate. It never writes
// backup payloads or reuses their writable-instance identity. A
// downloaded cloud backup may need a fresh coordination lock file.
func RecoverLibrary(ctx context.Context, backup, destination string, progress func(string)) (Config, error) {
	backup, destination = canonical(expandPath(backup)), canonical(expandPath(destination))
	if !filepath.IsAbs(destination) || destination == "/" || within(destination, backup) || within(backup, destination) {
		return Config{}, fmt.Errorf("choose a separate, new library folder")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("recovery destination already exists or is inaccessible: %s", destination)
	}
	var manifest backupManifest
	var repositoryConfig Config
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
		repositoryConfig = config
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
		lock, err = os.OpenFile(filepath.Join(backup, backupLockName), os.O_RDONLY|os.O_CREATE, 0o600)
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
	if !libraryCopy {
		for _, item := range manifest.Items {
			if item.Name != "config" {
				continue
			}
			path, err := safe(item.Path)
			if err != nil {
				return Config{}, err
			}
			repositoryConfig, err = loadConfig(path, false)
			if err != nil {
				return Config{}, err
			}
		}
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
	// Repository identity choices travel with the library. Carry only these
	// sections into the fresh config, leaving paths and credentials reset.
	text, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, err
	}
	var settings strings.Builder
	if len(repositoryConfig.RepositoryAliases) > 0 {
		settings.WriteString("\n[repositories.aliases]\n")
		keys := make([]string, 0, len(repositoryConfig.RepositoryAliases))
		for key := range repositoryConfig.RepositoryAliases {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&settings, "%q = %q\n", key, repositoryConfig.RepositoryAliases[key])
		}
	}
	if len(repositoryConfig.RepositorySeparate) > 0 {
		settings.WriteString("\n[repositories.separate]\n")
		for _, id := range repositoryConfig.RepositorySeparate {
			fmt.Fprintf(&settings, "%q = true\n", id)
		}
	}
	if err = writeFileAtomic(configPath, append(text, settings.String()...), 0o600); err != nil {
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
 DELETE FROM source_input_states; DELETE FROM source_states; DELETE FROM source_record_states; DELETE FROM source_item_states; DELETE FROM source_terminal_states; DELETE FROM sync_generations;`)
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
	if err = validateCaptureEvidence(ctx, config.CaptureRoot); err != nil {
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

// Every manifest reference is part of the recovery contract, including old
// file versions, database generations and nested immutable transfer inventories.
func validateCaptureEvidence(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link in capture evidence: %s", path)
		}
		if entry.IsDir() {
			if filepath.Base(filepath.Dir(path)) == ".imports" {
				// Interrupted private staging has never authorized source cleanup.
				// Published import IDs are hashes and cannot use this prefix.
				if strings.HasPrefix(entry.Name(), ".stage-") {
					return filepath.SkipDir
				}
				receipt, err := readTransferReceipt(filepath.Join(path, "transfer.json"))
				if err != nil {
					return fmt.Errorf("invalid retained import %s: %w", path, err)
				}
				if receipt.ID != entry.Name() {
					return fmt.Errorf("retained import identity does not match its directory: %s", path)
				}
				if err = verifyTransferPayloadContext(ctx, path, receipt); err != nil {
					return fmt.Errorf("retained import %s: %w", path, err)
				}
			}
			return nil
		}
		if entry.Name() != captureManifestName {
			return nil
		}
		dir := filepath.Dir(path)
		manifest, err := loadCaptureManifest(dir)
		if err != nil {
			return err
		}
		if err = validateRetainedRecords(ctx, dir, manifest); err != nil {
			return fmt.Errorf("retained records in %s: %w", dir, err)
		}
		verify := func(rel string, size int64, checksum string) error {
			if !filepath.IsLocal(rel) || rel == "." || strings.Contains(rel, "\\") {
				return fmt.Errorf("unsafe captured evidence path: %q", rel)
			}
			file := filepath.Join(dir, filepath.FromSlash(rel))
			info, err := os.Lstat(file)
			if err != nil {
				return fmt.Errorf("missing captured evidence %s: %w", file, err)
			}
			if !info.Mode().IsRegular() || info.Size() != size {
				return fmt.Errorf("captured evidence failed verification: %s", file)
			}
			if checksum != "" {
				digest, _, err := fileDigestContext(ctx, file)
				if err != nil {
					return err
				}
				if digest != checksum {
					return fmt.Errorf("captured evidence failed verification: %s", file)
				}
			}
			return nil
		}
		for _, file := range manifest.Files {
			if err = verify(file.Captured, file.Size, file.SHA256); err != nil {
				return err
			}
			for _, previous := range file.Previous {
				if err = verify(previous.Captured, previous.Size, previous.SHA256); err != nil {
					return err
				}
			}
		}
		for _, snapshot := range manifest.Snapshots {
			if err = verify(snapshot.Captured, snapshot.Size, ""); err != nil {
				return err
			}
			for _, generation := range snapshot.Generations {
				// An encoded generation must be its stored size on disk and
				// decode to the original size and digest. A compression killed
				// between its manifest and its rename left an entry marked
				// encoded over the original bytes; that is checked as plain.
				// The tree may be live, so one handle is classified and read.
				if !filepath.IsLocal(generation.Captured) || strings.Contains(generation.Captured, "\\") {
					return fmt.Errorf("unsafe captured evidence path: %q", generation.Captured)
				}
				file := filepath.Join(dir, filepath.FromSlash(generation.Captured))
				if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() {
					return fmt.Errorf("missing captured evidence %s: %v", file, err)
				}
				f, err := os.Open(file)
				if err != nil {
					return fmt.Errorf("missing captured evidence %s: %w", file, err)
				}
				generation = storedAsUnlocked(f, generation)
				info, err := f.Stat()
				stored := generation.Size
				if generation.Encoding != "" {
					stored = generation.StoredSize
				}
				var digest string
				var size int64
				if err == nil && info.Size() == stored && (generation.Encoding != "" || generation.SHA256 != "") {
					digest, size, err = capturedSHA256File(ctx, f, generation.Encoding)
				} else if err == nil {
					size = info.Size()
					if generation.Encoding != "" {
						size = -1
					}
				}
				f.Close()
				if err != nil || info.Size() != stored || size != generation.Size || generation.SHA256 != "" && digest != generation.SHA256 {
					return fmt.Errorf("captured evidence failed verification: %s", file)
				}
			}
		}
		return nil
	})
}

// A copied native snapshot is no longer the only preservation product. Check
// every published row-evidence reference, including heads and group receipts,
// before exposing the recovered library. Partial acquisitions remain evidence
// of incomplete work and do not claim a complete closure.
func validateRetainedRecords(ctx context.Context, dir string, manifest *captureManifest) error {
	if err := checkSQLiteRecordCompatibility(dir); err != nil {
		return err
	}
	pins, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	for _, pin := range pins.Pins {
		if pin.ReleasedAt != "" {
			continue
		}
		digest, size, err := capturedSHA256(ctx, filepath.Join(dir, filepath.FromSlash(pin.Path)), pin.Encoding)
		if err != nil {
			return err
		}
		if digest != pin.SHA256 || size != pin.Size {
			return fmt.Errorf("preservation pin failed verification: %s", pin.Path)
		}
		for _, dependency := range pin.Dependencies {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(dependency))); err != nil {
				return err
			}
		}
	}
	store := newRecordStore(&sourceCapture{ctx: ctx, dir: dir, manifest: manifest, source: SourceConfig{Kind: manifest.Source.Kind}})
	verified := map[recordObject]bool{}
	verify := func(ref recordObject) error {
		if verified[ref] {
			return nil
		}
		if err := store.verify(ref); err != nil {
			return err
		}
		verified[ref] = true
		return nil
	}
	return filepath.WalkDir(filepath.Join(dir, recordDir), func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == filepath.Join(dir, recordDir) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(filepath.Join(dir, recordDir), path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch parts[0] {
		case "entities":
			if entry.Name() == "head.json" {
				var head sqliteRecordHead
				if err = historyReadJSON(path, &head); err != nil {
					return err
				}
				if head.Version != recordFormat {
					return fmt.Errorf("unsupported SQLite head; upgrade Pharos")
				}
				return verify(head.Revision)
			}
			var ref recordObject
			if err = historyReadJSON(path, &ref); err != nil {
				return err
			}
			return verify(ref)
		case "groups":
			var group sqliteRecordGroup
			if err = historyReadJSON(path, &group); err != nil {
				return err
			}
			if group.Version != recordFormat {
				return fmt.Errorf("unsupported SQLite group; upgrade Pharos")
			}
			for _, ref := range group.Revisions {
				if err = verify(ref); err != nil {
					return err
				}
			}
		case "receipts":
			var receipt sqliteExportReceipt
			if err = historyReadJSON(path, &receipt); err != nil {
				return err
			}
			if receipt.Version != recordFormat {
				return fmt.Errorf("unsupported SQLite receipt; upgrade Pharos")
			}
			return verify(receipt.Revision)
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
	// The destination is new, so no library's service can see it; the
	// broker lists it from the app's lease directory (§6.5).
	return runCLIOperationIn([]string{appLeaseDir()}, []string{expandPath(*destination)}, "library-recover", OperationSpec{Kind: "library.recover"}, func(ctx context.Context, op *Op) error {
		config, err := RecoverLibrary(ctx, *backup, *destination, func(s string) { fmt.Fprintln(os.Stderr, s); op.Step(s, "", -1) })
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"config": config.Path, "capture_enabled": false})
	})
}
