package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHarnessVersionsFromTranscripts(t *testing.T) {
	cases := []struct{ name, provider, lines, harness, first, last string }{
		{"claude", "claude", `{"sessionId":"s","uuid":"a","type":"assistant","entrypoint":"cli","version":"2.1.263","message":{"content":"one"}}` + "\n" + `{"sessionId":"s","uuid":"b","type":"assistant","entrypoint":"cli","version":"2.1.276","message":{"content":"two"}}` + "\n", "claude-code/cli", "2.1.263", "2.1.276"},
		{"codex", "codex", strings.Join([]string{`{"type":"session_meta","payload":{"id":"s","originator":"codex_exec","cli_version":"0.154.0"}}`, `{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`, `{"type":"session_meta","payload":{"id":"s","originator":"codex_exec","cli_version":"0.155.0"}}`}, "\n") + "\n", "codex/codex_exec", "0.154.0", "0.155.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(tc.lines), 0600); err != nil {
				t.Fatal(err)
			}
			adapter := jsonlAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: path, Account: "local"}}, provider: tc.provider}
			record, ok, err := adapter.parse(path)
			if err != nil || !ok {
				t.Fatalf("parse: %v %v", ok, err)
			}
			got := record.Conversations[0]
			if got.Harness != tc.harness || got.HarnessVersionFirst != tc.first || got.HarnessVersionLast != tc.last {
				t.Fatalf("version: %#v", got)
			}
		})
	}
}

