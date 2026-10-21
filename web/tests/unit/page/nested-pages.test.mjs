import { test } from "node:test";
import assert from "node:assert/strict";
import { localPageNavigation, nestedPageTrail } from "../../../src/nested-pages-model";

test("navigation and breadcrumbs follow the selected page at any depth", () => {
  const pages = [
    { id: "dashboards", label: "Dashboards", children: [
      { id: "tokens", label: "Tokens", href: "/analyze?analyze_topic=tokens" },
    ] },
    { id: "settings", label: "Settings", children: [
      { id: "agents", label: "Agents", children: [
        { id: "mcp", label: "MCP", href: "/settings/mcp" },
      ] },
    ] },
  ];
  const before = structuredClone(pages);
  assert.deepEqual(nestedPageTrail(pages, "mcp").map(page => page.id), ["settings", "agents", "mcp"]);
  assert.equal(nestedPageTrail(pages, "tokens").at(-1).href, "/analyze?analyze_topic=tokens");
  assert.deepEqual(nestedPageTrail(pages, "agents").map(page => page.id), ["settings", "agents"]);
  assert.deepEqual(nestedPageTrail(pages, "missing"), []);
  assert.deepEqual(pages, before);
});

test("local page navigation preserves the complete destination and browser link semantics", () => {
  const base = "https://pharos.test/analyze?analyze_topic=tokens";
  const href = "/settings/mcp?client=codex#connection";
  assert.equal(localPageNavigation(href, base, { button: 0 }), href);
  assert.equal(localPageNavigation("sources?host=mac", "https://pharos.test/settings/mcp", { button: 0 }), "/settings/sources?host=mac");
  assert.equal(localPageNavigation("https://pharos.test" + href, base, { button: 0 }), href);
  for (const click of [
    { button: 1 }, { button: 2 }, { button: 0, metaKey: true }, { button: 0, ctrlKey: true },
    { button: 0, shiftKey: true }, { button: 0, altKey: true }, { button: 0, defaultPrevented: true },
    { button: 0, download: true }, { button: 0, target: "_blank" }, { button: 0, target: "other-window" },
  ]) assert.equal(localPageNavigation(href, base, click), null);
  for (const external of ["https://example.com/settings", "mailto:owner@example.com"])
    assert.equal(localPageNavigation(external, base, { button: 0 }), null);
  assert.equal(localPageNavigation(href, base, { button: 0, target: "_self" }), href);
});
