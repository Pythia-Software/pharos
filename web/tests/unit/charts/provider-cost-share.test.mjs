import assert from 'node:assert/strict';
import test from 'node:test';
import { core, schemas } from '../helpers/recipes.mjs';
import { queryRecipes, instantiateRecipe, loadQueryPreview, clearPreviewCache } from '../../../src/curiosity-data';
import { rollingAverageBuckets, shareBuckets, foldSeries } from '../../../src/chart-series';
import { loadQueryPreviews } from '../../../src/query-previews';
const schema = schemas.usage;
const recipe = queryRecipes.find(recipe => recipe.id === 'provider-cost-share');
const execution = { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} };

function fixture(t, mutate = result => result, { rows: selectRows = rows => rows, now = Date.parse('2026-10-21T18:00:00Z') } = {}) {
  const originalFetch = global.fetch, originalLocation = global.location, requests = [];
  global.location = { origin: 'http://preview.test' };
  t.mock.timers.enable({apis:['Date'],now});
  clearPreviewCache();
  global.fetch = async (url, init = {}) => {
    const request = init.body && JSON.parse(init.body);
    const json = body => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
    if (url.includes('computed-columns')) return json([]);
    if (url.endsWith('capabilities')) return json({ computedExecution: execution, metricCapabilities: { version: 2 }, queryCapabilities: { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 } });
    requests.push(request);
    const now = request.now;
    const fact = (id, daysAgo, provider, cost_usd, price_status = 'priced') => {
      const at = new Date(now - daysAgo * 86400000 - 1000);
      return { id, first_usage_at: at.getTime(), day: `${at.getFullYear()}-${String(at.getMonth() + 1).padStart(2, '0')}-${String(at.getDate()).padStart(2, '0')}`, provider, model_family: provider === 'claude' ? 'opus-4-6' : 'gpt-5.3-codex', cost_usd, price_status, total_tokens: 100, cache_read_input_tokens: 90 };
    };
    const rows = selectRows([fact('a', 1, 'claude', 90), fact('b', 1, 'codex', 10, 'assumed'), fact('c', 0, 'codex', 900, 'partial'), fact('unknown', 0, 'codex', null, 'unpriced'), fact('negative', 0, 'codex', -100), fact('old', 91, 'claude', 99999), fact('prior-codex',91,'codex',100), fact('boundary',90,'claude',70), fact('future', -1, 'claude', 99999)]);
    const query = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
      grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } };
    const prepared = core.prepareGrouping(rows, schema, query.grouping, { now });
    const result = core.applyQuery(rows, query, schema, { now });
    return json(mutate({ version: 3, now, sourceTotal: prepared.inputs.length, rows: request.rows ? result.rows : [], total: result.total,
      metrics: core.evaluateMetrics(rows, query, schema, { now, shownRows: result.rows }).metrics,
      schema: { name: prepared.schema.name, idField: prepared.schema.idField, fields: prepared.schema.fields.map(({ name, type }) => ({ name, type })) }, execution },request));
  };
  t.after(() => { global.fetch = originalFetch; global.location = originalLocation; clearPreviewCache(); });
  return requests;
}

test('90-day provider cost shares smooth amounts before normalizing, matching Machine Tokens pricing', async t => {
  const requests = fixture(t), item = instantiateRecipe(recipe, recipe.days[0]);
  const preview = await loadQueryPreview('usage', item.query, schema, { recipe: item, cover: item.cover, window: item.window, url: item.url });
  assert.equal(preview.state, 'ready');
  assert.equal(item.window.days, 90);
  assert.equal(new URL(item.url, location.origin).searchParams.get('window_days'), '90');
  assert.deepEqual(item.query.grouping.keys, ['day', 'provider']);
  assert.deepEqual(preview.timeline, { rollingPeriods: 7, percent: true });
  assert.equal(preview.buckets.filter(bucket=>!bucket.history).length, 91, 'elapsed 90 days touches both partial boundary dates');
  assert.equal(preview.total, 1000, 'include known assumed/partial costs, exclude missing, negative and out-of-window cost');
  assert.equal(preview.headlineMetric, undefined);
  assert.equal(requests.length, 2, 'exact visible totals and smoothing history are separate reductions');
  const averages = rollingAverageBuckets(preview.buckets, preview.timeline.rollingPeriods, {period:'day'}).filter(bucket=>!bucket.history);
  const shares = shareBuckets(averages);
  assert.ok(Math.abs(shares.at(-1).values['s:claude'] - .09) < 1e-12);
  assert.ok(Math.abs(shares.at(-1).values['s:codex'] - .91) < 1e-12);
  assert.equal(averages.at(-1).windowSize, 7);
  assert.equal(averages[0].windowSize,7);
  assert.equal(averages[0].values['s:claude'],(99999+70)/7,'the first bucket includes pre-range days and the full partial boundary day');
  assert.equal(averages[0].values['s:codex'],100/7);
  assert.ok(Math.abs(shares[0].values['s:claude']-(99999+70)/(99999+70+100))<1e-12);
  assert.equal(preview.series.find(series => series.value === 'claude').color, '#d97732');
});

