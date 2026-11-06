package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Ask Pharos suggestions (docs/ask-suggestions.md). Background signals score
// the areas of assets/ask-suggestions.json against the library and store the
// result in one meta row; a request ranks the catalog's generic questions from
// that row, this Mac's page visits and Ask history, without writing.

type askSuggestionCatalog struct {
	Version  int                   `json:"version"`
	Forms    []string              `json:"forms"`
	Areas    []askSuggestionArea   `json:"areas"`
	Families []askSuggestionFamily `json:"families"`
}
type askSuggestionArea struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Pages   []string `json:"pages"`
	Signals []string `json:"signals"`
}
type askSuggestionFamily struct {
	ID       string   `json:"id"`
	Area     string   `json:"area"`
	Form     string   `json:"form"`
	Prior    float64  `json:"prior"`
	Variants []string `json:"variants"`
	Signals  []string `json:"signals,omitempty"`
}

var loadedAskSuggestions = sync.OnceValues(func() (askSuggestionCatalog, error) {
	var catalog askSuggestionCatalog
	raw, err := assets.ReadFile("assets/ask-suggestions.json")
	if err == nil {
		err = json.Unmarshal(raw, &catalog)
	}
	return catalog, err
})

// askSignal is what one signal noticed. Score is 0 when there is nothing
// worth asking about; Repository names the repository most of it is in, and
// Period whether it stands out this week or over the month. Lead is the
// private observation an agent gets when the user asks the question as
// offered; it never appears in a suggestion.
type askSignal struct {
	Score      float64 `json:"score"`
	Support    int     `json:"support,omitempty"`
	Repository string  `json:"repository,omitempty"`
	Period     string  `json:"period,omitempty"`
	Lead       string  `json:"lead,omitempty"`
}

// askSuggestionState is the meta row the background item writes.
type askSuggestionState struct {
	BuiltAt string               `json:"builtAt"`
	Inputs  string               `json:"inputs"`
	TookMs  int64                `json:"tookMs"`
	Signals map[string]askSignal `json:"signals"`
	Timings map[string]int64     `json:"timingsMs,omitempty"`
	Errors  map[string]string    `json:"errors,omitempty"`
}

const askSuggestionsKey = "ask_suggestions"

// askSignalTimeout bounds each signal, so one slow query costs only its own
// area.
var askSignalTimeout = 30 * time.Second

// askSignalRun holds what several signals read, loaded once per run.
type askSignalRun struct {
	c      *Catalog
	server *Server
	now    time.Time
	tools  *askToolWindow
	usage  *askUsageWindow
}

type askSignalFunc func(context.Context, *askSignalRun) (askSignal, error)

// askSignalFuncs implements every signal the catalog names; pharos.unseen
// depends on this Mac's visits, so ranking scores it instead.
var askSignalFuncs = map[string]askSignalFunc{
	"verification.failures":  askVerificationFailures,
	"verification.time":      askVerificationTime,
	"tools.failures":         askToolFailures,
	"tools.time":             askToolTime,
	"context.output":         askContextOutput,
	"context.compaction":     askCompaction,
	"thinking.reasoning":     askReasoning,
	"spend.tokens":           askSpendTokens,
	"spend.change":           askSpendChange,
	"spend.cache":            askSpendCache,
	"models.mix":             askModelMix,
	"delegation.agents":      askDelegation,
	"activity.parallel":      askParallel,
	"activity.days":          askActiveDays,
	"activity.repos":         askRepositories,
	"writing.volume":         askWritingVolume,
	"labels.coverage":        askLabelCoverage,
	"writing.back_and_forth": askBackAndForth,
	"writing.supplied":       askSupplied,
	"agent.friction":         askAgentFriction,
	"agent.reactions":        askAgentReactions,
	"web.hosts":              askWebHosts,
	"web.new_hosts":          askNewHosts,
	"risk.safety":            askSafety,
	"improve.findings":       askOpenFindings,
	"improve.watching":       askWatchedFindings,
	"skills.usage":           askSkills,
	"mcp.calls":              askMCPCalls,
	"carbon.usage":           askCarbon,
	"recall.history":         askRecall,
	"pharos.ask_history":     askHistorySignal,
	"pharos.freshness":       askFreshnessSignal,
	"pharos.backups":         askBackupsSignal,
}

const askUnseenSignal = "pharos.unseen"

// saturate maps a positive amount onto [0, 1), reaching half at half.
func saturate(value, half float64) float64 {
	if value <= 0 {
		return 0
	}
	return value / (value + half)
}

func askDay(now time.Time, daysAgo int) string {
	return now.In(time.Local).AddDate(0, 0, -daysAgo).Format("2006-01-02")
}
func askSince(now time.Time, days int) string {
	return now.UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(timeLayout)
}

// askTop is the largest of counts, with its share of their total.
func askTop(counts map[string]float64) (string, float64) {
	total, best, name := 0.0, 0.0, ""
	for key, value := range counts {
		total += value
		if value > best || value == best && key < name {
			best, name = value, key
		}
	}
	if total <= 0 {
		return "", 0
	}
	return name, best / total
}

// askConcentrated names the repository holding most of counts, when at
// least two named repositories have any and one holds half.
func askConcentrated(counts map[string]float64) string {
	named := 0
	for key, value := range counts {
		if key != "" && value > 0 {
			named++
		}
	}
	name, share := askTop(counts)
	if named < 2 || name == "" || share < 0.5 {
		return ""
	}
	return name
}

func askPeriod(recent, month float64) string {
	if month > 0 && recent/month >= 0.5 {
		return "week"
	}
	return "month"
}

func askTableExists(ctx context.Context, db *sql.DB, name string) bool {
	var found int
	return db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&found) == nil && found > 0
}

// askToolWindow is the Tools rollup's last 28 days, by repository and command
// or tool category; recent is the last 7.
type askToolRow struct {
	recent                                       bool
	repository, command, tool                    string
	calls, errors, truncated, durationMs, tokens float64
}
type askToolWindow struct {
	rows []askToolRow
	// verificationCommands are the test and build commands with the most
	// failures, for the lead.
	verificationCommands []string
}

