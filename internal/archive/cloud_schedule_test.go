package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func cloudTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCloudScheduleCalendarAndDaylightSaving(t *testing.T) {
	for _, tc := range []struct {
		name, after, clock, zone, anchor, want string
		cadence, weekday                       int
	}{
		{"daily", "2026-10-23T15:00:00Z", "02:00", "UTC", "", "2026-10-24T02:00:00Z", 1, 0},
		{"weekly", "2026-10-23T15:00:00Z", "02:00", "America/Denver", "", "2026-10-25T08:00:00Z", 7, 0},
		{"spring gap", "2026-03-08T08:00:00Z", "02:30", "America/Denver", "", "2026-03-08T09:00:00Z", 1, 0},
		{"fall first occurrence", "2026-11-01T06:00:00Z", "01:30", "America/Denver", "", "2026-11-01T07:30:00Z", 1, 0},
		{"fall never repeats", "2026-11-01T08:15:00Z", "01:30", "America/Denver", "", "2026-11-02T08:30:00Z", 1, 0},
		{"exact minute consumed", "2026-10-23T02:00:00Z", "02:00", "UTC", "", "2026-10-24T02:00:00Z", 1, 0},
		{"thirty calendar days", "2026-01-31T20:00:00Z", "07:00", "America/Denver", "2026-01-31", "2026-03-02T14:00:00Z", 30, 0},
		{"anchor survives missed periods", "2026-04-05T20:00:00Z", "07:00", "America/Denver", "2026-01-31", "2026-05-01T13:00:00Z", 30, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := cloudSchedule{cloudScheduleSpec: cloudScheduleSpec{Cadence: tc.cadence, Time: tc.clock, Timezone: tc.zone, Weekday: tc.weekday}, Anchor: tc.anchor}
			next, err := nextCloudTime(s, cloudTestTime(t, tc.after))
			if err != nil || !next.Equal(cloudTestTime(t, tc.want)) {
				t.Fatalf("next backup: %s, %v; want %s", next, err, tc.want)
			}
		})
	}
	for _, spec := range []cloudScheduleSpec{{Cadence: 2, Time: "02:00", Timezone: "UTC"}, {Cadence: 1, Time: "25:00", Timezone: "UTC"}, {Cadence: 1, Time: "2:00", Timezone: "UTC"}, {Cadence: 1, Time: "02:00", Timezone: "Unknown/Nowhere"}, {Cadence: 1, Time: "02:00", Timezone: "Local"}, {Cadence: 7, Time: "02:00", Timezone: "UTC", Weekday: 7}} {
		if spec.validate() == nil {
			t.Fatalf("invalid schedule accepted: %+v", spec)
		}
	}
}

func cloudScheduleFixture(t *testing.T) (*Catalog, *Server, cloudDestination, time.Time) {
	t.Helper()
	c, _ := testCatalog(t)
	config := testConfig(t)
	config.CatalogPath = c.Path
	server := testServer(t, config, c)
	p := cloudConfig{Provider: "gcp", Region: "us-central1", Bucket: "pharos-schedule-test", Object: "pharos/library.tar.zst", Identity: "pharos-uploader", Project: "pharos-example", StorageClass: "STANDARD", Cadence: 1, Time: "02:00", Timezone: "UTC"}
	d := cloudDestination{ID: cloudID(p), plan: p, credential: []byte(`{"fixture":"upload-only"}`), ImportedAt: now()}
	d.Config, _ = json.Marshal(p)
	if err := c.saveCloudDestination(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	at := cloudTestTime(t, "2026-10-23T12:00:00Z")
	for _, kind := range []string{"sample", "full"} {
		run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: kind, State: "complete", Verified: true, StartedAt: at.Add(-time.Hour).Format(time.RFC3339Nano)}
		if err := c.saveCloudRun(context.Background(), &run, nil); err != nil {
			t.Fatal(err)
		}
	}
	return c, server, d, at
}

func onlyCloudSchedule(t *testing.T, c *Catalog) cloudSchedule {
	t.Helper()
	schedules, err := c.cloudSchedules(context.Background())
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules: %+v %v", schedules, err)
	}
	return schedules[0]
}

