import test from "node:test";
import assert from "node:assert/strict";
import { calendarValue, calendarWeeks, healthStats, level, monthLabels, normalizeHabitsSettings, punchcardGrid, recentWork, quartileLevels, toggleVacation, workdayShape, formatClock, formatGap } from "../../../src/habits-model";
import { habitsAnalysisCatalog } from "../../../src/habits-analysis-catalog";
import { catalogProblems } from "../../../src/dashboard-analysis-model";

const actor = (elapsed = 0, turns = 0) => ({ elapsed_ms: elapsed, active_ms: elapsed, turns });
function day(name, weekday, { messages = 0, engaged = 0, agents = 0, unattended = 0, automated = 0, vacation = false, first, last, blocks, focus = 0 } = {}) {
  return { day: name, weekday, vacation, you: { engaged_ms: engaged, messages, typed_words: 0, first, last, blocks, focus_blocks: focus ? 1 : 0, focus_ms: focus },
    with_you: actor(agents), unattended: actor(unattended), automated: actor(automated, automated ? 1 : 0), agents_ms: agents + unattended, parallel_ms: 0, peak_agents: 0,
    qualifies: { you: messages > 0, agents: agents + unattended >= 600_000, automated: automated > 0 } };
}

test("the habits catalog is a valid engine catalog", () => {
  assert.deepEqual(catalogProblems(habitsAnalysisCatalog), []);
});

test("settings keep what is valid, as the service does", () => {
  const settings = normalizeHabitsSettings({ vacations: ["2026-10-02", "2026-02-30", "2026-10-01", "2026-10-02", 3], youMessages: 2, agentMinutes: 0, engagedGap: 7, dayStart: 4 });
  assert.deepEqual(settings, { vacations: ["2026-10-01", "2026-10-02"], youMessages: 2, agentMinutes: 10, engagedGap: 15, dayStart: 4 });
  assert.deepEqual(toggleVacation(settings, "2026-10-01").vacations, ["2026-10-02"]);
  assert.deepEqual(toggleVacation(settings, "2026-09-30").vacations, ["2026-09-30", "2026-10-01", "2026-10-02"]);
});

test("calendar levels are quartiles of active days, so one huge day does not wash out the rest", () => {
  const values = [0, 1, 2, 3, 4, 5, 6, 7, 8, 1000];
  const thresholds = quartileLevels(values);
  assert.deepEqual(values.map(value => level(value, thresholds)), [0, 1, 1, 1, 2, 2, 3, 3, 4, 4]);
  // Few active days split the largest into quarters.
  assert.deepEqual([0, 1, 4].map(value => level(value, quartileLevels([1, 4]))), [0, 1, 4]);
  assert.equal(level(5, quartileLevels([])), 1);
  // A day that does not qualify draws as empty, whatever time it holds.
  assert.equal(calendarValue(day("2026-10-01", 3, { engaged: 120_000 }), "you"), 0);
  assert.equal(calendarValue(day("2026-10-01", 3, { messages: 1, engaged: 120_000 }), "you"), 120_000);
});

test("calendar weeks run Monday to Sunday and end with today's week", () => {
  // 2026-10-01 is a Thursday; today is Tuesday 2026-10-13.
  const days = Array.from({ length: 13 }, (_, i) => day(`2026-10-${String(i + 1).padStart(2, "0")}`, (i + 3) % 7));
  const weeks = calendarWeeks(days, 3);
  assert.equal(weeks.length, 3);
  assert.deepEqual(weeks[0].map(cell => cell?.day ?? null), [null, null, null, "2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04"]);
  assert.deepEqual(weeks[2].map(cell => cell?.day ?? null), ["2026-10-12", "2026-10-13", null, null, null, null, null]);
  assert.deepEqual(monthLabels(weeks), ["Oct", "", ""]);
});

test("the punchcard is minutes per week in each slot, and can compare agents with you", () => {
  const empty = () => Array.from({ length: 7 }, () => Array(24).fill(0));
  const cells = { you: empty(), with_you: empty(), unattended: empty(), automated: empty() };
  cells.you[0][9] = 4 * 60_000 * 2; // two Mondays, four minutes each
  cells.unattended[0][9] = 10 * 60_000 * 2;
  cells.with_you[0][9] = 2 * 60_000 * 2;
  const summary = { punchcard: { from: "", to: "", weeks: [2, 2, 2, 2, 2, 1, 1].map(count => Array(24).fill(count)), cells } };
  assert.equal(punchcardGrid(summary, "you")[0][9], 4);
  assert.equal(punchcardGrid(summary, "agents")[0][9], 12);
  assert.equal(punchcardGrid(summary, "difference")[0][9], 8);
  assert.equal(punchcardGrid(summary, "automated")[0][9], 0);
});

