package archive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// D7: CLI and external-tool friction. A command-line tool used mostly in one
// repository or automation, whose engagements (the calls to it within one
// conversation) keep asking for help or failing on usage, is a candidate for
// a skill, or for its schema in the automation's prompt. General tools used
// across unrelated repositories (git, gh, psql) are out of scope. The metric
// is the share of engagements with friction; a result also needs help and
// usage errors each to fall and engagements to keep ending in success.

// generalPrograms are tools every repository uses; their friction is not a
// repository's to fix.
var generalPrograms = map[string]bool{}

func init() {
	for _, program := range strings.Fields(`git gh psql sqlite3 ls cat sed awk grep rg find fd head tail wc sort uniq cut tr xargs echo printf cd pwd mkdir rm cp mv
		touch chmod ln env export source python python3 pip pip3 uv node npm npx pnpm yarn bun deno go cargo rustc make cmake curl wget jq yq tar gzip gunzip zcat unzip zip
		diff patch test [ [[ true false sleep kill ps top du df open osascript brew docker kubectl ssh scp rsync tee less more file stat date which whoami bash sh zsh
		python3.11 python3.12 python3.13 ruby perl java javac mvn gradle swift xcodebuild tsc eslint prettier pytest go1 time timeout nohup watch sqlite duckdb code
		codex claude gemini agy conductor pbcopy pbpaste realpath dirname basename mktemp nl od xxd hexdump shasum md5 base64 column paste comm join split seq yes`) {
		generalPrograms[program] = true
	}
}

var helpFlag = regexp.MustCompile(`(?:^|\s)(?:--help|-h|help)(?:\s|$)`)

