// The structural sweep: every page the shell serves, at a desktop, a phone
// and a narrow phone width. Each must render its main content without page,
// console or request errors, keep everything inside the viewport, main and
// its cards, and keep the header in place while main scrolls.
//
// Adding a page is one line in ROUTES: a URL and a selector that appears once
// its main content has rendered. `open` runs before the checks, for surfaces
// reached by a click rather than a URL.
import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { limits, open } from "./harness.mjs";
import { layoutProblems, scrollProblems } from "./layout.mjs";
import { rootConversation, workID } from "./fixtures.mjs";

const work = `/work/${workID}`;
const ROUTES = [
  { name: "Home", url: "/", ready: ".home-belt-row .query-card" },
  { name: "Dashboard Catalog", url: "/home?cards=library", ready: ".card-library-entry .query-card" },
  { name: "Library table", url: "/library", ready: "#queryTableLibrary .qt-row" },
  { name: "Library conversations", url: "/library?view=conversation", ready: ".conversation-result-card" },
  { name: "TL1 overview", url: "/tl1", ready: ".tl1-tiles" },
  { name: "TL1 configurations", url: "/tl1/configurations", ready: "#tl1-matrix tbody tr" },
  { name: "TL1 follow-ups", url: "/tl1/followups", ready: "#tl1-followup-next tbody tr" },
  { name: "TL1 quality", url: "/tl1/quality", ready: "#tl1-errors" },
  { name: "TL1 runs", url: "/tl1/runs", ready: "#tl1Page .qt-row" },
  { name: "Usage tokens", url: "/usage?usage=tokens&machine_view=tokens", ready: "#queryTableUsage .usage-chart" },
  { name: "Usage machine time", url: "/usage?usage=tokens&machine_view=time", ready: ".machine-call-surface .qt-row" },
  { name: "Usage parallelism", url: "/usage?usage=tokens&machine_view=parallelism&machine_demo=1", ready: ".concurrency-chart svg" },
  { name: "Human Words", url: "/usage?usage=writing", ready: ".writing-panel .usage-chart" },
  { name: "Human Words messages", url: "/usage?usage=writing&writing=messages", ready: "#queryTableUsage .qt-row" },
  { name: "Carbon Impact", url: "/usage?usage=carbon", ready: "#carbonSummary" },
  { name: "Usage conversations", url: "/usage?usage=conversations", ready: "#queryTableUsage .qt-row" },
  { name: "Tool calls", url: "/tools", ready: "#toolsPage .qt-row" },
  { name: "Tool summary", url: "/tools?tools_view=summary", ready: "#toolsPage .qt-row" },
  { name: "Skills", url: "/tools?tools_view=skills", ready: ".skill-usages .qt-row" },
  { name: "Findings", url: "/findings", ready: ".findings-card" },
  { name: "Finding detail", url: "/findings?finding=drift%3Arepo%3Arepo_tl1%3Aghi", ready: ".findings-detail svg" },
  { name: "MCP", url: "/mcp", ready: ".mcp-history .qt-row" },
  { name: "Library health", url: "/settings", ready: "#healthCards .panel:not([aria-busy])" },
  { name: "Sources", url: "/settings/sources", ready: "#sourceGrid .source-card" },
  { name: "Capture & index", url: "/settings/sync", ready: "#syncDashboard .sync-panel table" },
  { name: "Optimization settings", url: "/settings/optimization", ready: "#optimizationSettingsPage .findings-panel" },
  { name: "App preferences", url: "/settings/preferences", ready: "#optionalBehaviors .optional-behavior" },
  { name: "Workspace overview", url: work, ready: ".delegation-table tbody tr" },
  { name: "Workspace parallel activity", url: `${work}?explorer=parallel`, ready: ".delegation-parallel" },
  { name: "Long transcript", url: `${work}/conversation/${rootConversation}`, ready: ".conversation-panel .turn" },
  { name: "Child transcript", url: `${work}/conversation/conv-nested`, ready: ".conversation-panel .turn" },
  { name: "Quick search", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.keyboard.press("ControlOrMeta+k"), shown: "#searchDialog[open]" },
  { name: "Usage summary", url: "/library", ready: "#queryTableLibrary .qt-row", open: page => page.locator("#usageSummaryToggle").click(), shown: "#usageSummary" },
  { name: "Bookmarks drawer", url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await page.locator("#navigatorToggle").click(); await page.locator("#bookmarksToggle").click(); }, shown: "#bookmarksDrawer.open" },
  { name: "History drawer", url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-show-nav-button": true }, open: async page => { await page.locator("#navigatorToggle").click(); await page.locator("#historyToggle").click(); }, shown: "#historyDrawer.open" },
];
const WIDTHS = [{ width: 1440, height: 900 }, { width: 390, height: 844 }, { width: 320, height: 640 }];

