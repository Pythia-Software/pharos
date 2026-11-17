package archive

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsModelPermissionIsIdentityBoundAndRevocable(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	app := "synthetic-app"
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": app, "private_key": base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32))), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, app, false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash([]byte("synthetic")), true, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	view := teamsModelInspection{Provider: "Claude", Model: "synthetic-model", Effort: "Low", Account: "synthetic", SessionDigest: strings.Repeat("a", 64), AuthDigest: strings.Repeat("b", 64), SettingsRevision: settings.SettingsRevision}
	if err = c.grantTeamsModel(ctx, d, credential, view); err != nil {
		t.Fatal(err)
	}
	if selection,_,e:=c.loadTeamsModelPermission(ctx,d,credential);e!=nil || selection.Model!=view.Model {t.Fatalf("permission could not be read: %v",e)}
	other:=d;other.Instance="different-instance"
	if _,_,e:=c.loadTeamsModelPermission(ctx,other,credential);e==nil {t.Fatal("restored library inherited model permission")}
	if err = c.grantTeamsModel(ctx, d, credential, view); err == nil {
		t.Fatal("stale approval accepted")
	}
	var count int
	if err = c.DB.QueryRow(`SELECT COUNT(*) FROM teams_model_permission`).Scan(&count); err != nil || count != 1 {
		t.Fatal("permission missing", err)
	}
	if err = c.revokeTeamsModel(ctx, d, credential); err != nil {
		t.Fatal(err)
	}
	if _,_,e:=c.loadTeamsModelPermission(ctx,d,credential);e==nil {t.Fatal("revoked model permission remained usable")}
	if err = c.DB.QueryRow(`SELECT COUNT(*) FROM teams_model_permission`).Scan(&count); err != nil || count != 0 {
		t.Fatal("permission not revoked", err)
	}
}
