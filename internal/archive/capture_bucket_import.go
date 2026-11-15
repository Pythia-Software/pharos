package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// bucket-import collects what `pharos capture-push` uploaded
// (docs/capture-push.md) into this library's capture root, where each
// pushing machine is one more captured host. Indexing is unchanged: it
// already indexes every host under the root.
//
// Collection only adds. An export is rebuilt in a scratch folder and
// verified as a whole before anything in the capture root changes, and an
// export that lacks a file an earlier one held is refused.

// bucketImportMarker, in a host's folder, records that the host's captures
// were collected from a bucket. A host folder without one was captured by a
// Mac and is never merged into.
const bucketImportMarker = ".bucket-import.json"

const bucketImportsDir = ".bucket-imports"

// bucketImportCheckTimeout bounds the one read that proves a configuration
// before it is saved.
var bucketImportCheckTimeout = 30 * time.Second

type bucketImportedHost struct {
	Pusher      string         `json:"pusher"`
	Destination string         `json:"destination"`
	Owner       string         `json:"owner,omitempty"`
	Pool        string         `json:"pool,omitempty"`
	Platform    *pushPlatform  `json:"platform,omitempty"`
	Initiator   *pushInitiator `json:"initiator,omitempty"`
	ImportedAt  string         `json:"imported_at"`
}

// applyBucketImport gives a host collected from a bucket its machine and
// channel. Every host of a pool is one machine, so a thousand short-lived
// sandboxes are one value of Machine; a host outside any pool is a machine
// of its own. Neither is guessed from a hostname.
func applyBucketImport(hostDir string, host *Host) {
	data, err := os.ReadFile(filepath.Join(hostDir, bucketImportMarker))
	var marker bucketImportedHost
	if err != nil || json.Unmarshal(data, &marker) != nil || marker.Pusher == "" {
		return
	}
	host.machineKind, host.machineLabel = "cloud", host.Label
	host.MachineID = stableID("machine", "pusher", marker.Pusher)
	if marker.Pool != "" {
		host.MachineID, host.machineLabel = stableID("machine", "pool", marker.Destination, marker.Pool), marker.Pool
	}
	if marker.Platform != nil {
		evidence, _ := json.Marshal(map[string]any{"platform": marker.Platform, "initiator": marker.Initiator, "owner": marker.Owner})
		host.channel, host.channelEvidence = marker.Platform.Name, string(evidence)
	}
}

// bucketImportState is <capture root>/.bucket-imports/<destination>/state.json.
type bucketImportState struct {
	// Handled maps exports/<pusher>/<source> to the last export collected
	// from it and that export's files, which the next one may reuse.
	Handled map[string]bucketImportCursor `json:"handled"`
}

type bucketImportCursor struct {
	Key   string            `json:"key"`
	Host  string            `json:"host"`
	Files map[string]string `json:"files"` // export path -> SHA-256
}

type bucketImportResult struct {
	Prefix  string                 `json:"prefix"`
	Exports []bucketImportedExport `json:"exports"`
	Hosts   []string               `json:"hosts"`
	Errors  []string               `json:"errors,omitempty"`
}

