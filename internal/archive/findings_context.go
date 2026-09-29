package archive

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Context-cost detectors: D3 orientation tax (what agents read before their
// first edit), D4 delegable exploration (pre-edit calls a sub-agent could
// have made), D5 context-heavy command shapes, and D6 cost outliers with a
// cause Pharos can name.

// preEdit is one top-level conversation's work before its first edit, in the
// root session only.
type preEdit struct {
	Calls, ResultTokens, Carried int64
	Output                       float64
	Model                        string
	Files                        map[string]*preEditFile
}

type preEditFile struct {
	Tokens, Carried, Reads int64
	CallID, MessageID, At  string
}

// preEdits reads every top-level conversation's pre-edit phase once per
// pass, for D3 and D4.
func (env *findingEnv) preEdits() (map[string]*preEdit, error) {
	if env.cachedPreEdits != nil {
		return env.cachedPreEdits, nil
	}
	firsts, err := queryMapsContext(env.ctx, env.db, `SELECT t.conversation_id,MIN(t.sequence) first_edit FROM tool_calls t
		LEFT JOIN agent_sessions a ON a.id=t.agent_session_id
		WHERE t.started_at>=? AND t.tool_category='edit' AND COALESCE(a.depth,0)=0 GROUP BY 1`, env.fromUTC)
	if err != nil {
		return nil, err
	}
	output := map[string]*preEdit{}
	ids := []any{}
	edits := map[string]int64{}
	for _, row := range firsts {
		id := firstString(row["conversation_id"])
		if env.convs[id] == nil {
			continue
		}
		edits[id] = integer(row["first_edit"])
		ids = append(ids, id)
		output[id] = &preEdit{Files: map[string]*preEditFile{}}
	}
	for start := 0; start < len(ids); start += 300 {
		end := min(start+300, len(ids))
		rows, err := queryMapsContext(env.ctx, env.db, `SELECT t.id,t.conversation_id,t.sequence,t.tool_category,t.result_tokens,t.carried_tokens,t.output_tokens,COALESCE(t.model,'') model,
			COALESCE(t.repo_path,'') repo_path,COALESCE(t.path_repository_id,'') path_repository,t.started_at,COALESCE(t.call_message_id,'') message_id
			FROM tool_calls t LEFT JOIN agent_sessions a ON a.id=t.agent_session_id
			WHERE t.conversation_id IN (`+placeholders(end-start)+`) AND t.tool_category IN ('read','search','command') AND COALESCE(a.depth,0)=0`, ids[start:end]...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			id := firstString(row["conversation_id"])
			if integer(row["sequence"]) >= edits[id] {
				continue
			}
			phase := output[id]
			phase.Calls++
			phase.ResultTokens += integer(row["result_tokens"])
			phase.Carried += integer(row["carried_tokens"])
			value, _ := number(row["output_tokens"])
			phase.Output += value
			phase.Model = defaultString(phase.Model, firstString(row["model"]))
			path := firstString(row["repo_path"])
			conv := env.convs[id]
			if firstString(row["tool_category"]) != "read" || path == "" || firstString(row["path_repository"]) != conv.RepositoryID {
				continue
			}
			file := phase.Files[path]
			if file == nil {
				file = &preEditFile{CallID: firstString(row["id"]), MessageID: firstString(row["message_id"]), At: firstString(row["started_at"])}
				phase.Files[path] = file
			}
			file.Reads++
			file.Tokens += integer(row["result_tokens"])
			file.Carried += integer(row["carried_tokens"])
		}
		if env.ctx.Err() != nil {
			return nil, env.ctx.Err()
		}
	}
	env.cachedPreEdits = output
	return output, nil
}

// expectedReading are files agents are meant to read first: instructions
// and documentation. Reading them is expected cost, not waste.
var expectedReading = regexp.MustCompile(`(?i)(?:^|/)(?:AGENTS|CLAUDE|GEMINI|README|SKILL|RULES|OVERVIEW|CONTRIBUTING)[^/]*\.md$|\.md$`)

func detectOrientation(env *findingEnv) ([]*findingCandidate, error) {
	phases, err := env.preEdits()
	if err != nil {
		return nil, err
	}
	wanted := env.wants("orientation")
	// Hot files: read before the first edit in the most conversations.
	files := map[string]map[string]map[string]bool{}
	for id, phase := range phases {
		conv := env.convs[id]
		if conv.RepositoryID == "" || !env.inGate(conv.Day) {
			continue
		}
		for path := range phase.Files {
			if expectedReading.MatchString(path) {
				continue
			}
			if files[conv.RepositoryID] == nil {
				files[conv.RepositoryID] = map[string]map[string]bool{}
			}
			if files[conv.RepositoryID][path] == nil {
				files[conv.RepositoryID][path] = map[string]bool{}
			}
			files[conv.RepositoryID][path][id] = true
		}
	}
	output := []*findingCandidate{}
	scopes := map[string]bool{}
	if env.discover {
		for repository := range files {
			scopes["repo:"+repository] = true
		}
	}
	for _, spec := range wanted {
		scopes[spec.Scope] = true
	}
	for scope := range scopes {
		repository := strings.TrimPrefix(scope, "repo:")
		hot := []string{}
		if spec, ok := wanted[scope+"\x1forientation"]; ok && spec.Params["files"] != "" {
			hot = strings.Split(spec.Params["files"], "\n")
		} else {
			paths := sortedFindingKeys(files[repository])
			sort.SliceStable(paths, func(i, j int) bool { return len(files[repository][paths[i]]) > len(files[repository][paths[j]]) })
			for _, path := range paths {
				if len(files[repository][path]) >= findingCheckpoints[0] && len(hot) < 5 {
					hot = append(hot, path)
				}
			}
		}
		if len(hot) == 0 {
			continue
		}
		candidate := &findingCandidate{Spec: findingSpec{Detector: "orientation", Scope: scope, Pattern: "orientation", Params: map[string]string{"files": strings.Join(hot, "\n")}},
			RepositoryID: repository, Obs: map[string]*findingObs{}, Facts: map[string]any{},
			Metric: findingMetric{Kind: "mean", Unit: "conversations", Value: "tokens"}}
		readers := map[string]int{}
		tokens := map[string]int64{}
		for id, phase := range phases {
			conv := env.convs[id]
			if !env.inScope(scope, conv) {
				continue
			}
			obs := &findingObs{Unit: id, Conversation: id, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider, Value: float64(phase.ResultTokens)}
			for _, path := range hot {
				file := phase.Files[path]
				if file == nil {
					continue
				}
				obs.Hit = true
				obs.Occurrences++
				obs.Tokens += file.Tokens + file.Carried
				obs.CostUSD += env.toolCallCost(conv.Provider, phase.Model, conv.Day, file.Tokens, file.Carried, 0)
				if file.At > obs.At {
					obs.At = file.At
				}
				if env.inGate(conv.Day) {
					readers[path]++
					tokens[path] += file.Tokens
				}
				candidate.addEvidence(findingHandle{At: file.At, ConversationID: id, WorkspaceID: conv.WorkspaceID, ToolCallID: file.CallID, MessageID: file.MessageID,
					Where: env.evidenceWhere(conv), Did: "read " + path + " before the first edit", Happened: compactNumber(float64(file.Tokens)) + " tokens"})
			}
			candidate.Obs[id] = obs
		}
		candidate.Facts["hot_files"] = func() []map[string]any {
			rows := []map[string]any{}
			for _, path := range hot {
				rows = append(rows, map[string]any{"path": path, "conversations": readers[path], "tokens": tokens[path]})
			}
			return rows
		}()
		steps := []findingStep{
			{Lever: "repo-instructions", Label: "A map in the instructions", Change: fmt.Sprintf("Add a short map of where things live to the agent instructions, with a few lines on what %s provides, so agents don't read it in full before starting.", joinWords(backticked(hot[:min(2, len(hot))])))},
			{Lever: "repo-tooling", Label: "Smaller files", Change: fmt.Sprintf("Split %s so the part agents need is small, or add a generated summary of its API next to it.", "`"+hot[0]+"`")},
		}
		candidate.Lever = steps[0].Lever
		candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
			name := env.repositories[repository]
			top := hot[0]
			return findingCard{Title: fmt.Sprintf("Agents in %s read `%s` before starting work", name, baseName(top)),
				Explanation: fmt.Sprintf("Before their first edit, agents read %s in %s this month. On average they read %s before starting.",
					joinWords(backticked(hot[:min(2, len(hot))])), countNoun(stats.Affected, "conversation"), tokensPhrase(stats.Rate)),
				ImpactNote: countNoun(readers[top], "read") + " of " + baseName(top), Steps: steps, ChartTitle: "Average tokens read before the first edit",
				Rate: tokensPhrase(stats.Rate)}
		}
		output = append(output, candidate)
	}
	return output, nil
}

