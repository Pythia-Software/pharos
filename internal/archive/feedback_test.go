package archive

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	uifeedback "github.com/gbdubs/pharos/internal/uifeedback/protocol"
)

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
