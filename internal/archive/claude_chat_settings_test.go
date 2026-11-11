package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

const claudeSettingsSecret = "settings-fixture-do-not-retain"
const settingsOrg = "11111111-1111-4111-8111-111111111111"
const settingsConversation = "22222222-2222-4222-8222-222222222222"
const settingsSecond = "33333333-3333-4333-8333-333333333333"
const settingsCacheEntry = "0123456789abcdef_0"
const settingsBody = `{"data":[{"uuid":"` + settingsConversation + `","model":"claude-opus-fixture","settings":{"effort_level":"high","thinking_mode":"auto","account_secret":"` + claudeSettingsSecret + `"},"name":"` + claudeSettingsSecret + `","chat_messages":[{"text":"` + claudeSettingsSecret + `"}]},{"uuid":"` + settingsSecond + `","model":null,"settings":{"thinking_mode":"disabled"}}],"has_more":true,"quota":"` + claudeSettingsSecret + `"}`

func writeSettingsCache(t *testing.T, root, name, body, date string, compressed bool) string {
	t.Helper()
	key := "1/0/https://claude.ai/api/organizations/" + settingsOrg + "/chat_conversations_v2?limit=30"
	return writeSettingsCacheKey(t, root, name, key, body, date, compressed)
}

func writeSettingsCacheKey(t *testing.T, root, name, key, body, date string, compressed bool) string {
	t.Helper()
	payload := []byte(body)
	if compressed {
		encoder, err := zstd.NewWriter(nil)
		if err != nil {
			t.Fatal(err)
		}
		payload = encoder.EncodeAll(payload, nil)
		encoder.Close()
	}
	data := make([]byte, 24)
	binary.LittleEndian.PutUint64(data, 0xfcfb6d1ba7725c30)
	binary.LittleEndian.PutUint32(data[8:], 5)
	binary.LittleEndian.PutUint32(data[12:], uint32(len(key)))
	data = append(data, []byte(key)...)
	data = append(data, payload...)
	data = append(data, []byte("\x00HTTP/1.1 200 OK\x00date: "+date+"\x00authorization: "+claudeSettingsSecret+"\x00")...)
	file := filepath.Join(root, "Cache", "Cache_Data", name)
	writeSourceFile(t, file, string(data))
	return file
}
func claudeSettingsFixture(t *testing.T) SourceConfig {
	root := t.TempDir()
	writeSettingsCache(t, root, settingsCacheEntry, settingsBody, "Tue, 01 Sep 2026 12:00:00 GMT", true)
	return conformanceSource(claudeSettingsKind, root)
}
func TestClaudeSettingsProjection(t *testing.T) {
	endpoint := "https://claude.ai/api/organizations/" + settingsOrg + "/chat_conversations_v2?limit=30"
	for _, tc := range []struct {
		name, prefix string
	}{
		{"unpartitioned", "1/0/"},
		{"partitioned", "1/0/_dk_https://claude.ai https://claude.ai "},
		{"partitioned without credentials", "0/0/_dk_https://claude.ai https://claude.ai "},
		{"partitioned with initiator", "1/0/_dk_s_cn_https://claude.ai https://claude.ai https://" + claudeSettingsSecret + ".example "},
		{"upload identifier", "1/123/"},
	} {
		for _, compressed := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{false: "plain", true: "zstd"}[compressed], func(t *testing.T) {
				source := claudeSettingsFixture(t)
				upper := source
				upper.Kind = strings.ToUpper(source.Kind)
				if !retainedAcquisitionEnabled(upper) {
					t.Fatal("case-insensitive source kind bypassed preservation")
				}
				writeSettingsCacheKey(t, source.Path, settingsCacheEntry, tc.prefix+endpoint, settingsBody, "Tue, 01 Sep 2026 12:00:00 GMT", compressed)
				plan, err := planCapture(t.Context(), source)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.files) != 0 || len(plan.databases) != 0 || len(plan.projections) != 1 || !plan.retainAbsent {
					t.Fatalf("unsafe plan: %+v", plan)
				}
				projection := plan.projections[0]
				if bytes.Contains(projection.data, []byte(claudeSettingsSecret)) {
					t.Fatal("unrelated data crossed the projection boundary")
				}
				var snapshot claudeSettingsSnapshot
				if err = json.Unmarshal(projection.data, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.Organization != settingsOrg || snapshot.TimeBasis != "http_date" || snapshot.ObservedAt != "2026-09-01T12:00:00Z" || len(snapshot.Conversations) != 2 {
					t.Fatalf("%+v", snapshot)
				}
				first, second := snapshot.Conversations[0], snapshot.Conversations[1]
				if *first.Model != "claude-opus-fixture" || *first.Effort != "high" || *first.Thinking != "auto" || second.Model != nil || second.Effort != nil {
					t.Fatalf("%+v %+v", first, second)
				}
				// Source reads are bounded and cancellation does not publish a partial plan.
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if _, err := planCapture(ctx, source); !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			})
		}
	}
	for _, key := range []string{"https://evil.example/api/organizations/" + settingsOrg + "/chat_conversations_v2", "https://claude.ai/api/organizations/not-an-id/chat_conversations_v2", "https://claude.ai/api/organizations/" + settingsOrg + "/usage", "https://claude.ai.evil.example/api/organizations/" + settingsOrg + "/chat_conversations_v2"} {
		for _, prefix := range []string{"", "1/0/", "1/0/_dk_https://claude.ai https://claude.ai "} {
			if claudeSettingsOrganization(prefix+key) != "" {
				t.Fatal("accepted unrelated endpoint", prefix+key)
			}
		}
	}
	for _, key := range []string{"1/0/_dk_", "1/0/_dk_https://claude.ai ", "1/0/_dk_" + endpoint, "1/", "1/0", "1/invalid/" + endpoint, "2/0/" + endpoint, "1/0/unrelated " + endpoint} {
		if claudeSettingsOrganization(key) != "" {
			t.Fatal("accepted malformed cache key", key)
		}
	}
	for _, space := range []string{"", " ", "\t"} {
		snapshot, err := decodeClaudeSettings([]byte(`{"data":[]}`+"\x00date:"+space+"Tue, 01 Sep 2026 12:00:00 GMT\x00"), settingsOrg, settingsCacheEntry, time.Now())
		if err != nil || snapshot.TimeBasis != "http_date" || snapshot.ObservedAt != "2026-09-01T12:00:00Z" {
			t.Fatal("HTTP date whitespace handling", snapshot, err)
		}
	}
	snapshot, err := decodeClaudeSettings([]byte(`{"data":[]}`), settingsOrg, settingsCacheEntry, time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC))
	if err != nil || snapshot.TimeBasis != "cache_file_mtime" {
		t.Fatal(snapshot, err)
	}
	root := t.TempDir()
	external := claudeSettingsFixture(t)
	if err = os.Symlink(filepath.Join(external.Path, "Cache", "Cache_Data", settingsCacheEntry), filepath.Join(root, settingsCacheEntry)); err != nil {
		t.Fatal(err)
	}
	plan, err := planCapture(t.Context(), SourceConfig{Kind: claudeSettingsKind, Path: root})
	if err != nil || len(plan.projections) != 0 {
		t.Fatal(plan, err)
	}
}

