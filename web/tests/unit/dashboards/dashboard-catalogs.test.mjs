// The Tools, Pharos MCP, Agent Time and Skills catalogs: what each offers and
// why, how their older links read, and the engine rules they added (record
// views without units, a minimum counted in calls, maxima kept off the trend,
// timestamp cycles and compound unit keys).
import test from "node:test";
import assert from "node:assert/strict";
import { capabilities, core, execution, fullMatrix, instantiateRecipe, queryRecipes, schemas } from "../helpers/recipes.mjs";
import { relatedCardDashboardURL } from "../../../src/card-analysis-model";
import { usageDashboardReturn, usageWorkspaceURL } from "../../../src/usage-workspace-model";
import { dashboardQueryLinks } from "../../../src/dashboard-query-links";
import { normalizedDashboardChart } from "../../../src/analysis-catalogs";
import { now, toolCalls, toolsRollup, mcpCalls, skillUsages } from "../helpers/record-ledgers.mjs";
import { analysisInspectQuery, analysisNote, analysisQuery, analysisViews, catalogProblems, cycleResult, normalizeAnalysis, readAnalysisLink, switchView, trendAdjustments, trendUnavailable, validAnalysisState, viewOptions, analysisVersion } from "../../../src/dashboard-analysis-model";
import { trendNote, trendQuery, trendSeries } from "../../../src/dashboard-trend-model";
import { tokenAnalysisCatalog } from "../../../src/token-analysis-catalog";
import { toolsAnalysisCatalog as tools } from "../../../src/tools-analysis-catalog";
import { mcpAnalysisCatalog as mcp } from "../../../src/mcp-analysis-catalog";
import { agentTimeAnalysisCatalog as time, skillsAnalysisCatalog as skills } from "../../../src/record-analysis-catalogs";
import { dashboardChartURL } from "../../../src/dashboard-chart-links";
import { toolMetricPresentation } from "../../../src/tools-dashboard-model";
import { withTimeRange } from "../../../src/chart-range";
import { runAnalysisQuery } from "../../../src/analysis-request";
import { answerQuery } from "../../support/query-server.mjs";

const rows = { tools: toolsRollup(), mcp_calls: mcpCalls(), tool_calls: toolCalls(), skill_usages: skillUsages() };
const catalogs = [tools, mcp, time, skills];
const month = catalog => withTimeRange([], catalog.time, new Date(now - 30 * 86400000), new Date(now));
const run = (catalog, query) => core.evaluateMetrics(rows[catalog.dataset], query, schemas[catalog.dataset], { now }).metrics;
const compiles = (catalog, query, label) => assert.deepEqual(core.toQueryRequestV3(query, schemas[catalog.dataset], { ...capabilities, regexSourceFilters: true }, execution, { now, rows: false }).diagnostics, [], label);

test("every view of every measure and breakdown executes over its own dataset", () => {
  for (const catalog of catalogs) {
    assert.deepEqual(catalogProblems(catalog), [], catalog.dataset);
    const views = viewOptions(catalog, { metric: catalog.trend.metric, split: "none" }).filter(([, , reason]) => !reason).map(([view]) => view);
    // The full tier runs every breakdown; the default tier none and the last
    // (a combined or populated one in these catalogs).
    const splits = ["none", ...Object.keys(catalog.breakdowns)].filter((split, index, all) => fullMatrix || index < 2 || index === all.length - 1);
    for (const metric of Object.keys(catalog.measures)) for (const split of splits) for (const view of views) {
      const state = { view, metric, split, unit: view === "scatter" || view === "distribution" && !catalog.measures[metric].value ? undefined : "records", period: "week", rollingPeriods: 3 };
      const label = `${catalog.dataset} ${view} ${metric} by ${split}`;
      assert.ok(validAnalysisState(catalog, state), label);
      const query = view === "trend" ? trendQuery(catalog, state, month(catalog), { now }) : analysisQuery(catalog, state, month(catalog), { now, zone: "America/Denver" });
      for (const result of run(catalog, query.query)) assert.equal(result.error, undefined, label);
      compiles(catalog, query.query, label);
      compiles(catalog, query.supporting, `${label}: supporting`);
    }
  }
});