func (run *askSignalRun) toolWindow(ctx context.Context) (*askToolWindow, error) {
	if run.tools != nil {
		return run.tools, nil
	}
	window := &askToolWindow{}
	rows, err := run.c.reader().QueryContext(ctx, `SELECT day>=?,COALESCE(repository_name,''),COALESCE(command_category,''),COALESCE(tool_category,''),
 SUM(call_count),SUM(error_count),SUM(truncated_count),SUM(total_duration_ms),SUM(result_tokens)
 FROM tool_usage_daily WHERE day>=? GROUP BY 1,2,3,4`, askDay(run.now, 6), askDay(run.now, 27))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row askToolRow
		if err := rows.Scan(&row.recent, &row.repository, &row.command, &row.tool, &row.calls, &row.errors, &row.truncated, &row.durationMs, &row.tokens); err != nil {
			return nil, err
		}
		window.rows = append(window.rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	commands, err := run.c.reader().QueryContext(ctx, `SELECT command_name FROM tool_usage_daily WHERE day>=? AND command_category IN ('test','build')
 AND COALESCE(command_name,'')<>'' GROUP BY command_name HAVING SUM(error_count)>0 ORDER BY SUM(error_count) DESC,command_name LIMIT 3`, askDay(run.now, 27))
	if err != nil {
		return nil, err
	}
	defer commands.Close()
	for commands.Next() {
		var name string
		if err := commands.Scan(&name); err != nil {
			return nil, err
		}
		window.verificationCommands = append(window.verificationCommands, name)
	}
	run.tools = window
	return window, commands.Err()
}

// sum adds up value over the rows keep selects, by repository, with the
// last week's part.
func (w *askToolWindow) sum(keep func(askToolRow) bool, value func(askToolRow) float64) (total, recent float64, byRepository map[string]float64) {
	byRepository = map[string]float64{}
	for _, row := range w.rows {
		if !keep(row) {
			continue
		}
		v := value(row)
		total += v
		if row.recent {
			recent += v
		}
		byRepository[row.repository] += v
	}
	return total, recent, byRepository
}

func verificationRow(row askToolRow) bool { return row.command == "test" || row.command == "build" }
func everyRow(askToolRow) bool            { return true }

func askInRepository(repository string) string {
	if repository == "" {
		return ""
	}
	return ", mostly in " + repository
}

func askVerificationFailures(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	failures, recent, byRepository := w.sum(verificationRow, func(row askToolRow) float64 { return row.errors })
	calls, _, _ := w.sum(verificationRow, func(row askToolRow) float64 { return row.calls })
	if failures < 10 || calls == 0 {
		return askSignal{}, nil
	}
	repository := askConcentrated(byRepository)
	lead := fmt.Sprintf("Test and build commands failed %.0f of %.0f times in the last 28 days%s.", failures, calls, askInRepository(repository))
	if len(w.verificationCommands) > 0 {
		lead += " Most failures: " + strings.Join(w.verificationCommands, ", ") + "."
	}
	return askSignal{Score: saturate(failures, 40) * (0.5 + 0.5*math.Min(1, failures/calls/0.1)), Support: int(failures), Repository: repository, Period: askPeriod(recent, failures), Lead: lead}, nil
}

func askVerificationTime(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	duration := func(row askToolRow) float64 { return row.durationMs / 3.6e6 }
	hours, recent, byRepository := w.sum(verificationRow, duration)
	all, _, _ := w.sum(func(row askToolRow) bool { return row.command != "" }, duration)
	if hours < 0.5 || all == 0 {
		return askSignal{}, nil
	}
	repository := askConcentrated(byRepository)
	return askSignal{Score: saturate(hours, 3) * (0.5 + 0.5*math.Min(1, hours/all/0.15)), Support: int(hours * 60), Repository: repository, Period: askPeriod(recent, hours),
		Lead: fmt.Sprintf("Test and build commands ran for %.1f hours in the last 28 days, %.0f%% of recorded command time%s.", hours, 100*hours/all, askInRepository(repository))}, nil
}

func askToolFailures(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	failures, recent, byRepository := w.sum(everyRow, func(row askToolRow) float64 { return row.errors })
	calls, _, _ := w.sum(everyRow, func(row askToolRow) float64 { return row.calls })
	if failures < 20 {
		return askSignal{}, nil
	}
	// The last week against the three before it, per week.
	before := (failures - recent) / 3
	score := saturate(failures, 100)
	if recent >= 10 && before > 0 {
		score = math.Max(score, saturate(math.Abs(math.Log2(recent/before)), 1))
	}
	repository := askConcentrated(byRepository)
	return askSignal{Score: score, Support: int(failures), Repository: repository, Period: askPeriod(recent, failures),
		Lead: fmt.Sprintf("%.0f of %.0f tool calls failed in the last 28 days (%.0f in the last week, about %.0f a week before)%s.", failures, calls, recent, before, askInRepository(repository))}, nil
}

func askToolTime(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	hours, recent, byRepository := w.sum(everyRow, func(row askToolRow) float64 { return row.durationMs / 3.6e6 })
	if hours < 1 {
		return askSignal{}, nil
	}
	byCommand := map[string]float64{}
	for _, row := range w.rows {
		if row.command != "" {
			byCommand[row.command] += row.durationMs / 3.6e6
		}
	}
	top, share := askTop(byCommand)
	repository := askConcentrated(byRepository)
	return askSignal{Score: saturate(hours, 10), Support: int(hours * 60), Repository: repository, Period: askPeriod(recent, hours),
		Lead: fmt.Sprintf("Tool calls ran for %.1f hours in the last 28 days; %s commands took %.0f%% of command time%s.", hours, defaultString(top, "uncategorized"), 100*share, askInRepository(repository))}, nil
}

func askContextOutput(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	truncated, recent, byRepository := w.sum(everyRow, func(row askToolRow) float64 { return row.truncated })
	tokens, _, _ := w.sum(everyRow, func(row askToolRow) float64 { return row.tokens })
	if truncated < 5 {
		return askSignal{}, nil
	}
	repository := askConcentrated(byRepository)
	return askSignal{Score: saturate(truncated, 30), Support: int(truncated), Repository: repository, Period: askPeriod(recent, truncated),
		Lead: fmt.Sprintf("%.0f tool outputs were truncated in the last 28 days; tool results added about %.1fM tokens%s.", truncated, tokens/1e6, askInRepository(repository))}, nil
}

// Compactions use model_requests_compacted_idx, which holds only requests
// after a compaction.
func askCompaction(ctx context.Context, run *askSignalRun) (askSignal, error) {
	rows, err := run.c.reader().QueryContext(ctx, `SELECT COALESCE(r.display_name,''),COUNT(*),COUNT(DISTINCT m.conversation_id),SUM(m.requested_at>=?)
 FROM model_requests m JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id
 WHERE m.requested_at>=? AND m.compacted_before>0 GROUP BY 1`, askSince(run.now, 7), askSince(run.now, 28))
	if err != nil {
		return askSignal{}, err
	}
	defer rows.Close()
	byRepository := map[string]float64{}
	total, conversations, recent := 0.0, 0, 0.0
	for rows.Next() {
		var repository string
		var count, distinct, late float64
		if err := rows.Scan(&repository, &count, &distinct, &late); err != nil {
			return askSignal{}, err
		}
		byRepository[repository] += count
		total += count
		conversations += int(distinct)
		recent += late
	}
	if err := rows.Err(); err != nil || total < 3 || conversations < 2 {
		return askSignal{}, err
	}
	repository := askConcentrated(byRepository)
	return askSignal{Score: saturate(total, 20), Support: conversations, Repository: repository, Period: askPeriod(recent, total),
		Lead: fmt.Sprintf("Context was compacted %.0f times across %d conversations in the last 28 days%s.", total, conversations, askInRepository(repository))}, nil
}

// askUsageWindow is recorded token use over the last 28 days, by repository,
// model and delegation depth; recent is the last 7 and before the 28 days
// ahead of the window, for the change.
type askUsageRow struct {
	recent, delegated                           bool
	repository, model                           string
	tokens, input, cacheRead, output, reasoning float64
}
type askUsageWindow struct {
	rows   []askUsageRow
	before float64
}

func (run *askSignalRun) usageWindow(ctx context.Context) (*askUsageWindow, error) {
	if run.usage != nil {
		return run.usage, nil
	}
	window := &askUsageWindow{}
	rows, err := run.c.reader().QueryContext(ctx, `SELECT u.usage_hour>=?,s.depth>0,COALESCE(r.display_name,''),u.model,
 SUM(u.total_tokens),SUM(u.input_tokens),SUM(u.cache_read_input_tokens),SUM(u.output_tokens),SUM(u.reasoning_output_tokens)
 FROM agent_session_usage u JOIN agent_sessions s ON s.id=u.agent_session_id JOIN workspaces w ON w.id=s.workspace_id
 LEFT JOIN repositories r ON r.id=w.repository_id WHERE u.usage_hour>=? GROUP BY 1,2,3,4`, askSince(run.now, 7), askSince(run.now, 28))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row askUsageRow
		if err := rows.Scan(&row.recent, &row.delegated, &row.repository, &row.model, &row.tokens, &row.input, &row.cacheRead, &row.output, &row.reasoning); err != nil {
			return nil, err
		}
		window.rows = append(window.rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := run.c.reader().QueryRowContext(ctx, "SELECT COALESCE(SUM(total_tokens),0) FROM agent_session_usage WHERE usage_hour>=? AND usage_hour<?",
		askSince(run.now, 56), askSince(run.now, 28)).Scan(&window.before); err != nil {
		return nil, err
	}
	run.usage = window
	return window, nil
}

func (w *askUsageWindow) total(value func(askUsageRow) float64) (total, recent float64, byRepository map[string]float64) {
	byRepository = map[string]float64{}
	for _, row := range w.rows {
		v := value(row)
		total += v
		if row.recent {
			recent += v
		}
		byRepository[row.repository] += v
	}
	return total, recent, byRepository
}

func tokensOf(row askUsageRow) float64 { return row.tokens }

func askSpendTokens(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	tokens, recent, byRepository := w.total(tokensOf)
	if tokens < 1e6 {
		return askSignal{}, nil
	}
	repository := askConcentrated(byRepository)
	return askSignal{Score: 0.4 + 0.4*saturate(tokens, 5e8), Support: int(tokens / 1e6), Repository: repository, Period: askPeriod(recent, tokens),
		Lead: fmt.Sprintf("%.1fM tokens were recorded in the last 28 days%s.", tokens/1e6, askInRepository(repository))}, nil
}

// A week against the four before it, or this month against the last.
func askSpendChange(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	tokens, recent, _ := w.total(tokensOf)
	weekly := (tokens - recent) / 3
	best, period, lead := 0.0, "", ""
	if recent >= 1e6 && weekly >= 1e6 {
		best, period = saturate(math.Abs(math.Log2(recent/weekly))-0.5, 0.5), "week"
		lead = fmt.Sprintf("%.1fM tokens in the last 7 days against about %.1fM a week in the three weeks before.", recent/1e6, weekly/1e6)
	}
	if tokens >= 1e6 && w.before >= 1e6 {
		if score := saturate(math.Abs(math.Log2(tokens/w.before))-0.5, 0.5); score > best {
			best, period = score, "month"
			lead = fmt.Sprintf("%.1fM tokens in the last 28 days against %.1fM in the 28 days before.", tokens/1e6, w.before/1e6)
		}
	}
	if best < 0.05 {
		return askSignal{}, nil
	}
	return askSignal{Score: best, Support: int(tokens / 1e6), Period: period, Lead: lead}, nil
}

func askSpendCache(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	input, _, _ := w.total(func(row askUsageRow) float64 { return row.input })
	cached, _, _ := w.total(func(row askUsageRow) float64 { return row.cacheRead })
	if input < 1e7 {
		return askSignal{}, nil
	}
	hit := math.Min(1, cached/input)
	return askSignal{Score: 0.3 + 0.5*(1-hit), Support: int(input / 1e6), Lead: fmt.Sprintf("%.0f%% of %.1fM input tokens in the last 28 days were cache reads.", 100*hit, input/1e6)}, nil
}

func askReasoning(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	reasoning, recent, byRepository := w.total(func(row askUsageRow) float64 { return row.reasoning })
	output, _, _ := w.total(func(row askUsageRow) float64 { return row.output })
	if reasoning < 1e5 || output <= 0 {
		return askSignal{}, nil
	}
	share := math.Min(1, reasoning/output)
	return askSignal{Score: 0.25 + 0.5*saturate(share, 0.2), Support: int(reasoning / 1e3), Repository: askConcentrated(byRepository), Period: askPeriod(recent, reasoning),
		Lead: fmt.Sprintf("Reasoning was %.0f%% of %.1fM output tokens in the last 28 days.", 100*share, output/1e6)}, nil
}

func askModelMix(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	byModel := map[string]float64{}
	total := 0.0
	for _, row := range w.rows {
		if row.model != "" {
			byModel[row.model] += row.tokens
			total += row.tokens
		}
	}
	used := []string{}
	for model, tokens := range byModel {
		if total > 0 && tokens/total >= 0.05 {
			used = append(used, model)
		}
	}
	if total < 1e6 || len(used) < 2 {
		return askSignal{}, nil
	}
	sort.Slice(used, func(i, j int) bool {
		return byModel[used[i]] > byModel[used[j]] || byModel[used[i]] == byModel[used[j]] && used[i] < used[j]
	})
	return askSignal{Score: math.Min(0.8, 0.3+0.15*float64(len(used)-1)), Support: len(used), Lead: "Models with at least 5% of tokens in the last 28 days: " + strings.Join(used, ", ") + "."}, nil
}

func askDelegation(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	tokens, _, _ := w.total(tokensOf)
	delegated, recent, byRepository := w.total(func(row askUsageRow) float64 {
		if row.delegated {
			return row.tokens
		}
		return 0
	})
	if tokens <= 0 || delegated/tokens < 0.05 {
		return askSignal{}, nil
	}
	share := delegated / tokens
	return askSignal{Score: saturate(share, 0.25), Support: int(delegated / 1e6), Repository: askConcentrated(byRepository), Period: askPeriod(recent, delegated),
		Lead: fmt.Sprintf("Subagents and other delegated runs used %.0f%% of tokens in the last 28 days.", 100*share)}, nil
}

// Hours in which two or more top-level conversations recorded usage.
func askParallel(ctx context.Context, run *askSignalRun) (askSignal, error) {
	var busy, peak int
	err := run.c.reader().QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(n),0) FROM (SELECT COUNT(DISTINCT s.conversation_id) n
 FROM agent_session_usage u JOIN agent_sessions s ON s.id=u.agent_session_id WHERE u.usage_hour>=? AND s.depth=0 GROUP BY u.usage_hour) WHERE n>=2`,
		askSince(run.now, 28)).Scan(&busy, &peak)
	if err != nil || busy < 5 {
		return askSignal{}, err
	}
	return askSignal{Score: saturate(float64(busy), 40), Support: busy,
		Lead: fmt.Sprintf("In %d hours of the last 28 days two or more conversations were active at once; at most %d.", busy, peak)}, nil
}

func askActiveDays(ctx context.Context, run *askSignalRun) (askSignal, error) {
	var days int
	if err := run.c.reader().QueryRowContext(ctx, "SELECT COUNT(DISTINCT day) FROM tool_usage_daily WHERE day>=? AND call_count>0", askDay(run.now, 27)).Scan(&days); err != nil || days < 7 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.25 + 0.3*saturate(float64(days-7), 7), Support: days, Lead: fmt.Sprintf("Agents worked on %d of the last 28 days.", days)}, nil
}

func askRepositories(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	_, _, byRepository := w.sum(everyRow, func(row askToolRow) float64 { return row.calls })
	recentRepositories, olderRepositories := map[string]bool{}, map[string]bool{}
	for _, row := range w.rows {
		if row.repository == "" || row.calls == 0 {
			continue
		}
		if row.recent {
			recentRepositories[row.repository] = true
		} else {
			olderRepositories[row.repository] = true
		}
	}
	named := []string{}
	for name, calls := range byRepository {
		if name != "" && calls > 0 {
			named = append(named, name)
		}
	}
	if len(named) == 0 {
		return askSignal{}, nil
	}
	sort.Slice(named, func(i, j int) bool {
		return byRepository[named[i]] > byRepository[named[j]] || byRepository[named[i]] == byRepository[named[j]] && named[i] < named[j]
	})
	fresh := 0
	for name := range recentRepositories {
		if !olderRepositories[name] {
			fresh++
		}
	}
	score := 0.3 + 0.08*math.Min(float64(len(named)), 5) + 0.1*math.Min(float64(fresh), 2)
	return askSignal{Score: score, Support: len(named), Period: askPeriod(float64(len(recentRepositories)), float64(len(named))),
		Lead: fmt.Sprintf("Active repositories in the last 28 days, busiest first: %s.", strings.Join(named[:min(len(named), 6)], ", "))}, nil
}

// askWriting is the user's messages over the last 28 days, by conversation.
type askWriting struct {
	messages, typedMessages, typed, supplied float64
	repeated                                 int
	repeatedByRepository                     map[string]float64
}

func (run *askSignalRun) writing(ctx context.Context) (askWriting, error) {
	out := askWriting{repeatedByRepository: map[string]float64{}}
	rows, err := run.c.reader().QueryContext(ctx, `SELECT COALESCE(r.display_name,''),COUNT(*),SUM(a.typed_words>0),SUM(a.typed_words),SUM(a.pasted_words+a.attachment_words)
 FROM message_authorship a JOIN workspaces w ON w.id=a.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id
 WHERE a.day>=? GROUP BY a.conversation_id`, askDay(run.now, 27))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var repository string
		var messages, typedMessages, typed, supplied float64
		if err := rows.Scan(&repository, &messages, &typedMessages, &typed, &supplied); err != nil {
			return out, err
		}
		out.messages += messages
		out.typedMessages += typedMessages
		out.typed += typed
		out.supplied += supplied
		// Six or more typed messages in one conversation is a lot of steering.
		if typedMessages >= 6 {
			out.repeated++
			out.repeatedByRepository[repository]++
		}
	}
	return out, rows.Err()
}

func askWritingVolume(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.writing(ctx)
	if err != nil || w.typed < 500 || w.typedMessages < 10 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.3 + 0.4*saturate(w.typed, 5000), Support: int(w.typedMessages),
		Lead: fmt.Sprintf("You typed or dictated %.0f words in %.0f messages in the last 28 days.", w.typed, w.typedMessages)}, nil
}

func askBackAndForth(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.writing(ctx)
	if err != nil || w.repeated < 3 {
		return askSignal{}, err
	}
	repository := askConcentrated(w.repeatedByRepository)
	return askSignal{Score: saturate(float64(w.repeated), 15), Support: w.repeated, Repository: repository,
		Lead: fmt.Sprintf("%d conversations in the last 28 days had six or more of your typed messages%s.", w.repeated, askInRepository(repository))}, nil
}

func askSupplied(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.writing(ctx)
	if err != nil || w.supplied < 1000 {
		return askSignal{}, err
	}
	// Of what you wrote or brought, not of harness and automated text.
	share := w.supplied / (w.supplied + w.typed)
	return askSignal{Score: saturate(share, 0.3), Support: int(w.supplied),
		Lead: fmt.Sprintf("Pasted text and attachments were %.0f%% of the words you wrote or supplied in the last 28 days.", 100*share)}, nil
}

var askFrictionKinds = []string{"obstacle", "env_friction", "stale_guidance", "preexisting_failure", "missing_capability", "ambiguous_request", "stuck", "gave_up", "confusion"}
var askReactionKinds = []string{"discovery", "surprise", "own_mistake", "unblocking_insight"}

func (run *askSignalRun) agentEvents(ctx context.Context, kinds []string) (askSignal, error) {
	if !askTableExists(ctx, run.c.reader(), "agent_events") {
		return askSignal{}, nil
	}
	// Only events the verifier is confident of, as everywhere else they are
	// shown; one conversation with several kinds counts once.
	query := `SELECT e.conversation_id,COALESCE(r.display_name,''),e.family,COUNT(*) FROM agent_events e
 JOIN conversations c ON c.id=e.conversation_id JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id
 WHERE e.day>=? AND e.probability>=? AND e.family IN (` + strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",") + `) GROUP BY 1,2,3`
	args := []any{askDay(run.now, 27), agentEventThreshold}
	for _, kind := range kinds {
		args = append(args, kind)
	}
	rows, err := run.c.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return askSignal{}, err
	}
	defer rows.Close()
	byRepository, byKind := map[string]float64{}, map[string]float64{}
	seen := map[string]bool{}
	for rows.Next() {
		var conversation, repository, kind string
		var count float64
		if err := rows.Scan(&conversation, &repository, &kind, &count); err != nil {
			return askSignal{}, err
		}
		byRepository[repository] += count
		byKind[kind] += count
		seen[conversation] = true
	}
	conversations := len(seen)
	if err := rows.Err(); err != nil || conversations < 3 {
		return askSignal{}, err
	}
	top, _ := askTop(byKind)
	repository := askConcentrated(byRepository)
	return askSignal{Score: saturate(float64(conversations), 20), Support: conversations, Repository: repository,
		Lead: fmt.Sprintf("Agent events of these kinds appeared in %d conversations in the last 28 days; the most common was %s%s.", conversations, top, askInRepository(repository))}, nil
}

func askAgentFriction(ctx context.Context, run *askSignalRun) (askSignal, error) {
	return run.agentEvents(ctx, askFrictionKinds)
}
func askAgentReactions(ctx context.Context, run *askSignalRun) (askSignal, error) {
	return run.agentEvents(ctx, askReactionKinds)
}

func askWebHosts(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	calls, recent, _ := w.sum(func(row askToolRow) bool { return row.tool == "web" }, func(row askToolRow) float64 { return row.calls })
	if calls < 20 {
		return askSignal{}, nil
	}
	return askSignal{Score: 0.3 + 0.4*saturate(calls, 300), Support: int(calls), Period: askPeriod(recent, calls),
		Lead: fmt.Sprintf("Agents made %.0f web tool calls in the last 28 days.", calls)}, nil
}

// Hosts agents reached in the last week, never before. Search results are
// links a search returned, not sites an agent opened.
func askNewHosts(ctx context.Context, run *askSignalRun) (askSignal, error) {
	since := askSince(run.now, 7)
	rows, err := run.c.reader().QueryContext(ctx, `SELECT u.host FROM tool_calls t JOIN tool_urls u ON u.tool_call_id=t.id
 WHERE t.started_at>=? AND u.source<>'search_result' AND COALESCE(u.host,'')<>'' GROUP BY u.host
 HAVING NOT EXISTS (SELECT 1 FROM tool_urls o JOIN tool_calls p ON p.id=o.tool_call_id WHERE o.host=u.host AND o.source<>'search_result' AND p.started_at<?)
 ORDER BY u.host`, since, since)
	if err != nil {
		return askSignal{}, err
	}
	defer rows.Close()
	hosts := []string{}
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return askSignal{}, err
		}
		if askPublicHost(host) {
			hosts = append(hosts, host)
		}
	}
	if err := rows.Err(); err != nil || len(hosts) < 3 {
		return askSignal{}, err
	}
	return askSignal{Score: saturate(float64(len(hosts)), 15), Support: len(hosts), Period: "week",
		Lead: fmt.Sprintf("%d hosts were first reached in the last 7 days, including %s.", len(hosts), strings.Join(hosts[:min(len(hosts), 8)], ", "))}, nil
}

// askPublicHost leaves out this Mac, local networks and bare addresses:
// development servers are new every day.
func askPublicHost(host string) bool {
	name := strings.ToLower(host)
	if split, _, err := net.SplitHostPort(name); err == nil {
		name = split
	}
	name = strings.Trim(name, "[]")
	return name != "" && net.ParseIP(name) == nil && name != "localhost" && strings.Contains(name, ".") &&
		!strings.HasSuffix(name, ".localhost") && !strings.HasSuffix(name, ".local") && !strings.HasSuffix(name, ".internal")
}

func askSafety(ctx context.Context, run *askSignalRun) (askSignal, error) {
	if !askTableExists(ctx, run.c.reader(), "safety_cases") {
		return askSignal{}, nil
	}
	var open int
	if err := run.c.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM safety_cases c LEFT JOIN safety_state s ON s.case_id=c.id
 WHERE COALESCE(s.state,'open')='open' OR (s.state='handed_off' AND c.completion>s.handled_completion)`).Scan(&open); err != nil || open == 0 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.5 + 0.5*saturate(float64(open), 5), Support: open, Lead: fmt.Sprintf("Optimize → Risky actions has %d open reviews.", open)}, nil
}

