package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// The labeler's downloadable assets (docs/local-labeler.md, "Model
// download"). The model's weights are not in the app: a release attaches
// them to a GitHub release, the build pins the asset below, and each Mac
// downloads it once into its own support directory. The ONNX Runtime
// library is pinned the same way, for an app that does not bundle it.
// Labeling waits until both are present.

// labelsAsset is one pinned download: a .tar.gz unpacked into
// <support>/labeler/<name>-<version>/.
type labelsAsset struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// URL may name {name} and {version}.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Path is the file the labeler uses inside the unpacked directory
	// (empty: the directory itself, a model directory).
	Path string `json:"path,omitempty"`
}

// labelsAssetSet is the build's pinned assets.
type labelsAssetSet struct {
	Model   labelsAsset `json:"model"`
	Runtime labelsAsset `json:"onnxruntime"`
}

//go:embed labels/assets.json
var labelsAssetsJSON []byte

// labelsAssets is the build's descriptor; tests point it at a local server.
// PHAROS_LABELS_MODEL_URL fetches the model from elsewhere (a release's
// asset before it is published); the pinned digest and size still apply.
var labelsAssets = func() labelsAssetSet {
	var set labelsAssetSet
	if err := json.Unmarshal(labelsAssetsJSON, &set); err != nil {
		panic(err)
	}
	if url := os.Getenv("PHAROS_LABELS_MODEL_URL"); url != "" {
		set.Model.URL = url
	}
	return set
}()

// labelsDownloadClient fetches assets; no overall timeout, since a model is
// 150 MB. Headers must arrive within a minute, and a transfer that then
// stops sending fails after labelsDownloadStall without data.
var labelsDownloadClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: time.Minute, IdleConnTimeout: time.Minute}}

// labelsDownloadStall is how long a download may receive nothing before it
// is cancelled, keeping what arrived for the next attempt to resume. Neither
// transport timeout covers a body that stops mid-way, and a stalled
// transfer would hold the one-per-Mac download lock indefinitely.
var labelsDownloadStall = time.Minute

func (a labelsAsset) url() string {
	return strings.NewReplacer("{name}", a.Name, "{version}", a.Version).Replace(a.URL)
}

// dir is where the asset is unpacked: this Mac's support directory, never
// a library drive.
func (a labelsAsset) dir() string {
	return filepath.Join(pharosSupportDir(), "labeler", a.Name+"-"+a.Version)
}

// target is the unpacked file or directory the labeler uses.
func (a labelsAsset) target() string {
	if a.Path == "" {
		return a.dir()
	}
	return filepath.Join(a.dir(), filepath.FromSlash(a.Path))
}

func (a labelsAsset) marker() string { return filepath.Join(a.dir(), ".verified-"+a.SHA256) }

// pinned reports whether the build pins a real asset (a release fills in
// the model's digest and size).
func (a labelsAsset) pinned() bool { return len(a.SHA256) == 64 && a.Size > 0 }

// present reports whether the asset was downloaded, verified and unpacked.
func (a labelsAsset) present() bool {
	if !fileExists(a.marker()) {
		return false
	}
	_, err := os.Stat(a.target())
	return err == nil
}

// partial is the download in progress, kept between attempts.
func (a labelsAsset) partial() string { return a.dir() + ".download" }

// downloaded is how many bytes of the download are on disk.
func (a labelsAsset) downloaded() int64 {
	if info, err := os.Stat(a.partial()); err == nil {
		return info.Size()
	}
	return 0
}

// fetchLabelsAsset downloads, verifies and unpacks an asset, resuming a
// partial download with a range request. Unpacking happens in a new
// directory renamed into place, so a reader sees all of it or none.
func fetchLabelsAsset(ctx context.Context, asset labelsAsset, progress func(done, total int64)) error {
	if asset.present() {
		return nil
	}
	if !asset.pinned() {
		return fmt.Errorf("this build pins no %s download (its release has not attached the asset)", asset.Name)
	}
	if err := os.MkdirAll(filepath.Dir(asset.dir()), 0o700); err != nil {
		return err
	}
	if err := downloadLabelsAsset(ctx, asset, progress); err != nil {
		return err
	}
	if err := verifyLabelsDownload(asset); err != nil {
		os.Remove(asset.partial())
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(asset.dir()), "."+filepath.Base(asset.dir())+".unpack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := untarGzip(asset.partial(), staging); err != nil {
		return fmt.Errorf("unpack %s: %w", asset.Name, err)
	}
	if asset.Path != "" {
		if _, err := os.Stat(filepath.Join(staging, filepath.FromSlash(asset.Path))); err != nil {
			return fmt.Errorf("%s holds no %s", asset.Name, asset.Path)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, ".verified-"+asset.SHA256), []byte(asset.url()+"\n"), 0o600); err != nil {
		return err
	}
	// An earlier, incomplete unpack is replaced.
	os.RemoveAll(asset.dir())
	if err := os.Rename(staging, asset.dir()); err != nil {
		return err
	}
	return os.Remove(asset.partial())
}

