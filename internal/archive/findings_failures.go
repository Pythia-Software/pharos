package archive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// D1: recurring failure signatures, with D2's fail-then-fix recoveries as
// the proposed change. Errored calls carry the ledger's normalized signature
// (tool_errors.go); test failures are left out, since a red test means the
// tool worked. A signature in three or more repositories becomes one global
// finding per Mac and provider.

// failureCall is one errored call.
type failureCall struct {
	ID, Conversation, Root, Workspace, Provider, Model, Program, Subcommand string
	ToolName, Category, Command, ErrorType, Signature, StartedAt, Day       string
	CallMessage, ResultMessage                                              string
	Sequence                                                                int64
	DurationMS, ResultTokens, Carried                                       int64
	Output                                                                  float64
}

// failureExposure is who could have hit a failure: conversations that ran
// the program, used the tool, or ran any shell command.
type failureExposure struct {
	Kind, Key string
}

func (exposure failureExposure) key() string { return exposure.Kind + ":" + exposure.Key }

var (
	// failureNotice matches signatures that are harness notices or ordinary
	// output rather than failures.
	failureNotice = regexp.MustCompile(`(?i)^(?:warning: truncated output|total (?:<n>|\d+)$|traceback \(most recent call last\):?$|command timed out|<persisted-output>)`)
	// failureNoLever matches failures only the harness can fix, such as its
	// own tool-input parsing. They are kept for trend but never shown.
	failureNoLever = regexp.MustCompile(`(?i)input that could not be parsed as json|^inputvalidationerror: \w+ was called with input|stream closed|browser is not available|no browser is available`)
	// failureShell matches failures of the shell itself, which any shell
	// command could hit.
	failureShell = regexp.MustCompile(`(?i)command not found|^\(eval\)|^(?:zsh|bash|sh):|== not found|parse error near|bad substitution|no matches found|too complex to verify|^blocked: `)
	// failureUsage matches a CLI rejecting how it was called.
	failureUsage = regexp.MustCompile(`(?i)^usage:|unknown (?:flag|option|command|shorthand)|unrecognized (?:arguments|option)|invalid choice|flag provided but not defined|unexpected argument|required argument|missing required|too many arguments|invalid (?:value|argument) for`)
)

func failureExposureFor(call failureCall) failureExposure {
	if call.Category != "command" {
		return failureExposure{"tool", call.ToolName}
	}
	if call.ErrorType == "hook_blocked" || call.ErrorType == "harness_error" || failureShell.MatchString(call.Signature) || call.Program == "" {
		return failureExposure{"shell", ""}
	}
	return failureExposure{"program", call.Program}
}

