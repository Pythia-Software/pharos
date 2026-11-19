import type {LabelMessage,Taxonomy} from './conversation-labels';
import {labelIntervals,type ActivityMark} from './label-session-ribbon-model';
import type {AgentEventLink} from './event-link-layout';
import type {HumanEvent} from './human-events-model';
import type {TimeRange} from './timeline-label-model';
import {chosenGaps,idleGaps,laneBusy,laneWaits,timeScale,waitGaps,type IdleGap} from './timeline-idle-model';

export type ReaderContext={title:string;color:string;message:{id:string};sample:{stream:string;compact?:boolean;trigger?:string;size:number|null;window?:number|null};from:number;width:number;label:string};
export type ReaderNavigatorMark=Omit<ActivityMark,'start'|'end'>&{start:number|null;end:number|null};
export type ReaderNavigatorData={sessionID:string;messages:LabelMessage[];marks:ReaderNavigatorMark[];callerIDs:string[];events:AgentEventLink[];humanEvents?:HumanEvent[];taxonomy:Taxonomy;turns:{target:HTMLDetailsElement;messages:string[];label:string;card?:{title:string;at:string|null;when:string;prompt:string;stats:string[];tokens:string}}[];contexts:ReaderContext[];jumpContext:(entry:ReaderContext)=>void;jumpMessage:(id:string)=>void;jumpHumanEvent?:(event:HumanEvent)=>void;voice?:ReaderVoice|null};
/** A voice conversation's player, owned by the reader: the lines it can play and what is playing. */
export type ReaderVoiceLine={id:string;role:string;recorded:boolean;ready:boolean;duration:number|null;estimate:number};
export type ReaderVoiceSpan={line:ReaderVoiceLine;index:number;start:number;length:number};
/** The lines end to end, as the reader's player plays them (voiceSchedule in reader-model.js). */
export type ReaderVoicePlan={total:number;spans:ReaderVoiceSpan[]};
export type ReaderVoice={lines:ReaderVoiceLine[];recorded:number;snapshot:()=>{playing:boolean;current:string|null;active:string[];speech:boolean;pending:number;measured:number;plan:ReaderVoicePlan};subscribe:(listener:()=>void)=>()=>void;toggle:()=>void;playFrom:(id:string)=>void};
export function voiceClock(ms:number):string{const seconds=Math.max(0,Math.round(ms/1000));return `${Math.floor(seconds/60)}:${String(seconds%60).padStart(2,'0')}`;}
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
    // An untimed transcript's ordinal clock has no place for a wait's end.
    return [{...mark,start,end:known?mark.end!:start,end_known:known,...(timed?{}:{wait_kind:undefined,wait_end:undefined})}];
  });
  const intervals=labelIntervals(drawn,marks),byID=new Map<string,TimeRange>();
  for(const {message,start,end} of intervals)byID.set(message.id,[start,end]);
  for(const mark of marks)if(mark.message_id)byID.set(mark.message_id,[mark.start,mark.end_known?Math.max(mark.start,mark.end):mark.start]);
  let start=Infinity,end=-Infinity;
  for(const span of byID.values()){start=Math.min(start,span[0]);end=Math.max(end,span[1]);}
  const range:TimeRange=Number.isFinite(start)?[start,Math.max(start+1,end)]:[0,1];
  return {messages:drawn,marks,byID,range,timed};
}

/** Use the timeline's idle rule with all retained conversation activity, and
 * the workspace Timeline's gaps when it has published them. Machine waits
 * (the calls' recorded waits) count as activity unless skipped. */
