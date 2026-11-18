package archive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestScheduledAssignmentAtSourceReviewAndSubmission(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	var c teamsScheduledConfig
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	end := start.AddDate(0, 0, 1)
	c.ScheduleBounds.AnchorAt = start
	c.ScheduleBounds.UntilAt = end
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	period := teamsPeriod{Key: "period:0", StartAt: &start, EndAt: &end, AnchorAt: &start}
	pb, err := json.Marshal(period)
	if err != nil {
		t.Fatal(err)
	}
	a := teams.ScheduledAssignment{AssignmentID: "07000000-0000-4000-8000-000000000001", PeriodKey: period.Key, Timezone: "UTC", StartAt: start, DeadlineAt: start.Add(time.Hour)}
	w := teams.Work{ID: a.AssignmentID, Kind: "deterministic", Config: raw, ConfigSHA256: teamsPayloadHash(raw), Period: pb, ScheduledAssignment: &a, PopulationKey: "current", ExpiresAt: a.DeadlineAt}
	if _, err = teamsV2PlanFromWork(w); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"start_at", "end_at"} {
		test := w
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(w.Period, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, missing)
		test.Period, _ = json.Marshal(fields)
		if _, err = teamsV2PlanFromWork(test); err == nil {
			t.Fatalf("accepted period missing %s", missing)
		}
	}
	once := w
	once.ScheduledAssignment = nil
	once.Period = json.RawMessage(`{"key":"once"}`)
	if _, err = teamsV2PlanFromWork(once); err == nil {
		t.Fatal("scheduled publication reached manual report fallback")
	}
	if _, err = teamsPlanFromWork(w); err == nil {
		t.Fatal("legacy source path accepted a scheduled publication")
	}
	if !validTeamsSeriesReview(raw, w.ConfigSHA256, w.ConfigSHA256) || validTeamsSeriesReview(raw, w.ConfigSHA256, "") {
		t.Fatal("review must bind the exact scheduled config")
	}
	manifest := teamsCapabilityManifest{SchemaVersion: 2, AppVersion: "0.10.0", Features: []string{"deterministic_query_v1", "reader_labels_v1", "recurring_deterministic_v1", "report_automatic_metrics_v1", "source_snapshot_v2"}}
	if !teamsNumericWorkEligible(w, manifest) {
		t.Fatal("full capability rejected")
	}
	for i, feature := range manifest.Features {
		test := manifest
		test.Features = append(append([]string{}, manifest.Features[:i]...), manifest.Features[i+1:]...)
		if teamsNumericWorkEligible(w, test) {
			t.Fatalf("eligible without %s", feature)
		}
	}
	source, err := json.Marshal(teams.SourceAuthorization{ScheduledAssignment: &a})
	if err != nil {
		t.Fatal(err)
	}
	if !matchesTeamsSourceAuthorization(source, &a) || matchesTeamsSourceAuthorization(source, nil) || matchesTeamsSourceAuthorization([]byte("{}"), &a) {
		t.Fatal("lost scheduled source binding")
	}
	for _, mutate := range []func(*teams.ScheduledAssignment){
		func(v *teams.ScheduledAssignment) { v.AssignmentID = "07000000-0000-4000-8000-000000000002" },
		func(v *teams.ScheduledAssignment) { v.PeriodKey = "period:1" },
		func(v *teams.ScheduledAssignment) { v.Timezone = "Etc/UTC" },
		func(v *teams.ScheduledAssignment) { v.StartAt = v.StartAt.Add(time.Second) },
		func(v *teams.ScheduledAssignment) { v.DeadlineAt = v.DeadlineAt.Add(time.Second) },
	} {
		changed := a
		mutate(&changed)
		w.ScheduledAssignment = &changed
		if _, err = teamsV2PlanFromWork(w); err == nil || matchesTeamsSourceAuthorization(source, &changed) {
			t.Fatal("substituted period accepted")
		}
	}
	w.ScheduledAssignment = &a
	wrongEnd := end.Add(time.Hour)
	period.EndAt = &wrongEnd
	w.Period, _ = json.Marshal(period)
	if _, err = teamsV2PlanFromWork(w); err == nil {
		t.Fatal("different displayed period end accepted")
	}
	// The last collection period can end before its next calendar boundary.
	until := start.Add(20 * time.Minute)
	c.ScheduleBounds.UntilAt = until
	w.Config, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	w.ConfigSHA256 = teamsPayloadHash(w.Config)
	w.ExpiresAt = until
	a.DeadlineAt = until
	period.EndAt = &until
	w.Period, _ = json.Marshal(period)
	if _, err = teamsV2PlanFromWork(w); err != nil {
		t.Fatalf("final partial period rejected: %v", err)
	}
	period.EndAt = &end
	w.Period, _ = json.Marshal(period)
	if _, err = teamsV2PlanFromWork(w); err == nil {
		t.Fatal("final period extended past consent")
	}
}

func TestScheduledResultDisplaysExactInteger(t *testing.T) {
	raw := []byte(`{"outcome":"completed","form":"snapshot","coverage":{"source_state":"partial"},"rows":[{"key":[],"values":{"total_tokens":9007199254740993}}]}`)
	view := teamsScheduledResultForDisplay(raw, teamsPayloadHash(raw))
	if view == nil || view.Total != "9007199254740993" || view.ExactJSON != string(raw) || teamsScheduledResultForDisplay(raw, "") != nil {
		t.Fatal("result view lost an exact integer or accepted an unbound payload")
	}
}

