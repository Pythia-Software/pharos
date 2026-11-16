package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Startup upgrades (docs/background-work.md §3.5). Initialize runs once per
// new build, before the service listens, so its progress cannot go through the
// API. It writes it instead to a small file beside the catalog's initialize
// lock: the steps, the one that is running and an estimate of each. The app's
// broker reads the file while it waits for the service (application.go), so
// the window can say that a library upgrade is running, which step, and about
// how long it may take.
//
// Each upgrade is a one-off, so the estimate has two parts:
//   - routine work every upgrade repeats (checks, triggers, small backfills),
//     timed per step from this library's earlier upgrades on this Mac;
//   - one-off work this build brings: an index to build, statistics to
//     gather, derived data to clear. Each is detected before it runs and
//     priced by the rows it reads, at a per-row rate measured on this
//     library's earlier one-off work, or a wide default without any.
//
// Once a step finishes, how its time compared with its estimate rescales the
// steps after it (a busy or slow drive shows at once). A step that runs well
// past its estimate withdraws the countdown rather than counting below zero.

const initializeProgressName = ".initialize-progress.json"

// initializeSteps are Initialize's steps, in order, as the window names them.
var initializeSteps = []string{
	"Update tables and indexes",
	"Update project and analysis tables",
	"Add new columns and indexes",
	"Update request timings",
	"Update usage estimation and Library indexes",
	"Fill in derived records",
	"Refresh query planner statistics",
	"Schedule derived data for an update",
}

const (
	initStepTables = iota
	initStepAnalysis
	initStepColumns
	initStepTimings
	initStepUsage
	initStepBackfill
	initStepPlanner
	initStepDerivations
)

// initializeStepShare is each step's share of routine Initialize time, as
// measured on a large library (2026-11-15, 0.9.7 on a 110 GB catalog: 65 s in
// all). It splits whole-upgrade durations from before steps were recorded.
var initializeStepShare = []float64{0.05, 0.30, 0.03, 0.55, 0.02, 0.05, 0, 0}

// Kinds of one-off work, each priced per unit of its own: building an index
// sorts every row of its table; gathering statistics reads every index of
// it (units are rows × indexes); a scan reads every row and writes few.
const (
	oneOffIndex      = "index"
	oneOffStatistics = "statistics"
	oneOffScan       = "scan"
)

// initializeDefaultRates are seconds per unit of each kind without this
// library's own measurements. They span the same 110 GB catalog on its
// external SSD when quiet and when busy with other work (2026-11-15: an
// index on 1.8M tool calls 35 s / 75 s; its statistics, 20 indexes, 34 s /
// 74 s; a scan clearing estimates over 16.4M rows 28 s / 227 s).
var initializeDefaultRates = map[string][2]float64{
	oneOffIndex:      {12e-6, 45e-6},
	oneOffStatistics: {0.7e-6, 2.2e-6},
	oneOffScan:       {1.2e-6, 15e-6},
}

const (
	// One-off work reading fewer rows than this is not priced or recorded.
	initializeMinimumRows = 10_000
	// A step this far past its high estimate withdraws the countdown.
	initializeOverrunSeconds = 30
)

// initializeOneOff is one-off work a step will do, found before it runs.
type initializeOneOff struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Rows int64  `json:"rows"`
	// Units are what its kind is priced by (initializeDefaultRates).
	Units float64 `json:"units"`
	// Unmeasured is set when the rows it reads could not be counted.
	Unmeasured bool `json:"unmeasured,omitempty"`
}

// initializeStep is one step as the progress file and history store it.
type initializeStep struct {
	Name    string             `json:"name"`
	State   string             `json:"state"` // pending, running, done
	OneOffs []initializeOneOff `json:"one_offs,omitempty"`
	// LowSeconds and HighSeconds bound how long it takes; Known is false
	// when there is nothing to estimate it from.
	Known       bool    `json:"known"`
	LowSeconds  float64 `json:"low_seconds"`
	HighSeconds float64 `json:"high_seconds"`
	Basis       string  `json:"basis,omitempty"`
	StartedAt   string  `json:"started_at,omitempty"`
	Seconds     float64 `json:"seconds,omitempty"`
	// Weight is its share of the progress bar: its estimate's middle as
	// planned, kept when later steps are rescaled so segments do not move.
	Weight float64 `json:"weight"`
}

