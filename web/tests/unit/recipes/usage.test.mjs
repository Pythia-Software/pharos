import test from 'node:test';
import assert from 'node:assert/strict';
import * as core from '@pythia-software/query-table-core';
import {schemas} from '../helpers/recipes.mjs';
import {queryRecipes,instantiateRecipe} from '../../../src/curiosity-data';
import {cardCoverQuery} from '../../../src/card-cover';
import {tokenUsageTheses,tokenUsageReviewKey,tokenUsageFeedback} from '../../../src/token-usage-review-model';
import {filterBoxSamples,boxWhiskerExtent,sharedBoxScale,boxDetails} from '../../../src/compact-box-model';
import {compactPieSlices} from '../../../src/compact-pie-model';
import {compactMetricPresentation} from '../../../src/card-presentation';
const schema=schemas.usage,now=Date.parse('2026-10-16T12:00:00Z'),day=86400000;
const caps={version:3,profile:'qt-sqlite-v1',groupedRows:true,calendarGrouping:true,computedSourceFilters:true,maxRows:10000,maxGroups:10000,maxPopulation:250000},execution={profile:'qt-sqlite-v1',fields:{},resolvedRevisions:{},planToken:''};
const fact=(id,extra={})=>({id,first_usage_at:now-1000,last_usage_at:new Date(now-1000).toISOString(),day:'2026-10-16',workspace_id:'work',repository_name:'repo',root_repository_name:'root repo',root_conversation_id:'root',root_title:'Root title',agent_session_id:'run',root_provider:'codex',root_model_family:'gpt-6-sol',root_effort:'high',delegated_session_id:null,total_tokens:100,input_tokens:90,output_tokens:10,price_status:'unpriced',attribution:'request',...extra});
function card(id,days=7){const recipe=queryRecipes.find(r=>r.id===id);return{recipe,query:instantiateRecipe(recipe,days,new Date(now)).query};}
function table(id,rows){return core.applyQuery(rows,card(id).query,schema,{now}).rows;}
function metric(id,rows){const {recipe,query}=card(id),cover=cardCoverQuery(query,recipe);return core.evaluateMetrics(rows,cover.query,schema,{now}).metrics.find(m=>m.id===cover.metricId);}

test('token study compiles all ten covers and destinations with explicit matching windows',()=>{
 const ids=tokenUsageTheses.flatMap(t=>t.cards.map(c=>c.id));assert.equal(new Set(ids).size,10);assert.deepEqual([...ids].sort(),queryRecipes.filter(r=>r.category==='usage'&&!r.retired).map(r=>r.id).sort());
 for(const id of ids)for(const days of [7,30]){const {recipe,query}=card(id,days);for(const state of [query,cardCoverQuery(query,recipe).query])assert.deepEqual(core.toQueryRequestV3(state,schema,caps,execution,{now,rows:false}).diagnostics,[],id);assert.ok(instantiateRecipe(recipe,days).description.includes(String(days)),id);assert.equal(query.grouping.source.where.filter(t=>t.field==='first_usage_at').length,2);assert.ok(!query.grouping.source.where.some(t=>t.field==='price_status'),id);}
});

test('token allocation includes all price statuses, repeated cached input and missing lineage but excludes nonpositive and old facts',()=>{
 const rows=[fact('unpriced'),fact('assumed',{price_status:'assumed',root_conversation_id:null,total_tokens:200,input_tokens:190}),fact('partial',{price_status:'partial',total_tokens:300}),fact('priced',{price_status:'priced',total_tokens:400}),fact('old',{first_usage_at:now-8*day,total_tokens:99999}),fact('zero',{total_tokens:0}),fact('future',{first_usage_at:now})];
 assert.equal(compactPieSlices(metric('repository-token-share',rows),5).total,1000);assert.equal(metric('usage-repos',rows).buckets.reduce((n,b)=>n+b.value,0),1000);
 const many=Array.from({length:20},(_,i)=>fact(String(i),{repository_name:`repo-${i}`,total_tokens:i+1}));const pie=compactPieSlices(metric('repository-token-share',many),5);assert.equal(pie.slices.length,6);assert.equal(pie.total,210);assert.equal(pie.slices.reduce((n,s)=>n+s.value,0),210);
 for(const id of ['usage-repos','repository-token-share']){const {recipe,query}=card(id);assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].groupLimit,10000);}
});

