package archive

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNamingAuditSeparatesSourceAndResolvedTitles(t *testing.T) {
	useHost(t, "host-a")
	source := stableCodexSource(t, 2)
	c, _ := testCatalog(t)
	if result := ingestSource(t, c, source); result.Error != nil {
		t.Fatal(result.Error)
	}
	unit := firstSyncUnit(t, c, source)
	if result := c.auditUnit(t.Context(), unit, "before-link", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("initial audit: %+v", result)
	}
	linked := namingRecord("conductor", "named", "Repair login timeouts", "", "incremental-test")
	ingestNamingRecord(t, c, linked)
	id := stableID("workspace", "codex", "local", "incremental-test")
	if got := cachedWorkTitle(t, c, id); got != linked.Title {
		t.Fatalf("fixture did not borrow a title: %q", got)
	}
	if result := c.auditUnit(t.Context(), unit, "after-link", automaticAuditBytes); result.Outcome != "passed" {
		t.Fatalf("borrowed name failed independent audit: %+v", result)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'") != 0 {
		t.Fatal("borrowed name opened an integrity issue")
	}
	if got := cachedWorkTitle(t, c, id); got != linked.Title {
		t.Fatalf("audit changed the live display name: %q", got)
	}
	if _, err := c.DB.Exec("UPDATE messages SET text='corrupted transcript' WHERE conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?)", id); err != nil {
		t.Fatal(err)
	}
	if result := c.auditUnit(t.Context(), unit, "corruption", automaticAuditBytes); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "messages") {
		t.Fatalf("naming normalization hid transcript corruption: %+v", result)
	}
}

