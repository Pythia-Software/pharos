import {functionRole,functionStates,mainSession,scopedMessages,stateFamily,workflowSessions,type LabelConversation,type LabelMessage,type LabelStream,type Resolution,type TrajectoryScope} from './label-trajectories-model';
import {branchStreams,identitySeats} from './label-shape-model';

export type TimeRange=[number,number];
type SessionTreeRow={id:string;outline:string;depth:number;parent_id?:string};
export type TimelineSessionBranch={parentDepth?:number;rails:number[];children:boolean};
/** Tree rails continue only while a sibling remains in the visible session list. */
export function timelineSessionBranches(rows:SessionTreeRow[]){
  const ids=new Map(rows.map(row=>[row.id,row])),outlines=new Map(rows.map(row=>[row.outline,row]));
  const parents=new Map<string,SessionTreeRow>(),siblings=new Map<string,SessionTreeRow[]>();
  for(const row of rows){
    const parent=ids.get(row.parent_id??'')??outlines.get(row.outline.split('.').slice(0,-1).join('.'));
    if(!parent||parent.id===row.id||parent.depth>=row.depth)continue;
    parents.set(row.id,parent);siblings.set(parent.id,[...(siblings.get(parent.id)??[]),row]);
  }
  const remaining=new Set([...siblings.values()].flatMap(children=>children.slice(0,-1).map(row=>row.id)));
  return new Map(rows.map(row=>{
    const rails:number[]=[];let child=row,parent=parents.get(row.id);
    while(parent){if(remaining.has(child.id))rails.push(parent.depth);child=parent;parent=parents.get(parent.id);}
    return [row.id,{parentDepth:parents.get(row.id)?.depth,rails,children:siblings.has(row.id)} satisfies TimelineSessionBranch];
  }));
}
export type TimedEvent={message:LabelMessage;stream:LabelStream;index:number;at:number};
export const eventTime=(m:LabelMessage):number|null=>{
  const at=m.at?Date.parse(m.at):NaN;return Number.isFinite(at)?at:null;
};
export function workflowClock(c:LabelConversation,scope:TrajectoryScope,nested=true) {
  const streams=branchStreams(c,scope,nested),events:TimedEvent[]=[],untimed:{stream:LabelStream;message:LabelMessage}[]=[];
  for(const stream of streams)stream.messages.forEach((message,index)=>{
    const at=eventTime(message);if(at===null)untimed.push({stream,message});else events.push({message,stream,index,at});
  });
  const ids=new Set(streams.map(s=>s.id)),tools=(c.activity??[]).filter(t=>ids.has(t.session_id)),times=[...events.map(e=>e.at),...tools.flatMap(t=>[t.start,t.end].flatMap(value=>{const at=value?Date.parse(value):NaN;return Number.isFinite(at)?[at]:[];}))],start=times.length?Math.min(...times):0,end=times.length?Math.max(...times):0;
  const range:TimeRange=[start,Math.max(start+1,end)];
  const links=streams.flatMap(child=>{
    const parent=streams.find(s=>s.id===child.session.parent_id),call=parent?.messages.find(m=>m.id===child.session.call_message_id);
    const at=call?eventTime(call):null,first=events.filter(e=>e.stream.id===child.id).reduce<TimedEvent|undefined>((a,b)=>!a||b.at<a.at?b:a,undefined);
    return parent&&call&&at!==null&&first?[{parent,child,call,at,first}]:[];
  });
  return {streams,events,untimed,range,links};
}
export type WorkflowClock=ReturnType<typeof workflowClock>;
// Numeric outlines are presentation only; canonical session IDs own all links.
export function branchOutlines(c:LabelConversation) {
  const result=new Map<string,string>(),counts=new Map<string,number>();
  for(const session of workflowSessions(c)){
    const parent=session.parent_id?result.get(session.parent_id):undefined,key=parent??'';
    const index=(counts.get(key)??0)+1;counts.set(key,index);result.set(session.id,parent?parent+'.'+index:String(index));
  }return result;
}
export function clockRange(full:TimeRange,requested:TimeRange|null):TimeRange {
  if(!Array.isArray(requested)||requested.length!==2||!requested.every(Number.isFinite)||requested[1]<=requested[0])return full;
  const start=Math.max(full[0],requested[0]),end=Math.min(full[1],requested[1]);return end>start?[start,end]:full;
}
export function timeLanes(clock:WorkflowClock) {
  return clock.streams.flatMap(stream=>identitySeats(stream.messages,stream.session).map(seat=>({...seat,stream,key:stream.id+':'+seat.id,events:clock.events.filter(e=>e.stream.id===stream.id&&seat.events.some(s=>s.message.id===e.message.id))})));
}
export const scoreFamilies=['P.request','P.frame','P.control','C.input','C.feedback','C.deliberation','C.report','C.difficulty'];
export const staffFamily=(id:string)=>id==='P'||id==='C'?id:stateFamily(id);
// Keep branch order when finding the next observed Receiver. This is a visual
// succession link, never an assertion that the next message answers a request.
export function noteConnections(messages:LabelMessage[]) {
  const result:{from:LabelMessage;to:LabelMessage}[]=[];let sender:LabelMessage|undefined;
  for(const m of messages){const ids=functionStates(m);
    if(!ids.length){sender=undefined;continue;}
    if(sender&&ids.some(id=>functionRole(id)==='Receiver')){result.push({from:sender,to:m});sender=undefined;}
    if(ids.some(id=>functionRole(id)==='Sender'))sender=m;
  }return result;
}
export type ScoreNote={message:LabelMessage;members:LabelMessage[];stream:LabelStream;ids:string[];position:number;length:number};
export function scoreNotes(streams:LabelStream[],resolution:Resolution,changes:boolean,range?:TimeRange):ScoreNote[] {
  return streams.flatMap(stream=>{
    const notes:ScoreNote[]=[];
    stream.messages.forEach((message,index)=>{
      const at=eventTime(message);if(range&&(at===null||at<range[0]||at>range[1]))return;
      const ids=functionStates(message,resolution),last=notes.at(-1);
      if(changes&&last&&last.ids.join('|')===ids.join('|')&&ids.length){last.length++;last.members.push(message);return;}
      notes.push({message,members:[message],stream,ids,position:range?at!:index,length:1});
    });return notes;
  });
}
export function selectedStream(c:LabelConversation,scope:TrajectoryScope,id:string):LabelStream {
  const session=workflowSessions(c).find(s=>s.id===id)??workflowSessions(c).find(s=>s.id===mainSession(c))!;
  return {id:session.id,session,conversation:c,messages:scopedMessages(c,scope,session.id)};
}