export function readerNavigatorScale(clock:ReturnType<typeof readerNavigatorClock>,callerIDs:string[],skipIdle=true,workspaceGaps?:IdleGap[],skipWaits=false,workspaceWaitGaps?:IdleGap[]){
  if(!clock.timed||!skipIdle&&!skipWaits)return timeScale(clock.range);
  const own=workspaceGaps&&workspaceWaitGaps?null:readerGaps(clock,callerIDs);
  return timeScale(clock.range,chosenGaps({idle:workspaceGaps??own!.idle,waits:workspaceWaitGaps??own!.waits},{skipIdle,skipWaits}));
}
export function readerGaps(clock:ReturnType<typeof readerNavigatorClock>,callerIDs:string[]):{idle:IdleGap[];waits:IdleGap[]}{
  const callers=new Set(callerIDs),lane={marks:clock.marks,messages:clock.messages.map(message=>({created_at:message.at}))};
  const resumed=clock.messages.flatMap(message=>{
    const at=message.at?Date.parse(message.at):NaN;
    return callers.has(message.id)&&Number.isFinite(at)?[at]:[];
  });
  const waits=laneWaits(lane);
  return {idle:idleGaps({busy:laneBusy(lane),callers:resumed}),waits:waits.length?waitGaps({busy:[...laneBusy(lane,undefined,{waits:false}),...resumed.map(at=>({start:at,end:at}))],waits}):[]};
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

/** A context profile laid out on a track `width` units wide: its edges, its
 * heights in percent of the track (from before the request to after it), and
 * its position in the context list, which is also its stacking order. */
export type ContextShape={index:number;x0:number;x1:number;from:number;to:number;compact:boolean;stream:string;color:string};
export type ContextArea={color:string;d:string};
export const CONTEXT_MIN_WIDTH=3,CONTEXT_COMPACTION_WIDTH=2;
const coordinate=(value:number)=>Math.round(value*100)/100;

/** The same visible minimum-width marker for drawing, outlining and hits. */
export function contextProfileBounds(shape:ContextShape,width:number):[number,number]{
  const minimum=shape.compact?CONTEXT_COMPACTION_WIDTH:CONTEXT_MIN_WIDTH;
  const left=Math.max(0,Math.min(shape.x0,width-minimum));
  return [left,Math.max(shape.x1,left+minimum)];
}
export function contextProfileOutline(shape:ContextShape,width:number){
  const [left,right]=contextProfileBounds(shape,width);
  return `M${left} 100L${left} ${100-shape.from}L${right} ${100-shape.to}L${right} 100Z`;
}

/** Draw a session's context profiles as a few shapes instead of one element
 * per sample: a long session has thousands of samples, far more than the
 * track has pixels, and an element each made every scroll frame's hit test
 * walk all of them. Consecutive touching, same-stream, same-colored profiles
 * form one area, sampled at half-pixel resolution. Stream switches and
 * compactions start new layers so paint and hit order stay in source order. */
export function contextProfileLayout(entries:{entry:Pick<ReaderContext,'from'|'width'|'color'|'sample'>;start:number;end:number}[],fraction:(time:number)=>number,width:number):{shapes:ContextShape[];layers:ContextArea[]}{
  const place=(time:number)=>Math.max(0,Math.min(width,fraction(time)*width));
  const shapes=entries.map(({entry,start,end},index):ContextShape=>{
    const compact=!!entry.sample.compact,x0=place(start);
    if(compact){const left=Math.max(0,Math.min(x0,width-CONTEXT_COMPACTION_WIDTH));return {index,x0:left,x1:left+CONTEXT_COMPACTION_WIDTH,from:100,to:100,compact,stream:entry.sample.stream,color:entry.color};}
    return {index,x0,x1:Math.max(x0,place(end)),from:entry.from,to:entry.width,compact,stream:entry.sample.stream,color:entry.color};
  });
  const runs:ContextShape[][]=[];
  for(const shape of shapes){
    const run=runs[runs.length-1],last=run?.[run.length-1];
    if(run&&last&&!shape.compact&&!last.compact&&last.stream===shape.stream&&last.color===shape.color&&Math.abs(shape.x0-last.x1)<.5)run.push(shape);
    else runs.push([shape]);
  }
  const layers=runs.map(run=>{
    if(run.length===1)return {color:run[0].color,d:contextProfileOutline(run[0],width)};
    // A run narrower than a marker's minimum width still shows as one.
    const left=Math.max(0,Math.min(run[0].x0,width-CONTEXT_MIN_WIDTH)),right=Math.max(run[run.length-1].x1,left+CONTEXT_MIN_WIDTH);
    const points:[number,number][]=[];
    const add=(x:number,height:number)=>{const last=points[points.length-1];if(last&&points.length>1&&Math.abs(x-last[0])<.5)last[1]=Math.max(last[1],height);else points.push([x,height]);};
    run.forEach((shape,i)=>{add(i===0?left:shape.x0,shape.from);add(i===run.length-1?right:shape.x1,shape.to);});
    const top=points.map(([x,height])=>`${coordinate(x)} ${coordinate(100-height)}`).join('L');
    return {color:run[0].color,d:`M${coordinate(left)} 100L${top}L${coordinate(right)} 100Z`};
  });
  return {shapes,layers};
}

/** The profile under a point of the track (x in track units, height in
 * percent from its bottom): the topmost profile containing it, or -1 above
 * every profile, where the turn underneath takes the pointer. A narrow
 * profile keeps the marker's minimum width as a target. */
export function contextProfileHit(shapes:ContextShape[],x:number,height:number,width:number):number{
  if(x<0||x>width||height<0||height>100)return -1;
  for(let i=shapes.length-1;i>=0;i--){
    const shape=shapes[i],[left,right]=contextProfileBounds(shape,width);
    const top=shape.from+(shape.to-shape.from)*Math.max(0,Math.min(1,(x-left)/(right-left)));
    if(x>=left&&x<=right&&height<=top)return shape.index;
  }
  return -1;
}
