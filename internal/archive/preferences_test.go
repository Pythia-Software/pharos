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

func TestPreferencesFollowTheLibrary(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)

	if response := preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", ""); response.Code != http.StatusOK || response.Body.String() != "window.pharosPreferenceValues={};" {
		t.Fatalf("empty library: %d %q", response.Code, response.Body.String())
	}
	body := `{"set":{"pharos-theme":"light","pharos-show-nav-button":true,"pharos-bookmarks-v1":[{"id":"a","url":"/x</script>","title":"T"}]}}`
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
		!strings.Contains(script, `"pharos-bookmarks-v1":[{`) {
		t.Fatalf("script: %q", script)
	}
	values := libraryStatusCall(t, server, "/api/preferences")["values"].(map[string]any)
	if values["pharos-theme"] != "dark" || len(values) != 2 {
		t.Fatalf("values: %#v", values)
	}
}

func TestPreferencesRefuseBadNamesAndOversizedValues(t *testing.T) {
	useHost(t, "host-a")
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
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
