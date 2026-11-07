package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/gbdubs/pharos/internal/platform/filelock"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// A capture export copies one already-captured Claude Code or Codex source,
// exactly as stored, into a self-verifying directory that can be carried
// to another machine and checked there without Pharos's catalog, library or
// configuration (docs/capture-export.md):
//
//	OUT/<id>/export.json
//	OUT/<id>/captures/<host>/host.json
//	OUT/<id>/captures/<host>/<source>/manifest.json
//	OUT/<id>/captures/<host>/<source>/files/...     declared latest versions
//	OUT/<id>/captures/<host>/<source>/history/...   declared earlier versions
//
// export.json lists every other file with its size and the SHA-256 of its
// stored bytes; <id> is the SHA-256 of that envelope's canonical form
// without its id. Integrity hashes detect corruption; they do not say who
// made the export.

const (
	captureExportFormat       = "pharos-capture-export"
	captureExportVersion      = 1
	captureExportScope        = "native-capture"
	captureExportCompleteness = "unknown"
	captureExportEnvelope     = "export.json"
	captureExportCaptures     = "captures"
	captureExportStagePrefix  = ".stage-"
	// captureExportMaxSize is the largest integer every JSON reader holds
	// exactly (2^53-1).
	captureExportMaxSize = 1<<53 - 1
	// captureExportManifestFloor is the oldest manifest version exported;
	// captureExportManifestCeiling the newest this first scope knows.
	captureExportManifestFloor   = 1
	captureExportManifestCeiling = captureSnapshotsVersion
)

// captureExportLimitSet bounds what one export reads; tests lower it.
type captureExportLimitSet struct {
	Metadata  int64 // bytes in one metadata file (host.json, manifest.json, export.json)
	Members   int   // files in one export, metadata included
	PathBytes int   // UTF-8 bytes in one export-relative path
	Stored    int64 // stored bytes across the export
	Decoded   int64 // original (decoded) bytes across the export
}

var (
	captureExportLimits = captureExportLimitSet{Metadata: 16 << 20, Members: 100_000, PathBytes: 4096, Stored: 1 << 40, Decoded: 1 << 40}
	// captureExportHook injects failures and pauses in tests at named steps:
	// "copy" (before each member, rel names it), "staged" (the complete
	// stage before it is verified; rel is its path), "flush", "rename" and
	// "published" (each durability flush after the rename).
	captureExportHook func(step, rel string) error

	errCaptureExportDurability = errors.New("the export was published, but its durability could not be confirmed")
	errCaptureExportScope      = errors.New("this capture is outside what a version-1 capture export covers")
)

// CaptureExport is an export's envelope, export.json. Field order is the
// canonical serialization order.
type CaptureExport struct {
	Format             string              `json:"format"`
	Version            int                 `json:"version"`
	ID                 string              `json:"id,omitempty"`
	Scope              string              `json:"scope"`
	HostID             string              `json:"host_id"`
	SourceName         string              `json:"source_name"`
	SourceKind         string              `json:"source_kind"`
	SourceCompleteness string              `json:"source_completeness"`
	Files              []CaptureExportFile `json:"files"`
}

// CaptureExportFile is one stored file of an export.
type CaptureExportFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// canonical is the envelope's canonical bytes: compact encoding/json output
// of the struct, without a trailing newline.
func (e CaptureExport) canonical() ([]byte, error) {
	if e.Files == nil {
		e.Files = []CaptureExportFile{}
	}
	return json.Marshal(e)
}

// computeID is the SHA-256 of the canonical envelope with id omitted.
func (e CaptureExport) computeID() (string, error) {
	e.ID = ""
	data, err := e.canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ExportCapture publishes the capture of source under root/host as a new
// directory OUT/<id> in outbox, which must exist outside root. It holds the
// host's capture lock (failing with errCaptureBusy rather than waiting)
// while it reads the capture and verifies its own copy, then flushes and
// renames the copy into place without replacing anything. An existing
// OUT/<id> is accepted only if it verifies as the same export.
func ExportCapture(ctx context.Context, root, host, source, outbox string) (CaptureExport, error) {
	if !captureExportSupported {
		return CaptureExport{}, errCaptureExportUnsupported
	}
	if root == "" || host == "" || source == "" || outbox == "" {
		return CaptureExport{}, errors.New("capture export needs a capture root, host, source and outbox")
	}
	for _, name := range []string{host, source} {
		if !validCaptureName(name) || exportSegmentProblem(name) != "" {
			return CaptureExport{}, fmt.Errorf("%q cannot name a captured host or source", name)
		}
	}
	rootPath, err := resolveExportDir(root)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("capture root: %w", err)
	}
	outPath, err := resolveExportDir(outbox)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("outbox: %w", err)
	}
	rootDir, err := exportOpenRoot(rootPath)
	if err != nil {
		return CaptureExport{}, err
	}
	defer rootDir.Close()
	outDir, err := exportOpenRoot(outPath)
	if err != nil {
		return CaptureExport{}, err
	}
	defer outDir.Close()
	if err := checkExportOverlap(rootDir, outDir); err != nil {
		return CaptureExport{}, err
	}
	hostDir, err := rootDir.sub(host)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("host %s has no capture: %w", host, err)
	}
	defer hostDir.Close()
	sourceDir, err := hostDir.sub(source)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("source %s has no capture: %w", source, err)
	}
	defer sourceDir.Close()

	lock, err := freezeCaptureForExport(ctx, hostDir, filepath.Join(rootPath, host, captureLockName))
	if err != nil {
		return CaptureExport{}, err
	}
	locked := true
	unlock := func() {
		if locked {
			releaseLockFile(lock)
			locked = false
		}
	}
	defer unlock()

	stageName, err := exportStageName()
	if err != nil {
		return CaptureExport{}, err
	}
	if err = outDir.mkdir(stageName); err != nil {
		return CaptureExport{}, err
	}
	staged := true
	defer func() {
		if staged {
			_ = outDir.removeTree(stageName)
		}
	}()
	stage, err := outDir.sub(stageName)
	if err != nil {
		return CaptureExport{}, err
	}
	defer stage.Close()

	envelope, err := stageCaptureExport(ctx, rootDir, hostDir, sourceDir, stage, host, source)
	if err != nil {
		return CaptureExport{}, err
	}
	if err = exportHook("staged", stage.path); err != nil {
		return CaptureExport{}, err
	}
	// The copy is checked from its own bytes, as a receiver would, before the
	// capture may change again.
	verified, err := verifyExportDir(ctx, stage)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("the staged copy did not verify: %w", err)
	}
	if !sameExportEnvelope(verified, envelope) {
		return CaptureExport{}, errors.New("the staged copy verified as a different export")
	}
	unlock()

	// Cancellation is honoured up to the rename, the point of publication;
	// after it the export exists and only its flush remains.
	stagePath := filepath.Join(outPath, stageName)
	if err = exportHook("flush", stagePath); err != nil {
		return CaptureExport{}, err
	}
	if err = ctx.Err(); err != nil {
		return CaptureExport{}, err
	}
	if err = flushTree(stagePath); err != nil {
		return CaptureExport{}, err
	}
	if err = syncDirectory(outPath); err != nil {
		return CaptureExport{}, err
	}
	if err = exportHook("rename", envelope.ID); err != nil {
		return CaptureExport{}, err
	}
	if err = ctx.Err(); err != nil {
		return CaptureExport{}, err
	}
	finalPath := filepath.Join(outPath, envelope.ID)
	err = outDir.renameNoReplace(stageName, outDir, envelope.ID)
	if errors.Is(err, os.ErrExist) {
		existing, verr := verifyPublishedExport(ctx, outDir, envelope.ID)
		if verr != nil {
			return CaptureExport{}, fmt.Errorf("%s already exists and is not a valid export: %w", finalPath, verr)
		}
		if !sameExportEnvelope(existing, envelope) {
			return CaptureExport{}, fmt.Errorf("%s already exists and holds a different export", finalPath)
		}
		if err = ctx.Err(); err != nil {
			return CaptureExport{}, err
		}
		// An earlier attempt may have renamed it without confirming its
		// flush; confirm it now before reporting success.
		if err = flushPublishedExport(finalPath, outPath, true); err != nil {
			return envelope, err
		}
		return envelope, nil
	}
	if err != nil {
		return CaptureExport{}, err
	}
	staged = false
	if err = flushPublishedExport(finalPath, outPath, false); err != nil {
		return envelope, err
	}
	return envelope, nil
}

