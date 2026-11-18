package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/gbdubs/pharos/internal/teams"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamsLocalProtectionPolicyScope(t *testing.T) {
	scope := teamsProtectionPolicyAcknowledgement{Origin: "https://synthetic.invalid", OrgID: "org", PersonID: "person", InstallationID: "installation", LibraryID: "library", InstanceID: "instance", EnrollmentGeneration: strings.Repeat("a", 32)}
	ack := scope
	ack.PolicyID, ack.PolicyDigest = teamsLocalProtectionPolicyID, teamsLocalProtectionPolicyDigest()
	if !ack.applies(scope) {
		t.Fatal("exact acknowledgement rejected")
	}
	for _, change := range []func(*teamsProtectionPolicyAcknowledgement){
		func(a *teamsProtectionPolicyAcknowledgement) { a.PolicyDigest = "" },
		func(a *teamsProtectionPolicyAcknowledgement) { a.PersonID = "other" },
		func(a *teamsProtectionPolicyAcknowledgement) { a.InstallationID = "other" },
		func(a *teamsProtectionPolicyAcknowledgement) { a.InstanceID = "other" },
		func(a *teamsProtectionPolicyAcknowledgement) { a.EnrollmentGeneration = "other" },
	} {
		bad := ack
		change(&bad)
		if bad.applies(scope) {
			t.Fatal("mismatched acknowledgement accepted")
		}
	}
	if scope.applies(scope) {
		t.Fatal("numeric or absent acknowledgement accepted")
	}
}

func TestTeamsProtectionPolicyLocalSettingsAction(t *testing.T) {
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
	// Exercise the actual authenticated settings action through the fenced
	// writer, with no production activation.
	writer, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.db.Close()
	tx, err := teamsBeginProtectedWrite(context.Background(), writer)
	if err != nil {
		t.Fatal(err)
	}
	if err = teamsInstallProtectionFence(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	writer.authorized.Store(true)
	if err = teamsCaptureOpenContract(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	writer.authorized.Store(false)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &teamsCheckpointTestStore{}
	scope := teamsCheckpointScope("app", d.ID)
	guardKey := bytes.Repeat([]byte{42}, 32)
	if err = teamsCheckpointActivate(context.Background(), writer, store, "checkpoint", scope, teamsCheckpointInstance(d.ID, d.Instance), guardKey, func(tx *sql.Tx) error { return teamsVerifyProtectionFence(context.Background(), tx) }, nil); err != nil {
		t.Fatal(err)
	}
	writer.requireFence = true
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: writer, store: store, name: "checkpoint", scope: scope, guardKey: guardKey}
	defer func() { c.teamsCheckpoint = nil }()
	server := testServer(t, cfg, c)
	path := "/api/teams/worker/protection-policy"
	if w := apiCall(t, server, "", http.MethodGet, path, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	w := apiCall(t, server, cfg.APIToken, http.MethodGet, path, nil)
	var initial struct {
		Acknowledged     bool   `json:"acknowledged"`
		SettingsRevision int64  `json:"settingsRevision"`
		Disclosure       string `json:"disclosure"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.Acknowledged || initial.Disclosure != teamsLocalProtectionDisclosure {
		t.Fatalf("initial policy: %d %s", w.Code, w.Body.String())
	}
	if w = apiCall(t, server, cfg.APIToken, http.MethodPost, path, map[string]any{"acknowledge": true, "settingsRevision": initial.SettingsRevision + 1}); w.Code != 409 {
		t.Fatalf("stale action accepted: %d", w.Code)
	}
	if w = apiCall(t, server, cfg.APIToken, http.MethodPost, path, map[string]any{"acknowledge": true, "settingsRevision": initial.SettingsRevision}); w.Code != 200 {
		t.Fatalf("acknowledge: %d %s", w.Code, w.Body.String())
	}
	w = apiCall(t, server, cfg.APIToken, http.MethodGet, path, nil)
	var after struct {
		Acknowledged     bool  `json:"acknowledged"`
		SettingsRevision int64 `json:"settingsRevision"`
	}
	if json.Unmarshal(w.Body.Bytes(), &after) != nil || !after.Acknowledged || after.SettingsRevision != initial.SettingsRevision+1 {
		t.Fatalf("policy not retained: %s", w.Body.String())
	}
	settings, err := readTeamsSettings(context.Background(), c.reader())
	if err != nil || settings.AcknowledgedPolicy != 0 || settings.Enabled {
		t.Fatalf("numeric policy changed: %+v %v", settings, err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_baseline WHERE state='held'`); n != 1 {
		t.Fatal("acknowledgement cleared hold")
	}
	transfer, err := readTeamsSourceTransferAcknowledgement(context.Background(), c.reader(), d, credential)
	if err != nil || transfer {
		t.Fatalf("local-only acknowledgement granted transfer: %v %v", transfer, err)
	}
	if !strings.Contains(teamsSourceTransferDisclosure(credential), credential.Origin) || !strings.Contains(teamsSourceTransferDisclosure(credential), credential.OrgID) {
		t.Fatal("transfer disclosure omitted exact destination")
	}
	if err = c.setTeamsSourceTransferChoice(context.Background(), d, credential, settings.SettingsRevision, true); !errors.Is(err, errTeamsProtectionHeld) {
		t.Fatalf("format 34 allowed source transfer: %v", err)
	}
	if err = c.writeTransaction(context.Background(), "synthetic scope change", func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE teams_local_settings SET instance_id='different-instance' WHERE id=1`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, c, `SELECT count(*) FROM teams_protection_policy_ack`); n != 0 {
		t.Fatal("scope change retained acknowledgement")
	}
}
