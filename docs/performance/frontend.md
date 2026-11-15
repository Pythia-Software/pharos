# Frontend CPU and memory audit

Measured on 2026-11-10 against a private APFS copy of a large development
catalog (sources disabled), in headless Google Chrome at 1440 × 900. The
original catalog and the running application were not touched. These are
local measurements, not cross-machine guarantees; compare runs taken back to
back. Headless Chrome reports no separate GPU process time, so committed
frames and style recalculations stand in for GPU work. WebKit, which the Mac
app uses, was not measured.

## Method

`tools/frontend-profile.mjs` opens every route in the site map (plus any
listed in `PHAROS_PROFILE_ROUTES`) and records, per page:

- while loading: main-thread task, script, layout and style time, heap, DOM
  nodes, requests, and long tasks;
- while idle: main-thread share, style recalculations, layouts and requests;
- a scroll pass through the page's largest scroll container.

Pages that stood out were then examined with CPU profiles, heap sampling and
snapshots, and traces of the idle window and of one Home belt slide.

## What the walk found

Of about 66 routes, dashboards, tables and Settings cost 0.4–1 s of main
thread and about 15 MB of heap. Four places stood out:

| Page | Main thread while loading | Heap | Cause |
|---|---|---|---|
| Home | 12–23 s in the first 40 s | 135–180 MB | number formatting, re-rendering every cover per arrival, retained responses |
| Card Library | about 5 s, 40+ long tasks | about 100 MB | the same |
| Parallelism | — | 149 MB | a 47.5 MB `/api/usage/parallelism` snapshot (`model_spans` 31 MB) |
| Reader, 19.6k-message conversation | one 830 ms long task | 162 MB | conversation size |

Every page, when idle, issued about 60 polling requests a minute and ran
about 10 style recalculations and 4.5 committed frames a second.

## Changes and results

| Measure | Before | After |
|---|---|---|
| Home, main thread while loading | 23.3 s | 5.7 s |
| Home, script | 11.0 s | 1.05 s |
| Home, long tasks | 76 | 10 |
| Home, idle main-thread share | 15.6% | 5.9% |
| Home, retained heap (snapshot) | 130 MB | 69 MB |
| Card Library, main thread | 5.3 s | 1.7 s |
| Card Library, long tasks | 43 | 5 |
| Card Library, heap | 96 MB | 71 MB |
| Home belt, main thread per slide | 1.1–1.5 s | 0.36–0.45 s |
| Idle dashboard, style recalculations | 9.5/s | 1.1/s |
| Idle dashboard, committed frames | 4.4/s | 0.4/s |
| Idle dashboard, requests per 35 s | 35 | 14 |
| Card preview cache | 16.6 MB for 144 covers (its cap) | 9.1 MB for all 344 |

- **Number and date formatting.** `toLocaleString(undefined, options)` builds
  an Intl formatter on every call; chart covers call it per mark and tick.
  `web/src/locale-format.ts` keeps one formatter per locale and options, and
  the hot chart code uses it.
- **One render per batch of covers.** Each arriving cover re-rendered every
  card on Home and Card Library. `CardVisualization` is memoized, and
  arrivals are applied together (`batchPreviewArrivals`).
- **Retained responses.** Cover payloads kept labels for every group in the
  source rather than the groups drawn; the raw preview cache and the Human
  study caches kept whole responses past their 60-second reuse window. Both
  now expire, and labels are limited to what the cover draws.
- **Preview cache writes.** The IndexedDB cache measured every entry and
  wrote once per arrival. Sizes are remembered and writes coalesce.
- **Home belt slide.** The slide's focus variable invalidated style for each
  card's whole subtree on every frame. It is now a registered number
  (`--belt-focus`) whose dependants are bounded. The countdown bar no longer
  transitions between its 250 ms steps.
- **Idle work.** Ask's suggestion ticker measured layout every tick; it now
  uses an IntersectionObserver and skips unchanged marks. Sync status polls
  every 10 s instead of 3 s unless a run is active, the library is empty or
  Settings is open, and not at all while the page is hidden. The indexing
  watch stops while hidden. Cloud backup status polls every 60 s away from
  Settings.

## Not done

- **Parallelism** holds its whole snapshot. It needs server-side windowing or
  typed-array spans.
- **Very large conversations** in the reader keep about 8 KB of heap per
  message.
- **Query Table** `numberText` builds an `Intl.NumberFormat` per call. This
  belongs upstream.
- **Home** issues about 600 requests and parses about 114 MB of JSON in its
  first minutes (library prefetch and 20,000-message study batches).
- About 200 other `toLocaleString` call sites in 59 files are off the hot
  paths measured here.
- The header re-lays out about once every two seconds when idle.
- The frontend is one 3 MB script; Habits covers are about 250 KB each in the
  preview cache.
