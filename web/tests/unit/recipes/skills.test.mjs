import test from 'node:test';
import assert from 'node:assert/strict';
import * as core from '@pythia-software/query-table-core';
import {schemas} from '../helpers/recipes.mjs';
import {queryRecipes,instantiateRecipe} from '../../../src/curiosity-data';
import {cardCoverQuery} from '../../../src/card-cover';
import {skillsInstructionsTheses,skillsInstructionsReviewKey,skillsInstructionsFeedback} from '../../../src/skills-instructions-review-model';
import {filterBoxSamples} from '../../../src/compact-box-model';
import {compactPieSlices} from '../../../src/compact-pie-model';
const schema=schemas.skill_usages,now=Date.parse('2026-10-15T12:00:00Z');
const caps={version:3,profile:'qt-sqlite-v1',groupedRows:true,calendarGrouping:true,computedSourceFilters:true,maxRows:10000,maxGroups:10000,maxPopulation:250000},execution={profile:'qt-sqlite-v1',fields:{},resolvedRevisions:{},planToken:''};
const evidence=(id,extra={})=>({id,created_at:now-1000,skill_name:'review',skill_path:'/skills/review/SKILL.md',evidence_type:'file_load',status:'ok',conversation_id:'one',tool_call_id:id,repository_name:'repo',title:'Work context',content_bytes:null,...extra});
function card(id,days=7){const recipe=queryRecipes.find(r=>r.id===id);return{recipe,query:instantiateRecipe(recipe,days,new Date(now)).query};}
function table(id,rows){return core.applyQuery(rows,card(id).query,schema,{now}).rows;}
function metric(id,rows){const {recipe,query}=card(id),cover=cardCoverQuery(query,recipe);return core.evaluateMetrics(rows,cover.query,schema,{now}).metrics.find(m=>m.id===cover.metricId);}

test('skills review covers every active skill definition and compiles all covers/destinations',()=>{
 const ids=skillsInstructionsTheses.flatMap(t=>t.cards.map(c=>c.id));
 assert.deepEqual(ids,["skill-evidence","skill-reach","skill-body-sizes","skill-load-distribution"]);
 assert.deepEqual([...ids].sort(),queryRecipes.filter(r=>r.category==='skills'&&!r.retired).map(r=>r.id).sort());
 for(const id of ids){const {recipe,query}=card(id,30);for(const state of [query,cardCoverQuery(query,recipe).query]) assert.deepEqual(core.toQueryRequestV3(state,schema,caps,execution,{now,rows:false}).diagnostics,[],id);assert.ok(instantiateRecipe(recipe,30).description.includes('30'),id);assert.ok(recipe.action,id);}
 for(const id of ['skill-evidence-mix','instruction-bytes','skill-size-coverage','skill-reloads','largest-skill-load-count']) {
  const {recipe,query}=card(id,30);assert.equal(recipe.retired,true,id);
  for(const state of [query,cardCoverQuery(query,recipe).query]) assert.deepEqual(core.toQueryRequestV3(state,schema,caps,execution,{now,rows:false}).diagnostics,[],`${id} saved definition remains executable`);
 }
});

test('skill reach deduplicates conversations and uses successful evidence while repository breadth requires named repositories',()=>{
 const rows=[evidence('a'),evidence('duplicate',{evidence_type:'explicit_invocation'}),evidence('two',{conversation_id:'two',repository_name:'other'}),evidence('failed',{conversation_id:'failed',repository_name:'third',status:'error'}),evidence('missing',{conversation_id:null}),evidence('blank-name',{skill_name:''}),evidence('unnamed-repo',{conversation_id:'three',repository_name:''}),evidence('old',{created_at:now-8*86400000,conversation_id:'old'})];
 const reach=table('skill-evidence',rows)[0];assert.equal(reach.card_conversation_id,3);assert.equal(reach.card_count,4);
 const repositories=table('skill-reach',rows);assert.equal(repositories.length,1);assert.equal(repositories[0].card_repository_name,2);assert.equal(repositories[0].card_conversation_id,2);
 assert.equal(table('skill-reach',[evidence('single')]).length,0);
});

test('evidence composition counts records of every status rather than inventing unique skill uses',()=>{
 const rows=[evidence('a'),evidence('b',{status:'error'}),evidence('c',{status:'no_result'}),evidence('d',{evidence_type:'explicit_invocation',tool_call_id:'a'}),evidence('e',{evidence_type:'harness_load',tool_call_id:null})];
 const pie=compactPieSlices(metric('skill-evidence-mix',rows),3);assert.equal(pie.total,5);assert.equal(pie.slices.length,3);
 assert.equal(table('skill-evidence-mix',rows).length,5);
});

test('body volume and size boxes separate evidence types and do not substitute missing bytes',()=>{
 const rows=[100,1000,10000,0,null,-1].map((content_bytes,i)=>evidence(String(i),{content_bytes,evidence_type:'explicit_invocation'}));
 rows.push(evidence('sparse-a',{content_bytes:400,evidence_type:'harness_load'}),evidence('sparse-b',{content_bytes:800,evidence_type:'harness_load'}),evidence('failed',{content_bytes:999999,status:'error'}));
 const volume=table('instruction-bytes',rows);assert.equal(volume.length,2);assert.equal(volume.find(r=>r.evidence_type==='explicit_invocation').card_content_bytes,11100);
 const result=filterBoxSamples(metric('skill-body-sizes',rows),3);assert.equal(result.buckets.length,1);
 const summary=result.buckets[0].distribution.summary;assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,100,1000,10000]);
 assert.equal(table('skill-body-sizes',rows).length,2);
 assert.equal(card('skill-body-sizes').recipe.cover.sampleUnit,'body records');
});

