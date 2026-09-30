package archive

import (
	"fmt"
	"sort"
	"strings"
)

// TL1's concerns as findings (product decision 20): error clusters, cost
// outliers, and rework loops, each scoped to a flavor, with TL1 runs as the
// unit. The TL1 tab keeps its analysis; its recommendations live here, with
// the cart, measurement, and wins.

func detectTL1(env *findingEnv) ([]*findingCandidate, error) {
	installations, err := queryMapsContext(env.ctx, env.db, `SELECT i.installation_id,i.project,i.repository,i.config_path,i.database_path,i.transcripts_dir,i.synced_at,i.host_id,
		h.label host_label,(SELECT MAX(t.created_at) FROM tl1_tasks t WHERE t.installation_id=i.installation_id) last_task_at
		FROM tl1_installations i LEFT JOIN hosts h ON h.id=i.host_id ORDER BY last_task_at DESC`)
	if err != nil || len(installations) == 0 {
		return nil, err
	}
	wanted := env.wants("tl1")
	candidates := []*findingCandidate{}
	for _, project := range tl1Projects(installations) {
		selected, _ := project["installations"].([]map[string]any)
		data, err := env.catalog.loadTL1(selected, tl1Window{Days: findingWindowDays, Scope: "all"})
		if err != nil {
			return nil, err
		}
		found := tl1FindingCandidates(env, data, wanted)
		candidates = append(candidates, found...)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Spec.id() < candidates[j].Spec.id() })
	return candidates, nil
}

// tl1Unit turns a task into an observation for its flavor.
func (env *findingEnv) tl1Unit(task *tl1Task) *findingObs {
	obs := &findingObs{Unit: task.ID, Conversation: task.ID, Workspace: defaultString(task.WorkspaceID, task.ID), Day: localDay(task.CreatedAt), Provider: "tl1", At: task.CreatedAt}
	for _, attempt := range task.Attempts {
		if attempt.HasCost {
			obs.Value += attempt.Cost
		}
	}
	obs.Total = obs.Value
	return obs
}