func TestCloudScheduledBackupsCatchUpOnceAndAllowManualBackups(t *testing.T) {
	c, server, d, at := cloudScheduleFixture(t)
	ctx := context.Background()
	if err := c.setCloudSchedule(ctx, d.ID, true, cloudDefaultSchedule(d.plan), at); err != nil {
		t.Fatal(err)
	}
	started := make(chan cloudRun, 8)
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	finishedAt := at.AddDate(0, 0, 5)
	override(t, &cloudPerformUpload, func(s *Server, ctx context.Context, _ cloudDestination, run cloudRun) {
		started <- run
		select {
		case <-release:
			run.State = "complete"
			run.Version = "fixture-generation"
		case <-ctx.Done():
			run.State = "cancelled"
		}
		if err := c.saveCloudRun(context.Background(), &run, nil); err != nil {
			t.Error(err)
		}
		if err := c.settleCloudSchedule(context.Background(), run, finishedAt); err != nil {
			t.Error(err)
		}
	})
	if err := server.cloudScheduleTick(ctx, at); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
		t.Fatal("backup ran before its scheduled date")
	default:
	}
	if err := server.cloudScheduleTick(ctx, finishedAt); err != nil {
		t.Fatal(err)
	}
	var run cloudRun
	select {
	case run = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("missed backup did not run")
	}
	if run.Trigger != "scheduled" || run.ScheduledFor == "" {
		t.Fatal("scheduled run lost its trigger or original due date")
	}
	// The running upload is listed, so the drive is not reported idle.
	if activities := server.libraryActivities(); !slices.ContainsFunc(activities, func(a libraryActivity) bool { return a.ID == "cloud:upload:"+run.ID && a.Writes }) {
		t.Fatalf("a scheduled upload is not a library activity: %+v", activities)
	}
	if err := server.cloudScheduleTick(ctx, finishedAt); err != nil {
		t.Fatal(err)
	}
	// A second process cannot claim the same destination while it is in flight.
	other := run
	other.ID = randomToken()
	if err := c.claimCloudRun(ctx, d, &other, finishedAt); !errors.Is(err, errCloudBusy) {
		t.Fatalf("duplicate durable claim: %v", err)
	}
	close(release)
	released = true
	server.life.workers.Wait()
	if err := server.cloudScheduleTick(ctx, finishedAt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
		t.Fatal("missed slots produced a backlog of full backups")
	default:
	}
	schedule := onlyCloudSchedule(t, c)
	next, err := time.Parse(time.RFC3339Nano, schedule.NextRunAt)
	if err != nil || !next.After(finishedAt) || schedule.InFlight != "" {
		t.Fatalf("schedule did not advance after success: %+v", schedule)
	}
	for i := 0; i < 2; i++ {
		if _, err := server.startCloudRun(ctx, d.ID, "full", "manual", finishedAt); err != nil {
			t.Fatal(err)
		}
		server.life.workers.Wait()
		select {
		case run := <-started:
			if run.Trigger != "manual" {
				t.Fatal("manual backup was scheduled")
			}
		default:
			t.Fatal("repeat manual backup did not run")
		}
	}
}

func TestCloudScheduleRetriesPauseDisconnectAndRecovery(t *testing.T) {
	c, server, d, at := cloudScheduleFixture(t)
	ctx := context.Background()
	spec := cloudDefaultSchedule(d.plan)
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err != nil {
		t.Fatal(err)
	}
	for attempt, delay := range []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 0} {
		s := onlyCloudSchedule(t, c)
		at, _ = time.Parse(time.RFC3339Nano, s.NextRunAt)
		run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: "full", Trigger: "scheduled", State: "running", StartedAt: at.Format(time.RFC3339Nano)}
		if err := c.claimCloudRun(ctx, d, &run, at); err != nil {
			t.Fatal(err)
		}
		run.State, run.Error = "failed", "temporary connection failure"
		if err := c.saveCloudRun(ctx, &run, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.settleCloudSchedule(ctx, run, at); err != nil {
			t.Fatal(err)
		}
		s = onlyCloudSchedule(t, c)
		next, _ := time.Parse(time.RFC3339Nano, s.NextRunAt)
		if delay > 0 && !next.Equal(at.Add(delay)) {
			t.Fatalf("retry %d: %+v", attempt, s)
		}
		if delay == 0 && (s.Failures != 0 || !next.After(at)) {
			t.Fatalf("retry cap did not return to regular schedule: %+v", s)
		}
	}
	// Restore a persisted, interrupted claim on service startup without a UI poll.
	s := onlyCloudSchedule(t, c)
	at, _ = time.Parse(time.RFC3339Nano, s.NextRunAt)
	run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: "full", Trigger: "scheduled", State: "running", OwnerPID: 999999999, OwnerHost: currentHost().ID, StartedAt: at.Format(time.RFC3339Nano)}
	if err := c.claimCloudRun(ctx, d, &run, at); err != nil {
		t.Fatal(err)
	}
	stage, err := cloudWorkDir(server.Config(), "cloud-upload-"+run.ID+"-")
	if err != nil {
		t.Fatal(err)
	}
	otherStage, err := cloudWorkDir(server.Config(), "cloud-upload-unrelated-run-")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.cloudScheduleTick(ctx, at); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("interrupted snapshot was left to accumulate on retry")
	}
	if _, err := os.Stat(otherStage); err != nil {
		t.Fatal("interrupted cleanup removed another run's files")
	}
	stored, err := c.cloudRun(ctx, run.ID)
	if err != nil || stored.State != "failed" {
		t.Fatalf("interrupted claim not reconciled: %+v %v", stored, err)
	}
	s = onlyCloudSchedule(t, c)
	if s.InFlight != "" || s.Failures != 1 {
		t.Fatalf("interrupted upload did not arrange bounded retry: %+v", s)
	}
	if err := c.setCloudSchedule(ctx, d.ID, false, cloudScheduleSpec{}, at); err != nil {
		t.Fatal(err)
	}
	override(t, &cloudPerformUpload, func(*Server, context.Context, cloudDestination, cloudRun) {
		t.Error("paused schedule performed an upload")
	})
	if err := server.cloudScheduleTick(ctx, at.AddDate(0, 0, 20)); err != nil {
		t.Fatal(err)
	}
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err != nil {
		t.Fatal(err)
	}
	if err := c.forgetCloudCredential(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if onlyCloudSchedule(t, c).Enabled {
		t.Fatal("disconnect left automatic uploads enabled")
	}
}

