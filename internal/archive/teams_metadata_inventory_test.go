package archive

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/teams"
	"modernc.org/sqlite"
)

func TestTeamsMetadataInventoryMatchesLegacy(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("census requires local APFS on macOS")
	}
	useHost(t, "synthetic-host")
	c, cfg := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": "synthetic-app", "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, "synthetic-app", false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout-shared.jsonl")
	sources := []struct {
		name  string
		total int
	}{{"shared", 250}, {"prototype", 50}, {"private", 25}}
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	for _, v := range sources {
		p := filepath.Join(root, "sessions", "rollout-"+v.name+".jsonl")
		counts := fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":2,"output_tokens":2,"total_tokens":%d}`, v.total-2, v.total)
		data := strings.Replace(codexLines("synthetic-"+v.name, 1), `"cwd":"/proj"`, `"cwd":"/proj","model":"gpt-test"`, 1) + fmt.Sprintf(`{"type":"token_usage_record","timestamp":"2026-11-09T23:59:30Z","payload":{"response_id":"r1","usage":%s,"thread_token_usage":{"total_tokens":%d}}}`, counts, v.total) + "\n" + fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s}}}`, counts, counts) + "\n"
		writeSourceFile(t, p, data)
	}
	runCapture(t, cfg)
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	for _, v := range sources {
		p := filepath.Join(root, "sessions", "rollout-"+v.name+".jsonl")
		if _, err = c.captureTeamsTranscript(ctx, cfg, source, p); err != nil {
			t.Fatal(err)
		}
		indexCaptures(t, c, cfg.CaptureRoot, "synthetic-host")
	}
	for _, v := range sources {
		var id string
		if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id=?`, "synthetic-"+v.name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if v.name == "private" {
			if _, err = c.setTeamsPrivate(ctx, id, true); err != nil {
				t.Fatal(err)
			}
			continue
		}
		input, e := teamsManualReviewInput(ctx, c.reader(), id)
		if e != nil {
			t.Fatal(e)
		}
		privacy, e := c.teamsPrivacy(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.manuallyClearTeamsInput(ctx, id, input, privacy.Revision); e != nil {
			t.Fatal(e)
		}
	}

	var binding string
	var revision int64
	if err = c.DB.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE superseded_at=''`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = c.reviewTeamsSourceMapping(ctx, d, credential, binding, "work", revision, true); err != nil {
		t.Fatal(err)
	}
	// Baseline consent is separate from source-transfer consent.
	settings, err = readTeamsSettings(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acknowledgeTeamsProtectionPolicy(ctx, d, credential, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`CREATE TABLE teams_source_transfer_ack(id INTEGER PRIMARY KEY,policy_id TEXT,policy_sha256 TEXT,origin TEXT,org_id TEXT,person_id TEXT,installation_id TEXT,library_id TEXT,instance_id TEXT,enrollment_generation TEXT,acknowledged_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	// This metadata fixture is deliberately v33; record the separately
	// approved transfer only as synthetic setup. Production accepts it only
	// through the format-35 protected command.
	if _, err = c.DB.Exec(`INSERT INTO teams_source_transfer_ack(id,policy_id,policy_sha256,origin,org_id,person_id,installation_id,library_id,instance_id,enrollment_generation,acknowledged_at) SELECT 1,?,?,?,?,?,?,?,?,generation,'synthetic' FROM teams_protection_enrollment WHERE id=1`, teamsSourceTransferPolicyID, teamsSourceTransferPolicyDigest(), credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance); err != nil {
		t.Fatal(err)
	}
	session, err := beginCapture(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	roster, err := teamsCaptureRoster(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(root, "sessions", "rollout-extra.jsonl")
	writeSourceFile(t, extra, codexLines("extra", 1))
	if _, err = teamsCaptureRoster(ctx, cfg); err == nil {
		t.Fatal("unindexed live file admitted")
	}
	if err = os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	legacy, err := c.teamsObserveV2Content(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	// An older privacy row can coexist with a separate held assertion without
	// an explicit-private ledger row. The user's private mark still dominates.
	var privateID string
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id='synthetic-private'`).Scan(&privateID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`DELETE FROM teams_protection_assertions WHERE conversation_id=? AND kind='explicit_private'`, privateID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) VALUES(?,'codex','local','synthetic-private','positive_flag','synthetic-hold',1,1)`, privateID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"explicit_private", "positive_flag"} {
		if _, err = c.DB.Exec(`INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) VALUES('different-conversation','codex','local','synthetic-shared',?,'synthetic-case',0,1)`, kind); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	allowedReads := map[string]bool{
		"conversation_identity_links.":                         true,
		"conversations.":                                       true,
		"conversations.account":                                true,
		"conversations.id":                                     true,
		"conversations.native_id":                              true,
		"conversations.provider":                               true,
		"query_cache_domains.name":                             true,
		"query_cache_domains.value":                            true,
		"query_cache_generation.id":                            true,
		"query_cache_generation.value":                         true,
		"source_item_states.":                                  true,
		"source_item_states.host_id":                           true,
		"source_item_states.item":                              true,
		"source_item_states.source_name":                       true,
		"source_record_states.":                                true,
		"source_record_states.digest":                          true,
		"source_record_states.host_id":                         true,
		"source_record_states.source_account":                  true,
		"source_record_states.source_id":                       true,
		"source_record_states.source_kind":                     true,
		"source_record_states.source_name":                     true,
		"source_states.":                                       true,
		"source_states.coverage":                               true,
		"source_states.error":                                  true,
		"source_states.host_id":                                true,
		"source_states.pending_count":                          true,
		"source_states.source_name":                            true,
		"teams_codex_capture_custody.":                         true,
		"teams_codex_capture_custody.binding_id":               true,
		"teams_codex_capture_custody.capture_path":             true,
		"teams_codex_capture_custody.capture_revision":         true,
		"teams_codex_capture_custody.capture_sha256":           true,
		"teams_codex_capture_custody.captured_at":              true,
		"teams_codex_capture_custody.conversation_id":          true,
		"teams_codex_capture_custody.evidence_identity":        true,
		"teams_codex_capture_custody.evidence_sha256":          true,
		"teams_codex_capture_custody.indexed_digest":           true,
		"teams_codex_capture_custody.origin":                   true,
		"teams_codex_capture_custody.provenance_sha256":        true,
		"teams_codex_request_profiles.":                        true,
		"teams_codex_request_profiles.evidence_sha256":         true,
		"teams_codex_request_profiles.indexed_digest":          true,
		"teams_codex_transcript_evidence_v1.":                  true,
		"teams_codex_transcript_evidence_v1.binding_id":        true,
		"teams_codex_transcript_evidence_v1.capture_revision":  true,
		"teams_codex_transcript_evidence_v1.capture_sha256":    true,
		"teams_codex_transcript_evidence_v1.content_matched":   true,
		"teams_codex_transcript_evidence_v1.conversation_id":   true,
		"teams_codex_transcript_evidence_v1.gates_version":     true,
		"teams_codex_transcript_evidence_v1.indexed_digest":    true,
		"teams_codex_transcript_evidence_v1.origin":            true,
		"teams_codex_transcript_evidence_v1.review_input":      true,
		"teams_codex_transcript_evidence_v1.transcript_sha256": true,
		"teams_conversation_privacy.account":                   true,
		"teams_conversation_privacy.classifier_input":          true,
		"teams_conversation_privacy.classifier_reviewed":       true,
		"teams_conversation_privacy.classifier_state":          true,
		"teams_conversation_privacy.conversation_id":           true,
		"teams_conversation_privacy.native_id":                 true,
		"teams_conversation_privacy.provider":                  true,
		"teams_conversation_privacy.user_private":              true,
		"teams_native_source_proofs.":                          true,
		"teams_native_source_proofs.binding_id":                true,
		"teams_native_source_proofs.conversation_id":           true,
		"teams_native_source_proofs.indexed_digest":            true,
		"teams_native_source_proofs.native_id":                 true,
		"teams_native_source_proofs.origin":                    true,
		"teams_native_source_proofs.provider":                  true,
		"teams_native_source_proofs.status":                    true,
		"teams_native_source_proofs.total_digest":              true,
		"teams_native_source_proofs.total_tokens":              true,
		"teams_native_source_proofs.trailing_partial":          true,
		"teams_privacy_revision.id":                            true,
		"teams_privacy_revision.revision":                      true,
		"teams_protection_assertions.account":                  true,
		"teams_protection_assertions.active":                   true,
		"teams_protection_assertions.conversation_id":          true,
		"teams_protection_assertions.kind":                     true,
		"teams_protection_assertions.native_id":                true,
		"teams_protection_assertions.provider":                 true,
		"teams_source_bindings.binding_id":                     true,
		"teams_source_bindings.host_id":                        true,
		"teams_source_bindings.provenance_sha256":              true,
		"teams_source_bindings.source_name":                    true,
		"teams_source_bindings.source_root":                    true,
		"teams_source_bindings.superseded_at":                  true,
		"teams_source_mapping_current.binding_id":              true,
		"teams_source_mapping_current.revision":                true,
		"teams_source_mapping_revisions.installation_id":       true,
		"teams_source_mapping_revisions.instance_id":           true,
		"teams_source_mapping_revisions.library_id":            true,
		"teams_source_mapping_revisions.mode":                  true,
		"teams_source_mapping_revisions.org_id":                true,
		"teams_source_mapping_revisions.origin":                true,
		"teams_source_mapping_revisions.person_id":             true,
		"teams_source_mapping_revisions.revision":              true,
		"teams_source_revision.id":                             true,
		"teams_source_revision.revision":                       true,
	}
	if err = conn.Raw(func(raw any) error {
		return raw.(*catalogConn).sqliteConn.(sqlite.AuthorizerRegisterer).RegisterAuthorizer(func(action sqlite.AuthorizerActionCode, table, column, _, _ string) sqlite.AuthorizerReturnCode {
			if action == sqlite.AuthRead && !allowedReads[table+"."+column] {
				return sqlite.AuthorizerDeny
			}
			return sqlite.AuthorizerOK
		})
	}); err != nil {
		t.Fatal(err)
	}
	defer conn.Raw(func(raw any) error {
		return raw.(*catalogConn).sqliteConn.(sqlite.AuthorizerRegisterer).RegisterAuthorizer(nil)
	})
	for _, q := range []string{`SELECT text FROM messages LIMIT 1`, `SELECT role FROM messages LIMIT 1`, `SELECT model FROM conversations LIMIT 1`, `SELECT text FROM messages_fts LIMIT 1`} {
		var forbidden string
		if err = conn.QueryRowContext(ctx, q).Scan(&forbidden); err == nil {
			t.Fatalf("content authorizer allowed %s", q)
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := teamsMetadataInventoryIn(ctx, tx, cfg, d, credential, roster)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := credential.SourceKey()
	account, err := teams.SourceWorkNamespace(keyBytes, "codex")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]bool{}
	for _, domain := range []string{"codex", "codex-content", "codex-component"} {
		token, e := teams.SourceToken(keyBytes, domain, account, "synthetic-shared")
		if e != nil {
			t.Fatal(e)
		}
		tokens[token] = true
	}
	clear(keyBytes)
	for _, tc := range []struct {
		name, kind, want string
		active, held     int
	}{
		{"tuple-only explicit", "explicit_private", "private", 1, 0},
		{"explicit plus hold", "explicit_private", "private", 1, 1},
		{"hold only", "positive_flag", "unknown", 1, 0},
		{"inactive explicit", "explicit_private", "public", 0, 0},
	} {
		if _, err = tx.Exec(`SAVEPOINT assertion_case`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE teams_protection_assertions SET active=? WHERE conversation_id='different-conversation' AND native_id='synthetic-shared' AND kind=?`, tc.active, tc.kind); err != nil {
			t.Fatal(err)
		}
		if tc.held != 0 {
			if _, err = tx.Exec(`UPDATE teams_protection_assertions SET active=1 WHERE conversation_id='different-conversation' AND native_id='synthetic-shared' AND kind='positive_flag'`); err != nil {
				t.Fatal(err)
			}
		}
		variant, e := teamsMetadataInventoryIn(ctx, tx, cfg, d, credential, roster)
		if e != nil {
			t.Fatalf("%s: %v", tc.name, e)
		}
		matched := 0
		for _, item := range variant.Items {
			if tokens[item.Token] {
				matched++
				if item.Protection != tc.want {
					t.Fatalf("%s: %s protection %q", tc.name, item.Kind, item.Protection)
				}
			}
		}
		if matched != 3 {
			t.Fatalf("%s: matched %d graph items", tc.name, matched)
		}
		if _, err = tx.Exec(`ROLLBACK TO assertion_case`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`RELEASE assertion_case`); err != nil {
			t.Fatal(err)
		}
	}
	bad := make(map[string]teamsCapturedRosterEntry, len(roster))
	for k, v := range roster {
		bad[k] = v
	}
	bad[file] = teamsCapturedRosterEntry{captureSHA: strings.Repeat("a", 64), capturePath: roster[file].capturePath}
	if _, err = teamsMetadataInventoryIn(ctx, tx, cfg, d, credential, bad); err == nil {
		t.Fatal("mismatched custody admitted")
	}
	var publicTotal, privateTotal int64
	for _, item := range got.Items {
		if item.Kind == "codex_thread" {
			if item.Protection == "public" {
				publicTotal += got.values[item.Token]
			}
			if item.Protection == "private" {
				privateTotal += got.values[item.Token]
			}
		}
	}
	if publicTotal != 300 || privateTotal != 25 {
		t.Fatalf("unexpected witnessed totals: public=%d private=%d", publicTotal, privateTotal)
	}
	if !got.Complete || !reflect.DeepEqual(got.Items, legacy.Items) || !reflect.DeepEqual(got.values, legacy.values) {
		t.Fatalf("metadata graph differs: got=%+v legacy=%+v", got.Items, legacy.Items)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = conn.Raw(func(raw any) error {
		return raw.(*catalogConn).sqliteConn.(sqlite.AuthorizerRegisterer).RegisterAuthorizer(nil)
	}); err != nil {
		t.Fatal(err)
	}
	override(t, &teamsIndependentRosterProof, func(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, cred *teams.EnrollmentCredential) (teamsV2Inventory, error) {
		return teamsMetadataInventoryIn(ctx, tx, cfg, d, cred, roster)
	})
	d.ManagedBirth = true
	proofTx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer proofTx.Rollback()
	// Exercise the draft's format-35 transfer gate inside this rolled-back
	// metadata-only fixture; no production writer is activated here.
	if _, e = proofTx.Exec(`UPDATE meta SET value='35' WHERE key='schema_version'`); e != nil {
		t.Fatal(e)
	}
	if _, e = proofTx.Exec(`UPDATE teams_protection_assertions SET active=1 WHERE conversation_id='different-conversation' AND native_id='synthetic-shared' AND kind='positive_flag'`); e != nil {
		t.Fatal(e)
	}
	baseline, _, e := teamsRetainedBaselineDraft(ctx, proofTx, cfg, d, credential)
	if e != nil {
		t.Fatal("retained draft", e)
	}
	var public, private, unknown int64
	for _, item := range baseline.Items {
		if item.Kind != "codex_thread" {
			continue
		}
		switch item.Protection {
		case "public":
			public += baseline.values[item.Token]
		case "private":
			private += baseline.values[item.Token]
		case "unknown":
			unknown += baseline.values[item.Token]
		}
	}
	if public != 50 || private != 25 || unknown != 250 {
		t.Fatalf("retained protections changed: %d %d %d", public, private, unknown)
	}
	d.ManagedBirth = false
	if _, _, e = teamsRetainedBaselineDraft(ctx, proofTx, cfg, d, credential); !errors.Is(e, errTeamsProtectionHeld) {
		t.Fatalf("existing library baselined: %v", e)
	}
	if err = proofTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Even identical bytes with restored mtime on a replacement inode cannot
	// be reconciled with the acquisition stamp from the capture.
	data, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(file)
	if e != nil {
		t.Fatal(e)
	}
	replacement := filepath.Join(filepath.Dir(file), "replacement.tmp")
	if e = os.WriteFile(replacement, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(replacement, info.ModTime(), info.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(replacement, file); e != nil {
		t.Fatal(e)
	}
	if _, e = teamsCaptureRoster(ctx, cfg); e == nil {
		t.Fatal("same-bytes replacement admitted")
	}
}

func TestTeamsCaptureManifestReadIsBounded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("manifest proof is used by the macOS APFS census")
	}
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, captureManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(teamsCaptureManifestMaxBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsLoadBoundedCaptureManifest(dir); err == nil {
		t.Fatal("oversized capture manifest admitted")
	}
	manifest := filepath.Join(dir, captureManifestName)
	if err = os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "target"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("target", manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsLoadBoundedCaptureManifest(dir); err == nil {
		t.Fatal("symlinked capture manifest admitted")
	}
	if err = os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifest, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, "records"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "records", "v1"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsLoadBoundedCaptureManifest(dir); err != nil {
		t.Fatalf("supported records format rejected: %v", err)
	}
	if err = os.WriteFile(filepath.Join(dir, "records", "v2"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsLoadBoundedCaptureManifest(dir); err == nil {
		t.Fatal("unsupported records format admitted")
	}
	if err = os.Remove(filepath.Join(dir, "records", "v2")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	override(t, &teamsReadManifestBytes, func(r io.Reader) ([]byte, error) {
		data, e := io.ReadAll(r)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(manifest, []byte(`[]`), 0600); e != nil {
			return nil, e
		}
		if e = os.Chtimes(manifest, before.ModTime(), before.ModTime()); e != nil {
			return nil, e
		}
		return data, nil
	})
	if _, err = teamsLoadBoundedCaptureManifest(dir); !errors.Is(err, errTeamsSnapshotChanged) {
		t.Fatalf("same-size rewrite with restored mtime admitted: %v", err)
	}
}