func backticked(values []string) []string {
	output := make([]string, len(values))
	for index, value := range values {
		output[index] = "`" + value + "`"
	}
	return output
}

func baseName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

// delegableCalls is how many pre-edit calls make exploration worth handing
// to a sub-agent.
const delegableCalls = 20

func detectExploration(env *findingEnv) ([]*findingCandidate, error) {
	phases, err := env.preEdits()
	if err != nil {
		return nil, err
	}
	wanted := env.wants("exploration")
	candidates := map[string]*findingCandidate{}
	for id, phase := range phases {
		conv := env.convs[id]
		scope := env.scopeOf(conv)
		if !strings.HasPrefix(scope, "repo:") {
			continue
		}
		if _, ok := wanted[scope+"\x1fexploration"]; !ok && !env.discover {
			continue
		}
		candidate := candidates[scope]
		if candidate == nil {
			candidate = &findingCandidate{Spec: findingSpec{Detector: "exploration", Scope: scope, Pattern: "exploration"}, RepositoryID: conv.RepositoryID,
				Obs: map[string]*findingObs{}, Facts: map[string]any{}, Metric: findingMetric{Kind: "mean", Unit: "conversations", Value: "tokens"}}
			candidates[scope] = candidate
		}
		obs := &findingObs{Unit: id, Conversation: id, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider, Value: float64(phase.Carried)}
		if phase.Calls >= delegableCalls {
			obs.Hit = true
			obs.Occurrences = int(phase.Calls)
			// An upper bound: a sub-agent's summary of about 3k tokens would
			// be carried instead, and some results are used after the edit.
			obs.Tokens = max(phase.Carried-3000*max(conv.Requests-phase.Calls, 0), 0)
			obs.CostUSD = env.toolCallCost(conv.Provider, phase.Model, conv.Day, 0, obs.Tokens, 0)
			obs.At = conv.Started.UTC().Format(timeLayout)
			candidate.addEvidence(findingHandle{At: obs.At, ConversationID: id, WorkspaceID: conv.WorkspaceID, Where: env.evidenceWhere(conv) + " · " + providerLabel(conv.Provider),
				Did: countNoun(int(phase.Calls), "read, search, or command") + " before the first edit", Happened: compactNumber(float64(phase.Carried)) + " tokens carried afterwards"})
		}
		candidate.Obs[id] = obs
	}
	output := []*findingCandidate{}
	for scope, candidate := range candidates {
		repository := strings.TrimPrefix(scope, "repo:")
		providers := map[string]int{}
		for _, obs := range candidate.Obs {
			if obs.Hit {
				providers[obs.Provider]++
			}
		}
		main := topKey(providers)
		how := "Claude's Explore agent, Codex's spawned agents"
		if main == "codex" {
			how = "Codex's spawned agents, Claude's Explore agent"
		}
		steps := []findingStep{
			{Lever: "repo-instructions", Label: "A line in the instructions", Change: fmt.Sprintf("Ask agents to hand broad exploration to a sub-agent that returns a short summary (%s), and to start editing from that summary. Phrase it for the harnesses that do most of the work here, and check each version supports it.", how)},
			{Lever: "repo-tooling", Label: "An exploration skill", Change: "Add a skill or sub-agent definition that explores this repository and returns the files and functions a task touches."},
		}
		candidate.Lever = steps[0].Lever
		candidate.Facts["main_provider"] = main
		candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
			return findingCard{Title: fmt.Sprintf("Agents in %s explore at length before changing anything", env.repositories[repository]),
				Explanation: fmt.Sprintf("In %s this month, agents made %d or more reads, searches, and commands before their first edit, and every later request re-read those results. A sub-agent could explore and hand back a summary.",
					countNoun(stats.Affected, "conversation"), delegableCalls),
				ImpactNote: "up to " + tokensPhrase(float64(stats.Tokens)) + " carried", Steps: steps, ChartTitle: "Average tokens carried from exploration before the first edit",
				Rate: tokensPhrase(stats.Rate)}
		}
		output = append(output, candidate)
	}
	return output, nil
}

