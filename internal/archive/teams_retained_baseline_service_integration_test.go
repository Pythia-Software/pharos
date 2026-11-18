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

func TestTeamsRetainedBaselineSyntheticLoopback(t *testing.T) {
	origin := os.Getenv("PHAROS_TEAMS_SYNTHETIC_ORIGIN")
	corpus := os.Getenv("PHAROS_TEAMS_SYNTHETIC_CORPUS")
	if origin == "" || corpus == "" {
		t.Skip("no isolated synthetic corpus and service")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("synthetic service must be loopback")
	}
	useHost(t, "synthetic-managed-birth")
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	override(t, &teamsBirthHardwareUUID, func() (string, error) { return "synthetic-hardware", nil })
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	parent := t.TempDir()
	cfg, err := teamsCreateManagedLibrary(ctx, filepath.Join(parent, "Synthetic.app"), filepath.Join(parent, "fresh"), "Fresh synthetic")
	if err != nil {
		t.Fatal(err)
	}
	d, err := readLibraryDescriptor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, found, err := teamsOpenManagedCatalog(ctx, cfg)
	if err != nil || !found {
		t.Fatalf("managed open: %v", err)
	}
	defer c.Close()
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
			t.Fatalf("control %s status %d", path, resp.StatusCode)
		}
		if json.NewDecoder(resp.Body).Decode(out) != nil {
			t.Fatal("invalid control")
		}
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	library := d.ID
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

	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, false, credential.MetricsDefault.PolicyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	source := SourceConfig{Name: "codex", Kind: "codex", Path: filepath.Join(corpus, "source", "codex"), Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	paths, err := filepath.Glob(filepath.Join(source.Path, "sessions", "*.jsonl"))
	if err != nil || len(paths) != 3 {
		t.Fatal("expected three copied synthetic inputs")
	}
	runCapture(t, cfg)
	indexCaptures(t, c, cfg.CaptureRoot, "synthetic-managed-birth")
	for _, path := range paths {
		if _, err = c.captureTeamsTranscript(ctx, cfg, source, path); err != nil {
			t.Fatal(err)
		}
		indexCaptures(t, c, cfg.CaptureRoot, "synthetic-managed-birth")
	}
	for _, x := range []struct {
		id      string
		private bool
	}{{"00000000-0000-4000-8000-000000001001", false}, {"00000000-0000-4000-8000-000000001002", true}, {"00000000-0000-4000-8000-000000001003", false}} {
		var id string
		if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id=?`, x.id).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if x.private {
			if _, err = c.setTeamsPrivate(ctx, id, true); err != nil {
				t.Fatal(err)
			}
			continue
		}
		input, e := teamsManualReviewInput(ctx, c.reader(), id)
		if e != nil {
			t.Fatal(e)
		}
		privacy, e := c.teamsPrivacy(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.manuallyClearTeamsInput(ctx, id, input, privacy.Revision); e != nil {
			t.Fatal(e)
		}
	}
	var binding string
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
	settings, err = readTeamsSettings(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.acknowledgeTeamsProtectionPolicy(ctx, d, credential, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	settings, err = readTeamsSettings(ctx, c.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.setTeamsSourceTransferChoice(ctx, d, credential, settings.SettingsRevision, true); err != nil {
		t.Fatal(err)
	}
	roster, e := teamsCaptureRoster(ctx, cfg)
	if e != nil {
		t.Fatalf("capture roster: %v", e)
	}
	tx, e := c.reader().BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	inventory, e := teamsMetadataInventoryIn(ctx, tx, cfg, d, credential, roster)
	tx.Rollback()
	if e != nil {
		t.Fatalf("metadata inventory: %v", e)
	}
	countsByValue := map[int64]int{}
	for _, n := range inventory.values {
		countsByValue[n]++
	}
	if len(countsByValue) != 3 || countsByValue[250] != 1 || countsByValue[50] != 1 || countsByValue[25] != 1 {
		t.Fatal("synthetic request totals did not match copied inputs")
	}
	manifest := teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"source_snapshot_v2"}, V2OnlyFeatures: []string{"source_protection_v1", "source_recovery_v1"}}
	if _, e = client.Poll(ctx, teams.PollRequest{AppVersion: manifest.AppVersion, LibraryIDs: []string{d.ID}, CapabilityManifest: &manifest}); e != nil {
		t.Fatalf("synthetic capability: %v", e)
	}
	override(t, &teamsNonPrivateSourceProofEnabled, true)
	override(t, &teamsSourcePilotEnabled, true)
	for turn := 0; turn < 12; turn++ {
		err = c.teamsProduceRetainedBaseline(ctx, cfg, client, d, credential)
		if err == nil {
			break
		}
	}
	if err != nil {
		var counts [10]int
		for i, table := range []string{"conversations", "source_record_states", "source_item_states", "source_states", "teams_native_source_proofs", "teams_codex_request_profiles", "teams_codex_capture_custody", "teams_codex_transcript_evidence_v1", "teams_source_bindings", "conversation_identity_links"} {
			_ = c.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&counts[i])
		}
		t.Fatalf("baseline: %v metadata counts=%v", err, counts)
	}
	for turn := 0; turn < 40; turn++ {
		err = c.teamsRunIndependentSourcePilot(ctx, cfg, client, d, credential, manifest, true)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("signed source publication: %v", err)
	}
	var positive, private int
	if err = c.DB.QueryRow(`SELECT count(*) FROM teams_origin_outbox WHERE state='accepted' AND json_extract(observation_json,'$.protection')='non_private'`).Scan(&positive); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT count(*) FROM teams_origin_outbox WHERE state='accepted' AND json_extract(observation_json,'$.protection')='explicit_private'`).Scan(&private); err != nil {
		t.Fatal(err)
	}
	if positive != 2 || private != 1 {
		t.Fatalf("signed protections public=%d private=%d", positive, private)
	}
}
