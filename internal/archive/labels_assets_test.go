package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tarEntry is one entry of a test archive: a file, a directory (name ends
// in /) or a symbolic link (link set).
type tarEntry struct {
	name, body, link string
}

func testArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(zipped)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}
		switch {
		case entry.link != "":
			header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, entry.link, 0
		case strings.HasSuffix(entry.name, "/"):
			header.Typeflag, header.Mode, header.Size = tar.TypeDir, 0o755, 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// assetServer serves archive as a release asset. It honours range
// requests unless noRanges, and cuts the first full response after cutAfter
// bytes (0: never), as a dropped connection would, or, with stall, keeps
// the connection open sending nothing more until the client gives up.
type assetServer struct {
	*httptest.Server
	archive  []byte
	noRanges bool
	cutAfter int
	stall    bool
	mu       sync.Mutex
	ranges   []string
	cut      bool
}

func newAssetServer(t *testing.T, archive []byte) *assetServer {
	t.Helper()
	server := &assetServer{archive: archive}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/fixture-1.tar.gz") {
			http.NotFound(w, r)
			return
		}
		server.mu.Lock()
		server.ranges = append(server.ranges, r.Header.Get("Range"))
		cut := server.cutAfter > 0 && !server.cut
		server.cut = server.cut || cut
		server.mu.Unlock()
		offset := 0
		if value := r.Header.Get("Range"); value != "" && !server.noRanges {
			fmt.Sscanf(value, "bytes=%d-", &offset)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(archive)-1, len(archive)))
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)-offset))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		}
		body := archive[offset:]
		if cut && server.stall {
			// Promise everything, send part, then go quiet.
			w.Write(body[:server.cutAfter])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if cut {
			// Promise everything, send part, and drop the connection.
			w.Write(body[:server.cutAfter])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *assetServer) seenRanges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

func (s *assetServer) asset(archive []byte, path string) labelsAsset {
	sum := sha256.Sum256(archive)
	return labelsAsset{Name: "fixture", Version: "1", URL: s.URL + "/download/{name}-{version}.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive)), Path: path}
}

// useSupportDir points this Mac's support directory at a new one.
func useSupportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PHAROS_SUPPORT_DIR", dir)
	return dir
}

// The download lands in this Mac's support directory, verified and
// unpacked whole; a second fetch does nothing.
func TestLabelsAssetDownloadsVerifiesAndUnpacks(t *testing.T) {
	support := useSupportDir(t)
	archive := testArchive(t, tarEntry{name: "manifest.json", body: "{}"}, tarEntry{name: "lib/"}, tarEntry{name: "lib/libx.1.dylib", body: "library"},
		tarEntry{name: "lib/libx.dylib", link: "libx.1.dylib"})
	server := newAssetServer(t, archive)
	asset := server.asset(archive, "lib/libx.dylib")
	if got := labelsAssetStatus(asset); got != "not yet downloaded" {
		t.Fatalf("before: %q", got)
	}
	var reported int64
	if err := fetchLabelsAsset(t.Context(), asset, func(done, total int64) { reported = done }); err != nil {
		t.Fatal(err)
	}
	if !asset.present() || reported != asset.Size || !strings.HasPrefix(asset.dir(), filepath.Join(support, "labeler")) {
		t.Fatalf("present %v, reported %d of %d, in %s", asset.present(), reported, asset.Size, asset.dir())
	}
	if data, err := os.ReadFile(asset.target()); err != nil || string(data) != "library" {
		t.Fatalf("the unpacked link: %q %v", data, err)
	}
	if fileExists(asset.partial()) || !strings.HasPrefix(labelsAssetStatus(asset), "downloaded to ") {
		t.Fatalf("partial kept or status %q", labelsAssetStatus(asset))
	}
	if err := fetchLabelsAsset(t.Context(), asset, nil); err != nil || len(server.seenRanges()) != 1 {
		t.Fatalf("a present asset was fetched again: %v (%d requests)", err, len(server.seenRanges()))
	}
}

// A dropped connection keeps what arrived; the next attempt asks for the
// rest. A server without ranges starts again from the beginning.
func TestLabelsAssetDownloadResumes(t *testing.T) {
	for _, ranges := range []bool{true, false} {
		useSupportDir(t)
		archive := testArchive(t, tarEntry{name: "manifest.json", body: strings.Repeat("weights ", 4096)})
		server := newAssetServer(t, archive)
		server.cutAfter, server.noRanges = len(archive)/2, !ranges
		asset := server.asset(archive, "")
		if err := fetchLabelsAsset(t.Context(), asset, nil); err == nil {
			t.Fatal("a cut download succeeded")
		}
		if got := asset.downloaded(); got != int64(len(archive)/2) || !strings.HasPrefix(labelsAssetStatus(asset), "partly downloaded") {
			t.Fatalf("kept %d of %d bytes; status %q", got, len(archive), labelsAssetStatus(asset))
		}
		if err := fetchLabelsAsset(t.Context(), asset, nil); err != nil || !asset.present() {
			t.Fatalf("resumed (ranges %v): %v", ranges, err)
		}
		if seen := server.seenRanges(); seen[1] != fmt.Sprintf("bytes=%d-", len(archive)/2) {
			t.Fatalf("the second request asked %q", seen[1])
		}
	}
	// A server that goes quiet mid-transfer is given up on, keeping what
	// arrived, rather than holding the download (and its lock) forever.
	override(t, &labelsDownloadStall, 200*time.Millisecond)
	useSupportDir(t)
	archive := testArchive(t, tarEntry{name: "manifest.json", body: strings.Repeat("weights ", 4096)})
	server := newAssetServer(t, archive)
	server.cutAfter, server.stall = len(archive)/2, true
	asset := server.asset(archive, "")
	started := time.Now()
	if err := fetchLabelsAsset(t.Context(), asset, nil); err == nil || !strings.Contains(err.Error(), "no data for") {
		t.Fatalf("a stalled download: %v", err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("a stalled download took %s to give up", took)
	}
	if got := asset.downloaded(); got != int64(len(archive)/2) {
		t.Fatalf("a stalled download kept %d of %d bytes", got, len(archive))
	}
	if err := fetchLabelsAsset(t.Context(), asset, nil); err != nil || !asset.present() {
		t.Fatalf("resumed after a stall: %v", err)
	}
}

// A download that does not match the pin, or an archive that reaches
// outside its directory, leaves nothing behind to use.
func TestLabelsAssetRefusesWhatTheBuildDidNotPin(t *testing.T) {
	useSupportDir(t)
	archive := testArchive(t, tarEntry{name: "manifest.json", body: "{}"})
	server := newAssetServer(t, archive)
	asset := server.asset(archive, "")
	asset.SHA256 = strings.Repeat("0", 64)
	if err := fetchLabelsAsset(t.Context(), asset, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") || asset.present() || fileExists(asset.partial()) {
		t.Fatalf("a mismatched download: %v (present %v)", err, asset.present())
	}
	asset = server.asset(archive, "")
	asset.Size--
	if err := fetchLabelsAsset(t.Context(), asset, nil); err == nil || asset.present() {
		t.Fatalf("a download longer than its pin: %v", err)
	}
	for _, entries := range [][]tarEntry{
		{{name: "../escape", body: "x"}},
		{{name: "link", link: "/etc/passwd"}},
		{{name: "link", link: "../../outside"}},
	} {
		useSupportDir(t)
		hostile := testArchive(t, entries...)
		server := newAssetServer(t, hostile)
		asset := server.asset(hostile, "")
		if err := fetchLabelsAsset(t.Context(), asset, nil); err == nil || asset.present() {
			t.Fatalf("%v unpacked: %v", entries, err)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(asset.dir()), ".*unpack-*")); len(leftovers) > 0 {
			t.Fatalf("staging left behind: %v", leftovers)
		}
	}
	if err := fetchLabelsAsset(t.Context(), labelsAsset{Name: "unpinned", SHA256: "PENDING"}, nil); err == nil || !strings.Contains(err.Error(), "pins no") {
		t.Fatalf("an unpinned asset: %v", err)
	}
}

// One download at a time on a Mac: while one process holds the lock,
// another fails fast and status says it is downloading.
func TestLabelsFetchIsOnePerMac(t *testing.T) {
	useSupportDir(t)
	archive := testArchive(t, tarEntry{name: "manifest.json", body: "{}"})
	server := newAssetServer(t, archive)
	asset := server.asset(archive, "")
	if err := os.MkdirAll(filepath.Dir(labelsFetchLock()), 0o700); err != nil {
		t.Fatal(err)
	}
	lease, err := fileLease(labelsFetchLock(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := labelsAssetStatus(asset); !strings.HasPrefix(got, "downloading: 0 of") {
		t.Fatalf("status while another process downloads: %q", got)
	}
	if err := fetchLabelsAssets(t.Context(), []labelsAsset{asset}, nil, nil); err != errLabelsFetching {
		t.Fatalf("a second download: %v", err)
	}
	lease.Close()
	if err := fetchLabelsAssets(t.Context(), []labelsAsset{asset}, nil, nil); err != nil || !asset.present() {
		t.Fatalf("after the first ended: %v", err)
	}
}

// Labeling waits for the model: the stage stays pending and status says
// why, and once the download is present the same settings label.
func TestLabelsWaitForTheModelDownload(t *testing.T) {
	useSupportDir(t)
	catalog, config := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	configured := labelsTestSettings(t, "ok")
	// The model's files, served as its release asset.
	entries := []tarEntry{}
	for _, name := range []string{"manifest.json", "tokenizer.json", "model.int8.onnx"} {
		data, err := os.ReadFile(filepath.Join(configured.ModelDir, name))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, tarEntry{name: name, body: string(data)})
	}
	archive := testArchive(t, entries...)
	server := newAssetServer(t, archive)
	override(t, &labelsAssets, labelsAssetSet{Model: server.asset(archive, "")})
	config.Labels = configured
	config.Labels.ModelDir = ""
	srv := testServer(t, config, catalog)
	if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
		t.Fatal(err)
	}
	if err := srv.finishDeferredStages(t.Context(), findingsAuto, map[string]bool{"labels": true}, nil); err != nil {
		t.Fatal(err)
	}
	deferred, _ := catalog.freshnessDeferred(t.Context())
	if row := deferred["labels"]; catalog.hasLabelsLedger() || row.generation <= row.completed {
		t.Fatalf("labeled without the model (ledger %v, %+v)", catalog.hasLabelsLedger(), row)
	}
	waiting := config.Labels.labelsWaiting()
	if !strings.Contains(waiting, "the labeling model: not yet downloaded") {
		t.Fatalf("waiting: %q", waiting)
	}
	if sub := itemSub(backgroundItem{ID: "labels"}, srv.backgroundInputsFor(t.Context(), nil)); !strings.Contains(sub, "not yet downloaded") {
		t.Fatalf("the labels item says %q", sub)
	}
	status, err := catalog.LabelsStatus(t.Context(), config.Labels.withAssets())
	if err != nil || status["waiting"] != waiting {
		t.Fatalf("status: %v %v", status, err)
	}
	var out bytes.Buffer
	if err := fetchLabelsCLI(t.Context(), config.Labels, &out); err != nil || !strings.Contains(out.String(), "downloaded to") {
		t.Fatalf("fetch: %v %s", err, out.String())
	}
	if err := srv.finishDeferredStages(t.Context(), findingsAuto, map[string]bool{"authorship": true, "labels": true}, nil); err != nil {
		t.Fatal(err)
	}
	if n := labelCount(t, catalog, ""); n < 20 {
		t.Fatalf("after the download the stage labeled %d messages", n)
	}
}

// The broker downloads once for every library, as broker operation
// labels.fetch, then asks the open libraries to label now.
func TestBrokerFetchesLabelsAssetsThenAsksLibrariesToLabel(t *testing.T) {
	useSupportDir(t)
	archive := testArchive(t, tarEntry{name: "manifest.json", body: "{}"})
	server := newAssetServer(t, archive)
	override(t, &labelsAssets, labelsAssetSet{Model: server.asset(archive, "")})
	override(t, &labelsRuntimePinned, false)
	var asked atomic.Int32
	library := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/background/run" && r.Header.Get("Authorization") == "Bearer token" {
			asked.Add(1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer library.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := &applicationManager{ctx: ctx, client: &http.Client{Timeout: 5 * time.Second}, operations: newBrokerOperations(filepath.Join(t.TempDir(), "operations.json")),
		children: map[string]*applicationChild{"lib": {endpoint: applicationEndpoint{URL: library.URL, Token: "token"}}}}
	m.startLabelsFetch(LabelerSettings{Enabled: false})
	m.startLabelsFetch(LabelerSettings{Enabled: true, ModelDir: t.TempDir()})
	if len(m.operations.reg.views()) != 0 {
		t.Fatal("a download started for a library without labels, or with its own model")
	}
	m.startLabelsFetch(LabelerSettings{Enabled: true})
	if !waitFor(10*time.Second, func() {
		for asked.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}) {
		t.Fatal("the library was never asked to label")
	}
	views := m.operations.reg.views()
	if len(views) != 1 || views[0].Spec.Kind != "labels.fetch" || !labelsAssets.Model.present() {
		t.Fatalf("broker operations: %+v (present %v)", views, labelsAssets.Model.present())
	}
}