func downloadLabelsAsset(ctx context.Context, asset labelsAsset, progress func(done, total int64)) error {
	offset := asset.downloaded()
	if offset >= asset.Size {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	watchdog := time.AfterFunc(labelsDownloadStall, func() {
		stalled.Store(true)
		cancel()
	})
	defer watchdog.Stop()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.url(), nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	response, err := labelsDownloadClient.Do(request)
	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("download %s: no response within %s", asset.Name, labelsDownloadStall)
		}
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer response.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case response.StatusCode == http.StatusPartialContent && offset > 0:
		flags |= os.O_APPEND
	case response.StatusCode == http.StatusOK:
		// No range support (or a fresh start): from the beginning.
		flags |= os.O_TRUNC
		offset = 0
	default:
		return fmt.Errorf("download %s: %s from %s", asset.Name, response.Status, asset.url())
	}
	file, err := os.OpenFile(asset.partial(), flags, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := make([]byte, 1<<20)
	done := offset
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if done+int64(n) > asset.Size {
				return fmt.Errorf("download %s: more than the pinned %d bytes", asset.Name, asset.Size)
			}
			if _, werr := file.Write(buffer[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			watchdog.Reset(labelsDownloadStall)
			if progress != nil {
				progress(done, asset.Size)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// What arrived stays for the next attempt to resume.
			if stalled.Load() {
				return fmt.Errorf("download %s: no data for %s after %d of %d bytes", asset.Name, labelsDownloadStall, done, asset.Size)
			}
			return fmt.Errorf("download %s: %w", asset.Name, err)
		}
	}
	return file.Sync()
}

// verifyLabelsDownload checks the download's size and SHA-256 against the
// pin.
func verifyLabelsDownload(asset labelsAsset) error {
	file, err := os.Open(asset.partial())
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); size != asset.Size || got != asset.SHA256 {
		return fmt.Errorf("%s: downloaded %d bytes with SHA-256 %s; the build pins %d bytes, %s", asset.Name, size, got, asset.Size, asset.SHA256)
	}
	return nil
}

// untarGzip unpacks a .tar.gz into dir: regular files, directories and
// symbolic links that stay inside dir; anything else, or a path that leaves
// dir, is refused.
func untarGzip(path, dir string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	unzipped, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	reader := tar.NewReader(unzipped)
	root := filepath.Clean(dir) + string(filepath.Separator)
	inside := func(target string) bool {
		return strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), root)
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(header.Name))
		if !inside(target) {
			return fmt.Errorf("%s leaves the archive", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, reader)
			out.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) || !inside(filepath.Join(filepath.Dir(target), header.Linkname)) {
				return fmt.Errorf("%s links outside the archive", header.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("%s: unsupported entry type %c", header.Name, header.Typeflag)
		}
	}
}

// labelsFetchLock is held while a process downloads (the broker, or pharos
// labels fetch), so one Mac downloads once; status asks whether it is held.
func labelsFetchLock() string { return filepath.Join(pharosSupportDir(), "labeler", ".fetch.lock") }

