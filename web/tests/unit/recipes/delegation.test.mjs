import test from 'node:test';
import assert from 'node:assert/strict';
import {cardCoverQuery,recipeHarness,core,schemas} from '../helpers/recipes.mjs';
import {filterBoxSamples} from '../../../src/compact-box-model';
import {analysisQuery} from '../../../src/dashboard-analysis-model';
import {tokenAnalysisCatalog} from '../../../src/token-analysis-catalog';
import {trendQuery,trendSeries} from '../../../src/dashboard-trend-model';
import {compactMetricPresentation} from '../../../src/card-presentation';
const now=Date.parse('2026-10-16T12:00:00Z'),day=86400000,{card,table,metric}=recipeHarness(now);
const fact=(id,extra={})=>({id,first_usage_at:now-1000,last_usage_at:new Date(now-1000).toISOString(),day:'2026-10-16',workspace_id:'work',project_name:'child repo',root_project_name:'root repo',root_conversation_id:'root',root_title:'Root task',agent_session_id:id,session_kind:'root',delegated_session_id:null,delegated_run_depth:0,total_tokens:100,price_status:'unpriced',...extra});
const population=()=>[fact('parent',{total_tokens:1000}),fact('handle',{delegated_session_id:'child',delegated_run_depth:1,total_tokens:100,session_kind:'subagent',price_status:'assumed'}),fact('transcript',{delegated_session_id:'child',delegated_run_depth:1,total_tokens:200,workspace_id:'elsewhere',price_status:'partial'}),fact('nested',{delegated_session_id:'nested',delegated_run_depth:2,total_tokens:400}),fact('single',{root_conversation_id:'single',total_tokens:500}),fact('child-only',{root_conversation_id:'inactive-root',delegated_session_id:'active-child',delegated_run_depth:3,total_tokens:600}),fact('zero-repo',{root_conversation_id:'zero',root_project_name:'zero repo',total_tokens:300}),fact('unknown',{root_conversation_id:null,delegated_run_depth:null,total_tokens:99999}),fact('cycle',{delegated_session_id:'cycle',delegated_run_depth:null,total_tokens:99999}),fact('old',{first_usage_at:now-8*day,delegated_session_id:'old',delegated_run_depth:1,total_tokens:99999}),fact('future',{first_usage_at:now}),fact('zero',{total_tokens:0})];

test('token shares include linked root transcripts and all pricing statuses once with zero repositories retained',()=>{
 const rows=population();
 const r=table('repository-delegated-share',rows).find(r=>r.root_project_name==='root repo');assert.deepEqual([r.dg_child_tokens,r.dg_tokens,r.dg_runs,r.dg_tasks],[1300,2800,3,3]);assert.equal(r.dg_share,1300/2800);assert.equal(table('repository-delegated-share',rows).find(r=>r.root_project_name==='zero repo').dg_share,0);
 const {recipe,query}=card('repository-delegated-share');assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].display.format.kind,'percent');
});

test('task participation weights root tasks equally, retaining zero-delegation and child-only roots',()=>{
 const rows=population(),m=metric('delegating-task-share',rows),r=m.buckets.find(b=>b.keys[0]==='root repo');assert.equal(r.value,2/3);assert.equal(r.count,3);assert.equal(m.buckets.find(b=>b.keys[0]==='zero repo').value,0);
 const tasks=table('delegating-task-share',rows);assert.equal(tasks.length,4);assert.equal(tasks.find(r=>r.root_conversation_id==='root').dg_runs,2);assert.equal(tasks.find(r=>r.root_conversation_id==='single').dg_runs,0);
});

test('depth counts canonical runs, merging handles and transcripts and retaining inactive ancestor depth',()=>{
 assert.deepEqual(metric('delegation-depth',population()).buckets.map(b=>[b.keys[0],b.value]),[[1,1],[2,1],[3,1]]);assert.equal(table('delegation-depth',population()).length,3);
});

