//go:build probe

package archive

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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
			if leases, _ := filepath.Glob(filepath.Join(target.Dir, ".reader-*", "conductor.db")); len(leases) == 1 {
				if tmp, err := os.Stat(leases[0]); err == nil && tmp.Size() > peak {
					peak = tmp.Size()
				}
			}
		}
	}()
	started := time.Now()
	stored, err := compressEntry(t.Context(), target, manifest, compressCandidate{kind: "generation", database: "/probe/conductor.db", v: manifest.Snapshots[0].Generations[0]}, compressPolicy{}, loadCompressionOutcomes(target.Dir))
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

// TestCaptureCompressionSampleProbe measures what phase 2 would save on a
// plain file copy of real captures: for each source directory under
// PHAROS_COMPRESS_SAMPLE_DIR (named for its kind, holding that capture's
// files/), the bytes compressCaptures's allowlist admits, and their size at
// zstd level 3, in total and for files unchanged for 14 days. It encodes into
// a counter and writes nothing; it opens nothing with SQLite.
//
//	cp -Rc "<backup>/captures/<host>/claude" /Volumes/euclid/.pharos-compress-sample/claude
//	PHAROS_COMPRESS_SAMPLE_DIR=/Volumes/euclid/.pharos-compress-sample \
//	  go test -tags probe ./internal/archive -run TestCaptureCompressionSampleProbe -v -timeout 60m
func TestCaptureCompressionSampleProbe(t *testing.T) {
	root := os.Getenv("PHAROS_COMPRESS_SAMPLE_DIR")
	if root == "" {
		t.Skip("set PHAROS_COMPRESS_SAMPLE_DIR to copies of captured sources")
	}
	kinds, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Now().AddDate(0, 0, -14)
	for _, kind := range kinds {
		if !kind.IsDir() {
			continue
		}
		dir := filepath.Join(root, kind.Name())
		var all, allowed, allowedStored, coldBytes, coldStored int64
		var files, coldFiles int
		started := time.Now()
		err := filepath.WalkDir(filepath.Join(dir, captureFilesDir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			all += info.Size()
			rel, _ := filepath.Rel(dir, path)
			if !compressibleLatest(kind.Name(), filepath.ToSlash(rel)) || info.Size() < compressMinBytes {
				return nil
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			var counter countingWriter
			encoder, err := zstd.NewWriter(&counter, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(captureCompressLevel)), zstd.WithEncoderConcurrency(captureCompressThreads))
			if err != nil {
				return err
			}
			if _, err = io.Copy(encoder, in); err != nil {
				return err
			}
			if err = encoder.Close(); err != nil {
				return err
			}
			files++
			allowed += info.Size()
			allowedStored += counter.n
			if info.ModTime().Before(cold) {
				coldFiles++
				coldBytes += info.Size()
				coldStored += counter.n
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %.2f GB captured; allowlisted %d files %.2f GB -> %.2f GB (%.1fx); unchanged 14+ days %d files %.2f GB -> %.2f GB, saving %.2f GB; %s",
			kind.Name(), gb(all), files, gb(allowed), gb(allowedStored), float64(allowed)/float64(max(allowedStored, 1)), coldFiles, gb(coldBytes), gb(coldStored), gb(coldBytes-coldStored), time.Since(started).Round(time.Second))
	}
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(b []byte) (int, error) { c.n += int64(len(b)); return len(b), nil }

func gb(n int64) float64 { return float64(n) / 1e9 }
