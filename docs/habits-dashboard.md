# Habits dashboard: design proposal

Draft, 2026-11-03. Nothing here is implemented.

Habits is an Analyze dashboard about **regularity**: when work happens, how
consistently, and who started it. The question is not "how much did I spend"
(Tokens and Cost) or "how many agents ran at once" (Parallelism), but "what
does a normal day and week look like for me and for my agents, and is that
changing?" It is the home for streaks, the contribution calendar, the
weekday × hour punchcard, workday shape, and the rhythm of unattended and
scheduled machine work.

Sources: the competitor research snapshot of 2026-11-02 (`views-oss.md` §A1,
B1 and (c) 1–2, 15; `views-commercial.md` §B and recommendation 6;
`scores-commercial.md` C2 "Flow"), the `rhythm-*` ideas in
[homepage-query-cards.json](homepage-query-cards.json), and the engine in
[dashboard-analysis.md](dashboard-analysis.md).

## 1. What the field does, and what to learn from it

| Surface | Who | Notes worth keeping |
|---|---|---|
| Contribution calendar (day cells, weeks × weekdays) | AgentsView, Tokscale, cc-lens, SpecStory (365 days), Claude Code `/stats` (12 weeks) | AgentsView levels are **quartiles of non-zero days**, so one huge day does not wash out the year. Tokscale levels are fractions of the max day, which does. |
| Weekday × hour punchcard (7×24) | AgentsView, Splitrail, CCHV, SpecStory "When you code", `/insights` | Monday-first rows, local hours. CCHV's tooltip gives count and tokens. |
| Current / longest streak, active days | `/stats`, SpecStory, CodeBurn, Tokscale, cc-lens, Codex admin ranking | A public bug report says `/stats` streaks are wrong. **Definitions are the product**: time zone, what counts as active, and how "today" is treated. |
| Consistency (active days ÷ days) | cc-lens | Cheap and less gamey than a streak. |
| Active vs idle time, gaps capped | AgentsView (300 s), Splitrail (5 min), CodeBurn (60 s) | Every tool picks a different cap. Pharos should make the gap a visible setting, as Parallelism does. |
| Concurrency by session class (interactive / subagent / automated) | AgentsView | The direct ancestor of "human- vs machine-initiated". |
| Wrapped / share card | Tokscale, cc-lens, WakaTime | Year in review with longest streak and a mini calendar. Content-free. |
| Goals, achievements, gamification | WakaTime Goals, vibe-log | Brings people back; also the main risk (see §7). |

**Where Pharos is already ahead.** Nobody else separates typed from pasted or
automated words, or attributes headless runs to the conversation that
launched them, or has an inherited Channel/Machine. Competitor streaks count
"any activity", so a cron job or a `claude -p` loop keeps a streak alive.
Pharos can say whether *you* showed up, whether your *agents* did, and whether
those agents were working *with* you, *for* you while you were away, or *on a
schedule*. That human/machine split is the core of this dashboard.

## 2. The model: actors and initiation

Every habit view answers for one **actor**:

