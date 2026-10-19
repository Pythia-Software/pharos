import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {build} from 'esbuild';
import * as core from '@pythia-software/query-table-core';
const built=await build({entryPoints:['src/curiosity-data.ts','src/card-cover.ts','src/api-cost-review-model.ts','src/compact-box-model.ts','src/compact-pie-model.ts','src/card-presentation.ts','src/api-cost-trend.ts'],bundle:true,write:false,platform:'node',format:'esm',outdir:'out'});
const mod=name=>import(`data:text/javascript;base64,${Buffer.from(built.outputFiles.find(f=>f.path.endsWith(`/${name}.js`)).text).toString('base64')}`);
const {queryRecipes,instantiateRecipe}=await mod('curiosity-data'),{cardCoverQuery}=await mod('card-cover');
const {apiCostTheses,apiCostReviewKey,apiCostFeedback}=await mod('api-cost-review-model');
const {filterBoxSamples}=await mod('compact-box-model'),{compactPieSlices}=await mod('compact-pie-model'),{compactMetricPresentation}=await mod('card-presentation');
const {apiCostTrendQuery,apiCostTrendFacts,buildApiCostTrend,usesApiCostTrend}=await mod('api-cost-trend');
const schemas=Object.fromEntries(['usage','library'].map(name=>[name,core.loadSchema(JSON.parse(readFileSync(`../schemas/${name}.schema.json`,'utf8')))]));
const now=Date.parse('2026-10-15T12:00:00Z'),caps={version:3,profile:'qt-sqlite-v1',groupedRows:true,calendarGrouping:true,computedSourceFilters:true,maxRows:10000,maxGroups:10000,maxPopulation:250000},execution={profile:'qt-sqlite-v1',fields:{},resolvedRevisions:{},planToken:''};
const usage=(id,extra={})=>({id,first_usage_at:now-1000,last_usage_at:new Date(now-1000).toISOString(),day:'2026-10-15',workspace_id:'work',repository_name:'repo',title:'Fixture work',root_conversation_id:'root',root_repository_name:'repo',root_title:'Root task',total_tokens:100,cost_usd:5,cost_today_usd:4,price_status:'priced',...extra});
function trendFacts(rows,source,days){const q=apiCostTrendQuery(source,now-Math.max(37,days)*86400000,now);return apiCostTrendFacts(core.evaluateMetrics(rows,q,schemas.usage,{now}).metrics[0]);}
function card(id,days=7){const recipe=queryRecipes.find(r=>r.id===id);return{recipe,query:instantiateRecipe(recipe,days,new Date(now)).query};}
function table(id,rows){const {recipe,query}=card(id);return core.applyQuery(rows,query,schemas[recipe.dataset],{now}).rows;}
function metric(id,rows){const {recipe,query}=card(id),cover=cardCoverQuery(query,recipe);return core.evaluateMetrics(rows,cover.query,schemas[recipe.dataset],{now}).metrics.find(m=>m.id===cover.metricId);}

test('API cost study covers eight active definitions and compiles every destination and cover',()=>{
 const ids=apiCostTheses.flatMap(t=>t.cards.map(c=>c.id));assert.equal(new Set(ids).size,8);assert.deepEqual([...ids].sort(),queryRecipes.filter(r=>r.category==='cost'&&!r.retired).map(r=>r.id).sort());
 for(const id of ids){const {recipe,query}=card(id,30);for(const state of [query,cardCoverQuery(query,recipe).query])assert.deepEqual(core.toQueryRequestV3(state,schemas[recipe.dataset],caps,execution,{now,rows:false}).diagnostics,[],id);assert.ok(instantiateRecipe(recipe,30).description.includes('30'),id);}
});

test('period totals and exact repository shares retain confirmed zeros and exclude incomplete or out-of-window facts',()=>{
 const rows=[usage('a'),usage('b',{cost_usd:0}),usage('partial',{price_status:'partial',cost_usd:999}),usage('assumed',{price_status:'assumed',cost_usd:999}),usage('unpriced',{price_status:'unpriced',cost_usd:null}),usage('missing',{cost_usd:null}),usage('negative',{cost_usd:-1}),usage('zero-token',{total_tokens:0,cost_usd:999}),usage('old',{first_usage_at:now-8*86400000,cost_usd:999})];
 assert.equal(metric('api-cost-total',rows).buckets[0].value,5);assert.equal(compactPieSlices(metric('repository-cost-share',rows),5).total,5);assert.equal(table('api-cost-total',rows)[0].ac_tokens,200);
 const many=Array.from({length:20},(_,i)=>usage(String(i),{repository_name:`repo-${i}`,cost_usd:i+1}));const pie=compactPieSlices(metric('repository-cost-share',many),5);assert.equal(pie.slices.length,6);assert.equal(pie.total,210);assert.equal(pie.slices.reduce((sum,s)=>sum+s.value,0),210);
 for(const id of ['cost-repos','repository-cost-share']){const {recipe,query}=card(id);assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].groupLimit,10000);}
 assert.equal(metric('cost-repos',rows).buckets.reduce((sum,b)=>sum+b.value,0),5);
});

