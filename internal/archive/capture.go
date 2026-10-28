package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Capture copies this Mac's raw source data onto the capture root, normally on
// the library drive, so the drive can be unplugged and the data indexed later
// on any Mac. Copying is fast where parsing is not, and a capture also keeps
// raw evidence that the tools themselves delete. It never touches the catalog.
//
// Layout, per host and source:
//
//	<capture_root>/<host-id>/host.json
//	<capture_root>/<host-id>/<source>/manifest.json
//	<capture_root>/<host-id>/<source>/files/...          latest capture, mirroring the source
//	<capture_root>/<host-id>/<source>/history/<stamp>/... superseded versions
//
// Every file is written to a temporary name, fsynced and renamed into place,
// and the manifest is replaced atomically after a full device flush, so an
// interrupted run (error, kill, unplug) leaves the previous state valid and the
// next run resumes from the last manifest checkpoint.
const (
	// captureManifestVersion is the newest manifest this build reads. The
	// version is per kind of encoded entry, so that a build refuses exactly
	// the captures holding a kind it cannot read: a writer stamps the lowest
	// version describing its encoded entries (writerVersion), and
	// capturePlainManifestVersion when there are none.
	captureManifestVersion      = captureGenerationsVersion
	capturePlainManifestVersion = 5
	// captureGenerationsVersion: encoded snapshot generations.
	captureGenerationsVersion = 6
	captureManifestName       = "manifest.json"
	captureHostName           = "host.json"
	captureFilesDir           = "files"
	captureHistoryDir         = "history"
	captureTempSuffix         = ".pharos-tmp"
	captureLockName           = ".capture.lock"
	captureStampLayout        = "20060102T150405.000000000Z"
)

var (
	// captureFreeMargin stays free on the capture volume so a capture never
	// fills the drive the catalog lives on.
	captureFreeMargin = int64(1 << 30)
	// captureCheckpointInterval bounds the work a killed run loses.
	captureCheckpointInterval = 5 * time.Second
	// captureFault injects failures in tests; a returned error acts like the
	// capture drive failing at that point.
	captureFault func(stage, rel string) error
	// captureManifestUnderstood is the newest manifest version loading
	// accepts; tests lower it to stand in for an older build.
	captureManifestUnderstood = captureManifestVersion

	errCaptureBusy          = errors.New("another capture is already running for this host")
	errCaptureSourceChanged = errors.New("source file shrank while it was being copied")
)

type captureManifest struct {
	DeliveredFingerprint string                          `json:"source_fingerprint,omitempty"`
	RetainedInputCapture bool                            `json:"retained_input_capture,omitempty"`
	SQLiteRecordCapture  bool                            `json:"sqlite_record_capture,omitempty"`
	LatestRecords        bool                            `json:"latest_records,omitempty"`
	RecordCoverage       map[string]sqliteCoverageStatus `json:"sqlite_record_coverage,omitempty"`
	Version              int                             `json:"version"`
	Host                 Host                            `json:"host"`
	Source               captureSourceInfo               `json:"source"`
	UpdatedAt            string                          `json:"updated_at"`
	LastDataAt           string                          `json:"last_data_at,omitempty"`
	Files                []*capturedFile                 `json:"files"`
	Snapshots            []*capturedSnapshot             `json:"snapshots"`
	Installations        []capturedInstallation          `json:"installations,omitempty"`
	PathMap              []capturePathMapping            `json:"path_map"`
	LastRun              *captureRunRecord               `json:"last_run,omitempty"`
}

type captureSourceInfo struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Account string `json:"account"`
}

// The capture directory belongs to a host, source name and adapter kind.
// Paths and account labels are editable configuration. Immutable inventories
// and SQLite revisions keep the configuration with which they were acquired.
func (m *captureManifest) acceptsSource(host Host, source SourceConfig) bool {
	return m.Version == 0 || m.Host.ID == host.ID && m.Source.Name == source.Name && strings.EqualFold(m.Source.Kind, source.Kind)
}

// capturedFile is one source file. Captured paths are slash-separated and
// relative to the source's capture directory. The captured copy carries the
// source's mtime, so size+mtime identify it on both sides; captured_at changes
// whenever it is captured again.
type capturedFile struct {
	RecoveryID string `json:"recovery_id,omitempty"`
	Path       string `json:"path"`
	Captured   string `json:"captured"`
	Size       int64  `json:"size"`
	MTime      string `json:"mtime"`
	MTimeNS    int64  `json:"mtime_ns"`
	// Verified pending-file observations order rewrites even when an upstream
	// tool preserves size and mtime. Native metadata remains unchanged below.
	ObservedNS   int64             `json:"observed_ns,omitempty"`
	SHA256       string            `json:"sha256"`
	CapturedAt   string            `json:"captured_at"`
	MissingSince string            `json:"missing_since,omitempty"`
	Previous     []capturedVersion `json:"previous,omitempty"`
	// Encoding and StoredSize are as for capturedVersion: a cold latest file
	// may be stored encoded (docs/capture-compression.md, phase 2).
	Encoding   string `json:"encoding,omitempty"`
	StoredSize int64  `json:"stored_size,omitempty"`
}

// version is the file's latest capture as a capturedVersion, the shape the
// encoding helpers and pins work with.
func (f *capturedFile) version() capturedVersion {
	return capturedVersion{Captured: f.Captured, Size: f.Size, MTimeNS: f.MTimeNS, ObservedNS: f.ObservedNS, SHA256: f.SHA256, CapturedAt: f.CapturedAt, Encoding: f.Encoding, StoredSize: f.StoredSize}
}