// findingsSummary reads Optimize's counts once findings have been built;
// before then there is nothing to recommend.
func (run *askSignalRun) findingsSummary(ctx context.Context) (map[string]any, error) {
	if built, _ := run.c.metaValue(ctx, "findings_generation"); built == "" {
		return nil, nil
	}
	overview, err := run.c.FindingsOverview(ctx, "summary")
	if err != nil {
		return nil, err
	}
	summary, _ := overview["summary"].(map[string]any)
	return summary, nil
}

func askOpenFindings(ctx context.Context, run *askSignalRun) (askSignal, error) {
	summary, err := run.findingsSummary(ctx)
	open := int(floatOr(summary["open"]))
	if err != nil || open == 0 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.5 + 0.5*saturate(float64(open), 5), Support: open, Lead: fmt.Sprintf("Optimize lists %d open findings.", open)}, nil
}

func askWatchedFindings(ctx context.Context, run *askSignalRun) (askSignal, error) {
	summary, err := run.findingsSummary(ctx)
	watching := int(floatOr(summary["watching"]))
	if err != nil || watching == 0 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.6, Support: watching, Lead: fmt.Sprintf("Optimize is measuring %d findings you acted on.", watching)}, nil
}

func askSkills(ctx context.Context, run *askSignalRun) (askSignal, error) {
	var uses, skills int
	if err := run.c.reader().QueryRowContext(ctx, "SELECT COUNT(*),COUNT(DISTINCT skill_name) FROM skill_usages WHERE created_at>=?", askSince(run.now, 28)).Scan(&uses, &skills); err != nil || uses < 5 || skills < 2 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.3 + 0.4*saturate(float64(uses), 100), Support: uses, Lead: fmt.Sprintf("%d skill uses of %d skills were recorded in the last 28 days.", uses, skills)}, nil
}

