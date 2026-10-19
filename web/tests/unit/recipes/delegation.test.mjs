import test from 'node:test';
import assert from 'node:assert/strict';
import {cardCoverQuery,recipeHarness} from '../helpers/recipes.mjs';
import {filterBoxSamples} from '../../../src/compact-box-model';
const now=Date.parse('2026-10-16T12:00:00Z'),day=86400000,{card,table,metric}=recipeHarness(now);
const fact=(id,extra={})=>({id,first_usage_at:now-1000,last_usage_at:new Date(now-1000).toISOString(),day:'2026-10-16',workspace_id:'work',repository_name:'child repo',root_repository_name:'root repo',root_conversation_id:'root',root_title:'Root task',agent_session_id:id,session_kind:'root',delegated_session_id:null,delegated_run_depth:0,total_tokens:100,price_status:'unpriced',...extra});
const population=()=>[fact('parent',{total_tokens:1000}),fact('handle',{delegated_session_id:'child',delegated_run_depth:1,total_tokens:100,session_kind:'subagent',price_status:'assumed'}),fact('transcript',{delegated_session_id:'child',delegated_run_depth:1,total_tokens:200,workspace_id:'elsewhere',price_status:'partial'}),fact('nested',{delegated_session_id:'nested',delegated_run_depth:2,total_tokens:400}),fact('single',{root_conversation_id:'single',total_tokens:500}),fact('child-only',{root_conversation_id:'inactive-root',delegated_session_id:'active-child',delegated_run_depth:3,total_tokens:600}),fact('zero-repo',{root_conversation_id:'zero',root_repository_name:'zero repo',total_tokens:300}),fact('unknown',{root_conversation_id:null,delegated_run_depth:null,total_tokens:99999}),fact('cycle',{delegated_session_id:'cycle',delegated_run_depth:null,total_tokens:99999}),fact('old',{first_usage_at:now-8*day,delegated_session_id:'old',delegated_run_depth:1,total_tokens:99999}),fact('future',{first_usage_at:now}),fact('zero',{total_tokens:0})];

test('token shares include linked root transcripts and all pricing statuses once with zero repositories retained',()=>{
 const rows=population();
 const r=table('repository-delegated-share',rows).find(r=>r.root_repository_name==='root repo');assert.deepEqual([r.dg_child_tokens,r.dg_tokens,r.dg_runs,r.dg_tasks],[1300,2800,3,3]);assert.equal(r.dg_share,1300/2800);assert.equal(table('repository-delegated-share',rows).find(r=>r.root_repository_name==='zero repo').dg_share,0);
 const {recipe,query}=card('repository-delegated-share');assert.equal(cardCoverQuery(query,recipe).query.aggregations[0].display.format.kind,'percent');
});

test('task participation weights root tasks equally, retaining zero-delegation and child-only roots',()=>{
 const rows=population(),m=metric('delegating-task-share',rows),r=m.buckets.find(b=>b.keys[0]==='root repo');assert.equal(r.value,2/3);assert.equal(r.count,3);assert.equal(m.buckets.find(b=>b.keys[0]==='zero repo').value,0);
 const tasks=table('delegating-task-share',rows);assert.equal(tasks.length,4);assert.equal(tasks.find(r=>r.root_conversation_id==='root').dg_runs,2);assert.equal(tasks.find(r=>r.root_conversation_id==='single').dg_runs,0);
});

test('depth counts canonical runs, merging handles and transcripts and retaining inactive ancestor depth',()=>{
 assert.deepEqual(metric('delegation-depth',population()).buckets.map(b=>[b.keys[0],b.value]),[[1,1],[2,1],[3,1]]);assert.equal(table('delegation-depth',population()).length,3);
});

test('run boxes sum canonical own usage before sampling',()=>{
 const rows=population(),r=filterBoxSamples(metric('delegated-run-token-ranges',rows),3);assert.equal(r.buckets.length,1);const s=r.buckets[0].distribution.summary;assert.deepEqual([s.n,s.min,s.median,s.max],[3,300,400,600]);
});

test('explicit TL1 revisits sum attempts as a separate population',()=>{
 const attempts=[{id:'a',project:'p',task_id:'task',revisit:true,attempt_count:3,started_at:now-1000},{id:'b',project:'p',task_id:'task',revisit:false,attempt_count:9,started_at:now-1000}];assert.equal(table('tl1-revisits',attempts)[0].card_attempt_count,3);
});
