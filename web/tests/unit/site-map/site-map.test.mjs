// The site map (internal/archive/assets/site-map.json) and its cases
// (testdata/site-map-cases.json): that its routes are the ones the server and
// the page shell serve, that its tables decide every case as the page does,
// and that every link it builds reads back, through the page's own readers,
// as exactly the state that produced it. npm run check-site-map catches a
// stale file; these check what the file says.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { decodeQueryStrict, EMPTY_QUERY } from "@pythia-software/query-table-core";
import { assets } from "../helpers/classic-script.mjs";
import map from "../../../../internal/archive/assets/site-map.json";
import cases from "../../../../testdata/site-map-cases.json";
import { cardURL, chartVerdict, dashboardURL, drawChart, judgeChart, readChart, viewChart, settingsPages, siteSchemas, viewTitles, windowTerms } from "../../../src/site-map";
import { analysisCatalogs } from "../../../src/analysis-catalogs";
import { dashboardDefaults } from "../../../src/dashboard-defaults";
import { canonicalState, normalizeAnalysis, readAnalysisLink, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { readDashboardLink } from "../../../src/usage-dashboard-model";
import { readUsageWorkspace, usageTopics } from "../../../src/usage-workspace-model";
import { queryFromURL, readRollingWindow } from "../../../src/query-links";
import { restoreSearch } from "../../../src/search-composer-state";
import { readFindingsRoute } from "../../../src/findings-route";
import { readDelegationRoute } from "../../../src/delegation-route";
import { legacyRoutes, idSources, readerParams } from "../../../src/site-routes";

globalThis.location ??= new URL("http://localhost/");
const archive = dirname(assets), ui = readFileSync(join(assets, "ui.py"), "utf8");
const goSource = readdirSync(archive).filter(file => file.endsWith(".go") && !file.endsWith("_test.go")).map(file => readFileSync(join(archive, file), "utf8")).join("\n");
const params = url => new URL(url, "http://localhost").searchParams;
const plain = value => JSON.parse(JSON.stringify(value));

test("the site map's routes are the ones the server and the page shell serve", () => {
  const served = new Set(/func isUIPage[\s\S]*?case ([^:]*):/.exec(goSource)[1].match(/"[^"]*"/g).map(path => JSON.parse(path)));
  assert.match(goSource, /strings\.HasPrefix\(path, "\/work\/"\)/, "work pages are served by prefix");
  const routes = new Set(Object.values(map.nodes).map(node => node.route).filter(route => !route.includes("{")));
  assert.deepEqual([...served].filter(path => !routes.has(path) && !(path in legacyRoutes)), [], "every served page is in the site map or a legacy alias");
  assert.deepEqual([...routes].filter(route => !served.has(route)), [], "every site map route is served");
  assert.ok(map.nodes.work.route.startsWith("/work/"));
  // The shell's own lists.
  const shellRoutes = JSON.parse(/const ROUTES=new Set\((\[[^\]]*\])\)/.exec(ui)[1].replaceAll("'", "\""));
  for (const name of shellRoutes) assert.ok(served.has(`/${name}`), `ROUTES has ${name}`);
  // And the shell routes every top-level page; / is home, and work pages route by prefix.
  for (const node of Object.values(map.nodes).filter(node => !node.path.includes("/") && node.route !== "/" && !node.route.includes("{"))) assert.ok(shellRoutes.includes(node.route.slice(1)), `ROUTES lacks ${node.route}`);
  const settings = settingsPages(ui, readFileSync(join(assets, "managed-library.html"), "utf8"));
  assert.deepEqual(settings.map(page => page.route).sort(), Object.values(map.nodes).filter(node => node.path.startsWith("settings")).map(node => node.route).sort());
  assert.equal(viewTitles(ui).findings, map.nodes.findings.title);
});

test("ID sources are registered MCP tools, and the reader's parameters are the shell's", () => {
  for (const [id, sources] of Object.entries(idSources)) for (const { tool } of sources) assert.match(goSource, new RegExp(`case "${tool}":`), `${id} comes from ${tool}`);
  assert.deepEqual(map.id_sources, idSources);
  const update = /const updateConversationURL=[^;]*;[\s\S]*?syncNavigator\(\)\};/.exec(ui)[0];
  for (const { name } of readerParams) {
    assert.match(update, new RegExp(`params\\.set\\('${name}'`), `the reader writes ${name}`);
    assert.match(ui, new RegExp(`\\.get\\('${name}'\\)`), `the reader reads ${name}`);
  }
});

// Every dashboard case with its topic.
const chartCases = Object.entries(cases.analyze).flatMap(([topic, items]) => items.map(item => ({ ...item, topic })));
const merged = (topic, chart) => ({ ...map.analyze.dashboards[topic].merge_defaults, ...chart });