func TestCloudScheduleRequiresRestoreAndStaysOnItsMac(t *testing.T) {
	c, server, d, at := cloudScheduleFixture(t)
	ctx := context.Background()
	spec := cloudDefaultSchedule(d.plan)
	// Record the actual prerequisite without depending on the UI history limit.
	if _, err := c.DB.Exec("DELETE FROM cloud_backup_runs WHERE json_extract(status_json,'$.kind')='full'"); err != nil {
		t.Fatal(err)
	}
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err == nil {
		t.Fatal("unverified full restore enabled unattended uploads")
	}
	run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: "full", State: "complete", Verified: true, StartedAt: at.Format(time.RFC3339Nano)}
	if err := c.saveCloudRun(ctx, &run, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err != nil {
		t.Fatal(err)
	}
	override(t, &cloudPerformUpload, func(*Server, context.Context, cloudDestination, cloudRun) {
		t.Error("a different Mac ran this schedule")
	})
	t.Run("other Mac", func(t *testing.T) {
		useHost(t, "a-different-mac")
		if err := server.cloudScheduleTick(ctx, at.AddDate(0, 0, 2)); err != nil {
			t.Fatal(err)
		}
	})
	destinations, err := c.cloudDestinations(ctx)
	if err != nil || len(destinations) != 1 || !destinations[0].SampleVerified || !destinations[0].RestoreVerified {
		t.Fatalf("durable setup milestones unavailable: %+v %v", destinations, err)
	}
}

