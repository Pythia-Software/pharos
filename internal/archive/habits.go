package archive

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

// Habits summarizes the rhythm table by day: what each actor did, which days
// count toward a streak, the streaks, a weekday × hour punchcard, and the
// automations' runs. The Habits page, the menu bar and MCP read this one
// summary, so a streak means the same everywhere (docs/habits-dashboard.md).
//
// A day is a local calendar day starting at the settings' day start (0–6
// h), so work past midnight can belong to the evening before. Streaks count
// days that qualify: yours a day with at least YouMessages typed messages,
// your agents' one with at least AgentMinutes of agent time you started,
// automation's one with any automated turn. Excused days neither break nor
// extend a streak: vacation days, and Saturdays and Sundays in the weekdays
// mode. Today, until it qualifies, is pending: the current streak runs
// through yesterday.
const (
	habitsModeAll      = "all"
	habitsModeWeekdays = "weekdays"
)

var habitsWindows = []int{7, 30, 90, 365}

type habitsActorDay struct {
	ElapsedMs float64 `json:"elapsed_ms"`
	ActiveMs  int64   `json:"active_ms"`
	Turns     int64   `json:"turns"`
}

type habitsYouDay struct {
	EngagedMs   float64 `json:"engaged_ms"`
	Messages    int64   `json:"messages"`
	TypedWords  int64   `json:"typed_words"`
	First       *int64  `json:"first,omitempty"`
	Last        *int64  `json:"last,omitempty"`
	FocusBlocks int     `json:"focus_blocks"`
	FocusMs     int64   `json:"focus_ms"`
	// Blocks are the day's engaged blocks, [start, end) in Unix ms.
	Blocks [][2]int64 `json:"blocks,omitempty"`
}

type habitsDay struct {
	Day        string         `json:"day"`
	Weekday    int            `json:"weekday"` // 0 is Monday
	Vacation   bool           `json:"vacation,omitempty"`
	Excused    bool           `json:"excused,omitempty"`
	You        habitsYouDay   `json:"you"`
	WithYou    habitsActorDay `json:"with_you"`
	Unattended habitsActorDay `json:"unattended"`
	Automated  habitsActorDay `json:"automated"`
	// AgentsMs is wall time with any agent you started active, with you or
	// unattended: the agents' streak. Automatic runs neither add to it nor
	// share it.
	AgentsMs   int64           `json:"agents_ms"`
	ParallelMs float64         `json:"parallel_ms"`
	PeakAgents int             `json:"peak_agents"`
	Qualifies  map[string]bool `json:"qualifies"`
}

type habitsStreak struct {
	Current     int    `json:"current"`
	CurrentFrom string `json:"current_from,omitempty"`
	Longest     int    `json:"longest"`
	LongestFrom string `json:"longest_from,omitempty"`
	LongestTo   string `json:"longest_to,omitempty"`
	// done, pending (no qualifying activity yet) or excused.
	Today string `json:"today"`
}

type habitsWindow struct {
	Days       int            `json:"days"`
	Active     map[string]int `json:"active"`
	Considered int            `json:"considered"`
}

type habitsAutomation struct {
	Initiator   string         `json:"initiator"`
	Label       string         `json:"label"`
	Runs        int            `json:"runs"`
	ActiveDays  int            `json:"active_days"`
	AgentHours  float64        `json:"agent_hours"`
	FirstRun    int64          `json:"first_run"`
	LastRun     int64          `json:"last_run"`
	MedianGapMs int64          `json:"median_gap_ms"`
	Missed      int            `json:"missed"`
	Daily       map[string]int `json:"daily"`
}

type habitsPunchcard struct {
	From  string                    `json:"from"`
	To    string                    `json:"to"`
	Weeks [7][24]int                `json:"weeks"` // occurrences of each weekday × hour slot
	Cells map[string][7][24]float64 `json:"cells"` // ms by actor
}

type habitsSummary struct {
	GeneratedAt string                  `json:"generated_at"`
	Timezone    string                  `json:"timezone"`
	Today       string                  `json:"today"`
	PeriodStart string                  `json:"period_start"`
	Mode        string                  `json:"mode"`
	Settings    habitsSettings          `json:"settings"`
	Days        []habitsDay             `json:"days"`
	Streaks     map[string]habitsStreak `json:"streaks"`
	Windows     []habitsWindow          `json:"windows"`
	Punchcard   habitsPunchcard         `json:"punchcard"`
	Automations []habitsAutomation      `json:"automations"`
	Coverage    map[string]int          `json:"coverage"`
}

