package archive

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevUIConcurrencyPrototypeIsOptionalAndAuthenticated(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "ui.py"), []byte("APP_HTML = r'''<!doctype html><html><title>Pharos</title></html>'''\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "timings.json")
	if err := os.WriteFile(data, []byte(`{"threads":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(http.NotFoundHandler())
	defer service.Close()
	target, _ := url.Parse(service.URL)
	handler := newDevUI(assets, target, "secret", 8801)
	serve := func(path string, authenticated, crossOrigin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "pharos_dev_token_8801", Value: "secret"})
		}
		if crossOrigin {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := serve("/usage", true, false); strings.Contains(w.Body.String(), "data-concurrency-prototype") {
		t.Fatal("prototype enabled without a data file")
	}
	handler.concurrencyData = data
	if w := serve("/usage", true, false); !strings.Contains(w.Body.String(), "<html data-concurrency-prototype>") {
		t.Fatal("prototype not enabled for the opted-in dev UI")
	}
	path := "/api/usage/concurrency-prototype"
	if w := serve(path, false, false); w.Code != http.StatusUnauthorized {
		t.Fatal("personal timings available without authentication")
	}
	if w := serve(path, true, true); w.Code != http.StatusForbidden {
		t.Fatal("cross-origin timings request accepted")
	}
	if w := serve(path, true, false); w.Code != http.StatusOK || w.Body.String() != `{"threads":[]}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("prototype response: %d %s", w.Code, w.Body.String())
	}
}

func TestDevUIServesCheckoutAndForwardsAPIWithToken(t *testing.T) {
	var forwarded *http.Request
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r
		writeJSON(w, map[string]any{"ok": true}, http.StatusOK)
	}))
	defer service.Close()
	target, _ := url.Parse(service.URL)
	assets := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(assets, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ui.py", "APP_HTML = r'''<!doctype html><title>Pharos</title>first'''\n")
	write("library.js", "library()")
	handler := newDevUI(assets, target, "secret", 8799)
	serve := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		request.AddCookie(&http.Cookie{Name: "pharos_dev_token_8799", Value: "secret"})
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/?token=secret", nil))
	if login.Code != http.StatusSeeOther || !strings.Contains(login.Header().Get("Set-Cookie"), "pharos_dev_token_8799=secret") {
		t.Fatalf("token login: %d %q", login.Code, login.Header().Get("Set-Cookie"))
	}
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/library", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("page without cookie: %d", anonymous.Code)
	}

	page := serve(http.MethodGet, "/library", nil)
	if body := page.Body.String(); !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "<title>Pharos (dev)</title>first") || strings.Contains(body, "EventSource") {
		t.Fatalf("page: %q", body)
	}
	write("ui.py", "APP_HTML = r'''<!doctype html><title>Pharos</title>second'''\n")
	if body := serve(http.MethodGet, "/", nil).Body.String(); !strings.Contains(body, "second") {
		t.Fatalf("edited page not re-read: %q", body)
	}
	if asset := serve(http.MethodGet, "/assets/library.js", nil); asset.Body.String() != "library()" || !strings.HasPrefix(asset.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("asset: %q %q", asset.Body.String(), asset.Header().Get("Content-Type"))
	}
	write("library.js", "libraryUpdated()")
	if asset := serve(http.MethodGet, "/assets/library.js", nil); asset.Body.String() != "libraryUpdated()" || asset.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("refreshed asset: %q cache %q", asset.Body.String(), asset.Header().Get("Cache-Control"))
	}
	if forwarded != nil {
		t.Fatal("pages and assets must not reach the service")
	}

	api := serve(http.MethodPost, "/api/sync?kind=quick", map[string]string{"Origin": "http://evil.test", "Sec-Fetch-Site": "cross-site"})
	if api.Code != http.StatusForbidden {
		t.Fatalf("cross-origin API call: %d", api.Code)
	}
	api = serve(http.MethodPost, "/api/sync?kind=quick", map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"})
	if api.Code != http.StatusOK || forwarded == nil {
		t.Fatalf("same-origin API call: %d", api.Code)
	}
	if forwarded.URL.Path != "/api/sync" || forwarded.URL.RawQuery != "kind=quick" || forwarded.Method != http.MethodPost {
		t.Fatalf("forwarded %s %s", forwarded.Method, forwarded.URL)
	}
	if forwarded.Header.Get("Authorization") != "Bearer secret" || forwarded.Header.Get("Cookie") != "" || forwarded.Header.Get("Origin") != "" || forwarded.Header.Get("Sec-Fetch-Site") != "" {
		t.Fatalf("forwarded headers: %v", forwarded.Header)
	}

	service.Close()
	if down := serve(http.MethodGet, "/api/health", nil); down.Code != http.StatusBadGateway || !strings.Contains(down.Body.String(), "unreachable") {
		t.Fatalf("service down: %d %s", down.Code, down.Body.String())
	}
}

func TestDevUIDeepLoginPreservesFiltersAndReplacesStaleCookie(t *testing.T) {
	service, _ := url.Parse("http://127.0.0.1:8806")
	handler := newDevUI(t.TempDir(), service, "current-secret", 8807)
	path := "/usage?machine_view=time&activity_days=14&activity_repo=repo%2Fa"
	request := httptest.NewRequest(http.MethodGet, path+"&token=current-secret", nil)
	request.AddCookie(&http.Cookie{Name: handler.cookie, Value: "old-secret"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("deep login: %d", response.Code)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || location.Path != "/usage" || location.Query().Get("machine_view") != "time" || location.Query().Get("activity_days") != "14" || location.Query().Get("activity_repo") != "repo/a" || location.Query().Has("token") {
		t.Fatalf("login lost filters or retained token: %q", response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != handler.cookie || cookies[0].Value != "current-secret" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("login did not replace stale cookie securely")
	}
	for _, path := range []string{"/usage?token=old-secret", "/api/ready?token=current-secret", "//evil.test/usage?token=current-secret"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized || response.Header().Get("Set-Cookie") != "" {
			t.Fatalf("unexpected login at %s: %d", path, response.Code)
		}
	}
}
