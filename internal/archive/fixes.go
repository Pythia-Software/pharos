package archive

// Optimize → Fixes: recurring problems agents run into that the person can
// prevent with a change to guidance, the environment, a tool, setup or docs.
// A background projection (refreshFixes, fixes_store.go) groups agent events
// (fixes_grouping.go) and writes one fix_items row per problem; requests only
// read it.
//
// Scope: events about the person's own request (unclear instructions,
// assumptions, disagreeing, and confusion or mistakes that refer to the
// request) are left out before grouping: they are not the setup's to fix.
// Getting around a safeguard (circumventing_constraint) belongs to Optimize →
// Risky actions (safety_safeguards.go); a fix whose conversations also have
// such events links there instead of repeating them.
//
// Where a fix belongs is a rule of thumb on the group's spread, never an input
// to grouping: two or more repositories with none above 85% → the global
// setup; otherwise the repository with most of it.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const fixesVersion = "fixes-v2"

// fixesWindowDays bounds the events the projection reads.
const fixesWindowDays = 90

// fixesPass is one grouping pass: the kinds it groups and how many groups it
// keeps. Human-side kinds never feed one.
type fixesPass struct {
	Source string
	Kinds  map[string]bool
	Limit  int
}

var fixesPasses = []fixesPass{
	{"friction", map[string]bool{"obstacle": true, "env_friction": true, "preexisting_failure": true, "missing_capability": true, "stuck": true, "workaround": true, "gave_up": true}, 60},
	{"learning", map[string]bool{"discovery": true, "unblocking_insight": true}, 60},
	{"confusion", map[string]bool{"confusion": true, "surprise": true}, 30},
	{"stale", map[string]bool{"stale_guidance": true}, 30},
}

var fixesFriction = map[string]bool{"obstacle": true, "env_friction": true, "preexisting_failure": true, "missing_capability": true, "stuck": true}

// fixesRequestKinds are about the person's request, never the setup.
var fixesRequestKinds = map[string]bool{"ambiguous_request": true, "pushback": true, "assumption": true}

// fixesRequestRef: an assumption, confusion or own mistake that refers to the
// request (the user, what was asked, what it means) is about the request.
var fixesRequestRef = regexp.MustCompile(`(?i)\b(user|you|your|asked|ask|requested|request|instructions?|brief|prompt|meant|means|meaning|wants?|wanted|intended|intent|spec|wording|said|says|phrase|interpret\w*)\b`)

// fixesAboutRequest says whether an event is about the request rather than
// something the setup could fix.
func fixesAboutRequest(family, sentence string) bool {
	if family == "ambiguous_request" || family == "pushback" {
		return true
	}
	return (family == "assumption" || family == "confusion" || family == "own_mistake") && fixesRequestRef.MatchString(sentence)
}

// fixesCategories are the kinds of fix: a label and what it means.
var fixesCategories = []struct{ Key, Label, Explanation string }{
	{"env", "Environment or setup", "Something agents need is missing, too slow or the wrong version where they run."},
	{"harness", "Harness and shell behaviour", "The agent's shell, sandbox, tools or workspace manager behave in a way agents keep rediscovering."},
	{"tool", "Tool or integration", "A CLI, integration or account keeps failing the same way."},
	{"flaky", "Known failing check", "A test or check fails for reasons unrelated to the work, so every agent re-investigates it."},
	{"knowledge", "Codebase knowledge", "A fact about the code that agents keep working out again."},
	{"stale", "Out-of-date docs", "A doc or instruction says something the code or setup no longer does."},
	{"obstacle", "Recurring obstacle", "The same step keeps getting blocked; the cause isn't clear from the words alone."},
}

func fixesCategory(key string) (label, explanation string) {
	for _, category := range fixesCategories {
		if category.Key == key {
			return category.Label, category.Explanation
		}
	}
	return key, ""
}

// Word patterns that suggest a kind of fix.
var fixesCategoryTerms = map[string]*regexp.Regexp{
	"harness": regexp.MustCompile(`(?i)\bzsh\b|\bbash\b|\bshell\b|\bglob|heredoc|unquoted|quoting|foreground|background (task|command|job|bash)|\bsleep\b|` +
		`\bpgrep\b|\bps\b|write tool|edit tool|read tool|\bsubagent|conductor|worktree|sandbox|read-only|readonly|` +
		`not permitted|denied|approval|blocked by|harness|ending (my|the) turn|branch (was )?renamed|renamed the branch|` +
		`\$pwd|working directory|moved to the background`),
	"tool": regexp.MustCompile(`(?i)\bgh\b|github|\bpr (edit|view|create)|graphql|projects \(classic\)|connector|integration|\bcli\b|` +
		`\bgcloud\b|\bcodex\b|chatgpt|\baccount\b|\bauth\w*|\brest api\b|\bmcp server`),
	"env": regexp.MustCompile(`(?i)not installed|isn.t installed|unavailable|not available|command not found|no module|missing|\bversion\b|` +
		`\bnode\b|\bnvm\b|\bpython\d?\b|\bpip\b|\bnpm\b|\bbrew\b|\bpath\b|dependency|load average|heavily loaded|congest|` +
		`\b\d+[- ]?(minute|second)|time limit|timeout|timed out|disk|memory|cores|amd64|arm64|toolchain`),
	"flaky": regexp.MustCompile(`(?i)flaky|pre-?existing|unrelated (to|test|failure)|passed on rerun|passes on rerun|same \d+ failures|` +
		`(my|this|the) changes? (don.t|doesn.t|didn.t|do not|does not|did not) touch|test (that )?my changes? (don.t|doesn.t|didn.t) touch|` +
		`unrelated (\S+ ){1,3}(test|failure|check|assertion)|reproduces (on|with) (the )?(original|master|main)|previously reproduced|` +
		`already fail|base commit|untouched checkout|not something i introduced|on master too|also fails on|fails (the same way )?on (a )?(clean )?(origin/)?(master|main)|` +
		`outside (the|my) (change|changes|ui|code|work)\b|(isn.t|is not|wasn.t|not) (related|caused) (to|by) (my|this)`),
}

var (
	fixesExtensions = `(?:md|go|ts|tsx|js|mjs|cjs|py|json|toml|yaml|yml|sh|swift|sql|txt|csv|css|html|rs|lock|xlsx)`
	fixesBackticked = regexp.MustCompile("`([^`\\s]{2,80}\\." + fixesExtensions + "|[a-z][\\w-]*(?: [\\w./=*-]+){0,4})`")
	fixesBarePath   = regexp.MustCompile(`((?:[\w.-]+/)*[\w-][\w.-]*\.` + fixesExtensions + `)\b`)
	fixesExtension  = regexp.MustCompile(`\.` + fixesExtensions + `$`)
	fixesNumeric    = regexp.MustCompile(`^[\d.]+$`)
	fixesDocFile    = regexp.MustCompile(`(?i)\b((?:[\w.-]+/)*[\w.-]*(?:CLAUDE|AGENTS|HANDOFF|README|CHECKPOINT|PLAN|BRIEF|STATUS|NOTES|TODO|MEMORY|SKILL|REPORT)[\w.-]*(?:\.md)?|[\w./-]+\.md)\b`)
	fixesPlainWord  = regexp.MustCompile(`^[A-Z]?[a-z]+$`)
	fixesListMarker = regexp.MustCompile(`^\s*(?:[-*•]\s+|\d+[.)]\s+|#+\s+|\*\*|>\s*)+`)
)

// fixesSetupFiles are the files a repository's setup usually lives in.
var fixesSetupFiles = []string{"tools/setup.sh", "scripts/setup.sh", "setup.sh", "bin/setup", "script/setup", "Makefile", ".nvmrc", "pyproject.toml",
	"requirements.txt", "package.json", "Dockerfile", "conductor.json", ".conductor/settings.toml"}

// fixesEvent is one agent event in scope, with its work item and repository.
type fixesEvent struct {
	ID                                              int64
	MessageID, ConversationID, Workspace            string
	Repository, RepositoryName, Model, Provider     string
	Family, Source, Sentence, Subject, Response, At string
	Day                                             string
	SentenceStart                                   int
	Probability                                     float64
}

func (e fixesEvent) hasSubject() bool { return e.Subject != "" && e.Subject != e.Sentence }

// text is what grouping compares: the sentence, and what it was about.
func (e fixesEvent) text() string {
	if e.hasSubject() {
		return e.Sentence + " " + e.Subject
	}
	return e.Sentence + " "
}

func (e fixesEvent) repositoryLabel() string {
	if e.Repository == "" {
		return "No repository"
	}
	return defaultString(nilIfEmpty(e.RepositoryName), e.Repository)
}

// fixesGuide is one current guidance file.
type fixesGuide struct {
	Name  string
	Lines []string
}

// fixesCommit is a change to a repository's guidance files.
type fixesCommit struct {
	Repository, SHA, Day, Subject string
}

// fixesRepository is what Pharos knows of a repository's local clone.
type fixesRepository struct {
	Name    string
	Guides  []fixesGuide
	Setup   []string
	Commits []fixesCommit
	// Added lists the lines a guidance commit added.
	Added func(sha string) []string
}

func (r *fixesRepository) guide() string {
	if r != nil && len(r.Guides) > 0 {
		return r.Guides[0].Name
	}
	return "CLAUDE.md (none yet)"
}

// fixesMoment is one timestamped step of a conversation: a message (prompt
// when it starts a turn) or a tool call.
type fixesMoment struct {
	At, Message, Program, Error string
	Prompt, Call, Failed        bool
}

