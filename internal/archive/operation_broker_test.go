package archive

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The broker keeps its operations in its own state file, never a library
// catalog: written on start and end, and on the next start an operation an
// earlier broker left running is interrupted.
func TestBrokerOperationsPersistAndEndAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	broker := newBrokerOperations(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker.start(ctx)
	transfer := broker.begin(OperationSpec{Kind: "library.transfer", Variant: "move"}, "lib-a", "lib-b")
	transfer.Step("Copying conversations", "", -1)
	read := func() []brokerOperationRecord {
		var stored struct {
			Operations []brokerOperationRecord `json:"operations"`
		}
		data, _ := os.ReadFile(path)
		_ = json.Unmarshal(data, &stored)
		return stored.Operations
	}
	waitUntil(t, 5*time.Second, "the start to be written", func() bool {
		records := read()
		return len(records) == 1 && records[0].State == opRunning && strings.Join(records[0].Libraries, ",") == "lib-a,lib-b"
	})
	create := broker.begin(OperationSpec{Kind: "library.create"})
	create.Finish(errors.New("the folder is not writable"))
	waitUntil(t, 5*time.Second, "the end to be written", func() bool {
		for _, record := range read() {
			if record.Kind == "library.create" && record.State == opFailed {
				return true
			}
		}
		return false
	})
	// The broker exits (or crashes) while the transfer runs.
	restarted := newBrokerOperations(path)
	records := restarted.records()
	if len(records) != 2 {
		t.Fatalf("after a restart: %+v", records)
	}
	for _, record := range records {
		if record.Kind == "library.transfer" && (record.State != opInterrupted || record.Reason != "owner-exited" || record.EndedAt == "") {
			t.Fatalf("a transfer left running: %+v", record)
		}
	}
}

// GET /api/app/operations answers while the broker's main mutex is held
// (create and child starts hold it for minutes), and makes a library busy
// while a broker operation touches it; a connected library's own verdict
// comes from its service, and a service that does not answer is unsafe.
func TestBrokerOperationsAnswerWithoutTheMainLock(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/operations" || r.Header.Get("Authorization") != "Bearer child-token" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"version": 1, "operations": []any{}, "eject": computeEjectVerdict(verdictInput{ops: []opView{runningView("maintenance.library-view", nil)}})}, http.StatusOK)
	}))
	defer service.Close()
	m := &applicationManager{token: "broker-token", id: "installation", operations: newBrokerOperations(filepath.Join(t.TempDir(), "operations.json")),
		children: map[string]*applicationChild{}}
	m.operations.publish([]brokerLibrary{
		{ID: "a", Name: "Euclid", Selected: true, Child: &applicationEndpoint{URL: service.URL, Token: "child-token", PID: 42}, ChildStarted: time.Now()},
		{ID: "b", Name: "Archive"},
		{ID: "c", Name: "Gone", Child: &applicationEndpoint{URL: "http://127.0.0.1:1", Token: "x"}},
	})
	create := m.operations.begin(OperationSpec{Kind: "library.create"})
	m.operations.begin(OperationSpec{Kind: "library.transfer", Variant: "copy"}, "b", "d")
	m.mu.Lock()
	defer m.mu.Unlock()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/api/app/operations", nil)
		request.Header.Set("Authorization", "Bearer broker-token")
		response := httptest.NewRecorder()
		m.ServeHTTP(response, request)
		answered <- response
	}()
	var response *httptest.ResponseRecorder
	select {
	case response = <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("GET /api/app/operations waited for the broker's lock")
	}
	var document struct {
		Libraries []struct {
			ID        string         `json:"id"`
			Connected bool           `json:"connected"`
			Service   map[string]any `json:"service"`
			Eject     ejectVerdict   `json:"eject"`
		} `json:"libraries"`
		App struct {
			Operations []map[string]any `json:"operations"`
		} `json:"app"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil || response.Code != http.StatusOK {
		t.Fatalf("%d %s: %v", response.Code, response.Body.String(), err)
	}
	verdicts := map[string]string{}
	for _, library := range document.Libraries {
		verdicts[library.ID] = library.Eject.Verdict
	}
	if verdicts["a"] != verdictSafeIn || verdicts["b"] != verdictBusy || verdicts["c"] != verdictUnsafe {
		t.Fatalf("verdicts: %v", verdicts)
	}
	if document.Libraries[0].Service["state"] != "running" || !document.Libraries[0].Connected || document.Libraries[1].Service["state"] != "stopped" {
		t.Fatalf("services: %+v", document.Libraries)
	}
	running := map[string]string{}
	for _, operation := range document.App.Operations {
		running[firstString(operation["kind"])] = firstString(operation["verb"]) + "/" + firstString(operation["state"])
	}
	if running["library.create"] != "Creating the library/running" || running["library.transfer"] != "Copying the library/running" {
		t.Fatalf("broker operations: %v", running)
	}
	create.Finish(nil)
}
