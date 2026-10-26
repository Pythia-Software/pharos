package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

func lifecycleLibraries(t *testing.T) (Config, Config) {
	t.Helper()
	override(t, &libraryVolumeIdentity, testIdentity)
	root := t.TempDir()
	a := testLibrary(t, filepath.Join(root, "pending"))
	b, err := InitLibrary(filepath.Join(root, "archive"), testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}
func catalogCount(t *testing.T, cfg Config, table string) int {
	t.Helper()
	c := reopenCatalog(t, cfg.CatalogPath)
	defer c.Close()
	var n int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM " + quotedIdentifier(table)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestLibraryRegistryAssignmentAndCopies(t *testing.T) {
	a, b := lifecycleLibraries(t)
	r := installationRegistry{Version: 1, Libraries: []registeredLibrary{}}
	first, err := r.register(a, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.register(b, "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || r.MCP != first {
		t.Fatalf("default assignment changed: %+v", r)
	}
	r.Selected = second
	if r.MCP != first {
		t.Fatal("browsing changed MCP")
	}
	original, err := readLibraryDescriptor(a)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(filepath.Dir(a.Path)), "renamed")
	if err = os.Rename(filepath.Dir(a.Path), moved); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(filepath.Join(moved, libraryConfigName))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := readLibraryDescriptor(cfg)
	if err != nil || descriptor.ID != original.ID {
		t.Fatal("directory rename lost identity", err)
	}
	copyDir := filepath.Join(t.TempDir(), "copy")
	if _, err = retainedTree(context.Background(), moved, copyDir, "", false); err != nil {
		t.Fatal(err)
	}
	copied, err := LoadConfig(filepath.Join(copyDir, libraryConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readLibraryDescriptor(copied); err == nil {
		t.Fatal("unmanaged writable copy was accepted")
	}
	stable, err := RecoverLibrary(context.Background(), copyDir, filepath.Join(t.TempDir(), "independent"), nil)
	if err != nil {
		t.Fatal("copied library has no recovery path", err)
	}
	recovered, err := readLibraryDescriptor(stable)
	if err != nil || recovered.Instance == original.Instance || recovered.RecoveredFrom != original.ID {
		t.Fatal("recovery lost copy lineage", err)
	}
}
func TestLibraryTransferCopyMoveAndPreferences(t *testing.T) {
	a, b := lifecycleLibraries(t)
	c := reopenCatalog(t, a.CatalogPath)
	if err := c.SetMCPEnabled(false); err != nil {
		t.Fatal(err)
	}
	for id, expression := range map[string]string{"seconds": "[duration_ms]/1000", "double_seconds": "[@computed/seconds]*2"} {
		if _, err := (qt.SQLiteComputedColumnStore{DB: c.DB}).Save(context.Background(), queryTableScope, "pharos_tool_calls", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: id, Label: id, Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: expression}}}); err != nil {
			t.Fatal(err)
		}
	}
	// The source kind alone is not a privacy marker: ordinary imported history
	// may also identify Pharos as its source.
	_, err := c.DB.Exec(`UPDATE workspaces SET source_kind='pharos' WHERE source_id='work-1';
 INSERT OR REPLACE INTO ui_preferences(key,value) VALUES('saved-view','source');
 INSERT INTO cloud_destinations(id,config_json,credential_json,imported_at) VALUES('source-cloud','{"name":"Source backup"}','{"fixture":"upload-only"}','2026-10-23');
 INSERT INTO cloud_backup_runs(id,destination_id,status_json,created_at) VALUES('source-run','source-cloud','{"state":"complete"}','2026-10-23');
 INSERT INTO cloud_backup_schedules(destination_id,state_json) VALUES('source-cloud','{"enabled":true}');
 INSERT INTO message_authorship_spans(message_id,position,category,rule,start_byte,end_byte,chars,words) SELECT id,0,'typed','source',0,3,3,1 FROM messages WHERE role='user';`)
	if err != nil {
		t.Fatal(err)
	}
	server := testServer(t, a, c)
	private := askSession{ID: "private", HostID: currentHost().ID, CreatedAt: 1, UpdatedAt: 2,
		Selection: askSelection{Provider: "codex"}, Conversation: []askTurn{{ID: "turn", Prompt: "PRIVATE_ASK_QUESTION", Reply: "PRIVATE_ASK_REPLY"}}}
	if err = server.saveAskSession(t.Context(), private, true); err != nil {
		t.Fatal(err)
	}
	settleServer(t, server)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	c = reopenCatalog(t, b.CatalogPath)
	if _, err := (qt.SQLiteComputedColumnStore{DB: c.DB}).Save(context.Background(), queryTableScope, "pharos_tool_calls", qt.SaveComputedColumnRequest{Column: qt.ComputedColumn{ID: "seconds", Label: "Destination definition", Expression: qt.ComputedExpression{Language: "qt-expr", Version: 1, Source: "[duration_ms]/60"}}}); err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT OR REPLACE INTO ui_preferences(key,value) VALUES('saved-view','target')`)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(a.ArchiveRoot, "package", "patch.diff")
	writeSourceFile(t, preserved, "retained patch")
	receipt, err := TransferLibrary(context.Background(), a, b, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, b, "messages") != 2 || catalogCount(t, a, "messages") != 4 {
		t.Fatal("copy did not retain both histories")
	}
	for _, marker := range []string{"PRIVATE_ASK_QUESTION", "PRIVATE_ASK_REPLY"} {
		bytes, err := os.ReadFile(filepath.Join(b.CaptureRoot, ".imports", receipt.ID, "history.sqlite3"))
		if err != nil || strings.Contains(string(bytes), marker) {
			t.Fatal("retained history snapshot contains private Ask text", marker, err)
		}
	}
	if catalogCount(t, b, "message_authorship_spans") != 1 {
		t.Fatal("compound integer identities were not retained")
	}
	again, err := TransferLibrary(context.Background(), a, b, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != again.ID || catalogCount(t, b, "messages") != 2 {
		snapshot, _ := openReadOnlySQLite(filepath.Join(b.CaptureRoot, ".imports", receipt.ID, "history.sqlite3"))
		defer snapshot.Close()
		current, _ := openReadOnlySQLite(a.CatalogPath)
		defer current.Close()
		tables, _ := historyTables(context.Background(), current)
		for _, table := range tables {
			x, _ := queryMaps(snapshot, "SELECT * FROM "+quotedIdentifier(table)+" ORDER BY "+historyOrder(table))
			y, _ := queryMaps(current, "SELECT * FROM "+quotedIdentifier(table)+" ORDER BY "+historyOrder(table))
			if jsonText(x) != jsonText(y) {
				t.Logf("changed %s: before=%s after=%s", table, jsonText(x), jsonText(y))
			}
		}
		t.Fatalf("copy was not idempotent: first=%s second=%s", receipt.SourceDigest, again.SourceDigest)
	}
	c = reopenCatalog(t, b.CatalogPath)
	var preference string
	err = c.DB.QueryRow("SELECT value FROM ui_preferences WHERE key='saved-view'").Scan(&preference)
	c.Close()
	if err != nil || preference != "target" {
		t.Fatal("independent preferences merged", err)
	}
	if catalogCount(t, b, "ask_sessions") != 0 {
		t.Fatal("private Ask state transferred")
	}
	receipt, err = TransferLibrary(context.Background(), a, b, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "complete" || catalogCount(t, a, "messages") != 0 || catalogCount(t, b, "messages") != 2 {
		t.Fatal("verified move did not empty only the source")
	}
	if _, err = os.Stat(preserved); !os.IsNotExist(err) {
		t.Fatal("delivered evidence was retained in source", err)
	}
	if catalogCount(t, a, "ask_sessions") != 1 {
		t.Fatal("source user state was removed")
	}
	for _, table := range []string{"cloud_destinations", "cloud_backup_runs", "cloud_backup_schedules"} {
		if catalogCount(t, a, table) != 1 || catalogCount(t, b, table) != 0 {
			t.Fatal("move discarded or merged independent cloud backup configuration", table)
		}
	}
	c = reopenCatalog(t, a.CatalogPath)
	if enabled, err := c.MCPEnabled(); err != nil || enabled {
		t.Fatal("move reset source MCP consent", err)
	}
	err = c.DB.QueryRow("SELECT value FROM ui_preferences WHERE key='saved-view'").Scan(&preference)
	c.Close()
	if err != nil || preference != "source" {
		t.Fatal("source preference was removed", err)
	}
	for _, side := range []struct {
		config Config
		want   map[string]string
	}{{a, map[string]string{"seconds": "[duration_ms]/1000", "double_seconds": "[@computed/seconds]*2"}}, {b, map[string]string{"seconds": "[duration_ms]/60"}}} {
		c := reopenCatalog(t, side.config.CatalogPath)
		definitions, err := queryMaps(c.DB, "SELECT id,source FROM query_table_computed_columns WHERE scope=? AND dataset=?", queryTableScope, "pharos_tool_calls")
		c.Close()
		if err != nil || len(definitions) != len(side.want) {
			t.Fatal("move changed the saved computed-column catalogue", definitions, err)
		}
		for _, definition := range definitions {
			if side.want[firstString(definition["id"])] != firstString(definition["source"]) {
				t.Fatal("move discarded or merged independent computed definitions", definition)
			}
		}
	}
	if err = verifyTransfer(b, receipt); err != nil {
		t.Fatal(err)
	}
}
func TestLibraryTransferRejectsNewerCatalogAndBusySource(t *testing.T) {
	a, b := lifecycleLibraries(t)
	t.Run("insufficient space", func(t *testing.T) {
		override(t, &captureFreeMargin, int64(1<<62))
		if _, err := TransferLibrary(context.Background(), a, b, true, nil); err == nil || !strings.Contains(err.Error(), "free") {
			t.Fatal("transfer did not refuse insufficient SQLite working space", err)
		}
		if catalogCount(t, a, "messages") != 2 || catalogCount(t, b, "messages") != 0 {
			t.Fatal("space refusal changed library history")
		}
	})
	lock, err := fileLease(filepath.Join(filepath.Dir(a.CatalogPath), ".pharos-use.lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err == nil {
		t.Fatal("active source was moved")
	}
	lock.Close()
	c := reopenCatalog(t, a.CatalogPath)
	_, err = c.DB.Exec("UPDATE meta SET value=? WHERE key='schema_version'", fmt.Sprint(catalogSchemaVersion+1))
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatal("unsupported catalog was accepted", err)
	}
	if _, err = os.Stat(filepath.Join(b.CaptureRoot, ".imports")); !os.IsNotExist(err) {
		t.Fatal("target modified before compatibility check", err)
	}
}
func TestApplicationMCPConnectionAndLateResponse(t *testing.T) {
	a, b := lifecycleLibraries(t)
	m, err := newApplicationManager(context.Background(), filepath.Join(t.TempDir(), "Pharos.app"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.registry.register(a, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.registry.register(b, "")
	if err != nil {
		t.Fatal(err)
	}
	m.registry.Selected = second
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "search_work", "arguments": map[string]any{}}}
	if _, err = m.mcp(context.Background(), request); err == nil || !strings.Contains(err.Error(), "MCP is attached") {
		t.Fatal("wrong library was answered", err)
	}
	m.registry.Selected = first
	started, finish := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-finish
		writeJSON(w, map[string]any{"secret": "must not return"}, 200)
	}))
	defer server.Close()
	child := &applicationChild{done: make(chan struct{}), endpoint: applicationEndpoint{URL: server.URL, Token: "secret", Catalog: a.CatalogPath}}
	m.children[first] = child
	done := make(chan error, 1)
	go func() { _, err := m.mcp(context.Background(), request); done <- err }()
	<-started
	m.mu.Lock()
	m.registry.Selected = second
	m.generation++
	m.mu.Unlock()
	close(finish)
	if err = <-done; err == nil {
		t.Fatal("late response leaked after a library switch")
	}
	m.mu.Lock()
	m.registry.Selected = first
	delete(m.children, first)
	m.mu.Unlock()
	if _, err = m.mcp(context.Background(), request); err == nil {
		t.Fatal("offline MCP directly opened a catalog")
	}
}
func TestLibraryRecoveryCandidate(t *testing.T) {
	t.Run("all retained evidence", func(t *testing.T) {
		slowTest(t)
		a, b := lifecycleLibraries(t)
		writeSourceFile(t, filepath.Join(a.ArchiveRoot, "package", "patch.diff"), "retained patch")
		receipt, err := TransferLibrary(t.Context(), a, b, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		sourceDir := filepath.Join(b.CaptureRoot, currentHost().ID, "validation")
		files := map[string]string{"files/latest.json": "latest", "history/previous.json": "previous", "files/latest.db": "latest snapshot", "history/previous.db": "previous snapshot"}
		for rel, content := range files {
			writeSourceFile(t, filepath.Join(sourceDir, rel), content)
		}
		manifest := captureManifest{Version: captureManifestVersion, Host: currentHost(), Source: captureSourceInfo{Name: "validation", Kind: "canonical", Path: "/source"},
			Files: []*capturedFile{{Captured: "files/latest.json", Size: int64(len(files["files/latest.json"])), SHA256: hashBytes([]byte(files["files/latest.json"])),
				Previous: []capturedVersion{{Captured: "history/previous.json", Size: int64(len(files["history/previous.json"])), SHA256: hashBytes([]byte(files["history/previous.json"]))}}}},
			Snapshots: []*capturedSnapshot{{Captured: "files/latest.db", Size: int64(len(files["files/latest.db"])),
				Generations: []capturedVersion{{Captured: "history/previous.db", Size: int64(len(files["history/previous.db"]))}}}}}
		if err = durableJSON(filepath.Join(sourceDir, captureManifestName), manifest); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{b.ArchiveRoot, filepath.Join(filepath.Dir(b.Path), hostsDirName)} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		backup := filepath.Join(t.TempDir(), "backup")
		stubBackupDrives(t, canonical(filepath.Dir(backup)), nil)
		if run := runBackup(t, b, backup, false); run.State != "complete" {
			t.Fatal("evidence backup failed", run)
		}
		data, err := os.ReadFile(filepath.Join(backup, backupManifestName))
		var inventory backupManifest
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &inventory); err != nil {
			t.Fatal(err)
		}
		backupCaptures := ""
		for _, item := range inventory.Items {
			if item.Name == "captures" {
				backupCaptures = filepath.Join(backup, item.Path)
			}
		}
		if backupCaptures == "" {
			t.Fatal("backup has no capture tree")
		}
		for _, input := range []struct{ name, root, captures string }{{"copied library", filepath.Dir(b.Path), b.CaptureRoot}, {"backup", backup, backupCaptures}} {
			t.Run(input.name, func(t *testing.T) {
				if _, err := RecoverLibrary(t.Context(), input.root, filepath.Join(t.TempDir(), "intact"), nil); err != nil {
					t.Fatal("intact evidence rejected", err)
				}
				for _, rel := range []string{
					filepath.Join(currentHost().ID, "validation", "history", "previous.json"),
					filepath.Join(currentHost().ID, "validation", "history", "previous.db"),
					filepath.Join(".imports", receipt.ID, "history.sqlite3"),
					filepath.Join(".imports", receipt.ID, "preserved", "package", "patch.diff"),
					filepath.Join(".imports", receipt.ID, "transfer.json"),
				} {
					t.Run(filepath.ToSlash(rel), func(t *testing.T) {
						file := filepath.Join(input.captures, rel)
						data, err := os.ReadFile(file)
						if err != nil {
							t.Fatal(err)
						}
						if err = os.Remove(file); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.WriteFile(file, data, 0600); err != nil {
								t.Error(err)
							}
						})
						destination := filepath.Join(t.TempDir(), "incomplete")
						if _, err = RecoverLibrary(t.Context(), input.root, destination, nil); err == nil {
							t.Fatal("recovery published a candidate with missing retained evidence", rel)
						}
						if _, err = os.Stat(destination); !os.IsNotExist(err) {
							t.Fatal("failed recovery published its destination", err)
						}
					})
				}
			})
		}
	})
	a, _ := lifecycleLibraries(t)
	dest := filepath.Join(t.TempDir(), "backup")
	c := reopenCatalog(t, a.CatalogPath)
	_, err := c.DB.Exec(`INSERT INTO cloud_destinations(id,config_json,credential_json,imported_at) VALUES('recovery-cloud','{}','{"fixture":"upload-only"}','2026-10-23');
 INSERT INTO cloud_backup_schedules(destination_id,state_json) VALUES('recovery-cloud','{"enabled":true}');`)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}

	// The backup fixture's drive facts are isolated from this Mac.
	stubBackupDrives(t, canonical(filepath.Dir(dest)), nil)
	for _, dir := range []string{a.CaptureRoot, a.ArchiveRoot, filepath.Join(filepath.Dir(a.Path), hostsDirName)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	run := runBackup(t, a, dest, false)
	if run.State != "complete" {
		t.Fatalf("backup: %+v", run)
	}
	before, _, err := fileDigest(filepath.Join(dest, backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := RecoverLibrary(context.Background(), dest, filepath.Join(t.TempDir(), "candidate"), nil)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := readLibraryDescriptor(a)
	fresh, err := readLibraryDescriptor(candidate)
	if err != nil || old.Instance == fresh.Instance {
		t.Fatal("recovery reused writable identity", err)
	}
	if catalogCount(t, candidate, "messages") != 2 {
		t.Fatal("recovered history missing")
	}
	if catalogCount(t, candidate, "cloud_backup_schedules") != 0 || catalogCount(t, a, "cloud_backup_schedules") != 1 || catalogCount(t, candidate, "cloud_destinations") != 1 {
		t.Fatal("recovery must pause candidate cloud backups while keeping original settings intact")
	}
	after, _, err := fileDigest(filepath.Join(dest, backupManifestName))
	if err != nil || after != before {
		t.Fatal("backup was modified", err)
	}
	// A pending library whose payload was moved out retains small indexing
	// shortcuts. Recovering it must let surviving originals return on this Mac.
	path := filepath.Join(t.TempDir(), "surviving.json")
	canonicalExport(t, path, "Surviving original", "recapture after recovery")
	source := SourceConfig{Name: "surviving", Kind: "canonical", Path: path, Account: "local", Enabled: true}
	a.Sources = []SourceConfig{source}
	if err = capturePending(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(context.Background(), a, candidate, true, nil); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "source_record_states") == 0 {
		t.Fatal("fixture has no retained indexing checkpoint")
	}
	if run := runBackup(t, a, dest, false); run.State != "complete" {
		t.Fatalf("delivered pending backup: %+v", run)
	}
	for _, recoverySource := range []string{dest, filepath.Dir(a.Path)} {
		recovered, err := RecoverLibrary(context.Background(), recoverySource, filepath.Join(t.TempDir(), "pending-candidate"), nil)
		if err != nil {
			t.Fatal(err)
		}
		recovered.Sources = []SourceConfig{source}
		recovered.DeliveryTargetInstance = randomToken()
		if err = capturePending(context.Background(), recovered); err != nil {
			t.Fatal(err)
		}
		if catalogCount(t, recovered, "messages") != 1 {
			t.Fatal("recovery's stale indexing progress suppressed a surviving original")
		}
		if _, err = TransferLibrary(context.Background(), recovered, candidate, true, nil); err != nil {
			t.Fatal("recovered originals could not be delivered", err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dest, backupManifestName))
	var manifest backupManifest
	_ = json.Unmarshal(data, &manifest)
	manifest.Complete = false
	if err = durableJSON(filepath.Join(dest, backupManifestName), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = RecoverLibrary(context.Background(), dest, filepath.Join(t.TempDir(), "partial"), nil); err == nil {
		t.Fatal("unfinished backup accepted")
	}
}

func TestLibraryTransferPreservesNewerWriterAndMapsNativeIdentity(t *testing.T) {
	a, b := lifecycleLibraries(t)
	c := reopenCatalog(t, b.CatalogPath)
	if _, err := c.DB.Exec("ATTACH DATABASE ? AS fixture", a.CatalogPath); err != nil {
		t.Fatal(err)
	}
	_, err := c.DB.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at)
 SELECT 'legacy-work',source_kind,source_account,source_id,title,indexed_at FROM fixture.workspaces;
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin,origin_host_id)
 SELECT id,'legacy-work',provider,account,native_id,origin,origin_host_id FROM fixture.conversations;
 INSERT INTO messages(id,conversation_id,native_id,role,text,content_hash)
 SELECT id,conversation_id,native_id,role,'newer text','newer hash' FROM fixture.messages WHERE role='user';
 INSERT INTO copy_versions(host_id,workspace_id,conversation_id,origin,version,written_at)
 SELECT host_id,'legacy-work',conversation_id,origin,version+1,written_at FROM fixture.copy_versions;`)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(context.Background(), a, b, false, nil); err != nil {
		t.Fatal(err)
	}
	c = reopenCatalog(t, b.CatalogPath)
	var text string
	err = c.DB.QueryRow("SELECT text FROM messages WHERE role='user'").Scan(&text)
	c.Close()
	if err != nil || text != "newer text" || catalogCount(t, b, "messages") != 1 {
		t.Fatal("older copy rewrote or repopulated a newer writer", text, err)
	}
	c = reopenCatalog(t, a.CatalogPath)
	_, err = c.DB.Exec(`UPDATE copy_versions SET version=version+2; UPDATE messages SET text='latest text',content_hash='latest hash' WHERE role='user'`)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(context.Background(), a, b, false, nil); err != nil {
		t.Fatal(err)
	}
	c = reopenCatalog(t, b.CatalogPath)
	err = c.DB.QueryRow("SELECT text FROM messages WHERE role='user'").Scan(&text)
	c.Close()
	if err != nil || text != "latest text" || catalogCount(t, b, "messages") != 2 || catalogCount(t, b, "conversations") != 1 {
		t.Fatal("newer native copy did not map local identifiers", text, err)
	}
	t.Run("partial observations cannot replace owned metadata", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		if _, err := TransferLibrary(context.Background(), a, b, false, nil); err != nil {
			t.Fatal(err)
		}
		c := reopenCatalog(t, b.CatalogPath)
		_, err := c.DB.Exec(`INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin_host_id)
 SELECT 'other-session',id,'codex','local','other-session','another-host' FROM workspaces;
 DELETE FROM workspace_delivery_inputs;`)
		before := workspaceTitle(t, c, "work-1")
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		c = reopenCatalog(t, a.CatalogPath)
		_, err = c.DB.Exec(`UPDATE workspaces SET title='Partial observation'; UPDATE copy_versions SET version=version+1 WHERE conversation_id=''`)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = TransferLibrary(context.Background(), a, b, false, nil); err != nil {
			t.Fatal(err)
		}
		c = reopenCatalog(t, b.CatalogPath)
		defer c.Close()
		if title := workspaceTitle(t, c, "work-1"); title != before {
			t.Fatal("a partial copy advanced metadata beyond its last owned revision", before, title)
		}
		if catalogCount(t, b, "workspace_delivery_inputs") != 0 {
			t.Fatal("rejected metadata received a delivery fingerprint")
		}
	})
	t.Run("TL1 installation revisions", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		c := reopenCatalog(t, a.CatalogPath)
		_, err := c.DB.Exec(`INSERT INTO tl1_installations(installation_id,source_account,synced_at,source_version) VALUES('installation','local','2026-10-01',1);
 INSERT INTO tl1_tasks(installation_id,task_id,status) VALUES('installation','task','running');
 INSERT INTO tl1_review_findings(installation_id,finding_id,severity) VALUES('installation','finding','major');`)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = TransferLibrary(context.Background(), a, b, false, nil); err != nil {
			t.Fatal(err)
		}
		for index, revision := range []struct {
			version  int
			status   string
			severity string
		}{{2, "complete", "minor"}, {2, "running", "major"}, {1, "running", "major"}} {
			c := reopenCatalog(t, a.CatalogPath)
			_, err := c.DB.Exec(`UPDATE tl1_installations SET source_version=?`, revision.version)
			if err == nil {
				_, err = c.DB.Exec(`UPDATE tl1_tasks SET status=?`, revision.status)
			}
			if err == nil {
				_, err = c.DB.Exec(`UPDATE tl1_review_findings SET severity=?`, revision.severity)
			}
			c.Close()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = TransferLibrary(context.Background(), a, b, index == 2, nil); err != nil {
				t.Fatal(err)
			}
			c = reopenCatalog(t, b.CatalogPath)
			var version int
			var status, severity string
			err = c.DB.QueryRow(`SELECT source_version,status,severity FROM tl1_installations JOIN tl1_tasks USING(installation_id) JOIN tl1_review_findings USING(installation_id)`).Scan(&version, &status, &severity)
			c.Close()
			if err != nil || version != 2 || status != "complete" || severity != "minor" {
				t.Fatal("TL1 installation and child records did not retain the latest revision together", version, status, severity, err)
			}
		}
		if catalogCount(t, a, "tl1_tasks") != 0 {
			t.Fatal("verified TL1 move left source tasks behind")
		}
	})
}