// D5: command shapes whose results are large and common. Carried context
// comes mostly from mid-sized results, so this targets shapes with a high
// average result and high volume, scoped by provider since each harness
// reads files its own way.
var (
	sedRange      = regexp.MustCompile(`sed\s+-n\s+['"]?(\d+),(\d+|\$)p`)
	generatedFile = regexp.MustCompile(`(?i)(?:package-lock\.json|yarn\.lock|pnpm-lock\.yaml|go\.sum|Cargo\.lock|poetry\.lock|\.min\.(?:js|css)|(?:^|/)(?:dist|build|vendor|node_modules)/|\.pb\.go|_gen\.|generated|\.snap\b)`)
)

// heavyShape names a call's shape, or "" when it is bounded.
func heavyShape(program, subcommand, command string) string {
	parsed := parseShellCommand(command)
	words := shellWords(parsed.primary().Text)
	limited := len(parsed.Segments) > 1 && parsed.HasPipe && (strings.Contains(command, "| head") || strings.Contains(command, "|head") || strings.Contains(command, "| tail") || strings.Contains(command, "| wc"))
	if limited {
		return ""
	}
	flags := map[string]bool{}
	positional := 0
	for _, word := range words[min(1, len(words)):] {
		if strings.HasPrefix(word, "-") {
			flag, _, _ := strings.Cut(word, "=")
			flags[flag] = true
		} else {
			positional++
		}
	}
	switch program {
	case "rg", "grep":
		for _, flag := range []string{"-l", "--files-with-matches", "-c", "--count", "-m", "--max-count", "-q", "--quiet", "--files", "-L"} {
			if flags[flag] {
				return ""
			}
		}
		if program == "rg" && positional <= 1 {
			return "search-everything"
		}
		if program == "grep" && (flags["-r"] || flags["-R"] || flags["-rn"] || flags["-Rn"]) && positional <= 1 {
			return "search-everything"
		}
	case "git":
		if subcommand != "diff" && subcommand != "show" && subcommand != "log" {
			return ""
		}
		for _, flag := range []string{"--stat", "--name-only", "--shortstat", "--name-status", "--numstat", "--oneline", "-n", "--max-count", "-1", "--summary", "--quiet", "--exit-code"} {
			if flags[flag] {
				return ""
			}
		}
		if strings.Contains(command, " -- ") {
			return ""
		}
		if subcommand == "log" {
			if flags["-p"] || flags["--patch"] {
				return "full-log"
			}
			return ""
		}
		return "full-diff"
	case "sed":
		if match := sedRange.FindStringSubmatch(command); match != nil {
			from, _ := strconv.Atoi(match[1])
			to, err := strconv.Atoi(match[2])
			if match[2] == "$" || err == nil && to-from > 300 {
				return "long-range"
			}
		}
	case "cat":
		if generatedFile.MatchString(command) {
			return "generated-file"
		}
	}
	return ""
}