func tl1FindingCandidates(env *findingEnv, data *tl1Data, wanted map[string]findingSpec) []*findingCandidate {
	byFlavor := map[string][]*tl1Task{}
	for _, task := range data.Tasks {
		if task.ExecutionClass == "human" {
			continue
		}
		byFlavor[task.Flavor] = append(byFlavor[task.Flavor], task)
	}
	output := []*findingCandidate{}
	for flavor, tasks := range byFlavor {
		scope := "automation:tl1:" + flavor
		live := 0
		for _, task := range tasks {
			if env.inRecent(localDay(task.CreatedAt)) {
				live++
			}
		}
		definition := tl1FlavorDefinition(data, flavor, 1500)
		base := func(pattern string, metric findingMetric, params map[string]string) *findingCandidate {
			liveCount := live
			return &findingCandidate{Spec: findingSpec{Detector: "tl1", Scope: scope, Pattern: pattern, Params: params}, Obs: map[string]*findingObs{}, Live: &liveCount,
				Metric: metric, Target: scope, Facts: map[string]any{"project": data.Project, "flavor": flavor, "flavor_definition": definition},
				RemovableShare: findingTL1Removable[params["kind"]]}
		}
		evidence := func(candidate *findingCandidate, task *tl1Task, did, happened string) {
			candidate.addEvidence(findingHandle{At: task.CreatedAt, WorkspaceID: task.WorkspaceID, TaskID: task.ID, Where: "TL1 " + flavor, Did: did, Happened: happened})
		}
		// Error clusters, one per signature.
		signatures := map[string]*tl1Task{}
		for _, task := range tasks {
			if task.ErrorClass != "" && task.ErrorSignature != "" {
				signatures[task.ErrorSignature] = task
			}
		}
		for _, spec := range wanted {
			if signature := strings.TrimPrefix(spec.Pattern, "error:"); spec.Scope == scope && signature != spec.Pattern && signatures[signature] == nil {
				signatures[signature] = nil
			}
		}
		for signature, example := range signatures {
			pattern := "error:" + signature
			if _, ok := wanted[scope+"\x1f"+pattern]; !ok && !env.discover {
				continue
			}
			candidate := base(pattern, findingMetric{Kind: "rate", Unit: "runs", Failures: true}, map[string]string{"kind": "error"})
			attribution, class := "", ""
			for _, task := range tasks {
				obs := env.tl1Unit(task)
				if task.ErrorSignature == signature {
					obs.Hit, obs.Occurrences = true, 1
					obs.CostUSD = obs.Value
					for _, attempt := range task.Attempts {
						if attempt.HasDuration {
							obs.DurationMS += int64(attempt.Duration)
						}
						obs.Tokens += attempt.Tokens["total_tokens"]
					}
					attribution, class = task.ErrorAttribution, task.ErrorClass
					evidence(candidate, task, "ran "+clipText(defaultString(task.Title, "a task"), 60), "failed: "+clipText(signature, 90))
				}
				candidate.Obs[task.ID] = obs
			}
			candidate.Hidden = attribution == tl1AttributionProvider
			candidate.Facts["signature"], candidate.Facts["attribution"], candidate.Facts["error_class"] = signature, attribution, class
			if example != nil {
				candidate.Facts["example"] = tl1Clip(example.ErrorText, 1200)
			}
			describeTL1Error(candidate, flavor, signature, attribution)
			output = append(output, candidate)
		}
		// Rework: runs on a candidate this flavor already worked on.
		if (env.discover || wanted[scope+"\x1frework"].Detector != "") && data.Flavors[flavor] != nil && data.Flavors[flavor].ExecutionClass == "llm" {
			candidate := base("rework", findingMetric{Kind: "rate", Unit: "runs"}, map[string]string{"kind": "rework"})
			for _, task := range tasks {
				obs := env.tl1Unit(task)
				if task.Revisit {
					obs.Hit, obs.Occurrences, obs.CostUSD = true, 1, obs.Value
					evidence(candidate, task, "ran again on candidate "+short(task.CandidateID), clipText(defaultString(task.Title, ""), 80))
				}
				candidate.Obs[task.ID] = obs
			}
			steps := []findingStep{
				{Lever: "automation-prompt", Label: "A clearer handoff", Change: fmt.Sprintf("Find why %s runs again on the same candidate (an upstream step handing over incomplete work, unclear review criteria, or earlier failures), and change the prompts or transitions so one run is enough.", flavor)},
				{Lever: "automation-prompt", Label: "A transition change", Change: fmt.Sprintf("Change %s's outcome transitions so a repeat goes to a person or a different flavor instead of looping.", flavor)},
			}
			candidate.Lever = steps[0].Lever
			candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
				return findingCard{Title: fmt.Sprintf("%s keeps rerunning on the same candidates", flavor),
					Explanation: fmt.Sprintf("%s of %s's runs this month were on a candidate it had already worked on.", capitalize(fractionPhrase(stats.Rate, "")), flavor),
					ImpactNote:  countNoun(stats.Affected, "repeat run"), Steps: steps, ChartTitle: flavor + " runs on a candidate it already worked on", Rate: fractionPhrase(stats.Rate, "")}
			}
			output = append(output, candidate)
		}
		// Cost outliers: runs at least three times the flavor's median cost.
		costs := []float64{}
		for _, task := range tasks {
			if obs := env.tl1Unit(task); obs.Value > 0 {
				costs = append(costs, obs.Value)
			}
		}
		median, ok := tl1Median(costs)
		if ok && len(costs) >= 5 && (env.discover || wanted[scope+"\x1fcost"].Detector != "") {
			candidate := base("cost", findingMetric{Kind: "share", Unit: "runs", Value: "dollars", Threshold: 3 * median}, map[string]string{"kind": "cost"})
			for _, task := range tasks {
				obs := env.tl1Unit(task)
				if obs.Value >= 3*median && obs.Value-median >= 2 {
					obs.Hit, obs.Occurrences, obs.CostUSD = true, 1, obs.Value-median
					evidence(candidate, task, "ran "+clipText(defaultString(task.Title, "a task"), 60), fmt.Sprintf("cost %s, %.0f× the usual", dollars(obs.Value), obs.Value/median))
				}
				candidate.Obs[task.ID] = obs
			}
			steps := []findingStep{
				{Lever: "automation-prompt", Label: "A budget in the prompt", Change: fmt.Sprintf("Read the most expensive %s runs, find what made them long (a loop, re-reading large context, fighting the environment), and change the prompt or the flavor's budgets to stop it early.", flavor)},
				{Lever: "automation-prompt", Label: "A lower step budget", Change: fmt.Sprintf("Lower %s's budgets so a run that goes long stops and hands off.", flavor)},
			}
			candidate.Lever = steps[0].Lever
			candidate.Facts["median_cost_usd"] = median
			candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
				return findingCard{Title: fmt.Sprintf("A few %s runs cost several times the usual", flavor),
					Explanation: fmt.Sprintf("%s this month cost at least three times a typical %s run (%s). They took %s of the flavor's spend.",
						capitalize(countNoun(stats.Affected, "run")), flavor, dollars(median), fractionPhrase(stats.Rate, "")),
					ImpactNote: dollars(stats.CostUSD) + " above the usual", Steps: steps, ChartTitle: "Share of " + flavor + " spend from runs costing 3× the usual", Rate: fractionPhrase(stats.Rate, "")}
			}
			output = append(output, candidate)
		}
	}
	return output
}

