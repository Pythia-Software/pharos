import test from 'node:test';
import assert from 'node:assert/strict';
import { EMPTY_QUERY, encodeQuery } from '@pythia-software/query-table-core';
import { DASHBOARD_CARD_ACTION, dashboardCardLabels, dashboardCardMetadata, dashboardCardURL, dashboardCardSource, pageCardURL, readDashboardCard, collectDashboardCard, dashboardCardCollected, dashboardCardWindow, dashboardCardPresentation, dashboardCardTargets, dashboardCardDescription, dashboardCardGroups } from '../../../src/dashboard-card-model';
import { allAnalysisViews, validAnalysisState } from '../../../src/dashboard-analysis-model';
import { habitsCardTargets, habitsCardTitle, readHabitsCardView } from '../../../src/habits-card';
import { thinkingTopics, thinkingViews, thinkingBreakdowns } from '../../../src/thinking-model';
import { fittedCoverHeight } from '../../../src/card-loading-model';
import { loadDashboardCardPreview } from '../../../src/dashboard-card-preview';
import { analysisCatalogs } from '../../../src/analysis-catalogs';
import { dashboardDefaults } from '../../../src/dashboard-defaults';
import { readBookmarks, updateBookmark, moveBookmark, removeBookmark } from '../../../src/bookmarks';
import { normalizeAnalysis, analysisQuery, analysisResult, analysisLabels, selectGroups, cycleResult, switchView } from '../../../src/dashboard-analysis-model';
import { trendQuery, trendSeries } from '../../../src/dashboard-trend-model';
import { optionLabels } from '../../../src/option-labels';
import { runAnalysisQuery } from '../../../src/analysis-request';
import { compactionColumns, orderedBoxes, thinkingCardRecipe } from '../../../src/thinking-card';
import { initialThinking } from '../../../src/thinking-model';
import { schemas } from '../helpers/recipes.mjs';
import { fixture } from '../helpers/parity.mjs';
import { now, usageLedger } from '../helpers/usage-ledger.mjs';

const catalog = analysisCatalogs.tokens;
const where = [{field:'last_usage_at',op:'>=',value:'-7d'},{field:'first_usage_at',op:'<',value:'+0s'}, {field:'provider',op:'=',value:'claude'}];
const makeURL = (state, filters=where, address='/analyze?analyze_mode=dashboards&analyze_topic=tokens') => dashboardCardURL(address, catalog, state, filters);

test('dashboard cards retain the complete chart and relative population through collection, editing, ordering and removal', t => {
  fixture(t);
  const state = {...dashboardDefaults.tokens, metric:'tokens', split:'provider', rollingPeriods:3, rollingOp:'sum', selection:{values:['claude'],excluded:false}, norm:true};
  const url = makeURL(state,where,'/analyze?analyze_topic=tokens&overlay=history&highlight=page&chart_pins=%5B%5D');
  const restored = readDashboardCard(url);
  assert.deepEqual(restored.where,where);
  assert.deepEqual(restored.state,normalizeAnalysis(catalog,state));
  for (const key of ['overlay','highlight','chart_pins']) assert.equal(restored.params.has(key),false);
  assert.equal(dashboardCardWindow(url),'Past 7 days');
  assert.equal(dashboardCardCollected(url),false);
  const card = collectDashboardCard(url,' My tokens ',' My description ');
  assert.equal(dashboardCardCollected(url),true,'Ask offers each chart address once');
  const second = collectDashboardCard(makeURL({...state,split:'repository_name'}),'Repositories','');
  assert.equal(readBookmarks().length,2);
  assert.equal(card.title,'My tokens');
  const edited = updateBookmark(card.id,'Renamed','Edited description');
  assert.equal(edited.url,url);
  assert.equal(edited.dashboard,true);
  moveBookmark(second.id,card.id);
  assert.equal(readBookmarks()[0].id,second.id);
  removeBookmark(card.id);
  assert.deepEqual(readBookmarks().map(item=>item.id),[second.id]);
  assert.equal(dashboardCardCollected(url),false,'removing the card offers it again');
  assert.throws(()=>collectDashboardCard(url,'   ',''),/title/);
});

test('dashboard cards keep a chosen call to action and category, and store neither when it matches the default', t => {
  fixture(t);
  const url = makeURL({...dashboardDefaults.tokens, metric:'tokens'});
  assert.equal(dashboardCardPresentation(url).category,'usage');
  const plain = collectDashboardCard(url,'Tokens','','chart',{action:` ${DASHBOARD_CARD_ACTION} `,category:'usage'});
  assert.equal(Object.hasOwn(plain,'action'),false);
  assert.equal(Object.hasOwn(plain,'category'),false);
  const labelled = collectDashboardCard(url,'Tokens','','chart',{action:' See where tokens go ',category:'cost'});
  assert.equal(labelled.action,'See where tokens go');
  assert.equal(labelled.category,'cost');
  const kept = updateBookmark(labelled.id,'Renamed','');
  assert.deepEqual([kept.action,kept.category],['See where tokens go','cost'],'editing without labels keeps them');
  const reset = updateBookmark(labelled.id,'Renamed','',undefined,undefined,dashboardCardLabels(url,'chart',{action:'',category:'usage'}));
  assert.equal(Object.hasOwn(reset,'action'),false);
  assert.equal(Object.hasOwn(reset,'category'),false);
  assert.throws(()=>collectDashboardCard(url,'Tokens','','chart',{action:'x'.repeat(61)}),/call to action/);
  assert.throws(()=>collectDashboardCard(url,'Tokens','','chart',{category:'nonsense'}),/category/);
});

test('dashboard cards re-resolve Today in the pinned zone and preserve fixed and unbounded ranges', t => {
  fixture(t);
  const state = {...dashboardDefaults.tokens, metric:'tokens'};
  const url = makeURL(state,where,'/analyze?analyze_topic=tokens&analyze_window=today&analyze_timezone=America%2FDenver');
  const first = readDashboardCard(url,new Date('2026-10-16T18:00:00Z'));
  const next = readDashboardCard(url,new Date('2026-10-17T18:00:00Z'));
  assert.notDeepEqual(first.where,next.where);
  assert.equal(next.where.find(term=>term.field==='provider').value,'claude');
  assert.equal(dashboardCardWindow(url),'Today');
  assert.equal(dashboardCardWindow(makeURL(state,[])),'All time');
  const fixed = [{field:'last_usage_at',op:'>=',value:'2026-10-01T00:00:00Z'}];
  assert.deepEqual(readDashboardCard(makeURL(state,fixed)).where,fixed);
  assert.equal(dashboardCardWindow(makeURL(state,fixed)),'Saved range');
});

test('broken dashboard definitions fail without broadening their filters', t => {
  fixture(t);
  for (const change of [{analyze_filters:'broken'},{analyze_chart:'{"metric":"missing"}'},{analyze_topic:'missing'},{analyze_timezone:'bad/zone',analyze_window:'today'}]) {
    const url = new URL(makeURL(dashboardDefaults.tokens),'http://parity.test');
    for (const [key,value] of Object.entries(change)) url.searchParams.set(key,value);
    assert.throws(()=>readDashboardCard(url.href));
  }
  assert.throws(()=>readDashboardCard('https://elsewhere.test/analyze?analyze_topic=tokens'));
});

