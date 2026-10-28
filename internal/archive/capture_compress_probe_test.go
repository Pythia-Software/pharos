//go:build probe

package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCaptureCompressionProbe times compressing one real snapshot generation
// and decoding it again for a reader, as compressGeneration and
// materializeCaptured do on the library drive. It is a probe, built only with
// the probe tag, and runs only when PHAROS_COMPRESS_PROBE_FILE names a plain
// file copy of a generation (never the live library's file; this opens
// nothing with SQLite). The probe hard-links the copy into a scratch capture
// beside it and removes that capture afterwards; delete the copy yourself:
//
//	cp "<backup>/captures/<host>/conductor/history/<stamp>/conductor.db" /Volumes/euclid/.pharos-compress-probe/conductor.db
//	PHAROS_COMPRESS_PROBE_FILE=/Volumes/euclid/.pharos-compress-probe/conductor.db \
//	  go test -tags probe ./internal/archive -run TestCaptureCompressionProbe -v -timeout 60m
func TestCaptureCompressionProbe(t *testing.T) {
	copied := os.Getenv("PHAROS_COMPRESS_PROBE_FILE")
	if copied == "" {
		t.Skip("set PHAROS_COMPRESS_PROBE_FILE to a copy of a snapshot generation")
	}
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatal(err)
	}
	useHost(t, "probe")
	dir := filepath.Join(filepath.Dir(copied), "probe-capture")
	t.Cleanup(func() { os.RemoveAll(dir) })
	target := captureTarget{Host: currentHost(), Source: "conductor", Dir: filepath.Join(dir, "probe", "conductor")}
	rel := "history/20261023T232146.465000000Z/conductor.db"
	if err = os.MkdirAll(filepath.Dir(filepath.Join(target.Dir, rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(copied, filepath.Join(target.Dir, rel)); err != nil {
		t.Fatal(err)
	}
	manifest := &captureManifest{Version: capturePlainManifestVersion, Host: target.Host, Source: captureSourceInfo{Name: "conductor", Kind: "conductor", Path: "/probe"},
		Snapshots: []*capturedSnapshot{{Path: "/probe/conductor.db", Captured: "files/conductor.db", Generations: []capturedVersion{{Captured: rel, Size: info.Size(), CapturedAt: "2026-10-23T23:21:46.465Z"}}}}}
	if err = writeCaptureManifest(target.Dir, manifest); err != nil {
		t.Fatal(err)
	}
	// Peak temporary disk use is the encoded file, sampled while it grows.
	var peak int64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			if tmp, err := os.Stat(filepath.Join(target.Dir, rel) + captureTempSuffix); err == nil && tmp.Size() > peak {
				peak = tmp.Size()
			}
		}
	}()
	started := time.Now()
	stored, err := compressGeneration(t.Context(), target, "/probe/conductor.db", manifest.Snapshots[0].Generations[0])
	elapsed := time.Since(started)
	close(stop)
	<-sampled
	if err != nil || stored == 0 {
		t.Fatal(stored, err)
	}
	t.Logf("compressed %d bytes to %d (%.1fx) in %s (%.0f MB/s); peak temporary %d bytes", info.Size(), stored, float64(info.Size())/float64(stored), elapsed, float64(info.Size())/elapsed.Seconds()/1e6, max(peak, stored))
	encoded, _ := loadCaptureManifest(target.Dir)
	started = time.Now()
	lease, _, release, err := leaseDecoded(t.Context(), target.Dir, encoded.Snapshots[0].Generations[0])
	if err != nil {
		t.Fatal(err)
	}
	decodedInfo, _ := os.Stat(lease)
	release()
	t.Logf("decoded into a reader lease in %s (%d bytes of temporary disk)", time.Since(started), decodedInfo.Size())
}