func detectFailures(env *findingEnv) ([]*findingCandidate, error) {
	rows, err := queryMapsContext(env.ctx, env.db, `SELECT t.id,t.conversation_id,t.workspace_id,t.provider,COALESCE(t.model,'') model,COALESCE(t.program,'') program,
		COALESCE(t.subcommand,'') subcommand,t.tool_name,t.tool_category,COALESCE(t.command,'') command,COALESCE(t.error_type,'') error_type,t.error_signature,
		t.started_at,COALESCE(t.duration_ms,0) duration_ms,t.result_tokens,t.carried_tokens,t.output_tokens,COALESCE(t.call_message_id,'') call_message_id,
		COALESCE(t.result_message_id,'') result_message_id,t.sequence
		FROM tool_calls t WHERE t.started_at>=? AND t.status='error' AND t.test_failure=0 AND t.error_signature IS NOT NULL`, env.fromUTC)
	if err != nil {
		return nil, err
	}
	bySignature := map[string][]failureCall{}
	for _, row := range rows {
		root := env.roots[firstString(row["conversation_id"])]
		if root == "" {
			continue
		}
		call := failureCall{ID: firstString(row["id"]), Conversation: firstString(row["conversation_id"]), Root: root, Workspace: env.convs[root].WorkspaceID,
			Provider: firstString(row["provider"]), Model: firstString(row["model"]), Program: firstString(row["program"]), Subcommand: firstString(row["subcommand"]),
			ToolName: firstString(row["tool_name"]), Category: firstString(row["tool_category"]), Command: firstString(row["command"]), ErrorType: firstString(row["error_type"]),
			Signature: firstString(row["error_signature"]), StartedAt: firstString(row["started_at"]), Day: localDay(firstString(row["started_at"])),
			CallMessage: firstString(row["call_message_id"]), ResultMessage: firstString(row["result_message_id"]), Sequence: integer(row["sequence"]),
			DurationMS: integer(row["duration_ms"]), ResultTokens: integer(row["result_tokens"]), Carried: integer(row["carried_tokens"])}
		call.Output, _ = number(row["output_tokens"])
		if call.ErrorType == "user_rejected" || call.ErrorType == "timeout" || call.ErrorType == "interrupted" || failureNotice.MatchString(call.Signature) {
			continue
		}
		family := failureFamily(call.Signature)
		bySignature[family] = append(bySignature[family], call)
	}
	wanted := env.wants("failure")
	type group struct {
		scope    string
		calls    []failureCall
		exposure failureExposure
		global   bool
		aliases  []string
	}
	groups := map[string]*group{}
	add := func(scope, family string, call failureCall, global bool) {
		key := scope + "\x1f" + family
		if groups[key] == nil {
			groups[key] = &group{scope: scope, global: global}
		}
		groups[key].calls = append(groups[key].calls, call)
	}
	for signature, calls := range bySignature {
		repositories := map[string]bool{}
		for _, call := range calls {
			if conv := env.convs[call.Root]; env.inGate(call.Day) && conv.RepositoryID != "" {
				repositories[conv.RepositoryID] = true
			}
		}
		global := len(repositories) >= 3
		for _, call := range calls {
			conv := env.convs[call.Root]
			if global {
				add(globalScope(conv), signature, call, true)
			} else if scope := env.scopeOf(conv); scope != "" {
				add(scope, signature, call, false)
			}
		}
		// Measured specs keep their scope, whatever this pass decided.
		for _, spec := range wanted {
			if spec.Pattern != signature {
				continue
			}
			key := spec.Scope + "\x1f" + signature
			if groups[key] != nil {
				continue
			}
			groups[key] = &group{scope: spec.Scope, global: strings.HasPrefix(spec.Scope, "global:")}
			for _, call := range calls {
				if env.inScope(spec.Scope, env.convs[call.Root]) {
					groups[key].calls = append(groups[key].calls, call)
				}
			}
		}
	}
	for _, spec := range wanted {
		key := spec.Scope + "\x1f" + spec.Pattern
		if groups[key] == nil {
			groups[key] = &group{scope: spec.Scope, global: strings.HasPrefix(spec.Scope, "global:"),
				exposure: failureExposure{spec.Params["exposure"], spec.Params["key"]}}
		}
	}
	// Each group's exposure is the most common among its calls.
	needed := map[string]bool{}
	for key, item := range groups {
		signature := key[strings.Index(key, "\x1f")+1:]
		if !env.discover && wanted[key].Detector == "" {
			delete(groups, key)
			continue
		}
		if item.exposure.Kind == "" {
			counts := map[failureExposure]int{}
			for _, call := range item.calls {
				counts[failureExposureFor(call)]++
			}
			best, top := failureExposure{"shell", ""}, 0
			for exposure, count := range counts {
				if count > top || count == top && exposure.key() < best.key() {
					best, top = exposure, count
				}
			}
			item.exposure = best
		}
		if spec, ok := wanted[key]; ok && spec.Params["exposure"] != "" {
			item.exposure = failureExposure{spec.Params["exposure"], spec.Params["key"]}
		}
		needed[item.exposure.key()] = true
		_ = signature
	}
	exposures, err := failureExposures(env, needed)
	if err != nil {
		return nil, err
	}
	candidates := []*findingCandidate{}
	for key, item := range groups {
		signature := key[strings.Index(key, "\x1f")+1:]
		spec := findingSpec{Detector: "failure", Scope: item.scope, Pattern: signature,
			Params: map[string]string{"exposure": item.exposure.Kind, "key": item.exposure.Key}}
		candidate := &findingCandidate{Spec: spec, Obs: map[string]*findingObs{}, Metric: findingMetric{Kind: "rate", Unit: "conversations",
			GuardErrors: item.exposure.Kind == "program", Failures: true}, Facts: map[string]any{}}
		if strings.HasPrefix(item.scope, "repo:") {
			candidate.RepositoryID = strings.TrimPrefix(item.scope, "repo:")
		}
		exposed := exposures[item.exposure.key()]
		for root, counts := range exposed {
			conv := env.convs[root]
			if !env.inScope(item.scope, conv) {
				continue
			}
			candidate.Obs[root] = &findingObs{Unit: root, Conversation: root, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider,
				Calls: counts[0], Errors: counts[1]}
		}
		for _, call := range item.calls {
			conv := env.convs[call.Root]
			obs := candidate.Obs[call.Root]
			if obs == nil {
				obs = &findingObs{Unit: call.Root, Conversation: call.Root, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider}
				candidate.Obs[call.Root] = obs
			}
			obs.Hit = true
			obs.Occurrences++
			obs.Tokens += call.ResultTokens + call.Carried
			obs.CostUSD += env.toolCallCost(call.Provider, call.Model, call.Day, call.ResultTokens, call.Carried, call.Output)
			obs.DurationMS += call.DurationMS
			if call.StartedAt > obs.At {
				obs.At = call.StartedAt
			}
			candidate.addEvidence(findingHandle{At: call.StartedAt, ConversationID: call.Conversation, WorkspaceID: call.Workspace, MessageID: defaultString(call.ResultMessage, call.CallMessage),
				ToolCallID: call.ID, Where: env.evidenceWhere(conv), Did: failureDid(call), Happened: "failed: " + clipText(call.Signature, 110)})
		}
		// The same failure outside this scope, for results.
		candidate.Elsewhere = map[string]*findingObs{}
		for root, counts := range exposed {
			conv := env.convs[root]
			if env.inScope(item.scope, conv) {
				continue
			}
			candidate.Elsewhere[root] = &findingObs{Unit: root, Conversation: root, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider, Calls: counts[0], Errors: counts[1]}
		}
		for _, call := range bySignature[signature] {
			if obs := candidate.Elsewhere[call.Root]; obs != nil {
				obs.Hit = true
			}
		}
		if item.global {
			// The repository findings this one replaces.
			// A repository finding being measured stays in its own scope,
			// so its result isn't diluted across every repository.
			for _, call := range item.calls {
				if scope := env.scopeOf(env.convs[call.Root]); scope != "" {
					if alias := (findingSpec{Detector: "failure", Scope: scope, Pattern: signature}).id(); !env.measuring[alias] {
						candidate.Aliases = append(candidate.Aliases, alias)
					}
				}
			}
			candidate.Aliases = uniqueStrings(candidate.Aliases)
		}
		candidate.Hidden = failureNoLever.MatchString(signature)
		describeFailure(env, candidate, item.calls, item.exposure)
		candidates = append(candidates, candidate)
	}
	calls := groups2calls(candidates, bySignature)
	if err := addRecoveries(env, candidates, calls); err != nil {
		return nil, err
	}
	if !env.discover {
		// Recoveries are read on full passes; a measuring pass keeps them.
		for _, candidate := range candidates {
			row := env.stored[candidate.Spec.id()]
			if row == nil {
				continue
			}
			for _, key := range []string{"recoveries", "recovery", "recovery_change", "recovery_example", "iteration"} {
				if value, ok := row.Facts[key]; ok {
					candidate.Facts[key] = value
				}
			}
			if row.Facts["iteration"] == true {
				candidate.Hidden = true
			}
		}
	}
	for _, candidate := range candidates {
		if failureIteration(candidate.Spec.Pattern, calls[candidate], candidate.Facts["recovery"] != nil) {
			candidate.Hidden = true
			candidate.Facts["iteration"] = true
			delete(candidate.Facts, "recovery")
			delete(candidate.Facts, "recovery_change")
		}
	}
	return candidates, nil
}