test('breadth counts active direct children once per delegating execution, including inactive and nested parents',()=>{
 const child=(id,run,parent,extra={})=>fact(id,{delegated_session_id:run,delegated_parent_session_id:parent,delegated_run_depth:1,...extra});
 const rows=[
  child('handle','a','root-run',{first_usage_at:now-100*day}),
  child('transcript','a','root-run'),child('next-day','a','root-run',{first_usage_at:now-2*day}),
  child('sibling','b','root-run'),
  child('nested-a','c','a',{delegated_run_depth:2}),child('nested-b','d','a',{delegated_run_depth:2}),
  child('other','e','other-root',{root_conversation_id:'other'}),
  child('expired','expired','root-run',{first_usage_at:now-366*day}),
  child('future','future','root-run',{first_usage_at:now}),
  child('zero','zero','root-run',{total_tokens:0}),
  child('unknown','unknown',null),child('cycle','cycle','root-run',{delegated_run_depth:null}),
  fact('root-own-usage'),
 ];
 const {recipe}=card('delegation-breadth');assert.deepEqual(recipe.days,[365]);
 const parents=table('delegation-breadth',rows,365);
 assert.deepEqual(parents.map(r=>[r.delegated_parent_session_id,r.dg_runs]).sort(),[['a',2],['other-root',1],['root-run',2]]);
 const result=metric('delegation-breadth',rows,365),distribution=result.buckets[0].distribution;
 assert.equal(distribution.kind,'histogram');assert.equal(result.buckets[0].count,3);
 const dashboard=analysisQuery(tokenAnalysisCatalog,recipe.destination.chart,card('delegation-breadth',365).query.grouping.source.where);
 const dashboardRows=core.applyQuery(rows,dashboard.query,schemas.usage,{now}).rows;
 assert.deepEqual(dashboardRows.map(r=>r.analysis_value).sort(),[1,2,2]);
});

test('total delegated runs per root task includes nested descendants, child-only activity and zero-delegation tasks',()=>{
 const {recipe}=card('task-delegated-runs');assert.deepEqual(recipe.days,[365]);
 const rows=population(),tasks=table('task-delegated-runs',rows,365);
 assert.deepEqual(tasks.map(r=>[r.root_conversation_id,r.dg_runs]).sort(),[['inactive-root',1],['root',3],['single',0],['zero',0]]);
 assert.equal(metric('task-delegated-runs',rows,365).buckets[0].count,4);
 const dashboard=analysisQuery(tokenAnalysisCatalog,recipe.destination.chart,card('task-delegated-runs',365).query.grouping.source.where);
 assert.deepEqual(core.applyQuery(rows,dashboard.query,schemas.usage,{now}).rows.map(r=>r.analysis_value).sort(),[0,0,1,3]);
});

test('weekly fan-out gives each delegating parent equal weight and deduplicates child usage across days',()=>{
 const child=(id,run,parent,date,week,extra={})=>fact(id,{delegated_session_id:run,delegated_parent_session_id:parent,delegated_run_depth:1,first_usage_at:Date.parse(`${date}T12:00:00Z`),day:date,week,...extra});
 const rows=[
  child('a','a','root','2026-10-05','2026-10-05'),child('b','b','root','2026-10-05','2026-10-05'),child('c','c','root','2026-10-05','2026-10-05'),
  child('a-repeated','a','root','2026-10-07','2026-10-05'),child('nested','d','a','2026-10-08','2026-10-05',{delegated_run_depth:2}),
  child('e','e','root','2026-10-12','2026-10-12'),child('f','f','root','2026-10-12','2026-10-12'),child('g','g','other','2026-10-13','2026-10-12'),
  child('zero','zero','root','2026-10-12','2026-10-12',{total_tokens:0}),
  child('unknown','unknown',null,'2026-10-12','2026-10-12'),
  child('unresolved','unresolved','root','2026-10-12','2026-10-12',{root_conversation_id:null}),
  child('cycle','cycle','root','2026-10-12','2026-10-12',{delegated_run_depth:null}),
  child('expired','expired','root','2025-10-01','2025-09-29'),fact('root-only',{first_usage_at:Date.parse('2026-09-29T12:00:00Z'),week:'2026-09-28'}),
 ];
 const {recipe,query}=card('delegation-fanout-trend',365);assert.deepEqual(recipe.days,[365]);
 const values=result=>result.buckets.map(b=>[b.keys[0],b.value]).sort();
 assert.deepEqual(values(metric('delegation-fanout-trend',rows,365)),[['2026-10-05',2],['2026-10-12',1.5]]);
 const dashboard=trendQuery(tokenAnalysisCatalog,recipe.destination.chart,query.where,{now});
 const results=core.evaluateMetrics(rows,dashboard.query,schemas.usage,{now}).metrics;
 assert.deepEqual(values(results[0]),[['2026-10-05',2],['2026-10-12',1.5]]);
 const series=trendSeries(tokenAnalysisCatalog,recipe.destination.chart,dashboard,results,{now});
 assert.equal(series.error,undefined);assert.deepEqual(series.buckets.map(b=>b.values.total),[2,1.5]);
 const singleWeek=metric('delegation-fanout-trend',rows.filter(row=>row.week==='2026-10-05'),365);
 assert.equal(singleWeek.buckets.length,1);
 const clause=cardCoverQuery(query,recipe).query.aggregations[0];
 const cover=compactMetricPresentation({state:'ready',coverMetricId:clause.id,metrics:[{clause,result:singleWeek}]});
 assert.equal(cover.scalar,false);assert.equal(cover.kind,'line');
 for(const [period,field,unit] of [['days','day',day],['hours','hour',3600000]]){
  const data=Array.from({length:3},(_,index)=>['a','b'].map(run=>{
   const at=Date.parse('2026-10-13T12:00:00Z')+index*unit,date=new Date(at).toISOString();
   return fact(`${run}-${index}`,{delegated_session_id:run,delegated_parent_session_id:'root',delegated_run_depth:1,first_usage_at:at,day:date.slice(0,10),hour:date});
  })).flat();
  const state={...recipe.destination.chart,period,days:3,hours:3},asked=trendQuery(tokenAnalysisCatalog,state,query.where,{now});
  const actual=core.evaluateMetrics(data,asked.query,schemas.usage,{now}).metrics;
  const plotted=trendSeries(tokenAnalysisCatalog,state,asked,actual,{now});
  assert.equal(actual[0].buckets.length,3,`${field} buckets stay independent`);
  assert.deepEqual(plotted.buckets.map(bucket=>bucket.values.total),[2,2,2],`${period} cannot sum repeated daily/hourly fan-out`);
 }
});

