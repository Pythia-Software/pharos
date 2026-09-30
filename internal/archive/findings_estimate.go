package archive

import (
	"math"
	"strings"
)

// Estimates. A finding's impact is what its pattern cost over the last 28
// days: everything the pattern touched. Ranked by that alone, the biggest
// tasks came first whatever the change could do about them. The expected
// saving tempers the impact with three shares, each from the library where
// it can be (docs/findings.md, "Ranking"):
//
//	expected = impact × removable × takes × persists
//
//   - removable: the part of the cost the change could remove if agents
//     followed it every time. Each detector works it out: the context above a
//     compaction threshold, the carry a sub-agent would spare, the lines a
//     narrow read would skip, the searches a found instruction file ends.
//   - takes: how often a change of that kind works. It starts from natural
//     experiments in one library's instruction history and moves toward this
//     library's own results as they come in.
//   - persists: how much of a pattern like it is still there four weeks
//     after it passed the gate with nobody fixing it, from this library's own
//     history, per detector.

// Removable shares for the detectors that set one share for every
// observation because nothing measured says more. The rest work it out per
// observation (see each detector).
const (
	// A local copy of a reference is still read; the fetching goes.
	findingDocsRemovable = 0.5
	// A map in the instructions spares reading hot files to get oriented,
	// but agents still read the files they change.
	findingOrientationRemovable = 0.3
)

// findingTL1Removable is by TL1 pattern: a failed run's work is often done
// again by the next run, while a repeat run, or the cost above the flavor's
// median, goes entirely.
var findingTL1Removable = map[string]float64{"error": 0.5, "rework": 1, "cost": 1}

// Persistence. The priors are what a backtest on one heavy library showed
// on 2026-09-30 (the mean after-to-before rate of 86 failure, 22 CLI, and
// 13 outlier patterns): half of the failures were gone a month later while
// a few grew, and habits stayed. Documentation hosts and TL1 flavors had too
// few cases and are assumptions.
var findingPersistPriors = map[string]float64{"failure": 0.45, "cli": 0.85, "outlier": 0.9, "docs": 0.7, "tl1": 0.7}

const (
	// findingPersistGate is the gate threshold backtest cases pass at.
	findingPersistGate = 5
	// findingPersistWeight is how many cases the prior counts as.
	findingPersistWeight = 5.0
	// A pattern that grows counts as at most twice its rate, and no detector
	// is assumed to leave less than a tenth or more than all of a pattern.
	findingPersistCap   = 2.0
	findingPersistFloor = 0.1
)

// findingPersist is one detector's persistence and the backtest cases
// behind it.
type findingPersist struct {
	Share float64 `json:"share"`
	Cases int     `json:"cases"`
}

func findingPersistPrior(detector string) float64 {
	if prior, ok := findingPersistPriors[detector]; ok {
		return prior
	}
	return 1
}

// asOf is the environment as it looked at the end of an earlier day, for
// the gate and stats of a backtest.
func (env *findingEnv) asOf(day string) *findingEnv {
	at := *env
	at.today = day
	at.gateFrom = dayTime(day).AddDate(0, 0, -(findingGateDays - 1)).Format("2006-01-02")
	at.recentFrom = dayTime(day).AddDate(0, 0, -(findingRecentDays - 1)).Format("2006-01-02")
	return &at
}

// persistence backtests every detector on the pass's own days. At weekly
// gate days from four weeks ago back as far as the window reaches, each
// pattern nobody measured that passed the gate is followed for the next 28
// days; its after rate over its before rate is one case, and a scope with no
// relevant work afterwards counts as none left, since a fix would save
// nothing there either. A detector's share is the mean case, pulled toward
// its prior when it has few.
func (env *findingEnv) persistence(candidates []*findingCandidate, treated map[string]bool) map[string]findingPersist {
	sums, cases := map[string]float64{}, map[string]int{}
	start := env.from.Format("2006-01-02")
	for gate := dayTime(env.today).AddDate(0, 0, -findingGateDays); ; gate = gate.AddDate(0, 0, -7) {
		at := env.asOf(gate.Format("2006-01-02"))
		if at.gateFrom < start {
			break
		}
		afterFrom := gate.AddDate(0, 0, 1).Format("2006-01-02")
		afterTo := gate.AddDate(0, 0, 1+findingGateDays).Format("2006-01-02")
		for _, candidate := range candidates {
			if candidate.Hidden || treated[candidate.Spec.id()] {
				continue
			}
			// Most candidates are too small to pass; count their hits
			// before working out the gate.
			hits := map[string]bool{}
			for _, obs := range candidate.Obs {
				if obs.Hit && obs.Day >= at.gateFrom && obs.Day <= at.today {
					hits[obs.Conversation] = true
				}
			}
			if len(hits) < findingPersistGate || !at.stats(candidate).passes(findingPersistGate) {
				continue
			}
			before := env.window(candidate.Metric, candidate.Obs, at.gateFrom, afterFrom)
			after := env.window(candidate.Metric, candidate.Obs, afterFrom, afterTo)
			if before.Rate <= 0 {
				continue
			}
			sums[candidate.Spec.Detector] += math.Min(after.Rate/before.Rate, findingPersistCap)
			cases[candidate.Spec.Detector]++
		}
	}
	output := map[string]findingPersist{}
	for _, detector := range findingDetectors {
		name := detector.name
		share := (sums[name] + findingPersistWeight*findingPersistPrior(name)) / (float64(cases[name]) + findingPersistWeight)
		output[name] = findingPersist{Share: math.Min(math.Max(share, findingPersistFloor), 1), Cases: cases[name]}
	}
	return output
}