// habitStreakActors are the streaks the summary keeps, in display order.
var habitStreakActors = []string{actorYou, "agents", actorAutomated}

// habitDay is the day an instant belongs to under dayStart: its local date,
// or the date before when the local clock reads earlier than dayStart. The
// clock decides, not elapsed hours, so a DST change never moves the boundary.
func habitDay(at int64, dayStart int) time.Time {
	local := time.UnixMilli(at).In(time.Local)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	if local.Hour() < dayStart {
		day = day.AddDate(0, 0, -1)
	}
	return day
}

// habitDayEnd is when day ends under dayStart: the first instant habitDay
// files under the next date. When DST skips dayStart's hour, time.Date may
// resolve it backwards, into the day before; step forward until the clock
// has reached the next day.
func habitDayEnd(day time.Time, dayStart int) int64 {
	next := day.AddDate(0, 0, 1)
	end := time.Date(next.Year(), next.Month(), next.Day(), dayStart, 0, 0, 0, time.Local)
	for step := 0; step < 4 && habitDay(end.UnixMilli(), dayStart).Before(next); step++ {
		end = end.Add(30 * time.Minute)
	}
	return end.UnixMilli()
}

// splitByDay calls each with the parts of [start, end) in each day under
// dayStart. Every part is nonempty, so the walk always advances.
func splitByDay(start, end int64, dayStart int, each func(start, end int64)) {
	for start < end {
		part := min(end, habitDayEnd(habitDay(start, dayStart), dayStart))
		if part <= start {
			part = end
		}
		each(start, part)
		start = part
	}
}

// Habits summarizes the rhythm table. mode is all or weekdays; punchcardDays
// is how many days, through today, the punchcard and automation strips cover
// (zero or less: the whole period).
func (c *Catalog) Habits(ctx context.Context, mode string, punchcardDays int) (*habitsSummary, error) {
	settings := c.habitsSettings(ctx)
	table, err := c.rhythm(ctx, settings.EngagedGap)
	if err != nil {
		return nil, err
	}
	return summarizeHabits(table, settings, mode, punchcardDays, c.clock()), nil
}

