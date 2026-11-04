import {compareStates,functionRole,functionStates,mainSession,matchesLabel,phaseBin,specificLabels,stateFamily,stateRuns,workflowSessions,type LabelConversation,type LabelMessage,type LabelStream,type Resolution,type StepMode,type Taxonomy} from './label-trajectories-model';
import {eventTime} from './label-timeline-model';

export type Breakdown='phase'|'actor'|'model'|'provider'|'depth';
export const breakdowns=[['phase','Conversation phase'],['actor','User / agent / subagent'],['model','Generation model'],['provider','Provider'],['depth','Nesting depth']] as const;
export type SourceMatch={stream:LabelStream;message:LabelMessage};
export function eventColumn(stream:LabelStream,m:LabelMessage,index:number,by:Breakdown):string {
 if(by==='phase')return ['Opening','Early','Middle','Late','Ending'][phaseBin(stream.sourceMessages?stream.sourceMessages.findIndex(source=>source.id===m.id):index,stream.sourceMessages?.length??stream.messages.length,5)];
 if(by==='actor')return m.role==='tool'?'Tool':m.role==='user'?'User role':m.role==='agent'||m.role==='assistant'?(stream.session.parent_id?'Subagent':'Main agent'):'Other role';
 if(by==='model')return m.generation_model||((m.role==='agent'||m.role==='assistant')?stream.session.model:'')||'Not recorded';
 if(by==='provider')return stream.session.provider||stream.conversation.provider||'Not recorded';
 return 'Level '+stream.session.depth;
}
export type Allocation={ids:string[];weight:number};
export function messageAllocation(m:LabelMessage,taxonomy:Taxonomy):Allocation[]{
 const labels=functionStates(m,'label');
 if(!labels.length)return [{ids:['all','uncategorized',m.chunks?'no-match':'unlabeled'],weight:1}];
 const roots=['P','C'].filter(root=>labels.some(id=>id===root||id.startsWith(root+'.')));
 return roots.flatMap(root=>{const ids=labels.filter(id=>id===root||id.startsWith(root+'.'));return ids.map(id=>{
  const parts=id.split('.'),path=['all',...parts.map((_,i)=>parts.slice(0,i+1).join('.'))];
  if(taxonomy.some(t=>t.id.startsWith(id+'.'))||id===root)path.push(id+'~other');
  return {ids:path,weight:1/roots.length/ids.length};
 });});
}
export type FrequencyNode={id:string;children:FrequencyNode[]};
export function frequencyTree(taxonomy:Taxonomy):FrequencyNode {
 const all=[...new Set(taxonomy.flatMap(t=>{const parts=t.id.split('.');return parts.map((_,i)=>parts.slice(0,i+1).join('.'));}))];
 const build=(id:string):FrequencyNode=>{const kids=all.filter(t=>t.startsWith(id+'.')&&t.split('.').length===id.split('.').length+1).sort(compareStates).map(t=>build(t));return {id,children:kids.length||id==='P'||id==='C'?[...kids,{id:id+'~other',children:[]}]:[]};};
 return {id:'all',children:[build('P'),build('C'),{id:'uncategorized',children:[{id:'unlabeled',children:[]},{id:'no-match',children:[]}]}]};
}
export function frequencyTable(streams:LabelStream[],taxonomy:Taxonomy,by:Breakdown){
 const known=new Map(taxonomy.map(t=>[t.id,t]));
 for(const stream of streams)for(const m of stream.messages)for(const id of functionStates(m,'label'))if(!known.has(id))known.set(id,{id,name:id,definition:''});
 const definitions=[...known.values()];
 const entries=streams.flatMap(stream=>stream.messages.map((message,index)=>({stream,message,column:eventColumn(stream,message,index,by),allocations:messageAllocation(message,definitions)})));
 const columns=by==='phase'?['Opening','Early','Middle','Late','Ending']:by==='actor'?['User role','Main agent','Subagent','Tool','Other role'].filter(c=>entries.some(e=>e.column===c)):[...new Set(entries.map(e=>e.column))].sort((a,b)=>a==='Not recorded'?1:b==='Not recorded'?-1:a.localeCompare(b,undefined,{numeric:true}));
 const counts=new Map<string,number[]>(),totals=columns.map(c=>entries.filter(e=>e.column===c).length);
 for(const e of entries){const i=columns.indexOf(e.column);for(const allocation of e.allocations)for(const id of allocation.ids){const values=counts.get(id)??Array(columns.length).fill(0);values[i]+=allocation.weight;counts.set(id,values);}}
 return {columns,counts,totals,tree:frequencyTree(definitions)};
}
export function matchesFunction(m:LabelMessage,id:string):boolean {
 if(!id||id==='all')return true;
 if(id==='uncategorized')return !functionStates(m).length;
 if(id==='unlabeled')return !m.chunks;
 if(id==='no-match')return !!m.chunks&&!functionStates(m).length;
 if(id.endsWith('~other'))return functionStates(m,'label').includes(id.slice(0,-6));
 return matchesLabel(m,id);
}
export function frequencyMatches(streams:LabelStream[],by:Breakdown,id:string,column:string):SourceMatch[]{return streams.flatMap(stream=>stream.messages.flatMap((message,index)=>(!column||eventColumn(stream,message,index,by)===column)&&matchesFunction(message,id)?[{stream,message}]:[]));}
export function tagPrefixPaths(streams:LabelStream[],resolution:Resolution,mode:StepMode,prefix:string[]){
 return streams.map(stream=>{const runs=stateRuns(stream.messages,resolution,mode),gap=runs.findIndex(r=>!r.key);return {stream,runs:gap<0?runs:runs.slice(0,gap),missing:gap>=0||!runs.length};}).filter(path=>prefix.every((filter,i)=>filter.split('|').every(tag=>path.runs[i]?.key?.split('|').some(id=>id===tag||id.startsWith(tag+'.')))));
}
export function tagContinuations(streams:LabelStream[],resolution:Resolution,mode:StepMode,prefix:string[]){
 const paths=tagPrefixPaths(streams,resolution,mode,prefix),tags=new Map<string,number>();let ended=0,missing=0;
 for(const p of paths){const run=p.runs[prefix.length];if(!run){if(p.missing)missing++;else ended++;continue;}const ids=new Set<string>();for(const id of run.key!.split('|')){const parts=id.split('.');for(let d=1;d<=parts.length;d++)ids.add(parts.slice(0,d).join('.'));}for(const id of ids)tags.set(id,(tags.get(id)??0)+1);}
 return {paths,tags,ended,missing};
}
export function turnPosition(stream:LabelStream,message:LabelMessage){const messages=stream.sourceMessages??stream.messages;let turn=0,at=1;messages.forEach((m,i)=>{if(i===0||m.role==='user')turn++;if(m.id===message.id)at=turn;});return {at,total:turn,index:messages.findIndex(m=>m.id===message.id)+1,messages:messages.length};}
export function capturedSpan(c:LabelConversation){const times=c.messages.map(eventTime).filter((at):at is number=>at!==null);return times.length?Math.max(...times)-Math.min(...times):null;}
export type RibbonToken={ids:string[];count:number;depth:number;parallelism:number;messages:LabelMessage[]};
export type RibbonShare={id:string;colorID:string;weight:number;share:number;depth:number;parent:string|null;terminal:boolean};
/** One message unit, split across its accepted roots and deepest labels exactly
 * as in Phase Flow. Every terminal contributes a stripe, including parent-only
 * Report and unlabeled/no-match coverage; no top-N labels are discarded. */
