// Shared production message-label and conversation data.
import {compareStates,functionStates,mainSession,workflowSessions,type LabelConversation,type LabelMessage,type Taxonomy} from './conversation-labels';
import {eventTime} from './timeline-label-model';
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
export type RibbonToken={ids:string[];count:number;depth:number;parallelism:number;messages:LabelMessage[]};
export type RibbonShare={id:string;colorID:string;weight:number;share:number;depth:number;parent:string|null;terminal:boolean};
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
export const ribbonMessageTitle=(first:number,last:number,total:number)=>`${first===last?'Message':'Messages'} ${first===last?first:`${first}–${last}`} / ${total}`;
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
/** Segment widths: volume compresses logarithmically. */
export const ribbonWeights=(tokens:Pick<RibbonToken,'count'>[])=>tokens.map(t=>1+Math.log2(t.count+1));
/** Each message's x in [0, 1] of the fingerprint: its share of its segment, in order. */
export function fingerprintMessagePositions(tokens:RibbonToken[]):Map<string,number>{
 const weights=ribbonWeights(tokens),total=weights.reduce((a,b)=>a+b,0)||1,at=new Map<string,number>();let cursor=0;
 tokens.forEach((token,i)=>{const width=weights[i]/total;token.messages.forEach((m,k)=>at.set(m.id,cursor+width*(k+.5)/token.messages.length));cursor+=width;});
 return at;
}