test("the Tools rollup has no observations to distribute or pair, and says so", () => {
  const options = Object.fromEntries(viewOptions(tools, { metric: "calls", split: "tool_name" }).map(([view, , reason]) => [view, reason]));
  assert.deepEqual(Object.keys(options), analysisViews.map(([view]) => view));
  assert.match(options.distribution, /summaries, with no value per observation/);
  assert.match(options.scatter, /no such unit/);
  assert.match(options.cycle, /no hours or dates/);
  assert.equal(options.compare, undefined);
  for (const view of ["distribution", "scatter", "cycle"]) {
    assert.equal(validAnalysisState(tools, { view, metric: "calls", split: "tool_name" }), false, view);
    assert.equal(normalizeAnalysis(tools, { view, metric: "calls", split: "tool_name" }).view, "compare", view);
  }
  // Pharos requests are observations; a count of them has no value per request.
  assert.equal(normalizeAnalysis(mcp, { view: "distribution", metric: "calls", split: "tool_name" }).metric, "output", "the first measure with a value per request");
  assert.equal(normalizeAnalysis(mcp, { view: "distribution", metric: "time", split: "tool_name" }).metric, "time");
  assert.equal(viewOptions(mcp, { metric: "calls", split: "none" }).find(([view]) => view === "scatter")[2] !== undefined, true, "no unit to pair");
});

test("Tools reads its text days: every query and drill-down leaving the dashboard names whole days", () => {
  const where = month(tools);
  const dated = query => (query.grouping?.source ?? query).where.filter(term => (term.field ?? term.any?.[0]?.field) === "day");
  for (const query of [analysisQuery(tools, { view: "compare", metric: "calls", split: "tool_name" }, where, { now }), trendQuery(tools, { metric: "calls", split: "tool_name", rollingPeriods: 7 }, where, { now })]) {
    for (const executed of [query.query, query.supporting]) {
      assert.ok(dated(executed).length, "days are selected");
      assert.ok(dated(executed).every(term => term.op === "=" || term.op === "matches_regex" || term.any), JSON.stringify(dated(executed)));
    }
  }
  const trend = trendQuery(tools, { metric: "calls", split: "tool_name" }, where, { now });
  assert.ok(trend.range.some(term => term.field === "day" && term.op === ">="), "drawing keeps the bounds");
  const href = dashboardChartURL({ dataset: "tools", where, time: tools.time, range: { from: new Date(now - 3 * 86400000), to: new Date(now - 2 * 86400000), rolling: false }, series: { key: "s:Bash", label: "Bash", color: "", value: "Bash" }, split: "tool_name", translate: terms => tools.executable(terms, now) });
  const query = core.decodeQueryStrict(new URL(href, "http://localhost").searchParams.get("q_tools"));
  assert.ok(query.where.every(term => term.field !== "day" || term.op === "="), JSON.stringify(query.where));
  const day = new Date(now - 3 * 86400000), opened = core.applyQuery(rows.tools, query, schemas.tools).rows;
  assert.ok(opened.length && opened.every(row => row.tool_name === "Bash"));
  assert.ok(opened.some(row => row.day === `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, "0")}-${String(day.getDate()).padStart(2, "0")}`));
});

test("a Tools minimum counts calls, not daily summaries", () => {
  const state = { view: "compare", metric: "failure_rate", split: "tool_name", minSamples: 10 };
  const result = run(tools, analysisQuery(tools, state, [], { now }).query)[0];
  const calls = new Map();
  for (const row of rows.tools) calls.set(row.tool_name, (calls.get(row.tool_name) ?? 0) + row.call_count);
  assert.deepEqual(result.buckets.map(bucket => bucket.keys[0]).sort(), [...calls].filter(([, count]) => count >= 10).map(([name]) => name).sort());
  assert.match(analysisNote(tools, state), /fewer than 10 calls are hidden/);
  // Shares take no minimum; units of other catalogs count units.
  assert.equal(analysisQuery(tools, { ...state, metric: "calls", compareAs: "share" }, [], { now }).query.grouping, undefined);
  assert.match(analysisNote(time, { view: "distribution", unit: "conversation", metric: "calls", split: "repository_name", minSamples: 3 }), /fewer than 3 plotted agent conversations/);
});

