import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {build} from 'esbuild';
import * as core from '@pythia-software/query-table-core';
const built=await build({entryPoints:['src/curiosity-data.ts','src/card-cover.ts'],bundle:true,write:false,platform:'node',format:'esm',outdir:'out'});
const mod=name=>import(`data:text/javascript;base64,${Buffer.from(built.outputFiles.find(f=>f.path.endsWith(`/${name}.js`)).text).toString('base64')}`);
const {queryRecipes,instantiateRecipe,suggestionPool}=await mod('curiosity-data'),{cardCoverQuery}=await mod('card-cover');
const schema=core.loadSchema(JSON.parse(readFileSync('../schemas/writing_messages.schema.json','utf8')));
const now=Date.parse('2026-10-16T12:00:00Z'),day=86400000;
const caps={version:3,profile:'qt-sqlite-v1',groupedRows:true,calendarGrouping:true,computedSourceFilters:true,maxRows:10000,maxGroups:10000,maxPopulation:250000},execution={profile:'qt-sqlite-v1',fields:{},resolvedRevisions:{},planToken:''};
const message=(id,extra={})=>({id,sent_at:now-1000,text:'I meant the other index; great work on the parser.',typed_words:11,other_words:0,correction_cue:true,positive_cue:true,resent_words:0,pasted_words:0,quoted_words:0,template_words:0,attachment_words:0,harness_words:0,automated_words:0,repository_name:'repo',conversation_id:'one',title:'Index work',...extra});
function card(id,days=7){const recipe=queryRecipes.find(r=>r.id===id);return {recipe,query:instantiateRecipe(recipe,days,new Date(now)).query};}
function table(id,rows){return core.applyQuery(rows,card(id).query,schema,{now}).rows;}
function metric(id,rows){const {recipe,query}=card(id),cover=cardCoverQuery(query,recipe);return core.evaluateMetrics(rows,cover.query,schema,{now}).metrics.find(m=>m.id===cover.metricId);}

test('retired steering definitions remain executable but never enter discovery',()=>{
 const ids=['correction-phrases','correction-conversation-share','positive-feedback','positive-conversation-share','repeated-messages','within-conversation-repeats'];assert.equal(queryRecipes.filter(r=>r.category==='steering'&&!r.retired).length,0);assert.ok(!suggestionPool(new Date(now),queryRecipes).some(r=>r.category==='steering'));for(const id of ids)assert.equal(card(id).recipe.retired,true,id);
 for(const id of ids)for(const days of [1,7,30,90]){const {recipe,query}=card(id,days);for(const state of [query,cardCoverQuery(query,recipe).query])assert.deepEqual(core.toQueryRequestV3(state,schema,caps,execution,{now,rows:false}).diagnostics,[],id);assert.ok(query.grouping.source.where.some(t=>t.field==='sent_at'&&t.value===`-${days}d`));assert.ok(instantiateRecipe(recipe,days).description.includes(String(days)));assert.ok(recipe.action&&!recipe.title.includes('?'));}
 assert.equal(card('positive-feedback').recipe.measures[0].id,'feedback-messages');assert.equal(card('repeated-messages').recipe.measures[0].id,'message-reach');
});

test('cue flags drive counts once per message and retain authored evidence within mixed text',()=>{
 const rows=[message('one'),message('multiple',{text:'YOU MISUNDERSTOOD. I meant the path; that’s not what I asked.',positive_cue:false}),message('positive',{text:'Great job, thank you; this works!',correction_cue:false}),message('neutral',{text:'Please inspect the parser.',correction_cue:false,positive_cue:false}),message('mixed',{other_words:10,pasted_words:10,correction_cue:false,positive_cue:false}),message('mixed-own',{other_words:10,pasted_words:10,positive_cue:false}),message('machine',{typed_words:0,other_words:10,automated_words:10}),message('old',{sent_at:now-8*day}),message('future',{sent_at:now}),message('blank',{conversation_id:''}),message('missing',{conversation_id:null})];
 assert.equal(table('correction-phrases',rows)[0].card_count,3);assert.equal(table('positive-feedback',rows)[0].card_count,2);assert.equal(table('correction-conversation-share',rows)[0].st_corrections,3);assert.equal(table('positive-conversation-share',rows)[0].st_positive,2);assert.equal(table('correction-conversation-share',rows)[0].st_messages,6);
 const overlap=[message('negated',{text:"That's not what I asked; I did not say great work."})];assert.equal(table('correction-phrases',overlap)[0].card_count,1);assert.equal(table('positive-feedback',overlap)[0].card_count,1);
});

test('conversation shares weight transcripts equally and retain sparse and zero-cue observations',()=>{
 const rows=Array.from({length:20},(_,i)=>message(String(i)));rows.push(message('neutral',{conversation_id:'two',text:'Please inspect the parser.',correction_cue:false,positive_cue:false}),message('clean',{repository_name:'clean',text:'Please inspect the parser.',correction_cue:false,positive_cue:false}),message('mixed-only',{repository_name:'absent',typed_words:0,other_words:10}),message('unmapped',{repository_name:null,text:'Please inspect the parser.',correction_cue:false,positive_cue:false}));
 for(const id of ['correction-conversation-share','positive-conversation-share']){const result=metric(id,rows);assert.equal(result.buckets.find(b=>b.keys[0]==='repo').value,.5);assert.equal(result.buckets.find(b=>b.keys[0]==='repo').count,2);assert.equal(result.buckets.find(b=>b.keys[0]==='clean').value,0);assert.ok(result.buckets.some(b=>b.keys[0]===null));assert.ok(!result.buckets.some(b=>b.keys[0]==='absent'));assert.equal(table(id,rows).length,4);}
});

test('cross-conversation and local repeats use distinct grains, exclude context, and preserve exact text',()=>{
 const rows=[message('a'),message('b',{conversation_id:'two'}),message('c',{conversation_id:'three'}),message('d'),message('resent',{typed_words:0,resent_words:11,other_words:11}),message('machine',{automated_words:11}),message('mixed',{pasted_words:1}),message('blank',{conversation_id:''}),message('missing',{conversation_id:null})];rows.push(...Array.from({length:5},(_,i)=>message(`local-${i}`,{text:'Continue',conversation_id:'local'})));rows.push(message('case',{conversation_id:'case',text:'continue'}),message('space',{conversation_id:'space',text:'Continue '}),message('old',{sent_at:now-8*day}),message('future',{sent_at:now}));
 const across=table('repeated-messages',rows);assert.equal(across.length,1);assert.deepEqual([across[0].card_conversation_id,across[0].card_count],[3,5]);assert.deepEqual(table('within-conversation-repeats',rows).map(r=>[r.conversation_id,r.card_count]),[['local',5],['one',3]]);
});

test('latest matching excerpt stays tied to its repository and conversation',()=>{
 const rows=[message('old',{sent_at:now-10000,text:'I meant the old index.'}),message('latest',{sent_at:now-2000,text:'You missed the new index.'}),message('neutral',{sent_at:now-1000,text:'Please continue.',correction_cue:false,positive_cue:false}),message('other',{repository_name:'other',text:'I meant another repository.'})];const result=table('correction-phrases',rows);assert.equal(result.length,2);assert.equal(result.find(r=>r.repository_name==='repo').st_excerpt,'You missed the new index.');
});

