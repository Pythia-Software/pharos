package archive

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestMenuBarPreferenceLayouts(t *testing.T) {
	data, err := os.ReadFile("../../testdata/menu-bar-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string          `json:"name"`
		Saved   json.RawMessage `json:"saved"`
		Order   []string        `json:"order"`
		Enabled bool            `json:"enabled"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			values := map[string]json.RawMessage{menuBarPreferenceKey: tc.Saved}
			settings := menuBarSettings(values)
			order := menuBarOrder(values, settings)
			if !reflect.DeepEqual(order, tc.Order) || settings["enabled"] != tc.Enabled {
				t.Fatalf("layout: %v, enabled: %v; want %v, %v", order, settings["enabled"], tc.Order, tc.Enabled)
			}
			for _, id := range menuBarDefaultOrder {
				present := false
				for _, item := range order {
					present = present || item == id
				}
				if settings[id] != present {
					t.Fatalf("visibility of %s disagrees with order", id)
				}
			}
		})
	}
}

func TestMenuBarSourceFailureClearsAfterNewerSuccessfulIndex(t *testing.T) {
	useHost(t, "menu-mac")
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{{Name: "codex", Enabled: true}}
	server := testServer(t, config, catalog)
	host := currentHost().ID
	_, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,last_success_at,updated_at)
		VALUES(?, 'codex', 'codex', 'read', 'complete', '2026-10-13T10:00:00Z', '2026-10-13T10:00:00Z', '2026-10-13T10:00:00Z')`, host)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,error) VALUES(?, 'codex', '2026-10-13T11:00:00Z', 'permission denied')`, host)
	if err != nil {
		t.Fatal(err)
	}
	server.runs = []*SyncRun{{State: "running", Kind: automaticRunKind, StartedAt: "2026-10-13T11:01:00Z"}}
	status := libraryStatusCall(t, server, "/api/menu-bar/status")
	if status["error"] != "codex: permission denied" {
		t.Fatalf("an error must remain visible during work: %v", status)
	}
	if status["indexed_at"] != "2026-10-13T10:00:00Z" {
		t.Fatalf("freshness: %v", status["indexed_at"])
	}
	_, err = catalog.DB.Exec(`UPDATE source_states SET last_attempt_at='2026-10-13T12:00:00Z',last_success_at='2026-10-13T12:00:00Z',error=NULL WHERE host_id=?`, host)
	if err != nil {
		t.Fatal(err)
	}
	status = libraryStatusCall(t, server, "/api/menu-bar/status")
	if status["error"] != "" || status["indexed_at"] != "2026-10-13T12:00:00Z" {
		t.Fatalf("successful manual retry must clear older automatic failure: %v", status)
	}
	_, err = catalog.DB.Exec(`UPDATE source_states SET error='interrupted: stopped by user',last_attempt_at='2026-10-13T13:00:00Z' WHERE host_id=?`, host)
	if err != nil {
		t.Fatal(err)
	}
	if status := libraryStatusCall(t, server, "/api/menu-bar/status"); status["error"] != "" {
		t.Fatalf("deliberate stop is not an error: %v", status)
	}
}

func TestMenuBarIgnoresDisabledAndRemovedSourceFailures(t *testing.T) {
	useHost(t, "menu-mac")
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{{Name: "codex", Enabled: true}, {Name: "claude", Enabled: false}, {Name: "healthy", Enabled: true}}
	server := testServer(t, config, catalog)
	for _, name := range []string{"codex", "claude", "removed"} {
		_, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,error,updated_at)
			VALUES(?, ?, 'codex', 'read', 'failed', '2026-10-13T10:00:00Z', 'manual failure', '2026-10-13T10:00:00Z')`, currentHost().ID, name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,error)
			VALUES(?, ?, '2026-10-13T11:00:00Z', 'automatic failure')`, currentHost().ID, name)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Completed runs retain the same failures after the source is switched off.
	server.runs = []*SyncRun{{State: "failed", Sources: []string{"codex", "healthy"}, Error: "automatic failure",
		Results: []IngestResult{{Source: "codex", Error: "automatic failure"}, {Source: "healthy"}}}}
	server.captures.runs = []*CaptureRun{{State: "failed", Sources: []string{"codex", "healthy"},
		Errors: []captureError{{Error: "capture failure"}}, Results: []CaptureSourceResult{
			{Name: "codex", State: "failed", captureRunRecord: captureRunRecord{Errors: []captureError{{Error: "capture failure"}}}},
			{Name: "healthy", State: "complete"},
		}}}
	check := func(want string) {
		t.Helper()
		if got := libraryStatusCall(t, server, "/api/menu-bar/status")["error"]; got != want {
			t.Fatalf("source error: got %v, want %q", got, want)
		}
	}
	check("codex: automatic failure")
	server.configMu.Lock()
	server.config.Sources = []SourceConfig{{Name: "codex", Enabled: false}, {Name: "claude", Enabled: false}, {Name: "healthy", Enabled: true}}
	server.configMu.Unlock()
	check("")
	server.configMu.Lock()
	server.config.Sources = []SourceConfig{{Name: "healthy", Enabled: true}}
	server.configMu.Unlock()
	check("")
	// Source filtering must not hide a failure of the service itself.
	server.runs[0].Error = "catalog read failed"
	check("catalog read failed")
	// A failed backup links to Settings → Backups, where backups now live.
	server.runs, server.captures.runs = nil, nil
	server.backups.runs = []*BackupRun{{ID: "backup-1", State: "failed"}}
	status := libraryStatusCall(t, server, "/api/menu-bar/status")
	if status["error"] != "Backup failed. Open Backups for details." || status["error_path"] != "/settings/backups" {
		t.Fatalf("backup failure: %v, %v", status["error"], status["error_path"])
	}
}

func TestMenuBarFreshnessIsScopedToThisMac(t *testing.T) {
	useHost(t, "menu-mac")
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	insertManual := func(host, at string) {
		t.Helper()
		_, err := catalog.DB.Exec(`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_success_at,updated_at)
			VALUES(?, 'codex', 'codex', 'read', 'complete', ?, ?)`, host, at, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertAutomatic := func(host, at string) {
		t.Helper()
		_, err := catalog.DB.Exec(`INSERT INTO automatic_source_states(host_id,source_name,indexed_at) VALUES(?, 'codex', ?)`, host, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		if got := libraryStatusCall(t, server, "/api/menu-bar/status")["indexed_at"]; got != want {
			t.Fatalf("this Mac's freshness: got %v, want %q", got, want)
		}
	}
	insertManual("other-mac", "2026-10-13T15:00:00Z")
	insertAutomatic("other-mac", "2026-10-13T16:00:00Z")
	check("") // Another Mac's work cannot make a never-indexed Mac look fresh.
	insertManual(currentHost().ID, "2026-10-13T10:00:00Z")
	check("2026-10-13T10:00:00Z")
	insertAutomatic(currentHost().ID, "2026-10-13T12:00:00Z")
	check("2026-10-13T12:00:00Z")
}

func TestMenuBarOffAndAuthentication(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	if asset := preferencesCall(t, server, http.MethodGet, "/assets/menu-bar.js", ""); asset.Code != http.StatusOK || asset.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("menu settings script is not served: %d", asset.Code)
	}
	response := preferencesCall(t, server, http.MethodPost, "/api/preferences", `{"set":{"pharos-menu-bar":{"enabled":false,"words":false,"cost":true}}}`)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	status := libraryStatusCall(t, server, "/api/menu-bar/status")
	settings := status["settings"].(map[string]any)
	if settings["enabled"] != false || settings["words"] != false || settings["cost"] != true || settings["tokens"] != true || status["library"] != nil {
		t.Fatalf("off preferences: %v", status)
	}
	response = preferencesCall(t, server, http.MethodPost, "/api/preferences", `{"set":{"pharos-menu-bar":{"enabled":false,"layoutVersion":2,"cost":false,"words":true,"order":["cost","quit","status","open","settings"]}}}`)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	settings = libraryStatusCall(t, server, "/api/menu-bar/status")["settings"].(map[string]any)
	if !reflect.DeepEqual(settings["order"], []any{"cost", "quit", "status", "open", "settings"}) || settings["cost"] != true || settings["words"] != false {
		t.Fatalf("saved menu order and visibility: %v", settings)
	}

	response = preferencesCall(t, server, http.MethodPost, "/api/preferences", `{"set":{"pharos-menu-bar":{"enabled":false,"layoutVersion":2,"order":["open","separator:4","status","separator:1","settings","quit"]}}}`)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	settings = libraryStatusCall(t, server, "/api/menu-bar/status")["settings"].(map[string]any)
	if settings["layoutVersion"] != float64(2) || !reflect.DeepEqual(settings["order"], []any{"open", "separator:4", "status", "separator:1", "settings", "quit"}) {
		t.Fatalf("saved separators: %v", settings)
	}

	for _, path := range []string{"/api/menu-bar/status", "/api/menu-bar/usage"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s accepted unauthenticated request: %d", path, recorder.Code)
		}
	}
}

func TestMenuBarCarbonUsesUserAssumptions(t *testing.T) {
	doc := carbonDocument{Tiers: []carbonTier{{Key: "large", OutputWhPerMTok: carbonRange{100, 100, 100}}},
		CategoryRatios: map[string]carbonRatio{"uncached_input": {carbonRange: carbonRange{.5, .5, .5}}},
		PUE:            carbonRatio{carbonRange: carbonRange{1, 2, 3}}, DefaultGrid: "default", Grids: []carbonGrid{{Key: "default", GramsPerKWh: 500}}}
	tokens := map[string]map[string]float64{"large": {"uncached_input": 1e6, "output": 2e6}}
	if got := menuBarCarbon(doc, tokens, nil); got != 250 {
		t.Fatalf("default estimate: %v", got)
	}
	prefs := map[string]any{"grid": "custom", "customGrid": "200", "pueScenario": "low", "inputSensitivity": "third"}
	if got, want := menuBarCarbon(doc, tokens, prefs), (100*.5*5/3+200)*200/1000.0; math.Abs(got-want) > 1e-8 {
		t.Fatalf("custom assumptions: got %v, want %v", got, want)
	}
	prefs["customGrid"] = -10
	if got := menuBarCarbon(doc, tokens, prefs); got != 0 {
		t.Fatalf("negative custom grid: %v", got)
	}
}

func TestMenuBarUsageMatchesRollingSummary(t *testing.T) {
	catalog, config := testCatalog(t)
	ingestUsageFixture(t, catalog)
	catalog.now = func() time.Time { return time.Date(2026, 9, 21, 2, 15, 0, 0, time.UTC) }
	catalog.background = func(func(context.Context)) bool { return false }
	server := testServer(t, config, catalog)
	catalog.background = func(func(context.Context)) bool { return false }
	summary, err := catalog.UsageSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := libraryStatusCall(t, server, "/api/menu-bar/usage")
	for _, window := range summary["windows"].([]*usageSummaryWindow) {
		if window.Key != "24h" {
			continue
		}
		if got["tokens"] != float64(window.Tokens) || got["human_words"] != float64(window.HumanWords) || got["cost_usd"] != window.CostUSD {
			t.Fatalf("menu differs from Usage: %v vs %+v", got, window)
		}
		if got["co2e_g"] != menuBarCarbon(summary["carbon_factors"].(carbonDocument), window.CarbonTokens, nil) {
			t.Fatalf("carbon differs: %v", got)
		}
	}
}
