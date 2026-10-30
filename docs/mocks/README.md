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