func (s initializeStep) rows() int64 {
	var rows int64
	for _, item := range s.OneOffs {
		rows += item.Rows
	}
	return rows
}

// initializeProgress is the progress file.
type initializeProgress struct {
	PID       int              `json:"pid"`
	StartedAt string           `json:"started_at"`
	UpdatedAt string           `json:"updated_at"`
	Current   int              `json:"current"`
	Steps     []initializeStep `json:"steps"`
}

// initializeHistory is this library's earlier upgrades on this Mac.
type initializeHistory struct {
	Runs []initializeRun `json:"runs"`
}

type initializeRun struct {
	At      string                 `json:"at"`
	Seconds float64                `json:"seconds"`
	Steps   []initializeRunStepLog `json:"steps"`
}

type initializeRunStepLog struct {
	Name    string             `json:"name"`
	Seconds float64            `json:"seconds"`
	Rows    int64              `json:"rows,omitempty"`
	OneOffs []initializeOneOff `json:"one_offs,omitempty"`
}

const initializeHistoryKeep = 10

func initializeHistoryKey() string { return activityTimingKey("initialize-v1") }

// initializeTracker reports one Initialize. A nil tracker reports nothing.
type initializeTracker struct {
	path     string
	started  time.Time
	progress initializeProgress
	// planned are each step's estimates before any rescaling, which both
	// the ratio and the rescaled estimates are computed from.
	planned     [][2]float64
	stepStarted time.Time
}

// startInitializeProgress plans this Initialize and publishes its first step.
// Planning only reads the schema, statistics and meta; any failure leaves the
// steps unestimated rather than stopping the upgrade.
func (c *Catalog) startInitializeProgress() *initializeTracker {
	if c == nil || c.DB == nil || c.Path == "" || c.Path == ":memory:" {
		return nil
	}
	ctx := context.Background()
	t := &initializeTracker{path: filepath.Join(filepath.Dir(c.Path), initializeProgressName), started: time.Now()}
	t.progress = initializeProgress{PID: os.Getpid(), StartedAt: formatTime(t.started), Current: -1}
	oneOffs := planInitializeOneOffs(ctx, c.DB)
	history := loadInitializeHistory(ctx, c.DB)
	routine := history.routine(ctx, c.DB)
	rates := history.rates(routine)
	for i, name := range initializeSteps {
		step := initializeStep{Name: name, State: "pending", OneOffs: oneOffs[i]}
		estimate := routine[i]
		step.Known, step.LowSeconds, step.HighSeconds, step.Basis = estimate.known, estimate.low, estimate.high, estimate.basis
		for _, item := range step.OneOffs {
			if item.Unmeasured {
				step.Known = false
				continue
			}
			rate := rates[item.Kind]
			step.LowSeconds += item.Units * rate[0]
			step.HighSeconds += item.Units * rate[1]
		}
		if len(step.OneOffs) > 0 {
			step.Basis = strings.Trim(step.Basis+"; table sizes", "; ")
		}
		if step.Known {
			step.Weight = (step.LowSeconds + step.HighSeconds) / 2
		}
		t.progress.Steps = append(t.progress.Steps, step)
		t.planned = append(t.planned, [2]float64{step.LowSeconds, step.HighSeconds})
	}
	return t
}

// begin starts step index, finishing the one before.
func (t *initializeTracker) begin(index int) {
	if t == nil || index <= t.progress.Current || index >= len(t.progress.Steps) {
		return
	}
	at := time.Now()
	t.finishCurrent(at)
	t.progress.Current = index
	t.stepStarted = at
	step := &t.progress.Steps[index]
	step.State, step.StartedAt = "running", formatTime(at)
	t.publish(at)
}

func (t *initializeTracker) finishCurrent(at time.Time) {
	if t.progress.Current < 0 {
		return
	}
	step := &t.progress.Steps[t.progress.Current]
	step.State, step.Seconds = "done", at.Sub(t.stepStarted).Seconds()
	t.rescale()
}

