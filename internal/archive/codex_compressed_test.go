package archive

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// compressRollout does what Codex does to a rollout idle for a week: writes
// it as one zstd frame beside itself, keeps its mtime, deletes the plain file.
func compressRollout(t *testing.T, plain string) string {
	t.Helper()
	info, err := os.Stat(plain)
	if err != nil {
		t.Fatal(err)
	}
	writeCompressedRollout(t, plain+".zst", readText(t, plain), info.ModTime())
	if err := os.Remove(plain); err != nil {
		t.Fatal(err)
	}
	return plain + ".zst"
}

func writeCompressedRollout(t *testing.T, file, content string, modified time.Time) {
	t.Helper()
	// Codex writes one single-segment frame, which declares its content size.
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithSingleSegment(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, encoder.EncodeAll([]byte(content), nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func readCompressedRollout(t *testing.T, file string) string {
	t.Helper()
	content, err := (sourceFiles{}).ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	decoder, _ := zstd.NewReader(nil)
	defer decoder.Close()
	plain, err := decoder.DecodeAll(content, nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(plain)
}

func codexMessageCount(t *testing.T, c *Catalog, native string) (count int, origin string) {
	t.Helper()
	if err := c.DB.QueryRow(`SELECT COUNT(m.id),COALESCE(c.origin,'') FROM conversations c LEFT JOIN messages m ON m.conversation_id=c.id
		WHERE c.provider='codex' AND c.native_id=? GROUP BY c.id`, native).Scan(&count, &origin); err != nil {
		t.Fatalf("conversation %s: %v", native, err)
	}
	return count, origin
}

func TestCodexCompressedRolloutsAreIndexedCapturedAndProbed(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := t.TempDir()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	day := filepath.Join(root, "sessions", "2026", "09", "01")
	// Compressed only, named by session_index.jsonl, so naming reads its header.
	compressed := filepath.Join(day, "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, compressed, codexLines(probeID1, 2))
	compressed = compressRollout(t, compressed)
	writeSourceFile(t, filepath.Join(root, "session_index.jsonl"), `{"id":"`+probeID1+`","thread_name":"Cold thread","updated_at":"2026-09-01T12:00:00Z"}`+"\n")
	// Plain only.
	writeSourceFile(t, filepath.Join(day, "rollout-2026-09-01T01-00-00-"+probeID2+".jsonl"), codexLines(probeID2, 1))
	// Both forms, mid-resume: Codex reads the plain one, which has grown.
	both := filepath.Join(day, "rollout-2026-09-01T02-00-00-"+probeID3+".jsonl")
	writeSourceFile(t, both, codexLines(probeID3, 3))
	resumed, _ := os.Stat(both)
	writeCompressedRollout(t, both+".zst", codexLines(probeID3, 1), resumed.ModTime().Add(-time.Hour))
	// Archived and compressed.
	archived := filepath.Join(root, "archived_sessions", "rollout-2026-08-01T00-00-00-"+probeID4+".jsonl")
	writeSourceFile(t, archived, codexLines(probeID4, 1))
	compressRollout(t, archived)

	if result := ingestSource(t, c, source); result.Workspaces != 4 {
		t.Fatalf("ingest = %+v", result)
	}
	for native, want := range map[string]int{probeID1: 2, probeID2: 1, probeID3: 3, probeID4: 1} {
		if got, _ := codexMessageCount(t, c, native); got != want {
			t.Fatalf("%s messages = %d, want %d", native, got, want)
		}
	}
	if _, origin := codexMessageCount(t, c, probeID1); origin != plainRollout(compressed) {
		t.Fatalf("compressed origin = %q", origin)
	}
	if got := cachedWorkTitle(t, c, stableID("workspace", "codex", "local", probeID1)); got != "Cold thread" {
		t.Fatalf("compressed rollout title = %q", got)
	}
	if result := ingestSource(t, c, source); !result.SkippedUnchanged {
		t.Fatalf("unchanged compressed source = %+v", result)
	}

	// Probe counts each session once, compressed or not.
	home := probeHome(t)
	probePut(t, filepath.Join(home, ".codex", "sessions", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl"), "{}\n")
	probePut(t, filepath.Join(home, ".codex", "sessions", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl.zst"), "x")
	probePut(t, filepath.Join(home, ".codex", "archived_sessions", "rollout-2026-08-01T00-00-00-"+probeID2+".jsonl.zst"), "x")
	if probed := candidateNamed(t, probeSources(home, Config{}, nil), "codex"); probed.Count != 2 || probed.Files != 2 {
		t.Fatalf("probe = %+v", probed)
	}

	// Capture keeps the compressed file as it is, and only the form read.
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	rel := func(path string) string { r, _ := filepath.Rel(root, path); return "files/" + filepath.ToSlash(r) }
	if got, want := readText(t, capturedPath(config, "codex", rel(compressed))), readText(t, compressed); got != want {
		t.Fatal("compressed rollout was not captured verbatim")
	}
	if _, err := os.Stat(capturedPath(config, "codex", rel(both+".zst"))); !os.IsNotExist(err) {
		t.Fatalf("captured the compressed form beside its plain one: %v", err)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	for native, want := range map[string]int{probeID1: 2, probeID3: 3, probeID4: 1} {
		if got, origin := codexMessageCount(t, other, native); got != want || native == probeID1 && origin != plainRollout(compressed) {
			t.Fatalf("captured %s = %d messages from %q, want %d", native, got, origin, want)
		}
	}
}

// Codex compresses a rollout Pharos has already indexed and captured. The
// conversation is the same one, and an index of captures reads the newer,
// compressed copy rather than the plain one captured before it.
func TestCodexRolloutCompressedAfterCaptureKeepsOneConversation(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	root := t.TempDir()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	plain := filepath.Join(root, "sessions", "2026", "09", "01", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, plain, codexLines("thread", 1))
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	// Compression alone changes nothing Pharos knows the rollout by.
	compressed := compressRollout(t, plain)
	parses := countParses(t)
	if result := ingestSource(t, c, source); result.Error != nil || parses.Load() != 0 {
		t.Fatalf("compression alone parsed the rollout again: %+v, %d parses", result, parses.Load())
	}
	// Resumed, appended to and compressed again.
	writeSourceFile(t, plain, readCompressedRollout(t, compressed))
	os.Remove(compressed)
	appendSourceFile(t, plain, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"later"}}`+"\n")
	compressRollout(t, plain)
	if result := ingestSource(t, c, source); result.Workspaces != 1 {
		t.Fatalf("after compression = %+v", result)
	}
	if got, origin := codexMessageCount(t, c, "thread"); got != 2 || origin != plainRollout(compressed) {
		t.Fatalf("after compression: %d messages from %q", got, origin)
	}
	var conversations int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM conversations").Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("conversations = %d, %v", conversations, err)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got, origin := codexMessageCount(t, other, "thread"); got != 2 || origin != plainRollout(compressed) {
		t.Fatalf("captured: %d messages from %q", got, origin)
	}
}

// A compressed rollout that does not decode fails only itself: the other
// rollouts are indexed, and the broken one is read again next time.
func TestCodexCorruptCompressedRolloutFailsOnlyItsOwnGroup(t *testing.T) {
	c, _ := testCatalog(t)
	root := t.TempDir()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	day := filepath.Join(root, "sessions", "2026", "09", "01")
	writeSourceFile(t, filepath.Join(day, "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl.zst"), "not zstd at all")
	writeSourceFile(t, filepath.Join(day, "rollout-2026-09-01T01-00-00-"+probeID2+".jsonl"), codexLines("healthy", 1))
	// Named, so planning reads its header to match the name.
	writeSourceFile(t, filepath.Join(root, "session_index.jsonl"), `{"id":"`+probeID1+`","thread_name":"Broken","updated_at":"2026-09-01T12:00:00Z"}`+"\n")
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	result := c.Ingest(adapter, nil)
	if result.Error == nil || !strings.Contains(fmt.Sprint(result.Error), probeID1+".jsonl.zst") {
		t.Fatalf("corrupt rollout error = %v", result.Error)
	}
	if got, _ := codexMessageCount(t, c, "healthy"); got != 1 {
		t.Fatalf("healthy rollout messages = %d", got)
	}
	var buffer bytes.Buffer
	encoder, _ := zstd.NewWriter(&buffer)
	encoder.Write([]byte(codexLines("repaired", 1)))
	encoder.Close()
	writeSourceFile(t, filepath.Join(day, "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl.zst"), buffer.String())
	if result := ingestSource(t, c, source); result.Workspaces != 1 {
		t.Fatalf("after repair = %+v", result)
	}
	if got, _ := codexMessageCount(t, c, "repaired"); got != 1 {
		t.Fatalf("repaired rollout messages = %d", got)
	}
	if got := cachedWorkTitle(t, c, stableID("workspace", "codex", "local", "repaired")); got != "Codex repaired" {
		t.Fatalf("repaired rollout title = %q", got)
	}
}

func TestCodexCompressedForkPageKeepsLineage(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "rollout-2026-09-01T00-00-00-"+probeID1+"_"+probeID2+".jsonl")
	writeSourceFile(t, page, codexLines(probeID1, 1))
	page = compressRollout(t, page)
	record, ok, err := (&jsonlAdapter{provider: "codex"}).parse(page)
	if err != nil || !ok || record.SourceID != probeID2 || record.Conversations[0].ParentNativeID != probeID1 || len(record.Conversations[0].Messages) != 1 {
		t.Fatalf("compressed fork page: %#v, %v, %v", record, ok, err)
	}
}

// Verification freezes a rollout as it is on disk and parses the frozen copy;
// a compressed one must verify like a plain one.
func TestCodexCompressedRolloutPassesVerification(t *testing.T) {
	useHost(t, "host-a")
	root := t.TempDir()
	rollout := filepath.Join(root, "sessions", "2026", "09", "01", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, rollout, codexLines(probeID1, 2))
	compressed := compressRollout(t, rollout)
	// Its name is recorded against the compressed path; verification must
	// read the same naming row the index did.
	db, err := sql.Open("sqlite", filepath.Join(root, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, "CREATE TABLE threads(id TEXT,name TEXT,rollout_path TEXT);INSERT INTO threads VALUES('"+probeID1+"','Verified name','"+compressed+"'),('"+probeID2+"','Other thread','"+compressed+"')")
	db.Close()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	if got := cachedWorkTitle(t, server.Catalog, stableID("workspace", "codex", "local", probeID1)); got != "Verified name" {
		t.Fatalf("title = %q", got)
	}
	unit := firstSyncUnit(t, server.Catalog, source)
	if result := server.Catalog.auditUnit(t.Context(), unit, "manual", 0); result.Outcome != "passed" {
		t.Fatalf("compressed rollout verification = %+v", result)
	}
	if _, err := server.Catalog.DB.Exec("UPDATE messages SET text='damaged'"); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "manual", 0); result.Outcome != "mismatch" {
		t.Fatalf("damaged compressed rollout verification = %+v", result)
	}
}

// With capture before index, the compressed file is retained as it is and the
// index parses the retained copy.
func TestCodexCompressedRolloutRetainedBeforeIndex(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	rollout := filepath.Join(root, "sessions", "2026", "09", "01", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, rollout, codexLines(probeID1, 2))
	compressed := compressRollout(t, rollout)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
	if result := ingestSource(t, catalog, source); result.Workspaces != 1 {
		t.Fatalf("retained ingest = %+v", result)
	}
	if got, origin := codexMessageCount(t, catalog, probeID1); got != 2 || origin != plainRollout(compressed) {
		t.Fatalf("retained compressed rollout: %d messages from %q", got, origin)
	}
	if result := ingestSource(t, catalog, source); result.Parsed != 0 {
		t.Fatalf("unchanged retained rollout was parsed again: %+v", result)
	}
}

// A plain rollout captured before Codex appended to and compressed it is a
// superseded copy in every kind of retained input, including SQLite row
// replays, which restore both copies without file entries.
func TestCodexRecordReplayReadsCompressedRolloutOverStalePlainCopy(t *testing.T) {
	useHost(t, "host-a")
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	plain := filepath.Join(root, "sessions", "2026", "09", "01", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, plain, codexLines(probeID1, 1))
	db, err := sql.Open("sqlite", filepath.Join(root, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, "CREATE TABLE threads(id TEXT,name TEXT,rollout_path TEXT);INSERT INTO threads VALUES('"+probeID1+"','Replayed name','"+plain+".zst')")
	db.Close()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
	ingestSource(t, catalog, source)
	appendSourceFile(t, plain, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"later"}}`+"\n")
	compressRollout(t, plain)
	// Codex updates its state database while a thread is active.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "state_5.sqlite"), future, future); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, catalog, source)
	if got, origin := codexMessageCount(t, catalog, probeID1); got != 2 || origin != plain {
		t.Fatalf("replayed rollout: %d messages from %q", got, origin)
	}
	if got := cachedWorkTitle(t, catalog, stableID("workspace", "codex", "local", probeID1)); got != "Replayed name" {
		t.Fatalf("name recorded against the compressed path = %q", got)
	}
}

// A source configured as one compressed rollout keeps its original identity
// when indexed from its capture.
func TestCodexSingleCompressedRolloutSourceKeepsIdentityFromCapture(t *testing.T) {
	useHost(t, "host-a")
	c, config := testCatalog(t)
	plain := filepath.Join(t.TempDir(), "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, plain, codexLines(probeID1, 2))
	compressed := compressRollout(t, plain)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: compressed, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got, origin := codexMessageCount(t, other, probeID1); got != 2 || origin != plain {
		t.Fatalf("captured single rollout: %d messages from %q", got, origin)
	}
}

// A source configured as one rollout file follows it through compression and
// resume, live and in verification of its retained copy.
func TestCodexSingleRolloutSourceFollowsCompressionAndResume(t *testing.T) {
	useHost(t, "host-a")
	plain := filepath.Join(t.TempDir(), "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, plain, codexLines(probeID1, 1))
	source := SourceConfig{Name: "codex", Kind: "codex", Path: plain, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}
	server := automaticTestServer(t, source)
	ingestSource(t, server.Catalog, source)
	appendSourceFile(t, plain, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"later"}}`+"\n")
	compressed := compressRollout(t, plain)
	ingestSource(t, server.Catalog, source)
	if got, origin := codexMessageCount(t, server.Catalog, probeID1); got != 2 || origin != plain {
		t.Fatalf("after compression: %d messages from %q", got, origin)
	}
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	input, err := server.Catalog.acquireRetainedInput(t.Context(), adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	var unit syncUnit
	if err := input.Adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
		unit = unitOf(input.Adapter.Config(), parts, false)
		unit.Reader = input.Adapter
		return true
	}, nil); err != nil {
		t.Fatal(err)
	}
	if result := server.Catalog.auditUnit(t.Context(), unit, "manual", 0); result.Outcome != "passed" {
		t.Fatalf("retained compressed single rollout verification = %+v", result)
	}
	input.Close()
	// Resumed: the plain file comes back, newer, and the compressed one goes.
	writeSourceFile(t, plain, readCompressedRollout(t, compressed))
	os.Remove(compressed)
	appendSourceFile(t, plain, `{"type":"event_msg","timestamp":"2026-09-01T02:00:00Z","payload":{"type":"user_message","message":"resumed"}}`+"\n")
	ingestSource(t, server.Catalog, source)
	if got, _ := codexMessageCount(t, server.Catalog, probeID1); got != 3 {
		t.Fatalf("after resume: %d messages", got)
	}
}

// An ordinary capture of a source with SQLite naming records keeps the plain
// copy it took before compression; indexing that capture reads the newer,
// compressed rollout.
func TestCodexCaptureWithNamingRecordsReadsNewerCompressedRollout(t *testing.T) {
	useHost(t, "host-a")
	_, config := testCatalog(t)
	root := t.TempDir()
	plain := filepath.Join(root, "sessions", "2026", "09", "01", "rollout-2026-09-01T00-00-00-"+probeID1+".jsonl")
	writeSourceFile(t, plain, codexLines(probeID1, 1))
	state := filepath.Join(root, "state_5.sqlite")
	db, err := sql.Open("sqlite", state)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, "CREATE TABLE threads(id TEXT,name TEXT,rollout_path TEXT);INSERT INTO threads VALUES('"+probeID1+"','Captured name','"+plain+"')")
	db.Close()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
	config.Sources = []SourceConfig{source}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	appendSourceFile(t, plain, `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"later"}}`+"\n")
	compressRollout(t, plain)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(state, future, future); err != nil {
		t.Fatal(err)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatalf("capture = %+v", result)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if got, origin := codexMessageCount(t, other, probeID1); got != 2 || origin != plain {
		t.Fatalf("captured: %d messages from %q", got, origin)
	}
}
