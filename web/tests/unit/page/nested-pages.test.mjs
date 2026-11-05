import { test } from "node:test";
import assert from "node:assert/strict";
import { localPageNavigation, nestedPageTrail } from "../../../src/nested-pages-model";
import { analyzePages, appLocation, appLocationLabel, appSections, isNavigationSectionExpanded, navigationPageURL, optimizePages, toggleNavigationSection } from "../../../src/app-navigation-model";
import { EMPTY_QUERY, decodeQueryStrict, encodeQuery } from "@pythia-software/query-table-core";
import { analyzeQueryDatasets, usageTopics } from "../../../src/usage-workspace-model";

test("every dashboard belongs to People, Agents or Outcomes and retains its destination", () => {
  const params = new URLSearchParams('analyze_topic=human&analyze_days=7');
  const before = params.toString();
  for (const previews of [false, true]) {
    const pages = analyzePages(params, previews), groups = pages.find(page => page.id === 'dashboards').children;
    assert.deepEqual(groups.map(group => group.label), ['People', 'Agents', 'Outcomes']);
    const dashboards = groups.flatMap(group => group.children);
    // Agent Events is always listed, under Agents; the lab's Agent Experience waits on the preview.
    const topics = Object.keys(usageTopics).filter(topic => previews || topic !== 'agent_experience');
    assert.deepEqual(dashboards.map(page => page.id).sort(), topics.map(topic => `dashboards:${topic}`).sort());
    assert.equal(new Set(dashboards.map(page => page.id)).size, dashboards.length);
    for (const group of groups) {
      assert.ok(group.children.length);
      for (const page of group.children) {
        const destination = new URL(page.href, 'https://pharos.test');
        assert.equal(appLocation(destination.pathname, destination.searchParams).current, page.id);
        assert.deepEqual(nestedPageTrail(pages, page.id).map(page => page.id), ['dashboards', group.id, page.id]);
      }
    }
    assert.equal(nestedPageTrail(pages, 'dashboards:human')[1].label, 'People');
    assert.equal(nestedPageTrail(pages, 'dashboards:thinking-detail')[1].label, 'Agents');
    assert.equal(nestedPageTrail(pages, 'dashboards:agent_event_rates')[1].label, 'Agents');
    assert.equal(nestedPageTrail(pages, 'dashboards:carbon')[1].label, 'Outcomes');
  }
  assert.equal(params.toString(), before);
});