// VerifyCaptureExport checks a published export from its own contents: the
// envelope's canonical form and id, the exact set of files and directories,
// every stored size and digest, the native metadata's closure and each
// member's original bytes. The directory's own name and location do not
// matter.
func VerifyCaptureExport(ctx context.Context, dir string) (CaptureExport, error) {
	if !captureExportSupported {
		return CaptureExport{}, errCaptureExportUnsupported
	}
	if dir == "" {
		return CaptureExport{}, errors.New("capture export verification needs a directory")
	}
	if strings.HasPrefix(filepath.Base(filepath.Clean(dir)), captureExportStagePrefix) {
		return CaptureExport{}, fmt.Errorf("%s is an unpublished stage, not an export", dir)
	}
	handle, err := exportOpenRoot(dir)
	if err != nil {
		return CaptureExport{}, err
	}
	defer handle.Close()
	return verifyExportDir(ctx, handle)
}

func exportHook(step, rel string) error {
	if captureExportHook == nil {
		return nil
	}
	return captureExportHook(step, rel)
}

func exportStageName() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return captureExportStagePrefix + hex.EncodeToString(b[:]), nil
}

func sameExportEnvelope(a, b CaptureExport) bool {
	x, errA := a.canonical()
	y, errB := b.canonical()
	return errA == nil && errB == nil && a.ID == b.ID && bytes.Equal(x, y)
}

// flushPublishedExport makes a renamed export durable: on a retry its whole
// tree, then the outbox entry naming it. A failure leaves the export in
// place and says that it is there.
func flushPublishedExport(finalPath, outPath string, tree bool) error {
	err := exportHook("published", finalPath)
	if err == nil && tree {
		err = flushTree(finalPath)
	}
	if err == nil {
		err = syncDirectory(outPath)
	}
	if err != nil {
		return fmt.Errorf("%w: %s is in place; run the export again to confirm it: %v", errCaptureExportDurability, finalPath, err)
	}
	return nil
}

func verifyPublishedExport(ctx context.Context, outDir *exportDir, name string) (CaptureExport, error) {
	dir, err := outDir.sub(name)
	if err != nil {
		return CaptureExport{}, err
	}
	defer dir.Close()
	return verifyExportDir(ctx, dir)
}

// resolveExportDir is the existing directory path names, with every link
// in it resolved.
func resolveExportDir(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", name)
	}
	return real, nil
}

// checkExportOverlap refuses an outbox inside the capture root or a capture
// root inside the outbox, by directory identity, so that neither a second
// spelling nor a case-insensitive volume hides the overlap.
func checkExportOverlap(root, out *exportDir) error {
	rootStat, err := root.self()
	if err != nil {
		return err
	}
	outStat, err := out.self()
	if err != nil {
		return err
	}
	within := func(start string, target exportStat) (bool, error) {
		for dir := start; ; dir = filepath.Dir(dir) {
			st, err := exportStatPath(dir)
			if err != nil {
				return false, err
			}
			if st.Dev == target.Dev && st.Ino == target.Ino {
				return true, nil
			}
			if filepath.Dir(dir) == dir {
				return false, nil
			}
		}
	}
	if inside, err := within(out.path, rootStat); err != nil || inside {
		if err == nil {
			err = fmt.Errorf("the outbox %s is inside the capture root %s", out.path, root.path)
		}
		return err
	}
	if inside, err := within(root.path, outStat); err != nil || inside {
		if err == nil {
			err = fmt.Errorf("the capture root %s is inside the outbox %s", root.path, out.path)
		}
		return err
	}
	return nil
}

// freezeCaptureForExport takes the host's existing capture lock without
// waiting and without creating, truncating or writing anything.
func freezeCaptureForExport(ctx context.Context, hostDir *exportDir, file string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := hostDir.openLock(captureLockName, file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s does not exist; only an existing capture can be exported", file)
		}
		return nil, err
	}
	if err = filelock.Lock(lock, filelock.Exclusive, filelock.Try); err != nil {
		lock.Close()
		if errors.Is(err, filelock.ErrContended) {
			return nil, errCaptureBusy
		}
		return nil, err
	}
	opFrom(ctx).holdLock(lock)
	return lock, nil
}

// exportSource is the native metadata of one capture and the closure it
// declares, validated.
type exportSource struct {
	hostRaw, manifestRaw []byte
	manifest             *captureManifest
	closure              exportClosure
}

