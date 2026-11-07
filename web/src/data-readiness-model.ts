// The processing ledger owns freshness. These are conservative display scopes,
// not result-revision receipts: until the query API supplies those, the modal
// identifies this distinction and never infers lag from a publication time.
export type FreshnessRow = {
  domain: string; label: string; group?: string; as_of?: string | null;
  behind: {state: string; count?: number | null; unit?: string | null};
  reason?: {code: string; text: string; detail?: string} | null;
  catching_up?: string | null; catching_up_label?: string | null; next?: string | null; next_at?: string | null;
  pending_since?: string | null; output_missing?: boolean; task_id?: string;
};
export type ProcessingItem = {id: string; label: string; cadence?: string; cadence_label?: string; state: string; state_label?: string; next_at?: string | null; as_of?: string | null; why?: string; feeds?: string; error?: string | null};
export type ProcessingOperation = {id: string; kind?: string; state: string; label: string; detail?: string; step?: {done?: number | null; total?: number | null; unit?: string | null; name?: string; fraction?: number | null} | null; children?: ProcessingOperation[]};
export type ReadinessSnapshot = {
  generated_at?: string; freshness?: FreshnessRow[]; operations?: ProcessingOperation[];
  background?: {mode?: string; items?: ProcessingItem[]; cadences?: Array<{id: string; label: string; next_at?: string | null; description?: string}>};
  resources?: {cpu_cores?: number | null; [key: string]: unknown};
};
export type ReadinessScope = {id: string; title: string; dataset: string; query?: unknown; updatedAt?: number; refreshing?: boolean; refreshError?: string; empty?: boolean};
export type ReadinessStage = {id: string; label: string; state: string; reason: string; next: string; nextAt?: string | null; count?: number | null; unit?: string | null; done?: number | null; total?: number | null; operation?: ProcessingOperation; item?: ProcessingItem; row?: FreshnessRow};
export type ReadinessTone = 'current' | 'minutes' | 'recent' | 'hours' | 'orange' | 'days' | 'unknown';
export type ReadinessModel = {kind: 'current'|'scheduled'|'behind'|'missing'|'unknown'|'saved'; tone: ReadinessTone; lagMs: number | null; label: string; sentence: string; checkedAt: number | null; nextAt: number | null; stages: ReadinessStage[]; scopes: ReadinessScope[]; preview: boolean};
export const readinessScenarios = [
  ['clear','Up to date'],['minutes','3 minutes behind'],['recent','45 minutes behind'],['hours','3 hours behind'],['orange','9 hours behind'],['days','2 days behind'],['missing','First results not ready'],['running','Update in progress'],['paused','Automatic updates paused'],['failed','Last update failed'],['unknown','Update status unavailable'],
] as const;
export type ReadinessScenario = typeof readinessScenarios[number][0];
// Delay comes from observed input, not the last time a job ran. Unknown age
// stays neutral. Up to two hours is neutral; under 15 minutes is expected.
export function readinessTone(lag: number | null): ReadinessTone {
  return lag===null?'unknown':lag>1440*minute?'days':lag>480*minute?'orange':lag>120*minute?'hours':lag>=15*minute?'recent':'minutes';
}
const minute = 60_000;
export const timeValue = (value?: string | null) => {const result=value?Date.parse(value):NaN;return Number.isFinite(result)?result:null;};
export function readinessAge(ms: number) { return ms<minute?'less than a minute':ms<60*minute?`${Math.floor(ms/minute)} minute${ms<2*minute?'':'s'}`:ms<1440*minute?`${Math.floor(ms/(60*minute))} hour${ms<120*minute?'':'s'}`:`${Math.floor(ms/(1440*minute))} day${ms<2880*minute?'':'s'}`; }
const flatOperations = (items: ProcessingOperation[]): ProcessingOperation[] => items.flatMap(item=>[item,...flatOperations(item.children??[])]);

