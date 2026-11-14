import type {LabelMessage,Taxonomy} from './conversation-labels';
import {labelIntervals,type ActivityMark} from './label-session-ribbon-model';
import type {AgentEventLink} from './event-link-layout';
import type {HumanEvent} from './human-events-model';
import type {TimeRange} from './timeline-label-model';
import {idleGaps,laneBusy,timeScale,type IdleGap} from './timeline-idle-model';

export type ReaderContext={title:string;color:string;message:{id:string};sample:{stream:string;compact?:boolean;trigger?:string;size:number|null;window?:number|null};from:number;width:number;label:string};
export type ReaderNavigatorMark=Omit<ActivityMark,'start'|'end'>&{start:number|null;end:number|null};
export type ReaderNavigatorData={sessionID:string;messages:LabelMessage[];marks:ReaderNavigatorMark[];callerIDs:string[];events:AgentEventLink[];humanEvents?:HumanEvent[];taxonomy:Taxonomy;turns:{target:HTMLDetailsElement;messages:string[];label:string;card?:{title:string;at:string|null;when:string;prompt:string;stats:string[];tokens:string}}[];contexts:ReaderContext[];jumpContext:(entry:ReaderContext)=>void;jumpMessage:(id:string)=>void;jumpHumanEvent?:(event:HumanEvent)=>void};
export type ReaderSpan={top:number;bottom:number;start:number;end:number};
export type ReaderGeometry={spans:ReaderSpan[];ends:number[]};
export type ReaderNavigatorChoice='timeline'|'context'|'turns';

/** Offer bars only when they can show measured context or distinguish turns. */
export function availableReaderNavigators(data:Pick<ReaderNavigatorData,'contexts'|'turns'>,byID:Map<string,TimeRange>):ReaderNavigatorChoice[]{
  const available:ReaderNavigatorChoice[]=['timeline'];
  if(data.contexts.some(entry=>entry.sample.size!==null&&Number.isFinite(entry.sample.size)&&byID.has(entry.message.id)))available.push('context');
  if(data.turns.filter(turn=>turn.messages.some(id=>byID.has(id))).length>1)available.push('turns');
  return available;
}

/** Build once per session. Missing timestamps stay missing; an entirely
 * untimed transcript explicitly uses message order instead of a fake clock. */
export function readerNavigatorClock(messages:LabelMessage[],sourceMarks:ReaderNavigatorMark[]){
  const timed=messages.some(message=>message.at&&Number.isFinite(Date.parse(message.at)))||sourceMarks.some(mark=>mark.start!==null&&Number.isFinite(mark.start));
  const drawn=timed?messages:messages.map((message,index)=>({...message,at:new Date(index).toISOString()}));
  const ordinals=new Map(messages.map((message,index)=>[message.id,index]));
  const marks:ActivityMark[]=sourceMarks.flatMap(mark=>{
    const start=timed?mark.start:mark.message_id?ordinals.get(mark.message_id):undefined;
    if(start==null||!Number.isFinite(start))return [];
    const known=timed&&mark.end_known&&mark.end!==null&&Number.isFinite(mark.end)&&mark.end>=start;
    return [{...mark,start,end:known?mark.end!:start,end_known:known}];
  });
  const intervals=labelIntervals(drawn,marks),byID=new Map<string,TimeRange>();
  for(const {message,start,end} of intervals)byID.set(message.id,[start,end]);
  for(const mark of marks)if(mark.message_id)byID.set(mark.message_id,[mark.start,mark.end_known?Math.max(mark.start,mark.end):mark.start]);
  let start=Infinity,end=-Infinity;
  for(const span of byID.values()){start=Math.min(start,span[0]);end=Math.max(end,span[1]);}
  const range:TimeRange=Number.isFinite(start)?[start,Math.max(start+1,end)]:[0,1];
  return {messages:drawn,marks,byID,range,timed};
}

/** Use the timeline's idle rule with all retained conversation activity. */
export function readerNavigatorScale(clock:ReturnType<typeof readerNavigatorClock>,callerIDs:string[],skipIdle=true,workspaceGaps?:IdleGap[]){
  if(!clock.timed||!skipIdle)return timeScale(clock.range);
  if(workspaceGaps)return timeScale(clock.range,workspaceGaps);
  const callers=new Set(callerIDs);
  const busy=laneBusy({marks:clock.marks,messages:clock.messages.map(message=>({created_at:message.at}))});
  const resumed=clock.messages.flatMap(message=>{
    const at=message.at?Date.parse(message.at):NaN;
    return callers.has(message.id)&&Number.isFinite(at)?[at]:[];
  });
  return timeScale(clock.range,idleGaps({busy,callers:resumed}));
}

export function readerGeometry(spans:ReaderSpan[]):ReaderGeometry{
  spans=spans.filter(span=>span.bottom>span.top).sort((a,b)=>a.top-b.top);
  let end=-Infinity;
  return {spans,ends:spans.map(span=>(end=Math.max(end,span.bottom)))};
}

/** A prefix maximum permits binary search even for overlapping rendered
 * groups. Scrolling examines only rows intersecting the unobscured viewport. */
export function readerVisibleWindow({spans,ends}:ReaderGeometry,top:number,bottom:number):TimeRange|null{
  if(bottom<=top)return null;
  let lo=0,hi=ends.length;
  while(lo<hi){const mid=(lo+hi)>>>1;if(ends[mid]<=top)lo=mid+1;else hi=mid;}
  let start=Infinity,end=-Infinity;
  for(let i=lo;i<spans.length&&spans[i].top<bottom;i++){
    const span=spans[i];if(span.bottom<=top)continue;
    const fraction=(at:number)=>Math.max(0,Math.min(1,(at-span.top)/(span.bottom-span.top)));
    start=Math.min(start,span.start+(span.end-span.start)*fraction(top));
    end=Math.max(end,span.start+(span.end-span.start)*fraction(bottom));
  }
  return Number.isFinite(start)?[start,end]:null;
}

export function nearestReaderMessage(byID:Map<string,TimeRange>,at:number){
  let nearest:string|undefined,distance=Infinity,latest=-Infinity;
  for(const [id,[start,end]] of byID){const next=at<start?start-at:at>end?at-end:0;if(next<distance||next===distance&&start>latest){nearest=id;distance=next;latest=start;}}
  return nearest;
}
