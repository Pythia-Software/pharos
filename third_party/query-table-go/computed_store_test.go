package querytable

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeComputedRepo struct {
	scope, dataset string
	saved          int
	conflict       bool
}

func (f *fakeComputedRepo) List(_ context.Context, scope, dataset string) ([]ComputedColumn, error) {
	f.scope = scope
	f.dataset = dataset
	return nil, nil
}
func (f *fakeComputedRepo) Save(_ context.Context, scope, dataset string, r SaveComputedColumnRequest) (ComputedColumn, error) {
	f.saved++
	f.scope = scope
	f.dataset = dataset
	if f.conflict {
		return ComputedColumn{}, ErrComputedConflict
	}
	r.Column.Revision = "2"
	return r.Column, nil
}
func TestComputedHandlerScopeAndConflict(t *testing.T) {
	repo := &fakeComputedRepo{}
	handler := NewComputedColumnsHandler(repo, func(r *http.Request, dataset string, write bool) (string, error) {
		if dataset != "runs" {
			return "", errors.New("denied")
		}
		return "tenant-1", nil
	})
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/?dataset=runs", nil))
	if get.Code != 200 || strings.TrimSpace(get.Body.String()) != "[]" || repo.scope != "tenant-1" || repo.dataset != "runs" {
		t.Fatalf("unexpected response: %s", get.Body.String())
	}
	body := `{"column":{"id":"domain","label":"Domain","expression":{"language":"qt-expr","version":1,"source":"LEFT([email], 3)"}},"expectedRevision":"1"}`
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest("PUT", "/?dataset=runs", strings.NewReader(body)))
	if put.Code != 200 || !strings.Contains(put.Body.String(), `"revision":"2"`) {
		t.Fatalf("save failed: %d %s", put.Code, put.Body.String())
	}
	repo.conflict = true
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, httptest.NewRequest("PUT", "/?dataset=runs", strings.NewReader(body)))
	if conflict.Code != 409 {
		t.Fatalf("wanted conflict, got %d", conflict.Code)
	}
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest("PUT", "/?dataset=other", strings.NewReader(body)))
	if denied.Code != 403 || repo.saved != 2 {
		t.Fatal("authorization did not precede persistence")
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest("PUT", "/?dataset=runs", strings.NewReader(body+`{}`)))
	if invalid.Code != 400 || repo.saved != 2 {
		t.Fatal("trailing JSON must be rejected")
	}
	noAuth := httptest.NewRecorder()
	NewComputedColumnsHandler(repo, nil).ServeHTTP(noAuth, httptest.NewRequest("GET", "/?dataset=runs", nil))
	if noAuth.Code != 403 {
		t.Fatal("missing authorizer must fail closed")
	}
}

func TestComputedUnicodeLimitsMatchJavaScript(t *testing.T) {
	cases := []struct {
		name, label, source string
		valid               bool
	}{
		{"BMP label at limit", strings.Repeat("a", 200), `"ok"`, true},
		{"BMP label over limit", strings.Repeat("a", 201), `"ok"`, false},
		{"emoji label at limit", strings.Repeat("🦋", 100), `"ok"`, true},
		{"emoji label over limit", strings.Repeat("🦋", 101), `"ok"`, false},
		{"mixed label at limit", strings.Repeat("🦋", 99) + "ab", `"ok"`, true},
		{"mixed label over limit", strings.Repeat("🦋", 99) + "abc", `"ok"`, false},
		{"BMP source at limit", "Source", `"` + strings.Repeat("a", 9998) + `"`, true},
		{"BMP source over limit", "Source", `"` + strings.Repeat("a", 9999) + `"`, false},
		{"emoji source at limit", "Source", `"` + strings.Repeat("🦋", 4999) + `"`, true},
		{"emoji source over limit", "Source", `"` + strings.Repeat("🦋", 4999) + `a"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeComputedRepo{}
			handler := NewComputedColumnsHandler(repo, func(*http.Request, string, bool) (string, error) { return "tenant", nil })
			body, err := json.Marshal(SaveComputedColumnRequest{Column: ComputedColumn{ID: "unicode", Label: tc.label, Expression: ComputedExpression{Language: "qt-expr", Version: 1, Source: tc.source}}})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("PUT", "/?dataset=runs", strings.NewReader(string(body))))
			expectedStatus, expectedSaved := http.StatusBadRequest, 0
			if tc.valid {
				expectedStatus, expectedSaved = http.StatusOK, 1
			}
			if response.Code != expectedStatus || repo.saved != expectedSaved {
				t.Fatalf("status=%d writes=%d; wanted status=%d writes=%d", response.Code, repo.saved, expectedStatus, expectedSaved)
			}
		})
	}
}