func TestLibraryMoveResumesCleanupAndRejectsReplacedTarget(t *testing.T) {
	a, b := lifecycleLibraries(t)
	payload := filepath.Join(a.ArchiveRoot, "package", "evidence.txt")
	writeSourceFile(t, payload, "retained evidence")
	injected := fmt.Errorf("interrupted after source catalog publication")
	override(t, &libraryTransferFault, func(stage string) error {
		if stage == "source-catalog-emptied" {
			return injected
		}
		return nil
	})
	receipt, err := TransferLibrary(context.Background(), a, b, true, nil)
	if err != injected {
		t.Fatal("expected an interrupted cleanup", err)
	}
	if catalogCount(t, a, "messages") != 0 || readText(t, payload) != "retained evidence" {
		t.Fatal("interruption did not retain recoverable state")
	}
	d, err := readLibraryDescriptor(b)
	if err != nil {
		t.Fatal(err)
	}
	original := d.Instance
	d.Instance = randomToken()
	if err = durableJSON(descriptorPath(b), d); err != nil {
		t.Fatal(err)
	}
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err == nil {
		t.Fatal("restored target validated an obsolete receipt")
	}
	if _, err = os.Stat(payload); err != nil {
		t.Fatal("old receipt deleted evidence after target replacement")
	}
	d.Instance = original
	if err = durableJSON(descriptorPath(b), d); err != nil {
		t.Fatal(err)
	}
	libraryTransferFault = nil
	resumed, err := TransferLibrary(context.Background(), a, b, true, nil)
	if err != nil || resumed.ID != receipt.ID || resumed.State != "complete" {
		t.Fatal("move could not resume", err)
	}
	if _, err = os.Stat(payload); !os.IsNotExist(err) {
		t.Fatal("resumed cleanup kept delivered payload", err)
	}
}

