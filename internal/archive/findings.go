package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// findingsVersion names the detectors' keys, wording, and metrics. A new
// version rebuilds findings the next time the pass runs, even on the same day.
const findingsVersion = "findings-v1"

// The pass observes the last findingWindowDays days: 28 for the gate and the
// baseline, twelve weeks for sparklines, and 90 for savings after a copy.
const (
	findingWindowDays   = 91
	findingGateDays     = 28
	findingRecentDays   = 7
	findingLiveMinimum  = 5
	findingSpreadMin    = 3
	findingSavingsDays  = 90
	findingAfterMaxDays = 60
	findingFadeFactor   = 0.5
	findingGuardSlack   = 0.10
)

// findingSpec says what a finding measures, so its detector can measure it
// again after the pattern is gone.
type findingSpec struct {
	Detector string            `json:"detector"`
	Scope    string            `json:"scope"`
	Pattern  string            `json:"pattern"`
	Params   map[string]string `json:"params,omitempty"`
}

func (spec findingSpec) id() string {
	sum := sha256.Sum256([]byte(spec.Pattern))
	return spec.Detector + ":" + spec.Scope + ":" + hex.EncodeToString(sum[:])[:10]
}

// findingMetric is the one number verification tracks: events over exposure.
// Kind "rate" counts units (usually conversations) that hit the pattern;
// "mean" averages a per-unit value (tokens before the first edit); "share" is
// the value of units above a fixed threshold over all units' value.
type findingMetric struct {
	Kind string `json:"kind"`
	// Unit is what exposure counts: conversations, calls, engagements, runs.
	Unit string `json:"unit"`
	// Events and Exposure describe the ratio in the finding's own words, for
	// the chart and the result sentence.
	Events   string `json:"events"`
	Exposure string `json:"exposure"`
	// Value names a mean or share metric's value, such as "tokens".
	Value string `json:"value,omitempty"`
	// Threshold is a share metric's per-unit bar, fixed when copied.
	Threshold float64 `json:"threshold,omitempty"`
	// GuardErrors and GuardSuccess add the failure and engagement guards.
	GuardErrors  bool `json:"guard_errors,omitempty"`
	GuardSuccess bool `json:"guard_success,omitempty"`
	// Failures marks detectors whose occurrences are failures avoided.
	Failures bool `json:"failures,omitempty"`
}

// findingObs is one unit's observation: a conversation, a call, or a TL1 run.
type findingObs struct {
	Unit         string
	Conversation string
	Workspace    string
	Day          string
	Provider     string
	Hit          bool
	// Value is the unit's number for mean and share metrics; Total is its
	// denominator contribution for share metrics.
	Value, Total float64
	// Occurrences, Tokens, CostUSD, and DurationMS are what the pattern
	// cost in this unit.
	Occurrences int
	Tokens      int64
	CostUSD     float64
	DurationMS  int64
	// Calls and Errors feed the failure guard, Success the engagement guard.
	Calls, Errors int
	Success       *bool
	HelpCalls     int
	UsageErrors   int
	At            string
}