func askMCPCalls(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.toolWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	calls, recent, _ := w.sum(func(row askToolRow) bool { return row.tool == "mcp" }, func(row askToolRow) float64 { return row.calls })
	var served float64
	if err := run.c.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_calls WHERE called_at>=?", askSince(run.now, 28)).Scan(&served); err != nil {
		return askSignal{}, err
	}
	if calls+served < 10 {
		return askSignal{}, nil
	}
	return askSignal{Score: 0.3 + 0.4*saturate(calls+served, 200), Support: int(calls + served), Period: askPeriod(recent, calls),
		Lead: fmt.Sprintf("Agents made %.0f MCP tool calls in the last 28 days; Pharos's own MCP server answered %.0f.", calls, served)}, nil
}

func askCarbon(ctx context.Context, run *askSignalRun) (askSignal, error) {
	w, err := run.usageWindow(ctx)
	if err != nil {
		return askSignal{}, err
	}
	if tokens, _, _ := w.total(tokensOf); tokens < 1e6 {
		return askSignal{}, nil
	}
	return askSignal{Score: 0.35, Support: 1}, nil
}

// Enough older work that finding something in it is worth a question.
func askRecall(ctx context.Context, run *askSignalRun) (askSignal, error) {
	var older int
	if err := run.c.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM workspaces WHERE activity_at<?", askSince(run.now, 14)).Scan(&older); err != nil || older < 30 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.35 + 0.3*saturate(float64(older), 500), Support: older}, nil
}

