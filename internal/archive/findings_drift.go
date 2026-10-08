package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// D9: instruction and skill format drift. Each harness reads its own files:
// Claude Code reads CLAUDE.md and .claude/skills, Codex reads AGENTS.md and
// its own skill locations, Antigravity reads GEMINI.md. A repository written
// for one harness gives nothing to the others, and nothing fails loudly: the
// agent searches for the file it expected, or reads the other harness's by
// hand. The metric is the share of a provider's conversations in a
// repository that hunt for instructions this way.

// harnessFiles are each provider's own instruction files and folders.
var harnessFiles = map[string][]string{
	"claude":      {"CLAUDE.md", ".claude/"},
	"codex":       {"AGENTS.md", ".codex/", ".agents/"},
	"antigravity": {"GEMINI.md", ".gemini/", ".agent/"},
}

var (
	instructionName = regexp.MustCompile(`(?:^|[/\s'"*=])((?:AGENTS|CLAUDE|GEMINI)(?:\.override)?\.md)\b`)
	skillFile       = regexp.MustCompile(`(?:^|[\s'"=/])\.(claude|codex|agents|gemini|agent)/skills/[^\s'"]*SKILL\.md`)
	// homeHarness matches a harness's own folder in a home directory: global
	// files and harness internals, not the repository's.
	homeHarness   = regexp.MustCompile(`(?:~|\$HOME|/Users/[^/\s]+|/home/[^/\s]+)/\.(?:claude|codex|agents|gemini)\b`)
	fileSearchers = map[string]bool{"find": true, "fd": true, "ls": true, "locate": true, "mdfind": true}
	fileReaders   = map[string]bool{"cat": true, "head": true, "tail": true, "sed": true, "less": true, "nl": true, "bat": true, "wc": true}
)

// instructionHunt classifies one call: whether a provider's agent searched
// for instruction files by name, or read another harness's instructions or
// skills by hand. Content searches (rg, grep) and files in home folders don't
// count: they are work about those files, or global files.
func instructionHunt(provider, category, toolName, program, command, filePath string) (kind, file string) {
	text := command
	if category == "read" {
		text = filePath
	}
	text = homeHarness.ReplaceAllString(text, "~")
	if strings.Contains(text, "~/") && !strings.Contains(strings.ReplaceAll(text, "~/", ""), ".md") {
		return "", ""
	}
	name := ""
	if match := instructionName.FindStringSubmatch(text); match != nil && !strings.Contains(text, "~/"+match[1]) {
		name = match[1]
	}
	skill := ""
	if match := skillFile.FindStringSubmatch(text); match != nil {
		skill = "." + match[1] + "/skills"
	}
	if name == "" && skill == "" {
		return "", ""
	}
	own := func(item string) bool {
		for _, candidate := range harnessFiles[provider] {
			if strings.HasPrefix(item, strings.TrimSuffix(candidate, "/")) {
				return true
			}
		}
		return false
	}
	searching := false
	reading := category == "read"
	switch {
	case category == "command" && (fileSearchers[program] || program == "rg" && strings.Contains(command, "--files") || program == "git" && strings.Contains(command, "ls-files")):
		searching = true
	case category == "command" && fileReaders[program]:
		reading = true
	case toolName == "Glob":
		searching = true
	}
	switch {
	case searching && name != "":
		return "search", name
	case reading && skill != "" && !own(skill):
		return "skill", skill
	case reading && name != "" && !own(name):
		return "read", name
	}
	return "", ""
}

