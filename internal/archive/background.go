package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// backgroundExplicitKey marks a background pass someone asked for (Run now).
type backgroundExplicitKey struct{}

func withBackgroundExplicit(ctx context.Context, explicit bool) context.Context {
	return context.WithValue(ctx, backgroundExplicitKey{}, explicit)
}
func backgroundExplicit(ctx context.Context) bool {
	explicit, _ := ctx.Value(backgroundExplicitKey{}).(bool)
	return explicit
}

// Background Processing (docs/background-work.md §11). Every kind of derived
// data has an automatic owner on one of three cadences: Frequently, with the
// scheduled index; Hourly, once the library has been quiet; and Daily,
// overnight. An adaptive item sits in the most frequent cadence its measured
// cost allows, so it moves as it gets faster or slower. Permanent overrides
// slow work down; a temporary override can move any item between all four columns.

const (
	cadenceFrequent = "frequent"
	cadenceHourly   = "hourly"
	cadenceDaily    = "daily"
	cadenceManual   = "manual"
)

// cadences are the schedules, most frequent first.
var cadences = []string{cadenceFrequent, cadenceHourly, cadenceDaily}

var cadenceLabels = map[string]string{cadenceFrequent: "Frequently", cadenceHourly: "Hourly", cadenceDaily: "Daily", cadenceManual: "Only when you ask"}

// cadenceRank orders cadences from the most frequent; manual is last.
func cadenceRank(cadence string) int {
	if index := slices.Index(cadences, cadence); index >= 0 {
		return index
	}
	return len(cadences)
}

// validBackgroundCadence includes the fourth, unscheduled column.
func validBackgroundCadence(cadence string) bool {
	return cadence == cadenceManual || slices.Contains(cadences, cadence)
}

// cadenceLimit is the longest typical run, in seconds, each cadence takes.
// Daily takes the rest.
var cadenceLimit = map[string]float64{cadenceFrequent: 60, cadenceHourly: 600}

// The schedule's timings, package variables so tests can shorten them.
var (
	// backgroundTick is how often the loop looks for due work; a scheduled
	// index that wrote something wakes it at once.
	backgroundTick = 20 * time.Second
	// Hourly work waits an hour after its last run, then for the library to
	// be quiet this long, but no longer than hourlyLatest after that run.
	hourlyEvery  = time.Hour
	hourlyQuiet  = 10 * time.Minute
	hourlyLatest = 90 * time.Minute
	// Daily work runs at the daily time, or the first quiet moment after;
	// past dailyLatest it runs whether or not the library is quiet.
	dailyLatest = 12 * time.Hour
	// backgroundRetry is how long a failed item waits before it runs again.
	backgroundRetry = 15 * time.Minute
	// backgroundMissed is how late daily work is before it counts as missed.
	backgroundMissed = time.Hour
)

// backgroundItem is one row of Settings → Background Processing: a kind of work
// a person recognizes, what it feeds, and how it runs.
type backgroundItem struct {
	ID                string
	Label, Verb, Sub  string
	Feeds, Why        string
	Cadence           string
	Adaptive, Fixed   bool
	OptIn             bool
	Stages            []string
	Kinds             []string
	continuous        bool
	readsWholeSources bool
	// requiresLabels items run only where labels_enabled is on;
	// requiresAgentEvents ones only where agent_events_enabled is.
	requiresLabels      bool
	requiresAgentEvents bool
	// requiresAsk items exist only where Ask Pharos is enabled on this Mac.
	requiresAsk bool
	// afterChain items read what the chain writes, so a pass runs them last.
	afterChain bool
}