// fixesInput is everything the analysis reads.
type fixesInput struct {
	Events []fixesEvent
	// Reads counts messages the events pass read, by repository and day: the
	// denominator for whether a problem has gone quiet.
	Reads        map[[2]string]int
	End          string // the latest day the pass read
	Repositories map[string]*fixesRepository
	Global       []fixesGuide
	// Timelines loads conversations' messages and tool calls in order.
	Timelines func(conversations []string) (map[string][]fixesMoment, error)
}

type fixCount struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

type fixQuote struct {
	EventID        string  `json:"event_id"`
	Sentence       string  `json:"sentence"`
	About          string  `json:"about,omitempty"`
	Kind           string  `json:"kind"`
	KindLabel      string  `json:"kind_label"`
	Repository     string  `json:"repository"`
	Model          string  `json:"model"`
	Day            string  `json:"day"`
	At             string  `json:"at,omitempty"`
	Source         string  `json:"source"`
	WorkspaceID    string  `json:"workspace_id"`
	ConversationID string  `json:"conversation_id"`
	MessageID      string  `json:"message_id"`
	Similarity     float64 `json:"similarity,omitempty"`
}

type fixGuidanceHit struct {
	Repository string `json:"repository"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Term       string `json:"term"`
	Text       string `json:"text"`
}

type fixCost struct {
	Episodes   int     `json:"episodes"`
	Hours      float64 `json:"hours"`
	Calls      int     `json:"calls"`
	Unresolved int     `json:"unresolved"`
}

type fixCommitNote struct {
	Repository string `json:"repository"`
	SHA        string `json:"sha"`
	Day        string `json:"day"`
	Subject    string `json:"subject"`
	Term       string `json:"term"`
	Line       string `json:"line"`
	After      int    `json:"events_after,omitempty"`
}

type fixStatus struct {
	State         string          `json:"state"` // recurring, quiet, quiet-fixed, unclear, unknown
	Label         string          `json:"label"`
	Detail        string          `json:"detail"`
	First         string          `json:"first,omitempty"`
	Last          string          `json:"last,omitempty"`
	DaysQuiet     int             `json:"days_quiet"`
	Expected      *float64        `json:"expected,omitempty"`
	MessagesAfter int             `json:"messages_after"`
	CommitsSince  []fixCommitNote `json:"commits_since,omitempty"`
	Persisted     *fixCommitNote  `json:"persisted,omitempty"`
}

// fixItem is one recurring problem, ready to show.
type fixItem struct {
	ID            string           `json:"id"`
	Source        string           `json:"source"`
	Category      string           `json:"category"`
	CategoryLabel string           `json:"category_label"`
	Title         string           `json:"title"`
	Terms         []string         `json:"terms"`
	Files         []string         `json:"files"`
	Events        int              `json:"events"`
	WorkItems     int              `json:"work_items"`
	Conversations int              `json:"conversations"`
	Thinking      int              `json:"thinking"`
	Repositories  []fixCount       `json:"repositories"`
	Models        []fixCount       `json:"models"`
	Kinds         []fixCount       `json:"kinds"`
	Scope         string           `json:"scope"`
	ScopeReason   string           `json:"scope_reason"`
	Target        string           `json:"target"`
	TargetLabel   string           `json:"target_label"`
	DestKind      string           `json:"destination_kind"`
	DestWhere     string           `json:"destination"`
	Suggestion    string           `json:"suggestion"`
	GuidanceHits  []fixGuidanceHit `json:"guidance_hits"`
	ToolErrors    []fixCount       `json:"tool_errors"`
	Quotes        []fixQuote       `json:"quotes"`
	Members       []fixQuote       `json:"members"`
	MemberCount   int              `json:"member_count"`
	Groups        int              `json:"groups"`
	Cost          fixCost          `json:"cost"`
	Status        fixStatus        `json:"status"`
	Tentative     bool             `json:"tentative"`
	// Lead is the member that best states the problem on its own; the
	// title is written from the group, the lead quotes it.
	Lead string `json:"lead"`
	// Check names the failing test or check a known-failing item is about.
	Check string `json:"check,omitempty"`
	// Intended marks a problem the guidance causes on purpose: its agents
	// cite the rule they are following, so there is nothing to fix.
	Intended       bool   `json:"intended"`
	IntendedReason string `json:"intended_reason,omitempty"`
	// Safeguards counts events in these conversations where an agent got
	// around a safeguard; they are Risky actions' to show.
	Safeguards int `json:"safeguards"`

	anchor      string
	anchorShare float64
	events      []fixesEvent
}

// fixesMembersShown bounds the members an item keeps for its detail.
const fixesMembersShown = 40

// fixesGroup is a recurring group of one pass, before it becomes an item.
type fixesGroup struct {
	source      string
	events      []fixesEvent // members, most confident first
	similarity  map[int64]float64
	leader      fixesEvent
	anchor      string
	anchorShare float64
	anchors     []string
	parts       int
}

// fixesRecurring groups the events of one pass and keeps groups that recur
// across work items and hold together.
func fixesRecurring(events []fixesEvent, pass fixesPass, docs map[int64]fixesTerms) []fixesGroup {
	items := []fixesEvent{}
	for _, e := range events {
		if pass.Kinds[e.Family] {
			items = append(items, e)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Probability != items[j].Probability {
			return items[i].Probability > items[j].Probability
		}
		return items[i].ID < items[j].ID
	})
	terms := make([]fixesTerms, len(items))
	for i, e := range items {
		terms[i] = docs[e.ID]
	}
	clustering := fixesClusterTexts(terms)
	groups := []fixesGroup{}
	for _, cluster := range clustering.groups {
		work := map[string]bool{}
		for _, i := range cluster.members {
			work[items[i].Workspace] = true
		}
		if len(work) < fixesMinWork {
			continue
		}
		mean, anchor, share := clustering.cohesion(cluster)
		if share < fixesAnchorShare || mean < fixesCohesion {
			continue
		}
		group := fixesGroup{source: pass.Source, similarity: map[int64]float64{}, anchor: anchor, anchorShare: share, parts: len(cluster.parts)}
		// The head is the largest pass-1 part; its leader is its most
		// confident member.
		largest := cluster.parts[0]
		for _, part := range cluster.parts {
			if len(part.members) > len(largest.members) {
				largest = part
			}
		}
		group.leader = items[largest.leader]
		anchors := map[string]int{}
		order := []string{}
		for _, i := range cluster.members {
			e := items[i]
			group.events = append(group.events, e)
			group.similarity[e.ID] = clustering.similarity(cluster, i)
			seen := map[string]bool{}
			for _, a := range fixesAnchors(e.text()) {
				if !seen[a] {
					seen[a] = true
					if anchors[a] == 0 {
						order = append(order, a)
					}
					anchors[a]++
				}
			}
		}
		sort.SliceStable(order, func(i, j int) bool { return anchors[order[i]] > anchors[order[j]] })
		for i, a := range order {
			if i >= 3 {
				break
			}
			if anchors[a] >= 2 {
				group.anchors = append(group.anchors, a)
			}
		}
		sort.SliceStable(group.events, func(i, j int) bool { return group.events[i].Probability > group.events[j].Probability })
		groups = append(groups, group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		wi, wj := fixesWorkItems(groups[i].events), fixesWorkItems(groups[j].events)
		if wi != wj {
			return wi > wj
		}
		return len(groups[i].events) > len(groups[j].events)
	})
	if len(groups) > pass.Limit {
		groups = groups[:pass.Limit]
	}
	return groups
}

func fixesWorkItems(events []fixesEvent) int {
	work := map[string]bool{}
	for _, e := range events {
		work[e.Workspace] = true
	}
	return len(work)
}

// fixesCodeUse says how often each word is written as code, and at all: a
// word is code-like when most of its uses are inside code, so one `fields` in
// backticks doesn't make "fields" specific everywhere.
type fixesCodeUse struct{ code, all map[string]int }

func (use fixesCodeUse) codeLike(word string) bool {
	return use.all[word] > 0 && float64(use.code[word]) >= 0.5*float64(use.all[word])
}

// fixesKeyTermsOf are the terms a card names: code-like spans most members
// share, and the group's anchor when it is code.
func fixesKeyTermsOf(group fixesGroup, use fixesCodeUse) []string {
	terms := []string{}
	for _, a := range group.anchors {
		bare := strings.Trim(a, "`")
		// A plain word counts when agents mostly write it as code (`trimesh`),
		// not when one of them happened to (`fields`).
		plain := fixesPlainWord.MatchString(bare) && !use.codeLike(strings.ToLower(bare))
		if len(bare) > 3 && len(bare) < 40 && len(strings.Fields(a)) <= 3 && !plain && strings.ToUpper(bare) != "CLAUDE.MD" && strings.ToUpper(bare) != "AGENTS.MD" {
			terms = append(terms, bare)
		}
	}
	a := group.anchor
	if a != "" && !strings.Contains(a, " ") && use.codeLike(a) && strings.ContainsFunc(a, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') }) && fixesASCII(a) && !containsString(terms, a) {
		terms = append(terms, a)
	}
	if len(terms) > 4 {
		terms = terms[:4]
	}
	return terms
}

func fixesASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return false
		}
	}
	return true
}

func fixesShare(events []fixesEvent, pattern *regexp.Regexp) float64 {
	if len(events) == 0 {
		return 0
	}
	n := 0
	for _, e := range events {
		about := ""
		if e.hasSubject() {
			about = e.Subject
		}
		if pattern.MatchString(e.Sentence + " " + about) {
			n++
		}
	}
	return float64(n) / float64(len(events))
}

