//go:build probe

// Replays the operation forecast over a library's recorded runs and prints its
// error and range coverage beside those of the estimates stored with the runs.
// It reads the meta table of a copy, read-only:
//
//	PHAROS_FORECAST_REPLAY=/path/to/copy.sqlite3 \
//	  go test -tags probe -run '^TestOperationForecastReplayProbe$' -v ./internal/archive
//
// PHAROS_FORECAST_REPLAY_HOST picks a host ID prefix when the copy holds
// several; the default is the host with the most runs.
// PHAROS_FORECAST_REPLAY_WRITE=1 rewrites testdata/forecast-replay.json from
// the copy: durations and counts only, see operation_forecast_replay_test.go.

package archive

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestOperationForecastReplayProbe(t *testing.T) {
	path := os.Getenv("PHAROS_FORECAST_REPLAY")
	if path == "" {
		t.Skip("set PHAROS_FORECAST_REPLAY to a copy of a library catalog or of its meta table")
	}
	if strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe a copy, never the live catalog")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT key,value FROM meta WHERE key LIKE 'activity_durations:%:` + forecastVersion + `:%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hosts := map[string]map[string][]forecastRun{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(key, ":", 4)
		var runs []forecastRun
		if len(parts) != 4 || json.Unmarshal([]byte(value), &runs) != nil {
			continue
		}
		if hosts[parts[1]] == nil {
			hosts[parts[1]] = map[string][]forecastRun{}
		}
		hosts[parts[1]][parts[3]] = runs
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var recorded map[string][]forecastRun
	most := 0
	for host, models := range hosts {
		count := len(models[automaticRunKind]) + len(models[manualSyncRunKind])
		if prefix := os.Getenv("PHAROS_FORECAST_REPLAY_HOST"); prefix != "" && !strings.HasPrefix(host, prefix) {
			continue
		}
		if count > most {
			most, recorded = count, models
		}
	}
	if recorded == nil {
		t.Fatal("no recorded operations")
	}
	replayed, stored := replayForecasts(recorded)
	for _, model := range []string{automaticRunKind, manualSyncRunKind, indexRunKind, "library-update:update"} {
		for _, checkpoint := range replayCheckpoints {
			name := model + " " + checkpoint
			if len(replayed[name]) == 0 {
				continue
			}
			t.Logf("%-22s %-10s stored   %s", model, checkpoint, summarizeReplay(stored[name]))
			t.Logf("%-22s %-10s replayed %s", "", "", summarizeReplay(replayed[name]))
		}
	}
	if os.Getenv("PHAROS_FORECAST_REPLAY_WRITE") == "" {
		return
	}
	fixture := anonymousForecastReplay(recorded)
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	// One run a line keeps the file readable in a diff.
	text := strings.ReplaceAll(string(data), `{"model"`, "\n{\"model\"")
	if err := os.WriteFile(filepath.Join("..", "..", "testdata", "forecast-replay.json"), []byte(text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, _ := replayForecasts(fixture.runs())
	for _, model := range []string{automaticRunKind, manualSyncRunKind} {
		for _, checkpoint := range replayCheckpoints {
			t.Logf("fixture %-14s %-10s %s", model, checkpoint, summarizeReplay(again[model+" "+checkpoint]))
		}
	}
}

// anonymousForecastReplay keeps what the replay reads and nothing else. Run
// IDs, times, labels, byte and message counts and machine load are dropped,
// values are rounded, and sources that never had work are left out.
func anonymousForecastReplay(recorded map[string][]forecastRun) forecastReplayFixture {
	fixture := forecastReplayFixture{Note: "Recorded automatic and manual index runs of one library, as durations and counts only. Written by operation_forecast_probe_test.go; read by operation_forecast_replay_test.go."}
	round := func(value float64) float64 {
		rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'g', 3, 64), 64)
		return rounded
	}
	index := func(list *[]string, value string) int {
		for i, item := range *list {
			if item == value {
				return i
			}
		}
		*list = append(*list, value)
		return len(*list) - 1
	}
	type entry struct {
		model string
		run   forecastRun
	}
	all := []entry{}
	worked := map[string]bool{}
	for _, model := range []string{automaticRunKind, manualSyncRunKind} {
		for _, run := range recorded[model] {
			all = append(all, entry{model, run})
			for _, step := range run.Steps {
				if !strings.HasPrefix(step.ID, "source:") || step.Features.Groups > 0 || step.Seconds >= .5 {
					worked[step.ID] = true
				}
			}
		}
	}
	slices.SortStableFunc(all, func(a, b entry) int { return strings.Compare(a.run.FinishedAt, b.run.FinishedAt) })
	codes := func(names map[string]string, value string) string {
		for code, name := range names {
			if name == value {
				return code
			}
		}
		return "p"
	}
	for _, item := range all {
		run := forecastReplayFixtureRun{Model: item.model, State: item.run.State, Seconds: round(item.run.Seconds), Steps: [][]any{}, Checkpoints: [][]any{}}
		for _, step := range item.run.Steps {
			if !worked[step.ID] {
				continue
			}
			groups, dirty := -1.0, -1
			if step.Inventory {
				groups = step.Features.Groups
			}
			if step.Features.Dirty != nil {
				dirty = 0
				if *step.Features.Dirty {
					dirty = 1
				}
			}
			run.Steps = append(run.Steps, []any{index(&fixture.IDs, step.ID), index(&fixture.Models, step.Model), codes(replayStates, step.State), round(step.Seconds), groups, dirty})
		}
		seen, quarter := map[string]bool{}, 1
		for _, snapshot := range item.run.Forecasts {
			if snapshot.Actual == nil {
				continue
			}
			keep := (snapshot.Reason == "launch" || snapshot.Reason == "discovered") && !seen[snapshot.Reason]
			seen[snapshot.Reason] = true
			for ; quarter <= 3 && item.run.Seconds > 0 && snapshot.At >= float64(quarter)/4*item.run.Seconds; quarter++ {
				keep = true
			}
			if keep {
				run.Checkpoints = append(run.Checkpoints, []any{round(snapshot.At), codes(replayReasons, snapshot.Reason), round(*snapshot.Actual)})
			}
		}
		fixture.Runs = append(fixture.Runs, run)
	}
	return fixture
}