| Actor | Meaning | Evidence today |
|---|---|---|
| **You** | Your own attention: genuine human messages. | `writing_messages` with typed text; `conversation_activity.human_messages`. |
| **Agents, with you** | Agent work in a conversation you started, while you are engaged in it. | Parallelism turns, joined to your engaged blocks (§3). |
| **Agents, unattended** | Agent work you started, continuing while you are not engaged: long runs, delegated subagents, background sessions. | Same, outside engaged blocks. |
| **Agents, automated** | Work no human message started at that moment: scheduled heartbeats, automation senders, scripted headless streams, app-internal runs (Conductor's title `codex exec`). | Authorship's Automated category and its reasons; `conversation_launches`. |

The split needs one new conversation-level attribute, **initiator**, derived
with the authorship classification from the root conversation's first user
message:

| `initiator` | Rule (first rule that applies) |
|---|---|
| `scheduled` | `<heartbeat>` envelope with an automation ID, or `automation:<key>` sender. |
| `app` | A headless run the app started in a work's folder (Conductor titles and summaries). |
| `scripted` | A headless run (`claude -p`, `agy -p`, `codex exec`, Claude SDK not started by Conductor). It keeps its `conversation_launches` link, so it can still be attributed to the session whose script launched it. |
| `delegated` | A subagent or launched run; inherits its root's initiator for "who started this work". |
| `human` | The root's first user message has typed text from you. |
| `unknown` | Anything else; shown, never folded into `human`. |

Two readings follow, and the UI must say which it uses:

- **Own initiator**: a scripted run is automated, even if a session launched it.
- **Root initiator**: a scripted run launched by your session is your work,
  done by machine. This is the "leverage" reading.

Default to own initiator for "Agents, automated" and root initiator for
"Agents, unattended". `initiator` should also become a breakdown wherever
Channel is (parallelism, usage, tool calls). Parallelism today counts all three
providers' conversations with no notion of who started them, so a scheduled
heartbeat inflates "agents at once".

## 3. Engaged time (the "You" side)

Message counts make a poor habit measure: one long prompt and ten quick
"yes" replies are different days. Define an **engaged block**: consecutive
human messages (across all conversations, after mirror removal) whose gaps are
at most `engaged_gap` minutes. A block runs from its first message minus a
**lead-in** (the typing time implied by its typed words at a conservative rate,
capped) to its last message plus a short tail. That gives:

- **Engaged minutes** per local hour, and so per day.
- **First and last engaged time** per day (workday shape).
- **Blocks ≥ 25 minutes** ("focus blocks"), and **project switches** inside a
  block (the deferred `rhythm-02`).

`engaged_gap` is a saved setting with a default of 15 minutes. It mirrors
Parallelism's `gap_minutes`, is visible in a note, and travels in links (see
the Parallelism limitation about unsaved settings; this design should take a
per-request override from the start, like Carbon's `carbon_settings`).

It is an estimate. Reading an agent's reply, or thinking before typing,
leaves no trace. The note says "estimated from message times", as Parallelism
does.

## 4. Data: one hourly occupancy dataset

Streaks, calendars and punchcards are all folds of one fact: **how much of
each local hour each actor was active**. Parallelism already publishes this
shape for agents (one row per local hour × unit × level). Rather than five
bespoke queries, add one derived dataset:

**`rhythm`**: one row per local clock hour × actor × unit, over the same
400-day period.

| Field | Meaning |
|---|---|
| `started_at`, `day`, `hour_of_day`, `weekday`, `week`, `month` | Local hour, as in `parallelism`. |
| `actor` | `you`, `agents_with_you`, `agents_unattended`, `agents_automated`. |
| `initiator`, `root_initiator` | §2. |
| `unit_id`, `conversation_id`, `repository_name`, `workspace_name`, `provider`, `model`, `channel`, `machine_id` | As in parallelism and the origin fields. |
| `active_ms`, `elapsed_ms` | Unit time, and its share of wall time (sums never double-count), exactly Parallelism's pair. |
| `level` | Agents at once (agent actors only). |
| `human_messages`, `typed_words` | On `you` rows, so a qualifying day can be defined in words or messages. |

Derivation follows the existing pattern in `parallelism.go`: per-conversation
spans stored incrementally (dirty marking, short read snapshots and write
transactions in `maintainLibrary`), never written on a request path
(requests never write). Human engaged spans need message timestamps and
authorship, so they rebuild on the authorship cadence (Hourly today).

**Why not compute streaks in SQL or in Go?** A streak is a fold over a daily
series. The browser can read ≤ 400 daily totals from `/metrics` and fold them
in a pure, unit-tested module, as `cycleResult` already folds usage cycles.
Keeping the definition in one TypeScript function (`habit-model.ts`) means the
dashboard, cards, the menu bar and MCP all share it. MCP and Ask Pharos would
need the same fold in Go; a shared fixture in `testdata/habit-streaks.json`
keeps the two in step, as `testdata/carbon-estimates.json` does for Carbon.

**Longer history.** 400 days bounds "longest streak ever". Daily totals are
tiny, so a second, day-grained table without the 400-day bound (`rhythm_days`:
day × actor × initiator) would let lifetime streaks and the yearly Wrapped
reach back to the first transcript. That can wait until after phase 2.

## 5. Views

Each view below is an **engine capability**, available to any catalog whose
records carry a timestamp, not a Habits-only chart, so that every card opens a
standard dashboard, as every Home card must.

### 5.1 Calendar (new engine view: `view: "calendar"`)

- Columns are ISO weeks and rows are weekdays, Monday first. One cell per local day.
- **Levels**: 0, plus four quartiles of non-zero days in range (AgentsView), with a
  fixed 1–4 fallback when there are fewer than eight active days. The legend
  shows each level's value range, never a bare "less → more".
- **Two actors, one axis.** "You" and "Agents" are two calendars stacked on the same
  week columns, so a glance shows the days your agents worked without you. A
  third, optional row shows automated work alone.
- Click a day: the trend for that day by hour (fills the "drill-down from a day"
  gap). Ranges: 12 weeks, 6 months, 1 year, or "all retained".
- A qualifying-day threshold (§6) can dim cells below it, so the streak and the
  calendar agree visibly.

### 5.2 Punchcard (cycle key `week_hour`)

- 7 × 24 grid; rows Monday-first; columns local hours; cell area or colour by
  measure, with record and day counts in the tooltip.
- This is a third cycle key beside `hour` and `weekday`. The engine already
  derives both from `cycleFrame`, so 168 slots is one more key (the research
  rates this high confidence). Additive measures fold by total or by mean per
  week; ratios by pooled ratio.
- **Compare two actors**: You vs Agents side by side, or a diverging
  "agents − you" grid that shows where machines carry the hours you don't.

### 5.3 Streaks and consistency (a summary panel any calendar draws)

Tiles: **current streak**, **longest streak** (with dates, click to select them
on the calendar), **active days / days in range**, **active weeks** (≥ N active
days), and **weekday regularity** (share of active days falling on the user's
three most common weekdays). Each tile states its definition in its tooltip.

### 5.4 Workday shape

Per day, a vertical bar from first to last engaged minute, with engaged blocks
drawn solid and gaps hollow; agent unattended time is drawn beside it in the
agents' colour. Over weeks this shows "starting later", "ending after
midnight", "agents keep going after I stop". Needs `rhythm` (§4); it is a
records view of daily first/last values rather than a new chart type, so it
fits Distribution (box per week of start times) as a first cut.

### 5.5 Parallelism habits

Parallelism stays the place for the ribbon. Habits draws its *regularity*:
calendar of peak agents at once, streak of days with ≥ 2 agents at once, share
of engaged time with ≥ 2 agents running (your "fan-out habit"), punchcard of
parallel hours. All are existing parallelism measures under the calendar and
punchcard views, plus `initiator`.

### 5.6 Machine habits: scheduled and scripted work

Automations have habits of their own, and these are about **reliability**, not
encouragement:

- One row per automation (heartbeat `automation_id` or `automation:<key>`) with a
  calendar strip of runs, the **inferred cadence** (median gap between runs),
  **missed runs** (gaps over 1.5 × cadence), last run, and agent hours per run.
- Scripted streams (from `conversation_launches`): runs per stream, the session that
  launched them, and their burst shape. These are what make a day look busy
  when you weren't there.

This is the part no competitor has, because no competitor knows which runs
were automated.

## 6. Definitions that must be fixed and tested

These are where `/stats`-style bugs come from. Each is a pure function in
`habit-model.ts` with node tests, plus a Go fixture where MCP needs it.

- **Day boundary.** The viewer's local zone, from timestamps (the engine's
  `timestamp` cycle rule), not the recording Mac's `day`. A Mac that travelled
  can file a late record on a different day than the day trend; the note says
  so, as the engine's does. DST days have 23 or 25 hours.
