package archive

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTeamsCodexTranscriptEvidenceCaptureAndInvalidation(t *testing.T) {
	useHost(t, "synthetic-host")
	c, cfg := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := strings.Replace(codexLines("thread-a", 1), `"cwd":"/proj"`, `"cwd":"/proj","model":"gpt-test"`, 1) +
		`{"type":"token_usage_record","timestamp":"2026-11-09T23:59:30Z","payload":{"response_id":"r1","usage":{"input_tokens":8,"cached_input_tokens":2,"output_tokens":2,"total_tokens":10},"thread_token_usage":{"total_tokens":10}}}` + "\n"
	writeSourceFile(t, file, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	if _, e := c.captureTeamsTranscript(t.Context(), cfg, source, file); e == nil {
		t.Fatal("backfill admitted before source proof was requested")
	}
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	settings, err := c.setTeamsLocalSettings(t.Context(), d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(t.Context(), d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	index := func() { runCapture(t, cfg); indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host") }
	assertProof := func(expected string) {
		t.Helper()
		_, digest, e := teamsCodexContentFile(source, file, "thread-a")
		if e != nil {
			t.Fatal(e)
		}
		var conversation, capture, transcript, review string
		if e = c.DB.QueryRow(`SELECT conversation_id,capture_sha256,transcript_sha256,review_input FROM teams_codex_transcript_evidence_v1`).Scan(&conversation, &capture, &transcript, &review); e != nil {
			t.Fatal(e)
		}
		actualReview, e := teamsManualReviewInput(t.Context(), c.DB, conversation)
		if e != nil || capture != teamsPayloadHash([]byte(expected)) || transcript != hex.EncodeToString(digest[:]) || review != actualReview || !strings.HasPrefix(review, teamsManualReviewPrefix) || !validRecordHash(strings.TrimPrefix(review, teamsManualReviewPrefix)) {
			t.Fatalf("capture transcript proof mismatch: %v", e)
		}
	}
	index()
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_transcript_evidence_v1`); n != 0 {
		t.Fatal("ordinary capture unexpectedly created Teams evidence")
	}
	var ordinarySignal string
	if err = c.DB.QueryRow(`SELECT COALESCE(signal,'') FROM source_item_states WHERE item=?`, file).Scan(&ordinarySignal); err != nil || strings.Contains(ordinarySignal, "teams-capture-v1:") {
		t.Fatalf("unstamped file acquired a Teams reindex signal: %v", err)
	}
	if _, err = c.captureTeamsTranscript(t.Context(), cfg, source, file); err != nil {
		t.Fatal(err)
	}
	if retry, e := c.captureTeamsTranscript(t.Context(), cfg, source, file); e != nil || retry.FilesUnchanged != 1 {
		t.Fatalf("retry after capture before indexing: %+v %v", retry, e)
	}
	// A compression interrupted after its manifest update can leave the
	// original plaintext inode in place. Verify it without copying again.
	interrupted := readCaptureManifest(t, cfg, "codex")
	interrupted.Files[0].Encoding = captureEncodingZstd
	interrupted.Files[0].StoredSize = interrupted.Files[0].Size - 1
	interruptedJSON, err := json.Marshal(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cfg.CaptureRoot, "synthetic-host", "codex", captureManifestName), interruptedJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if retry, e := c.captureTeamsTranscript(t.Context(), cfg, source, file); e != nil || retry.FilesUnchanged != 1 || retry.FilesCopied != 0 {
		t.Fatalf("interrupted compression forced a copy: %+v %v", retry, e)
	}
	initial := manifestFile(t, readCaptureManifest(t, cfg, "codex"), "files/sessions/rollout.jsonl")
	retained := capturedPath(cfg, "codex", initial.Captured)
	if err = os.WriteFile(retained, []byte(strings.Replace(data, "message 0", "message x", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	retainedTime := time.Unix(0, initial.MTimeNS)
	if err = os.Chtimes(retained, retainedTime, retainedTime); err != nil {
		t.Fatal(err)
	}
	if retry, e := c.captureTeamsTranscript(t.Context(), cfg, source, file); e != nil || retry.FilesCopied != 1 {
		t.Fatalf("corrupt same-size retained image was not recopied: %+v %v", retry, e)
	}
	targets, err := captureTargets(cfg.CaptureRoot, []string{"synthetic-host"}, false, nil)
	if err != nil || len(targets) != 1 {
		t.Fatalf("capture targets: %v", err)
	}
	_, _, _, stalePlan, ok := c.captureIndexPlan(t.Context(), targets[0])
	if !ok || stalePlan == nil {
		t.Fatal("automatic capture plan unavailable")
	}
	res := c.IndexCapture(withIngestMode(t.Context(), ingestMode{automatic: true, plan: stalePlan}), targets[0], nil)
	if res.Error != nil {
		t.Fatalf("automatic sync of recapture: %v", res.Error)
	}
	assertProof(data)
	var before, after int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil || after != before {
		t.Fatalf("idle reindex changed source revision %d -> %d: %v", before, after, err)
	}
	// An attachment must recheck both its sealed options and the proof flag
	// under the writer, even if an earlier plan observed an enabled source.
	sealed := readCaptureManifest(t, cfg, "codex")
	selected := *sealed.Files[0]
	selected.teamsTranscript = &teamsRetainedTranscript{Digest: strings.Repeat("a", 64)}
	view := newCaptureView(currentHost(), filepath.Join(cfg.CaptureRoot, "synthetic-host", "codex"), &sealed)
	unknown := selected.version()
	unknown.Encoding = "future-unsupported"
	if _, e := teamsBoundedRetainedDigest(t.Context(), view.dir, filepath.Join(view.dir, filepath.FromSlash(selected.Captured)), unknown); e == nil {
		t.Fatal("unknown retained encoding admitted")
	}
	sealed.Files[0].Encoding = "future-unsupported"
	if f, e := teamsOpenBoundedCapture(view, selected.Captured); e == nil {
		f.Close()
		t.Fatal("unknown capture encoding admitted")
	}
	sealed.Files[0].Encoding = ""
	var version int64
	if err = c.DB.QueryRow(`SELECT version FROM source_item_states WHERE item=?`, file).Scan(&version); err != nil {
		t.Fatal(err)
	}
	record := WorkspaceRecord{Conversations: []ConversationRecord{{Origin: file, NativeID: "thread-a", Provider: "codex", Account: "local"}}}
	parts := []sourcePart{{item: file, version: version}}
	var priorCustody string
	if err = c.DB.QueryRow(`SELECT capture_revision FROM teams_codex_capture_custody`).Scan(&priorCustody); err != nil {
		t.Fatal(err)
	}
	checkNoMutation := func() {
		t.Helper()
		var custody string
		var rev int64
		if e := c.DB.QueryRow(`SELECT capture_revision FROM teams_codex_capture_custody`).Scan(&custody); e != nil {
			t.Fatal(e)
		}
		if e := c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&rev); e != nil || custody != priorCustody || rev != before {
			t.Fatalf("attachment mutated denied state: %v", e)
		}
	}
	sealed.Source.TeamsOptionsSHA256 = strings.Repeat("f", 64)
	if attached, e := c.teamsAttachUnchangedTranscript(t.Context(), "synthetic-host", source, view, record, map[string]*capturedFile{file: &selected}, &partTracker{}, parts); e != nil || attached {
		t.Fatalf("changed options attached: %v %v", attached, e)
	}
	checkNoMutation()
	sealed.Source.TeamsOptionsSHA256, err = teamsSourceOptionsFingerprint(source.Options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE teams_source_revision SET proof_requested=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if attached, e := c.teamsAttachUnchangedTranscript(t.Context(), "synthetic-host", source, view, record, map[string]*capturedFile{file: &selected}, &partTracker{}, parts); e != nil || attached {
		t.Fatalf("revoked proof attached: %v %v", attached, e)
	}
	checkNoMutation()
	if _, err = c.DB.Exec(`UPDATE teams_source_revision SET proof_requested=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, e := c.captureTeamsTranscript(t.Context(), cfg, source, file+"-other"); e == nil {
		t.Fatal("backfill admitted an unbound origin")
	}
	wrongSource := source
	wrongSource.Options = map[string]any{"synthetic": true}
	if _, e := c.captureTeamsTranscript(t.Context(), cfg, wrongSource, file); e == nil {
		t.Fatal("backfill admitted different source options")
	}
	wrongConfig := cfg
	wrongConfig.Sources = []SourceConfig{wrongSource}
	if _, e := c.captureTeamsTranscript(t.Context(), wrongConfig, wrongSource, file); e == nil {
		t.Fatal("backfill admitted changed configured provenance")
	}
	disabled := cfg
	disabled.Sources = append([]SourceConfig(nil), cfg.Sources...)
	disabled.Sources[0].Enabled = false
	if _, e := c.captureTeamsTranscript(t.Context(), disabled, source, file); e == nil {
		t.Fatal("backfill admitted disabled source")
	}
	manifest := readCaptureManifest(t, cfg, "codex")
	if len(manifest.Files) != 1 || manifest.Files[0].TeamsAcquisitionRoot == nil {
		t.Fatal("capture omitted source-root identity")
	}
	manifest.Files[0].TeamsAcquisitionRoot = nil
	legacy, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cfg.CaptureRoot, "synthetic-host", "codex", captureManifestName), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if got := captureResult(t, runCapture(t, cfg), "codex"); got.FilesCopied != 0 {
		t.Fatalf("ordinary legacy capture unexpectedly recopied: %+v", got)
	}
	if got, e := c.captureTeamsTranscript(t.Context(), cfg, source, file); e != nil || got.FilesCopied != 1 {
		t.Fatalf("proof-enabled bounded recapture failed: %+v %v", got, e)
	}
	for _, mutation := range []string{
		`UPDATE messages SET selected=1-selected WHERE id=(SELECT id FROM messages LIMIT 1)`,
		`UPDATE messages SET raw_text='synthetic change' WHERE id=(SELECT id FROM messages LIMIT 1)`,
		`DELETE FROM messages WHERE id=(SELECT id FROM messages LIMIT 1)`,
		`UPDATE conversations SET native_id=native_id || '-changed' WHERE id=(SELECT conversation_id FROM teams_codex_transcript_evidence_v1 LIMIT 1)`,
		`UPDATE teams_codex_capture_custody SET captured_at='synthetic-change'`,
		`DELETE FROM teams_codex_capture_custody`,
	} {
		tx, e := c.DB.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(mutation); e != nil {
			t.Fatal(e)
		}
		var remaining int
		if e = tx.QueryRow(`SELECT count(*) FROM teams_codex_transcript_evidence_v1`).Scan(&remaining); e != nil || remaining != 0 {
			t.Fatalf("mutation %s retained %d proofs: %v", mutation, remaining, e)
		}
		if e = tx.Rollback(); e != nil {
			t.Fatal(e)
		}
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(data, "message 0", "message 1", 1)
	writeSourceFile(t, file, changed)
	if err = os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.captureTeamsTranscript(t.Context(), cfg, source, file); err != nil {
		t.Fatal(err)
	}
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_transcript_evidence_v1`); n != 0 {
		t.Fatalf("ambiguous same-time rewrite retained %d proofs", n)
	}
	// Equal-time conflicting observations cannot replace the indexed record.
	// Once the source has a newer timestamp, ordinary ingestion can re-prove it.
	newer := info.ModTime().Add(time.Second)
	if err = os.Chtimes(file, newer, newer); err != nil {
		t.Fatal(err)
	}
	index()
	if _, err = c.captureTeamsTranscript(t.Context(), cfg, source, file); err != nil {
		t.Fatal(err)
	}
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	assertProof(changed)
	if _, err = c.DB.Exec(`UPDATE messages SET text=text || ' changed' WHERE id=(SELECT id FROM messages LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_transcript_evidence_v1`); n != 0 {
		t.Fatalf("message edit retained %d proofs", n)
	}
	appendSourceFile(t, file, "\n")
	appends := 0
	override(t, &captureFault, func(stage, rel string) error {
		if stage != "copy-read" {
			return nil
		}
		appends++
		f, e := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0)
		if e != nil {
			return e
		}
		_, e = f.WriteString("\n")
		if closeErr := f.Close(); e == nil {
			e = closeErr
		}
		return e
	})
	if _, e := c.captureTeamsTranscript(t.Context(), cfg, source, file); e == nil || appends != 2 {
		t.Fatalf("unstable copied images reported success: attempts=%d err=%v", appends, e)
	}
}

func TestTeamsUnchangedAttachmentVersionsCustodyWithoutReview(t *testing.T) {
	useHost(t, "synthetic-host")
	c, cfg := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := strings.Replace(codexLines("thread-a", 1), `"cwd":"/proj"`, `"cwd":"/proj","model":"gpt-test"`, 1) +
		`{"type":"token_usage_record","timestamp":"2026-11-09T23:59:30Z","payload":{"response_id":"r1","usage":{"input_tokens":8,"cached_input_tokens":2,"output_tokens":2,"total_tokens":10},"thread_token_usage":{"total_tokens":10}}}` + "\n"
	writeSourceFile(t, file, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	settings, err := c.setTeamsLocalSettings(t.Context(), d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(t.Context(), d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	runCapture(t, cfg)
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	if _, err = c.captureTeamsTranscript(t.Context(), cfg, source, file); err != nil {
		t.Fatal(err)
	}
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_transcript_evidence_v1`); n != 1 {
		t.Fatalf("initial evidence count: %d", n)
	}
	// No indexed messages makes manual review unavailable, while an empty
	// parsed transcript still matches the indexed content.
	for _, q := range []string{
		`CREATE TEMP TABLE saved_transcript_evidence AS SELECT * FROM teams_codex_transcript_evidence_v1`,
		`CREATE TEMP TABLE saved_native_proofs AS SELECT * FROM teams_native_source_proofs`,
		`CREATE TEMP TABLE saved_profiles AS SELECT * FROM teams_codex_request_profiles`,
		`CREATE TEMP TABLE saved_custody AS SELECT * FROM teams_codex_capture_custody`,
		`DELETE FROM messages`,
		`INSERT OR IGNORE INTO teams_native_source_proofs SELECT * FROM saved_native_proofs`,
		`INSERT OR IGNORE INTO teams_codex_request_profiles SELECT * FROM saved_profiles`,
		`INSERT OR IGNORE INTO teams_codex_capture_custody SELECT * FROM saved_custody`,
	} {
		if _, err = c.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	sealed := readCaptureManifest(t, cfg, "codex")
	selected := *sealed.Files[0]
	selected.teamsTranscript = &teamsRetainedTranscript{Digest: strings.Repeat("a", 64)}
	view := newCaptureView(currentHost(), filepath.Join(cfg.CaptureRoot, "synthetic-host", "codex"), &sealed)
	record := WorkspaceRecord{EvidenceIdentity: strings.Repeat("b", 64), Conversations: []ConversationRecord{{Origin: file, NativeID: "thread-a", Provider: "codex", Account: "local"}}}
	var version int64
	if err = c.DB.QueryRow(`SELECT version FROM source_item_states WHERE item=?`, file).Scan(&version); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			// Simulate a prior row becoming invalid during this attachment; it
			// must remain deleted rather than being restored by rollback.
			if _, err = c.DB.Exec(`INSERT INTO teams_codex_transcript_evidence_v1 SELECT * FROM saved_transcript_evidence`); err != nil {
				t.Fatal(err)
			}
		}
		var before int64
		if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		view.evidenceRevision = strings.Repeat(string(rune('c'+i)), 64)
		attached, e := c.teamsAttachUnchangedTranscript(t.Context(), "synthetic-host", source, view, record, map[string]*capturedFile{file: &selected}, &partTracker{}, []sourcePart{{item: file, version: version}})
		if e != nil || attached {
			t.Fatalf("unexpected attachment: %v %v", attached, e)
		}
		var after int64
		var custody string
		if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if err = c.DB.QueryRow(`SELECT capture_revision FROM teams_codex_capture_custody`).Scan(&custody); err != nil {
			t.Fatal(err)
		}
		if custody != view.evidenceRevision || after <= before {
			t.Fatalf("unversioned custody change: revision %d -> %d", before, after)
		}
		if n := countRows(t, c, `SELECT count(*) FROM teams_codex_transcript_evidence_v1`); n != 0 {
			t.Fatalf("unavailable review retained evidence: %d", n)
		}
	}
}

func TestTeamsCodexAcquisitionConfinesPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	file := filepath.Join(root, "nested", "transcript.jsonl")
	writeSourceFile(t, file, "synthetic\n")
	f, _, err := teamsOpenCodexAcquisition(root, file)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	rootAlias := root + "-alias"
	if err = os.Symlink(root, rootAlias); err != nil {
		t.Fatal(err)
	}
	if alias, e := exportOpenAcquisitionRoot(rootAlias); e == nil {
		alias.Close()
		t.Fatal("acquisition followed final root symlink")
	}
	// Existing export intentionally accepts a named root alias.
	if alias, e := exportOpenRoot(rootAlias); e != nil {
		t.Fatal(e)
	} else {
		alias.Close()
	}
	parentAlias := filepath.Join(filepath.Dir(root), "parent-alias")
	if err = os.Symlink(filepath.Dir(root), parentAlias); err != nil {
		t.Fatal(err)
	}
	if alias, _, e := teamsOpenCodexAcquisition(filepath.Join(parentAlias, "root"), filepath.Join(parentAlias, "root", "nested", "transcript.jsonl")); e != nil {
		t.Fatal(e)
	} else {
		alias.Close()
	}
	if _, _, err = teamsOpenCodexAcquisition(root, filepath.Join(root, "..", "outside")); err == nil {
		t.Fatal("accepted outside file")
	}
	link := filepath.Join(root, "alias")
	if err = os.Symlink(filepath.Join(root, "nested"), link); err != nil {
		t.Fatal(err)
	}
	if f, _, err = teamsOpenCodexAcquisition(root, filepath.Join(link, "transcript.jsonl")); err == nil {
		f.Close()
		t.Fatal("accepted symlink component")
	}
	if f, _, err = teamsOpenCodexAcquisition(root, root+"/nested/../nested/transcript.jsonl"); err == nil {
		f.Close()
		t.Fatal("accepted ambiguous path")
	}
	stamp, oldRoot, err := teamsAcquisitionStamp(root, file)
	if err != nil {
		t.Fatal(err)
	}
	old := root + "-old"
	if err = os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(old, "nested", "transcript.jsonl"), file); err != nil {
		t.Fatal(err)
	}
	newStamp, newRoot, err := teamsAcquisitionStamp(root, file)
	if err != nil {
		t.Fatal(err)
	}
	if newStamp.Device != stamp.Device || newStamp.Inode != stamp.Inode || (oldRoot.Dev == newRoot.Dev && oldRoot.Ino == newRoot.Ino) {
		t.Fatal("root replacement did not retain separate identity")
	}
}

func TestTeamsCodexCaptureKeepsPrefixWhenSourceAppends(t *testing.T) {
	useHost(t, "synthetic-host")
	_, cfg := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	prefix := codexLines("thread-a", 1)
	appended := `{"type":"event_msg","payload":{"type":"user_message","message":"later"}}` + "\n"
	writeSourceFile(t, file, prefix)
	cfg.Sources = []SourceConfig{{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}}
	didAppend := false
	override(t, &captureFault, func(stage, rel string) error {
		if stage == "copy-read" && !didAppend {
			didAppend = true
			f, e := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
			if e != nil {
				return e
			}
			_, e = f.WriteString(appended)
			if closeErr := f.Close(); e == nil {
				e = closeErr
			}
			return e
		}
		return nil
	})
	ctx := context.WithValue(t.Context(), teamsProofRecaptureKey{}, file)
	summary, err := Capture(ctx, cfg, cfg.Sources, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := captureResult(t, summary, "codex")
	entry := manifestFile(t, readCaptureManifest(t, cfg, "codex"), "files/sessions/rollout.jsonl")
	if !didAppend || result.FilesCopied != 1 || entry.Size != int64(len(prefix)) || entry.SHA256 != teamsPayloadHash([]byte(prefix)) || entry.TeamsAcquisition != nil || entry.TeamsAcquisitionRoot != nil {
		t.Fatalf("unstable copy did not retain a provisional prefix: %+v %+v", result, entry)
	}
	if got := readText(t, capturedPath(cfg, "codex", entry.Captured)); got != prefix {
		t.Fatalf("captured bytes differ from prefix: %q", got)
	}
	summary, err = Capture(ctx, cfg, cfg.Sources, nil)
	if err != nil {
		t.Fatal(err)
	}
	result = captureResult(t, summary, "codex")
	entry = manifestFile(t, readCaptureManifest(t, cfg, "codex"), "files/sessions/rollout.jsonl")
	if result.FilesCopied != 1 || entry.TeamsAcquisition == nil || entry.TeamsAcquisitionRoot == nil || readText(t, capturedPath(cfg, "codex", entry.Captured)) != prefix+appended {
		t.Fatal("next capture did not acquire the complete stable file")
	}
}

func TestTeamsCodexCaptureSignalStopsAfterOptOut(t *testing.T) {
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	writeSourceFile(t, file, codexLines("thread-a", 1))
	a := &jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}}, provider: "codex", teamsCaptureHashes: map[string]string{file: strings.Repeat("a", 64)}}
	if got := a.teamsCaptureSignal(file, "legacy"); got != "legacy" {
		t.Fatalf("disabled signal changed: %q", got)
	}
	if _, err := a.planParts(); err != nil {
		t.Fatal(err)
	}
	if a.teamsCaptureHashes != nil {
		t.Fatal("disabled plan retained prior Teams signals")
	}
}