test("the site map's rules judge every dashboard case, partial charts included, as the page does", () => {
  const verdicts = { valid: 0, coercible: 0, invalid: 0 };
  let partial = 0;
  for (const item of chartCases) {
    const name = `${item.topic} ${JSON.stringify(item.chart)}`, facts = map.analyze.dashboards[item.topic];
    assert.equal(chartVerdict(item.topic, item.chart), item.verdict, name);
    assert.equal(judgeChart(facts, map.analyze.version, item.chart), item.verdict, name);
    verdicts[item.verdict]++;
    const view = facts.views[merged(item.topic, item.chart).view ?? "trend"];
    if (view && view.keys.some(key => !(key in item.chart))) partial++;
    // What the rules draw is what the page draws, and keeping only the
    // view's keys (what Go writes) changes neither the verdict nor the drawing.
    if (item.verdict !== "invalid") {
      const kept = viewChart(item.topic, item.chart), viewDrawing = chart => plain(view.keys.map(key => normalizeAnalysis(analysisCatalogs[item.topic], merged(item.topic, chart))[key]));
      const renamed = readChart(facts, item.chart).renamed;
      assert.equal(readChart(facts, kept).renamed, false, `${name} is written in current names`);
      if (renamed) assert.notEqual(chartVerdict(item.topic, kept), "invalid", name); else assert.equal(chartVerdict(item.topic, kept), item.verdict, `${name} with only its view's keys`);
      assert.deepEqual(viewDrawing(kept), viewDrawing(item.chart), `${name} draws the same with only its view's keys`);
      const drawn = drawChart(view, readChart(facts, item.chart).merged), page = normalizeAnalysis(analysisCatalogs[item.topic], merged(item.topic, item.chart));
      for (const [key, value] of Object.entries(drawn)) assert.deepEqual(value, plain(page[key]), `${name} draws ${key}`);
    }
  }
  for (const verdict of Object.keys(verdicts)) assert.ok(verdicts[verdict] > 0, `some cases are ${verdict}`);
  // Canonical charts (every link the cases carry, cards' states, view defaults) are valid, never merely coercible.
  const canonical = [...chartCases.filter(item => item.url), cases.example].map(item => [item.topic, item.chart])
    .concat(map.cards.items.filter(card => card.state).map(card => [card.page.split("/")[1], card.state]))
    .concat(Object.entries(map.analyze.dashboards).flatMap(([topic, facts]) => Object.values(facts.views).map(view => [topic, view.defaults])));
  for (const [topic, chart] of canonical) assert.equal(judgeChart(map.analyze.dashboards[topic], map.analyze.version, chart), "valid", `${topic} ${JSON.stringify(chart)}`);
  for (const card of map.cards.items.filter(card => card.chart)) {
    const topic = card.page.split("/")[1], kept = viewChart(topic, card.chart);
    assert.equal(readChart(map.analyze.dashboards[topic], kept).renamed, false, `${card.id} is written in current names`);
  }
  assert.ok(partial > 1000, "the cases include partial charts");
  // Charts the page writes itself carry keys their view does not draw.
  for (const kind of ["switchView", "card"]) assert.ok(chartCases.some(item => item.written?.startsWith(kind) && item.verdict === "valid" && Object.keys(item.chart).some(key => !map.analyze.dashboards[item.topic].views[merged(item.topic, item.chart).view ?? "trend"].keys.includes(key) && key !== "selection")), `${kind} charts with keys their view does not draw`);
  assert.equal(chartCases.filter(item => item.written?.startsWith("card")).length, map.cards.items.filter(card => card.chart).length, "every card's own chart");
  for (const topic of Object.keys(usageTopics)) assert.ok(chartCases.some(item => item.topic === topic && item.verdict === "valid" && item.url), topic);
  for (const [topic, facts] of Object.entries(map.analyze.dashboards)) for (const [name, view] of Object.entries(facts.views)) {
    assert.equal(judgeChart(facts, map.analyze.version, view.defaults), "valid", `${topic} ${name} defaults`);
    for (const rule of view.rules) for (const constraint of rule.constraints) {
      const leaves = constraint.given.reduce(nodes => nodes.flatMap(node => Object.values(node)), [constraint.allowed]);
      for (const leaf of leaves) for (const value of leaf) assert.ok(view.domains[rule.option].includes(value), `${topic} ${name} ${rule.option} allows ${value}`);
    }
  }
});

// A dashboard link as the page opens it, with the reader each dashboard
// passes useUsageDashboardState: readAnalysisLink over the catalog's terms.
function openDashboard(url) {
  const search = params(url), workspace = readUsageWorkspace(search), catalog = analysisCatalogs[workspace.topic];
  assert.equal(workspace.mode, "dashboards");
  return { workspace, ...readDashboardLink(search, dashboardDefaults[workspace.topic], view => validAnalysisState(catalog, view), catalog.time, (view, where) => readAnalysisLink(catalog, canonicalState(catalog, view), where)) };
}

