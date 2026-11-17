package archive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsSourceAccountsLocalAPI(t *testing.T) {
	support := t.TempDir()
	t.Setenv("PHAROS_SUPPORT_DIR", support)
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(support, "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic library")
	if err != nil {
		t.Fatal(err)
	}
	key, err := teamsInstallationKey(cfg, "https://synthetic.invalid", true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	appID := installationID(cfg.Application)
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": appID, "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, appID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if err = c.saveTeamsCredential(context.Background(), cfg, d, credential); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO teams_source_bindings(binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,created_at) VALUES('binding','hash','synthetic-host','synthetic-source','codex','codex','local','/synthetic','now')`,
		`INSERT INTO source_states(host_id,source_name,kind,capability,coverage,updated_at) VALUES('synthetic-host','unsupported-source','other','incremental','complete','now')`)
	s := testServer(t, cfg, c)
	path := "/api/teams/source-accounts"
	if w := apiCall(t, s, "", http.MethodGet, path, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	w := apiCall(t, s, cfg.APIToken, http.MethodGet, path, nil)
	var status struct {
		SourceRevision   int64                   `json:"sourceRevision"`
		SettingsRevision int64                   `json:"settingsRevision"`
		Sources          []teamsSourceAccountRow `json:"sources"`
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &status) != nil || len(status.Sources) != 2 || status.Sources[0].Mode != "unassigned" || !status.Sources[1].Unindexed {
		t.Fatalf("initial status: %d %s", w.Code, w.Body.String())
	}
	invalid := `{"sourceBindingId":"binding","expectedRevision":0,"mode":"work","attested":true,"Attested":false}`
	if w = apiCall(t, s, cfg.APIToken, http.MethodPost, path+"/review", invalid); w.Code != 400 {
		t.Fatalf("case-alias accepted: %d", w.Code)
	}
	body := map[string]any{"sourceBindingId": "binding", "expectedRevision": status.SourceRevision, "mode": "work", "attested": true}
	if w = apiCall(t, s, cfg.APIToken, http.MethodPost, path+"/review", body); w.Code != 200 {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	if w = apiCall(t, s, cfg.APIToken, http.MethodPost, path+"/review", body); w.Code != 409 {
		t.Fatalf("stale review accepted: %d", w.Code)
	}
	w = apiCall(t, s, cfg.APIToken, http.MethodGet, path, nil)
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Sources[0].Mode != "work" {
		t.Fatalf("review not reflected: %s", w.Body.String())
	}
}
