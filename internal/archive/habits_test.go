package archive

import (
	"slices"
	"testing"
	"time"
)

func TestRhythmEngagementJoinsMessagesWithinTheGap(t *testing.T) {
	minute := int64(60_000)
	typed := []rhythmMessage{
		{at: 100 * minute, conversation: "a", typed: 20},  // 30 s of typing reads as the minimum minute
		{at: 110 * minute, conversation: "b", typed: 200}, // 5 minutes; joins: 10 minutes after the last
		{at: 200 * minute, conversation: "a", typed: 1000},
	}
	blocks, pieces := rhythmEngagement(typed, 15*minute)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].start != 99*minute || blocks[0].end != 112*minute || blocks[0].messages != 2 {
		t.Fatalf("first block = %+v", blocks[0])
	}
	// The second block starts ten minutes (the most typing counts) before.
	if blocks[1].start != 190*minute || blocks[1].end != 202*minute {
		t.Fatalf("second block = %+v", blocks[1])
	}
	want := []rhythmPiece{{[2]int64{99 * minute, 100 * minute}, "a"}, {[2]int64{100 * minute, 110 * minute}, "b"}, {[2]int64{110 * minute, 112 * minute}, "b"},
		{[2]int64{190 * minute, 200 * minute}, "a"}, {[2]int64{200 * minute, 202 * minute}, "a"}}
	if !slices.Equal(pieces, want) {
		t.Fatalf("pieces = %+v", pieces)
	}
	// Messages join by when they were sent: 25 minutes apart is past a
	// 15-minute gap, however long the second took to type, and its typing
	// estimate starts no earlier than the first block ends.
	blocks, _ = rhythmEngagement([]rhythmMessage{{at: 100 * minute, conversation: "a", typed: 1}, {at: 125 * minute, conversation: "a", typed: 400}}, 15*minute)
	if len(blocks) != 2 || blocks[0].end != 102*minute || blocks[1].start != 115*minute {
		t.Fatalf("blocks 25 minutes apart = %+v", blocks)
	}
	blocks, _ = rhythmEngagement([]rhythmMessage{{at: 100 * minute, conversation: "a", typed: 1}, {at: 120 * minute, conversation: "a", typed: 1000}}, 15*minute)
	if len(blocks) != 2 || blocks[1].start != 110*minute {
		t.Fatalf("a long estimate reaching the last block = %+v", blocks)
	}
	// A tail never outlasts a short gap, so blocks stay apart.
	if blocks, _ := rhythmEngagement(typed[:1], 1*minute); blocks[0].end != 101*minute {
		t.Fatalf("tail under a one-minute gap = %+v", blocks)
	}
}

func TestIntersectSpansSplitsATurnAtEngagedBlocks(t *testing.T) {
	blocks := []rhythmBlock{{start: 10, end: 20}, {start: 30, end: 40}, {start: 60, end: 70}}
	inside, outside := intersectSpans([2]int64{15, 50}, blocks)
	if !slices.Equal(inside, parallelismSpans{{15, 20}, {30, 40}}) || !slices.Equal(outside, parallelismSpans{{20, 30}, {40, 50}}) {
		t.Fatalf("inside %v, outside %v", inside, outside)
	}
	inside, outside = intersectSpans([2]int64{41, 59}, blocks)
	if len(inside) != 0 || !slices.Equal(outside, parallelismSpans{{41, 59}}) {
		t.Fatalf("between blocks: inside %v, outside %v", inside, outside)
	}
}

func TestHabitDayFollowsTheLocalClockAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	override(t, &time.Local, zone)
	at := func(text string) int64 {
		value, _ := time.ParseInLocation("2006-01-02 15:04", text, zone)
		return value.UnixMilli()
	}
	for text, want := range map[string]string{
		"2026-03-08 04:30": "2026-03-08", "2026-03-08 03:30": "2026-03-07", // spring forward: 02:00 → 03:00
		"2026-11-01 03:30": "2026-10-31", "2026-11-01 04:00": "2026-11-01", // fall back: 02:00 → 01:00
	} {
		if got := habitDay(at(text), 4).Format("2006-01-02"); got != want {
			t.Errorf("%s with a 04:00 day start = %s, want %s", text, got, want)
		}
	}
	if end := habitDayEnd(habitDay(at("2026-10-31 12:00"), 4), 4); end != at("2026-11-01 04:00") {
		t.Errorf("Oct 31 ends at %v", time.UnixMilli(end).In(zone))
	}
	// With a 02:00 start, spring forward skips the hour the day would start:
	// March 7 runs to 03:00 EDT, and a block across it splits there.
	march7 := habitDay(at("2026-03-07 12:00"), 2)
	if end := habitDayEnd(march7, 2); end != at("2026-03-08 03:00") {
		t.Errorf("March 7 ends at %v", time.UnixMilli(end).In(zone))
	}
	parts := make(chan [][2]int64, 1)
	go func() {
		split := [][2]int64{}
		splitByDay(at("2026-03-08 01:50"), at("2026-03-08 03:40"), 2, func(start, end int64) { split = append(split, [2]int64{start, end}) })
		parts <- split
	}()
	select {
	case split := <-parts:
		if len(split) != 2 || split[0][1] != at("2026-03-08 03:00") || split[0][1]-split[0][0] != 10*60_000 {
			t.Errorf("block across the skipped hour = %v", split)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("splitting a block across the skipped day start did not finish")
	}
}

