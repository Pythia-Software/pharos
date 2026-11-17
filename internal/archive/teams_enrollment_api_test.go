package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestTeamsEnrollmentStatusStaysDisabledUntilConfigured(t *testing.T) {
	t.Setenv("PHAROS_TEAMS_URL", "")
	c, cfg := testCatalog(t)
	cfg.Application = t.TempDir() + "/Synthetic.app"
	d, err := ensureLibraryDescriptor(cfg, "Synthetic library")
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t, cfg, c)
	if w := apiCall(t, s, "", http.MethodGet, "/api/teams/enrollment", nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	w := apiCall(t, s, cfg.APIToken, http.MethodGet, "/api/teams/enrollment", nil)
	var v map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v["available"] != false || v["state"] != "disconnected" || v["library"] != "Synthetic library" {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	if w = apiCall(t, s, cfg.APIToken, http.MethodPost, "/api/teams/enrollment", nil); w.Code != 503 {
		t.Fatalf("unconfigured start: %d", w.Code)
	}
	if _, err = c.setTeamsLocalSettings(context.Background(), d.ID, "different-instance", "synthetic-org", "00000000-0000-4000-8000-000000000003", teamsPayloadHash([]byte("synthetic-key")), false, true, 0); err != nil {
		t.Fatal(err)
	}
	w = apiCall(t, s, cfg.APIToken, http.MethodGet, "/api/teams/enrollment", nil)
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v["state"] != "reconcile" || v["available"] != false {
		t.Fatalf("restored: %s", w.Body.String())
	}
}
