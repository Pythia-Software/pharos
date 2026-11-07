//go:build probe

// Read-only fingerprint inputs for the standalone compact-rendering prototype.
// No Initialize, migrations, inference, background work or catalog writes.
// PHAROS_FINGERPRINT_CATALOG=/path/catalog.sqlite3 \
// PHAROS_FINGERPRINT_MANIFEST=/path/manifest.json \
// PHAROS_FINGERPRINT_OUTPUT=/path/output \
// go test -tags probe -run '^TestCompactFingerprintExport$' -v ./internal/archive
package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompactFingerprintExport(t *testing.T) {
	path, manifest, output := os.Getenv("PHAROS_FINGERPRINT_CATALOG"), os.Getenv("PHAROS_FINGERPRINT_MANIFEST"), os.Getenv("PHAROS_FINGERPRINT_OUTPUT")
	if path == "" || manifest == "" || output == "" {
		t.Skip("set the three PHAROS_FINGERPRINT_* paths to export prototype inputs")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Population bool   `json:"population"`
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	catalog := &Catalog{DB: db, Path: path, readOnly: true, queryOnly: true}
	server := &Server{Catalog: catalog}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	// The study needs full histories. The product endpoint retains its limit.
	override(t, &messageLabelFingerprintLimit, 1000000)
	for i, sample := range samples {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		request := httptest.NewRequest("GET", "/api/labels/conversations/"+url.PathEscape(sample.ID), nil).WithContext(ctx)
		response := httptest.NewRecorder()
		server.getMessageLabelFingerprint(response, request)
		if response.Code != 200 {
			cancel()
			t.Fatalf("sample %s: %d %s", sample.ID, response.Code, response.Body.String())
		}
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			cancel()
			t.Fatal(err)
		}
		tools, err := queryMapsContext(ctx, db, `SELECT id,COALESCE(agent_session_id,?) session_id,call_message_id message_id,tool_name name,tool_category category,command_category,status,started_at start,ended_at end,duration_ms FROM tool_calls WHERE conversation_id=? ORDER BY sequence,id`, sample.ID, sample.ID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		conversation := value["conversation"].(map[string]any)
		conversation["activity"] = tools
		value["study"] = map[string]any{"name": sample.Name, "population": sample.Population, "synthetic": false, "full_history": value["truncated"] != true, "read_ms": time.Since(start).Milliseconds()}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, sample.ID+".json"), encoded, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d/%d %s: %d messages, %d events, %d tools, %s", i+1, len(samples), sample.Name, len(conversation["messages"].([]any)), len(value["agent_events"].([]any)), len(tools), time.Since(start))
	}
}