test("every valid dashboard link reads back as the state that built it", () => {
  for (const item of [...chartCases.filter(item => item.url), cases.example]) {
    const opened = openDashboard(item.url), name = `${item.topic} ${item.url}`;
    assert.equal(opened.workspace.topic, item.topic, name);
    assert.equal(opened.workspace.dataset, map.analyze.dashboards[item.topic].dataset, name);
    assert.equal(opened.error, "", name);
    assert.equal(opened.migrated, false, name);
    assert.deepEqual(plain(opened.view), plain({ ...dashboardDefaults[item.topic], ...item.chart }), name);
    const where = [...item.filters, ...windowTerms(map.analyze.dashboards[item.topic].time, item.days)];
    assert.deepEqual(plain(opened.where), where, name);
    assert.deepEqual(item.decoded.analyze_filters.where, where, name);
    const drawn = normalizeAnalysis(analysisCatalogs[item.topic], opened.view);
    for (const key of Object.keys(item.chart)) assert.deepEqual(plain(drawn[key]), item.chart[key], `${name} draws ${key}`);
    assert.equal(dashboardURL(item.topic, item.chart, item.filters, item.days), item.url, name);
  }
});

test("the motivating example is a valid tokens dashboard", () => {
  const { example } = cases;
  assert.equal(example.verdict, "valid");
  assert.deepEqual([example.chart.view, example.chart.metric, example.chart.split, example.chart.rollingPeriods, example.days], ["trend", "tokens", "model_family", 7, 30]);
  assert.deepEqual(example.filters, [{ field: "provider", op: "=", value: "claude" }, { field: "model_family", op: "contains", value: "opus" }]);
  const provider = map.datasets.usage.fields.find(field => field.name === "provider");
  assert.ok(provider.values.some(option => option.value === "claude"), "Anthropic usage is recorded as provider claude");
  assert.ok(map.op_sets[provider.ops].includes("="));
  assert.ok(map.op_sets[map.datasets.usage.fields.find(field => field.name === "model_family").ops].includes("contains"));
});

test("the sampled card links rebuild from the site map and open their dashboards", () => {
  const cards = new Map(map.cards.items.map(card => [card.id, card]));
  for (const item of cases.cards) {
    const card = cards.get(item.id), name = `${item.id} at ${item.days}`;
    assert.equal(cardURL(card, item.days), item.url, name);
    if (!card.page.startsWith("analyze/") || card.page.startsWith("analyze/query/")) continue;
    const opened = openDashboard(item.url);
    assert.equal(`analyze/${opened.workspace.topic}`, card.page, name);
    assert.equal(opened.error, "", name);
    const where = [...card.filters, ...item.days === null ? [] : JSON.parse(JSON.stringify(card.window.terms).replaceAll("{window_days}", String(item.days)))];
    assert.deepEqual(plain(opened.where), where, name);
    assert.deepEqual(item.decoded.analyze_filters.where, where, name);
    // A card's state is the chart its dashboard draws, in canonical form.
    const drawn = normalizeAnalysis(analysisCatalogs[opened.workspace.topic], opened.view);
    for (const [key, value] of Object.entries(card.state)) if (!(card.window?.chart_days ?? []).includes(key)) assert.deepEqual(plain(drawn[key]), value, `${name} draws ${key}`);
    assert.equal(chartVerdict(opened.workspace.topic, card.state), "valid", name);
  }
  assert.equal(new Set(cases.cards.map(item => item.id)).size, map.cards.items.length, "every card has a case");
  assert.ok(map.cards.items.some(card => card.window && cases.cards.filter(item => item.id === card.id).length === card.window.days.length), "some cards have every window");
});