func TestClaudeSettingsCaptureRetainsHistory(t *testing.T) {
	useHost(t, "settings-host")
	source := claudeSettingsFixture(t)
	config := captureTestConfig(t, source)
	if result := runCapture(t, config); !result.OK {
		t.Fatal(result)
	}
	sourceFile := writeSettingsCache(t, source.Path, settingsCacheEntry, strings.ReplaceAll(settingsBody, "\"high\"", "\"low\""), "Wed, 02 Sep 2026 12:00:00 GMT", false)
	if result := runCapture(t, config); !result.OK {
		t.Fatal(result)
	}
	if result := runCapture(t, config); !result.OK || result.Sources[0].FilesCopied != 0 {
		t.Fatal(result)
	}
	if err := os.Remove(sourceFile); err != nil {
		t.Fatal(err)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatal(result)
	}
	targets, err := captureTargets(config.CaptureRoot, nil, true, nil)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	manifest, err := loadCaptureManifest(targets[0].Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 2 {
		t.Fatal("cache eviction removed observations", manifest.Files)
	}
	for _, entry := range manifest.Files {
		if entry.MissingSince != "" {
			t.Fatal("cache eviction marked an observation missing")
		}
	}
	if err = os.RemoveAll(source.Path); err != nil {
		t.Fatal(err)
	}
	c, _ := testCatalog(t)
	for range 2 {
		result := c.IndexCapture(t.Context(), targets[0], nil)
		if result.Error != nil || result.Workspaces != 0 || result.Conversations != 0 || result.Messages != 0 {
			t.Fatalf("settings created transcript entities: %+v", result)
		}
	}
	if countRows(t, c, "SELECT COUNT(*) FROM claude_chat_settings_observations") != 4 || countRows(t, c, "SELECT COUNT(*) FROM workspaces") != 0 || countRows(t, c, "SELECT COUNT(*) FROM work_items") != 0 {
		t.Fatal("settings ingestion lost history or created work")
	}
	observations, err := c.claudeConversationSettings(t.Context(), source.Account, settingsConversation)
	if err != nil || len(observations) != 2 || observations[0]["effort_level"] != "low" {
		t.Fatal(observations, err)
	}
	other, err := c.claudeConversationSettings(t.Context(), "other-account", settingsConversation)
	if err != nil || len(other) != 0 {
		t.Fatal("cross-account join", other, err)
	}
}

func TestClaudeSettingsJoinExportInEitherOrder(t *testing.T) {
	for _, settingsFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "settings-first", false: "export-first"}[settingsFirst], func(t *testing.T) {
			c, _ := testCatalog(t)
			settings := claudeSettingsFixture(t)
			export := SourceConfig{Name: "chat-export", Kind: "claude-ai", Account: settings.Account, Enabled: true, Path: writeSourceFileAt(t, strings.ReplaceAll(claudeAIExport, "conv-1", settingsConversation))}
			addSettings := func() {
				config := captureTestConfig(t, settings)
				if summary := runCapture(t, config); !summary.OK {
					t.Fatal(summary)
				}
				targets, err := captureTargets(config.CaptureRoot, nil, true, nil)
				if err != nil {
					t.Fatal(err)
				}
				if result := c.IndexCapture(t.Context(), targets[0], nil); result.Error != nil {
					t.Fatal(result.Error)
				}
			}
			if settingsFirst {
				addSettings()
			}
			ingestSource(t, c, export)
			if !settingsFirst {
				addSettings()
			}
			var id, workspace string
			if err := c.DB.QueryRow("SELECT id,workspace_id FROM conversations WHERE native_id=?", settingsConversation).Scan(&id, &workspace); err != nil {
				t.Fatal(err)
			}
			conversation, err := c.WorkConversationContext(t.Context(), workspace, id)
			if err != nil {
				t.Fatal(err)
			}
			selections := mapValue(conversation["selected_settings"])
			if selections == nil || selections["scope"] != "observed_conversation_selection" {
				t.Fatal("settings did not join", selections)
			}
			if firstString(conversation["model"]) != "" || countRows(t, c, "SELECT COUNT(*) FROM messages WHERE model IS NOT NULL") != 0 {
				t.Fatal("selection was presented as executed model")
			}
			if countRows(t, c, "SELECT COUNT(*) FROM model_requests WHERE model IS NOT NULL") != 0 {
				t.Fatal("settings fabricated execution usage")
			}
		})
	}
}