export function ribbonSegment(messages:LabelMessage[],taxonomy:Taxonomy=[]){
 const known=new Map(taxonomy.map(t=>[t.id,t]));
 for(const m of messages)for(const id of functionStates(m,'label'))if(!known.has(id))known.set(id,{id,name:id,definition:''});
 const definitions=[...known.values()],weights=new Map<string,number>(),children=new Map<string,Set<string>>(),terminals=new Set<string>();
 for(const message of messages)for(const allocation of messageAllocation(message,definitions)){
  allocation.ids.forEach((id,i)=>{weights.set(id,(weights.get(id)??0)+allocation.weight);if(i){const parent=allocation.ids[i-1],ids=children.get(parent)??new Set<string>();ids.add(id);children.set(parent,ids);}});
  terminals.add(allocation.ids.at(-1)!);
 }
 const rows:RibbonShare[]=[],visit=(id:string,depth:number,parent:string|null)=>{
  const colorID=id.endsWith('~other')?id.slice(0,-6):['all','uncategorized','unlabeled','no-match'].includes(id)?'?':id;
  rows.push({id,colorID,weight:weights.get(id)??0,share:messages.length?(weights.get(id)??0)/messages.length:0,depth,parent,terminal:terminals.has(id)});
  const ids=[...(children.get(id)??[])].sort((a,b)=>Number(a.endsWith('~other'))-Number(b.endsWith('~other'))||Number(a==='uncategorized')-Number(b==='uncategorized')||Number(b==='unlabeled')-Number(a==='unlabeled')||compareStates(a,b));
  for(const child of ids)visit(child,depth+1,id);
 };
 if(messages.length)visit('all',0,null);
 return {rows,slices:rows.filter(row=>row.terminal)};
}
export function conversationRibbon(c:LabelConversation,limit=40):RibbonToken[]{
 const sessions=new Map(workflowSessions(c).map(s=>[s.id,s]));
 const intervals=[...sessions.keys()].flatMap(id=>{const times=[...c.messages.filter(m=>(m.session_id??mainSession(c))===id).map(eventTime).filter((at):at is number=>at!==null),...(c.activity??[]).filter(t=>t.session_id===id).flatMap(t=>[t.start,t.end].flatMap(value=>{const at=value?Date.parse(value):NaN;return Number.isFinite(at)?[at]:[];}))];return times.length?[{start:Math.min(...times),end:Math.max(...times)}]:[];});
 const extraAt=(at:number|null)=>at===null?0:Math.max(0,intervals.filter(s=>s.start<=at&&s.end>=at).length-1);
 const events=c.messages.map((m,index)=>({m,index,at:eventTime(m)})).sort((a,b)=>a.at===null?b.at===null?a.index-b.index:1:b.at===null?-1:a.at-b.at||a.index-b.index);
 const runs:RibbonToken[]=[];for(const {m,at} of events){const ids=functionStates(m,'label'),depth=sessions.get(m.session_id??mainSession(c))?.depth??0,parallelism=extraAt(at),last=runs.at(-1),known=ids.length?ids:['?'];if(last&&last.ids.join('|')===known.join('|')&&last.depth===depth&&last.parallelism===parallelism){last.count++;last.messages.push(m);}else runs.push({ids:known,count:1,depth,parallelism,messages:[m]});}
 if(runs.length<=limit)return runs;
 const result:RibbonToken[]=[];for(let i=0;i<limit;i++){const chunk=runs.slice(Math.floor(i*runs.length/limit),Math.floor((i+1)*runs.length/limit));if(chunk.length)result.push({ids:[...new Set(chunk.flatMap(r=>r.ids))].sort(compareStates),count:chunk.reduce((n,r)=>n+r.count,0),depth:Math.max(...chunk.map(r=>r.depth)),parallelism:Math.max(...chunk.map(r=>r.parallelism)),messages:chunk.flatMap(r=>r.messages)});}return result;
}