func summarizeHabits(table *rhythmTable, settings habitsSettings, mode string, punchcardDays int, now time.Time) *habitsSummary {
	if mode != habitsModeWeekdays {
		mode = habitsModeAll
	}
	started := time.UnixMilli(table.start).In(time.Local)
	first, today := time.Date(started.Year(), started.Month(), started.Day(), 0, 0, 0, 0, time.Local), habitDay(now.UnixMilli(), settings.DayStart)
	summary := &habitsSummary{GeneratedAt: now.UTC().Format(time.RFC3339), Timezone: time.Local.String(), Today: today.Format("2006-01-02"),
		PeriodStart: first.Format("2006-01-02"), Mode: mode, Settings: settings, Streaks: map[string]habitsStreak{}, Automations: []habitsAutomation{},
		Coverage: map[string]int{"turns": table.coverage.Turns, "unmatched_turns": table.coverage.UnmatchedTurns, "typed_messages": table.coverage.TypedMessages, "messages": table.coverage.Messages}}
	vacations := map[string]bool{}
	for _, day := range settings.Vacations {
		vacations[day] = true
	}
	index := map[string]int{}
	for day := first; !day.After(today); day = day.AddDate(0, 0, 1) {
		name, weekday := day.Format("2006-01-02"), (int(day.Weekday())+6)%7
		index[name] = len(summary.Days)
		summary.Days = append(summary.Days, habitsDay{Day: name, Weekday: weekday, Vacation: vacations[name],
			Excused: vacations[name] || mode == habitsModeWeekdays && weekday >= 5, Qualifies: map[string]bool{}})
	}
	dayOf := func(at int64) *habitsDay {
		if at, ok := index[habitDay(at, settings.DayStart).Format("2006-01-02")]; ok {
			return &summary.Days[at]
		}
		return nil
	}

	// The punchcard's whole local days, through today; zero or less is the
	// whole period.
	cardFrom := first
	if punchcardDays > 0 {
		cardFrom = today.AddDate(0, 0, 1-min(max(punchcardDays, 7), 400))
	}
	summary.Punchcard = habitsPunchcard{From: cardFrom.Format("2006-01-02"), To: summary.Today, Cells: map[string][7][24]float64{}}
	// Cells are the clock's weekday and hour; a day starting after midnight
	// moves which of them the range holds, so each slot counts its own, up to
	// the current hour.
	for at := habitDayEnd(cardFrom.AddDate(0, 0, -1), settings.DayStart); at < min(habitDayEnd(today, settings.DayStart), now.UnixMilli()); at += 3_600_000 {
		local := time.UnixMilli(at).In(time.Local)
		summary.Punchcard.Weeks[(int(local.Weekday())+6)%7][local.Hour()]++
	}
	cells := map[string]*[7][24]float64{}
	for _, actor := range []string{actorYou, actorWithYou, actorUnattended, actorAutomated} {
		cells[actor] = &[7][24]float64{}
	}

	automations, keys := map[string]*habitsAutomation{}, []string{}
	for _, row := range table.rows {
		unit, day := table.units[row.unit], dayOf(row.hour)
		local, inRange := time.UnixMilli(row.hour).In(time.Local), !habitDay(row.hour, settings.DayStart).Before(cardFrom)
		if inRange {
			cells[unit.actor][(int(local.Weekday())+6)%7][local.Hour()] += row.elapsed
		}
		if day == nil {
			continue
		}
		switch unit.actor {
		case actorYou:
			day.You.EngagedMs += row.elapsed
			day.You.Messages += row.starts
			day.You.TypedWords += row.typedWords
			continue
		case actorWithYou:
			day.WithYou.add(row)
		case actorUnattended:
			day.Unattended.add(row)
		case actorAutomated:
			day.Automated.add(row)
			if !inRange {
				break
			}
			key := unit.initiator + "\x00" + unit.automation
			if automations[key] == nil {
				automations[key] = &habitsAutomation{Initiator: unit.initiator, Label: unit.automation, Daily: map[string]int{}}
				keys = append(keys, key)
			}
			automations[key].AgentHours += float64(row.active) / 36e5
		}
		day.ParallelMs += row.parallel
		day.PeakAgents = max(day.PeakAgents, row.peak)
	}
	for actor, grid := range cells {
		summary.Punchcard.Cells[actor] = *grid
	}
	// A block crossing the day's boundary is split there: each day gets its
	// part, and a focus block's minutes go to the day they fell on. It counts
	// as one focus block, on the day it started.
	for _, block := range table.blocks {
		focus := block.end-block.start >= rhythmFocusMs
		splitByDay(block.start, block.end, settings.DayStart, func(start, end int64) {
			day := dayOf(start)
			if day == nil {
				return
			}
			day.You.Blocks = append(day.You.Blocks, [2]int64{start, end})
			if day.You.First == nil || start < *day.You.First {
				day.You.First = &start
			}
			if day.You.Last == nil || end > *day.You.Last {
				day.You.Last = &end
			}
			if focus {
				day.You.FocusMs += end - start
				if start == block.start {
					day.You.FocusBlocks++
				}
			}
		})
	}
	// The agents you started, as one wall clock per day.
	yours := parallelismSpans{}
	for _, unit := range table.units {
		if unit.actor == actorWithYou || unit.actor == actorUnattended {
			yours = append(yours, unit.spans...)
		}
	}
	for _, span := range mergeSpans(yours, 0) {
		splitByDay(span[0], span[1], settings.DayStart, func(start, end int64) {
			if day := dayOf(start); day != nil {
				day.AgentsMs += end - start
			}
		})
	}

	// Automation runs: each turn a script, schedule or agent started within
	// the range, so runs, days, cadence and misses describe the range.
	starts := map[string][]int64{}
	for _, unit := range table.units {
		if unit.actor == actorAutomated {
			key := unit.initiator + "\x00" + unit.automation
			for _, at := range unit.starts {
				if !habitDay(at, settings.DayStart).Before(cardFrom) {
					starts[key] = append(starts[key], at)
				}
			}
		}
	}
	for _, key := range keys {
		automation, runs := automations[key], starts[key]
		slices.Sort(runs)
		if len(runs) == 0 {
			continue
		}
		automation.Runs, automation.FirstRun, automation.LastRun = len(runs), runs[0], runs[len(runs)-1]
		active := map[string]bool{}
		for _, at := range runs {
			name := habitDay(at, settings.DayStart).Format("2006-01-02")
			active[name] = true
			automation.Daily[name]++
		}
		automation.ActiveDays = len(active)
		automation.MedianGapMs, automation.Missed = runCadence(runs, automation.Initiator == "scheduled")
		summary.Automations = append(summary.Automations, *automation)
	}
	slices.SortFunc(summary.Automations, func(a, b habitsAutomation) int {
		if a.LastRun != b.LastRun {
			return int(b.LastRun - a.LastRun)
		}
		return int(b.Runs - a.Runs)
	})

	for i := range summary.Days {
		day := &summary.Days[i]
		day.Qualifies[actorYou] = day.You.Messages >= settings.YouMessages
		day.Qualifies["agents"] = day.AgentsMs >= settings.AgentMinutes*60_000
		day.Qualifies[actorAutomated] = day.Automated.Turns > 0
	}
	for _, actor := range habitStreakActors {
		summary.Streaks[actor] = habitStreak(summary.Days, actor)
	}
	for _, days := range habitsWindows {
		window := habitsWindow{Days: days, Active: map[string]int{}}
		for i := max(0, len(summary.Days)-days); i < len(summary.Days); i++ {
			day := summary.Days[i]
			if day.Excused {
				continue
			}
			counted := false
			for _, actor := range habitStreakActors {
				if day.Qualifies[actor] {
					window.Active[actor]++
					counted = true
				}
			}
			// Today counts once it is active; until then it is still open.
			if i < len(summary.Days)-1 || counted {
				window.Considered++
			}
		}
		summary.Windows = append(summary.Windows, window)
	}
	return summary
}