var heavyShapeWords = map[string]struct{ title, did, change, command string }{
	"search-everything": {"searches the whole repository and reads every match", "searched the whole repository with `rg` (no `-l` or path)", "search with `rg -l` or a path limit first, then read the matches that matter", "rg"},
	"full-diff":         {"reads whole diffs", "read whole diffs with `git diff` (no `--stat` or path)", "start with `git diff --stat` and then read the diffs of the files that matter", "git diff"},
	"full-log":          {"reads whole patch logs", "read patch logs with `git log -p`", "use `git log --oneline` and show only the commit that matters", "git log -p"},
	"long-range":        {"reads files hundreds of lines at a time", "read more than 300 lines at a time with `sed -n`", "find the symbol with `rg -n` first, then read ranges of 120 lines or fewer", "sed -n"},
	"generated-file":    {"reads generated files in full", "read lock files or generated code in full with `cat`", "search lock files and generated code for the one entry needed instead of reading them in full", "cat"},
}

// heavyResultTokens is the result size that makes a call count as heavy.
const heavyResultTokens = 2000

func detectHeavyOutput(env *findingEnv) ([]*findingCandidate, error) {
	rows, err := queryMapsContext(env.ctx, env.db, `SELECT t.id,t.conversation_id,t.provider,COALESCE(t.program,'') program,COALESCE(t.subcommand,'') subcommand,t.command,
		t.result_tokens,t.carried_tokens,t.output_tokens,COALESCE(t.model,'') model,t.started_at,COALESCE(t.call_message_id,'') message_id
		FROM tool_calls t WHERE t.started_at>=? AND t.tool_category='command' AND t.program IN ('rg','grep','git','sed','cat') AND t.result_tokens>0`, env.fromUTC)
	if err != nil {
		return nil, err
	}
	type call struct {
		conv  *findingConversation
		row   map[string]any
		shape string
	}
	byShape := map[string][]call{}
	repositories := map[string]map[string]bool{}
	for _, row := range rows {
		root := env.roots[firstString(row["conversation_id"])]
		if root == "" {
			continue
		}
		shape := heavyShape(firstString(row["program"]), firstString(row["subcommand"]), firstString(row["command"]))
		if shape == "" {
			continue
		}
		conv := env.convs[root]
		key := shape + ":" + conv.Provider
		byShape[key] = append(byShape[key], call{conv, row, shape})
		if repositories[key] == nil {
			repositories[key] = map[string]bool{}
		}
		if conv.RepositoryID != "" && integer(row["result_tokens"]) >= heavyResultTokens && env.inGate(conv.Day) {
			repositories[key][conv.RepositoryID] = true
		}
	}
	wanted := env.wants("heavy-output")
	candidates := map[string]*findingCandidate{}
	add := func(scope, shape, provider string, item call) {
		pattern := "shape:" + shape + ":provider:" + provider
		id := scope + "\x1f" + pattern
		candidate := candidates[id]
		if candidate == nil {
			candidate = &findingCandidate{Spec: findingSpec{Detector: "heavy-output", Scope: scope, Pattern: pattern, Params: map[string]string{"shape": shape, "provider": provider}},
				Obs: map[string]*findingObs{}, Facts: map[string]any{"shape": shape, "provider": provider}, Metric: findingMetric{Kind: "mean", Unit: "calls", Value: "tokens"}}
			if strings.HasPrefix(scope, "repo:") {
				candidate.RepositoryID = strings.TrimPrefix(scope, "repo:")
			}
			candidates[id] = candidate
		}
		result, carried := integer(item.row["result_tokens"]), integer(item.row["carried_tokens"])
		output, _ := number(item.row["output_tokens"])
		obs := &findingObs{Unit: firstString(item.row["id"]), Conversation: item.conv.ID, Workspace: item.conv.WorkspaceID, Day: item.conv.Day, Provider: provider,
			Value: float64(result), Hit: result >= heavyResultTokens, At: firstString(item.row["started_at"])}
		if obs.Hit {
			obs.Occurrences = 1
			obs.Tokens = result + carried
			obs.CostUSD = env.toolCallCost(provider, firstString(item.row["model"]), item.conv.Day, result, carried, output)
			candidate.addEvidence(findingHandle{At: obs.At, ConversationID: item.conv.ID, WorkspaceID: item.conv.WorkspaceID, ToolCallID: obs.Unit,
				MessageID: firstString(item.row["message_id"]), Where: env.evidenceWhere(item.conv) + " · " + providerLabel(provider),
				Did: "ran `" + clipText(firstLine(firstString(item.row["command"])), 80) + "`", Happened: compactNumber(float64(result)) + " tokens back, re-read " + compactNumber(float64(carried)) + " more"})
		}
		candidate.Obs[obs.Unit] = obs
	}
	for key, calls := range byShape {
		shape, provider, _ := strings.Cut(key, ":")
		pattern := "shape:" + shape + ":provider:" + provider
		// A shape in three or more repositories is a global habit of that
		// provider on that Mac.
		global := len(repositories[key]) >= 3
		for _, item := range calls {
			if env.discover {
				scope := env.scopeOf(item.conv)
				if global {
					scope = globalScope(item.conv)
				}
				if scope != "" {
					add(scope, shape, provider, item)
				}
			}
			// Measured findings keep their scope, whatever this pass decided.
			for _, spec := range wanted {
				if spec.Pattern == pattern && env.inScope(spec.Scope, item.conv) && (env.discover == false || candidates[spec.Scope+"\x1f"+pattern] == nil || candidates[spec.Scope+"\x1f"+pattern].Obs[firstString(item.row["id"])] == nil) {
					add(spec.Scope, shape, provider, item)
				}
			}
		}
	}
	for id, spec := range wanted {
		if candidates[id] == nil {
			candidates[id] = &findingCandidate{Spec: spec, Obs: map[string]*findingObs{}, Facts: map[string]any{"shape": spec.Params["shape"], "provider": spec.Params["provider"]},
				Metric: findingMetric{Kind: "mean", Unit: "calls", Value: "tokens"}, RepositoryID: strings.TrimPrefix(spec.Scope, "repo:")}
			if !strings.HasPrefix(spec.Scope, "repo:") {
				candidates[id].RepositoryID = ""
			}
		}
	}
	output := []*findingCandidate{}
	for _, candidate := range candidates {
		shape, provider := candidate.Spec.Params["shape"], candidate.Spec.Params["provider"]
		words := heavyShapeWords[shape]
		global := strings.HasPrefix(candidate.Spec.Scope, "global:")
		file := "the repository's agent instructions"
		lever := "repo-instructions"
		if global {
			file, lever = globalInstructionFile(provider), "global-instructions"
		}
		steps := []findingStep{
			{Lever: lever, Label: "A line in " + shortInstructionFile(file), Change: fmt.Sprintf("Tell %s to %s, in %s.", providerLabel(provider), words.change, file)},
			{Lever: "harness-settings", Label: "A hook", Change: fmt.Sprintf("Add a hook that suggests the narrower form when %s runs `%s` without a limit.", providerLabel(provider), words.command)},
		}
		candidate.Lever = steps[0].Lever
		if global {
			for _, obs := range candidate.Obs {
				if conv := env.convs[obs.Conversation]; conv != nil && conv.RepositoryID != "" {
					candidate.Aliases = append(candidate.Aliases, findingSpec{Detector: "heavy-output", Scope: "repo:" + conv.RepositoryID, Pattern: candidate.Spec.Pattern}.id())
				}
			}
			candidate.Aliases = uniqueStrings(candidate.Aliases)
		}
		candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
			where := env.scopeName(candidate.Spec.Scope)
			return findingCard{Title: fmt.Sprintf("%s %s", providerLabel(provider), words.title) + map[bool]string{true: env.hostSuffix(candidate.Spec.Scope), false: " in " + where}[global],
				Explanation: fmt.Sprintf("%s %s %s this month. Each result averaged %s, which every later request re-read.",
					providerLabel(provider), words.did, countNoun(stats.Occurrences, "time"), tokensPhrase(stats.Rate)),
				ImpactNote: tokensPhrase(float64(stats.Tokens)), Steps: steps, ChartTitle: "Average tokens returned per `" + words.command + "` call", Rate: tokensPhrase(stats.Rate)}
		}
		output = append(output, candidate)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Spec.id() < output[j].Spec.id() })
	return output, nil
}