// exportClosure is everything a manifest declares, relative to the source
// directory: each stored member with its recorded claim, and the
// directories an export materializes.
type exportClosure struct {
	members   []string
	claims    map[string]capturedVersion
	emptyDirs []string
	// prefix is the source directory relative to the export,
	// captures/<host>/<source>/.
	prefix string
	files  []string // export-relative regular files: metadata and members
	dirs   []string // export-relative directories, parents first
}

// The manifest fields Go writes as null when empty.
var manifestNullable = map[string]bool{"files": true, "snapshots": true, "path_map": true, "last_run.errors": true}

// parseExportSource validates a capture's raw host.json and manifest.json
// for host and source, then derives the closure they declare.
func parseExportSource(hostRaw, manifestRaw []byte, host, source string) (*exportSource, error) {
	record := captureHostRecord{}
	if err := decodeStrictJSON(hostRaw, &record, nil); err != nil {
		return nil, fmt.Errorf("%s: %w", captureHostName, err)
	}
	if record.ID != host {
		return nil, fmt.Errorf("%s names host %q, not %q", captureHostName, record.ID, host)
	}
	manifest := &captureManifest{}
	if err := decodeStrictJSON(manifestRaw, manifest, manifestNullable); err != nil {
		return nil, fmt.Errorf("%s: %w", captureManifestName, err)
	}
	ceiling := min(captureExportManifestCeiling, captureManifestUnderstood)
	switch {
	case manifest.Version < captureExportManifestFloor:
		return nil, fmt.Errorf("%s has no supported version (%d)", captureManifestName, manifest.Version)
	case manifest.Version > ceiling:
		return nil, fmt.Errorf("%s has version %d; this build exports up to %d", captureManifestName, manifest.Version, ceiling)
	case manifest.writerVersion() > capturePlainManifestVersion && manifest.Version < manifest.writerVersion():
		// Builds that read only the stamped version would hand zstd to a
		// reader; plain entries are valid at every version.
		return nil, fmt.Errorf("%s has version %d but holds entries encoded as version %d", captureManifestName, manifest.Version, manifest.writerVersion())
	}
	if manifest.Host.ID != host || manifest.Source.Name != source {
		return nil, fmt.Errorf("%s belongs to %s/%s, not %s/%s", captureManifestName, manifest.Host.ID, manifest.Source.Name, host, source)
	}
	spec, known := providerFor(manifest.Source.Kind)
	if !known || spec.Kind != "claude" && spec.Kind != "codex" {
		return nil, fmt.Errorf("%w: only Claude Code and Codex captures are exported, not %q", errCaptureExportScope, manifest.Source.Kind)
	}
	if problem := manifest.exportScopeProblem(); problem != "" {
		return nil, fmt.Errorf("%w: %s", errCaptureExportScope, problem)
	}
	if bad, unsafe := manifest.unsafePath(); unsafe {
		return nil, fmt.Errorf("%s names unsafe captured path %q", captureManifestName, bad)
	}
	closure, err := manifest.exportClosure(host, source)
	if err != nil {
		return nil, err
	}
	return &exportSource{hostRaw: hostRaw, manifestRaw: manifestRaw, manifest: manifest, closure: closure}, nil
}

// exportScopeProblem names a layout whose evidence lives outside the
// native files and history this first scope copies.
func (m *captureManifest) exportScopeProblem() string {
	switch {
	case m.SQLiteRecordCapture:
		return "it captures SQLite records"
	case m.LatestRecords:
		return "it keeps latest SQLite records"
	case m.RetainedInputCapture:
		return "it retains immutable inputs"
	case len(m.RecordCoverage) > 0:
		return "it records SQLite record coverage"
	case len(m.Installations) > 0:
		return "it maps installations"
	}
	for _, file := range m.Files {
		if file.RecoveryID != "" {
			return "a file belongs to a recovery"
		}
	}
	for _, snapshot := range m.Snapshots {
		if snapshot.RecoveryID != "" {
			return "a snapshot belongs to a recovery"
		}
	}
	return ""
}

// exportClosure lists each file's latest and earlier versions and each
// snapshot's latest copy and generations, missing-at-source entries
// included, with consistent claims for any path two entries share.
func (m *captureManifest) exportClosure(host, source string) (exportClosure, error) {
	c := exportClosure{claims: map[string]capturedVersion{}, prefix: path.Join(captureExportCaptures, host, source) + "/"}
	current := map[string]bool{}
	add := func(v capturedVersion, latest bool) error {
		if err := checkExportClaim(v); err != nil {
			return err
		}
		if latest {
			current[v.Captured] = true
		}
		old, seen := c.claims[v.Captured]
		if !seen {
			c.claims[v.Captured] = v
			c.members = append(c.members, v.Captured)
			return nil
		}
		if old.Size != v.Size || old.Encoding != v.Encoding || old.StoredSize != v.StoredSize || old.SHA256 != "" && v.SHA256 != "" && old.SHA256 != v.SHA256 {
			return fmt.Errorf("%s is claimed twice with different contents", v.Captured)
		}
		if old.SHA256 == "" {
			old.SHA256 = v.SHA256
			c.claims[v.Captured] = old
		}
		return nil
	}
	for _, file := range m.Files {
		if err := add(file.version(), true); err != nil {
			return c, err
		}
		for _, previous := range file.Previous {
			if err := add(previous, false); err != nil {
				return c, err
			}
		}
	}
	for _, snapshot := range m.Snapshots {
		if err := add(snapshot.version(), true); err != nil {
			return c, err
		}
		for _, generation := range snapshot.Generations {
			if err := add(generation, false); err != nil {
				return c, err
			}
		}
	}
	slices.Sort(c.members)
	limits := captureExportLimits
	if len(c.members) > limits.Members-2 {
		return c, fmt.Errorf("%w: it declares %d files; an export holds up to %d", errCaptureExportScope, len(c.members), limits.Members-2)
	}
	var stored, decoded int64
	for _, rel := range c.members {
		v := c.claims[rel]
		if stored, decoded = addBounded(stored, v.storedSize(), limits.Stored), addBounded(decoded, v.Size, limits.Decoded); stored < 0 || decoded < 0 {
			return c, fmt.Errorf("%w: its declared bytes exceed the export limits", errCaptureExportScope)
		}
	}

	// Directories: every member's ancestors, and each mapped root that has
	// no member, which an export materializes empty.
	dirs := map[string]bool{}
	addAncestors := func(rel string) {
		for dir := path.Dir(rel); dir != "." && !dirs[dir]; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for _, rel := range c.members {
		addAncestors(rel)
	}
	for _, mapping := range m.PathMap {
		rel := mapping.Captured
		if current[rel] || dirs[rel] {
			continue
		}
		if _, member := c.claims[rel]; member {
			return c, fmt.Errorf("mapped root %s is an earlier version, not a latest capture", rel)
		}
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			if _, member := c.claims[dir]; member {
				return c, fmt.Errorf("mapped root %s lies under the captured file %s", rel, dir)
			}
		}
		if !slices.Contains(c.emptyDirs, rel) {
			c.emptyDirs = append(c.emptyDirs, rel)
		}
		dirs[rel] = true
		addAncestors(rel)
	}
	slices.Sort(c.emptyDirs)
	for rel := range c.claims {
		if dirs[rel] {
			return c, fmt.Errorf("%s is both a captured file and a directory", rel)
		}
	}

	hostDir := path.Join(captureExportCaptures, host)
	c.files = []string{path.Join(hostDir, captureHostName), c.prefix + captureManifestName}
	for _, rel := range c.members {
		c.files = append(c.files, c.prefix+rel)
	}
	c.dirs = []string{captureExportCaptures, hostDir, strings.TrimSuffix(c.prefix, "/")}
	for dir := range dirs {
		c.dirs = append(c.dirs, c.prefix+dir)
	}
	slices.Sort(c.files)
	slices.Sort(c.dirs)
	if err := checkExportPaths(c.files, c.dirs); err != nil {
		return c, err
	}
	return c, nil
}

