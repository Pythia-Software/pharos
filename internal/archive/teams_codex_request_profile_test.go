package archive

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestTeamsCodexRequestProfileProof(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"thread-a","model":"gpt-test"}}` + "\n"
	req := func(id, stamp string, input, cached, output, reasoning, total, running int) string {
		return fmt.Sprintf(`{"type":"token_usage_record","timestamp":%q,"payload":{"response_id":%q,"usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d},"thread_token_usage":{"total_tokens":%d}}}`+"\n", stamp, id, input, cached, output, reasoning, total, running)
	}
	first := req("r1", "2026-11-09T23:59:30Z", 8, 2, 2, 1, 10, 10)
	second := req("r2", "2026-11-10T00:00:30Z", 15, 5, 5, 2, 20, 30)
	counter := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"total_tokens":10}}}}` + "\n"
	parse := func(data string) *teamsCodexRequestProof {
		var quality teamsJSONLQuality
		events, err := readJSONLinesObserved(strings.NewReader(data), int64(len(data)), quality.observe)
		if err != nil {
			t.Fatal(err)
		}
		return teamsCodexRequestProfile(teamsCodexNativeProof(events, "thread-a", quality), events, "")
	}
	valid := header + first + counter + `{"type":"turn_context","payload":{"model":"gpt-other"}}` + "\n" + second
	proof := parse(valid)
	if proof == nil || len(proof.Facts) != 2 || proof.Facts[0].Total != 10 || proof.Facts[1].Total != 20 || proof.Facts[1].Model != "gpt-other" || len(proof.Digest) != 64 {
		t.Fatalf("valid proof: %+v", proof)
	}
	for _, tc := range []struct{ name, data string }{
		{"gap", header + first + "bad-json\n" + second},
		{"invalid-utf8", header + strings.Replace(first, "r1", "r\xff", 1)},
		{"numeric-id", header + strings.Replace(first, `"response_id":"r1"`, `"response_id":12`, 1)},
		{"conflicting-model", header + strings.Replace(first, `"response_id":"r1"`, `"response_id":"r1","model":"other"`, 1)},
		{"conflicting-effort", header + strings.Replace(first, `"response_id":"r1"`, `"response_id":"r1","effort":"high"`, 1)},
		{"overflow", header + strings.Replace(first, `"total_tokens":10`, `"total_tokens":9007199254740992`, 1)},
		{"unproved-model-transition", header + first + `{"type":"turn_context","payload":{}}` + "\n" + second},
		{"partial", header + first + `{"type":`},
		{"inherited", strings.Replace(header, `"id":"thread-a"`, `"id":"thread-a","forked_from_id":"parent"`, 1) + first},
		{"missing-model", strings.Replace(header, `,"model":"gpt-test"`, "", 1) + first},
		{"missing-time", header + strings.Replace(first, `"2026-11-09T23:59:30Z"`, `""`, 1)},
		{"fractional", header + strings.Replace(first, `"input_tokens":8`, `"input_tokens":8.5`, 1)},
		{"contradictory-snapshot", header + first + strings.Replace(counter, `"total_tokens":10`, `"total_tokens":11`, 1)},
		{"skipped-prefix", header + strings.Replace(second, `"total_tokens":30`, `"total_tokens":40`, 1)},
		{"conflicting-duplicate", header + first + strings.Replace(first, `"input_tokens":8`, `"input_tokens":9`, 1)},
		{"unsupported-other-usage", header + first + `{"type":"turn.completed","usage":{"total_tokens":10}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parse(tc.data); got != nil {
				t.Fatalf("accepted unproved sequence: %+v", got)
			}
		})
	}
	snapshot := strings.Replace(counter, `"last_token_usage":{"total_tokens":10}`, `"last_token_usage":{"total_tokens":10},"latest_token_usage_record":{"response_id":"r1","usage":{"total_tokens":10},"thread_token_usage":{"total_tokens":10}}`, 1)
	if p := parse(header + first + snapshot); p == nil || len(p.Facts) != 1 {
		t.Fatal("matching modern snapshot was not deduplicated")
	}
	if p := parse(header + first + strings.Replace(snapshot, `"response_id":"r1"`, `"response_id":"wrong"`, 1)); p != nil {
		t.Fatal("mismatched modern snapshot accepted")
	}
	unknown := parse(header + strings.Replace(first, `"cached_input_tokens":2,`, "", 1))
	if unknown == nil || unknown.Facts[0].Cached.Known {
		t.Fatal("missing cached count was not preserved as unknown")
	}
	duplicate := parse(header + first + counter + first)
	if duplicate == nil || len(duplicate.Facts) != 1 || len(duplicate.Occurrences) != 2 {
		t.Fatalf("exact duplicate not deduplicated: %+v", duplicate)
	}
}

