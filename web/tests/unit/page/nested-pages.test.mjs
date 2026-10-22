import { test } from "node:test";
import assert from "node:assert/strict";
import { localPageNavigation, nestedPageTrail } from "../../../src/nested-pages-model";
import { appLocation, appLocationLabel, appSections, initialNavigationExpansion, toggleNavigationSection } from "../../../src/app-navigation-model";

test("the app tree resolves global, nested, and legacy destinations without changing their queries", () => {
  const params = new URLSearchParams('analyze_mode=query&analyze_data=usage&q_usage=saved&analyze_days=7');
  const before = params.toString();
  const settings = [{ id: '/settings', href: '/settings', label: 'Library health' }, { id: '/settings/sources', href: '/settings/sources', label: 'Sources' }];
  const sections = appSections(settings, params, true);
  assert.ok(sections.some(section => section.id === 'tl1'));
  assert.ok(!appSections(settings, params, false).some(section => section.id === 'tl1'));
  const active = appLocation('/analyze', params);
  assert.equal(appLocationLabel(sections, active).page.label, 'Token usage');
  const link = new URL(appLocationLabel(sections, active).page.href, 'https://pharos.test');
  assert.equal(link.searchParams.get('q_usage'), 'saved');
  assert.equal(params.toString(), before, 'building navigation never mutates the current URL');
  assert.equal(appLocationLabel(sections, appLocation('/settings/sources', params)).page.label, 'Sources');
  assert.equal(appLocation('/work/work-1/conversation/conv-1', params).section, 'library');
  for (const [path, query, current] of [
    ['/analyze', 'analyze_view=writing', 'dashboards:human'],
    ['/analyze', 'analyze_view=writing&writing=messages', 'query:writing_messages'],
    ['/analyze', 'analyze_view=conversations', 'query:conversation_activity'],
    ['/analyze', 'analyze_view=tokens&machine_view=time', 'dashboards:time'],
    ['/analyze', 'analyze_view=carbon', 'dashboards:carbon'],
    ['/tools', 'tools_view=skills', 'query:skill_usages'],
    ['/tools', 'tools_view=summary', 'query:tools'],
    ['/mcp', '', 'dashboards:mcp'],
  ]) assert.equal(appLocation(path, new URLSearchParams(query)).current, current);
});

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
  const section = { id: "test", label: "Test", href: "/", pages };
  assert.deepEqual(initialNavigationExpansion(section, "mcp").test, ["settings", "agents"]);
  assert.deepEqual(initialNavigationExpansion(section, "missing").test, []);
  assert.deepEqual(pages, before);
});

test("navigation expansion preserves each section's last toggle independently", () => {
  const section = appSections([], new URLSearchParams(), false).find(section => section.id === "analyze");
  const initial = initialNavigationExpansion(section, "dashboards:tokens");
  assert.deepEqual(initial.analyze, ["dashboards"]);

  const both = toggleNavigationSection(initial, "analyze", "query");
  assert.deepEqual(both.analyze, ["dashboards", "query"]);
  const closed = toggleNavigationSection(both, "analyze", "dashboards");
  assert.deepEqual(closed.analyze, ["query"]);
  const elsewhere = toggleNavigationSection(closed, "settings", "query");
  assert.deepEqual(elsewhere.analyze, ["query"]);
  assert.deepEqual(elsewhere.settings, ["query"]);
  assert.deepEqual(toggleNavigationSection(elsewhere, "analyze", "query").analyze, []);
  assert.deepEqual(toggleNavigationSection(elsewhere, "analyze", "dashboards").analyze, ["query", "dashboards"]);
  assert.deepEqual(initial.analyze, ["dashboards"], "toggles do not mutate earlier state");
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