func TestNamingAuditFreezesMetadata(t *testing.T) {
	for _, store := range []string{"codex-index", "codex-state", "codex-empty-state", "claude-index", "claude-single-file"} {
		t.Run(store, func(t *testing.T) {
			useHost(t, "host-a")
			root := t.TempDir()
			source := SourceConfig{Name: "names", Kind: "codex", Path: root, Account: "local", Enabled: true}
			if store == "claude-index" || store == "claude-single-file" {
				source.Kind = "claude"
				file := filepath.Join(root, "project", "native.jsonl")
				writeSourceFile(t, file, claudeLine("native", "prompt", "Fix login", "", 1))
				writeSourceFile(t, filepath.Join(root, "project", "sessions-index.json"), `{"entries":[{"sessionId":"native","summary":"Repair login"}]}`)
				if store == "claude-single-file" {
					source.Path = file
				}
			} else {
				writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("native", 2))
				if store == "codex-index" {
					writeSourceFile(t, filepath.Join(root, "session_index.jsonl"), `{"id":"native","thread_name":"Repair login"}`+"\n")
				} else {
					db, err := sql.Open("sqlite", filepath.Join(root, "state_5.sqlite"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					name := "Repair login"
					if store == "codex-empty-state" {
						name = ""
					}
					execSQL(t, db, "PRAGMA journal_mode=WAL", "CREATE TABLE threads(id TEXT,name TEXT)")
					if _, err := db.Exec("INSERT INTO threads VALUES('native',?)", name); err != nil {
						t.Fatal(err)
					}
				}
			}
			c, config := testCatalog(t)
			config.Sources = []SourceConfig{source}
			if result := ingestSource(t, c, source); result.Error != nil {
				t.Fatal(result.Error)
			}
			unit := firstSyncUnit(t, c, source)
			if unit.Parts[0].signal == "" {
				t.Fatal("fixture lacks naming dependency signal")
			}
			if result := c.auditUnit(t.Context(), unit, "live", automaticAuditBytes); result.Outcome != "passed" {
				t.Fatalf("metadata-dependent transcript failed live audit: %+v", result)
			}
			if store == "claude-single-file" {
				return
			}
			if result := runCapture(t, config); !result.OK {
				t.Fatal(result)
			}
			targets, err := captureTargets(config.CaptureRoot, []string{"host-a"}, false, nil)
			if err != nil || len(targets) != 1 {
				t.Fatalf("capture targets: %v, %v", targets, err)
			}
			reader, _, view, err := openCaptureAdapter(targets[0])
			if err != nil {
				t.Fatal(err)
			}
			capturedSource := reader.Config()
			var captured syncUnit
			// Discovery must use the capture's mappings to retain original IDs.
			err = reader.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
				captured = unitOf(capturedSource, parts, false)
				return true
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			captured.Reader, captured.Host = reader, view.host.ID
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if result := c.auditUnit(t.Context(), captured, "capture", automaticAuditBytes); result.Outcome != "passed" {
				t.Fatalf("metadata-dependent transcript failed captured audit: %+v", result)
			}
			if store != "codex-empty-state" {
				if _, err := c.DB.Exec("UPDATE workspace_names SET source_title='Corrupted source title' WHERE workspace_id=?", stableID("workspace", source.Kind, "local", "native")); err != nil {
					t.Fatal(err)
				}
				if result := c.auditUnit(t.Context(), captured, "source-title", automaticAuditBytes); result.Outcome != "mismatch" || !slices.Contains(result.Differences, "workspace_names") {
					t.Fatalf("audit failed to verify source title independently: %+v", result)
				}
			}
		})
	}
}

func TestClaudeMalformedNamingIndexDoesNotBlockTranscripts(t *testing.T) {
	root := t.TempDir()
	source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local", Enabled: true}
	badIndex := filepath.Join(root, "broken", "sessions-index.json")
	writeSourceFile(t, filepath.Join(root, "broken", "bad.jsonl"), claudeLine("bad", "prompt", "Fix login", "", 1))
	writeSourceFile(t, badIndex, `{"entries":[`)
	writeSourceFile(t, filepath.Join(root, "healthy", "good.jsonl"), claudeLine("good", "prompt", "Fix logout", "", 1))
	writeSourceFile(t, filepath.Join(root, "healthy", "sessions-index.json"), `{"entries":[{"sessionId":"good","summary":"Repair logout"}]}`)
	c, _ := testCatalog(t)
	if result := ingestSource(t, c, source); result.Error != nil || result.Workspaces != 2 {
		t.Fatalf("optional malformed index blocked transcripts: %+v", result)
	}
	if got := cachedWorkTitle(t, c, stableID("workspace", "claude", "local", "bad")); got != "Claude bad" {
		t.Fatalf("broken directory did not use transcript fallback: %q", got)
	}
	if got := cachedWorkTitle(t, c, stableID("workspace", "claude", "local", "good")); got != "Repair logout" {
		t.Fatalf("broken sibling suppressed healthy metadata: %q", got)
	}
	writeSourceFile(t, badIndex, `{"entries":[{"sessionId":"bad","summary":"Repair login"}]}`)
	if result := ingestSource(t, c, source); result.Error != nil || result.Parsed != 1 {
		t.Fatalf("repaired optional metadata was not retried: %+v", result)
	}
	if got := cachedWorkTitle(t, c, stableID("workspace", "claude", "local", "bad")); got != "Repair login" {
		t.Fatalf("repaired index name = %q", got)
	}
}

func TestNamingAuditDefersChangingMetadataAndHonorsBudget(t *testing.T) {
	for _, store := range []string{"index", "state"} {
		t.Run(store, func(t *testing.T) {
			useHost(t, "host-a")
			root := t.TempDir()
			source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
			writeSourceFile(t, filepath.Join(root, "sessions", "rollout.jsonl"), codexLines("native", 2))
			var rename func()
			if store == "index" {
				index := filepath.Join(root, "session_index.jsonl")
				writeSourceFile(t, index, `{"id":"native","thread_name":"Repair login"}`+"\n")
				rename = func() {
					writeSourceFile(t, index, `{"id":"native","thread_name":"Repair login and logout"}`+"\n")
				}
			} else {
				db, err := sql.Open("sqlite", filepath.Join(root, "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				execSQL(t, db, "PRAGMA journal_mode=WAL", "CREATE TABLE threads(id TEXT,name TEXT)", "INSERT INTO threads VALUES('native','Repair login')")
				rename = func() { execSQL(t, db, "UPDATE threads SET name='Repair login and logout'") }
			}
			c, _ := testCatalog(t)
			if result := ingestSource(t, c, source); result.Error != nil {
				t.Fatal(result.Error)
			}
			unit := firstSyncUnit(t, c, source)
			if result := c.auditUnit(t.Context(), unit, "metadata-budget", unit.Bytes); result.Outcome != "deferred" {
				t.Fatalf("metadata was not counted in the evidence budget: %+v", result)
			}
			previous := parseHook
			t.Cleanup(func() { parseHook = previous })
			parseHook = func(string) { rename() }
			if result := c.auditUnit(t.Context(), unit, "metadata-movement", automaticAuditBytes); result.Outcome != "deferred" {
				t.Fatalf("moving metadata was compared as stable evidence: %+v", result)
			}
			parseHook = previous
			if countRows(t, c, "SELECT COUNT(*) FROM sync_integrity_issues") != 0 {
				t.Fatal("unstable or oversized metadata opened an integrity issue")
			}
		})
	}
}