// labelsFetchActive reports whether some process is downloading. It only
// opens an existing lock file, so a status read creates nothing.
func labelsFetchActive() bool {
	file, err := os.Open(labelsFetchLock())
	if err != nil {
		return false
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false
}

// errLabelsFetching reports a download another process is running.
var errLabelsFetching = errors.New("the labeler download is already running")

// fetchLabelsAssets downloads assets in order under the fetch lock,
// reporting each as a step of op (nil outside an operation).
func fetchLabelsAssets(ctx context.Context, assets []labelsAsset, op *Op, progress func(asset labelsAsset, done, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(labelsFetchLock()), 0o700); err != nil {
		return err
	}
	lease, err := fileLease(labelsFetchLock(), true)
	if err != nil {
		return errLabelsFetching
	}
	defer lease.Close()
	for _, asset := range assets {
		op.Step("Download "+asset.Name, "byte", asset.Size)
		err := fetchLabelsAsset(ctx, asset, func(done, total int64) {
			op.SetDone(done)
			if progress != nil {
				progress(asset, done, total)
			}
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// labelsAssetStatus describes an asset for status: downloaded, downloading
// (with progress), partly downloaded, or not yet downloaded.
func labelsAssetStatus(asset labelsAsset) string {
	switch {
	case asset.present():
		return "downloaded to " + asset.dir()
	case !asset.pinned():
		return "not available: this build pins no download (its release attaches the asset)"
	case labelsFetchActive():
		done := asset.downloaded()
		return fmt.Sprintf("downloading: %d of %d MB (%.0f%%)", done>>20, asset.Size>>20, 100*float64(done)/float64(asset.Size))
	case asset.downloaded() > 0:
		return fmt.Sprintf("partly downloaded (%d of %d MB, resumed on the next attempt)", asset.downloaded()>>20, asset.Size>>20)
	}
	return "not yet downloaded"
}

// labelsRuntimePinned is whether the pinned ONNX Runtime suits this Mac
// (the pin is the macOS arm64 build); tests whose fake worker needs no
// runtime turn it off.
var labelsRuntimePinned = goruntime.GOOS == "darwin" && goruntime.GOARCH == "arm64"

// runtimeBundled reports whether the worker finds its runtime beside
// itself, as in the app bundle.
func runtimeBundled(worker string) bool {
	return worker != "" && fileExists(filepath.Join(filepath.Dir(worker), "libonnxruntime.dylib"))
}

// needsRuntime is whether settings leave the runtime to a download.
func (settings LabelerSettings) needsRuntime() bool {
	return settings.Runtime == "" && !runtimeBundled(settings.Worker) && labelsRuntimePinned
}

// withAssets fills in what settings leave unset from this Mac's downloads:
// configuration and the environment come first, then the app bundle, then
// the download.
func (settings LabelerSettings) withAssets() LabelerSettings {
	if settings.ModelDir == "" && labelsAssets.Model.present() {
		settings.ModelDir = labelsAssets.Model.target()
	}
	if settings.needsRuntime() && labelsAssets.Runtime.present() {
		settings.Runtime = labelsAssets.Runtime.target()
	}
	return settings
}

// missingAssets is what settings still need downloaded, smallest first.
func (settings LabelerSettings) missingAssets() []labelsAsset {
	settings = settings.withAssets()
	missing := []labelsAsset{}
	if settings.needsRuntime() {
		missing = append(missing, labelsAssets.Runtime)
	}
	if settings.ModelDir == "" {
		missing = append(missing, labelsAssets.Model)
	}
	return missing
}

// labelsWaiting is why labeling waits for a download ("" when it does not).
func (settings LabelerSettings) labelsWaiting() string {
	parts := []string{}
	for _, asset := range settings.missingAssets() {
		what := "the labeling model"
		if asset.Name == labelsAssets.Runtime.Name {
			what = "ONNX Runtime"
		}
		parts = append(parts, what+": "+labelsAssetStatus(asset))
	}
	if len(parts) == 0 {
		return ""
	}
	return "waiting for the download (" + strings.Join(parts, "; ") + ")"
}

// labelsFetchRetry is how long the broker waits to retry a failed download.
var labelsFetchRetry = 15 * time.Minute

// startLabelsFetch is the broker's side (operation labels.fetch): when a
// library that labels opens on a Mac without the assets, the broker
// downloads them once for every library, then asks the open libraries'
// services to label now. A failed download is retried later; an interrupted
// one resumes.
func (m *applicationManager) startLabelsFetch(settings LabelerSettings) {
	if m.ctx.Err() != nil {
		return
	}
	missing := settings.missingAssets()
	if !settings.Enabled || len(missing) == 0 || slices.ContainsFunc(missing, func(asset labelsAsset) bool { return !asset.pinned() }) {
		return
	}
	if !m.labelsFetching.CompareAndSwap(false, true) {
		return
	}
	op := m.operations.begin(OperationSpec{Kind: "labels.fetch", Trigger: triggerStartup})
	m.operations.launch(op, func(op *Op) error {
		defer m.labelsFetching.Store(false)
		err := fetchLabelsAssets(m.ctx, missing, op, nil)
		switch {
		case errors.Is(err, errLabelsFetching):
			// Another process (pharos labels fetch) is downloading.
			return nil
		case err != nil && m.ctx.Err() == nil:
			time.AfterFunc(labelsFetchRetry, func() { m.startLabelsFetch(settings) })
			return err
		case err != nil:
			return err
		}
		m.requestLabels()
		return nil
	})
}

// requestLabels asks every open library's service to label now (Run now of
// its labels item); a library without labels ignores it.
func (m *applicationManager) requestLabels() {
	m.mu.Lock()
	endpoints := []applicationEndpoint{}
	for _, child := range m.children {
		endpoints = append(endpoints, child.endpoint)
	}
	m.mu.Unlock()
	for _, endpoint := range endpoints {
		request, err := http.NewRequestWithContext(m.ctx, "POST", endpoint.URL+"/api/background/run", strings.NewReader(`{"item":"labels"}`))
		if err != nil {
			continue
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		request.Header.Set("Content-Type", "application/json")
		if response, err := m.client.Do(request); err == nil {
			response.Body.Close()
		}
	}
}