// addBounded is total+n, or -1 once it would pass limit.
func addBounded(total, n, limit int64) int64 {
	if total < 0 || n < 0 || n > limit-total {
		return -1
	}
	return total + n
}

func checkExportClaim(v capturedVersion) error {
	switch {
	case v.Size < 0 || v.Size > captureExportMaxSize:
		return fmt.Errorf("%s records an invalid size %d", v.Captured, v.Size)
	case v.StoredSize < 0 || v.StoredSize > captureExportMaxSize:
		return fmt.Errorf("%s records an invalid stored size %d", v.Captured, v.StoredSize)
	case v.SHA256 != "" && !validExportHash(v.SHA256):
		return fmt.Errorf("%s records a malformed digest", v.Captured)
	case v.Encoding == "" && v.StoredSize != 0:
		return fmt.Errorf("%s records a stored size without an encoding", v.Captured)
	case v.Encoding == captureEncodingZstd && v.StoredSize == 0:
		return fmt.Errorf("%s is encoded without a stored size", v.Captured)
	case v.Encoding != "" && v.Encoding != captureEncodingZstd:
		return fmt.Errorf("%s has unsupported capture encoding %q", v.Captured, v.Encoding)
	}
	return nil
}

func validExportHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !('0' <= s[i] && s[i] <= '9' || 'a' <= s[i] && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// checkExportPaths requires every export-relative path to be a canonical
// slash name that every supported filesystem stores as itself, and no two
// to collide once normalized (NFC) and case-folded, whole or in part. Stored
// names are compared, never rewritten.
func checkExportPaths(files, dirs []string) error {
	folder := cases.Fold()
	seen := map[string]string{}
	check := func(rel string) error {
		if len(rel) > captureExportLimits.PathBytes {
			return fmt.Errorf("%w: a path is longer than %d bytes", errCaptureExportScope, captureExportLimits.PathBytes)
		}
		if !utf8.ValidString(rel) {
			return fmt.Errorf("%q is not valid UTF-8", rel)
		}
		if len(rel) >= 2 && rel[1] == ':' || strings.HasPrefix(rel, "//") {
			return fmt.Errorf("%q is a drive or network path", rel)
		}
		for _, segment := range strings.Split(rel, "/") {
			if problem := exportSegmentProblem(segment); problem != "" {
				return fmt.Errorf("%q %s", rel, problem)
			}
		}
		key := norm.NFC.String(folder.String(norm.NFC.String(rel)))
		if other, ok := seen[key]; ok && other != rel {
			return fmt.Errorf("%q and %q name the same file on a case-insensitive or normalizing filesystem", other, rel)
		}
		seen[key] = rel
		return nil
	}
	for _, list := range [][]string{dirs, files} {
		for _, rel := range list {
			if err := check(rel); err != nil {
				return err
			}
		}
	}
	if len(seen) != len(files)+len(dirs) {
		return errors.New("a path names both a file and a directory")
	}
	return nil
}

// exportSegmentProblem describes why one path segment cannot travel, or "".
func exportSegmentProblem(segment string) string {
	switch {
	case segment == "":
		return "has an empty segment"
	case segment == "." || segment == "..":
		return "has a dot segment"
	case strings.ContainsAny(segment, "\\:\x00"):
		return "has a backslash, colon or NUL"
	case strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " "):
		return "has a segment ending in a dot or space"
	}
	stem, _, _ := strings.Cut(segment, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return "has a reserved Windows name"
	}
	if len(stem) >= 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) {
		if digit := stem[3:]; len(digit) == 1 && '0' <= digit[0] && digit[0] <= '9' || digit == "¹" || digit == "²" || digit == "³" {
			return "has a reserved Windows name"
		}
	}
	return ""
}

