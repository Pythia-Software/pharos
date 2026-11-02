package archive

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownLinksResolveAndOpenFiles(t *testing.T) {
	catalog, config := testCatalog(t)
	root := t.TempDir()
	path := filepath.Join(root, "My report.html")
	content := "<script>this.must.stay.text()</script>"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tx := seedTx(t, catalog.DB)
	if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,location,indexed_at) VALUES ('links','codex','local','links','Links',?,'2026-10-30')`, root); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	server := testServer(t, config, catalog)
	get := func(query url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/markdown-link?"+query.Encode(), nil)
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	for _, href := range []string{path, path + ":12:3", "file://" + path, "My report.html", "My report.html:12:3", "./My%20report.html", path + "#L12"} {
		q := url.Values{"href": {href}, "workspace": {"links"}}
		response := get(q)
		var result map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result["status"] != "file" || result["href"] == "" {
			t.Fatalf("%s: %s", href, response.Body.String())
		}
		q.Set("open", "1")
		opened := get(q)
		if opened.Code != http.StatusOK || opened.Body.String() != content {
			t.Fatalf("open %s: %d %s", href, opened.Code, opened.Body.String())
		}
		if !strings.HasPrefix(opened.Header().Get("Content-Type"), "text/plain") || !strings.Contains(opened.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("active file content must stay isolated: %v", opened.Header())
		}
	}
	binary := filepath.Join(root, "Report.pdf")
	if err := os.WriteFile(binary, []byte{'P', 'D', 'F', 0, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	opened := get(url.Values{"href": {binary}, "open": {"1"}})
	if opened.Code != http.StatusOK || !strings.Contains(opened.Header().Get("Content-Disposition"), "Report.pdf") {
		t.Fatal("download lost its filename", opened.Header())
	}

	for _, href := range []string{"missing.md", root, "file://other-host" + path, "https://example.com", "javascript:alert(1)", path + "?raw=1", path + "%00"} {
		response := get(url.Values{"href": {href}, "workspace": {"links"}})
		if !strings.Contains(response.Body.String(), `"status":"unavailable"`) {
			t.Fatalf("%s: %s", href, response.Body.String())
		}
	}
	// Relative paths must not resolve against Pharos's process directory.
	if response := get(url.Values{"href": {"My report.html"}}); !strings.Contains(response.Body.String(), `"status":"unavailable"`) {
		t.Fatal(response.Body.String())
	}
}

func TestMarkdownLinksCheckOnlyLoopbackWithoutFollowingRedirects(t *testing.T) {
	catalog, config := testCatalog(t)
	server := testServer(t, config, catalog)
	calls := 0
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected probe: %s %v", r.Method, r.Header)
		}
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(404)
		case "/redirect":
			http.Redirect(w, r, "/must-not-follow", 302)
		case "/protected":
			w.WriteHeader(401)
		case "/get-only":
			w.WriteHeader(405)
		}
	}))
	defer local.Close()
	for path, status := range map[string]string{"/": "server", "/missing": "unavailable", "/redirect": "server", "/protected": "server", "/get-only": "server"} {
		r := httptest.NewRequest(http.MethodGet, "/api/markdown-link?"+url.Values{"href": {local.URL + path}}.Encode(), nil)
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), `"status":"`+status+`"`) {
			t.Fatalf("%s: %s", path, w.Body.String())
		}
	}
	if calls != 5 {
		t.Fatalf("followed a redirect: %d requests", calls)
	}
	for _, raw := range []string{"http://example.com", "http://192.168.1.1", "http://169.254.169.254", "file:///etc/hosts", "http://user:pass@localhost"} {
		if _, ok := markdownLocalURL(raw); ok {
			t.Fatalf("accepted non-loopback target %s", raw)
		}
	}
	for _, raw := range []string{"http://app.localhost:3000/", "http://127.0.0.2:8080", "http://0.0.0.0:3000", "http://[::1]:3000"} {
		if target, ok := markdownLocalURL(raw); !ok {
			t.Fatalf("rejected local target %s", raw)
		} else if strings.Contains(raw, "127.0.0.2") && target.Hostname() != "127.0.0.2" {
			t.Fatal("loopback target changed", target)
		}
	}
	local.Close()
	r := httptest.NewRequest(http.MethodGet, "/api/markdown-link?"+url.Values{"href": {local.URL}}.Encode(), nil)
	w := httptest.NewRecorder()
	server.markdownLink(w, r)
	if !strings.Contains(w.Body.String(), `"status":"unavailable"`) {
		t.Fatal(w.Body.String())
	}
}

type markdownImageTransport func(*http.Request) (*http.Response, error)

func (f markdownImageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMarkdownFaviconOnlyAcceptsSmallRasterImagesFromPublicHosts(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "10.1.1.1", "192.168.0.1", "169.254.169.254", "fc00::1", "fe80::1"} {
		if markdownPublicIP(net.ParseIP(raw)) {
			t.Fatalf("private IP accepted: %s", raw)
		}
	}
	if !markdownPublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP refused")
	}
	body := "<svg onload='bad()'></svg>"
	client := &http.Client{Transport: markdownImageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://example.com/favicon.ico" || r.Header.Get("Cookie") != "" {
			t.Errorf("favicon leaked page details: %v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"image/png"}}}, nil
	})}
	override(t, &markdownFaviconClient, client)
	server := &Server{}
	get := func(host string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.markdownFavicon(w, httptest.NewRequest(http.MethodGet, "/api/markdown-favicon?"+url.Values{"host": {host}}.Encode(), nil))
		return w
	}
	for _, host := range []string{"example.com", "localhost", "example.com/private?secret", "example.com:123"} {
		if w := get(host); w.Code != 200 || strings.Contains(w.Body.String(), "onload") {
			t.Fatalf("unsafe favicon %s: %s", host, w.Body.String())
		}
	}
	body = "\x89PNG\r\n\x1a\nrest"
	if w := get("example.com"); w.Header().Get("Content-Type") != "image/png" || w.Body.String() != body {
		t.Fatalf("valid image lost: %v %q", w.Header(), w.Body.String())
	}
	body = strings.Repeat("x", 128<<10+1)
	if w := get("example.com"); w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatal("oversized image accepted")
	}
}
