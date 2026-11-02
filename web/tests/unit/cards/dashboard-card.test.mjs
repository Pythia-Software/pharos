import test from 'node:test';
import assert from 'node:assert/strict';
import { EMPTY_QUERY } from '@pythia-software/query-table-core';
import { dashboardCardURL, readDashboardCard, collectDashboardCard, dashboardCardWindow } from '../../../src/dashboard-card-model';
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
  const card = collectDashboardCard(url,' My tokens ',' My description ');
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
  assert.throws(()=>collectDashboardCard(url,'   ',''),/title/);
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