test('root-task ranking includes cross-repository children and distinguishes independent roots sharing a workspace',()=>{
 const rows=[fact('root'),fact('child',{workspace_id:'child',repository_name:'child repo',total_tokens:200,agent_session_id:'child'}),fact('child-only',{workspace_id:'elsewhere',root_conversation_id:'inactive-root',total_tokens:50}),fact('separate',{root_conversation_id:'independent',total_tokens:25}),fact('orphan',{root_conversation_id:null,total_tokens:99999})];
 const ranked=table('usage-work',rows);assert.deepEqual(ranked.map(r=>r.card_total_tokens),[300,50,25]);assert.ok(ranked.every(r=>r.root_repository_name==='root repo'&&r.card_work_title==='Root title'));assert.equal(ranked[0].card_agent_session_id,2);
 assert.equal(card('usage-work').recipe.measures[0].id,'work-tokens','legacy source metric remains available');
});

test('token boxes require root-task observations and output boxes retain input-only zeros',()=>{
 const rows=[fact('a',{output_tokens:0}),fact('child',{workspace_id:'child',total_tokens:200,output_tokens:0}),fact('b',{root_conversation_id:'second',total_tokens:50,output_tokens:5}),fact('c',{root_conversation_id:'third',total_tokens:25,output_tokens:1}),...Array.from({length:10},(_,i)=>fact(`sparse-${i}`,{root_repository_name:'sparse',root_conversation_id:'one sparse task'}))];
 const total=filterBoxSamples(metric('task-token-ranges',rows),3);assert.equal(total.buckets.length,1);assert.deepEqual([total.buckets[0].distribution.summary.n,total.buckets[0].distribution.summary.min,total.buckets[0].distribution.summary.median,total.buckets[0].distribution.summary.max],[3,25,50,300]);
 const output=filterBoxSamples(metric('task-output-ranges',rows),3);assert.deepEqual([output.buckets[0].distribution.summary.n,output.buckets[0].distribution.summary.min,output.buckets[0].distribution.summary.max],[3,0,5]);assert.equal(table('task-output-ranges',rows).length,4,'sparse root remains inspectable');
});

test('output shares use ratios of total token counters rather than averages or priced-only populations',()=>{
 const rows=[fact('small',{total_tokens:10,input_tokens:0,output_tokens:10}),fact('large',{total_tokens:990,input_tokens:980,output_tokens:0,unclassified_tokens:10,cache_read_input_tokens:900})];
 assert.equal(metric('repository-output-share',rows).buckets[0].value,.01);const result=table('repository-output-share',rows)[0];assert.equal(result.tu_output,10);assert.equal(result.tu_tokens,1000);assert.equal(result.tu_input,980);
});

test('repository input/output uses log axes and retains nonpositive points in the destination',()=>{
 const rows=[fact('one'),fact('two',{input_tokens:20,output_tokens:5}),fact('zero',{repository_name:'no output',output_tokens:0}),fact('no-input',{repository_name:'no input',input_tokens:0})];
 const result=metric('repository-input-output',rows),repo=result.buckets.find(b=>b.keys[0]==='repo');assert.equal(repo.value,110);assert.equal(repo.y,15);
 const {recipe,query}=card('repository-input-output'),p=compactMetricPresentation({metrics:[{clause:cardCoverQuery(query,recipe).query.aggregations[0],result}],coverMetricId:'tu_input',fields:schema.fields});assert.equal(p.axisScales.xScale.mode,'log');assert.equal(p.axisScales.yScale.mode,'log');assert.equal(p.omittedLogPoints,2);assert.equal(table('repository-input-output',rows).length,3);
});