test("a trailing window pools a mean's parts: rates and means of calls, never a mean of daily means", () => {
  const day = (offset, extra) => ({ day: `2026-10-0${offset}`, week: "2026-09-28", month: "2026-10", tool_name: "shell", call_count: 1, error_count: 0, timed_count: 1, ...extra });
  const busy = [day(1, { total_duration_ms: 100000, duration_ms: 100000, called_at: "2026-10-01T12:00:00Z" }), ...Array.from({ length: 99 }, (_, index) => day(2, { id: index, total_duration_ms: 1000, duration_ms: 1000, called_at: "2026-10-02T12:00:00Z" }))];
  const where = [{ field: "day", op: ">=", value: new Date(2026, 9, 7).toISOString() }, { field: "day", op: "<", value: new Date(2026, 9, 10).toISOString() }];
  for (const [catalog, range] of [[tools, where], [mcp, where.map(term => ({ ...term, field: "called_at" }))]]) {
    const state = { metric: "mean_time", split: "none", period: "day", rollingPeriods: 7 }, at = new Date(2026, 9, 12).getTime();
    const query = trendQuery(catalog, state, range, { now: at });
    const results = core.evaluateMetrics(busy, { ...query.query, where: [] }, schemas[catalog.dataset], { now: at }).metrics;
    const built = trendSeries(catalog, state, query, results, { now: at });
    const values = built.buckets.map(bucket => bucket.values.total);
    assert.ok(Math.abs(values[0] - 199000 / 100) < 1e-8, `${catalog.dataset}: the window holds 100 calls`);
    assert.equal(values[1], 1000, catalog.dataset);
    assert.equal(values[2], undefined, `${catalog.dataset}: an empty window is a gap`);
  }
});

test("Over time ranks a rate's groups by their calls, and folds the rest into Other", () => {
  const state = { metric: "failure_rate", split: "tool_name", seriesLimit: 2 };
  const query = trendQuery(tools, state, month(tools), { now });
  const built = trendSeries(tools, state, query, run(tools, query.query), { now });
  // Only the drawn periods rank the series.
  const first = new Date(now - 30 * 86400000), from = `${first.getFullYear()}-${String(first.getMonth() + 1).padStart(2, "0")}-${String(first.getDate()).padStart(2, "0")}`;
  const calls = new Map();
  for (const row of rows.tools) if (row.day >= from) calls.set(row.tool_name, (calls.get(row.tool_name) ?? 0) + row.call_count);
  const busiest = [...calls].sort((a, b) => b[1] - a[1]).slice(0, 2).map(([name]) => name);
  assert.deepEqual(built.series.filter(item => item.value).map(item => item.value).sort(), busiest.sort());
  assert.ok(built.series.some(item => item.key === "other"));
});

test("the largest value per group is compared, never drawn over time", () => {
  const compare = { view: "compare", metric: "largest_output", split: "tool_name" };
  assert.match(trendUnavailable(mcp, compare), /largest value per group/);
  assert.equal(switchView(mcp, compare, "trend").view, "compare", "the view stays where it is");
  assert.equal(normalizeAnalysis(mcp, { metric: "largest_output", split: "tool_name" }).metric, "calls");
  assert.match(trendAdjustments(mcp, { metric: "largest_output", split: "tool_name" })[0], /Over time does not draw it; calls is shown instead/);
  const result = run(mcp, analysisQuery(mcp, compare, [], { now }).query)[0];
  for (const bucket of result.buckets) assert.equal(bucket.value, Math.max(...rows.mcp_calls.filter(row => row.tool_name === bucket.keys[0]).map(row => row.estimated_output_tokens ?? 0)));
});

test("older links: numbered metrics read as measures; only dashboards with a legend read a filter as its selection", () => {
  assert.equal(normalizeAnalysis(time, { metric: 0, split: "activity" }).metric, "time");
  assert.equal(normalizeAnalysis(time, { metric: 1, split: "activity" }).metric, "calls");
  assert.equal(normalizeAnalysis(skills, { metric: 1, split: "skill_name" }).metric, "body");
  assert.ok(validAnalysisState(skills, { metric: 0, split: "skill_name", period: "week", rollingPeriods: 0, display: "cumulative", scale: "linear", seriesLimit: 15 }));
  const filter = { field: "activity", op: "=", value: "command:test" };
  const old = readAnalysisLink(time, { metric: 0, split: "activity" }, [filter]);
  assert.deepEqual([old.where, old.state.selection, old.state.v], [[], { values: ["command:test"], excluded: false }, analysisVersion]);
  // Tools and MCP drew no legend: their filters were always populations.
  const tool = { field: "tool_name", op: "=", value: "Bash" };
  for (const [catalog, state] of [[tools, { metric: "failure_rate", split: "tool_name", view: "trend", period: "week", rollingPeriods: 7, display: "interval" }], [mcp, { metric: "mean_output", split: "tool_name", view: "compare", period: "day", rollingPeriods: 0, display: "interval" }]]) {
    assert.ok(validAnalysisState(catalog, state), catalog.dataset);
    assert.deepEqual(readAnalysisLink(catalog, state, [tool]).where, [tool], catalog.dataset);
  }
  assert.equal(normalizeAnalysis(mcp, { metric: "mean_output", split: "tool_name" }).metric, "mean_output", "a mean is now a pooled ratio");
  assert.ok(mcp.measures.mean_output.ratio);
});

