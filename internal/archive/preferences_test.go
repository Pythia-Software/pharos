package archive

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if err := c.SetUIPreferences(t.Context(), map[string]any{"delete": []any{agentEventsFlag}}); err != nil {
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