// persistFor is a detector's persistence: this pass's backtest, or the last
// full pass's for a pass that only measures, or the prior.
func (env *findingEnv) persistFor(detector string) findingPersist {
	if persist, ok := env.persist[detector]; ok {
		return persist
	}
	for _, row := range env.stored {
		if share, ok := number(row.Impact["persists"]); ok && share > 0 && row.Detector == detector {
			return findingPersist{Share: share, Cases: int(integer(row.Impact["persist_cases"]))}
		}
	}
	return findingPersist{Share: findingPersistPrior(detector)}
}

// Change kinds, by how reliably a change of the kind works.
const (
	// changeEnforced is a harness setting or a hook: it applies whether or
	// not an agent reads anything.
	changeEnforced = "enforced"
	// changeInstruction is a concrete line: use this command, not that one.
	changeInstruction = "instruction"
	// changePrompt is a line in an automation's own prompt, read every run.
	changePrompt = "prompt"
	// changeTooling is a script, skill, or file in the repository.
	changeTooling = "tooling"
	// changeHabit is a line asking agents to work differently.
	changeHabit = "habit"
	// changeDelegation is a habit that also needs the harness to hand work
	// to a sub-agent.
	changeDelegation = "delegation"
)

// findingChangeKind classifies a detector's step by its lever: instruction
// lines are concrete for the detectors that name a command to use instead,
// and habits for those that ask agents to change how they work.
func findingChangeKind(detector, lever string) string {
	switch lever {
	case "harness-settings":
		return changeEnforced
	case "automation-prompt":
		return changePrompt
	case "repo-tooling":
		return changeTooling
	}
	switch detector {
	case "exploration":
		return changeDelegation
	case "outlier", "orientation":
		return changeHabit
	}
	return changeInstruction
}

// findingTakesPriors are how often each kind of change works before a
// library has results of its own. From natural experiments in one
// repository's instruction history (2026-09-30): concrete lines cut their
// pattern by half to nine tenths (a build path 33% → 4% of conversations,
// bare pkill 3.5% → 1.8%, gtimeout followed about nine times in ten), while
// habit lines were followed in 14% to 46% of conversations or not visibly
// at all. Enforced, prompt, and tooling changes are assumptions.
var findingTakesPriors = map[string]float64{
	changeEnforced: 0.9, changeInstruction: 0.7, changePrompt: 0.7, changeTooling: 0.6, changeHabit: 0.3, changeDelegation: 0.3,
}

// findingTakesWeight is how many results the prior counts as.
const findingTakesWeight = 10.0

// findingTakesPrior is a kind's prior for a finding's providers. Codex and
// Antigravity rarely hand work to sub-agents (Codex did in 196 of 3,408
// conversations in September 2026), so delegation takes less often there.
func findingTakesPrior(kind string, providers []map[string]any) float64 {
	if kind != changeDelegation {
		return findingTakesPriors[kind]
	}
	sum, total := 0.0, 0.0
	for _, item := range providers {
		count := floatOr(item["conversations"])
		prior := 0.1
		if firstString(item["provider"]) == "claude" {
			prior = findingTakesPriors[changeDelegation]
		}
		sum += count * prior
		total += count
	}
	if total == 0 {
		return findingTakesPriors[changeDelegation]
	}
	return sum / total
}

// findingTakesRecord counts one kind's decided results: improved ones, and
// every improved, unchanged, or worse one.
type findingTakesRecord struct {
	Improved, Decided int
}

// findingTakesRecords counts the library's results by change kind.
func findingTakesRecords(states map[string]*findingUserState, rows map[string]*findingRow) map[string]findingTakesRecord {
	records := map[string]findingTakesRecord{}
	for id, state := range states {
		detector, _, _ := strings.Cut(id, ":")
		if row := rows[id]; row != nil {
			detector = row.Detector
		}
		for _, item := range state.Interventions {
			if item.Status != interventionImproved && item.Status != interventionUnchanged && item.Status != interventionWorse {
				continue
			}
			kind := findingChangeKind(detector, item.Lever)
			record := records[kind]
			record.Decided++
			if item.Status == interventionImproved {
				record.Improved++
			}
			records[kind] = record
		}
	}
	return records
}

// findingEstimate is a finding's expected saving and the shares behind it.
type findingEstimate struct {
	Expected  map[string]float64 `json:"expected"`
	Removable float64            `json:"removable"`
	Takes     float64            `json:"takes"`
	Kind      string             `json:"kind"`
	// Results is how many of the library's results of this kind moved takes
	// from its prior.
	Results      int     `json:"results"`
	Persists     float64 `json:"persists"`
	PersistCases int     `json:"persist_cases"`
}

// estimateFinding works out a finding's expected saving for its next step.
// Rows from before the estimate have no shares yet; they count in full, at
// the fixed fade, until the next full pass.
func estimateFinding(row *findingRow, step findingStep, records map[string]findingTakesRecord) findingEstimate {
	removable, ok := number(row.Impact["removable"])
	if !ok {
		removable = 1
	}
	persists, ok := number(row.Impact["persists"])
	if !ok || persists <= 0 {
		persists = findingFadeFactor
	}
	kind := findingChangeKind(row.Detector, defaultString(step.Lever, row.Lever))
	record := records[kind]
	takes := (findingTakesPrior(kind, row.Providers)*findingTakesWeight + float64(record.Improved)) / (findingTakesWeight + float64(record.Decided))
	estimate := findingEstimate{Expected: map[string]float64{}, Removable: removable, Takes: takes, Kind: kind, Results: record.Decided,
		Persists: persists, PersistCases: int(integer(row.Impact["persist_cases"]))}
	for _, unit := range []string{"usd", "tokens", "minutes", "failures"} {
		estimate.Expected[unit] = floatOr(row.Impact[unit]) * removable * takes * persists
	}
	return estimate
}