func detectDrift(env *findingEnv) ([]*findingCandidate, error) {
	rows, err := env.featureRows("drift", `SELECT t.rowid feature_order,t.id,t.conversation_id,t.provider,t.tool_category,t.tool_name,COALESCE(t.program,'') program,COALESCE(t.command,'') command,
		COALESCE(t.file_path,'') file_path,t.started_at,t.result_tokens,t.carried_tokens,t.output_tokens,COALESCE(t.model,'') model,COALESCE(t.call_message_id,'') message_id
		FROM tool_calls t WHERE t.started_at>=? AND t.tool_category IN ('command','read','search')
		AND (t.command LIKE '%AGENTS%' OR t.command LIKE '%CLAUDE%' OR t.command LIKE '%GEMINI%' OR t.command LIKE '%SKILL.md%'
		OR t.file_path LIKE '%AGENTS%.md' OR t.file_path LIKE '%CLAUDE%.md' OR t.file_path LIKE '%GEMINI%.md' OR t.file_path LIKE '%SKILL.md') /* finding partition */ `, "t.conversation_id", env.fromUTC)
	if err != nil {
		return nil, err
	}
	type hunt struct {
		kind, file string
		row        map[string]any
	}
	hunts := map[string][]hunt{}
	for _, row := range rows {
		root := env.roots[firstString(row["conversation_id"])]
		if root == "" {
			continue
		}
		conv := env.convs[root]
		kind, file := firstString(row["feature_hunt_kind:"+conv.Provider]), firstString(row["feature_hunt_file:"+conv.Provider])
		if harnessFiles[conv.Provider] == nil {
			kind, file = firstString(row["feature_hunt_kind:"]), firstString(row["feature_hunt_file:"])
		}
		if !env.featureCache {
			kind, file = instructionHunt(conv.Provider, firstString(row["tool_category"]), firstString(row["tool_name"]), firstString(row["program"]), firstString(row["command"]), firstString(row["file_path"]))
		}
		if kind == "" {
			continue
		}
		hunts[root] = append(hunts[root], hunt{kind, file, row})
	}
	wanted := env.wants("drift")
	candidates := map[string]*findingCandidate{}
	candidate := func(repository, provider string) *findingCandidate {
		scope := "repo:" + repository
		pattern := "provider:" + provider
		key := scope + "\x1f" + pattern
		if candidates[key] == nil {
			candidates[key] = &findingCandidate{Spec: findingSpec{Detector: "drift", Scope: scope, Pattern: pattern, Params: map[string]string{"provider": provider}},
				RepositoryID: repository, Obs: map[string]*findingObs{}, Elsewhere: map[string]*findingObs{}, Facts: map[string]any{},
				Metric: findingMetric{Kind: "rate", Unit: "conversations"}}
		}
		return candidates[key]
	}
	counts := map[string]map[string]int{}
	for _, conversationID := range sortedFindingKeys(env.convs) {
		conv := env.convs[conversationID]
		if conv.RepositoryID == "" || harnessFiles[conv.Provider] == nil || conv.SourceKind == "tl1" {
			continue
		}
		key := "repo:" + conv.RepositoryID + "\x1fprovider:" + conv.Provider
		if !env.discover {
			if _, ok := wanted[key]; !ok {
				continue
			}
		}
		item := candidate(conv.RepositoryID, conv.Provider)
		obs := &findingObs{Unit: conv.ID, Conversation: conv.ID, Workspace: conv.WorkspaceID, Day: conv.Day, Provider: conv.Provider}
		item.Obs[conv.ID] = obs
		if counts[key] == nil {
			counts[key] = map[string]int{}
		}
		for _, found := range hunts[conv.ID] {
			obs.Hit = true
			obs.Occurrences++
			result, carried := integer(found.row["result_tokens"]), integer(found.row["carried_tokens"])
			output, _ := number(found.row["output_tokens"])
			obs.Tokens += result + carried
			cost := env.toolCallCost(conv.Provider, firstString(found.row["model"]), conv.Day, result, carried, output)
			obs.CostUSD += cost
			// Once the harness finds the file, the searches for it go. The
			// hand reads don't: the harness loads the same text itself, in
			// every conversation, so the fix saves no tokens on them.
			if found.kind == "search" {
				obs.Removable += cost
				obs.RemovableTokens += result + carried
			}
			at := firstString(found.row["started_at"])
			if at > obs.At {
				obs.At = at
			}
			if env.inGate(conv.Day) {
				counts[key][found.kind+"\x1f"+found.file]++
			}
			did := map[string]string{"search": "searched for " + found.file, "read": "read " + found.file + " by hand", "skill": "read a skill from " + found.file + " by hand"}[found.kind]
			item.addEvidence(findingHandle{At: at, ConversationID: conv.ID, WorkspaceID: conv.WorkspaceID, MessageID: firstString(found.row["message_id"]),
				ToolCallID: firstString(found.row["id"]), Where: env.evidenceWhere(conv) + " · " + providerLabel(conv.Provider), Did: did,
				Happened: "`" + clipText(firstLine(defaultString(firstString(found.row["command"]), firstString(found.row["file_path"]))), 80) + "`"})
		}
	}
	// The other providers in the same repository are the built-in comparison.
	for _, item := range candidates {
		for _, other := range candidates {
			if other != item && other.RepositoryID == item.RepositoryID {
				for id, obs := range other.Obs {
					item.Elsewhere[id] = obs
				}
			}
		}
	}
	inventories := newInstructionInventories()
	output := []*findingCandidate{}
	// The loaded-instructions record, where the harness writes one, says
	// what each provider actually loaded (G7, instructions.go).
	coverage := map[string]map[string]instructionUse{}
	for _, item := range candidates {
		if coverage[item.RepositoryID] != nil {
			continue
		}
		coverage[item.RepositoryID] = map[string]instructionUse{}
		uses, err := env.catalog.instructionCoverage(env.ctx, env.db, item.RepositoryID, formatTime(dayTime(env.gateFrom)))
		if err != nil {
			return nil, err
		}
		for _, use := range uses {
			coverage[item.RepositoryID][use.Provider] = use
		}
	}
	for key, item := range candidates {
		provider := item.Spec.Params["provider"]
		files := inventories.current(env, item.RepositoryID)
		describeDrift(env, item, provider, counts[key], files, coverage[item.RepositoryID][provider])
		output = append(output, item)
	}
	sort.Slice(output, func(i, j int) bool { return output[i].Spec.id() < output[j].Spec.id() })
	return output, nil
}

