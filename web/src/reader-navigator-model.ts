import type {LabelMessage,Taxonomy} from './conversation-labels';
import {labelIntervals,type ActivityMark} from './label-session-ribbon-model';
import type {AgentEventLink} from './event-link-layout';
import type {TimeRange} from './timeline-label-model';

export type ReaderContext={title:string;color:string;message:{id:string};sample:{stream:string;compact?:boolean;trigger?:string;size:number|null;window?:number|null};from:number;width:number;label:string};
export type ReaderNavigatorData={messages:LabelMessage[];marks:ActivityMark[];events:AgentEventLink[];taxonomy:Taxonomy;turns:{target:HTMLDetailsElement;messages:string[];label:string}[];contexts:ReaderContext[];jumpContext:(entry:ReaderContext)=>void;jumpMessage:(id:string)=>void};
export type ReaderSpan={top:number;bottom:number;start:number;end:number};
export type ReaderGeometry={spans:ReaderSpan[];ends:number[]};

/** Build once per session. Missing timestamps stay missing; an entirely
 * untimed transcript explicitly uses message order instead of a fake clock. */
export function readerNavigatorClock(messages:LabelMessage[],marks:ActivityMark[]){
  const timed=messages.some(message=>message.at&&Number.isFinite(Date.parse(message.at)))||marks.length>0;
  const drawn=timed?messages:messages.map((message,index)=>({...message,at:new Date(index).toISOString()}));
  const intervals=labelIntervals(drawn,marks),byID=new Map<string,TimeRange>();
  for(const {message,start,end} of intervals)byID.set(message.id,[start,end]);
  for(const mark of marks)if(mark.message_id)byID.set(mark.message_id,[mark.start,mark.end_known?Math.max(mark.start,mark.end):mark.start]);
  let start=Infinity,end=-Infinity;
  for(const span of byID.values()){start=Math.min(start,span[0]);end=Math.max(end,span[1]);}
  const range:TimeRange=Number.isFinite(start)?[start,Math.max(start+1,end)]:[0,1];
  return {messages:drawn,byID,range,timed};
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
