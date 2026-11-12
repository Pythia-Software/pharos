package archive

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func preferencesCall(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func TestPreferencesChangingAgentAnalysisEndsOldDemand(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	s.work.once.Do(func() {})
	s.work.runner.SetPaused(true)
	if err := c.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{agentEventsFlag: true}}); err != nil {
		t.Fatal(err)
	}
	_, request, err := s.work.submit(t.Context(), workRequestSpec{Kinds: []string{"analysis.agent_events"}, Priority: workPriorityLive})
	if err != nil {
		t.Fatal(err)
	}
	// Other preferences leave the analysis policy, and its demand, alone.
	if err := c.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{"pharos-theme": "dark"}, "delete": []any{"pharos-density"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-request.done:
		t.Fatal("an unrelated preference ended agent analysis demand")
	default:
	}
	if err := c.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{agentEventsFlag: false}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-request.done:
	default:
		t.Fatal("disabling agent analysis stranded its request")
	}
	if request.facts.State != opInterrupted || len(s.work.runner.Inspect().Requests) != 0 {
		t.Fatalf("old policy demand survived: %+v", request.facts)
	}
}

func TestPreferencesFollowTheLibrary(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)

	if response := preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", ""); response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), "window.pharosPreferenceValues={};window.pharosPreviewScope=\"") {
		t.Fatalf("empty library: %d %q", response.Code, response.Body.String())
	}
	body := `{"set":{"pharos-theme":"light","pharos-show-nav-button":true,"pharos-fingerprint-events":false,"pharos-bookmarks-v1":[{"id":"a","url":"/x</script>","title":"T"}]}}`
	if response := preferencesCall(t, server, http.MethodPost, "/api/preferences", body); response.Code != http.StatusOK {
		t.Fatalf("set: %d %s", response.Code, response.Body.String())
	}
	if response := preferencesCall(t, server, http.MethodPost, "/api/preferences", `{"set":{"pharos-theme":"dark"},"delete":["pharos-show-nav-button"]}`); response.Code != http.StatusOK {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	// A second Mac opening the same catalog reads the same values.
	response := preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", "")
	script := response.Body.String()
	if response.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
		!strings.HasPrefix(script, "window.pharosPreferenceValues={") ||
		!strings.Contains(script, `"pharos-theme":"dark"`) || strings.Contains(script, "pharos-show-nav-button") ||
		!strings.Contains(script, `"pharos-bookmarks-v1":[{`) || !strings.Contains(script, `"pharos-fingerprint-events":false`) {
		t.Fatalf("script: %q", script)
	}
	// The preview cache scope survives requests/restarts and separates catalogs
	// served on the same loopback origin. It exposes neither path nor token.
	previewScope := func(server *Server) string {
		response := preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", "")
		_, scope, ok := strings.Cut(response.Body.String(), "window.pharosPreviewScope=")
		scope, _, _ = strings.Cut(scope, ";")
		if !ok || len(strings.Trim(scope, "\"")) != 64 {
			t.Fatalf("missing opaque preview scope: %s", response.Body.String())
		}
		return scope
	}
	// A configuration that turns Agent Experience on says so beside the
	// preferences, which do not record it.
	configured := func(server *Server) string {
		_, flags, _ := strings.Cut(preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", "").Body.String(), "window.pharosConfiguredFlags=")
		return flags
	}
	if flags := configured(server); flags != "{};" {
		t.Fatalf("configured flags without agent_events_enabled: %q", flags)
	}
	enabled := config
	enabled.AgentEvents.Enabled = true
	if flags := configured(testServer(t, enabled, catalog)); flags != `{"`+agentEventsFlag+`":true};` {
		t.Fatalf("configured flags with agent_events_enabled: %q", flags)
	}
	scope := previewScope(server)
	if scope != previewScope(testServer(t, config, catalog)) {
		t.Fatal("preview cache scope changed for the same library")
	}
	other, otherConfig := testCatalog(t)
	if scope == previewScope(testServer(t, otherConfig, other)) {
		t.Fatal("different libraries share preview snapshots")
	}
	values := libraryStatusCall(t, server, "/api/preferences")["values"].(map[string]any)
	if values["pharos-theme"] != "dark" || values["pharos-fingerprint-events"] != false || len(values) != 3 {
		t.Fatalf("values: %#v", values)
	}
}

func TestPreferencesRefuseBadNamesAndOversizedValues(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	for _, body := range []string{
		`{"set":{"":1}}`,
		`{"set":{"has space":1}}`,
		`{"set":{"../escape":1}}`,
		`{"delete":["bad key"]}`,
		`{"set":{"big":"` + strings.Repeat("x", maxPreferenceBytes) + `"}}`,
	} {
		if response := preferencesCall(t, server, http.MethodPost, "/api/preferences", body); response.Code != http.StatusBadRequest {
			t.Errorf("%.60s: %d, want 400", body, response.Code)
		}
	}
	if values, err := catalog.UIPreferences(t.Context()); err != nil || len(values) != 0 {
		t.Fatalf("refused writes stored %v (%v)", values, err)
	}
}

func TestPageHistoryPreferenceBudget(t *testing.T) {
	data, err := os.ReadFile("../../testdata/page-history-budget.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Prefixes []struct {
			Value        string `json:"value"`
			EncodedBytes int    `json:"encoded_bytes"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range fixture.Prefixes {
		encoded, err := json.Marshal(prefix.Value)
		if err != nil || len(encoded) != prefix.EncodedBytes {
			t.Fatalf("history encoding: %d bytes, want %d (%v)", len(encoded), prefix.EncodedBytes, err)
		}
	}
	// Reproduce the rejected history save using a small preference budget.
	limit := fixture.Prefixes[2]
	override(t, &maxPreferenceBytes, limit.EncodedBytes)
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	key := "pharos-session:0123456789abcdef0123456789abcdef:pharos-history-v1"
	for _, prefix := range fixture.Prefixes {
		body, _ := json.Marshal(map[string]any{"set": map[string]any{key: prefix.Value}})
		want := http.StatusOK
		if prefix.EncodedBytes > maxPreferenceBytes {
			want = http.StatusBadRequest
		}
		if response := preferencesCall(t, server, http.MethodPost, "/api/preferences", string(body)); response.Code != want {
			t.Fatalf("save %d bytes: %d %s, want %d", prefix.EncodedBytes, response.Code, response.Body.String(), want)
		}
	}
	values, err := catalog.UIPreferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var saved string
	if err := json.Unmarshal(values[key], &saved); err != nil || saved != limit.Value {
		t.Fatalf("rejected history replaced the last saved visits: %v", err)
	}
}

func TestDisposableSessionPreferencesUnderWritePressure(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	prefix := "pharos-session:0123456789abcdef0123456789abcdef:"
	history := prefix + "pharos-history-v1"
	nav := prefix + "pharos-nav-open"
	if err := catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{history: "old", nav: "true"}}); err != nil {
		t.Fatal(err)
	}
	release, err := catalog.acquireWriteGate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	response := preferencesCall(t, server, http.MethodPost, "/api/session-preferences", `{"set":{"`+history+`":"first"},"delete":["`+nav+`"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("buffer: %d %s", response.Code, response.Body.String())
	}
	response = preferencesCall(t, server, http.MethodPost, "/api/session-preferences", `{"set":{"`+history+`":"latest"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("coalesce: %d %s", response.Code, response.Body.String())
	}
	values, err := catalog.UIPreferences(t.Context())
	if err != nil || string(values[history]) != `"latest"` || values[nav] != nil {
		t.Fatalf("pending values: %s, %s (%v)", values[history], values[nav], err)
	}
	var stored string
	if err := catalog.reader().QueryRow("SELECT value FROM ui_preferences WHERE key=?", history).Scan(&stored); err != nil || stored != `"old"` {
		t.Fatalf("buffer wrote while gate held: %q (%v)", stored, err)
	}
	if catalog.writersWaiting() {
		t.Fatal("disposable state queued ahead of other writers")
	}
	release()
	release = func() {}
	waitUntil(t, 5*time.Second, "session preferences to persist", func() bool {
		var count int
		return catalog.reader().QueryRow("SELECT COUNT(*) FROM ui_preferences WHERE key=? AND value=? AND NOT EXISTS (SELECT 1 FROM ui_preferences WHERE key=?)", history, `"latest"`, nav).Scan(&count) == nil && count == 1
	})
	values, err = catalog.UIPreferences(t.Context())
	if err != nil || string(values[history]) != `"latest"` || values[nav] != nil {
		t.Fatalf("persisted values: %v (%v)", values, err)
	}
}

func TestDisposableSessionPreferencesRejectImportantAndUnboundedData(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	prefix := "pharos-session:0123456789abcdef0123456789abcdef:"
	for _, body := range []string{
		`{"set":{"` + prefix + `pharos-ui-feedback-draft-v1":"draft"}}`,
		`{"set":{"pharos-theme":"dark"}}`,
		`{"set":{"` + prefix + `pharos-history-v1":true}}`,
		`{"set":{"` + prefix + `pharos-history-v1":"` + strings.Repeat("x", maxPreferenceBytes) + `"}}`,
	} {
		if response := preferencesCall(t, server, http.MethodPost, "/api/session-preferences", body); response.Code != http.StatusBadRequest {
			t.Errorf("%.80s: %d, want 400", body, response.Code)
		}
	}
	values, err := catalog.UIPreferences(t.Context())
	if err != nil || len(values) != 0 {
		t.Fatalf("invalid batch changed values: %v (%v)", values, err)
	}
	set := make(map[string]any)
	for i := 0; i <= sessionPreferenceLimit; i++ {
		set[fmt.Sprintf("%squery-table:last:%d", prefix, i)] = "value"
	}
	if err := catalog.queueSessionPreferences(map[string]any{"set": set}); err == nil {
		t.Fatal("overfull batch was accepted")
	}
	values, err = catalog.UIPreferences(t.Context())
	if err != nil || len(values) != 0 {
		t.Fatalf("overfull batch changed values: %v (%v)", values, err)
	}
}

func TestDisposableSessionPreferencesYieldToOtherProcesses(t *testing.T) {
	catalog, _ := testCatalog(t)
	key := "pharos-session:0123456789abcdef0123456789abcdef:pharos-history-v1"
	catalog.sessionPreferences.pending = map[string]sessionPreferenceChange{key: {value: `"history"`, version: 1}}
	other, err := sql.Open("sqlite", sqliteURI(catalog.Path)+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer other.Exec("ROLLBACK")
	started := time.Now()
	if err := catalog.flushSessionPreferences(); err != errJournalBusy {
		t.Fatalf("busy writer: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("session preferences held the gate for %v", elapsed)
	}
	release, ok := catalog.tryWriteGate()
	if !ok {
		t.Fatal("session preferences retained the writer gate")
	}
	release()
	if _, ok := catalog.sessionPreferences.pending[key]; !ok {
		t.Fatal("busy write discarded the pending value")
	}
}

func TestDisposableSessionPreferencesKeepLatestWhileFlushing(t *testing.T) {
	catalog, _ := testCatalog(t)
	key := "pharos-session:0123456789abcdef0123456789abcdef:pharos-history-v1"
	for i := 0; i < 100; i++ {
		if err := catalog.queueSessionPreferences(map[string]any{"set": map[string]any{key: strconv.Itoa(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 5*time.Second, "latest session preference to persist", func() bool {
		var value string
		return catalog.reader().QueryRow("SELECT value FROM ui_preferences WHERE key=?", key).Scan(&value) == nil && value == `"99"`
	})
}

func TestExplicitHistoryChangesSupersedeBufferedWrites(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(strconv.FormatBool(newer), func(t *testing.T) {
			catalog, _ := testCatalog(t)
			key := "pharos-session:0123456789abcdef0123456789abcdef:pharos-history-v1"
			release, err := catalog.acquireWriteGate(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release() }()
			if err := catalog.queueSessionPreferences(map[string]any{"set": map[string]any{key: "old"}}); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				finished <- catalog.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{key: "cleared"}})
			}()
			waitUntil(t, 5*time.Second, "explicit history write to wait for the gate", catalog.writersWaiting)
			if newer {
				if err := catalog.queueSessionPreferences(map[string]any{"set": map[string]any{key: "after clear"}}); err != nil {
					t.Fatal(err)
				}
			}
			release()
			release = func() {}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			want := `"cleared"`
			if newer {
				want = `"after clear"`
			}
			waitUntil(t, 5*time.Second, "current history to persist", func() bool {
				var value string
				return catalog.reader().QueryRow("SELECT value FROM ui_preferences WHERE key=?", key).Scan(&value) == nil && value == want
			})
			values, err := catalog.UIPreferences(t.Context())
			if err != nil || string(values[key]) != want {
				t.Fatalf("history: %s (%v), want %s", values[key], err, want)
			}
		})
	}
}

func TestDisposableSessionPreferencesStopWithBusyWriter(t *testing.T) {
	catalog, _ := testCatalog(t)
	release, err := catalog.acquireWriteGate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	key := "pharos-session:0123456789abcdef0123456789abcdef:pharos-history-v1"
	body := map[string]any{"set": map[string]any{key: "temporary"}}
	if err := catalog.queueSessionPreferences(body); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { catalog.closeSessionPreferences(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session preference worker did not stop while the writer was busy")
	}
	if err := catalog.queueSessionPreferences(body); err == nil {
		t.Fatal("closed catalog accepted a new buffered write")
	}
}