test('coverage distinguishes zero-sized bodies, missing sizes and failed observations without hiding sparse types',()=>{
 const rows=[0,100,null,-1].map((content_bytes,i)=>evidence(String(i),{content_bytes,evidence_type:'explicit_invocation'}));
 rows.push(evidence('file'),evidence('failed',{status:'error',content_bytes:1}),evidence('harness',{evidence_type:'harness_load',content_bytes:200}));
 const result=table('skill-size-coverage',rows);assert.equal(result.length,3);
 const explicit=result.find(r=>r.evidence_type==='explicit_invocation');assert.equal(explicit.si_size_coverage,.5);assert.equal(explicit.si_sized,2);assert.equal(explicit.si_records,4);
 assert.equal(result.find(r=>r.evidence_type==='file_load').si_size_coverage,0);
 assert.equal(result.find(r=>r.evidence_type==='harness_load').si_size_coverage,1);
});

test('file load distributions count distinct parent calls per conversation/path and include single loads',()=>{
 const rows=[1,2,5].flatMap((n,c)=>Array.from({length:n},(_,i)=>evidence(`${c}-${i}`,{conversation_id:`c-${c}`})));
 rows.push(evidence('duplicate',{conversation_id:'c-2',tool_call_id:'2-0'}),evidence('harness',{conversation_id:'c-2',evidence_type:'harness_load'}),evidence('failed',{conversation_id:'c-2',status:'error'}),evidence('missing',{tool_call_id:null}),evidence('blank',{skill_path:''}));
 const groups=table('skill-load-distribution',rows);assert.equal(groups.length,3);assert.deepEqual(groups.map(r=>r.si_loads).sort((a,b)=>a-b),[1,2,5]);
 const result=filterBoxSamples(metric('skill-load-distribution',rows),3),summary=result.buckets[0].distribution.summary;assert.deepEqual([summary.n,summary.min,summary.median,summary.max],[3,1,2,5]);
 assert.equal(card('skill-load-distribution').recipe.cover.sampleUnit,'conversation/path groups');
 const repeated=table('skill-reloads',rows);assert.equal(repeated.length,2);assert.deepEqual(repeated.map(r=>r.card_tool_call_id),[5,2]);
});

test('rare file-load maxima remain visible and bodies are not required to count observed loads',()=>{
 const rows=Array.from({length:12},(_,i)=>evidence(String(i),{created_at:new Date(now-1000+i).toISOString()}));
 const result=metric('largest-skill-load-count',rows);assert.equal(result.buckets[0].value,12);assert.deepEqual(result.buckets[0].keys,[]);
 const destination=table('largest-skill-load-count',rows)[0];assert.equal(destination.si_loads,12);assert.equal(destination.si_call,'11');assert.equal(destination.si_title,'Work context');
 assert.equal(filterBoxSamples(metric('skill-load-distribution',rows),3).buckets.length,0);
});

test('skill bodies use the chosen rolling window and preserve original source metric IDs',()=>{
 const rows=[evidence('old',{created_at:now-8*86400000,content_bytes:1024,evidence_type:'explicit_invocation'})];assert.equal(table('instruction-bytes',rows).length,0);
 const {recipe,query}=card('instruction-bytes',30);assert.equal(core.applyQuery(rows,query,schema,{now}).rows[0].card_content_bytes,1024);
 for(const [id,metricId] of [['instruction-bytes','skill-bytes'],['skill-reloads','skill-file-loads'],['skill-reach','skill-repositories']]) assert.ok(card(id).recipe.measures.some(m=>m.id===metricId));
});

test('skills feedback is isolated and exports card/group/set notes with the body window',()=>{
 assert.equal(skillsInstructionsReviewKey,'pharos-skills-instructions-review-v2');
 const draft=skillsInstructionsFeedback.readReviewFeedback(JSON.stringify({overall:'Keep the ranges clear.',theses:{bodies:'Compare sizes with load counts.'},cards:{'skill-body-sizes':{verdict:'keep',note:'Keep ranges compact.'},'mcp-response-size':{verdict:'drop',note:'Other study'},'skill-evidence-mix':{verdict:'drop',note:'Earlier review'}}}));
 assert.equal(draft.cards['mcp-response-size'],undefined);
 assert.equal(draft.cards['skill-evidence-mix'],undefined);
 assert.equal(skillsInstructionsFeedback.reviewedCardCount(draft),1);
 assert.doesNotMatch(skillsInstructionsFeedback.exportFeedback(draft,7),/evidence pie|coverage counts|two body-size charts|Earlier review/);
 const report=skillsInstructionsFeedback.exportFeedback(draft,7,{'skill-body-sizes':'Skill body sizes'},{'skill-body-sizes':30});
 for(const text of ['Skills and instructions cards — review 2','past 7 days','past 30 days','Keep the ranges clear.','Compare sizes with load counts.','Keep ranges compact.','Verdict: keep']) assert.ok(report.includes(text),text);
});