func habitDays(from string, qualifies string, excused map[int]bool) []habitsDay {
	start, _ := time.Parse("2006-01-02", from)
	days := []habitsDay{}
	for i, mark := range qualifies {
		day := start.AddDate(0, 0, i)
		days = append(days, habitsDay{Day: day.Format("2006-01-02"), Excused: excused[i], Qualifies: map[string]bool{actorYou: mark == '1'}})
	}
	return days
}

func TestHabitStreakSkipsExcusedDaysAndWaitsForToday(t *testing.T) {
	// 0110111 1 0: a three-day run, then today pending.
	streak := habitStreak(habitDays("2026-10-01", "01101110", nil), actorYou)
	if streak.Current != 3 || streak.CurrentFrom != "2026-10-05" || streak.Today != "pending" || streak.Longest != 3 || streak.LongestTo != "2026-10-07" {
		t.Fatalf("pending today: %+v", streak)
	}
	streak = habitStreak(habitDays("2026-10-01", "01101111", nil), actorYou)
	if streak.Current != 4 || streak.Today != "done" || streak.Longest != 4 {
		t.Fatalf("done today: %+v", streak)
	}
	// An excused gap neither breaks nor extends a run.
	streak = habitStreak(habitDays("2026-10-01", "1100111", map[int]bool{2: true, 3: true}), actorYou)
	if streak.Current != 5 || streak.CurrentFrom != "2026-10-01" || streak.Longest != 5 {
		t.Fatalf("excused gap: %+v", streak)
	}
	// An active excused day does not count either.
	streak = habitStreak(habitDays("2026-10-01", "111", map[int]bool{1: true}), actorYou)
	if streak.Current != 2 {
		t.Fatalf("active excused day: %+v", streak)
	}
	streak = habitStreak(habitDays("2026-10-01", "110", map[int]bool{2: true}), actorYou)
	if streak.Today != "excused" || streak.Current != 2 {
		t.Fatalf("excused today: %+v", streak)
	}
	if streak := habitStreak(nil, actorYou); streak.Current != 0 || streak.Today != "pending" {
		t.Fatalf("no days: %+v", streak)
	}
}

func TestRunCadenceCountsMissedScheduledRuns(t *testing.T) {
	hour := int64(3_600_000)
	runs := []int64{0, hour, 2 * hour, 3 * hour, 6 * hour, 7 * hour}
	median, missed := runCadence(runs, true)
	if median != hour || missed != 2 {
		t.Fatalf("median %d, missed %d", median, missed)
	}
	if _, missed := runCadence(runs, false); missed != 0 {
		t.Fatalf("scripted runs miss nothing, got %d", missed)
	}
	if median, missed := runCadence(runs[:1], true); median != 0 || missed != 0 {
		t.Fatalf("one run: %d, %d", median, missed)
	}
}

func TestNormalizeHabitsSettingsKeepsWhatIsValid(t *testing.T) {
	settings := normalizeHabitsSettings(`{"vacations":["2026-10-02","bad","2026-10-01","2026-10-02",3],"youMessages":2,"agentMinutes":0,"engagedGap":7,"dayStart":4}`)
	if !slices.Equal(settings.Vacations, []string{"2026-10-01", "2026-10-02"}) || settings.YouMessages != 2 || settings.AgentMinutes != 10 || settings.EngagedGap != 15 || settings.DayStart != 4 {
		t.Fatalf("settings = %+v", settings)
	}
	if settings := normalizeHabitsSettings("not json"); settings.YouMessages != 1 || settings.EngagedGap != 15 || len(settings.Vacations) != 0 {
		t.Fatalf("defaults = %+v", settings)
	}
}

