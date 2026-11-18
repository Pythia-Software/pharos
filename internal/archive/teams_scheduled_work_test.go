package archive

import (
	"encoding/json"
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
}
