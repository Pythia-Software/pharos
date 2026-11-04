import {compareStates,functionRole,functionStates,shortLabel,stateColor,type LabelMessage,type LabelTool,type Taxonomy} from './conversation-labels';
import {eventTime,type TimeRange} from './timeline-label-model';
import {authorshipSegments,authorshipExcerpt,callerSource} from './timeline-caller-model';

export type ActivityMark={start:number;end:number;end_known:boolean;category:string;count:number;error:boolean;tool_id?:string;message_id?:string;preview?:string;preview_truncated?:boolean;status?:string;name:string};
export type RibbonObservation={id:string;start:number;end:number;title:string;color:string;thinking:boolean;label?:string;message?:LabelMessage;mark?:ActivityMark;inferred?:boolean;authorship?:string;excerpt?:{text:string;truncated:boolean}|null;composition?:{start:number;end:number};share?:number;attribution?:'preceding'|'following'|'instant'};
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

/** The same origin run owns its texture, hover excerpt and drill-down span. */
export function ribbonOriginParts(item:RibbonObservation):RibbonObservation[]{
 if(!item.message)return [];
 const message=item.message;
 return authorshipSegments(message).map((part,index)=>({...item,id:JSON.stringify(['authorship',message.id,index,item.label]),start:item.start+(item.end-item.start)*part.start,end:item.start+(item.end-item.start)*part.end,title:callerSource(part.category).name,excerpt:authorshipExcerpt(message,part),authorship:part.category,share:part.end-part.start,composition:{start:part.start,end:part.end}}));
}

/** Thinking is a message kind, not a third function. Split it out before roles. */
export function sessionRibbonTree(messages:LabelMessage[],marks:ActivityMark[],inferred:{start:number;end:number}[],taxonomy:Taxonomy,range:TimeRange,detail=true,caller=false,neighbors=messages,boundaryMarks=marks):RibbonNode{
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
 return group('session','Session',[labelNode,authorship,activityNode].filter(node=>node.items.length));
}
/** Count a retained event once even if it has labels, origin runs and a tool glyph. */
export function ribbonEventCount(items:RibbonObservation[]){
 const messages=new Set(items.flatMap(item=>item.message?[item.message.id]:[]));
 const marks=new Map(items.filter(item=>item.mark).map(item=>[item.id,item.mark!]));
 const inferred=new Set(items.filter(item=>item.inferred).map(item=>item.id));
 return messages.size+[...marks.values()].reduce((total,mark)=>total+Math.max(0,(mark.count??1)-(mark.message_id&&messages.has(mark.message_id)?1:0)),0)+inferred.size;
}
export function ribbonCounts(items:RibbonObservation[]){return {messages:new Set(items.flatMap(item=>item.message?[item.message.id]:[])).size,thinking:new Set(items.flatMap(item=>item.message&&item.thinking?[item.message.id]:[])).size,activity:items.reduce((n,item)=>n+(item.mark?.count??0),0),inferred:items.filter(item=>item.inferred).length};}
/** Hit testing includes every overlapping glyph/span, including covered marks. */
export function ribbonHits(items:RibbonObservation[],at:number,tolerance:number){
 const hits=items.filter(item=>item.start<=at+tolerance&&item.end>=at-tolerance);
 // A generous target helps tiny tool marks, but a text-origin hover should
 // inspect the run under the pointer rather than nearby, different text.
 const exact=new Set(hits.filter(item=>item.composition&&item.end>item.start&&item.start<=at&&(item.end>at||item.end===at&&item.composition.end===1)));
 const messages=new Set([...exact].map(item=>item.message!.id));
 return hits.filter(item=>!item.composition||!messages.has(item.message!.id)||exact.has(item));
}

/** Partition each connected overlap group into reusable tracks. Touching
 * durations may share a track; simultaneous point observations may not. */
export function ribbonToolTracks(items:RibbonObservation[]){
 const result=new Map<string,{slot:number;total:number}>();
 const ordered=items.filter(item=>item.mark).sort((a,b)=>a.start-b.start||b.end-a.end||a.id.localeCompare(b.id));
 let group:RibbonObservation[]=[],end=-Infinity,pointAtEnd=false;
 const finish=()=>{
  const tracks:{end:number;point:boolean}[]=[],assigned:{item:RibbonObservation;slot:number}[]=[];
  for(const item of group){
   let slot=tracks.findIndex(track=>track.end<item.start||track.end===item.start&&!track.point);
   if(slot<0)slot=tracks.length;
   tracks[slot]={end:item.end,point:item.end===item.start};assigned.push({item,slot});
  }
  for(const {item,slot} of assigned)result.set(item.id,{slot,total:tracks.length});
 };
 for(const item of ordered){
  if(group.length&&(item.start>end||item.start===end&&!pointAtEnd)){finish();group=[];end=-Infinity;pointAtEnd=false;}
  group.push(item);
  if(item.end>end){end=item.end;pointAtEnd=item.end===item.start;}
  else if(item.end===end&&item.start===item.end)pointAtEnd=true;
 }
 if(group.length)finish();
 return result;
}

/** Prefer the tool track actually under the pointer, preserving every call
 * in the layout while keeping one call's hover content unambiguous. */
export function ribbonPointerHits(items:RibbonObservation[],at:number,tolerance:number,y:number,unitsPerPixel:number,tracks=ribbonToolTracks(items)){
 const hits=ribbonHits(items,at,tolerance);
 const tools=hits.filter(item=>item.mark).map(item=>{
  const track=tracks.get(item.id)!,center=32*(track.slot+.5)/track.total,dy=Math.abs(center-y),size=Math.min(8*unitsPerPixel,32/track.total*.75);
  return {item,dy,dx:Math.max(item.start-at,at-item.end,0),near:dy<=Math.max(3*unitsPerPixel,size/2+unitsPerPixel)};
 }).filter(hit=>hit.near).sort((a,b)=>a.dy-b.dy||a.dx-b.dx||b.item.start-a.item.start||a.item.id.localeCompare(b.item.id));
 return tools.length?[tools[0].item]:hits.filter(item=>!item.mark);
}

/** Common modes form the backdrop. Rarer modes sit above them, then tool rails.
 * Counts use distinct messages, so coexisting labels do not inflate support. */
export function ribbonLayers(items:RibbonObservation[]){
 const tracks=ribbonToolTracks(items);
 const support=new Map<string,Set<string>>();
 for(const item of items)if(item.label&&item.message){const ids=support.get(item.label)??new Set<string>();ids.add(item.message.id);support.set(item.label,ids);}
 const frequencies=[...new Set([...support.values()].map(ids=>ids.size))].sort((a,b)=>b-a);
 return [...items].sort((a,b)=>Number(!a.message)-Number(!b.message)||(support.get(b.label??'')?.size??0)-(support.get(a.label??'')?.size??0)||(b.end-b.start)-(a.end-a.start)||a.id.localeCompare(b.id)).map(item=>{
  const rank=Math.max(0,frequencies.indexOf(support.get(item.label??'')?.size??0)),height=item.message?Math.max(8,32-rank*5):item.inferred?4:8;
  const track=tracks.get(item.id);
  return {item,height,y:item.message?(32-height)/2:track?32*(track.slot+.5)/track.total-height/2:16-height/2,trackCount:track?.total??1};
 });
}

/** Reveal the retained source, preserving the current workspace's timeline state. */
export function ribbonSourceURL(item:RibbonObservation,source:{workspace:string;conversation:string;session:string},currentURL:string){
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
