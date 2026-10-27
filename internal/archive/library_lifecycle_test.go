package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// A private idle child makes service-stop behavior observable without opening
// a real library server or sharing resources with the user's application.
func lifecycleChild(t *testing.T) *applicationChild {
	t.Helper()
	cmd := exec.Command("/usr/bin/tail", "-f", "/dev/null")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &applicationChild{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(child.done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-child.done })
	return child
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
func TestLibraryTransferRetainedEvidence(t *testing.T) {
	useHost(t, "host-a")
	a, b := lifecycleLibraries(t)
	sourceDir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, sourceDir)
	exec(addConductorSession("portable", 2)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Account: "local", Path: sourceDir, Enabled: true, Options: map[string]any{"sqlite_record_capture": true}}
	a.Sources = []SourceConfig{source}
	if run := runCapture(t, a); !run.OK {
		t.Fatal(run)
	}
	c := reopenCatalog(t, a.CatalogPath)
	c.setCaptureRoot(a.CaptureRoot)
	recordTestReplay(t, a, source.Name, c)
	var conversation string
	if err := c.DB.QueryRow("SELECT id FROM conversations WHERE native_id='conductor:portable'").Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB,
		`INSERT INTO machines(id,label,kind) VALUES('portable-mac','Portable Mac','device')`,
		`INSERT INTO host_machines(host_id,machine_id) VALUES('host-a','portable-mac')`,
		`INSERT INTO origin_observations(conversation_id,evidence_key,channel,machine_id,execution_machine_id,priority,evidence,channel_priority,machine_priority,execution_priority) VALUES('`+conversation+`','portable-test','desktop','portable-mac','portable-mac',100,'explicit fixture',100,100,100)`,
	)
	evidence, err := queryMaps(c.DB, "SELECT * FROM copy_evidence ORDER BY conversation_id")
	if err != nil || len(evidence) == 0 {
		t.Fatal("fixture did not associate raw evidence", err)
	}
	c.Close()
	receipt, err := TransferLibrary(t.Context(), a, b, true, nil)
	if err != nil {
		t.Fatal("move row evidence", err)
	}
	if catalogCount(t, a, "messages") != 0 || catalogCount(t, a, "copy_evidence") != 0 {
		t.Fatal("move left history in pending")
	}
	c = reopenCatalog(t, b.CatalogPath)
	actual, err := queryMaps(c.DB, "SELECT * FROM copy_evidence ORDER BY conversation_id")
	if err != nil || jsonText(actual) != jsonText(evidence) {
		t.Fatal("move lost raw authority", actual, evidence, err)
	}
	origins, err := c.conversationOrigins(t.Context())
	if err != nil || origins[conversation]["channel"] != "desktop" || origins[conversation]["machine_id"] != "portable-mac" {
		t.Fatal("move lost origin observations", origins[conversation], err)
	}
	// Equal source versions with different raw bytes must stay rejected even
	// after a transfer, while a newer extractor can reuse the same evidence.
	var rejected bool
	err = c.writeTransaction(t.Context(), "check transferred evidence", func(tx *sql.Tx) error {
		row := actual[len(actual)-1]
		var e error
		rejected, e = evidenceVersionRejected(tx, firstString(row["host_id"]), firstString(row["workspace_id"]), firstString(row["conversation_id"]), firstString(row["origin"]), integer(row["version"]), integer(row["version"]), true, "different-raw-identity")
		return e
	})
	c.Close()
	if err != nil || !rejected {
		t.Fatal("transferred authority accepted conflicting equal-version evidence", err)
	}
	imported := filepath.Join(b.CaptureRoot, ".imports", receipt.ID, "captures")
	if err = validateCaptureEvidence(t.Context(), imported); err != nil {
		t.Fatal("moved record closure is invalid", err)
	}
	recovered, err := RecoverLibrary(t.Context(), filepath.Dir(b.Path), filepath.Join(t.TempDir(), "recovered"), nil)
	if err != nil {
		t.Fatal("recover moved row evidence", err)
	}
	if catalogCount(t, recovered, "copy_evidence") != len(evidence) {
		t.Fatal("recovery lost evidence authority")
	}
	// A logical-only source still has a closure to validate without any native
	// database snapshot. Missing row chunks must never publish a usable library.
	logical, _ := lifecycleLibraries(t)
	logical.Sources = []SourceConfig{source}
	if err = acquireConductorSession(t.Context(), logical.CaptureRoot, source, filepath.Join(sourceDir, "conductor.db"), "portable", func(WorkspaceRecord) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = validateCaptureEvidence(t.Context(), logical.CaptureRoot); err != nil {
		t.Fatal("logical-only evidence rejected", err)
	}
	if _, err = RecoverLibrary(t.Context(), filepath.Dir(logical.Path), filepath.Join(t.TempDir(), "logical-recovered"), nil); err != nil {
		t.Fatal("logical-only recovery failed", err)
	}
	store := recordTestStore(t, logical, source.Name)
	revisions, err := store.revisions()
	if err != nil || len(revisions) == 0 {
		t.Fatal(revisions, err)
	}
	var chunk recordObject
	for _, table := range revisions[0].Revision.Tables {
		if len(table.Chunks) > 0 {
			chunk = table.Chunks[0]
			break
		}
	}
	if chunk.Hash == "" {
		t.Fatal("fixture has no row chunk")
	}
	if err = os.Remove(store.objectPath(chunk.Hash)); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "incomplete")
	if _, err = RecoverLibrary(t.Context(), filepath.Dir(logical.Path), dest, nil); err == nil {
		t.Fatal("recovery accepted a missing logical row chunk")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("failed logical recovery published a library", err)
	}
}