var (
	genericException = regexp.MustCompile(`^(?:[\w.]+\.)?\w*(?:Error|Exception|Warning)\b|^(?:TypeError|ReferenceError|SyntaxError)\b|^error: [\w\s]+ not found$|^jq: error|^jq: parse error`)
	inlineScript     = regexp.MustCompile(`<<|\s-c\s|\s-e\s|\s-\s*$|\bpython3?\s+-\s`)
	exploring        = map[string]bool{"ls": true, "cat": true, "head": true, "tail": true, "find": true, "stat": true, "file": true, "wc": true, "cd": true, "tree": true,
		"grep": true, "ugrep": true, "rg": true, "sed": true, "awk": true, "du": true, "open": true, "less": true}
)

// failureIteration reports failures that are the agent's own iteration, not
// a setup problem: stale edits, exploring paths that don't exist, and
// exceptions from scripts the agent wrote inline. A known failure pattern
// never is.
func signatureName(signature string) string {
	if match := signatureProgram.FindStringSubmatch(signature); match != nil {
		return match[1]
	}
	return ""
}

func failureIteration(signature string, calls []failureCall, recovered bool) bool {
	if knownFailure(signature) != nil || len(calls) == 0 {
		return false
	}
	own := 0
	for _, call := range calls {
		switch {
		case call.ErrorType == "edit_no_match" || call.ErrorType == "file_not_found" || call.ErrorType == "file_too_large":
			own++
		case strings.Contains(strings.ToLower(signature), "no such file or directory") && (exploring[call.Program] || exploring[signatureName(signature)]):
			own++
		case !recovered && genericException.MatchString(signature) && inlineScript.MatchString(call.Command):
			own++
		}
	}
	return float64(own) >= 0.8*float64(len(calls))
}