func TestHarnessBackfillAndAlias(t *testing.T) {
	c, _ := testCatalog(t)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('wc','claude','local','c','Claude','2026-09-01'),('ww','conductor','local','w','Conductor','2026-09-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,harness) VALUES('c','wc','claude','local','c','claude-code'),('w','ww','claude','local','conductor:w','conductor')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,raw_text,source_order,content_hash) VALUES('m1','c','m1','assistant','message','one','{"version":"2.1.1","entrypoint":"cli"}',1,'x'),('m2','c','m2','assistant','message','two','{"version":"2.1.2","entrypoint":"cli"}',2,'y')`,
		`INSERT INTO conversation_identity_links(left_id,right_id,relationship,confidence,evidence_json) VALUES('c','w','native-alias',1,'{}')`,
	} {
		if _, err := c.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := c.backfillHarnessVersions(); err != nil {
			t.Fatal(err)
		}
		if err := c.inheritHarnessAliases(); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := queryMaps(c.DB, `SELECT id,harness,harness_version_first,harness_version_last,harness_version_source FROM conversations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	if firstString(rows[0]["harness"]) != "claude-code/cli" || firstString(rows[0]["harness_version_first"]) != "2.1.1" || firstString(rows[0]["harness_version_last"]) != "2.1.2" || firstString(rows[1]["harness_version_last"]) != "2.1.2" || firstString(rows[1]["harness_version_source"]) != "alias" {
		t.Fatalf("backfill/alias: %#v", rows)
	}
}

func TestHarnessOptionalFieldsKeepDigest(t *testing.T) {
	record := WorkspaceRecord{SourceID: "w", SourceKind: "claude", Conversations: []ConversationRecord{{NativeID: "c", Provider: "claude"}}}
	before, err := workspaceRecordDigest(record)
	if err != nil {
		t.Fatal(err)
	}
	// Adding optional fields to the struct must not change legacy JSON.
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Harness") {
		t.Fatalf("empty harness fields in digest: %s", encoded)
	}
	after, err := workspaceRecordDigest(record)
	if err != nil || before != after {
		t.Fatalf("digest changed: %s %s %v", before, after, err)
	}
	record.Conversations[0].Harness = "claude-code/cli"
	withHarness, err := workspaceRecordDigest(record)
	if err != nil || withHarness == before {
		t.Fatalf("populated harness did not change legacy digest: %s %s %v", before, withHarness, err)
	}
}

func TestInstalledVersionDoesNotChangeDigestOrPastConversation(t *testing.T) {
	observation := installedVersionObservation{Version: "2.17.0", Since: "2026-09-28T12:00:00Z"}
	if first, last := installedVersionForActivity(observation, "2026-09-27T12:00:00Z", "2026-09-27T13:00:00Z"); first != "" || last != "" {
		t.Fatalf("past conversation got installed version: %q %q", first, last)
	}
	if first, last := installedVersionForActivity(observation, "2026-09-28T11:00:00Z", "2026-09-28T13:00:00Z"); first != "" || last != "2.17.0" {
		t.Fatalf("ongoing conversation got %q %q", first, last)
	}
	first, last := installedVersionForActivity(observation, "2026-09-28T13:00:00Z", "2026-09-28T14:00:00Z")
	if first != "2.17.0" || last != "2.17.0" {
		t.Fatalf("new conversation got %q %q", first, last)
	}
	record := WorkspaceRecord{Conversations: []ConversationRecord{{Harness: "antigravity", HarnessVersionFirst: first, HarnessVersionLast: last, HarnessVersionSource: "installed-app"}}}
	before, err := workspaceRecordDigest(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Conversations[0].HarnessVersionFirst = "2.18.0"
	record.Conversations[0].HarnessVersionLast = "2.18.0"
	after, err := workspaceRecordDigest(record)
	if err != nil || before != after || record.Conversations[0].HarnessVersionLast != "2.18.0" {
		t.Fatalf("installed version changed digest or mutated record: %s %s %v", before, after, err)
	}

	adapter := &antigravityAdapter{installedVersions: map[string]installedVersionObservation{"antigravity": observation}}
	adapter.view = &captureView{host: currentHost()}
	if got := adapter.installedHarnessVersion("antigravity"); got.Version != "2.17.0" {
		t.Fatalf("same-host capture lost version: %#v", got)
	}
	adapter.view = &captureView{host: Host{ID: "other-host"}}
	if got := adapter.installedHarnessVersion("antigravity"); got.Version != "" {
		t.Fatalf("other-host capture used local version: %#v", got)
	}
}

func TestInstalledVersionObservationMovesOnUpgrade(t *testing.T) {
	c, _ := testCatalog(t)
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	version := "2.17.0"
	a := &antigravityAdapter{installedVersion: func(app string) string {
		if app == "antigravity" {
			return version
		}
		return ""
	}}
	a.view = &captureView{host: currentHost()}
	if err := a.prepareInstalledVersions(c); err != nil {
		t.Fatal(err)
	}
	first := a.installedHarnessVersion("antigravity")
	clock = clock.Add(time.Hour)
	if err := a.prepareInstalledVersions(c); err != nil {
		t.Fatal(err)
	}
	if got := a.installedHarnessVersion("antigravity"); got != first {
		t.Fatalf("unchanged version moved observation: %#v %#v", first, got)
	}
	version = "2.18.0"
	if err := a.prepareInstalledVersions(c); err != nil {
		t.Fatal(err)
	}
	newVersion := a.installedHarnessVersion("antigravity")
	if newVersion.Version != version || newVersion.Since == first.Since {
		t.Fatalf("upgrade observation = %#v after %#v", newVersion, first)
	}
	if first, last := installedVersionForActivity(newVersion, "2026-09-28T12:10:00Z", "2026-09-28T12:30:00Z"); first != "" || last != "" {
		t.Fatalf("earlier conversation relabeled: %q %q", first, last)
	}
}

func TestInstalledVersionUpsertPreservesFirstAndPastLast(t *testing.T) {
	c, _ := testCatalog(t)
	if _, err := c.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','antigravity','local','w','Work','2026-09-28')`); err != nil {
		t.Fatal(err)
	}
	write := func(first, last string) {
		t.Helper()
		tx, err := c.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, err = upsertConversation(tx, "w", "", ConversationRecord{NativeID: "a", Provider: "antigravity", Account: "local", Harness: "antigravity", HarnessVersionFirst: first, HarnessVersionLast: last, HarnessVersionSource: "installed-app"}, currentHost().ID)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	write("2.17.0", "2.17.0")
	write("", "")
	var first, last string
	if err := c.DB.QueryRow(`SELECT harness_version_first,harness_version_last FROM conversations WHERE native_id='a'`).Scan(&first, &last); err != nil || first != "2.17.0" || last != "2.17.0" {
		t.Fatalf("past version changed: %q %q %v", first, last, err)
	}
	write("", "2.18.0")
	if err := c.DB.QueryRow(`SELECT harness_version_first,harness_version_last FROM conversations WHERE native_id='a'`).Scan(&first, &last); err != nil || first != "2.17.0" || last != "2.18.0" {
		t.Fatalf("upgrade span lost: %q %q %v", first, last, err)
	}
}

func TestHarnessBackfillRetriesCustomCaptureRoot(t *testing.T) {
	c, _ := testCatalog(t)
	c.setCaptureRoot(filepath.Join(t.TempDir(), "custom-captures"))
	origin := "/other-mac/rollout.jsonl"
	dir := filepath.Join(c.captureRootPath(), "other-host", "codex-source")
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := captureManifest{Version: captureManifestVersion, Files: []*capturedFile{{Path: origin, Captured: "files/rollout.jsonl"}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, captureManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('wc','codex','local','c','Codex','2026-09-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin,origin_host_id) VALUES('c','wc','codex','local','c','/other-mac/rollout.jsonl','other-host')`,
	} {
		if _, err := c.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.backfillHarnessVersions(); err != nil {
		t.Fatal(err)
	}
	var cursor string
	if err := c.DB.QueryRow(`SELECT value FROM meta WHERE key='harness_version_backfill'`).Scan(&cursor); err != nil || cursor != "0" {
		t.Fatalf("completed cursor = %q: %v", cursor, err)
	}
	content := `{"type":"session_meta","payload":{"originator":"codex_exec","cli_version":"0.155.0"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "files", "rollout.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.backfillHarnessVersions(); err != nil {
		t.Fatal(err)
	}
	var harness, version string
	if err := c.DB.QueryRow(`SELECT harness,harness_version_last FROM conversations WHERE id='c'`).Scan(&harness, &version); err != nil || harness != "codex/codex_exec" || version != "0.155.0" {
		t.Fatalf("captured version = %q %q: %v", harness, version, err)
	}
}