// stageCaptureExport reads the frozen capture and copies its declared
// closure into stage, writing the envelope last.
func stageCaptureExport(ctx context.Context, rootDir, hostDir, sourceDir, stage *exportDir, host, source string) (CaptureExport, error) {
	if err := checkNoHistoryBackfill(rootDir); err != nil {
		return CaptureExport{}, err
	}
	hostRaw, hostStat, err := readExportMetadata(hostDir, captureHostName)
	if err != nil {
		return CaptureExport{}, err
	}
	manifestRaw, manifestStat, err := readExportMetadata(sourceDir, captureManifestName)
	if err != nil {
		return CaptureExport{}, err
	}
	src, err := parseExportSource(hostRaw, manifestRaw, host, source)
	if err != nil {
		return CaptureExport{}, err
	}
	c := src.closure
	if err = checkSourceRoot(sourceDir); err != nil {
		return CaptureExport{}, err
	}
	present, err := enumerateNative(ctx, sourceDir)
	if err != nil {
		return CaptureExport{}, err
	}
	for _, rel := range c.members {
		if _, ok := present[rel]; !ok {
			return CaptureExport{}, fmt.Errorf("declared member %s is missing from the capture", rel)
		}
	}
	for _, rel := range c.emptyDirs {
		if _, ok := present[rel]; ok {
			return CaptureExport{}, fmt.Errorf("mapped root %s is a file the manifest does not declare", rel)
		}
	}

	for _, dir := range c.dirs {
		if err = mkdirExport(stage, dir); err != nil {
			return CaptureExport{}, err
		}
	}
	envelope := CaptureExport{Format: captureExportFormat, Version: captureExportVersion, Scope: captureExportScope,
		HostID: host, SourceName: source, SourceKind: src.manifest.Source.Kind, SourceCompleteness: captureExportCompleteness}
	manifestPath := c.prefix + captureManifestName
	for _, meta := range []struct {
		rel  string
		data []byte
	}{{path.Join(captureExportCaptures, host, captureHostName), hostRaw}, {manifestPath, manifestRaw}} {
		if err = writeExportFile(stage, meta.rel, meta.data); err != nil {
			return CaptureExport{}, err
		}
		sum := sha256.Sum256(meta.data)
		envelope.Files = append(envelope.Files, CaptureExportFile{Path: meta.rel, Size: int64(len(meta.data)), SHA256: hex.EncodeToString(sum[:])})
	}

	budget := &exportBudget{}
	if budget.stored = addBounded(0, int64(len(hostRaw)+len(manifestRaw)), captureExportLimits.Stored); budget.stored < 0 {
		return CaptureExport{}, fmt.Errorf("%w: its metadata exceeds the stored-byte limit", errCaptureExportScope)
	}
	sources := map[string]exportStat{}
	for _, rel := range c.members {
		if err = exportHook("copy", rel); err != nil {
			return CaptureExport{}, err
		}
		file, st, err := copyExportMember(ctx, sourceDir, stage, c.prefix, rel, c.claims[rel], present[rel], budget)
		if err != nil {
			return CaptureExport{}, err
		}
		sources[rel] = st
		envelope.Files = append(envelope.Files, file)
	}
	slices.SortFunc(envelope.Files, func(a, b CaptureExportFile) int { return strings.Compare(a.Path, b.Path) })

	// Before the freeze ends: the metadata still holds the same bytes, every
	// member is the file that was copied, unchanged, and nothing began a
	// layout this export cannot carry.
	for _, meta := range []struct {
		dir  *exportDir
		name string
		data []byte
		st   exportStat
	}{{hostDir, captureHostName, hostRaw, hostStat}, {sourceDir, captureManifestName, manifestRaw, manifestStat}} {
		again, st, err := readExportMetadata(meta.dir, meta.name)
		if err != nil {
			return CaptureExport{}, err
		}
		if !bytes.Equal(again, meta.data) || !st.sameFile(meta.st) {
			return CaptureExport{}, fmt.Errorf("%s changed while it was exported", meta.name)
		}
	}
	for _, rel := range c.members {
		st, ok, err := sourceDir.statPath(rel)
		if err != nil {
			return CaptureExport{}, err
		}
		if !ok || !st.sameFile(sources[rel]) {
			return CaptureExport{}, fmt.Errorf("%s changed while it was exported", rel)
		}
	}
	if err = checkNoHistoryBackfill(rootDir); err != nil {
		return CaptureExport{}, err
	}

	if envelope.ID, err = envelope.computeID(); err != nil {
		return CaptureExport{}, err
	}
	data, err := envelope.canonical()
	if err != nil {
		return CaptureExport{}, err
	}
	if err = writeExportFile(stage, captureExportEnvelope, append(data, '\n')); err != nil {
		return CaptureExport{}, err
	}
	return envelope, nil
}

func checkNoHistoryBackfill(rootDir *exportDir) error {
	_, exists, err := rootDir.stat(historyDir)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: the capture root has %s, whose recovered history an export does not yet carry", errCaptureExportScope, historyDir)
	}
	return nil
}

// checkSourceRoot admits, at the top of a source's capture directory, only
// the manifest, files and history, and the state an export leaves out:
// the index marker and lock, compression outcomes and reader leases.
func checkSourceRoot(sourceDir *exportDir) error {
	names, err := sourceDir.entries()
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		st, ok, err := sourceDir.stat(name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		want := exportTypeRegular
		switch {
		case name == captureManifestName, name == indexMarkerName, name == indexLockName, name == compressionOutcomesName:
		case name == captureFilesDir, name == captureHistoryDir:
			want = exportTypeDir
		case strings.HasPrefix(name, ".reader-pin-"):
			return fmt.Errorf("%w: %s is a preservation-pin reader, which an export does not yet carry", errCaptureExportScope, name)
		case (strings.HasPrefix(name, ".reader-") || strings.HasPrefix(name, leaseStagingPrefix)) && strings.HasSuffix(name, captureTempSuffix):
			want = exportTypeDir
		case name == "records", name == "inputs", name == preservationPinsName, name == ".delivered.json":
			return fmt.Errorf("%w: the capture has %s, whose retained evidence an export does not yet carry", errCaptureExportScope, name)
		default:
			return fmt.Errorf("%w: the capture has an unrecognized entry %s", errCaptureExportScope, name)
		}
		if st.Type != want {
			return fmt.Errorf("%s in the capture is not a %s", name, map[uint32]string{exportTypeRegular: "regular file", exportTypeDir: "directory"}[want])
		}
	}
	return nil
}