func askHistorySignal(ctx context.Context, run *askSignalRun) (askSignal, error) {
	var chats int
	if err := run.c.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM ask_sessions WHERE json_array_length(value,'$.conversation')>0").Scan(&chats); err != nil || chats < 3 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.3, Support: chats}, nil
}

// Domains behind with nothing catching them up.
func askFreshnessSignal(ctx context.Context, run *askSignalRun) (askSignal, error) {
	if run.server == nil {
		return askSignal{}, nil
	}
	// The running and queued operations, so a domain being caught up is not
	// reported as left behind.
	_, views := run.server.operationsDocumentViews(ctx, false)
	behind := []string{}
	for _, row := range run.server.freshness(ctx, views, true) {
		if row.Behind.State == "behind" && row.CatchingUp == nil {
			behind = append(behind, row.Label)
		}
	}
	if len(behind) == 0 {
		return askSignal{}, nil
	}
	return askSignal{Score: 0.7, Support: len(behind), Lead: "Behind with nothing catching up: " + strings.Join(behind, ", ") + "."}, nil
}

// A library of real size with neither a local nor a cloud backup.
func askBackupsSignal(ctx context.Context, run *askSignalRun) (askSignal, error) {
	if run.server == nil || len(loadBackupHistory(run.server.Config()).Backups) > 0 {
		return askSignal{}, nil
	}
	var destinations, workspaces int
	if err := run.c.reader().QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM cloud_destinations),(SELECT COUNT(*) FROM workspaces)").Scan(&destinations, &workspaces); err != nil || destinations > 0 || workspaces < 50 {
		return askSignal{}, err
	}
	return askSignal{Score: 0.45, Support: workspaces, Lead: "This library has no local or cloud backup on record."}, nil
}