test('root costs include children in other repositories, inactive roots, independent roots in shared workspaces and zeros',()=>{
 const rows=[usage('a'),usage('b',{cost_usd:2}),usage('child',{workspace_id:'child',repository_name:'child repo',cost_usd:10}),usage('child-only',{workspace_id:'elsewhere',repository_name:'elsewhere',root_conversation_id:'inactive-root',cost_usd:4}),usage('zero',{root_conversation_id:'zero',cost_usd:0}),usage('missing',{root_conversation_id:null,cost_usd:999}),usage('old',{first_usage_at:now-8*86400000,cost_usd:999})];
 const work=table('window-work-cost',rows);assert.deepEqual(work.map(r=>r.ac_cost),[17,4,0]);assert.ok(work.every(r=>r.root_repository_name==='repo'&&r.ac_title==='Root task'));
 const boxes=filterBoxSamples(metric('work-cost-distribution',rows),3);assert.equal(boxes.buckets.length,1);const s=boxes.buckets[0].distribution.summary;assert.deepEqual([s.n,s.min,s.median,s.max],[3,0,4,17]);
 const sparse=rows.filter(r=>r.root_conversation_id!=='zero');assert.equal(filterBoxSamples(metric('work-cost-distribution',sparse),3).buckets.length,0);assert.equal(table('work-cost-distribution',sparse).length,2);
});

test('root-count scatter shares the delegated cost population, with zero-cost points retained in its destination',()=>{
 const rows=[usage('a'),usage('b',{workspace_id:'child',repository_name:'child repo'}),usage('second',{root_conversation_id:'second',cost_usd:10}),usage('missing',{root_conversation_id:null,cost_usd:999}),usage('zero',{root_repository_name:'zero',root_conversation_id:'zero',cost_usd:0})];
 const result=metric('work-volume-cost',rows),repo=result.buckets.find(b=>b.keys[0]==='repo');assert.equal(repo.value,2);assert.equal(repo.y,20);assert.equal(result.buckets.length,2);
 const {recipe,query}=card('work-volume-cost');const p=compactMetricPresentation({metrics:[{clause:cardCoverQuery(query,recipe).query.aggregations[0],result}],coverMetricId:'ac_work',fields:schemas.usage.fields});assert.equal(p.axisScales.xScale.mode,'log');assert.equal(p.axisScales.yScale.mode,'log');assert.equal(p.omittedLogPoints,1);assert.equal(table('work-volume-cost',rows).length,2);
});

test('cost history uses exact elapsed boundaries, divides by seven even for sparse windows, and separates headline period',()=>{
 const d=86400000,rows=[usage('recent',{first_usage_at:now-d,cost_usd:70}),usage('edge',{first_usage_at:now-7*d,cost_usd:7}),usage('before',{first_usage_at:now-7*d-1,cost_usd:140}),usage('start',{first_usage_at:now-37*d,cost_usd:21}),usage('too-old',{first_usage_at:now-37*d-1,cost_usd:999}),usage('now',{first_usage_at:now,cost_usd:999}),usage('unpriced',{price_status:'partial',cost_usd:999})];
 for(const days of [7,30]){
  const {recipe,query}=card('api-cost-total',days),history=apiCostTrendQuery(query.grouping.source,now-Math.max(37,days)*d,now);
  for(let offset=0;offset<history.aggregations.length;offset+=20)assert.deepEqual(core.toMetricQuery({...history,aggregations:history.aggregations.slice(offset,offset+20)},schemas.usage,execution).diagnostics,[]);
  const result=core.evaluateMetrics(rows,history,schemas.usage,{now}).metrics[0],study=buildApiCostTrend(apiCostTrendFacts(result),days,now);
  assert.equal(study.value,days===7?77:217);assert.equal(study.points.length,31);assert.equal(study.points[30],11);assert.equal(study.points[0],3);assert.equal(study.lookbackDays,7);assert.equal(study.pointTimes[0],now-30*d);assert.equal(study.pointTimes[30],now);assert.equal(study.format,'USD');assert.match(study.pointValueLabel,/per day|\/ day/);
  assert.throws(()=>apiCostTrendFacts({...result,coverage:'partial'}),/Complete/);assert.equal(apiCostTrendFacts({...result,groupCount:result.buckets.length+1}),undefined);assert.throws(()=>apiCostTrendFacts({...result,processedRows:999}),/Incomplete/);
 }
 const {recipe,query}=card('api-cost-total');
 const job={id:'cost',dataset:'usage',query,schema:schemas.usage,options:{recipe,window:{field:'first_usage_at',mode:'relative',days:7}}};assert.equal(usesApiCostTrend(job),true);assert.equal(usesApiCostTrend({...job,query:core.decodeQueryStrict(core.encodeQuery(query))}),true,"saved default cards retain their trend after query normalization");
 for(const altered of [{...job,query:{...query,where:[{field:'ac_cost',op:'>',value:'1'}]}},{...job,options:{...job.options,cover:{mode:'result',metricId:'ac_tokens'}}},{...job,query:{...query,grouping:{...query.grouping,source:{...query.grouping.source,where:[...query.grouping.source.where,{field:'repository_name',op:'=',value:'x'}]}}}}])assert.equal(usesApiCostTrend(altered),false);
 const empty=buildApiCostTrend(trendFacts([],query.grouping.source,7),7,now);assert.equal(empty.value,undefined);assert.ok(empty.points.every(p=>p===0));
 const zero=buildApiCostTrend(trendFacts([usage('zero',{cost_usd:0})],query.grouping.source,7),7,now);assert.equal(zero.value,0);
});