test('partial daily totals cannot silently become a percentage denominator', async t => {
  fixture(t, result => ({ ...result, metrics: result.metrics.map(metric => ({ ...metric, coverage: 'partial' })) }));
  const item = instantiateRecipe(recipe, 90);
  await assert.rejects(loadQueryPreview('usage', item.query, schema, { recipe: item, cover: item.cover, window: item.window }), /Complete daily totals/);
});

test('incomplete or missing smoothing history fails instead of reverting to a partial window', async t => {
  let missing=false;
  const requests = fixture(t,(result,request)=>request.source.where.some(term=>term.field==='first_usage_at'&&term.op==='>='&&!term.value.startsWith('-')) ? {...result,metrics:missing?[]:result.metrics.map(metric=>({...metric,coverage:'partial'}))}:result);
  const item=instantiateRecipe(recipe,90);
  await assert.rejects(loadQueryPreview('usage',item.query,schema,{recipe:item,cover:item.cover,window:item.window}),/Complete daily totals are required for smoothing/);
  assert.equal(requests.length,2);
  missing=true;clearPreviewCache();
  await assert.rejects(loadQueryPreview('usage',item.query,schema,{recipe:item,cover:item.cover,window:item.window}),/metric|history/i);
});

test('all smoothed share cards keep the same first average when the displayed range changes across DST',async t=>{
  const previousTZ=process.env.TZ;process.env.TZ='America/Denver';
  t.after(()=>{if(previousTZ===undefined)delete process.env.TZ;else process.env.TZ=previousTZ;});
  fixture(t,undefined,{now:Date.parse('2026-11-08T18:00:00Z'),rows:()=>Array.from({length:140},(_,index)=>['claude','codex'].map((provider,group)=>{
    const at=new Date(Date.now()-index*86400000-3600000);
    return {id:`${index}-${provider}`,first_usage_at:at.getTime(),day:`${at.getFullYear()}-${String(at.getMonth()+1).padStart(2,'0')}-${String(at.getDate()).padStart(2,'0')}`,provider,model_family:provider==='claude'?'opus-4-6':'gpt-5.3-codex',cost_usd:(index+1)*(group+1),total_tokens:(index+3)*(group+1),price_status:'priced'};
  })).flat()});
  for(const id of ['provider-cost-share','model-token-share-trend','model-cost-share-trend']){
    const recipe=queryRecipes.find(item=>item.id===id);
    const previews=[];
    for(const days of [7,30]){
      const item=instantiateRecipe(recipe,days),before=JSON.stringify(item.query);
      const preview=await loadQueryPreview('usage',item.query,schema,{recipe:item,window:item.window,cover:{...item.cover,timeline:{rollingPeriods:20,percent:true}}});
      assert.equal(JSON.stringify(item.query),before);
      previews.push({preview,buckets:rollingAverageBuckets(preview.buckets,20,{period:'day'}).filter(bucket=>!bucket.history)});
    }
    const first=previews[0].buckets[0],sameDay=previews[1].buckets.find(bucket=>bucket.key===first.key);
    assert.equal(first.windowSize,20);
    assert.deepEqual(first.values,sameDay.values,id);
    assert.ok(Object.values(first.values).every(value=>value>0));
    assert.ok(previews[0].preview.total<previews[1].preview.total,'totals still follow the displayed range');
  }
});

test('smoothing history stays out of totals and destination queries across cached custom windows',async t=>{
  const requests=fixture(t),item=instantiateRecipe(recipe,90),queryBefore=JSON.stringify(item.query),urlBefore=item.url;
  const options={recipe:item,cover:item.cover,window:item.window,url:item.url};
  const first=await loadQueryPreview('usage',item.query,schema,options);
  const again=await loadQueryPreview('usage',item.query,schema,options);
  assert.deepEqual(again,first);
  assert.equal(requests.length,2,'both reductions are cached');
  const cover={...item.cover,timeline:{rollingPeriods:20,percent:true}};
  const extended=await loadQueryPreview('usage',item.query,schema,{...options,cover});
  assert.equal(requests.length,3,'a different window reuses visible data and fetches its own history');
  assert.equal(extended.total,1000);
  assert.deepEqual(extended.metrics,first.metrics);
  assert.equal(JSON.stringify(item.query),queryBefore);
  assert.equal(item.url,urlBefore);
  const averages=rollingAverageBuckets(extended.buckets,20,{period:'day'}).filter(bucket=>!bucket.history);
  assert.equal(averages[0].windowSize,20);
  assert.equal(averages[0].values['s:claude'],(99999+70)/20);
  const shares=shareBuckets(averages);
  assert.ok(Math.abs(Object.values(shares[0].values).reduce((sum,value)=>sum+value,0)-1)<1e-12);
  const folded=await loadQueryPreview('usage',item.query,schema,{...options,cover:{...cover,maxGroups:1}});
  const foldedShares=shareBuckets(rollingAverageBuckets(folded.buckets,20,{period:'day'})).filter(bucket=>!bucket.history);
  assert.ok(foldedShares[0].parts.other.length,'folded history retains its source breakdown');
  assert.ok(Math.abs(foldedShares[0].values.other-shares[0].values['s:codex'])<1e-12);
});