// askSuggestionInputs changes when anything a signal reads may have: an
// analysis stage completing, safety reviews being rebuilt, or the day.
func (c *Catalog) askSuggestionInputs(ctx context.Context, now time.Time) string {
	parts := []string{askDay(now, 0)}
	if deferred, err := c.freshnessDeferred(ctx); err == nil {
		for _, stage := range stageOrder {
			parts = append(parts, fmt.Sprintf("%s:%d", stage, deferred[stage].completed))
		}
	}
	for _, key := range []string{"safety_built_at", "findings_generation", "findings_measured_at"} {
		value, _ := c.metaValue(ctx, key)
		parts = append(parts, value)
	}
	return strings.Join(parts, "|")
}

func (c *Catalog) askSuggestionState(ctx context.Context) (askSuggestionState, bool) {
	var state askSuggestionState
	raw, err := c.metaValue(ctx, askSuggestionsKey)
	if err != nil || raw == "" || json.Unmarshal([]byte(raw), &state) != nil {
		return askSuggestionState{}, false
	}
	return state, true
}

// askSuggestionsStale is whether the background item has work: no stored
// signals, or inputs that changed since.
func (c *Catalog) askSuggestionsStale(ctx context.Context, now time.Time) bool {
	state, ok := c.askSuggestionState(ctx)
	return !ok || state.Inputs != c.askSuggestionInputs(ctx, now)
}

// computeAskSignals runs every signal, each with its own deadline. A failed
// signal leaves its areas out rather than failing the run.
func computeAskSignals(ctx context.Context, c *Catalog, server *Server, now time.Time) askSuggestionState {
	started := time.Now()
	run := &askSignalRun{c: c, server: server, now: now}
	state := askSuggestionState{Signals: map[string]askSignal{}, Timings: map[string]int64{}}
	names := make([]string, 0, len(askSignalFuncs))
	for name := range askSignalFuncs {
		names = append(names, name)
	}
	sort.Strings(names)
	reportWork(ctx, "Checking Ask suggestion signals", "check", 0, len(names))
	for index, name := range names {
		if ctx.Err() != nil {
			break
		}
		at := time.Now()
		signalCtx, cancel := context.WithTimeout(ctx, askSignalTimeout)
		signal, err := askSignalFuncs[name](signalCtx, run)
		cancel()
		state.Timings[name] = time.Since(at).Milliseconds()
		reportWork(ctx, "Checking Ask suggestion signals", "check", index+1, len(names))
		if err != nil {
			if state.Errors == nil {
				state.Errors = map[string]string{}
			}
			state.Errors[name] = err.Error()
			continue
		}
		if signal.Score > 0 {
			signal.Score = math.Round(math.Min(1, signal.Score)*1000) / 1000
			state.Signals[name] = signal
		}
	}
	state.TookMs = time.Since(started).Milliseconds()
	return state
}

