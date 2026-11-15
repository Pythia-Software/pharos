package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// exportFixture is a tiny synthetic capture: root/host/source with a
// host.json, an existing capture lock and a manifest built in memory.
type exportFixture struct {
	t                        *testing.T
	root, host, source, dir  string
	out                      string
	manifest                 *captureManifest
	stamp                    int
	keepVersion              bool
	hostRecord               captureHostRecord
	manifestBytes, hostBytes []byte
}

func newExportFixture(t *testing.T, kind string) *exportFixture {
	t.Helper()
	base := t.TempDir()
	f := &exportFixture{t: t, root: filepath.Join(base, "Captures"), host: "host-a", source: "main", out: filepath.Join(base, "Outbox")}
	f.dir = filepath.Join(f.root, f.host, f.source)
	for _, dir := range []string{f.dir, f.out} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, f.host, captureLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.hostRecord = captureHostRecord{ID: f.host, Label: "Test Mac", User: "tester", FirstCaptureAt: "2026-11-01T00:00:00Z", LastCaptureAt: "2026-11-02T00:00:00Z"}
	origin := "/nowhere/" + kind
	f.manifest = &captureManifest{Host: Host{ID: f.host, Label: "Test Mac", User: "tester"},
		Source: captureSourceInfo{Name: f.source, Kind: kind, Path: origin, Account: "work"}, UpdatedAt: "2026-11-02T00:00:00Z",
		Files: []*capturedFile{}, Snapshots: []*capturedSnapshot{}, PathMap: []capturePathMapping{{Original: origin, Captured: captureFilesDir}},
		LastRun: &captureRunRecord{StartedAt: "2026-11-02T00:00:00Z", Errors: []captureError{}, Warnings: []captureError{{Path: "x", Error: "kept warning"}}}}
	return f
}

func exportSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	out := encoder.EncodeAll(data, nil)
	if len(out) >= len(data) {
		t.Fatal("fixture content does not compress")
	}
	return out
}

func exportContent(label string) []byte {
	return []byte(strings.Repeat(`{"type":"user","text":"`+label+`"}`+"\n", 40))
}