func TestTeamsCodexRequestProfileIndexedParityAndInvalidation(t *testing.T) {
	useHost(t, "synthetic-host")
	c, _ := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"thread-a","model":"gpt-test"}}` + "\n" +
		`{"type":"token_usage_record","timestamp":"2026-11-09T23:59:30Z","payload":{"response_id":"r1","usage":{"input_tokens":8,"cached_input_tokens":2,"output_tokens":2,"reasoning_output_tokens":1,"total_tokens":10},"thread_token_usage":{"total_tokens":10}}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"gpt-other"}}` + "\n" +
		`{"type":"token_usage_record","timestamp":"2026-11-10T00:00:30Z","payload":{"response_id":"r2","usage":{"input_tokens":15,"cached_input_tokens":5,"output_tokens":5,"reasoning_output_tokens":2,"total_tokens":20},"thread_token_usage":{"total_tokens":30}}}` + "\n"
	writeSourceFile(t, file, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	settings, err := c.setTeamsLocalSettings(t.Context(), d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(t.Context(), d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	ingestSource(t, c, source)
	var factsJSON, digest string
	if err = c.DB.QueryRow(`SELECT facts_json,evidence_sha256 FROM teams_codex_request_profiles`).Scan(&factsJSON, &digest); err != nil {
		t.Fatal(err)
	}
	var facts []teamsCodexRequestFact
	if err = json.Unmarshal([]byte(factsJSON), &facts); err != nil || len(facts) != 2 || facts[0].Total != 10 || facts[1].Total != 20 || facts[1].Model != "gpt-other" || len(digest) != 64 {
		t.Fatalf("indexed proof: %+v %v", facts, err)
	}
	// The ledger remains in the same hour, but a changed indexed request time
	// must fail proof parity before any aggregate can authorize it.
	var q teamsJSONLQuality
	events, e := readJSONLinesObserved(strings.NewReader(data), int64(len(data)), q.observe)
	if e != nil {
		t.Fatal(e)
	}
	proof := teamsCodexRequestProfile(teamsCodexNativeProof(events, "thread-a", q), events, "")
	if proof == nil {
		t.Fatal("missing raw proof")
	}
	var conversation string
	if e = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id='thread-a'`).Scan(&conversation); e != nil {
		t.Fatal(e)
	}
	tx, e := c.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE messages SET created_at='2026-11-09T23:59:31Z' WHERE raw_text LIKE '%"response_id":"r1"%'`); e != nil {
		t.Fatal(e)
	}
	if ok, e := teamsRequestIndexParity(tx, conversation, proof); e != nil || ok {
		t.Fatalf("same-hour timestamp drift passed parity: %v", e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	var before, after int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE messages SET model=model WHERE raw_text LIKE '%"response_id":"r1"%'`); err != nil {
		t.Fatal(err)
	}
	var afterNoop int64
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&afterNoop); err != nil || afterNoop != before || countRows(t, c, `SELECT count(*) FROM teams_codex_request_profiles`) != 1 {
		t.Fatal("no-op metadata update invalidated profile")
	}
	if _, err = c.DB.Exec(`UPDATE messages SET model='forged' WHERE raw_text LIKE '%"response_id":"r1"%'`); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_request_profiles`); n != 0 || after <= before {
		t.Fatalf("message change retained profile n=%d rev=%d/%d", n, before, after)
	}
	// Reindexing a changed request time keeps the v1 total unchanged but must
	// produce a different full request-sequence commitment.
	changedData := strings.Replace(data, "23:59:30Z", "23:59:31Z", 1)
	writeSourceFile(t, file, changedData)
	ingestSource(t, c, source)
	var changedDigest string
	if e = c.DB.QueryRow(`SELECT evidence_sha256 FROM teams_codex_request_profiles`).Scan(&changedDigest); e != nil || changedDigest == digest {
		t.Fatalf("request-time rewrite not reflected in proof: %v", e)
	}
	// A new capture/index pass must not certify an unsupported or gapped stream.
	appendSourceFile(t, file, `not-json`+"\n")
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_request_profiles`); n != 0 {
		t.Fatal("capture gap retained a profile")
	}
	writeSourceFile(t, file, changedData)
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_request_profiles`); n != 1 {
		t.Fatal("valid source not recertified")
	}
	writeSourceFile(t, file, "not-json\n")
	ingestSource(t, c, source)
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_request_profiles`); n != 0 {
		t.Fatal("unparseable changed source retained profile")
	}
}

