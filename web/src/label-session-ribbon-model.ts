import {EVENT_GROUPS,eventColor,eventGroup,type AgentEventLink} from './event-link-layout';
import {compareStates,functionRole,functionStates,shortLabel,stateColor,type LabelMessage,type LabelTool,type Taxonomy} from './conversation-labels';
import {eventTime,type TimeRange} from './timeline-label-model';
import {authorshipSegments,authorshipExcerpt,callerSource} from './timeline-caller-model';
import {laneCandidates,laneMark,nearestMark,placeMarks,type HitTarget,type PlacedMark} from './ribbon-marks';

export type ActivityMark={start:number;end:number;end_known:boolean;category:string;count:number;error:boolean;tool_id?:string;message_id?:string;preview?:string;preview_truncated?:boolean;status?:string;name:string};
export type RibbonObservation={event?:AgentEventLink;id:string;start:number;end:number;title:string;color:string;thinking:boolean;label?:string;message?:LabelMessage;mark?:ActivityMark;inferred?:boolean;authorship?:string;excerpt?:{text:string;truncated:boolean}|null;composition?:{start:number;end:number};share?:number;attribution?:'preceding'|'following'|'instant'};
export type RibbonNode={id:string;title:string;items:RibbonObservation[];children:RibbonNode[]};
const isThinking=(m:LabelMessage)=>m.kind==='thinking'||m.kind==='reasoning';
const activityNames:Record<string,string>={read:'Read',write:'Write',run:'Run',git:'Git',network:'Network',delegate:'Delegation',wait:'Wait',poll:'Poll',other:'Other',model:'Model / orchestration · inferred'};
const group=(id:string,title:string,children:RibbonNode[]):RibbonNode=>({id,title,children,items:children.flatMap(child=>child.items)});


export function activityCategory(tool:LabelTool):string{
 const name=tool.name.toLowerCase(),cat=(tool.category??'').toLowerCase(),command=(tool.command_category??'').toLowerCase();
 if(['wait_agent','wait','taskoutput'].includes(name))return 'wait';
 if(/sleep|poll/.test(name)||name==='write_stdin')return 'poll';
 if(cat==='agent'||/delegate|spawn/.test(name)||name==='task')return 'delegate';
 if(['read','search'].includes(cat)||/read|search|glob/.test(name))return 'read';
 if(['edit','write'].includes(cat)||/patch|write/.test(name)||name==='edit')return 'write';
 if(command.includes('git')||name.includes('git'))return 'git';
 if(cat==='network'||/web|fetch/.test(name))return 'network';
 return ['shell','command'].includes(cat)||/exec|terminal/.test(name)||name==='bash'?'run':'other';
}
export function recordedToolMarks(tools:LabelTool[]):ActivityMark[]{return tools.flatMap(tool=>{const start=tool.start?Date.parse(tool.start):NaN,end=tool.end?Date.parse(tool.end):NaN;if(!Number.isFinite(start))return [];const known=Number.isFinite(end)&&end>=start;return [{start,end:known?end:start,end_known:known,category:activityCategory(tool),count:1,error:tool.status==='error',tool_id:tool.id,message_id:tool.message_id,name:tool.name,preview:tool.preview,preview_truncated:tool.preview_truncated,status:tool.status}];});}
/** Elapsed attribution between observations; never a measurement of processing. */
export function labelIntervals(messages:LabelMessage[],marks:ActivityMark[],maxGap=30*60*1000,neighbors=messages){
 const boundaries=[...new Set([...messages,...neighbors].map(eventTime).filter((at):at is number=>at!==null).concat(marks.flatMap(mark=>mark.end_known?[mark.start,mark.end]:[mark.start])))].sort((a,b)=>a-b),positions=new Map(boundaries.map((at,i)=>[at,i]));
 return messages.flatMap(message=>{const at=eventTime(message);if(at===null)return [];const i=positions.get(at)!,neighbor=isThinking(message)?boundaries[i-1]:boundaries[i+1],spanning=neighbor!==undefined&&Math.abs(neighbor-at)<=maxGap;return [{message,start:spanning?Math.min(at,neighbor):at,end:spanning?Math.max(at,neighbor):at,attribution:spanning?(isThinking(message)?'preceding':'following'):'instant'} as const];});
}

/** Where a share of a message's text falls in time: the message's span on
 * the ribbon (labelIntervals), start + share × duration, the mapping its
 * text-origin parts use. A message with no duration, or a share that was not
 * recorded, stays at the message's own time. */