test("a timestamp cycle groups natively in the viewer’s zone and counts every complete local day", () => {
  const zone = "America/Denver", state = { view: "cycle", cycle: "weekday", metric: "calls", split: "none", reduce: "median" };
  const where = month(mcp), analysis = analysisQuery(mcp, state, where, { now, zone });
  assert.deepEqual(analysis.clause.dateGrouping, { field: "called_at", interval: "day", timeZone: zone, includeEmpty: true, range: "observed" });
  assert.equal(analysis.supporting.grouping, undefined, "a grouped query cannot group timestamps by date, so the link opens records");
  const from = Date.parse(analysis.query.where.find(term => term.op === ">=").value), to = Date.parse(where.find(term => term.op === "<").value);
  assert.equal(new Intl.DateTimeFormat("en-US", { hour: "numeric", hourCycle: "h23", timeZone: zone }).format(from), "00", "the range starts at a local midnight");
  const folded = cycleResult(mcp, { ...state, reduce: "total" }, run(mcp, analysis.query)[0], { from, to }, zone);
  // Every complete local day counts, idle ones as zero; today is partial.
  const weekday = new Intl.DateTimeFormat("en-US", { weekday: "long", timeZone: zone }), midnight = core.metricDateFloor(now, "day", zone);
  const expected = Object.fromEntries(["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"].map(day => [day, 0]));
  for (const row of rows.mcp_calls) { const at = Date.parse(row.called_at); if (at >= from && at < midnight) expected[weekday.format(at)] += 1; }
  assert.deepEqual(Object.fromEntries(folded.result.buckets.map(bucket => [bucket.keys[0], bucket.value])), expected);
  assert.equal(folded.slots, Math.round((midnight - from) / 86400000));
  assert.match(analysisNote(mcp, state), /complete day in your time zone/);
  // Hours are UTC hours, placed at the viewer's local hour.
  const hours = analysisQuery(mcp, { ...state, cycle: "hour" }, where, { now, zone });
  assert.equal(hours.clause.dateGrouping.timeZone, "UTC");
  const profile = cycleResult(mcp, { ...state, cycle: "hour", reduce: "total" }, run(mcp, hours.query)[0], { from: Date.parse(where[0].value), to }, zone);
  const local = new Intl.DateTimeFormat("en-US", { hour: "numeric", hourCycle: "h23", timeZone: zone });
  const hourly = new Array(24).fill(0);
  for (const row of rows.mcp_calls) if (Date.parse(row.called_at) >= Date.parse(where[0].value) && Date.parse(row.called_at) < to) hourly[Number(local.format(Date.parse(row.called_at))) % 24] += 1;
  assert.deepEqual(profile.result.buckets.map(bucket => bucket.value), hourly);
});

test("a unit may combine fields: one observation per tool in a conversation, or per skill path", () => {
  const pairs = analysisQuery(time, { view: "compare", unit: "tool_conversation", reduce: "count", metric: "calls", split: "tool_name" }, [], { now });
  assert.deepEqual(pairs.query.grouping.keys, ["tool_name", "conversation_id"]);
  const none = analysisQuery(time, { view: "distribution", unit: "tool_conversation", metric: "calls", split: "none" }, [], { now });
  assert.deepEqual(none.query.grouping.keys, ["conversation_id", "tool_name"]);
  const counted = run(time, pairs.query)[0];
  const expected = new Map();
  for (const row of rows.tool_calls) if (row.conversation_id) expected.set(row.tool_name, (expected.get(row.tool_name) ?? new Set()).add(row.conversation_id));
  assert.deepEqual(Object.fromEntries(counted.buckets.map(bucket => [bucket.keys[0], bucket.value])), Object.fromEntries([...expected].map(([name, set]) => [name, set.size])));
  // A conversation's breakdowns are constant within it; its activity is not.
  assert.equal(normalizeAnalysis(time, { view: "compare", unit: "conversation", metric: "calls", split: "activity" }).split, "none");
  const loads = analysisQuery(skills, { view: "distribution", unit: "skill_path", metric: "loads", split: "skill_name" }, [], { now });
  assert.deepEqual(loads.query.grouping.keys, ["skill_name", "conversation_id", "skill_path"]);
  assert.equal(run(skills, loads.query)[0].error, undefined);
});