// Layout bugs found by this sweep and not fixed yet. A case whose only
// problems match here is marked todo with the bug, so the suite stays honest;
// any other problem still fails it.
const KNOWN = [
  { match: /content escapes span\.human-study-cover\.is-profile .*: span\.human-study-rows .* past the bottom/,
    bug: "The Human Words size-profile cover's six rows are taller than the cover and run about 4px past its bottom (web/src/human-study.css .human-study-rows)." },
  { match: /: span\.findings-in-cart "/,
    bug: "At 320px a finding's \"In prompt for …\" chip and its Remove from prompt button stay on one line and run past the card and main (web/src/findings.css .findings-in-cart does not wrap)." },
  { match: /: svg "0%[^"]*" (spans|past the right)/,
    bug: "At 320px the finding detail chart keeps a 320px minimum width (web/src/findings.tsx WeeklyChart, Math.max(320, …)) and runs past its panel and main." },
  { match: /a fixed surface leaves .*: section#usageSummary\.usage-summary /,
    bug: "At 320px the header's Usage summary popover is about 366px wide and leaves the viewport; its All time column is cut off (ui.py .usage-summary)." },
  { match: /content escapes details\.turn .*: text "\+\d+m\d+s" past the left/,
    bug: "At phone widths a transcript event offset longer than the gutter (\"+1m03s\") is cut off past the turn's left edge (ui.py .timeline-time)." },
  { match: /so it can scroll instead of main, reaching past it: (span\.qt-sr-only|span\.sr-only|caption\.sync-sr-only)( \(|$)/,
    bug: "Screen-reader-only labels (Query Table's .qt-sr-only and .sr-only, sync.js's .sync-sr-only) are absolutely positioned with no positioned ancestor inside main, so main does not clip them: the document grows taller than the window, and a scrollIntoView then scrolls the whole app, header included." },
  { match: /a control is cut off .*: button\.mcp-detail-button "[^"]*" in td\.qt-cell\.qt-trailing-cell /,
    bug: "The trailing action buttons in Query Table rows (Details, Tool call, Evidence, Messages on Tools, MCP, Machine time and Human Words messages) are wider than their column and are cut off (web/src/index.tsx trailing cells)." },
  { match: /a control is cut off .*: button#navBack\.header-icon in header /,
    bug: "At 320px with the navigation bar open, the header's rows overflow its clipped box and it scrolls sideways: the Back button, the brand and the Home tab are cut off (ui.py header .navigator)." },
  { match: /a control is cut off .*: button "High" in div\.carbon-segmented /,
    bug: "At 320px a Carbon Impact Low/Central/High switch is wider than its clipped box and High is cut off (carbon.js .carbon-segmented)." },
];

describe("structure", { concurrency: Number(process.env.PHAROS_SWEEP_CONCURRENCY || 3) }, () => {
  for (const route of ROUTES) {
    for (const viewport of WIDTHS) {
      const name = `${route.name} @ ${viewport.width}`;
      test(name, limits, async t => {
        const app = await open(t, { url: route.url, viewport, ready: route.ready, preferences: route.preferences });
        if (route.open) {
          await route.open(app.page);
          await app.page.locator(route.shown).first().waitFor();
          await app.settled();
        }
        await app.shot("top");
        const problems = await layoutProblems(app.page);
        if (!route.open) {
          problems.push(...await scrollProblems(app.page));
          await app.settled();
          await app.shot("scrolled");
          problems.push(...(await layoutProblems(app.page)).filter(problem => !problems.includes(problem)));
        }
        problems.push(...app.problems());
        const known = KNOWN.filter(bug => problems.some(problem => bug.match.test(problem)));
        if (problems.some(problem => !known.some(bug => bug.match.test(problem)))) assert.fail(`${name} (${route.url}):\n  ${problems.join("\n  ")}`);
        if (known.length) t.todo(known.map(bug => bug.bug).join("; "));
      });
    }
  }
});