func TestCloudScheduleCancellationChangesAndCopiedLibrary(t *testing.T) {
	c, server, d, at := cloudScheduleFixture(t)
	ctx := context.Background()
	spec := cloudDefaultSchedule(d.plan)
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err != nil {
		t.Fatal(err)
	}
	schedule := onlyCloudSchedule(t, c)
	due, _ := time.Parse(time.RFC3339Nano, schedule.NextRunAt)
	run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: "full", Trigger: "scheduled", State: "running", StartedAt: due.Format(time.RFC3339Nano)}
	if err := c.claimCloudRun(ctx, d, &run, due); err != nil {
		t.Fatal(err)
	}
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err == nil {
		t.Fatal("schedule changed while an upload held its claim")
	}
	if err := c.setCloudSchedule(ctx, d.ID, false, cloudScheduleSpec{}, at); err != nil {
		t.Fatal(err)
	}
	if onlyCloudSchedule(t, c).InFlight != run.ID {
		t.Fatal("pausing lost the running backup's claim")
	}
	run.State, run.Error = "cancelled", "Cloud operation cancelled"
	if err := c.saveCloudRun(ctx, &run, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.settleCloudSchedule(ctx, run, due); err != nil {
		t.Fatal(err)
	}
	if s := onlyCloudSchedule(t, c); s.Enabled || s.InFlight != "" || s.Failures != 0 {
		t.Fatalf("cancelled backup retried despite pause: %+v", s)
	}
	spec.Cadence, spec.Time, spec.Timezone = 30, "14:45", "America/Denver"
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err != nil {
		t.Fatal(err)
	}
	schedule = onlyCloudSchedule(t, c)
	if schedule.Cadence != 30 || schedule.Time != "14:45" || schedule.Timezone != "America/Denver" || schedule.Anchor != "2026-10-23" {
		t.Fatalf("schedule edits did not persist: %+v", schedule)
	}
	// Simulate opening an intact restored catalog at a different path on the
	// same Mac. Merely opening the copy must not upload to the original bucket.
	schedule.CatalogPath = filepath.Join(t.TempDir(), "original-library.sqlite3")
	if err := c.writeTransaction(ctx, "test-original-location", func(tx *sql.Tx) error { return saveCloudScheduleTx(ctx, tx, schedule, at) }); err != nil {
		t.Fatal(err)
	}
	override(t, &cloudPerformUpload, func(*Server, context.Context, cloudDestination, cloudRun) {
		t.Error("a recovered library copy uploaded automatically")
	})
	if err := server.cloudScheduleTick(ctx, at.AddDate(0, 0, 90)); err != nil {
		t.Fatal(err)
	}
	if err := c.setCloudSchedule(ctx, d.ID, true, spec, at); err != nil {
		t.Fatal(err)
	}
	if onlyCloudSchedule(t, c).CatalogPath != canonical(c.Path) {
		t.Fatal("explicit activation did not bind to the new library location")
	}

	// A release (before an eject) cancels a running upload within the
	// release budget; the upload then records itself as cancelled.
	uploading, stopped := make(chan struct{}), make(chan error, 1)
	override(t, &cloudPerformUpload, func(_ *Server, ctx context.Context, _ cloudDestination, run cloudRun) {
		close(uploading)
		<-ctx.Done()
		stopped <- ctx.Err()
	})
	run, err := server.startCloudRun(ctx, d.ID, "sample", "manual", at)
	if err != nil {
		t.Fatal(err)
	}
	<-uploading
	if !slices.ContainsFunc(server.libraryActivities(), func(a libraryActivity) bool { return a.ID == "cloud:upload:"+run.ID }) {
		t.Fatal("a running upload is not a library activity")
	}
	if err := server.stop(stopTimeout); err != nil {
		t.Fatalf("a release waited for the upload: %v", err)
	}
	if err := <-stopped; !errors.Is(err, context.Canceled) {
		t.Fatalf("the upload's context after a release: %v", err)
	}
}

func TestCloudScheduleAdmissionFailureBacksOff(t *testing.T) {
	c, server, d, at := cloudScheduleFixture(t)
	ctx := context.Background()
	if err := c.setCloudSchedule(ctx, d.ID, true, cloudDefaultSchedule(d.plan), at); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE cloud_backup_runs SET status_json=json_set(status_json,'$.verified',json('false')) WHERE json_extract(status_json,'$.kind')='sample'"); err != nil {
		t.Fatal(err)
	}
	s := onlyCloudSchedule(t, c)
	at, _ = time.Parse(time.RFC3339Nano, s.NextRunAt)
	override(t, &cloudPerformUpload, func(*Server, context.Context, cloudDestination, cloudRun) {
		t.Error("missing prerequisite reached the uploader")
	})
	if err := server.cloudScheduleTick(ctx, at); err != nil {
		t.Fatal(err)
	}
	s = onlyCloudSchedule(t, c)
	next, _ := time.Parse(time.RFC3339Nano, s.NextRunAt)
	if s.LastError == "" || s.Failures != 1 || !next.Equal(at.Add(time.Hour)) {
		t.Fatalf("admission failure was not visible or backed off: %+v", s)
	}
}

func TestCloudScheduleAPI(t *testing.T) {
	c, server, d, _ := cloudScheduleFixture(t)
	token := server.Config().APIToken
	for _, enabled := range []bool{true, false, true} {
		response := apiCall(t, server, token, "POST", "/api/cloud-backup/schedule", map[string]any{"destination_id": d.ID, "enabled": enabled, "schedule": cloudDefaultSchedule(d.plan)})
		if response.Code != 200 {
			t.Fatalf("schedule save: %d %s", response.Code, response.Body.String())
		}
		if onlyCloudSchedule(t, c).Enabled != enabled {
			t.Fatal("API schedule state did not persist")
		}
	}
	response := apiCall(t, server, token, "GET", "/api/cloud-backup", nil)
	if response.Code != 200 {
		t.Fatalf("status: %d %s", response.Code, response.Body.String())
	}
	var status struct {
		Schedules    []cloudSchedule    `json:"schedules"`
		Destinations []cloudDestination `json:"destinations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Schedules) != 1 || !status.Schedules[0].Enabled || len(status.Destinations) != 1 || !status.Destinations[0].RestoreVerified {
		t.Fatal("API did not expose schedule and setup milestones")
	}
	response = apiCall(t, server, token, "POST", "/api/cloud-backup/schedule", map[string]any{"destination_id": d.ID, "enabled": "true"})
	if response.Code != 400 {
		t.Fatalf("malformed schedule request accepted: %d", response.Code)
	}
}