// fixesCategorize decides the kind of fix from the members' kinds and words.
func fixesCategorize(source string, events []fixesEvent) string {
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Family]++
	}
	total := float64(max(len(events), 1))
	if source == "stale" || float64(kinds["stale_guidance"])/total >= 0.5 {
		return "stale"
	}
	shares := map[string]float64{}
	for name, pattern := range fixesCategoryTerms {
		shares[name] = fixesShare(events, pattern)
	}
	if float64(kinds["preexisting_failure"])/total >= 0.3 || shares["flaky"] >= 0.25 {
		return "flaky"
	}
	// The strongest of tool / harness / environment wording wins when common
	// enough; lessons and confusion need more, since they often name a tool in
	// passing while being about the code.
	lift := 0.0
	if source == "learning" || source == "confusion" {
		lift = 0.2
	}
	floors := map[string]float64{"tool": 0.35 + lift, "harness": 0.3 + lift, "env": 0.4 + lift}
	if envish := float64(kinds["env_friction"]+kinds["missing_capability"]) / total; envish >= 0.35 && lift == 0 {
		shares["env"] = math.Max(shares["env"], floors["env"])
	}
	best, bestShare := "", -1.0
	for _, name := range []string{"tool", "harness", "env"} {
		if shares[name] >= floors[name] && shares[name] > bestShare {
			best, bestShare = name, shares[name]
		}
	}
	if best != "" {
		return best
	}
	if lift > 0 {
		return "knowledge"
	}
	return "obstacle"
}

// fixesScope is where a fix probably belongs, from the repositories its
// events are in: the global setup, or one repository.
func fixesScope(events []fixesEvent) (scope, repository, reason string) {
	counts := map[string]int{}
	labels := map[string]string{}
	for _, e := range events {
		if e.Repository != "" {
			counts[e.Repository]++
			labels[e.Repository] = e.repositoryLabel()
		}
	}
	ranked := fixesRank(counts, labels)
	total := 0
	for _, entry := range ranked {
		total += entry.Count
	}
	if total == 0 {
		return "global", "", "No event is in a repository (home or scratch folders), so it isn't any one repository's to fix."
	}
	top := ranked[0]
	if len(ranked) >= 2 && float64(top.Count)/float64(total) < 0.85 {
		parts := []string{}
		for i, entry := range ranked {
			if i >= 4 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %d", entry.Label, entry.Count))
		}
		outside := ""
		if n := len(events) - total; n > 0 {
			outside = fmt.Sprintf(", and %d outside any repository", n)
		}
		return "global", "", fmt.Sprintf("Hit in %d repositories (%s%s), so the cause is probably shared: the machine, the harness or a common tool.", len(ranked), strings.Join(parts, ", "), outside)
	}
	parts := []string{}
	if total > top.Count {
		parts = append(parts, fmt.Sprintf("%d in other repositories", total-top.Count))
	}
	if outside := len(events) - total; outside > 0 {
		parts = append(parts, fmt.Sprintf("%d outside any repository", outside))
	}
	rest := ""
	if len(parts) > 0 {
		rest = " (" + strings.Join(parts, ", ") + ")"
	}
	return "repo", top.Key, fmt.Sprintf("%d of its %d events are in %s%s, so the fix probably lives there.", top.Count, len(events), top.Label, rest)
}