// rescale sets the steps still to run to their planned estimates times the
// finished steps' time over their planned estimates' middles, once those add
// to ten seconds, so a drive running three times slower than planned
// stretches the rest likewise. Both sides use the planned estimates: a
// finished step's own was already rescaled while it waited.
func (t *initializeTracker) rescale() {
	var actual, expected float64
	for i, step := range t.progress.Steps {
		if step.State == "done" && step.Known && i < len(t.planned) {
			actual += step.Seconds
			expected += (t.planned[i][0] + t.planned[i][1]) / 2
		}
	}
	if expected < 10 {
		return
	}
	scale := min(max(actual/expected, 0.5), 4)
	for i := range t.progress.Steps {
		step := &t.progress.Steps[i]
		if step.State == "pending" && i < len(t.planned) {
			step.LowSeconds, step.HighSeconds = t.planned[i][0]*scale, t.planned[i][1]*scale
		}
	}
}

func (t *initializeTracker) publish(at time.Time) {
	t.progress.UpdatedAt = formatTime(at)
	data, err := json.Marshal(t.progress)
	if err != nil {
		return
	}
	// A progress note, not catalog state: no drive flush.
	temporary := t.path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if os.WriteFile(temporary, data, 0o644) == nil && os.Rename(temporary, t.path) != nil {
		_ = os.Remove(temporary)
	}
}

// record adds this run to the library's history: called once the last step
// has run, inside Initialize, so a failed upgrade records nothing.
func (t *initializeTracker) record(c *Catalog) {
	if t == nil {
		return
	}
	at := time.Now()
	t.finishCurrent(at)
	t.progress.Current = len(t.progress.Steps)
	ctx := context.Background()
	history := loadInitializeHistory(ctx, c.DB)
	run := initializeRun{At: formatTime(t.started), Seconds: at.Sub(t.started).Seconds()}
	for _, step := range t.progress.Steps {
		run.Steps = append(run.Steps, initializeRunStepLog{Name: step.Name, Seconds: step.Seconds, Rows: step.rows(), OneOffs: step.OneOffs})
	}
	history.Runs = append(history.Runs, run)
	if len(history.Runs) > initializeHistoryKeep {
		history.Runs = history.Runs[len(history.Runs)-initializeHistoryKeep:]
	}
	if data, err := json.Marshal(history); err == nil {
		_ = setMeta(c.DB, initializeHistoryKey(), string(data))
	}
}

// close removes the progress file, however Initialize ended.
func (t *initializeTracker) close() {
	if t != nil {
		_ = os.Remove(t.path)
	}
}

func loadInitializeHistory(ctx context.Context, db *sql.DB) initializeHistory {
	var history initializeHistory
	var value string
	if db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", initializeHistoryKey()).Scan(&value) == nil {
		_ = json.Unmarshal([]byte(value), &history)
	}
	return history
}

// rates are the per-unit rate ranges for each kind of one-off work: from
// this library's earlier steps that did one kind of it (their time past
// their routine estimate's middle, over their units), else the default.
// A measured rate keeps a wide range: the same drive runs several times
// slower while other work reads it.
func (h initializeHistory) rates(routine []initializeEstimate) map[string][2]float64 {
	samples := map[string][]float64{}
	for _, run := range h.Runs {
		for _, step := range run.Steps {
			kind, units := "", 0.0
			for _, item := range step.OneOffs {
				if kind != "" && item.Kind != kind {
					kind = "mixed"
				} else {
					kind = item.Kind
				}
				units += item.Units
			}
			index := slices.Index(initializeSteps, step.Name)
			if kind == "" || kind == "mixed" || units <= 0 || index < 0 {
				continue
			}
			spent := step.Seconds
			if routine[index].known {
				spent -= (routine[index].low + routine[index].high) / 2
			}
			if spent > 1 {
				samples[kind] = append(samples[kind], spent/units)
			}
		}
	}
	rates := maps.Clone(initializeDefaultRates)
	for kind, values := range samples {
		rate := median(values)
		rates[kind] = [2]float64{rate * 0.7, rate * 3}
	}
	return rates
}

type initializeEstimate struct {
	known     bool
	low, high float64
	basis     string
}

