import {compareStates,functionStates,mainSession,scopedMessages,stateRuns,workflowSessions,type LabelConversation,type LabelMessage,type LabelSession,type LabelStream,type Resolution,type StepMode,type TrajectoryScope} from './label-trajectories-model';

// Actor seats are transport/authorship evidence, independent of function labels.
export function identitySeat(m:LabelMessage):string {
  if(m.segment.startsWith('H-'))return 'human';
  if(m.segment==='AIUR')return 'agent-input';
  if(m.role==='user')return 'incoming';
  if(m.role==='tool')return 'tool';
  return 'agent';
}
export const seatOrder=['human','agent-input','incoming','agent','tool'];
export function seatName(id:string,session:LabelSession):string {
  return id==='human'?'Caller · human input':id==='agent-input'?'Caller · agent input':id==='incoming'?'Incoming · author unverified':id==='tool'?'Tool results':session.name;
}
export function identitySeats(messages:LabelMessage[],session:LabelSession) {
  return seatOrder.map(id=>({id,name:seatName(id,session),events:messages.flatMap((message,index)=>identitySeat(message)===id?[{message,index}]:[])})).filter(s=>s.events.length);
}
export function branchStreams(c:LabelConversation,scope:TrajectoryScope,nested=true):LabelStream[] {
  return workflowSessions(c).filter(s=>nested||s.id===mainSession(c)).map(session=>({id:session.id,session,conversation:c,messages:scopedMessages(c,scope,session.id)}));
}
export function labelScoreRows(messages:LabelMessage[],session:LabelSession,resolution:Resolution) {
  return identitySeats(messages,session).map(seat=>({...seat,labels:[...new Set(seat.events.flatMap(e=>functionStates(e.message,resolution)))].sort(compareStates)}));
}
export type CoverageKind='sender'|'receiver'|'both'|'none'|'unlabeled';
export const coverageKinds:CoverageKind[]=['sender','receiver','both','none','unlabeled'];
export const coverageNames:Record<CoverageKind,string>={sender:'Sender only',receiver:'Receiver only',both:'Both functions',none:'No accepted match',unlabeled:'Unlabeled'};
export function coverageKind(m:LabelMessage):CoverageKind {
  if(!m.chunks)return 'unlabeled';
  const states=functionStates(m),p=states.some(id=>id==='P'||id.startsWith('P.')),c=states.some(id=>id==='C'||id.startsWith('C.'));
  return p&&c?'both':p?'sender':c?'receiver':'none';
}
export const structuralRole=(m:LabelMessage)=>m.role==='user'?'incoming':m.role==='assistant'||m.role==='agent'?'agent':m.role==='tool'?'tool':'other';
export function coveragePartitions(streams:LabelStream[]) {
  return ['incoming','agent','tool','other'].map(role=>{
    const all=streams.flatMap(stream=>stream.messages.filter(m=>structuralRole(m)===role).map(message=>({stream,message})));
    return {role,total:all.length,parts:coverageKinds.map(kind=>{const matches=all.filter(e=>coverageKind(e.message)===kind);return {kind,count:matches.length,examples:matches.slice(0,12)};})};
  }).filter(row=>row.total);
}
export function reliefCounts(m:LabelMessage,resolution:Resolution) {
  const ids=functionStates(m,resolution);
  return {sender:ids.filter(id=>id==='P'||id.startsWith('P.')).length,receiver:ids.filter(id=>(id==='C'||id.startsWith('C.'))&&!id.startsWith('C.report')).length,report:ids.some(id=>id.startsWith('C.report')),missing:!m.chunks,none:coverageKind(m)==='none'};
}
export type PrefixNode={prefix:string[];state:string|null;count:number;ended:number;missing:number;omitted:number;example?:{stream:LabelStream;message:LabelMessage};children:PrefixNode[]};
export function prefixTree(streams:LabelStream[],resolution:Resolution,mode:StepMode,prefix:string[],wide=false,previewWidth=6):PrefixNode {
  const paths=streams.map(stream=>{const runs=stateRuns(stream.messages,resolution,mode),gap=runs.findIndex(r=>!r.key);return {stream,runs:gap<0?runs:runs.slice(0,gap),missing:gap>=0||!runs.length};}).filter(path=>prefix.every((s,i)=>path.runs[i]?.key===s));
  type Paths=typeof paths;
  const build=(paths:Paths,at:string[],depth:number):PrefixNode=>{
    const node:PrefixNode={prefix:at,state:at.at(-1)??null,count:paths.length,ended:0,missing:0,omitted:0,children:[]};
    const groups=new Map<string,typeof paths>();
    for(const path of paths){const next=path.runs[at.length];if(!next){if(path.missing)node.missing++;else node.ended++;}else{const group=groups.get(next.key!)??[];group.push(path);groups.set(next.key!,group);}}
    if(!depth)return node;
    const ranked=[...groups].sort((a,b)=>b[1].length-a[1].length||a[0].localeCompare(b[0])),limit=depth===2?(wide?ranked.length:previewWidth):3;
    node.omitted=ranked.slice(limit).reduce((n,[,paths])=>n+paths.length,0);
    node.children=ranked.slice(0,limit).map(([state,paths])=>({...build(paths,[...at,state],depth-1),example:{stream:paths[0].stream,message:paths[0].runs[at.length].message}}));
    return node;
  };
  return build(paths,prefix,2);
}