func detectCLIFriction(env *findingEnv) ([]*findingCandidate, error) {
	groups, err := env.callGroups()
	if err != nil {
		return nil, err
	}
	// One engagement per conversation and program, across tool names.
	merged := map[string]map[string]any{}
	order := []string{}
	for _, group := range groups {
		if group.Category != "command" || group.Program == "" {
			continue
		}
		key := group.Conversation + "\x1f" + group.Program
		row := merged[key]
		if row == nil {
			row = map[string]any{"conversation_id": group.Conversation, "program": group.Program, "calls": int64(0), "help_calls": int64(0), "errors": int64(0),
				"usage_errors": int64(0), "tokens": int64(0), "result_tokens": int64(0), "carried": int64(0), "output": 0.0, "duration_ms": int64(0), "last_sequence": int64(-1)}
			merged[key] = row
			order = append(order, key)
		}
		for field, value := range map[string]int64{"calls": group.Calls, "help_calls": group.Help, "errors": group.Errors, "usage_errors": group.Usage,
			"tokens": group.Tokens, "result_tokens": group.ResultTokens, "carried": group.Carried, "duration_ms": group.DurationMS} {
			row[field] = row[field].(int64) + value
		}
		row["output"] = row["output"].(float64) + group.Output
		if group.LastSequence > row["last_sequence"].(int64) {
			row["last_sequence"], row["last_status"], row["last_at"], row["model"] = group.LastSequence, group.LastStatus, group.LastAt, group.Model
		}
	}
	rows := make([]map[string]any, 0, len(order))
	for _, key := range order {
		rows = append(rows, merged[key])
	}
	type engagement struct {
		conv *findingConversation
		row  map[string]any
	}
	byProgram := map[string][]engagement{}
	repositories := map[string]map[string]int{}
	for _, row := range rows {
		program := firstString(row["program"])
		root := env.roots[firstString(row["conversation_id"])]
		if root == "" || generalPrograms[program] || strings.HasPrefix(program, "-") || strings.Contains(program, "=") {
			continue
		}
		conv := env.convs[root]
		byProgram[program] = append(byProgram[program], engagement{conv, row})
		if repositories[program] == nil {
			repositories[program] = map[string]int{}
		}
		if conv.RepositoryID != "" && env.inGate(conv.Day) {
			repositories[program][conv.RepositoryID]++
		}
	}
	wanted := env.wants("cli")
	candidates := map[string]*findingCandidate{}
	for program, engagements := range byProgram {
		// A tool that several repositories use a lot is general, not theirs.
		busy := 0
		for _, count := range repositories[program] {
			if count >= 3 {
				busy++
			}
		}
		general := busy >= 4
		for _, item := range engagements {
			scope := env.scopeOf(item.conv)
			if scope == "" {
				continue
			}
			key := scope + "\x1fprogram:" + program
			if _, ok := wanted[key]; !ok && (!env.discover || general) {
				continue
			}
			candidate := candidates[key]
			if candidate == nil {
				candidate = &findingCandidate{Spec: findingSpec{Detector: "cli", Scope: scope, Pattern: "program:" + program, Params: map[string]string{"program": program}},
					Obs: map[string]*findingObs{}, Facts: map[string]any{}, Metric: findingMetric{Kind: "rate", Unit: "engagements", GuardSuccess: true}}
				if strings.HasPrefix(scope, "repo:") {
					candidate.RepositoryID = strings.TrimPrefix(scope, "repo:")
				}
				candidates[key] = candidate
			}
			row := item.row
			help, usage := int(integer(row["help_calls"])), int(integer(row["usage_errors"]))
			success := firstString(row["last_status"]) == "ok"
			output, _ := number(row["output"])
			obs := &findingObs{Unit: item.conv.ID, Conversation: item.conv.ID, Workspace: item.conv.WorkspaceID, Day: item.conv.Day, Provider: item.conv.Provider,
				Hit: help > 0 || usage > 0, Occurrences: help + usage, Calls: int(integer(row["calls"])), Errors: int(integer(row["errors"])), Success: &success,
				HelpCalls: help, UsageErrors: usage, Value: float64(integer(row["tokens"])), At: firstString(row["last_at"])}
			if obs.Hit {
				obs.Tokens = integer(row["tokens"])
				obs.CostUSD = env.toolCallCost(item.conv.Provider, firstString(row["model"]), item.conv.Day, integer(row["result_tokens"]), integer(row["carried"]), output)
				obs.DurationMS = integer(row["duration_ms"])
			}
			candidate.Obs[item.conv.ID] = obs
		}
	}
	output := []*findingCandidate{}
	for _, candidate := range candidates {
		if err := discountPromptedHelp(env, candidate); err != nil {
			return nil, err
		}
		if err := addCLIEvidence(env, candidate); err != nil {
			return nil, err
		}
		describeCLI(env, candidate)
		output = append(output, candidate)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Spec.id() < output[j].Spec.id() })
	return output, nil
}

// discountPromptedHelp stops counting help calls the conversation's own
// first message asked for (a TL1 flavor that says "run tl1m handoff --help").
func discountPromptedHelp(env *findingEnv, candidate *findingCandidate) error {
	program := candidate.Spec.Params["program"]
	ids := []any{}
	for id, obs := range candidate.Obs {
		if obs.HelpCalls > 0 {
			ids = append(ids, id)
		}
	}
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		rows, err := queryMapsContext(env.ctx, env.db, `SELECT conversation_id,substr(text,1,6000) text FROM messages WHERE conversation_id IN (`+placeholders(end-start)+`)
			AND +role='user' AND kind='message' ORDER BY conversation_id,source_order IS NULL,source_order,created_at`, ids[start:end]...)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, row := range rows {
			id := firstString(row["conversation_id"])
			if seen[id] {
				continue
			}
			seen[id] = true
			text := firstString(row["text"])
			if strings.Contains(text, program+" --help") || strings.Contains(text, program+" help") || strings.Contains(text, program+" -h") {
				obs := candidate.Obs[id]
				obs.Occurrences -= obs.HelpCalls
				obs.HelpCalls = 0
				obs.Hit = obs.UsageErrors > 0
			}
		}
	}
	return nil
}

