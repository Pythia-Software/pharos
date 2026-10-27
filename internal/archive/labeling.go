package archive

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// stringList is a repeatable flag.
type stringList []string

func (l *stringList) String() string         { return strings.Join(*l, ",") }
func (l *stringList) Set(value string) error { *l = append(*l, value); return nil }

const labelingUsage = `usage:
  pharos [--config PATH] labeling prepare --dir DIR [--catalog PATH] [--fresh-days 14] [--limit-conversations N] [--conversation ID ...] [--splits-from OLD_DIR] [--snapshot-copy] [--replace] [--dry-run]
  pharos labeling plan --dir DIR --route claude|codex --model M --effort E [--items-per-request 4] [--max-request-chars N] [--concurrency 3] [--cohort C ...] [--split S ...] [--limit-items N]
  pharos labeling status --dir DIR`

// runLabelingCLI runs the offline labeling commands. None of them opens the
// catalog through OpenCatalog: prepare reads one read-only snapshot, and plan
// and status read only the dataset.
func runLabelingCLI(configPath string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(labelingUsage)
	}
	switch args[0] {
	case "prepare":
		return runLabelingPrepare(configPath, args[1:], stdout, stderr)
	case "plan":
		return runLabelingPlan(args[1:], stdout)
	case "status":
		flags := flag.NewFlagSet("labeling status", flag.ContinueOnError)
		dir := flags.String("dir", "", "dataset directory")
		if err := flags.Parse(args[1:]); err != nil || *dir == "" || flags.NArg() != 0 {
			return errors.New(labelingUsage)
		}
		return labelingStatus(*dir, stdout)
	}
	return errors.New(labelingUsage)
}