test("activities read as Agent Time names them, in the trend and in Compare", () => {
  const state = { metric: "time", split: "activity" }, query = trendQuery(time, state, month(time), { now });
  const built = trendSeries(time, state, query, run(time, query.query), { now });
  assert.ok(built.series.some(item => item.value === "command:test" && item.label === "test"), JSON.stringify(built.series.map(item => item.label)));
  assert.equal(tokenAnalysisCatalog.labels, undefined);
});

test("a timestamp cycle runs on /query-v3, which groups calendars", async t => {
  const prior = global.fetch, requests = [];
  global.fetch = async (url, init = {}) => {
    const path = String(url), body = init.body ? JSON.parse(init.body) : undefined;
    requests.push(path);
    return Response.json(answerQuery(path.split("/").at(-1), body, rows.mcp_calls, schemas.mcp_calls, { now }));
  };
  t.after(() => { global.fetch = prior; });
  const cycle = analysisQuery(mcp, { view: "cycle", cycle: "weekday", metric: "calls", split: "none" }, month(mcp), { now, zone: "UTC" });
  const [served] = await runAnalysisQuery("mcp_calls", schemas.mcp_calls, cycle.query);
  assert.deepEqual(requests, ["/api/query/mcp_calls/capabilities", "/api/query/mcp_calls/query-v3"]);
  assert.deepEqual(served.buckets.map(bucket => [bucket.keys[0], bucket.value]), run(mcp, cycle.query)[0].buckets.map(bucket => [bucket.keys[0], bucket.value]));
});

test("every view's note says once what one observation is", () => {
  for (const catalog of [tokenAnalysisCatalog, ...catalogs]) {
    const views = viewOptions(catalog, { metric: catalog.trend.metric, split: "none" }).filter(([view, , reason]) => view !== "trend" && !reason).map(([view]) => view);
    const units = ["records", ...Object.keys(catalog.units)];
    // Every breakdown with a note, and one without.
    const splits = ["none", ...Object.keys(catalog.breakdowns).filter(id => catalog.breakdowns[id].note), Object.keys(catalog.breakdowns)[0]];
    for (const view of views) for (const unit of units) for (const split of splits) for (const cycle of ["hour", "weekday"]) {
      const state = normalizeAnalysis(catalog, { view, unit, split, metric: catalog.trend.metric, cycle });
      const note = analysisNote(catalog, state);
      assert.equal(note.match(/One observation is/g)?.length, 1, `${catalog.dataset} ${state.view} ${state.unit} ${state.split}: ${note}`);
      if (state.split !== "none" && catalog.breakdowns[state.split].note && state.view !== "cycle") assert.ok(note.includes(catalog.breakdowns[state.split].note));
    }
  }
});

test("Compare names two values that share a label, and each opens its own records", () => {
  const result = { id: "analysis", buckets: [{ keys: ["command:Edit"], value: 3, count: 3 }, { keys: ["tool:Edit"], value: 5, count: 5 }, { keys: ["tool:Read"], value: 1, count: 1 }] };
  const shown = toolMetricPresentation(result, ["activity"], time.labels);
  assert.deepEqual(shown.metric.buckets.map(bucket => bucket.keys[0]), ["Edit (command:Edit)", "Edit (tool:Edit)", "Read"]);
  for (const [index, bucket] of shown.metric.buckets.entries()) assert.equal(shown.sourceBucket(bucket), result.buckets[index]);
});

test("a Tools minimum opens a bar's daily summaries, and says it cannot count what it hides", () => {
  const state = { view: "compare", metric: "failure_rate", split: "tool_name", minSamples: 10 };
  const analysis = analysisQuery(tools, state, month(tools), { now }), result = run(tools, analysis.query)[0];
  const bucket = result.buckets.find(item => item.keys[0] === "Bash");
  const opened = analysisInspectQuery(analysis, bucket, analysis.clause, schemas.tools, "compare");
  assert.equal(opened.grouping, undefined, "records, not the grouped row");
  const summaries = core.applyQuery(rows.tools, opened, schemas.tools, { now }).rows;
  assert.ok(summaries.length > 1 && summaries.every(row => row.tool_name === "Bash"));
  assert.match(analysisNote(tools, state), /hidden; how many is not shown/);
});