// findingHandle is a piece of evidence MCP can read.
type findingHandle struct {
	At             string `json:"at"`
	ConversationID string `json:"conversation_id,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	// Where, Did, and Happened are the Evidence tab's one line.
	Where    string `json:"where"`
	Did      string `json:"did"`
	Happened string `json:"happened,omitempty"`
}

// findingStep is one attempt's change: the lever it pulls and the proposed
// change, in the detector's words. Each unchanged result moves to the next.
type findingStep struct {
	Lever  string `json:"lever"`
	Change string `json:"change"`
	// Label names the change in attempts lists and wins: "A line in AGENTS.md".
	Label string `json:"label"`
}

// findingCard is what the detector writes for the specific case.
type findingCard struct {
	Title       string        `json:"title"`
	Explanation string        `json:"explanation"`
	ImpactNote  string        `json:"impact_note"`
	Steps       []findingStep `json:"steps"`
	ChartTitle  string        `json:"chart_title"`
	// Rate phrases the 28-day rate, as in "1 of every 3 conversations".
	Rate string `json:"rate"`
}

// findingCandidate is a pattern one detector found in one scope.
type findingCandidate struct {
	Spec         findingSpec
	Lever        string
	RepositoryID string
	Target       string
	Metric       findingMetric
	Obs          map[string]*findingObs
	Evidence     []findingHandle
	Facts        map[string]any
	Aliases      []string
	// Live, when set, replaces the scope's top-level conversations in the
	// last week (TL1 runs are counted by the detector).
	Live *int
	// Elsewhere is the same pattern outside the scope, shown next to results.
	Elsewhere map[string]*findingObs
	// Hidden candidates are kept for trend but never shown: patterns with no
	// lever the user controls.
	Hidden bool
	write  func(*findingCandidate, findingStats) findingCard
	card   findingCard
	stats  findingStats
}

// findingStats summarizes a candidate's last 28 days.
type findingStats struct {
	Affected   int     `json:"affected"`
	Exposure   int     `json:"exposure"`
	Recent     int     `json:"recent"`
	Live       int     `json:"live"`
	Days       int     `json:"days"`
	Workspaces int     `json:"workspaces"`
	Events     float64 `json:"events"`
	Denom      float64 `json:"denominator"`
	Rate       float64 `json:"rate"`
	// Occurrences, Tokens, CostUSD, and Minutes are what the pattern cost in
	// 28 days; PerEvent divides them by events.
	Occurrences int     `json:"occurrences"`
	Tokens      int64   `json:"tokens"`
	CostUSD     float64 `json:"cost_usd"`
	Minutes     float64 `json:"minutes"`
	// DailyExposure is the recent daily rate of relevant work, which sets
	// the length of a result's after window.
	DailyExposure float64          `json:"daily_exposure"`
	LastSeen      string           `json:"last_seen"`
	Providers     []map[string]any `json:"providers"`
}

// passes applies the observability gate at a threshold.
func (stats findingStats) passes(threshold int) bool {
	return stats.Affected >= threshold && stats.Recent >= 1 && stats.Live >= findingLiveMinimum &&
		stats.Days >= findingSpreadMin && stats.Workspaces >= findingSpreadMin
}

// findingConversation is one top-level conversation in the window, with its
// sub-agents' usage folded in.
type findingConversation struct {
	ID, WorkspaceID, RepositoryID, Provider, Harness, Version, HostID string
	Day, SourceKind, Flavor, Location, Model, Automation              string
	Started                                                           time.Time
	Tokens                                                            int64
	CostUSD                                                           float64
	Requests, Compactions, PeakInput                                  int64
}

// findingEnv is what every detector reads in one pass.
type findingEnv struct {
	catalog    *Catalog
	ctx        context.Context
	db         queryer
	now        time.Time
	today      string
	from       time.Time
	fromUTC    string
	gateFrom   string
	recentFrom string
	book       priceBook
	convs      map[string]*findingConversation
	// roots maps every conversation in the window, sub-agents included, to
	// its top-level conversation.
	roots        map[string]string
	repositories map[string]string
	locations    map[string][]string
	hosts        map[string]string
	discover     bool
	specs        map[string][]findingSpec
	// cachedPreEdits is D3 and D4's shared read (see preEdits), and
	// cachedCalls D1 and D7's (see callGroups).
	cachedPreEdits map[string]*preEdit
	cachedCalls    []callGroup
}

// callGroup is one conversation's calls of one tool and program.
type callGroup struct {
	Conversation, Category, Tool, Program, Model, LastStatus, LastAt string
	Calls, Errors, Help, Usage, LastSequence                         int64
	Tokens, ResultTokens, Carried, DurationMS                        int64
	Output                                                           float64
}

// callGroups groups the window's calls by conversation, tool, and program
// once per pass, for failure exposure and CLI engagements.
func (env *findingEnv) callGroups() ([]callGroup, error) {
	if env.cachedCalls != nil {
		return env.cachedCalls, nil
	}
	signature := "lower(COALESCE(error_signature,''))"
	usage := []string{}
	for _, pattern := range []string{"usage:%", "%unknown flag%", "%unknown command%", "%unrecognized argument%", "%invalid choice%", "%unexpected argument%",
		"%required argument%", "%flag provided but not defined%", "%unknown option%", "%missing required%"} {
		usage = append(usage, signature+" LIKE '"+pattern+"'")
	}
	rows, err := env.db.QueryContext(env.ctx, `SELECT conversation_id,tool_category,tool_name,COALESCE(program,''),COUNT(*),SUM(status='error' AND test_failure=0),
		SUM(tool_category='command' AND (instr(COALESCE(command,''),'--help')>0 OR COALESCE(subcommand,'') IN ('help','--help','-h') OR instr(COALESCE(command,'')||' ',' -h ')>0)),
		SUM(status='error' AND test_failure=0 AND (`+strings.Join(usage, " OR ")+`)),
		SUM(result_tokens+carried_tokens),SUM(result_tokens),SUM(carried_tokens),COALESCE(SUM(output_tokens),0),SUM(COALESCE(duration_ms,0)),
		MAX(sequence),status,started_at,COALESCE(model,'')
		FROM tool_calls WHERE started_at>=? GROUP BY 1,2,3,4`, env.fromUTC)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []callGroup{}
	for rows.Next() {
		var group callGroup
		var lastStatus, lastAt, model sql.NullString
		if err := rows.Scan(&group.Conversation, &group.Category, &group.Tool, &group.Program, &group.Calls, &group.Errors, &group.Help, &group.Usage,
			&group.Tokens, &group.ResultTokens, &group.Carried, &group.Output, &group.DurationMS, &group.LastSequence, &lastStatus, &lastAt, &model); err != nil {
			return nil, err
		}
		group.LastStatus, group.LastAt, group.Model = lastStatus.String, lastAt.String, model.String
		if env.roots[group.Conversation] != "" {
			groups = append(groups, group)
		}
	}
	env.cachedCalls = groups
	return groups, rows.Err()
}

// findingDetector is one detector: it discovers candidates when env.discover
// is set, and measures env.specs for its name either way.
type findingDetector struct {
	name string
	run  func(env *findingEnv) ([]*findingCandidate, error)
}

// findingDetectors lists the detectors in the order their findings are built.
var findingDetectors = []findingDetector{
	{"failure", detectFailures},
	{"drift", detectDrift},
	{"cli", detectCLIFriction},
	{"docs", detectDocsHosts},
	{"orientation", detectOrientation},
	{"exploration", detectExploration},
	{"heavy-output", detectHeavyOutput},
	{"outlier", detectOutliers},
	{"tl1", detectTL1},
}

// findingsState tracks the pass in this process.
type findingsState struct {
	mu        sync.Mutex
	running   bool
	lastError string
	phase     string
}

func (env *findingEnv) inGate(day string) bool   { return day >= env.gateFrom && day <= env.today }
func (env *findingEnv) inRecent(day string) bool { return day >= env.recentFrom && day <= env.today }

// wants reports the specs to measure for a detector, keyed by pattern.
func (env *findingEnv) wants(detector string) map[string]findingSpec {
	specs := map[string]findingSpec{}
	for _, spec := range env.specs[detector] {
		specs[spec.Scope+"\x1f"+spec.Pattern] = spec
	}
	return specs
}

// scopeOf returns where a conversation's repository-level fix goes: TL1
// runs belong to their flavor, everything else to its repository.
func (env *findingEnv) scopeOf(conv *findingConversation) string {
	if conv.SourceKind == "tl1" && conv.Flavor != "" {
		return "automation:tl1:" + conv.Flavor
	}
	if conv.RepositoryID != "" {
		return "repo:" + conv.RepositoryID
	}
	return ""
}

// inScope reports whether a top-level conversation is in a scope.
func (env *findingEnv) inScope(scope string, conv *findingConversation) bool {
	kind, rest, _ := strings.Cut(scope, ":")
	switch kind {
	case "repo":
		return conv.RepositoryID == rest && !(conv.SourceKind == "tl1" && conv.Flavor != "")
	case "global":
		host, provider, _ := strings.Cut(rest, ":")
		return conv.HostID == host && conv.Provider == provider
	case "automation":
		return env.scopeOf(conv) == scope
	case "provider":
		return conv.Provider == rest
	}
	return false
}

// hostSuffix names the Mac of a global scope when the library has more than
// one, since each Mac has its own global instruction files.
func (env *findingEnv) hostSuffix(scope string) string {
	if !strings.HasPrefix(scope, "global:") || len(env.hosts) < 2 {
		return ""
	}
	host, _, _ := strings.Cut(strings.TrimPrefix(scope, "global:"), ":")
	if label := env.hosts[host]; label != "" {
		return " on " + label
	}
	return ""
}

// globalScope is one provider's global instructions on one Mac.
func globalScope(conv *findingConversation) string {
	return "global:" + conv.HostID + ":" + conv.Provider
}

// scopeTarget is the cart a scope's findings go to by default.
func scopeTarget(scope string) string {
	kind, rest, _ := strings.Cut(scope, ":")
	switch kind {
	case "repo", "global":
		return scope
	case "automation":
		if strings.HasPrefix(rest, "tl1:") {
			return "automation:" + rest
		}
		return scope
	case "provider":
		return scope
	}
	return scope
}

// scopeName names a scope in a detector's sentences.
func (env *findingEnv) scopeName(scope string) string {
	kind, rest, _ := strings.Cut(scope, ":")
	switch kind {
	case "repo":
		return defaultString(env.repositories[rest], "this repository")
	case "global":
		_, provider, _ := strings.Cut(rest, ":")
		return "every repository " + providerLabel(provider) + " works in"
	case "automation":
		parts := strings.Split(rest, ":")
		return "TL1's " + parts[len(parts)-1] + " runs"
	case "provider":
		return providerLabel(rest) + " conversations"
	}
	return scope
}

// loadFindingEnv reads the window's top-level conversations, their usage
// with sub-agents folded in, and the names detectors write with.
func (c *Catalog) loadFindingEnv(ctx context.Context, db queryer, discover bool, specs []findingSpec) (*findingEnv, error) {
	current := c.clock()
	today := current.Local().Format("2006-01-02")
	from := dayTime(today).AddDate(0, 0, -(findingWindowDays - 1))
	env := &findingEnv{catalog: c, ctx: ctx, db: db, now: current, today: today, from: from, fromUTC: formatTime(from),
		gateFrom:   dayTime(today).AddDate(0, 0, -(findingGateDays - 1)).Format("2006-01-02"),
		recentFrom: dayTime(today).AddDate(0, 0, -(findingRecentDays - 1)).Format("2006-01-02"),
		convs:      map[string]*findingConversation{}, roots: map[string]string{}, repositories: map[string]string{},
		locations: map[string][]string{}, hosts: map[string]string{}, discover: discover, specs: map[string][]findingSpec{}}
	for _, spec := range specs {
		env.specs[spec.Detector] = append(env.specs[spec.Detector], spec)
	}
	book, err := c.loadPriceBook()
	if err != nil {
		return nil, err
	}
	env.book = book
	repositories, err := queryMapsContext(ctx, db, "SELECT id,display_name,local_locations_json FROM repositories")
	if err != nil {
		return nil, err
	}
	for _, row := range repositories {
		id := firstString(row["id"])
		env.repositories[id] = firstString(row["display_name"])
		env.locations[id] = repositoryStrings(firstString(row["local_locations_json"]))
	}
	hosts, err := queryMapsContext(ctx, db, "SELECT id,label FROM hosts")
	if err != nil {
		return nil, err
	}
	for _, row := range hosts {
		env.hosts[firstString(row["id"])] = firstString(row["label"])
	}
	legacy, _ := c.metaValue(ctx, legacyHostKey)
	rows, err := queryMapsContext(ctx, db, `SELECT c.id,c.workspace_id,w.repository_id,c.provider,COALESCE(c.harness,'') harness,
		COALESCE(c.harness_version_last,'') version,COALESCE(c.origin_host_id,?) host_id,c.started_at,w.source_kind,COALESCE(w.flavor,'') flavor,
		COALESCE(w.location,'') location,COALESCE(c.model,'') model
		FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE c.parent_id IS NULL AND c.agent_depth=0 AND c.started_at>=? AND c.started_at<=?
		AND c.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)`, legacy, env.fromUTC, formatTime(current))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		started, ok := parseTime(firstString(row["started_at"]))
		if !ok || findingExcluded(firstString(row["source_kind"]), firstString(row["location"])) {
			continue
		}
		conv := &findingConversation{ID: firstString(row["id"]), WorkspaceID: firstString(row["workspace_id"]), RepositoryID: firstString(row["repository_id"]),
			Provider: firstString(row["provider"]), Harness: firstString(row["harness"]), Version: firstString(row["version"]), HostID: firstString(row["host_id"]),
			Day: started.Local().Format("2006-01-02"), SourceKind: firstString(row["source_kind"]), Flavor: firstString(row["flavor"]),
			Location: firstString(row["location"]), Model: firstString(row["model"]), Started: started}
		env.convs[conv.ID] = conv
		env.roots[conv.ID] = conv.ID
	}
	children, err := queryMapsContext(ctx, db, "SELECT id,parent_id FROM conversations WHERE parent_id IS NOT NULL")
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for _, row := range children {
		parents[firstString(row["id"])] = firstString(row["parent_id"])
	}
	for child := range parents {
		at, seen := child, 0
		for parents[at] != "" && seen < 16 {
			at, seen = parents[at], seen+1
		}
		if env.convs[at] != nil {
			env.roots[child] = at
		}
	}
	if err := env.loadUsage(); err != nil {
		return nil, err
	}
	if err := env.loadAutomation(); err != nil {
		return nil, err
	}
	return env, nil
}

// findingExcluded leaves out runs Pharos can tell are probes or tests:
// Antigravity sessions in /tmp and anything under a test's temporary folder.
func findingExcluded(sourceKind, location string) bool {
	if sourceKind == "antigravity" && (strings.HasPrefix(location, "/tmp") || strings.HasPrefix(location, "/private/tmp")) {
		return true
	}
	return strings.HasPrefix(location, "/var/folders/") && strings.Contains(location, "/T/") ||
		strings.HasPrefix(location, "/private/var/folders/") && strings.Contains(location, "/T/")
}

// loadUsage adds each top-level conversation's tokens, API-equivalent cost,
// and model requests, with its sub-agents' usage.
func (env *findingEnv) loadUsage() error {
	sums := make([]string, len(usageTokenFields))
	for index, field := range usageTokenFields {
		sums[index] = "SUM(u." + field + ") " + field
	}
	rows, err := queryMapsContext(env.ctx, env.db, `SELECT a.conversation_id,substr(u.usage_hour,1,10) day,COALESCE(NULLIF(u.model,''),a.model,'') model,`+strings.Join(sums, ",")+`
		FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id
		WHERE u.usage_hour>=? AND u.total_tokens>0 GROUP BY 1,2,3`, env.fromUTC[:13])
	if err != nil {
		return err
	}
	for _, row := range rows {
		conv := env.convs[env.roots[firstString(row["conversation_id"])]]
		if conv == nil {
			continue
		}
		tokens := ledgerTokens(row)
		conv.Tokens += tokens["total_tokens"]
		if priced := env.book.cost(defaultString(firstString(row["model"]), "Unknown model"), firstString(row["day"]), tokens); priced.cost != nil {
			conv.CostUSD += *priced.cost
		}
	}
	requests, err := queryMapsContext(env.ctx, env.db, `SELECT m.conversation_id,COUNT(*) requests,SUM(m.compacted_before) compactions,MAX(m.input_tokens) peak
		FROM model_requests m JOIN conversations c ON c.id=m.conversation_id WHERE c.started_at>=? GROUP BY 1`, env.fromUTC)
	if err != nil {
		return err
	}
	for _, row := range requests {
		if conv := env.convs[env.roots[firstString(row["conversation_id"])]]; conv != nil {
			conv.Requests += integer(row["requests"])
			if conv.ID == firstString(row["conversation_id"]) {
				conv.Compactions += integer(row["compactions"])
				conv.PeakInput = max(conv.PeakInput, integer(row["peak"]))
			}
		}
	}
	return nil
}

// loadAutomation marks conversations whose first message was sent by a
// script or orchestrator rather than typed (see docs/human-authorship.md).
func (env *findingEnv) loadAutomation() error {
	rows, err := queryMapsContext(env.ctx, env.db, `SELECT conversation_id,automated_chars,total_chars,spans_json FROM message_authorship
		WHERE day>=? ORDER BY conversation_id,sent_at`, env.from.Format("2006-01-02"))
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
		conv := env.convs[id]
		if conv == nil || integer(row["automated_chars"])*2 <= integer(row["total_chars"]) {
			continue
		}
		conv.Automation = "automated"
		for _, span := range sliceMaps(parseJSONValue(firstString(row["spans_json"]))) {
			if firstString(span["category"]) == "automated" {
				conv.Automation = defaultString(firstString(span["reason"]), "automated")
				break
			}
		}
	}
	return nil
}

func parseJSONValue(text string) any {
	var value any
	_ = jsonUnmarshalString(text, &value)
	return value
}

func sliceMaps(value any) []map[string]any {
	items, _ := value.([]any)
	output := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			output = append(output, row)
		}
	}
	return output
}

// toolCallCost prices what a tool call added to the context: its result
// sent once as new input, re-read from cache by later requests, and the
// output that produced it.
func (env *findingEnv) toolCallCost(provider, model, day string, resultTokens, carried int64, output float64) float64 {
	context := map[string]int64{"cache_read_input_tokens": carried, "output_tokens": int64(output + 0.5)}
	if provider == "claude" {
		context["cache_creation_input_tokens"] = resultTokens
	} else {
		context["uncached_input_tokens"] = resultTokens
	}
	if priced := env.book.cost(defaultString(model, "Unknown model"), day, context); priced.cost != nil {
		return *priced.cost
	}
	return 0
}

// stats summarizes a candidate's observations over the gate window.
func (env *findingEnv) stats(candidate *findingCandidate) findingStats {
	stats := findingStats{}
	days, workspaces, conversations, hits, recent := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	providers := map[string]int{}
	recentExposure := map[string]bool{}
	for _, obs := range candidate.Obs {
		if !env.inGate(obs.Day) {
			continue
		}
		conversations[obs.Conversation] = true
		if env.inRecent(obs.Day) {
			recentExposure[obs.Unit] = true
		}
		switch candidate.Metric.Kind {
		case "rate":
			stats.Denom++
			if obs.Hit {
				stats.Events++
			}
		case "mean":
			stats.Denom++
			stats.Events += obs.Value
		case "share":
			stats.Denom += obs.Total
			if obs.Hit {
				stats.Events += obs.Value
			}
		}
		if !obs.Hit {
			continue
		}
		hits[obs.Conversation] = true
		days[obs.Day] = true
		workspaces[obs.Workspace] = true
		if env.inRecent(obs.Day) {
			recent[obs.Conversation] = true
		}
		providers[obs.Provider]++
		stats.Occurrences += max(obs.Occurrences, 1)
		stats.Tokens += obs.Tokens
		stats.CostUSD += obs.CostUSD
		stats.Minutes += float64(obs.DurationMS) / 60_000
		if obs.At > stats.LastSeen {
			stats.LastSeen = obs.At
		}
	}
	stats.Affected, stats.Exposure, stats.Recent = len(hits), len(conversations), len(recent)
	stats.Days, stats.Workspaces = len(days), len(workspaces)
	if stats.Denom > 0 {
		stats.Rate = stats.Events / stats.Denom
	}
	// Relevant work per day over the gate window; the last week alone swings
	// too much to plan a result's window on.
	units := 0
	for _, obs := range candidate.Obs {
		if env.inGate(obs.Day) {
			units++
		}
	}
	stats.DailyExposure = math.Max(float64(units)/findingGateDays, float64(len(recentExposure))/findingRecentDays/2)
	if candidate.Live != nil {
		stats.Live = *candidate.Live
	} else {
		for _, conv := range env.convs {
			if env.inRecent(conv.Day) && env.inScope(candidate.Spec.Scope, conv) {
				stats.Live++
			}
		}
	}
	for _, provider := range sortedFindingKeys(providers) {
		stats.Providers = append(stats.Providers, map[string]any{"provider": provider, "conversations": providers[provider],
			"share": float64(providers[provider]) / float64(max(len(hits), 1))})
	}
	sort.SliceStable(stats.Providers, func(i, j int) bool {
		return integer(stats.Providers[i]["conversations"]) > integer(stats.Providers[j]["conversations"])
	})
	return stats
}

// daily sums a candidate's observations by day for finding_daily.
func (candidate *findingCandidate) daily() map[string][3]float64 {
	return findingDaily(candidate.Metric, candidate.Obs)
}

func findingDaily(metric findingMetric, observations map[string]*findingObs) map[string][3]float64 {
	days := map[string][3]float64{}
	for _, obs := range observations {
		day := days[obs.Day]
		switch metric.Kind {
		case "rate":
			day[1]++
			if obs.Hit {
				day[0]++
			}
		case "mean":
			day[1]++
			day[0] += obs.Value
		case "share":
			day[1] += obs.Total
			if obs.Hit {
				day[0] += obs.Value
			}
		}
		if obs.Hit {
			day[2] += obs.CostUSD
		}
		days[obs.Day] = day
	}
	return days
}

// providerSplit phrases a candidate's providers for sentences: "Codex" when
// one provider has four in five of the events.
func (stats findingStats) mainProvider() (string, float64) {
	if len(stats.Providers) == 0 {
		return "", 0
	}
	share, _ := stats.Providers[0]["share"].(float64)
	return firstString(stats.Providers[0]["provider"]), share
}

// findingsGeneration is what a full pass is built for: the detectors'
// version and the local day. Every index between full passes only measures
// the findings being watched.
func (c *Catalog) findingsGeneration() string {
	return findingsVersion + "/" + c.clock().Local().Format("2006-01-02")
}

// RefreshFindings runs the full findings pass when the day or detectors
// changed since the last one (or force is set), and otherwise measures only
// the findings the user is watching. The service runs it after each index,
// once the tool rollup is current.
func (c *Catalog) RefreshFindings(ctx context.Context, force bool) error {
	state := &c.findings
	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		return nil
	}
	state.running, state.phase = true, "starting"
	state.mu.Unlock()
	started := time.Now()
	built, _ := c.metaValue(ctx, "findings_generation")
	full := force || built != c.findingsGeneration()
	err := c.runFindingsPass(ctx, full)
	state.mu.Lock()
	state.running, state.phase, state.lastError = false, "", ""
	if err != nil {
		state.lastError = err.Error()
	}
	state.mu.Unlock()
	if err == nil && full {
		_ = c.writeTransaction(ctx, "findings-meta", func(tx *sql.Tx) error {
			for key, value := range map[string]string{"findings_generation": c.findingsGeneration(), "findings_built_at": formatTime(c.clock()),
				"findings_took_ms": fmt.Sprint(time.Since(started).Milliseconds())} {
				if _, err := tx.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err == nil && !full {
		_ = c.writeTransaction(ctx, "findings-meta", func(tx *sql.Tx) error {
			_, err := tx.Exec("INSERT INTO meta(key,value) VALUES('findings_measured_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", formatTime(c.clock()))
			return err
		})
	}
	return err
}

// refreshFindingsInBackground runs RefreshFindings on the catalog's
// background executor.
func (c *Catalog) refreshFindingsInBackground(force bool) {
	c.runBackground(func(ctx context.Context) {
		if err := c.RefreshFindings(ctx, force); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Findings: %v\n", err)
		}
	})
}

// findingsRunning reports a findings pass in progress and what it is doing.
func (c *Catalog) findingsRunning() (bool, string) {
	c.findings.mu.Lock()
	defer c.findings.mu.Unlock()
	return c.findings.running, c.findings.phase
}

func (c *Catalog) setFindingsPhase(phase string) {
	c.findings.mu.Lock()
	c.findings.phase = phase
	c.findings.mu.Unlock()
}

// runFindingsPass measures the specs of every finding the user acted on,
// discovers new candidates on a full pass, and stores the results.
func (c *Catalog) runFindingsPass(ctx context.Context, full bool) error {
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	states, err := c.findingUserStates(ctx, conn)
	if err != nil {
		return err
	}
	existing, err := loadFindingRows(ctx, conn, "")
	if err != nil {
		return err
	}
	retired, err := queryMapsContext(ctx, conn, "SELECT old_id,new_id FROM repository_retirements")
	if err != nil {
		return err
	}
	survivors := map[string]string{}
	for _, row := range retired {
		survivors["repo:"+firstString(row["old_id"])] = "repo:" + firstString(row["new_id"])
	}
	specs := []findingSpec{}
	measured := map[string]bool{}
	for id, row := range existing {
		state := states[id]
		if !full && !findingNeedsMeasuring(state, c.clock()) {
			continue
		}
		if full && state == nil {
			continue
		}
		// A finding of a merged repository is measured in the survivor.
		spec := row.Spec
		if survivor := survivors[spec.Scope]; survivor != "" {
			spec.Scope = survivor
		}
		specs = append(specs, spec)
		measured[spec.id()] = true
	}
	if !full && len(specs) == 0 {
		return nil
	}
	c.setFindingsPhase("reading conversations")
	env, err := c.loadFindingEnv(ctx, conn, full, specs)
	if err != nil {
		return err
	}
	candidates := []*findingCandidate{}
	for _, detector := range findingDetectors {
		if !full && len(env.specs[detector.name]) == 0 {
			continue
		}
		c.setFindingsPhase(detector.name)
		found, err := detector.run(env)
		if err != nil {
			return fmt.Errorf("%s detector: %w", detector.name, err)
		}
		candidates = append(candidates, found...)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	kept := []*findingCandidate{}
	for _, candidate := range candidates {
		candidate.stats = env.stats(candidate)
		id := candidate.Spec.id()
		if candidate.stats.Affected < findingCheckpoints[0] && !measured[id] && states[id] == nil {
			continue
		}
		if candidate.write != nil {
			candidate.card = candidate.write(candidate, candidate.stats)
		}
		if candidate.Target == "" {
			candidate.Target = scopeTarget(candidate.Spec.Scope)
		}
		if candidate.Lever == "" && len(candidate.card.Steps) > 0 {
			candidate.Lever = candidate.card.Steps[0].Lever
		}
		kept = append(kept, candidate)
	}
	kept = dedupeFindingCandidates(kept)
	c.setFindingsPhase("saving")
	if err := c.storeFindings(ctx, env, kept, existing, states, retired, full); err != nil {
		return err
	}
	c.setFindingsPhase("measuring results")
	return c.decideInterventions(ctx, env, kept)
}

// findingNeedsMeasuring reports whether an index should update a finding
// between full passes: it is being measured, its savings are accruing, or it
// is snoozed until it gets worse.
func findingNeedsMeasuring(state *findingUserState, now time.Time) bool {
	if state == nil {
		return false
	}
	if state.SnoozeWorse {
		return true
	}
	latest := state.latest()
	if latest == nil {
		return false
	}
	if latest.Status == interventionWatching {
		return true
	}
	copied, _ := parseTime(latest.CopiedAt)
	return latest.Status == interventionImproved && latest.RegressedAt == "" && now.Sub(copied) < findingSavingsDays*24*time.Hour
}

// dedupeFindingCandidates keeps one candidate per ID, preferring the one
// with more affected conversations, and drops candidates another one
// replaces (a repository finding folded into a global one).
func dedupeFindingCandidates(candidates []*findingCandidate) []*findingCandidate {
	byID := map[string]*findingCandidate{}
	order := []string{}
	replaced := map[string]bool{}
	for _, candidate := range candidates {
		for _, alias := range candidate.Aliases {
			if alias != candidate.Spec.id() {
				replaced[alias] = true
			}
		}
	}
	for _, candidate := range candidates {
		if replaced[candidate.Spec.id()] {
			continue
		}
		id := candidate.Spec.id()
		if previous := byID[id]; previous == nil {
			order = append(order, id)
			byID[id] = candidate
		} else if candidate.stats.Affected > previous.stats.Affected {
			byID[id] = candidate
		}
	}
	output := make([]*findingCandidate, 0, len(order))
	for _, id := range order {
		output = append(output, byID[id])
	}
	return output
}

// findingRow is a stored finding.
type findingRow struct {
	ID, Detector, Scope, Lever, RepositoryID, Pattern, Target string
	Spec                                                      findingSpec
	Card                                                      findingCard
	Metric                                                    findingMetric
	Stats                                                     findingStats
	Baseline, Impact                                          map[string]any
	Providers                                                 []map[string]any
	Facts                                                     map[string]any
	Evidence                                                  []findingHandle
	Active                                                    bool
	Hidden                                                    bool
	FirstSeenAt, GatePassedAt, UpdatedAt                      string
	// recentRate is the last week's rate, for snoozes until worse.
	recentRate float64
}

func loadFindingRows(ctx context.Context, q queryer, where string, args ...any) (map[string]*findingRow, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT id,detector,scope,lever,repository_id,pattern,spec_json,card_json,metric_json,baseline_json,impact_json,gate_json,
		providers_json,facts_json,evidence_json,target,active,first_seen_at,gate_passed_at,updated_at FROM findings `+where, args...)
	if err != nil {
		return nil, err
	}
	output := map[string]*findingRow{}
	for _, row := range rows {
		item := &findingRow{ID: firstString(row["id"]), Detector: firstString(row["detector"]), Scope: firstString(row["scope"]), Lever: firstString(row["lever"]),
			RepositoryID: firstString(row["repository_id"]), Pattern: firstString(row["pattern"]), Target: firstString(row["target"]),
			Active: integer(row["active"]) != 0, FirstSeenAt: firstString(row["first_seen_at"]), GatePassedAt: firstString(row["gate_passed_at"]), UpdatedAt: firstString(row["updated_at"])}
		_ = jsonUnmarshalString(firstString(row["spec_json"]), &item.Spec)
		_ = jsonUnmarshalString(firstString(row["card_json"]), &item.Card)
		_ = jsonUnmarshalString(firstString(row["metric_json"]), &item.Metric)
		_ = jsonUnmarshalString(firstString(row["gate_json"]), &item.Stats)
		_ = jsonUnmarshalString(firstString(row["baseline_json"]), &item.Baseline)
		_ = jsonUnmarshalString(firstString(row["impact_json"]), &item.Impact)
		_ = jsonUnmarshalString(firstString(row["providers_json"]), &item.Providers)
		_ = jsonUnmarshalString(firstString(row["facts_json"]), &item.Facts)
		_ = jsonUnmarshalString(firstString(row["evidence_json"]), &item.Evidence)
		item.Hidden = item.Lever == "none"
		output[item.ID] = item
	}
	recent, err := queryMapsContext(ctx, q, `SELECT finding_id,SUM(events) events,SUM(exposure) exposure FROM finding_daily WHERE day>=? GROUP BY 1`,
		time.Now().AddDate(0, 0, -(findingRecentDays-1)).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	for _, row := range recent {
		if item := output[firstString(row["finding_id"])]; item != nil {
			if exposure := floatOr(row["exposure"]); exposure > 0 {
				item.recentRate = floatOr(row["events"]) / exposure
			}
		}
	}
	return output, nil
}