test('run boxes sum canonical own usage before sampling',()=>{
 const rows=population(),r=filterBoxSamples(metric('delegated-run-token-ranges',rows),3);assert.equal(r.buckets.length,1);const s=r.buckets[0].distribution.summary;assert.deepEqual([s.n,s.min,s.median,s.max],[3,300,400,600]);
});

test('explicit TL1 revisits sum attempts as a separate population',()=>{
 const attempts=[{id:'a',project:'p',task_id:'task',revisit:true,attempt_count:3,started_at:now-1000},{id:'b',project:'p',task_id:'task',revisit:false,attempt_count:9,started_at:now-1000}];assert.equal(table('tl1-revisits',attempts)[0].card_attempt_count,3);
});

test('lineage cards count unverified, inferred and conflicted runs but never present them as exact',async()=>{
 const {cardQualification,usesLineage}=await import('../../../src/card-qualifier');
 const {queryRecipes}=await import('../../../src/curiosity-data');
 const depth=queryRecipes.find(r=>r.id==='delegation-depth');
 assert.ok(usesLineage(depth)&&depth.exploration.qualifier,'depth cards carry the shared ancestry qualifier');
 for(const id of ['task-delegated-runs','delegation-breadth','repository-delegated-share','run-effort-token-ranges'])assert.ok(queryRecipes.find(r=>r.id===id)?.exploration?.qualifier,id);
 assert.equal(queryRecipes.find(r=>r.id==='repository-token-share')?.exploration?.qualifier,undefined,'cards without lineage fields are unaffected');
 const quality=(id,extra)=>fact(id,{delegated_run_depth_quality:'observed',root_quality:'observed',run_identity:'exact',...extra});
 const rows=[quality('parent',{total_tokens:1000}),quality('observed',{delegated_session_id:'a',delegated_run_depth:1}),quality('legacy',{delegated_session_id:'b',delegated_run_depth:2,delegated_run_depth_quality:'unverified'}),quality('conflict',{delegated_session_id:'c',delegated_run_depth:1,run_identity:'uncertain'})];
 const groups=table('delegation-depth',rows);
 // Every run still counts; the qualified column says which are not exact.
 assert.deepEqual(groups.map(r=>[r.delegated_run_depth,r.lineage_qualified]).sort(),[[1,0],[1,1],[2,1]]);
 // Groups are counted, never summed sessions, so a session in two groups is not counted twice.
 assert.deepEqual(cardQualification(groups,depth.exploration.qualifier,true),{mark:'≈',note:'2 groups include sessions counted through an inferred or unverified link, or with unresolved identity'});
 // A usage row without a session still qualifies through its conversation.
 const sessionless=table('delegation-depth',[quality('parent'),quality('orphan',{agent_session_id:null,conversation_id:'conv-x',delegated_session_id:'x',delegated_run_depth:1,run_identity:'uncertain'})]);
 assert.equal(sessionless.find(r=>r.delegated_run_depth===1).lineage_qualified,1);
 assert.match(cardQualification(groups,depth.exploration.qualifier,false).note,/^At least 2 /);
 assert.equal(cardQualification(table('delegation-depth',[quality('parent'),quality('observed',{delegated_session_id:'a',delegated_run_depth:1})]),depth.exploration.qualifier,true),null,'exact lineage stays unmarked');
});