// knownFailure returns the known pattern a signature (or a family key)
// matches, if any.
func knownFailure(signature string) *failurePattern {
	for index := range failurePatterns {
		if "known:"+failurePatterns[index].name == signature || failurePatterns[index].match.MatchString(signature) {
			return &failurePatterns[index]
		}
	}
	return nil
}

// failureFamily is the key a signature's finding is built on: a known
// pattern's name, so its variants are one finding, or the signature.
func failureFamily(signature string) string {
	if known := knownFailure(signature); known != nil {
		return "known:" + known.name
	}
	return signature
}

// signatureProgram is the program an error line names ("ls: …"), which in
// a compound command is often not the command's first program.
var signatureProgram = regexp.MustCompile(`^(?:\(eval\):(?:<n>|\d+): |\(eval\):)?([\w][\w.+-]*):(?:<n>:|\d+:)? `)

// groups2calls maps each candidate to its errored calls again, for the
// recovery pass.
func groups2calls(candidates []*findingCandidate, bySignature map[string][]failureCall) map[*findingCandidate][]failureCall {
	output := map[*findingCandidate][]failureCall{}
	for _, candidate := range candidates {
		for _, call := range bySignature[candidate.Spec.Pattern] {
			if obs := candidate.Obs[call.Root]; obs != nil && obs.Hit {
				output[candidate] = append(output[candidate], call)
			}
		}
	}
	return output
}

// failureExposures reads, per top-level conversation, the calls and errors
// of each needed program or tool, and whether it ran any shell command.
func failureExposures(env *findingEnv, needed map[string]bool) (map[string]map[string][2]int, error) {
	output := map[string]map[string][2]int{}
	for key := range needed {
		output[key] = map[string][2]int{}
	}
	if len(needed) == 0 {
		return output, nil
	}
	groups, err := env.callGroups()
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		root := env.roots[group.Conversation]
		keys := []string{"tool:" + group.Tool}
		if group.Category == "command" {
			keys = append(keys, "shell:")
			if group.Program != "" {
				keys = append(keys, "program:"+group.Program)
			}
		}
		for _, key := range keys {
			if set := output[key]; set != nil {
				counts := set[root]
				counts[0] += int(group.Calls)
				counts[1] += int(group.Errors)
				set[root] = counts
			}
		}
	}
	return output, nil
}

// evidenceWhere names where an example happened: the repository, or the
// harness for work outside one.
func (env *findingEnv) evidenceWhere(conv *findingConversation) string {
	if conv == nil {
		return ""
	}
	if conv.SourceKind == "tl1" && conv.Flavor != "" {
		return "TL1 " + conv.Flavor
	}
	if name := env.repositories[conv.RepositoryID]; name != "" {
		return name
	}
	return providerLabel(conv.Provider)
}

func failureDid(call failureCall) string {
	if call.Category == "command" && call.Command != "" {
		return "ran `" + clipText(firstLine(call.Command), 80) + "`"
	}
	return "used " + call.ToolName
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index] + " …"
	}
	return text
}

// failurePattern is a known failure with wording better than the generic
// template: what the user would call it and the fix to make.
type failurePattern struct {
	// name keys the finding, so every variant of the pattern is one finding.
	name   string
	match  *regexp.Regexp
	title  func(program, scope string) string
	why    string
	change string
	lever  string
	label  string
	// harness marks failures the harness raises, which only its settings
	// or global instructions can prevent.
	harness bool
}