// capturedVersion is a superseded capture kept under history/: a file whose
// source was rewritten rather than appended to, or an older database snapshot.
type capturedVersion struct {
	Captured     string `json:"captured"`
	Size         int64  `json:"size"`
	MTimeNS      int64  `json:"mtime_ns,omitempty"`
	ObservedNS   int64  `json:"observed_ns,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	CapturedAt   string `json:"captured_at"`
	SupersededAt string `json:"superseded_at"`
	// Source is a database snapshot's source state, which dates its content.
	Source *sqliteSourceState `json:"source,omitempty"`
	// Encoding is how the file is stored ("zstd", or empty for the original
	// bytes); StoredSize is then its size on disk. Size, SHA256 and the mtimes
	// always describe the original bytes (docs/capture-compression.md).
	Encoding   string `json:"encoding,omitempty"`
	StoredSize int64  `json:"stored_size,omitempty"`
}

// capturedSnapshot is a consistent copy of a SQLite database made with the
// online backup API. Source records the database and WAL state it was taken
// from; an unchanged state skips the next snapshot.
type capturedSnapshot struct {
	RecoveryID      string            `json:"recovery_id,omitempty"`
	Path            string            `json:"path"`
	Captured        string            `json:"captured"`
	Size            int64             `json:"size"`
	CapturedMTimeNS int64             `json:"captured_mtime_ns"`
	CapturedAt      string            `json:"captured_at"`
	Method          string            `json:"method"`
	Source          sqliteSourceState `json:"source"`
	MissingSince    string            `json:"missing_since,omitempty"`
	Generations     []capturedVersion `json:"generations,omitempty"`
}

type sqliteSourceState struct {
	Size        int64  `json:"size"`
	MTimeNS     int64  `json:"mtime_ns"`
	WALSize     int64  `json:"wal_size"`
	WALMTimeNS  int64  `json:"wal_mtime_ns"`
	Identity    string `json:"identity,omitempty"`
	WALIdentity string `json:"wal_identity,omitempty"`
}

// capturedInstallation maps a TL1 registry entry's absolute paths to their
// captures, which the registry itself cannot express.
type capturedInstallation struct {
	ID                  string `json:"id"`
	Project             string `json:"project,omitempty"`
	DBPath              string `json:"db_path"`
	CapturedDB          string `json:"captured_db"`
	TranscriptsDir      string `json:"transcripts_dir,omitempty"`
	CapturedTranscripts string `json:"captured_transcripts,omitempty"`
	CodeRepo            string `json:"code_repo,omitempty"`
	// ConfigPath is the TL1 config (flavor definitions); it is not captured.
	ConfigPath   string `json:"config_path,omitempty"`
	MissingSince string `json:"missing_since,omitempty"`
}

type capturePathMapping struct {
	Original string `json:"original"`
	Captured string `json:"captured"`
}

type captureError struct {
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

type captureRunRecord struct {
	Phase              string         `json:"phase,omitempty"`
	PhaseStartedAt     string         `json:"phase_started_at,omitempty"`
	StartedAt          string         `json:"started_at"`
	FinishedAt         string         `json:"finished_at,omitempty"`
	FilesSeen          int            `json:"files_seen"`
	FilesPlanned       int            `json:"files_planned"`
	FilesCopied        int            `json:"files_copied"`
	FilesUnchanged     int            `json:"files_unchanged"`
	FilesMissing       int            `json:"files_missing"`
	BytesPlanned       int64          `json:"bytes_planned"`
	BytesCopied        int64          `json:"bytes_copied"`
	VersionsPreserved  int            `json:"versions_preserved"`
	DatabasesSeen      int            `json:"databases_seen"`
	SnapshotsTaken     int            `json:"snapshots_taken"`
	SnapshotsUnchanged int            `json:"snapshots_unchanged"`
	SnapshotBytes      int64          `json:"snapshot_bytes"`
	Errors             []captureError `json:"errors"`
	Warnings           []captureError `json:"warnings,omitempty"`
}

// CaptureSourceResult is one source's progress and outcome.
type CaptureSourceResult struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Dir   string `json:"dir,omitempty"`
	captureRunRecord
}

func (r CaptureSourceResult) clone() CaptureSourceResult {
	r.Errors = slices.Clone(r.Errors)
	r.Warnings = slices.Clone(r.Warnings)
	return r
}

type CaptureSummary struct {
	Host           Host                  `json:"host"`
	Root           string                `json:"root"`
	StartedAt      string                `json:"started_at"`
	FinishedAt     string                `json:"finished_at"`
	OK             bool                  `json:"ok"`
	FilesCopied    int                   `json:"files_copied"`
	BytesCopied    int64                 `json:"bytes_copied"`
	SnapshotsTaken int                   `json:"snapshots_taken"`
	Errors         []captureError        `json:"errors"`
	Sources        []CaptureSourceResult `json:"sources"`
}

type captureHostRecord struct {
	ID             string `json:"id"`
	MachineID      string `json:"machine_id,omitempty"`
	Label          string `json:"label"`
	User           string `json:"user"`
	FirstCaptureAt string `json:"first_capture_at"`
	LastCaptureAt  string `json:"last_capture_at,omitempty"`
}

// destinationError marks a failure writing the capture drive, which most
// likely affects every later file too, so the source's run stops there.
type destinationError struct{ err error }

func (e destinationError) Error() string { return "capture drive: " + e.err.Error() }
func (e destinationError) Unwrap() error { return e.err }

func toDestination(err error) error {
	var existing destinationError
	if err == nil || errors.As(err, &existing) {
		return err
	}
	return destinationError{err}
}

// captureSources resolves CLI/API source names; none means every enabled
// source of this host. Naming a disabled source captures it anyway.
func captureSources(config Config, names []string) ([]SourceConfig, error) {
	sources := []SourceConfig{}
	if len(names) == 0 {
		for _, source := range config.Sources {
			if source.Enabled {
				sources = append(sources, source)
			}
		}
		return sources, nil
	}
	for _, name := range names {
		index := slices.IndexFunc(config.Sources, func(source SourceConfig) bool { return source.Name == name })
		if index < 0 {
			return nil, fmt.Errorf("unknown source: %s", name)
		}
		sources = append(sources, config.Sources[index])
	}
	return sources, nil
}

// Capture runs one capture of sources into config.CaptureRoot.
func Capture(ctx context.Context, config Config, sources []SourceConfig, progress func(CaptureSourceResult)) (CaptureSummary, error) {
	session, err := beginCapture(config)
	if err != nil {
		return CaptureSummary{}, err
	}
	defer session.Close()
	opFrom(ctx).holdLock(session.lock)
	return session.Run(ctx, sources, progress), nil
}

type captureSession struct {
	// Pending evidence is retained until verified delivery, including indexed
	// snapshots whose original source may subsequently prune or rewrite history.
	preserveUnindexed bool
	deliveryTarget    string
	recoveryID        string
	hostDir           string
	host              Host
	generations       int
	lock              *os.File
	// buffers bound a copy's memory; they circulate between the copy and
	// its hasher.
	buffers chan []byte
}

// beginCapture takes the per-host capture lock, which also keeps a CLI run and
// the service from capturing at once.
func beginCapture(config Config) (*captureSession, error) {
	if d, err := readLibraryDescriptor(config); err == nil {
		config.PendingCapture = config.PendingCapture || d.RetainPendingEvidence
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	root := config.CaptureRoot
	if root == "" {
		return nil, errors.New("capture_root is not configured")
	}
	// Never create a missing mount point: under /Volumes that would put the
	// capture on the startup disk while the drive is away.
	if _, err := os.Stat(filepath.Dir(root)); err != nil {
		return nil, fmt.Errorf("capture root %s is unavailable; is its drive mounted? %w", root, err)
	}
	host := currentHost()
	if host.Fallback {
		return nil, errHostUnknown
	}
	if !validCaptureName(host.ID) {
		return nil, fmt.Errorf("host id %q cannot name a capture directory", host.ID)
	}
	hostDir := filepath.Join(root, host.ID)
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(hostDir, captureLockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errCaptureBusy
		}
		return nil, err
	}
	session := &captureSession{deliveryTarget: config.DeliveryTargetInstance, preserveUnindexed: config.PendingCapture, recoveryID: config.CaptureRecoveryID, hostDir: hostDir, host: host, generations: max(config.CaptureGenerations, 0), lock: lock, buffers: make(chan []byte, 4)}
	for range cap(session.buffers) {
		session.buffers <- make([]byte, 1<<20)
	}
	return session, nil
}

// Close releases the capture lock.
func (s *captureSession) Close() error { return releaseLockFile(s.lock) }

func (s *captureSession) Run(ctx context.Context, sources []SourceConfig, progress func(CaptureSourceResult)) CaptureSummary {
	summary := CaptureSummary{Host: s.host, Root: s.hostDir, StartedAt: now(), Errors: []captureError{}, Sources: []CaptureSourceResult{}}
	if err := s.writeHost(summary.StartedAt, ""); err != nil {
		summary.Errors = append(summary.Errors, captureError{Path: filepath.Join(s.hostDir, captureHostName), Error: err.Error()})
	}
	withCaptureIOPolicy(func() {
		for _, source := range sources {
			summary.Sources = append(summary.Sources, s.source(ctx, source, progress))
		}
	})
	summary.FinishedAt = now()
	if err := s.writeHost(summary.StartedAt, summary.FinishedAt); err != nil {
		summary.Errors = append(summary.Errors, captureError{Path: filepath.Join(s.hostDir, captureHostName), Error: err.Error()})
	}
	summary.OK = len(summary.Errors) == 0
	for _, result := range summary.Sources {
		summary.FilesCopied += result.FilesCopied
		summary.BytesCopied += result.BytesCopied + result.SnapshotBytes
		summary.SnapshotsTaken += result.SnapshotsTaken
		summary.OK = summary.OK && result.State == "complete"
	}
	return summary
}

func (s *captureSession) writeHost(started, finished string) error {
	file := filepath.Join(s.hostDir, captureHostName)
	record := captureHostRecord{}
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &record)
	}
	if record.FirstCaptureAt != "" && finished == "" {
		return nil
	}
	record.ID, record.Label, record.User = s.host.ID, s.host.Label, s.host.User
	record.MachineID = defaultString(s.host.MachineID, record.MachineID)
	record.FirstCaptureAt = defaultString(record.FirstCaptureAt, started)
	if finished != "" {
		record.LastCaptureAt = finished
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileDurably(file, append(data, '\n'))
}

// validCaptureName admits one directory name that cannot escape its parent or
// collide with the capture's own bookkeeping.
func validCaptureName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.HasPrefix(name, ".") && name != captureHostName &&
		!strings.ContainsAny(name, "/\\:\x00")
}

type sourceCapture struct {
	readerLease              string
	recordDependencyCache    map[string]recordDependency
	recordDependencies       []recordDependency
	recordDependenciesLoaded bool
	session                  *captureSession
	ctx                      context.Context
	source                   SourceConfig
	dir                      string
	manifest                 *captureManifest
	files                    map[string]*capturedFile
	snapshots                map[string]*capturedSnapshot
	result                   CaptureSourceResult
	progress                 func(CaptureSourceResult)
	checkpoint               time.Time
	// Pending captures verify source bytes before declaring them delivered.
	verifyDelivery bool
	// prune lists superseded snapshot generations to delete once a committed
	// manifest no longer names them.
	prune []string
	// indexed lists the snapshots the index no longer needs.
	indexed *indexMarker
}

func (s *captureSession) source(ctx context.Context, source SourceConfig, progress func(CaptureSourceResult)) CaptureSourceResult {
	return s.sourcePlanned(ctx, source, progress, nil)
}

// A bounded verification may acquire only one filesystem dependency group.
// The private plan controls acquisition, never bypasses durable publication.
func (s *captureSession) sourcePlanned(ctx context.Context, source SourceConfig, progress func(CaptureSourceResult), selected *capturePlan) CaptureSourceResult {
	c := &sourceCapture{session: s, ctx: ctx, source: source, progress: progress, checkpoint: time.Now(),
		result:         CaptureSourceResult{Name: source.Name, Kind: source.Kind, State: "running", captureRunRecord: captureRunRecord{StartedAt: now(), Errors: []captureError{}}},
		verifyDelivery: s.preserveUnindexed || s.deliveryTarget != ""}
	if !validCaptureName(source.Name) {
		return c.finish(fmt.Errorf("source name %q cannot name a capture directory", source.Name))
	}
	c.dir = filepath.Join(s.hostDir, source.Name)
	c.result.Dir = c.dir
	c.phase("discovering files")
	manifest, err := loadCaptureManifest(c.dir)
	if err != nil {
		return c.finish(err)
	}
	if !manifest.acceptsSource(s.host, source) {
		return c.finish(errors.New("capture source identity changed; use a new source name to preserve the existing capture"))
	}
	c.manifest, c.indexed = manifest, loadIndexMarker(c.dir)
	if err := checkNativeSnapshotPause(c.dir); err != nil {
		return c.finish(err)
	}
	c.files, c.snapshots = map[string]*capturedFile{}, map[string]*capturedSnapshot{}
	for _, file := range manifest.Files {
		c.files[file.Captured] = file
	}
	for _, snapshot := range manifest.Snapshots {
		c.snapshots[snapshot.Captured] = snapshot
	}
	var plan capturePlan
	if selected != nil {
		plan = *selected
	} else {
		plan, err = planCapture(source)
	}
	if err == nil && isConductorSource(source.Kind) {
		includeKnownCaptureDatabases(&plan, manifest, source)
	}
	c.result.Warnings = append(c.result.Warnings, plan.warnings...)
	if err != nil {
		// A source that is wholly unavailable is more likely unmounted or moved
		// than deleted, so nothing is marked missing and nothing is written.
		return c.finish(err)
	}
	var delivered struct {
		Fingerprint string `json:"fingerprint"`
		Target      string `json:"destination_instance"`
	}
	if b, e := os.ReadFile(filepath.Join(c.dir, ".delivered.json")); e == nil && json.Unmarshal(b, &delivered) == nil && c.session.recoveryID == "" && delivered.Fingerprint != "" && (c.session.deliveryTarget == "" || c.session.deliveryTarget == delivered.Target) {
		c.verifyDelivery = true
		fingerprint, err := captureDeliveryFingerprint(ctx, source, plan, nil)
		if err != nil {
			return c.finish(err)
		}
		if delivered.Fingerprint == fingerprint {
			c.result.Phase = "Already delivered; source is unchanged"
			return c.finish(nil)
		}
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return c.finish(toDestination(err))
	}
	if _, err := loadPreservationPins(c.dir); err != nil {
		return c.finish(err)
	}
	c.mergeMappings(plan)
	// Publish writer compatibility before any rotation or record temporary.
	// A crash must not leave a version-1 writer free to prune new inputs.
	if err := c.commit(false); err != nil {
		return c.finish(err)
	}
	removeCaptureTemporaries(c.dir)
	present := map[string]bool{}
	for _, rel := range plan.skipped {
		present[path.Join(captureFilesDir, rel)] = true
	}
	err = c.captureFiles(plan.files, present)
	if err == nil {
		c.phase("copying database snapshots")
		err = c.captureDatabases(plan.databases, present)
	}
	if err == nil && plan.complete {
		c.markMissing(present)
	}
	if err == nil && plan.complete && len(c.result.Errors) == 0 && source.Options["sqlite_record_capture"] == true {
		if e := c.commitRecordGroup(); e != nil {
			c.fail("", e)
		} else {
			c.manifest.LatestRecords = false
		}
	}
	if err != nil {
		c.fail("", err)
	}
	c.phase("saving capture manifest")
	c.manifest.DeliveredFingerprint = ""
	if len(c.result.Errors) == 0 {
		fingerprint, fingerprintErr := captureDeliveryFingerprint(ctx, source, plan, c)
		if fingerprintErr != nil {
			c.fail("", fingerprintErr)
		} else {
			c.manifest.DeliveredFingerprint = fingerprint
		}
	}
	err = c.commit(true)
	if err == nil {
		c.removePruned(true)
	}
	return c.finish(err)
}

// mergeMappings keeps the mappings of installations and paths that left the
// source: their captures are kept, so the index step still needs them.
func (c *sourceCapture) mergeMappings(plan capturePlan) {
	installations, paths := plan.installations, plan.pathMap
	current := map[string]bool{}
	for _, installation := range installations {
		current[installation.ID+"\x1f"+installation.DBPath] = true
	}
	for _, installation := range c.manifest.Installations {
		if !current[installation.ID+"\x1f"+installation.DBPath] {
			installation.MissingSince = defaultString(installation.MissingSince, now())
			installations = append(installations, installation)
		}
	}
	for _, mapping := range paths {
		current[mapping.Original] = true
	}
	for _, mapping := range c.manifest.PathMap {
		if !current[mapping.Original] {
			paths = append(paths, mapping)
		}
	}
	c.manifest.Installations, c.manifest.PathMap = installations, paths
}

func (c *sourceCapture) finish(err error) CaptureSourceResult {
	if err != nil {
		c.result.Errors = append(c.result.Errors, captureError{Error: err.Error()})
	}
	c.result.FinishedAt = now()
	c.result.State = "complete"
	if len(c.result.Errors) > 0 {
		c.result.State = "failed"
		if errors.Is(err, errNativeSnapshotPaused) {
			c.result.State = "pending"
		}
	}
	c.report()
	return c.result.clone()
}

func (c *sourceCapture) report() {
	if c.progress != nil {
		c.progress(c.result.clone())
	}
}

func (c *sourceCapture) phase(phase string) {
	c.result.Phase, c.result.PhaseStartedAt = phase, now()
	c.report()
}

func (c *sourceCapture) fail(path string, err error) {
	c.result.Errors = append(c.result.Errors, captureError{Path: path, Error: err.Error()})
}

func loadCaptureManifest(dir string) (*captureManifest, error) {
	if err := checkSQLiteRecordCompatibility(dir); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, captureManifestName)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &captureManifest{}, nil
	} else if err != nil {
		return nil, err
	}
	manifest := &captureManifest{}
	// Starting over would forget missing_since and history, so an unreadable
	// or newer manifest stops this source instead.
	if err := json.Unmarshal(data, manifest); err != nil {
		return nil, fmt.Errorf("capture manifest %s is unreadable: %w", file, err)
	}
	if manifest.Version > captureManifestUnderstood {
		return nil, fmt.Errorf("capture manifest %s has version %d; this build understands up to %d", file, manifest.Version, captureManifestUnderstood)
	}
	if bad := manifest.unsafePath(); bad != "" {
		return nil, fmt.Errorf("capture manifest %s names %q, outside its capture directory", file, bad)
	}
	return manifest, nil
}

// unsafePath returns a captured path that is not clean, relative, and under
// files/ (latest) or history/ (superseded), so that no manifest can make a
// capture or an index read, move or delete anything outside its directory.
func (m *captureManifest) unsafePath() string {
	check := func(rel string, root string) bool {
		return rel == path.Clean(rel) && !strings.Contains(rel, "\\") && (rel == root || strings.HasPrefix(rel, root+"/"))
	}
	bad := ""
	test := func(rel, root string) {
		if bad == "" && !check(rel, root) {
			bad = rel
		}
	}
	for _, file := range m.Files {
		test(file.Captured, captureFilesDir)
		for _, version := range file.Previous {
			test(version.Captured, captureHistoryDir)
		}
	}
	for _, snapshot := range m.Snapshots {
		test(snapshot.Captured, captureFilesDir)
		for _, generation := range snapshot.Generations {
			test(generation.Captured, captureHistoryDir)
		}
	}
	for _, installation := range m.Installations {
		test(installation.CapturedDB, captureFilesDir)
		if installation.CapturedTranscripts != "" {
			test(installation.CapturedTranscripts, captureFilesDir)
		}
	}
	for _, mapping := range m.PathMap {
		test(mapping.Captured, captureFilesDir)
	}
	return bad
}

// lastCapturedDataAt ignores no-op captures and later indexing. Individual
// entries retain the time their bytes were copied from the source Mac.
func (m *captureManifest) lastCapturedDataAt() string {
	latest := m.LastDataAt
	latestTime, valid := parseTime(latest)
	consider := func(value string) {
		if at, ok := parseTime(value); ok && (!valid || at.After(latestTime)) {
			latest, latestTime, valid = value, at, true
		}
	}
	for _, file := range m.Files {
		consider(file.CapturedAt)
	}
	for _, snapshot := range m.Snapshots {
		consider(snapshot.CapturedAt)
	}
	return latest
}

// commit atomically replaces the manifest. Its full device flush also makes
// every file fsynced before it durable, which is why files need only fsync.
func (c *sourceCapture) commit(final bool) error {
	m := c.manifest
	m.SQLiteRecordCapture = c.source.Options["sqlite_record_capture"] == true
	m.RetainedInputCapture = retainedAcquisitionEnabled(c.source)
	m.Host = c.session.host
	m.Source = captureSourceInfo{Name: c.source.Name, Kind: c.source.Kind, Path: c.source.Path, Account: c.source.Account}
	m.UpdatedAt = now()
	m.Files = sortedCaptures(c.files)
	m.Snapshots = sortedCaptures(c.snapshots)
	m.Version = m.writerVersion()
	m.LastDataAt = m.lastCapturedDataAt()
	record := c.result.clone().captureRunRecord
	if final {
		record.FinishedAt = m.UpdatedAt
	}
	m.LastRun = &record
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	if err := c.ensureSpace(int64(len(data)) + 4096); err != nil {
		return err
	}
	if err := writeFileDurably(filepath.Join(c.dir, captureManifestName), append(data, '\n')); err != nil {
		return toDestination(err)
	}
	c.checkpoint = time.Now()
	return nil
}

func (c *sourceCapture) maybeCheckpoint() error {
	if time.Since(c.checkpoint) < captureCheckpointInterval {
		return nil
	}
	return c.commit(false)
}

func sortedCaptures[T any](values map[string]*T) []*T {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]*T, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func (c *sourceCapture) captureFiles(items []captureItem, present map[string]bool) error {
	type job struct {
		item     captureItem
		previous *capturedFile
	}
	jobs := []job{}
	for _, item := range items {
		rel := path.Join(captureFilesDir, item.rel)
		info, err := os.Stat(item.path)
		if err != nil || !info.Mode().IsRegular() {
			// Vanished since it was listed: missing, unless it is unreadable.
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				c.fail(item.path, err)
				present[rel] = true
			}
			continue
		}
		present[rel] = true
		c.result.FilesSeen++
		previous := c.files[rel]
		if previous != nil {
			previous.MissingSince = ""
			if previous.Path == item.path && (c.session.recoveryID == "" || previous.RecoveryID == c.session.recoveryID) && previous.Size == info.Size() && previous.MTimeNS == info.ModTime().UnixNano() && c.intactFile(previous) {
				unchanged := true
				if c.verifyDelivery {
					digest, err := captureDeliveryDigest(c.ctx, item.path)
					if err != nil {
						return err
					}
					unchanged = digest == previous.SHA256
				}
				if unchanged {
					c.result.FilesUnchanged++
					continue
				}
			}
		}
		jobs = append(jobs, job{item, previous})
		c.result.BytesPlanned += info.Size()
	}
	c.result.FilesPlanned = len(jobs)
	if err := c.ensureSpace(c.result.BytesPlanned); err != nil {
		return err
	}
	c.phase("copying files")
	for _, job := range jobs {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		entry, version, err := c.copyFile(job.item, job.previous)
		if err != nil {
			var destination destinationError
			if errors.As(err, &destination) {
				return err
			}
			c.fail(job.item.path, err)
			continue
		}
		if version != nil {
			c.result.VersionsPreserved++
		}
		c.files[entry.Captured] = entry
		c.result.FilesCopied++
		c.result.BytesCopied += entry.Size
		c.report()
		if err := c.maybeCheckpoint(); err != nil {
			return err
		}
	}
	return nil
}

// intact reports whether a captured file is still what the manifest says, so a
// capture lost to an unplug before the device flush is copied again.
func (c *sourceCapture) intact(rel string, size, mtimeNS int64) bool {
	info, err := os.Stat(filepath.Join(c.dir, filepath.FromSlash(rel)))
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	actual := info.ModTime().UnixNano()
	// Filesystems with coarse timestamps (HFS+, exFAT) truncate the copied
	// mtime; APFS keeps nanoseconds and must match exactly.
	coarse := actual%int64(time.Second) == 0 && max(actual-mtimeNS, mtimeNS-actual) < int64(2*time.Second)
	return actual == mtimeNS || coarse
}

// storedFile is a latest file's entry as its copy is actually stored: an
// entry a killed compression marked encoded before renaming its file is the
// original bytes (storedAs).
func (c *sourceCapture) storedFile(f *capturedFile) capturedVersion {
	return storedAs(filepath.Join(c.dir, filepath.FromSlash(f.Captured)), f.version())
}

// intactFile is intact for a latest file, at the size it is stored at. An
// encoded entry's file is opened (storedAs) only when it has the original
// size, the one sign of a compression killed before its rename; otherwise
// the stat alone decides.
func (c *sourceCapture) intactFile(f *capturedFile) bool {
	size := f.storedSize()
	if f.Encoding != "" {
		if info, err := os.Stat(filepath.Join(c.dir, filepath.FromSlash(f.Captured))); err == nil && info.Size() == f.Size && f.Size != f.StoredSize {
			size = c.storedFile(f).storedSize()
		}
	}
	return c.intact(f.Captured, size, f.MTimeNS)
}

func (c *sourceCapture) ensureSpace(bytes int64) error {
	if bytes == 0 {
		return nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.dir, &stat); err != nil {
		return toDestination(err)
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free-bytes < captureFreeMargin {
		return destinationError{fmt.Errorf("needs %d bytes but only %d are free", bytes+captureFreeMargin, free)}
	}
	return nil
}

func (c *sourceCapture) copyFile(item captureItem, previous *capturedFile) (*capturedFile, *capturedVersion, error) {
	for attempt := 0; ; attempt++ {
		entry, version, err := c.copyOnce(item, previous)
		if !errors.Is(err, errCaptureSourceChanged) || attempt > 0 {
			return entry, version, err
		}
	}
}

func (c *sourceCapture) copyOnce(item captureItem, previous *capturedFile) (*capturedFile, *capturedVersion, error) {
	rel := path.Join(captureFilesDir, item.rel)
	if captureFault != nil {
		if err := captureFault("copy", rel); err != nil {
			return nil, nil, toDestination(err)
		}
	}
	source, err := os.Open(item.path)
	if err != nil {
		return nil, nil, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, nil, err
	}
	// Exactly this many bytes are copied: an append-only log that grows
	// meanwhile yields a consistent prefix, and the next run sees the new size.
	size, mtime := info.Size(), info.ModTime()
	if err := reserveCaptureAcquisition(c.dir, size); err != nil {
		return nil, nil, err
	}
	destination := filepath.Join(c.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return nil, nil, toDestination(err)
	}
	temporary := destination + captureTempSuffix
	out, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, toDestination(err)
	}
	abandon := func(err error) (*capturedFile, *capturedVersion, error) {
		out.Close()
		os.Remove(temporary)
		return nil, nil, err
	}
	digest := c.session.newHash()
	defer digest.close()
	// Appending keeps the previous capture as a prefix; anything else is a
	// rewrite, and the previous capture is kept rather than overwritten.
	keep := previous != nil && previous.SHA256 != "" && c.intactFile(previous)
	rewritten := keep
	copied := int64(0)
	if keep && c.session.recoveryID == "" && previous.Size <= size {
		n, err := c.copyN(out, digest, source, previous.Size)
		if err != nil {
			return abandon(err)
		}
		copied = n
		rewritten = digest.sum() != previous.SHA256
	}
	n, err := c.copyN(out, digest, source, size-copied)
	if err != nil {
		return abandon(err)
	}
	if copied += n; copied < size {
		return abandon(errCaptureSourceChanged)
	}
	if c.session.recoveryID != "" {
		rewritten = keep && digest.sum() != previous.SHA256
	}
	if err := os.Chtimes(temporary, mtime, mtime); err != nil {
		return abandon(toDestination(err))
	}
	if err := syscall.Fsync(int(out.Fd())); err != nil {
		return abandon(toDestination(err))
	}
	if err := out.Close(); err != nil {
		os.Remove(temporary)
		return nil, nil, toDestination(err)
	}
	entry := &capturedFile{Path: item.path, Captured: rel, Size: size, MTime: mtime.UTC().Format(time.RFC3339Nano), MTimeNS: mtime.UnixNano(),
		SHA256: digest.sum(), CapturedAt: now(), RecoveryID: c.session.recoveryID}
	if previous != nil {
		entry.ObservedNS = previous.ObservedNS
	}
	if c.verifyDelivery && (previous == nil || previous.SHA256 != entry.SHA256) {
		observed, _ := parseTime(entry.CapturedAt)
		entry.ObservedNS = max(entry.MTimeNS, entry.ObservedNS+1, observed.UnixNano())
	}
	var version *capturedVersion
	if previous != nil {
		entry.Previous = previous.Previous
	}
	if rewritten {
		kept, err := c.preserve(c.storedFile(previous), true) // an encoded copy moves to history encoded
		if err != nil {
			os.Remove(temporary)
			return nil, nil, err
		}
		entry.Previous = append(slices.Clone(entry.Previous), kept)
		version = &kept
	}
	if err := os.Rename(temporary, destination); err != nil {
		os.Remove(temporary)
		return nil, nil, toDestination(err)
	}
	return entry, version, nil
}

// copyN streams n bytes through the session's buffers, so memory stays
// bounded whatever the file size, and tells write failures from read failures.
func (c *sourceCapture) copyN(dst io.Writer, digest *overlappedHash, src io.Reader, n int64) (int64, error) {
	copied := int64(0)
	for copied < n {
		buffer := <-c.session.buffers
		chunk := buffer[:min(int64(len(buffer)), n-copied)]
		read, err := src.Read(chunk)
		if read > 0 {
			if _, werr := dst.Write(chunk[:read]); werr != nil {
				c.session.buffers <- buffer
				return copied, toDestination(werr)
			}
			digest.write(chunk[:read])
			copied += int64(read)
		} else {
			c.session.buffers <- buffer
		}
		if err == io.EOF {
			return copied, nil
		} else if err != nil {
			return copied, err
		}
	}
	return copied, nil
}

// overlappedHash computes SHA-256 on its own goroutine. Hashing runs at about
// the speed of the copy (~2.4 GB/s here), so doing it inline halved capture
// throughput; overlapped, it costs CPU but little time.
type overlappedHash struct {
	hash    hash.Hash
	chunks  chan []byte
	pending sync.WaitGroup
}

func (s *captureSession) newHash() *overlappedHash {
	h := &overlappedHash{hash: sha256.New(), chunks: make(chan []byte, cap(s.buffers))}
	go func() {
		for chunk := range h.chunks {
			h.hash.Write(chunk)
			s.buffers <- chunk[:cap(chunk)]
			h.pending.Done()
		}
	}()
	return h
}

// write hands chunk, a session buffer, to the hasher, which returns it.
func (h *overlappedHash) write(chunk []byte) {
	h.pending.Add(1)
	h.chunks <- chunk
}

func (h *overlappedHash) sum() string {
	h.pending.Wait()
	return hex.EncodeToString(h.hash.Sum(nil))
}

// close waits for every buffer to return before the next copy may use them.
func (h *overlappedHash) close() {
	h.pending.Wait()
	close(h.chunks)
}

// preserve moves a capture's current bytes to history/<its captured_at>/,
// hard-linking where possible so the current path never disappears. The
// deterministic name lets a run interrupted after this step find it again.
func (c *sourceCapture) preserve(version capturedVersion, present bool) (capturedVersion, error) {
	stamp := version.CapturedAt
	if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
		stamp = parsed.UTC().Format(captureStampLayout)
	}
	rel := path.Join(captureHistoryDir, stamp, strings.TrimPrefix(version.Captured, captureFilesDir+"/"))
	from := filepath.Join(c.dir, filepath.FromSlash(version.Captured))
	to := filepath.Join(c.dir, filepath.FromSlash(rel))
	if present {
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return version, toDestination(err)
		}
		if err := os.Link(from, to); err != nil && !errors.Is(err, fs.ErrExist) {
			if err := os.Rename(from, to); err != nil {
				return version, toDestination(err)
			}
		}
	}
	if _, err := os.Stat(to); err != nil {
		return version, toDestination(err)
	}
	if err := c.carrySQLiteExportReceipt(version.Captured, rel); err != nil {
		return version, err
	}
	version.Captured = rel
	version.SupersededAt = now()
	return version, nil
}

func (c *sourceCapture) markMissing(present map[string]bool) {
	timestamp := now()
	for rel, file := range c.files {
		if !present[rel] {
			if file.MissingSince == "" {
				file.MissingSince = timestamp
			}
			c.result.FilesMissing++
		}
	}
	for rel, snapshot := range c.snapshots {
		if !present[rel] && snapshot.MissingSince == "" {
			snapshot.MissingSince = timestamp
		}
	}
}

// removePruned deletes superseded snapshot generations the committed manifest
// no longer names. The sweep, once every database has had the chance to adopt
// what an interrupted rotation left in history, also deletes unnamed ones.
func (c *sourceCapture) removePruned(sweep bool) {
	for _, rel := range c.prune {
		if strings.HasPrefix(rel, captureHistoryDir+"/") && c.canPruneSnapshot(rel) {
			file := filepath.Join(c.dir, filepath.FromSlash(rel))
			os.Remove(file)
			removeEmptyDirs(filepath.Dir(file), filepath.Join(c.dir, captureHistoryDir))
		}
	}
	c.prune = nil
	stamps, err := os.ReadDir(filepath.Join(c.dir, captureHistoryDir))
	if !sweep || err != nil {
		return
	}
	for _, snapshot := range c.snapshots {
		named := map[string]bool{}
		for _, generation := range snapshot.Generations {
			named[generation.Captured] = true
		}
		tail := strings.TrimPrefix(snapshot.Captured, captureFilesDir+"/")
		for _, stamp := range stamps {
			rel := path.Join(captureHistoryDir, stamp.Name(), tail)
			file := filepath.Join(c.dir, filepath.FromSlash(rel))
			if _, err := os.Lstat(file); err == nil && !named[rel] && c.canPruneSnapshot(rel) {
				os.Remove(file)
				removeEmptyDirs(filepath.Dir(file), filepath.Join(c.dir, captureHistoryDir))
			}
		}
	}
}

func removeEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)) && os.Remove(dir) == nil {
		dir = filepath.Dir(dir)
	}
}

// removeCaptureTemporaries deletes what a killed run left half-written. The
// capture lock guarantees no other run is writing them.
func removeCaptureTemporaries(dir string) {
	readerCleanup := cleanupPreservationReaders(dir)
	if readerCleanup != nil {
		fmt.Fprintf(os.Stderr, "Pharos: deferred reader pin cleanup for %s: %v\n", dir, readerCleanup)
	}
	_ = filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			if file != dir && entry.Name() == "inputs" {
				return fs.SkipDir
			}
			if strings.HasPrefix(entry.Name(), ".reader-") && strings.HasSuffix(entry.Name(), captureTempSuffix) {
				if strings.HasPrefix(entry.Name(), ".reader-pin-") && readerCleanup != nil {
					return fs.SkipDir
				}
				// Active readers hold this lease flock, including audits.
				// After a crash the lock is released, allowing native space back.
				lock, e := os.OpenFile(filepath.Join(file, ".reader.lock"), os.O_RDWR|os.O_CREATE, 0600)
				if e == nil {
					if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
						_ = os.RemoveAll(file)
					}
					lock.Close()
				}
				return fs.SkipDir
			}
		}
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), captureTempSuffix) {
			os.Remove(file)
		}
		return nil
	})
}

// writeFileDurably replaces file atomically after a full device flush.
func writeFileDurably(file string, data []byte) error {
	temporary := file + captureTempSuffix
	out, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	if err == nil {
		// os.File.Sync is F_FULLFSYNC on macOS: it flushes the drive's cache,
		// including every file fsynced earlier, before the rename commits.
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, file)
	}
	if err != nil {
		os.Remove(temporary)
		return err
	}
	if dir, err := os.Open(filepath.Dir(file)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

func syncPath(file string) (os.FileInfo, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	if err := syscall.Fsync(int(handle.Fd())); err != nil {
		return nil, err
	}
	return handle.Stat()
}
