// node --test tests/delegation-ui.test.cjs
// Uses Pharos's real reader and checked-in React bundle with synthetic ledgers.
const { test, before, after } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { createRequire } = require("node:module");
const root = path.resolve(__dirname, "..");
const { chromium } = createRequire(
  path.join(root, ".context/browser-tests/package.json"),
)("playwright");
const source = fs.readFileSync(
  path.join(root, "internal/archive/assets/ui.py"),
  "utf8",
);
const html = source.slice(
  source.indexOf("r'''") + 4,
  source.lastIndexOf("'''"),
);
let browser;
before(async () => {
  browser = await chromium.launch({
    headless: true,
    executablePath:
      process.env.CHROME_PATH ||
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  });
});
after(async () => {
  await browser?.close();
});
const start = Date.parse("2026-09-24T12:00:00Z"),
  end = start + 300000;
const keys = [
  "start",
  "tokens",
  "cost",
  "calls",
  "errors",
  "files",
  "messages",
  "requests",
  "turns",
  "clock",
  "total",
  "toolTime",
  "toolAgent",
  "toolSum",
  "wait",
  "modelTime",
  "backgroundTime",
  "episodes",
  "children",
  "leaves",
  "depth",
  "input",
  "uncached",
  "cacheRead",
  "cacheWrite",
  "output",
  "reasoning",
  "remainder",
];
const specs = keys.map((id) => ({
  id,
  label:
    {
      start: "First evidence",
      tokens: "Tokens",
      cost: "API cost",
      calls: "Tool calls",
      errors: "Errors",
      files: "Files Edited",
      clock: "Clock span",
      total: "Session spans",
      toolTime: "Tool elapsed",
    }[id] || id,
  unit:
    id === "cost"
      ? "usd"
      : [
            "clock",
            "total",
            "toolTime",
            "toolAgent",
            "toolSum",
            "wait",
            "modelTime",
            "backgroundTime",
          ].includes(id)
        ? "ms"
        : id === "start"
          ? "date"
          : "count",
  definition: `Definition for ${id}`,
}));
function metric(value, quality = "observed") {
  return { value, quality, known: value === null ? 0 : 1, total: 1 };
}
function session(id, conv, parent, provider, outline, depth, label, tokens) {
  const self = Object.fromEntries(keys.map((k) => [k, metric(0)]));
  Object.assign(self, {
    tokens: metric(tokens),
    calls: metric(3),
    files: metric(depth === 0 ? 1 : depth === 1 ? 2 : 0),
    cost: metric(tokens / 1000),
    clock: metric(300000),
    start: metric(start),
    wait: metric(null, "unknown"),
    modelTime: metric(null, "unknown"),
    backgroundTime: metric(null, "unknown"),
  });
  return {
    id,
    workspace_id: "work",
    conversation_id: conv,
    parent_id: parent,
    provider,
    outline,
    depth,
    label,
    model: provider === "codex" ? "gpt-test" : "claude-test",
    effort: provider === "codex" ? "high" : "medium",
    coverage: "complete",
    usage_status: "reported-reconciled",
    order: depth,
    child_count: depth < 2 ? 1 : 0,
    start,
    end,
    subtree_start: start,
    subtree_end: end,
    self,
    all: { ...self },
  };
}
const nodes = [
  session("s-root", "root", "", "claude", "1", 0, "Build the feature", 1000),
  session(
    "s-child",
    "child",
    "s-root",
    "codex",
    "1.1",
    1,
    "Review implementation",
    600,
  ),
  session(
    "s-nested",
    "nested",
    "s-child",
    "claude",
    "1.1.1",
    2,
    "Review timing",
    300,
  ),
];
nodes[1].message_id = "launch-root";
nodes[2].message_id = "launch-child";
nodes[0].all = { ...nodes[0].self, tokens: metric(1900) };
nodes[1].all = { ...nodes[1].self, tokens: metric(900) };
const messages = (conv) => [
  {
    id: `prompt-${conv}`,
    native_id: `prompt-${conv}`,
    kind: "message",
    role: "user",
    text: `Do work in ${conv}`,
    created_at: new Date(start).toISOString(),
  },
  ...(conv === "nested"
    ? []
    : [
        {
          id: `launch-${conv}`,
          native_id: `launch-${conv}`,
          kind: "delegation",
          role: "assistant",
          text: JSON.stringify({
            tool: "spawn_agent",
            input: { description: "Review implementation" },
          }),
          call_id: `call-${conv}`,
          created_at: new Date(start + 2000).toISOString(),
        },
      ]),
  {
    id: `reply-${conv}`,
    native_id: `reply-${conv}`,
    kind: "message",
    role: "assistant",
    text: "Reviewed the change.",
    created_at: new Date(end).toISOString(),
  },
];
const work = {
  id: "work",
  title: "Delegated feature",
  conversations: nodes.map((n) => ({
    id: n.conversation_id,
    provider: n.provider,
    native_id: n.conversation_id,
    started_at: new Date(start).toISOString(),
    first_prompt: n.label,
    message_count: 3,
  })),
  changes: [],
  metrics: [
    { name: "input_tokens", value: 1900, unit: "tokens", status: "observed" },
  ],
  prs: [],
  identity_links: [],
};
async function fixture(t, options = {}) {
  const page = await browser.newPage({
    viewport: options.viewport || { width: 1400, height: 1000 },
    timezoneId: "America/Denver",
    locale: "en-US",
    reducedMotion: "reduce",
  });
  t.after(() => page.close());
  const errors = [];
  const transcriptRequests = [];
  const fixtureNodes = options.nodes || nodes;
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("http://delegation.test/**", (route) => {
    const url = new URL(route.request().url());
    const pathname = url.pathname;
    if (pathname.startsWith("/work/") || pathname === "/")
      return route.fulfill({ body: html, contentType: "text/html" });
    if (pathname.startsWith("/assets/")) {
      const file = path.join(root, "internal/archive", pathname.slice(1));
      if (fs.existsSync(file))
        return route.fulfill({
          body: fs.readFileSync(file),
          contentType: pathname.endsWith(".css")
            ? "text/css"
            : "text/javascript",
        });
      return route.fulfill({ body: "" });
    }
    let body = {
      items: [],
      rows: [],
      total: 0,
      metrics: [],
      status: { running: false },
      backfill: { running: false, done: 0, total: 0, error: null },
      pending_conversations: 0,
      conversations: 0,
      summary: { open: 0, saved: {} },
      settings: {},
      cart: [],
      findings: [],
      targets: [],
      repositories: [],
      wins: [],
      checkpoints: [],
    };
    if (pathname === "/api/work/work") body = work;
    if (pathname.startsWith("/api/work/work/conversations/")) {
      const conv = pathname.split("/").at(-1);
      transcriptRequests.push(conv);
      body = {
        ...work.conversations.find((c) => c.id === conv),
        messages: options.messages ? options.messages(conv) : messages(conv),
      };
    }
    if (pathname === "/api/work/work/delegation") {
      assert.equal(url.searchParams.get("limit"), "40");
      const conv = url.searchParams.get("conversation");
      const query = url.searchParams.get("search") || "";
      let rows = fixtureNodes.filter(
        (n) =>
          !query ||
          n.label.toLowerCase().includes(query.toLowerCase()) ||
          n.outline === "1",
      );
      if (url.searchParams.get("mode") === "rank") {
        const family = url.searchParams.get("scope") === "all" ? "all" : "self";
        const key = url.searchParams.get("sort") || "tokens";
        const sign = url.searchParams.get("direction") === "asc" ? 1 : -1;
        rows.sort(
          (a, b) => sign * (a[family][key].value - b[family][key].value),
        );
      } else {
        const collapsed = new Set(
          (url.searchParams.get("collapsed") || "").split(","),
        );
        rows = rows.filter((n) => {
          let parent = fixtureNodes.find((p) => p.id === n.parent_id);
          while (parent) {
            if (collapsed.has(parent.id)) return false;
            parent = fixtureNodes.find((p) => p.id === parent.parent_id);
          }
          return true;
        });
      }
      body = {
        snapshot: "fixture-v1",
        rows,
        ancestors: [],
        focus: fixtureNodes,
        matched: Object.fromEntries(rows.map((n) => [n.id, true])),
        total: rows.length,
        session_count: fixtureNodes.length,
        offset: 0,
        next_cursor: "",
        metrics: specs,
        maxima: {
          self: Object.fromEntries(
            keys.map((k) => [k, k === "tokens" ? 1000 : 300000]),
          ),
          all: Object.fromEntries(
            keys.map((k) => [k, k === "tokens" ? 1900 : 300000]),
          ),
        },
        totals: {
          ...nodes[0].all,
          clock: metric(300000),
          total: metric(900000),
        },
        start,
        end,
        density: Array.from({ length: 120 }, (_, i) => i % 13),
        warnings: [],
      };
    }
    if (pathname === "/api/work/work/parallel") {
      const from = Number(url.searchParams.get("start")),
        to = Number(url.searchParams.get("end"));
      body = {
        snapshot: "fixture-v1",
        start: from,
        end: to,
        inference: "Inferred, not measured",
        lanes: (url.searchParams.get("sessions") || "")
          .split(",")
          .filter(Boolean)
          .map((id) => ({
            session: id,
            model_inferred: [
              {
                start: Math.max(from, start + 1000),
                end: Math.min(to, end - 1000),
              },
            ],
            episodes: [{ start, end }],
            truncated: false,
            marks: [
              {
                start: start + 1000,
                end: start + 100000,
                end_known: true,
                category: "read",
                name: "Read",
                count: 1,
                tool_id: "tool-read",
                message_id: `prompt-${id === "s-root" ? "root" : "child"}`,
              },
              {
                start: start + 110000,
                end: start + 125000,
                end_known: true,
                category: "run",
                name: "Test",
                count: 1,
                error: true,
                tool_id: "tool-error",
                message_id: "reply-child",
              },
            ].filter((m) => m.start <= to && m.end >= from),
          })),
      };
    }
    return route.fulfill({
      body: JSON.stringify(body),
      contentType: "application/json",
    });
  });
  await page.goto("http://delegation.test" + (options.url || "/work/work?explorer=table"));
  if (options.url?.includes("conversation")) await page.locator(".delegation-session-heading").waitFor();
  else await page.locator(".delegation-table tbody tr").first().waitFor();
  return { page, errors, transcriptRequests };
}
test("Pharos table keeps hierarchy, absolute values, complete metric families and global tint", async (t) => {
  const { page, errors } = await fixture(t);
  const panel = page.locator(".delegation-panel");
  assert.equal(await panel.locator("tbody tr").count(), 3);
  assert.equal(await panel.locator(".delegation-pagination").count(), 0);
  assert.equal(await panel.getByRole("button", { name: "Next", exact: true }).count(), 0);
  assert.equal(await panel.getByRole("button", { name: "Previous", exact: true }).count(), 0);
  assert.equal(await panel.getByRole("button", { name: "Files Edited", exact: true }).count(), 2);
  assert.equal(await panel.locator(".delegation-rails").count(), 0);
  assert.equal(
    await panel.locator("tbody tr").first().locator("td").nth(1).innerText(),
    "1K",
  );
  assert.match(
    await panel
      .locator("tbody tr")
      .first()
      .locator("td")
      .nth(1)
      .getAttribute("title"),
    /1,000/,
  );
  assert.deepEqual(
    await panel.locator(".delegation-family").allTextContents(),
    ["Self", "Self + children"],
  );
  assert.ok(
    (await panel.locator("td").allTextContents()).every(
      (s) => !s.includes("%"),
    ),
  );
  assert.equal(
    await panel.locator("tbody tr").nth(1).locator("td").nth(1).innerText(),
    "600",
  );
  assert.match(
    await panel
      .locator("tbody tr")
      .nth(1)
      .locator("td")
      .nth(1)
      .getAttribute("style"),
    /color-mix/,
  );
  await panel.getByLabel("Cell tint").selectOption("off");
  assert.equal(
    await panel
      .locator("tbody td")
      .evaluateAll(
        (cells) => cells.filter((cell) => cell.style.background).length,
      ),
    0,
  );
  await panel
    .locator("thead tr")
    .nth(1)
    .locator("th")
    .first()
    .getByRole("button")
    .click();
  await panel.getByLabel("Cell tint").selectOption("sort");
  assert.equal(
    await panel
      .locator("tbody td")
      .evaluateAll(
        (cells) => cells.filter((cell) => cell.style.background).length,
      ),
    3,
  );
  await panel.locator(".delegation-columns summary").click();
  for (const checkbox of await panel.locator(".delegation-columns input").all())
    await checkbox.check();
  assert.equal(
    await panel.locator("tbody tr").first().locator("td").count(),
    57,
  );
  assert.deepEqual(errors, []);
});
test("workspace header owns totals and the explorer keeps only direct controls", async (t) => {
  const { page, errors } = await fixture(t);
  const panel = page.locator(".delegation-panel"),
    header = page.locator(".work-heading");
  assert.equal(await panel.locator(".delegation-totals").count(), 0);
  assert.equal(await panel.getByRole("button", { name: "Expand branches", exact: true }).count(), 0);
  assert.equal(await panel.getByRole("button", { name: "Collapse branches", exact: true }).count(), 0);
  const toolbar = panel.locator(".delegation-toolbar");
  assert.equal(await toolbar.getByRole("tab").count(), 2);
  assert.equal(await toolbar.getByLabel("Cell tint").locator("option:checked").innerText(), "Tint · All columns");
  const tabs = toolbar.getByRole("tablist");
  await toolbar.getByRole("tab", { name: "Hierarchy & usage", exact: true }).focus();
  await page.keyboard.press("ArrowRight");
  assert.equal(await tabs.getAttribute("data-view"), "parallel");
  assert.equal(await toolbar.getByRole("tab", { name: "Parallel activity", exact: true }).getAttribute("aria-selected"), "true");
  await page.keyboard.press("Home");
  assert.equal(await tabs.getAttribute("data-view"), "table");
  assert.equal(await header.locator(".delegation-totals > span").count(), 5);
  assert.match(
    await header.locator(".delegation-totals").innerText(),
    /Tokens[\s\S]*1.9K[\s\S]*API cost[\s\S]*Tool calls[\s\S]*Clock span[\s\S]*Session spans/,
  );
  for (const name of [
    "Find sessions",
    "Order",
    "Metric",
    "Scope",
    "Direction",
    "Count only matching sessions",
  ])
    assert.equal(await panel.getByLabel(name, { exact: true }).count(), 0);
  assert.doesNotMatch(
    await panel.innerText(),
    /Filters change rows|Tint uses fixed workspace column maxima/,
  );
  assert.equal(
    await header
      .locator(".work-disclosure > summary")
      .getByText("Usage", { exact: true })
      .count(),
    0,
  );
  assert.equal(
    await header.getByRole("button", { name: "Share", exact: true }).count(),
    1,
  );
  assert.match(
    await header
      .getByRole("button", { name: "Share", exact: true })
      .getAttribute("title"),
    /^Share/,
  );
  const metadata = panel.locator(
    'tr[data-session="s-child"] .delegation-session-meta',
  );
  assert.equal(await metadata.innerText(), "codex · gpt-test · high");
  assert.equal(await metadata.locator("span").count(), 0);
  assert.deepEqual(
    await panel
      .locator(".delegation-node")
      .evaluateAll((nodes) =>
        nodes.map((node) =>
          Number.parseFloat(getComputedStyle(node).paddingLeft),
        ),
      ),
    [4, 12, 20],
  );
  const heading = await header.locator("h1").boundingBox(),
    totals = await header.locator(".delegation-totals").boundingBox();
  assert.ok(totals.x > heading.x + heading.width);
  const toggle = panel.getByRole("button", { name: "Workspace Sessions", exact: true });
  assert.equal(await panel.locator(":scope > summary").count(), 0);
  assert.equal(await toggle.getAttribute("aria-expanded"), "true");
  await toggle.click();
  assert.equal(await toggle.getAttribute("aria-expanded"), "false");
  assert.equal(await panel.locator(".delegation-table").isVisible(), false);
  assert.equal(await toolbar.getByRole("tablist").isVisible(), true);
  assert.equal(await header.locator(".delegation-totals").isVisible(), true);
  assert.equal(new URL(page.url()).searchParams.has("explorer"), false);
  await toggle.click();
  assert.equal(await panel.locator(".delegation-table").isVisible(), true);
  await toolbar.getByRole("tab", { name: "Parallel activity", exact: true }).click();
  await page.locator(".delegation-mark").first().waitFor();
  await toggle.click();
  assert.equal(await panel.locator(".delegation-parallel").isVisible(), false);
  await toggle.click();
  assert.equal(await panel.locator(".delegation-parallel").isVisible(), true);
  // A view choice also opens the hidden workspace sessions.
  await toggle.click();
  await toolbar.getByRole("tab", { name: "Hierarchy & usage", exact: true }).click();
  assert.equal(await toggle.getAttribute("aria-expanded"), "true");
  assert.equal(await panel.locator(".delegation-table").isVisible(), true);
  await page.goto(
    "http://delegation.test/work/work?explorer=table&delegation_search=missing&delegation_matching=1",
  );
  await page.locator(".delegation-table tbody tr").first().waitFor();
  assert.equal(await page.locator(".delegation-table tbody tr").count(), 3);
  assert.equal(
    new URL(page.url()).searchParams.has("delegation_search"),
    false,
  );
  assert.equal(
    new URL(page.url()).searchParams.has("delegation_matching"),
    false,
  );
  await page.setViewportSize({ width: 390, height: 850 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  assert.equal(await header.locator(".delegation-totals").isVisible(), true);
  assert.deepEqual(errors, []);
});
test("column handles reorder both families, preserve hidden positions, and survive reload", async (t) => {
  const { page, errors } = await fixture(t);
  const panel = page.locator(".delegation-panel");
  const labels = () =>
    panel.locator("thead tr").nth(1).locator("th").allTextContents();
  await panel.locator(".delegation-columns summary").click();
  await panel
    .getByRole("button", { name: "Move API cost", exact: true })
    .dragTo(panel.locator('[data-column="tokens"]'), {
      targetPosition: { x: 80, y: 2 },
    });
  assert.deepEqual((await labels()).slice(0, 3), [
    "API cost",
    "Tokens",
    "Tool calls",
  ]);
  assert.deepEqual((await labels()).slice(7, 10), [
    "API cost",
    "Tokens",
    "Tool calls",
  ]);
  await panel
    .getByRole("button", { name: "Move Tool calls", exact: true })
    .focus();
  await page.keyboard.press("Home");
  assert.deepEqual((await labels()).slice(0, 3), [
    "Tool calls",
    "API cost",
    "Tokens",
  ]);
  await panel.locator('[data-column="cost"] input').uncheck();
  assert.deepEqual((await labels()).slice(0, 2), ["Tool calls", "Tokens"]);
  await panel.locator('[data-column="cost"] input').check();
  assert.deepEqual((await labels()).slice(0, 3), [
    "Tool calls",
    "API cost",
    "Tokens",
  ]);
  await page.reload();
  await page.locator(".delegation-table tbody tr").first().waitFor();
  assert.deepEqual((await labels()).slice(0, 3), [
    "Tool calls",
    "API cost",
    "Tokens",
  ]);
  await panel.locator(".delegation-columns summary").click();
  assert.deepEqual(
    await panel
      .locator(".delegation-column-option")
      .evaluateAll((items) =>
        items.slice(0, 3).map((item) => item.dataset.column),
      ),
    ["calls", "cost", "tokens"],
  );
  await page.setViewportSize({ width: 390, height: 850 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  assert.deepEqual(errors, []);
});

test("hierarchy frames span every cell and header sorting cycles back to hierarchy", async (t) => {
  const { page, errors } = await fixture(t);
  const panel = page.locator(".delegation-panel");
  for (const name of [
    "Find sessions",
    "Order",
    "Metric",
    "Scope",
    "Direction",
    "Count only matching sessions",
  ])
    assert.equal(await panel.getByLabel(name, { exact: true }).count(), 0);
  assert.deepEqual(await panel.locator(".delegation-level").allTextContents(), [
    "L1",
    "L2",
    "L3",
  ]);
  const root = panel.locator('tr[data-session="s-root"]');
  const child = panel.locator('tr[data-session="s-child"]');
  const leaf = panel.locator('tr[data-session="s-nested"]');
  for (const row of [root, child]) {
    assert.equal(
      await row.locator("td").count(),
      await row.locator(".delegation-frame-line.start").count(),
    );
    const divider = row.locator(".delegation-label-cell .parent-divider");
    assert.equal(
      await divider.evaluate((el) => el.getBoundingClientRect().height),
      1,
    );
    assert.equal(
      await divider.evaluate((el) => getComputedStyle(el).backgroundColor),
      await row
        .locator(".delegation-label-cell .start")
        .evaluate((el) => getComputedStyle(el).backgroundColor),
    );
  }
  for (const edge of ["left", "right"]) {
    const sides = leaf.locator(`.delegation-frame-side.${edge}`);
    assert.equal(await sides.count(), 2);
    const coordinates = await sides.evaluateAll((els) =>
      els.map((el) => el.getBoundingClientRect().x),
    );
    assert.equal(Math.abs(coordinates[0] - coordinates[1]), 8);
    assert.equal(
      await leaf
        .locator(`td:${edge === "left" ? "first" : "last"}-child .end`)
        .count(),
      2,
    );
  }
  const header = panel.locator("thead tr").nth(1).locator("th").first();
  await header.getByRole("button").click();
  assert.equal(await header.getAttribute("aria-sort"), "descending");
  await header.getByRole("button").click();
  assert.equal(await header.getAttribute("aria-sort"), "ascending");
  await page.waitForFunction(
    () =>
      document.querySelector(".delegation-table tbody tr").dataset.session ===
      "s-nested",
  );
  await header.getByRole("button").click();
  assert.equal(
    new URL(page.url()).searchParams.get("delegation_mode"),
    "hierarchy",
  );
  assert.equal(await header.getAttribute("aria-sort"), "none");
  await page.waitForFunction(
    () =>
      document.querySelector(".delegation-table tbody tr").dataset.session ===
      "s-root",
  );
  assert.equal(new URL(page.url()).searchParams.has("delegation_sort"), false);
  await header.getByRole("button").click();
  await panel
    .getByRole("button", { name: "Execution session", exact: true })
    .click();
  assert.equal(
    new URL(page.url()).searchParams.get("delegation_mode"),
    "hierarchy",
  );
  await panel
    .getByRole("button", { name: "Collapse Build the feature", exact: true })
    .click();
  await page.waitForFunction(
    () => document.querySelectorAll(".delegation-table tbody tr").length === 1,
  );
  assert.equal(
    await root.locator("td").count(),
    await root.locator(".delegation-frame-line.end").count(),
  );
  assert.equal(await root.locator(".delegation-frame-side").count(), 2);
  assert.deepEqual(errors, []);
});

test("overview table fills the page and session identity stays in the borderless reader header", async (t) => {
  const { page, errors } = await fixture(t);
  const table = page.locator(".delegation-table-size");
  const width = (await table.boundingBox()).width;
  assert.equal(await page.locator(".delegation-resize-footer").count(), 0);
  assert.equal(await page.getByRole("button", { name: "Resize execution session table", exact: true }).count(), 0);
  assert.equal(await page.locator(".delegation-table-scroll").evaluate(el => getComputedStyle(el).maxHeight), "none");
  await page.locator('tr[data-session="s-child"] td').nth(1).click();
  await page.waitForFunction(() => document.querySelector(".delegation-session-heading")?.dataset.session === "s-child");
  await page.locator('.delegation-inbox-row.current[data-session="s-child"]').waitFor();
  assert.equal(new URL(page.url()).pathname, "/work/work/conversation/child");
  assert.equal(await page.locator(".conversation-selector").count(), 0);
  assert.match(await page.locator(".delegation-session-heading").innerText(), /Session 1.1[\s\S]*codex · gpt-test · high/);
  const bar = page.locator(".workspace-reader-bar");
  assert.equal(await bar.locator(".delegation-session-heading").count(), 1);
  assert.equal(await page.locator(".conversation-panel .delegation-session-heading").count(), 0);
  assert.equal(await page.locator(".conversation-panel").evaluate(el => getComputedStyle(el).borderTopWidth), "0px");
  assert.equal(await page.locator(".conversation-panel").evaluate(el => getComputedStyle(el).borderRadius), "0px");
  const reader = await page.locator(".workspace-reader").boundingBox();
  const header = await bar.boundingBox();
  assert.equal(header.x, reader.x - 8);
  await page.locator(".workspace-reader-content").evaluate(el => el.scrollTop = 300);
  assert.equal((await bar.boundingBox()).y, header.y);
  const divider = page.getByRole("separator", { name: "Resize workspace sessions sidebar", exact: true });
  const edge = await divider.boundingBox();
  await page.mouse.move(edge.x + edge.width / 2, edge.y + edge.height / 2);
  await page.mouse.down();
  await page.mouse.move(edge.x + edge.width / 2 + 80, edge.y + edge.height / 2);
  await page.mouse.up();
  const sidebarWidth = Number(await divider.getAttribute("aria-valuenow"));
  assert.ok(sidebarWidth >= 475);
  await page.reload();
  await page.locator(".delegation-session-heading").waitFor();
  assert.equal(Number(await divider.getAttribute("aria-valuenow")), sidebarWidth);
  await page.locator(".workspace-nav-bar").getByRole("button", { name: "Open workspace overview", exact: true }).click();
  await page.locator(".delegation-table tbody tr").first().waitFor();
  assert.equal((await table.boundingBox()).width, width);
  assert.equal(new URL(page.url()).pathname, "/work/work");
  assert.equal(await page.locator(".workspace-reader").isVisible(), false);
  assert.deepEqual(errors, []);
});

test("full timeline overview stays fixed through zoom, pan, range selection and reload", async (t) => {
  const { page, errors } = await fixture(t);
  await page
    .getByRole("tab", { name: "Parallel activity", exact: true })
    .click();
  await page.locator(".delegation-mark").first().waitFor();
  const overview = await page
      .locator(".delegation-overview-bounds")
      .innerText(),
    bars = await page
      .locator(".delegation-histogram i")
      .evaluateAll((items) => items.map((el) => el.style.height));
  assert.equal(
    await page
      .locator(".delegation-lane")
      .first()
      .evaluate((el) => el.getBoundingClientRect().height),
    45,
  );
  for (const name of ["Zoom in", "Zoom out", "Pan earlier", "Pan later", "Fit selected session"]) {
    assert.equal(await page.getByRole("button", { name, exact: true }).count(), 0);
  }
  assert.equal(await page.locator('input[type="datetime-local"]').count(), 0);
  assert.match(overview, /Sep 24, 2026[\s\S]*6:00:00 AM MDT/);
  assert.doesNotMatch(overview, /T12:00|Z/);
  const axis = await page.locator(".delegation-axis > div").boundingBox();
  await page.mouse.move(axis.x + axis.width * 0.25, axis.y + 20);
  await page.mouse.down();
  await page.mouse.move(axis.x + axis.width * 0.75, axis.y + 20, { steps: 8 });
  assert.ok(await page.locator(".delegation-brush").count() >= 4);
  assert.ok(Math.abs(Number.parseFloat(await page.locator(".delegation-viewport").evaluate(el => el.style.width)) - 50) < 0.2);
  await page.mouse.up();
  await page.waitForFunction(
    () =>
      !document
        .querySelector(".delegation-window")
        .textContent.includes("Loading"),
  );
  assert.equal(
    await page.locator(".delegation-overview-bounds").innerText(),
    overview,
  );
  assert.deepEqual(
    await page
      .locator(".delegation-histogram i")
      .evaluateAll((items) => items.map((el) => el.style.height)),
    bars,
  );
  assert.ok(Math.abs(Number.parseFloat(await page.locator(".delegation-viewport").evaluate(el => el.style.width)) - 50) < 0.2);
  await page.locator(".delegation-viewport").focus();
  await page.keyboard.press("ArrowLeft");
  await page.waitForTimeout(100);
  assert.ok(Math.abs(Number(new URL(page.url()).searchParams.get("delegation_start")) - start - 60000) < 300);
  await page.reload();
  await page.locator(".delegation-mark").first().waitFor();
  assert.ok(Math.abs(Number.parseFloat(await page.locator(".delegation-viewport").evaluate(el => el.style.left)) - 20) < 0.2);
  const beforePan = new URL(page.url()).searchParams;
  const span = Number(beforePan.get("delegation_end")) - Number(beforePan.get("delegation_start"));
  const windowBox = await page.locator(".delegation-viewport").boundingBox();
  const overviewBox = await page.locator(".delegation-overview").boundingBox();
  await page.mouse.move(windowBox.x + windowBox.width / 2, windowBox.y + 20);
  await page.mouse.down();
  await page.mouse.move(windowBox.x + windowBox.width / 2 + overviewBox.width * 0.1, windowBox.y + 20, { steps: 8 });
  await page.mouse.up();
  await page.waitForFunction(() => Number(new URL(location.href).searchParams.get("delegation_start")) > Date.parse("2026-09-24T12:01:20Z"));
  const afterPan = new URL(page.url()).searchParams;
  assert.ok(Math.abs(Number(afterPan.get("delegation_end")) - Number(afterPan.get("delegation_start")) - span) < 1);
  // Panning at the edge keeps the same duration and stops at the workspace bound.
  await page.locator(".delegation-viewport").focus();
  await page.keyboard.press("Home");
  await page.waitForTimeout(100);
  assert.equal(Number(new URL(page.url()).searchParams.get("delegation_start")), start);
  await page.keyboard.press("End");
  await page.waitForTimeout(100);
  assert.equal(Number(new URL(page.url()).searchParams.get("delegation_end")), end);
  // Resize with an edge handle, rather than the removed date inputs.
  const handle = await page.getByRole("slider", { name: "Timeline window start", exact: true }).boundingBox();
  await page.mouse.move(handle.x + handle.width / 2, handle.y + 20);
  await page.mouse.down();
  await page.mouse.move(handle.x + handle.width / 2 + overviewBox.width * 0.25, handle.y + 20, { steps: 8 });
  await page.mouse.up();
  await page.waitForFunction(() => Number(new URL(location.href).searchParams.get("delegation_start")) > Date.parse("2026-09-24T12:03:30Z"));
  assert.ok(Math.abs(Number.parseFloat(await page.locator(".delegation-viewport").evaluate(el => el.style.width)) - 25) < 0.3);
  // Escape cancels a brush without changing the visible range.
  const beforeCancel = page.url();
  const cancelAxis = await page.locator(".delegation-axis > div").boundingBox();
  await page.mouse.move(cancelAxis.x + cancelAxis.width * 0.2, cancelAxis.y + 20);
  await page.mouse.down();
  await page.mouse.move(cancelAxis.x + cancelAxis.width * 0.5, cancelAxis.y + 20, { steps: 8 });
  await page.keyboard.press("Escape");
  await page.mouse.up();
  assert.equal(page.url(), beforeCancel);
  assert.equal(await page.locator(".delegation-brush").count(), 0);
  await page.getByRole("slider", { name: "Timeline window start", exact: true }).focus();
  await page.keyboard.press("Home");
  await page.waitForTimeout(100);
  const main = await page.locator(".delegation-lane-track").first().boundingBox();
  await page.mouse.move(main.x + main.width * 0.25, main.y + 38);
  await page.mouse.down();
  await page.mouse.move(main.x + main.width * 0.75, main.y + 38, { steps: 8 });
  await page.mouse.up();
  await page.waitForTimeout(100);
  const box = await page.locator(".delegation-overview").boundingBox();
  await page.mouse.move(box.x + box.width * 0.8, box.y + 15);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.95, box.y + 15);
  await page.mouse.up();
  await page.waitForTimeout(150);
  assert.ok(
    Number(new URL(page.url()).searchParams.get("delegation_start")) >=
      start + 230000,
  );
  await page
    .locator(".delegation-overview")
    .dblclick({ position: { x: box.width * 0.98, y: 20 } });
  await page.waitForTimeout(150);
  assert.equal(
    await page.locator(".delegation-viewport").evaluate((el) => el.style.width),
    "100%",
  );
  // Starting a range drag on a tool mark must zoom, without opening the call.
  const tool = await page.locator(".delegation-mark.read").first().boundingBox();
  const toolTrack = await page.locator(".delegation-lane-track").first().boundingBox();
  await page.mouse.move(tool.x + tool.width / 2, tool.y + 4);
  await page.mouse.down();
  await page.mouse.move(toolTrack.x + toolTrack.width * 0.6, tool.y + 4, { steps: 8 });
  await page.mouse.up();
  await page.waitForFunction(() => Number.parseFloat(document.querySelector(".delegation-viewport").style.width) < 100);
  assert.equal(await page.locator(".delegation-event-detail").count(), 0);
  assert.ok(
    (
      await page
        .locator(".delegation-model-bg,.delegation-mark")
        .evaluateAll((els) =>
          els.map((el) => getComputedStyle(el).backgroundImage),
        )
    ).every((s) => s === "none"),
  );
  assert.deepEqual(errors, []);
});
test("parallel row navigation, activity expansion and tool-call popovers are independent", async (t) => {
  const { page, errors } = await fixture(t);
  await page.getByRole("tab", { name: "Parallel activity", exact: true }).click();
  const rootLane = page.locator('.delegation-lane[data-session="s-root"]');
  const childLane = page.locator('.delegation-lane[data-session="s-child"]');
  await childLane.locator(".delegation-mark").first().waitFor();
  assert.equal(await page.locator(".delegation-lane.detailed").count(), 0);
  await childLane.locator(".delegation-name").click();
  await page.waitForFunction(() => document.querySelector(".delegation-session-heading")?.dataset.session === "s-child");
  assert.equal(new URL(page.url()).pathname, "/work/work/conversation/child");
  await page.waitForFunction(() => document.activeElement === document.querySelector(".conversation-panel"));
  assert.equal(await page.locator(".delegation-lane.detailed").count(), 0);
  assert.equal(await childLane.locator(".delegation-activity-toggle").getAttribute("aria-expanded"), "false");
  await page.locator(".workspace-reader-bar").getByRole("button", { name: "Hide workspace sessions", exact: true }).click();
  await page.getByRole("button", { name: "Show workspace sessions", exact: true }).click();
  await page.getByRole("tab", { name: "Parallel activity", exact: true }).click();
  await childLane.locator(".delegation-mark").first().waitFor();
  const beforeExpansion = page.url();
  await childLane.getByRole("button", { name: "Expand activity for Review implementation", exact: true }).click();
  assert.equal(page.url(), beforeExpansion);
  assert.equal(await childLane.locator(".delegation-category-label").count(), 9);
  const geometries = await childLane.evaluate(lane => [...lane.querySelectorAll(".delegation-mark")].map(mark => {
    const category = [...lane.querySelectorAll(".delegation-category-label")].find(label => label.textContent === (mark.classList.contains("read") ? "Read / search" : "Run / build / test"));
    return { labelBottom: category.getBoundingClientRect().bottom, markTop: mark.getBoundingClientRect().top };
  }));
  assert.ok(geometries.every(({ labelBottom, markTop }) => labelBottom < markTop));
  const snippetStyles = await childLane.locator(".delegation-mark").evaluateAll(marks => marks.map(mark => {
    const css = getComputedStyle(mark);
    return { border: css.borderTopWidth, shadow: css.boxShadow };
  }));
  assert.ok(snippetStyles.every(style => style.border === "0px" && style.shadow === "none"));
  const snippet = childLane.locator(".delegation-mark.read");
  await snippet.click();
  const menu = page.getByRole("dialog", { name: "Activity: Read", exact: true });
  await menu.waitFor();
  assert.equal(page.url(), beforeExpansion);
  const jump = menu.getByRole("button", { name: "Jump to tool call ↗", exact: true });
  assert.equal(await jump.evaluate(el => document.activeElement === el), true);
  const menuBox = await menu.boundingBox(), snippetBox = await snippet.boundingBox();
  assert.ok(menuBox.x >= 0 && menuBox.x + menuBox.width <= 1400 && menuBox.y >= 0 && menuBox.y + menuBox.height <= 1000);
  await page.keyboard.press("Escape");
  assert.equal(await menu.count(), 0);
  assert.equal(await snippet.evaluate(el => document.activeElement === el), true);
  await childLane.getByRole("button", { name: "Collapse activity for Review implementation", exact: true }).click();
  assert.equal(await page.locator(".delegation-lane.detailed").count(), 0);
  // A simple click on the empty part of a lane opens its conversation too.
  const track = await rootLane.locator(".delegation-lane-track").boundingBox();
  await page.mouse.click(track.x + track.width * .8, track.y + 38);
  await page.waitForFunction(() => document.querySelector(".delegation-session-heading")?.dataset.session === "s-root");
  assert.deepEqual(errors, []);
});

test("child transcripts and their parents use the native conversation reader and launch links", async (t) => {
  const { page, errors } = await fixture(t);
  await page.locator('tr[data-session="s-root"] .delegation-name').click();
  await page.locator(".delegation-context").waitFor();
  await page
    .getByRole("button", { name: "Open child", exact: false })
    .first()
    .click();
  await page.waitForFunction(
    () => new URL(location.href).searchParams.get("conversation") === "child",
  );
  await waitForReader(page, "s-child");
  const context = page.locator(".delegation-context");
  assert.equal(await context.getByRole("button", { name: /Parent|Parallel activity/ }).count(), 0);
  assert.match(await context.innerText(), /self tokens/);
  await openInboxSession(page, "s-root");
  await page
    .getByRole("tab", { name: "Parallel activity", exact: true })
    .click();
  await page.locator(".delegation-mark.failed").first().waitFor();
  await page.locator(".delegation-mark.failed").nth(1).click();
  assert.match(
    await page.locator(".delegation-event-detail").innerText(),
    /Reported error/,
  );
  await page
    .getByRole("button", { name: "Jump to tool call ↗", exact: true })
    .click();
  await page.waitForFunction(
    () => new URL(location.href).searchParams.get("conversation") === "child",
  );
  await page.setViewportSize({ width: 390, height: 850 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  assert.deepEqual(errors, []);
});

test("cross-workspace children keep nesting and open the actual child transcript", async (t) => {
  const child = {
    ...nodes[1],
    conversation_id: "outside-child",
    workspace_id: "outside-work",
    external_child: { conversation_id: "outside-child", workspace_id: "outside-work", provider: "codex", label: "Review implementation" },
    self: Object.fromEntries(keys.map(k => [k, metric(null, "unknown")])),
  };
  const nested = { ...nodes[2], conversation_id: "outside-nested", external_child: { conversation_id: "outside-nested", workspace_id: "nested-work", provider: "claude", label: "Review timing" } };
  const { page, errors } = await fixture(t, { nodes: [nodes[0], child, nested] });
  assert.equal(await page.locator('tr[data-session="s-nested"]').getAttribute("data-session"), "s-nested");
  await page.locator('tr[data-session="s-root"] .delegation-name').click();
  await waitForReader(page, "s-root");
  await page.evaluate(() => { window.pharosNavigate = target => { window.__externalTarget = target; }; });
  await page.locator('.delegation-transcript-link').click();
  assert.equal(await page.evaluate(() => window.__externalTarget), "/work/outside-work?conversation=outside-child");
  await page.locator(".delegation-child-menu").evaluate(el => { el.closest("details").open = true; });
  assert.equal(await page.locator(".delegation-child-menu button").count(), 1);
  assert.equal(await page.locator(".delegation-context").getByRole("button", { name: /Open child/ }).count(), 0);
  assert.deepEqual(errors, []);
});

test("an unresolved launch does not advertise a child transcript", async (t) => {
  const placeholder = { ...nodes[1], conversation_id: "root", self: Object.fromEntries(keys.map(k => [k, metric(0)])) };
  const { page, errors } = await fixture(t, { nodes: [nodes[0], placeholder] });
  await page.locator('tr[data-session="s-root"] .delegation-name').click();
  await waitForReader(page, "s-root");
  assert.equal(await page.locator(".delegation-transcript-link").count(), 0);
  assert.deepEqual(errors, []);
});

test("workspace sessions pages contain forty rows and keep navigation on the last page", async (t) => {
  const { page, errors } = await fixture(t);
  const allNodes = Array.from({ length: 45 }, (_, i) => ({
    ...session(`page-${i}`, "root", "", "claude", String(i + 1), 0, `Session ${i + 1}`, i),
    child_count: 0,
  }));
  await page.route("http://delegation.test/api/work/work/delegation?**", route => {
    const url = new URL(route.request().url());
    assert.equal(url.searchParams.get("limit"), "40");
    const offset = Number(url.searchParams.get("cursor") || 0);
    const rows = allNodes.slice(offset, offset + 40);
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        snapshot: "pages-v1", rows, ancestors: [], focus: nodes,
        matched: Object.fromEntries(rows.map(n => [n.id, true])),
        total: 45, session_count: 45, offset,
        next_cursor: offset === 0 ? "40" : "",
        metrics: specs, maxima: { self: {}, all: {} }, totals: nodes[0].all,
        start, end, density: [], warnings: [],
      }),
    });
  });
  await page.reload();
  await page.waitForFunction(() => document.querySelectorAll(".delegation-table tbody tr").length === 40);
  const scroll = page.locator(".delegation-table-scroll");
  assert.ok((await scroll.boundingBox()).height > 1000);
  assert.equal(await scroll.evaluate(el => el.scrollHeight > el.clientHeight), false);
  await page.locator(".workspace-overview").evaluate(el => el.scrollTop = 650);
  assert.ok(await page.locator(".workspace-overview").evaluate(el => el.scrollTop) >= 650);
  assert.equal(await scroll.evaluate(el => el.scrollTop), 0);
  const pagination = page.locator(".delegation-pagination");
  assert.match(await pagination.innerText(), /1–40 of 45 sessions/);
  assert.equal(await pagination.getByRole("button", { name: "Previous", exact: true }).isDisabled(), true);
  await pagination.getByRole("button", { name: "Next", exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll(".delegation-table tbody tr").length === 5);
  assert.match(await pagination.innerText(), /41–45 of 45 sessions/);
  assert.equal(await pagination.getByRole("button", { name: "Next", exact: true }).isDisabled(), true);
  await pagination.getByRole("button", { name: "Previous", exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll(".delegation-table tbody tr").length === 40);
  assert.match(await pagination.innerText(), /1–40 of 45 sessions/);
  assert.deepEqual(errors, []);
});

test("First evidence uses local time and readable tooltips in the table and inbox", async (t) => {
  const { page, errors } = await fixture(t);
  const columns = page.locator(".delegation-columns");
  await columns.locator("summary").click();
  await columns.getByLabel("First evidence", { exact: true }).check();
  await columns.locator("summary").click();
  const index = await page.getByRole("button", { name: "First evidence", exact: true }).first().evaluate(button => button.closest("th").cellIndex + 1);
  const cell = page.locator('tr[data-session="s-root"] td').nth(index);
  assert.match(await cell.innerText(), /Sep 24, 6:00 AM MDT/);
  const title = await cell.getAttribute("title");
  assert.match(title, /Sep 24, 2026, 6:00:00 AM MDT/);
  assert.ok(!title.includes(start.toLocaleString("en-US")));
  assert.ok(!title.includes(" date"));
  await page.locator('tr[data-session="s-root"] .delegation-name').click();
  await waitForReader(page, "s-root");
  await page.getByRole("combobox", { name: "Session comparison metric", exact: true }).selectOption("self:start");
  const compact = page.locator('.delegation-inbox-row[data-session="s-root"] .delegation-inbox-metric');
  assert.match(await compact.innerText(), /Sep 24, 6:00 AM MDT/);
  assert.equal(await compact.getAttribute("title"), title);
  assert.deepEqual(errors, []);
});

test("conversation search stays scoped to the inbox reader and reveals retained hits", async (t) => {
  const { page, errors } = await fixture(t, { url: "/work/work/conversation/child" });
  await waitForReader(page, "s-child");
  const requests = [];
  await page.route("http://delegation.test/api/search/text", route => {
    requests.push(route.request().postDataJSON());
    return route.fulfill({ contentType: "application/json", body: JSON.stringify({
      items: [{ message_id: "reply-child", conversation_id: "child", workspace_id: "work", snippet: "Reviewed the change.", start_byte: 0, end_byte: 8, range_kind: "literal" }],
      complete: true, skipped_oversize: 0, elapsed_ms: 1, stop_reason: "complete", coverage: "Fixture coverage",
    }) });
  });
  await page.getByRole("button", { name: "Find", exact: true }).click();
  const search = page.locator(".workspace-reader-content .conversation-search-composer");
  await search.getByRole("combobox", { name: "Match using", exact: true }).selectOption("literal");
  await search.getByRole("textbox", { name: "Text to find exactly", exact: true }).fill("Reviewed");
  await search.getByRole("button", { name: "Search messages", exact: true }).click();
  await search.getByRole("button", { name: "Reviewed the change.", exact: true }).click();
  assert.equal(requests.length, 1);
  assert.equal(requests[0].workspace_id, "work");
  assert.equal(requests[0].conversation_id, "child");
  const hit = page.locator('[data-message-id="reply-child"].authorship-target');
  assert.equal(await hit.locator("mark.conversation-find-match").innerText(), "Reviewed");
  await search.getByRole("button", { name: "Close conversation search", exact: true }).click();
  assert.equal(await hit.count(), 0);
  assert.deepEqual(errors, []);
});

async function waitForReader(page, session) {
  await page.waitForFunction(id => document.querySelector(".delegation-session-heading")?.dataset.session === id && document.activeElement === document.querySelector(".conversation-panel"), session);
}
async function openInboxSession(page, session) {
  await page.locator(`.delegation-inbox-row[data-session="${session}"] .delegation-name`).click();
  await waitForReader(page, session);
}

test("overview loads no transcript; compact comparison sorting carries back to the full table", async (t) => {
  const { page, errors, transcriptRequests } = await fixture(t);
  assert.deepEqual(transcriptRequests, []);
  assert.equal(await page.locator(".conversation-panel").count(), 0);
  await page.locator('tr[data-session="s-root"] .delegation-name').click();
  await waitForReader(page, "s-root");
  assert.deepEqual(transcriptRequests, ["root"]);
  const metric = page.getByRole("combobox", { name: "Session comparison metric", exact: true });
  assert.equal(await metric.locator("option").count(), keys.length * 2);
  await metric.selectOption("self:tokens");
  await page.waitForFunction(() => new URL(location.href).searchParams.get("delegation_scope") === "self");
  await page.getByRole("button", { name: "Sort Tokens, currently descending", exact: true }).click();
  await page.waitForFunction(() => document.querySelector(".delegation-inbox-row").dataset.session === "s-nested");
  await page.locator(".workspace-reader-bar").getByRole("button", { name: "Hide workspace sessions", exact: true }).click();
  assert.equal(await page.locator(".workspace-overview").evaluate(el => el.inert), true);
  await page.getByRole("button", { name: "Show workspace sessions", exact: true }).click();
  assert.equal(await page.locator(".workspace-overview").evaluate(el => el.inert), false);
  await page.locator(".workspace-nav-bar").getByRole("button", { name: "Open workspace overview", exact: true }).click();
  await page.locator(".delegation-table tbody tr").first().waitFor();
  assert.equal(await page.locator(".delegation-table tbody tr").first().getAttribute("data-session"), "s-nested");
  assert.equal(new URL(page.url()).searchParams.get("delegation_direction"), "asc");
  assert.equal(await page.locator(".workspace-reader").isVisible(), false);
  assert.deepEqual(errors, []);
});

test("single long turns open on first visit and retain the user's closed state when revisited", async (t) => {
  const singleTurn = conv => [
    { id: `prompt-${conv}`, native_id: `prompt-${conv}`, kind: "message", role: "user", text: "Run a long task", created_at: new Date(start).toISOString() },
    ...Array.from({ length: 310 }, (_, i) => ({ id: `reply-${conv}-${i}`, native_id: `reply-${conv}-${i}`, kind: "message", role: "assistant", text: `Progress ${i}`, created_at: new Date(start + i * 1000).toISOString() })),
  ];
  const { page, errors } = await fixture(t, { messages: singleTurn });
  await page.locator('tr[data-session="s-child"] .delegation-name').click();
  await waitForReader(page, "s-child");
  const turn = page.locator(".workspace-reader-content .turn-list > .turn");
  assert.equal(await turn.count(), 1);
  assert.equal(await turn.evaluate(el => el.open), true);
  assert.ok(await turn.locator(".timeline-entry").count() > 0);
  assert.equal(await page.getByRole("button", { name: "Show in sessions ←", exact: true }).count(), 0);
  assert.equal(await page.locator(".workspace-reader-content .turn-list").evaluate(el => getComputedStyle(el).backgroundColor), "rgba(0, 0, 0, 0)");
  await turn.locator(":scope > summary").click();
  assert.equal(await turn.evaluate(el => el.open), false);
  await openInboxSession(page, "s-root");
  assert.equal(await turn.evaluate(el => el.open), true);
  await openInboxSession(page, "s-child");
  assert.equal(await turn.evaluate(el => el.open), false);
  assert.deepEqual(errors, []);
});

test("browser history restores reader position and expanded turns with independent pane scrolling", async (t) => {
  const longMessages = conv => Array.from({ length: 36 }, (_, i) => [
    { id: `prompt-${conv}-${i}`, kind: "message", role: "user", text: `Task ${i} in ${conv}`, created_at: new Date(start + i * 3000).toISOString() },
    { id: `reply-${conv}-${i}`, kind: "message", role: "assistant", text: `Results for ${conv}, turn ${i}.\n`.repeat(12), created_at: new Date(start + i * 3000 + 1000).toISOString() },
  ]).flat();
  const { page, errors } = await fixture(t, { messages: longMessages });
  const overviewURL = page.url();
  await page.locator('tr[data-session="s-root"] .delegation-name').click();
  await waitForReader(page, "s-root");
  const reader = page.locator(".workspace-reader-content");
  await reader.locator(".turn-list > .turn").nth(8).locator(":scope > summary").click();
  await reader.evaluate(el => { el.scrollTop = 820; });
  const position = await reader.evaluate(el => el.scrollTop);
  const turns = await reader.locator(".turn-list > .turn").evaluateAll(turns => turns.map(turn => turn.open));
  assert.ok(position > 700);
  const sidebarPosition = await page.locator(".workspace-overview").evaluate(el => el.scrollTop);
  await reader.evaluate(el => { el.scrollTop += 50; });
  assert.equal(await page.locator(".workspace-overview").evaluate(el => el.scrollTop), sidebarPosition);
  await reader.evaluate((el, top) => { el.scrollTop = top; }, position);
  await openInboxSession(page, "s-child");
  assert.equal(await reader.evaluate(el => el.scrollTop), 0);
  await page.goBack();
  await waitForReader(page, "s-root");
  assert.ok(Math.abs(await reader.evaluate(el => el.scrollTop) - position) <= 2, `Restored ${await reader.evaluate(el => el.scrollTop)}, expected ${position}`);
  assert.deepEqual(await reader.locator(".turn-list > .turn").evaluateAll(turns => turns.map(turn => turn.open)), turns);
  await page.goBack();
  await page.locator(".delegation-table tbody tr").first().waitFor();
  assert.equal(page.url(), overviewURL);
  assert.equal(await page.locator(".workspace-reader").isVisible(), false);
  await page.goForward();
  await waitForReader(page, "s-root");
  assert.ok(Math.abs(await reader.evaluate(el => el.scrollTop) - position) <= 2);
  assert.deepEqual(errors, []);
});

test("mobile reader uses a dismissible session sheet and keeps conversation selection explicit", async (t) => {
  const { page, errors } = await fixture(t, { viewport: { width: 390, height: 850 } });
  await page.locator('tr[data-session="s-child"] .delegation-name').click();
  await waitForReader(page, "s-child");
  const navigator = page.locator(".workspace-overview");
  assert.equal(await navigator.evaluate(el => el.inert), true);
  assert.equal(await page.locator(".workspace-divider").isVisible(), false);
  await page.getByRole("button", { name: "Show workspace sessions", exact: true }).click();
  const sheet = page.getByRole("dialog", { name: "Workspace sessions", exact: true });
  await sheet.waitFor();
  assert.equal(await sheet.getAttribute("aria-modal"), "true");
  assert.equal(await page.locator(".workspace-reader").evaluate(el => el.inert), true);
  await page.keyboard.press("Control+f");
  assert.equal(await sheet.evaluate(el => el.contains(document.activeElement)), true);
  await sheet.locator('button[aria-label="Close workspace sessions"]').focus();
  await page.keyboard.press("Shift+Tab");
  assert.equal(await sheet.evaluate(el => el.contains(document.activeElement)), true);
  await page.keyboard.press("Escape");
  assert.equal(await sheet.count(), 0);
  assert.equal(await page.getByRole("button", { name: "Show workspace sessions", exact: true }).evaluate(el => document.activeElement === el), true);
  await page.getByRole("button", { name: "Show workspace sessions", exact: true }).click();
  await openInboxSession(page, "s-nested");
  assert.equal(await sheet.count(), 0);
  assert.equal(await page.locator(".workspace-reader").evaluate(el => el.inert), false);
  assert.equal(new URL(page.url()).pathname, "/work/work/conversation/nested");
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.locator(".workspace-reader-bar").getByRole("button", { name: "Open workspace overview", exact: true }).click();
  await page.locator(".delegation-table tbody tr").first().waitFor();
  assert.equal(await navigator.evaluate(el => el.inert), false);
  assert.deepEqual(errors, []);
});

test("conversation deep links retain the tool anchor and canonicalize legacy links", async (t) => {
  const { page, errors } = await fixture(t, { url: "/work/work/conversation/child?explorer=parallel&delegation_session=s-child&message=prompt-child" });
  await waitForReader(page, "s-child");
  await page.locator('[data-message-id="prompt-child"].authorship-target').waitFor();
  assert.equal(new URL(page.url()).searchParams.get("message"), "prompt-child");
  assert.equal(await page.locator('.delegation-lane-current[data-session="s-child"]').count(), 1);
  await page.goto("http://delegation.test/work/work?conversation=nested&delegation_session=s-nested");
  await waitForReader(page, "s-nested");
  assert.equal(new URL(page.url()).pathname, "/work/work/conversation/nested");
  assert.equal(await page.locator('.delegation-inbox-row.current[data-session="s-nested"]').count(), 1);
  assert.deepEqual(errors, []);
});

test("slow earlier transcript responses cannot replace the latest inbox selection", async (t) => {
  const { page, errors } = await fixture(t);
  let release;
  const held = new Promise(resolve => { release = resolve; });
  let requested;
  const requestStarted = new Promise(resolve => { requested = resolve; });
  await page.route("http://delegation.test/api/work/work/conversations/child", async route => {
    requested();
    await held;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify({ ...work.conversations[1], messages: messages("child") }) });
  });
  await page.locator('tr[data-session="s-child"] .delegation-name').click();
  await requestStarted;
  await openInboxSession(page, "s-nested");
  const response = page.waitForResponse(response => response.url().endsWith("/conversations/child"));
  release();
  await response;
  await page.waitForTimeout(100);
  assert.equal(new URL(page.url()).pathname, "/work/work/conversation/nested");
  assert.equal(await page.locator(".delegation-session-heading").getAttribute("data-session"), "s-nested");
  assert.equal(await page.locator(".conversation-panel").count(), 1);
  assert.deepEqual(errors, []);
});

test("Back restores timeline bounds and branch collapse from the overview route", async (t) => {
  const { page, errors } = await fixture(t);
  await page.getByRole("button", { name: "Collapse Review implementation", exact: true }).click();
  await page.getByRole("tab", { name: "Parallel activity", exact: true }).click();
  await page.locator(".delegation-mark").first().waitFor();
  const axis = await page.locator(".delegation-axis > div").boundingBox();
  await page.mouse.move(axis.x + axis.width * .2, axis.y + 20);
  await page.mouse.down();
  await page.mouse.move(axis.x + axis.width * .8, axis.y + 20, { steps: 8 });
  await page.mouse.up();
  await page.waitForFunction(() => Number.parseFloat(document.querySelector(".delegation-viewport").style.width) < 100);
  const bounds = new URL(page.url()).searchParams;
  await page.locator('.delegation-lane[data-session="s-root"] .delegation-name').click();
  await waitForReader(page, "s-root");
  await page.getByRole("slider", { name: "Timeline window start", exact: true }).focus();
  await page.keyboard.press("Home");
  await page.waitForFunction(() => new URL(location.href).searchParams.get("delegation_start") === String(Date.parse("2026-09-24T12:00:00Z")));
  await page.goBack();
  await page.locator(".delegation-mark").first().waitFor();
  assert.equal(await page.locator(".workspace-reader").isVisible(), false);
  assert.equal(new URL(page.url()).searchParams.get("delegation_start"), bounds.get("delegation_start"));
  assert.equal(new URL(page.url()).searchParams.get("delegation_end"), bounds.get("delegation_end"));
  assert.equal(await page.locator('.delegation-lane[data-session="s-nested"]').count(), 0);
  assert.equal(await page.getByRole("tab", { name: "Parallel activity", exact: true }).getAttribute("aria-selected"), "true");
  assert.deepEqual(errors, []);
});
