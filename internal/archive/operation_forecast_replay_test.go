package archive

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The forecast replay runs the estimator over recorded operations the way it
// met them: each run is forecast from the runs that finished before it, at the
// elapsed times its own snapshots were taken, and then joins the history with
// the snapshots this estimator would have stored. Nothing in a run's forecast
// has seen that run's outcome.
//
// testdata/forecast-replay.json is such a record from one real library, reduced
// to durations and counts: step IDs, model version strings, states, seconds,
// conversation-group counts and checkpoint times. It holds no paths, names or
// text. The probe (operation_forecast_probe_test.go) writes it.

type replayRow struct{ point, low, high, actual float64 }

type replaySummary struct {
	Rows, Predicted         int
	MedianError, P90Error   float64 // |point − actual| / max(1 s, actual)
	Coverage                float64 // share of predicted rows with low ≤ actual ≤ high
	MedianWidth, MedianSpan float64 // (high − low) / max(1 s, actual); high / low
}

func summarizeReplay(rows []replayRow) replaySummary {
	result := replaySummary{Rows: len(rows)}
	errors, widths, spans := []float64{}, []float64{}, []float64{}
	covered := 0
	for _, row := range rows {
		if row.point <= 0 {
			continue
		}
		result.Predicted++
		errors = append(errors, math.Abs(row.point-row.actual)/max(1, row.actual))
		widths = append(widths, (row.high-row.low)/max(1, row.actual))
		spans = append(spans, row.high/max(row.low, .5))
		if row.low <= row.actual && row.actual <= row.high {
			covered++
		}
	}
	if result.Predicted == 0 {
		return result
	}
	slices.Sort(errors)
	slices.Sort(widths)
	slices.Sort(spans)
	result.MedianError, result.P90Error = quantile(errors, .5), quantile(errors, .9)
	result.Coverage = float64(covered) / float64(result.Predicted)
	result.MedianWidth, result.MedianSpan = quantile(widths, .5), quantile(spans, .5)
	return result
}

func (s replaySummary) String() string {
	return fmt.Sprintf("n=%3d/%3d error med=%.2f p90=%.2f cover=%3.0f%% width/actual=%7.2f high/low=%7.1f",
		s.Predicted, s.Rows, s.MedianError, s.P90Error, s.Coverage*100, s.MedianWidth, s.MedianSpan)
}

var replayCheckpoints = []string{"launch", "discovered", "25%", "50%", "75%"}

// replaySteps rebuilds a run's plan as it stood at an elapsed time. Steps ran
// in order, so each began when the ones before it had taken their recorded
// time. Before discovery nothing is known about the sources: every one is
// pending, without a count, including those discovery will skip.
func replaySteps(run forecastRun, elapsed float64, discovered bool, started time.Time) []forecastStep {
	steps := make([]forecastStep, 0, len(run.Steps))
	begins := 0.0
	for _, recorded := range run.Steps {
		step := recorded
		step.Seconds, step.State = 0, "pending"
		variable := strings.HasPrefix(step.ID, "source:") || step.ID == "index"
		switch {
		case !discovered:
			if variable {
				step.Features, step.Inventory = workFeatures{}, false
			}
			if step.Features.Dirty != nil && !*step.Features.Dirty {
				step.Features.Dirty = nil
			}
			step.Units = nil
		case variable && recorded.State == "skipped", begins+recorded.Seconds <= elapsed:
			step.State, step.Seconds = recorded.State, recorded.Seconds
		case begins <= elapsed:
			step.State, step.Started = "running", started.Add(time.Duration(begins*float64(time.Second)))
			step.Units = nil
		default:
			step.Units = nil
		}
		if !discovered && step.ID == "discovery" && begins <= elapsed {
			step.State, step.Started = "running", started
		}
		begins += recorded.Seconds
		steps = append(steps, step)
	}
	return steps
}

// replayForecasts returns this estimator's rows per "model checkpoint", and
// the rows of the estimates recorded with the runs. A model's first ten runs
// only train.
func replayForecasts(recorded map[string][]forecastRun) (replayed, stored map[string][]replayRow) {
	type entry struct {
		model string
		run   forecastRun
	}
	all := []entry{}
	for model, runs := range recorded {
		for _, run := range runs {
			all = append(all, entry{model, run})
		}
	}
	slices.SortStableFunc(all, func(a, b entry) int { return strings.Compare(a.run.FinishedAt, b.run.FinishedAt) })
	replayed, stored = map[string][]replayRow{}, map[string][]replayRow{}
	history := map[string][]forecastRun{}
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, item := range all {
		run, warm := item.run, len(history[item.model]) >= 10
		result := run
		result.Forecasts = nil
		if run.State == "complete" {
			op := &operationForecast{model: item.model, started: started, history: operationHistory(item.model, func(model string) []forecastRun { return history[model] })}
			discoveryEnds, discovered, quarter := 0.0, false, 1
			for _, step := range run.Steps {
				if step.ID == "discovery" {
					discoveryEnds = step.Seconds
				}
			}
			seen := map[string]bool{}
			for _, snapshot := range run.Forecasts {
				if snapshot.Actual == nil {
					continue
				}
				discovered = discovered || snapshot.Reason == "discovered" || snapshot.At > discoveryEnds
				at := started.Add(time.Duration(snapshot.At * float64(time.Second)))
				op.steps, op.snapshots = replaySteps(run, snapshot.At, discovered, started), result.Forecasts
				estimate := op.estimateLocked(at, snapshot.Reason)
				result.Forecasts = append(result.Forecasts, forecastSnapshot{At: snapshot.At, Reason: snapshot.Reason, Estimate: estimate, Actual: snapshot.Actual, Estimator: forecastEstimator})
				keys := []string{}
				if (snapshot.Reason == "launch" || snapshot.Reason == "discovered") && !seen[snapshot.Reason] {
					seen[snapshot.Reason] = true
					keys = append(keys, snapshot.Reason)
				}
				for ; quarter <= 3 && run.Seconds > 0 && snapshot.At >= float64(quarter)/4*run.Seconds; quarter++ {
					keys = append(keys, fmt.Sprintf("%d%%", quarter*25))
				}
				for _, key := range keys {
					scored := estimate
					if key != snapshot.Reason {
						// The same moment, read as work in progress.
						op.snapshots = result.Forecasts
						scored = op.estimateLocked(at, "stage")
					}
					if warm {
						name := item.model + " " + key
						replayed[name] = append(replayed[name], replayRow{scored.Point, scored.LowSeconds, scored.HighSeconds, *snapshot.Actual})
						stored[name] = append(stored[name], replayRow{snapshot.Estimate.Point, snapshot.Estimate.LowSeconds, snapshot.Estimate.HighSeconds, *snapshot.Actual})
					}
				}
			}
		}
		history[item.model] = append(history[item.model], result)
	}
	return replayed, stored
}