func TestClaudeSettingsRecording(t *testing.T) {
	c, config := testCatalog(t)
	source := claudeSettingsFixture(t)
	config.Sources = []SourceConfig{source}
	config.CaptureRoot = t.TempDir()
	server := testServer(t, config, c)
	code, doc := probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/recording", `{"source":"claude-ai-settings","active":true}`)
	if code != 200 || doc["active"] != true {
		t.Fatal(code, doc)
	}
	// Contention waits for the next tick; it must not start a second writer.
	if !server.ingestMu.TryLock() {
		t.Fatal("ingest lock is busy")
	}
	server.checkClaudeSettingsRecording()
	server.ingestMu.Unlock()
	if countRows(t, c, "SELECT COUNT(*) FROM claude_chat_settings_observations") != 0 {
		t.Fatal("ingested while another writer held the lock")
	}
	server.checkClaudeSettingsRecording()
	code, doc = probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/recording?source=claude-ai-settings", "")
	if code != 200 || integer(doc["known_conversations"]) != 2 || integer(doc["new_conversations"]) != 2 {
		t.Fatal(code, doc)
	}
	if code, doc = probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/recording", `{"source":"claude-ai-settings","active":false}`); code != 200 || doc["active"] != false {
		t.Fatal(code, doc)
	}
	server.claudeSettingsRecording.mu.Lock()
	ctx := server.claudeSettingsRecording.ctx
	server.claudeSettingsRecording.mu.Unlock()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("stop did not cancel recording")
	}
	probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/recording", `{"source":"claude-ai-settings","active":true}`)
	server.claudeSettingsRecording.mu.Lock()
	server.claudeSettingsRecording.expires = time.Now().Add(-time.Second)
	server.claudeSettingsRecording.mu.Unlock()
	server.checkClaudeSettingsRecording()
	_, doc = probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/recording?source=claude-ai-settings", "")
	if doc["active"] != false {
		t.Fatal("recording did not expire", doc)
	}
	probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/recording", `{"source":"claude-ai-settings","active":true}`)
	server.configMu.Lock()
	server.config.Sources[0].Enabled = false
	server.configMu.Unlock()
	server.checkClaudeSettingsRecording()
	_, doc = probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/recording?source=claude-ai-settings", "")
	if doc["active"] != false {
		t.Fatal("paused source kept recording", doc)
	}
	if code, _ = probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/recording", `{"source":"claude-ai-settings","active":true}`); code != 400 {
		t.Fatal("recording started for disabled source", code)
	}
	server.configMu.Lock()
	server.config.Sources[0].Enabled = true
	server.configMu.Unlock()
	probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/recording", `{"source":"claude-ai-settings","active":true}`)
	server.beginStop()
	server.claudeSettingsRecording.mu.Lock()
	ctx = server.claudeSettingsRecording.ctx
	server.claudeSettingsRecording.mu.Unlock()
	if ctx.Err() == nil {
		t.Fatal("service shutdown left recording live")
	}

}