var failurePatterns = []failurePattern{
	{name: "python-missing", match: regexp.MustCompile(`(?i)command not found: python$|python: command not found`), title: func(string, string) string { return "Agents call `python`, which isn't installed" },
		why: "Only `python3` is on this Mac's path, so each call fails and the agent retries with `python3`.", change: "tell agents to use `python3`, or add a `python` shim that runs it"},
	{name: "zsh-equals", match: regexp.MustCompile(`(?i)== not found|= not found$`), title: func(string, string) string { return "zsh rejects the comparisons agents write" },
		why: "Agents write `[ \"$a\" == \"$b\" ]`, which zsh rejects, and retry with `=`.", change: "tell agents that the shell is zsh, so comparisons use `=` or `[[ ]]`"},
	{name: "gzip-as-text", match: regexp.MustCompile(`(?i)0x8b in position`), title: func(_, scope string) string { return "Gzipped files are read as text" + scopeSuffix(scope) },
		why: "Agents open gzip-compressed files as UTF-8 text, which fails on the first byte.", change: "tell agents which files are gzipped and to read them with `gzip.open` or `zcat`"},
	{name: "gh-unpushed", match: regexp.MustCompile(`(?i)must first push the current branch`), title: func(string, string) string { return "`gh pr create` fails on unpushed branches" },
		why: "Agents open a pull request before pushing the branch, and `gh` refuses.", change: "push the branch before `gh pr create`, or pass `--head <branch>`"},
	{name: "sleep-blocked", match: regexp.MustCompile(`(?i)^blocked: sleep`), title: func(string, string) string { return "Agents wait with `sleep` and get blocked" }, harness: true,
		why: "Claude Code blocks `sleep` followed by another command, so the agent has to try again.", change: "tell agents to wait with the Monitor tool or a background task instead of `sleep`", lever: "global-instructions"},
	{name: "worktree-guard", match: regexp.MustCompile(`(?i)too complex to verify that it stays inside`), title: func(string, string) string { return "Claude can't check that commands stay in the worktree" }, harness: true,
		why: "Claude Code blocks compound commands in an isolated worktree when it can't verify where they write.", change: "tell agents to run one simple command per call in worktrees, without `cd` chains or subshells", lever: "global-instructions"},
	{name: "ssl-verify", match: regexp.MustCompile(`(?i)certificate verify failed|ssl: certificate`), title: func(program, _ string) string {
		return "`" + defaultString(program, "A script") + "` can't verify SSL certificates"
	},
		why: "The request fails before it reaches the server, because the script doesn't trust the certificate chain.", change: "point the script at the right CA bundle (for example `certifi` or the system store); an instruction can't fix this", lever: "repo-tooling", label: "A fix to the script"},
	{name: "sandbox-write", match: regexp.MustCompile(`(?i)operation not permitted`), title: func(program, _ string) string {
		return "The sandbox stops `" + defaultString(program, "a command") + "`"
	},
		why: "The agent's sandbox refuses the write, so the command fails however it is phrased.", change: "allow that path in the harness's sandbox settings, or move the write inside the workspace", lever: "harness-settings", label: "A sandbox setting"},
	{name: "test-imports", match: regexp.MustCompile(`(?i)importerror while importing test module|no module named`), title: func(program, scope string) string { return "Tests" + scopeIn(scope) + " fail on imports" },
		why: "The test runner can't import the package, usually because the working directory isn't on the import path.", change: "document how to run the tests (for example `PYTHONPATH=. pytest`), or add a test command that sets it"},
	{name: "zsh-glob", match: regexp.MustCompile(`(?i)no matches found`), title: func(string, string) string { return "zsh stops on file patterns that match nothing" },
		why: "Unlike bash, zsh fails a command when a wildcard matches no files, so the agent has to rerun it.", change: "tell agents that the shell is zsh: quote wildcards meant for the tool, and check that files exist before globbing them"},
}

func scopeSuffix(scope string) string {
	if scope == "" {
		return ""
	}
	return " in " + scope
}

func scopeIn(scope string) string {
	if scope == "" {
		return ""
	}
	return " in " + scope
}