test('zero and one-interval smoothing do not request extra history',async t=>{
  const requests=fixture(t),item=instantiateRecipe(recipe,90);
  for(const rollingPeriods of [0,1]){
    clearPreviewCache();const count=requests.length;
    const preview=await loadQueryPreview('usage',item.query,schema,{recipe:item,window:item.window,cover:{...item.cover,timeline:{rollingPeriods,percent:true}}});
    assert.equal(requests.length-count,1);
    assert.equal(preview.total,1000);
    assert.ok(preview.buckets.every(bucket=>!bucket.history));
  }
});

test('prior activity can produce a smoothed cover while visible totals remain zero',async t=>{
  fixture(t,undefined,{rows:rows=>rows.filter(row=>row.id==='old'||row.id==='prior-codex')});
  const item=instantiateRecipe(recipe,90);
  const preview=await loadQueryPreview('usage',item.query,schema,{recipe:item,cover:item.cover,window:item.window});
  assert.equal(preview.total,0);
  assert.equal(preview.state,'ready');
  const visible=rollingAverageBuckets(preview.buckets,7,{period:'day'}).filter(bucket=>!bucket.history);
  assert.equal(visible.length,91);
  assert.ok(visible[0].values['s:claude']>0);
  assert.deepEqual(visible[7].values,{});
});

test('cancelling while history loads never publishes an incomplete cover',async t=>{
  fixture(t);
  const fetch=global.fetch,item=instantiateRecipe(recipe,90),controller=new AbortController(),publications=[];
  let release,started;
  const waiting=new Promise(resolve=>{release=resolve;}),historyStarted=new Promise(resolve=>{started=resolve;});
  global.fetch=async(url,init)=>{
    const request=init?.body&&JSON.parse(init.body);
    if(request?.source?.where.some(term=>term.field==='first_usage_at'&&term.op==='>='&&!term.value.startsWith('-'))){started();await waiting;}
    return fetch(url,init);
  };
  const pending=loadQueryPreviews([{id:'card',dataset:'usage',query:item.query,schema,options:{recipe:item,cover:item.cover,window:item.window}}],(...args)=>publications.push(args),controller.signal);
  await historyStarted;controller.abort();release();await pending;
  assert.deepEqual(publications,[]);
});

test('model timelines use different token and cost denominators with the same smoothing and model colors', async t => {
  fixture(t);
  for (const [id, total, claudeShare, codexShare] of [['model-token-share-trend', 500, .2, .8], ['model-cost-share-trend', 1000, .09, .91]]) {
    const modelRecipe = queryRecipes.find(recipe => recipe.id === id), item = instantiateRecipe(modelRecipe, modelRecipe.days[0]);
    const preview = await loadQueryPreview('usage', item.query, schema, { recipe: item, cover: item.cover, window: item.window, url: item.url });
    assert.equal(item.window.days, 90);
    assert.deepEqual(item.query.grouping.keys, ['day', 'model_family']);
    assert.deepEqual(preview.timeline, { rollingPeriods: 7, percent: true });
    assert.equal(preview.state, 'ready');
    assert.equal(preview.total, total, id);
    const values = shareBuckets(rollingAverageBuckets(preview.buckets, 7, {period:'day'})).at(-1).values;
    assert.ok(Math.abs(values['s:opus-4-6'] - claudeShare) < 1e-12, id);
    assert.ok(Math.abs(values['s:gpt-5.3-codex'] - codexShare) < 1e-12, id);
    assert.equal(preview.series.find(series => series.value === 'opus-4-6').color, '#e67a30');
    assert.equal(preview.series.find(series => series.value === 'gpt-5.3-codex').color, '#087e62');
  }
});

test('calendar gaps, zero windows, and exact Other retain cost-weighted shares', () => {
  const buckets = Array.from({ length: 8 }, (_, i) => ({ key: String(i), date: new Date(2026, 9, i + 1), values: {} }));
  foldSeries([['0', 'a', 80], ['0', 'b', 15], ['0', 'c', 5]], new Map(buckets.map(bucket => [bucket.key, bucket])), name => name, undefined, [], 1, 'provider');
  const smoothed = rollingAverageBuckets(buckets, 7), shares = shareBuckets(smoothed);
  assert.equal(smoothed[6].values['s:a'], 80 / 7);
  assert.ok(Math.abs(shares[6].values['s:a'] - .8) < 1e-12);
  assert.ok(Math.abs(shares[6].values.other - .2) < 1e-12);
  assert.ok(Math.abs(shares[6].parts.other.reduce((sum, [, value]) => sum + value, 0) - .2) < 1e-12);
  assert.deepEqual(shares[7].values, {}, 'cost drops out after seven calendar buckets');
  assert.deepEqual(shareBuckets([{ ...buckets[0], values: { a: 0 } }])[0].values, { a: 0 });
});
