import test from 'node:test';
import assert from 'node:assert/strict';
import {build} from 'esbuild';
const built=await build({entryPoints:['src/coverage-review-model.ts'],bundle:true,write:false,platform:'node',format:'esm',outdir:'out'});
const {coverageDrafts,coverageTheses,coverageFeedback,coverageStatus,draftContext}=await import(`data:text/javascript;base64,${Buffer.from(built.outputFiles[0].text).toString('base64')}`);
const day=86400000,hour=3600000;
// Carbon: 1 g per output token and nothing else, so expected grams are token sums.
globalThis.window={addEventListener(){},removeEventListener(){},pharosCarbon:{estimate:(f,p)=>{let co2=0;const byCategory={};for(const c of Object.values(p.tokens))for(const [k,v] of Object.entries(c)){if(k==='output')co2+=v;byCategory[k]={wh:(byCategory[k]?.wh??0)+(k==='output'?v:0)};}return {pue:1,co2,facilityWh:co2,byCategory,grams:wh=>wh};},mass:g=>`${g} g`,energy:wh=>`${wh} Wh`}};
const draft=id=>coverageDrafts.find(card=>card.id===id);
const localDay=t=>{const d=new Date(t);return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;};
function stub(routes){const calls=[];globalThis.fetch=async(url,init)=>{calls.push([url,init?.body&&JSON.parse(init.body)]);const body=routes(url,init?.body&&JSON.parse(init.body));return {ok:true,status:200,text:async()=>JSON.stringify(body)};};return calls;}
const now=new Date(2026,9,16,15,0,0).getTime();

test('every review entry has exactly one draft with copy, a destination and a loader',()=>{
 const ids=coverageTheses.flatMap(t=>t.cards.map(c=>c.id));
 assert.equal(new Set(ids).size,ids.length);assert.deepEqual([...ids].sort(),coverageDrafts.map(c=>c.id).sort());
 for(const card of coverageDrafts){assert.ok(card.title(30)&&!card.title(30).includes('?'),card.id);assert.ok(card.description(30).length>30,card.id);assert.match(card.action,/^[A-Z]/);assert.ok(card.url(30).startsWith('/'));assert.equal(typeof card.load,'function');}
 for(const t of coverageTheses)for(const c of t.cards){assert.ok(c.rationale.length>20);assert.ok(c.calculation.length>60);assert.ok(coverageStatus[c.id],c.id);}
 for(const id of ['draft-parallel-summary','draft-carbon-everyday','draft-carbon-token-types','draft-library-glance','draft-library-freshness','draft-library-storage','draft-tl1-candidates','draft-tl1-human'])assert.equal(draft(id),undefined,`${id} was discarded in review 1`);
});

