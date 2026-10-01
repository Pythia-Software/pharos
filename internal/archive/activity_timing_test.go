package archive

import (
	"testing"
	"time"
)

func TestActivityDurationHistory(t *testing.T) {
	useHost(t, "timing-host")
	catalog, _ := testCatalog(t)
	server := &Server{Catalog: catalog}
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if got := server.activityDuration("index"); got != 0 {
		t.Fatalf("no history: %s", got)
	}
	for _, seconds := range []int{60, 120, 180, 9000} {
		server.recordActivityDuration("index", formatTime(started), formatTime(started.Add(time.Duration(seconds)*time.Second)))
	}
	if got := server.activityDuration("index"); got != 150*time.Second {
		t.Fatalf("median: %s", got)
	}
	if got := server.activityDuration("capture"); got != 0 {
		t.Fatalf("different activity reused index history: %s", got)
	}
	server.recordActivityDuration("index", "invalid", now())
	server.recordActivityDuration("index", now(), "invalid")
	server.recordActivityDuration("index", formatTime(started), formatTime(started))
	server.recordActivityDuration("index", formatTime(started), formatTime(started.Add(-time.Minute)))
	if got := (&Server{Catalog: catalog}).activityDuration("index"); got != 150*time.Second {
		t.Fatalf("persisted history or invalid duration: %s", got)
	}
	for seconds := 1; seconds <= 10; seconds++ {
		server.recordActivityDuration("index", formatTime(started), formatTime(started.Add(time.Duration(seconds)*time.Second)))
	}
	if got := server.activityDuration("index"); got != 5500*time.Millisecond {
		t.Fatalf("last 10 durations: %s", got)
	}
	if err := setMeta(catalog.DB, activityTimingKey("backup"), "broken json"); err != nil {
		t.Fatal(err)
	}
	if got := server.activityDuration("backup"); got != 0 {
		t.Fatalf("invalid history: %s", got)
	}
	useHost(t, "other-host")
	if got := server.activityDuration("index"); got != 0 {
		t.Fatalf("another Mac reused history: %s", got)
	}
}

func TestLibraryActivityCompletionEstimate(t *testing.T) {
	useHost(t, "timing-host")
	catalog, config := testCatalog(t)
	server := NewServer(config, catalog)
	started := time.Now().Add(-time.Minute)
	run := server.startRunNamed(indexRunKind, []string{"claude"})
	server.updateRun(run.ID, func(run *SyncRun) {
		run.StartedAt = formatTime(started)
	})
	if got := server.libraryStatus()["activities"].([]libraryActivity)[0].EstimatedCompletionAt; got != "" {
		t.Fatalf("estimate without history: %s", got)
	}
	server.updateRun(run.ID, func(run *SyncRun) {
		run.State, run.CompletedAt = "complete", formatTime(started.Add(2*time.Minute))
	})
	server.updateRun(run.ID, func(run *SyncRun) {})
	if got := len(server.activityDurations("index")); got != 1 {
		t.Fatalf("recorded completion %d times", got)
	}
	for _, state := range []string{"failed", "interrupted", "stopped"} {
		failed := server.startRunNamed(indexRunKind, []string{"claude"})
		server.updateRun(failed.ID, func(run *SyncRun) {
			run.StartedAt, run.CompletedAt, run.State = formatTime(started), now(), state
		})
	}
	stopped := server.startRunNamed(indexRunKind, []string{"claude"})
	server.updateRun(stopped.ID, func(run *SyncRun) {
		run.StartedAt, run.CompletedAt, run.State = formatTime(started), now(), "complete"
		run.StopRequested = true
	})
	if got := len(server.activityDurations("index")); got != 1 {
		t.Fatalf("unsuccessful runs recorded: %d", got)
	}
	active := server.startRunNamed(indexRunKind, []string{"claude"})
	server.updateRun(active.ID, func(run *SyncRun) { run.StartedAt = formatTime(started) })
	activities := server.libraryStatus()["activities"].([]libraryActivity)
	if got, want := activities[0].EstimatedCompletionAt, formatTime(started.Add(2*time.Minute)); got != want {
		t.Fatalf("completion: %s, want %s", got, want)
	}
}