// enumerateNative lists every regular file and directory under the
// source's files/ and history/, relative to the source directory, and
// fails on a link or special node anywhere there.
func enumerateNative(ctx context.Context, sourceDir *exportDir) (map[string]exportStat, error) {
	files, dirs := map[string]exportStat{}, map[string]bool{}
	for _, top := range []string{captureFilesDir, captureHistoryDir} {
		st, ok, err := sourceDir.stat(top)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if !st.isDir() {
			return nil, fmt.Errorf("%s in the capture is not a directory", top)
		}
		if err = walkExportTree(ctx, sourceDir, top, files, dirs); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// walkExportTree records rel, a directory below base, and everything in it.
func walkExportTree(ctx context.Context, base *exportDir, rel string, files map[string]exportStat, dirs map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := base.subPath(rel)
	if err != nil {
		return err
	}
	defer dir.Close()
	dirs[rel] = true
	names, err := dir.entries()
	if err != nil {
		return err
	}
	slices.Sort(names)
	for _, name := range names {
		st, ok, err := dir.stat(name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s/%s disappeared while it was listed", rel, name)
		}
		child := rel + "/" + name
		switch st.Type {
		case exportTypeRegular:
			files[child] = st
		case exportTypeDir:
			if err = walkExportTree(ctx, base, child, files, dirs); err != nil {
				return err
			}
		case exportTypeSymlink:
			return fmt.Errorf("%s is a symbolic link", child)
		default:
			return fmt.Errorf("%s is not a regular file or directory", child)
		}
	}
	return nil
}

// readExportMetadata reads the bounded metadata file name in dir.
func readExportMetadata(dir *exportDir, name string) ([]byte, exportStat, error) {
	f, st, err := dir.openRead(name)
	if err != nil {
		return nil, st, err
	}
	defer f.Close()
	return readBoundedExportFile(f, st)
}

func readBoundedExportFile(f *os.File, st exportStat) ([]byte, exportStat, error) {
	if st.Size > captureExportLimits.Metadata {
		return nil, st, fmt.Errorf("%w: %s is larger than %d bytes", errCaptureExportScope, f.Name(), captureExportLimits.Metadata)
	}
	data, err := io.ReadAll(io.LimitReader(f, st.Size+1))
	if err != nil {
		return nil, st, err
	}
	after, err := fstatExport(f)
	if err != nil {
		return nil, st, err
	}
	if int64(len(data)) != st.Size || !after.sameFile(st) {
		return nil, st, fmt.Errorf("%s changed while it was read", f.Name())
	}
	return data, st, nil
}

func mkdirExport(stage *exportDir, rel string) error {
	parent, name, err := stage.walkParent(rel)
	if err != nil {
		return err
	}
	if parent != stage {
		defer parent.Close()
	}
	return parent.mkdir(name)
}

func createExportFile(stage *exportDir, rel string) (*os.File, error) {
	parent, name, err := stage.walkParent(rel)
	if err != nil {
		return nil, err
	}
	if parent != stage {
		defer parent.Close()
	}
	return parent.create(name)
}

func writeExportFile(stage *exportDir, rel string, data []byte) error {
	f, err := createExportFile(stage, rel)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// exportBudget counts stored and decoded bytes across one export.
type exportBudget struct{ stored, decoded int64 }

// copyExportMember copies one declared member into the stage from a single
// open handle, hashing exactly the bytes it copies and checking them against
// the manifest's claim, and requires the source file to be the one listed
// and unchanged throughout.
func copyExportMember(ctx context.Context, sourceDir, stage *exportDir, prefix, rel string, claim capturedVersion, listed exportStat, budget *exportBudget) (CaptureExportFile, exportStat, error) {
	f, before, err := sourceDir.openReadFile(rel)
	if err != nil {
		return CaptureExportFile{}, before, err
	}
	defer f.Close()
	if before.Dev != listed.Dev || before.Ino != listed.Ino {
		return CaptureExportFile{}, before, fmt.Errorf("%s changed while it was exported", rel)
	}
	dst, err := createExportFile(stage, prefix+rel)
	if err != nil {
		return CaptureExportFile{}, before, err
	}
	digest, err := checkExportMember(ctx, f, before, claim, dst, budget)
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return CaptureExportFile{}, before, fmt.Errorf("%s: %w", rel, err)
	}
	after, err := fstatExport(f)
	if err != nil {
		return CaptureExportFile{}, before, err
	}
	again, ok, err := sourceDir.statPath(rel)
	if err != nil {
		return CaptureExportFile{}, before, err
	}
	if !after.sameFile(before) || !ok || !again.sameFile(before) {
		return CaptureExportFile{}, before, fmt.Errorf("%s changed while it was exported", rel)
	}
	return CaptureExportFile{Path: prefix + rel, Size: before.Size, SHA256: digest}, before, nil
}

// checkExportMember streams the stored bytes of f (st as opened) once:
// into dst when set, into the stored-byte digest it returns, and, decoded
// when encoded, against the claim's original size and digest. An entry
// recorded as zstd whose file still holds its verified original bytes (a
// compression interrupted before its rename) passes; compressed bytes under
// a plain entry do not.
func checkExportMember(ctx context.Context, f *os.File, st exportStat, claim capturedVersion, dst io.Writer, budget *exportBudget) (string, error) {
	v := storedAsFile(f, claim)
	if want := v.storedSize(); st.Size != want {
		return "", fmt.Errorf("%w: it stores %d bytes where %d are recorded", errCapturedContentMismatch, st.Size, want)
	}
	if budget.stored = addBounded(budget.stored, st.Size, captureExportLimits.Stored); budget.stored < 0 {
		return "", fmt.Errorf("%w: the export exceeds %d stored bytes", errCaptureExportScope, captureExportLimits.Stored)
	}
	stored := sha256.New()
	var out io.Writer = stored
	if dst != nil {
		out = io.MultiWriter(dst, stored)
	}
	src := &recordCountReader{r: io.NewSectionReader(f, 0, st.Size+1), ctx: ctx}
	tee := io.TeeReader(src, out)
	buffer := make([]byte, 1<<20)
	var decoded int64
	logical := ""
	switch v.Encoding {
	case "":
		n, err := io.CopyBuffer(io.Discard, tee, buffer)
		if err != nil {
			return "", err
		}
		decoded = n
	case captureEncodingZstd:
		decoder, err := newCaptureDecoder(bufio.NewReaderSize(tee, 1<<20))
		if err != nil {
			return "", err
		}
		digest := sha256.New()
		n, err := io.CopyBuffer(digest, &recordCountReader{r: io.LimitReader(decoder, v.Size+1), ctx: ctx}, buffer)
		decoder.Close()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			return "", fmt.Errorf("%w: %v", errCapturedContentMismatch, err)
		}
		// Whatever follows the frames still belongs to the stored bytes.
		if _, err = io.CopyBuffer(io.Discard, tee, buffer); err != nil {
			return "", err
		}
		decoded, logical = n, hex.EncodeToString(digest.Sum(nil))
	default:
		return "", fmt.Errorf("unsupported capture encoding %q", v.Encoding)
	}
	if src.n != st.Size {
		return "", fmt.Errorf("it changed size while it was read")
	}
	storedDigest := hex.EncodeToString(stored.Sum(nil))
	if logical == "" {
		logical = storedDigest
	}
	if decoded != v.Size {
		return "", fmt.Errorf("%w: it holds %d original bytes where %d are recorded", errCapturedContentMismatch, decoded, v.Size)
	}
	if v.SHA256 != "" && logical != v.SHA256 {
		return "", fmt.Errorf("%w: its original bytes do not match the recorded digest", errCapturedContentMismatch)
	}
	if budget.decoded = addBounded(budget.decoded, decoded, captureExportLimits.Decoded); budget.decoded < 0 {
		return "", fmt.Errorf("%w: the export exceeds %d decoded bytes", errCaptureExportScope, captureExportLimits.Decoded)
	}
	return storedDigest, nil
}

// verifyExportDir checks the export rooted at dir; see VerifyCaptureExport.
func verifyExportDir(ctx context.Context, dir *exportDir) (CaptureExport, error) {
	names, err := dir.entries()
	if err != nil {
		return CaptureExport{}, err
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{captureExportCaptures, captureExportEnvelope}) {
		return CaptureExport{}, fmt.Errorf("an export holds exactly %s and %s/, not %v", captureExportEnvelope, captureExportCaptures, names)
	}
	f, st, err := dir.openRead(captureExportEnvelope)
	if err != nil {
		return CaptureExport{}, err
	}
	data, _, err := readBoundedExportFile(f, st)
	f.Close()
	if err != nil {
		return CaptureExport{}, err
	}
	envelope, err := parseExportEnvelope(data)
	if err != nil {
		return CaptureExport{}, fmt.Errorf("%s: %w", captureExportEnvelope, err)
	}

	files, dirs := map[string]exportStat{}, map[string]bool{}
	if err = walkExportTree(ctx, dir, captureExportCaptures, files, dirs); err != nil {
		return CaptureExport{}, err
	}
	host, source := envelope.HostID, envelope.SourceName
	if !validCaptureName(host) || !validCaptureName(source) {
		return CaptureExport{}, errors.New("the envelope names an invalid host or source")
	}
	hostPath := path.Join(captureExportCaptures, host, captureHostName)
	manifestPath := path.Join(captureExportCaptures, host, source, captureManifestName)
	listed := map[string]CaptureExportFile{}
	for _, file := range envelope.Files {
		listed[file.Path] = file
	}
	if _, ok := listed[hostPath]; !ok {
		return CaptureExport{}, fmt.Errorf("the envelope does not list %s", hostPath)
	}
	if _, ok := listed[manifestPath]; !ok {
		return CaptureExport{}, fmt.Errorf("the envelope does not list %s", manifestPath)
	}
	read := func(rel string) ([]byte, error) {
		f, st, err := dir.openReadFile(rel)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, _, err := readBoundedExportFile(f, st)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if entry := listed[rel]; entry.Size != int64(len(data)) || entry.SHA256 != hex.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("%s does not match its recorded size and digest", rel)
		}
		return data, nil
	}
	hostRaw, err := read(hostPath)
	if err != nil {
		return CaptureExport{}, err
	}
	manifestRaw, err := read(manifestPath)
	if err != nil {
		return CaptureExport{}, err
	}
	src, err := parseExportSource(hostRaw, manifestRaw, host, source)
	if err != nil {
		return CaptureExport{}, err
	}
	if envelope.SourceKind != src.manifest.Source.Kind {
		return CaptureExport{}, fmt.Errorf("the envelope's source kind %q is not the manifest's %q", envelope.SourceKind, src.manifest.Source.Kind)
	}
	c := src.closure
	paths := make([]string, 0, len(envelope.Files))
	for _, file := range envelope.Files {
		paths = append(paths, file.Path)
	}
	present := make([]string, 0, len(files))
	for rel := range files {
		present = append(present, rel)
	}
	slices.Sort(present)
	if !slices.Equal(paths, c.files) {
		return CaptureExport{}, fmt.Errorf("the envelope lists %d files where the manifest declares %d", len(paths), len(c.files))
	}
	if !slices.Equal(present, c.files) {
		return CaptureExport{}, exportSetDifference("file", c.files, present)
	}
	presentDirs := make([]string, 0, len(dirs))
	for rel := range dirs {
		presentDirs = append(presentDirs, rel)
	}
	slices.Sort(presentDirs)
	if !slices.Equal(presentDirs, c.dirs) {
		return CaptureExport{}, exportSetDifference("directory", c.dirs, presentDirs)
	}

	budget := &exportBudget{stored: int64(len(hostRaw) + len(manifestRaw))}
	for _, rel := range c.members {
		entry := listed[c.prefix+rel]
		f, st, err := dir.openReadFile(c.prefix + rel)
		if err != nil {
			return CaptureExport{}, err
		}
		if st.Size != entry.Size {
			f.Close()
			return CaptureExport{}, fmt.Errorf("%s holds %d bytes where the envelope records %d", entry.Path, st.Size, entry.Size)
		}
		digest, err := checkExportMember(ctx, f, st, c.claims[rel], nil, budget)
		f.Close()
		if err != nil {
			return CaptureExport{}, fmt.Errorf("%s: %w", entry.Path, err)
		}
		if digest != entry.SHA256 {
			return CaptureExport{}, fmt.Errorf("%s does not match its recorded digest", entry.Path)
		}
	}
	return envelope, nil
}