// routine estimates each step's repeated work: the range of its times in
// earlier upgrades that did no one-off work in it, else this library's
// earlier whole-upgrade times (from the operation journal) split by the
// reference shares; unknown without either.
func (h initializeHistory) routine(ctx context.Context, db *sql.DB) []initializeEstimate {
	estimates := make([]initializeEstimate, len(initializeSteps))
	for i, name := range initializeSteps {
		var times []float64
		for _, run := range h.Runs {
			for _, step := range run.Steps {
				if step.Name == name && step.Rows == 0 {
					times = append(times, step.Seconds)
				}
			}
		}
		if len(times) > 0 {
			sort.Float64s(times)
			estimates[i] = initializeEstimate{known: true, low: times[0] * 0.8, high: times[len(times)-1] * 1.25, basis: "earlier upgrades"}
		}
	}
	missing := slices.ContainsFunc(estimates, func(e initializeEstimate) bool { return !e.known })
	if !missing {
		return estimates
	}
	totals := journalInitializeSeconds(ctx, db)
	if len(totals) == 0 {
		return estimates
	}
	sort.Float64s(totals)
	// The shortest and the median: longer ones did one-off work.
	low, high := totals[0], median(totals)
	for i := range estimates {
		if !estimates[i].known {
			estimates[i] = initializeEstimate{known: true, low: low * initializeStepShare[i], high: high * initializeStepShare[i], basis: "earlier upgrades"}
		}
	}
	return estimates
}

// journalInitializeSeconds are the last successful catalog.initialize
// durations the operation journal kept (it keeps those over two seconds).
func journalInitializeSeconds(ctx context.Context, db *sql.DB) []float64 {
	rows, err := db.QueryContext(ctx, `SELECT (julianday(ended_at)-julianday(started_at))*86400 FROM operation_journal
 WHERE kind='catalog.initialize' AND state='succeeded' AND ended_at IS NOT NULL ORDER BY started_at DESC LIMIT 10`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var seconds []float64
	for rows.Next() {
		var value sql.NullFloat64
		if rows.Scan(&value) == nil && value.Valid && value.Float64 > 0 {
			seconds = append(seconds, value.Float64)
		}
	}
	return seconds
}

// createIndexPattern finds the indexes a statement list creates if missing.
var createIndexPattern = regexp.MustCompile(`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+([A-Za-z_][A-Za-z0-9_]*)\s+ON\s+([A-Za-z_][A-Za-z0-9_]*)`)

// initializeIndexSources are the statement lists Initialize creates indexes
// from, by step. A new index on a large table belongs in one of them, so its
// build is announced and priced.
func initializeIndexSources() map[int][]string {
	return map[int][]string{
		initStepTables:  {schemaSQL()},
		initStepColumns: initializeIndexStatements,
		initStepUsage:   initializeLateIndexStatements,
		initStepPlanner: machineCallIndexes,
	}
}

// planInitializeOneOffs finds each step's one-off work.
func planInitializeOneOffs(ctx context.Context, db *sql.DB) map[int][]initializeOneOff {
	planned := map[int][]initializeOneOff{}
	tables := initializeTables(ctx, db)
	if tables == nil {
		return planned
	}
	add := func(step int, kind, name string, readTables ...string) {
		item := initializeOneOff{Name: name, Kind: kind}
		for _, table := range readTables {
			count, ok := estimatedTableRows(ctx, db, table)
			if !ok {
				item.Unmeasured = true
				continue
			}
			item.Rows += count
			units := float64(count)
			if kind == oneOffStatistics {
				units *= float64(max(1, tableIndexes(ctx, db, table)))
			}
			item.Units += units
		}
		if item.Rows >= initializeMinimumRows || item.Unmeasured {
			planned[step] = append(planned[step], item)
		}
	}
	indexed := map[string]bool{}
	steps := initializeIndexSources()
	for _, step := range slices.Sorted(maps.Keys(steps)) {
		byTable := map[string][]string{}
		for _, source := range steps[step] {
			for _, match := range createIndexPattern.FindAllStringSubmatch(source, -1) {
				index, table := match[1], match[2]
				if tables[table] && !tables["index:"+index] && !slices.Contains(byTable[table], index) {
					byTable[table] = append(byTable[table], index)
				}
			}
		}
		for _, table := range slices.Sorted(maps.Keys(byTable)) {
			name := "Build a new index on " + table
			if count := len(byTable[table]); count > 1 {
				name = fmt.Sprintf("Build %d new indexes on %s", count, table)
			}
			add(step, oneOffIndex, name, table)
			indexed[table] = true
		}
	}
	// ensureQueryPlannerStatistics analyzes a table without statistics for
	// any of its indexes, which includes every index built above.
	missing, err := plannerTablesToAnalyze(ctx, db)
	if err == nil {
		for _, table := range queryPlannerTables {
			if tables[table] && (missing[table] || indexed[table]) {
				add(initStepPlanner, oneOffStatistics, "Gather statistics for "+table, table)
			}
		}
	}
	// scheduleToolDerivations clears every stored usage estimate when the
	// estimator changes.
	if changed, err := usageEstimatorChanged(ctx, db); err == nil && changed {
		add(initStepDerivations, oneOffScan, "Clear usage estimates for the new estimator", usageEstimateTables...)
	}
	return planned
}

// initializeTables lists the catalog's tables, and its indexes as
// "index:name"; nil when the schema cannot be read.
func initializeTables(ctx context.Context, db *sql.DB) map[string]bool {
	rows, err := db.QueryContext(ctx, "SELECT type,name FROM sqlite_schema WHERE type IN ('table','index')")
	if err != nil {
		return nil
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var kind, name string
		if rows.Scan(&kind, &name) != nil {
			return nil
		}
		if kind == "index" {
			name = "index:" + name
		}
		names[name] = true
	}
	return names
}

// estimatedTableRows is a table's row count at its last ANALYZE, else its
// largest rowid; neither reads the table. Each sqlite_stat1 row starts with
// the entries of one index, and a partial index's are only those it holds
// (a few thousand of 2M tool calls), so the count is the largest of them.
func estimatedTableRows(ctx context.Context, db *sql.DB, table string) (int64, bool) {
	var counted sql.NullInt64
	if db.QueryRowContext(ctx, "SELECT max(CAST(stat AS INTEGER)) FROM sqlite_stat1 WHERE tbl=?", table).Scan(&counted) == nil && counted.Valid {
		return counted.Int64, true
	}
	var largest sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT max(rowid) FROM "+sqlIdentifier(table)).Scan(&largest); err != nil {
		return 0, false
	}
	return max(largest.Int64, 0), true
}