test("a scatter says when one measure's population narrows the other", () => {
  assert.match(analysisNote(skills, { view: "scatter", unit: "conversation", metric: "records", metricY: "body", split: "none" }), /Evidence records counts only retained body volume's records here/);
  assert.doesNotMatch(analysisNote(skills, { view: "scatter", unit: "conversation", metric: "records", metricY: "conversations", split: "none" }), /counts only/);
});

// No Tokens card filters its own breakdown's field (effort-cost filters
// priced usage with a conversation, by model + effort); the Skills cards do.
test("an unversioned saved card link opens its related dashboard with the card's filters as the population", () => {
  for (const [id, catalog, old] of [["skill-evidence", skills, { period: "day", rollingPeriods: 0, display: "interval", scale: "linear", seriesLimit: 15, metric: 0, split: "skill_name" }], ["skill-reach", skills, undefined]]) {
    const recipe = queryRecipes.find(item => item.id === id), url = new URL(instantiateRecipe(recipe, 7, new Date(now)).url, "http://localhost");
    // As saved before charts carried v: 2.
    const chart = old ?? (({ v: _, ...rest }) => rest)(JSON.parse(url.searchParams.get("analyze_chart")));
    url.searchParams.set("analyze_chart", JSON.stringify(chart));
    const filters = core.decodeQueryStrict(url.searchParams.get("analyze_filters")).where;
    const field = catalog.breakdowns[normalizeAnalysis(catalog, chart).split].fields[0];
    assert.ok(filters.some(term => term.field === field), `${id}: the card filters its breakdown's own field`);
    const related = new URL(relatedCardDashboardURL(url.searchParams), "http://localhost").searchParams;
    assert.equal(related.has("card_query"), false);
    const read = readAnalysisLink(catalog, JSON.parse(related.get("analyze_chart")), core.decodeQueryStrict(related.get("analyze_filters")).where);
    assert.deepEqual([read.where, read.state.selection], [filters, undefined], `${id}: the card's filters stay its population`);
    // Leaving the card for a query view marks it current too.
    const query = new URL(usageWorkspaceURL({ mode: "query", dataset: catalog.dataset === "usage" ? "usage" : catalog.dataset }, url.searchParams), "http://localhost").searchParams;
    if (query.has("analyze_chart") && !query.has("card_query")) assert.equal(JSON.parse(query.get("analyze_chart")).v, analysisVersion, id);
  }
});

test("Back to dashboard from a Tools query: an edited date predicate becomes the range, never intersected with the old one", () => {
  const where = month(tools), filters = core.encodeQuery({ ...core.EMPTY_QUERY, where });
  const dashboard = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "tools", analyze_data: "tools", analyze_chart: JSON.stringify({ v: analysisVersion, view: "compare", metric: "calls", split: "tool_name" }), analyze_filters: filters });
  const supporting = analysisQuery(tools, { view: "compare", metric: "calls", split: "tool_name" }, where, { now }).supporting;
  const opened = new URL(dashboardQueryLinks("tools", supporting, schemas.tools, dashboard)[0].href, "http://localhost").searchParams;
  const back = params => { const result = usageDashboardReturn(params); return { label: result.label, where: core.decodeQueryStrict(new URL(result.href, "http://localhost").searchParams.get("analyze_filters")).where }; };
  assert.deepEqual(back(opened), { label: "Back to dashboard", where });
  const query = core.decodeQueryStrict(opened.get("q_tools")), source = query.grouping.source;
  const edit = terms => { const next = new URLSearchParams(opened); next.set("q_tools", core.encodeQuery({ ...query, grouping: { ...query.grouping, source: { ...source, where: terms } } })); return back(next); };
  // Deleting the translated days: no range at all, said in the link.
  const dayless = edit(source.where.filter(term => (term.field ?? term.any?.[0]?.field) !== "day"));
  assert.deepEqual([dayless.label, dayless.where.filter(term => (term.field ?? term.any?.[0]?.field) === "day")], ["Back to dashboard (dates from this query)", []]);
  // Choosing one earlier day: that day is the range.
  const earlier = { field: "day", op: "=", value: "2026-08-01" };
  assert.deepEqual(edit([...source.where.filter(term => (term.field ?? term.any?.[0]?.field) !== "day"), earlier]).where.filter(term => term.field === "day"), [earlier]);
  // Another edit keeps the dashboard's range.
  const other = { field: "provider", op: "=", value: "codex" };
  assert.deepEqual(edit([...source.where, other]), { label: "Back to dashboard", where: [...where, other] });
});