func exportSetDifference(kind string, want, have []string) error {
	for _, rel := range have {
		if _, found := slices.BinarySearch(want, rel); !found {
			return fmt.Errorf("the export has an undeclared %s %s", kind, rel)
		}
	}
	for _, rel := range want {
		if _, found := slices.BinarySearch(have, rel); !found {
			return fmt.Errorf("the export is missing the %s %s", kind, rel)
		}
	}
	return fmt.Errorf("the export's %ss differ from its declaration", kind)
}

// parseExportEnvelope accepts exactly the canonical envelope, optionally
// followed by one newline.
func parseExportEnvelope(data []byte) (CaptureExport, error) {
	envelope := CaptureExport{}
	if err := decodeStrictJSON(data, &envelope, nil); err != nil {
		return envelope, err
	}
	switch {
	case envelope.Format != captureExportFormat:
		return envelope, fmt.Errorf("format %q is not %q", envelope.Format, captureExportFormat)
	case envelope.Version != captureExportVersion:
		return envelope, fmt.Errorf("version %d is not supported; this build reads version %d", envelope.Version, captureExportVersion)
	case envelope.Scope != captureExportScope:
		return envelope, fmt.Errorf("scope %q is not supported", envelope.Scope)
	case envelope.SourceCompleteness != captureExportCompleteness:
		return envelope, fmt.Errorf("source completeness %q is not %q", envelope.SourceCompleteness, captureExportCompleteness)
	case !validExportHash(envelope.ID):
		return envelope, errors.New("the id is not 64 lowercase hex digits")
	case len(envelope.Files) > captureExportLimits.Members:
		return envelope, fmt.Errorf("%w: it lists %d files", errCaptureExportScope, len(envelope.Files))
	}
	var stored int64
	for i, file := range envelope.Files {
		switch {
		case i > 0 && file.Path <= envelope.Files[i-1].Path:
			return envelope, fmt.Errorf("files are not sorted and unique at %q", file.Path)
		case file.Path == captureExportEnvelope:
			return envelope, fmt.Errorf("files lists %s", captureExportEnvelope)
		case file.Size < 0 || file.Size > captureExportMaxSize:
			return envelope, fmt.Errorf("%s records an invalid size", file.Path)
		case !validExportHash(file.SHA256):
			return envelope, fmt.Errorf("%s records a malformed digest", file.Path)
		}
		if stored = addBounded(stored, file.Size, captureExportLimits.Stored); stored < 0 {
			return envelope, fmt.Errorf("%w: it exceeds %d stored bytes", errCaptureExportScope, captureExportLimits.Stored)
		}
	}
	canonical, err := envelope.canonical()
	if err != nil {
		return envelope, err
	}
	if !bytes.Equal(data, canonical) && !bytes.Equal(data, append(canonical, '\n')) {
		return envelope, errors.New("it is not in canonical form")
	}
	id, err := envelope.computeID()
	if err != nil {
		return envelope, err
	}
	if id != envelope.ID {
		return envelope, errors.New("its id does not match its contents")
	}
	return envelope, nil
}

