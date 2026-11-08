import type {LabelMessage} from './conversation-labels';
import {trimTimelineSnippet} from './timeline-preview';

export type AuthorshipPart = {category:string;bytes:number};
export const callerSources = [
  {id:'typed',name:'Typed',pattern:'solid',hint:'Inferred human writing or dictation; no other authorship rule claimed it.'},
  {id:'pasted',name:'Likely pasted',pattern:'diagonal',hint:'Inferred from code, logs, formatting or arrival rate; not a clipboard recording.'},
  {id:'quoted',name:'Copied / quoted',pattern:'diagonal',hint:'Explicit quotation or a match to previously retained content.'},
  {id:'resent',name:'Re-sent',pattern:'diagonal',hint:'Matches text sent previously.'},
  {id:'template',name:'Template',pattern:'grid',hint:'Retained prompt-template classification.'},
  {id:'attachment',name:'Attachment',pattern:'rings',hint:'Attachment or image placeholder.'},
  {id:'harness',name:'App context',pattern:'crosshatch',hint:'Context supplied by the agent app.'},
  {id:'automated',name:'Automated input',pattern:'dashes',hint:'Recorded as coming from a script or another agent.'},
  {id:'unknown',name:'Unknown origin',pattern:'dots',hint:'No retained authorship classification; user role alone does not establish human writing.'},
] as const;
export const callerSource=(id:string)=>callerSources.find(source=>source.id===id)??callerSources.at(-1)!;
/** Ordered byte runs, projected left to right within a message's display span. */
export function authorshipSegments(message:LabelMessage) {
  const parts=(message.authorship??[]).filter(part=>Number.isFinite(part.bytes)&&part.bytes>0);
  const source:NonNullable<LabelMessage['authorship']>=parts.length?parts:[{category:'unknown',bytes:1}],total=source.reduce((sum,part)=>sum+part.bytes,0);
  let offset=0;
  return source.map(part=>{const start=offset/total;offset+=part.bytes;return {category:callerSource(part.category).id,bytes:part.bytes,start,end:offset/total,...(part.text!==undefined?{text:part.text,truncated:part.truncated}: {})};});
}
/** Retained run excerpts take precedence over the message's shorter preview. */
export function authorshipExcerpt(message:LabelMessage,part:ReturnType<typeof authorshipSegments>[number]) {
  if(part.text!==undefined)return {text:part.text,truncated:part.truncated??false};
  const preview=new TextEncoder().encode(message.text),total=(message.authorship??[]).filter(run=>Number.isFinite(run.bytes)&&run.bytes>0).reduce((sum,run)=>sum+run.bytes,0)||preview.length;
  const start=Math.round(part.start*total),end=Math.round(part.end*total);
  if(start>=preview.length)return null;
  const text=trimTimelineSnippet(new TextDecoder().decode(preview.subarray(start,Math.min(end,preview.length)))),characters=Array.from(text);
  return {text:characters.slice(0,240).join(''),truncated:end>preview.length||characters.length>240||!message.authorship&&message.truncated};
}
export function authorshipMix(message:LabelMessage):AuthorshipPart[] {
  const byCategory=new Map<string,number>();
  for(const part of authorshipSegments(message))byCategory.set(part.category,(byCategory.get(part.category)??0)+part.bytes);
  const known=[...byCategory].map(([category,bytes])=>({category,bytes}));
  return known.length?known:[{category:'unknown',bytes:1}];
}
/** External callers include automation; embedded agent assignments stay with their agent. */
export function splitTimelineCallers(source:Map<string,LabelMessage[]>,sessions:{id:string;depth:number}[]) {
  const roots=new Set(sessions.filter(session=>session.depth===0).map(session=>session.id));
  const caller:LabelMessage[]=[],seen=new Set<string>(),messages=new Map<string,LabelMessage[]>();
  for(const [sid,items] of source) messages.set(sid,items.filter(message=>{
    if(message.role!=='user'||message.kind!=='message')return true;
    const origins=message.authorship?authorshipMix(message).map(part=>part.category):[];
    const humanEvidence=origins.some(category=>!['harness','automated','unknown'].includes(category));
    const belongs=roots.has(sid)||humanEvidence||!origins.includes('automated')&&message.segment.startsWith('H-');
    if(!belongs)return true;
    if(!seen.has(message.id)){seen.add(message.id);caller.push(message);}
    return false;
  }));
  caller.sort((a,b)=>(Date.parse(a.at??'')||0)-(Date.parse(b.at??'')||0)||a.id.localeCompare(b.id));
  return {caller,messages};
}