// describeDrift writes a drift finding: what the provider looked for, and
// which harness's files the repository has.
func describeDrift(env *findingEnv, candidate *findingCandidate, provider string, counts map[string]int, files map[string]int64, loaded instructionUse) {
	repository := env.repositories[candidate.RepositoryID]
	searched, readOther, skills := 0, 0, 0
	searchedFor, readFile := "", ""
	for key, count := range counts {
		kind, file, _ := strings.Cut(key, "\x1f")
		switch kind {
		case "search":
			searched += count
			if searchedFor == "" || count > counts["search\x1f"+searchedFor] {
				searchedFor = file
			}
		case "read":
			readOther += count
			if readFile == "" || count > counts["read\x1f"+readFile] {
				readFile = file
			}
		case "skill":
			skills += count
		}
	}
	native := harnessFiles[provider][0]
	has := func(name string) bool { _, ok := files[name]; return ok }
	present := []string{}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "GEMINI.md"} {
		if has(name) {
			present = append(present, name)
		}
	}
	candidate.Lever = "repo-tooling"
	candidate.Facts = map[string]any{"provider": provider, "native_file": native, "files_on_default_branch": sortedFindingKeys(files),
		"searches": searched, "hand_reads": readOther, "skill_reads": skills, "searched_for": nilIfEmpty(searchedFor), "read_by_hand": nilIfEmpty(readFile),
		"loaded_instructions": map[string]any{"conversations": loaded.Conversations, "recorded": loaded.Recorded, "loaded_project_file": loaded.ProjectLoaded, "files": loaded.Files}}
	steps := []findingStep{
		{Lever: "repo-tooling", Label: "One shared instructions file", Change: fmt.Sprintf("Make one instructions file that every harness here reads (%s), with a symlink, an import line, or the harness's fallback-filename setting, whichever the harness versions in use support. Do the same for skills, and keep the shared file short.", strings.Join(present, " and "))},
		{Lever: "harness-settings", Label: "A fallback setting", Change: fmt.Sprintf("Point %s at the existing file in its own settings (for Codex, `project_doc_fallback_filenames` in `.codex/config.toml`), and check the result in a fresh session.", providerLabel(provider))},
		{Lever: "repo-instructions", Label: "A pointer in " + native, Change: fmt.Sprintf("Add a short %s that tells %s where the instructions and skills are.", native, providerLabel(provider))},
	}
	if len(present) == 0 {
		steps[0].Change = fmt.Sprintf("Write a short %s for this repository from what agents keep looking up, and make it the one file every harness here reads.", native)
		steps[0].Label = "A shared instructions file"
	}
	actions := []string{}
	if searched > 0 {
		actions = append(actions, "searching for "+defaultString(searchedFor, native))
	}
	if readOther > 0 {
		actions = append(actions, "reading "+defaultString(readFile, "another harness's instructions")+" by hand")
	}
	if skills > 0 {
		actions = append(actions, "reading another harness's skills by hand")
	}
	candidate.write = func(candidate *findingCandidate, stats findingStats) findingCard {
		title := fmt.Sprintf("%s can't find %s's instructions", providerLabel(provider), repository)
		why := ""
		switch {
		case len(present) > 0 && !has(native):
			why = fmt.Sprintf("%s keeps its agent instructions in %s, which %s doesn't read. ", repository, strings.Join(present, " and "), providerLabel(provider))
		case len(present) == 0 && len(files) > 0:
			why = fmt.Sprintf("%s has no agent instructions file. ", repository)
		}
		explanation := fmt.Sprintf("%s%s looked for them in %s this month, %s.", why, providerLabel(provider), fractionPhrase(stats.Rate, "conversations"), joinWords(actions))
		// Where the harness records what it loaded, say plainly that it
		// loaded nothing here.
		if loaded.Recorded >= 10 && loaded.ProjectLoaded == 0 {
			explanation += fmt.Sprintf(" None of its %s here loaded a project instructions file.", countNoun(int(loaded.Recorded), "conversation"))
		}
		return findingCard{Title: title, Explanation: explanation, ImpactNote: countNoun(searched+readOther+skills, "lookup"), Steps: steps,
			ChartTitle: providerLabel(provider) + " conversations that looked for instructions", Rate: fractionPhrase(stats.Rate, "")}
	}
}