test('timing evidence weights tokens and retains unknown placement rather than counting facts',()=>{
 const rows=[fact('one',{total_tokens:900}),fact('two',{total_tokens:50,attribution:'session-start'}),fact('three',{total_tokens:50,attribution:null})];
 const pie=compactPieSlices(metric('token-time-placement',rows),5);assert.equal(pie.total,1000);assert.equal(pie.slices.find(s=>s.bucket.keys[0]==='request').value,900);assert.equal(table('token-time-placement',rows).length,3);
});

test('token feedback has isolated card/group/set drafts and a complete report',()=>{
 assert.equal(tokenUsageReviewKey,'pharos-token-usage-review-v3');const draft=tokenUsageFeedback.readReviewFeedback(JSON.stringify({overall:'Look at volume.',theses:{tasks:'Include delegated work.'},cards:{'task-output-ranges':{verdict:'keep',note:'Keep zeros.'},'api-cost-total':{note:'Another study'}}}));assert.equal(draft.cards['api-cost-total'],undefined);const report=tokenUsageFeedback.exportFeedback(draft,30,{'task-output-ranges':'Task output ranges'});for(const text of ['Token usage cards — review 3','past 30 days','Look at volume.','Include delegated work.','Keep zeros.','Verdict: keep','all pricing statuses'])assert.ok(report.includes(text),text);
});


test('cohort boxes sum tasks before comparing root effort, model family and their intersection',()=>{
 const rows=[fact('root',{total_tokens:1000}),fact('child',{workspace_id:'child',provider:'claude',model_family:'opus',effort:'low',total_tokens:100,delegated_session_id:'child'}),fact('second',{root_conversation_id:'second',total_tokens:2000}),fact('third',{root_conversation_id:'third',total_tokens:3000}),...Array.from({length:12},(_,i)=>fact(`sparse-${i}`,{root_conversation_id:'sparse',root_model_family:'gpt-6-astra',root_effort:'low',total_tokens:50})),...Array.from({length:3},(_,i)=>fact(`unknown-${i}`,{root_conversation_id:`unknown-${i}`,root_provider:null,root_model_family:null,root_effort:null,total_tokens:400}))];
 for(const id of ['task-effort-token-ranges','task-model-token-ranges','task-model-effort-token-ranges']){
  const r=filterBoxSamples(metric(id,rows),3);assert.equal(r.buckets.length,2,id);assert.ok(r.buckets.every(b=>b.distribution.summary.n===3),id);
  const known=r.buckets.find(b=>b.keys.includes('gpt-6-sol')||b.keys.includes('high'));assert.deepEqual([known.distribution.summary.min,known.distribution.summary.median,known.distribution.summary.max],[1100,2000,3000],id);
  assert.ok(r.buckets.some(b=>b.keys.every(key=>key===null)),id);assert.equal(table(id,rows).length,7,'sparse task stays in destination');
  const {recipe,query}=card(id);assert.ok(query.grouping.keys.includes('root_conversation_id'));assert.equal(recipe.cover.density,'compact');assert.equal(recipe.cover.minSamples,3);
 }
 assert.deepEqual(card('task-effort-token-ranges').query.grouping.keys,['root_effort','root_conversation_id']);assert.deepEqual(card('task-model-token-ranges').query.grouping.keys,['root_provider','root_model_family','root_conversation_id']);assert.deepEqual(card('task-model-effort-token-ranges').query.grouping.keys,['root_provider','root_model_family','root_effort','root_conversation_id']);
});

test('model tail scatter compares the median and maximum of whole-task totals with task counts',()=>{
 const rows=[fact('one',{total_tokens:10}),fact('child',{total_tokens:90,workspace_id:'child',model_family:'child model'}),fact('two',{root_conversation_id:'two',total_tokens:200}),fact('three',{root_conversation_id:'three',total_tokens:900})];
 const result=metric('model-task-token-spread',rows);assert.equal(result.buckets.length,1);assert.deepEqual([result.buckets[0].value,result.buckets[0].y,result.buckets[0].count],[200,900,3]);assert.equal(card('model-task-token-spread').recipe.cover.minSamples,3);
});

