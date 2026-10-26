package uifeedback

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pythia-software/pythia-auth/libraries/auth-go/authtest"
	"github.com/pythia-software/pythia-auth/libraries/auth-go/types"
)

type testStore struct {
	entry  Entry
	exists bool
}

func (s *testStore) Create(_ context.Context, d Document) (bool, error) {
	if s.exists {
		old, _ := json.Marshal(s.entry.Submission)
		value, _ := json.Marshal(d.Submission)
		if !bytes.Equal(old, value) {
			return false, ErrConflict
		}
		return false, nil
	}
	s.exists = true
	s.entry = Entry{Document: d, Tags: []string{}, Revision: "1:1"}
	return true, nil
}
func (s *testStore) Get(_ context.Context, id string) (Entry, error) {
	if !s.exists || id != s.entry.ID {
		return Entry{}, ErrNotFound
	}
	return s.entry, nil
}
func (s *testStore) List(context.Context) ([]Summary, error) {
	if !s.exists {
		return []Summary{}, nil
	}
	return []Summary{summarize(s.entry)}, nil
}
func (s *testStore) SetTags(_ context.Context, id, revision string, tags []string) error {
	if !s.exists {
		return ErrNotFound
	}
	if revision != s.entry.Revision {
		return ErrConflict
	}
	s.entry.Tags = tags
	s.entry.Revision = "1:2"
	return nil
}
func (s *testStore) Delete(_ context.Context, id, revision string) error {
	if !s.exists {
		return nil
	}
	if revision != s.entry.Revision {
		return ErrConflict
	}
	s.exists = false
	s.entry = Entry{}
	return nil
}
func testInbox(t *testing.T) (*Server, *authtest.Signer, http.Handler) {
	t.Helper()
	signer, err := authtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Close)
	a, err := NewAdminAuth(AuthConfig{Issuer: signer.Issuer(), Project: "feedback", Origin: "https://inbox.example.test", CookieKey: bytes.Repeat([]byte{1}, 32), Client: signer.HTTPClient()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: &testStore{}, Auth: a, Assets: fstest.MapFS{"index.html": {Data: []byte("inbox")}}}
	return s, signer, s.Handler()
}
func adminToken(t *testing.T, signer *authtest.Signer, email, subject, audience string) string {
	t.Helper()
	token, err := signer.SignClaims(types.PythiaClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: subject, Audience: jwt.ClaimStrings{audience}}, SubType: strings.Split(subject, ":")[0], Email: email, Project: "feedback"})
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func call(h http.Handler, method, path, token string, value any) *httptest.ResponseRecorder {
	var data []byte
	if value != nil {
		data, _ = json.Marshal(value)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestInboxPublicIntakeAndAdminBoundary(t *testing.T) {
	_, signer, h := testInbox(t)
	s := submission()
	s.Email = "private@example.test"
	first := call(h, "POST", "/v1/submissions", "", s)
	if first.Code != 201 || strings.Contains(first.Body.String(), s.Email) {
		t.Fatalf("receipt: %d %s", first.Code, first.Body.String())
	}
	if w := call(h, "POST", "/v1/submissions", "", s); w.Code != 200 {
		t.Fatalf("retry: %d", w.Code)
	}
	s.Comment = "different"
	if w := call(h, "POST", "/v1/submissions", "", s); w.Code != 409 {
		t.Fatalf("reused ID: %d", w.Code)
	}
	for _, token := range []string{"", adminToken(t, signer, "other@example.test", "user:other", "feedback"), adminToken(t, signer, AdminEmail, "service:other", "feedback"), adminToken(t, signer, AdminEmail, "user:admin", "another-project")} {
		for _, endpoint := range []struct{ method, path string }{{"GET", "/api/submissions"}, {"GET", "/api/submissions/" + reportID}, {"PATCH", "/api/submissions/" + reportID}, {"DELETE", "/api/submissions/" + reportID}} {
			w := call(h, endpoint.method, endpoint.path, token, nil)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("%s %s: %d", endpoint.method, endpoint.path, w.Code)
			}
		}
	}
	token := adminToken(t, signer, AdminEmail, "user:admin", "feedback")
	w := call(h, "GET", "/api/submissions", token, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), s.Email) {
		t.Fatalf("admin list: %d %s", w.Code, w.Body.String())
	}
	w = call(h, "PATCH", "/api/submissions/"+reportID, token, map[string]any{"tags": []string{"acknowledged"}, "revision": "1:1"})
	if w.Code != 204 {
		t.Fatalf("tags: %d %s", w.Code, w.Body.String())
	}
	w = call(h, "PATCH", "/api/submissions/"+reportID, token, map[string]any{"tags": []string{}, "revision": "1:1"})
	if w.Code != 409 {
		t.Fatalf("lost-update guard: %d", w.Code)
	}
	r := httptest.NewRequest("DELETE", "/api/submissions/"+reportID, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("If-Match", "1:2")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w = call(h, "GET", "/api/submissions/"+reportID, token, nil); w.Code != 404 {
		t.Fatal("deleted report remains readable")
	}
	t.Run("general reports without annotation arrays remain readable", func(t *testing.T) {
		for _, annotations := range []string{"", `,"annotations":null`} {
			_, signer, h := testInbox(t)
			req := httptest.NewRequest("POST", "/v1/submissions", strings.NewReader(`{"version":1,"id":"`+reportID+`","application":{"name":"Pharos"},"kind":"feedback","comment":"General feedback"`+annotations+`}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 201 {
				t.Fatalf("general intake: %d %s", w.Code, w.Body.String())
			}
			token := adminToken(t, signer, AdminEmail, "user:admin", "feedback")
			w = call(h, "GET", "/api/submissions/"+reportID, token, nil)
			var got struct {
				Annotations []Annotation `json:"annotations"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Annotations == nil {
				t.Fatalf("general details omitted annotation array: %d %s", w.Code, w.Body.String())
			}
		}
	})

}
func TestInboxRequestLimitsAndCSRF(t *testing.T) {
	_, signer, h := testInbox(t)
	token := adminToken(t, signer, AdminEmail, "user:admin", "feedback")
	r := httptest.NewRequest("PATCH", "/api/submissions/"+reportID, strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "https://elsewhere.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin mutation: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "/v1/submissions", strings.NewReader(strings.Repeat("x", MaxBodyBytes+1)))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("oversized body: %d", w.Code)
	}
	for i := 0; i < 31; i++ {
		w = call(h, "POST", "/v1/submissions", "", submission())
	}
	if w.Code != 429 {
		t.Fatalf("intake rate limit: %d", w.Code)
	}
}
func TestInboxCookieAndPKCE(t *testing.T) {
	s, signer, h := testInbox(t)
	token := adminToken(t, signer, AdminEmail, "user:admin", "feedback")
	w := call(h, "GET", "/?submission="+reportID, "", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/auth/login?submission="+reportID {
		t.Fatal("login lost the report link")
	}
	w = call(h, "GET", w.Header().Get("Location"), "", nil)
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "code_challenge_method=S256") {
		t.Fatal("missing PKCE login")
	}
	loginRequest := httptest.NewRequest("GET", "/", nil)
	for _, c := range w.Result().Cookies() {
		loginRequest.AddCookie(c)
	}
	var preserved loginState
	if err := s.Auth.open(loginRequest, "feedback_login", &preserved); err != nil || preserved.Submission != reportID {
		t.Fatal("login state lost the report link")
	}
	r := httptest.NewRequest("GET", "/api/submissions", nil)
	r.AddCookie(&http.Cookie{Name: "feedback_session", Value: s.Auth.seal("feedback_session", token)})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("session: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/api/submissions", nil)
	r.AddCookie(&http.Cookie{Name: "feedback_session", Value: s.Auth.seal("feedback_login", token)})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("cross-purpose cookie: %d", w.Code)
	}
	for _, state := range []loginState{{State: "state", Verifier: "verifier", Expires: time.Now().Add(-time.Minute)}, {State: "other", Verifier: "verifier", Expires: time.Now().Add(time.Minute)}} {
		r = httptest.NewRequest("GET", "/auth/callback?code=abc&state=state", nil)
		r.AddCookie(&http.Cookie{Name: "feedback_login", Value: s.Auth.seal("feedback_login", state)})
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid callback: %d", w.Code)
		}
	}
	// Complete the code exchange with a real signed Pythia user token. The
	// backend must validate it before setting a usable encrypted session.
	s.Auth.cfg.Client = &http.Client{Transport: testTokenExchange(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/oauth/token" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != "feedback" || r.Form.Get("redirect_uri") != "https://inbox.example.test/auth/callback" || r.Form.Get("code_verifier") != "verifier" {
			t.Fatal("incorrect PKCE code exchange")
		}
		body, _ := json.Marshal(map[string]any{"access_token": token, "expires_in": 900})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	r = httptest.NewRequest("GET", "/auth/callback?code=code&state=state", nil)
	r.AddCookie(&http.Cookie{Name: "feedback_login", Value: s.Auth.seal("feedback_login", loginState{State: "state", Verifier: "verifier", Expires: time.Now().Add(time.Minute), Submission: reportID})})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/?submission="+reportID {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/api/submissions", nil)
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "feedback_session" {
			if !cookie.Secure || !cookie.HttpOnly {
				t.Fatal("insecure browser session")
			}
			r.AddCookie(cookie)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("callback session: %d", w.Code)
	}
}

