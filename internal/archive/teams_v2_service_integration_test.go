package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// This is only for the dedicated unsigned-control, literal-loopback harness.
// Its catalog, source data, installation key and identities are all synthetic.
func TestTeamsV2SyntheticPostgres(t *testing.T) {
	origin := os.Getenv("PHAROS_TEAMS_SYNTHETIC_ORIGIN")
	if origin == "" {
		t.Skip("no synthetic integration harness")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("synthetic harness must be literal loopback")
	}
	useHost(t, "synthetic-host")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	control := func(path string, in, out any) {
		t.Helper()
		b, _ := json.Marshal(in)
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			io.Copy(io.Discard, resp.Body)
			t.Fatalf("synthetic control %s status %d", path, resp.StatusCode)
		}
		if json.NewDecoder(resp.Body).Decode(out) != nil {
			t.Fatal("invalid synthetic control")
		}
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	const library = "synthetic-library-a"
	attempt, err := teams.BeginEnrollmentWithKey(ctx, origin, library, "Synthetic library", "1", key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.Close()
	approval, err := url.Parse(attempt.ApprovalURL)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(approval.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	var authorized struct {
		CallbackURL string `json:"callback_url"`
	}
	control("/_test/authorize", map[string]string{"enrollment_id": strings.TrimPrefix(approval.Path, "/enroll/"), "enrollment_confirmation": fragment.Get("confirmation"), "fingerprint": attempt.Fingerprint}, &authorized)
	callback, err := url.Parse(authorized.CallbackURL)
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/teams/enrollment" {
		t.Fatal("invalid synthetic callback")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("callback status %d", response.StatusCode)
	}
	credential, err := attempt.Redeem(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	client, err := teams.NewDeviceClient(origin, credential.InstallationID, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "application")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	d.ID = library
	if err = durableJSON(descriptorPath(cfg), d); err != nil {
		t.Fatal(err)
	}
	credentialBytes, err := credential.CredentialBytes(d.Instance, installationID(cfg.Application))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credentialBytes)
	override(t, &teamsLoadCredential, func(_ *Catalog, _ Config, desc LibraryDescriptor) (*teams.EnrollmentCredential, error) {
		return teams.ParseCredential(credentialBytes, desc.ID, desc.Instance, installationID(cfg.Application), true)
	})
	override(t, &teamsDeviceClient, func(origin, installation string, key ed25519.PrivateKey, _ bool) (*teams.DeviceClient, error) {
		return teams.NewDeviceClient(origin, installation, key, true)
	})
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, false, credential.MetricsDefault.PolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := codexLines("synthetic-thread", 1) + `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":123},"last_token_usage":{"total_tokens":123}}}}` + "\n"
	writeSourceFile(t, file, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	var binding, conversation string
	var revision int64
	if err = c.DB.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE superseded_at=''`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err = c.reviewTeamsSourceMapping(ctx, d, credential, binding, "work", revision, true); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id='synthetic-thread'`).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	if _, err = c.setTeamsPrivate(ctx, conversation, true); err != nil {
		t.Fatal(err)
	}
	var issued struct {
		VersionID string `json:"version_id"`
	}
	control("/_test/send", map[string]string{"library_id": library}, &issued)
	manifest := teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	poll, err := client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{library}, CapabilityManifest: &manifest})
	if err != nil {
		t.Fatal(err)
	}
	var work *teams.Work
	for i := range poll.Work {
		if poll.Work[i].VersionID == issued.VersionID {
			work = &poll.Work[i]
		}
	}
	if work == nil {
		t.Fatalf("synthetic work missing: status %q", poll.CapabilityStatus)
	}
	override(t, &teamsWorkerCapabilities, func() teamsCapabilityManifest { return manifest })
	override(t, &teamsLoadSourceConfig, func(string) (Config, error) { return cfg, nil })
	service := testServer(t, cfg, c)
	if _, err = service.checkTeamsWork(ctx); err != nil {
		t.Fatalf("worker check: %v", err)
	}
	draft, err := readTeamsV2Draft(ctx, c.reader(), work.ID)
	if err != nil || draft.State != "accepted" {
		t.Fatalf("worker draft: %s %v", draft.State, err)
	}
	if !bytes.Contains(draft.Payload, []byte(`"total_tokens":123`)) {
		t.Fatal("synthetic result was not 123")
	}
	var receipt teams.Receipt
	if err = json.Unmarshal([]byte(draft.ReceiptJSON), &receipt); err != nil || (receipt.PublicationState != "staged" && receipt.PublicationState != "published") {
		t.Fatalf("worker receipt: %v %s", err, receipt.PublicationState)
	}
	var view struct {
		Rows []struct {
			Values map[string]json.RawMessage `json:"values"`
		} `json:"rows"`
		Coverage []struct {
			SourceState string `json:"source_state"`
		} `json:"coverage"`
	}
	control("/_test/read", map[string]string{"version_id": issued.VersionID}, &view)
	if len(view.Rows) != 1 || string(view.Rows[0].Values["total_tokens"]) != "123" || len(view.Coverage) != 1 || view.Coverage[0].SourceState != "partial" {
		t.Fatalf("projection did not contain the partial synthetic total: %+v", view)
	}
}
