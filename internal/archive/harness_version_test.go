package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}