// Optional label fields are dependencies even when the base dataset stays the
// same (including where/grouping/metric expressions). Unknown datasets stay
// unknown. These declarations are explicitly provisional; the executed query
// must eventually return its own authoritative dependency receipt.
// Captures are optional: include the ledger's evidence when present without
// requiring capture history in libraries that only read live sources.
const sources=['conv:*','capt:*'];
const domainScopes: Record<string,string[]> = {
  library:[...sources,'view'], usage:sources, model_requests:[...sources,'an.ledger'], compactions:sources,
  conversation_activity:[...sources,'an.authorship'],writing:[...sources,'an.authorship'],writing_messages:[...sources,'an.authorship'],
  tools:[...sources,'an.ledger','an.tools'],tool_calls:[...sources,'an.ledger'],skill_usages:[...sources,'an.ledger'],
  parallelism:sources,rhythm:[...sources,'an.authorship'],tl1_attempts:sources,
  session_shape:[...sources,'an.ledger','an.authorship'],
  message_labels:[...sources,'an.authorship','@labels'],people_message_labels:[...sources,'an.authorship','@labels'],
  agent_events:[...sources,'@agent_events'],agent_event_messages:[...sources,'@agent_events'],
  findings:[...sources,'findings'],safety:[...sources,'an.ledger','findings'],fixes:[...sources,'@agent_events','findings'],
  mcp_calls:['@mcp'],
};
function usesLabels(value: unknown): boolean {
  if(typeof value==='string')return /(?:message_label|label_family|people_label|\[label\])/i.test(value);
  if(Array.isArray(value))return value.some(usesLabels);
  if(value&&typeof value==='object')return Object.values(value).some(usesLabels);
  return false;
}
export function readinessDomains(scope: Pick<ReadinessScope,'dataset'|'query'>): string[] {
  const base=domainScopes[scope.dataset];
  if(!base)return ['@unknown:'+scope.dataset];
  return [...new Set([...base,...usesLabels(scope.query)?['an.authorship','@labels']:[]])];
}
const matches=(pattern: string,domain: string)=>pattern.endsWith('*')?domain.startsWith(pattern.slice(0,-1)):pattern===domain;
// Source names don't identify their importer. Only the service knows whether
// a source supports incremental reads or belongs to the whole-source task.
// Library upgrade rebuilds the versioned conversation ledger (including model
// requests). Tools & usage only rebuilds its rollups.
const itemFor=(domain: string,taskID?:string)=>taskID??(domain==='view'||domain.startsWith('search.')?'search':({'an.authorship':'writing','an.ledger':'upgrade','an.tools':'tools','findings':'findings','@labels':'labels','@agent_events':'agent_events'} as Record<string,string>)[domain]);
// Copy belongs to the display boundary: keep processing identifiers and raw
// service diagnostics in the snapshot, and describe their meaning here.
export function readinessStageLabel(id:string, label?:string):string {
  if(id.startsWith('conv:'))return id==='conv:*'?'Read new conversations':(label??'Conversations').replace(/^Conversations/,'Read new conversations');
  if(id.startsWith('capt:'))return (label??'Captures').replace(/^Captures/,'Read saved conversations');
  return ({view:'Update search results','an.authorship':'Estimate your writing','an.authorship.refined':'Refine writing estimates','an.ledger':'Read tool calls','an.tools':'Summarize tool use',findings:'Find patterns in your work','@labels':'Label messages','@agent_events':'Find events in agent messages','@mcp':'Read calls to Pharos'})[id]??'Check for updates';
}
const stageReasons:Record<string,string>={
  indexing:'Reading new or changed conversations.', 'capture-running':'Saving conversation files.',
  refreshing:'Updating these results.', building:'Preparing these results.', running:'Updating these results.',
  'never-indexed':'Conversations need to be read before results can appear.',
  'source-error':'Pharos couldn’t read the latest conversations.',
  'capture-busy':'Waiting for conversation files to finish saving.',
  'needs-capture':'New conversations are waiting to be added.',
  'schedule-off':'Automatic updates are off for this step.',
  'manual-only':'This step runs when you request an update in settings.',
  'scheduled-frequent':'Waiting for the next regular update.',
  'scheduled-hourly':'Waiting for the next hourly update.',
  'scheduled-daily':'Waiting for the next daily update.',
  late:'The scheduled update hasn’t started yet.', interrupted:'The last update stopped before it finished.',
  overdue:'This step hasn’t been checked on schedule.', 'not-indexed':'Saved conversations are waiting to be read.',
  stopped:'You stopped this update.', version:'This step needs to run again after the Pharos update.',
  'deferred-by-schedule':'This step runs when you request an update in settings.',
  failed:'The last update couldn’t finish. Check update settings.',
  'never-built':'This step needs to finish before results can appear.',
  changed:'New changes are waiting to be included.', 'daily-pass-due':'Today’s update hasn’t run yet.',
  'upgrade-available':'The library needs an upgrade.', paused:'Automatic updates are paused for this step.',
  'no-record':'This step needs a new check after the Pharos update.',
  blocked:'An earlier step couldn’t finish. Check update settings.',
  'no-schedule':'No automatic update is scheduled for this step.',
  'not-counted':'Pharos can’t check this step’s update status yet.',
  unreadable:'Pharos can’t read the saved conversation files.', empty:'There’s no data to check yet.',
};
const nextChecks:Record<string,string>={
  'updates with the next scheduled index':'Runs with the next scheduled update.',
  'updates within minutes':'Scheduled to check again within a few minutes.',
  'updates with the next hourly update':'Runs with the next hourly update.',
  'updates with the next daily update':'Runs with the next daily update.',
  'updates with the next manual index':'Runs when you request an update in settings.',
  'carries on by itself':'Continues automatically.',
  'carries on when you resume it or Pharos next opens the library':'Continues when you resume it or reopen the library.',
  'updates with the next manual index or Tools page':'Runs when you request an update or open Tools.',
  'updates when you run the library upgrade':'Runs when you upgrade the library.',
  'updates with Refresh findings now, or the first index tomorrow':'Runs when you request a findings update or with tomorrow’s library update.',
  'updates with Refresh findings now, or the next manual index':'Runs when you request a findings update or a library update.',
  'updates when the capture folder can be read':'Continues when the saved conversation files can be read.',
};
export function readinessStageDescription(stage:ReadinessStage):string {
  const code=stage.row?.reason?.code;
  // Keep actionable timing when there is no timestamp. Do not expose the
  // service's indexing terminology or discard its manual-update requirement.
  if(!stage.nextAt&&(!code||code==='changed')&&nextChecks[stage.next])return nextChecks[stage.next];
  if(code&&stageReasons[code])return stageReasons[code];
  return stage.state==='unknown'?'Pharos can’t check this part of the data yet.':stage.state==='paused'?'Automatic updates are paused.':stage.state==='failed'?'The last update couldn’t finish. Check update settings.':stage.state==='blocked'?'Waiting for another update to finish.':'';
}
export function readinessStageState(stage:ReadinessStage):string {
  return ({running:'Updating',current:'Done',scheduled:'Scheduled',unknown:'Status unavailable',behind:'Waiting',blocked:'Waiting',failed:'Couldn’t finish',paused:'Paused',queued:'Waiting to start',disabled:'Turned off',stopped:'Stopped',interrupted:'Stopped'})[stage.operation?.state??stage.state]??'Status unavailable';
}
export function readinessProgressText(stage:ReadinessStage):string {
  const done=stage.operation?.step?.done??stage.done,total=stage.operation?.step?.total??stage.total;
  const hasDone=typeof done==='number'&&Number.isFinite(done)&&done>=0,hasTotal=typeof total==='number'&&Number.isFinite(total)&&total>=0;
  const count=hasTotal?total:hasDone?done:stage.count;
  const sourceUnit=stage.operation?.step?.unit??stage.unit??'item';
  const unit=count===1?sourceUnit.replace(/s$/,''):sourceUnit.endsWith('s')?sourceUnit:sourceUnit+'s';
  return hasDone?`${done.toLocaleString()}${hasTotal?' of '+total.toLocaleString():''} ${unit}${hasTotal?'':' done'}`:typeof stage.count==='number'?`${stage.count.toLocaleString()} ${unit} waiting`:'No progress details yet';
}
function readinessCopy(kind:ReadinessModel['kind'],checkedAt:number|null,lag:number|null,now:number) {
  const label=kind==='unknown'?'Update status unavailable':kind==='missing'?'No results yet':kind==='saved'?'Showing saved results':kind==='scheduled'?'Update scheduled':kind==='current'?checkedAt===null?'No updates waiting':`Up to date as of ${readinessAge(Math.max(0,now-checkedAt))} ago`:lag===null?'Update needed':`${readinessAge(lag)} behind`;
  const sentence=kind==='unknown'?'Pharos can’t tell whether all the data shown here is up to date.':kind==='missing'?'Pharos needs to finish these steps before it can show results.':kind==='saved'?'Refresh this view to check for newer results.':kind==='scheduled'?'Some changes are waiting for the next scheduled update.':kind==='current'?checkedAt===null?'The last check time is unavailable.':'Activity since then may not appear here yet.':'Some changes are not included in these results yet.';
  return {label,sentence};
}
function stageOf(row: FreshnessRow, snapshot: ReadinessSnapshot): ReadinessStage {
  const item=snapshot.background?.items?.find(item=>item.id===itemFor(row.domain,row.task_id));
  const operations=flatOperations(snapshot.operations??[]);
  const operation=operations.find(operation=>operation.id===row.catching_up);
  return {id:row.domain,label:readinessStageLabel(row.domain,row.label),state:operation?.state??row.behind.state,reason:row.reason?.text??'',next:row.next??'',nextAt:row.next_at??item?.next_at,
    count:row.behind.count,unit:row.behind.unit,done:operation?.step?.done,total:operation?.step?.total,operation,item,row};
}
export function readinessModel(snapshot: ReadinessSnapshot | null, scopes: ReadinessScope[], now=Date.now(), unavailable=false): ReadinessModel {
  const stages: ReadinessStage[]=[], seen=new Set<string>();
  for(const scope of scopes)for(const pattern of readinessDomains(scope)) {
    const rows=(snapshot?.freshness??[]).filter(row=>matches(pattern,row.domain));
    if(rows.length){for(const row of rows)if(!seen.has(row.domain)){seen.add(row.domain);stages.push(stageOf(row,snapshot!));}continue;}
    if(pattern==='capt:*')continue;
    if(seen.has(pattern))continue;seen.add(pattern);
    const item=snapshot?.background?.items?.find(item=>item.id===itemFor(pattern));
    // A scheduled item alone doesn't prove that an output is current. In
    // particular labels and agent events don't yet have ledger domains.
    stages.push({id:pattern,label:readinessStageLabel(pattern),
      state:'unknown',reason:'Pharos can’t check this part of the data yet.',next:item?.cadence_label??'',nextAt:item?.next_at,item});
  }
  const sourceRows=stages.flatMap(stage=>stage.id.startsWith('conv:')&&stage.row?[stage.row]:[]);
  const checks=sourceRows.map(row=>timeValue(row.as_of));
  const checkedAt=checks.length&&checks.every(at=>at!==null)?Math.min(...checks as number[]):null;
  const nexts=stages.map(stage=>timeValue(stage.nextAt)).filter((at): at is number=>at!==null);
  const nextAt=nexts.length?Math.min(...nexts):null;
  const stagesFor=(scope:ReadinessScope)=>stages.filter(stage=>readinessDomains(scope).some(pattern=>matches(pattern,stage.id)));
  // A missing source is incomplete coverage, not proof that a displayed result
  // is absent. A wildcard prerequisite is absent only when every source is.
  // The result owner must also confirm its own successful query returned empty.
  const noOutput=(stage:ReadinessStage)=>stage.row?.output_missing===true;
  const missing=scopes.length>0&&scopes.every(scope=>scope.empty===true&&readinessDomains(scope).some(pattern=>{
    const inputs=stagesFor(scope).filter(stage=>matches(pattern,stage.id));
    return inputs.length>0&&inputs.every(noOutput);
  }));
  const behind=stages.some(stage=>stage.state==='behind'||stage.row?.behind.state==='behind'||['running','queued','blocked'].includes(stage.state));
  const scheduled=stages.some(stage=>stage.state==='scheduled');
  const unknown=unavailable||!snapshot||!stages.length||stages.some(stage=>stage.state==='unknown');
  const since=stages.filter(stage=>stage.row?.behind.state==='behind').map(stage=>timeValue(stage.row?.pending_since)).filter((at):at is number=>at!==null);
  const lag=since.length?Math.max(0,now-Math.min(...since)):null;
  const tone=readinessTone(lag);
  const saved=scopes.some(scope=>scope.refreshError||scope.updatedAt!==undefined&&stagesFor(scope).some(stage=>{if(stage.id.startsWith('conv:')||stage.id.startsWith('capt:'))return false;const at=timeValue(stage.row?.as_of);return at!==null&&at>scope.updatedAt!;}));
  const kind=unavailable||!snapshot?'unknown':missing?'missing':behind?'behind':unknown?'unknown':saved?'saved':scheduled?'scheduled':'current';
  const {label,sentence}=readinessCopy(kind,checkedAt,lag,now);
  return {kind,tone:kind==='unknown'?'unknown':kind==='current'?'current':kind==='scheduled'?'current':tone,lagMs:kind==='current'?0:kind==='behind'?lag:null,label,sentence,checkedAt,nextAt,stages,scopes,preview:false};
}