func TestTeamsCodexProfileAnchorFailsClosed(t *testing.T) {
	key := make([]byte, 32)
	account, _ := teams.SourceWorkNamespace(key, "codex")
	anchorToken, _ := teams.SourceToken(key, "codex", account, "thread")
	anchorWitness, _ := teams.SourceWitness(key, "codex", "codex_thread", "thread")
	anchor := sourcewire.Item{Kind: "codex_thread", Token: anchorToken, Witness: anchorWitness, Protection: "unknown", Profile: &sourcewire.Profile{ID: "codex_thread_usage", Version: 1}}
	child, err := teamsCodexProfileItem(key, anchor, strings.Repeat("a", 64))
	if err != nil || child.Token == anchorToken || len(child.Dependencies) != 1 || child.Dependencies[0] != anchorToken || child.Validate() != nil {
		t.Fatalf("anchor: %+v %v", child, err)
	}
	anchor.Protection = "unavailable"
	if _, err = teamsCodexProfileItem(key, anchor, strings.Repeat("a", 64)); err == nil {
		t.Fatal("unavailable anchor accepted")
	}
	if teamsRequireTypedUsageEligibility() == nil {
		t.Fatal("typed eligibility enabled without server proof")
	}
}

func TestTeamsCodexRequestProfileRetainedCaptureParity(t *testing.T) {
	useHost(t, "synthetic-host")
	c, cfg := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"thread-a","model":"gpt-test"}}` + "\n" +
		`{"type":"token_usage_record","timestamp":"2026-11-09T23:59:30Z","payload":{"response_id":"r1","usage":{"input_tokens":8,"cached_input_tokens":2,"output_tokens":2,"reasoning_output_tokens":1,"total_tokens":10},"thread_token_usage":{"total_tokens":10}}}` + "\n"
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
	var capturedFacts, capturedDigest string
	if err = c.DB.QueryRow(`SELECT facts_json,evidence_sha256 FROM teams_codex_request_profiles`).Scan(&capturedFacts, &capturedDigest); err != nil {
		t.Fatal(err)
	}
	var custodyHash, capturedAt, custodyEvidence string
	if err = c.DB.QueryRow(`SELECT capture_sha256,captured_at,evidence_sha256 FROM teams_codex_capture_custody`).Scan(&custodyHash, &capturedAt, &custodyEvidence); err != nil || custodyHash != teamsPayloadHash([]byte(data)) || capturedAt == "" || custodyEvidence != capturedDigest {
		t.Fatalf("capture custody mismatch: hash=%q time=%q evidence=%q err=%v", custodyHash, capturedAt, custodyEvidence, err)
	}
	var binding, conversation, origin, indexed, provenance string
	if err = c.DB.QueryRow(`SELECT binding_id,conversation_id,origin,indexed_digest,provenance_sha256 FROM teams_codex_capture_custody`).Scan(&binding, &conversation, &origin, &indexed, &provenance); err != nil {
		t.Fatal(err)
	}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	custody, err := teamsReadCodexCaptureCustody(tx, binding, conversation, origin)
	if err != nil || custody.CaptureSHA256 != custodyHash || custody.EvidenceSHA256 != capturedDigest {
		t.Fatalf("current metadata preflight: %+v %v", custody, err)
	}
	if _, err = teamsReadCodexCaptureCustody(tx, binding, conversation, origin+"-other"); err != errTeamsSourceUnavailable {
		t.Fatalf("foreign origin admitted: %v", err)
	}
	changed, err := teamsPersistCodexCaptureCustody(tx, nil, WorkspaceRecord{}, nil, binding, conversation, origin, indexed, provenance)
	if err != nil || !changed {
		t.Fatalf("missing capture did not invalidate custody: %v %v", changed, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_capture_custody`); n != 0 {
		t.Fatalf("stale custody remains: %d", n)
	}
	// A new trusted capture of the same parsed facts gets a new custody
	// commitment even when the profile's fact digest is unchanged.
	writeSourceFile(t, file, data+"\n")
	runCapture(t, cfg)
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	if err = c.DB.QueryRow(`SELECT capture_sha256,evidence_sha256 FROM teams_codex_capture_custody`).Scan(&custodyHash, &custodyEvidence); err != nil || custodyHash != teamsPayloadHash([]byte(data+"\n")) || custodyEvidence != capturedDigest {
		t.Fatalf("rewritten capture custody mismatch: hash=%q evidence=%q err=%v", custodyHash, custodyEvidence, err)
	}
	// Index the same native source: its proof must be the same irrespective of
	// whether the adapter consumed retained capture bytes or the live source.
	ingestSource(t, c, source)
	var same int
	if err = c.DB.QueryRow(`SELECT count(*) FROM teams_codex_request_profiles WHERE facts_json=? AND evidence_sha256=?`, capturedFacts, capturedDigest).Scan(&same); err != nil || same != 1 {
		t.Fatalf("capture/live proof mismatch: %d %v", same, err)
	}
	if _, err = c.DB.Exec(`DELETE FROM teams_codex_request_profiles`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_codex_capture_custody`); n != 0 {
		t.Fatalf("profile deletion retained custody: %d", n)
	}
}

func TestTeamsCodexRequestProfileUpgradeSchedulesOptedInSourcesOnce(t *testing.T) {
	useHost(t, "synthetic-host")
	c, _ := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("thread-a", 1))
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	settings, err := c.setTeamsLocalSettings(t.Context(), d.ID, d.Instance, "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), true, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(t.Context(), d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	var before, after, again int64
	if err = c.DB.QueryRow(`SELECT generation FROM sync_generations WHERE host_id='synthetic-host' AND source_name='codex'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`DELETE FROM meta WHERE key IN ('teams_codex_request_profile_reindex','teams_codex_capture_custody_reindex')`); err != nil {
		t.Fatal(err)
	}
	if err = c.ensureTeamsCodexRequestSchema(); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT generation FROM sync_generations WHERE host_id='synthetic-host' AND source_name='codex'`).Scan(&after); err != nil || after != before+1 {
		t.Fatalf("upgrade did not schedule proof reindex %d/%d %v", before, after, err)
	}
	if err = c.ensureTeamsCodexRequestSchema(); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT generation FROM sync_generations WHERE host_id='synthetic-host' AND source_name='codex'`).Scan(&again); err != nil || again != after {
		t.Fatalf("upgrade reindex repeated %d/%d %v", after, again, err)
	}
}