func runLabelingPrepare(configPath string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("labeling prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "dataset directory (created; must be empty unless --replace)")
	catalogPath := flags.String("catalog", "", "catalog to read instead of the configured library's")
	freshDays := flags.Int("fresh-days", 14, "families whose first activity is this recent form the fresh holdout")
	limit := flags.Int("limit-conversations", 0, "prepare a deterministic sample of this many conversations")
	var conversations stringList
	flags.Var(&conversations, "conversation", "prepare only this conversation (repeatable)")
	replace := flags.Bool("replace", false, "replace a dataset already in DIR")
	splitsFrom := flags.String("splits-from", "", "keep the splits an earlier dataset gave its conversations")
	dryRun := flags.Bool("dry-run", false, "print the manifest counts without writing anything")
	snapshotCopy := flags.Bool("snapshot-copy", false, "the catalog is a static copy (a backup): read it immutably, without its WAL or shared memory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*dir == "" && !*dryRun) || *freshDays < 0 || *limit < 0 {
		return errors.New(labelingUsage)
	}
	if *limit > 0 && len(conversations) > 0 {
		return errors.New("use --limit-conversations or --conversation, not both")
	}
	if *catalogPath == "" {
		config, err := LoadConfig(configPath)
		if err != nil {
			return err
		}
		if err := config.checkLibrary(volumeIdentity); err != nil {
			return err
		}
		use, err := labelingLibraryLease(config, *dryRun)
		if err != nil {
			return err
		}
		if use != nil {
			defer use.Close()
		}
		*catalogPath = config.CatalogPath
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// SQLite URIs need absolute paths.
	if *dir != "" {
		*dir, _ = filepath.Abs(expandPath(*dir))
	}
	*catalogPath, _ = filepath.Abs(expandPath(*catalogPath))
	opts := labelingPrepareOptions{Dir: *dir, CatalogPath: *catalogPath, FreshDays: *freshDays, Limit: *limit, Conversations: conversations,
		DryRun: *dryRun, SnapshotCopy: *snapshotCopy, Progress: stderr}
	if *splitsFrom != "" {
		opts.SplitsFrom, _ = filepath.Abs(expandPath(*splitsFrom))
	}
	if *dryRun {
		manifest, err := prepareLabeling(ctx, opts)
		if err != nil {
			return err
		}
		return writeLabelingJSON(stdout, manifest)
	}
	manifest, err := prepareLabelingDataset(ctx, opts, *replace)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, manifest["population"])
	fmt.Fprintf(stdout, "Wrote %s\n", filepath.Join(opts.Dir, labelingManifestFile))
	return nil
}

// labelingLibraryLease takes a shared lease on the library, like other
// readers, so a library move waits for preparation. A dry run writes nothing:
// it leases only an existing lock file and never creates one.
func labelingLibraryLease(config Config, dryRun bool) (*os.File, error) {
	path := libraryUseLock(config)
	if !dryRun {
		return fileLease(path, false)
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("Pharos is using this library; close its active operation and retry: %w", err)
	}
	return file, nil
}

func writeLabelingJSON(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// labelingDatasetFiles are the files preparation owns in a dataset
// directory; --replace removes only these.
var labelingDatasetFiles = []string{labelingManifestFile, labelingIncompleteFile, labelingDatasetFile, labelingDatasetFile + "-wal", labelingDatasetFile + "-shm"}

// prepareLabelingDataset prepares into dir under its lock. The manifest is
// published last, atomically, so a dataset without one is incomplete; the
// marker file says what was attempted and why it stopped.
func prepareLabelingDataset(ctx context.Context, opts labelingPrepareOptions, replace bool) (map[string]any, error) {
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := fileLease(filepath.Join(opts.Dir, labelingLockFile), true)
	if err != nil {
		return nil, fmt.Errorf("another labeling command is using %s", opts.Dir)
	}
	defer lock.Close()
	entries, err := os.ReadDir(opts.Dir)
	if err != nil {
		return nil, err
	}
	others := []string{}
	for _, entry := range entries {
		if entry.Name() == labelingLockFile {
			continue
		}
		if !replace {
			return nil, fmt.Errorf("%s is not empty; pass --replace to replace its dataset", opts.Dir)
		}
		if !slices.Contains(labelingDatasetFiles, entry.Name()) {
			others = append(others, entry.Name())
		}
	}
	if len(others) > 0 {
		return nil, fmt.Errorf("%s holds files a dataset does not (%s); move them or choose another directory", opts.Dir, strings.Join(others, ", "))
	}
	// Earlier splits are read before a replacement removes their dataset.
	if opts.SplitsFrom != "" {
		if opts.splits, err = loadLabelingSplits(opts.SplitsFrom); err != nil {
			return nil, fmt.Errorf("--splits-from: %w", err)
		}
	}
	// The manifest goes first, so an interrupted replacement is incomplete.
	for _, name := range labelingDatasetFiles {
		if err := os.Remove(filepath.Join(opts.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	marker := filepath.Join(opts.Dir, labelingIncompleteFile)
	attempt := map[string]any{"state": "preparing", "started_at": now(), "pid": os.Getpid(), "catalog": opts.CatalogPath,
		"snapshot_copy": opts.SnapshotCopy, "limit_conversations": opts.Limit, "conversations": opts.Conversations, "fresh_days": opts.FreshDays}
	if err := durableJSON(marker, attempt); err != nil {
		return nil, err
	}
	manifest, err := prepareLabeling(ctx, opts)
	if err != nil {
		attempt["state"], attempt["failed_at"], attempt["error"] = "failed", now(), err.Error()
		if ctx.Err() != nil {
			attempt["state"] = "interrupted"
		}
		_ = durableJSON(marker, attempt)
		return nil, err
	}
	if err := durableJSON(filepath.Join(opts.Dir, labelingManifestFile), manifest); err != nil {
		return nil, err
	}
	return manifest, os.Remove(marker)
}

// labelingStatus summarizes a dataset directory.
func labelingStatus(dir string, w io.Writer) error {
	data, err := os.ReadFile(filepath.Join(dir, labelingManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		if marker, err := os.ReadFile(filepath.Join(dir, labelingIncompleteFile)); err == nil {
			fmt.Fprintf(w, "Preparation incomplete: no manifest was published.\n%s", marker)
			return nil
		}
		fmt.Fprintf(w, "No prepared dataset in %s.\n", dir)
		return nil
	}
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("read %s: %w", labelingManifestFile, err)
	}
	snapshot, _ := manifest["snapshot"].(map[string]any)
	fmt.Fprintf(w, "Prepared dataset (%s)\n%s\nSnapshot %s of %s\n", labelingUnitsVersion, manifest["population"], snapshot["started_at"], snapshot["catalog"])
	fmt.Fprintf(w, "Units by cohort: %s\n", compactJSON(manifest["cohorts"]))
	fmt.Fprintf(w, "Items: %v; long units: %s; excluded records: %v\n", manifest["items"], compactJSON(manifest["long"]), manifest["excluded_records"])
	return nil
}

func compactJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