export function readinessSettingsURL(model:Pick<ReadinessModel,'stages'>):string {
  const pending=model.stages.filter(stage=>stage.state!=='current'&&stage.item);
  // An upgrade must rebuild old conversation data before downstream analyses
  // can use it. Keep that prerequisite visible even if rollups also need work.
  const task=(pending.find(stage=>stage.item?.id==='upgrade')??pending.at(-1))?.item;
  return '/settings/sync'+(task?'?'+new URLSearchParams({highlight:'section:background-'+task.id,disclosures:JSON.stringify({['background:'+task.id]:true})}):'');
}

// Explicit URL opt-in for UX review inside the app. This never enters the live
// status store, changes Settings, submits work or replaces query results.
export function readinessPreview(model: ReadinessModel, scenario?: ReadinessScenario | null, now=Date.now()): ReadinessModel {
  if(!scenario)return model;
  const lag=({minutes:3,recent:45,hours:187,orange:540,days:2963} as Record<string,number>)[scenario]??43;
  const state=scenario==='clear'?'current':scenario==='unknown'?'unknown':scenario==='running'?'running':scenario==='paused'?'paused':scenario==='failed'?'failed':'behind';
  const stages=model.stages.length?model.stages:[{id:'an.authorship',label:readinessStageLabel('an.authorship'),state:'unknown',reason:'',next:''}];
  const kind=scenario==='clear'?'current':scenario==='missing'?'missing':scenario==='unknown'?'unknown':'behind';
  const tone:ReadinessTone=scenario==='clear'?'current':scenario==='unknown'?'unknown':readinessTone(lag*minute);
  const copy=readinessCopy(kind,now-6*minute,lag*minute,now);
  return {...model,kind,tone,lagMs:kind==='current'?0:kind==='behind'?lag*minute:null,...copy,preview:true,checkedAt:now-6*minute,nextAt:scenario==='paused'||scenario==='unknown'?null:now+3*minute,
    stages:stages.map((stage,index)=>({...stage,state:index===stages.length-1?state:scenario==='hours'||scenario==='days'?'behind':'current',reason:index===stages.length-1?scenario==='paused'?'Automatic updates are paused.':scenario==='failed'?'The last update couldn’t finish.':copy.sentence:'',next:'Next scheduled check',nextAt:scenario==='paused'||scenario==='unknown'?null:new Date(now+3*minute).toISOString(),count:128,unit:'messages',done:scenario==='running'?58:0,total:scenario==='unknown'?null:128,operation:undefined,row:undefined,item:stage.item?{...stage.item,state, next_at:scenario==='paused'||scenario==='unknown'?null:new Date(now+3*minute).toISOString()}:undefined})),};
}