export function textTime(span:{start:number;end:number;at:number},share?:number|null){
 return share==null||!Number.isFinite(share)||span.end<=span.start?span.at:span.start+(span.end-span.start)*Math.max(0,Math.min(1,share));
}
/** Times for agent events on a session's clock: each event at its sentence's
 * share of its message (AgentEventLink.position), and each target at its
 * quoted text's share of the target message, so events spread through the
 * message the way its message modes do instead of piling at one end. A
 * target outside these messages (another session's) uses its own time. */
export function agentEventClock(messages:LabelMessage[],marks:ActivityMark[],neighbors=messages){
 const spans=new Map(labelIntervals(messages,marks,30*60*1000,neighbors).map(({message,start,end})=>[message.id,{start,end,at:eventTime(message)!}]));
 for(const m of neighbors)if(!spans.has(m.id)){const at=eventTime(m);if(at!==null)spans.set(m.id,{start:at,end:at,at});}
 return {
  at:(e:AgentEventLink)=>{const span=spans.get(e.message_id);return span?textTime(span,e.position):null;},
  targetAt:(e:AgentEventLink)=>{const t=e.target;if(!t?.message_id)return null;const span=spans.get(t.message_id);if(span)return textTime(span,t.position);const at=t.at?Date.parse(t.at):NaN;return Number.isFinite(at)?at:null;},
 };
}

/** The same origin run owns its texture, hover excerpt and drill-down span. */
export function ribbonOriginParts(item:RibbonObservation):RibbonObservation[]{
 if(!item.message)return [];
 const message=item.message;
 return authorshipSegments(message).map((part,index)=>({...item,id:JSON.stringify(['authorship',message.id,index,item.label]),start:item.start+(item.end-item.start)*part.start,end:item.start+(item.end-item.start)*part.end,title:callerSource(part.category).name,excerpt:authorshipExcerpt(message,part),authorship:part.category,share:part.end-part.start,composition:{start:part.start,end:part.end}}));
}

