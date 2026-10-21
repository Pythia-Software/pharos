#!/usr/bin/env node
// Combine an explicitly selected baseline and repair runs. Never treats a
// browser/harness interruption as coverage, and never publishes query values.
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { createRequire } from 'node:module';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const core = require('@pythia-software/query-table-core');
const stable = value => JSON.stringify(value, (_key, v) => v && typeof v === 'object' && !Array.isArray(v) ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))) : v);
const args=process.argv.slice(2), option=(name,fallback)=>{const index=args.indexOf(`--${name}`);return index<0?fallback:args[index+1];};
const runs=option('runs','audit-01').split(','), target=resolve(option('report','docs/usage-audit.md'));
const selected=new Map(), manifests=[];
for(const run of runs){
 const folder=resolve('.context/usage-audit',run), manifest=JSON.parse(readFileSync(join(folder,'manifest.json'),'utf8'));
 const cases=new Map(manifest.cases.map(item=>[item.id,item]));
 manifests.push({run,hash:manifest.sourceHash,backendHash:manifest.backendHash});
 for(const line of readFileSync(join(folder,'results.jsonl'),'utf8').split('\n').filter(Boolean)){
  const row=JSON.parse(line);
  const item=cases.get(row.id);
  if(!item)throw new Error(`${run}/${row.id}: missing manifest case`);
  if(row.errors.some(issue=>/browser\.newContext|browser has been closed|Target page, context/.test(issue)))throw new Error(`${run}/${row.id}: harness failure cannot count as audited coverage`);
  // A handled /metrics budget response followed by a successful V3
  // fallback is a recovery, not a failed visible view. Keep it explicit.
  const recoveries=row.errors.filter(issue=>/group population exceeds host budget/.test(issue)&&issue.startsWith('/api/query/')&&(row.requestTimings??[]).some(request=>request.path.endsWith('/query-v3'))&&!row.errors.some(other=>other==='group population exceeds host budget'));
  const urlIssues=[...row.urlIssues];
  if(item.query&&row.url){
   const decoded=core.decodeQueryStrict(new URL(row.url,'http://localhost').searchParams.get(`q_${item.dataset}`));
   if(decoded){
    const expected=core.normalizeQueryState(item.query),actual=core.normalizeQueryState(decoded);
    if(stable(actual.where)===stable(expected.where)){const i=urlIssues.indexOf('Query WHERE changed silently on load');if(i>=0)urlIssues.splice(i,1);}
    for(const key of Object.keys(expected))if(stable(actual[key])!==stable(expected[key]))urlIssues.push(`Query ${key} changed silently on load`);
   }
  }
  const linkIssues=[...row.linkIssues];
  if(row.page==='dashboard:tools'&&row.url){
   const final=new URL(row.url,'http://localhost');
   const requested=core.decodeQueryStrict(final.searchParams.get('analyze_filters')??'')?.where??item.where??[];
   const wanted=core.normalizeQueryState({...core.EMPTY_QUERY,where:requested.filter(term=>!('field'in term&&term.field==='day'))}).where;
   for(const link of row.linkTargets??[]){
    if(!/grouped|records/i.test(link.label))continue;
    const url=new URL(link.href,'http://localhost'),dataset=url.searchParams.get('analyze_data');
    const query=core.decodeQueryStrict(url.searchParams.get(`q_${dataset}`)??'');
    if(!query)continue;
    const actual=core.normalizeQueryState(query.grouping?.source??query).where;
    if(wanted.some(term=>!actual.some(candidate=>stable(candidate)===stable(term))))linkIssues.push(`${link.label}: source filter missing`);
   }
  }
  selected.set(row.id,{...row,run,caseURL:item.url,interactionRequested:item.interaction,recoveries,urlIssues:[...new Set(urlIssues)],linkIssues:[...new Set(linkIssues)],errors:row.errors.filter(issue=>!recoveries.includes(issue))});
 }
}
if(args.includes('--link-sources'))for(const line of readFileSync(resolve(option('link-sources')),'utf8').split('\n').filter(Boolean)){
 const source=JSON.parse(line),original=selected.get(source.id);
 if(!original||original.url!==source.url)throw new Error(`${source.id}: supplemental link source does not match the saved view URL`);
 original.linkTargets=source.linkTargets;
}
const flatLinks=args.includes('--links')?readFileSync(resolve(option('links')),'utf8').trim().split('\n').filter(Boolean).map(JSON.parse):[];
if(flatLinks.length){
 const byReference=new Map(flatLinks.flatMap(link=>link.references.map(ref=>[`${ref.id}\n${ref.label}`,link])));
 for(const row of selected.values()){
  row.linkIssues=row.linkIssues.filter(issue=>{
   const ref=(row.linkTargets??[]).find(link=>issue.startsWith(link.label+':')&&/records/i.test(link.label));
   return !ref||!byReference.has(`${row.id}\n${ref.label}`)||/source filter|missing decodable/.test(issue);
  });
  for(const link of row.linkTargets??[]){const checked=byReference.get(`${row.id}\n${link.label}`);if(checked&&!checked.ok)row.linkIssues.push(`${link.label}: ${checked.error}`);}
 }
}
const rows=[...selected.values()], pages=[...new Set(rows.map(row=>row.page))];
const pageLabels={
 'dashboard:tokens':'Tokens & cost dashboard','dashboard:human':'Human words dashboard','dashboard:time':'Agent time dashboard',
 'dashboard:parallelism':'Parallelism dashboard','dashboard:tools':'Tools dashboard','dashboard:mcp':'Pharos MCP dashboard',
 'dashboard:skills':'Skills dashboard','dashboard:carbon':'Carbon impact dashboard','query:usage':'Token usage query',
 'query:conversation_activity':'Conversation activity query','query:writing':'Human words by conversation query',
 'query:writing_messages':'Human messages query','query:tools':'Tool summary query','query:tool_calls':'Tool calls query',
 'query:mcp_calls':'MCP calls query','query:skill_usages':'Skill evidence query','query:parallelism':'Parallel activity query',
};
const distinctByPage=Object.fromEntries(pages.map(page=>[page,new Set(rows.filter(row=>row.page===page).map(row=>row.caseURL)).size]));
const complete=pages.length===17&&pages.every(page=>distinctByPage[page]>=25);
const band=ms=>ms>=30000?'Black':ms>=4000?'Red':ms>=1000?'Yellow':'Green';
const quantile=(values,p)=>values.length?(values.sort((a,b)=>a-b)[Math.ceil(p*values.length)-1]/1000).toFixed(2)+' s':'—';
const counts=Object.fromEntries(['Green','Yellow','Red','Black'].map(color=>[color,rows.filter(row=>row.band===color).length]));
const lines=['# Usage audit','',`Status: **${complete?'Complete':'In progress'}**. Updated ${new Date().toISOString()}. ${rows.length} completed cases across ${pages.length} pages. Selected runs: ${runs.map(run=>'`'+run+'`').join(', ')}.`, '',
`Primary views: **${Object.entries(counts).map(([color,count])=>`${count} ${color}`).join(', ')}**. ${rows.filter(row=>row.errors.length).length} cases returned an error or failed to settle. Fast error responses still count as errors.`, '',
'## Method','',
'The audit drives the real Chrome UI and service against the existing isolated development library. It runs serially, intercepts preference writes inside each browser context, and leaves the source library and saved settings unchanged. Each case varies valid dates, categories, keywords, numeric/null/OR filters, grouping, formulas, sorting, or dashboard controls. Empty results require a completed response. Fresh-context URL replays use different saved display preferences to detect hidden state.', '',
'Timings include data requests and rendering. A case uses the slowest of initial navigation, changed-control completion, and URL replay. **Green <1 s; Yellow 1–<4 s; Red 4–<30 s; Black ≥30 s without a settled result.** Errors are counted separately from latency. Linked queries have their own timing summary below. These are local measurements with warm service caches, not guarantees for other hardware or cold catalogs.', '',
'For views that settle, every distinct dashboard query/record link is executed. Up to five representative outgoing links per dashboard also open in fresh browsers. Equivalent queries and records/search links reuse a native execution within the frozen snapshot, including its original failure if it timed out. Native grouped results inspect a source group when one exists. Query pages check their dashboard return link. This samples the large combination space; it does not prove all possible queries work.', '',
'The complete canonical query is compared with the resulting URL: source predicates, columns, sorting, limits, offsets, metrics, grouping, and HAVING. Singleton-OR normalization is equivalent. Default, reduced-column, and paged Tool summary and Skill evidence cases also check row actions. Additional targeted cases cover chart-only legend choices, expanded series, and Parallelism selection/zoom. Passive distinct-value suggestions do not gate readiness.', '',
`${rows.filter(row=>row.replayMs!==undefined).length}/${rows.length} cases completed fresh-context replay. A timed-out primary view can block replay and outgoing checks; those unexercised checks are not passes.`, '',
'Error counts include failed filter-choice lookups (`/distinct`) even if the requested rows rendered. Those auxiliary failures are listed separately in the inventory. Successfully handled metric-budget fallbacks are recoveries rather than visible view failures.', '',
`${rows.filter(row=>row.interaction===true).length} requested control interactions completed. ${rows.filter(row=>row.interactionRequested&&row.interaction===false).length} optional interactions were unavailable in that filtered view; those cases still tested initial loading and URL replay.`, '',
'## View results','',
'| Page | Cases | Green | Yellow | Red | Black | Errors | URL issues | Link issues | p50 | p95 |',
'| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |'];
for(const page of Object.keys(pageLabels)){const values=rows.filter(row=>row.page===page);lines.push(`| ${pageLabels[page]} | ${values.length} | ${['Green','Yellow','Red','Black'].map(color=>values.filter(row=>row.band===color).length).join(' | ')} | ${values.filter(row=>row.errors.length).length} | ${values.filter(row=>row.urlIssues.length).length} | ${values.filter(row=>row.linkIssues.length).length} | ${quantile(values.map(row=>row.ms),.5)} | ${quantile(values.map(row=>row.ms),.95)} |`);}
const transitions=rows.flatMap(row=>row.transitions.filter(item=>item.kind!=='equivalent'&&(!flatLinks.length||!/records/i.test(item.label)||item.kind==='browser')).map(item=>({...item,id:row.id,page:row.page})));
for(const item of flatLinks)transitions.push({label:'Records / search records',kind:item.ok?'api-v2':'failed',ms:item.ms,id:item.references[0].id,page:rows.find(row=>row.id===item.references[0].id)?.page,error:item.error});
lines.push('','## Linked-query timing','',`Measured ${transitions.length} outgoing executions: ${['Green','Yellow','Red','Black'].map(color=>`${color} ${transitions.filter(item=>band(item.ms)===color).length}`).join('; ')}. p50 ${quantile(transitions.map(item=>item.ms),.5)}, p95 ${quantile(transitions.map(item=>item.ms),.95)}. Browser transitions include a fresh context; API transitions include native query preparation and execution.`,'','## Findings and next actions','',
`- Performance: ${rows.filter(row=>row.ms>=4000).length} primary views and ${transitions.filter(item=>item.ms>=4000).length} outgoing executions exceed the Yellow threshold.`,
`- Query execution: ${rows.filter(row=>row.errors.length).length} cases have errors; auxiliary metadata failures are included.`,
`- URL completeness: ${rows.filter(row=>row.urlIssues.length).length} cases have replay or query-state mismatches.`,
`- Cohesive transitions: ${rows.filter(row=>row.linkIssues.length).length} cases have unresolved filter or row-action issues.`, '',
'Repairs and implementation details are tracked in [usage-audit-repairs.md](usage-audit-repairs.md). The inventory below is generated only from the selected runs.', '',
'## Failure inventory','');
const recovered=rows.filter(row=>row.recoveries.length);
if(recovered.length)lines.push(`Handled metric-budget recoveries occurred in ${recovered.length} cases. Their full fallback duration remains in the timing bands; the initial diagnostic remains in private evidence.`, '');
const findings=new Map();
for(const row of rows)for(const [kind,issues] of [['Error',row.errors],['URL',row.urlIssues],['Link',row.linkIssues]])for(const issue of issues){
 const text=String(issue).replace(/https?:\/\/\S+/g,'[private URL]').replace(/\u001b\[[0-9;]*m/g,'').replace(/\s+/g,' ').trim();
 const key=kind+': '+text;const finding=findings.get(key)??{kind,text,ids:[]};finding.ids.push(row.id);findings.set(key,finding);
}
if(!findings.size)lines.push('No unresolved errors, URL replay mismatches, or filter-transition issues in the selected cases.');
else{lines.push('| Kind | Finding | Cases | Examples |','| --- | --- | ---: | --- |');for(const finding of findings.values())lines.push(`| ${finding.kind} | ${finding.text.replaceAll('|','\\|').slice(0,600)} | ${finding.ids.length} | ${finding.ids.slice(0,4).map(id=>'`'+id+'`').join(', ')} |`);}
const slow=rows.filter(row=>['Red','Black'].includes(row.band));
lines.push('','## Slow views','');
if(!slow.length)lines.push('No primary view exceeded four seconds.');
else{lines.push('| Case | Band | Time | API maximum |','| --- | --- | ---: | ---: |');for(const row of slow)lines.push(`| ${row.id} | ${row.band} | ${(row.ms/1000).toFixed(2)} s | ${row.apiMaxMs===undefined?'Incomplete':(row.apiMaxMs/1000).toFixed(2)+' s'} |`);}
const slowLinks=transitions.filter(item=>item.ms>=4000).sort((a,b)=>b.ms-a.ms);
lines.push('','## Slow outgoing queries','');
if(!slowLinks.length)lines.push('No successful outgoing query exceeded four seconds.');
else{lines.push('| Case | Transition | Mode | Time |','| --- | --- | --- | ---: |');for(const item of slowLinks)lines.push(`| ${item.id} | ${item.label.replaceAll('|','\\|')} | ${item.kind} | ${(item.ms/1000).toFixed(2)} s |`);}
lines.push('','## Reproduction and evidence','',
'Run `node tools/audit-usage.mjs --run NAME` against the existing dev copy. Use `--pages dashboard:tokens,query:usage` to target selected pages, `--cases 25` for the matrix, `--ids dashboard-carbon-06` for a specific reproduction, and the same run name to resume checkpoints. `--controls --cases 30` includes targeted control cases. `--origin` and `--config` select another isolated service. Only one audit runs at a time. `tools/summarize-usage-audit.mjs --runs BASELINE,CORRECTIONS,CONTROLS` combines explicitly selected evidence.', '',
`Use --in-place on a selected dashboard run to exercise live in-app transitions. The selected evidence is reproduced with tools/summarize-usage-audit.mjs --runs ${runs.join(',')}. Optional --summary FILE and --validation FILE add the machine-readable inventory and check results.`, '',
'Private manifests contain exact case URLs, sampled categories, frontend/backend hashes, and projection readiness. JSONL checkpoints contain request/transition timings and failure evidence under .context/usage-audit/<run>/. Query values and credentials are excluded from this report.', '',
'Ordinary record drilldowns execute through /rows-v2, matching the UI and its exact-search budget. Grouped drilldowns execute through native V3. Equivalent links reuse a verified execution within the frozen snapshot; browser transitions are measured separately.', '',
'The selected corrections exclude read-only Carbon labels from interactive legend checks and compare normalized predicates. A generator test caught repeated Agent time combinations; the affected cases were replaced with distinct time-bucket combinations. Completion requires at least 25 distinct initial parameter URLs on each page. Targeted control cases can share a starting URL while exercising different actions.', '',
...manifests.map(item=>`- ${item.run}: frontend SHA-256 \`${item.hash}\`${item.backendHash ? `; backend SHA-256 \`${item.backendHash}\`` : ""}.`), '');
if(flatLinks.length)lines.push('## Records transport correction','',`${flatLinks.length} distinct ordinary record queries were re-executed through the UI records endpoint: ${flatLinks.filter(item=>item.ok).length} succeeded and ${flatLinks.filter(item=>!item.ok).length} failed. Each result applies to every dashboard link with the same dataset and canonical query. Earlier raw V3 probe failures remain in private historical evidence only.`,'');
const navigation=args.includes('--navigation')?readFileSync(resolve(option('navigation')),'utf8').trim().split('\n').filter(Boolean).map(JSON.parse):[];
if(navigation.length){
 lines.push('## In-app navigation','', 'These additional checks clicked query links from filtered dashboards and compared the complete destination query after the in-app route settled. They supplement the primary timing matrix without replacing its measurements.','','| Case | Transition | Time | Result |','| --- | --- | ---: | --- |');
 for(const row of navigation){const checked=row.transitions.find(item=>item.kind==='browser-in-place'||item.kind==='failed');lines.push(`| ${row.id} | ${checked?.label?.replaceAll('|','\\|')??'Unavailable'} | ${checked?(checked.ms/1000).toFixed(2)+' s':'—'} | ${checked?.kind==='browser-in-place'?'Query preserved':checked?.error?.replaceAll('|','\\|')??'Blocked before transition'} |`);}
}
if(args.includes('--validation')){
 const checks=JSON.parse(readFileSync(resolve(option('validation')),'utf8'));
 lines.push('## Tooling validation','',...checks.map(check=>`- ${check.command}: ${check.result}.`),'');
}
writeFileSync(target,lines.join('\n'));
const summary={complete,runs,cases:rows.length,pages:pages.length,distinctByPage,counts,errors:rows.filter(row=>row.errors.length).length,urlIssues:rows.filter(row=>row.urlIssues.length).length,linkIssues:rows.filter(row=>row.linkIssues.length).length,transitions:transitions.length,flatLinkChecks:flatLinks.length,navigationChecks:navigation.length,findings:[...findings.values()]};
if(args.includes('--summary'))writeFileSync(resolve(option('summary')),JSON.stringify(summary,null,2));
console.log(JSON.stringify({...summary,findings:findings.size}));