// tableIndexes counts a table's indexes, those this build adds included
// once they exist; statistics read each.
func tableIndexes(ctx context.Context, db *sql.DB, table string) int {
	var count int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='index' AND tbl_name=?", table).Scan(&count)
	return count
}

// initializeView is the progress as the window shows it at a moment: the
// running step, the time so far and the range still to go.
type initializeView struct {
	Step      int      `json:"step"`
	Steps     int      `json:"steps"`
	StepName  string   `json:"step_name"`
	Work      []string `json:"work,omitempty"`
	StartedAt string   `json:"started_at"`
	Elapsed   float64  `json:"elapsed_seconds"`
	// Remaining is nil when the steps left cannot all be estimated or the
	// current one has run well past its estimate (Overrun).
	Remaining *timeRange `json:"remaining,omitempty"`
	Overrun   bool       `json:"overrun,omitempty"`
	Label     string     `json:"label"`
	Basis     string     `json:"basis,omitempty"`
	// Segments are the progress bar, a step each in order, and Done the
	// share of it the finished steps fill. The running step's segment
	// shows activity, not a fill: no SQL statement here reports how far it
	// is (docs/progress-estimates.md).
	Segments []initializeSegment `json:"segments"`
	Done     float64             `json:"fraction_done"`
}

// initializeSegment is one step's part of the progress bar.
type initializeSegment struct {
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Fraction float64 `json:"fraction"`
	Seconds  float64 `json:"seconds,omitempty"`
}

// initializeMinimumSegment keeps a quick step visible on the bar.
const initializeMinimumSegment = 0.04