/** Thinking is a message kind, not a third function. Split it out before roles. */
export function sessionRibbonTree(messages:LabelMessage[],marks:ActivityMark[],inferred:{start:number;end:number}[],taxonomy:Taxonomy,range:TimeRange,detail=true,caller=false,neighbors=messages,boundaryMarks=marks,events:AgentEventLink[]=[]):RibbonNode{
 const intervals=labelIntervals(messages,boundaryMarks,30*60*1000,neighbors);
 const labels:RibbonObservation[]=intervals.flatMap(({message,start,end,attribution})=>{if(start>range[1]||end<range[0])return [];const ids=functionStates(message,detail?'label':'family');return (ids.length?ids:['?']).map(label=>({id:JSON.stringify(['label',message.id,label]),start,end,attribution,title:shortLabel(label,taxonomy),color:stateColor(label),thinking:isThinking(message),label,message}));});
 const activities:RibbonObservation[]=[...marks.flatMap(mark=>mark.start>range[1]||Math.max(mark.start,mark.end_known?mark.end:mark.start)<range[0]?[]:[{id:JSON.stringify(['activity',mark.message_id,mark.tool_id,mark.name,mark.start,mark.end,mark.category]),start:mark.start,end:mark.end_known?Math.max(mark.start,mark.end):mark.start,title:mark.name,color:mark.error?'var(--activity-error)':`var(--activity-${mark.category})`,thinking:false,mark}]),...inferred.filter(span=>span.start<=range[1]&&span.end>=range[0]).map(span=>({id:JSON.stringify(['inferred',span.start,span.end]),...span,title:'Inferred orchestration',color:'var(--activity-model)',thinking:false,inferred:true}))];
 const leaves=(items:RibbonObservation[],path:string):RibbonNode[]=>items.map(item=>({id:path+':'+item.id,title:item.message?`${item.message.role} · ${item.message.kind} · ${new Date(item.start).toLocaleTimeString()}`:item.title,items:[item],children:[]}));
 function taxonomyNode(id:string,items:RibbonObservation[],path:string):RibbonNode{
  const exact=items.filter(item=>item.label===id),childIDs=[...new Set(items.filter(item=>item.label!==id).map(item=>item.label!.split('.').slice(0,id.split('.').length+1).join('.')))].sort(compareStates);
  const children=childIDs.map(child=>taxonomyNode(child,items.filter(item=>item.label===child||item.label!.startsWith(child+'.')),path+':'+child));
  return {id:path,title:shortLabel(id,taxonomy),items,children:[...children,...leaves(exact,path)]};
 }
 function roles(items:RibbonObservation[],path:string):RibbonNode[]{
  return ['P','C','?'].flatMap(id=>{const found=items.filter(item=>id==='?'?item.label==='?':item.label===id||item.label!.startsWith(id+'.'));if(!found.length)return [];return [id==='?'?{id:path+':uncategorized',title:'Uncategorized',items:found,children:leaves(found,path+':uncategorized')}:{...taxonomyNode(id,found,path+':'+id),title:functionRole(id)}];});
 }
 const plain=labels.filter(item=>!item.thinking),thinking=labels.filter(item=>item.thinking);
 const thoughtRoles=roles(thinking,'labels:thinking');
 // A Thinking branch with only one function role does not need a duplicate row.
 const thoughtNode=group('labels:thinking',thoughtRoles.length===1?'Thinking · '+thoughtRoles[0].title:'Thinking',thoughtRoles.length===1?thoughtRoles[0].children:thoughtRoles);
 const labelNode=group('labels','Message modes',[...roles(plain,'labels'),...(thinking.length?[thoughtNode]:[])]);
 const categories=[...new Set(activities.map(item=>item.inferred?'model':item.mark!.category))];
 const activityNode=group('activity','Tool actions',categories.map(category=>{const items=activities.filter(item=>(item.inferred?'model':item.mark!.category)===category),names=[...new Set(items.map(item=>item.title))];return {id:'activity:'+category,title:activityNames[category]??category,items,children:names.map(name=>{const found=items.filter(item=>item.title===name),id='activity:'+category+':'+name;return found.length===1?{...leaves(found,id)[0],title:name}:group(id,name,leaves(found,id));})};}));
 const originMessages:RibbonNode[]=caller?intervals.flatMap(({message,start,end})=>{
  if(start>range[1]||end<range[0])return [];
  const ids=functionStates(message,detail?'label':'family'),labels=ids.length?ids:['?'],path='authorship:'+message.id;
  const tracks=labels.map(label=>ribbonOriginParts({id:'',start,end,title:'',color:stateColor(label),thinking:false,label,message}));
  const parts=tracks[0].map((part,index)=>({id:path+':'+index,title:part.title,items:tracks.map(track=>track[index]),children:[]}));
  return [group(path,`${message.role} · ${new Date(start).toLocaleTimeString()}`,parts)];
 }):[];
 const authorship=group('authorship','Text origin',originMessages);
 // Agent events decompose like Tool actions: group, then kind, then each event.
 // They draw through the event overlay (shapes and arcs), not as ribbon glyphs.
 const clock=agentEventClock(messages,boundaryMarks,neighbors);
 const eventItems:RibbonObservation[]=events.flatMap(e=>{const at=clock.at(e);return at===null||at<range[0]||at>range[1]?[]:[{id:JSON.stringify(['event',e.id]),start:at,end:at,title:e.label,color:eventColor(e.group),thinking:e.source==='thinking',event:e}];});
 const eventNode=group('events','Agent events',EVENT_GROUPS.flatMap(g=>{const items=eventItems.filter(item=>eventGroup(item.event!.group).key===g.key);if(!items.length)return [];const kinds=[...new Set(items.map(item=>item.title))];return [{id:'events:'+g.key,title:g.short,items,children:kinds.map(kind=>{const found=items.filter(item=>item.title===kind),id='events:'+g.key+':'+kind;return group(id,kind,found.map(item=>({id:id+':'+item.event!.id,title:item.event!.sentence,items:[item],children:[]})));})}];}));
 // Agent events come before Tool actions: they read with the message modes they sit in.
 return group('session','Session',[labelNode,authorship,eventNode,activityNode].filter(node=>node.items.length));
}
/** Session summaries count source messages once across every visible layer.
 * Agent-event rows count classifications, even when several share a message. */
export function ribbonEventCount(items:RibbonObservation[],scope:'session'|'events'='session'){
 if(scope==='events')return new Set(items.flatMap(item=>item.event?[item.event.id]:[])).size;
 const messages=new Set(items.flatMap(item=>item.message?[item.message.id]:item.event?[item.event.message_id]:[]));
 const marks=new Map(items.filter(item=>item.mark).map(item=>[item.id,item.mark!]));
 for(const mark of marks.values())if(mark.message_id&&(mark.count??1)>0)messages.add(mark.message_id);
 const inferred=new Set(items.filter(item=>item.inferred).map(item=>item.id));
 return messages.size+[...marks.values()].reduce((total,mark)=>total+Math.max(0,(mark.count??1)-(mark.message_id?1:0)),0)+inferred.size;
}
export function ribbonCounts(items:RibbonObservation[]){return {events:new Set(items.flatMap(item=>item.event?[item.event.id]:[])).size,messages:new Set(items.flatMap(item=>item.message?[item.message.id]:[])).size,thinking:new Set(items.flatMap(item=>item.message&&item.thinking?[item.message.id]:[])).size,activity:items.reduce((n,item)=>n+(item.mark?.count??0),0),inferred:items.filter(item=>item.inferred).length};}
/** Hit testing includes every overlapping glyph/span, including covered marks. */
export function ribbonHits(items:RibbonObservation[],at:number,tolerance:number){
 const hits=items.filter(item=>item.start<=at+tolerance&&item.end>=at-tolerance);
 // A generous target helps tiny tool marks, but a text-origin hover should
 // inspect the run under the pointer rather than nearby, different text.
 const exact=new Set(hits.filter(item=>item.composition&&item.end>item.start&&item.start<=at&&(item.end>at||item.end===at&&item.composition.end===1)));
 const messages=new Set([...exact].map(item=>item.message!.id));
 return hits.filter(item=>!item.composition||!messages.has(item.message!.id)||exact.has(item));
}