func describeTL1Error(candidate *findingCandidate, flavor, signature, attribution string) {
	why := map[string]string{
		tl1AttributionConfiguration:  "The agent configuration failed before the work started.",
		tl1AttributionContract:       "The agent's handoff didn't match the flavor's output format, and TL1 couldn't repair it.",
		tl1AttributionScript:         "The flavor's script failed.",
		tl1AttributionInfrastructure: "The worker running it died or was restarted.",
	}[attribution]
	var steps []findingStep
	switch attribution {
	case tl1AttributionConfiguration:
		steps = []findingStep{{Lever: "repo-tooling", Label: "A configuration fix", Change: "Fix the agent configuration in tl1.json (executor, model, or environment) so it starts cleanly, or stop routing this flavor to it."}}
	case tl1AttributionContract:
		steps = []findingStep{
			{Lever: "automation-prompt", Label: "Clearer output instructions", Change: fmt.Sprintf("Tighten the output instructions in %s's prompt template so agents produce the handoff the schema expects.", flavor)},
			{Lever: "automation-prompt", Label: "A simpler schema", Change: fmt.Sprintf("Simplify %s's outputs schema where agents keep getting it wrong.", flavor)},
		}
	case tl1AttributionScript, tl1AttributionInfrastructure:
		steps = []findingStep{{Lever: "repo-tooling", Label: "A script fix", Change: "Fix the script or environment that fails, so the runs get past it."}}
	default:
		steps = []findingStep{
			{Lever: "automation-prompt", Label: "A line in the prompt", Change: fmt.Sprintf("Add to %s's prompt template what agents need to know to avoid this failure.", flavor)},
			{Lever: "repo-tooling", Label: "A tooling fix", Change: "Change the tool or environment behind the failure so it can't happen."},
		}
	}
	candidate.Lever = steps[0].Lever
	candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
		explanation := fmt.Sprintf("%s failed this way this month: %s of all %s runs.", capitalize(countNoun(stats.Affected, flavor+" run")), fractionPhrase(stats.Rate, ""), flavor)
		if why != "" {
			explanation += " " + why
		}
		return findingCard{Title: fmt.Sprintf("%s runs fail with “%s”", flavor, clipText(signature, 70)), Explanation: explanation,
			ImpactNote: countNoun(stats.Affected, "failed run"), Steps: steps, ChartTitle: flavor + " runs that failed this way", Rate: fractionPhrase(stats.Rate, "")}
	}
}