func TestClaudeSettingsAutomaticAcquiresBeforeIndexing(t *testing.T) {
	source := claudeSettingsFixture(t)
	server := automaticTestServer(t, source)
	if !server.runAutomatic(t.Context(), syncSettings{Enabled: true, Interval: 60}) {
		t.Fatal("scheduled acquisition did not run")
	}
	c := server.Catalog
	if countRows(t, c, "SELECT COUNT(*) FROM claude_chat_settings_observations") != 2 {
		t.Fatal("scheduled source did not ingest settings")
	}
	targets, err := captureTargets(server.Config().CaptureRoot, nil, true, nil)
	if err != nil || len(targets) != 1 {
		t.Fatal("scheduled acquisition did not preserve source", targets, err)
	}
	if countRows(t, c, "SELECT COUNT(*) FROM workspaces") != 0 || countRows(t, c, "SELECT COUNT(*) FROM model_requests") != 0 {
		t.Fatal("automatic settings acquisition created execution data")
	}
	// One malformed live entry cannot block independent, valid observations.
	writeSettingsCache(t, source.Path, "fedcba9876543210_0", `{"data":[`, "Wed, 02 Sep 2026 12:00:00 GMT", false)
	plan, err := planCapture(t.Context(), source)
	if err != nil || len(plan.projections) != 1 || len(plan.warnings) != 1 {
		t.Fatal(plan, err)
	}
	if !server.runAutomatic(t.Context(), syncSettings{Enabled: true, Interval: 60}) {
		t.Fatal("scheduled retry did not run")
	}
	if countRows(t, c, "SELECT COUNT(*) FROM claude_chat_settings_observations") != 2 {
		t.Fatal("partial entry corrupted stored observations")
	}
}