/** Tool dots use the ribbon's shared mark placement (ribbon-marks.ts): each
 * call takes the first lane, centre out, where its dot and duration touch no
 * other call or event marker, so simultaneous and near-simultaneous calls sit
 * apart whenever the row has room. x maps a time to the ribbon's pixels. */
export const TOOL_RADIUS=3,MARK_GAP=.5;
export function ribbonToolPlacement(items:RibbonObservation[],x:(at:number)=>number,height:number,occupied:PlacedMark[]=[],r=TOOL_RADIUS,gap=MARK_GAP){
 const lanes=laneCandidates(height,r,gap),tools=[...new Map(items.filter(item=>item.mark).map(item=>[item.id,item])).values()];
 return new Map(placeMarks(tools.map(item=>laneMark(item.id,x(item.start),r,lanes,item.end>item.start?x(item.end):x(item.start))),{gap,occupied}).map(m=>[m.id,m]));
}
export type RibbonHitTarget=HitTarget&{ids:string[]};
/** One hit test for every mark in a ribbon: the tool dot or event marker
 * nearest the pointer (within slack pixels) wins; otherwise the message
 * spans under it. pointer is in the ribbon's pixels. */
export function ribbonPointerHits<T extends RibbonHitTarget>(items:RibbonObservation[],at:number,tolerance:number,pointer:{x:number;y:number},targets:T[],slack=4):{items:RibbonObservation[];target:T|null}{
 const target=nearestMark(targets,pointer.x,pointer.y,slack);
 if(target){const ids=new Set(target.ids);return {items:items.filter(item=>ids.has(item.id)),target};}
 return {items:ribbonHits(items.filter(item=>!item.mark&&!item.event),at,tolerance),target:null};
}

/** Common modes form the backdrop. Rarer modes sit above them, then tool rails
 * (placed by ribbonToolPlacement).
 * Counts use distinct messages, so coexisting labels do not inflate support. */
export function ribbonLayers(items:RibbonObservation[]){
 const support=new Map<string,Set<string>>();
 for(const item of items)if(item.label&&item.message){const ids=support.get(item.label)??new Set<string>();ids.add(item.message.id);support.set(item.label,ids);}
 const frequencies=[...new Set([...support.values()].map(ids=>ids.size))].sort((a,b)=>b-a);
 return [...items].sort((a,b)=>Number(!a.message)-Number(!b.message)||(support.get(b.label??'')?.size??0)-(support.get(a.label??'')?.size??0)||(b.end-b.start)-(a.end-a.start)||a.id.localeCompare(b.id)).map(item=>{
  const rank=Math.max(0,frequencies.indexOf(support.get(item.label??'')?.size??0)),height=item.message?Math.max(8,32-rank*5):item.inferred?4:8;
  return {item,height,y:item.message?(32-height)/2:16-height/2};
 });
}

/** Reveal the retained source, preserving the current workspace's timeline state. */
export function ribbonSourceURL(item:Pick<RibbonObservation,'start'> & Partial<RibbonObservation>,source:{workspace:string;conversation:string;session:string},currentURL:string){
 const workspace=item.message?.work_id||source.workspace,conversation=item.message?.conversation_id||source.conversation;
 const message=item.message?.id||item.mark?.message_id,call=item.mark?.tool_id;
 if(!workspace||!conversation||!message&&!call)return null;
 const path=`/work/${encodeURIComponent(workspace)}`,current=new URL(currentURL,'http://pharos.local');
 const params=current.pathname===path||current.pathname.startsWith(path+'/')?new URLSearchParams(current.search):new URLSearchParams();
 for(const key of ['message','call','at','reveal','reader_view','delegation_event'])params.delete(key);
 params.set('reader_view','conversation');params.set('conversation',conversation);params.set('delegation_session',item.message?.session_id||source.session);
 if(message){params.set('message',message);params.set('reveal',`message:${message}`);}
 if(!message&&call)params.set('reveal',`call:${call}`);
 if(call){params.set('call',call);params.set('at',new Date(item.start).toISOString());}
 return `${path}?${params}`;
}