test('every dashboard topic retains its requested chart and dataset in a saved URL', t => {
  fixture(t);
  for (const [topic,state] of Object.entries(dashboardDefaults)) {
    const catalog = analysisCatalogs[topic];
    const url = dashboardCardURL(`/analyze?analyze_topic=${topic}`,catalog,state,[]);
    const restored = readDashboardCard(url);
    assert.equal(restored.topic,topic);
    assert.equal(restored.dataset,catalog.dataset);
    assert.deepEqual(restored.state,normalizeAnalysis(catalog,state));
  }
});

test('every dashboard creation target supplies bounded default copy from its own controls and filters', t => {
  fixture(t);
  const check = (url, schema) => {
    for (const target of dashboardCardTargets(url, schema)) {
      const copy = dashboardCardMetadata(url, target.id, schema);
      assert.equal(target.title, copy.title, `${url}: ${target.id}`);
      assert.ok(copy.title.trim() && copy.title.length <= 200);
      assert.ok(copy.description.trim() && copy.description.length <= 500);
      assert.doesNotMatch(copy.title, /undefined|\[object Object\]|Days: you|Your pace/);
    }
  };
  // Discover catalogs and vocabulary rather than keeping a second list of pages.
  // Future measures and views therefore have to produce usable defaults too.
  for (const [topic, defaults] of Object.entries(dashboardDefaults)) {
    const catalog = analysisCatalogs[topic], schema = schemas[catalog.dataset];
    for (const metric of Object.keys(catalog.measures)) for (const [view] of allAnalysisViews) for (const split of [defaults.split, ...Object.keys(catalog.components ?? {})]) {
      const state = normalizeAnalysis(catalog, { ...defaults, metric, view, split });
      if (!validAnalysisState(catalog, state)) continue;
      check(dashboardCardURL(`/analyze?analyze_topic=${topic}`, catalog, state, []), schema);
    }
    const filters = [{ field: 'provider', op: '=', value: 'codex' }];
    const url = dashboardCardURL(`/analyze?analyze_topic=${topic}`, catalog, defaults, filters);
    assert.match(dashboardCardMetadata(url, 'chart', schema).title, /codex/i);
    assert.match(dashboardCardMetadata(url, 'chart', schema).description, /codex/i);
  }
  for (const topic of thinkingTopics) for (const [view] of thinkingViews) for (const [groupBy] of thinkingBreakdowns) {
    const url = pageCardURL('/analyze?' + new URLSearchParams({ analyze_topic: topic, thinking: JSON.stringify({ view, groupBy }),
      analyze_filters: encodeQuery({ ...EMPTY_QUERY, where: [{ field: 'harness', op: '=', value: 'codex' }] }) }));
    check(url, schemas.model_requests);
    for (const target of dashboardCardTargets(url)) assert.match(target.title, /codex/);
  }
});

test('catalog default copy follows chart semantics, selections, time windows and predicate grammar', t => {
  fixture(t);
  const copy = (state, filters = []) => dashboardCardMetadata(makeURL({ ...dashboardDefaults.tokens, metric: 'tokens', split: 'provider', ...state }, filters), 'chart', schemas.usage);
  const trend = copy({ rollingPeriods: 7, period: 'day', completePeriods: true, nonzero: true, norm: true, normPeriods: 10 });
  assert.match(trend.title, /7-day average.*[Tt]okens per day by provider/);
  assert.match(trend.description, /Whole periods only/);
  assert.match(trend.description, /Zero values omitted/);
  assert.match(trend.description, /mean of 10 earlier periods/);
  assert.match(copy({ display: 'cumulative' }).title, /Cumulative/);
  assert.match(copy({ share: true }).title, /Share of/);
  assert.match(copy({ view: 'compare', unit: 'conversation', reduce: 'median' }).title, /Median per conversation/);
  assert.match(copy({ view: 'compare', unit: 'conversation', reduce: 'count' }).title, /Number of conversations/);
  assert.match(copy({ view: 'compare', compareAs: 'share' }).title, /Share of/);
  assert.match(copy({ view: 'distribution', unit: 'conversation', minSamples: 5 }).description, /Minimum 5 conversations/);
  assert.match(copy({ view: 'distribution', unit: 'conversation' }).title, /distribution per conversation/);
  const scatter = copy({ view: 'scatter', unit: 'conversation', metricY: 'cost' });
  assert.match(scatter.title, /vs.*cost.*per conversation/i);
  assert.match(copy({ view: 'cycle', cycle: 'hour', reduce: 'mean' }).title, /Mean.*hour of day/);
  assert.match(copy({ selection: { values: ['codex'], excluded: true } }).title, /Exclude provider: codex/);
  for (const excluded of [false, true]) {
    const selected = { ...dashboardDefaults.tokens, metric: 'tokens', split: 'provider', selection: { values: ['codex'], excluded } };
    for (const cycle of ['hour', 'weekday']) {
      const switched = switchView(catalog, { ...selected, cycle }, 'cycle');
      assert.deepEqual(switched.selection, selected.selection, 'switching views retains the dormant selection');
      assert.equal(analysisQuery(catalog, switched, []).selection, undefined, 'cycles ignore the retained selection');
      assert.deepEqual(copy(switched), copy({ ...switched, selection: undefined }), 'cycle titles and context describe the unrestricted chart');
    }
    for (const view of ['trend', 'compare', 'distribution', 'scatter']) {
      const ungrouped = normalizeAnalysis(catalog, { ...selected, view, split: 'none' });
      assert.deepEqual(copy(ungrouped), copy({ ...ungrouped, selection: undefined }), `${view} without a breakdown ignores the retained selection`);
      const grouped = switchView(catalog, selected, view);
      assert.match(copy(grouped).title, excluded ? /Exclude provider: codex/ : /Only provider: codex/, `${view} still names an active selection`);
    }
  }
  const part = catalog.components.type.parts[0].key;
  assert.match(copy({ split: 'type', selection: { values: [part], excluded: false } }).title, new RegExp(`Only series: ${catalog.components.type.parts[0].label}`), 'component trends still name their selected parts');
  const predicates = [{ any: [{ field: 'provider', op: '=', value: 'codex' }, { field: 'provider', op: '=', value: 'claude' }] },
    { field: 'repository_name', op: 'contains', value: 'pharos', negated: true }];
  const filtered = copy({}, predicates);
  assert.match(filtered.description, /Codex or Claude/);
  assert.match(filtered.description, /not \(Repository contains pharos\)/);
  assert.notEqual(copy({}).title, filtered.title);
  const today = makeURL(dashboardDefaults.tokens, where, '/analyze?analyze_topic=tokens&analyze_window=today');
  assert.match(dashboardCardDescription(today), /Today/);
  assert.doesNotMatch(dashboardCardDescription(today), /\d{4}-\d{2}-\d{2}T/);
  const long = copy({}, [{ field: 'repository_name', op: '=', value: 'long repository '.repeat(100) }]);
  assert.ok(long.title.length <= 200 && long.description.length <= 500);
  assert.match(long.description, /…$/);
  const groups = makeURL(dashboardDefaults.tokens, []);
  assert.match(dashboardCardDescription(groups, 'chart', schemas.usage, 6), /Show up to 6 groups/);
});

