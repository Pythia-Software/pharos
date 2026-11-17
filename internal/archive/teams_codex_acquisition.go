package archive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gbdubs/pharos/internal/platform/localidentity"
)

// teamsAcquisitionRoot is the no-follow source root observed on this host.
// Like the file stamp, it does not establish freshness on unverified media.
type teamsAcquisitionRoot struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func teamsAcquisitionEligible(f *capturedFile) bool {
	return f != nil && f.TeamsAcquisition != nil && f.TeamsAcquisition.Inode != 0 && f.TeamsAcquisition.Size == f.Size && f.TeamsAcquisition.ChangeNanos >= 0 && f.TeamsAcquisition.ChangeNanos < 1e9 && f.TeamsAcquisitionRoot != nil && f.TeamsAcquisitionRoot.Inode != 0 && f.Size > 0 && f.Size <= 4<<20 && strings.HasSuffix(strings.ToLower(f.Path), ".jsonl") && validRecordHash(f.SHA256)
}

// teamsOpenCodexAcquisition opens only a regular file below the pinned source
// root using component-by-component no-follow opens. Failure is not evidence.
func teamsOpenCodexAcquisition(root, path string) (*os.File, exportStat, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) || filepath.Clean(root) != root || filepath.Clean(path) != path {
		return nil, exportStat{}, errTeamsSourceUnavailable
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, exportStat{}, errTeamsSourceUnavailable
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, exportStat{}, errTeamsSourceUnavailable
	}
	dir, err := exportOpenAcquisitionRoot(root)
	if err != nil {
		return nil, exportStat{}, err
	}
	defer dir.Close()
	rootStat, err := dir.self()
	if err != nil || !rootStat.isDir() {
		return nil, exportStat{}, errTeamsSourceUnavailable
	}
	dev, err := placementDevice(info)
	if err != nil {
		return nil, exportStat{}, errTeamsSourceUnavailable
	}
	ino, err := libraryDirectoryInode(info)
	if err != nil || ino == 0 || rootStat.Dev != dev || rootStat.Ino != ino {
		return nil, exportStat{}, errTeamsSourceUnavailable
	}
	f, _, err := dir.openReadFile(filepath.ToSlash(rel))
	if err != nil {
		return nil, exportStat{}, err
	}
	return f, rootStat, nil
}