// forecastReplayFixture is the compact, anonymous form of recorded runs.
type forecastReplayFixture struct {
	Note   string                     `json:"note"`
	IDs    []string                   `json:"step_ids"`
	Models []string                   `json:"step_models"`
	Runs   []forecastReplayFixtureRun `json:"runs"`
}

type forecastReplayFixtureRun struct {
	Model   string  `json:"model"`
	State   string  `json:"state"`
	Seconds float64 `json:"seconds"`
	// Each step: ID index, model index, state (c, s, f, p), seconds, conversation
	// groups (−1 without a known inventory), reusable-analysis flag (−1 unset).
	Steps [][]any `json:"steps"`
	// Each checkpoint: elapsed seconds, reason (l, d, s, p), remaining seconds.
	Checkpoints [][]any `json:"checkpoints"`
}

var (
	replayStates  = map[string]string{"c": "complete", "s": "skipped", "f": "failed", "p": "pending"}
	replayReasons = map[string]string{"l": "launch", "d": "discovered", "s": "stage", "p": "progress"}
)

func (f forecastReplayFixture) runs() map[string][]forecastRun {
	recorded := map[string][]forecastRun{}
	for index, item := range f.Runs {
		run := forecastRun{Version: forecastVersion, Model: item.Model, State: item.State, Seconds: item.Seconds, FinishedAt: fmt.Sprintf("%06d", index)}
		for i, values := range item.Steps {
			step := forecastStep{ID: f.IDs[int(values[0].(float64))], Model: f.Models[int(values[1].(float64))], State: replayStates[values[2].(string)], Seconds: values[3].(float64)}
			if i > 0 {
				step.After = []string{run.Steps[i-1].ID}
			}
			if groups := values[4].(float64); groups >= 0 {
				step.Inventory, step.Features.Groups = true, groups
			}
			if dirty := values[5].(float64); dirty >= 0 {
				value := dirty > 0
				step.Features.Dirty = &value
			}
			run.Steps = append(run.Steps, step)
		}
		for _, values := range item.Checkpoints {
			actual := values[2].(float64)
			run.Forecasts = append(run.Forecasts, forecastSnapshot{At: values[0].(float64), Reason: replayReasons[values[1].(string)], Actual: &actual})
		}
		recorded[item.Model] = append(recorded[item.Model], run)
	}
	return recorded
}

func TestOperationForecastReplayKeepsCalibratedNarrowRanges(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "forecast-replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture forecastReplayFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	replayed, _ := replayForecasts(fixture.runs())
	for _, model := range []string{automaticRunKind, manualSyncRunKind} {
		for _, checkpoint := range replayCheckpoints {
			summary := summarizeReplay(replayed[model+" "+checkpoint])
			t.Logf("%-14s %-10s %s", model, checkpoint, summary)
			if summary.Rows < 10 {
				t.Fatalf("%s %s: the fixture scores only %d runs", model, checkpoint, summary.Rows)
			}
			// Every recorded run had comparable history, including across the
			// version changes inside the record.
			if summary.Predicted < summary.Rows*9/10 {
				t.Errorf("%s %s: forecast only %d of %d runs", model, checkpoint, summary.Predicted, summary.Rows)
			}
			if checkpoint == "launch" {
				// Before discovery a run is either nothing or real work; the
				// surfaces do not show this range (operationTime).
				continue
			}
			// The target is about 80% coverage from a range of about the
			// remaining time itself. These bounds are loose on purpose: they
			// separate that from a range hundreds of times too wide, or one too
			// narrow to mean anything, not one fit from another.
			minimum := .6
			if model == manualSyncRunKind {
				// Few runs, each across a version change.
				minimum = .4
			}
			if summary.Coverage < minimum || summary.MedianWidth > 4 || summary.MedianError > .6 {
				t.Errorf("%s %s: %s", model, checkpoint, summary)
			}
		}
	}
}