// backgroundItems in the order they run within a pass (runBackgroundItems),
// which is the order Settings and the status panel list them: new data and
// the other standalone work first, then the analysis chain's items in chain
// order, then work that reads the chain's results.
var backgroundItems = []backgroundItem{
	{ID: "conversations", Label: "New conversations", Verb: "Adding new conversations", Feeds: "Search, Home and Work pages", Cadence: cadenceFrequent, Fixed: true,
		Kinds: []string{"index.changes.automatic"}, Why: "It reads only what changed since the last check."},
	{ID: "search", Label: "Search", Verb: "Updating search", Feeds: "Search and the Library", Cadence: cadenceFrequent, Fixed: true, continuous: true,
		Why: "It updates the conversations that changed, as soon as they are added."},
	{ID: "fingerprints", Label: "Conversation fingerprints", Verb: "Rendering conversation fingerprints", Feeds: "Fingerprint columns and conversation cards", Cadence: cadenceFrequent, continuous: true,
		Kinds: []string{"maintenance.fingerprints"}, Why: "Prepares compact images in the background and refreshes them when their messages, labels, events or tools change."},
	{ID: "other-sources", Label: "Other sources", Verb: "Reading other sources", Feeds: "Their pages, such as TL1", Cadence: cadenceHourly, readsWholeSources: true,
		Kinds: []string{"background.capture/sources"}, Why: "These sources can’t be checked for small changes, so each update reads them in full."},
	{ID: "preserve", Label: "Preserve originals", Verb: "Preserving originals", Feeds: "Recovery, and history from your other Macs", Cadence: cadenceDaily,
		Kinds: []string{"background.capture/preserve"}, Why: "Copies this Mac’s transcripts into the library, so they survive tools deleting them, and indexes what your other Macs captured."},
	{ID: "verification", Label: "Verify indexed data", Verb: "Verifying queued inputs", Feeds: "Integrity checks deferred by quick indexing", Cadence: cadenceHourly,
		Kinds: []string{"index.verify"}, Why: "Checks a bounded batch of larger inputs once the library is quiet. Completed checks survive interruptions; remaining inputs stay queued."},
	{ID: "upgrade", Label: "Library upgrade", Verb: "Upgrading the library", Feeds: "Everything, after a Pharos update", Cadence: cadenceDaily, Fixed: true,
		Kinds: []string{"library.upgrade"}, Why: "Runs only when a Pharos update needs it."},
	{ID: "backup", Label: "Back up", Verb: "Backing up", Feeds: "Recovery", Cadence: cadenceDaily, OptIn: true,
		Kinds: []string{"backup.local"}, Why: "Copies the library to your last backup destination when it is connected. Each backup copies the whole catalog, so it is off unless you give it a schedule."},
	{ID: "repositories", Label: "Repositories & merges", Verb: "Linking repositories", Feeds: "Work pages and repository filters", Cadence: cadenceFrequent, Adaptive: true,
		Stages: []string{"identities", "git"}, Kinds: []string{"analysis.github", "analysis.identities", "analysis.git"}, Why: "Links conversations to their repositories, and asks Git which branches merged."},
	{ID: "tools", Label: "Tools & usage", Verb: "Updating tools and usage", Feeds: "Analyze → Tools, and the usage cards", Cadence: cadenceFrequent, Adaptive: true,
		Stages: []string{"tools"}, Kinds: []string{"analysis.tools"}, Why: "Adds the commands and tokens of the workspaces that changed."},
	{ID: "writing", Label: "Update writing estimates", Verb: "Updating writing estimates", Feeds: "Human Words cards, and Analyze → Writing", Cadence: cadenceFrequent,
		Stages: []string{"authorship"}, Kinds: []string{"analysis.authorship"}, Why: "Updates a bounded batch of new or changed messages. Estimates are refined overnight."},
	{ID: "findings", Label: "Findings", Verb: "Updating findings", Feeds: "Optimize", Cadence: cadenceHourly, Adaptive: true,
		Stages: []string{"findings"}, Kinds: []string{"analysis.findings"}, Why: "Measures the findings you watch against new work."},
	{ID: "labels", Label: "Message labels", Verb: "Labeling messages", Feeds: "Local labels (pharos labels)", Cadence: cadenceHourly, Adaptive: true, requiresLabels: true,
		Stages: []string{"labels"}, Kinds: []string{"analysis.labels/"}, Why: "Labels the messages that changed on this Mac, after your writing; a backlog continues when plugged in."},
	{ID: "agent_events", Label: "Agent events", Verb: "Finding agent events", Feeds: "Analyze → Agent Experience", Cadence: cadenceHourly, Adaptive: true, requiresAgentEvents: true,
		Stages: []string{"agent_events"}, Kinds: []string{"analysis.agent_events/"}, Why: "Finds what agents ran into in the replies and thinking that changed on this Mac; a backlog continues when plugged in."},
	{ID: "review", Label: "Findings review", Verb: "Reviewing findings", Feeds: "Optimize: new patterns and savings", Cadence: cadenceDaily, Fixed: true,
		Kinds: []string{"analysis.findings"}, Why: "Looks at the last 28 days again for new patterns; the window moves once a day."},
	{ID: "writing-refine", Label: "Refine writing history", Verb: "Refining writing history", Feeds: "Writing classifications and copied-text evidence", Cadence: cadenceDaily, afterChain: true,
		Kinds: []string{"analysis.authorship.refine"}, Why: "Applies the complete analysis to retained history, reconciling templates, copied text and conversation relationships."},
	{ID: "suggestions", Label: "Ask Pharos suggestions", Verb: "Choosing Ask Pharos suggestions", Feeds: "Ask Pharos suggestions", Cadence: cadenceHourly, Adaptive: true, requiresAsk: true, afterChain: true,
		Kinds: []string{"ask.suggestions"}, Why: "Looks over the last few weeks' summaries for questions worth suggesting, after the analyses they read change."},
}

// stageOrder is the analysis chain's order (finishDeferredStages).
var stageOrder = []string{"identities", "git", "tools", "authorship", "findings", "labels", "agent_events"}

func backgroundItemByID(id string) (backgroundItem, bool) {
	for _, item := range backgroundItems {
		if item.ID == id {
			return item, true
		}
	}
	return backgroundItem{}, false
}

// backgroundSettings is this Mac's schedule. Mode is on, paused or manual
// ("only when I ask"); manual is scheduled indexing turned off.
type backgroundSettings struct {
	Mode         string                      `json:"mode"`
	PausedUntil  string                      `json:"paused_until,omitempty"`
	Interval     int                         `json:"interval_seconds"`
	DailyAt      string                      `json:"daily_at"`
	HoldWhenBusy bool                        `json:"hold_when_busy"`
	Overrides    map[string]string           `json:"overrides"`
	Placements   map[string]string           `json:"placements"`
	Temporary    map[string]temporaryCadence `json:"temporary_cadences"`
	// Battery is what each cadence does on battery (power.go).
	Battery               batteryPolicy `json:"battery"`
	sync                  syncSettings
	lastConversationCheck time.Time
}

const defaultDailyAt = "03:00"
const temporaryCadenceDuration = 7 * 24 * time.Hour

type temporaryCadence struct {
	Cadence   string `json:"cadence"`
	ExpiresAt string `json:"expires_at"`
}

// backgroundSettings reads this Mac's schedule. A catalog without the table
// (an older catalog opened read-only) has the defaults.
func (c *Catalog) backgroundSettings(ctx context.Context, at time.Time) (backgroundSettings, error) {
	indexing, err := c.syncSettings(ctx)
	if err != nil {
		return backgroundSettings{}, err
	}
	settings := backgroundSettings{Interval: indexing.Interval, DailyAt: defaultDailyAt, HoldWhenBusy: true, Overrides: map[string]string{}, Placements: map[string]string{},
		Battery: defaultBatteryPolicy(), sync: indexing}
	var paused sql.NullString
	var hold int
	var overrides, placements, battery string
	err = c.reader().QueryRowContext(ctx, "SELECT paused_until,daily_at,hold_when_busy,overrides_json,placements_json,battery_json FROM background_settings WHERE host_id=?", currentHost().ID).
		Scan(&paused, &settings.DailyAt, &hold, &overrides, &placements, &battery)
	switch {
	case err == nil:
		settings.PausedUntil, settings.HoldWhenBusy = paused.String, hold != 0
		_ = json.Unmarshal([]byte(overrides), &settings.Overrides)
		_ = json.Unmarshal([]byte(placements), &settings.Placements)
		// Stored choices over the defaults, so a new choice gets its default.
		_ = json.Unmarshal([]byte(battery), &settings.Battery)
	case !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table"):
		return settings, err
	}
	settings.Temporary = map[string]temporaryCadence{}
	rows, err := queryMapsContext(ctx, c.reader(), "SELECT item,cadence,expires_at FROM background_temporary_cadences WHERE host_id=?", currentHost().ID)
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return settings, err
	}
	for _, row := range rows {
		id, cadence := asString(row["item"]), asString(row["cadence"])
		_, known := backgroundItemByID(id)
		until := asString(row["expires_at"])
		if expiry, ok := parseTime(until); known && validBackgroundCadence(cadence) && ok && at.Before(expiry) {
			settings.Temporary[id] = temporaryCadence{Cadence: cadence, ExpiresAt: until}
		}
	}
	// Settings saves may move next_at, but only a real index run completes
	// a check. History is persisted per Mac and survives service restarts.
	var checked string
	err = c.reader().QueryRowContext(ctx, "SELECT finished_at FROM sync_history WHERE host_id=? ORDER BY id DESC LIMIT 1", currentHost().ID).Scan(&checked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table") {
		return settings, err
	}
	settings.lastConversationCheck, _ = parseTime(checked)
	settings.Mode = "on"
	if until, ok := parseTime(settings.PausedUntil); ok && at.Before(until) {
		settings.Mode = "paused"
	} else {
		settings.PausedUntil = ""
	}
	if !indexing.Enabled {
		settings.Mode, settings.PausedUntil = "manual", ""
	}
	return settings, nil
}

