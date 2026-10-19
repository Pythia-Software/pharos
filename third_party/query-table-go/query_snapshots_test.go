package querytable

import (
	"bytes"
	"compress/flate"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type snapshotTestRepo struct{ rows map[string]QuerySnapshot }

func (s snapshotTestRepo) Save(_ context.Context, scope, key string, q QuerySnapshot) (QuerySnapshot, error) {
	id := scope + key + q.Code
	if old, ok := s.rows[id]; ok {
		if old.Token != q.Token {
			return QuerySnapshot{}, ErrQuerySnapshotConflict
		}
		return old, nil
	}
	s.rows[id] = q
	return q, nil
}
func (s snapshotTestRepo) Resolve(_ context.Context, scope, key, code string) (QuerySnapshot, error) {
	q, ok := s.rows[scope+key+code]
	if !ok {
		return q, sql.ErrNoRows
	}
	return q, nil
}
func TestQuerySnapshotsHTTP(t *testing.T) {
	repo := snapshotTestRepo{rows: map[string]QuerySnapshot{}}
	handler := NewQuerySnapshotsHandler(repo, func(r *http.Request, key string, write bool) (string, error) { return r.Header.Get("Actor"), nil })
	code := "aaaaaaaaaaaaaaaaaaaaaa"
	snapshot := QuerySnapshot{Version: 1, Code: code, Token: "2j.e30", CreatedAt: 1}
	send := func(method, actor, key string, body string) int {
		r := httptest.NewRequest(method, "/snapshots?dataset="+key+"&code="+code, strings.NewReader(body))
		r.Header.Set("Actor", actor)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	body, _ := json.Marshal(snapshot)
	for _, tc := range []struct {
		method, actor, key, body string
		status                   int
	}{
		{"PUT", "one", "runs", string(body), 200}, {"PUT", "one", "runs", string(body), 200},
		{"GET", "one", "runs", "", 200}, {"GET", "two", "runs", "", 404}, {"GET", "one", "other", "", 404},
		{"PUT", "", "runs", string(body), 403}, {"POST", "one", "runs", string(body), 405},
		{"PUT", "one", "runs", string(body) + ` {}`, 400},
	} {
		if got := send(tc.method, tc.actor, tc.key, tc.body); got != tc.status {
			t.Fatalf("%+v: %d", tc, got)
		}
	}
	snapshot.Token = "2j.eyJsIjoyNX0"
	body, _ = json.Marshal(snapshot)
	if got := send("PUT", "one", "runs", string(body)); got != 409 {
		t.Fatal(got)
	}
}
func TestQuerySnapshotCompressedBounds(t *testing.T) {
	var compressed bytes.Buffer
	writer, _ := flate.NewWriter(&compressed, flate.BestSpeed)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), 4*1024*1024+1))
	_ = writer.Close()
	q := QuerySnapshot{Version: 1, Code: "aaaaaaaaaaaaaaaaaaaaaa", Token: "2d." + base64.RawURLEncoding.EncodeToString(compressed.Bytes())}
	if err := ValidateQuerySnapshot(q); err == nil {
		t.Fatal("oversized expanded state accepted")
	}
	for _, token := range []string{"3j.e30", "2j.bnVsbA", "2d.AA"} {
		q.Token = token
		if ValidateQuerySnapshot(q) == nil {
			t.Fatal(token)
		}
	}
}

type quotaSnapshotRepo struct{ snapshotTestRepo }

func (s quotaSnapshotRepo) Save(context.Context, string, string, QuerySnapshot) (QuerySnapshot, error) {
	return QuerySnapshot{}, ErrQuerySnapshotQuota
}
func TestQuerySnapshotQuotaHTTP(t *testing.T) {
	handler := NewQuerySnapshotsHandler(quotaSnapshotRepo{}, func(*http.Request, string, bool) (string, error) { return "local", nil })
	r := httptest.NewRequest("PUT", "/snapshots?dataset=runs&code=aaaaaaaaaaaaaaaaaaaaaa", strings.NewReader(`{"version":1,"code":"aaaaaaaaaaaaaaaaaaaaaa","token":"2j.e30","createdAt":0}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), "quota") {
		t.Fatal(w.Code, w.Body.String())
	}
}