func (day *habitsActorDay) add(row rhythmRow) {
	day.ElapsedMs += row.elapsed
	day.ActiveMs += row.active
	day.Turns += row.starts
}

// habitStreak finds the current and longest runs of qualifying days, skipping
// excused ones.
func habitStreak(days []habitsDay, actor string) habitsStreak {
	streak := habitsStreak{Today: "pending"}
	run, from := 0, ""
	for _, day := range days {
		if day.Excused {
			continue
		}
		if !day.Qualifies[actor] {
			run = 0
			continue
		}
		if run == 0 {
			from = day.Day
		}
		if run++; run > streak.Longest {
			streak.Longest, streak.LongestFrom, streak.LongestTo = run, from, day.Day
		}
	}
	if len(days) == 0 {
		return streak
	}
	last := len(days) - 1
	switch today := days[last]; {
	case today.Excused:
		streak.Today = "excused"
		last--
	case today.Qualifies[actor]:
		streak.Today = "done"
	default:
		last--
	}
	for i := last; i >= 0; i-- {
		if days[i].Excused {
			continue
		}
		if !days[i].Qualifies[actor] {
			break
		}
		streak.Current++
		streak.CurrentFrom = days[i].Day
	}
	return streak
}

// runCadence is the median gap between runs and, for a schedule, how many
// runs its longer gaps missed: a gap of k medians missed k-1 runs once it is
// half a median late.
func runCadence(runs []int64, scheduled bool) (median int64, missed int) {
	if len(runs) < 2 {
		return 0, 0
	}
	gaps := make([]int64, 0, len(runs)-1)
	for i := 1; i < len(runs); i++ {
		if gap := runs[i] - runs[i-1]; gap > 0 {
			gaps = append(gaps, gap)
		}
	}
	if len(gaps) == 0 {
		return 0, 0
	}
	sorted := slices.Clone(gaps)
	slices.Sort(sorted)
	median = sorted[len(sorted)/2]
	if scheduled && median > 0 {
		for _, gap := range gaps {
			if float64(gap) > 1.5*float64(median) {
				missed += int(math.Round(float64(gap)/float64(median))) - 1
			}
		}
	}
	return median, missed
}

// habitsMenuValue is the menu bar's streak line: days, and whether today is
// done yet.
func habitsMenuValue(streak habitsStreak) string {
	days := strconv.Itoa(streak.Current) + " day"
	if streak.Current != 1 {
		days += "s"
	}
	switch streak.Today {
	case "done":
		return days + " · today ✓"
	case "excused":
		return days + " · day off"
	}
	return fmt.Sprintf("%s · today pending", days)
}