func TestScheduledSecondPeriodFromIssuer(t *testing.T) {
	// Period and assignment from consecutive leased production-role issuer calls.
	const period = `{"key":"period:1","start_at":"2026-11-18T07:28:12.000000Z","end_at":"2026-11-19T07:28:12.000000Z","anchor_at":"2026-11-18T07:28:12.000000Z"}`
	const assignment = `{"assignment_id":"2689dfb8-f8a0-48ce-8403-3f8db5e57988","period_key":"period:1","timezone":"UTC","start_at":"2026-11-18T07:28:12Z","deadline_at":"2026-11-19T08:28:12Z"}`
	var a teams.ScheduledAssignment
	if err := json.Unmarshal([]byte(assignment), &a); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/teams-report-series/scheduled_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseTeamsScheduledConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	c.Schedule.ResponseWindowSeconds = 172800
	c.ScheduleBounds.AnchorAt = a.StartAt.AddDate(0, 0, -1)
	c.ScheduleBounds.UntilAt = a.DeadlineAt
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	w := teams.Work{ID: a.AssignmentID, Kind: "deterministic", Config: raw, ConfigSHA256: teamsPayloadHash(raw), Period: json.RawMessage(period), ScheduledAssignment: &a, PopulationKey: "current", ExpiresAt: a.DeadlineAt}
	if _, err = teamsV2PlanFromWorkAt(w, a.StartAt); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsV2PlanFromWorkAt(w, a.DeadlineAt); !errors.Is(err, errTeamsScheduledPeriodEnded) {
		t.Fatal("closed period did not report the deadline", err)
	}
}

func TestScheduledModeAndSourceLedgerMustMatch(t *testing.T) {
	start := time.Now().UTC().Truncate(time.Second)
	work := "07000000-0000-4000-8000-000000000001"
	snapshot := "07000000-0000-4000-8000-000000000002"
	a := teams.ScheduledAssignment{AssignmentID: work, PeriodKey: "period:0", Timezone: "UTC", StartAt: start, DeadlineAt: start.Add(time.Hour)}
	digest := teamsPayloadHash([]byte("synthetic"))
	u := teamsV2Upload{WorkID: work, SnapshotID: snapshot, ConfigHash: digest, InventoryHash: digest, LocalRevision: 1, SettingsRevision: 1, InventoryRevision: 1, EvaluationRevision: 1, PolicyVersion: 2, Mode: "automatic_metrics"}
	source := teams.SourceAuthorization{ScheduledAssignment: &a, SnapshotID: snapshot, InventoryRevision: 1, InventorySHA256: digest, LocalRevision: 1, EvaluationRevision: 1, ConfigSHA256: digest, Mode: u.Mode, DecidedAt: start, SettingsRevision: 1, OrgPolicyVersion: 2}
	u.Authorization, _ = json.Marshal(source)
	if !u.matchesSourceProof(&a) {
		t.Fatal("current automatic proof rejected")
	}
	for _, mutate := range []func(*teams.SourceAuthorization){
		func(s *teams.SourceAuthorization) {
			s.Mode = "reviewed"
			s.OrgPolicyVersion = 0
			s.DecisionID = snapshot
		},
		func(s *teams.SourceAuthorization) { s.OrgPolicyVersion++ },
		func(s *teams.SourceAuthorization) { s.InventoryRevision++ },
		func(s *teams.SourceAuthorization) { s.DecisionID = snapshot },
		func(s *teams.SourceAuthorization) {
			s.Mode = "standing"
			s.DecisionID = snapshot
			s.OrgPolicyVersion = 0
		},
	} {
		changed := source
		mutate(&changed)
		u.Authorization, _ = json.Marshal(changed)
		if u.matchesSourceProof(&a) {
			t.Fatal("source authorization mutation accepted")
		}
	}
	source.Mode = "reviewed"
	source.OrgPolicyVersion = 0
	source.DecisionID = snapshot
	u.Mode = source.Mode
	u.PolicyVersion = 0
	u.Authorization, _ = json.Marshal(source)
	if !u.matchesSourceProof(&a) {
		t.Fatal("valid distinct source review rejected")
	}
	c, cfg := testCatalog(t)
	d, e := ensureLibraryDescriptor(cfg, "Synthetic")
	if e != nil {
		t.Fatal(e)
	}
	credential := &teams.EnrollmentCredential{OrgID: "synthetic-org", InstallationID: "synthetic-installation"}
	_, e = c.DB.Exec(`INSERT INTO teams_v2_uploads(work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,state) VALUES(?,?,?,?,?,?,?,?,?,1,?,1,1,1,0,0,0,'reviewed',0,?,?,'{}','sealed')`, work, snapshot, d.ID, d.Instance, credential.OrgID, credential.InstallationID, digest, snapshot, snapshot, digest, u.Authorization, []byte("[]"))
	if e != nil {
		t.Fatal(e)
	}
	w := teams.Work{ID: work, ConfigSHA256: digest, ScheduledAssignment: &a}
	for _, failure := range []*teams.HTTPError{{Status: 404, Code: "source_authorization_expired"}, {Status: 409, Code: "source_authorization_required"}} {
		if c.scheduledSourceFailure(context.Background(), w, d, credential, failure, "unavailable") != "unavailable" {
			t.Fatal("unknown authorization classified as expired")
		}
	}
	expired := &teams.HTTPError{Status: 409, Code: "source_authorization_expired"}
	other := w
	other.ConfigSHA256 = teamsPayloadHash([]byte("other"))
	if c.scheduledSourceFailure(context.Background(), other, d, credential, expired, "unavailable") != "unavailable" {
		t.Fatal("expiry crossed config scope")
	}
	if c.scheduledSourceFailure(context.Background(), w, d, credential, expired, "unavailable") != teamsExpiredScheduleState {
		t.Fatal("definite expiry was not held")
	}
	stored, e := readTeamsV2Upload(context.Background(), c.reader(), work)
	if e != nil || stored.State != "abandoned" {
		t.Fatal("expired upload was not durably retired")
	}

}