func TestTeamsCodexCaptureRequiresMatchingBytes(t *testing.T) {
	dir := t.TempDir()
	const origin = "/synthetic/rollout.jsonl"
	const data = "synthetic capture"
	writeSourceFile(t, filepath.Join(dir, "files", "rollout.jsonl"), data)
	f := &capturedFile{Path: origin, Captured: "files/rollout.jsonl", Size: int64(len(data)), SHA256: teamsPayloadHash([]byte(data)), CapturedAt: "2026-11-09T00:00:00Z"}
	manifest := &captureManifest{Files: []*capturedFile{f}, PathMap: []capturePathMapping{{Original: "/synthetic", Captured: "files"}}}
	view := newCaptureView(Host{}, dir, manifest)
	view.evidenceRevision = strings.Repeat("a", 64)
	record := WorkspaceRecord{EvidenceIdentity: strings.Repeat("b", 64)}
	if teamsVerifiedCodexCapture(view, record, origin, SourceConfig{}) == nil {
		t.Fatal("matching capture rejected")
	}
	f.MissingSince = "2026-11-10T00:00:00Z"
	if teamsVerifiedCodexCapture(view, record, origin, SourceConfig{}) == nil {
		t.Fatal("verified retained capture rejected after source disappeared")
	}
	f.SHA256 = strings.Repeat("c", 64)
	if teamsVerifiedCodexCapture(view, record, origin, SourceConfig{}) != nil {
		t.Fatal("mismatched captured bytes admitted")
	}
	f.SHA256 = teamsPayloadHash([]byte(data))
	manifest.Files = append(manifest.Files, f)
	if teamsVerifiedCodexCapture(view, record, origin, SourceConfig{}) != nil {
		t.Fatal("ambiguous capture admitted")
	}
}
