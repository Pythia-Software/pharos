package archive

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uifeedback "github.com/gbdubs/pharos/internal/uifeedback/protocol"
)

func TestFeedbackScreenshotExport(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	s := testServer(t, Config{}, nil)
	var paths []string
	for _, mime := range []string{"image/png", "image/jpeg", "image/png"} {
		var data bytes.Buffer
		img := image.NewRGBA(image.Rect(0, 0, 2, 3))
		if mime == "image/png" {
			if err := png.Encode(&data, img); err != nil {
				t.Fatal(err)
			}
		} else if err := jpeg.Encode(&data, img, nil); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(uifeedback.Screenshot{MIME: mime, Data: base64.StdEncoding.EncodeToString(data.Bytes())})
		w := httptest.NewRecorder()
		s.feedbackHTTP(w, httptest.NewRequest("POST", "/api/feedback/screenshot", bytes.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("export failed: %d %s", w.Code, w.Body.String())
		}
		var result struct{ URL string }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		link, err := url.Parse(result.URL)
		if err != nil || link.Scheme != "file" || link.Host != "" {
			t.Fatalf("invalid local file link: %q", result.URL)
		}
		path := filepath.FromSlash(link.Path)
		rel, err := filepath.Rel(temp, path)
		if err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("file escaped temporary storage: %q", path)
		}
		ext := ".png"
		if mime == "image/jpeg" {
			ext = ".jpg"
		}
		if filepath.Ext(path) != ext {
			t.Fatalf("wrong image extension: %q", path)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data.Bytes()) {
			t.Fatalf("export changed screenshot bytes: %v", err)
		}
		for _, entry := range []struct {
			path string
			mode os.FileMode
		}{{path, 0600}, {filepath.Dir(path), 0700}} {
			info, err := os.Stat(entry.path)
			if err != nil || info.Mode().Perm() != entry.mode {
				t.Fatalf("export must be private: %s: %v", entry.path, err)
			}
		}
		for _, previous := range paths {
			if previous == path {
				t.Fatal("exports must not overwrite earlier copied files")
			}
			if _, err := os.ReadFile(previous); err != nil {
				t.Fatalf("earlier copy no longer available: %v", err)
			}
		}
		paths = append(paths, path)
	}
}

func TestFeedbackScreenshotExportFailures(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	for _, body := range []string{
		`{}`, `{"mime":"image/svg+xml","data":""}`, `{"mime":"image/png","data":"invalid"}`,
		`{"mime":"image/png","data":"aGVsbG8="}`, `{"mime":"image/png","data":"","path":"/tmp/overwrite"}`,
	} {
		w := httptest.NewRecorder()
		exportFeedbackScreenshot(w, httptest.NewRequest("POST", "/api/feedback/screenshot", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid screenshot accepted: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	exportFeedbackScreenshot(w, httptest.NewRequest("POST", "/api/feedback/screenshot", strings.NewReader(strings.Repeat("x", (2<<20)+1025))))
	if w.Code != 413 {
		t.Fatalf("unbounded export: %d", w.Code)
	}
	entries, err := os.ReadDir(temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid requests created files: %v", err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(uifeedback.Screenshot{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())})
	t.Setenv("TMPDIR", filepath.Join(temp, "missing"))
	w = httptest.NewRecorder()
	exportFeedbackScreenshot(w, httptest.NewRequest("POST", "/api/feedback/screenshot", bytes.NewReader(body)))
	if w.Code != 500 {
		t.Fatalf("failed write reported success: %d", w.Code)
	}
}

func TestFeedbackSubmissionProxy(t *testing.T) {
	const id = "4d8d0d0e-270b-4f85-8b75-b01234567890"
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/submissions" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("proxy leaked local credentials or used wrong endpoint")
		}
		var submission uifeedback.Submission
		if err := json.NewDecoder(r.Body).Decode(&submission); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		if calls == 2 {
			_, _ = w.Write([]byte(`{"id":"wrong","received":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","received":true}`))
	}))
	defer upstream.Close()
	t.Setenv("PHAROS_FEEDBACK_URL", upstream.URL)
	s := testServer(t, Config{}, nil)
	data, _ := json.Marshal(uifeedback.Submission{Version: 1, ID: id, Kind: "feedback", Comment: "General feedback", Application: uifeedback.Application{Name: "Pharos"}, Annotations: []uifeedback.Annotation{}})
	for _, want := range []int{503, 502, 200} {
		r := httptest.NewRequest("POST", "/api/feedback/submit", bytes.NewReader(data))
		r.Header.Set("Cookie", "private=session")
		r.Header.Set("Authorization", "Bearer local-token")
		w := httptest.NewRecorder()
		s.feedbackHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status: %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	bad := httptest.NewRequest("POST", "/api/feedback/submit", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.feedbackHTTP(w, bad)
	if w.Code != 400 || calls != 3 {
		t.Fatal("invalid submission reached hosted intake")
	}
}
func TestFeedbackDiagnosticBoundsAndRedaction(t *testing.T) {
	var b feedbackLogBuffer
	_, _ = b.Write([]byte(strings.Repeat("x", 3*uifeedback.MaxLogBytes)))
	_, _ = b.Write([]byte(" Bearer private-token token=private-key /Users/alice/project"))
	text := b.text()
	if len(text) > uifeedback.MaxLogBytes || strings.Contains(text, "private-token") || strings.Contains(text, "private-key") || strings.Contains(text, "alice") {
		t.Fatal("diagnostic tail exceeded limit or exposed a known secret")
	}
}