// refreshAskSuggestions is the background item: compute and store.
func (s *Server) refreshAskSuggestions(ctx context.Context) error {
	now := time.Now()
	inputs := s.Catalog.askSuggestionInputs(ctx, now)
	state := computeAskSignals(ctx, s.Catalog, s, now)
	if err := ctx.Err(); err != nil {
		return err
	}
	state.BuiltAt, state.Inputs = now.UTC().Format(timeLayout), inputs
	reportWork(ctx, "Saving Ask suggestion signals", "", 0, -1)
	return s.Catalog.writeTransaction(ctx, "ask-suggestions", func(tx *sql.Tx) error { return setMeta(tx, askSuggestionsKey, jsonText(state)) })
}

// askRankInput is what ranking reads besides the stored signals.
type askRankInput struct {
	Seed int
	Now  time.Time
	// Page is the site map page the user is on, if any.
	Page string
	// Visited is each site map page's latest visit on this Mac; Visits how
	// many visits are retained, since few visits say little about what is
	// unseen.
	Visited map[string]time.Time
	Visits  int
	Asked   []askAsked
	// Repositories counts named repositories, for scoping.
	Repositories int
}

// askAsked is an earlier Ask question on this Mac.
type askAsked struct {
	Family, Prompt string
	At             time.Time
	// Answered is whether it got destinations, nil while unknown.
	Answered *bool
}

// askSuggestion is one offered question. Text is all the user sees.
type askSuggestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Area string `json:"area"`
	Form string `json:"form"`
}

// askSlateSize is the most questions offered at once.
const askSlateSize = 12

// askWords are a question's longer words, for comparing questions.
func askWords(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
		if len(word) > 3 {
			words[word] = true
		}
	}
	return words
}

func askSimilar(a, b string) float64 {
	left, right := askWords(a), askWords(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	shared := 0
	for word := range left {
		if right[word] {
			shared++
		}
	}
	return float64(shared) / float64(len(left)+len(right)-shared)
}

// askHash is a stable number in [0, 1) for a seed and parts.
func askHash(parts ...any) float64 {
	h := fnv.New64a()
	fmt.Fprint(h, parts...)
	return float64(h.Sum64()>>11) / float64(1<<53)
}

// askOutcomes is each family's answered share, smoothed toward 75%, as a
// multiplier on its prior.
func askOutcomes(asked []askAsked) map[string]float64 {
	answered, total := map[string]float64{}, map[string]float64{}
	for _, item := range asked {
		if item.Family == "" || item.Answered == nil {
			continue
		}
		total[item.Family]++
		if *item.Answered {
			answered[item.Family]++
		}
	}
	out := map[string]float64{}
	for family, n := range total {
		out[family] = math.Max(0.5, math.Min(1.2, (answered[family]+1.5)/(n+2)/0.75))
	}
	return out
}

// rankAskSuggestions chooses the questions to offer. The same signals and
// input always give the same slate; Seed varies it for Refresh.
func rankAskSuggestions(catalog askSuggestionCatalog, state askSuggestionState, in askRankInput) []askSuggestion {
	areas := map[string]askSuggestionArea{}
	for _, area := range catalog.Areas {
		areas[area.ID] = area
	}
	signal := func(name string) askSignal { return state.Signals[name] }
	// An area worth asking about whose pages have all gone unvisited, among
	// the visits retained here.
	unseen := 0
	if in.Visits >= 20 {
		for _, area := range catalog.Areas {
			if area.ID == "discovery" || area.ID == "library-health" || area.ID == "backups" {
				continue
			}
			best := 0.0
			for _, name := range area.Signals {
				best = math.Max(best, signal(name).Score)
			}
			visited := false
			for _, page := range area.Pages {
				_, seen := in.Visited[page]
				visited = visited || seen
			}
			if best >= 0.3 && !visited {
				unseen++
			}
		}
	}
	signals := map[string]askSignal{}
	for name, value := range state.Signals {
		signals[name] = value
	}
	if unseen >= 3 {
		signals[askUnseenSignal] = askSignal{Score: 0.4 + 0.4*saturate(float64(unseen-2), 3), Support: unseen}
	}
	outcomes := askOutcomes(in.Asked)
	type candidate struct {
		family askSuggestionFamily
		signal askSignal
		score  float64
		key    float64
	}
	candidates := []candidate{}
	for _, family := range catalog.Families {
		area, ok := areas[family.Area]
		if !ok {
			continue
		}
		names := family.Signals
		if len(names) == 0 {
			names = area.Signals
		}
		var best askSignal
		for _, name := range names {
			if value := signals[name]; value.Score > best.Score {
				best = value
			}
		}
		if best.Score <= 0 {
			continue
		}
		score := family.Prior * best.Score
		if multiplier, ok := outcomes[family.ID]; ok {
			score *= multiplier
		}
		recent := false
		for _, page := range area.Pages {
			if page == in.Page {
				score *= 1.3
			}
			if at, ok := in.Visited[page]; ok && in.Now.Sub(at) < 7*24*time.Hour {
				recent = true
			}
		}
		if recent {
			score *= 1.15
		}
		for _, asked := range in.Asked {
			if in.Now.Sub(asked.At) > 14*24*time.Hour {
				continue
			}
			if asked.Family == family.ID {
				score *= 0.2
				break
			}
			for _, variant := range family.Variants {
				if askSimilar(asked.Prompt, variant) >= 0.5 {
					score *= 0.4
					break
				}
			}
		}
		// Weighted sampling without replacement: a seeded draw scaled by
		// score, so stronger questions usually lead and Refresh reshuffles.
		draw := askHash(in.Seed, askDay(in.Now, 0), family.ID)
		candidates = append(candidates, candidate{family: family, signal: best, score: score, key: math.Pow(draw, 1/score)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].key != candidates[j].key {
			return candidates[i].key > candidates[j].key
		}
		return candidates[i].family.ID < candidates[j].family.ID
	})
	slate := []askSuggestion{}
	perArea := map[string]int{}
	taken := map[string]bool{}
	// One question per area, never three of one form in a row; a second
	// pass allows a second per area, never two in a row.
	for pass := 0; pass < 2 && len(slate) < askSlateSize; pass++ {
		for _, item := range candidates {
			if len(slate) >= askSlateSize {
				break
			}
			family := item.family
			if taken[family.ID] || perArea[family.Area] > pass {
				continue
			}
			if len(slate) > 0 {
				last := slate[len(slate)-1]
				if last.Area == family.Area || pass == 0 && len(slate) > 1 && last.Form == family.Form && slate[len(slate)-2].Form == family.Form {
					continue
				}
			}
			text := askSuggestionText(family, item.signal, in)
			if text == "" {
				continue
			}
			taken[family.ID] = true
			perArea[family.Area]++
			slate = append(slate, askSuggestion{ID: family.ID, Text: text, Area: family.Area, Form: family.Form})
		}
	}
	return slate
}

// askSuggestionText fills a seeded variant's scope and period. A scoped
// variant needs a repository holding most of what the signal saw, in a
// library with several; otherwise its scope is left out.
func askSuggestionText(family askSuggestionFamily, signal askSignal, in askRankInput) string {
	if len(family.Variants) == 0 {
		return ""
	}
	text := family.Variants[int(askHash(in.Seed, family.ID, "variant")*float64(len(family.Variants)))]
	scope := ""
	if signal.Repository != "" && in.Repositories >= 2 && askHash(in.Seed, family.ID, "scope") < 0.6 {
		scope = " in the " + signal.Repository + " repository"
	}
	period := map[string]string{"week": "this week", "month": "this month"}[signal.Period]
	if period == "" {
		period = "lately"
	}
	return strings.NewReplacer("{scope}", scope, "{period}", period).Replace(text)
}

// askRankInputFor reads this Mac's visits and Ask history.
func (s *Server) askRankInputFor(ctx context.Context, page string, seed int) (askRankInput, error) {
	in := askRankInput{Seed: seed, Now: time.Now(), Visited: map[string]time.Time{}}
	if page != "" && askSafeURL(page) {
		in.Page = askViewOf(page).Page
	}
	visits, err := s.Catalog.reader().QueryContext(ctx, "SELECT url,at FROM ask_visits WHERE host_id=? ORDER BY at DESC LIMIT ?", currentHost().ID, askVisitLimit)
	if err != nil {
		return in, err
	}
	for visits.Next() {
		var raw string
		var at int64
		if err := visits.Scan(&raw, &at); err != nil {
			visits.Close()
			return in, err
		}
		in.Visits++
		if view := askViewOf(raw); view.Page != "" {
			if old, ok := in.Visited[view.Page]; !ok || time.UnixMilli(at).After(old) {
				in.Visited[view.Page] = time.UnixMilli(at)
			}
		}
	}
	visits.Close()
	if err := visits.Err(); err != nil {
		return in, err
	}
	sessions, err := s.Catalog.reader().QueryContext(ctx, "SELECT value FROM ask_sessions WHERE host_id=? ORDER BY updated_at DESC LIMIT 100", currentHost().ID)
	if err != nil {
		return in, err
	}
	defer sessions.Close()
	for sessions.Next() {
		var raw string
		if err := sessions.Scan(&raw); err != nil {
			return in, err
		}
		var session askSession
		if json.Unmarshal([]byte(raw), &session) != nil {
			continue
		}
		for _, turn := range session.Conversation {
			asked := askAsked{Family: turn.Suggestion, Prompt: turn.Prompt, At: time.UnixMilli(turn.StartedAt)}
			switch turn.Status {
			case "single", "multiple":
				answered := len(turn.Cards) > 0
				asked.Answered = &answered
			case "error":
				answered := false
				asked.Answered = &answered
			}
			if turn.SuggestionEdited {
				asked.Answered = nil
			}
			in.Asked = append(in.Asked, asked)
		}
	}
	if err := sessions.Err(); err != nil {
		return in, err
	}
	err = s.Catalog.reader().QueryRowContext(ctx, `SELECT COUNT(DISTINCT repository_name) FROM tool_usage_daily WHERE day>=? AND COALESCE(repository_name,'')<>''`, askDay(in.Now, 27)).Scan(&in.Repositories)
	return in, err
}

// askSuggestionsHTTP answers GET /api/ask/suggestions?page=URL&seed=N. It
// only reads; with nothing stored yet it asks the background item to run.
func (s *Server) askSuggestionsHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.askSettings(ctx)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if !settings.Enabled {
		writeJSON(w, map[string]any{"suggestions": []askSuggestion{}}, 200)
		return
	}
	catalog, err := loadedAskSuggestions()
	if err != nil {
		writeError(w, err, 500)
		return
	}
	state, ok := s.Catalog.askSuggestionState(ctx)
	if !ok {
		s.requestBackground("suggestions")
		writeJSON(w, map[string]any{"suggestions": []askSuggestion{}, "pending": true}, 200)
		return
	}
	in, err := s.askRankInputFor(ctx, r.URL.Query().Get("page"), clamp(queryInt(r, "seed", 0), 0, 1<<30))
	if err != nil && !errors.Is(err, context.Canceled) {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, map[string]any{"suggestions": rankAskSuggestions(catalog, state, in), "builtAt": state.BuiltAt}, 200)
}

