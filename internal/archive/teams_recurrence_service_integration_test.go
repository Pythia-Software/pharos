package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
func TestTeamsRecurringSyntheticPostgres(t *testing.T) {
	testTeamsRecurringResult(t, os.Getenv("PHAROS_TEAMS_SYNTHETIC_RECURRENCE_ORIGIN"), false, false)
}

func TestTeamsScheduledReportAutomaticPostgres(t *testing.T) {
	testTeamsRecurringResult(t, os.Getenv("PHAROS_TEAMS_SYNTHETIC_SCHEDULED_ORIGIN"), true, false)
}

func TestTeamsScheduledReportReviewedPostgres(t *testing.T) {
	testTeamsRecurringResult(t, os.Getenv("PHAROS_TEAMS_SYNTHETIC_SCHEDULED_ORIGIN"), true, true)
}

func testTeamsRecurringResult(t *testing.T, origin string, scheduledReport, optOut bool) {
	t.Helper()
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
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, optOut, credential.MetricsDefault.PolicyVersion)
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
	if scheduledReport {
		control("/_test/send-scheduled", map[string]string{"library_id": library}, &issued)
	} else {
		control("/_test/send", map[string]any{"library_id": library, "recurring": true}, &issued)
	}
	manifest := teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "recurring_deterministic_v1", "source_snapshot_v2"}}
	if scheduledReport {
		// A pilot capability supplied only to this paired loopback fixture.
		// The production build advertises it after integrated rollout.
		manifest.V2OnlyFeatures = []string{"reader_labels_v1", "report_automatic_metrics_v1"}
	}
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
	if scheduledReport && (work.ScheduledAssignment == nil || work.ScheduledAssignment.AssignmentID != work.ID || work.ScheduledAssignment.PeriodKey != "period:0") {
		t.Fatal("missing scheduled assignment from signed poll")
	}
	if scheduledReport {
		if !teamsNumericWorkEligible(*work, manifest) {
			t.Fatal("issued scheduled work fails native capability check")
		}
		if _, planErr := teamsV2PlanFromWork(*work); planErr != nil {
			t.Fatalf("issued scheduled work fails plan validation: %v", planErr)
		}
	}
	if optOut {
		reviewScheduledSyntheticWork(t, service, *work)
		if _, err = service.checkTeamsWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	draft, err := readTeamsV2Draft(ctx, c.reader(), work.ID)
	if err != nil || draft.State != "accepted" {
		t.Fatalf("worker draft: %s (%s) %v", draft.State, service.teamsWorker.items[work.ID].State, err)
	}
	if !bytes.Contains(draft.Payload, []byte(`"total_tokens":123`)) {
		t.Fatal("synthetic result was not 123")
	}
	var receipt teams.Receipt
	if err = json.Unmarshal([]byte(draft.ReceiptJSON), &receipt); err != nil || (receipt.PublicationState != "staged" && receipt.PublicationState != "published") {
		t.Fatalf("worker receipt: %v %s", err, receipt.PublicationState)
	}
	if scheduledReport {
		var submitted teams.Submission
		upload, e := readTeamsV2Upload(ctx, c.reader(), work.ID)
		if e != nil || json.Unmarshal(draft.Submission, &submitted) != nil || !sameTeamsScheduledAssignment(submitted.ScheduledAssignment, work.ScheduledAssignment) || !matchesTeamsSourceAuthorization(upload.Authorization, work.ScheduledAssignment) || receipt.SubmissionSHA256 != teamsPayloadHash(draft.Submission) {
			t.Fatal("accepted receipt does not bind the exact source authorization, assignment, and submission")
		}
		if optOut && (submitted.Approval.Mode != "reviewed" || submitted.Approval.DecisionID == "") {
			t.Fatal("missing exact result review after opt-out")
		}
		if !optOut && (submitted.Approval.Mode != "automatic_metrics" || submitted.Approval.OrgPolicyVersion != credential.MetricsDefault.PolicyVersion) {
			t.Fatal("automatic result did not acknowledge the current organization policy")
		}
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

func reviewScheduledSyntheticWork(t *testing.T, s *Server, work teams.Work) {
	t.Helper()
	call := func(method, path string, body any) []byte {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		rec := httptest.NewRecorder()
		s.teamsWorkerHTTP(rec, httptest.NewRequest(method, "/api/teams/worker"+path, bytes.NewReader(raw)))
		if rec.Code != 200 {
			t.Fatalf("local review %s: %d", path, rec.Code)
		}
		return rec.Body.Bytes()
	}
	for _, state := range []string{"awaiting_source_consent", "awaiting_review"} {
		call(http.MethodPost, "/reader-labels", map[string]string{"workId": work.ID, "configHash": work.ConfigSHA256})
		var view struct {
			SettingsRevision int64 `json:"settingsRevision"`
			Items            []struct {
				WorkID      string                     `json:"workId"`
				State       string                     `json:"state"`
				Token       string                     `json:"labelsToken"`
				Ready       bool                       `json:"labelsReady"`
				PayloadHash string                     `json:"payloadHash"`
				Payload     json.RawMessage            `json:"payload"`
				Terms       *teamsScheduledDisclosure  `json:"scheduledTerms"`
				Assignment  *teams.ScheduledAssignment `json:"scheduledAssignment"`
			} `json:"items"`
		}
		if err := json.Unmarshal(call(http.MethodGet, "", nil), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Items) != 1 {
			t.Fatalf("expected exactly one work card, got %d", len(view.Items))
		}
		i := view.Items[0]
		if i.WorkID != work.ID || i.State != state || !i.Ready || len(i.Token) != 64 || i.Terms == nil || !sameTeamsScheduledAssignment(i.Assignment, work.ScheduledAssignment) {
			t.Fatalf("missing scheduled consent in %s (state %s)", state, i.State)
		}
		if state == "awaiting_source_consent" {
			call(http.MethodPost, "/allow-source", map[string]any{"workId": work.ID, "configHash": work.ConfigSHA256, "settingsRevision": view.SettingsRevision, "labelsToken": i.Token})
			if _, err := s.checkTeamsWork(context.Background()); err != nil {
				t.Fatal(err)
			}
		} else {
			if i.PayloadHash != teamsPayloadHash(i.Payload) || !bytes.Contains(i.Payload, []byte(`"total_tokens":123`)) {
				t.Fatal("unexpected result for review")
			}
			call(http.MethodPost, "/approve-result", map[string]string{"workId": work.ID, "configHash": work.ConfigSHA256, "payloadHash": i.PayloadHash, "labelsToken": i.Token})
		}
	}
}
