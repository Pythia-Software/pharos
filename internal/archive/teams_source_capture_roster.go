package archive

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/gbdubs/pharos/internal/platform/localidentity"
)

const teamsCaptureManifestMaxBytes = 8 << 20

var teamsReadManifestBytes = io.ReadAll

// Inspect the optional compatibility directory through the same pinned root.
// At most the single supported name is allowed.
func teamsCaptureRecordsCompatible(root *exportDir) error {
	entry, exists, err := root.stat("records")
	if err != nil || !exists {
		return err
	}
	if !entry.isDir() {
		return errTeamsSourceUnavailable
	}
	dir, err := root.sub("records")
	if err != nil {
		return err
	}
	defer dir.Close()
	before, err := dir.self()
	if err != nil || !entry.sameFile(before) {
		return errTeamsSnapshotChanged
	}
	lister, err := dir.list()
	if err != nil {
		return err
	}
	defer lister.Close()
	count := 0
	for {
		names, readErr := lister.next(2)
		for _, name := range names {
			count++
			if count > 1 || name != "v1" {
				return errTeamsSourceUnavailable
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
		if len(names) == 0 {
			return errTeamsSourceUnavailable
		}
	}
	after, err := dir.self()
	if err != nil || !before.sameFile(after) {
		return errTeamsSnapshotChanged
	}
	named, exists, err := root.stat("records")
	if err != nil || !exists || !after.sameFile(named) {
		return errTeamsSnapshotChanged
	}
	return nil
}

// Read at most one bounded manifest from a pinned descriptor. A size check
// alone is insufficient because another process can grow the file after stat.
func teamsLoadBoundedCaptureManifest(dir string) (*captureManifest, error) {
	root, err := exportOpenAcquisitionRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = teamsCaptureRecordsCompatible(root); err != nil {
		return nil, err
	}
	f, initial, err := root.openRead(captureManifestName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > teamsCaptureManifestMaxBytes {
		return nil, errTeamsSourceUnavailable
	}
	beforeStamp, err := localidentity.Acquisition(before)
	if err != nil {
		return nil, errTeamsSourceUnavailable
	}
	raw, err := teamsReadManifestBytes(io.LimitReader(f, teamsCaptureManifestMaxBytes+1))
	if err != nil || len(raw) > teamsCaptureManifestMaxBytes {
		return nil, errTeamsSourceUnavailable
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || int64(len(raw)) != after.Size() {
		return nil, errTeamsSnapshotChanged
	}
	afterStamp, err := localidentity.Acquisition(after)
	if err != nil || beforeStamp != afterStamp {
		return nil, errTeamsSnapshotChanged
	}
	named, exists, err := root.stat(captureManifestName)
	if err != nil || !exists || !named.isRegular() || !initial.sameFile(named) {
		return nil, errTeamsSnapshotChanged
	}
	pinnedRoot, err := root.self()
	if err != nil {
		return nil, errTeamsSnapshotChanged
	}
	namedInfo, err := os.Lstat(dir)
	if err != nil || !namedInfo.IsDir() || namedInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errTeamsSnapshotChanged
	}
	namedDevice, err := placementDevice(namedInfo)
	if err != nil {
		return nil, errTeamsSnapshotChanged
	}
	namedInode, err := libraryDirectoryInode(namedInfo)
	if err != nil || namedInode == 0 || pinnedRoot.Dev != namedDevice || pinnedRoot.Ino != namedInode {
		return nil, errTeamsSnapshotChanged
	}
	finalInfo, err := f.Stat()
	if err != nil {
		return nil, errTeamsSnapshotChanged
	}
	finalStamp, err := localidentity.Acquisition(finalInfo)
	if err != nil || finalStamp != beforeStamp {
		return nil, errTeamsSnapshotChanged
	}
	var manifest captureManifest
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.Version > captureManifestUnderstood {
		return nil, errTeamsSourceUnavailable
	}
	if _, unsafe := manifest.unsafePath(); unsafe {
		return nil, errTeamsSourceUnavailable
	}
	return &manifest, nil
}

// Caller holds the host capture lock before entering any SQLite transaction.
// The before/after walks use metadata only; neither opens a transcript.
func teamsCaptureRoster(ctx context.Context, cfg Config) (map[string]teamsCapturedRosterEntry, error) {
	if len(cfg.Sources) != 1 || !cfg.Sources[0].Enabled || !isCodexSource(cfg.Sources[0].Kind) {
		return nil, errTeamsSourceUnavailable
	}
	source := cfg.Sources[0]
	before, err := teamsSourceWalk(ctx, source)
	if err != nil {
		return nil, err
	}
	if !validCaptureName(source.Name) {
		return nil, errTeamsSourceUnavailable
	}
	manifest, err := teamsLoadBoundedCaptureManifest(filepath.Join(cfg.CaptureRoot, currentHost().ID, source.Name))
	if err != nil {
		return nil, err
	}
	options, err := teamsSourceOptionsFingerprint(source.Options)
	if err != nil {
		return nil, err
	}
	if manifest.Host.ID != currentHost().ID || manifest.Source.Name != source.Name || manifest.Source.Kind != source.Kind || manifest.Source.Path != source.Path || defaultString(manifest.Source.Account, "local") != source.Account || manifest.Source.TeamsProvenanceAmbiguous || manifest.Source.TeamsOptionsSHA256 != options || manifest.LastRun == nil || manifest.LastRun.FinishedAt == "" || len(manifest.LastRun.Errors) != 0 || len(manifest.Snapshots) != 0 || len(manifest.Installations) != 0 || len(manifest.Files) != len(before) {
		return nil, errTeamsSourceUnavailable
	}
	roster := make(map[string]teamsCapturedRosterEntry, len(before))
	for _, entry := range manifest.Files {
		if !teamsAcquisitionEligible(entry) || entry.MissingSince != "" {
			return nil, errTeamsSourceUnavailable
		}
		stamp, ok := before[entry.Path]
		if !ok {
			return nil, errTeamsSourceUnavailable
		}
		rel, e := filepath.Rel(source.Path, entry.Path)
		if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) || entry.Captured != path.Join(captureFilesDir, filepath.ToSlash(rel)) {
			return nil, errTeamsSourceUnavailable
		}
		a := entry.TeamsAcquisition
		r := entry.TeamsAcquisitionRoot
		if a.Device != stamp.device || a.Inode != stamp.inode || a.ChangeSeconds != stamp.changeSeconds || a.ChangeNanos != stamp.changeNanos || a.Size != stamp.size || a.MTimeNS != stamp.mtimeNS || r.Device != stamp.rootDevice || r.Inode != stamp.rootInode {
			return nil, errTeamsSnapshotChanged
		}
		if _, duplicate := roster[entry.Path]; duplicate {
			return nil, errTeamsSourceUnavailable
		}
		roster[entry.Path] = teamsCapturedRosterEntry{captureSHA: entry.SHA256, capturePath: entry.Captured}
	}
	after, err := teamsSourceWalk(ctx, source)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, after) {
		return nil, errTeamsSnapshotChanged
	}
	return roster, nil
}