// segments lays the steps out on the bar by their planned weights; a step
// without an estimate weighs as much as the average one with.
func (p initializeProgress) segments() ([]initializeSegment, float64) {
	var known, count float64
	for _, step := range p.Steps {
		if step.Weight > 0 {
			known, count = known+step.Weight, count+1
		}
	}
	fallback := 1.0
	if count > 0 {
		fallback = known / count
	}
	weights := make([]float64, len(p.Steps))
	var total float64
	for i, step := range p.Steps {
		weights[i] = step.Weight
		if weights[i] <= 0 {
			weights[i] = fallback
		}
		total += weights[i]
	}
	// Raise the smallest to the minimum, taking the difference from the rest.
	var small, large float64
	for i := range weights {
		weights[i] /= total
		if weights[i] < initializeMinimumSegment {
			small += initializeMinimumSegment - weights[i]
			weights[i] = initializeMinimumSegment
		} else {
			large += weights[i]
		}
	}
	segments := make([]initializeSegment, len(p.Steps))
	var done float64
	for i, step := range p.Steps {
		fraction := weights[i]
		if fraction > initializeMinimumSegment && large > 0 {
			fraction -= small * fraction / large
		}
		segments[i] = initializeSegment{Name: step.Name, State: step.State, Fraction: fraction, Seconds: step.Seconds}
		if step.State == "done" {
			done += fraction
		}
	}
	return segments, done
}

type timeRange struct {
	Low  float64 `json:"low_seconds"`
	High float64 `json:"high_seconds"`
}

// view is the progress at at.
func (p initializeProgress) view(at time.Time) initializeView {
	view := initializeView{Steps: len(p.Steps), StartedAt: p.StartedAt}
	view.Segments, view.Done = p.segments()
	if started, ok := parseTime(p.StartedAt); ok {
		view.Elapsed = math.Max(0, at.Sub(started).Seconds())
	}
	if p.Current < 0 || p.Current >= len(p.Steps) {
		view.Label = "Preparing the upgrade"
		return view
	}
	current := p.Steps[p.Current]
	view.Step, view.StepName = p.Current+1, current.Name
	for _, item := range current.OneOffs {
		work := item.Name
		if item.Rows > 0 {
			work += " (" + rowCountLabel(item.Rows) + ")"
		}
		view.Work = append(view.Work, work)
	}
	var low, high float64
	known := true
	bases := []string{}
	for i := p.Current; i < len(p.Steps); i++ {
		step := p.Steps[i]
		if !step.Known {
			known = false
			continue
		}
		stepLow, stepHigh := step.LowSeconds, step.HighSeconds
		if i == p.Current {
			if started, ok := parseTime(step.StartedAt); ok {
				spent := at.Sub(started).Seconds()
				if spent > stepHigh+initializeOverrunSeconds {
					view.Overrun = true
				}
				stepLow, stepHigh = math.Max(0, stepLow-spent), math.Max(0, stepHigh-spent)
			}
		}
		low, high = low+stepLow, high+stepHigh
		for _, basis := range strings.Split(step.Basis, "; ") {
			if basis != "" && !slices.Contains(bases, basis) {
				bases = append(bases, basis)
			}
		}
	}
	switch {
	case view.Overrun:
		view.Label = "This step is taking longer than estimated"
	case !known:
		view.Label = "No estimate yet: Pharos has not timed an upgrade of this library"
	default:
		view.Remaining = &timeRange{Low: low, High: high}
		if high < 15 {
			view.Label = "Almost done"
		} else {
			label := rangeLabel(low, high)
			view.Label = strings.ToUpper(label[:1]) + label[1:] + " left"
		}
		view.Basis = "Estimated from " + strings.Join(bases, " and ")
	}
	return view
}

func rowCountLabel(rows int64) string {
	switch {
	case rows >= 1_000_000:
		return strconv.FormatFloat(float64(rows)/1e6, 'f', 1, 64) + "M rows"
	case rows >= 1_000:
		return strconv.FormatInt(rows/1_000, 10) + "k rows"
	}
	return plural(int(rows), "row")
}

// readInitializeProgress is the progress of an Initialize running on the
// catalog at path, if one is.
func readInitializeProgress(path string) (initializeProgress, bool) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), initializeProgressName))
	if err != nil {
		return initializeProgress{}, false
	}
	var progress initializeProgress
	if json.Unmarshal(data, &progress) != nil || len(progress.Steps) == 0 {
		return initializeProgress{}, false
	}
	return progress, true
}