test("collected cards compare dashboard charts as drawn: defaults filled, keys sorted at every depth", () => {
  const key = (topic, chart) => normalizedDashboardChart(topic, JSON.stringify(chart));
  // An old Skills card chart, and the versioned one for the same view.
  assert.equal(key("skills", { period: "day", rollingPeriods: 0, display: "interval", scale: "linear", seriesLimit: 15, metric: 0, split: "skill_name" }), key("skills", { v: analysisVersion, view: "trend", metric: "records", split: "skill_name", period: "day" }));
  assert.equal(key("tokens", { metric: "tokens", split: "provider", selection: { values: [], excluded: false } }), key("tokens", { split: "provider", metric: "tokens" }));
  // A selection is part of the view, compared in full.
  const selected = values => key("tokens", { metric: "tokens", split: "provider", selection: { values, excluded: false } });
  assert.notEqual(selected(["codex"]), selected(["claude"]));
  assert.equal(key("tokens", { metric: "tokens", split: "provider", selection: { excluded: false, values: ["codex"] } }), selected(["codex"]));
  assert.equal(normalizedDashboardChart("retired", '{"b":1,"a":2}'), '{"b":1,"a":2}', "other dashboards compare as written");
});

// ----- 2c engine items -------------------------------------------------------

const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
test("one day boundary: Over time places timestamps in the viewer's periods, as the cycle does, and offers hours", () => {
  // Days another Mac recorded in its own zone: the trend no longer reads them.
  const moved = rows.mcp_calls.map(row => ({ ...row, day: "2001-01-01", week: "2001-01-01", month: "2001-01" }));
  const where = month(mcp), state = { metric: "calls", split: "none" };
  const query = trendQuery(mcp, state, where, { now, zone: localZone });
  assert.deepEqual(query.query.aggregations[0].dateGrouping, { field: "called_at", interval: "day", timeZone: localZone, includeEmpty: false, range: "query" });
  const built = trendSeries(mcp, state, query, core.evaluateMetrics(moved, query.query, schemas.mcp_calls, { now }).metrics, { now });
  const local = date => { const at = new Date(date); return `${at.getFullYear()}-${String(at.getMonth() + 1).padStart(2, "0")}-${String(at.getDate()).padStart(2, "0")}`; };
  const expected = {};
  for (const row of moved) if (Date.parse(row.called_at) >= Date.parse(where[0].value)) expected[local(row.called_at)] = (expected[local(row.called_at)] ?? 0) + 1;
  assert.deepEqual(Object.fromEntries(built.buckets.filter(bucket => bucket.values.total).map(bucket => [bucket.key, bucket.values.total])), expected);
  // The weekday cycle's days are the same days.
  const cycle = analysisQuery(mcp, { view: "cycle", cycle: "weekday", metric: "calls", split: "none" }, where, { now, zone: localZone });
  for (const bucket of core.evaluateMetrics(moved, cycle.query, schemas.mcp_calls, { now }).metrics[0].buckets) if (bucket.value) assert.equal(bucket.value, expected[local(bucket.keys[0])], String(bucket.keys[0]));
  // Hours: every catalog with a timestamp offers them, as UTC clock hours.
  for (const catalog of [mcp, time, skills]) {
    assert.equal(trendAdjustments(catalog, { metric: catalog.trend.metric, split: "none", period: "hour" }).length, 0, catalog.dataset);
    const hours = trendQuery(catalog, { metric: catalog.trend.metric, split: "none", period: "hour" }, where.map(term => ({ ...term, field: catalog.time.first })), { now, zone: localZone });
    assert.equal(hours.query.aggregations[0].dateGrouping.timeZone, "UTC");
    const drawn = trendSeries(catalog, { metric: catalog.trend.metric, split: "none", period: "hour" }, hours, run(catalog, hours.query), { now });
    assert.equal(drawn.error, undefined);
    assert.ok(drawn.buckets.length > 24 * 29 && drawn.buckets.some(bucket => bucket.values.total), catalog.dataset);
  }
  assert.match(trendNote(mcp, state), /days in your time zone, as in the hour and weekday cycle/);
  // A grouping stage cannot group a timestamp by calendar period, so the
  // supporting query opens the records drawn.
  assert.equal(query.supporting.aggregations, undefined);
  // Usage keeps its recorded days.
  assert.equal(tokenAnalysisCatalog.timestamp, undefined);
  assert.equal(trendQuery(tokenAnalysisCatalog, { metric: "tokens", split: "none" }, [], { now }).query.aggregations[0].dateGrouping, undefined);
});