func TestSummarizeHabitsCountsActorsByDay(t *testing.T) {
	override(t, &time.Local, time.UTC)
	day := func(text string) int64 { at, _ := time.Parse(time.RFC3339, text); return at.UnixMilli() }
	minute := int64(60_000)
	data := &concurrencyData{start: day("2026-10-01T00:00:00Z"), end: day("2026-10-04T00:00:00Z"), canonical: map[string]string{"mirror": "native"},
		Threads: []concurrencyThread{
			{ID: "native", Title: "Work", Turns: parallelismSpans{{day("2026-10-01T09:00:00Z"), day("2026-10-01T10:00:00Z")}, {day("2026-10-02T23:00:00Z"), day("2026-10-02T23:30:00Z")}}},
			{ID: "headless", Title: "Script", Turns: parallelismSpans{{day("2026-10-01T09:30:00Z"), day("2026-10-01T09:40:00Z")}}},
		}}
	messages := []rhythmMessage{
		// Sent to the mirror; it starts the native thread's first turn.
		{at: day("2026-10-01T09:00:00Z"), conversation: "mirror", typed: 30, initiator: "human"},
		{at: day("2026-10-01T09:30:00Z"), conversation: "headless", initiator: "scripted", automation: "claude -p"},
		// No message starts the second turn: it is unknown, and unattended.
	}
	table := newRhythmTable(data, messages, 15)
	if table.coverage.Turns != 3 || table.coverage.UnmatchedTurns != 1 {
		t.Fatalf("coverage = %+v", table.coverage)
	}
	now := time.UnixMilli(day("2026-10-03T12:00:00Z"))
	summary := summarizeHabits(table, defaultHabitsSettings, habitsModeAll, 7, now)
	if len(summary.Days) != 3 || summary.Today != "2026-10-03" {
		t.Fatalf("days = %+v", summary.Days)
	}
	first := summary.Days[0]
	if first.You.Messages != 1 || first.You.EngagedMs != float64(3*minute) || !first.Qualifies[actorYou] || len(first.You.Blocks) != 1 || first.You.Blocks[0][1]-first.You.Blocks[0][0] != 3*minute {
		t.Fatalf("you on day one = %+v", first.You)
	}
	// Two minutes of the turn are within your block's tail; the rest is not.
	if first.WithYou.ActiveMs != 2*minute || first.Unattended.ActiveMs != 58*minute || first.WithYou.Turns != 1 {
		t.Fatalf("agents on day one: with you %+v, unattended %+v", first.WithYou, first.Unattended)
	}
	if first.Automated.Turns != 1 || first.Automated.ActiveMs != 10*minute || first.PeakAgents != 2 || first.ParallelMs != float64(10*minute) {
		t.Fatalf("automation on day one: %+v, peak %d, parallel %v", first.Automated, first.PeakAgents, first.ParallelMs)
	}
	if !summary.Days[1].Qualifies["agents"] || summary.Days[1].Unattended.Turns != 1 {
		t.Fatalf("day two = %+v", summary.Days[1])
	}
	if you := summary.Streaks[actorYou]; you.Current != 0 || you.Longest != 1 || you.Today != "pending" {
		t.Fatalf("your streak = %+v", you)
	}
	if agents := summary.Streaks["agents"]; agents.Current != 2 || agents.Today != "pending" {
		t.Fatalf("agent streak = %+v", agents)
	}
	if len(summary.Automations) != 1 || summary.Automations[0].Label != "claude -p" || summary.Automations[0].Runs != 1 {
		t.Fatalf("automations = %+v", summary.Automations)
	}
	// A scheduled prompt landing as your turn's last response does merges the
	// two in the thread's occupancy; it still starts its own automated turn.
	touching := &concurrencyData{start: data.start, end: data.end, Threads: []concurrencyThread{
		{ID: "shared", Turns: parallelismSpans{{day("2026-10-01T09:00:00Z"), day("2026-10-01T09:40:00Z")}}}}}
	merged := newRhythmTable(touching, []rhythmMessage{
		{at: day("2026-10-01T09:00:00Z"), conversation: "shared", typed: 5, initiator: "human"},
		{at: day("2026-10-01T09:20:00Z"), conversation: "shared", initiator: "scheduled", automation: "nightly"},
	}, 15)
	runs := summarizeHabits(merged, defaultHabitsSettings, habitsModeAll, 7, now)
	if merged.coverage.Turns != 2 || runs.Days[0].Automated.Turns != 1 || runs.Days[0].Automated.ActiveMs != 20*minute ||
		runs.Days[0].WithYou.Turns+runs.Days[0].Unattended.Turns != 1 || len(runs.Automations) != 1 || runs.Automations[0].Label != "nightly" {
		t.Fatalf("touching turns: coverage %+v, day %+v, automations %+v", merged.coverage, runs.Days[0], runs.Automations)
	}
	// The minute of typing falls in the hour before the message.
	if cells := summary.Punchcard.Cells[actorYou][3]; cells[8] != float64(minute) || cells[9] != float64(2*minute) {
		t.Fatalf("Thursday punchcard = %v", cells)
	}
	// A day starting at 04:00 files the 23:00 turn under its own evening,
	// and a vacation on day two removes it from the agents' run.
	settings := defaultHabitsSettings
	settings.DayStart, settings.Vacations = 4, []string{"2026-10-02"}
	summary = summarizeHabits(table, settings, habitsModeAll, 7, now)
	if agents := summary.Streaks["agents"]; agents.Current != 1 || !summary.Days[1].Excused {
		t.Fatalf("agent streak with a vacation = %+v", agents)
	}
	// A block from 23:44 to 00:17 is two days' work: each gets its part,
	// and the focus minutes that fell on it.
	crossing := &rhythmTable{start: day("2026-10-01T00:00:00Z"), blocks: []rhythmBlock{{start: day("2026-10-01T23:44:00Z"), end: day("2026-10-02T00:17:00Z"), messages: 3}}}
	summary = summarizeHabits(crossing, defaultHabitsSettings, habitsModeAll, 7, now)
	if one, two := summary.Days[0].You, summary.Days[1].You; one.FocusMs != 16*minute || two.FocusMs != 17*minute || one.FocusBlocks != 1 || two.FocusBlocks != 0 ||
		len(two.Blocks) != 1 || *two.First != day("2026-10-02T00:00:00Z") || *one.Last != day("2026-10-02T00:00:00Z") {
		t.Fatalf("a block across midnight: %+v / %+v", one, two)
	}
	// Ten minutes of an agent you started counts in full beside a schedule's
	// run, and two of yours at once are one wall clock.
	ten := parallelismSpans{{day("2026-10-01T10:00:00Z"), day("2026-10-01T10:10:00Z")}}
	busy := &rhythmTable{start: day("2026-10-01T00:00:00Z"), units: []rhythmUnit{
		{parallelismUnit: parallelismUnit{spans: ten}, actor: actorUnattended}, {parallelismUnit: parallelismUnit{spans: ten}, actor: actorWithYou},
		{parallelismUnit: parallelismUnit{spans: ten}, actor: actorAutomated, initiator: "scheduled", automation: "nightly"}}}
	if first := summarizeHabits(busy, defaultHabitsSettings, habitsModeAll, 7, now).Days[0]; first.AgentsMs != 10*minute || !first.Qualifies["agents"] {
		t.Fatalf("your agents beside a schedule: %d ms, qualifies %v", first.AgentsMs, first.Qualifies["agents"])
	}
	// Automation statistics describe the range: the run on October 1 is
	// outside the week to October 20, and in the month to it.
	later := time.UnixMilli(day("2026-10-20T12:00:00Z"))
	if summary := summarizeHabits(table, defaultHabitsSettings, habitsModeAll, 30, later); len(summary.Automations) != 1 || summary.Automations[0].Runs != 1 {
		t.Fatalf("automations in the range = %+v", summary.Automations)
	}
	if summary := summarizeHabits(table, defaultHabitsSettings, habitsModeAll, 7, later); len(summary.Automations) != 0 {
		t.Fatalf("automations outside the range = %+v", summary.Automations)
	}
	// An hour every night from 02:00, with days starting at 04:00, is an hour
	// a week in every 02:00 slot, whichever day each night is filed under.
	nightly := &rhythmTable{start: day("2026-09-01T00:00:00Z"), units: []rhythmUnit{{actor: actorYou}}}
	for at := day("2026-09-01T02:00:00Z"); at < day("2026-11-03T00:00:00Z"); at += 24 * 60 * minute {
		nightly.rows = append(nightly.rows, rhythmRow{hour: at, elapsed: float64(60 * minute)})
	}
	early := defaultHabitsSettings
	early.DayStart = 4
	card := summarizeHabits(nightly, early, habitsModeAll, 30, time.UnixMilli(day("2026-11-02T12:00:00Z"))).Punchcard
	for weekday := range 7 {
		if average := card.Cells[actorYou][weekday][2] / float64(card.Weeks[weekday][2]); average != float64(60*minute) {
			t.Fatalf("weekday %d at 02:00: %v ms over %d", weekday, card.Cells[actorYou][weekday][2], card.Weeks[weekday][2])
		}
	}
	if value := habitsMenuValue(habitsStreak{Current: 1, Today: "done"}); value != "1 day · today ✓" {
		t.Fatalf("menu value = %q", value)
	}
}