test('delegation scatters deduplicate canonical runs, count child transcript usage and preserve zero/complete delegation',()=>{
 const rows=[fact('root',{total_tokens:1000,delegated_session_id:''}),fact('handle',{total_tokens:100,delegated_session_id:'canonical-child',agent_session_id:'handle',workspace_id:'child',session_kind:'subagent'}),fact('child',{total_tokens:200,delegated_session_id:'canonical-child',agent_session_id:'child-transcript',workspace_id:'elsewhere',session_kind:'root',provider:'claude'}),fact('zero',{root_conversation_id:'zero',total_tokens:500}),fact('child-only',{root_conversation_id:'inactive-parent',total_tokens:600,delegated_session_id:'canonical-active-child'}),fact('unresolved',{root_conversation_id:null,total_tokens:99999,delegated_session_id:'orphan'})];
 for(const id of ['task-token-delegation-count','task-token-delegation-share']){
  const r=metric(id,rows);assert.equal(r.buckets.length,3,id);const root=r.buckets.find(b=>b.keys.at(-1)==='root'),zero=r.buckets.find(b=>b.keys.at(-1)==='zero'),childOnly=r.buckets.find(b=>b.keys.at(-1)==='inactive-parent');
  if(id.endsWith('count')){assert.deepEqual([root.value,root.y],[1,1300]);assert.equal(zero.value,0);assert.deepEqual([childOnly.value,childOnly.y],[1,600]);}else{assert.deepEqual([root.value,root.y],[1300,300/1300]);assert.equal(zero.y,0);assert.equal(childOnly.y,1);}
  const {recipe,query}=card(id),p=compactMetricPresentation({metrics:[{clause:cardCoverQuery(query,recipe).query.aggregations[0],result:r}],coverMetricId:'tu_tokens',fields:schema.fields});assert.equal(p.omittedLogPoints,id.endsWith('count')?1:0,id);assert.equal(table(id,rows).length,3);assert.equal(p.axisScales[id.endsWith('count')?'yScale':'xScale'].mode,'log');
 }
 const row=table('task-token-delegation-share',rows).find(r=>r.root_conversation_id==='root');assert.equal(row.tu_delegates,1);assert.equal(row.tu_delegated_tokens,300);
});

test('review retains saved definitions for dropped cards and avoids Model Choice scatter questions',()=>{
 const ids=tokenUsageTheses.flatMap(t=>t.cards.map(c=>c.id));for(const id of ['repository-input-output','token-time-placement','model-task-token-spread','task-token-delegation-count','task-token-delegation-share']){assert.equal(card(id).recipe.retired,true);assert.ok(!ids.includes(id));}
 for(const id of ['model-task-token-spread','task-token-delegation-count','task-token-delegation-share']){const {recipe,query}=card(id),metric=cardCoverQuery(query,recipe).query.aggregations[0];assert.ok(!JSON.stringify(metric).includes('cost_usd'));assert.ok(!JSON.stringify(metric).includes('input_tokens'));assert.equal(recipe.category,'usage');}
});