// instructionInventories reads which instruction files a repository's
// default branch tracks, from its local clone. Results are cached per
// commit for the life of the process.
type instructionInventories struct {
	mu    sync.Mutex
	cache map[string]map[string]int64
}

var sharedInstructionInventories = &instructionInventories{cache: map[string]map[string]int64{}}

func newInstructionInventories() *instructionInventories { return sharedInstructionInventories }

// current lists the instruction files on the default branch now.
func (inventories *instructionInventories) current(env *findingEnv, repository string) map[string]int64 {
	files, _ := inventories.at(env.locations[repository], time.Time{})
	return files
}

// at lists instruction files and their sizes on the default branch as of
// a time (zero: now). ok is false when no local clone could answer.
func (inventories *instructionInventories) at(locations []string, when time.Time) (map[string]int64, bool) {
	clone := repositoryClone(locations)
	if clone == "" {
		return map[string]int64{}, false
	}
	ref := defaultBranchRef(clone)
	if ref == "" {
		return map[string]int64{}, false
	}
	args := []string{"rev-list", "-1", ref}
	if !when.IsZero() {
		args = []string{"rev-list", "-1", "--before=" + when.UTC().Format(time.RFC3339), ref}
	}
	commit, err := gitOutput(clone, args...)
	if err != nil || commit == "" {
		return map[string]int64{}, false
	}
	inventories.mu.Lock()
	if files, ok := inventories.cache[clone+"@"+commit]; ok {
		inventories.mu.Unlock()
		return files, true
	}
	inventories.mu.Unlock()
	listing, err := gitOutput(clone, "ls-tree", "-r", "-l", commit, "--", "AGENTS.md", "CLAUDE.md", "GEMINI.md", ".claude", ".codex", ".agents", ".gemini")
	if err != nil {
		return map[string]int64{}, false
	}
	files := map[string]int64{}
	for _, line := range strings.Split(listing, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) < 4 {
			continue
		}
		size, _ := strconv.ParseInt(fields[3], 10, 64)
		switch {
		case path == "AGENTS.md" || path == "CLAUDE.md" || path == "GEMINI.md" || strings.HasSuffix(path, "/SKILL.md") ||
			path == ".codex/config.toml" || path == ".claude/CLAUDE.md":
			files[path] = size
		}
	}
	inventories.mu.Lock()
	inventories.cache[clone+"@"+commit] = files
	inventories.mu.Unlock()
	return files, true
}

// repositoryClone picks a repository's main clone on this Mac: an existing
// checkout whose .git is a folder (not a worktree), else any checkout.
func repositoryClone(locations []string) string {
	fallback := ""
	sorted := append([]string{}, locations...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) < len(sorted[j]) })
	for _, location := range sorted {
		info, err := os.Stat(filepath.Join(location, ".git"))
		if err != nil {
			continue
		}
		if info.IsDir() {
			return location
		}
		if fallback == "" {
			fallback = location
		}
	}
	return fallback
}

// defaultBranchRef is the clone's remote default branch, or a local main.
func defaultBranchRef(clone string) string {
	if ref, err := gitOutput(clone, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return ref
	}
	for _, ref := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := gitOutput(clone, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref
		}
	}
	return ""
}
