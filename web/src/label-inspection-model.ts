import {compareStates,functionStates,shortLabel,type LabelMessage,type Taxonomy} from './label-trajectories-model';
import {matchesFunction} from './label-discovery-model';

export type LabelOccurrence = {id:string;count:number;children:LabelOccurrence[]};
export function labelNodeName(id:string,taxonomy:Taxonomy):string {
 return id==='all'?'All messages':id==='P'?'Sender':id==='C'?'Receiver':id==='?'||id==='uncategorized'?'Uncategorized':id==='unlabeled'?'Not labeled':id==='no-match'?'No accepted match':id.endsWith('~other')?'Other':shortLabel(id,taxonomy);
}
export const labelNodeColorID=(id:string)=>id.endsWith('~other')?id.slice(0,-6):['all','?','uncategorized','unlabeled','no-match'].includes(id)?'?':id;
export const matchesLabelNode=(message:LabelMessage,id:string)=>matchesFunction(message,id==='?'?'uncategorized':id);
/** Distinct message occurrences, not additive allocation. A multi-label message
 * counts once in every matching ancestor. Coverage retains its own partition. */
export function labelOccurrenceTree(messages:LabelMessage[]):LabelOccurrence {
 const counts=new Map<string,number>(),children=new Map<string,Set<string>>();
 for(const message of messages){
  const ids=new Set(['all']),labels=functionStates(message,'label');
  const paths=labels.length?labels.map(id=>{const parts=id.split('.');return ['all',...parts.map((_,i)=>parts.slice(0,i+1).join('.')),id+'~other'];}):[['all','uncategorized',message.chunks?'no-match':'unlabeled']];
  for(const path of paths)for(let i=1;i<path.length;i++){ids.add(path[i]);const next=children.get(path[i-1])??new Set();next.add(path[i]);children.set(path[i-1],next);}
  for(const id of ids)counts.set(id,(counts.get(id)??0)+1);
 }
 const build=(id:string):LabelOccurrence=>({id,count:counts.get(id)??0,children:[...(children.get(id)??[])].filter(child=>!child.endsWith('~other')||(children.get(id)?.size??0)>1||id==='P'||id==='C').sort((a,b)=>Number(a==='uncategorized')-Number(b==='uncategorized')||Number(a.endsWith('~other'))-Number(b.endsWith('~other'))||compareStates(a,b)).map(build)});
 return build('all');
}
export function labelFocusPath(id:string):string[]{
 if(!id||id==='all')return [];
 if(['?','uncategorized'].includes(id))return ['uncategorized'];
 if(['unlabeled','no-match'].includes(id))return ['uncategorized',id];
 const base=id.endsWith('~other')?id.slice(0,-6):id,parts=base.split('.');
 return [...parts.map((_,i)=>parts.slice(0,i+1).join('.')),...(id.endsWith('~other')?[id]:[])];
}
export type LabelDetailWindow={session:string;start:string};
/** Anchor by source identity. Selecting within the page never moves its axis;
 * selecting an off-page message or changing branches reveals that message. */
export function labelDetailWindow(messages:LabelMessage[],session:string,selected:string|undefined,saved:unknown,size=80){
 const value=saved&&typeof saved==='object'?saved as Partial<LabelDetailWindow>:{},chosen=Math.max(0,messages.findIndex(m=>m.id===selected)),anchor=value.session===session?messages.findIndex(m=>m.id===value.start):-1;
 const start=anchor>=0&&chosen>=anchor&&chosen<anchor+size?anchor:Math.floor(chosen/size)*size;
 return {start,messages:messages.slice(start,start+size),previous:start?messages[Math.max(0,start-size)]?.id:undefined,next:messages[start+size]?.id};
}
/** Preserve population/date context when moving from inspection back to trends. */
export function labelTrendsURL(params:URLSearchParams,id:string){
 const next=new URLSearchParams(params);next.set('label_lab','distribution');next.set('label_focus',id==='all'?'':id);
 let chart:Record<string,unknown>={};try{const raw=JSON.parse(next.get('label_chart')??'{}');if(raw&&typeof raw==='object'&&!Array.isArray(raw))chart=raw;}catch{}
 next.set('label_chart',JSON.stringify({...chart,resolution:'label'}));next.delete('label_series');next.delete('chart_pins');next.set('label_match_page','0');
 return '/analyze?'+next;
}

export function labelEvidenceRows(message:LabelMessage){
 const scores=new Map(message.chunks?message.labels:[]),ids=new Set<string>();
 for(const label of functionStates(message,'label')){const parts=label.split('.');parts.forEach((_,i)=>ids.add(parts.slice(0,i+1).join('.')));}
 return [...ids].sort(compareStates).map(id=>({id,depth:id.split('.').length-1,score:scores.get(id)}));
}
