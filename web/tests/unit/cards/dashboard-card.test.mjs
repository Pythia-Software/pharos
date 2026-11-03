import test from 'node:test';
import assert from 'node:assert/strict';
import { EMPTY_QUERY } from '@pythia-software/query-table-core';
import { DASHBOARD_CARD_ACTION, dashboardCardLabels, dashboardCardURL, readDashboardCard, collectDashboardCard, dashboardCardCollected, dashboardCardWindow, dashboardCardPresentation, dashboardCardTargets, dashboardCardDescription } from '../../../src/dashboard-card-model';
import { fittedCoverHeight } from '../../../src/card-loading-model';
import { loadDashboardCardPreview } from '../../../src/dashboard-card-preview';
import { analysisCatalogs } from '../../../src/analysis-catalogs';
import { dashboardDefaults } from '../../../src/dashboard-defaults';
import { readBookmarks, updateBookmark, moveBookmark, removeBookmark } from '../../../src/bookmarks';
import { normalizeAnalysis, analysisQuery, selectGroups, cycleResult } from '../../../src/dashboard-analysis-model';
import { trendQuery, trendSeries } from '../../../src/dashboard-trend-model';
import { optionLabels } from '../../../src/option-labels';
import { runAnalysisQuery } from '../../../src/analysis-request';
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
  const second = collectDashboardCard(makeURL({...state,split:'project_name'}),'Repositories','');
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
    {id:'query',dataset:'usage',schema:schemas.usage,query:{...EMPTY_QUERY,aggregations:[{id:'tokens',op:'sum',field:'total_tokens',groupBy:[]}]}}
  ],(id,preview,error)=>{assert.ifError(error);results.set(id,preview);});
  assert.equal(results.get('dashboard').state,'ready');
  assert.equal(results.get('query').state,'ready');
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
  const thread = (id,project,turns) => ({id,conversation_id:id,work_id:id,workspace_id:id,workspace_name:id,repository:project,project,title:id,provider:'claude',model:'m',agent_depth:0,turns,model_spans:[]});
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
  const address = '/analyze?' + new URLSearchParams({ analyze_topic: 'habits', habits_calendar: 'automated', habits_weekends: 'hide', habits_weeks: '13', habits_punchcard: 'agents', habits_punchcard_days: 'all', habits_day: '2026-10-03' });
  const url = dashboardCardURL(address, analysisCatalogs.habits, dashboardDefaults.habits, []);
  assert.equal(new URL(url, location.origin).searchParams.has('habits_day'), false, 'a selected day is not part of a card');
  assert.deepEqual(dashboardCardTargets(url).map(target => target.id), ['habits-streaks', 'habits-calendar', 'habits-punchcard', 'habits-days', 'habits-pace', 'habits-automations', 'chart']);
  assert.equal(dashboardCardTargets(url).find(target => target.id === 'habits-calendar').title, 'Days: automatic agent runs, weekdays');
  assert.equal(dashboardCardWindow(url, 'habits-calendar'), 'Past 13 weeks');
  // A year's calendar draws the weeks a card can show, and says so.
  const year = dashboardCardURL(address.replace('habits_weeks=13', 'habits_weeks=53'), analysisCatalogs.habits, dashboardDefaults.habits, []);
  assert.equal(dashboardCardWindow(year, 'habits-calendar'), 'Past 17 weeks');
  const yearPreview = await loadDashboardCardPreview(year, schemas.rhythm, undefined, undefined, Date.parse('2026-10-07T12:00:00Z'), 'habits-calendar', undefined, summary);
  assert.equal(yearPreview.habits.weeks, 17);
  assert.match(yearPreview.note, /open it for the year/);
  assert.equal(dashboardCardWindow(url, 'habits-punchcard'), 'All time');
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
  assert.equal((await preview('habits-days')).habits.chart, 'days');
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

test('percentage-valued dashboard trends retain their selected scale independently of share-of-total views', async t => {
  fixture(t);
  const local = usageLedger();
  for (const scale of ['linear', 'logarithmic', 'half']) {
    const url = makeURL({ ...dashboardDefaults.tokens, metric:'cache_read_share', split:'provider', scale }, []);
    const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
    assert.equal(preview.dashboardTrend.percent, false);
    assert.equal(preview.dashboardTrend.unit, 'percent');
    assert.equal(preview.dashboardTrend.scale, scale);
  }
  const url = makeURL({ ...dashboardDefaults.tokens, metric:'tokens', split:'provider', share:true }, []);
  const preview = await loadDashboardCardPreview(url,schemas.usage,undefined,local,now);
  assert.equal(preview.dashboardTrend.percent, true);
  assert.equal(preview.dashboardTrend.unit, 'percent');
});