// D6: cost outliers with a nameable cause. Runaway conversations (more than
// 1,000 requests or 10 compactions) and oversized context (a request over
// 500k input tokens). The metric is the share of the scope's tokens from
// conversations with the cause; the thresholds are fixed, so the bar does
// not move as outliers shrink.
const (
	runawayRequests    = 1000
	runawayCompactions = 10
	oversizedInput     = 500_000
)

func detectOutliers(env *findingEnv) ([]*findingCandidate, error) {
	wanted := env.wants("outlier")
	candidates := map[string]*findingCandidate{}
	for _, conv := range env.convs {
		scope := env.scopeOf(conv)
		if !strings.HasPrefix(scope, "repo:") {
			continue
		}
		for _, cause := range []string{"runaway", "oversized"} {
			key := scope + "\x1fcause:" + cause
			if _, ok := wanted[key]; !ok && !env.discover {
				continue
			}
			candidate := candidates[key]
			if candidate == nil {
				candidate = &findingCandidate{Spec: findingSpec{Detector: "outlier", Scope: scope, Pattern: "cause:" + cause, Params: map[string]string{"cause": cause}},
					RepositoryID: conv.RepositoryID, Obs: map[string]*findingObs{}, Facts: map[string]any{"cause": cause},
					Metric: findingMetric{Kind: "share", Unit: "conversations", Value: "tokens"}}
				candidates[key] = candidate
			}
			hit := cause == "runaway" && (conv.Requests > runawayRequests || conv.Compactions > runawayCompactions) || cause == "oversized" && conv.PeakInput > oversizedInput
			obs := &findingObs{Unit: conv.ID, Conversation: conv.ID, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider,
				Value: float64(conv.Tokens), Total: float64(conv.Tokens), Hit: hit}
			if hit {
				obs.Occurrences, obs.Tokens, obs.CostUSD, obs.At = 1, conv.Tokens, conv.CostUSD, conv.Started.UTC().Format(timeLayout)
				detail := fmt.Sprintf("%s requests, %s compactions", thousands(int(conv.Requests)), thousands(int(conv.Compactions)))
				if cause == "oversized" {
					detail = "a request with " + compactNumber(float64(conv.PeakInput)) + " tokens of context"
				}
				candidate.addEvidence(findingHandle{At: obs.At, ConversationID: conv.ID, WorkspaceID: conv.WorkspaceID, Where: env.evidenceWhere(conv) + " · " + providerLabel(conv.Provider),
					Did: "ran one conversation to " + tokensPhrase(float64(conv.Tokens)), Happened: detail})
			}
			candidate.Obs[conv.ID] = obs
		}
	}
	output := []*findingCandidate{}
	for _, candidate := range candidates {
		cause := candidate.Spec.Params["cause"]
		repository := env.repositories[candidate.RepositoryID]
		var steps []findingStep
		if cause == "runaway" {
			candidate.Metric.Threshold = runawayRequests
			steps = []findingStep{
				{Lever: "repo-instructions", Label: "A line in the instructions", Change: "Tell agents to split long tasks, and to write a short handoff (what's done, what's next, where things are) and start a fresh session when a task passes a few hundred requests."},
				{Lever: "repo-tooling", Label: "A handoff skill", Change: "Add a skill that writes a checkpoint of the task's state, so a fresh session can pick it up."},
			}
		} else {
			candidate.Metric.Threshold = oversizedInput
			steps = []findingStep{
				{Lever: "harness-settings", Label: "An earlier compaction", Change: "Lower the auto-compaction threshold for this repository's sessions (for Claude Code in `.claude/settings.json`, for Codex `model_auto_compact_token_limit` in `.codex/config.toml`), after checking the setting names against the documentation for the versions in use."},
				{Lever: "repo-instructions", Label: "A line in the instructions", Change: "Tell agents to delegate large reads to a sub-agent and to keep long outputs out of the main conversation."},
			}
		}
		candidate.Lever = steps[0].Lever
		candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
			if cause == "runaway" {
				return findingCard{Title: fmt.Sprintf("A few very long conversations in %s use much of its tokens", repository),
					Explanation: fmt.Sprintf("%s this month ran past %s requests or %d compactions. They used %s of the repository's tokens.",
						capitalize(countNoun(stats.Affected, "conversation")), thousands(runawayRequests), runawayCompactions, fractionPhrase(stats.Rate, "")),
					ImpactNote: tokensPhrase(float64(stats.Tokens)), Steps: steps, ChartTitle: "Share of tokens from very long conversations", Rate: fractionPhrase(stats.Rate, "")}
			}
			return findingCard{Title: fmt.Sprintf("Conversations in %s carry very large contexts", repository),
				Explanation: fmt.Sprintf("%s this month sent a request with more than %s tokens of context. They used %s of the repository's tokens.",
					capitalize(countNoun(stats.Affected, "conversation")), compactNumber(oversizedInput), fractionPhrase(stats.Rate, "")),
				ImpactNote: tokensPhrase(float64(stats.Tokens)), Steps: steps, ChartTitle: "Share of tokens from conversations with very large contexts", Rate: fractionPhrase(stats.Rate, "")}
		}
		output = append(output, candidate)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Spec.id() < output[j].Spec.id() })
	return output, nil
}