func TestLibraryTransferCopyMoveAndPreferences(t *testing.T) {
	t.Run("legacy operational column order", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		c := reopenCatalog(t, a.CatalogPath)
		execTx(t, c.DB, `ALTER TABLE source_states DROP COLUMN index_version`, `ALTER TABLE source_states ADD COLUMN index_version TEXT`,
			`INSERT INTO source_states(host_id,source_name,kind,capability,fingerprint,coverage,pending_count,updated_at,index_version) VALUES('legacy','one','codex','incremental','fingerprint-a','complete',7,'2026-10-20',NULL),('legacy','two','claude','incremental','fingerprint-b','pending',3,'2026-10-21','parser-v1')`)
		before, err := queryMaps(c.DB, "SELECT * FROM source_states ORDER BY source_name")
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = TransferLibrary(t.Context(), a, b, true, nil); err != nil {
			t.Fatal("move upgraded legacy source states", err)
		}
		c = reopenCatalog(t, a.CatalogPath)
		defer c.Close()
		after, err := queryMaps(c.DB, "SELECT * FROM source_states ORDER BY source_name")
		if err != nil || jsonText(before) != jsonText(after) {
			t.Fatal("move shifted legacy source-state columns", before, after, err)
		}
	})
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
	t.Run("unpublished staging", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		partial := filepath.Join(a.CaptureRoot, ".imports", ".stage-interrupted", "partial.txt")
		writeSourceFile(t, partial, "unpublished output")
		receipt, err := TransferLibrary(t.Context(), a, b, false, nil)
		if err != nil {
			t.Fatal("interrupted staging blocked a later transfer", err)
		}
		copied := filepath.Join(b.CaptureRoot, ".imports", receipt.ID, "captures", ".imports", ".stage-interrupted", "partial.txt")
		if _, err = os.Stat(copied); !os.IsNotExist(err) {
			t.Fatal("transfer copied unpublished staging", err)
		}
		candidate, err := RecoverLibrary(t.Context(), filepath.Dir(a.Path), filepath.Join(t.TempDir(), "recovered"), nil)
		if err != nil {
			t.Fatal("unpublished staging blocked recovery", err)
		}
		if _, err = os.Stat(filepath.Join(candidate.CaptureRoot, ".imports", ".stage-interrupted")); !os.IsNotExist(err) {
			t.Fatal("recovery copied unpublished staging", err)
		}
		writeSourceFile(t, filepath.Join(a.CaptureRoot, ".imports", hashBytes([]byte("published")), "history.sqlite3"), "broken published import")
		if err = validateCaptureEvidence(t.Context(), a.CaptureRoot); err == nil {
			t.Fatal("ignored a broken published import")
		}
	})
	a, b := lifecycleLibraries(t)
	t.Run("separate storage", func(t *testing.T) {
		for _, sourcePath := range []string{a.CaptureRoot, a.ArchiveRoot} {
			for _, field := range []string{"captures", "preserved", "catalog", "config"} {
				t.Run(filepath.Base(sourcePath)+"/"+field, func(t *testing.T) {
					nested := b
					path := filepath.Join(sourcePath, "nested")
					switch field {
					case "captures":
						nested.CaptureRoot = path
					case "preserved":
						nested.ArchiveRoot = path
					case "catalog":
						nested.CatalogPath = filepath.Join(path, "catalog.sqlite3")
					case "config":
						nested.Path = filepath.Join(path, libraryConfigName)
					}
					for _, pair := range [][2]Config{{a, nested}, {nested, a}} {
						if _, err := TransferLibrary(t.Context(), pair[0], pair[1], true, nil); err == nil || !strings.Contains(err.Error(), "separate storage") {
							t.Fatal("overlapping storage accepted", field, err)
						}
					}
				})
			}
		}
		alias := filepath.Join(t.TempDir(), "preserved-link")
		if err := os.MkdirAll(a.ArchiveRoot, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(a.ArchiveRoot, alias); err != nil {
			t.Fatal(err)
		}
		nested := b
		nested.CaptureRoot = filepath.Join(alias, "new-library", "captures")
		if _, err := TransferLibrary(t.Context(), a, nested, false, nil); err == nil || !strings.Contains(err.Error(), "separate storage") {
			t.Fatal("symlink overlap accepted", err)
		}
		if _, err := os.Stat(filepath.Join(b.CaptureRoot, ".imports")); !os.IsNotExist(err) {
			t.Fatal("overlap refusal created staging output", err)
		}
	})
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
	if _, err = m.mcp(context.Background(), request); err == nil || strings.Contains(err.Error(), "working with") {
		t.Fatal("an offline selection was described as a connected library", err)
	}
	m.children[second] = &applicationChild{done: make(chan struct{})}
	if _, err = m.mcp(context.Background(), request); err == nil || !strings.Contains(err.Error(), "working with") {
		t.Fatal("a different connected library was not identified", err)
	}
	delete(m.children, second)
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

func TestApplicationSetupWithoutLibrary(t *testing.T) {
	t.Run("switch and open preserve the MCP assignment", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		application := filepath.Join(t.TempDir(), "Pharos.app")
		app, err := newApplicationManager(t.Context(), application)
		if err != nil {
			t.Fatal(err)
		}
		first, err := app.registry.register(a, "Local history")
		if err != nil {
			t.Fatal(err)
		}
		second, err := app.registry.register(b, "Archive")
		if err != nil {
			t.Fatal(err)
		}
		app.registry.Selected = first
		app.children[second] = &applicationChild{done: make(chan struct{})}
		request := func(action string, body map[string]any) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			app.api(w, httptest.NewRequest(http.MethodPost, "/api/app/"+action, strings.NewReader(jsonText(body))))
			return w
		}
		if w := request("connect", map[string]any{"id": second}); w.Code != http.StatusOK || app.registry.Selected != second {
			t.Fatal("switch failed", w.Body.String())
		}
		if w := request("connect", map[string]any{"id": "missing"}); w.Code == http.StatusOK || app.registry.Selected != second {
			t.Fatal("failed switch lost the selected library", w.Body.String())
		}
		if w := request("open", map[string]any{"path": filepath.Dir(a.Path)}); w.Code != http.StatusOK || app.registry.Selected != first || len(app.registry.Libraries) != 2 {
			t.Fatal("opening a known library duplicated it or failed to select it", w.Body.String())
		}
		if w := request("create", map[string]any{"name": "Research", "path": filepath.Join(t.TempDir(), "Research"), "location": "mac"}); w.Code != http.StatusOK || len(app.registry.Libraries) != 3 || app.registry.Selected == first {
			t.Fatal("creating another library failed", w.Body.String())
		}
		saved, err := loadInstallation(application)
		if err != nil || saved.Selected != app.registry.Selected || saved.MCP != first {
			t.Fatal("selection did not persist independently of MCP", saved, err)
		}
		app.busy = true
		for _, action := range []string{"create", "open", "connect"} {
			w := request(action, map[string]any{"name": "Blocked", "path": filepath.Dir(b.Path), "id": second})
			if w.Code != http.StatusConflict || app.registry.Selected != saved.Selected || len(app.registry.Libraries) != 3 {
				t.Fatal("an active operation allowed changing libraries", action, w.Body.String())
			}
		}
	})
	t.Run("managed pages expose library controls", func(t *testing.T) {
		a, _ := lifecycleLibraries(t)
		app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
		if err != nil {
			t.Fatal(err)
		}
		id, err := app.registry.register(a, `History </script><script>bad()</script>`)
		if err != nil {
			t.Fatal(err)
		}
		app.registry.Selected = id
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeHTML(w, appHTML()) }))
		defer server.Close()
		app.children[id] = &applicationChild{done: make(chan struct{}), endpoint: applicationEndpoint{URL: server.URL}}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/settings/libraries", nil)
		r.Header.Set("Authorization", "Bearer "+app.token)
		app.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "window.pharosManagedLibrary=") || !strings.Contains(body, "/assets/managed-library.js") || !strings.Contains(body, `data-settings-route="/settings/libraries"`) || strings.Contains(body, "<script>bad()") {
			t.Fatal("managed page omitted library controls or embedded an unsafe name", w.Code)
		}
		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodGet, "/api/share?id=work", nil)
		r.Header.Set("Authorization", "Bearer "+app.token)
		app.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != appHTML() {
			t.Fatal("HTML exports received this Mac's library controls or metadata")
		}
	})
	t.Run("unavailable initial library leaves chooser available", func(t *testing.T) {
		for _, kind := range []string{"missing", "newer"} {
			t.Run(kind, func(t *testing.T) {
				initial := filepath.Join(t.TempDir(), "unmounted", "library.toml")
				var config Config
				if kind == "newer" {
					config, _ = lifecycleLibraries(t)
					initial = config.Path
					c := reopenCatalog(t, config.CatalogPath)
					execSQL(t, c.DB, "UPDATE meta SET value='"+fmt.Sprint(catalogSchemaVersion+1)+"' WHERE key='schema_version'")
					c.Close()
				}
				root := t.TempDir()
				endpoint := filepath.Join(root, "endpoint.json")
				cmd := exec.Command(os.Args[0], "-test.run=^TestApplicationStartupHelper$")
				cmd.Env = append(os.Environ(), "PHAROS_APP_TEST_INITIAL="+initial, "PHAROS_APP_TEST_PATH="+filepath.Join(root, "Pharos.app"), "PHAROS_APP_TEST_SUPPORT="+filepath.Join(root, "support"), "PHAROS_APP_TEST_ENDPOINT="+endpoint)
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				var processErr error
				go func() { processErr = cmd.Wait(); close(done) }()
				t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
				deadline := time.NewTimer(10 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				var ep applicationEndpoint
				for ep.URL == "" {
					if data, err := os.ReadFile(endpoint); err == nil {
						if err = json.Unmarshal(data, &ep); err != nil {
							t.Fatal(err)
						}
						break
					}
					select {
					case <-done:
						t.Fatal("initial library prevented startup", processErr)
					case <-deadline.C:
						t.Fatal("application did not publish its endpoint")
					case <-tick.C:
					}
				}
				client := &http.Client{Timeout: 2 * time.Second}
				request, _ := http.NewRequest(http.MethodGet, ep.URL+"/api/app/state", nil)
				request.Header.Set("Authorization", "Bearer "+ep.Token)
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				var state map[string]any
				err = json.NewDecoder(response.Body).Decode(&state)
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || state["selected"] != "" || len(state["libraries"].([]any)) != 0 || state["job"].(map[string]any)["state"] != "paused" {
					t.Fatal("startup did not expose library recovery choices", state, err)
				}
				request, _ = http.NewRequest(http.MethodGet, ep.URL+"/app", nil)
				request.Header.Set("Authorization", "Bearer "+ep.Token)
				response, err = client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatal("setup is inaccessible after initial-library failure", response.StatusCode)
				}
				if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
					if processErr != nil {
						t.Fatal(processErr)
					}
				case <-deadline.C:
					t.Fatal("application failed to stop")
				}
				if kind == "newer" {
					db, err := openReadOnlySQLite(config.CatalogPath)
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					var version string
					if err = db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version); err != nil || version != fmt.Sprint(catalogSchemaVersion+1) {
						t.Fatal("startup modified an incompatible catalog", version, err)
					}
				}
			})
		}
	})
	t.Run("edited default local folder", func(t *testing.T) {
		support := t.TempDir()
		t.Setenv("PHAROS_SUPPORT_DIR", support)
		override(t, &libraryVolumeIdentity, testIdentity)
		app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(support, "Libraries", "My Library")
		w := httptest.NewRecorder()
		app.api(w, httptest.NewRequest(http.MethodPost, "/api/app/create", strings.NewReader(jsonText(map[string]any{"name": "My Library", "location": "mac", "path": path}))))
		if w.Code != http.StatusOK || len(app.registry.Libraries) != 1 {
			t.Fatal("edited default failed on fresh installation", w.Body.String())
		}
		if _, err = os.Stat(filepath.Join(path, libraryConfigName)); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "missing-drive", "Libraries", "Other")
		w = httptest.NewRecorder()
		app.api(w, httptest.NewRequest(http.MethodPost, "/api/app/create", strings.NewReader(jsonText(map[string]any{"name": "Other", "location": "ssd", "path": missing}))))
		if w.Code != http.StatusBadRequest {
			t.Fatal("created a missing external parent", w.Body.String())
		}
		if _, err = os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
			t.Fatal("external refusal created a mount-like parent", err)
		}
	})
	t.Run("rejected transfer preserves active services", func(t *testing.T) {
		a, _ := lifecycleLibraries(t)
		app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
		if err != nil {
			t.Fatal(err)
		}
		id, err := app.registry.register(a, "")
		if err != nil {
			t.Fatal(err)
		}
		child := lifecycleChild(t)
		app.children[id] = child
		for _, body := range []map[string]any{{"source": id, "target": id}, {"source": id, "target": "unknown"}} {
			w := httptest.NewRecorder()
			app.startOperation(w, httptest.NewRequest(http.MethodPost, "/api/app/transfer", nil), body)
			if w.Code != http.StatusBadRequest {
				t.Fatal(w.Code, w.Body.String())
			}
			select {
			case <-child.done:
				t.Fatal("rejected transfer stopped a running library")
			default:
			}
			if app.children[id] != child || app.busy || app.generation != 0 {
				t.Fatal("rejected transfer changed connection state")
			}
		}
	})
	m, err := newApplicationManager(context.Background(), filepath.Join(t.TempDir(), "Pharos.app"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/app", "/assets/query-tables.css", "/assets/ask-pharos.css", "/assets/managed-library.js", "/assets/managed-library.css"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		m.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous setup resource %s: %d", path, response.Code)
		}
		request.Header.Set("Authorization", "Bearer "+m.token)
		response = httptest.NewRecorder()
		m.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("setup resource %s requires a library: %d", path, response.Code)
		}
	}
	if len(m.children) != 0 || len(m.registry.Libraries) != 0 {
		t.Fatal("showing setup started a library")
	}
	t.Run("browser folder picker", func(t *testing.T) {
		calls := 0
		chosen := "/Volumes/My SSD/quoted \"folder\"\nname"
		var pickerError error
		override(t, &applicationFolderPicker, func(ctx context.Context) (string, error) {
			calls++
			return chosen, pickerError
		})
		request := func(method, origin, token string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, "/api/app/choose-folder", strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			m.ServeHTTP(w, r)
			return w
		}
		for _, input := range []struct{ method, origin, token string }{
			{http.MethodPost, "", "wrong"},
			{http.MethodPost, "http://other.example", m.token},
			{http.MethodGet, "", m.token},
		} {
			if w := request(input.method, input.origin, input.token); w.Code == http.StatusOK || calls != 0 {
				t.Fatal("picker opened without an authorized same-origin POST", w.Code, calls)
			}
		}
		for _, path := range []string{chosen, ""} {
			chosen = path
			w := request(http.MethodPost, "", m.token)
			var result map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result["path"] != path {
				t.Fatal("selection or cancellation was not preserved", w.Body.String(), err)
			}
		}
		pickerError = fmt.Errorf("picker unavailable")
		if w := request(http.MethodPost, "", m.token); w.Code != http.StatusInternalServerError {
			t.Fatal("picker failure reported success", w.Body.String())
		}
		callsBefore := calls
		m.pickerMu.Lock()
		w := request(http.MethodPost, "", m.token)
		m.pickerMu.Unlock()
		if w.Code != http.StatusConflict || calls != callsBefore {
			t.Fatal("opened a second picker", w.Code, calls)
		}
		if len(m.children) != 0 || len(m.registry.Libraries) != 0 {
			t.Fatal("folder selection created or started a library")
		}
	})
	t.Run("explicit storage choices", func(t *testing.T) {
		support := t.TempDir()
		t.Setenv("PHAROS_SUPPORT_DIR", support)
		override(t, &libraryVolumeIdentity, testIdentity)
		app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
		if err != nil {
			t.Fatal(err)
		}
		// Exercise a portable app without creating or touching a real volume.
		app.registry.Application = "/Volumes/Portable/Pharos.app"
		response := httptest.NewRecorder()
		app.api(response, httptest.NewRequest(http.MethodGet, "/api/app/state", nil))
		var state map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state["local_library_parent"] != filepath.Join(support, "Libraries") {
			t.Fatal("wrong local folder", state)
		}
		response = httptest.NewRecorder()
		app.api(response, httptest.NewRequest(http.MethodPost, "/api/app/create", strings.NewReader(`{"name":"Archive","location":"ssd"}`)))
		if response.Code != http.StatusBadRequest || len(app.registry.Libraries) != 0 {
			t.Fatal("external choice silently fell back to default storage", response.Body.String())
		}
		response = httptest.NewRecorder()
		app.api(response, httptest.NewRequest(http.MethodPost, "/api/app/create", strings.NewReader(`{"name":"Local history","location":"mac"}`)))
		if response.Code != http.StatusOK || len(app.registry.Libraries) != 1 {
			t.Fatal("local setup failed", response.Body.String())
		}
		config := app.registry.Libraries[0].Config
		if config != filepath.Join(support, "Libraries", "Local history", libraryConfigName) {
			t.Fatal("portable app overrode the chosen Mac storage", config)
		}
		if len(app.children) != 0 {
			t.Fatal("setup started capture before source approval")
		}
	})

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
	configText := readText(t, a.Path) + `
[repositories.aliases]
"github.com/acme/old" = "github.com/acme/new"
[repositories.separate]
"repo_keep" = true
`
	if err := os.WriteFile(a.Path, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	assertRepositoryChoices := func(t *testing.T, config Config) {
		t.Helper()
		if config.RepositoryAliases["github.com/acme/old"] != "github.com/acme/new" || len(config.RepositorySeparate) != 1 || config.RepositorySeparate[0] != "repo_keep" {
			t.Fatal("recovery lost repository identity choices", config.RepositoryAliases, config.RepositorySeparate)
		}
		loaded, err := LoadConfig(config.Path)
		if err != nil || loaded.RepositoryAliases["github.com/acme/old"] != "github.com/acme/new" || len(loaded.RepositorySeparate) != 1 {
			t.Fatal("recovered repository choices did not survive reload", err)
		}
	}
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
	assertRepositoryChoices(t, candidate)
	copied, err := RecoverLibrary(t.Context(), filepath.Dir(a.Path), filepath.Join(t.TempDir(), "copy-candidate"), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertRepositoryChoices(t, copied)
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
	if err = capturePendingLibrary(context.Background(), a); err != nil {
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
		if err = capturePendingLibrary(context.Background(), recovered); err != nil {
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

func remapLifecycleIdentities(t *testing.T, config Config, prefix string) {
	t.Helper()
	c := reopenCatalog(t, config.CatalogPath)
	defer c.Close()
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("PRAGMA defer_foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ table, column string }{
		{"messages", "id"}, {"workspace_names", "authored_message_id"},
		{"conversation_documents", "initiation_message_id"}, {"conversation_documents", "outcome_message_id"},
		{"agent_sessions", "delegation_message_id"}, {"agent_session_messages", "message_id"},
		{"tool_calls", "call_message_id"}, {"tool_calls", "result_message_id"},
		{"skill_usages", "evidence_message_id"}, {"skill_usages", "body_message_id"},
		{"agent_sessions", "id"}, {"agent_sessions", "parent_id"},
		{"agent_session_messages", "agent_session_id"}, {"agent_session_usage", "agent_session_id"},
		{"model_requests", "agent_session_id"}, {"tool_calls", "agent_session_id"}, {"skill_usages", "agent_session_id"},
	} {
		if _, err = tx.Exec("UPDATE "+item.table+" SET "+item.column+"=?||"+item.column+" WHERE "+item.column+" IS NOT NULL", prefix); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryTransferPreservesNewerWriterAndMapsNativeIdentity(t *testing.T) {
	t.Run("accepted session facts replace prior revision", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		if _, err := TransferLibrary(t.Context(), a, b, false, nil); err != nil {
			t.Fatal(err)
		}
		c := reopenCatalog(t, b.CatalogPath)
		_, err := c.DB.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider)
 SELECT 'obsolete',workspace_id,id,'obsolete','subagent',provider FROM conversations;
 INSERT INTO agent_session_usage(agent_session_id,usage_hour,attribution,total_tokens)
 SELECT id,'2026-01-01T00:00:00Z','request',999 FROM agent_sessions WHERE native_id='main';`)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		c = reopenCatalog(t, a.CatalogPath)
		_, err = c.DB.Exec("UPDATE copy_versions SET version=version+1")
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = TransferLibrary(t.Context(), a, b, false, nil); err != nil {
			t.Fatal(err)
		}
		if catalogCount(t, b, "agent_sessions") != catalogCount(t, a, "agent_sessions") || catalogCount(t, b, "agent_session_usage") != catalogCount(t, a, "agent_session_usage") {
			t.Fatal("accepted session facts retained an obsolete revision")
		}
	})
	t.Run("legacy stored identities", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		remapLifecycleIdentities(t, a, "legacy-")
		var err error
		for _, divergent := range []bool{false, true} {
			if divergent {
				remapLifecycleIdentities(t, b, "destination-")
				for _, input := range []struct {
					config Config
					id     string
				}{{a, "source-only"}, {b, "target-only"}} {
					c := reopenCatalog(t, input.config.CatalogPath)
					_, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,text,content_hash)
 SELECT ?,id,?,'user',?,'new' FROM conversations;`, input.id, input.id, input.id)
					c.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := TransferLibrary(t.Context(), a, b, false, nil); err != nil {
				t.Fatal("valid legacy history rejected", divergent, err)
			}
			c := reopenCatalog(t, b.CatalogPath)
			var links int
			err = c.DB.QueryRow(`SELECT COUNT(*) FROM agent_session_messages am JOIN messages m ON m.id=am.message_id JOIN agent_sessions a ON a.id=am.agent_session_id`).Scan(&links)
			c.Close()
			want := 2
			if divergent {
				want = 4
			}
			if err != nil || links != want {
				t.Fatal("import lost stored message/session references", links, want, err)
			}
		}
	})
	t.Run("Claude group delegation", func(t *testing.T) {
		a, b := lifecycleLibraries(t)
		c := reopenCatalog(t, a.CatalogPath)
		root := ConversationRecord{NativeID: "root", Provider: "claude", Model: "opus", Account: "local", StartedAt: "2026-09-20T10:00:00Z", Messages: []MessageRecord{
			{NativeID: "request", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"request","usage":{"input_tokens":90,"output_tokens":10}}}`},
			{NativeID: "delegate", Kind: "delegation", CallID: "tool-1", Text: `{}`},
			{NativeID: "result", Kind: "delegation_result", CallID: "tool-1", Text: `{"details":{"agentId":"child1"}}`},
			{NativeID: "claim", Kind: "metadata", Text: `{"type":"cost-state","modelUsage":{"opus":{"input_tokens":270,"output_tokens":30}}}`},
		}}
		child := ConversationRecord{NativeID: "root:subagent:agent-child1", ParentNativeID: "root", Provider: "claude", Model: "opus", Account: "local", AgentDepth: 1, Messages: []MessageRecord{
			{NativeID: "child-request", Kind: "metadata", Model: "opus", Text: `{"type":"assistant","message":{"id":"child-request","usage":{"input_tokens":180,"output_tokens":20}}}`},
		}}
		tx, err := c.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = ingestWorkspace(tx, WorkspaceRecord{SourceID: "claude-group", SourceKind: "claude", Account: "local", Conversations: []ConversationRecord{root, child}}, false)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		c.Close()
		assertGroup := func(config Config) {
			c := reopenCatalog(t, config.CatalogPath)
			defer c.Close()
			var status, linked string
			var tokens int
			if err := c.DB.QueryRow(`SELECT usage_status,child_conversation_id FROM agent_sessions WHERE native_id='tool-1'`).Scan(&status, &linked); err != nil {
				t.Fatal(err)
			}
			if err := c.DB.QueryRow(`SELECT SUM(total_tokens) FROM agent_sessions WHERE provider='claude'`).Scan(&tokens); err != nil {
				t.Fatal(err)
			}
			if status != "in-child-conversation" || linked == "" || tokens != 300 {
				t.Fatal("delegation or reconciled usage lost", status, linked, tokens)
			}
			var broken int
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM model_requests r LEFT JOIN agent_sessions a ON a.id=r.agent_session_id WHERE a.id IS NULL`).Scan(&broken); err != nil || broken != 0 {
				t.Fatal("rebuilt tool usage lost stored session references", broken, err)
			}
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls t LEFT JOIN messages m ON m.id=t.call_message_id WHERE t.call_message_id IS NOT NULL AND m.id IS NULL`).Scan(&broken); err != nil || broken != 0 {
				t.Fatal("rebuilt tool call lost stored message references", broken, err)
			}
		}
		remapLifecycleIdentities(t, a, "legacy-")
		assertGroup(a)
		for _, divergent := range []bool{false, true} {
			if divergent {
				for _, input := range []struct {
					config Config
					id     string
				}{{a, "source-extra"}, {b, "target-extra"}} {
					c := reopenCatalog(t, input.config.CatalogPath)
					_, err := c.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,text,content_hash) SELECT ?,id,?,'user',?,'extra' FROM conversations WHERE native_id='root'`, input.id, input.id, input.id)
					c.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := TransferLibrary(t.Context(), a, b, false, nil); err != nil {
				t.Fatal(err)
			}
			assertGroup(b)
		}
	})

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
		if err := capturePendingLibrary(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if _, err := TransferLibrary(t.Context(), a, b, true, nil); err != nil {
			t.Fatal(err)
		}
		writeSourceFile(t, file, `{"workspaces":[{"id":"after","title":"After","conversations":[`+moving+`]},{"id":"stable","title":"Stable","conversations":[`+staying+`]}]}`)
		if err := capturePendingLibrary(t.Context(), a); err != nil {
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
		if err := capturePendingLibrary(t.Context(), a); err != nil {
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
		if err = capturePendingLibrary(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		captured, err := capturedPathFor(a.CaptureRoot, currentHost().ID, "rewrite", file)
		if err != nil || !strings.Contains(readText(t, captured), "after!") {
			t.Fatal("pending capture kept old bytes after a same-size, same-mtime rewrite", err)
		}
		manifest := readCaptureManifest(t, a, "rewrite")
		entry := manifest.Files[0]
		if entry.MTimeNS != info.ModTime().UnixNano() || entry.ObservedNS <= entry.MTimeNS || len(entry.Previous) == 0 || entry.ObservedNS <= entry.Previous[0].ObservedNS {
			t.Fatal("verified rewrite did not retain its native metadata and newer observation", entry)
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
		if err = capturePendingLibrary(t.Context(), a); err != nil {
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
	if err := capturePendingLibrary(context.Background(), a); err != nil {
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
	if err = capturePendingLibrary(context.Background(), a); err != nil {
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
	if err = capturePendingLibrary(context.Background(), a); err != nil {
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
	if err = capturePendingLibrary(context.Background(), a); err != nil {
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
	if err = capturePendingLibrary(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if catalogCount(t, a, "messages") != 2 {
		t.Fatal("a new destination accepted obsolete delivery proof instead of recapturing surviving inputs")
	}
}

func TestApplicationReleasePreservesOtherLibraryAndLegacyMCPGate(t *testing.T) {
	t.Run("unpinned library", func(t *testing.T) {
		a, _ := lifecycleLibraries(t)
		if err := SetRootString(a.Path, "volume_id", ""); err != nil {
			t.Fatal(err)
		}
		a, err := LoadConfig(a.Path)
		if err != nil {
			t.Fatal(err)
		}
		app, err := newApplicationManager(t.Context(), filepath.Join(t.TempDir(), "Pharos.app"))
		if err != nil {
			t.Fatal(err)
		}
		id, err := app.registry.register(a, "")
		if err != nil {
			t.Fatal(err)
		}
		if app.registry.library(id).Volume != testVolume {
			t.Fatal("registration did not measure the unpinned library drive", app.registry.library(id).Volume)
		}
		child := lifecycleChild(t)
		app.children[id] = child
		// Simulate a registry saved before drive measurement was introduced.
		app.registry.library(id).Volume = ""
		w := httptest.NewRecorder()
		app.api(w, httptest.NewRequest(http.MethodPost, "/api/app/release-volume", strings.NewReader(jsonText(map[string]any{"volume": testVolume}))))
		if w.Code != http.StatusOK || app.children[id] != nil || !app.suspended[id] {
			t.Fatal("unpinning bypassed release", w.Body.String())
		}
		select {
		case <-child.done:
		default:
			t.Fatal("release returned before the library stopped")
		}
	})
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

	m.registry.Selected = first
	if err = m.registry.save(); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	m.api(response, httptest.NewRequest(http.MethodPost, "/api/app/release-volume", strings.NewReader(`{"volume":"uuid:TEST-VOLUME"}`)))
	if response.Code != 200 || m.registry.Selected != first || !m.suspended[first] {
		t.Fatal("releasing the selected volume lost its intended selection", response.Body.String())
	}
	fresh, err := newApplicationManager(context.Background(), application)
	if err != nil || fresh.registry.Selected != first || fresh.suspended[first] || fresh.registry.MCP != first {
		t.Fatal("eject/relaunch did not restore the intended library and MCP assignment", err)
	}
	if fresh.registry.DeliverySource != second || fresh.registry.DeliveryTarget != first {
		t.Fatal("eject/relaunch lost the delivery pair")
	}

	m.registry.DeliverySource, m.registry.DeliveryTarget = first, second
	if err = m.registry.save(); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	m.api(response, httptest.NewRequest(http.MethodPost, "/api/app/disconnect", strings.NewReader(fmt.Sprintf(`{"id":%q}`, first))))
	if response.Code != 200 || m.registry.Selected != first || m.registry.DeliverySource != first {
		t.Fatal("release erased the source's selection or delivery configuration", response.Body.String())
	}
	m.deliveryCycle(first, second)
	if m.job != nil || m.busy {
		t.Fatal("released source continued automatic capture")
	}

	m.registry.Selected = ""
	if err = m.registry.save(); err != nil {
		t.Fatal(err)
	}
	fresh, err = newApplicationManager(context.Background(), application)
	if err != nil || fresh.registry.Selected != first {
		t.Fatal("an older eject-erased selection was not repaired from its MCP assignment", err)
	}
}

// Runs only as the owned subprocess used by the initial-library startup test.
func TestApplicationStartupHelper(t *testing.T) {
	initial := os.Getenv("PHAROS_APP_TEST_INITIAL")
	if initial == "" {
		return
	}
	t.Setenv("PHAROS_SUPPORT_DIR", os.Getenv("PHAROS_APP_TEST_SUPPORT"))
	t.Setenv(parentEnv, "")
	override(t, &libraryVolumeIdentity, testIdentity)
	if err := runApplicationCLI([]string{"--application", os.Getenv("PHAROS_APP_TEST_PATH"), "--endpoint", os.Getenv("PHAROS_APP_TEST_ENDPOINT"), "--initial-config", initial}); err != nil {
		t.Fatal(err)
	}
}