func TestLibraryMoveKeepsChangedSource(t *testing.T) {
	a, b := lifecycleLibraries(t)
	changed := false
	_, err := TransferLibrary(context.Background(), a, b, true, func(phase string) {
		if phase == "Verifying destination acceptance" && !changed {
			changed = true
			c := reopenCatalog(t, a.CatalogPath)
			defer c.Close()
			if _, e := c.DB.Exec("UPDATE messages SET text='new source revision',content_hash='new hash' WHERE role='user'"); e != nil {
				t.Fatal(e)
			}
		}
	})
	if err == nil || catalogCount(t, a, "messages") != 2 {
		t.Fatal("source changed during transfer but was removed", err)
	}
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err != nil {
		t.Fatal("changed source could not resume with a fresh inventory", err)
	}
	if catalogCount(t, a, "messages") != 0 {
		t.Fatal("fresh delivery did not complete")
	}
}

func TestPendingCaptureDoesNotRecaptureDeliveredInputs(t *testing.T) {
	t.Run("workspace reassignment", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		file := filepath.Join(t.TempDir(), "workspace.json")
		moving := `{"id":"move-chat","provider":"codex","messages":[{"id":"q","role":"user","text":"moving conversation"}]}`
		staying := `{"id":"stay-chat","provider":"codex","messages":[{"id":"q","role":"user","text":"unchanged sibling"}]}`
		writeSourceFile(t, file, `{"workspaces":[{"id":"before","title":"Before","conversations":[`+moving+`]},{"id":"stable","title":"Stable","conversations":[`+staying+`]}]}`)
		a.Sources = []SourceConfig{{Name: "workspace", Kind: "canonical", Path: file, Account: "local", Enabled: true}}
		if err := capturePending(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if _, err := TransferLibrary(t.Context(), a, b, true, nil); err != nil {
			t.Fatal(err)
		}
		writeSourceFile(t, file, `{"workspaces":[{"id":"after","title":"After","conversations":[`+moving+`]},{"id":"stable","title":"Stable","conversations":[`+staying+`]}]}`)
		if err := capturePending(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if catalogCount(t, a, "conversations") != 1 {
			t.Fatal("workspace reassignment was suppressed or repopulated an unchanged sibling")
		}
		if _, err := TransferLibrary(t.Context(), a, b, true, nil); err != nil {
			t.Fatal(err)
		}
		c := reopenCatalog(t, b.CatalogPath)
		defer c.Close()
		rows, err := queryMaps(c.DB, `SELECT c.native_id,w.source_id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE c.native_id IN ('move-chat','stay-chat')`)
		if err != nil || len(rows) != 2 {
			t.Fatal("transferred conversations missing", rows, err)
		}
		for _, row := range rows {
			want := "stable"
			if row["native_id"] == "move-chat" {
				want = "after"
			}
			if row["source_id"] != want {
				t.Fatal("conversation workspace change did not reach archive", row)
			}
		}
	})
	t.Run("same-metadata rewrite before delivery", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		file := filepath.Join(t.TempDir(), "rewrite.json")
		canonicalExport(t, file, "Rewrite", "before")
		a.Sources = []SourceConfig{{Name: "rewrite", Kind: "canonical", Path: file, Account: "local", Enabled: true}}
		if err := capturePending(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		writeSourceFile(t, file, strings.Replace(readText(t, file), "before", "after!", 1))
		if err = os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err = capturePending(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		captured, err := capturedPathFor(a.CaptureRoot, currentHost().ID, "rewrite", file)
		if err != nil || !strings.Contains(readText(t, captured), "after!") {
			t.Fatal("pending capture kept old bytes after a same-size, same-mtime rewrite", err)
		}
		if _, err = TransferLibrary(t.Context(), a, b, true, nil); err != nil {
			t.Fatal(err)
		}
		c := reopenCatalog(t, b.CatalogPath)
		var messages string
		err = c.DB.QueryRow("SELECT group_concat(text) FROM messages").Scan(&messages)
		c.Close()
		if err != nil || !strings.Contains(messages, "after!") {
			t.Fatal("rewritten bytes were not delivered", messages, err)
		}
		if err = capturePending(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if catalogCount(t, a, "messages") != 0 {
			t.Fatal("delivered rewrite returned to pending")
		}
	})
	a, b := lifecycleLibraries(t)
	path := filepath.Join(t.TempDir(), "history.json")
	writeSourceFile(t, path, `{"workspaces":[{"id":"pending-session","title":"Pending","conversations":[{"id":"pending-chat","provider":"codex","messages":[{"id":"q","role":"user","text":"pending text"}]},{"id":"unchanged-chat","provider":"codex","messages":[{"id":"q2","role":"user","text":"already delivered sibling"}]}]}]}`)
	source := SourceConfig{Name: "pending", Kind: "canonical", Path: path, Account: "local", Enabled: true}
	a.Sources = []SourceConfig{source}
	if err := capturePending(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := TransferLibrary(context.Background(), a, b, true, nil); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "messages") != 0 {
		t.Fatal("pending history retained after delivery")
	}
	summary, err := Capture(context.Background(), a, []SourceConfig{source}, nil)
	if err != nil || !summary.OK {
		t.Fatal("unchanged capture failed", err)
	}
	if targets, err := captureTargets(a.CaptureRoot, nil, false, nil); err != nil || len(targets) != 0 {
		t.Fatal("unchanged source was recaptured", err)
	}
	var origin string
	c := reopenCatalog(t, b.CatalogPath)
	err = c.DB.QueryRow("SELECT COALESCE(origin,'') FROM conversations WHERE native_id='pending-chat'").Scan(&origin)
	c.Close()
	if err != nil {
		t.Fatal(err)
	}
	captured, err := capturedPathFor(b.CaptureRoot, currentHost().ID, "pending", path)
	if err != nil || !strings.Contains(readText(t, captured), "pending text") {
		t.Fatal("transferred capture evidence could not be read", origin, err)
	}
	// A rewritten file with the same size and timestamp is still a new input.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(readText(t, path), "pending text", "changed text", 1)
	writeSourceFile(t, path, contents)
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err = capturePending(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "messages") != 1 {
		t.Fatal("changed source must keep only its changed conversation pending")
	}
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err != nil {
		t.Fatal(err)
	}
	// Metadata changes carry information even when every message is identical.
	contents = strings.Replace(readText(t, path), `"id":"unchanged-chat"`, `"id":"unchanged-chat","harness_version_last":"2.0"`, 1)
	writeSourceFile(t, path, contents)
	if err = capturePending(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "messages") != 1 {
		t.Fatal("changed conversation metadata must become pending without repopulating its sibling")
	}
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err != nil {
		t.Fatal(err)
	}
	// Workspace metadata has a separate delivery input: it must travel without
	// bringing unchanged conversations back into the local pending library.
	contents = strings.Replace(readText(t, path), `"title":"Pending"`, `"title":"Renamed workspace","outcome":"complete","metadata":{"branch":"updated-branch"}`, 1)
	writeSourceFile(t, path, contents)
	if err = capturePending(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "messages") != 0 {
		t.Fatal("workspace metadata update repopulated delivered conversations")
	}
	c = reopenCatalog(t, a.CatalogPath)
	if got := workspaceTitle(t, c, "pending-session"); got != "Renamed workspace" {
		t.Fatal("workspace-only change did not become pending", got)
	}
	c.Close()
	if _, err = TransferLibrary(context.Background(), a, b, true, nil); err != nil {
		t.Fatal(err)
	}
	c = reopenCatalog(t, b.CatalogPath)
	var title, branch, outcome string
	err = c.DB.QueryRow("SELECT title,branch,outcome FROM workspaces WHERE source_id='pending-session'").Scan(&title, &branch, &outcome)
	c.Close()
	if err != nil || title != "Renamed workspace" || branch != "updated-branch" || outcome != "complete" {
		t.Fatal("workspace metadata did not reach the archive", title, branch, outcome, err)
	}
	if catalogCount(t, a, "workspaces") != 0 || catalogCount(t, b, "messages") != 4 {
		t.Fatal("workspace-only delivery changed conversation storage or failed to empty the pending workspace")
	}
	a.DeliveryTargetInstance = randomToken()
	if err = capturePending(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "messages") != 2 {
		t.Fatal("a new destination accepted obsolete delivery proof instead of recapturing surviving inputs")
	}
}

func TestApplicationReleasePreservesOtherLibraryAndLegacyMCPGate(t *testing.T) {
	a, b := lifecycleLibraries(t)
	application := filepath.Join(t.TempDir(), "Pharos.app")
	m, err := newApplicationManager(context.Background(), application)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.registry.register(a, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.registry.register(b, "")
	if err != nil {
		t.Fatal(err)
	}
	m.registry.library(second).Volume = "uuid:OTHER"
	m.registry.Selected = second
	m.registry.DeliverySource = second
	m.registry.DeliveryTarget = first
	if err = m.registry.save(); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(application, "Contents", "MacOS", "pharos")
	if got := managedApplicationForConfig(executable, a); got != application {
		t.Fatal("legacy registration can bypass its app's MCP gate", got)
	}
	if got := managedApplicationForConfig("/usr/local/bin/pharos", a); got != "" {
		t.Fatal("explicit standalone CLI mode was taken over", got)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/app/release-volume", strings.NewReader(`{"volume":"uuid:TEST-VOLUME"}`))
	m.api(response, request)
	if response.Code != 200 || m.registry.Selected != second || m.registry.MCP != first || m.suspended[second] || !m.suspended[first] {
		t.Fatal("volume release affected another library", response.Body.String())
	}
	if m.registry.DeliverySource != second || m.registry.DeliveryTarget != first {
		t.Fatal("releasing archive stopped local pending capture")
	}
}