// decodeStrictJSON decodes exactly one JSON value into v, a pointer to a
// struct, after checking it against v's type: valid UTF-8, no duplicate or
// unknown keys (spelled exactly), the right JSON type for every field, and
// null only at the paths nullable names (as "files" or "last_run.errors").
func decodeStrictJSON(data []byte, v any, nullable map[string]bool) error {
	if !utf8.Valid(data) {
		return errors.New("it is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := strictJSONValue(decoder, reflect.TypeOf(v).Elem(), "", nullable); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("it holds more than one JSON value")
	}
	return json.Unmarshal(data, v)
}

func strictJSONValue(decoder *json.Decoder, t reflect.Type, at string, nullable map[string]bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	name := at
	if name == "" {
		name = "the document"
	}
	if token == nil {
		if nullable[at] && (t.Kind() == reflect.Slice || t.Kind() == reflect.Map) {
			return nil
		}
		return fmt.Errorf("%s is null", name)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	mismatch := fmt.Errorf("%s is not a JSON %s", name, t.Kind())
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		if delim, ok := token.(json.Delim); !ok || delim != '{' {
			return mismatch
		}
		fields := map[string]reflect.Type{}
		if t.Kind() == reflect.Struct {
			jsonFieldTypes(t, fields)
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key := token.(string)
			if seen[key] {
				return fmt.Errorf("%s repeats the key %q", name, key)
			}
			seen[key] = true
			child, field := t, joinJSONPath(at, key)
			if t.Kind() == reflect.Map {
				child, field = t.Elem(), joinJSONPath(at, "*")
			} else if child = fields[key]; child == nil {
				return fmt.Errorf("%s has an unknown key %q", name, key)
			}
			if err = strictJSONValue(decoder, child, field, nullable); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case reflect.Slice:
		if delim, ok := token.(json.Delim); !ok || delim != '[' {
			return mismatch
		}
		for decoder.More() {
			if err = strictJSONValue(decoder, t.Elem(), at+"[]", nullable); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case reflect.String:
		if _, ok := token.(string); !ok {
			return mismatch
		}
	case reflect.Bool:
		if _, ok := token.(bool); !ok {
			return mismatch
		}
	case reflect.Int, reflect.Int64:
		number, ok := token.(json.Number)
		if !ok {
			return mismatch
		}
		if _, err := strconv.ParseInt(string(number), 10, 64); err != nil {
			return fmt.Errorf("%s is not an integer", name)
		}
	default:
		return fmt.Errorf("%s has an unsupported type", name)
	}
	return nil
}

func joinJSONPath(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// jsonFieldTypes collects the JSON names encoding/json uses for t's fields,
// flattening embedded structs.
func jsonFieldTypes(t reflect.Type, fields map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" || !field.IsExported() && !field.Anonymous {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			jsonFieldTypes(field.Type, fields)
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
}

// runCaptureExportCLI is pharos capture-export. It reads only the capture
// root and writes only the outbox: no configuration, library or catalog.
func runCaptureExportCLI(args []string) error {
	usage := errors.New("usage: pharos capture-export --root CAPTURE_ROOT --host HOST --out EXISTING_OUTBOX SOURCE")
	flags := flag.NewFlagSet("capture-export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root, host, out := &onceString{}, &onceString{}, &onceString{}
	flags.Var(root, "root", "capture root")
	flags.Var(host, "host", "captured host id")
	flags.Var(out, "out", "existing outbox directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || root.value == "" || host.value == "" || out.value == "" {
		return usage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	export, err := ExportCapture(ctx, expandPath(root.value), host.value, flags.Arg(0), expandPath(out.value))
	if err != nil {
		if errors.Is(err, errCaptureExportDurability) {
			_ = printJSON(export)
		}
		return err
	}
	return printJSON(export)
}

// runCaptureExportVerifyCLI is pharos capture-export-verify.
func runCaptureExportVerifyCLI(args []string) error {
	flags := flag.NewFlagSet("capture-export-verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		return errors.New("usage: pharos capture-export-verify EXPORT_DIR")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	export, err := VerifyCaptureExport(ctx, expandPath(flags.Arg(0)))
	if err != nil {
		return err
	}
	return printJSON(export)
}

// onceString is a flag that may be given once.
type onceString struct{ value string }

func (s *onceString) String() string { return s.value }
func (s *onceString) Set(value string) error {
	if s.value != "" {
		return errors.New("given twice")
	}
	if value == "" {
		return errors.New("empty")
	}
	s.value = value
	return nil
}