/** Exact captured overlap, with a bounded number of glyphs plus an overflow count. */
export function parallelThreads(segments:{x:number;width:number;token:Pick<RibbonToken,'parallelism'>}[],limit=6){
 const peak=Math.max(0,...segments.map(s=>s.token.parallelism)),levels=[];
 for(let level=1;level<=Math.min(limit,peak);level++){
  const groups:{start:number;end:number}[]=[];
  for(const s of segments){if(s.token.parallelism<level)continue;const last=groups.at(-1);if(last&&Math.abs(last.end-s.x)<.01)last.end=s.x+s.width;else groups.push({start:s.x,end:s.x+s.width});}
  levels.push({level,groups});
 }
 return {levels,peak,overflow:Math.max(0,peak-limit)};
}

export const ribbonMessageTitle=(first:number,last:number,total:number)=>`${first===last?'Message':'Messages'} ${first===last?first:`${first}–${last}`} / ${total}`;

/** Symmetric overlap envelopes leave the ribbon edges visible. Height grows
 * monotonically with overlap, approaching a bound rather than capping counts. */
export function parallelEnvelopes(segments:{x:number;width:number;token:Pick<RibbonToken,'parallelism'>}[]){
 const groups:typeof segments[]=[];let current:typeof segments=[];
 for(const segment of segments){if(segment.token.parallelism>0)current.push(segment);else if(current.length){groups.push(current);current=[];}}
 if(current.length)groups.push(current);
 return groups.map(group=>{
  const start=group[0].x,last=group.at(-1)!,end=last.x+last.width,center=19;
  const points=group.map(s=>{const half=13*s.token.parallelism/(s.token.parallelism+3);return {x:s.x+s.width/2,upper:center-half,lower:center+half};});
  let path=`M${start} ${center} Q${start} ${points[0].upper} ${points[0].x} ${points[0].upper}`;
  for(let i=1;i<points.length;i++){const p=points[i-1],q=points[i],mid=(p.x+q.x)/2;path+=` C${mid} ${p.upper} ${mid} ${q.upper} ${q.x} ${q.upper}`;}
  path+=` Q${end} ${points.at(-1)!.upper} ${end} ${center} Q${end} ${points.at(-1)!.lower} ${points.at(-1)!.x} ${points.at(-1)!.lower}`;
  for(let i=points.length-2;i>=0;i--){const p=points[i+1],q=points[i],mid=(p.x+q.x)/2;path+=` C${mid} ${p.lower} ${mid} ${q.lower} ${q.x} ${q.lower}`;}
  path+=` Q${start} ${points[0].lower} ${start} ${center} Z`;
  return {path,points,peak:Math.max(...group.map(s=>s.token.parallelism))};
 });
}