- **Optional day-start hour.** A "day" starting at 04:00 local, so midnight
  sessions belong to the evening before. It is a setting, off by default, and it
  shifts the calendar, punchcard and streaks together.
- **Qualifying day.** Per actor, a threshold the user can see and change:
  - You: ≥ 1 message with typed text, *or* ≥ N engaged minutes (default 10).
    Template-only and automated messages never qualify.
  - Agents: ≥ N active agent minutes (default 10), by own or root initiator.
  The threshold is in the view state, so a link reproduces the streak.
- **Today.** The current streak counts through yesterday when today has no
  qualifying activity yet, and today shows as "pending", never as a broken
  streak (GitHub's convention). At the evaluation time's midnight the state
  rolls over, matching the Today range rules in dashboard-analysis.md.
- **Rest days.** An optional "weekdays only" streak (weekends neither break nor
  extend it) and an optional allowance of *k* skipped days per rolling week.
  Both are labelled in the tile; the default is plain consecutive days.
- **Coverage.** A day with no transcripts because a Mac's library was not
  captured is not a day off. The calendar marks days outside a machine's
  captured coverage as hatched "not recorded", and streaks treat them as
  unknown. A streak that bridges an unknown day shows a mark there.
- **Mirrors and identity links** are removed first, as in Usage and Parallelism.

## 7. Health, not engagement-bait

Pharos's own card guidance says "activity is not measured productivity". A
habit dashboard that only celebrates longer streaks rewards overwork. Put
health measures on the same page, with equal weight:

- **Rest**: days off in the last 4 weeks, longest stretch without a day off.
- **Typical day**: median engaged time on days you worked. (A late-hours
  measure was dropped on review: the page should not encourage working late
  or early.)
- **Delegation leverage**: agent unattended hours per engaged hour, which is a good
  habit when it rises.
- **Interrupted focus**: share of engaged time in blocks shorter than 25 minutes.

No leaderboards or public profiles. A **Wrapped** export (yearly, a fixed PNG
with no transcript content) is a later, optional addition; it reuses the
calendar and tiles.

## 8. Cards

Each card maps to a Habits dashboard state; nothing renders bespoke. Ids are
provisional; existing ideas are reused where they fit.

| Card | Dataset / view | Idea |
|---|---|---|
| Your streak (current, longest, last 12 weeks) | `rhythm` actor=you, calendar | new `habits-01` |
| Agent streak, split by initiator | `rhythm` agents, calendar | `habits-02` |
| When you work (7×24) | `writing_messages` or `rhythm` you, cycle `week_hour` | `rhythm-01` (closes its H04/H12 gap) |
| When your agents work vs when you do | `rhythm`, diverging punchcard | `habits-03` |
| Consistency this quarter | `rhythm` you, active days ÷ days | `habits-04` |
| Workday shape | `rhythm` you, first/last engaged by week | `habits-05` |
| Agents working while you're away | `rhythm` agents_unattended, trend | `habits-06` |
| Fan-out habit: days with 2+ agents at once | `parallelism`, calendar | `rhythm-03` |
| Scheduled runs: on time, missed | `rhythm` agents_automated by automation | `habits-07` |
| Focus blocks per week | `rhythm` you, blocks ≥ 25 min | `habits-08`; leads to `rhythm-02` |
| Rest days and typical day | `rhythm` you | `habits-09` |
| Highlights vs previous window | any of the above, norm line | uses the existing `norm` |

## 9. Where this sits in the app

- **Analyze → Dashboards → Habits** (`topic: "habits"`, dataset `rhythm`), a
  `CatalogDashboard` whose default view is Calendar with the streak panel above
  and the automations table below, as Parallelism keeps its ribbon.
- **Home**: the streak card in the conveyor; the menu bar can optionally show
  "🔥 12 days" beside words typed.
- **MCP / Ask Pharos**: `browse_site`/`site_link` know the new views via the
  site map. A `get_habits` summary (streaks, consistency, last 7 days by
  actor) answers "how consistent have I been?" without a chart.
- **URL state**: `view: "calendar"`, `cycle: "week_hour"`, `actor`, `threshold`,
  `day_start`, `engaged_gap` are view inputs in `web/src/view-contract.ts`; the
  automations table rows use stable automation ids. Run
  `generate-view-controls` and `generate-site-map`.

## 10. Dependencies and decisions

- **Rendering a two-key grid.** Query Table request H12 ("bounded two-key
  heatmap") is open. Thinking's time-of-day calendar is drawn by Pharos
  (`thinking-dashboard.tsx`), so a Pharos-owned calendar/punchcard renderer has
  precedent and is not a Query Table patch. **Decision needed**: build the
  calendar and punchcard as Pharos engine views now (and move them to Query Table
  when H12 ships), or wait for H12. Recommendation: build them in Pharos,
  shaped like the H12 proposal so the move is mechanical, and send an updated
  H12 handoff with these two layouts as its examples.
- **Hour-of-week fold.** For timestamp catalogs, Query Table groups `hourOfDay`
  and `dayOfWeek` natively; `week_hour` either needs a combined grouping (a
  small upstream request, H04 family) or a two-key query folded in the browser
  as usage cycles are. The browser fold works today.
- **Per-request settings.** `rhythm` should accept `engaged_gap` and
  `day_start` per request from the start (a Go change Parallelism still lacks).

## 11. Phases

1. **Views over existing data.** No new dataset.
   - Engine: calendar view, `week_hour` cycle, streak panel, `habit-model.ts` with
     tests (DST, travel, today pending, thresholds, rest-day options).
   - Catalogs that gain them: Human Words (you), Parallelism (agents),
     Conversations.
   - Cards: your streak (typed messages), When you work 7×24, fan-out calendar.
   - This alone matches every competitor's habit surface, with a better "you".
2. **Initiator.** Conversation-level `initiator`/`root_initiator` from
   authorship; a breakdown on parallelism, usage and tool calls; parallelism can
   exclude automated runs. The Habits dashboard appears with "Agents, automated"
   separate.
3. **`rhythm` dataset.** Engaged blocks, agents with you vs unattended, workday
   shape, focus blocks, health measures, per-request settings, MCP summary.
4. **Machine habits.** Automation cadence, missed runs, scripted streams.
5. **Later.** `rhythm_days` for lifetime streaks, Wrapped export, menu-bar streak,
   project switching (`rhythm-02`), resumed tasks (`rhythm-04`).

## 12. Decisions (2026-11-03)

1. Agent work you started but did not attend (unattended) does **not** count
   toward your streak. Agents have their own streak.
2. One typed message makes an active day for you (`youMessages`, default 1).
   Agents need 10 minutes (`agentMinutes`).
3. Both plain consecutive and weekdays-only streaks, as a page filter
   (`habits_streak`). Vacation days, saved in `pharos-habits-v1`, neither
   break nor extend any streak; mark them from the calendar's day details.
4. "Your streak" and "Agent streak" are optional items in the menu bar editor
   (group `habits`, off by default). They read through `/api/menu-bar/usage`,
   only when shown, and always count every day.
5. Calendars and punchcard are Pharos-drawn SVG for now; move to Query Table
   when H12 ships.

## 13. Implementation

- Service: `internal/archive/rhythm.go` (the `rhythm` dataset: one record per
  local hour × actor × top-level conversation) and `internal/archive/habits.go`
  (`/api/habits`: days, streaks, windows, punchcard, automations).
- Page: `web/src/habits.tsx` (panel) over `web/src/habits-model.ts`, with
  `habitsAnalysisCatalog` driving the standard views below it.
- Settings: `pharos-habits-v1` (vacations, thresholds, engaged gap, day start), shared by the page, the menu bar and the `rhythm` records.
- Cards: each section (streaks, calendar, punchcard, your days, pace,
  automatic runs) is a dashboard card target (`web/src/habits-card.ts`),
  added from its heading's "Add to Home". The Card Library offers nine of them
  (`web/src/habits-card-recipes.ts`): your streak, weekday streak, your active
  days, days with agents you started, when you work, when agents work while
  you're away, your days, your pace and automatic runs. Each template is the
  Habits page at its selectors plus `reveal=section:<target>`, so it opens on
  its section; it draws, and is collected, as that dashboard card. The landing
  also tells apart templates that share the page's default address, which is
  why the site map reader matches a card's own `reveal`. These supersede the
  provisional table in §8.