type testTokenExchange func(*http.Request) (*http.Response, error)

func (f testTokenExchange) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStoragePushEnvelopeAndAuthentication(t *testing.T) {
	s, signer, _ := testInbox(t)
	s.Bucket = "feedback-bucket"
	s.EventAccount = "push@example.test"
	called := 0
	s.Event = func(_ context.Context, id, generation string) error {
		called++
		if id != reportID || generation != "123" {
			t.Fatal("wrong notification target")
		}
		return nil
	}
	token := adminToken(t, signer, AdminEmail, "user:admin", "https://inbox.example.test")
	for _, bearer := range []string{"", token} {
		w := call(s.Handler(), "POST", "/events/storage", bearer, map[string]any{})
		if w.Code != 401 && w.Code != 403 {
			t.Fatalf("accepted non-push identity: %d", w.Code)
		}
	}
	// Pub/Sub sends both protobuf JSON names and legacy underscore aliases.
	envelope := map[string]any{"deliveryAttempt": 2, "subscription": "projects/example/subscriptions/feedback", "message": map[string]any{"data": "e30=", "messageId": "1", "message_id": "1", "publishTime": "2026-10-26T00:00:00Z", "publish_time": "2026-10-26T00:00:00Z", "orderingKey": "", "attributes": map[string]string{"bucketId": s.Bucket, "eventType": "OBJECT_FINALIZE", "objectId": objectPrefix + reportID + ".json", "objectGeneration": "123"}}}
	w := call(http.HandlerFunc(s.event), "POST", "/events/storage", "", envelope)
	if w.Code != 204 || called != 1 {
		t.Fatalf("push envelope: %d %s", w.Code, w.Body.String())
	}
}