// storeFindings writes the pass's candidates. A full pass replaces every
// finding the user never acted on; findings with user state stay, inactive,
// when their pattern is no longer detected. Daily counts are replaced within
// the window and kept before it.
func (c *Catalog) storeFindings(ctx context.Context, env *findingEnv, candidates []*findingCandidate, existing map[string]*findingRow, states map[string]*findingUserState, retired []map[string]any, full bool) error {
	settings, err := c.findingSettings(ctx)
	if err != nil {
		return err
	}
	threshold := settings.Threshold
	if threshold == 0 {
		// Follow the recommendation, as the view will.
		rows := map[string]*findingRow{}
		for _, candidate := range candidates {
			rows[candidate.Spec.id()] = &findingRow{ID: candidate.Spec.id(), Active: true, Hidden: candidate.Hidden, Metric: candidate.Metric, Stats: candidate.stats}
		}
		weekly := 0
		for _, conv := range env.convs {
			if env.inGate(conv.Day) {
				weekly++
			}
		}
		_, threshold = findingCheckpointPreview(&findingView{rows: rows, states: map[string]*findingUserState{}}, float64(weekly)/4)
	}
	stamp := formatTime(env.now)
	retiredIDs := map[string]string{}
	for _, row := range retired {
		retiredIDs[firstString(row["new_id"])] = retiredIDs[firstString(row["new_id"])] + "\x1f" + firstString(row["old_id"])
	}
	return c.writeTransaction(ctx, "findings", func(tx *sql.Tx) error {
		produced := map[string]bool{}
		for _, candidate := range candidates {
			id := candidate.Spec.id()
			produced[id] = true
			aliases := append([]string{}, candidate.Aliases...)
			// Scopes of repositories a merge retired point here now.
			if candidate.RepositoryID != "" {
				for _, old := range strings.Split(retiredIDs[candidate.RepositoryID], "\x1f") {
					if old == "" {
						continue
					}
					spec := candidate.Spec
					spec.Scope = strings.Replace(spec.Scope, "repo:"+candidate.RepositoryID, "repo:"+old, 1)
					aliases = append(aliases, spec.id())
				}
			}
			firstSeen, gatePassed := stamp, any(nil)
			if previous := existing[id]; previous != nil {
				firstSeen = previous.FirstSeenAt
				if previous.GatePassedAt != "" {
					gatePassed = previous.GatePassedAt
				}
			}
			for _, alias := range aliases {
				if previous := existing[alias]; previous != nil && alias != id {
					if previous.FirstSeenAt < firstSeen {
						firstSeen = previous.FirstSeenAt
					}
					if gatePassed == nil && previous.GatePassedAt != "" {
						gatePassed = previous.GatePassedAt
					}
					if _, err := tx.Exec("DELETE FROM findings WHERE id=?", alias); err != nil {
						return err
					}
					if _, err := tx.Exec("DELETE FROM finding_daily WHERE finding_id=?", alias); err != nil {
						return err
					}
				}
			}
			if gatePassed == nil && candidate.stats.passes(threshold) && !candidate.Hidden {
				gatePassed = stamp
			}
			lever := candidate.Lever
			if candidate.Hidden {
				lever = "none"
			}
			baseline := map[string]any{"events": candidate.stats.Events, "exposure": candidate.stats.Denom, "rate": candidate.stats.Rate,
				"affected": candidate.stats.Affected, "conversations": candidate.stats.Exposure}
			impact := findingImpact(candidate.stats, candidate.Metric)
			candidate.Facts = defaultFacts(candidate.Facts)
			candidate.Facts["evidence_count"] = len(candidate.Evidence)
			if _, err := tx.Exec(`INSERT INTO findings(id,detector,scope,lever,repository_id,pattern,spec_json,card_json,metric_json,baseline_json,impact_json,gate_json,
				affected,providers_json,facts_json,evidence_json,target,active,first_seen_at,gate_passed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?)
				ON CONFLICT(id) DO UPDATE SET detector=excluded.detector,scope=excluded.scope,lever=excluded.lever,repository_id=excluded.repository_id,
				pattern=excluded.pattern,spec_json=excluded.spec_json,card_json=excluded.card_json,metric_json=excluded.metric_json,baseline_json=excluded.baseline_json,
				impact_json=excluded.impact_json,gate_json=excluded.gate_json,affected=excluded.affected,providers_json=excluded.providers_json,facts_json=excluded.facts_json,
				evidence_json=excluded.evidence_json,target=excluded.target,active=1,first_seen_at=excluded.first_seen_at,
				gate_passed_at=COALESCE(findings.gate_passed_at,excluded.gate_passed_at),updated_at=excluded.updated_at`,
				id, candidate.Spec.Detector, candidate.Spec.Scope, lever, nilIfEmpty(candidate.RepositoryID), candidate.Spec.Pattern, jsonText(candidate.Spec),
				jsonText(candidate.card), jsonText(candidate.Metric), jsonText(baseline), jsonText(impact), jsonText(candidate.stats), candidate.stats.Affected,
				jsonText(candidate.stats.Providers), jsonText(defaultFacts(candidate.Facts)), jsonText(limitHandles(candidate.Evidence, 20)), candidate.Target,
				firstSeen, gatePassed, stamp); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM finding_aliases WHERE finding_id=?", id); err != nil {
				return err
			}
			for _, alias := range append(aliases, id) {
				if _, err := tx.Exec("INSERT INTO finding_aliases(alias,finding_id) VALUES(?,?) ON CONFLICT(alias) DO UPDATE SET finding_id=excluded.finding_id", alias, id); err != nil {
					return err
				}
			}
			if err := writeFindingDaily(tx, id, env.from.Format("2006-01-02"), candidate.daily()); err != nil {
				return err
			}
			if candidate.Elsewhere != nil {
				if err := writeFindingDaily(tx, id+"#elsewhere", env.from.Format("2006-01-02"), findingDaily(candidate.Metric, candidate.Elsewhere)); err != nil {
					return err
				}
			}
		}
		if !full {
			return nil
		}
		if err := storeFindingsVolume(tx, env); err != nil {
			return err
		}
		for id := range existing {
			if produced[id] {
				continue
			}
			if states[id] != nil {
				if _, err := tx.Exec("UPDATE findings SET active=0 WHERE id=?", id); err != nil {
					return err
				}
				continue
			}
			for _, statement := range []string{"DELETE FROM findings WHERE id=?", "DELETE FROM finding_daily WHERE finding_id=? OR finding_id=?||'#elsewhere'", "DELETE FROM finding_aliases WHERE finding_id=?"} {
				args := []any{id}
				if strings.Count(statement, "?") == 2 {
					args = append(args, id)
				}
				if _, err := tx.Exec(statement, args...); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// writeFindingDaily replaces a finding's daily counts from a day on.
func writeFindingDaily(tx *sql.Tx, id, from string, days map[string][3]float64) error {
	if _, err := tx.Exec("DELETE FROM finding_daily WHERE finding_id=? AND day>=?", id, from); err != nil {
		return err
	}
	for day, values := range days {
		if day < from {
			continue
		}
		if _, err := tx.Exec("INSERT INTO finding_daily(finding_id,day,events,exposure,cost_usd) VALUES(?,?,?,?,?)", id, day, values[0], values[1], values[2]); err != nil {
			return err
		}
	}
	return nil
}

// findingImpact is what a finding cost in the last 28 days, shown as "a
// month".
func findingImpact(stats findingStats, metric findingMetric) map[string]any {
	// Over the last 28 days, the same window as the counts in the wording.
	failures := 0.0
	if metric.Failures {
		failures = float64(stats.Occurrences)
	}
	return map[string]any{"usd": stats.CostUSD, "tokens": float64(stats.Tokens), "minutes": stats.Minutes, "failures": failures, "window_days": findingGateDays}
}

func defaultFacts(facts map[string]any) map[string]any {
	if facts == nil {
		return map[string]any{}
	}
	return facts
}

// limitHandles keeps the newest evidence.
func limitHandles(handles []findingHandle, limit int) []findingHandle {
	sort.SliceStable(handles, func(i, j int) bool { return handles[i].At > handles[j].At })
	if len(handles) > limit {
		handles = handles[:limit]
	}
	if handles == nil {
		return []findingHandle{}
	}
	return handles
}

// addEvidence records a handle, keeping at most a few per conversation so
// one long session doesn't fill the Evidence tab.
func (candidate *findingCandidate) addEvidence(handle findingHandle) {
	same := 0
	for _, existing := range candidate.Evidence {
		if existing.ConversationID == handle.ConversationID && handle.ConversationID != "" {
			same++
		}
	}
	if same >= 2 {
		return
	}
	candidate.Evidence = append(candidate.Evidence, handle)
	if len(candidate.Evidence) > 200 {
		candidate.Evidence = limitHandles(candidate.Evidence, 60)
	}
}