// addCLIEvidence reads one help call or usage error per engagement with
// friction, newest first.
func addCLIEvidence(env *findingEnv, candidate *findingCandidate) error {
	program := candidate.Spec.Params["program"]
	ids := []any{}
	for id, obs := range candidate.Obs {
		if obs.Hit && env.inGate(obs.Day) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return candidate.Obs[ids[i].(string)].At > candidate.Obs[ids[j].(string)].At })
	if len(ids) > 40 {
		ids = ids[:40]
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := queryMapsContext(env.ctx, env.db, `SELECT id,conversation_id,workspace_id,command,status,COALESCE(error_signature,'') signature,started_at,COALESCE(call_message_id,'') message_id
		FROM tool_calls WHERE conversation_id IN (`+placeholders(len(ids))+`) AND program=? AND tool_category='command' ORDER BY started_at`, append(ids, program)...)
	if err != nil {
		return err
	}
	commands := map[string]int{}
	for _, row := range rows {
		command := firstString(row["command"])
		help := helpFlag.MatchString(command)
		usage := firstString(row["status"]) == "error" && failureUsage.MatchString(firstString(row["signature"]))
		if !help && !usage {
			continue
		}
		commands[normalizeCommandShape(command)]++
		conv := env.convs[env.roots[firstString(row["conversation_id"])]]
		happened := "read the help"
		if usage {
			happened = "failed: " + clipText(firstString(row["signature"]), 90)
		}
		candidate.addEvidence(findingHandle{At: firstString(row["started_at"]), ConversationID: firstString(row["conversation_id"]), WorkspaceID: firstString(row["workspace_id"]),
			MessageID: firstString(row["message_id"]), ToolCallID: firstString(row["id"]), Where: env.evidenceWhere(conv), Did: "ran `" + clipText(firstLine(command), 80) + "`", Happened: happened})
	}
	candidate.Facts["friction_commands"] = topKeys(commands, 5)
	return nil
}

func describeCLI(env *findingEnv, candidate *findingCandidate) {
	program := candidate.Spec.Params["program"]
	scope := candidate.Spec.Scope
	automated, total := 0, 0
	for _, obs := range candidate.Obs {
		if conv := env.convs[obs.Conversation]; conv != nil {
			total++
			if conv.Automation != "" {
				automated++
			}
		}
	}
	automation := strings.HasPrefix(scope, "automation:") || total > 0 && automated*2 > total
	var steps []findingStep
	if automation {
		steps = []findingStep{
			{Lever: "automation-prompt", Label: "Usage in the prompt", Change: fmt.Sprintf("Put the `%s` commands these runs need, with their arguments and output format, in the prompt that starts them, so agents don't have to look them up.", program)},
			{Lever: "repo-tooling", Label: "A simpler command", Change: fmt.Sprintf("Give `%s` a subcommand or clearer `--help` for what these runs do most, and name it in the prompt.", program)},
		}
	} else {
		steps = []findingStep{
			{Lever: "repo-tooling", Label: "A skill", Change: fmt.Sprintf("Add a skill for `%s` with the invocations agents use most and what each returns, so they stop reading the help first.", program)},
			{Lever: "repo-instructions", Label: "A line in the instructions", Change: fmt.Sprintf("List the common `%s` commands in the repository's agent instructions.", program)},
			{Lever: "repo-tooling", Label: "A simpler command", Change: fmt.Sprintf("Make `%s` easier to call: better defaults, clearer errors, and a short `--help` for the common cases.", program)},
		}
	}
	candidate.Lever = steps[0].Lever
	candidate.Facts["program"] = program
	candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
		help, usage, calls := 0, 0, 0
		for _, obs := range candidate.Obs {
			if env.inGate(obs.Day) {
				help += obs.HelpCalls
				usage += obs.UsageErrors
				calls += obs.Calls
			}
		}
		candidate.Facts["help_calls"], candidate.Facts["usage_errors"], candidate.Facts["calls"] = help, usage, calls
		where := env.scopeName(scope)
		title := fmt.Sprintf("Agents in %s keep looking up how to use `%s`", where, program)
		if usage > help {
			title = fmt.Sprintf("Agents in %s call `%s` the wrong way", where, program)
		}
		if automation && strings.HasPrefix(scope, "automation:") {
			title = fmt.Sprintf("%s runs look up `%s` every time", strings.TrimSuffix(strings.TrimPrefix(where, "TL1's "), " runs"), program)
		}
		parts := []string{}
		if help > 0 {
			parts = append(parts, fmt.Sprintf("read its help %s", countNoun(help, "time")))
		}
		if usage > 0 {
			parts = append(parts, fmt.Sprintf("called it wrongly %s", countNoun(usage, "time")))
		}
		explanation := fmt.Sprintf("Agents %s this month, in %s that used `%s`.", joinWords(parts), fractionPhrase(stats.Rate, "conversations"), program)
		if automation {
			explanation += " These runs start from a script's prompt, which doesn't say how to call it."
		}
		note := countNoun(help, "help call")
		if usage > help {
			note = countNoun(usage, "usage error")
		}
		return findingCard{Title: title, Explanation: explanation, ImpactNote: note, Steps: steps,
			ChartTitle: "Conversations using `" + program + "` that looked it up or called it wrongly", Rate: fractionPhrase(stats.Rate, "")}
	}
}