test('run effort boxes merge canonical executions and separate own effort and role without descendant duplication',()=>{
 const rows=[];
 for(let i=0;i<3;i++) {
  rows.push(fact(`standalone-${i}`,{root_conversation_id:`single-${i}`,run_role:'standalone',effort:'low',total_tokens:100}));
  rows.push(fact(`parent-${i}`,{root_conversation_id:`parent-${i}`,run_role:'parent',effort:'high',total_tokens:200}));
  rows.push(fact(`parent-continuation-${i}`,{root_conversation_id:`parent-${i}`,agent_session_id:'continuation',run_role:'parent',effort:'high',total_tokens:50}));
  rows.push(fact(`handle-${i}`,{root_conversation_id:`parent-${i}`,agent_session_id:'handle',delegated_session_id:`child-${i}`,run_role:'delegated',effort:'low',total_tokens:300}));
  rows.push(fact(`child-transcript-${i}`,{root_conversation_id:`parent-${i}`,agent_session_id:'child-transcript',delegated_session_id:`child-${i}`,run_role:'delegated',effort:'low',total_tokens:400}));
  rows.push(fact(`changed-effort-${i}`,{root_conversation_id:`parent-${i}`,delegated_session_id:`child-${i}`,run_role:'delegated',effort:'high',total_tokens:25}));
  rows.push(fact(`unknown-${i}`,{root_conversation_id:`unknown-${i}`,run_role:'standalone',effort:null,total_tokens:5}));
 }
 rows.push(fact('sparse',{root_conversation_id:'sparse',run_role:'standalone',effort:'ultra',total_tokens:500}));
 rows.push(fact('unresolved',{root_conversation_id:null,run_role:null,total_tokens:99999}));
 rows.push(fact('execution-cycle',{run_role:null,total_tokens:99999}));
 rows.push(fact('old',{root_conversation_id:'old',run_role:'standalone',effort:'low',first_usage_at:now-8*day,total_tokens:99999}));
 const all=metric('run-effort-token-ranges',rows),visible=filterBoxSamples(all,3);
 assert.equal(visible.buckets.length,5);
 for(const [keys,expected] of [[['low','standalone'],100],[['high','parent'],250],[['low','delegated'],700],[['high','delegated'],25],[[null,'standalone'],5]]) {
  const summary=visible.buckets.find(b=>JSON.stringify(b.keys)===JSON.stringify(keys)).distribution.summary;
  assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,expected,expected,expected]);
 }
 const destination=table('run-effort-token-ranges',rows);
 assert.equal(destination.length,16,'sparse observation remains inspectable');
 assert.equal(destination.reduce((n,r)=>n+r.tu_tokens,0),3740,'each valid period token contributes once, including changed effort');
 assert.deepEqual(card('run-effort-token-ranges').query.grouping.keys,['effort','run_role','root_conversation_id','delegated_session_id']);
 const {recipe,query}=card('run-effort-token-ranges'),original=structuredClone(visible);
 const shown=compactMetricPresentation({metrics:[{clause:cardCoverQuery(query,recipe).query.aggregations[0],result:visible}],coverMetricId:'tu_tokens',fields:schema.fields});
 assert.deepEqual(shown.metric.result.buckets.map(b=>b.keys),[['low','standalone'],['low','delegated'],['high','parent'],['high','delegated'],[null,'standalone']],'roles stay adjacent within increasing effort levels');
 assert.deepEqual(visible,original,'presentation ordering leaves the query results unchanged');
});

test('sparse skewed box whiskers meet interpolated quartiles without passing the true extremes',()=>{
 // The reported GPT 6.1 Sol Ultra cohort: Q3 lies past the last observed inlier.
 for(const values of [[28798,729273,3177124,1728195886],[0,100,101,102],[5,5,5],[1,2,3,4,5]]) {
  const summary=core.boxSummary(values,'tukey'), original=structuredClone(summary), extent=boxWhiskerExtent(summary);
  assert.ok(summary.min<=extent.low && extent.low<=summary.q1);
  assert.ok(summary.q3<=extent.high && extent.high<=summary.max);
  const axis=sharedBoxScale([{keys:[],value:summary.median,distribution:{kind:'box',summary}}],1,'log');
  for(const value of [extent.low,summary.q1,summary.median,summary.q3,extent.high,summary.max]) assert.ok(axis.fraction(value)>=0 && axis.fraction(value)<=1);
  assert.deepEqual(summary,original,'render geometry does not alter statistical values or classify new outliers');
 }
 const summary=core.boxSummary([28798,729273,3177124,1728195886],'tukey');
 assert.equal(summary.high,3177124);assert.equal(summary.q3,434431814.5);assert.deepEqual(summary.outliers,[1728195886]);
 const tooltip=boxDetails('Codex GPT 6.1 Sol Ultra',summary,'tokens');
 assert.match(tooltip,/Observed whisker range: 28,798 – 3,177,124/);assert.match(tooltip,/Max: 1,728,195,886/);assert.match(tooltip,/Interpolated quartile/);
});