// describeFailure sets the candidate's lever and wording.
func describeFailure(env *findingEnv, candidate *findingCandidate, calls []failureCall, exposure failureExposure) {
	signatures := map[string]int{}
	for _, call := range calls {
		signatures[call.Signature]++
	}
	signature := defaultString(topKey(signatures), candidate.Spec.Pattern)
	scope := candidate.Spec.Scope
	global := strings.HasPrefix(scope, "global:")
	name := env.scopeName(scope)
	programs := map[string]int{}
	commands := map[string]int{}
	repositories := map[string]bool{}
	automated := 0
	convs := map[string]bool{}
	for _, call := range calls {
		programs[defaultString(call.Program, call.ToolName)]++
		if call.Command != "" {
			commands[normalizeCommandShape(call.Command)]++
		}
		conv := env.convs[call.Root]
		if conv.RepositoryID != "" {
			repositories[env.repositories[conv.RepositoryID]] = true
		}
		if !convs[call.Root] {
			convs[call.Root] = true
			if conv.Automation != "" {
				automated++
			}
		}
	}
	program := topKey(programs)
	known := knownFailure(signature)
	instructionLever, instructionFile := "repo-instructions", "the repository's agent instructions"
	switch {
	case global:
		_, provider, _ := strings.Cut(strings.TrimPrefix(scope, "global:"), ":")
		instructionLever, instructionFile = "global-instructions", globalInstructionFile(provider)
	case strings.HasPrefix(scope, "automation:"):
		instructionLever, instructionFile = "automation-prompt", "the "+strings.TrimPrefix(name, "TL1's ")+" prompt"
	case len(convs) > 0 && automated*2 > len(convs):
		instructionLever, instructionFile = "automation-prompt", "the prompt of the script that starts these runs"
	}
	steps := []findingStep{}
	if known != nil && known.lever != "" && known.lever != "global-instructions" {
		steps = append(steps, findingStep{Lever: known.lever, Change: capitalize(known.change) + ".", Label: defaultString(known.label, "A tooling change")})
	} else {
		change := "Add a line to " + instructionFile + " that says what goes wrong and how to avoid it."
		if known != nil {
			change = capitalize(known.change) + ", in " + instructionFile + "."
		}
		steps = append(steps, findingStep{Lever: instructionLever, Change: change, Label: "A line in " + shortInstructionFile(instructionFile)})
		mechanical := findingStep{Lever: "repo-tooling", Change: "Make the right way the easy way: a script, shim, or Makefile target that runs `" + defaultString(program, "the command") + "` correctly, and point agents at it.", Label: "A script or shim"}
		if global || known != nil && known.harness {
			mechanical = findingStep{Lever: "harness-settings", Change: "Add a hook that catches the failing form of the command before it runs and says what to do instead.", Label: "A hook"}
		}
		steps = append(steps, mechanical)
		if !global {
			steps = append(steps, findingStep{Lever: "repo-tooling", Change: "Add a skill for this task that shows the working command, so agents follow it instead of guessing.", Label: "A skill"})
		}
	}
	candidate.Lever = steps[0].Lever
	candidate.Facts = map[string]any{"signature": signature, "program": nilIfEmpty(program), "exposure": map[string]string{"kind": exposure.Kind, "key": exposure.Key},
		"commands": topKeys(commands, 5), "repositories": sortedFindingKeys(repositories), "automated_share": ratio(automated, len(convs))}
	candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
		title := "`" + defaultString(program, "A tool") + "` fails with “" + clipText(signature, 70) + "”"
		why := ""
		if known != nil {
			title = known.title(program, map[bool]string{true: "", false: strings.TrimSuffix(name, " runs")}[global])
			why = " " + known.why
		}
		where := "In " + name
		if global {
			where = "Across " + countNoun(len(repositories), "repository")
			if len(repositories) < 2 {
				where = "In one repository"
			}
			title += env.hostSuffix(scope)
		}
		exposureWords := map[string]string{"program": "conversations that ran `" + exposure.Key + "`", "tool": "conversations that used " + exposure.Key,
			"shell": "conversations that ran shell commands"}[exposure.Kind]
		explanation := strings.TrimSpace(why + fmt.Sprintf(" %s, it happened in %s this month.", where, fractionPhrase(stats.Rate, exposureWords)))
		if recovery := firstString(candidate.Facts["recovery"]); recovery != "" {
			explanation += " Agents usually recovered by " + recovery + "."
		}
		if provider, share := stats.mainProvider(); share >= 0.8 && provider != "" && len(stats.Providers) > 1 {
			explanation += fmt.Sprintf(" Nearly all were %s.", providerLabel(provider))
		}
		card := findingCard{Title: title, Explanation: explanation, ImpactNote: countNoun(stats.Occurrences, "failed "+map[bool]string{true: "command", false: "call"}[exposure.Kind != "tool"]),
			Steps: steps, ChartTitle: capitalize(exposureWords) + " that hit this", Rate: fractionPhrase(stats.Rate, "")}
		if recovery := firstString(candidate.Facts["recovery_change"]); recovery != "" && candidate.Lever != "repo-tooling" && candidate.Lever != "harness-settings" {
			card.Steps = append([]findingStep{}, steps...)
			card.Steps[0].Change = capitalize(recovery) + ": say so in " + instructionFile + "."
		}
		return card
	}
}

func globalInstructionFile(provider string) string {
	switch provider {
	case "claude":
		return "`~/.claude/CLAUDE.md`"
	case "codex":
		return "`~/.codex/AGENTS.md`"
	case "antigravity", "gemini":
		return "`~/.gemini/GEMINI.md`"
	}
	return "your global agent instructions"
}

