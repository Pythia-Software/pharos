import test from 'node:test';
import assert from 'node:assert/strict';
import {cardCoverQuery,queryRecipes,recipeHarness,schemas} from '../helpers/recipes.mjs';
import {filterBoxSamples} from '../../../src/compact-box-model';
import {compactPieSlices} from '../../../src/compact-pie-model';
import {compactMetricPresentation} from '../../../src/card-presentation';
const schema=schemas.usage,now=Date.parse('2026-10-16T12:00:00Z'),day=86400000,{card,table,metric}=recipeHarness(now);
const fact=(id,extra={})=>({id,first_usage_at:now-1000,last_usage_at:new Date(now-1000).toISOString(),day:'2026-10-16',workspace_id:'work',repository_name:'repo',root_repository_name:'root repo',root_conversation_id:'root',root_title:'Root title',agent_session_id:'run',root_provider:'codex',root_model_family:'gpt-6-sol',root_effort:'high',delegated_session_id:null,total_tokens:100,input_tokens:90,output_tokens:10,price_status:'unpriced',attribution:'request',...extra});

test('usage cards bound one rolling window and never filter by price status',()=>{
 for(const recipe of queryRecipes.filter(r=>r.category==='usage'&&!r.retired))for(const days of [7,30]){const {query}=card(recipe.id,days);assert.equal(query.grouping.source.where.filter(t=>t.field==='first_usage_at').length,2,recipe.id);assert.ok(!query.grouping.source.where.some(t=>t.field==='price_status'),recipe.id);}
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
