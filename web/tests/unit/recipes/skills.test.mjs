import test from 'node:test';
import assert from 'node:assert/strict';
import {recipeHarness} from '../helpers/recipes.mjs';
import {filterBoxSamples} from '../../../src/compact-box-model';
const now=Date.parse('2026-10-15T12:00:00Z'),{card,table,metric}=recipeHarness(now);
const evidence=(id,extra={})=>({id,created_at:now-1000,skill_name:'review',skill_path:'/skills/review/SKILL.md',evidence_type:'file_load',status:'ok',conversation_id:'one',tool_call_id:id,repository_name:'repo',title:'Work context',content_bytes:null,...extra});

test('skill reach deduplicates conversations and uses successful evidence while repository breadth requires named repositories',()=>{
 const rows=[evidence('a'),evidence('duplicate',{evidence_type:'explicit_invocation'}),evidence('two',{conversation_id:'two',repository_name:'other'}),evidence('failed',{conversation_id:'failed',repository_name:'third',status:'error'}),evidence('missing',{conversation_id:null}),evidence('blank-name',{skill_name:''}),evidence('unnamed-repo',{conversation_id:'three',repository_name:''}),evidence('old',{created_at:now-8*86400000,conversation_id:'old'})];
 const reach=table('skill-evidence',rows)[0];assert.equal(reach.card_conversation_id,3);assert.equal(reach.card_count,4);
 const repositories=table('skill-reach',rows);assert.equal(repositories.length,1);assert.equal(repositories[0].card_repository_name,2);assert.equal(repositories[0].card_conversation_id,2);
 assert.equal(table('skill-reach',[evidence('single')]).length,0);
});

test('body size boxes separate evidence types and do not substitute missing bytes',()=>{
 const rows=[100,1000,10000,0,null,-1].map((content_bytes,i)=>evidence(String(i),{content_bytes,evidence_type:'explicit_invocation'}));
 rows.push(evidence('sparse-a',{content_bytes:400,evidence_type:'harness_load'}),evidence('sparse-b',{content_bytes:800,evidence_type:'harness_load'}),evidence('failed',{content_bytes:999999,status:'error'}));
 const result=filterBoxSamples(metric('skill-body-sizes',rows),3);assert.equal(result.buckets.length,1);
 const summary=result.buckets[0].distribution.summary;assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,100,1000,10000]);
 assert.equal(table('skill-body-sizes',rows).length,2);
 assert.equal(card('skill-body-sizes').recipe.cover.sampleUnit,'body records');
});

test('file load distributions count distinct parent calls per conversation/path and include single loads',()=>{
 const rows=[1,2,5].flatMap((n,c)=>Array.from({length:n},(_,i)=>evidence(`${c}-${i}`,{conversation_id:`c-${c}`})));
 rows.push(evidence('duplicate',{conversation_id:'c-2',tool_call_id:'2-0'}),evidence('harness',{conversation_id:'c-2',evidence_type:'harness_load'}),evidence('failed',{conversation_id:'c-2',status:'error'}),evidence('missing',{tool_call_id:null}),evidence('blank',{skill_path:''}));
 const groups=table('skill-load-distribution',rows);assert.equal(groups.length,3);assert.deepEqual(groups.map(r=>r.si_loads).sort((a,b)=>a-b),[1,2,5]);
 const result=filterBoxSamples(metric('skill-load-distribution',rows),3),summary=result.buckets[0].distribution.summary;assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,1,2,5]);
 assert.equal(card('skill-load-distribution').recipe.cover.sampleUnit,'conversation/path groups');
 const single=Array.from({length:12},(_,i)=>evidence(String(i),{created_at:new Date(now-1000+i).toISOString()}));
 assert.equal(filterBoxSamples(metric('skill-load-distribution',single),3).buckets.length,0,'one conversation/path group is below the sample cutoff');
});