// backgroundPaused is whether the schedule is paused at at; scheduled
// indexing waits too.
func (c *Catalog) backgroundPaused(ctx context.Context, at time.Time) bool {
	var until string
	err := c.reader().QueryRowContext(ctx, "SELECT COALESCE(paused_until,'') FROM background_settings WHERE host_id=?", currentHost().ID).Scan(&until)
	paused, ok := parseTime(until)
	return err == nil && ok && at.Before(paused)
}

// placed is where an item runs before any override: its measured placement
// for an adaptive item, else its declared cadence.
func (settings backgroundSettings) placed(item backgroundItem) string {
	if placement := settings.Placements[item.ID]; item.Adaptive && cadenceRank(placement) < len(cadences) {
		return placement
	}
	return item.Cadence
}

// cadence is where an item runs: a live temporary schedule takes precedence
// for all work; permanent overrides only slow down the placement.
func (settings backgroundSettings) cadence(item backgroundItem) string {
	if temporary, ok := settings.Temporary[item.ID]; ok {
		return temporary.Cadence
	}
	return settings.usual(item)
}

// usual is where an item runs without a temporary schedule.
func (settings backgroundSettings) usual(item backgroundItem) string {
	override := settings.Overrides[item.ID]
	if item.OptIn {
		if override == cadenceDaily {
			return cadenceDaily
		}
		return cadenceManual
	}
	placed := settings.placed(item)
	if !item.Fixed && override != "" && cadenceRank(override) > cadenceRank(placed) {
		return override
	}
	return placed
}