test('card descriptions describe the population without advertising a display count',()=>{
 for(const recipe of queryRecipes)assert.doesNotMatch(recipe.description,/\b(?:five|six|seven|eight|twelve|\d+) (?:largest|leading|busiest|repositories|models)\b|plus Other|top \d+/i,recipe.id);
 for(const id of ['price-comparison','expensive-work'])assert.equal(card(id).recipe.retired,true,id);
});

test('token pricing coverage weights every positive token across confirmed assumed partial and unknown status',()=>{
 const rows=[usage('priced',{total_tokens:100}),usage('assumed',{total_tokens:900,price_status:'assumed'}),usage('partial',{total_tokens:50,price_status:'partial'}),usage('unpriced',{total_tokens:25,price_status:'unpriced',cost_usd:null}),usage('unknown',{total_tokens:10,price_status:null,cost_usd:null}),usage('zero',{total_tokens:0})];
 const pie=compactPieSlices(metric('price-coverage',rows),4);assert.equal(pie.total,1085);assert.equal(pie.slices.length,5);assert.equal(pie.slices.reduce((sum,s)=>sum+s.value,0),1085);assert.equal(table('price-coverage',rows).length,5);
});

test('repricing matches both known fields and discloses unverified current-price completeness',()=>{
 const rows=[usage('a'),usage('b',{cost_usd:10,cost_today_usd:3}),usage('missing',{cost_today_usd:null,cost_usd:999}),usage('partial-history',{price_status:'partial',cost_usd:999,cost_today_usd:999})];
 const r=metric('price-comparison',rows);assert.equal(r.buckets[0].value,15);assert.equal(r.buckets[0].y,7);assert.equal(table('price-comparison',rows)[0].card_price_delta,-8);assert.match(card('price-comparison').recipe.note,/may be partial or assumed/);assert.equal(card('price-comparison').recipe.measures[0].id,'price-pair');
});

test('lifetime ranking selects by recent activity while retaining lifetime costs and original source metrics',()=>{
 const recent={id:'recent',activity_at:now-1000,token_count:1000,cost_usd:500,price_status:'priced',title:'Long running work',repository_name:'repo'};
 const selected=table('expensive-work',[recent,{...recent,id:'old',activity_at:now-8*86400000,cost_usd:999},{...recent,id:'partial',price_status:'partial',cost_usd:999}]);assert.equal(selected.length,1);assert.equal(selected[0].card_cost_usd,500);assert.equal(selected[0].card_work_title,'Long running work');
 for(const [id,source] of [['cost-repos','sum:cost_usd:day,repository_name'],['expensive-work','work-costs'],['price-coverage','pricing-tokens']])assert.ok(card(id).recipe.measures.some(m=>m.id===source),id);
});

test('API cost feedback exports every level with its own draft and explicit time bases',()=>{
 assert.equal(apiCostReviewKey,'pharos-api-cost-review-v2');const draft=apiCostFeedback.readReviewFeedback(JSON.stringify({overall:'Keep time bases clear.',theses:{accounting:'Show pricing limits.'},cards:{'work-cost-distribution':{verdict:'keep',note:'Keep zero cost visible.'},'primary-domains':{verdict:'keep',note:'Other study'}}}));assert.equal(draft.cards['primary-domains'],undefined);
 const report=apiCostFeedback.exportFeedback(draft,30,{'work-cost-distribution':'Work cost ranges'});for(const text of ['API cost cards — review 2','past 30 days','Keep time bases clear.','Show pricing limits.','Keep zero cost visible.','Verdict: keep','not subscription spend'])assert.ok(report.includes(text),text);
});