test('Habits defaults describe each actor and range, independently of unrelated catalog filters', t => {
  fixture(t);
  const base = readHabitsCardView(new URLSearchParams());
  const url = view => dashboardCardURL('/analyze?' + new URLSearchParams({ analyze_topic: 'habits', ...view }), analysisCatalogs.habits, dashboardDefaults.habits,
    [{ field: 'repository_name', op: '=', value: 'catalog-only' }]);
  for (const target of habitsCardTargets) {
    const copy = dashboardCardMetadata(url({}), target);
    assert.equal(copy.title, habitsCardTitle(target, base));
    assert.doesNotMatch(copy.description, /catalog-only/);
  }
  assert.match(dashboardCardMetadata(url({}), 'habits-calendar').title, /Your active days/);
  assert.match(dashboardCardMetadata(url({ habits_calendar: 'agents', habits_weekends: 'hide' }), 'habits-calendar').title, /agents you started.*weekdays only/);
  assert.match(dashboardCardDescription(url({ habits_weeks: '53' }), 'habits-calendar'), /17 weeks.*all 53 weeks/);
  assert.match(dashboardCardDescription(url({ habits_punchcard: 'difference', habits_punchcard_days: '30' }), 'habits-punchcard'), /Past 30 days.*Agent time minus your engaged time/);
  assert.match(dashboardCardDescription(url({ habits_shape_days: '7' }), 'habits-days'), /last 7 days/);
  assert.match(dashboardCardMetadata(url({}), 'habits-pace').title, /work and rest/);
});