func shortInstructionFile(file string) string {
	switch {
	case strings.Contains(file, "CLAUDE.md"):
		return "CLAUDE.md"
	case strings.Contains(file, "AGENTS.md"):
		return "AGENTS.md"
	case strings.Contains(file, "GEMINI.md"):
		return "GEMINI.md"
	case strings.Contains(file, "prompt"):
		return "the prompt"
	}
	return "the instructions"
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	if strings.HasPrefix(text, "`") {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

func topKey(counts map[string]int) string {
	best, top := "", 0
	for key, count := range counts {
		if count > top || count == top && key < best {
			best, top = key, count
		}
	}
	return best
}

func topKeys(counts map[string]int, limit int) []string {
	keys := sortedFindingKeys(counts)
	sort.SliceStable(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

func ratio(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

var (
	shapeNumber = regexp.MustCompile(`\b\d+\b`)
	shapePath   = regexp.MustCompile(`(?:[\w.-]*/)+[\w.-]+`)
)

// normalizeCommandShape keeps a command's program, subcommand, and flags,
// replacing paths and numbers, so similar commands group together.
func normalizeCommandShape(command string) string {
	parsed := parseShellCommand(command)
	segment := parsed.primary()
	text := segment.Text
	if text == "" {
		text = firstLine(command)
	}
	words := shellWords(text)
	if len(words) > 8 {
		words = append(words[:8], "…")
	}
	shape := strings.Join(words, " ")
	shape = shapePath.ReplaceAllString(shape, "<path>")
	return clipText(shapeNumber.ReplaceAllString(shape, "<n>"), 90)
}

// recovery is one failed call followed by a working call of the same program.
type recovery struct {
	failed, fixed string
	key           string
	iteration     bool
}

// addRecoveries is D2: for each candidate with enough conversations, find
// the working call that followed each failure. Consistent recoveries become
// the proposed change; failures whose recoveries only change targets or
// script bodies are ordinary iteration and are dropped.
func addRecoveries(env *findingEnv, candidates []*findingCandidate, calls map[*findingCandidate][]failureCall) error {
	byConversation := map[string][]failureCall{}
	programs := map[string]bool{}
	for candidate, items := range calls {
		if candidate.Spec.Params["exposure"] != "program" || !env.discover {
			continue
		}
		hits := 0
		for _, obs := range candidate.Obs {
			if obs.Hit && env.inGate(obs.Day) {
				hits++
			}
		}
		if hits < findingCheckpoints[0] {
			continue
		}
		for _, call := range items {
			byConversation[call.Conversation] = append(byConversation[call.Conversation], call)
			programs[call.Program] = true
		}
	}
	if len(byConversation) == 0 {
		return nil
	}
	ids := sortedFindingKeys(byConversation)
	following := map[string][]map[string]any{}
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		args := []any{}
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		rows, err := queryMapsContext(env.ctx, env.db, `SELECT conversation_id,sequence,COALESCE(program,'') program,COALESCE(command,'') command,status,test_failure
			FROM tool_calls WHERE tool_category='command' AND conversation_id IN (`+placeholders(len(args))+`) ORDER BY conversation_id,sequence`, args...)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if programs[firstString(row["program"])] {
				following[firstString(row["conversation_id"])] = append(following[firstString(row["conversation_id"])], row)
			}
		}
	}
	for candidate, items := range calls {
		found := []recovery{}
		for _, call := range items {
			for _, next := range following[call.Conversation] {
				if integer(next["sequence"]) <= call.Sequence || firstString(next["program"]) != call.Program {
					continue
				}
				if integer(next["sequence"]) > call.Sequence+25 {
					break
				}
				if firstString(next["status"]) != "ok" {
					continue
				}
				if item, ok := commandRecovery(call.Command, firstString(next["command"])); ok {
					found = append(found, item)
				}
				break
			}
		}
		if len(found) == 0 {
			continue
		}
		keys := map[string]int{}
		iteration := 0
		examples := map[string]recovery{}
		for _, item := range found {
			if item.iteration {
				iteration++
				continue
			}
			keys[item.key]++
			examples[item.key] = item
		}
		best := topKey(keys)
		candidate.Facts["recoveries"] = len(found)
		if best != "" && keys[best] >= 3 && float64(keys[best]) >= 0.5*float64(len(found)-iteration) {
			example := examples[best]
			candidate.Facts["recovery"] = recoveryPhrase(best)
			candidate.Facts["recovery_change"] = recoveryChange(best, example)
			candidate.Facts["recovery_example"] = map[string]string{"failed": clipText(firstLine(example.failed), 160), "worked": clipText(firstLine(example.fixed), 160)}
			continue
		}
		if len(found) >= 5 && float64(iteration) >= 0.8*float64(len(found)) && knownFailure(candidate.Spec.Pattern) == nil {
			// Ordinary iteration: a different test target or script each time.
			candidate.Hidden = true
			candidate.Facts["iteration"] = true
		}
	}
	return nil
}

var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// commandRecovery compares a failed command with the working one. A change
// to flags, environment, or the steps around the command is a setup fix;
// a change to arguments or a heredoc body is iteration.
func commandRecovery(failed, fixed string) (recovery, bool) {
	if strings.TrimSpace(failed) == "" || strings.TrimSpace(fixed) == "" || failed == fixed {
		return recovery{}, false
	}
	result := recovery{failed: failed, fixed: fixed}
	before, after := parseShellCommand(failed), parseShellCommand(fixed)
	// Compare the failing command with the same program's segment in the
	// working one; any other segment there is a step added around it.
	program := before.primary().Program
	match := after.Primary
	for index, segment := range after.Segments {
		if segment.Program == program {
			match = index
			break
		}
	}
	after.Primary = match
	setup := []string{}
	segments := func(parsed shellCommand) map[string]bool {
		set := map[string]bool{}
		for index, segment := range parsed.Segments {
			if index != parsed.Primary && segment.Program != "" {
				set[strings.TrimSpace(segment.Program+" "+segment.Subcommand)] = true
			}
		}
		return set
	}
	oldSteps, newSteps := segments(before), segments(after)
	for step := range newSteps {
		if !oldSteps[step] && step != "cd" && step != "echo" {
			setup = append(setup, "+step:"+step)
		}
	}
	words := func(parsed shellCommand) []string { return shellWords(parsed.primary().Text) }
	oldWords, newWords := words(before), words(after)
	marks := func(values []string) map[string]bool {
		set := map[string]bool{}
		for _, value := range values {
			switch {
			case envAssignment.MatchString(value):
				set["env:"+value] = true
			case strings.HasPrefix(value, "-") && len(value) > 1:
				flag, _, _ := strings.Cut(value, "=")
				set["flag:"+flag] = true
			}
		}
		return set
	}
	oldMarks, newMarks := marks(oldWords), marks(newWords)
	for mark := range newMarks {
		if !oldMarks[mark] {
			setup = append(setup, "+"+mark)
		}
	}
	for mark := range oldMarks {
		if !newMarks[mark] {
			setup = append(setup, "-"+mark)
		}
	}
	if len(oldWords) > 1 && len(newWords) > 1 && oldWords[0] == newWords[0] && before.primary().Subcommand != after.primary().Subcommand {
		setup = append(setup, "+sub:"+after.primary().Subcommand)
	}
	sort.Strings(setup)
	if len(setup) == 0 {
		result.iteration = true
		return result, true
	}
	result.key = strings.Join(setup, " ")
	return result, true
}

// recoveryPhrase turns a recovery key into the words of an explanation.
func recoveryPhrase(key string) string {
	parts := []string{}
	for _, part := range strings.Fields(key) {
		switch {
		case strings.HasPrefix(part, "+env:"):
			parts = append(parts, "setting `"+strings.TrimPrefix(part, "+env:")+"`")
		case strings.HasPrefix(part, "+flag:"):
			parts = append(parts, "adding `"+strings.TrimPrefix(part, "+flag:")+"`")
		case strings.HasPrefix(part, "-flag:"):
			parts = append(parts, "dropping `"+strings.TrimPrefix(part, "-flag:")+"`")
		case strings.HasPrefix(part, "+step:"):
			parts = append(parts, "running `"+strings.TrimPrefix(part, "+step:")+"` first")
		case strings.HasPrefix(part, "+sub:"):
			parts = append(parts, "using `"+strings.TrimPrefix(part, "+sub:")+"` instead")
		case strings.HasPrefix(part, "-env:"):
			parts = append(parts, "unsetting `"+strings.TrimPrefix(part, "-env:")+"`")
		}
	}
	return joinWords(parts)
}

func recoveryChange(key string, example recovery) string {
	return "agents fixed it by " + recoveryPhrase(key) + " (`" + clipText(firstLine(example.fixed), 70) + "`)"
}

func joinWords(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
}