func (f *exportFixture) put(rel string, data []byte) {
	f.t.Helper()
	file := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// stored writes data as rel is stored, returning its claim.
func (f *exportFixture) stored(rel string, data []byte, encode bool) capturedVersion {
	f.stamp++
	v := capturedVersion{Captured: rel, Size: int64(len(data)), SHA256: exportSum(data), MTimeNS: int64(f.stamp), CapturedAt: fmt.Sprintf("2026-11-01T00:00:%02dZ", f.stamp), SupersededAt: "2026-11-02T00:00:00Z"}
	if encode {
		encoded := zstdBytes(f.t, data)
		v.Encoding, v.StoredSize = captureEncodingZstd, int64(len(encoded))
		f.put(rel, encoded)
	} else {
		f.put(rel, data)
	}
	return v
}

func (f *exportFixture) file(rel string, data []byte, encode bool) *capturedFile {
	v := f.stored(rel, data, encode)
	file := &capturedFile{Path: "/nowhere/" + rel, Captured: rel, Size: v.Size, MTime: "2026-11-01T00:00:00Z", MTimeNS: v.MTimeNS, SHA256: v.SHA256, CapturedAt: v.CapturedAt, Encoding: v.Encoding, StoredSize: v.StoredSize}
	f.manifest.Files = append(f.manifest.Files, file)
	return file
}

func (f *exportFixture) previous(file *capturedFile, rel string, data []byte, encode bool) {
	file.Previous = append(file.Previous, f.stored(rel, data, encode))
}

func (f *exportFixture) snapshot(rel string, data []byte, encode, digest bool) *capturedSnapshot {
	v := f.stored(rel, data, encode)
	s := &capturedSnapshot{Path: "/nowhere/" + rel, Captured: rel, Size: v.Size, CapturedMTimeNS: v.MTimeNS, CapturedAt: v.CapturedAt, Method: "sqlite-backup",
		Source: sqliteSourceState{Size: v.Size, MTimeNS: 7}, Encoding: v.Encoding, StoredSize: v.StoredSize}
	if digest || encode {
		s.SHA256 = v.SHA256
	}
	f.manifest.Snapshots = append(f.manifest.Snapshots, s)
	return s
}

func (f *exportFixture) generation(s *capturedSnapshot, rel string, data []byte, encode, digest bool) {
	v := f.stored(rel, data, encode)
	if !digest {
		v.SHA256 = ""
	}
	v.Source = &sqliteSourceState{Size: v.Size, MTimeNS: 3}
	s.Generations = append(s.Generations, v)
}

// save writes host.json and manifest.json as the capture writer does,
// stamping the writer's version unless the test fixed one.
func (f *exportFixture) save() {
	f.t.Helper()
	if !f.keepVersion {
		f.manifest.Version = f.manifest.writerVersion()
	}
	data, err := json.MarshalIndent(f.manifest, "", " ")
	if err != nil {
		f.t.Fatal(err)
	}
	f.saveManifest(append(data, '\n'))
	host, err := json.MarshalIndent(f.hostRecord, "", "  ")
	if err != nil {
		f.t.Fatal(err)
	}
	f.saveHost(append(host, '\n'))
}

func (f *exportFixture) saveManifest(data []byte) {
	f.t.Helper()
	f.manifestBytes = data
	if err := os.WriteFile(filepath.Join(f.dir, captureManifestName), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *exportFixture) saveHost(data []byte) {
	f.t.Helper()
	f.hostBytes = data
	if err := os.WriteFile(filepath.Join(f.root, f.host, captureHostName), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// editManifest rewrites the saved manifest through a generic JSON value.
func (f *exportFixture) editManifest(edit func(map[string]any)) {
	f.t.Helper()
	value := map[string]any{}
	if err := json.Unmarshal(f.manifestBytes, &value); err != nil {
		f.t.Fatal(err)
	}
	edit(value)
	data, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	f.saveManifest(data)
}

func (f *exportFixture) export() (CaptureExport, error) {
	return ExportCapture(context.Background(), f.root, f.host, f.source, f.out)
}

func (f *exportFixture) mustExport() CaptureExport {
	f.t.Helper()
	export, err := f.export()
	if err != nil {
		f.t.Fatal(err)
	}
	return export
}

func (f *exportFixture) published(export CaptureExport) string {
	return filepath.Join(f.out, export.ID)
}

// treeState is every entry under root: its type, size, mtime and content
// digest, so a test can see that nothing changed.
func treeState(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(file)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, file)
		value := fmt.Sprintf("%v %d %d", info.Mode().Type(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			value += " " + exportSum(data)
		}
		if info.IsDir() {
			value = fmt.Sprintf("%v", info.Mode().Type())
		}
		state[filepath.ToSlash(rel)] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func listTree(t *testing.T, root string) []string {
	t.Helper()
	names := []string{}
	for rel, state := range treeState(t, root) {
		if rel == "." {
			continue
		}
		if strings.HasPrefix(state, "d") {
			rel += "/"
		}
		names = append(names, rel)
	}
	slices.Sort(names)
	return names
}

func entriesOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// claudeFixture is a Claude Code capture with a rewritten file's earlier
// versions (one encoded), a file missing at its source, unreferenced
// leftovers and the index, compression and reader state an export leaves
// out.
func claudeFixture(t *testing.T) *exportFixture {
	f := newExportFixture(t, "Claude")
	current := f.file("files/projects/p/a.jsonl", exportContent("a3"), false)
	f.previous(current, "history/20261101T000001.000000000Z/projects/p/a.jsonl", exportContent("a1"), false)
	f.previous(current, "history/20261101T000002.000000000Z/projects/p/a.jsonl", exportContent("a2"), true)
	missing := f.file("files/projects/p/b.jsonl", exportContent("b"), false)
	missing.MissingSince = "2026-11-02T00:00:00Z"
	f.file("files/projects/q/c.jsonl", exportContent("c"), true)
	f.put("files/projects/p/stray.jsonl.pharos-tmp", []byte("half"))
	f.put("history/20261101T000001.000000000Z/projects/p/a.jsonl.conflict-1", []byte("aside"))
	f.put(indexMarkerName, []byte(`{"indexed":true}`))
	f.put(indexLockName, nil)
	f.put(compressionOutcomesName, []byte(`{}`))
	f.put(".reader-abc"+captureTempSuffix+"/.reader.lock", nil)
	f.put(".reader-abc"+captureTempSuffix+"/files/projects/q/c.jsonl", exportContent("c"))
	f.put(leaseStagingPrefix+"def"+captureTempSuffix+"/.reader.lock", nil)
	f.save()
	return f
}

func TestCaptureExportClaudeHistoryAndMissingEntries(t *testing.T) {
	f := claudeFixture(t)
	before := treeState(t, f.root)
	export := f.mustExport()
	if after := treeState(t, f.root); !mapsEqual(before, after) {
		t.Fatalf("the capture changed:\nbefore %v\nafter  %v", before, after)
	}
	if export.Format != captureExportFormat || export.Version != 1 || export.Scope != captureExportScope || export.HostID != "host-a" ||
		export.SourceName != "main" || export.SourceKind != "Claude" || export.SourceCompleteness != "unknown" || !validExportHash(export.ID) {
		t.Fatalf("envelope = %+v", export)
	}
	if got := entriesOf(t, f.out); !slices.Equal(got, []string{export.ID}) {
		t.Fatalf("outbox holds %v", got)
	}
	want := []string{
		"captures/", "captures/host-a/", "captures/host-a/host.json", "captures/host-a/main/",
		"captures/host-a/main/files/", "captures/host-a/main/files/projects/", "captures/host-a/main/files/projects/p/",
		"captures/host-a/main/files/projects/p/a.jsonl", "captures/host-a/main/files/projects/p/b.jsonl",
		"captures/host-a/main/files/projects/q/", "captures/host-a/main/files/projects/q/c.jsonl",
		"captures/host-a/main/history/", "captures/host-a/main/history/20261101T000001.000000000Z/",
		"captures/host-a/main/history/20261101T000001.000000000Z/projects/", "captures/host-a/main/history/20261101T000001.000000000Z/projects/p/",
		"captures/host-a/main/history/20261101T000001.000000000Z/projects/p/a.jsonl",
		"captures/host-a/main/history/20261101T000002.000000000Z/", "captures/host-a/main/history/20261101T000002.000000000Z/projects/",
		"captures/host-a/main/history/20261101T000002.000000000Z/projects/p/", "captures/host-a/main/history/20261101T000002.000000000Z/projects/p/a.jsonl",
		"captures/host-a/main/manifest.json", "export.json",
	}
	final := f.published(export)
	if got := listTree(t, final); !slices.Equal(got, want) {
		t.Fatalf("export holds\n%v\nwant\n%v", got, want)
	}
	// Bytes travel exactly as stored: metadata unreserialized, encoded
	// members still encoded; copies are independent files.
	for _, rel := range []string{"captures/host-a/main/manifest.json", "captures/host-a/host.json", "captures/host-a/main/files/projects/q/c.jsonl", "captures/host-a/main/history/20261101T000002.000000000Z/projects/p/a.jsonl"} {
		got, err := os.ReadFile(filepath.Join(final, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(f.root, filepath.FromSlash(strings.TrimPrefix(rel, "captures/")))
		original, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Errorf("%s differs from the capture", rel)
		}
	}
	assertNoHardLinks(t, final)
	envelope, err := os.ReadFile(filepath.Join(final, captureExportEnvelope))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := export.canonical()
	if !bytes.Equal(envelope, append(canonical, '\n')) {
		t.Fatalf("export.json is not the canonical envelope:\n%s", envelope)
	}
	if !strings.Contains(string(f.manifestBytes), "kept warning") {
		t.Fatal("fixture lost its warning")
	}

	verified, err := VerifyCaptureExport(context.Background(), final)
	if err != nil || !sameExportEnvelope(verified, export) {
		t.Fatalf("verify = %+v, %v", verified, err)
	}
	// Integrity comes from contents: a copy under another name verifies.
	moved := filepath.Join(t.TempDir(), "carried")
	if err := os.Rename(final, moved); err != nil {
		t.Fatal(err)
	}
	if verified, err = VerifyCaptureExport(context.Background(), moved); err != nil || verified.ID != export.ID {
		t.Fatalf("renamed export: %+v, %v", verified, err)
	}
	if err := os.Rename(moved, final); err != nil {
		t.Fatal(err)
	}
	// A second export of the unchanged capture reuses the publication.
	again := f.mustExport()
	if !sameExportEnvelope(again, export) || !slices.Equal(entriesOf(t, f.out), []string{export.ID}) {
		t.Fatalf("second export = %+v; outbox %v", again, entriesOf(t, f.out))
	}
	if after := treeState(t, f.root); !mapsEqual(before, after) {
		t.Fatal("the capture changed on the second export")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func assertNoHardLinks(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			info, _ := os.Lstat(file)
			if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
				t.Errorf("%s is hard-linked", file)
			}
		}
		return nil
	})
}

func TestCaptureExportCodexNamingSnapshots(t *testing.T) {
	f := newExportFixture(t, "codex")
	f.file("files/sessions/2026/11/01/rollout-x.jsonl", exportContent("x"), false)
	// Codex's own compressed rollout is a plain entry whose bytes are zstd.
	native := zstdBytes(t, exportContent("y"))
	f.file("files/sessions/2026/11/01/rollout-y.jsonl.zst", native, false)
	state := f.snapshot("files/state_5.sqlite", exportContent("db3"), true, true)
	f.generation(state, "history/20261101T000001.000000000Z/state_5.sqlite", exportContent("db1"), false, false)
	f.generation(state, "history/20261101T000002.000000000Z/state_5.sqlite", exportContent("db2"), true, true)
	gone := f.snapshot("files/state_4.sqlite", exportContent("old"), false, false)
	gone.MissingSince = "2026-11-02T00:00:00Z"
	f.save()
	if f.manifest.Version != captureSnapshotsVersion {
		t.Fatalf("fixture version %d", f.manifest.Version)
	}
	before := treeState(t, f.root)
	export := f.mustExport()
	if after := treeState(t, f.root); !mapsEqual(before, after) {
		t.Fatal("the capture changed")
	}
	paths := []string{}
	for _, file := range export.Files {
		paths = append(paths, file.Path)
	}
	want := []string{"captures/host-a/host.json", "captures/host-a/main/files/sessions/2026/11/01/rollout-x.jsonl",
		"captures/host-a/main/files/sessions/2026/11/01/rollout-y.jsonl.zst", "captures/host-a/main/files/state_4.sqlite",
		"captures/host-a/main/files/state_5.sqlite", "captures/host-a/main/history/20261101T000001.000000000Z/state_5.sqlite",
		"captures/host-a/main/history/20261101T000002.000000000Z/state_5.sqlite", "captures/host-a/main/manifest.json"}
	if !slices.Equal(paths, want) {
		t.Fatalf("files = %v", paths)
	}
	for _, file := range export.Files {
		data, err := os.ReadFile(filepath.Join(f.published(export), filepath.FromSlash(file.Path)))
		if err != nil || exportSum(data) != file.SHA256 || int64(len(data)) != file.Size {
			t.Fatalf("%s does not match its entry", file.Path)
		}
	}
	if _, err := VerifyCaptureExport(context.Background(), f.published(export)); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureExportEmptyCapture(t *testing.T) {
	// No members and no mappings: path_map and snapshots are null.
	f := newExportFixture(t, "claude")
	f.manifest.PathMap, f.manifest.Snapshots, f.manifest.LastRun = nil, nil, nil
	f.save()
	if !bytes.Contains(f.manifestBytes, []byte(`"path_map": null`)) {
		t.Fatalf("fixture: %s", f.manifestBytes)
	}
	export := f.mustExport()
	if len(export.Files) != 2 || export.SourceCompleteness != "unknown" {
		t.Fatalf("envelope = %+v", export)
	}
	want := []string{"captures/", "captures/host-a/", "captures/host-a/host.json", "captures/host-a/main/", "captures/host-a/main/manifest.json", "export.json"}
	if got := listTree(t, f.published(export)); !slices.Equal(got, want) {
		t.Fatalf("export holds %v", got)
	}

	// The files root mapped with no members, and no files directory in the
	// capture: an empty directory, no invented file.
	g := newExportFixture(t, "codex")
	g.save()
	export = g.mustExport()
	want = []string{"captures/", "captures/host-a/", "captures/host-a/host.json", "captures/host-a/main/", "captures/host-a/main/files/", "captures/host-a/main/manifest.json", "export.json"}
	if got := listTree(t, g.published(export)); !slices.Equal(got, want) || len(export.Files) != 2 {
		t.Fatalf("export holds %v; %d files", got, len(export.Files))
	}
	if _, err := VerifyCaptureExport(context.Background(), g.published(export)); err != nil {
		t.Fatal(err)
	}
	// The verifier wants the materialized directory.
	if err := os.Remove(filepath.Join(g.published(export), "captures/host-a/main/files")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCaptureExport(context.Background(), g.published(export)); err == nil || !strings.Contains(err.Error(), "missing the directory") {
		t.Fatalf("verify without the mapped root: %v", err)
	}

	// A mapped root that the capture holds as an undeclared file cannot
	// become a directory.
	h := newExportFixture(t, "claude")
	h.manifest.PathMap = append(h.manifest.PathMap, capturePathMapping{Original: "/old", Captured: "files/old"})
	h.put("files/old", []byte("x"))
	h.save()
	if _, err := h.export(); err == nil || !strings.Contains(err.Error(), "mapped root") {
		t.Fatalf("mapped root over a file: %v", err)
	}

	// Only files may be mapped without a latest member below it. Any other
	// unmatched root is refused, alone or beside valid members, whether or not
	// the capture holds an empty directory there; nothing is invented for it.
	for name, setup := range map[string]func(f *exportFixture){
		"missing alone": func(f *exportFixture) {},
		"missing beside members": func(f *exportFixture) {
			f.file("files/a.jsonl", exportContent("a"), false)
		},
		"undeclared empty dir": func(f *exportFixture) {
			f.manifest.PathMap[1].Captured = "files/empty"
			os.MkdirAll(filepath.Join(f.dir, "files/empty"), 0o700)
		},
		"undeclared empty dir beside members": func(f *exportFixture) {
			f.file("files/a.jsonl", exportContent("a"), false)
			f.manifest.PathMap[1].Captured = "files/empty"
			os.MkdirAll(filepath.Join(f.dir, "files/empty"), 0o700)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newExportFixture(t, "codex")
			f.manifest.PathMap = append(f.manifest.PathMap, capturePathMapping{Original: "/nowhere/state.sqlite", Captured: "files/missing.sqlite"})
			setup(f)
			f.save()
			before := treeState(t, f.root)
			if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "mapped root") {
				t.Fatalf("export: %v", err)
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
			if after := treeState(t, f.root); !mapsEqual(before, after) {
				t.Fatal("the capture changed")
			}
		})
	}
	// Mappings to a latest member and to directories holding one stay valid.
	v := newExportFixture(t, "claude")
	v.file("files/projects/p/a.jsonl", exportContent("a"), false)
	v.manifest.PathMap = append(v.manifest.PathMap,
		capturePathMapping{Original: "/nowhere/projects", Captured: "files/projects"},
		capturePathMapping{Original: "/nowhere/a.jsonl", Captured: "files/projects/p/a.jsonl"})
	v.save()
	if _, err := VerifyCaptureExport(context.Background(), v.published(v.mustExport())); err != nil {
		t.Fatal(err)
	}

	// The verifier applies the same rule to an export whose hashes and id are
	// consistent but whose manifest maps a root it does not hold.
	d := newExportFixture(t, "codex")
	d.save()
	final := d.published(d.mustExport())
	resealExport(t, final, func(m map[string]any) {
		m["path_map"] = append(m["path_map"].([]any), map[string]any{"original": "/nowhere/state.sqlite", "captured": "files/missing.sqlite"})
	})
	if err := os.Mkdir(filepath.Join(final, "captures/host-a/main/files/missing.sqlite"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCaptureExport(context.Background(), final); err == nil || !strings.Contains(err.Error(), "mapped root") {
		t.Fatalf("verify a dangling mapped root: %v", err)
	}
}

// resealExport rewrites a published export's manifest through edit and then
// its envelope, so that every size, digest and the id agree again.
func resealExport(t *testing.T, final string, edit func(map[string]any)) {
	t.Helper()
	manifestFile := filepath.Join(final, "captures/host-a/main/manifest.json")
	raw, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	value := map[string]any{}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	edit(value)
	if raw, err = json.MarshalIndent(value, "", " "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(final, captureExportEnvelope))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := parseExportEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range envelope.Files {
		if envelope.Files[i].Path == "captures/host-a/main/manifest.json" {
			envelope.Files[i].Size, envelope.Files[i].SHA256 = int64(len(raw)), exportSum(raw)
		}
	}
	if envelope.ID, err = envelope.computeID(); err != nil {
		t.Fatal(err)
	}
	if data, err = envelope.canonical(); err != nil {
		t.Fatal(err)
	}
	if _, err := parseExportEnvelope(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(final, captureExportEnvelope), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureExportManifestVersions(t *testing.T) {
	build := func(t *testing.T, version int, encode string) *exportFixture {
		f := newExportFixture(t, "codex")
		file := f.file("files/a.jsonl", exportContent("a"), encode == "file")
		f.previous(file, "history/20261101T000001.000000000Z/a.jsonl", exportContent("a0"), false)
		s := f.snapshot("files/state_5.sqlite", exportContent("s"), encode == "snapshot", false)
		f.generation(s, "history/20261101T000002.000000000Z/state_5.sqlite", exportContent("g"), encode == "generation", false)
		f.manifest.Version, f.keepVersion = version, true
		f.save()
		return f
	}
	for _, tc := range []struct {
		version int
		encode  string
		ok      bool
	}{
		{1, "", true}, {2, "", true}, {3, "", true}, {4, "", true}, {5, "", true},
		{6, "generation", true}, {7, "file", true}, {8, "snapshot", true}, {8, "", true},
		{0, "", false}, {9, "", false}, {5, "file", false}, {6, "file", false}, {7, "snapshot", false},
	} {
		t.Run(fmt.Sprintf("v%d-%s", tc.version, tc.encode), func(t *testing.T) {
			f := build(t, tc.version, tc.encode)
			export, err := f.export()
			if tc.ok != (err == nil) {
				t.Fatalf("export: %v", err)
			}
			if err == nil {
				if _, err = VerifyCaptureExport(context.Background(), f.published(export)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		f := build(t, 5, "")
		f.editManifest(func(m map[string]any) { delete(m, "version") })
		if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("export: %v", err)
		}
	})
	t.Run("understood", func(t *testing.T) {
		f := build(t, 7, "file")
		override(t, &captureManifestUnderstood, 6)
		if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "exports up to 6") {
			t.Fatalf("export: %v", err)
		}
	})
}

func TestCaptureExportEnvelopeCanonicalGolden(t *testing.T) {
	// Non-ASCII text stays UTF-8; <, > and & are escaped, as are U+2028 and
	// U+2029, exactly as encoding/json writes them.
	envelope := CaptureExport{Format: captureExportFormat, Version: 1, Scope: captureExportScope, HostID: "hôst<&>",
		SourceName: "sourcé\u2028", SourceKind: "Claude", SourceCompleteness: "unknown",
		Files: []CaptureExportFile{
			{Path: "captures/hôst<&>/host.json", Size: 12, SHA256: strings.Repeat("ab", 32)},
			{Path: "captures/hôst<&>/sourcé\u2028/manifest.json", Size: 1099511627776 - 12, SHA256: strings.Repeat("0f", 32)},
		}}
	id, err := envelope.computeID()
	if err != nil {
		t.Fatal(err)
	}
	envelope.ID = id
	canonical, err := envelope.canonical()
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("..", "..", "testdata", "capture-export", "envelope-v1.json")
	if os.Getenv("PHAROS_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, canonical, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, want) {
		t.Fatalf("canonical envelope changed:\n%s\nwant\n%s", canonical, want)
	}
	const wantID = "a248c9461a1c4e57a7ce6f7480c383f2ee9575b1574aaed8dc2c3896f52623dd"
	if id != wantID {
		t.Fatalf("id = %s, want %s", id, wantID)
	}
	if !bytes.Contains(want, []byte(`"host_id":"hôst\u003c\u0026\u003e"`)) || !bytes.Contains(want, []byte(`sourcé\u2028`)) {
		t.Fatalf("golden lost its escapes: %s", want)
	}
	if _, err := parseExportEnvelope(want); err != nil {
		t.Fatal(err)
	}
	if _, err := parseExportEnvelope(append(slices.Clone(want), '\n')); err != nil {
		t.Fatal(err)
	}

	// Anything but that serialization is refused.
	text := string(want)
	replace := func(old, new string) []byte {
		if !strings.Contains(text, old) {
			t.Fatalf("golden has no %q", old)
		}
		return []byte(strings.Replace(text, old, new, 1))
	}
	reid := func(edit func(*CaptureExport)) []byte {
		e := envelope
		e.Files = slices.Clone(envelope.Files)
		edit(&e)
		e.ID, _ = e.computeID()
		data, _ := e.canonical()
		return data
	}
	for name, data := range map[string][]byte{
		"two newlines":     append(slices.Clone(want), '\n', '\n'),
		"indented":         func() []byte { var b bytes.Buffer; json.Indent(&b, want, "", " "); return b.Bytes() }(),
		"unknown field":    replace(`"format"`, `"extra":1,"format"`),
		"duplicate key":    replace(`"format":"pharos-capture-export"`, `"format":"pharos-capture-export","format":"pharos-capture-export"`),
		"case key":         replace(`"format"`, `"Format"`),
		"null files":       []byte(text[:strings.Index(text, `"files":`)] + `"files":null}`),
		"null member":      replace(`"scope":"native-capture"`, `"scope":null`),
		"extra value":      append(slices.Clone(want), []byte(" {}")...),
		"invalid utf-8":    replace("ô", "\xff"),
		"escaped non-html": replace("ô", `\u00f4`),
		"version 2":        reid(func(e *CaptureExport) { e.Version = 2 }),
		"other scope":      reid(func(e *CaptureExport) { e.Scope = "everything" }),
		"complete":         reid(func(e *CaptureExport) { e.SourceCompleteness = "complete" }),
		"unsorted":         reid(func(e *CaptureExport) { e.Files[0], e.Files[1] = e.Files[1], e.Files[0] }),
		"duplicate path":   reid(func(e *CaptureExport) { e.Files[1].Path = e.Files[0].Path }),
		"upper hash":       reid(func(e *CaptureExport) { e.Files[0].SHA256 = strings.Repeat("AB", 32) }),
		"short hash":       reid(func(e *CaptureExport) { e.Files[0].SHA256 = "ab" }),
		"negative size":    reid(func(e *CaptureExport) { e.Files[0].Size = -1 }),
		"oversized":        reid(func(e *CaptureExport) { e.Files[1].Size = 1 << 53 }),
		"fraction":         replace(`"size":12`, `"size":12.5`),
		"exponent":         replace(`"size":12`, `"size":1.2e1`),
		"wrong id":         replace(id, strings.Repeat("0", 64)),
		"upper id":         replace(id, strings.ToUpper(id)),
		"lists itself": reid(func(e *CaptureExport) {
			e.Files = append(e.Files, CaptureExportFile{Path: "export.json", Size: 1, SHA256: strings.Repeat("ab", 32)})
		}),
	} {
		if _, err := parseExportEnvelope(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCaptureExportInterruptedCompression(t *testing.T) {
	// The manifest names zstd but the file still holds its original bytes:
	// a compression killed before its rename. Accepted, copied as stored.
	f := newExportFixture(t, "claude")
	data := exportContent("a")
	file := f.file("files/a.jsonl", data, false)
	file.Encoding, file.StoredSize = captureEncodingZstd, int64(len(zstdBytes(t, data)))
	f.save()
	if f.manifest.Version != captureFilesVersion {
		t.Fatalf("fixture version %d", f.manifest.Version)
	}
	export := f.mustExport()
	got, err := os.ReadFile(filepath.Join(f.published(export), "captures/host-a/main/files/a.jsonl"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("copied %q, %v", got, err)
	}
	if _, err := VerifyCaptureExport(context.Background(), f.published(export)); err != nil {
		t.Fatal(err)
	}

	// Compressed bytes under a plain entry are a torn state, refused.
	g := newExportFixture(t, "claude")
	g.file("files/a.jsonl", data, false)
	g.save()
	g.put("files/a.jsonl", zstdBytes(t, data))
	if _, err := g.export(); !errors.Is(err, errCapturedContentMismatch) {
		t.Fatalf("compressed under plain: %v", err)
	}
	if entries := entriesOf(t, g.out); len(entries) != 0 {
		t.Fatalf("outbox holds %v", entries)
	}
}

func TestCaptureExportCorruptionAndTruncation(t *testing.T) {
	data := exportContent("a")
	for name, damage := range map[string]func(f *exportFixture){
		"stored byte": func(f *exportFixture) { f.put("files/a.jsonl", append(slices.Clone(data[:len(data)-1]), 'X')) },
		"truncated":   func(f *exportFixture) { f.put("files/a.jsonl", data[:len(data)-3]) },
		"grown":       func(f *exportFixture) { f.put("files/a.jsonl", append(slices.Clone(data), '\n')) },
		"encoded content": func(f *exportFixture) {
			f.put("files/b.jsonl", zstdBytes(t, append(slices.Clone(data[:len(data)-1]), 'Y')))
		},
		"encoded frame": func(f *exportFixture) {
			frame := zstdBytes(t, data)
			frame[len(frame)/2] ^= 0xff
			f.put("files/b.jsonl", frame)
		},
		"encoded truncated": func(f *exportFixture) {
			frame := zstdBytes(t, data)
			f.put("files/b.jsonl", frame[:len(frame)-2])
		},
		"generation digest": func(f *exportFixture) {
			f.put("history/20261101T000003.000000000Z/state.sqlite", exportContent("gX"))
		},
		"missing member": func(f *exportFixture) { os.Remove(filepath.Join(f.dir, "files", "a.jsonl")) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newExportFixture(t, "codex")
			f.file("files/a.jsonl", data, false)
			f.file("files/b.jsonl", data, true)
			s := f.snapshot("files/state.sqlite", exportContent("s"), false, false)
			f.generation(s, "history/20261101T000003.000000000Z/state.sqlite", exportContent("g1"), false, true)
			f.save()
			damage(f)
			before := treeState(t, f.root)
			if _, err := f.export(); err == nil {
				t.Fatal("exported a damaged capture")
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
			if after := treeState(t, f.root); !mapsEqual(before, after) {
				t.Fatal("the capture changed")
			}
		})
	}
}

func TestCaptureExportVerifierRejectsDamage(t *testing.T) {
	f := claudeFixture(t)
	export := f.mustExport()
	final := f.published(export)
	member := filepath.Join(final, "captures/host-a/main/files/projects/p/a.jsonl")
	encoded := filepath.Join(final, "captures/host-a/main/files/projects/q/c.jsonl")
	pristine := t.TempDir()
	copyTree := func(from, to string) {
		t.Helper()
		os.RemoveAll(to)
		err := filepath.WalkDir(from, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(from, file)
			target := filepath.Join(to, rel)
			if entry.IsDir() {
				return os.MkdirAll(target, 0o700)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	copyTree(final, filepath.Join(pristine, "x"))
	for name, damage := range map[string]func(){
		"flipped byte": func() {
			data, _ := os.ReadFile(member)
			data[0] ^= 1
			os.WriteFile(member, data, 0o600)
		},
		"truncated":       func() { os.Truncate(member, 3) },
		"encoded swapped": func() { os.WriteFile(encoded, zstdBytes(t, exportContent("other")), 0o600) },
		"extra file": func() {
			os.WriteFile(filepath.Join(final, "captures/host-a/main/files/extra.jsonl"), []byte("x"), 0o600)
		},
		"extra top": func() { os.WriteFile(filepath.Join(final, "README"), []byte("x"), 0o600) },
		"extra dir": func() { os.Mkdir(filepath.Join(final, "captures/host-a/main/files/new"), 0o700) },
		"missing":   func() { os.Remove(member) },
		"symlink":   func() { os.Remove(member); os.Symlink(filepath.Join(f.dir, "files/projects/p/a.jsonl"), member) },
		"fifo":      func() { os.Remove(member); syscall.Mkfifo(member, 0o600) },
		"envelope":  func() { os.WriteFile(filepath.Join(final, captureExportEnvelope), []byte("{}"), 0o600) },
		"manifest": func() {
			os.WriteFile(filepath.Join(final, "captures/host-a/main/manifest.json"), []byte("{}\n"), 0o600)
		},
	} {
		damage()
		if _, err := VerifyCaptureExport(context.Background(), final); err == nil {
			t.Errorf("%s: verified", name)
		}
		copyTree(filepath.Join(pristine, "x"), final)
		if _, err := VerifyCaptureExport(context.Background(), final); err != nil {
			t.Fatalf("after %s, restoring failed: %v", name, err)
		}
	}

	// The permitted tree comes from the metadata, so an undeclared directory
	// is refused without being listed, however wide or deep it is, and extras
	// beside declared files are refused as they are met.
	scanned := []string{}
	override(t, &captureExportHook, func(step, rel string) error {
		if step == "scan" {
			scanned = append(scanned, rel)
		}
		return nil
	})
	for name, extra := range map[string]func(){
		"wide undeclared dir": func() {
			for i := range 300 {
				os.MkdirAll(filepath.Join(final, "captures/host-a/main/files/wide"), 0o700)
				os.WriteFile(filepath.Join(final, fmt.Sprintf("captures/host-a/main/files/wide/%03d", i)), nil, 0o600)
			}
		},
		"deep undeclared dir": func() {
			os.MkdirAll(filepath.Join(final, "captures/host-a/main/history/deep"+strings.Repeat("/d", 40)), 0o700)
		},
		"wide extras beside members": func() {
			for i := range 300 {
				os.WriteFile(filepath.Join(final, fmt.Sprintf("captures/host-a/main/files/projects/p/extra-%03d", i)), nil, 0o600)
			}
		},
	} {
		scanned = scanned[:0]
		extra()
		if _, err := VerifyCaptureExport(context.Background(), final); err == nil {
			t.Errorf("%s: verified", name)
		}
		for _, rel := range scanned {
			if strings.Contains(rel, "/wide") || strings.Contains(rel, "/deep") {
				t.Errorf("%s: listed %s", name, rel)
			}
		}
		copyTree(filepath.Join(pristine, "x"), final)
	}
	// Bounds lowered below what the export holds refuse it as out of scope.
	inspection, limits := captureExportInspection, captureExportLimits
	for name, set := range map[string]func(){
		"entries": func() { captureExportInspection.Entries = 5 },
		"depth":   func() { captureExportInspection.Depth = 5 },
		"path":    func() { captureExportLimits.PathBytes = 40 },
	} {
		set()
		if _, err := VerifyCaptureExport(context.Background(), final); !errors.Is(err, errCaptureExportScope) {
			t.Errorf("%s: %v", name, err)
		}
		captureExportInspection, captureExportLimits = inspection, limits
	}
	// Cancelled while a declared directory is listed.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	override(t, &captureExportHook, func(step, rel string) error {
		if step == "scan" && rel == "captures/host-a/main/files/projects/p" {
			cancel()
		}
		return nil
	})
	if _, err := VerifyCaptureExport(ctx, final); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verify: %v", err)
	}
	captureExportHook = nil

	stage := filepath.Join(f.out, captureExportStagePrefix+"abc")
	copyTree(final, stage)
	if _, err := VerifyCaptureExport(context.Background(), stage); err == nil {
		t.Error("a stage verified as a publication")
	}
}

func TestCaptureExportStrictMetadata(t *testing.T) {
	base := func(t *testing.T) *exportFixture {
		f := newExportFixture(t, "claude")
		f.file("files/a.jsonl", exportContent("a"), false)
		f.save()
		return f
	}
	file0 := func(m map[string]any) map[string]any { return m["files"].([]any)[0].(map[string]any) }
	accept := map[string]func(map[string]any){
		"null path_map":  func(m map[string]any) { m["path_map"] = nil },
		"null snapshots": func(m map[string]any) { m["snapshots"] = nil },
		"null errors":    func(m map[string]any) { m["last_run"].(map[string]any)["errors"] = nil },
	}
	for name, edit := range accept {
		t.Run(name, func(t *testing.T) {
			f := base(t)
			f.editManifest(edit)
			if _, err := f.export(); err != nil {
				t.Fatal(err)
			}
		})
	}
	refuse := map[string]func(map[string]any){
		"null source":     func(m map[string]any) { m["source"] = nil },
		"null file":       func(m map[string]any) { m["files"] = []any{nil} },
		"null string":     func(m map[string]any) { file0(m)["sha256"] = nil },
		"null warnings":   func(m map[string]any) { m["last_run"].(map[string]any)["warnings"] = nil },
		"unknown field":   func(m map[string]any) { m["extra"] = 1 },
		"nested unknown":  func(m map[string]any) { file0(m)["extra"] = 1 },
		"case key":        func(m map[string]any) { m["Version"] = m["version"]; delete(m, "version") },
		"string number":   func(m map[string]any) { file0(m)["size"] = "12" },
		"fraction":        func(m map[string]any) { file0(m)["size"] = 1.5 },
		"negative":        func(m map[string]any) { file0(m)["size"] = -1 },
		"oversized":       func(m map[string]any) { file0(m)["size"] = float64(1 << 53) },
		"upper hash":      func(m map[string]any) { file0(m)["sha256"] = strings.ToUpper(file0(m)["sha256"].(string)) },
		"bad encoding":    func(m map[string]any) { file0(m)["encoding"] = "gzip"; file0(m)["stored_size"] = 3 },
		"stray stored":    func(m map[string]any) { file0(m)["stored_size"] = 3 },
		"other host":      func(m map[string]any) { m["host"].(map[string]any)["id"] = "host-b" },
		"other source":    func(m map[string]any) { m["source"].(map[string]any)["name"] = "other" },
		"other kind":      func(m map[string]any) { m["source"].(map[string]any)["kind"] = "copilot" },
		"unknown kind":    func(m map[string]any) { m["source"].(map[string]any)["kind"] = "mystery" },
		"unsafe path":     func(m map[string]any) { file0(m)["captured"] = "files/../manifest.json" },
		"history current": func(m map[string]any) { file0(m)["captured"] = "history/a.jsonl" },
	}
	for name, edit := range refuse {
		t.Run(name, func(t *testing.T) {
			f := base(t)
			f.editManifest(edit)
			if _, err := f.export(); err == nil {
				t.Fatal("exported")
			}
		})
	}
	raw := map[string]func(*exportFixture) []byte{
		"duplicate key": func(f *exportFixture) []byte {
			return bytes.Replace(f.manifestBytes, []byte("{"), []byte(`{"version": 5,`), 1)
		},
		"extra value": func(f *exportFixture) []byte { return append(slices.Clone(f.manifestBytes), []byte("{}")...) },
		"invalid utf-8": func(f *exportFixture) []byte {
			return bytes.Replace(f.manifestBytes, []byte("Test Mac"), []byte("Test \xffac"), 1)
		},
		"truncated": func(f *exportFixture) []byte { return f.manifestBytes[:len(f.manifestBytes)/2] },
	}
	for name, make := range raw {
		t.Run(name, func(t *testing.T) {
			f := base(t)
			f.saveManifest(make(f))
			if _, err := f.export(); err == nil {
				t.Fatal("exported")
			}
		})
	}
	for name, data := range map[string]string{
		"host unknown":   `{"id":"host-a","label":"x","user":"u","first_capture_at":"t","os":"mac"}`,
		"host duplicate": `{"id":"host-a","id":"host-a","label":"x","user":"u","first_capture_at":"t"}`,
		"host other id":  `{"id":"host-b","label":"x","user":"u","first_capture_at":"t"}`,
		"host null":      `{"id":"host-a","label":null,"user":"u","first_capture_at":"t"}`,
		"host array":     `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := base(t)
			f.saveHost([]byte(data))
			if _, err := f.export(); err == nil {
				t.Fatal("exported")
			}
		})
	}
	t.Run("host minimal", func(t *testing.T) {
		f := base(t)
		f.saveHost([]byte(`{"id":"host-a","label":"x","user":"u","first_capture_at":"t"}`))
		if _, err := f.export(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCaptureExportRefusesUnsupportedInventories(t *testing.T) {
	cases := map[string]func(f *exportFixture){
		"sqlite records":  func(f *exportFixture) { f.manifest.SQLiteRecordCapture = true },
		"latest records":  func(f *exportFixture) { f.manifest.LatestRecords = true },
		"retained inputs": func(f *exportFixture) { f.manifest.RetainedInputCapture = true },
		"record coverage": func(f *exportFixture) { f.manifest.RecordCoverage = map[string]sqliteCoverageStatus{"x": {}} },
		"recovery file":   func(f *exportFixture) { f.manifest.Files[0].RecoveryID = "r1" },
		"installations": func(f *exportFixture) {
			f.manifest.Installations = []capturedInstallation{{ID: "i", DBPath: "/x", CapturedDB: "files/x"}}
		},
		"records dir":      func(f *exportFixture) { os.Mkdir(filepath.Join(f.dir, "records"), 0o700) },
		"inputs dir":       func(f *exportFixture) { os.Mkdir(filepath.Join(f.dir, "inputs"), 0o700) },
		"pins":             func(f *exportFixture) { f.put(preservationPinsName, []byte("{}")) },
		"delivered":        func(f *exportFixture) { f.put(".delivered.json", []byte("{}")) },
		"pin reader":       func(f *exportFixture) { os.Mkdir(filepath.Join(f.dir, ".reader-pin-x"+captureTempSuffix), 0o700) },
		"unknown empty":    func(f *exportFixture) { os.Mkdir(filepath.Join(f.dir, "notes"), 0o700) },
		"unknown hidden":   func(f *exportFixture) { f.put(".DS_Store", nil) },
		"temporary file":   func(f *exportFixture) { f.put("manifest.json"+captureTempSuffix, nil) },
		"reader as file":   func(f *exportFixture) { f.put(".reader-x"+captureTempSuffix, nil) },
		"index as dir":     func(f *exportFixture) { os.Mkdir(filepath.Join(f.dir, indexMarkerName), 0o700) },
		"history backfill": func(f *exportFixture) { os.Mkdir(filepath.Join(f.root, historyDir), 0o700) },
		"files symlink": func(f *exportFixture) {
			os.Rename(filepath.Join(f.dir, "files"), filepath.Join(f.dir, "..", "elsewhere"))
			os.Symlink(filepath.Join(f.dir, "..", "elsewhere"), filepath.Join(f.dir, "files"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newExportFixture(t, "claude")
			f.file("files/a.jsonl", exportContent("a"), false)
			setup(f)
			f.save()
			before := treeState(t, f.root)
			_, err := f.export()
			if err == nil {
				t.Fatal("exported")
			}
			if strings.Contains(err.Error(), "delete") || strings.Contains(err.Error(), "remove") {
				t.Fatalf("the refusal suggests deleting evidence: %v", err)
			}
			if after := treeState(t, f.root); !mapsEqual(before, after) {
				t.Fatal("the capture changed")
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
		})
	}
}

func TestCaptureExportLimits(t *testing.T) {
	build := func(t *testing.T) *exportFixture {
		f := newExportFixture(t, "claude")
		f.file("files/a.jsonl", exportContent("a"), false)
		f.file("files/b.jsonl", exportContent("b"), true)
		f.save()
		return f
	}
	limits := captureExportLimits
	for name, lowered := range map[string]captureExportLimitSet{
		"members":  {Metadata: limits.Metadata, Members: 3, PathBytes: limits.PathBytes, Stored: limits.Stored, Decoded: limits.Decoded},
		"path":     {Metadata: limits.Metadata, Members: limits.Members, PathBytes: 30, Stored: limits.Stored, Decoded: limits.Decoded},
		"metadata": {Metadata: 64, Members: limits.Members, PathBytes: limits.PathBytes, Stored: limits.Stored, Decoded: limits.Decoded},
		"stored":   {Metadata: limits.Metadata, Members: limits.Members, PathBytes: limits.PathBytes, Stored: 1000, Decoded: limits.Decoded},
		"decoded":  {Metadata: limits.Metadata, Members: limits.Members, PathBytes: limits.PathBytes, Stored: limits.Stored, Decoded: 1000},
	} {
		t.Run(name, func(t *testing.T) {
			f := build(t)
			override(t, &captureExportLimits, lowered)
			if _, err := f.export(); !errors.Is(err, errCaptureExportScope) {
				t.Fatalf("export: %v", err)
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
		})
	}

	// Inspecting the source is bounded by the names it examines, kept or not,
	// by depth and by path length, whatever the manifest declares; passing a
	// bound fails rather than skipping what is left.
	inspection := captureExportInspection
	for name, tc := range map[string]struct {
		setup  func(f *exportFixture)
		limits captureExportInspectionLimits
		paths  int
	}{
		"wide leftovers": {func(f *exportFixture) {
			for i := range 12 {
				f.put(fmt.Sprintf("files/left/%02d.jsonl.pharos-tmp", i), []byte("x"))
			}
		}, captureExportInspectionLimits{Entries: 10, Depth: inspection.Depth}, limits.PathBytes},
		"deep leftovers": {func(f *exportFixture) {
			f.put("files/d/d/d/d/d/x.pharos-tmp", []byte("x"))
		}, captureExportInspectionLimits{Entries: inspection.Entries, Depth: 6}, limits.PathBytes},
		"deep member": {func(f *exportFixture) {
			f.file("files/d/d/d/d/a.jsonl", exportContent("d"), false)
		}, captureExportInspectionLimits{Entries: inspection.Entries, Depth: 6}, limits.PathBytes},
		"long leftover": {func(f *exportFixture) {
			f.put("files/left/"+strings.Repeat("x", 40), []byte("x"))
		}, inspection, 50},
	} {
		t.Run(name, func(t *testing.T) {
			f := build(t)
			tc.setup(f)
			f.save()
			// The same capture exports under the default bounds.
			f.mustExport()
			os.RemoveAll(f.out)
			os.Mkdir(f.out, 0o700)
			override(t, &captureExportInspection, tc.limits)
			lowered := limits
			lowered.PathBytes = tc.paths
			override(t, &captureExportLimits, lowered)
			if _, err := f.export(); !errors.Is(err, errCaptureExportScope) {
				t.Fatalf("export: %v", err)
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
		})
	}

	// Cancelled while a wide directory is being listed: the export returns
	// at once, closes every handle and removes its stage.
	t.Run("cancelled wide scan", func(t *testing.T) {
		f := build(t)
		for i := range 40 {
			f.put(fmt.Sprintf("files/wide/%02d.jsonl.pharos-tmp", i), []byte("x"))
		}
		f.save()
		f.mustExport() // settles the runtime's own descriptors
		os.RemoveAll(f.out)
		os.Mkdir(f.out, 0o700)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		visited := 0
		override(t, &captureExportHook, func(step, rel string) error {
			if step == "scan" && rel == "files/wide" {
				visited++
				cancel()
			}
			return nil
		})
		before := openExportDescriptors(t)
		if _, err := ExportCapture(ctx, f.root, f.host, f.source, f.out); !errors.Is(err, context.Canceled) {
			t.Fatalf("export: %v", err)
		}
		if visited != 1 {
			t.Fatalf("the wide directory was listed %d times", visited)
		}
		if after := openExportDescriptors(t); after != before {
			t.Fatalf("%d descriptors open before, %d after", before, after)
		}
		if entries := entriesOf(t, f.out); len(entries) != 0 {
			t.Fatalf("outbox holds %v", entries)
		}
	})

	if got := addBounded(1<<62, 1<<62, 1<<63-1); got != -1 {
		t.Fatalf("overflow: %d", got)
	}
	if got := addBounded(5, 5, 10); got != 10 {
		t.Fatalf("addBounded = %d", got)
	}
}

// openExportDescriptors counts this process's open file descriptors.
func openExportDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("no /dev/fd to count descriptors")
	}
	return len(entries)
}

func TestCaptureExportPathRules(t *testing.T) {
	ok := []string{"captures/h/s/files/a.jsonl", "captures/h/s/files/.hidden", "captures/h/s/files/con-tribution.txt", "captures/h/s/files/COM10", "captures/h/s/files/über.jsonl"}
	if err := checkExportPaths(ok, nil); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"captures/h/s/files/", "captures/h/s/files//a", "captures/h/s/files/./a", "captures/h/s/files/../a", "/captures/a",
		"captures/h/s/files/a\\b", "captures/h/s/files/a\x00b", "captures/h/s/files/a:b", "C:/captures/a", "//server/share/a",
		"captures/h/s/files/a.", "captures/h/s/files/a ", "captures/h/s/files/CON", "captures/h/s/files/con.txt", "captures/h/s/files/Nul.tar.gz",
		"captures/h/s/files/aux .txt", "captures/h/s/files/COM1", "captures/h/s/files/lpt9.log", "captures/h/s/files/COM¹", "captures/h/s/files/CONIN$",
		"captures/h/s/files/\xff",
	} {
		if err := checkExportPaths([]string{bad}, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, pair := range [][2]string{
		{"captures/h/s/files/A.jsonl", "captures/h/s/files/a.jsonl"},
		{"captures/h/s/files/caf\u00e9", "captures/h/s/files/cafe\u0301"},
		{"captures/h/s/files/Straße", "captures/h/s/files/STRASSE"},
	} {
		if err := checkExportPaths(pair[:], nil); err == nil {
			t.Errorf("%q and %q accepted", pair[0], pair[1])
		}
	}
	// Directory components collide too.
	if err := checkExportPaths([]string{"captures/h/s/files/Dir/x", "captures/h/s/files/dir/y"}, []string{"captures/h/s/files/Dir", "captures/h/s/files/dir"}); err == nil {
		t.Error("colliding directories accepted")
	}
	if err := checkExportPaths([]string{"captures/a"}, []string{"captures/a"}); err == nil {
		t.Error("a path that is both a file and a directory accepted")
	}

	// In a manifest: collisions are refused before anything is read.
	f := newExportFixture(t, "claude")
	f.file("files/Dir/x.jsonl", exportContent("x"), false)
	f.manifest.Files = append(f.manifest.Files, &capturedFile{Captured: "files/dir/y.jsonl", Size: 1, SHA256: exportSum([]byte("y"))})
	f.save()
	if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "same file") {
		t.Fatalf("collision: %v", err)
	}
	g := newExportFixture(t, "claude")
	g.file("files/a.jsonl", exportContent("a"), false)
	g.manifest.Files = append(g.manifest.Files, &capturedFile{Captured: "files/a.jsonl/b", Size: 1})
	g.save()
	if _, err := g.export(); err == nil {
		t.Fatal("a member under another member was accepted")
	}
	h := newExportFixture(t, "claude")
	a := h.file("files/a.jsonl", exportContent("a"), false)
	h.manifest.Files = append(h.manifest.Files, &capturedFile{Captured: a.Captured, Size: a.Size + 1, SHA256: a.SHA256})
	h.save()
	if _, err := h.export(); err == nil || !strings.Contains(err.Error(), "claimed twice") {
		t.Fatalf("inconsistent claims: %v", err)
	}
}

func TestCaptureExportRefusesLinksAndSpecialFiles(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, exportContent("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(f *exportFixture){
		"member symlink": func(f *exportFixture) {
			os.Remove(filepath.Join(f.dir, "files/p/a.jsonl"))
			os.Symlink(outside, filepath.Join(f.dir, "files/p/a.jsonl"))
		},
		"ancestor symlink": func(f *exportFixture) {
			real := filepath.Join(f.dir, "..", "real-p")
			os.Rename(filepath.Join(f.dir, "files/p"), real)
			os.Symlink(real, filepath.Join(f.dir, "files/p"))
		},
		"unreferenced symlink": func(f *exportFixture) { os.Symlink(outside, filepath.Join(f.dir, "files/p/link.jsonl")) },
		"history fifo": func(f *exportFixture) {
			os.MkdirAll(filepath.Join(f.dir, "history/x"), 0o700)
			syscall.Mkfifo(filepath.Join(f.dir, "history/x/pipe"), 0o600)
		},
		"member fifo": func(f *exportFixture) {
			os.Remove(filepath.Join(f.dir, "files/p/a.jsonl"))
			syscall.Mkfifo(filepath.Join(f.dir, "files/p/a.jsonl"), 0o600)
		},
		"member dir": func(f *exportFixture) {
			os.Remove(filepath.Join(f.dir, "files/p/a.jsonl"))
			os.Mkdir(filepath.Join(f.dir, "files/p/a.jsonl"), 0o700)
		},
		"host.json symlink": func(f *exportFixture) {
			hostFile := filepath.Join(f.root, f.host, captureHostName)
			os.Rename(hostFile, hostFile+".real")
			os.Symlink(hostFile+".real", hostFile)
		},
		"manifest fifo": func(f *exportFixture) {
			os.Remove(filepath.Join(f.dir, captureManifestName))
			syscall.Mkfifo(filepath.Join(f.dir, captureManifestName), 0o600)
		},
		"lock symlink": func(f *exportFixture) {
			lock := filepath.Join(f.root, f.host, captureLockName)
			os.Rename(lock, lock+".real")
			os.Symlink(lock+".real", lock)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newExportFixture(t, "claude")
			f.file("files/p/a.jsonl", exportContent("a"), false)
			f.save()
			setup(f)
			// A FIFO must be refused from its handle, not block the export.
			if _, err := f.export(); err == nil {
				t.Fatal("exported")
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
		})
	}
}

func TestCaptureExportCaptureLock(t *testing.T) {
	f := newExportFixture(t, "claude")
	f.file("files/a.jsonl", exportContent("a"), false)
	f.save()
	lockPath := filepath.Join(f.root, f.host, captureLockName)

	// Busy: another holder fails the export at once.
	holder, err := os.Open(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.export(); !errors.Is(err, errCaptureBusy) {
		t.Fatalf("busy: %v", err)
	}
	holder.Close()

	// Held from the first metadata read until the copy is verified, and
	// released afterwards.
	held := 0
	override(t, &captureExportHook, func(step, rel string) error {
		if step == "copy" || step == "staged" {
			probe, err := os.Open(lockPath)
			if err != nil {
				return err
			}
			defer probe.Close()
			if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				return fmt.Errorf("%s ran without the capture lock: %v", step, err)
			}
			held++
		}
		return nil
	})
	f.mustExport()
	if held != 2 {
		t.Fatalf("checked the lock %d times", held)
	}
	probe, err := os.Open(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	probe.Close()

	// Missing: never created.
	g := newExportFixture(t, "claude")
	g.save()
	os.Remove(filepath.Join(g.root, g.host, captureLockName))
	if _, err := g.export(); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing lock: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(g.root, g.host, captureLockName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the export created the capture lock")
	}
	// Missing source or host.
	if _, err := ExportCapture(context.Background(), f.root, f.host, "absent", f.out); err == nil {
		t.Fatal("exported an absent source")
	}
	if _, err := ExportCapture(context.Background(), f.root, "absent", f.source, f.out); err == nil {
		t.Fatal("exported an absent host")
	}
	if _, err := ExportCapture(context.Background(), f.root, f.host, f.source, filepath.Join(f.out, "absent")); err == nil {
		t.Fatal("created an outbox")
	}
}

func TestCaptureExportOutboxOverlap(t *testing.T) {
	f := newExportFixture(t, "claude")
	f.save()
	inside := filepath.Join(f.root, "outbox")
	os.Mkdir(inside, 0o700)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.root, alias); err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"outbox in root":       {f.root, inside},
		"root in outbox":       {f.root, filepath.Dir(f.root)},
		"same":                 {f.root, f.root},
		"outbox through alias": {alias, inside},
		"source as outbox":     {f.root, f.dir},
	} {
		if _, err := ExportCapture(context.Background(), pair[0], f.host, f.source, pair[1]); err == nil || !strings.Contains(err.Error(), "inside") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An alias of the root is resolved, and the export works through it.
	if _, err := ExportCapture(context.Background(), alias, f.host, f.source, f.out); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureExportPublication(t *testing.T) {
	t.Run("existing incorrect", func(t *testing.T) {
		f := claudeFixture(t)
		export := f.mustExport()
		member := filepath.Join(f.published(export), "captures/host-a/main/files/projects/p/a.jsonl")
		os.WriteFile(member, []byte("damaged"), 0o600)
		if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "not a valid export") {
			t.Fatalf("export over a damaged copy: %v", err)
		}
		if data, _ := os.ReadFile(member); string(data) != "damaged" {
			t.Fatal("the existing directory was replaced")
		}
		if entries := entriesOf(t, f.out); !slices.Equal(entries, []string{export.ID}) {
			t.Fatalf("outbox holds %v", entries)
		}
	})
	t.Run("existing empty", func(t *testing.T) {
		f := claudeFixture(t)
		export := f.mustExport()
		os.RemoveAll(f.published(export))
		os.Mkdir(f.published(export), 0o700)
		if _, err := f.export(); err == nil {
			t.Fatal("exported over an empty directory")
		}
		if entries := entriesOf(t, f.published(export)); len(entries) != 0 {
			t.Fatalf("the empty directory now holds %v", entries)
		}
	})
	t.Run("existing different", func(t *testing.T) {
		f := claudeFixture(t)
		g := newExportFixture(t, "claude")
		g.save()
		other := g.mustExport()
		id := f.mustExport().ID
		os.RemoveAll(filepath.Join(f.out, id))
		if err := os.Rename(g.published(other), filepath.Join(f.out, id)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.export(); err == nil {
			t.Fatal("accepted another export under its id")
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		f := claudeFixture(t)
		arrived, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		override(t, &captureExportHook, func(step, rel string) error {
			if step == "rename" {
				first := false
				once.Do(func() { first = true })
				if first {
					close(arrived)
					<-release
				}
			}
			return nil
		})
		type result struct {
			export CaptureExport
			err    error
		}
		done := make(chan result, 1)
		go func() {
			export, err := f.export()
			done <- result{export, err}
		}()
		<-arrived
		// The first export has released the capture lock and waits to
		// rename; the second publishes first.
		second, err := f.export()
		if err != nil {
			t.Fatal(err)
		}
		close(release)
		first := <-done
		if first.err != nil || !sameExportEnvelope(first.export, second) {
			t.Fatalf("first = %+v, %v", first.export, first.err)
		}
		if entries := entriesOf(t, f.out); !slices.Equal(entries, []string{second.ID}) {
			t.Fatalf("outbox holds %v", entries)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		// Cancelled while copying, while rereading members at the end of the
		// freeze, after verification (before the flush) and just before the
		// rename: nothing is published.
		for _, step := range []string{"copy", "recheck", "flush", "rename"} {
			f := claudeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			override(t, &captureExportHook, func(at, rel string) error {
				if at == step {
					cancel()
				}
				return nil
			})
			before := treeState(t, f.root)
			if _, err := ExportCapture(ctx, f.root, f.host, f.source, f.out); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled at %s: %v", step, err)
			}
			cancel()
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("cancelled at %s, the outbox holds %v", step, entries)
			}
			if after := treeState(t, f.root); !mapsEqual(before, after) {
				t.Fatalf("cancelled at %s, the capture changed", step)
			}
		}
		// A cancelled retry does not report an existing export as confirmed.
		f := claudeFixture(t)
		f.mustExport()
		ctx, cancel := context.WithCancel(context.Background())
		override(t, &captureExportHook, func(at, rel string) error {
			if at == "rename" {
				cancel()
			}
			return nil
		})
		if _, err := ExportCapture(ctx, f.root, f.host, f.source, f.out); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled retry: %v", err)
		}
		cancel()
	})
	t.Run("source changed", func(t *testing.T) {
		// The first of two members changes while the second is copied. Each
		// change is caught before the freeze ends, even one that keeps the
		// file's inode, size and nanosecond mtime; only the unpublished stage
		// is removed, and the changed capture is left as it is.
		for name, change := range map[string]func(t *testing.T, file string){
			"same inode, size and mtime": func(t *testing.T, file string) {
				info, err := os.Lstat(file)
				if err != nil {
					t.Fatal(err)
				}
				w, err := os.OpenFile(file, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.WriteAt([]byte("Z"), 3); err != nil {
					t.Fatal(err)
				}
				w.Close()
				if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				after, _ := os.Lstat(file)
				if !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() || !os.SameFile(info, after) {
					t.Fatal("the change did not keep the file's identity, size and mtime")
				}
			},
			"replaced inode": func(t *testing.T, file string) {
				data, _ := os.ReadFile(file)
				os.WriteFile(file+".new", data, 0o600)
				if err := os.Rename(file+".new", file); err != nil {
					t.Fatal(err)
				}
			},
			"grown": func(t *testing.T, file string) {
				w, _ := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0)
				w.Write([]byte("\n"))
				w.Close()
			},
			"touched": func(t *testing.T, file string) {
				info, _ := os.Lstat(file)
				later := info.ModTime().Add(1)
				if err := os.Chtimes(file, later, later); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				f := newExportFixture(t, "claude")
				f.file("files/a.jsonl", exportContent("a"), false)
				f.file("files/b.jsonl", exportContent("b"), true)
				f.save()
				first := filepath.Join(f.dir, "files/a.jsonl")
				override(t, &captureExportHook, func(step, rel string) error {
					if step == "copy" && rel == "files/b.jsonl" {
						change(t, first)
					}
					return nil
				})
				before := treeState(t, f.root)
				if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "changed while it was exported") {
					t.Fatalf("export: %v", err)
				}
				if entries := entriesOf(t, f.out); len(entries) != 0 {
					t.Fatalf("outbox holds %v", entries)
				}
				if after := treeState(t, f.root); mapsEqual(before, after) {
					t.Fatal("the source change was reverted")
				}
			})
		}
	})
	for _, step := range []string{"copy", "staged", "flush", "rename"} {
		t.Run(step+" failure", func(t *testing.T) {
			f := claudeFixture(t)
			boom := errors.New("injected " + step)
			override(t, &captureExportHook, func(at, rel string) error {
				if at == step {
					return boom
				}
				return nil
			})
			if _, err := f.export(); !errors.Is(err, boom) {
				t.Fatalf("export: %v", err)
			}
			if entries := entriesOf(t, f.out); len(entries) != 0 {
				t.Fatalf("outbox holds %v", entries)
			}
		})
	}
	t.Run("staged copy damaged", func(t *testing.T) {
		f := claudeFixture(t)
		override(t, &captureExportHook, func(step, rel string) error {
			if step == "staged" {
				return os.WriteFile(filepath.Join(rel, "captures/host-a/main/files/projects/p/b.jsonl"), exportContent("B"), 0o600)
			}
			return nil
		})
		if _, err := f.export(); err == nil || !strings.Contains(err.Error(), "did not verify") {
			t.Fatalf("export: %v", err)
		}
		if entries := entriesOf(t, f.out); len(entries) != 0 {
			t.Fatalf("outbox holds %v", entries)
		}
	})
	t.Run("rename errors", func(t *testing.T) {
		f := claudeFixture(t)
		override(t, &exportRenameNoReplace, func(int, string, int, string) error { return syscall.EIO })
		if _, err := f.export(); err == nil || !errors.Is(err, syscall.EIO) {
			t.Fatalf("export: %v", err)
		}
		override(t, &exportRenameNoReplace, func(int, string, int, string) error { return syscall.ENOTSUP })
		if _, err := f.export(); !errors.Is(err, errCaptureExportUnsupported) {
			t.Fatalf("export: %v", err)
		}
		if entries := entriesOf(t, f.out); len(entries) != 0 {
			t.Fatalf("outbox holds %v", entries)
		}
	})
	t.Run("post-rename durability", func(t *testing.T) {
		f := claudeFixture(t)
		boom := errors.New("injected flush")
		flushes := 0
		override(t, &captureExportHook, func(step, rel string) error {
			if step == "published" {
				flushes++
				if flushes == 1 {
					return boom
				}
			}
			return nil
		})
		export, err := f.export()
		if !errors.Is(err, errCaptureExportDurability) || !strings.Contains(err.Error(), boom.Error()) || !validExportHash(export.ID) {
			t.Fatalf("export = %+v, %v", export, err)
		}
		// The renamed export is kept and is valid.
		if _, err := VerifyCaptureExport(context.Background(), f.published(export)); err != nil {
			t.Fatal(err)
		}
		again, err := f.export()
		if err != nil || !sameExportEnvelope(again, export) || flushes != 2 {
			t.Fatalf("retry = %+v, %v after %d flushes", again, err, flushes)
		}
		if entries := entriesOf(t, f.out); !slices.Equal(entries, []string{export.ID}) {
			t.Fatalf("outbox holds %v", entries)
		}
	})
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	output := make(chan string)
	go func() {
		data, _ := io.ReadAll(read)
		output <- string(data)
	}()
	err = fn()
	os.Stdout = saved
	write.Close()
	return <-output, err
}

func TestCaptureExportCommands(t *testing.T) {
	f := claudeFixture(t)
	config := filepath.Join(t.TempDir(), "never", "library.toml")
	support := os.Getenv("PHAROS_SUPPORT_DIR")
	supportBefore := []string{}
	if entries, err := os.ReadDir(support); err == nil {
		for _, entry := range entries {
			supportBefore = append(supportBefore, entry.Name())
		}
	}
	out, err := captureStdout(t, func() error {
		return Run([]string{"--config", config, "capture-export", "--root", f.root, "--host", f.host, "--out", f.out, f.source})
	})
	if err != nil {
		t.Fatal(err)
	}
	export := CaptureExport{}
	if err := json.Unmarshal([]byte(out), &export); err != nil || !validExportHash(export.ID) {
		t.Fatalf("output %q: %v", out, err)
	}
	verified, err := captureStdout(t, func() error { return Run([]string{"capture-export-verify", f.published(export)}) })
	if err != nil {
		t.Fatal(err)
	}
	again := CaptureExport{}
	if err := json.Unmarshal([]byte(verified), &again); err != nil || !sameExportEnvelope(again, export) {
		t.Fatalf("verify output %q: %v", verified, err)
	}
	if _, err := os.Stat(filepath.Dir(config)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a configuration was created")
	}
	supportAfter := []string{}
	if entries, err := os.ReadDir(support); err == nil {
		for _, entry := range entries {
			supportAfter = append(supportAfter, entry.Name())
		}
	}
	for _, name := range supportAfter {
		if !slices.Contains(supportBefore, name) && strings.Contains(strings.ToLower(name), "catalog") {
			t.Fatalf("the command created %s", name)
		}
	}
	for _, args := range [][]string{
		{"capture-export"},
		{"capture-export", "--root", f.root, "--host", f.host, f.source},
		{"capture-export", "--root", f.root, "--host", f.host, "--out", f.out},
		{"capture-export", "--root", f.root, "--host", f.host, "--out", f.out, f.source, "extra"},
		{"capture-export", "--root", f.root, "--root", f.root, "--host", f.host, "--out", f.out, f.source},
		{"capture-export", "--root", f.root, "--host", f.host, "--out", f.out, "--force", f.source},
		{"capture-export", "--root", "", "--host", f.host, "--out", f.out, f.source},
		{"capture-export-verify"},
		{"capture-export-verify", "a", "b"},
		{"capture-export-verify", "--deep", f.published(export)},
	} {
		if err := Run(args); err == nil || !strings.Contains(err.Error(), "usage: pharos "+args[0]) {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, name := range []string{"../x", ".hidden", "a/b", "host.json"} {
		if _, err := ExportCapture(context.Background(), f.root, name, f.source, f.out); err == nil {
			t.Errorf("host %q accepted", name)
		}
		if _, err := ExportCapture(context.Background(), f.root, f.host, name, f.out); err == nil {
			t.Errorf("source %q accepted", name)
		}
	}
}
