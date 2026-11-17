package archive

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsCredentialProtectedAndBoundToInstance(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("protected-file host lane unsupported")
	}
	support := t.TempDir()
	t.Setenv("PHAROS_SUPPORT_DIR", support)
	c, cfg := testCatalog(t)
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	application := filepath.Join(support, "Pharos.app")
	appID := installationID(application)
	cfg.Application = application
	key, err := teamsInstallationKey(cfg, "https://teams.example", true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	again, err := teamsInstallationKey(cfg, "https://teams.example/", true)
	if err != nil || !bytes.Equal(key, again) {
		t.Fatalf("installation key not reused: %v", err)
	}
	clear(again)
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://teams.example", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": appID, "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, appID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.saveTeamsCredential(context.Background(), cfg, d, credential); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(support, "teams-credentials", teamsCredentialName(appID, d.ID))
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file mode: %v", err)
	}
	s, err := readTeamsSettings(context.Background(), c.reader())
	if err != nil || s.Enabled || !s.MetricsOptOut || s.AcknowledgedPolicy != 0 {
		t.Fatalf("enrollment not disabled: %+v %v", s, err)
	}
	loaded, err := c.loadTeamsCredential(cfg, d)
	if err != nil || loaded.InstallationID != credential.InstallationID {
		t.Fatalf("load: %v", err)
	}
	other := cfg
	other.Application = filepath.Join(support, "Another.app")
	if _, err = c.loadTeamsCredential(other, d); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other installation accessed credential: %v", err)
	}
	overlap := cfg
	overlap.ArchiveRoot = filepath.Join(support, "teams-credentials")
	if _, err = teamsCredentialStore(overlap); !errors.Is(err, credentialstore.ErrUnsafe) {
		t.Fatalf("credential root inside archive: %v", err)
	}
	if err = c.saveTeamsCredential(context.Background(), cfg, d, credential); !errors.Is(err, credentialstore.ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
	otherCatalog, otherConfig := testCatalog(t)
	otherConfig.Application = application
	otherDescriptor := LibraryDescriptor{ID: "other-library", Instance: "other-instance"}
	var different map[string]any
	if json.Unmarshal(raw, &different) != nil {
		t.Fatal("fixture")
	}
	different["library_id"] = otherDescriptor.ID
	different["library_instance"] = otherDescriptor.Instance
	different["source_token_key"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	differentRaw, _ := json.Marshal(different)
	otherCredential, e := teams.ParseCredential(differentRaw, otherDescriptor.ID, otherDescriptor.Instance, appID, false)
	if e != nil {
		t.Fatal(e)
	}
	defer otherCredential.Destroy()
	if e = otherCatalog.saveTeamsCredential(context.Background(), otherConfig, otherDescriptor, otherCredential); !errors.Is(e, errTeamsNotEnrolled) {
		t.Fatalf("different person source key accepted: %v", e)
	}
	d.Instance = "recovered-instance"
	if _, err = c.loadTeamsCredential(cfg, d); !errors.Is(err, errTeamsNotEnrolled) {
		t.Fatalf("restored library accepted: %v", err)
	}
}