test('bespoke card context follows saved assumptions and chart controls', t => {
  fixture(t);
  const carbon = settings => dashboardCardURL('/analyze?' + new URLSearchParams({ analyze_topic: 'carbon', analyze_carbon: JSON.stringify(settings) }), analysisCatalogs.carbon, dashboardDefaults.carbon, []);
  const assumptions = dashboardCardDescription(carbon({ scenario: 'high', pueScenario: 'low', grid: 'custom', customGrid: 200, inputSensitivity: 'third' }));
  assert.match(assumptions, /Energy estimate: high/);
  assert.match(assumptions, /Facility overhead: low/);
  assert.match(assumptions, /Grid: 200 g CO₂e\/kWh/);
  assert.match(assumptions, /Watershed input sensitivity/);
  const parallel = settings => dashboardCardURL('/analyze?' + new URLSearchParams({ analyze_topic: 'parallelism', analyze_parallelism: JSON.stringify(settings) }), analysisCatalogs.parallelism, dashboardDefaults.parallelism, []);
  const ribbonURL = parallel({ counting: 'agent', mode: 'user', gap: 15, repository: 'pharos', range: 'day', day: '2026-10-01' });
  const ribbon = dashboardCardMetadata(ribbonURL, 'ribbon');
  assert.match(ribbon.title, /Concurrent sessions.*pharos/);
  assert.match(ribbon.description, /2026-10-01.*gaps up to 15 minutes/);
  assert.match(dashboardCardDescription(ribbonURL), /Count top-level sessions.*15 minutes/);
  const workspace = new URL(parallel({ counting: 'workspace' }), location.origin);
  workspace.searchParams.set('analyze_chart', JSON.stringify({ ...dashboardDefaults.parallelism, split: 'provider' }));
  assert.doesNotMatch(dashboardCardMetadata(workspace.href).title, /provider/i, 'counting controls normalize unavailable breakdowns as the chart does');
  assert.match(dashboardCardDescription(parallel({ mode: 'model' }), 'ribbon'), /Model activity intervals/);
  assert.doesNotMatch(dashboardCardDescription(parallel({ mode: 'model' }), 'ribbon'), /gaps/);
  const thinking = controls => pageCardURL('/analyze?' + new URLSearchParams({ analyze_topic: 'thinking-detail', thinking: JSON.stringify(controls) }));
  const time = dashboardCardMetadata(thinking({ view: 'time', period: 'hours', hours: 6, normalization: 'count', range: '7d', model: 'gpt-5', effort: 'high' }), 'thinking-distributions');
  assert.match(time.title, /gpt-5.*high effort/);
  assert.match(time.description, /Past 7 days.*Periods: 6 hours/);
  assert.match(time.description, /shaded by its requests/);
  assert.match(dashboardCardDescription(thinking({ view: 'heatmap', normalization: 'share' }), 'thinking-distributions'), /share of the group's waits/);
  assert.match(dashboardCardDescription(thinking({ view: 'calendar', calendarRows: 'week', calendarMetric: 'ratio' }), 'thinking-distributions'), /Rows: week.*workload estimate/);
});

test('saved dashboard trend previews match smoothing, cumulative totals, shares and legend selections', async t => {
  fixture(t);
  const local = usageLedger();
  for (const options of [{rollingPeriods:3,rollingOp:'sum',norm:true},{display:'cumulative'},{share:true},{metric:'cache_read_share'}, {selection:{values:['claude'],excluded:true}}]) {
    const url = makeURL({...dashboardDefaults.tokens,metric:'tokens',split:'provider',scale:'linear',...options},[]);
    const {state,where} = readDashboardCard(url,new Date(now));
    const query = trendQuery(catalog,state,where,{now});
    const labels = optionLabels(schemas.usage.fields);
    const expected = trendSeries(catalog,state,query,await runAnalysisQuery('usage',schemas.usage,query.query,{local,now}),{now,label:(field,value)=>labels.get(field)?.get(String(value)) ?? String(value)});
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    assert.deepEqual(preview.buckets,expected.buckets);
    assert.deepEqual(preview.series,expected.series.filter(item=>!expected.selected.length || expected.selected.includes(item.key)!==expected.excluded));
    assert.equal(preview.dashboardTrend.prepared,true);
    assert.equal(preview.dashboardTrend.reference,expected.norm);
  }
});

test('saved comparison, distribution and scatter previews use the dashboard populations and results', async t => {
  fixture(t);
  const local = usageLedger();
  for (const view of ['compare','distribution','scatter']) {
    const url = makeURL({metric:'tokens',metricY:'cost',split:'provider',unit:'conversation',view,minSamples:0},[]);
    const {state,where} = readDashboardCard(url,new Date(now));
    const analysis = analysisQuery(catalog,state,where,{now});
    const expected = selectGroups((await runAnalysisQuery('usage',schemas.usage,analysis.query,{local,now}))[0],analysis.selection);
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    assert.deepEqual(preview.metrics[0].clause,analysis.clause);
    assert.deepEqual(preview.metrics[0].result,expected);
  }
});

// A dashboard address has no authored cover; Ask and Home fit it to the chart.
test('dashboard card covers fit the marks they draw, loading and loaded', async t => {
  fixture(t);
  const local = usageLedger();
  const views = {
    value: {view:'compare',split:'none'}, bars: {view:'compare',split:'provider'}, share: {view:'compare',compareAs:'share',split:'provider'},
    distribution: {view:'distribution',split:'provider',unit:'conversation'}, scatter: {view:'scatter',metricY:'cost',split:'provider',unit:'conversation'},
    hours: {view:'cycle',cycle:'hour',reduce:'median',unit:'conversation',split:'none'}, trend: {view:'trend',split:'provider'},
  };
  const heights = {};
  for (const [name, view] of Object.entries(views)) {
    const url = makeURL({metric:'tokens',minSamples:0,...view},[]);
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    const {loading} = dashboardCardPresentation(url);
    for (const height of [preview.coverHeight, loading.height]) assert.ok(height >= 110 && height <= 800, `${name} cover is ${height}px`);
    heights[name] = preview.coverHeight;
    if (name === 'hours') assert.equal(preview.density,'compact','many box groups use the dense rows the height assumes');
  }
  assert.ok(heights.value < heights.hours && heights.bars < heights.hours, 'a value or a short list is shorter than 24 box rows');
  for (const kind of ['list','box','pie']) assert.ok(fittedCoverHeight(kind,2) < fittedCoverHeight(kind,12), `${kind} covers grow with their rows`);
  assert.equal(fittedCoverHeight('columns',4),fittedCoverHeight('columns',24),'column charts keep one height');
});

test('a dashboard card can show its own count of the chart groups while the dashboard keeps its own', async t => {
  fixture(t);
  const local = usageLedger();
  const url = view => makeURL({metric:'tokens',minSamples:0,split:'repository_name',...view},[]);
  assert.deepEqual(dashboardCardGroups(url({view:'compare'})),{noun:'repositories',max:100,fallback:12});
  assert.equal(dashboardCardGroups(url({view:'compare',compareAs:'share'})).fallback,8);
  assert.equal(dashboardCardGroups(url({view:'trend'})).fallback,15);
  for (const view of [{view:'compare',split:'none'},{view:'scatter',metricY:'cost',unit:'conversation'},{view:'cycle',cycle:'hour',split:'none'}]) assert.equal(dashboardCardGroups(url(view)),undefined,JSON.stringify(view));
  for (const view of [{view:'compare'},{view:'distribution',unit:'conversation'}]) {
    const address = url(view), before = readDashboardCard(address).state;
    const preview = await loadDashboardCardPreview(address,schemas.usage,undefined,local,now,'chart',undefined,undefined,undefined,1);
    assert.equal(preview.metrics[0].clause.display.maxGroups,1);
    assert.deepEqual(readDashboardCard(address).state,before,'the dashboard address is unchanged');
  }
  const trend = url({view:'trend'});
  const all = await loadDashboardCardPreview(trend,schemas.usage,undefined,local,now);
  const one = await loadDashboardCardPreview(trend,schemas.usage,undefined,local,now,'chart',undefined,undefined,undefined,1);
  assert.ok(all.series.length > one.series.length && one.series.length <= 2, 'one series, plus Other');
  const card = collectDashboardCard(url({view:'compare'}),'Repositories','','chart',{},undefined,3);
  assert.equal(card.maxGroups,3);
  assert.equal(updateBookmark(card.id,'Repositories','',undefined,undefined,undefined,null).maxGroups,undefined);
  assert.throws(()=>collectDashboardCard(url({view:'scatter',metricY:'cost',unit:'conversation'}),'Pairs','','chart',{},undefined,3),/whole number/);
  assert.equal(dashboardCardPresentation(url({view:'compare'}),'chart',3).loading.count,3);
});

test('cycle previews fold the same complete weekdays as the dashboard', async t => {
  fixture(t);
  const local = usageLedger();
  const filters = [{field:'last_usage_at',op:'>=',value:new Date(now-30*86400000).toISOString()},{field:'first_usage_at',op:'<',value:new Date(now).toISOString()}];
  const url = makeURL({metric:'tokens',view:'cycle',cycle:'weekday',reduce:'median'},filters);
  const {state,where} = readDashboardCard(url,new Date(now));
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const analysis = analysisQuery(catalog,state,where,{now,zone});
  const entry = (await runAnalysisQuery('usage',schemas.usage,analysis.query,{local,now}))[0];
  const expected = cycleResult(catalog,state,entry,{from:now-30*86400000,to:now},zone);
  const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
  assert.deepEqual(preview.metrics[0].clause,expected.clause);
  assert.deepEqual(preview.metrics[0].result,expected.result);
});

test('timestamp cycle card covers preserve native slots and weekday labels', async t => {
  fixture(t);
  const topic = 'mcp', catalog = analysisCatalogs[topic];
  const where = [{field:'called_at',op:'>=',value:new Date(now-7*86400000).toISOString()},{field:'called_at',op:'<',value:new Date(now).toISOString()}];
  const url = dashboardCardURL('/analyze?analyze_topic=mcp',catalog,{metric:'calls',view:'cycle',cycle:'weekday',reduce:'median'},where);
  const preview = await loadDashboardCardPreview(url,schemas.mcp_calls,undefined,{rows:[{id:1,called_at:new Date(now-86400000).toISOString()}]},now);
  assert.equal(preview.metrics[0].result.buckets.length,7);
  assert.equal(preview.groupLabels['[1]'],'Monday');
  assert.equal(preview.groupLabels['[7]'],'Sunday');
});

test('dashboard cards use the existing Home preview loader alongside ordinary saved queries', async t => {
  fixture(t);
  global.location = new URL('http://parity.test/home');
  const {loadQueryPreviews} = await import('../../../src/query-previews');
  const results = new Map();
  const address = makeURL({metric:'tokens',split:'provider',view:'compare'},[]);
  await loadQueryPreviews([
    {id:'dashboard',dataset:'usage',schema:schemas.usage,query:EMPTY_QUERY,options:{dashboard:address}},
    {id:'query',dataset:'usage',schema:schemas.usage,query:{...EMPTY_QUERY,aggregations:[{id:'tokens',op:'sum',field:'total_tokens',groupBy:[]}]}},
    {id:'counted',dataset:'usage',schema:schemas.usage,query:EMPTY_QUERY,options:{dashboard:address,groups:2}},
    {id:'counted-query',dataset:'usage',schema:schemas.usage,query:{...EMPTY_QUERY,aggregations:[{id:'tokens',op:'sum',field:'total_tokens',groupBy:['provider']}]},options:{groups:2}},
  ],(id,preview,error)=>{assert.ifError(error);results.set(id,preview);});
  assert.equal(results.get('dashboard').state,'ready');
  assert.equal(results.get('query').state,'ready');
  for (const id of ['counted','counted-query']) assert.equal(results.get(id).metrics[0].clause.display.maxGroups,2,`${id} carries its card's count`);
});

test('Carbon cards retain independent assumptions when refreshed from Home', async t => {
  fixture(t);
  const {loadClassicScript} = await import('../helpers/classic-script.mjs');
  const {carbonAnalysisCatalog} = await import('../../../src/carbon-analysis-catalog');
  const factors = (await import('../../../../carbon/co2_factors.json')).default;
  const calculator = loadClassicScript('carbon.js',{pharosPrefs:{get:(_key,fallback)=>fallback,set(){}}}).window.pharosCarbon;
  window.pharosCarbon = calculator;
  const before = structuredClone(calculator.settingsSnapshot(factors));
  const local = usageLedger();
  local.rows = local.rows.map(row=>({...row,carbon_tier:'medium'}));
  const totals = [];
  for (const scenario of ['low','high']) {
    const settings = {scenario,grid:'custom',customGrid:100};
    const catalog = carbonAnalysisCatalog({settings,factors,estimate:(factors,period)=>calculator.estimate(factors,period,settings)});
    const url = dashboardCardURL('/analyze?analyze_topic=carbon',catalog,dashboardDefaults.carbon,[]);
    assert.deepEqual(JSON.parse(readDashboardCard(url).params.get('analyze_carbon')),settings);
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    totals.push(preview.buckets.reduce((sum,bucket)=>sum+Object.values(bucket.values).reduce((a,b)=>a+b,0),0));
  }
  assert.ok(totals[0]>0 && totals[1]>totals[0]);
  assert.deepEqual(structuredClone(calculator.settingsSnapshot(factors)),before);
});

test('Parallelism cards carry their own counting and gap into Home preview requests', async t => {
  const {values} = fixture(t);
  global.location = new URL('http://parity.test/home');
  values.set('pharos-concurrency-v1',{counting:'workspace',gap:60});
  const captured = {counting:'agent',gap:15};
  const url = dashboardCardURL('/analyze?'+new URLSearchParams({analyze_topic:'parallelism',analyze_parallelism:JSON.stringify(captured)}),analysisCatalogs.parallelism,dashboardDefaults.parallelism,[]);
  const originalFetch = global.fetch, settings = [];
  global.fetch = async (address,init) => {
    const request = new URL(address,location.origin);
    if (request.searchParams.has('parallelism_settings')) settings.push(JSON.parse(request.searchParams.get('parallelism_settings')));
    return originalFetch(request.pathname,init);
  };
  await loadDashboardCardPreview(url,schemas.parallelism,undefined,undefined,now);
  assert.ok(settings.length>0);
  assert.ok(settings.every(value=>value.counting==='agent' && value.gap===15));
  assert.deepEqual(values.get('pharos-concurrency-v1'),{counting:'workspace',gap:60});
});


// Parallelism's URL holds two charts: its ribbon and its catalog chart.
test('a Parallelism URL offers its ribbon and its chart as cards, each collected and previewed on its own', async t => {
  fixture(t);
  const hour = 3600000, start = Date.parse('2026-10-01T06:00:00Z');
  const ribbon = {range:'day',day:'2026-10-01',counting:'workspace',gap:0,mode:'user'};
  const url = dashboardCardURL('/analyze?'+new URLSearchParams({analyze_topic:'parallelism',analyze_parallelism:JSON.stringify(ribbon)}),analysisCatalogs.parallelism,dashboardDefaults.parallelism,[]);
  assert.deepEqual(dashboardCardTargets(url).map(target=>target.id),['ribbon','chart']);
  assert.deepEqual(dashboardCardTargets(makeURL(dashboardDefaults.tokens)).map(target=>target.id),['chart']);
  assert.throws(()=>collectDashboardCard(makeURL(dashboardDefaults.tokens),'Ribbon','','ribbon'),/cannot/);
  const card = collectDashboardCard(url,'Busy workspaces','','ribbon');
  assert.equal(card.dashboardTarget,'ribbon');
  assert.equal(dashboardCardCollected(url,'ribbon'),true);
  assert.equal(dashboardCardCollected(url),false,'the chart on the same URL is still offered');
  assert.equal(collectDashboardCard(url,'Chart','').dashboardTarget,undefined);
  assert.equal(dashboardCardWindow(url,'ribbon'),'Saved range');
  assert.equal(dashboardCardPresentation(url,'ribbon').category,'machines');
  // Two workspaces in two repositories; one busy for two hours, one for half an hour.
  const thread = (id,repository,turns) => ({id,conversation_id:id,work_id:id,workspace_id:id,workspace_name:id,repository,title:id,provider:'claude',model:'m',agent_depth:0,turns,model_spans:[]});
  const timing = {generated_at:'',timezone:'UTC',period_start:'2026-10-01',period_end:'2026-10-01',days:['2026-10-01'],coverage:{},
    threads:[thread('a','alpha',[[start,start+2*hour]]),thread('b','beta',[[start+hour,start+1.5*hour]])]};
  const preview = await loadDashboardCardPreview(url,schemas.parallelism,undefined,undefined,start,'ribbon',timing);
  assert.equal(preview.state,'ready');
  assert.equal(preview.buckets.length,24,'a day is drawn by the hour');
  assert.deepEqual(preview.series.map(item=>item.key),['alpha','beta']);
  const at = time => preview.buckets.find(bucket=>bucket.date.getTime()===time).values;
  assert.deepEqual(at(start),{alpha:1});
  assert.deepEqual(at(start+hour),{alpha:1,beta:.5});
  assert.deepEqual(at(start+2*hour),{});
  const filtered = dashboardCardURL('/analyze?'+new URLSearchParams({analyze_topic:'parallelism',analyze_parallelism:JSON.stringify({...ribbon,repository:'beta',range:'7d'})}),analysisCatalogs.parallelism,dashboardDefaults.parallelism,[]);
  assert.equal(dashboardCardWindow(filtered,'ribbon'),'Past 7 days');
  const only = await loadDashboardCardPreview(filtered,schemas.parallelism,undefined,undefined,start,'ribbon',timing);
  assert.deepEqual(only.series.map(item=>item.key),['beta']);
});


// The Habits URL holds its sections and its catalog chart; each section's
// card keeps the selectors in the URL and draws from the habits summary.
test('a Habits URL offers each section as a card, titled, windowed and previewed by its selectors', async t => {
  fixture(t);
  const day = (name, weekday, engaged, unattended = 0, automated = 0) => ({ day: name, weekday, you: { engaged_ms: engaged, messages: engaged ? 3 : 0, typed_words: 0, focus_blocks: 0, focus_ms: 0,
    ...engaged ? { first: Date.parse(`${name}T09:00:00Z`), last: Date.parse(`${name}T09:00:00Z`) + engaged, blocks: [[Date.parse(`${name}T09:00:00Z`), Date.parse(`${name}T09:00:00Z`) + engaged]] } : {} },
    with_you: { elapsed_ms: 0, active_ms: 0, turns: 0 }, unattended: { elapsed_ms: unattended, active_ms: unattended, turns: 1 }, automated: { elapsed_ms: automated, active_ms: automated, turns: automated ? 1 : 0 },
    agents_ms: unattended, parallel_ms: 0, peak_agents: 1, qualifies: { you: engaged > 0, agents: unattended >= 600000, automated: automated > 0 } });
  const hour = 3600000, empty = () => Array.from({ length: 7 }, () => Array(24).fill(0));
  const summary = { generated_at: '', timezone: 'UTC', today: '2026-10-07', period_start: '2026-10-01', mode: 'all', settings: { vacations: [], youMessages: 1, agentMinutes: 10, engagedGap: 15, dayStart: 0 },
    days: ['01','02','03','04','05','06','07'].map((d, i) => day(`2026-10-${d}`, (i + 3) % 7, i === 3 ? 0 : 2 * hour, hour, i % 2 ? hour : 0)),
    streaks: { you: { current: 3, longest: 3, longest_from: '2026-10-05', longest_to: '2026-10-07', today: 'done' }, agents: { current: 7, longest: 7, today: 'done' }, automated: { current: 0, longest: 1, today: 'pending' } },
    windows: [], punchcard: { from: '2026-10-01', to: '2026-10-07', weeks: empty().map(hours => hours.map(() => 1)), cells: { you: empty(), with_you: empty(), unattended: empty(), automated: empty() } },
    automations: [{ initiator: 'scripted', label: 'claude -p', runs: 3, active_days: 3, agent_hours: 3, first_run: 0, last_run: 0, median_gap_ms: 2 * 86400000, missed: 0, daily: {} }],
    coverage: { turns: 10, unmatched_turns: 0, typed_messages: 6, messages: 6 } };
  const address = '/analyze?' + new URLSearchParams({ analyze_topic: 'habits', habits_calendar: 'automated', habits_weekends: 'hide', habits_weeks: '13', habits_punchcard: 'agents', habits_punchcard_days: 'all', habits_shape_days: '7', habits_day: '2026-10-03' });
  const url = dashboardCardURL(address, analysisCatalogs.habits, dashboardDefaults.habits, []);
  assert.equal(new URL(url, location.origin).searchParams.has('habits_day'), false, 'a selected day is not part of a card');
  assert.deepEqual(dashboardCardTargets(url).map(target => target.id), ['habits-streaks', 'habits-calendar', 'habits-punchcard', 'habits-days', 'habits-pace', 'habits-automations', 'chart']);
  assert.equal(dashboardCardTargets(url).find(target => target.id === 'habits-calendar').title, 'Days with automatic agent runs · weekdays only');
  assert.equal(dashboardCardWindow(url, 'habits-calendar'), 'Past 13 weeks');
  // A year's calendar draws the weeks a card can show, and says so.
  const year = dashboardCardURL(address.replace('habits_weeks=13', 'habits_weeks=53'), analysisCatalogs.habits, dashboardDefaults.habits, []);
  assert.equal(dashboardCardWindow(year, 'habits-calendar'), 'Past 17 weeks');
  const yearPreview = await loadDashboardCardPreview(year, schemas.rhythm, undefined, undefined, Date.parse('2026-10-07T12:00:00Z'), 'habits-calendar', undefined, summary);
  assert.equal(yearPreview.habits.weeks, 17);
  assert.match(yearPreview.note, /open it for the year/);
  assert.equal(dashboardCardWindow(url, 'habits-punchcard'), 'All time');
  assert.equal(dashboardCardWindow(url, 'habits-days'), 'Past 7 days');
  assert.equal(dashboardCardWindow(url, 'habits-streaks'), 'Today');
  assert.equal(dashboardCardPresentation(url, 'habits-calendar').category, 'machines');
  assert.equal(dashboardCardPresentation(url, 'habits-streaks').category, 'human');
  assert.match(dashboardCardDescription(url, 'habits-calendar'), /automatic agent runs/);
  assert.equal(dashboardCardDescription(url), dashboardCardDescription(url, 'chart'));
  assert.throws(() => collectDashboardCard(makeURL(dashboardDefaults.tokens), 'Streak', '', 'habits-streaks'), /cannot/);
  for (const target of ['habits-streaks', 'habits-calendar', 'habits-punchcard', 'habits-days', 'habits-pace', 'habits-automations']) {
    const card = collectDashboardCard(url, target, '', target);
    assert.equal(card.dashboardTarget, target);
    assert.equal(dashboardCardCollected(url, target), true);
  }
  const preview = target => loadDashboardCardPreview(url, schemas.rhythm, undefined, undefined, Date.parse('2026-10-07T12:00:00Z'), target, undefined, summary);
  const streaks = await preview('habits-streaks');
  assert.deepEqual(streaks.insights.map(item => [item.label, item.value]), [['Your streak', '3 days'], ['Longest streak', '3 days'], ['Yesterday', '2.0 h'], ['Last 7 days', '12 h']]);
  const calendar = await preview('habits-calendar');
  assert.deepEqual([calendar.state, calendar.habits.chart, calendar.habits.actor, calendar.habits.weeks, calendar.habits.weekends], ['ready', 'calendar', 'automated', 13, false]);
  assert.equal((await preview('habits-punchcard')).habits.view, 'agents');
  const shape = await preview('habits-days');
  assert.deepEqual([shape.habits.chart, shape.habits.count], ['days', 7]);
  assert.equal((await preview('habits-pace')).insights.length, 4);
  assert.deepEqual((await preview('habits-automations')).ranking.map(item => item.value), [3]);

  // The Card Library's Habits templates are the same cards: each opens the
  // Habits page at its selectors, offers its section, draws through the
  // ordinary preview loader and is collected as that dashboard card.
  const { queryRecipes, instantiateRecipe, suggestionWindowLabel } = await import('../../../src/curiosity-data');
  const { collectCardTemplate } = await import('../../../src/dashboard-card-model');
  const { loadQueryPreviews } = await import('../../../src/query-previews');
  const templates = queryRecipes.filter(recipe => recipe.habits);
  assert.deepEqual(new Set(templates.map(recipe => recipe.habits.target)), new Set(['habits-streaks', 'habits-calendar', 'habits-punchcard', 'habits-days', 'habits-pace', 'habits-automations']));
  t.mock.method(global, 'fetch', async request => { assert.match(String(request), /^\/api\/habits\?/); return new Response(JSON.stringify(summary)); });
  for (const recipe of templates) {
    const item = instantiateRecipe(recipe, null), address = new URL(item.url, location.origin);
    assert.equal(`${address.pathname}?${address.searchParams.get('analyze_topic')}`, '/analyze?habits', recipe.id);
    assert.ok(dashboardCardTargets(item.url).some(target => target.id === recipe.habits.target), recipe.id);
    assert.equal(suggestionWindowLabel(item), dashboardCardWindow(item.url, recipe.habits.target), recipe.id);
    let drawn;
    await loadQueryPreviews([{ id: item.id, dataset: item.dataset, query: item.query, schema: schemas.rhythm, options: { recipe: item, url: item.url } }], (_id, value, error) => { assert.ifError(error); drawn = value; });
    assert.equal(drawn.summary, dashboardCardTargets(item.url).find(target => target.id === recipe.habits.target).title, recipe.id);
  }
  const away = instantiateRecipe(templates.find(recipe => recipe.id === 'habits-punchcard-away'), null);
  assert.equal(new URL(away.url, location.origin).searchParams.get('habits_punchcard'), 'unattended');
  const kept = await collectCardTemplate(away);
  assert.deepEqual([kept.dashboard, kept.dashboardTarget, kept.templateId, kept.url], [true, 'habits-punchcard', away.instanceId, away.url]);
});

test('Waiting and Thinking charts are dashboard cards over their page population', async t => {
  const { requests } = fixture(t);
  const page = (topic, thinking) => '/analyze?' + new URLSearchParams({ analyze_mode: 'dashboards', analyze_topic: topic, analyze_data: 'model_requests', thinking: JSON.stringify(thinking),
    analyze_filters: encodeQuery({ ...EMPTY_QUERY, where: [{ field: 'harness', op: '=', value: 'codex' }] }), highlight: 'section:x', card_id: 'thinking-compactions' });
  const overview = pageCardURL(page('thinking-overview', { range: '30d', model: 'gpt-5' })), params = new URL(overview, location.origin).searchParams;
  assert.deepEqual([params.has('highlight'), params.has('card_id'), params.get('analyze_topic')], [false, false, 'thinking-overview']);
  assert.deepEqual(dashboardCardTargets(overview).map(target => [target.id, target.title]), [['thinking-waits', 'Model wait distribution: gpt-5 · harness is codex']]);
  assert.equal(dashboardCardWindow(overview, 'thinking-waits'), 'Past 30 days');
  assert.equal(dashboardCardPresentation(overview, 'thinking-compactions').category, 'thinking');
  // Home reads the page's query filters and controls as the card's records.
  const source = dashboardCardSource(overview);
  assert.equal(source.dataset, 'model_requests');
  assert.deepEqual(source.where.slice(0, 3), [{ field: 'harness', op: '=', value: 'codex' }, { field: 'model', op: '=', value: 'gpt-5' }, { field: 'requested_at', op: '>=', value: '-30d' }]);
  assert.throws(() => collectDashboardCard(overview, 'Thinking', '', 'thinking-distributions'), /cannot/);
  assert.throws(() => collectDashboardCard(overview, 'Compactions', '', 'thinking-compactions'), /cannot/);
  const waits = await loadDashboardCardPreview(overview, schemas.model_requests, undefined, undefined, now, 'thinking-waits');
  assert.equal(waits.action, DASHBOARD_CARD_ACTION);
  assert.deepEqual([waits.metrics[0].clause.distribution.kind, waits.metrics[0].clause.groupBy, waits.metrics[0].clause.display.xScale.mode], ['box', ['model'], 'log']);
  assert.ok(requests.some(request => JSON.stringify(request.request ?? '').includes('gpt-5')), 'the preview reads the page population');
  await assert.rejects(loadDashboardCardPreview(overview, schemas.usage, undefined, undefined, now, 'thinking-waits'), /unavailable/);
  // Compactions draw a column for every day from the first to the last, stacked by model.
  const compactions = await loadDashboardCardPreview(overview, schemas.model_requests, undefined, undefined, now, 'thinking-compactions');
  assert.ok(Array.isArray(compactions.buckets) && Array.isArray(compactions.series));
  const drawn = compactionColumns({ buckets: [['2026-10-01', 'a', 2], ['2026-10-03', 'b', 1], ['2026-10-03', 'a', 1]].map(([day, model, value]) => ({ keys: [day, model], value, count: value })) });
  assert.deepEqual(drawn.buckets.map(bucket => [bucket.key, bucket.values]), [['2026-10-01', { 's:a': 2 }], ['2026-10-02', {}], ['2026-10-03', { 's:a': 1, 's:b': 1 }]]);
  assert.deepEqual(drawn.series.map(item => item.label), ['a', 'b']);
  // A compaction without a recorded day is left off rather than emptying the card.
  const undated = compactionColumns({ buckets: [['2026-10-01', 'a', 2], [null, 'a', 1]].map(([day, model, value]) => ({ keys: [day, model], value, count: value })) });
  assert.deepEqual(undated.buckets.map(bucket => bucket.key), ['2026-10-01']);

  // The page's own response: its groups, their order and their grids.
  const edges = [0.1, 0.2, 1, 5, 10, 100], counts = [1, 2, 3, 4, 0];
  const group = (label, scale) => ({ key: label, label, model: label, effort: '', Retained: 20, Timed: 10, Adjusted: 8, Conversations: 4, RetainedConversations: 5,
    stats: { n: 10, median: 2 * scale, p75: null, p90: null, p95: null, q1: null, q3: null, low: null, high: null }, counts,
    periods: { '2026-10-01': { stats: { n: 10 }, retained: 20, conversations: 4, counts } },
    calendar: [{ day: '2026-10-01', hour: 9, Raw: 4 * scale, Ratio: 0.5, N: 10, Adjusted: 8, Conversations: 4, AdjustedConversations: 4 }, { day: '2026-10-02', hour: 14, Raw: 8 * scale, Ratio: 1.5, N: 10, Adjusted: 8, Conversations: 4, AdjustedConversations: 4 }],
    bands: [], comparison: { days: 0, Ratio: null, Low: null, High: null } });
  const response = { periodBounds: {}, calendarBounds: {}, Retained: 40, Timed: 20, Adjusted: 16, Conversations: 8, Hidden: 0, Models: [], Efforts: [], edges,
    groups: [group('a', 1), group('b', 2)], summary: {}, periods: ['2026-10-01'], calendarPeriods: ['2026-10-01', '2026-10-02'], From: '', To: '', timezone: 'UTC' };
  // Thinking's comparison follows its view and breakdown.
  const detail = (thinking) => pageCardURL(page('thinking-detail', thinking));
  const harness = detail({ view: 'wait-box', groupBy: 'harness', range: 'all' });
  assert.deepEqual(dashboardCardTargets(harness).map(target => [target.id, target.title]), [['thinking-distributions', 'Wait by agent / harness · harness is codex']]);
  assert.equal(dashboardCardWindow(harness, 'thinking-distributions'), 'All time');
  assert.deepEqual((await loadDashboardCardPreview(harness, schemas.model_requests, undefined, undefined, now, 'thinking-distributions', undefined, undefined, response)).metrics[0].clause.groupBy, ['harness']);
  const reasoning = detail({ view: 'reasoning', groupBy: 'repository' });
  assert.equal(dashboardCardTargets(reasoning)[0].title, 'Reported reasoning by repository · harness is codex');
  assert.deepEqual((await loadDashboardCardPreview(reasoning, schemas.model_requests, undefined, undefined, now, 'thinking-distributions', undefined, undefined, response)).metrics[0].clause.groupBy, ['repository_name']);
  assert.equal(dashboardCardTargets(detail({ view: 'histogram', groupBy: 'none' }))[0].title, 'Model wait distribution · harness is codex');
  // A whole library's exact quantiles exceed the service's budget; without a breakdown the card draws a box per model.
  assert.deepEqual((await loadDashboardCardPreview(detail({ view: 'histogram', groupBy: 'none' }), schemas.model_requests, undefined, undefined, now, 'thinking-distributions')).metrics[0].clause.groupBy, ['model']);

  // A card keeps the page's sort: its first boxes are the page's first groups.
  const boxes = { id: 'w', buckets: ['claude-code', 'other', 'codex'].map(harness => ({ keys: [harness], value: 1, count: 1 })) };
  const order = groups => orderedBoxes(boxes, groups.map(label => group(label, 1))).buckets.map(bucket => bucket.keys[0]);
  assert.deepEqual([order(['codex', 'claude-code']), order(['claude-code', 'codex'])], [['codex', 'claude-code', 'other'], ['claude-code', 'codex', 'other']]);
  assert.deepEqual(thinkingCardRecipe('thinking-distributions', initialThinking(new URLSearchParams({ thinking: JSON.stringify({ view: 'wait-box', groupBy: 'harness', sortBy: 'median' }) }))).measures[0].sort, undefined);
  // Volume is each box's timed waits, which the query sorts on itself.
  const volume = await loadDashboardCardPreview(detail({ view: 'wait-box', groupBy: 'harness', sortBy: 'volume', sortDirection: 'asc' }), schemas.model_requests, undefined, undefined, now, 'thinking-distributions');
  assert.deepEqual(volume.metrics[0].clause.sort, [{ key: 'samples', dir: 'asc' }]);

  // Every breakdown the page offers draws on a card, numeric ones included.
  const { thinkingBreakdowns } = await import('../../../src/thinking-model');
  for (const [groupBy] of thinkingBreakdowns) for (const view of ['wait-box', 'reasoning']) {
    const drawn = await loadDashboardCardPreview(detail({ view, groupBy, range: '30d' }), schemas.model_requests, undefined, undefined, now, 'thinking-distributions', undefined, undefined, response);
    assert.equal(drawn.metrics[0].result.error, undefined, `${view} by ${groupBy}`);
  }

  // Thinking's grids draw as grids, from the response the page draws.
  const grid = async thinking => { const url = detail({ groupBy: 'model', range: '30d', ...thinking }); return [dashboardCardTargets(url)[0].title, await loadDashboardCardPreview(url, schemas.model_requests, undefined, undefined, now, 'thinking-distributions', undefined, undefined, response)]; };
  const [dayTitle, days] = await grid({ view: 'calendar' });
  assert.equal(dayTitle, 'Wait by time of day · harness is codex');
  assert.deepEqual([days.grid.rows, days.grid.columns.length, days.grid.cells[0][9], days.grid.cells[1][14], days.grid.cells[0][0], days.grid.tone], [['10-01', '10-02'], 24, 0.5, 1, null, 'sequential']);
  assert.match(days.grid.caption, /^a, the busiest of 2 groups/);
  assert.ok(days.coverHeight > 0 && days.state === 'ready');
  const [, against] = await grid({ view: 'calendar', calendarMetric: 'ratio' });
  assert.deepEqual([against.grid.tone, against.grid.cells[0][9] < 0, against.grid.cells[1][14] > 0], ['diverging', true, true]);
  const [heatTitle, heat] = await grid({ view: 'heatmap', normalization: 'share' });
  assert.equal(heatTitle, 'Wait heatmap by model · harness is codex');
  // Shades run to the 95th percentile of the drawn cells, which the legend
  // names; the wait range no request fell in is left off.
  assert.deepEqual([heat.grid.rows, heat.grid.cells[0].map(cell => Math.round(cell * 100) / 100), heat.grid.columnTicks, heat.grid.legend], [['a', 'b'], [0.25, 0.5, 0.75, 1], [0, 2], ['0%', '40%+ of a group']]);
  const [, time] = await grid({ view: 'time', normalization: 'count' });
  // The slowest waits sit at the top, as on the page.
  assert.deepEqual([time.grid.rows[0], time.grid.cells.map(row => row[0])], ['5 s', [1, 0.75, 0.5, 0.25]]);
});

test('percentage-valued dashboard trends retain their selected scale independently of share-of-total views', async t => {
  fixture(t);
  const local = usageLedger();
  for (const scale of ['linear', 'logarithmic', 'half']) {
    const url = makeURL({ ...dashboardDefaults.tokens, metric:'cache_read_share', split:'provider', scale }, []);
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    assert.equal(preview.dashboardTrend.percent, false);
    assert.equal(preview.dashboardTrend.unit, 'percent');
    assert.equal(preview.dashboardTrend.scale, scale);
    assert.equal(dashboardCardDescription(url).includes('Logarithmic scale'), scale === 'logarithmic', 'percentage-valued metrics use their selected scale');
  }
  const selected = { ...dashboardDefaults.tokens, metric:'tokens', split:'provider', scale:'logarithmic' };
  const url = makeURL({ ...selected, share:true }, []);
  const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
  assert.equal(preview.dashboardTrend.percent, true);
  assert.equal(preview.dashboardTrend.unit, 'percent');
  assert.equal(readDashboardCard(url).state.scale, 'logarithmic', 'share trends retain the scale for switching back');
  assert.doesNotMatch(dashboardCardDescription(url), /Logarithmic scale/, 'share trends draw a linear percentage axis');
  const pie = switchView(catalog, { ...selected, share: true }, 'compare');
  assert.equal(analysisQuery(catalog, pie, []).clause.display.kind, 'pie');
  assert.equal(pie.scale, 'logarithmic', 'pies retain the dormant scale');
  assert.doesNotMatch(dashboardCardDescription(makeURL(pie, [])), /Logarithmic scale/);
  for (const view of ['trend', 'compare', 'distribution', 'scatter', 'cycle']) {
    const active = switchView(catalog, selected, view);
    assert.match(dashboardCardDescription(makeURL(active, [])), /Logarithmic scale/, `${view} still reports an active logarithmic axis`);
  }
  assert.doesNotMatch(dashboardCardDescription(makeURL({ ...selected, view: 'compare', split: 'none' }, [])), /Logarithmic scale/, 'a scalar comparison has no axis');
});


test('compaction card defaults follow trigger/service controls and filters', t => {
  fixture(t);
  const catalog = analysisCatalogs.compaction;
  const address = (state, where = []) => dashboardCardURL('/analyze?analyze_topic=compaction', catalog, { ...dashboardDefaults.compaction, ...state }, where);
  const triggers = dashboardCardMetadata(address({}), 'chart', schemas.compactions);
  assert.match(triggers.title, /Compactions per day by trigger/);
  assert.equal(dashboardCardPresentation(address({})).category, 'thinking');
  const services = dashboardCardMetadata(address({ view: 'compare', split: 'provider' }, [{ field: 'trigger', op: '=', value: 'human' }]), 'chart', schemas.compactions);
  assert.match(services.title, /Compactions by service/);
  assert.match(services.description, /human/);
  assert.notEqual(services.title, triggers.title);
});


test('saved component shares keep their metric, denominator and context through card previews', async t => {
  fixture(t);
  const local = usageLedger();
  for (const [metric, split] of [['tokens','type'],['input','input_type']]) {
    const url = makeURL({metric,split,view:'compare',compareAs:'share'},[]);
    const {state,where} = readDashboardCard(url,new Date(now));
    assert.equal(state.split,split);
    const analysis = analysisQuery(catalog,state,where,{now});
    const expected = analysisResult(analysis,await runAnalysisQuery('usage',schemas.usage,analysis.query,{local,now}));
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    assert.equal(preview.state,'ready');
    assert.deepEqual(preview.metrics[0].clause,analysis.clause);
    assert.deepEqual(preview.metrics[0].result.buckets.map(bucket=>bucket.value),expected.buckets.map(bucket=>bucket.value));
    assert.deepEqual(preview.metrics[0].result.buckets.map(bucket=>bucket.keys[0]),expected.buckets.map(bucket=>analysisLabels(catalog,state)[split](bucket.keys[0])));
    assert.match(dashboardCardMetadata(url,'chart',schemas.usage).title,/Share of .* by .*type/);
  }
});