test("a ratio part may state a condition: the empty-search rate pools zero-result searches per period and group", () => {
  const where = month(mcp), state = { metric: "empty_rate", split: "tool_name", rollingPeriods: 7 };
  const population = rows.mcp_calls.filter(row => row.status === "ok" && row.result_count >= 0);
  // Compare: each tool's pooled rate.
  const compare = analysisQuery(mcp, { ...state, view: "compare" }, where, { now }), result = run(mcp, compare.query)[0];
  const rate = calls => calls.filter(row => row.result_count === 0).length / calls.length;
  for (const bucket of result.buckets) assert.equal(bucket.value, rate(population.filter(row => row.tool_name === bucket.keys[0] && Date.parse(row.called_at) >= Date.parse(where[0].value))), String(bucket.keys[0]));
  assert.ok(result.buckets.some(bucket => bucket.value > 0 && bucket.value < 1));
  // Over time: each window pools its parts, never averaging daily rates.
  const query = trendQuery(mcp, { ...state, split: "none" }, where, { now });
  assert.equal(query.query.aggregations[0].expression, "SUM(IF([result_count]=0,1,0))");
  const built = trendSeries(mcp, { ...state, split: "none" }, query, run(mcp, query.query), { now });
  const last = built.buckets.at(-1), end = last.date.getTime() + 86400000, start = new Date(last.date); start.setDate(start.getDate() - 6);
  assert.equal(last.values.total, rate(population.filter(row => Date.parse(row.called_at) >= start.getTime() && Date.parse(row.called_at) < end)));
  assert.match(trendNote(mcp, { ...state, split: "none" }), /requests with no results divided by successful requests with a recorded result count/);
  assert.deepEqual(catalogProblems({ ...mcp, measures: { ...mcp.measures, bad: { ...mcp.measures.empty_rate, ratio: [{ op: "sum", expression: "1" }, "call_count"] } } }), ["bad has an expression part without a label."]);
});

test("a catalog's request: its parameters reach every request, and its population joins every query but never the dashboard's filters", async t => {
  const population = [{ field: "status", op: "=", value: "ok" }];
  const catalog = { ...mcp, request: { params: { carbon_settings: '{"grid":"custom"}' }, where: population, note: "Built under your settings." } };
  const where = month(catalog);
  const compare = analysisQuery(catalog, { view: "compare", metric: "calls", split: "tool_name" }, where, { now });
  const trend = trendQuery(catalog, { metric: "calls", split: "tool_name" }, where, { now });
  for (const query of [compare.query, compare.supporting, trend.query, trend.supporting]) assert.ok((query.grouping?.source ?? query).where.some(term => term.field === "status"), JSON.stringify(query.where));
  assert.ok(trend.records.some(term => term.field === "status"), "drill-downs open the same population");
  assert.match(analysisNote(catalog, { view: "compare", metric: "calls", split: "tool_name" }), /Built under your settings/);
  assert.match(trendNote(catalog, { metric: "calls", split: "tool_name" }), /Built under your settings/);
  // Back to dashboard leaves the population out of the dashboard's filters.
  const dashboard = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "mcp", analyze_data: "mcp_calls", analyze_filters: core.encodeQuery({ ...core.EMPTY_QUERY, where }) });
  const opened = new URL(dashboardQueryLinks("mcp_calls", compare.supporting, schemas.mcp_calls, dashboard)[0].href, "http://localhost").searchParams;
  assert.deepEqual(core.decodeQueryStrict(new URL(usageDashboardReturn(opened).href, "http://localhost").searchParams.get("analyze_filters")).where, where);
  // Parameters ride on /metrics and /query-v3 alike.
  const prior = global.fetch, requests = [];
  global.fetch = async (url, init = {}) => {
    const path = String(url), body = init.body ? JSON.parse(init.body) : undefined;
    requests.push(path);
    return Response.json(answerQuery(path.split("?")[0].split("/").at(-1), body, rows.mcp_calls, schemas.mcp_calls, { now }));
  };
  t.after(() => { global.fetch = prior; });
  await runAnalysisQuery("mcp_calls", schemas.mcp_calls, compare.query, { params: catalog.request.params });
  await runAnalysisQuery("mcp_calls", schemas.mcp_calls, trend.query, { params: catalog.request.params });
  const encoded = new URLSearchParams(catalog.request.params).toString();
  assert.deepEqual(requests, [`/api/query/mcp_calls/metrics?${encoded}`, "/api/query/mcp_calls/capabilities", `/api/query/mcp_calls/query-v3?${encoded}`]);
});