test('the report keeps card IDs and records verdicts',()=>{
 const feedback=coverageFeedback.readReviewFeedback(JSON.stringify({overall:'Keep carbon',theses:{carbon:'Good set'},cards:{'draft-carbon-trend':{verdict:'drop',note:'Too noisy'},unknown:{verdict:'keep',note:'x'}}}));
 assert.equal(coverageFeedback.reviewedCardCount(feedback),1);
 const report=coverageFeedback.exportFeedback(feedback,30,{'draft-carbon-trend':'Estimated CO₂e'});
 assert.match(report,/### Estimated CO₂e \(draft-carbon-trend\)/);assert.match(report,/Verdict: drop/);assert.match(report,/Too noisy/);assert.doesNotMatch(report,/unknown/);
});

test('carbon cards share one local-calendar period, so the trend endpoint equals the period total',async()=>{
 const series=[];for(let back=0;back<45;back++)series.push({day:localDay(now-back*day),value:'',tokens:{large:{output:back+1}}});
 const calls=stub((url,body)=>{const from=Date.parse(body.where[0].value),kept=series.filter(p=>Date.parse(`${p.day}T12:00:00`)>=from);return {factors:{tiers:[{key:'large',label:'Large models'}],equivalents:[]},windows:[{tokens:{large:{output:kept.reduce((n,p)=>n+p.tokens.large.output,0)}}}],series:kept,undated_tokens:0};});
 const context=draftContext(7,new AbortController().signal,now);
 const trend=await draft('draft-carbon-trend').load(context),tiers=await draft('draft-carbon-tiers').load(context);
 // Today and the six days before: 1 + … + 7 grams.
 assert.equal(trend.draft.study.value*1000,28);assert.equal(tiers.draft.rows[0].value,28);
 assert.equal(trend.draft.study.points.length,31);assert.equal(trend.draft.study.points[29]*1000,35);
 assert.equal(calls.filter(([url])=>url==='/api/query/usage/carbon').length,2,'cards reading the same request share it');
});

test('parallel hours split each day by concurrency level with 3+ on the baseline',async()=>{
 const base=new Date(2026,9,8,9,0,0).getTime(),tz=Intl.DateTimeFormat().resolvedOptions().timeZone;
 const thread=(id,workspace,start,end,extra={})=>({id,conversation_id:id,work_id:id,workspace_id:workspace,workspace_name:workspace,repository:'repo',title:id,provider:'claude',model:'opus',agent_depth:0,turns:[[start,end]],model_spans:[],...extra});
 // a: 9–11, b: 10–12, c (delegated, ignored in threaded mode): 9–12.
 const data={generated_at:new Date(now).toISOString(),timezone:tz,period_start:'2026-10-08',period_end:'2026-10-08',days:['2026-10-08'],threads:[thread('a','w1',base,base+2*hour),thread('b','w2',base+hour,base+3*hour),thread('c','w3',base,base+3*hour,{agent_depth:1})],coverage:{conversations:3,mirrors_removed:0,unexplained_waits_excluded:0,timing:'estimated',catalog:'test'}};
 stub(()=>data);const context=draftContext(7,new AbortController().signal,now);
 const days=await draft('draft-parallel-days').load(context);
 assert.deepEqual(days.draft.buckets[0].values,{one:2,two:1,more:0});assert.equal(days.headlineMetric.value,'33%');
 assert.deepEqual(days.draft.series.map(s=>s.key),['more','two','one'],'3+ is drawn first, on the baseline');assert.equal(days.draft.legendColumns,3);assert.match(days.evidence,/whole snapshot/);
 const hours=await draft('draft-parallel-hours').load(context);
 assert.equal(hours.draft.values[9],1);assert.equal(hours.draft.values[10],2);assert.equal(hours.draft.values[11],1);assert.equal(hours.draft.values[12],0);
});

test('parallelism cards explain a missing timing snapshot instead of failing',async()=>{
 globalThis.fetch=async()=>({ok:false,status:404,statusText:'Not Found',text:async()=>'{"error":"not found"}'});
 const result=await draft('draft-parallel-days').load(draftContext(7,new AbortController().signal,now));
 assert.equal(result.state,'empty');assert.match(result.emptyLabel,/timing snapshot/);
});

test('open findings rank by likely saving, falling back to impact when no estimate exists',async()=>{
 stub(()=>({settings:{rank:'usd'},summary:{open:3,watching:0,won:0,dismissed:0,snoozed:0,regressed:0,at_stake_usd:500,new:0,next_result_days:null,saved:{}},findings:[{id:'a',title:'A',detector_label:'D',change_label:'A line in the instructions',impact:{usd:400},expected:{usd:40}},{id:'b',title:'B',detector_label:'D',change_label:'A clearer handoff',impact:{usd:90}},{id:'c',title:'C',detector_label:'D',change_label:'A clearer handoff',impact:{usd:0},expected:{usd:0}},{id:'d',title:'D',detector_label:'D',change_label:'A clearer handoff',impact:{usd:20},expected:{usd:10}},{id:'e',title:'E',detector_label:'D',impact:{usd:5},expected:{usd:5}}]}));
 const context=draftContext(30,new AbortController().signal,now),result=await draft('draft-findings-open').load(context);
 assert.deepEqual(result.draft.rows.map(r=>r.label),['B','A','D'],'top three only');assert.equal(result.headlineMetric.value,'$145');
 const fixes=await draft('draft-findings-fixes').load(context);
 assert.deepEqual(fixes.draft.rows.map(r=>[r.label,r.value]),[['Clearer handoff',100],['Line in the instructions',40],['Other change',5]],'older services: the step without its article');
});

test('machine time refuses incomplete operation groups rather than showing a partial total',async()=>{
 stub(()=>({metrics:[{id:'time',buckets:[{keys:['test','2026-10-10'],value:hour,count:2}]},{id:'timed',buckets:[{keys:['test','2026-10-10'],value:2,count:2}]},{id:'population',buckets:[{keys:[],value:5}]}]}));
 await assert.rejects(draft('draft-machine-activities').load(draftContext(30,new AbortController().signal,now)),/incomplete/);
});

test('net savings subtract added context cost',async()=>{
 stub(()=>({summary:{open:0,watching:0,won:2,dismissed:0,snoozed:0,regressed:0,at_stake_usd:0,new:0,next_result_days:null,saved:{usd:120,added_usd:20,weeks:3}}}));
 const result=await draft('draft-findings-results').load(draftContext(30,new AbortController().signal,now));
 assert.equal(result.draft.kind,'value');assert.equal(result.draft.value,'$100');assert.match(result.draft.footnote,/3 weeks/);
});

test('carbon by model, provider and effort place each model family in its tier and sum to the same estimate',async()=>{
 const factors={tiers:[{key:'small',label:'Small',match:['haiku']},{key:'large',label:'Large',match:['opus']}],default_tier:'medium',equivalents:[]};
 const facts=[['opus-5-5','claude','high',30],['haiku-4','claude',null,5],['gpt-6-sol','codex','xhigh',7]];
 // Output-only calculator weights tiers: small ×1, medium ×2, large ×3.
 window.pharosCarbon.estimate=(f,p)=>{let co2=0;for(const [tier,c] of Object.entries(p.tokens))co2+=(c.output??0)*({small:1,medium:2,large:3}[tier]);return {pue:1,co2,facilityWh:co2,byCategory:{},grams:wh=>wh};};
 stub((url,body)=>url==='/api/query/usage/carbon'?{factors,windows:[{tokens:{}}],series:[],undated_tokens:0}:{metrics:[...body.aggregations.filter(a=>a.id!=='population').map(a=>({id:a.id,buckets:facts.map(([family,provider,effort,out])=>({keys:a.groupBy.length===1?[family]:[family,a.groupBy[1]==='provider'?provider:effort],value:a.field==='output_tokens'?out:0,count:1}))})),{id:'population',buckets:[{keys:[],value:3,count:3}]}]});
 const context=draftContext(30,new AbortController().signal,now);
 const models=await draft('draft-carbon-models').load(context),providers=await draft('draft-carbon-providers').load(context),effort=await draft('draft-carbon-effort').load(context);
 assert.deepEqual(models.draft.rows.map(r=>[r.label,r.value]),[['opus-5-5',90],['gpt-6-sol',14],['haiku-4',5]]);
 assert.deepEqual(providers.draft.rows.map(r=>[r.label,r.value]),[['Claude',95],['Codex',14]]);
 assert.deepEqual(effort.draft.rows.map(r=>[r.label,r.value]),[['high',90],['xhigh',14],['Unspecified',5]]);
});

test('savings by kind of fix group findings by lever, whatever their step wording',async()=>{
 stub(()=>({settings:{rank:'usd'},summary:{open:4},findings:[{id:'a',title:'A',lever:'repo-instructions',change_label:'A line in AGENTS.md',impact:{usd:30},expected:{usd:30}},{id:'b',title:'B',lever:'repo-instructions',change_label:'A map in the instructions',impact:{usd:10},expected:{usd:10}},{id:'c',title:'C',lever:'automation-prompt',change_label:'A clearer handoff',impact:{usd:25},expected:{usd:25}},{id:'d',title:'D',lever:'harness-settings',change_label:'A hook',impact:{usd:5},expected:{usd:5}}]}));
 const fixes=await draft('draft-findings-fixes').load(draftContext(30,new AbortController().signal,now));
 assert.deepEqual(fixes.draft.rows.map(r=>[r.label,r.value,r.detail]),[['Repository instructions',40,'2 open findings'],['Automation prompts',25,'1 open finding'],['Harness settings',5,'1 open finding']]);
});

test('machine time names six activities before folding Other, with a three-column key',async()=>{
 const acts=['read','test','build','edit','git','setup','delegate','plan'],today=new Date(now);today.setDate(today.getDate()-1);
 const dayKey=localDay(today.getTime()),rows=acts.map((a,i)=>({keys:[a,dayKey],value:(10-i)*hour,count:1}));
 stub((url,body)=>({metrics:[{id:'time',buckets:body.aggregations[0].groupBy.length?rows:[{keys:[],value:hour,count:8}]},{id:'timed',buckets:body.aggregations[0].groupBy.length?rows.map(r=>({...r,value:1})):[{keys:[],value:8,count:8}]},{id:'population',buckets:[{keys:[],value:8,count:8}]}]}));
 const result=await draft('draft-machine-time').load(draftContext(7,new AbortController().signal,now));
 assert.deepEqual(result.draft.series.map(s=>s.key),['read','test','build','edit','git','setup','other']);assert.equal(result.draft.legendColumns,3);
 assert.equal(result.draft.buckets.find(b=>b.key===dayKey).values.other,4+3,'Other holds delegate (4 h) and plan (3 h)');
});

test('tool call trends average seven complete days per endpoint and pool latency over timed calls',async()=>{
 // One call a day for the 30 days before yesterday; yesterday has 8 calls, 2 timed at 10 s and 20 s.
 const yesterday=new Date(now);yesterday.setDate(yesterday.getDate()-1);const y=localDay(yesterday.getTime());
 const calls=[],timed=[],time=[];for(let back=1;back<=37;back++){const d=new Date(now);d.setDate(d.getDate()-back);const key=localDay(d.getTime());calls.push({keys:[key],value:key===y?8:1,count:1});timed.push({keys:[key],value:key===y?2:1,count:1});time.push({keys:[key],value:key===y?30000:4000,count:1});}
 const requests=stub((url,body)=>{const keep=new Set(body.where[0].any.map(t=>t.value)),only=b=>b.filter(x=>keep.has(x.keys[0]));return {metrics:[{id:'calls',buckets:only(calls)},{id:'timed',buckets:only(timed)},{id:'time',buckets:only(time)}]};});
 const context=draftContext(30,new AbortController().signal,now);
 const volume=await draft('draft-machine-call-trend').load(context),latency=await draft('draft-machine-latency-trend').load(context);
 assert.equal(requests.length,2,'both trends share two day-list requests');assert.deepEqual(requests.map(r=>r[1].where[0].any.length),[7,30]);
 assert.equal(volume.draft.study.points.length,31);assert.equal(volume.draft.study.value,(8+6)/7);assert.equal(volume.draft.study.points[0],1);
 assert.equal(latency.draft.study.value,(30000+6*4000)/(2+6)/1000);assert.equal(latency.draft.study.points[0],4);
 const asked=requests.flatMap(r=>r[1].where[0].any.map(t=>t.value));assert.equal(asked.at(-1),y,'today is excluded');assert.equal(new Set(asked).size,37);
});

test('agents at once uses trailing seven captured days and the overlap card has no headline',async()=>{
 const tz=Intl.DateTimeFormat().resolvedOptions().timeZone,days=[];for(let i=0;i<9;i++)days.push(`2026-10-0${i+1}`);
 const at=(d,h)=>new Date(`${d}T${String(h).padStart(2,'0')}:00:00`).getTime();
 const t=(id,w,d,a,b)=>({id,conversation_id:id,work_id:id,workspace_id:w,workspace_name:w,repository:'repo',title:id,provider:'claude',model:'opus',agent_depth:0,turns:[[at(d,a),at(d,b)]],model_spans:[]});
 // Every day: w1 9–11; on the last day w2 also runs 9–11 (two at once).
 const threads=[...days.map(d=>t(`a${d}`,'w1',d,9,11)),t('b','w2',days.at(-1),9,11)];
 stub(()=>({generated_at:new Date(now).toISOString(),timezone:tz,period_start:days[0],period_end:days.at(-1),days,threads,coverage:{}}));
 const context=draftContext(30,new AbortController().signal,now),trendCard=await draft('draft-parallel-trend').load(context);
 assert.deepEqual(trendCard.draft.study.points,[1,1,(7*2+2)/(7*2)]);assert.equal(trendCard.draft.study.historyDays,2);assert.equal(trendCard.draft.study.baseline,1);
 const hours=await draft('draft-parallel-hours').load(context);
 assert.equal(hours.headlineMetric,undefined);assert.match(hours.evidence,/busiest at 09:00/);
});

test('carbon periods and trend endpoints step by calendar days across daylight saving',async()=>{
 const previous=process.env.TZ;process.env.TZ='America/Los_Angeles';
 try{
  // Clocks go forward on Mar 8, 2026, inside both the 30-day period and the trend.
  const march=new Date(2026,2,20,15,0,0).getTime(),key=t=>{const d=new Date(t);return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;};
  const series=[];for(let back=0;back<70;back++){const d=new Date(2026,2,20);d.setDate(d.getDate()-back);series.push({day:key(d.getTime()),value:'',tokens:{large:{output:1}}});}
  window.pharosCarbon.estimate=(f,p)=>{let co2=0;for(const c of Object.values(p.tokens))co2+=c.output??0;return {pue:1,co2,facilityWh:co2,byCategory:{},grams:wh=>wh};};
  const requests=stub((url,body)=>{const from=Date.parse(body.where[0].value),kept=series.filter(p=>Date.parse(`${p.day}T12:00:00`)>=from);return {factors:{tiers:[{key:'large',label:'Large models'}],equivalents:[]},windows:[{tokens:{large:{output:kept.length}}}],series:kept,undated_tokens:0};});
  const context=draftContext(30,new AbortController().signal,march);
  const trend=await draft('draft-carbon-trend').load(context),tiers=await draft('draft-carbon-tiers').load(context);
  const days=trend.draft.study.pointTimes.map(key);
  assert.equal(days.length,31);assert.ok(days.includes('2026-03-08'),'Mar 8 is an endpoint');
  for(let i=1;i<days.length;i++){const d=new Date(`${days[i-1]}T12:00:00`);d.setDate(d.getDate()+1);assert.equal(days[i],key(d.getTime()),`consecutive after ${days[i-1]}`);}
  assert.equal(days.at(-1),'2026-03-20');
  // Every endpoint sums exactly 30 calendar days (1 g each), and the latest equals the period total.
  assert.ok(trend.draft.study.points.every(value=>Math.abs(value*1000-30)<1e-9),'30 days per endpoint');
  assert.equal(tiers.draft.rows[0].value,30);
  const periodStart=requests.map(r=>r[1].where[0].value).find(v=>v.startsWith('2026-02-19'));
  assert.equal(periodStart,'2026-02-19T00:00:00-08:00','the period starts at local midnight');
 }finally{process.env.TZ=previous;if(previous===undefined)delete process.env.TZ;}
});