func teamsAcquisitionStamp(root, path string) (localidentity.AcquisitionStamp, exportStat, error) {
	f, rootStat, err := teamsOpenCodexAcquisition(root, path)
	if err != nil {
		return localidentity.AcquisitionStamp{}, exportStat{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return localidentity.AcquisitionStamp{}, exportStat{}, err
	}
	stamp, err := localidentity.Acquisition(info)
	if err != nil {
		return localidentity.AcquisitionStamp{}, exportStat{}, err
	}
	return stamp, rootStat, nil
}

func teamsSameAcquisition(root, path string, source *os.File, before localidentity.AcquisitionStamp, rootBefore exportStat) error {
	info, err := source.Stat()
	if err != nil {
		return err
	}
	after, err := localidentity.Acquisition(info)
	if err != nil || before != after {
		return errCaptureSourceChanged
	}
	named, rootAfter, err := teamsAcquisitionStamp(root, path)
	if err != nil || named != after || rootBefore.Dev != rootAfter.Dev || rootBefore.Ino != rootAfter.Ino {
		return errCaptureSourceChanged
	}
	return nil
}

// A caller in the proof-enabled workflow can reacquire exactly one previously
// indexed origin per turn. Ordinary capture never consults the catalog or
// recopies a legacy file merely to add this optional evidence.
type teamsProofRecaptureKey struct{}
type teamsProofForceKey struct{}

func teamsProofForce(ctx context.Context) bool {
	value, _ := ctx.Value(teamsProofForceKey{}).(bool)
	return value
}

func teamsBoundedSourceDigest(ctx context.Context, root, name string) (string, error) {
	f, rootStamp, err := teamsOpenCodexAcquisition(root, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > 4<<20 {
		return "", errTeamsSourceUnavailable
	}
	stamp, err := localidentity.Acquisition(info)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, info.Size()+1))
	if err != nil || n != info.Size() || ctx.Err() != nil || teamsSameAcquisition(root, name, f, stamp, rootStamp) != nil {
		return "", errTeamsSourceUnavailable
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func teamsBoundedRetainedDigest(ctx context.Context, root, name string, version capturedVersion) (string, error) {
	if version.Size <= 0 || version.Size > 4<<20 {
		return "", errTeamsSourceUnavailable
	}
	if version.Encoding != "" && version.Encoding != captureEncodingZstd {
		return "", errTeamsSourceUnavailable
	}
	f, _, err := teamsOpenCodexAcquisition(root, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	version = storedAsFile(f, version)
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > 4<<20 {
		return "", errTeamsSourceUnavailable
	}
	var reader io.Reader = io.LimitReader(f, info.Size()+1)
	if version.Encoding == captureEncodingZstd {
		decoder, e := newCaptureDecoder(bufio.NewReaderSize(reader, 64<<10))
		if e != nil {
			return "", e
		}
		defer decoder.Close()
		reader = decoder
	} else if version.Encoding != "" {
		return "", errTeamsSourceUnavailable
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(reader, version.Size+1))
	if err != nil || n != version.Size || ctx.Err() != nil {
		return "", errTeamsSourceUnavailable
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func teamsOpenBoundedCapture(view *captureView, rel string) (io.ReadCloser, error) {
	file, _, err := teamsOpenCodexAcquisition(view.dir, filepath.Join(view.dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > 4<<20 {
		file.Close()
		return nil, errTeamsSourceUnavailable
	}
	entry, ok := view.manifest.entry(rel)
	if !ok {
		file.Close()
		return nil, errTeamsSourceUnavailable
	}
	if entry.Encoding != "" && entry.Encoding != captureEncodingZstd {
		file.Close()
		return nil, errTeamsSourceUnavailable
	}
	if view.decoded[rel] {
		return file, nil
	}
	entry = storedAsUnlocked(file, entry)
	if entry.Encoding == "" {
		return file, nil
	}
	if entry.Encoding != captureEncodingZstd {
		file.Close()
		return nil, errTeamsSourceUnavailable
	}
	decoder, err := newCaptureDecoder(bufio.NewReaderSize(io.LimitReader(file, info.Size()+1), 64<<10))
	if err != nil {
		file.Close()
		return nil, err
	}
	return &sourceFile{Reader: decoder, file: file, decoder: decoder, info: originalInfo{FileInfo: info, size: entry.Size}}, nil
}

func teamsProofRecapture(ctx context.Context, path string) bool {
	value, _ := ctx.Value(teamsProofRecaptureKey{}).(string)
	return value != "" && value == path
}

func (c *Catalog) captureTeamsTranscript(ctx context.Context, config Config, source SourceConfig, origin string) (CaptureSourceResult, error) {
	if !source.Enabled || !isCodexSource(source.Kind) || !filepath.IsAbs(source.Path) || !filepath.IsAbs(origin) || filepath.Clean(source.Path) != source.Path || filepath.Clean(origin) != origin {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	rel, err := filepath.Rel(source.Path, origin)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !strings.HasSuffix(strings.ToLower(origin), ".jsonl") {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	options, err := teamsSourceOptionsFingerprint(source.Options)
	if err != nil {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	matches := 0
	for _, configured := range config.Sources {
		if configured.Name != source.Name {
			continue
		}
		fingerprint, e := teamsSourceOptionsFingerprint(configured.Options)
		if e != nil || !configured.Enabled || configured.Kind != source.Kind || configured.Path != source.Path || configured.Account != source.Account || fingerprint != options {
			return CaptureSourceResult{}, errTeamsSourceUnavailable
		}
		matches++
	}
	if matches != 1 {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	// Take the capture lock first. No SQLite writer transaction is held while
	// reading or copying source bytes.
	session, err := beginCapture(config)
	if err != nil {
		return CaptureSourceResult{}, err
	}
	defer session.Close()
	provenance, err := teamsSourceBindingFingerprint(session.host.ID, source.Name, source.Kind, source.Account, source.Path, options)
	if err != nil {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	var eligible bool
	err = c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_source_revision r JOIN teams_source_bindings b ON b.superseded_at='' JOIN teams_native_source_proofs p ON p.binding_id=b.binding_id WHERE r.id=1 AND r.proof_requested=1 AND b.host_id=? AND b.source_name=? AND lower(b.source_kind)='codex' AND b.source_root=? AND b.account_label=? AND b.provenance_sha256=? AND p.origin=?)`, session.host.ID, source.Name, source.Path, source.Account, provenance, origin).Scan(&eligible)
	if err != nil {
		return CaptureSourceResult{}, err
	}
	if !eligible {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	f, _, err := teamsOpenCodexAcquisition(source.Path, origin)
	if err != nil {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	info, err := f.Stat()
	f.Close()
	if err != nil || info.Size() <= 0 || info.Size() > 4<<20 {
		return CaptureSourceResult{}, errTeamsSourceUnavailable
	}
	plan := capturePlan{files: []captureItem{{path: origin, rel: filepath.ToSlash(rel)}}, retainAbsent: true, pathMap: []capturePathMapping{{Original: source.Path, Captured: captureFilesDir}}}
	ctx = context.WithValue(ctx, teamsProofRecaptureKey{}, origin)
	var result CaptureSourceResult
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			ctx = context.WithValue(ctx, teamsProofForceKey{}, true)
		}
		result = session.sourcePlanned(ctx, source, nil, &plan)
		if result.State != "complete" {
			return result, errTeamsSourceUnavailable
		}
		manifest, e := loadCaptureManifest(result.Dir)
		if e != nil {
			return result, errTeamsSourceUnavailable
		}
		for _, entry := range manifest.Files {
			if entry == nil || entry.Path != origin || entry.Captured != path.Join(captureFilesDir, filepath.ToSlash(rel)) || entry.TeamsAcquisition == nil || entry.TeamsAcquisitionRoot == nil || !validRecordHash(entry.SHA256) {
				continue
			}
			stamp, root, e := teamsAcquisitionStamp(source.Path, origin)
			if e != nil || stamp != *entry.TeamsAcquisition || root.Dev != entry.TeamsAcquisitionRoot.Device || root.Ino != entry.TeamsAcquisitionRoot.Inode {
				continue
			}
			hash, e := teamsBoundedRetainedDigest(ctx, result.Dir, filepath.Join(result.Dir, filepath.FromSlash(entry.Captured)), entry.version())
			if e == nil && hash == entry.SHA256 {
				return result, nil
			}
		}
	}
	return result, errTeamsSourceUnavailable
}
