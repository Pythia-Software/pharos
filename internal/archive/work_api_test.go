package archive

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestWorkAPIBackfillFreezesItsActivityWindow(t *testing.T) {
	c, config := testCatalog(t)
	config.WorkEngine = "scheduled"
	s := testServer(t, config, c)
	s.work.once.Do(func() {})
	_, selected := ingestRevisionFixture(t, c)
	execTx(t, c.DB, `INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) SELECT 'outside-window',workspace_id,provider,account,'outside-window','2020-01-01T00:00:00Z' FROM conversations LIMIT 1`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('outside-message','outside-window','outside-message','user','message','Older work','older','2020-01-01T00:00:00Z',1)`)
	from, until := "2026-09-20T00:00:00Z", "2026-09-21T00:00:00Z"
	for _, body := range []map[string]any{
		{"action": "backfill", "from": from},
		{"action": "backfill", "from": until, "until": from},
		{"action": "recent", "from": from, "until": until},
	} {
		if response := apiCall(t, s, config.APIToken, http.MethodPost, "/api/work", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid window accepted: %d %s", response.Code, response.Body.String())
		}
	}
	response := apiCall(t, s, config.APIToken, http.MethodPost, "/api/work", map[string]any{"action": "backfill", "kinds": []string{"maintenance.parallelism"}, "from": from, "until": until})
	var result struct {
		ID    uint64 `json:"request_id"`
		From  string `json:"from"`
		Until string `json:"until"`
	}
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.ID == 0 || result.From != from || result.Until != until {
		t.Fatalf("backfill window: %d %s", response.Code, response.Body.String())
	}
	s.work.mu.Lock()
	request := s.work.requests[result.ID]
	s.work.mu.Unlock()
	waitUntil(t, 5*time.Second, "window settlement", func() bool {
		s.work.runner.Pump()
		select {
		case <-request.done:
			return true
		default:
			return false
		}
	})
	if err := s.work.wait(t.Context(), request); err != nil || request.facts.Members != 1 {
		t.Fatalf("window coverage: %+v %v", request.facts, err)
	}
	if pending, err := c.parallelismPendingFor(t.Context(), []string{selected, "outside-window"}); err != nil || len(pending) != 1 || pending[0] != "outside-window" {
		t.Fatalf("window processed unrelated work or missed its member: %v %v", pending, err)
	}
}