func TestClaudeSettingsKeepsUnknownsAndOrganizationsSeparate(t *testing.T) {
	c, _ := testCatalog(t)
	model, effort, thinking := "claude-opus-fixture", "high", "auto"
	for _, entry := range []struct {
		org, date     string
		model, effort *string
	}{
		{settingsOrg, "2026-09-03T12:00:00Z", nil, nil},
		{settingsOrg, "2026-09-01T12:00:00Z", &model, &effort}, // older input arrives later
		{settingsSecond, "2026-09-02T12:00:00Z", &model, nil},
	} {
		snapshot := claudeSettingsSnapshot{Version: 1, Organization: entry.org, ObservedAt: entry.date, TimeBasis: "http_date", CacheEntry: settingsCacheEntry, Conversations: []claudeConversationSetting{{Conversation: settingsConversation, Model: entry.model, Effort: entry.effort, Thinking: &thinking}}}
		record := claudeSettingsRecord(SourceConfig{Account: "local"}, snapshot)
		if err := c.writeTransaction(t.Context(), "settings test", func(tx *sql.Tx) error { return ingestClaudeSettings(t.Context(), tx, record, "host-a", "settings") }); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := c.claudeConversationSettings(t.Context(), "local", settingsConversation)
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	if rows[0]["model"] != nil || rows[0]["effort_level"] != nil || rows[0]["organization_uuid"] != settingsOrg {
		t.Fatal("unknown fields inherited an older or another organization's selection", rows)
	}
	if rows[1]["organization_uuid"] != settingsSecond || rows[2]["effort_level"] != "high" {
		t.Fatal("organization or observation history was lost", rows)
	}
}

func TestClaudeSettingsImportSetup(t *testing.T) {
	probeHome(t)
	useHost(t, "settings-import-host")
	cache := t.TempDir()
	path := probeLibrary(t, `[[sources]]
name = "claude-import"
kind = "claude-ai"
path = "/example/export"
account = "work"

[[sources]]
name = "personal-settings"
kind = "claude-ai-settings"
path = "/example/personal"
account = "personal"
enabled = true
`)
	server := probeServer(t, path)
	before := readText(t, path)
	code, offer := probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/setup?name=claude-import", "")
	if code != 200 || offer["enabled"] != false || offer["configured"] != false || offer["account"] != "work" {
		t.Fatal(code, offer)
	}
	if readText(t, path) != before {
		t.Fatal("inspecting setup changed configuration")
	}
	// A failed setup never prevents the import from remaining configured.
	if code, _ := probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/setup", `{"name":"claude-import","path":"relative"}`); code != 400 {
		t.Fatal(code)
	}
	if code, _ := probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/setup", `{"name":"personal-settings","path":"`+cache+`"}`); code != 400 {
		t.Fatal("accepted a non-export source", code)
	}
	body := jsonText(map[string]any{"name": "claude-import", "path": cache})
	for range 2 {
		code, enabled := probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/setup", body)
		if code != 200 || enabled["enabled"] != true || enabled["account"] != "work" {
			t.Fatal(code, enabled)
		}
	}
	config := server.Config()
	count := 0
	for _, source := range config.Sources {
		if source.Kind == claudeSettingsKind && source.Account == "work" {
			count++
			if !source.Enabled || source.Path != cache || source.File != hostConfigPath(config) {
				t.Fatal(source)
			}
		}
	}
	if count != 1 {
		t.Fatal("setup duplicated or lost the account-specific source", count)
	}
	code, offer = probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/setup?name=claude-import", "")
	if code != 200 || offer["enabled"] != true {
		t.Fatal(code, offer)
	}
	// Re-enabling a paused source keeps its identity and source options.
	name := firstString(offer["source"])
	if err := server.setSourceEnabled(name, false); err != nil {
		t.Fatal(err)
	}
	host := hostConfigPath(server.Config())
	original := readText(t, host)
	writeSourceFile(t, host, original+"retained_input_capture = true\n")
	if err := server.reloadSources(); err != nil {
		t.Fatal(err)
	}
	code, paused := probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/setup?name=claude-import", "")
	if code != 200 || paused["configured"] != true || paused["enabled"] != false || paused["source"] != name {
		t.Fatal(code, paused)
	}
	code, enabled := probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/setup", body)
	if code != 200 || enabled["source"] != name {
		t.Fatal(code, enabled)
	}
	restored, _ := sourceNamed(server.Config().Sources, name)
	if !restored.Enabled || restored.Options["retained_input_capture"] != true {
		t.Fatal("setup replaced source settings", restored)
	}
}

func TestClaudeSettingsImportSetupSharedSource(t *testing.T) {
	probeHome(t)
	useHost(t, "settings-shared-host")
	cache := t.TempDir()
	path := probeLibrary(t, fmt.Sprintf(`[[sources]]
name = "export"
kind = "claude-ai"
path = "/example/export"

[[sources]]
name = "shared-settings"
kind = "claude-ai-settings"
path = %q
enabled = false
`, cache))
	server := probeServer(t, path)
	code, offer := probeCall(t, server, http.MethodGet, "/api/claude-chat-settings/setup?name=export", "")
	if code != 200 || offer["scope"] != "library" || offer["enabled"] != false {
		t.Fatal(code, offer)
	}
	code, saved := probeCall(t, server, http.MethodPost, "/api/claude-chat-settings/setup", jsonText(map[string]any{"name": "export", "path": cache}))
	if code != 200 || saved["source"] != "shared-settings" {
		t.Fatal(code, saved)
	}
	source, _ := sourceNamed(server.Config().Sources, "shared-settings")
	if !source.Enabled || source.File != path {
		t.Fatal("shared source was duplicated instead of enabled", source)
	}
}