type bucketImportedExport struct {
	Key      string `json:"key"`
	Pusher   string `json:"pusher"`
	Host     string `json:"host"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Files    int    `json:"files"`
	Written  int    `json:"written"`
	Fetched  int64  `json:"bytes_fetched"`
	Platform string `json:"platform,omitempty"`
	Owner    string `json:"owner,omitempty"`
}

// openPushReadStore opens the bucket to read; tests replace it.
var openPushReadStore = newPushReadStore

// importBucket collects every pusher's newest export under the read
// configuration's prefix into captureRoot.
func importBucket(ctx context.Context, captureRoot, readConfig string) (bucketImportResult, error) {
	var result bucketImportResult
	config, err := readPushConfig(readConfig)
	if err != nil {
		return result, err
	}
	if strings.Contains(config.Prefix, "{owner}") {
		return result, errors.New("a read configuration names one owner's folder; replace {owner} in its prefix")
	}
	if info, err := os.Stat(captureRoot); err != nil || !info.IsDir() {
		return result, fmt.Errorf("capture root %s is unavailable; is its drive mounted?", captureRoot)
	}
	store, err := openPushReadStore(config, filepath.Dir(readConfig))
	if err != nil {
		return result, err
	}
	d := config.Destination
	destination := stableID("push-destination", d.Type, d.Bucket, d.Endpoint, d.Account, d.Container, d.URLTemplate, config.Prefix)
	stateDir := filepath.Join(captureRoot, bucketImportsDir, destination)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return result, err
	}
	state := bucketImportState{}
	statePath := filepath.Join(stateDir, "state.json")
	if data, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return result, fmt.Errorf("%s is damaged: %w", statePath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, err
	}
	if state.Handled == nil {
		state.Handled = map[string]bucketImportCursor{}
	}

	result.Prefix, result.Exports, result.Hosts = config.Prefix, []bucketImportedExport{}, []string{}
	exports := pushKey(config.Prefix, "exports") + "/"
	keys, err := store.list(ctx, exports)
	if err != nil {
		return result, fmt.Errorf("listing %s: %w", exports, err)
	}
	// Keys sort by time within a pusher's source, and each export holds
	// everything its predecessors did: the last one is the one to collect.
	newest := map[string]string{}
	for _, key := range keys {
		parts := strings.Split(strings.TrimPrefix(key, exports), "/")
		if len(parts) != 3 || !strings.HasSuffix(parts[2], ".json") {
			continue
		}
		newest[parts[0]+"/"+parts[1]] = key
	}
	chains := make([]string, 0, len(newest))
	for chain := range newest {
		chains = append(chains, chain)
	}
	slices.Sort(chains)
	for _, chain := range chains {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		key := newest[chain]
		if state.Handled[chain].Key == key {
			continue
		}
		imported, cursor, err := importBucketExport(ctx, store, config.Prefix, key, chain, captureRoot, stateDir, destination, state.Handled[chain])
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		state.Handled[chain] = cursor
		data, err := json.Marshal(state)
		if err != nil {
			return result, err
		}
		if err := writeFileAtomic(statePath, data, 0o600); err != nil {
			return result, err
		}
		result.Exports = append(result.Exports, imported)
		if !slices.Contains(result.Hosts, imported.Host) {
			result.Hosts = append(result.Hosts, imported.Host)
		}
	}
	if len(result.Errors) > 0 {
		return result, fmt.Errorf("%d of %d exports could not be collected", len(result.Errors), len(result.Errors)+len(result.Exports))
	}
	return result, nil
}

func importBucketExport(ctx context.Context, store pushReadStore, prefix, key, chain, captureRoot, stateDir, destination string, last bucketImportCursor) (bucketImportedExport, bucketImportCursor, error) {
	var imported bucketImportedExport
	data, err := store.get(ctx, key)
	if err != nil {
		return imported, last, err
	}
	var doc pushExportDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return imported, last, fmt.Errorf("it is not a pushed export: %w", err)
	}
	pusher, source, _ := strings.Cut(chain, "/")
	if doc.Pusher != pusher || doc.Envelope.SourceName != source {
		return imported, last, fmt.Errorf("it says it is %s/%s", doc.Pusher, doc.Envelope.SourceName)
	}
	host := doc.Envelope.HostID
	if last.Host != "" && last.Host != host {
		return imported, last, fmt.Errorf("this pusher's host changed from %s to %s", last.Host, host)
	}
	scratch, err := os.MkdirTemp(stateDir, "export-*")
	if err != nil {
		return imported, last, err
	}
	defer os.RemoveAll(scratch)
	imported = bucketImportedExport{Key: key, Pusher: pusher, Host: host, Source: source, Kind: doc.Envelope.SourceKind, Owner: doc.Owner}
	if doc.Platform != nil {
		imported.Platform = doc.Platform.Name
	}
	// A file the last export already brought, unchanged, is copied from the
	// capture root instead of fetched again.
	local := func(file CaptureExportFile) string {
		rel, ok := strings.CutPrefix(file.Path, captureExportCaptures+"/")
		if !ok || last.Files[file.Path] != file.SHA256 || !fs.ValidPath(rel) {
			return ""
		}
		return filepath.Join(captureRoot, filepath.FromSlash(rel))
	}
	get := func(ctx context.Context, key string) ([]byte, error) {
		data, err := store.get(ctx, key)
		imported.Fetched += int64(len(data))
		return data, err
	}
	envelope, err := assemblePushExport(ctx, get, local, prefix, doc, scratch)
	if err != nil {
		return imported, last, err
	}
	marker := bucketImportedHost{Pusher: pusher, Destination: destination, Owner: doc.Owner, Pool: doc.Pool, Platform: doc.Platform, Initiator: doc.Initiator, ImportedAt: now()}
	written, err := mergeCaptureExport(ctx, captureRoot, scratch, envelope, marker)
	if err != nil {
		return imported, last, err
	}
	imported.Files, imported.Written = len(envelope.Files), written
	cursor := bucketImportCursor{Key: key, Host: host, Files: map[string]string{}}
	for _, file := range envelope.Files {
		cursor.Files[file.Path] = file.SHA256
	}
	return imported, cursor, nil
}

// errCaptureImportRegression refuses an export that would leave the
// library with less than it has.
var errCaptureImportRegression = errors.New("the export lacks files this library already collected from the same machine")

// mergeCaptureExport adds the verified export in exportDir to captureRoot
// as its host's capture of its source, and returns how many files it wrote.
// It never removes a file: an export that lacks one the capture holds is
// refused whole. The manifest is written last, so an interrupted merge
// leaves files the old manifest does not yet describe and the next run
// completes it.
func mergeCaptureExport(ctx context.Context, captureRoot, exportDir string, envelope CaptureExport, marker bucketImportedHost) (int, error) {
	host, source := envelope.HostID, envelope.SourceName
	for _, name := range []string{host, source} {
		if !validCaptureName(name) || exportSegmentProblem(name) != "" {
			return 0, fmt.Errorf("%q cannot name a captured host or source", name)
		}
	}
	if host == currentHost().ID {
		return 0, errors.New("the export claims to come from this Mac")
	}
	hostDir := filepath.Join(captureRoot, host)
	markerPath := filepath.Join(hostDir, bucketImportMarker)
	if _, err := os.Stat(hostDir); err == nil {
		data, err := os.ReadFile(markerPath)
		var existing bucketImportedHost
		if err != nil || json.Unmarshal(data, &existing) != nil {
			return 0, fmt.Errorf("host %s already has a capture that was not collected from a bucket", host)
		}
		if existing.Pusher != marker.Pusher {
			return 0, fmt.Errorf("host %s was collected from pusher %s, not %s", host, existing.Pusher, marker.Pusher)
		}
	}
	sourceDir := filepath.Join(hostDir, source)
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		return 0, err
	}
	lock, err := lockCaptureTarget(ctx, sourceDir)
	if err != nil {
		return 0, err
	}
	defer releaseLockFile(lock)

	prefix := captureExportCaptures + "/" + host + "/"
	incoming := map[string]CaptureExportFile{} // path below the host folder
	for _, file := range envelope.Files {
		rel, ok := strings.CutPrefix(file.Path, prefix)
		if !ok || !fs.ValidPath(rel) {
			return 0, fmt.Errorf("the export holds %s, outside its host", file.Path)
		}
		incoming[rel] = file
	}
	// Nothing the capture holds may be missing from what replaces it.
	err = filepath.WalkDir(sourceDir, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if name := entry.Name(); strings.HasPrefix(name, ".") || strings.HasSuffix(name, captureTempSuffix) {
			return nil
		}
		rel, err := filepath.Rel(hostDir, file)
		if err != nil {
			return err
		}
		if _, ok := incoming[filepath.ToSlash(rel)]; !ok {
			return fmt.Errorf("%w: %s", errCaptureImportRegression, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	manifest := source + "/" + captureManifestName
	order := make([]string, 0, len(incoming))
	for rel := range incoming {
		if rel != manifest {
			order = append(order, rel)
		}
	}
	slices.Sort(order)
	if _, ok := incoming[manifest]; !ok {
		return 0, errors.New("the export has no manifest")
	}
	order = append(order, manifest)
	written := 0
	for _, rel := range order {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		file := incoming[rel]
		target := filepath.Join(hostDir, filepath.FromSlash(rel))
		if sum, size, err := fileSHA256(target); err == nil && size == file.Size && sum == file.SHA256 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return written, err
		}
		if err := copyFileDurably(filepath.Join(exportDir, filepath.FromSlash(file.Path)), target); err != nil {
			return written, err
		}
		written++
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return written, err
	}
	return written, writeFileAtomic(markerPath, append(data, '\n'), 0o600)
}

// copyFileDurably puts a copy of source at target, replacing it at once.
func copyFileDurably(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	temporary := target + captureTempSuffix
	out, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, target)
	}
	if err != nil {
		os.Remove(temporary)
	}
	return err
}

// runBucketImportCLI is pharos bucket-import: collect, then say which
// hosts to index.
func runBucketImportCLI(config Config, args []string) error {
	usage := errors.New("usage: pharos bucket-import [--from READ_CONFIG [--save]] | --list | --remove ID")
	flags := flag.NewFlagSet("bucket-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from", "", "read configuration (pharos-read.json)")
	save := flags.Bool("save", false, "keep the configuration for scheduled collection")
	list := flags.Bool("list", false, "list saved buckets")
	remove := flags.String("remove", "", "forget a saved bucket")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *save && *from == "" {
		return usage
	}
	switch {
	case *list:
		return printJSON(map[string]any{"buckets": savedBucketImports(config)})
	case *remove != "":
		return removeBucketImport(config, *remove)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCLIOperation(config, "bucket-import", OperationSpec{Kind: "capture.import", Variant: "bucket"}, func(context.Context, *Op) error {
		if *from == "" {
			results, _, err := importSavedBuckets(ctx, config)
			if printErr := printJSON(results); err == nil {
				err = printErr
			}
			return err
		}
		path := expandPath(*from)
		if *save {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			saved, err := saveBucketImport(ctx, config, raw)
			if err != nil {
				return err
			}
			path = saved.path
		}
		result, err := importBucket(ctx, config.CaptureRoot, path)
		if printErr := printJSON(result); err == nil {
			err = printErr
		}
		return err
	})
}

// Saved read configurations: the buckets the Background Processing item
// "Collect from other machines" reads. They hold this Mac's credentials, so
// they live in its support folder, not in the library.

type savedBucketImport struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Bucket string `json:"bucket,omitempty"`
	Prefix string `json:"prefix"`
	// Machines counts the machines collected from so far, and CollectedAt is
	// when the last collection finished; both come from the import's state.
	Machines    int    `json:"machines"`
	CollectedAt string `json:"collected_at,omitempty"`
	// Waiting counts the machines that have checked in, known only when the
	// configuration has just been checked against the bucket.
	Waiting     *int `json:"checked_in,omitempty"`
	path        string
	destination string
}

// collected fills in what the import's state says about a saved bucket.
func (saved *savedBucketImport) collected(captureRoot string) {
	path := filepath.Join(captureRoot, bucketImportsDir, saved.ID, "state.json")
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	saved.CollectedAt = info.ModTime().UTC().Format(time.RFC3339)
	var state bucketImportState
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &state) == nil {
		machines := map[string]bool{}
		for key := range state.Handled {
			pusher, _, _ := strings.Cut(key, "/")
			machines[pusher] = true
		}
		saved.Machines = len(machines)
	}
}

func bucketImportStore(config Config) string {
	return filepath.Join(pharosSupportDir(), "bucket-imports", stableID("capture-root", config.CaptureRoot))
}

func savedBucketImports(config Config) []savedBucketImport {
	saved := []savedBucketImport{}
	entries, _ := os.ReadDir(bucketImportStore(config))
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || entry.IsDir() {
			continue
		}
		path := filepath.Join(bucketImportStore(config), entry.Name())
		read, err := readPushConfig(path)
		if err != nil {
			continue
		}
		d := read.Destination
		one := savedBucketImport{ID: id, Type: d.Type, Bucket: firstString(d.Bucket, d.Container), Prefix: read.Prefix, path: path}
		one.collected(config.CaptureRoot)
		saved = append(saved, one)
	}
	return saved
}

// saveBucketImport checks a read configuration, reads the bucket with it
// once, and keeps it for the background item. A configuration that cannot
// read the bucket is not kept. The same bucket and prefix replace their
// earlier entry.
func saveBucketImport(ctx context.Context, config Config, raw []byte) (savedBucketImport, error) {
	dir := bucketImportStore(config)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return savedBucketImport{}, err
	}
	scratch, err := os.CreateTemp(dir, ".check-*")
	if err != nil {
		return savedBucketImport{}, err
	}
	defer os.Remove(scratch.Name())
	_, err = scratch.Write(raw)
	if closeErr := scratch.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return savedBucketImport{}, err
	}
	read, err := readPushConfig(scratch.Name())
	if err != nil {
		return savedBucketImport{}, errors.New(strings.TrimPrefix(err.Error(), scratch.Name()+": "))
	}
	d := read.Destination
	switch {
	case read.Access != "read":
		return savedBucketImport{}, errors.New(`a library needs the read configuration ("access": "read")`)
	case strings.Contains(read.Prefix, "{owner}"):
		return savedBucketImport{}, errors.New("a read configuration names one owner's folder; replace {owner} in its prefix")
	}
	for _, file := range []string{d.SecretAccessKey.file, d.ServiceAccountKey.file, d.SASURL.file, d.Header.file} {
		if file != "" && !filepath.IsAbs(expandPath(file)) {
			return savedBucketImport{}, fmt.Errorf("give %s as an absolute path", file)
		}
	}
	for _, token := range []*pushIdentityToken{d.IdentityToken, d.AccessToken} {
		if token != nil && token.File != "" && !filepath.IsAbs(expandPath(token.File)) {
			return savedBucketImport{}, fmt.Errorf("give %s as an absolute path", token.File)
		}
	}
	store, err := openPushReadStore(read, dir)
	if err != nil {
		return savedBucketImport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, bucketImportCheckTimeout)
	defer cancel()
	hello := pushKey(read.Prefix, "hello") + "/"
	greetings, err := store.list(ctx, hello)
	if err != nil {
		return savedBucketImport{}, fmt.Errorf("the bucket could not be read: %w", err)
	}
	id := stableID("push-destination", d.Type, d.Bucket, d.Endpoint, d.Account, d.Container, d.URLTemplate, read.Prefix)
	path := filepath.Join(dir, id+".json")
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return savedBucketImport{}, err
	}
	saved := savedBucketImport{ID: id, Type: d.Type, Bucket: firstString(d.Bucket, d.Container), Prefix: read.Prefix, path: path, Waiting: &[]int{len(greetings)}[0]}
	saved.collected(config.CaptureRoot)
	return saved, nil
}

// removeBucketImport forgets a saved configuration. What it collected stays.
func removeBucketImport(config Config, id string) error {
	for _, saved := range savedBucketImports(config) {
		if saved.ID == id {
			return os.Remove(saved.path)
		}
	}
	return fmt.Errorf("no saved bucket %q", id)
}

// importSavedBuckets collects from every saved bucket and returns the hosts
// that received something.
func importSavedBuckets(ctx context.Context, config Config) ([]bucketImportResult, []string, error) {
	results, hosts := []bucketImportResult{}, []string{}
	var failure error
	for _, saved := range savedBucketImports(config) {
		if ctx.Err() != nil {
			return results, hosts, ctx.Err()
		}
		result, err := importBucket(ctx, config.CaptureRoot, saved.path)
		if err != nil {
			failure = errors.Join(failure, fmt.Errorf("%s %s: %w", saved.Type, firstString(saved.Bucket, saved.Prefix), err))
		}
		results = append(results, result)
		for _, host := range result.Hosts {
			if !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	return results, hosts, failure
}

// backgroundBucketImport is the Background Processing item: collect from
// the saved buckets, then index the hosts that received something.
func (s *Server) backgroundBucketImport(ctx context.Context, op *Op) error {
	config := s.Config()
	ctx, child := op.Child(OperationSpec{Kind: "capture.import", Variant: "bucket", Trigger: triggerChained})
	_, hosts, failure := importSavedBuckets(ctx, config)
	if len(hosts) > 0 && ctx.Err() == nil {
		if err := s.indexCapturedHosts(ctx, hosts); err != nil {
			failure = errors.Join(failure, err)
		}
	}
	child.Finish(failure)
	return failure
}

// indexCapturedHosts indexes the captures of the hosts named, as the
// scheduled capture-and-index pass does for every host.
func (s *Server) indexCapturedHosts(ctx context.Context, hosts []string) error {
	targets, err := captureTargets(s.Config().CaptureRoot, hosts, false, nil)
	if err != nil {
		return err
	}
	var failure error
	wrote := false
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result := s.Catalog.admittedIngest(ctx, target.Source, target.Host.ID, func(ctx context.Context) IngestResult {
			return s.Catalog.IndexCapture(withIngestMode(ctx, ingestMode{deferGit: true}), target, nil)
		})
		wrote = wrote || result.Workspaces > 0
		if result.Error != nil {
			failure = errors.Join(failure, fmt.Errorf("%s: %v", target.label(), result.Error))
		}
	}
	if wrote {
		if err := s.Catalog.writeTransaction(ctx, "background deferred", func(tx *sql.Tx) error { return markSyncDeferred(tx) }); err != nil {
			failure = errors.Join(failure, err)
		}
	}
	return failure
}

// getBucketImports lists the saved buckets, without their credentials.
func (s *Server) getBucketImports(w http.ResponseWriter) {
	writeJSON(w, map[string]any{"buckets": savedBucketImports(s.Config())}, http.StatusOK)
}

// postBucketImports saves a read configuration ({"config": {...}}) or
// forgets one ({"remove": ID}), and asks for a collection.
func (s *Server) postBucketImports(ctx context.Context, w http.ResponseWriter, body map[string]any) {
	config := s.Config()
	if id, ok := body["remove"].(string); ok && id != "" {
		if err := removeBucketImport(config, id); err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		s.getBucketImports(w)
		return
	}
	read, ok := body["config"].(map[string]any)
	if !ok {
		writeError(w, errors.New(`send {"config": READ_CONFIGURATION} or {"remove": ID}`), http.StatusBadRequest)
		return
	}
	raw, err := json.MarshalIndent(read, "", "  ")
	var saved savedBucketImport
	if err == nil {
		saved, err = saveBucketImport(ctx, config, raw)
	}
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	s.requestBackground("bucket-import")
	writeJSON(w, map[string]any{"buckets": savedBucketImports(config), "connected": saved}, http.StatusOK)
}