// askSuggestionLeads are the private observations behind a suggestion the
// user sent as offered.
func (c *Catalog) askSuggestionLeads(ctx context.Context, id string) []string {
	catalog, err := loadedAskSuggestions()
	state, ok := c.askSuggestionState(ctx)
	if err != nil || !ok {
		return nil
	}
	for _, family := range catalog.Families {
		if family.ID != id {
			continue
		}
		names := family.Signals
		if len(names) == 0 {
			for _, area := range catalog.Areas {
				if area.ID == family.Area {
					names = area.Signals
				}
			}
		}
		leads := []string{}
		for _, name := range names {
			if lead := state.Signals[name].Lead; lead != "" {
				leads = append(leads, lead)
			}
		}
		return leads
	}
	return nil
}

func askSuggestionFamilyKnown(id string) bool {
	catalog, err := loadedAskSuggestions()
	return err == nil && id != "" && slices.ContainsFunc(catalog.Families, func(family askSuggestionFamily) bool { return family.ID == id })
}

func (s *Server) askEnabled(ctx context.Context) bool {
	settings, err := s.askSettings(ctx)
	return err == nil && settings.Enabled
}

// Label exploration is useful only after the optional labeler has completed
// conversations. A writing-volume signal would offer it in unlabeled libraries.
func askLabelCoverage(ctx context.Context, run *askSignalRun) (askSignal, error) {
	if !askTableExists(ctx, run.c.reader(), "message_labels_conversations") || !askTableExists(ctx, run.c.reader(), "message_labels_dirty") {
		return askSignal{}, nil
	}
	var count int
	err := run.c.reader().QueryRowContext(ctx, `SELECT count(*) FROM message_labels_conversations c WHERE c.units>0 AND NOT EXISTS(SELECT 1 FROM message_labels_dirty d WHERE d.conversation_id=c.conversation_id)`).Scan(&count)
	if err != nil || count == 0 {
		return askSignal{}, err
	}
	return askSignal{Score: saturate(float64(count), 200), Support: count, Lead: fmt.Sprintf("%d conversations have stored message classifications available to explore.", count)}, nil
}