test("the app tree resolves global, nested, and legacy destinations without changing their queries", () => {
  const params = new URLSearchParams('analyze_mode=query&analyze_data=usage&q_usage=saved&analyze_days=7');
  const before = params.toString();
  const settings = [{ id: '/settings', href: '/settings', label: 'Library health' }, { id: '/settings/sources', href: '/settings/sources', label: 'Sources' }];
  const sections = appSections(settings, params);
  const active = appLocation('/analyze', params);
  assert.equal(appLocationLabel(sections, active).page.label, 'Token usage');
  const link = new URL(appLocationLabel(sections, active).page.href, 'https://pharos.test');
  assert.deepEqual(decodeQueryStrict(link.searchParams.get('q_usage')).where, [], 'dataset navigation opens the complete source');
  assert.equal(params.toString(), before, 'building navigation never mutates the current URL');
  assert.equal(appLocationLabel(sections, appLocation('/settings/sources', params)).page.label, 'Sources');
  assert.equal(appLocation('/work/work-1/conversation/conv-1', params).section, 'library');
  const cards = appLocationLabel(sections, appLocation('/cards', new URLSearchParams('card_topic=human')));
  assert.equal(cards.section.id, 'home');
  assert.equal(cards.page.label, 'Card Library');
  assert.deepEqual(cards.section.pages, [], 'the library is reached through the catalog, without adding a Home sidebar');
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

test("Optimize resolves separate efficiency, safety and fixes pages", () => {
  const sections = appSections([], new URLSearchParams());
  const optimize = sections.find(section => section.id === "findings");
  assert.deepEqual(optimize.pages.map(page => page.id), ["findings", "safety", "fixes"]);
  assert.deepEqual(optimizePages().map(page => page.id), ["findings", "safety", "fixes"]);
  for (const [path, query, trail] of [
    ["/findings", "", ["findings", "findings:dashboard"]],
    ["/findings", "list=watching&repo=example", ["findings", "findings:dashboard"]],
    ["/findings", "finding=example&tab=evidence", ["findings", "findings:dashboard"]],
    ["/findings", "view=table&q_findings=saved", ["findings", "findings:table"]],
    ["/findings", "view=settings", ["findings", "findings:settings"]],
    ["/settings/optimization", "", ["findings", "findings:settings"]],
    ["/findings", "optimize_area=safety&view=table", ["safety", "safety:table"]],
    ["/findings", "optimize_area=safety&view=settings", ["safety", "safety:settings"]],
    ["/findings", "optimize_area=safety&safety_list=handed_off&safety_case=example", ["safety", "safety:dashboard"]],
    ["/findings", "optimize_area=fixes", ["fixes", "fixes:dashboard"]],
    ["/findings", "optimize_area=fixes&fixes_item=x&fixes_list=dismissed", ["fixes", "fixes:dashboard"]],
    ["/findings", "optimize_area=fixes&view=table&q_fixes=saved", ["fixes", "fixes:table"]],
    ["/findings", "optimize_area=fixes&view=settings", ["fixes", "fixes:settings"]],
  ]) {
    const params = new URLSearchParams(query), before = params.toString();
    const selected = appLocation(path, params);
    assert.equal(selected.section, optimize.id);
    assert.deepEqual(nestedPageTrail(optimize.pages, selected.current).map(page => page.id), trail);
    const destination = appLocationLabel(sections, selected).page;
    const url = new URL(destination.href, "https://pharos.test");
    assert.deepEqual(appLocation(url.pathname, url.searchParams), selected, "page links resolve back to their menu selection");
    assert.equal(params.toString(), before);
  }
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
  assert.deepEqual(pages, before);
});

test("navigation groups preserve each section's choices independently of their defaults", () => {
  const initial = {};
  const sections = appSections([], new URLSearchParams());
  for (const section of sections) {
    for (const page of section.pages.filter(page => page.children)) {
      assert.equal(isNavigationSectionExpanded(initial[section.id] ?? {}, page.id), true);
    }
  }

  const closed = toggleNavigationSection(initial, "analyze", "query");
  assert.equal(isNavigationSectionExpanded(closed.analyze, "query"), false);
  assert.equal(isNavigationSectionExpanded(closed.analyze, "dashboards"), true);
  const elsewhere = toggleNavigationSection(closed, "findings", "findings");
  assert.equal(isNavigationSectionExpanded(elsewhere.analyze, "query"), false);
  assert.equal(isNavigationSectionExpanded(elsewhere.findings, "findings"), false);
  const reopened = toggleNavigationSection(elsewhere, "analyze", "query");
  assert.equal(isNavigationSectionExpanded(reopened.analyze, "query"), true);
  assert.equal(isNavigationSectionExpanded(reopened.findings, "findings"), false);
  assert.deepEqual(initial, {}, "toggles do not mutate earlier state");
  assert.equal(isNavigationSectionExpanded(closed.analyze, "query"), false);
  const nested = toggleNavigationSection(reopened, 'analyze', 'dashboard-group:people');
  const parentClosed = toggleNavigationSection(nested, 'analyze', 'dashboards');
  const parentReopened = toggleNavigationSection(parentClosed, 'analyze', 'dashboards');
  assert.equal(isNavigationSectionExpanded(parentReopened.analyze, 'dashboard-group:people'), false);
  assert.equal(isNavigationSectionExpanded(parentReopened.analyze, 'dashboard-group:agents'), true);
  assert.equal(isNavigationSectionExpanded(parentReopened.analyze, 'dashboards'), true);
  assert.equal(isNavigationSectionExpanded({}, 'dashboard-group:people', false), false);
  assert.equal(isNavigationSectionExpanded({}, 'dashboard-group:outcomes', true), true);
  const opened = toggleNavigationSection({}, 'analyze', 'dashboard-group:people', false);
  assert.equal(isNavigationSectionExpanded(opened.analyze, 'dashboard-group:people', false), true);
  assert.equal(isNavigationSectionExpanded(nested.analyze, 'dashboard-group:people', true), false, 'explicit choices override the current-page default');
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

test('sidebar presentation updates keep the picker open while page and query changes close it', () => {
  const base = 'https://pharos.test/analyze?analyze_topic=tokens';
  const presentation = new URL(base);
  presentation.searchParams.set('nav_expanded', JSON.stringify({analyze:{'dashboard-group:outcomes':false}}));
  presentation.searchParams.set('nav_collapsed', 'false');
  assert.equal(navigationPageURL(presentation.href), navigationPageURL(base));
  for (const destination of ['/settings', '/analyze?analyze_topic=human', '/analyze?analyze_topic=tokens&analyze_days=7', '/analyze?analyze_topic=tokens#chart']) {
    assert.notEqual(navigationPageURL(new URL(destination, base).href), navigationPageURL(base));
  }
});


test("dataset navigation groups every available table and opens complete records", () => {
  const current = new URLSearchParams({ analyze_mode: "query", analyze_data: "messages", q_messages: encodeQuery({ ...EMPTY_QUERY, where: [{ field: "role", op: "=", value: "user" }] }), record_messages: "old keyword", window_dataset: "messages", window_days: "7" });
  const before = current.toString();
  for (const previews of [false, true]) {
    const tree = analyzePages(current, previews), groups = tree.find(page => page.id === "query").children;
    assert.deepEqual(groups.map(group => group.label), ["Conversations & agent sessions", "Tools & context", "Usage & activity", "Agent events"]);
    const pages = groups.flatMap(group => group.children);
    const expected = analyzeQueryDatasets.filter(dataset => previews || !["agent_events", "agent_event_messages"].includes(dataset));
    assert.deepEqual(pages.map(page => page.id).sort(), expected.map(dataset => `query:${dataset}`).sort());
    assert.equal(new Set(pages.map(page => page.id)).size, pages.length);
    for (const group of groups) {
      assert.ok(group.children.length);
      for (const page of group.children) {
        const params = new URL(page.href, "https://pharos.test").searchParams;
        const dataset = params.get("analyze_data"), query = decodeQueryStrict(params.get(`q_${dataset}`));
        assert.ok(query, dataset);
        assert.deepEqual(query.where, [], dataset);
        assert.equal(query.grouping, undefined, dataset);
        assert.equal([...params.keys()].some(key => key.startsWith("record_") || key.startsWith("window_")), false, dataset);
        assert.equal(appLocation("/analyze", params).current, page.id);
        assert.deepEqual(nestedPageTrail(tree, page.id).map(item => item.id), ["query", group.id, page.id]);
      }
    }
  }
  assert.equal(current.toString(), before);
});
