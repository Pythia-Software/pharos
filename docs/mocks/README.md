# Freshness icon comparison

Open [freshness-icons.html](freshness-icons.html) directly in a browser. This
self-contained guide compares the original PR glyph with five alternatives
drawn to [the interface icon standard](../iconography.md): a plain clock, clock
with status mark, single gap to now, existing history symbol, and database with
clock. All examples use simulated data; the reference is the original code-rendered
glyph, not a photograph.

Choose a direction to compare it with the reference in header and card contexts.
Switch the freshness state, light/dark theme, matrix size (14–18px), and monochrome
mode. The guide also shows hover, selected and disabled treatments, both surface
colors, and the selected SVG source. Click a preview icon for example status
details. Choices can be shared in the URL query string. This study does not change
processing behavior. The approved database clock is also implemented in the app.

The database-clock direction is selected by default. Its face fills clockwise as
a pie on a 12-hour scale: 3h is a quarter, 6h a half, and 9h three quarters. At
12h+ it stays full; exact age remains in the status text. Up-to-date data gets a
checkmark instead of the clock. Unknown and missing states retain the database
with unfilled question and dash badges. The enlarged sequence includes the
earlier database-clock reference next to the new states. Only the quantitative
pie is filled; the rest follows the repo's outline icon rules.

# Analysis readiness prototype review

Open [analysis-readiness.html](analysis-readiness.html), or run
`python3 -m http.server ${CONDUCTOR_PORT:-8000} --bind 127.0.0.1 --directory docs/mocks`
from the repository root. The prototype works offline with its adjacent CSS,
model and UI scripts. Data, time, progress and resource capacities are simulated.

Study 03 uses a nav freshness button and one icon on each result. Severity
changes the icon's emphasis; hover or keyboard focus provides the one-sentence
explanation. Try minutes, hours, days and Up to date in the example picker.

- **Analysis page:** switch Chart/Table or add a Message label breakdown. The
  breakdown adds label dependencies to the same writing result.
- **Empty state:** see counts and progress for each prerequisite.
- **Dashboard:** compare the card icons with the header. **Show only Tool calls**
  removes writing/label dependencies from the header summary.
- **Status modal:** read the gap, source-check interval and prerequisite progress.
  It only explains status. **Update settings** opens the full
  settings view, highlighting the relevant stage.
- **Processing map:** click **View processing map** in the modal, or **Processing
  map** in the sidebar. The direct link [#map-all](analysis-readiness.html#map-all)
  opens the interactive graph immediately. Select nodes to inspect triggers,
  gates, tasks and proposed CPU/GPU pools. Switch **This view / All processing**;
  scroll larger graphs or view/copy the generated diagram text. The modal’s
  **Preview example** picker updates the map in place, retaining its scope,
  selected node and scroll position. The same picker updates the status modal;
  Settings uses the page picker. The selected state is preserved on reload and
  can be linked directly, for example `?state=cpu#map-all`.
- **Settings:** the same snapshot shows the workload, other-library resource
  holders, available capacity, automatic mode and analysis progress. All schedule
  controls and **Update now** live here. A request includes unfinished analysis
  across the library, including work outside the selected result. One-time
  requests preserve paused/manual mode and still respect resource limits.
- **Simulation:** in Settings, start a stage, report progress and finish it using
  the separately marked prototype controls. Unknown totals use indeterminate
  bars. **Finish competing work** rehearses contention; **New messages found**
  demonstrates new work arriving during a run.
- **New results ready; old results still shown:** return to the analysis page and choose
  **Refresh these results**. Processing completion alone does not change
  the receipt of the data already on screen. **Up to date** shows the last check time; activity since that check may not yet be included.

The [contract proposal](../handoffs/analysis-readiness-contract.md) describes the
existing server ledger/status stream, query-result receipts, scope registration,
publication races and which graph capabilities are proposed. Production code,
API contracts and dependency packages are unchanged by this experiment.

# Onboarding prototype review

Open [onboarding.html](onboarding.html) in a browser. The file is self-contained:
it needs no server, account, network access, installed service, or sample library.
It runs entirely in memory and resets when reloaded. The displayed data, paths,
sizes, verification, and backups are examples; no real capture or transfer runs.

## Suggested walkthrough

1. Create a library with the app in Applications and the library on an SSD.
   Compare the storage estimate with Conductor selected and unselected. Skip
   backup setup and review what remains available when the SSD is absent.
2. Finish setup and connect to the other example library. Open MCP settings.
   The first library remains assigned, so MCP is unavailable while the app is
   working elsewhere. Save a new assignment explicitly and simulate a request.
3. In Libraries, disconnect the example SSD and reconnect it. The assignment
   remains unchanged. Select another application or Mac in Review controls to
   inspect the independent setting for that context.
4. In Review controls, load Fresh pending history. Step through copy,
   verification, and cleanup. No history leaves the source before verification.
   Source settings remain after its delivered history is gone.
5. Load the interrupted, new-revision, restored-destination, full-destination,
   and incompatible-version scenarios. Inspect the blocked or remaining state.
   Reconnect the SSD through Libraries when needed. Scenarios reset example data.
6. Open Recovery and inspect a separate rehearsal library. Its schedules and
   capture are paused; opening it does not reassign MCP. Compare importing backup
   history into a connected library, which retains that library's preferences.
7. Return to Setup and try the portable-app, existing-library, and backup-recovery
   branches. Small non-history machine settings are explained separately from
   conversation history and caches.

## Review questions

Can you predict where history will be stored, what will disappear from the
source after a move, and what will be unavailable after disconnecting a drive?
Is MCP assignment clear without understanding ports or services? Are the source
storage estimate and optional backup choice understandable? Which choices feel
premature, and where would a diagram or a different default help?

The [plan](../onboarding-and-library-plan.md),
[audit](../onboarding-and-library-audit.md), and
[technical design](../onboarding-and-library-design.md) provide the corresponding
product promises, code evidence, and production build stages.

# Background updates prototype review

Open [background-updates.html](background-updates.html) in a browser. It is
self-contained and runs nothing. Its costs are measured on the euclid library
(2026-10-28/29) where they exist, and are marked as projections where they
don't.

The prototype covers two surfaces, both rendered from one declaration of what
runs Frequently, Hourly or Daily:

- **The status dropdown** (the drive chip). It shows a headline in plain
  words, the work running now, one row per cadence that expands to its items,
  Eject as an action, and "Update everything now" with its cost.
- **Settings → Background updates.** It shows the three cadences with each
  item's cost class, what it feeds and why it sits where it does, an override
  per item, the placement rule, and the daily cost of background work.

Use **Review controls** to switch situations: up to date, frequent or hourly
work running, the drive away overnight, a failed update, paused, and after a
Pharos update. The Eject control there switches between safe and "finishing
a write".

## Review questions

- **The dropdown:** can you tell, without reading Settings, whether a page
  shows recent work, and when it will catch up?
- **The cadence names:** are "Frequently / Hourly / Daily" and the placement
  rule enough to explain why "Your writing" lags?
- **The cost card:** is "5 h of one core per day" useful, or alarming?
- **Overrides:** which per-item override, if any, should exist at all?