test("workday shape places a day's engaged blocks after its start, cut at each hour for its colour", () => {
  const at = text => Date.parse(text);
  const blocks = [[at("2026-10-01T09:30:00Z"), at("2026-10-01T11:00:00Z")], [at("2026-10-02T01:00:00Z"), at("2026-10-02T01:15:00Z")]];
  const days = [day("2026-10-01", 3, { messages: 3, engaged: 3_600_000, first: blocks[0][0], last: blocks[1][1], blocks })];
  const [shape] = workdayShape(days, 7, 0, "UTC");
  assert.equal(shape.start, 9.5);
  assert.equal(shape.end, 25.25);
  assert.equal(shape.blockCount, 2);
  assert.deepEqual(shape.blocks.map(piece => [piece.start, piece.end]), [[9.5, 10], [10, 11], [25, 25.25]]);
  // Pieces in different hours take different hour colours.
  assert.notEqual(shape.blocks[0].color, shape.blocks[2].color);
  // A day starting at 04:00 measures from then.
  assert.equal(workdayShape(days, 7, 4, "UTC")[0].start, 5.5);
  // Across daylight-saving changes, pieces sit at the clock times worked:
  // twenty minutes from 01:50 EST skip to 03:00 EDT, and an hour repeated in
  // the autumn keeps the day's end after its start.
  const rounded = shape => [shape.start, shape.end, ...shape.blocks.flatMap(piece => [piece.start, piece.end])].map(value => Math.round(value * 60));
  const spring = workdayShape([day("2026-03-08", 6, { first: at("2026-03-08T06:50:00Z"), last: at("2026-03-08T07:10:00Z"), blocks: [[at("2026-03-08T06:50:00Z"), at("2026-03-08T07:10:00Z")]] })], 1, 0, "America/New_York")[0];
  assert.deepEqual(rounded(spring), [110, 190, 110, 120, 180, 190]);
  const autumn = workdayShape([day("2026-11-01", 6, { first: at("2026-11-01T05:30:00Z"), last: at("2026-11-01T06:10:00Z"), blocks: [[at("2026-11-01T05:30:00Z"), at("2026-11-01T06:10:00Z")]] })], 1, 0, "America/New_York")[0];
  assert.deepEqual(rounded(autumn), [60, 120, 90, 120, 60, 70]);
  assert.equal(formatClock(25.25), "1:15 AM");
  assert.equal(formatClock(0), "12 AM");
  assert.equal(formatClock(13.5), "1:30 PM");
});

test("pace counts rest days, a typical day, agent leverage and focus", () => {
  const days = [
    day("2026-10-01", 3, { messages: 2, engaged: 2 * 3_600_000, unattended: 3_600_000, focus: 3_600_000 }),
    day("2026-10-02", 4),
    day("2026-10-03", 5, { messages: 1, engaged: 2 * 3_600_000, unattended: 3 * 3_600_000, vacation: true }),
    day("2026-10-04", 6, { messages: 1, engaged: 0 }),
  ];
  const health = healthStats(days, 28);
  assert.equal(health.restDays, 2); // a day without work, and a vacation; today is still open
  assert.equal(health.longestWithoutRest, 1);
  // The median of the days you worked with engaged time: 2 h and 2 h.
  assert.equal(health.typicalHours, 2);
  assert.equal(health.leverage, 1);
  assert.equal(health.focusShare, 0.25);
  assert.equal(formatGap(90 * 60_000), "every 1.5 h");
  assert.equal(formatGap(20_000), "under a minute apart");
});

test("the tiles' working time is yesterday's engaged time and the last seven days', today included", () => {
  const days = Array.from({ length: 9 }, (_, i) => day(`2026-10-0${i + 1}`, i % 7, { messages: 1, engaged: (i + 1) * 60_000 }));
  assert.deepEqual(recentWork(days), { yesterdayMs: 8 * 60_000, weekMs: (3 + 4 + 5 + 6 + 7 + 8 + 9) * 60_000 });
  assert.deepEqual(recentWork(days.slice(0, 1)), { yesterdayMs: 0, weekMs: 60_000 });
});
