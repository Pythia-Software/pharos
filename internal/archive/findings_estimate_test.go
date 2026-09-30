package archive

import (
	"fmt"
	"math"
	"testing"
)

func TestHeavyRemovable(t *testing.T) {
	for _, test := range []struct {
		shape, command string
		want           float64
	}{
		{"long-range", "sed -n '1,900p' main.go", 0.8},
		{"long-range", "sed -n '100,$p' main.go", 0.5},
		{"full-diff", "git diff", 0.5},
		{"generated-file", "cat go.sum", 0.8},
	} {
		if got := heavyRemovable(test.shape, test.command); math.Abs(got-test.want) > 1e-9 {
			t.Errorf("heavyRemovable(%q, %q) = %v, want %v", test.shape, test.command, got, test.want)
		}
	}
}

func TestFindingChangeKind(t *testing.T) {
	for _, test := range []struct{ detector, lever, want string }{
		{"failure", "global-instructions", changeInstruction},
		{"heavy-output", "harness-settings", changeEnforced},
		{"exploration", "repo-instructions", changeDelegation},
		{"outlier", "repo-instructions", changeHabit},
		{"cli", "automation-prompt", changePrompt},
		{"drift", "repo-tooling", changeTooling},
	} {
		if got := findingChangeKind(test.detector, test.lever); got != test.want {
			t.Errorf("findingChangeKind(%q, %q) = %q, want %q", test.detector, test.lever, got, test.want)
		}
	}
}

func TestEstimateFinding(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	row := &findingRow{Detector: "failure", Lever: "repo-instructions",
		Impact: map[string]any{"usd": 100.0, "tokens": 1e6, "failures": 20.0, "removable": 0.5, "persists": 0.8, "persist_cases": 12.0}}
	estimate := estimateFinding(row, findingStep{Lever: "repo-instructions"}, nil)
	if estimate.Kind != changeInstruction || !near(estimate.Takes, 0.7) || !near(estimate.Expected["usd"], 28) || !near(estimate.Expected["failures"], 5.6) || estimate.PersistCases != 12 {
		t.Fatalf("estimate: %+v", estimate)
	}
	// Ten unchanged results of the same kind halve how often it takes.
	estimate = estimateFinding(row, findingStep{Lever: "repo-instructions"}, map[string]findingTakesRecord{changeInstruction: {Decided: 10}})
	if !near(estimate.Takes, 0.35) || estimate.Results != 10 {
		t.Fatalf("with results: %+v", estimate)
	}
	// Rows from before the estimate count in full at the fixed fade.
	old := &findingRow{Detector: "outlier", Lever: "repo-instructions", Impact: map[string]any{"usd": 100.0}}
	if estimate := estimateFinding(old, findingStep{Lever: "repo-instructions"}, nil); !near(estimate.Expected["usd"], 100*0.3*findingFadeFactor) {
		t.Fatalf("old row: %+v", estimate)
	}
	// Delegation takes less often where Codex does the work.
	providers := []map[string]any{{"provider": "codex", "conversations": 3.0}, {"provider": "claude", "conversations": 1.0}}
	if got := findingTakesPrior(changeDelegation, providers); !near(got, 0.15) {
		t.Fatalf("delegation prior %v", got)
	}
}

func TestFindingTakesRecords(t *testing.T) {
	states := map[string]*findingUserState{
		"failure:repo:r:1": {Interventions: []*findingIntervention{{Lever: "repo-instructions", Status: interventionUnchanged}, {Lever: "repo-tooling", Status: interventionImproved}}},
		"outlier:repo:r:2": {Interventions: []*findingIntervention{{Lever: "repo-instructions", Status: interventionWatching}, {Lever: "repo-instructions", Status: interventionInconclusive}}},
	}
	records := findingTakesRecords(states, nil)
	if records[changeInstruction] != (findingTakesRecord{Decided: 1}) || records[changeTooling] != (findingTakesRecord{Improved: 1, Decided: 1}) || records[changeHabit] != (findingTakesRecord{}) {
		t.Fatalf("records: %+v", records)
	}
}

// A pattern that stopped two months ago persists less than one that kept going,
// and a detector with no cases keeps its prior.
func TestFindingPersistence(t *testing.T) {
	today := "2026-09-28"
	env := &findingEnv{today: today, from: dayTime(today).AddDate(0, 0, -(findingWindowDays - 1)), convs: map[string]*findingConversation{}}
	env.gateFrom = env.asOf(today).gateFrom
	live := 10
	pattern := func(detector string, until int) *findingCandidate {
		candidate := &findingCandidate{Spec: findingSpec{Detector: detector, Scope: "repo:r", Pattern: detector}, Obs: map[string]*findingObs{},
			Metric: findingMetric{Kind: "rate", Unit: "conversations"}, Live: &live}
		for back := findingWindowDays - 1; back >= 0; back-- {
			day := dayTime(today).AddDate(0, 0, -back).Format("2006-01-02")
			for index, hit := range []bool{back >= until, false} {
				id := fmt.Sprintf("%s-%d-%d", detector, back, index)
				candidate.Obs[id] = &findingObs{Unit: id, Conversation: id, Workspace: fmt.Sprint("ws-", back%5), Day: day, Hit: hit}
			}
		}
		return candidate
	}
	persist := env.persistence([]*findingCandidate{pattern("failure", 60), pattern("heavy-output", 0)}, nil)
	if failure := persist["failure"]; failure.Cases == 0 || failure.Share >= findingPersistPriors["failure"] {
		t.Fatalf("faded pattern: %+v", failure)
	}
	if heavy := persist["heavy-output"]; heavy.Cases == 0 || math.Abs(heavy.Share-1) > 1e-9 {
		t.Fatalf("steady pattern: %+v", heavy)
	}
	if outlier := persist["outlier"]; outlier.Cases != 0 || outlier.Share != findingPersistPriors["outlier"] {
		t.Fatalf("no cases: %+v", outlier)
	}
}