// fixesRank orders counts, most first, then by label.
func fixesRank(counts map[string]int, labels map[string]string) []fixCount {
	out := []fixCount{}
	for key, n := range counts {
		out = append(out, fixCount{Key: key, Label: labels[key], Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// fixesGlobalFile is the global guidance file for the members' usual harness.
func fixesGlobalFile(events []fixesEvent, global []fixesGuide) string {
	providers := map[string]int{}
	for _, e := range events {
		providers[e.Provider]++
	}
	providers["antigravity"] += providers["gemini"]
	// Registry order breaks ties, so Claude Code wins one.
	provider, best := "claude", 0
	for _, spec := range providerRegistry {
		if spec.Instructions != "" && providers[spec.Kind] > best {
			provider, best = spec.Kind, providers[spec.Kind]
		}
	}
	file := strings.Trim(globalInstructionFile(provider), "`")
	if !instructionPath(file) {
		return file // kept in the app's settings, which Pharos cannot read
	}
	for _, guide := range global {
		if (guide.Name == file || strings.HasSuffix(file, "/") && strings.HasPrefix(guide.Name, file)) && !fixesBlank(guide.Lines) {
			return file
		}
	}
	return file + " (empty today)"
}

func fixesBlank(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

// fixesDestination says where to change, and what kind of change.
func fixesDestination(category, scope, label string, repository *fixesRepository, files []string, global string) (kind, where string) {
	guide := repository.guide()
	setup := "no setup script found"
	if repository != nil && len(repository.Setup) > 0 {
		setup = strings.Join(repository.Setup[:min(3, len(repository.Setup))], ", ")
	}
	doc, code := "", ""
	for _, file := range files {
		upper := strings.ToUpper(file)
		if doc == "" && strings.HasSuffix(strings.ToLower(file), ".md") && !strings.HasPrefix(upper, "CLAUDE") && !strings.HasPrefix(upper, "AGENTS") {
			doc = file
		}
		if code == "" && !strings.HasSuffix(strings.ToLower(file), ".md") {
			code = file
		}
	}
	if scope == "global" {
		switch category {
		case "env":
			return "Environment", "Machine setup: shell profile, version manager default, installed tools, sandbox limits"
		case "harness":
			return "Global guidance", "Global agent guidance: " + global + "; or the harness's own settings (sandbox, workspace manager)"
		case "tool":
			return "Shared tool", "A shared tool or wrapper on PATH, with a line in " + global
		case "flaky":
			return "Tests", "The shared test tooling, or each repository's list of known failures"
		case "knowledge":
			return "Global guidance", "Global agent guidance: " + global
		case "stale":
			return "Docs", "The doc each event names"
		}
		return "Investigate", "Investigate first; then the machine setup or " + global
	}
	switch category {
	case "env":
		return "Environment / setup", fmt.Sprintf("%s's setup (%s), plus a line in %s/%s on what to do when it's missing", label, setup, label, guide)
	case "harness":
		return "Repo guidance", label + "/" + guide
	case "tool":
		return "Repo tooling", fmt.Sprintf("%s's scripts, or a line in %s/%s", label, label, guide)
	case "flaky":
		where := "The failing test itself"
		if code != "" {
			where += " (" + code + ")"
		}
		return "Tests", fmt.Sprintf("%s; until it's fixed, a known-failures note in %s/%s", where, label, guide)
	case "knowledge":
		where := label + "/" + guide
		if code != "" {
			where += ", or the doc beside " + code
		}
		if doc != "" {
			where += " (" + doc + ")"
		}
		return "Repo guidance or docs", where
	case "stale":
		target := doc
		if target == "" && len(files) > 0 {
			target = files[0]
		}
		if target == "" {
			target = guide
		}
		return "Docs", label + "/" + target
	}
	return "Investigate", fmt.Sprintf("Investigate in %s; then its setup or %s/%s", label, label, guide)
}

// fixesSuggestion is a template per kind of fix, filled from the evidence.
func fixesSuggestion(category, key, lead string) string {
	k := "it"
	if key != "" {
		k = "`" + key + "`"
	}
	switch category {
	case "env":
		return fmt.Sprintf("Make what agents needed (%s) available where they run, at the right version and fast enough, so it works without their help; then say in one line what to do when it doesn't.", k)
	case "harness":
		return fmt.Sprintf("Add one short rule with the behaviour and the command that works. Start from the agents' own words: “%s”", lead)
	case "tool":
		return fmt.Sprintf("Replace or wrap the failing call (%s) with one that works, and name it in the guidance so agents use it first.", k)
	case "flaky":
		return fmt.Sprintf("Fix or quarantine the failing check (%s). Until then, list it as a known failure with how to tell it apart from a real one.", k)
	case "knowledge":
		return fmt.Sprintf("Write the fact down where agents look first. Start from: “%s”", lead)
	case "stale":
		return fmt.Sprintf("Correct the doc: agents found it says something the code no longer does. Start from: “%s”", lead)
	}
	return fmt.Sprintf("Reproduce the blocked step and remove the cause, or document the workaround agents settled on. Start from: “%s”", lead)
}

// fixesTermPattern matches a term not inside a longer word, path or flag.
func fixesTermIndex(line, term string) bool {
	lower, t := strings.ToLower(line), strings.ToLower(term)
	for from := 0; ; {
		i := strings.Index(lower[from:], t)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(t)
		before := i == 0 || !(fixesWordByte(lower[i-1]) || lower[i-1] == '-')
		after := end == len(lower) || !(fixesWordByte(lower[end]) || lower[end] == '-')
		if before && after {
			return true
		}
		from = i + 1
	}
}

// fixesMentions are lines of the current guidance (each repository's and the
// global files) that name one of the item's key terms. A term on many lines
// is the file's topic, not this note, so it doesn't count.
func fixesMentions(input fixesInput, repositories []string, terms []string) []fixGuidanceHit {
	hits := []fixGuidanceHit{}
	seen := map[string]bool{}
	type source struct {
		repository string
		guides     []fixesGuide
	}
	sources := []source{}
	for _, id := range repositories {
		if repository := input.Repositories[id]; repository != nil {
			sources = append(sources, source{repository.Name, repository.Guides})
		}
	}
	sources = append(sources, source{"", input.Global})
	for _, src := range sources {
		for _, guide := range src.guides {
			for _, term := range terms {
				found := []int{}
				for n, line := range guide.Lines {
					if fixesTermIndex(line, term) {
						found = append(found, n)
					}
				}
				if len(found) == 0 || len(found) > 3 {
					continue
				}
				for _, n := range found {
					text := fixesClip(strings.TrimSpace(guide.Lines[n]), 220)
					// CLAUDE.md and AGENTS.md are often copies.
					if seen[src.repository+"\x00"+text] {
						continue
					}
					seen[src.repository+"\x00"+text] = true
					hits = append(hits, fixGuidanceHit{Repository: src.repository, File: guide.Name, Line: n + 1, Term: term, Text: text})
				}
			}
			if len(hits) >= 4 {
				return hits[:4]
			}
		}
	}
	return hits
}

func fixesClip(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	cut := string(runes[:n])
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}

// fixesTitle is a member's sentence as a title: list markers and emphasis
// off, and no longer than a card shows.
func fixesTitle(text string) string {
	text = strings.TrimSpace(fixesListMarker.ReplaceAllString(strings.TrimSpace(text), ""))
	text = strings.ReplaceAll(text, "**", "")
	text = strings.Join(strings.Fields(text), " ")
	if text != "" {
		runes := []rune(text)
		if r := runes[0]; r >= 'a' && r <= 'z' && !strings.ContainsAny(strings.Fields(text)[0], "`/._-()") {
			runes[0] = r - 'a' + 'A'
			text = string(runes)
		}
	}
	return fixesClip(text, 200)
}

// fixesFilesNamed are files and commands most members name.
func fixesFilesNamed(events []fixesEvent) []string {
	counts := map[string]int{}
	order := []string{}
	add := func(name string) {
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
	}
	for _, e := range events {
		text := e.Sentence + " "
		if e.hasSubject() {
			text += e.Subject
		}
		names := map[string]bool{}
		for _, match := range fixesBackticked.FindAllStringSubmatch(text, -1) {
			names[strings.Trim(match[1], ".,:;")] = true
		}
		for _, span := range fixesBarePath.FindAllStringSubmatchIndex(text, -1) {
			if span[2] > 0 {
				before := text[span[2]-1]
				if fixesWordByte(before) || strings.IndexByte("/.-`", before) >= 0 {
					continue
				}
			}
			names[strings.Trim(text[span[2]:span[3]], ".,:;")] = true
		}
		for name := range names {
			if strings.HasPrefix(name, "-") || strings.HasPrefix(name, "http") || fixesNumeric.MatchString(name) || !(strings.Contains(name, " ") || fixesExtension.MatchString(name)) {
				delete(names, name)
			}
		}
		for _, doc := range fixesDocFile.FindAllString(text, -1) {
			if strings.HasSuffix(strings.ToLower(doc), ".md") || strings.ToUpper(doc) == doc {
				names[doc] = true
			}
		}
		sorted := make([]string, 0, len(names))
		for name := range names {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)
		for _, name := range sorted {
			add(name)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	out := []string{}
	for _, name := range order {
		if counts[name] >= 2 && len(out) < 6 {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		out = order[:min(3, len(order))]
	}
	return out
}

func fixesQuote(e fixesEvent, similarity float64) fixQuote {
	label := e.Family
	if family, ok := agentEventFamilyByKey(e.Family); ok {
		label = family.Label
	}
	about := ""
	if e.hasSubject() {
		about = fixesClip(e.Subject, 300)
	}
	return fixQuote{EventID: fmt.Sprintf("e%d", e.ID), Sentence: fixesClip(e.Sentence, 400), About: about, Kind: e.Family, KindLabel: label, Repository: e.repositoryLabel(),
		Model: defaultString(nilIfEmpty(e.Model), "Unknown model"), Day: e.Day, At: e.At, Source: e.Source, WorkspaceID: e.Workspace, ConversationID: e.ConversationID, MessageID: e.MessageID, Similarity: math.Round(similarity*1000) / 1000}
}

// fixesStatus says whether a problem still recurs. Expected is its rate per
// message read in its repositories while it was active, times the messages
// read there since; a guidance commit counts only when a line it added names
// a key term.
func fixesStatus(input fixesInput, events []fixesEvent, terms []string) fixStatus {
	days := []string{}
	repositories := map[string]bool{}
	for _, e := range events {
		if e.Day != "" {
			days = append(days, e.Day)
		}
		if e.Repository != "" {
			repositories[e.Repository] = true
		}
	}
	if len(days) == 0 {
		return fixStatus{State: "unknown", Label: "No dates recorded", Detail: "None of its events has a time."}
	}
	sort.Strings(days)
	first, last := days[0], days[len(days)-1]
	inScope := func(repository string) bool {
		if len(repositories) == 0 {
			return repository == ""
		}
		return repositories[repository]
	}
	during, after := 0, 0
	for key, n := range input.Reads {
		if !inScope(key[0]) {
			continue
		}
		if key[1] >= first && key[1] <= last {
			during += n
		} else if key[1] > last {
			after += n
		}
	}
	status := fixStatus{First: first, Last: last, MessagesAfter: after}
	if during > 0 {
		expected := math.Round(float64(len(events))*float64(after)/float64(during)*10) / 10
		status.Expected = &expected
	}
	status.DaysQuiet = fixesDaysBetween(last, input.End)
	// Guidance commits about this problem: a line they added names a key
	// term (a bare directory is too broad).
	keys := []string{}
	for _, term := range terms {
		if strings.Contains(term, "/") && !strings.Contains(term[strings.LastIndex(term, "/")+1:], ".") {
			continue
		}
		keys = append(keys, term)
	}
	about := func(commit fixesCommit) *fixCommitNote {
		repository := input.Repositories[commit.Repository]
		if repository == nil || repository.Added == nil || len(keys) == 0 {
			return nil
		}
		for _, line := range repository.Added(commit.SHA) {
			for _, term := range keys {
				if fixesTermIndex(line, term) {
					return &fixCommitNote{Repository: repository.Name, SHA: commit.SHA[:min(8, len(commit.SHA))], Day: commit.Day, Subject: fixesClip(commit.Subject, 140), Term: term, Line: fixesClip(line, 200)}
				}
			}
		}
		return nil
	}
	ids := make([]string, 0, len(repositories))
	for id := range repositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var within *fixCommitNote
	var withinDay string
	for _, id := range ids {
		repository := input.Repositories[id]
		if repository == nil {
			continue
		}
		for _, commit := range repository.Commits {
			switch {
			case commit.Day > last:
				if note := about(commit); note != nil {
					status.CommitsSince = append(status.CommitsSince, *note)
				}
			case commit.Day > first && commit.Day <= last:
				if note := about(commit); note != nil && commit.Day >= withinDay {
					within, withinDay = note, commit.Day
				}
			}
		}
	}
	sort.SliceStable(status.CommitsSince, func(i, j int) bool { return status.CommitsSince[i].Day < status.CommitsSince[j].Day })
	if len(status.CommitsSince) > 3 {
		status.CommitsSince = status.CommitsSince[len(status.CommitsSince)-3:]
	}
	if within != nil {
		n := 0
		for _, e := range events {
			if e.Day > within.Day && repositories[e.Repository] {
				n++
			}
		}
		if n > 0 {
			within.After = n
			status.Persisted = within
		}
	}
	plural := func(n int, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	switch {
	case status.DaysQuiet <= 3:
		status.State, status.Label = "recurring", "Still recurring"
		status.Detail = fmt.Sprintf("Last seen %s, %s before the latest scanned message.", dayLabel(last), plural(status.DaysQuiet, "day"))
		if status.DaysQuiet == 0 {
			status.Detail = fmt.Sprintf("Last seen %s, the latest day scanned.", dayLabel(last))
		}
	case status.Expected != nil && *status.Expected >= 3:
		status.State, status.Label = "quiet", "Gone quiet"
		if len(status.CommitsSince) > 0 {
			status.State, status.Label = "quiet-fixed", "Quiet since a guidance change"
		}
		status.Detail = fmt.Sprintf("None in %s; about %s would be expected at its earlier rate over the %s messages read since.", plural(status.DaysQuiet, "day"), fixesNumber(*status.Expected), fixesThousands(after))
	default:
		expected := 0.0
		if status.Expected != nil {
			expected = *status.Expected
		}
		status.State, status.Label = "unclear", "Too little activity since to tell"
		status.Detail = fmt.Sprintf("None in %s, but at its earlier rate only about %s would be expected.", plural(status.DaysQuiet, "day"), fixesNumber(expected))
	}
	return status
}

func fixesNumber(value float64) string {
	if value == math.Trunc(value) {
		return fmt.Sprintf("%d", int(value))
	}
	return fmt.Sprintf("%.1f", value)
}

func fixesThousands(n int) string {
	text := fmt.Sprint(n)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}

func fixesDaysBetween(a, b string) int {
	ta, errA := time.Parse("2006-01-02", a)
	tb, errB := time.Parse("2006-01-02", b)
	if errA != nil || errB != nil {
		return 0
	}
	return max(0, int(math.Round(tb.Sub(ta).Hours()/24)))
}

// fixesIdleCap bounds a gap in a conversation's activity: idle hours are not
// time lost.
const fixesIdleCap = 300.0

// fixesEpisode is one friction episode: from a friction event to the first
// resolution (a lesson or workaround), giving up, or the turn's end.
type fixesEpisode struct {
	seconds float64
	calls   int
	outcome string
}

// fixesEpisodes finds the friction episodes the given events start.
func fixesEpisodes(events []fixesEvent, timelines map[string][]fixesMoment) map[int64]fixesEpisode {
	out := map[int64]fixesEpisode{}
	type placed struct {
		event fixesEvent
		pos   int
	}
	byTurn := map[[2]string][]placed{}
	turnLast := map[[2]string]int{}
	positions := map[string]map[string][2]int{} // conversation → message → (position, turn start)
	for conversation, moments := range timelines {
		index := map[string][2]int{}
		turn := 0
		for pos, moment := range moments {
			if moment.Prompt {
				turn = pos
			}
			if moment.Message != "" {
				if _, ok := index[moment.Message]; !ok {
					index[moment.Message] = [2]int{pos, turn}
				}
			}
			turnLast[[2]string{conversation, fmt.Sprint(turn)}] = pos
		}
		positions[conversation] = index
	}
	for _, e := range events {
		at, ok := positions[e.ConversationID][e.MessageID]
		if !ok {
			continue
		}
		key := [2]string{e.ConversationID, fmt.Sprint(at[1])}
		byTurn[key] = append(byTurn[key], placed{e, at[0]})
	}
	keys := make([][2]string, 0, len(byTurn))
	for key := range byTurn {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
	})
	for _, key := range keys {
		list := byTurn[key]
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].pos != list[j].pos {
				return list[i].pos < list[j].pos
			}
			return list[i].event.ID < list[j].event.ID
		})
		moments := timelines[key[0]]
		for i := 0; i < len(list); {
			if !fixesFriction[list[i].event.Family] {
				i++
				continue
			}
			start := list[i]
			endPos, outcome, j := turnLast[key], "unresolved", i+1
			found := false
			for ; j < len(list); j++ {
				family := list[j].event.Family
				if family == "unblocking_insight" || family == "workaround" {
					endPos, outcome, found = list[j].pos, "resolved", true
					break
				}
				if family == "gave_up" {
					endPos, outcome, found = list[j].pos, "gave up", true
					break
				}
			}
			episode := fixesEpisode{outcome: outcome}
			from := start.pos
			// A reply reports a problem after the fact: the time it cost runs
			// from the turn's first failed call up to the report.
			if start.event.Source == "reply" && !found {
				turnStart, _ := strconv.Atoi(key[1])
				for pos := turnStart; pos < start.pos && pos < len(moments); pos++ {
					if moments[pos].Call && moments[pos].Failed {
						from, endPos, episode.outcome = pos, start.pos, "reported"
						break
					}
				}
			}
			var previous time.Time
			for pos := from; pos <= endPos && pos < len(moments); pos++ {
				moment := moments[pos]
				if moment.Call {
					episode.calls++
				}
				at, ok := parseTime(moment.At)
				if !ok {
					continue
				}
				if !previous.IsZero() && !at.Before(previous) {
					episode.seconds += math.Min(fixesIdleCap, at.Sub(previous).Seconds())
				}
				previous = at
			}
			out[start.event.ID] = episode
			if found {
				i = j + 1
			} else {
				i = len(list)
			}
		}
	}
	return out
}

// fixesToolErrors are the failed calls just before members, in their turns.
func fixesToolErrors(events []fixesEvent, timelines map[string][]fixesMoment) []fixCount {
	counts := map[string]int{}
	for i, e := range events {
		if i >= 25 {
			break
		}
		moments := timelines[e.ConversationID]
		pos := -1
		for p, moment := range moments {
			if moment.Message == e.MessageID {
				pos = p
				break
			}
		}
		found := 0
		for p := pos - 1; p >= 0 && found < 2; p-- {
			moment := moments[p]
			if moment.Prompt {
				break
			}
			if moment.Call && moment.Failed {
				counts[fixesClip(defaultString(nilIfEmpty(moment.Program), "tool")+": "+defaultString(nilIfEmpty(moment.Error), "error"), 90)]++
				found++
			}
		}
	}
	ranked := fixesRank(counts, map[string]string{})
	if len(ranked) > 4 {
		ranked = ranked[:4]
	}
	return ranked
}

// fixesBuild is the whole analysis: groups per pass, items, merged items,
// stale-file items, and each item's status, cost and guidance.
func fixesBuild(input fixesInput) ([]fixItem, map[string]int, error) {
	excluded := map[string]int{}
	inScope := []fixesEvent{}
	safeguards := map[string]int{}
	for _, e := range input.Events {
		switch {
		case e.Family == "circumventing_constraint":
			safeguards[e.ConversationID]++
		case fixesRequestKinds[e.Family] || fixesAboutRequest(e.Family, e.Sentence):
			excluded[e.Family]++
		case fixesPlanOnly(e.Sentence):
			excluded["plan"]++
		default:
			inScope = append(inScope, e)
		}
	}
	docs := map[int64]fixesTerms{}
	use := fixesCodeUse{code: map[string]int{}, all: map[string]int{}}
	for _, e := range inScope {
		terms := fixesTokens(e.text())
		docs[e.ID] = terms
		for _, word := range terms.words {
			use.all[word]++
		}
		for _, word := range terms.codeWords {
			use.code[word]++
		}
	}
	groups := []fixesGroup{}
	taken := map[int64]bool{}
	for _, pass := range fixesPasses {
		for _, group := range fixesRecurring(inScope, pass, docs) {
			groups = append(groups, group)
			if pass.Source == "stale" {
				for _, e := range group.events {
					taken[e.ID] = true
				}
			}
		}
	}
	items := []*fixItem{}
	for _, group := range groups {
		for _, part := range fixesSplitKnownFailures(group) {
			items = append(items, fixesItemFromGroup(input, part, use))
		}
	}
	items = fixesMergeSame(input, items)
	items = append(items, fixesStaleFileItems(input, inScope, taken)...)
	// Time lost and failed calls need the members' conversations in order.
	conversations := map[string]bool{}
	for _, item := range items {
		for _, e := range item.events {
			conversations[e.ConversationID] = true
		}
	}
	ids := make([]string, 0, len(conversations))
	for id := range conversations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	timelines := map[string][]fixesMoment{}
	if input.Timelines != nil && len(ids) > 0 {
		loaded, err := input.Timelines(ids)
		if err != nil {
			return nil, nil, err
		}
		timelines = loaded
	}
	episodes := fixesEpisodes(inScope, timelines)
	out := make([]fixItem, 0, len(items))
	for _, item := range items {
		for _, e := range item.events {
			if episode, ok := episodes[e.ID]; ok {
				item.Cost.Episodes++
				item.Cost.Hours += episode.seconds / 3600
				item.Cost.Calls += episode.calls
				if episode.outcome != "resolved" {
					item.Cost.Unresolved++
				}
			}
		}
		item.Cost.Hours = math.Round(item.Cost.Hours*100) / 100
		item.ToolErrors = fixesToolErrors(item.events, timelines)
		seen := map[string]bool{}
		for _, e := range item.events {
			if !seen[e.ConversationID] {
				seen[e.ConversationID] = true
				item.Safeguards += safeguards[e.ConversationID]
			}
		}
		// Codebase facts that few work items share are often the task's own
		// findings rather than guidance.
		item.Tentative = item.Category == "knowledge" && item.WorkItems < 5
		fixesMarkIntended(item)
		// A key term shows on the card only when most members name it.
		item.Terms = fixesCommonTerms(item.events, item.Terms)
		for _, list := range []*[]string{&item.Terms, &item.Files} {
			if *list == nil {
				*list = []string{}
			}
		}
		if item.GuidanceHits == nil {
			item.GuidanceHits = []fixGuidanceHit{}
		}
		if item.ToolErrors == nil {
			item.ToolErrors = []fixCount{}
		}
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, excluded, nil
}

// fixesFill sets an item's counts, places and status from its events.
func fixesFill(input fixesInput, item *fixItem) {
	events := item.events
	work, conversations := map[string]bool{}, map[string]bool{}
	repositories, models, kinds := map[string]int{}, map[string]int{}, map[string]int{}
	labels := map[string]string{}
	item.Thinking = 0
	for _, e := range events {
		work[e.Workspace] = true
		conversations[e.ConversationID] = true
		repositories[e.repositoryLabel()]++
		models[defaultString(nilIfEmpty(e.Model), "Unknown model")]++
		kinds[e.Family]++
		if family, ok := agentEventFamilyByKey(e.Family); ok {
			labels[e.Family] = family.Label
		}
		if e.Source == "thinking" {
			item.Thinking++
		}
	}
	item.Events, item.WorkItems, item.Conversations = len(events), len(work), len(conversations)
	item.Repositories = fixesRank(repositories, map[string]string{})
	item.Models = fixesRank(models, map[string]string{})
	if len(item.Models) > 5 {
		item.Models = item.Models[:5]
	}
	item.Kinds = fixesRank(kinds, labels)
	item.Status = fixesStatus(input, events, item.Terms)
	// A merge adds events, so the reason counts what the item holds now; the
	// place stays where the item was put.
	_, _, item.ScopeReason = fixesScope(events)
}

// fixesSplitKnownFailures takes the known-failure sentences ("an unrelated
// test failed once and passed on rerun") out of a group about something
// else. They become a group of their own when they span two work items, to
// merge with the check they name; otherwise they are left out.
func fixesSplitKnownFailures(group fixesGroup) []fixesGroup {
	if group.source == "stale" || fixesCategorize(group.source, group.events) == "flaky" {
		return []fixesGroup{group}
	}
	known, rest := []fixesEvent{}, []fixesEvent{}
	for _, e := range group.events {
		if fixesCategoryTerms["flaky"].MatchString(e.Sentence) {
			known = append(known, e)
		} else {
			rest = append(rest, e)
		}
	}
	if len(known) == 0 || fixesWorkItems(rest) < 2 {
		return []fixesGroup{group}
	}
	main := group
	main.events = rest
	out := []fixesGroup{main}
	if fixesWorkItems(known) >= 2 {
		split := group
		split.events = known
		split.anchor, split.anchorShare = "", 0
		out = append(out, split)
	}
	return out
}

func fixesItemFromGroup(input fixesInput, group fixesGroup, use fixesCodeUse) *fixItem {
	category := fixesCategorize(group.source, group.events)
	scope, repository, reason := fixesScope(group.events)
	files := fixesFilesNamed(group.events)
	terms := fixesKeyTermsOf(group, use)
	lead := fixesRepresentative(group.events, group.similarity, terms)
	item := &fixItem{Source: group.source, Category: category, Terms: terms, Files: files, Scope: scope, ScopeReason: reason, Groups: 1,
		anchor: group.anchor, anchorShare: group.anchorShare, events: group.events, Lead: fixesTitle(lead.Sentence)}
	item.CategoryLabel, _ = fixesCategory(category)
	if category == "flaky" {
		item.Check = fixesCheckName(group.events)
	}
	fixesPlace(input, item, repository)
	item.Title = fixesProblemTitle(item)
	item.Suggestion = fixesSuggestion(category, defaultString(nilIfEmpty(item.Check), first(terms)), fixesClip(item.Lead, 240))
	item.Quotes = append(item.Quotes, fixesQuote(lead, group.similarity[lead.ID]))
	for _, e := range group.events {
		if len(item.Quotes) < 8 && e.ID != lead.ID {
			item.Quotes = append(item.Quotes, fixesQuote(e, group.similarity[e.ID]))
		}
	}
	for _, e := range group.events {
		if len(item.Members) < fixesMembersShown {
			item.Members = append(item.Members, fixesQuote(e, group.similarity[e.ID]))
		}
	}
	item.MemberCount = len(group.events)
	identity := "event:" + group.leader.MessageID + ":" + fmt.Sprint(group.leader.SentenceStart) + ":" + group.leader.Family
	if group.anchorShare >= 0.6 && group.anchor != "" {
		identity = "term:" + group.anchor
	}
	item.ID = stableID("fix", category, item.Target, identity)
	fixesFill(input, item)
	return item
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// fixesPlace sets an item's destination from its scope.
func fixesPlace(input fixesInput, item *fixItem, repository string) {
	global := fixesGlobalFile(item.events, input.Global)
	if item.Scope == "global" {
		item.Target, item.TargetLabel = "global", "Global setup"
		item.DestKind, item.DestWhere = fixesDestination(item.Category, "global", "", nil, item.Files, global)
	} else {
		info := input.Repositories[repository]
		label := repository
		for _, e := range item.events {
			if e.Repository == repository {
				label = e.repositoryLabel()
				break
			}
		}
		item.Target, item.TargetLabel = "repo:"+repository, label
		item.DestKind, item.DestWhere = fixesDestination(item.Category, "repo", label, info, item.Files, global)
	}
	repositories := []string{}
	seen := map[string]bool{}
	for _, e := range item.events {
		if e.Repository != "" && !seen[e.Repository] {
			seen[e.Repository] = true
			repositories = append(repositories, e.Repository)
		}
	}
	sort.Strings(repositories)
	if len(repositories) == 0 && repository != "" {
		repositories = []string{repository}
	}
	item.GuidanceHits = fixesMentions(input, repositories, fixesCommonTerms(item.events, item.Terms))
}

// fixesMergeSame joins items that one issue split across passes or
// sub-groups: the same key term, kind of fix and destination become one.
func fixesMergeSame(input fixesInput, items []*fixItem) []*fixItem {
	sort.SliceStable(items, func(i, j int) bool { return items[i].WorkItems > items[j].WorkItems })
	out := []*fixItem{}
	hosts := map[string]*fixItem{}
	for _, item := range items {
		key := ""
		if item.anchorShare >= 0.6 && item.anchor != "" {
			key = item.anchor + "\x00" + item.Category + "\x00" + item.Target
		}
		host := hosts[key]
		// Known failures of one test are one item however agents describe it.
		if item.Category == "flaky" {
			for _, other := range out {
				if other.Category == "flaky" && other.Target == item.Target && (fixesSameCheck(other.events, item.events) || fixesNamesTerm(other.events, item.Terms)) {
					host, key = other, "check"
					break
				}
			}
		}
		// Cards that describe one obstacle in one place are one fix.
		if host == nil {
			for _, other := range out {
				if other.Target == item.Target && other.Category == item.Category && fixesSameObstacle(other, item) {
					host, key = other, "near"
					break
				}
			}
		}
		if key == "" || host == nil {
			if key != "" {
				hosts[key] = item
			}
			out = append(out, item)
			continue
		}
		host.Groups++
		ids := map[int64]bool{}
		for _, e := range host.events {
			ids[e.ID] = true
		}
		for _, e := range item.events {
			if !ids[e.ID] {
				host.events = append(host.events, e)
			}
		}
		host.Quotes = append(host.Quotes, item.Quotes[:min(2, len(item.Quotes))]...)
		if len(host.Quotes) > 8 {
			host.Quotes = host.Quotes[:8]
		}
		for _, member := range item.Members {
			if len(host.Members) < fixesMembersShown {
				host.Members = append(host.Members, member)
			}
		}
		host.MemberCount = len(host.events)
		for _, file := range item.Files {
			if !containsString(host.Files, file) && len(host.Files) < 6 {
				host.Files = append(host.Files, file)
			}
		}
		for _, term := range item.Terms {
			if !containsString(host.Terms, term) {
				host.Terms = append(host.Terms, term)
			}
		}
	}
	for _, item := range out {
		if item.Groups > 1 {
			fixesFill(input, item)
			if item.Category == "flaky" {
				item.Check = fixesCheckName(item.events)
			}
			item.Title = fixesProblemTitle(item)
		}
	}
	return out
}

// fixesStaleFileItems are out-of-date-doc events naming the same file in the
// same repository, when two or more work items share it and grouping did not
// already take them.
func fixesStaleFileItems(input fixesInput, events []fixesEvent, taken map[int64]bool) []*fixItem {
	type key struct{ repository, name string }
	files := map[key][]fixesEvent{}
	for _, e := range events {
		if e.Family != "stale_guidance" || taken[e.ID] {
			continue
		}
		text := e.text() + " " + e.Response
		names := map[string]bool{}
		for _, match := range fixesDocFile.FindAllString(text, -1) {
			name := match[strings.LastIndex(match, "/")+1:]
			stem := strings.SplitN(name, ".", 2)[0]
			if strings.HasSuffix(strings.ToLower(name), ".md") || stem != "" && strings.ToUpper(stem) == stem {
				names[name] = true
			}
		}
		for name := range names {
			files[key{e.Repository, name}] = append(files[key{e.Repository, name}], e)
		}
	}
	keys := make([]key, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].repository < keys[j].repository || keys[i].repository == keys[j].repository && keys[i].name < keys[j].name
	})
	out := []*fixItem{}
	for _, k := range keys {
		es := files[k]
		if fixesWorkItems(es) < 2 {
			continue
		}
		sort.SliceStable(es, func(i, j int) bool { return es[i].Probability > es[j].Probability })
		scope, repository, reason := fixesScope(es)
		title := k.name + " is out of date"
		if k.repository != "" {
			title += " in " + es[0].repositoryLabel()
		}
		item := &fixItem{Source: "stale-file", Category: "stale", Title: title, Terms: []string{k.name}, Files: []string{k.name}, Scope: scope, ScopeReason: reason, Groups: 1, events: es}
		item.CategoryLabel, _ = fixesCategory("stale")
		item.Lead = fixesTitle(fixesRepresentative(es, nil, item.Terms).Sentence)
		fixesPlace(input, item, repository)
		item.GuidanceHits = nil
		item.Suggestion = fmt.Sprintf("Read %s against the code and correct what agents found wrong; each quote says what.", k.name)
		for i, e := range es {
			if i < 8 {
				item.Quotes = append(item.Quotes, fixesQuote(e, 0))
			}
			if i < fixesMembersShown {
				item.Members = append(item.Members, fixesQuote(e, 0))
			}
		}
		item.MemberCount = len(es)
		item.ID = stableID("fix", "stale", item.Target, "file:"+k.name)
		fixesFill(input, item)
		out = append(out, item)
	}
	return out
}

// A sentence that leans on an earlier one ("The same restriction…", "It
// seems…") doesn't stand alone as a card's quote.
var (
	fixesAntecedent  = regexp.MustCompile(`(?i)^\W*(?:the same|same|that|this|these|those|it|they|also|again|still|odd|hmm+|ok(?:ay)?|so|but|and|then|now|here|there|i notice|i see|it seems|seems|it looks|looks like)\b`)
	fixesFirstPerson = regexp.MustCompile(`(?i)\b(?:i|i'm|i've|i'll|i’m|i’ve|i’ll|me|my|we|our)\b`)
	fixesPlan        = regexp.MustCompile(`(?i)\s*[,;:—–]\s*(?:(?:so|and|but|then|which)\s+(?:i|i'm|i've|i'll|i’m|i’ve|i’ll|we|let me)\b|(?:i|we)\s+(?:should|will|need to|must|have to)\b|(?:re-?)?[a-z]+ing\s+(?:with|it|them|the|a)\b).*$`)
	fixesMissing     = regexp.MustCompile(`(?i)not installed|isn.t installed|missing|lacks?\b|no module|command not found|not found|unavailable|not available`)
	fixesSlow        = regexp.MustCompile(`(?i)timed? ?out|timeout|too slow|slow\b|\b\d+[- ]?(?:minute|second)|load average|heavily loaded`)
	fixesVersionWord = regexp.MustCompile(`(?i)\bversion\b|\bnode \d+|\bpython ?3\.\d+|requires node`)
	fixesQuotedName  = regexp.MustCompile("[\"“]([^\"”]{6,80})[\"”]")
	fixesCheckSplit  = regexp.MustCompile(`[^\pL\pN]+`)
	fixesCheckFiller = fixesWordSet(`unrelated failure failures failing fails failed fail test tests one only remaining remains pre preexisting existing
		master main origin clean change changes changed branch looks look likely probably unit web browser suite full check checks run runs rerun reruns
		same way issue issues error errors my mine me i i'm i've i'll it its that this which so likely just seems seem flaky flake still also again
		there here pass passes passed passing all now left out tier fast go npm prefix assertion assert something introduced caused cause related
		without with clean dashboard's itself note noted opening open pr current currently today verify verified against confirm confirmed
		previously reproduced reproduces identified isolated including alone hit hits
		broader summary lines line known output truncated lost list probably maybe might`)
	fixesNoticing    = regexp.MustCompile(`(?i)^\W*(?:actually,?\s*|so,?\s*|now,?\s*)?(?:i'?m|i’m|i am|i(?:'ve| have)?|i’ve)\s+(?:realizing|noticing|seeing|realize[sd]?|notice[sd]?|see|saw|found|confirmed|spotted)\s+(?:that\s+|now\s+)?`)
	fixesReportLabel = regexp.MustCompile(`(?i)^\W*(?:test status|status|verification|tests?|testing|result|results|note|blocker|caveat)[\s*_]*:[\s*_]*`)
	fixesProhibits   = regexp.MustCompile(`(?i)\b(?:never|must not|mustn.t|not to|don.t|do not|forbids?|forbidden|avoid|without)\b`)
	fixesFollowsRule = regexp.MustCompile(`(?i)\bper (?:my|the|our|your) (?:notes|rules?|instructions|guidance|memory|memories|preferences?)\b|` +
		`\b(?:my|the) (?:notes|rules?|memory|memories|instructions|guidance) (?:say|says|advise|advises|tell|tells|require|requires|forbid|forbids)\b|` +
		`\bas (?:my|the) (?:notes|rules?|instructions|guidance) (?:say|advise|require)|\bi (?:recall|remember) (?:never|not to|to avoid)\b|` +
		`\baccording to (?:my|the) (?:notes|rules?|instructions|guidance|memory)\b|\b(?:claude|agents)\.md (?:says|requires|forbids)\b`)
)

// fixesRepresentative is the member that best states the problem by itself:
// close to the group, naming a key term, from a reply, of a readable length,
// and not leaning on an earlier sentence.
func fixesRepresentative(events []fixesEvent, similarity map[int64]float64, terms []string) fixesEvent {
	terms = fixesCommonTerms(events, terms)
	best, bestScore := events[0], math.Inf(-1)
	for _, e := range events {
		text := strings.TrimSpace(fixesListMarker.ReplaceAllString(e.Sentence, ""))
		score := 0.5
		if similarity != nil {
			if value, ok := similarity[e.ID]; ok {
				score = value
			}
		}
		for _, term := range terms {
			if fixesTermIndex(text, term) {
				score += 0.6
				break
			}
		}
		if e.Source == "reply" {
			score += 0.2
		}
		if fixesAntecedent.MatchString(text) {
			score -= 1
		}
		if fixesFirstPerson.MatchString(text) {
			score -= 0.3
		}
		if fixesPlan.MatchString(text) {
			score -= 0.2
		}
		if n := len([]rune(text)); n < 35 || n > 220 {
			score -= 0.4
		}
		if score > bestScore {
			best, bestScore = e, score
		}
	}
	return best
}

// fixesCommonTerms are the key terms at least 30% of the events name. A term
// two members of a large group mention names their problem, not the group's.
func fixesCommonTerms(events []fixesEvent, terms []string) []string {
	out := []string{}
	for _, term := range terms {
		n := 0
		for _, e := range events {
			if fixesTermIndex(e.Sentence, term) {
				n++
			}
		}
		if float64(n) >= 0.3*float64(len(events)) {
			out = append(out, term)
		}
	}
	return out
}

// fixesCheckWords are what a known-failure sentence says about which check
// fails, without the words every such sentence shares.
func fixesCheckWords(e fixesEvent) map[string]bool {
	words := map[string]bool{}
	for _, word := range fixesCheckSplit.Split(strings.ToLower(e.Sentence), -1) {
		if len(word) >= 3 && !fixesStop[word] && !fixesDiscourse[word] && !fixesCheckFiller[word] && !fixesNumeric.MatchString(word) {
			words[word] = true
		}
	}
	return words
}

// fixesCheckTop are a group's most shared check words, at most limit, each
// in two or more members.
func fixesCheckTop(events []fixesEvent, limit int) []string {
	counts := map[string]int{}
	for _, e := range events {
		for word := range fixesCheckWords(e) {
			counts[word]++
		}
	}
	ranked := fixesRank(counts, map[string]string{})
	out := []string{}
	for _, entry := range ranked {
		if entry.Count >= 2 && len(out) < limit {
			out = append(out, entry.Key)
		}
	}
	return out
}

// fixesSameCheck says whether two known-failure groups are about one check:
// they quote the same test name, or share two of their eight most shared check
// words ("dashboard parity", "floating rounding").
func fixesSameCheck(a, b []fixesEvent) bool {
	if name := fixesCheckQuoted(a); name != "" && name == fixesCheckQuoted(b) {
		return true
	}
	// Compared by stem: "scheduler" and "scheduling" are one check.
	top := map[string]bool{}
	for _, word := range fixesCheckTop(a, 8) {
		top[fixesCheckStem(word)] = true
	}
	shared := 0
	for _, word := range fixesCheckTop(b, 8) {
		if top[fixesCheckStem(word)] {
			shared++
		}
	}
	return shared >= 2
}

// fixesCheckQuoted is the test name members quote most, when two do.
func fixesCheckQuoted(events []fixesEvent) string {
	counts := map[string]int{}
	for _, e := range events {
		seen := map[string]bool{}
		for _, match := range fixesQuotedName.FindAllStringSubmatch(e.Sentence, -1) {
			name := strings.TrimSpace(match[1])
			if !seen[name] {
				seen[name] = true
				counts[name]++
			}
		}
	}
	ranked := fixesRank(counts, map[string]string{})
	if len(ranked) > 0 && ranked[0].Count >= 2 {
		return ranked[0].Key
	}
	return ""
}

// fixesCheckName names the failing check: a quoted test name, or its most
// shared words in the order agents write them.
func fixesCheckName(events []fixesEvent) string {
	if name := fixesCheckQuoted(events); name != "" {
		return "“" + name + "”"
	}
	// A test named in code ("`TestBackgroundPass…`") names the check.
	tests := map[string]int{}
	for _, e := range events {
		seen := map[string]bool{}
		for _, match := range fixesTestName.FindAllStringSubmatch(e.Sentence, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				tests[match[1]]++
			}
		}
	}
	if name := fixesTopCount(tests); name != "" && tests[name] >= 2 {
		return "`" + name + "`"
	}
	top := []string{}
	stems := map[string]bool{}
	for _, word := range fixesCheckTop(events, 6) {
		if stem := fixesCheckStem(word); !stems[stem] && len(top) < 3 {
			stems[stem] = true
			top = append(top, word)
		}
	}
	if len(top) == 0 {
		return ""
	}
	if len(top) > 3 {
		top = top[:3]
	}
	// Order the words as the first member using most of them does.
	for _, e := range events {
		lower := strings.ToLower(e.Sentence)
		positions := map[string]int{}
		for _, word := range top {
			if i := strings.Index(lower, word); i >= 0 {
				positions[word] = i
			}
		}
		if len(positions) == len(top) {
			sort.SliceStable(top, func(i, j int) bool { return positions[top[i]] < positions[top[j]] })
			break
		}
	}
	return strings.Join(top, " ")
}

// fixesProblemTitle names the problem from the group: its key term, kind of
// fix and place. Without a key term, the lead's own words, cut to the
// problem.
func fixesProblemTitle(item *fixItem) string {
	place := item.TargetLabel
	if item.Target == "global" {
		place = "this Mac"
	}
	share := func(pattern *regexp.Regexp) float64 { return fixesShare(item.events, pattern) }
	// A number ("2.16666666666") is a value agents quote, not the problem.
	terms := []string{}
	for _, term := range fixesCommonTerms(item.events, item.Terms) {
		if !fixesNumeric.MatchString(term) {
			terms = append(terms, term)
		}
	}
	term := first(terms)
	code := "`" + term + "`"
	switch {
	case item.Category == "flaky" && item.Check != "":
		return "Known failing check: " + item.Check
	case item.Category == "env" && fixesVersionClash(item.events) != "":
		tool, versions := fixesVersionClashParts(item.events)
		return "Wrong " + tool + " version in " + place + "'s environment (" + versions + ")"
	case item.Category == "env" && term != "":
		switch {
		case share(fixesMissing) >= 0.3:
			return code + " missing from " + place + "'s environment"
		case share(fixesVersionWord) >= 0.3:
			return "Wrong " + code + " version in " + place + "'s environment"
		case share(fixesSlow) >= 0.3:
			return code + " too slow or timing out in " + place
		}
		return code + " keeps failing in " + place + "'s environment"
	case item.Category == "tool" && term != "":
		return code + " keeps failing for agents"
	case item.Category == "harness" && term != "":
		return "The agent's shell or sandbox trips on " + code
	case item.Category == "flaky" && term != "":
		return "Known failing check involving " + code
	}
	if title := fixesObstacleTitle(item, place); title != "" {
		return title
	}
	return fixesLeadTitle(item.Lead)
}

// fixesLeadTitle cuts a sentence to the problem it states: no leaning start,
// no trailing plan ("…, so I'll …"), and short.
func fixesLeadTitle(text string) string {
	text = fixesPlan.ReplaceAllString(text, "")
	if loc := fixesNoticing.FindStringIndex(text); loc != nil && len(text)-loc[1] > 20 {
		text = text[loc[1]:]
	}
	if loc := fixesReportLabel.FindStringIndex(text); loc != nil && len(text)-loc[1] > 20 {
		text = text[loc[1]:]
	}
	for i := 0; i < 2; i++ {
		if loc := fixesAntecedent.FindStringIndex(text); loc != nil && loc[1] < len(text) {
			rest := strings.TrimLeft(text[loc[1]:], " ,:—–-")
			if len(rest) > 20 {
				text = rest
			}
		}
	}
	text = strings.TrimRight(strings.TrimSpace(text), ".")
	return fixesTitle(fixesClip(text, 120))
}

// fixesMarkIntended marks a problem the guidance causes on purpose: its
// agents cite the rule they are following. Nothing is wrong with the setup.
func fixesMarkIntended(item *fixItem) {
	// Guidance that names a requirement the setup misses ("the guidance says
	// Node 24") points at the fix; only a rule against something is intended.
	following := 0
	for _, e := range item.events {
		if text := e.Sentence + " " + e.Subject; fixesFollowsRule.MatchString(text) && fixesProhibits.MatchString(text) {
			following++
		}
	}
	if following < 2 || float64(following) < 0.2*float64(len(item.events)) {
		return
	}
	item.Intended = true
	item.IntendedReason = fmt.Sprintf("%d of its %d events cite a rule the agent was following (its notes, instructions or guidance), so this is the guidance working as intended rather than something to fix.", following, len(item.events))
	item.Suggestion = "Nothing to fix: agents are following a rule on purpose. If they lose time getting around it, add the approved way to do the task beside the rule."
}

// fixesNamesTerm says whether two or more of a known failure's members name
// one of another's key terms: "the `parallel-hours` test failed once" and
// "rounding in the parallel-hours parity test" are the same check.
func fixesNamesTerm(events []fixesEvent, terms []string) bool {
	for _, term := range terms {
		if fixesNumeric.MatchString(term) {
			continue
		}
		n := 0
		for _, e := range events {
			if fixesTermIndex(e.Sentence, term) {
				n++
			}
		}
		if n >= 2 {
			return true
		}
	}
	return false
}

// A sentence that only says what the agent will do next ("I should verify
// the test fails without the fix", "Re-arming with a glob-safe condition")
// is a plan, not a problem: it neither seeds nor titles a group.
var fixesPlanOpener = regexp.MustCompile(`(?i)^\W*(?:(?:so|now|next|then|first|ok(?:ay)?),?\s+)*(?:` +
	`(?:i|we)\s+(?:should|will|need to|must|have to|want to|plan to|am going to|'m going to|’m going to)|i'll|i’ll|we'll|let me|let's|` +
	`(?:i'm|i’m|i am)\s+(?:going to|about to|re-?arming|retrying|restarting|re-?running)|re-?arming|retrying|restarting|re-?running|trying again)\b`)

// A plan that also names an obstacle is kept: "I want to verify this, since
// it's blocking a real scenario". An obstacle inside what the agent means to
// check ("I should verify the test fails without the fix") is hypothetical,
// and a rule ("I must not hold a read…") is not a plan.
func fixesPlanOnly(sentence string) bool {
	text := fixesListMarker.ReplaceAllString(strings.TrimSpace(sentence), "")
	loc := fixesPlanOpener.FindStringIndex(text)
	if loc == nil || fixesPlanRule.MatchString(text) {
		return false
	}
	rest := text[loc[1]:]
	clauses := fixesClauseCut.Split(rest, 2)
	if len(clauses) == 2 && fixesObstacleWord.MatchString(clauses[1]) {
		return false
	}
	return fixesPlanCheck.MatchString(rest) || !fixesObstacleWord.MatchString(clauses[0])
}

var (
	fixesPlanRule     = regexp.MustCompile(`(?i)\b(?:never|must not|mustn.t|avoid|not to|shouldn.t)\b`)
	fixesPlanCheck    = regexp.MustCompile(`(?i)^\s*(?:also\s+|first\s+|then\s+|just\s+)?(?:verify|check|confirm|see|test|make sure|figure out|investigate|look|search|find out|try|run|re-?run|restart|use|wait|inspect|read|open)\b`)
	fixesObstacleWord = regexp.MustCompile(`(?i)fail|error|block|denied|refus|lack|missing|can.t|cannot|could not|couldn.t|unable|\bbr(?:eak|oke|oken)\b|wrong|conflict|time ?out|timed out|killed|stopped|not (?:installed|found|available)|mismatch|differ|stale|out of date|\blimit|reject|crash|\bbug\b|isn.t|doesn.t|didn.t|won.t|aren.t`)
)

// fixesSameObstacle says whether two cards in one place and of one kind
// describe the same obstacle: they share a key term most of each card's
// members name, or the words most of their members use overlap. Overlap is
// the shared words over the smaller card's words, at least 0.5 and at least
// three words, so "dev server" alone doesn't join two problems.
func fixesSameObstacle(a, b *fixItem) bool {
	if thing := fixesMismatchThing(a); thing != "" && thing == fixesMismatchThing(b) {
		return true
	}
	for _, x := range fixesCommonTerms(a.events, a.Terms) {
		for _, y := range fixesCommonTerms(b.events, b.Terms) {
			if strings.EqualFold(x, y) && !fixesNumeric.MatchString(x) {
				return true
			}
		}
	}
	wa, wb := fixesSignature(a.events), fixesSignature(b.events)
	shared := 0
	for word := range wa {
		if wb[word] {
			shared++
		}
	}
	smaller := min(len(wa), len(wb))
	return shared >= 3 && float64(shared) >= 0.5*float64(smaller)
}

// fixesSignature are the content words at least 40% of the members use,
// folded to a rough stem ("conflicts", "conflicted" → "conflict"), and the
// tool versions ("node 24") at least a quarter of them name.
func fixesSignature(events []fixesEvent) map[string]bool {
	counts := map[string]int{}
	for _, e := range events {
		seen := map[string]bool{}
		// A tool and its version ("Node 20", "node v24") count as one word.
		for _, match := range fixesToolVersion.FindAllStringSubmatch(strings.ToLower(e.Sentence), -1) {
			if word := match[1] + " " + match[2]; !seen[word] {
				seen[word] = true
				counts[word]++
			}
		}
		for _, word := range fixesCheckSplit.Split(strings.ToLower(e.Sentence), -1) {
			if len(word) < 3 || fixesStop[word] || fixesDiscourse[word] || fixesCheckFiller[word] || fixesNumeric.MatchString(word) || fixesSignatureFiller[word] {
				continue
			}
			word = fixesStem(strings.TrimPrefix(word, "v"))
			if !seen[word] {
				seen[word] = true
				counts[word]++
			}
		}
	}
	out := map[string]bool{}
	for word, n := range counts {
		share := 0.4
		if strings.Contains(word, " ") {
			share = 0.25 // a tool and its version is a strong signal
		}
		if n >= 2 && float64(n) >= share*float64(len(events)) {
			out[word] = true
		}
	}
	return out
}

var fixesTestName = regexp.MustCompile("`((?:Test|test)[A-Za-z0-9_]{3,80}|[a-z0-9-]+\\.test\\.m?[jt]s)`")

var fixesToolVersion = regexp.MustCompile(`\b(node|python|go|npm|swift|xcode|java|ruby|postgres|sqlite)\s+(?:is\s+|version\s+)?v?(\d+(?:\.\d+)?)\b`)

var fixesSignatureFiller = fixesWordSet(`could couldn't can't cannot not did didn't does doesn't don't was were would will should need needs needed
	get got gets getting make made because since after before then when while into onto over about only again also
	notice noticed noticing realize realized realizing seems seem see saw wonder wondering`)

func fixesCheckStem(word string) string {
	if len(word) > 6 && strings.HasSuffix(word, "er") {
		word = strings.TrimSuffix(word, "er")
	}
	return fixesStem(word)
}

func fixesStem(word string) string {
	for _, suffix := range []string{"ing", "ed", "es", "s"} {
		if len(word) > len(suffix)+3 && strings.HasSuffix(word, suffix) {
			return strings.TrimSuffix(word, suffix)
		}
	}
	return word
}

// fixesVersionClash is the tool whose versions members disagree on ("the
// shell has Node 20 and the runner requires Node 24"): two or more versions
// of it, each named by at least a fifth of them.
func fixesVersionClash(events []fixesEvent) string {
	tool, _ := fixesVersionClashParts(events)
	return tool
}

func fixesVersionClashParts(events []fixesEvent) (string, string) {
	counts := map[string]map[string]int{}
	for _, e := range events {
		seen := map[string]bool{}
		for _, match := range fixesToolVersion.FindAllStringSubmatch(strings.ToLower(e.Sentence), -1) {
			if key := match[1] + " " + match[2]; !seen[key] {
				seen[key] = true
				if counts[match[1]] == nil {
					counts[match[1]] = map[string]int{}
				}
				counts[match[1]][match[2]]++
			}
		}
	}
	tools := make([]string, 0, len(counts))
	for tool := range counts {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		versions := []string{}
		for version, n := range counts[tool] {
			if float64(n) >= 0.2*float64(len(events)) && n >= 2 {
				versions = append(versions, version)
			}
		}
		if len(versions) >= 2 {
			sort.Strings(versions)
			return fixesCapital(tool), strings.Join(versions, " vs ")
		}
	}
	return "", ""
}