test("every other page's link reads back through its page's reader", () => {
  const paths = new Set();
  for (const item of cases.nodes) {
    paths.add(item.path);
    const url = new URL(item.url, "http://localhost"), search = url.searchParams, node = map.nodes[item.path], name = `${item.path} ${item.url}`;
    assert.ok(node, name);
    if (item.url === node.route) continue;
    if (node.dataset && node.options?.some(option => option.kind === "where") && node.path !== "library") {
      const read = queryFromURL(item.url), field = map.datasets[node.dataset].window_field;
      assert.ok(read, name);
      assert.equal(read.dataset, node.dataset, name);
      const window = item.state.days === null ? [] : JSON.parse(JSON.stringify(map.windows.query_terms).replaceAll("{window_days}", String(item.state.days)).replaceAll("{field}", field));
      assert.deepEqual(plain(read.query.where), [...item.state.filters, ...window], name);
      assert.deepEqual(item.decoded[`q_${node.dataset}`].where, [...item.state.filters, ...window], name);
      assert.equal(read.window?.days ?? null, item.state.days, name);
      for (const [key, value] of Object.entries(node.fixed ?? {})) assert.equal(search.get(key), value, name);
    } else if (item.path === "library") {
      const restored = restoreSearch(search, siteSchemas.library);
      assert.ok(!restored.error, name);
      assert.equal(restored.kind, item.state.kind, name);
      if (item.state.kind === "file") assert.deepEqual([restored.file, restored.fileCase], [item.state.search, item.state.case], name);
      if (item.state.rules) {
        assert.deepEqual(restored.text.root.children.map(rule => ({ kind: rule.kind, text: rule.text })), item.state.rules, name);
        assert.deepEqual([restored.text.scope.surface, restored.text.scope.roles], [item.state.surface, item.state.roles], name);
      }
      if (item.state.window) assert.equal(readRollingWindow(search, "library").days, item.state.window, name);
      assert.deepEqual(plain(restored.query), plain({ ...decodeQueryStrict(search.get("q_library")), offset: 0 }), name);
    } else if (item.path.startsWith("findings")) {
      const route = readFindingsRoute(search);
      for (const [key, value] of Object.entries(item.state ?? {})) assert.equal(route[key], value, name);
    } else if (item.path === "cards") {
      for (const option of node.options) {
        const value = search.get(option.param);
        if (value !== null && option.values) assert.ok(option.values.some(choice => choice.value === value), `${name} ${option.param}`);
      }
      for (const [key, value] of Object.entries(item.state ?? {})) assert.equal(search.get(key), value, name);
    } else if (item.path === "work") {
      assert.equal(decodeURIComponent(url.pathname.split("/")[2]), item.state.work_id, name);
      for (const { name: param } of readerParams) assert.equal(search.get(param), item.state[param] ?? null, `${name} ${param}`);
      if (item.state.mode) {
        const route = readDelegationRoute(search);
        assert.deepEqual(plain({ mode: route.mode, sort: route.sort, scope: route.scope, direction: route.direction, collapsed: route.collapsed, selected: route.selected, range: route.range, tab: route.tab }),
          { mode: item.state.mode, sort: item.state.sort, scope: item.state.scope, direction: item.state.direction, collapsed: item.state.collapsed, selected: item.state.selected, range: item.state.range, tab: item.state.tab }, name);
      }
    } else assert.fail(`${name} has no reader check`);
  }
  for (const path of Object.keys(map.nodes)) if (!map.nodes[path].children || map.nodes[path].options?.length) assert.ok(paths.has(path) || path.startsWith("analyze/") && !path.startsWith("analyze/query"), `${path} has a link case`);
});

test("an option's requires and conflicts name its node's options and their values", () => {
  for (const node of Object.values(map.nodes)) for (const option of node.options ?? []) {
    const other = name => node.options.find(item => item.name === name);
    for (const need of option.requires ?? []) {
      assert.ok(other(need.option) && need.option !== option.name, `${node.path} ${option.name} requires ${need.option}`);
      for (const value of need.values ?? []) assert.ok(other(need.option).values.some(choice => choice.value === value), `${node.path} ${option.name} requires ${need.option} ${value}`);
    }
    for (const name of option.conflicts ?? []) assert.ok(other(name) && name !== option.name, `${node.path} ${option.name} conflicts with ${name}`);
  }
});

test("the site map leaves out legacy aliases, private and login parameters", () => {
  const text = JSON.stringify(map.nodes);
  for (const name of ["ask_chat", "\"token\"", "analyze_view", "machine_view", "tools_view"]) assert.ok(!text.includes(name), name);
  for (const route of Object.keys(legacyRoutes)) assert.ok(!Object.values(map.nodes).some(node => node.route === route), route);
  for (const topic of ["carbon", "parallelism"]) assert.ok(map.nodes[`analyze/${topic}`].settings, `${topic} says it follows the viewer's settings`);
  assert.deepEqual(plain(map.empty_query), plain(EMPTY_QUERY));
});


test('native query help includes every referenced public type from the pinned release',()=>{
 const {version,types}=map.query_types;
 assert.ok(/^\d+\.\d+\.\d+/.test(version));assert.ok(types.QueryState);assert.ok(types.MetricDisplay);
 for(const name of ['GroupingDefinition','SummaryColumn','GroupKey','RepresentativeRow'])assert.ok(types[name],`Undocumented inherited grouping type ${name}`);
 assert.ok(types.GroupingState.uses.includes('GroupingDefinition'));
 for(const type of Object.values(types)){assert.ok(type.definition);for(const name of type.uses)assert.ok(types[name],`Undocumented query type ${name}`);}
});