// itemCost is an item's typical run: the median of its latest measured runs.
type itemCost struct {
	Seconds float64 `json:"seconds"`
	Runs    int     `json:"runs"`
	// Low and High bound the middle of the recent runs, for "Typically
	// 2–3 h".
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// typicalRange is the middle of sorted run times: the median and the runs
// a quarter of the way in from each end.
func typicalRange(sorted []float64) (median, low, high float64) {
	n := len(sorted)
	median = sorted[n/2]
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return median, sorted[(n-1)/4], sorted[n-1-(n-1)/4]
}

// costRuns is how many recent runs make an item's typical cost, and
// placementRuns how many it needs before it may move.
const (
	costRuns      = 7
	placementRuns = 3
)

// backgroundCosts measures each item from the journal (and, for scheduled
// indexing, its run history). An item measured by several kinds (the
// repositories' three stages) costs the sum of their medians.
func (c *Catalog) backgroundCosts(ctx context.Context) map[string]itemCost {
	costs := map[string]itemCost{}
	for _, item := range backgroundItems {
		if item.ID == "conversations" {
			if cost, ok := c.automaticIndexCost(ctx); ok {
				costs[item.ID] = cost
			}
			continue
		}
		total, low, high, runs := 0.0, 0.0, 0.0, -1
		for _, kind := range item.Kinds {
			// "kind/variant" is that variant; "kind/" only the plain kind
			// (labels' bounded share, not its backlog runs); "kind" all.
			name, variant, exact := strings.Cut(kind, "/")
			rows, err := c.reader().QueryContext(ctx, `SELECT (julianday(ended_at)-julianday(started_at))*86400 FROM operation_journal
				WHERE host_id=? AND kind=? AND (NOT ? OR COALESCE(variant,'')=?) AND state='succeeded' AND ended_at IS NOT NULL ORDER BY started_at DESC LIMIT ?`, currentHost().ID, name, exact, variant, costRuns)
			if err != nil {
				runs = 0
				break
			}
			values := []float64{}
			for rows.Next() {
				var seconds float64
				if rows.Scan(&seconds) == nil && seconds >= 0 {
					values = append(values, seconds)
				}
			}
			rows.Close()
			if len(values) == 0 {
				runs = 0
				break
			}
			sort.Float64s(values)
			median, lower, upper := typicalRange(values)
			total, low, high = total+median, low+lower, high+upper
			if runs < 0 || len(values) < runs {
				runs = len(values)
			}
		}
		if runs > 0 {
			costs[item.ID] = itemCost{Seconds: total, Runs: runs, Low: low, High: high}
		}
	}
	return costs
}

// automaticIndexCost is scheduled indexing's typical run, from its run
// history: the journal keeps only its runs over 10 s.
func (c *Catalog) automaticIndexCost(ctx context.Context) (itemCost, bool) {
	samples := c.automaticSamples(ctx, 50)
	values := []float64{}
	for _, sample := range samples {
		values = append(values, sample.Seconds)
	}
	if len(values) == 0 {
		return itemCost{}, false
	}
	sort.Float64s(values)
	median, low, high := typicalRange(values)
	return itemCost{Seconds: median, Runs: len(values), Low: low, High: high}, true
}

// automaticSample is the part of a scheduled index's run history the
// schedule reads.
type automaticSample struct {
	FinishedAt string   `json:"finished_at"`
	Seconds    float64  `json:"seconds"`
	CPUSeconds *float64 `json:"cpu_seconds"`
	Workspaces int      `json:"workspaces"`
	// Peak is null for a run whose RSS was never read.
	Peak    *int64 `json:"rss_observed_peak_bytes"`
	Trigger string `json:"trigger"`
}

func (c *Catalog) automaticSamples(ctx context.Context, limit int) []automaticSample {
	rows, err := c.reader().QueryContext(ctx, "SELECT sample_json FROM sync_history WHERE host_id=? ORDER BY id DESC LIMIT ?", currentHost().ID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	samples := []automaticSample{}
	for rows.Next() {
		var data string
		var sample automaticSample
		if rows.Scan(&data) == nil && json.Unmarshal([]byte(data), &sample) == nil && sample.Trigger == "automatic" {
			samples = append(samples, sample)
		}
	}
	return samples
}

// placeCadence is where a measured cost puts an item now in current: the
// most frequent cadence whose limit it fits. It moves to a faster cadence
// only once it fits with a quarter to spare, and to a slower one only once
// it is a quarter over, so a cost near a limit does not flap.
func placeCadence(current string, cost itemCost) string {
	if cost.Runs < placementRuns {
		return current
	}
	target := cadenceDaily
	for _, cadence := range []string{cadenceFrequent, cadenceHourly} {
		if cost.Seconds <= cadenceLimit[cadence] {
			target = cadence
			break
		}
	}
	switch {
	case cadenceRank(target) < cadenceRank(current) && cost.Seconds <= 0.75*cadenceLimit[target]:
		return target
	case cadenceRank(target) > cadenceRank(current) && cost.Seconds > 1.25*cadenceLimit[current]:
		return target
	}
	return current
}

// costClass is the words for a typical run's weight.
func costClass(cost itemCost, measured bool) string {
	switch {
	case !measured:
		return ""
	case cost.Seconds < 60:
		return "light"
	case cost.Seconds < 180:
		return "moderate"
	}
	return "heavy"
}

// backgroundRunRow is an item's last scheduled run on this Mac.
type backgroundRunRow struct {
	started, finished, succeeded, err string
	seconds                           float64
}

func (c *Catalog) backgroundRuns(ctx context.Context) map[string]backgroundRunRow {
	runs := map[string]backgroundRunRow{}
	rows, err := c.reader().QueryContext(ctx, "SELECT item,COALESCE(started_at,''),COALESCE(finished_at,''),COALESCE(succeeded_at,''),COALESCE(error,''),COALESCE(seconds,0) FROM background_runs WHERE host_id=?", currentHost().ID)
	if err != nil {
		return runs
	}
	defer rows.Close()
	for rows.Next() {
		var item string
		var run backgroundRunRow
		if rows.Scan(&item, &run.started, &run.finished, &run.succeeded, &run.err, &run.seconds) == nil {
			runs[item] = run
		}
	}
	return runs
}

// recordBackgroundRun stores an item's outcome: started when it begins,
// then finished with its error (nil for success). A withdrawn run
// (backgroundWithdrawn) finishes with neither: it did not fail, and its
// work is still pending.
func (c *Catalog) recordBackgroundRun(ctx context.Context, item string, started time.Time, finished bool, failure error) {
	_ = c.writeTransaction(ctx, "background run", func(tx *sql.Tx) error {
		if !finished {
			_, err := tx.ExecContext(ctx, `INSERT INTO background_runs(host_id,item,started_at) VALUES(?,?,?)
				ON CONFLICT(host_id,item) DO UPDATE SET started_at=excluded.started_at`, currentHost().ID, item, formatTime(started))
			return err
		}
		stamp, problem := now(), errorString(failure)
		if backgroundWithdrawn(failure) {
			problem = ""
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO background_runs(host_id,item,started_at,finished_at,succeeded_at,error,seconds) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(host_id,item) DO UPDATE SET finished_at=excluded.finished_at,succeeded_at=COALESCE(excluded.succeeded_at,background_runs.succeeded_at),
			error=excluded.error,seconds=excluded.seconds`, currentHost().ID, item, formatTime(started), stamp, conditionalString(failure == nil, stamp),
			nilIfEmpty(problem), time.Since(started).Seconds())
		return err
	})
}

// backgroundWithdrawn reports an item whose only outcome is work a Stop
// held and its await withdrew (errWorkHeld): a deliberate Stop, not a
// failure. Chain stages already return nil for this, as the classic
// errStageContinues did.
func backgroundWithdrawn(err error) bool {
	return errors.Is(err, errWorkHeld) && len(withoutHeld(err)) == 0
}

// dailyInstant is the latest daily time at or before at, in local time.
func dailyInstant(at time.Time, dailyAt string) time.Time {
	clock, err := time.Parse("15:04", dailyAt)
	if err != nil {
		clock, _ = time.Parse("15:04", defaultDailyAt)
	}
	local := at.In(time.Local)
	instant := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, time.Local)
	if instant.After(at) {
		instant = instant.AddDate(0, 0, -1)
	}
	return instant
}

// backgroundInputs is what the plan reads besides the settings.
type backgroundInputs struct {
	deferred map[string]deferredRow
	runs     map[string]backgroundRunRow
	// findingsToday is whether today's full findings pass ran;
	// findingsBuilt whether any did.
	findingsToday, findingsBuilt bool
	writingPending               bool
	writingBacklog               authorshipBacklog
	writingRefinedAt             string
	writingRefinementPending     bool
	upgradePending               bool
	otherSources                 []string
	backupDestination            string
	lastBackup                   string
	requested                    map[string]bool
	// power is this Mac's power source, for the battery policy.
	power            powerState
	verificationNext time.Time
	// labels is whether labels_enabled is on; labelsWaiting why labeling
	// waits for a download.
	labels        bool
	labelsWaiting string
	// agentEvents is whether agent_events_enabled is on; agentEventsWaiting
	// why the pass waits for a download.
	agentEvents        bool
	agentEventsWaiting string
	// labelerProblem is why neither can run, whatever is downloaded.
	labelerProblem string
	// ask is whether Ask Pharos is enabled here; suggestionsStale whether
	// its suggestions' inputs changed since they were chosen.
	ask, suggestionsStale bool
}

func (c *Catalog) readBackgroundInputs(ctx context.Context) backgroundInputs {
	in := backgroundInputs{runs: c.backgroundRuns(ctx)}
	in.verificationNext = c.queuedVerificationNext(ctx)
	want, built, err := c.authorshipEstimateGenerations()
	var backlogErr error
	in.writingBacklog, backlogErr = c.authorshipBacklog(ctx)
	in.writingPending = err != nil || backlogErr != nil || want != built || in.writingBacklog.Pending > 0
	in.writingRefinedAt, _ = c.metaValue(ctx, "authorship_refined_at")
	in.writingRefinementPending, err = c.authorshipRefinementPending(ctx)
	in.writingRefinementPending = in.writingRefinementPending || err != nil
	in.deferred, _ = c.freshnessDeferred(ctx)
	generation, _ := c.metaValue(ctx, "findings_generation")
	in.findingsBuilt, in.findingsToday = generation != "", generation == c.findingsGeneration()
	if value, err := c.metaValue(ctx, upgradeCountKey); err == nil && value != "" {
		var count upgradeCount
		if json.Unmarshal([]byte(value), &count) == nil {
			for _, step := range count.Steps {
				in.upgradePending = in.upgradePending || step.Pending > 0
			}
		}
	}
	return in
}

// itemPlan is an item's place in the schedule now.
type itemPlan struct {
	ID, Cadence string
	// Due is whether a pass should run it now; Next is when it next runs,
	// zero when that waits for new work.
	Due  bool
	Next time.Time
	// Pending is new work waiting for it; LastDone when it last finished
	// successfully.
	Pending  bool
	LastDone time.Time
	// Failed is its last scheduled run failing, retried at Next.
	Failed bool
	Error  string
	// Missed is daily work that did not run at its time.
	Missed bool
	// Overdue is work late enough to run even while this Mac is busy.
	Overdue bool
	// WaitsForPower is work the battery policy holds until plugged in.
	WaitsForPower bool
}

// backgroundPlan is every item's place in the schedule at at.
func backgroundPlan(settings backgroundSettings, in backgroundInputs, at time.Time) []itemPlan {
	changed := time.Time{}
	for _, row := range in.deferred {
		if value, ok := parseTime(row.changedAt); ok && value.After(changed) {
			changed = value
		}
	}
	quiet := changed.Add(hourlyQuiet)
	instant := dailyInstant(at, settings.DailyAt)
	nextDaily := instant.AddDate(0, 0, 1)
	plans := []itemPlan{}
	for _, item := range backgroundItems {
		if item.requiresLabels && !in.labels || item.requiresAgentEvents && !in.agentEvents || item.requiresAsk && !in.ask {
			continue
		}
		plan := itemPlan{ID: item.ID, Cadence: settings.cadence(item)}
		run := in.runs[item.ID]
		if value, ok := parseTime(run.succeeded); ok {
			plan.LastDone = value
		}
		// When each cadence next runs work that is waiting.
		daily := func() {
			if plan.LastDone.Before(instant) {
				plan.Next = maxTime(instant, quiet)
				// Work that has run before is overdue past dailyLatest;
				// work that never ran waits for a quiet library.
				if at.Sub(instant) > dailyLatest && !plan.LastDone.IsZero() {
					plan.Next, plan.Overdue = instant, true
				}
				plan.Missed = !plan.LastDone.IsZero() && at.Sub(instant) > backgroundMissed
			} else {
				plan.Next = nextDaily
			}
		}
		switch {
		case item.ID == "conversations":
			plan = settings.conversationsPlan(quiet, at)
		case item.continuous:
			if plan.Cadence != cadenceFrequent {
				plan.Pending = true
				standaloneNext(&plan, settings, quiet, at)
			}
		case item.ID == "verification":
			plan.Pending = !in.verificationNext.IsZero()
			if plan.Pending {
				standaloneNext(&plan, settings, quiet, at)
				plan.Next = maxTime(plan.Next, in.verificationNext)
			}
		case item.ID == "writing":
			row := in.deferred["authorship"]
			plan.Pending = in.writingPending || row.generation > row.completed
			if completed, ok := parseTime(row.completedAt); ok {
				plan.LastDone = maxTime(plan.LastDone, completed)
			}
			if plan.Pending {
				standaloneNext(&plan, settings, quiet, at)
				if plan.Cadence == cadenceHourly {
					plan.Next = maxTime(plan.LastDone.Add(hourlyEvery), minTime(quiet, plan.LastDone.Add(hourlyLatest)))
				}
			}
		case item.ID == "writing-refine":
			if refined, ok := parseTime(in.writingRefinedAt); ok {
				plan.LastDone = maxTime(plan.LastDone, refined)
			}
			plan.Pending = in.writingRefinementPending
			if plan.Pending {
				standaloneNext(&plan, settings, quiet, at)
			}
		case len(item.Stages) > 0:
			completed := time.Time{}
			first := true
			for _, stage := range item.Stages {
				row := in.deferred[stage]
				plan.Pending = plan.Pending || row.generation > row.completed
				done, _ := parseTime(row.completedAt)
				if first || done.Before(completed) {
					completed = done
				}
				first = false
			}
			// A bounded pass can succeed while its backlog stays deferred.
			// Keep that run's time for Last run and the cadence's clock;
			// Pending still follows the deferred generations.
			plan.LastDone = maxTime(plan.LastDone, completed)
			switch {
			case !plan.Pending:
				if plan.Cadence == cadenceDaily {
					plan.Next = nextDaily
				}
			case plan.Cadence == cadenceFrequent:
				plan.Next = at
			case plan.Cadence == cadenceHourly:
				plan.Next = maxTime(plan.LastDone.Add(hourlyEvery), minTime(quiet, plan.LastDone.Add(hourlyLatest)))
				plan.Overdue = at.Sub(plan.LastDone) > 3*hourlyEvery
			case plan.Cadence == cadenceDaily:
				daily()
			}
		case item.readsWholeSources:
			if len(in.otherSources) == 0 {
				continue
			}
			plan.Pending = true
			standaloneNext(&plan, settings, time.Time{}, at)
		case item.ID == "review":
			// Today's full pass, or the first one ever.
			plan.Pending = !in.findingsToday
			// By default today's pass waits for today's daily time.
			if plan.Cadence != cadenceDaily {
				if plan.Pending {
					standaloneNext(&plan, settings, quiet, at)
				}
			} else if plan.Pending && dailyInstant(at, settings.DailyAt).Day() == at.In(time.Local).Day() {
				plan.Next = maxTime(instant, quiet)
				if at.Sub(instant) > dailyLatest {
					plan.Next, plan.Overdue = instant, true
				}
			} else {
				plan.Next = nextDaily
			}
		case item.ID == "suggestions":
			plan.Pending = in.suggestionsStale
			switch {
			case !plan.Pending:
			case plan.Cadence == cadenceFrequent:
				plan.Next = at
			case plan.Cadence == cadenceHourly:
				plan.Next = plan.LastDone.Add(hourlyEvery)
			case plan.Cadence == cadenceDaily:
				daily()
			}
		case item.ID == "upgrade":
			plan.Pending = in.upgradePending
			if plan.Pending {
				standaloneNext(&plan, settings, quiet, at)
			}
		case item.ID == "backup":
			if in.backupDestination == "" {
				break
			}
			if last, ok := parseTime(in.lastBackup); ok {
				plan.LastDone = maxTime(plan.LastDone, last)
			}
			plan.Pending = true
			standaloneNext(&plan, settings, quiet, at)
		default:
			plan.Pending = true
			standaloneNext(&plan, settings, quiet, at)
		}
		if value, ok := parseTime(run.finished); ok && !value.Before(plan.LastDone) && (run.err != "" || run.finished != run.succeeded) {
			// A failure, or a withdrawn run (finished with neither an error
			// nor a success): both wait backgroundRetry, so a kind left
			// stopped is not asked again on every check; only a failure
			// says Failed.
			plan.Failed, plan.Error = run.err != "", run.err
			if retry := value.Add(backgroundRetry); retry.After(plan.Next) {
				plan.Next = retry
			}
		}
		if cadenceRank(plan.Cadence) < len(cadences) {
			switch settings.Battery.onPower(plan.Cadence, in.power) {
			case batteryWait:
				plan.Next, plan.WaitsForPower = time.Time{}, plan.Pending || item.ID == "conversations" || item.continuous
			case batterySlower:
				if item.ID == "conversations" && !plan.Next.IsZero() {
					plan.Next = plan.Next.Add(slowerBy(settings.Interval))
				}
			}
		}
		if plan.Cadence == cadenceManual || settings.Mode != "on" {
			plan.Next = time.Time{}
		}
		plan.Due = !plan.Next.IsZero() && !at.Before(plan.Next) && plan.Pending && item.ID != "conversations" && !(item.continuous && plan.Cadence == cadenceFrequent)
		if in.requested[item.ID] && item.ID != "conversations" {
			plan.Due = true
		}
		plans = append(plans, plan)
	}
	return plans
}

// standaloneNext schedules work with no analysis-stage completion record.
func standaloneNext(plan *itemPlan, settings backgroundSettings, quiet, at time.Time) {
	switch plan.Cadence {
	case cadenceFrequent:
		plan.Next = plan.LastDone.Add(time.Duration(max(settings.Interval, 1)) * time.Second)
	case cadenceHourly:
		plan.Next = maxTime(quiet, plan.LastDone.Add(hourlyEvery))
		if !plan.LastDone.IsZero() && at.Sub(plan.LastDone) > 3*hourlyEvery {
			plan.Next, plan.Overdue = at, true
		}
	case cadenceDaily:
		instant := dailyInstant(at, settings.DailyAt)
		if plan.LastDone.Before(instant) {
			plan.Next = maxTime(instant, quiet)
			plan.Missed = !plan.LastDone.IsZero() && at.Sub(instant) > backgroundMissed
			if !plan.LastDone.IsZero() && at.Sub(instant) > dailyLatest {
				plan.Next, plan.Overdue = instant, true
			}
		} else {
			plan.Next = instant.AddDate(0, 0, 1)
		}
	}
}

// conversationsPlan uses the next check for Frequently and the actual last
// check for Hourly/Daily. Settings edits cannot count as completed work.
func (settings backgroundSettings) conversationsPlan(quiet, at time.Time) itemPlan {
	item, _ := backgroundItemByID("conversations")
	plan := itemPlan{ID: item.ID, Cadence: settings.cadence(item), LastDone: settings.lastConversationCheck}
	next, _ := parseTime(settings.sync.NextAt)
	if plan.Cadence == cadenceFrequent {
		plan.Next = next
		return plan
	}
	standaloneNext(&plan, settings, quiet, at)
	return plan
}

// slowerBy is how much later scheduled indexing runs on battery when
// Frequently runs slower: up to batterySlowerInterval after the last run.
func slowerBy(intervalSeconds int) time.Duration {
	return max(0, batterySlowerInterval-time.Duration(intervalSeconds)*time.Second)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// backgroundState is the service's side of the schedule: what Run now
// asked for, the wake signal, and the cached costs.
type backgroundState struct {
	mu        sync.Mutex
	requested map[string]bool
	wake      chan struct{}
	costs     map[string]itemCost
	costsAt   time.Time
	// power is the last power reading (cachedPower).
	power   powerState
	powerAt time.Time
	// indexNow is a Run now of new conversations: the scheduled index runs
	// once whatever the mode, power or time.
	indexNow bool
	// backup is the last backup's destination and time, from backups.json.
	backup   [2]string
	backupAt time.Time
}

// cachedBackup is the last backup's destination and finish, read from the
// library's backups.json at most once per costsMaxAge.
func (s *Server) cachedBackup(config Config) (string, string) {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	if s.background.backupAt.IsZero() || time.Since(s.background.backupAt) > costsMaxAge {
		s.background.backup, s.background.backupAt = [2]string{}, time.Now()
		if history := loadBackupHistory(config); len(history.Backups) > 0 {
			s.background.backup = [2]string{history.Backups[0].Destination, history.Backups[0].FinishedAt}
		}
	}
	return s.background.backup[0], s.background.backup[1]
}

// costsMaxAge is how long measured costs are reused: the status snapshot
// reads them on every computation.
var costsMaxAge = time.Minute

func (s *Server) cachedCosts(ctx context.Context) map[string]itemCost {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	if s.background.costs == nil || time.Since(s.background.costsAt) > costsMaxAge {
		s.background.costs, s.background.costsAt = s.Catalog.backgroundCosts(ctx), time.Now()
	}
	return s.background.costs
}

// wakeBackground makes the loop look for due work now.
func (s *Server) wakeBackground() {
	select {
	case s.background.wake <- struct{}{}:
	default:
	}
}

// requestBackground asks the next pass to run items now, whatever their
// cadence (Run now).
func (s *Server) requestBackground(items ...string) {
	s.background.mu.Lock()
	if s.background.requested == nil {
		s.background.requested = map[string]bool{}
	}
	for _, item := range items {
		s.background.requested[item] = true
	}
	s.background.mu.Unlock()
	s.wakeBackground()
}

// requestIndex asks automaticLoop for one scheduled index now (Run now of
// new conversations); it stays asked until an index runs.
func (s *Server) requestIndex() {
	s.background.mu.Lock()
	s.background.indexNow = true
	s.background.mu.Unlock()
}

func (s *Server) indexRequested() bool {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	return s.background.indexNow
}

func (s *Server) takeIndexRequest() {
	s.background.mu.Lock()
	s.background.indexNow = false
	s.background.mu.Unlock()
}

func (s *Server) takeRequested() map[string]bool {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	requested := s.background.requested
	s.background.requested = nil
	return requested
}

func (s *Server) peekRequested() map[string]bool {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	return maps.Clone(s.background.requested)
}

// backgroundInputsFor adds the service's view (sources, backups, Run now)
// to what the catalog says.
func (s *Server) backgroundInputsFor(ctx context.Context, requested map[string]bool) backgroundInputs {
	in := s.Catalog.readBackgroundInputs(ctx)
	config := s.Config()
	for _, source := range config.Sources {
		if source.Enabled && !automaticProvider(source.Kind) && !isConductorSource(source.Kind) {
			in.otherSources = append(in.otherSources, source.Name)
		}
	}
	in.backupDestination, in.lastBackup = s.cachedBackup(config)
	in.requested = requested
	in.power = s.cachedPower(ctx)
	if config.Labels.Enabled {
		in.labels, in.labelsWaiting = true, config.Labels.labelsWaiting()
	}
	if s.agentEventsEnabled(ctx) {
		in.agentEvents, in.agentEventsWaiting = true, config.AgentEvents.waiting(config.Labels)
	}
	if in.labels || in.agentEvents {
		in.labelerProblem = config.Labels.withAssets().labelerProblem()
	}
	if s.askEnabled(ctx) {
		in.ask, in.suggestionsStale = true, s.Catalog.askSuggestionsStale(ctx, time.Now())
	}
	return in
}

// machineBusy is whether other work is using most of this Mac's CPU.
func machineBusy() bool {
	load := machineLoad()
	return load != nil && *load > 0.8*float64(runtime.NumCPU())
}

// backgroundLoop runs due work: after a scheduled index that wrote
// something, and otherwise every backgroundTick.
func (s *Server) backgroundLoop(ctx context.Context) {
	ticker := time.NewTicker(backgroundTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.background.wake:
		}
		s.backgroundPass(ctx, time.Now())
	}
}

// backgroundPass runs the most frequent cadence that has due work, with
// every item of it that is due; the next pass takes the next cadence.
func (s *Server) backgroundPass(ctx context.Context, at time.Time) bool {
	settings, err := s.Catalog.backgroundSettings(ctx, at)
	if err != nil {
		return false
	}
	requested := s.peekRequested()
	if settings.Mode == "on" {
		s.updatePlacements(ctx, settings)
	} else if len(requested) == 0 {
		return false
	}
	plans := backgroundPlan(settings, s.backgroundInputsFor(ctx, requested), at)
	group, cadence := []itemPlan{}, ""
	for _, plan := range plans {
		if !plan.Due {
			continue
		}
		label := plan.Cadence
		if requested[plan.ID] {
			label = "requested"
		}
		if cadence == "" || label == "requested" && cadence != "requested" || cadence != "requested" && cadenceRank(label) < cadenceRank(cadence) {
			cadence, group = label, nil
		}
		if label == cadence {
			group = append(group, plan)
		}
	}
	if len(group) == 0 {
		return false
	}
	if cadence != "requested" && cadence != cadenceFrequent && settings.HoldWhenBusy && machineBusy() && !slices.ContainsFunc(group, func(plan itemPlan) bool { return plan.Overdue }) {
		return false
	}
	ids := []string{}
	for _, plan := range group {
		ids = append(ids, plan.ID)
	}
	return s.runBackgroundPass(ctx, cadence, ids, plans)
}

// updatePlacements moves adaptive items to where their measured cost puts
// them, and stores the result when it changed.
func (s *Server) updatePlacements(ctx context.Context, settings backgroundSettings) {
	costs := s.cachedCosts(ctx)
	placements := maps.Clone(settings.Placements)
	for _, item := range backgroundItems {
		if item.Adaptive {
			placements[item.ID] = placeCadence(settings.placed(item), costs[item.ID])
		}
	}
	if maps.Equal(placements, settings.Placements) {
		return
	}
	_ = s.Catalog.writeTransaction(ctx, "background placements", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO background_settings(host_id,placements_json,updated_at) VALUES(?,?,?)
			ON CONFLICT(host_id) DO UPDATE SET placements_json=excluded.placements_json,updated_at=excluded.updated_at`, currentHost().ID, jsonText(placements), now())
		return err
	})
}

// runBackgroundPass runs items as one background.update operation, holding the
// ingest coordinator as a scheduled index does: a manual index stops it,
// and a scheduled index waits for it.
func (s *Server) runBackgroundPass(parent context.Context, cadence string, ids []string, plans []itemPlan) bool {
	s.auto.mu.Lock()
	if s.auto.cancel != nil || s.auto.manualWaiters > 0 || !s.ingestMu.TryLock() {
		s.auto.mu.Unlock()
		return false
	}
	trigger := triggerSchedule
	if cadence == "requested" {
		trigger = triggerUser
	}
	ctx, handle := s.begin(parent, OperationSpec{Kind: "background.update", Variant: cadence, Trigger: trigger})
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.auto.cancel, s.auto.done = cancel, done
	s.auto.mu.Unlock()
	// A run in /api/activity (not shadowing the operation, which has its
	// own steps), so open pages refresh once a pass changed something, as
	// after an index (sync.js, finishedRunsChanged). The operation names it,
	// so the eject verdict and Stop treat the run as the operation's.
	run := s.startRunNamed(backgroundRunKind, ids)
	handle.linkLegacy(run.ID)
	registered, end := s.beginIngest(run.ID)
	stop := context.AfterFunc(registered, cancel)
	var failure error
	changed := false
	defer func() {
		state := "complete"
		if ctx.Err() != nil {
			state = "interrupted"
		} else if failure != nil {
			state = "failed"
		}
		s.updateRun(run.ID, func(run *SyncRun) {
			run.State, run.Phase, run.Changed, run.CompletedAt, run.CurrentSource = state, state, changed, now(), nil
			if failure != nil {
				run.Error = failure.Error()
			}
		})
		stop()
		end()
		cancel()
		s.auto.mu.Lock()
		s.auto.cancel, s.auto.done = nil, nil
		s.ingestMu.Unlock()
		close(done)
		s.auto.mu.Unlock()
		handle.Finish(failure)
	}()
	for _, id := range ids {
		s.background.mu.Lock()
		delete(s.background.requested, id)
		s.background.mu.Unlock()
	}
	handle.Run()
	handle.Plan(backgroundSteps(ids)...)
	changed, failure = s.runBackgroundItems(ctx, handle, run.ID, cadence, ids, plans)
	return true
}

// backgroundRunKind is a background pass's run kind in /api/activity.
const backgroundRunKind = "background-update"

// backgroundSteps are a pass's steps, one per item, for "step N of M".
func backgroundSteps(ids []string) []StepPlan {
	steps := []StepPlan{}
	for _, id := range ids {
		item, _ := backgroundItemByID(id)
		steps = append(steps, StepPlan{Name: item.Label})
	}
	return steps
}

// runBackgroundItems runs each item in turn, recording each one's outcome:
// first the items that bring in new data (captures), then the chain's
// stages together, in chain order, then the items that read the chain's
// results. A stage reads what the stages before it
// wrote, so those with work waiting come along, read after the captures
// wrote their inputs, unless their own item is held: set to only when you
// ask, waiting for power, or on a slower cadence than this pass (Run now
// brings along all but the first). A held stage's dependents run on what
// it last wrote; its own run later moves the generations they read, so
// they run again. Findings only measure unless the daily review is in the
// pass. It reports whether any item succeeded (and so may have changed
// what pages show).
func (s *Server) runBackgroundItems(ctx context.Context, op *Op, runID, cadence string, ids []string, plans []itemPlan) (succeeded bool, failure error) {
	// Under population execution, Run now is explicit demand; a scheduled
	// pass is ordinary live demand. Neither makes history foreground.
	ctx = withBackgroundExplicit(ctx, cadence == "requested")
	record := func(id string, started time.Time, err error) {
		s.Catalog.recordBackgroundRun(context.WithoutCancel(ctx), id, started, true, err)
		succeeded = succeeded || err == nil
	}
	stepped := map[string]bool{}
	next := func(id string) {
		if stepped[id] {
			return
		}
		stepped[id] = true
		item, _ := backgroundItemByID(id)
		op.Step(item.Label, "", -1)
		s.updateRun(runID, func(run *SyncRun) { run.Phase = item.Verb })
	}
	chainItem := func(id string) bool {
		item, _ := backgroundItemByID(id)
		return len(item.Stages) > 0 || id == "review"
	}
	standalone := func(after bool) {
		for _, id := range ids {
			item, _ := backgroundItemByID(id)
			if ctx.Err() != nil {
				break
			}
			if chainItem(id) || item.afterChain != after {
				continue
			}
			next(id)
			started := time.Now()
			s.Catalog.recordBackgroundRun(ctx, id, started, false, nil)
			err := s.runBackgroundItem(ctx, op, id)
			if ctx.Err() != nil && err == nil {
				err = ctx.Err()
			}
			record(id, started, err)
			if err != nil && failure == nil && !backgroundWithdrawn(err) {
				failure = fmt.Errorf("%s: %w", item.Label, err)
			}
		}
	}
	standalone(false)
	// Named results: what these record reaches the caller.
	defer standalone(true)
	stages := map[string]bool{}
	chainItems := []string{}
	for _, item := range backgroundItems {
		if !slices.Contains(ids, item.ID) || !chainItem(item.ID) {
			continue
		}
		chainItems = append(chainItems, item.ID)
		for _, stage := range item.Stages {
			stages[stage] = true
		}
		if item.ID == "review" {
			stages["findings"] = true
		}
	}
	if len(stages) == 0 || ctx.Err() != nil {
		return succeeded, failure
	}
	plansByID := map[string]itemPlan{}
	for _, plan := range plans {
		plansByID[plan.ID] = plan
	}
	held := func(stage string) bool {
		plan := plansByID[stageItems[stage]]
		if plan.Cadence == cadenceManual {
			return true
		}
		return cadence != "requested" && (plan.WaitsForPower || cadenceRank(plan.Cadence) > cadenceRank(cadence))
	}
	deferred, _ := s.Catalog.freshnessDeferred(ctx)
	last := -1
	for index, stage := range stageOrder {
		if stages[stage] {
			last = index
		}
	}
	for _, stage := range stageOrder[:last+1] {
		if row := deferred[stage]; row.generation > row.completed && !held(stage) {
			stages[stage] = true
		}
	}
	// Discovery can bring earlier analyses into this pass. Include them in
	// its remaining steps, so a prerequisite never leaves the last finished
	// standalone task (such as Library upgrade) looking as though it is running.
	stageItem := func(name string) string {
		if name == "github" {
			return "repositories"
		}
		if name == "findings" && slices.Contains(ids, "review") && !slices.Contains(ids, "findings") {
			return "review"
		}
		return stageItems[name]
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	for name := range stages {
		wanted[stageItem(name)] = true
	}
	expanded := []string{}
	for _, item := range backgroundItems {
		if wanted[item.ID] {
			expanded = append(expanded, item.ID)
		}
	}
	op.Plan(backgroundSteps(expanded)...)
	findings := findingsMeasure
	if slices.Contains(ids, "review") {
		findings = findingsAuto
	}
	started := time.Now()
	for _, id := range chainItems {
		s.Catalog.recordBackgroundRun(ctx, id, started, false, nil)
	}
	outcome := map[string]error{}
	err := s.finishDeferredStages(ctx, findings, stages, func(name string, work func() error) error {
		if id := stageItem(name); id != "" {
			next(id)
		}
		for _, id := range chainItems {
			item, _ := backgroundItemByID(id)
			if slices.Contains(item.Stages, name) || id == "review" && name == "findings" {
				next(id)
			}
		}
		err := work()
		outcome[name] = err
		return err
	})
	for _, id := range chainItems {
		item, _ := backgroundItemByID(id)
		names := item.Stages
		if id == "review" {
			names = []string{"findings"}
		}
		var itemErr error
		for _, name := range names {
			result, ran := outcome[name]
			switch {
			case ran && result != nil:
				itemErr = result
			case !ran && err != nil:
				itemErr = fmt.Errorf("waiting for an earlier update that failed: %w", err)
			case !ran:
				itemErr = ctx.Err()
			}
			if itemErr != nil {
				break
			}
		}
		record(id, started, itemErr)
	}
	if err != nil && failure == nil {
		failure = err
	}
	return succeeded, failure
}

// stageItems is the background item each chain stage belongs to.
var stageItems = map[string]string{"identities": "repositories", "git": "repositories", "tools": "tools", "authorship": "writing", "findings": "findings", "labels": "labels", "agent_events": "agent_events"}