// documentationServer matches MCP servers that serve reference docs.
var documentationServer = regexp.MustCompile(`(?i)docs?\b|context7|deepwiki|documentation|reference|manual`)

// Documentation hosts: agents in one repository fetching the same
// reference site again and again, which a vendored copy would answer.
var generalHosts = regexp.MustCompile(`(?i)^(?:localhost|127\.|0\.0\.0\.0|\[::1\]|github\.com$|gist\.github|www\.google\.|google\.com$|bing\.com|duckduckgo|api\.|.*\.local$)`)

func detectDocsHosts(env *findingEnv) ([]*findingCandidate, error) {
	rows, err := queryMapsContext(env.ctx, env.db, `SELECT t.conversation_id,u.host,COUNT(*) fetches,MAX(t.started_at) last_at,SUM(t.result_tokens) result_tokens,SUM(t.carried_tokens) carried,
		SUM(t.output_tokens) output,COALESCE(t.model,'') model,SUM(COALESCE(t.duration_ms,0)) duration_ms,MIN(u.url) url,MIN(t.id) call_id,MIN(COALESCE(t.call_message_id,'')) message_id
		FROM tool_urls u JOIN tool_calls t ON t.id=u.tool_call_id WHERE t.started_at>=? AND u.source IN ('input','result') AND COALESCE(u.host,'')<>'' GROUP BY 1,2`, env.fromUTC)
	if err != nil {
		return nil, err
	}
	// Documentation MCP servers count like documentation sites: the same
	// lookups again and again are a reference worth keeping in the repository.
	servers, err := queryMapsContext(env.ctx, env.db, `SELECT t.conversation_id,'mcp:'||t.mcp_server host,COUNT(*) fetches,MAX(t.started_at) last_at,SUM(t.result_tokens) result_tokens,
		SUM(t.carried_tokens) carried,SUM(t.output_tokens) output,COALESCE(t.model,'') model,SUM(COALESCE(t.duration_ms,0)) duration_ms,MIN(t.tool_name) url,MIN(t.id) call_id,
		MIN(COALESCE(t.call_message_id,'')) message_id FROM tool_calls t WHERE t.started_at>=? AND COALESCE(t.mcp_server,'')<>'' GROUP BY 1,2`, env.fromUTC)
	if err != nil {
		return nil, err
	}
	for _, row := range servers {
		if documentationServer.MatchString(strings.TrimPrefix(firstString(row["host"]), "mcp:")) {
			rows = append(rows, row)
		}
	}
	wanted := env.wants("docs")
	type fetch struct {
		conv *findingConversation
		row  map[string]any
	}
	byHost := map[string][]fetch{}
	repositories := map[string]map[string]bool{}
	for _, row := range rows {
		host := strings.ToLower(firstString(row["host"]))
		root := env.roots[firstString(row["conversation_id"])]
		if root == "" || generalHosts.MatchString(host) {
			continue
		}
		conv := env.convs[root]
		byHost[host] = append(byHost[host], fetch{conv, row})
		if repositories[host] == nil {
			repositories[host] = map[string]bool{}
		}
		if conv.RepositoryID != "" {
			repositories[host][conv.RepositoryID] = true
		}
	}
	candidates := map[string]*findingCandidate{}
	hits := map[string]map[string]*findingObs{}
	for host, fetches := range byHost {
		for _, item := range fetches {
			scope := env.scopeOf(item.conv)
			if !strings.HasPrefix(scope, "repo:") {
				continue
			}
			key := scope + "\x1fhost:" + host
			if _, ok := wanted[key]; !ok && (!env.discover || len(repositories[host]) >= 4) {
				continue
			}
			if hits[key] == nil {
				hits[key] = map[string]*findingObs{}
			}
			output, _ := number(item.row["output"])
			obs := &findingObs{Unit: item.conv.ID, Conversation: item.conv.ID, Workspace: item.conv.WorkspaceID, Day: item.conv.Day, Provider: item.conv.Provider, Hit: true,
				Occurrences: int(integer(item.row["fetches"])), Tokens: integer(item.row["result_tokens"]) + integer(item.row["carried"]),
				CostUSD:    env.toolCallCost(item.conv.Provider, firstString(item.row["model"]), item.conv.Day, integer(item.row["result_tokens"]), integer(item.row["carried"]), output),
				DurationMS: integer(item.row["duration_ms"]), At: firstString(item.row["last_at"])}
			hits[key][item.conv.ID] = obs
			if candidates[key] == nil {
				candidates[key] = &findingCandidate{Spec: findingSpec{Detector: "docs", Scope: scope, Pattern: "host:" + host, Params: map[string]string{"host": host}},
					RepositoryID: strings.TrimPrefix(scope, "repo:"), Facts: map[string]any{"host": host}, Metric: findingMetric{Kind: "rate", Unit: "conversations"}}
			}
			candidates[key].addEvidence(findingHandle{At: obs.At, ConversationID: item.conv.ID, WorkspaceID: item.conv.WorkspaceID, ToolCallID: firstString(item.row["call_id"]),
				MessageID: firstString(item.row["message_id"]), Where: env.evidenceWhere(item.conv), Did: "fetched " + clipText(firstString(item.row["url"]), 80),
				Happened: countNoun(obs.Occurrences, "page") + " from " + host})
		}
	}
	for key, spec := range wanted {
		if candidates[key] == nil {
			candidates[key] = &findingCandidate{Spec: spec, RepositoryID: strings.TrimPrefix(spec.Scope, "repo:"), Facts: map[string]any{"host": spec.Params["host"]},
				Metric: findingMetric{Kind: "rate", Unit: "conversations"}}
		}
	}
	output := []*findingCandidate{}
	for key, candidate := range candidates {
		candidate.Obs = map[string]*findingObs{}
		for _, conv := range env.convs {
			if env.inScope(candidate.Spec.Scope, conv) {
				candidate.Obs[conv.ID] = &findingObs{Unit: conv.ID, Conversation: conv.ID, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider}
			}
		}
		for id, obs := range hits[key] {
			candidate.Obs[id] = obs
		}
		host := candidate.Spec.Params["host"]
		if server, ok := strings.CutPrefix(host, "mcp:"); ok {
			host = "the " + server + " MCP server"
		}
		steps := []findingStep{
			{Lever: "repo-tooling", Label: "A local copy of the reference", Change: fmt.Sprintf("Save the parts of %s that agents keep reading into the repository (for example `docs/reference/`), and point the instructions at them.", host)},
			{Lever: "repo-tooling", Label: "A skill", Change: fmt.Sprintf("Add a skill that summarizes what agents look up on %s, with links for the rest.", host)},
		}
		candidate.Lever = steps[0].Lever
		candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
			return findingCard{Title: fmt.Sprintf("Agents in %s keep reading %s", env.scopeName(candidate.Spec.Scope), host),
				Explanation: fmt.Sprintf("Agents fetched pages from %s in %s this month, looking up the same reference each time.", host, fractionPhrase(stats.Rate, "conversations")),
				ImpactNote:  countNoun(stats.Occurrences, "page fetch"), Steps: steps, ChartTitle: "Conversations that fetched " + host, Rate: fractionPhrase(stats.Rate, "")}
		}
		output = append(output, candidate)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Spec.id() < output[j].Spec.id() })
	return output, nil
}
