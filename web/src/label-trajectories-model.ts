// The experiment's calculations are DOM-free and operate on retained order.
// A dominant family is a display reduction of the most confident leaf label,
// matching the reader. Recurrence and label detail keep multiple functions.
export const labelFamilies = [
  {id:"P.request",name:"Request",color:"var(--label-request)"},
  {id:"P.frame",name:"Frame",color:"var(--label-frame)"},
  {id:"P.control",name:"Control",color:"var(--label-control)"},
  {id:"C.input",name:"Input",color:"var(--label-input)"},
  {id:"C.feedback",name:"Feedback",color:"var(--label-feedback)"},
  {id:"C.deliberation",name:"Deliberation",color:"var(--label-deliberation)"},
  {id:"C.report",name:"Report",color:"var(--label-report)"},
  {id:"C.difficulty",name:"Difficulty",color:"var(--label-difficulty)"},
  {id:"root",name:"Generic",color:"var(--muted)"},
  {id:"none",name:"No match",color:"color-mix(in srgb,var(--muted) 45%,var(--panel))"},
  {id:"unknown",name:"Unlabeled",color:"var(--line)"},
] as const;
export type Family = typeof labelFamilies[number]["id"];
// Authorship runs follow original text order; a category may occur more than once.
export type LabelMessage = {preview?:string;preview_truncated?:boolean;authorship?:{category:string;bytes:number;text?:string;truncated?:boolean}[];generation_model?:string;session_id?:string;conversation_id?:string;work_id?:string;order?:number|null;call_id?:string|null;id:string;role:string;kind:string;at:string|null;segment:string;labels:[string,number][];text:string;text_source:string;truncated:boolean;chunks:number};
export type LabelTool={preview?:string;preview_truncated?:boolean;id:string;session_id:string;message_id?:string;name:string;category:string;command_category?:string;status:string;start:string|null;end:string|null;duration_ms?:number|null};
export type LabelConversation = {activity?:LabelTool[]|null;sessions?:LabelSession[];main_session?:string;id:string;work_id:string;title:string;repository:string;provider:string;started_at:string|null;messages:LabelMessage[]};
export type LabelSnapshot = {version:1|2;source:{kind:string;catalog:string;exported_at:string;models:string[];taxonomy:string;ledger:{messages:number;conversations:number;first_labeled:string;last_labeled:string};candidate_conversations:number;limit:number;minimum_labeled_prose:number;selection:string};taxonomy:{id:string;parent?:string|null;name:string;definition:string}[];conversations:LabelConversation[]};
export const trajectoryViews = [
  {id:"distribution",number:"18",name:"Labels over time",group:"revised",question:"How do message functions change over time?",description:"Explore label frequency over time, browse its hierarchy and open matching conversations. Human input, agent output and thinking have distinct populations."},
  {id:"fingerprints",number:"01",name:"Fingerprints",group:"revised",question:"What does this conversation look like when identities have room?",description:"Compact ribbons summarize volume, functions and nesting. Centered diagonal hatching shows extra overlapping branches. Color blocks give each workflow a reusable identity; inspect any segment for its labels."},
  {id:"timeline",number:"00",name:"Workspace timeline",group:"revised",question:"What do the labels add to the workspace timeline?",description:"One ribbon per execution session on the existing workspace clock. Expand Message modes and Tool actions recursively to individual observations; thinking keeps its functions inside its own branch."},
  {id:"flow",number:"02",name:"Phase flow",group:"revised",question:"How does initiating work turn into responding work?",description:"Break the same additive hierarchy down by phase, recorded role, generation model, provider or nesting depth. Other and Uncategorized account for the remaining message weight. Choose a cell to browse matching conversations."},
  {id:"transitions",number:"03",name:"Transitions",group:"archive",question:"Which Sender and Receiver states follow one another?",description:"Observed next-state incidences within a branch, grouped by the label hierarchy. One multi-function message can support several edges. Counts measure adjacent pairs, not exclusive probabilities."},
  {id:"path",number:"05",name:"State path",group:"revised",question:"What does the conversation sound like on a score?",description:"A Sender / Receiver duet, an ensemble of agent staves, or a choir grouped by nesting depth. Chords retain coexisting labels, with compact family lines and explicit call connections."},
  {id:"motifs",number:"06",name:"Recurring motifs",group:"revised",question:"Which short state paths recur across workflows?",description:"Three observed states, each retaining its full Sender/Receiver label set. Repeated identical states collapse in State changes mode. Missing labels break paths; independent branches never become adjacent."},
  {id:"labels",number:"09",name:"Label detail",group:"revised",question:"What is happening inside each Sender or Receiver phase?",description:"Inspect a branch in message order. Expand the label hierarchy, see coverage gaps, select a message to read its text, or explore a label over time."},
  {id:"graph",number:"10",name:"Machine map",group:"archive",question:"What does the observed state machine look like?",description:"A directed map of observed label-to-label edges, with Sender and Receiver regions. Inspect a state or connection to see its source. This describes observed paths, not allowed transitions or causal rules."},
  {id:"replay",number:"11",name:"Step through",group:"archive",question:"What changes at this exact step?",description:"Replay a real branch one message at a time. Compare the previous, current and next state, retaining simultaneous Sender and Receiver functions and the actor who produced them."},
  {id:"calls",number:"12",name:"Call / result",group:"archive",question:"How does one machine invoke another?",description:"Recorded delegation calls open child branches; matched tool results close the native call. A result may only acknowledge an asynchronous launch. It does not prove the child finished its work."},
  {id:"trie",number:"13",name:"Branching paths",group:"revised",question:"Where do workflows with the same beginning diverge?",description:"An empirical prefix tree of complete function states. Follow a branch to condition the next-step distribution. Captured ends and missing-label boundaries remain distinct."},
  {id:"cycles",number:"14",name:"Return loops",group:"archive",question:"Which state does the work return to?",description:"Closed observed paths of two to five edges, after optional repeat compression. Loops describe recurring message functions; they do not establish repeated semantic content, wasted work or failure."},
] as const;
export type TrajectoryView = typeof trajectoryViews[number]["id"];
export type TrajectoryScope = "prose"|"thinking";
export const familyInfo = (id:Family) => labelFamilies.find(f=>f.id===id)!;
export function specificLabels(message:LabelMessage):[string,number][] {
  return message.labels.filter(([id])=>!message.labels.some(([other])=>other.startsWith(id+".")))
    .sort((a,b)=>b[1]-a[1]||a[0].localeCompare(b[0]));
}
export function familySet(message:LabelMessage):Family[] {
  return [...new Set(message.labels.map(([id])=>id.split(".").slice(0,2).join(".")).filter(id=>labelFamilies.some(f=>f.id===id)))] as Family[];
}
export function dominantFamily(message:LabelMessage):Family {
  if(!message.chunks)return "unknown";
  for(const [id] of specificLabels(message)) {
    const family=id.split(".").slice(0,2).join(".");
    if(labelFamilies.some(f=>f.id===family))return family as Family;
  }
  return message.labels.length?"root":"none";
}
export function scopedMessages(conversation:LabelConversation, scope:TrajectoryScope, session=mainSession(conversation)):LabelMessage[] {
  return conversation.messages.filter(m=>(m.session_id??mainSession(conversation))===session && (scope==="thinking"||["message","delegation","delegation_result"].includes(m.kind)));
}
export function transitions(sequences:LabelMessage[][]) {
  const n=labelFamilies.length, counts=Array.from({length:n},()=>Array(n).fill(0) as number[]);
  const index=(m:LabelMessage)=>labelFamilies.findIndex(f=>f.id===dominantFamily(m));
  for(const sequence of sequences)for(let i=1;i<sequence.length;i++)counts[index(sequence[i-1])][index(sequence[i])]++;
  return counts.map(row=>({counts:row,total:row.reduce((a,b)=>a+b,0)}));
}
export function phaseBin(index:number,length:number,bins=20) {
  return Math.min(bins-1,Math.floor(index*bins/Math.max(1,length-1)));
}
export function phaseFlow(sequences:LabelMessage[][],bins=20) {
  const result=Array.from({length:bins},()=>({shares:Array(labelFamilies.length).fill(0) as number[],support:0}));
  for(const sequence of sequences) {
    const local=Array.from({length:bins},()=>Array(labelFamilies.length).fill(0) as number[]);
    sequence.forEach((m,i)=>local[phaseBin(i,sequence.length,bins)][labelFamilies.findIndex(f=>f.id===dominantFamily(m))]++);
    local.forEach((counts,i)=>{const total=counts.reduce((a,b)=>a+b,0);if(!total)return;result[i].support++;counts.forEach((count,j)=>result[i].shares[j]+=count/total);});
  }
  for(const bin of result)if(bin.support)bin.shares=bin.shares.map(n=>n/bin.support);
  return result;
}
export function familyRuns(messages:LabelMessage[]) {
  const runs:{family:Family;start:number;length:number}[]=[];
  messages.forEach((m,i)=>{const family=dominantFamily(m),last=runs.at(-1);if(last?.family===family)last.length++;else runs.push({family,start:i,length:1});});
  return runs;
}
export function motifs(conversations:LabelConversation[],scope:TrajectoryScope) {
  const found=new Map<string,{families:Family[];occurrences:number;conversations:string[];first:Record<string,string>}>();
  for(const c of conversations) {
    const messages=scopedMessages(c,scope),runs=familyRuns(messages);
    for(let i=0;i+2<runs.length;i++) {
      const families=runs.slice(i,i+3).map(r=>r.family);
      if(families.some(f=>["unknown","root","none"].includes(f)))continue;
      const key=families.join("/"),item=found.get(key)??{families,occurrences:0,conversations:[],first:{}};
      item.occurrences++;
      if(!item.conversations.includes(c.id)){item.conversations.push(c.id);item.first[c.id]=messages[runs[i].start].id;}
      found.set(key,item);
    }
  }
  return [...found.entries()].map(([id,item])=>({id,...item})).sort((a,b)=>b.conversations.length-a.conversations.length||b.occurrences-a.occurrences||a.id.localeCompare(b.id));
}
export function turnPairs(messages:LabelMessage[]) {
  const turns:{human:LabelMessage|null;agent:LabelMessage[]}[]=[];
  for(const message of messages) {
    const human=message.role==="user"&&(message.segment.startsWith("H-")||!message.segment);
    if(human)turns.push({human:message,agent:[]});
    else {if(!turns.length)turns.push({human:null,agent:[]});turns.at(-1)!.agent.push(message);}
  }
  return turns;
}
export function jaccard(a:LabelMessage,b:LabelMessage):number|null {
  const left=familySet(a),right=familySet(b);
  if(!left.length||!right.length)return null;
  return left.filter(f=>right.includes(f)).length/new Set([...left,...right]).size;
}
export function shape(messages:LabelMessage[]) {
  const counts=new Map<Family,number>();let switches=0,pairs=0;
  const families=messages.map(dominantFamily);
  const missing=(f:Family)=>f==="unknown"||f==="none";
  families.forEach((f,i)=>{if(missing(f))return;counts.set(f,(counts.get(f)??0)+1);if(i&&!missing(families[i-1])){pairs++;if(families[i-1]!==f)switches++;}});
  const total=[...counts.values()].reduce((a,b)=>a+b,0);
  const diversity=total?[...counts.values()].reduce((sum,n)=>{const p=n/total;return sum-p*Math.log2(p);},0)/Math.log2(labelFamilies.length-2):0;
  const dominant=[...counts].sort((a,b)=>b[1]-a[1]||a[0].localeCompare(b[0]))[0]?.[0]??"unknown";
  return {diversity,switchRate:pairs?switches/pairs:0,pairs,labeled:total,dominant};
}
export function messageURL(conversation:LabelConversation,message?:Pick<LabelMessage,'id'|'conversation_id'|'work_id'>) {
  const params=new URLSearchParams({conversation:message?.conversation_id??conversation.id,reader_labels:"1"});
  if(message)params.set("reveal",`message:${message.id}`);
  return `/work/${encodeURIComponent(message?.work_id??conversation.work_id)}?${params}`;
}

export type LabelSession={id:string;parent_id:string|null;conversation_id:string;work_id:string;name:string;kind:string;depth:number;link_evidence:string;call_message_id:string|null;result_message_id:string|null;labels_pending?:boolean;model?:string;provider?:string};
export type Resolution="family"|"label";
export type StepMode="changes"|"messages";
export type LabelStream={sourceMessages?:LabelMessage[];id:string;conversation:LabelConversation;session:LabelSession;messages:LabelMessage[]};
export type Taxonomy=LabelSnapshot["taxonomy"];
export const mainSession=(c:LabelConversation)=>c.main_session??c.id;
export function workflowSessions(c:LabelConversation):LabelSession[] {
  if(!c.sessions)return [{id:c.id,parent_id:null,conversation_id:c.id,work_id:c.work_id,name:"Main agent",kind:"root",depth:0,link_evidence:"Earlier export: main session only",call_message_id:null,result_message_id:null}];
  const children=new Map<string,LabelSession[]>(),known=new Set(c.sessions.map(s=>s.id)),ordered:LabelSession[]=[],seen=new Set<string>();
  for(const session of c.sessions)if(session.parent_id){const siblings=children.get(session.parent_id)??[];siblings.push(session);children.set(session.parent_id,siblings);}
  const visit=(session:LabelSession)=>{if(seen.has(session.id))return;seen.add(session.id);ordered.push(session);for(const child of children.get(session.id)??[])visit(child);};
  const main=c.sessions.find(s=>s.id===mainSession(c));if(main)visit(main);
  for(const session of c.sessions)if(!session.parent_id||!known.has(session.parent_id))visit(session);
  for(const session of c.sessions)visit(session);
  return ordered;
}
export function collectStreams(conversations:LabelConversation[],scope:TrajectoryScope,nested=true):LabelStream[] {
  const seen=new Set<string>();
  return conversations.flatMap(c=>workflowSessions(c).filter(s=>nested||s.id===mainSession(c)).flatMap(session=>{
    if(seen.has(session.id))return [];seen.add(session.id);
    return [{id:session.id,conversation:c,session,messages:scopedMessages(c,scope,session.id)}];
  }));
}
export const functionRole=(id:string)=>id==="P"||id.startsWith("P.")?"Sender":id==="C"||id.startsWith("C.")?"Receiver":"Unclassified";
export const stateFamily=(id:string):Family=>labelFamilies.find(f=>f.id===id.split('.').slice(0,2).join('.'))?.id??"root";
// Colors distinguish semantic branches; leaf identity is also named on hover and
// in the detail rows. Avoid pretending dozens of near-identical shades are a key.
const labelPalette:Record<string,string>={
 'P':'#28bfdf','P.request':'#399bed','P.request.work':'#399bed',
 'P.request.work.investigate':'#20bfae','P.request.work.implement':'#399bed','P.request.work.review':'#b36df1','P.request.work.revise':'#678fe9','P.request.work.verify':'#2bc48e',
 'P.request.input':'#28bfdf','P.request.input.information':'#28bfdf','P.request.input.clarification':'#53c8ed','P.request.input.choice':'#7b73ed','P.request.input.permission':'#b76de5','P.request.input.guidance':'#28ad96',
 'P.frame':'#36b4e9','P.frame.scope':'#32b4e8','P.frame.constraint':'#4a88e3','P.frame.criteria':'#777ae5',
 'P.control':'#ac62df','P.control.continue':'#ac62df','P.control.redirect':'#8060dc','P.control.pause':'#9d73df','P.control.cancel':'#b760b5',
 'C':'#f4b632','C.input':'#e6cd35','C.input.answer':'#e6cd35','C.input.option':'#dbb323','C.input.permission':'#e5c347','C.input.permission.grant':'#e5c347','C.input.permission.deny':'#ee7652','C.input.permission.defer':'#d7a93e',
 'C.feedback':'#f28a30','C.feedback.accept':'#f5c33d','C.feedback.qualify':'#f28a30','C.feedback.challenge':'#ed6742','C.feedback.reject':'#e04450',
 'C.deliberation':'#f16b52','C.deliberation.plan':'#f47843','C.deliberation.hypothesis':'#dc54b9','C.deliberation.evidence':'#e65075','C.deliberation.reconsider':'#ef638e',
 'C.difficulty':'#ed505d','C.difficulty.ambiguity':'#ed7762','C.difficulty.inconsistency':'#d84d83','C.difficulty.guidance':'#e68540','C.difficulty.blocker':'#e42d48',
 'C.report':'#efbb2b','C.report.intent':'#ed7846','C.report.progress':'#f4b632',
 'C.report.product':'#ef9535','C.report.finding':'#eace32','C.report.verification':'#d3c437',
 'C.report.completion':'#f1c13d','C.report.limitation':'#d7a02f',
};
export function stateColor(id:string):string {
  if(['?','none','unknown'].includes(id))return 'color-mix(in srgb,var(--muted) 30%,transparent)';
  const parts=id.split('.');
  while(parts.length){const color=labelPalette[parts.join('.')];if(color)return color;parts.pop();}
  return 'var(--muted)';
}
const briefLabels:Record<string,string>={
  'P.request.work':'Work','P.request.input':'Ask for input','P.request.input.information':'Information',
  'P.frame':'Frame','P.frame.scope':'Scope','P.frame.criteria':'Criteria','P.control':'Control',
  'C.input':'Input','C.input.answer':'Answer','C.input.option':'Choose',
  'C.input.permission':'Permission response','C.feedback':'Feedback','C.feedback.challenge':'Challenge',
  'C.report':'Report','C.report.product':'Deliver','C.report.finding':'Finding',
  'C.report.verification':'Check','C.report.completion':'Completion claim','C.report.limitation':'Limits',
  'C.deliberation':'Deliberation','C.deliberation.plan':'Plan','C.deliberation.hypothesis':'Hypothesis',
  'C.deliberation.evidence':'Evidence','C.deliberation.reconsider':'Reconsider',
  'C.difficulty':'Difficulty','C.difficulty.ambiguity':'Missing context','C.difficulty.inconsistency':'Conflict',
  'C.difficulty.guidance':'Missing guidance',
};
export const shortLabel=(id:string,taxonomy:Taxonomy=[])=>briefLabels[id]??stateName(id,taxonomy);
export function labelHint(id:string,taxonomy:Taxonomy=[]):string {
  const definition=taxonomy.find(t=>t.id===id)?.definition;
  const family=stateFamily(id),context=family==='root'?functionRole(id):functionRole(id)+' · '+shortLabel(family,taxonomy);
  return [shortLabel(id,taxonomy),context,definition].filter(Boolean).join('\n');
}
export const matchesLabel=(m:LabelMessage,id:string)=>functionStates(m,'label').some(label=>label===id||label.startsWith(id+'.'));
export function compareStates(a:string,b:string):number {
  const root=(id:string)=>functionRole(id)==='Sender'?0:1;
  const rank=(id:string)=>id==='P'||id==='C'?-1:labelFamilies.findIndex(f=>f.id===stateFamily(id));
  return root(a)-root(b)||rank(a)-rank(b)||a.localeCompare(b);
}
export function stateName(id:string,taxonomy:Taxonomy=[]):string {
  if(id==="P")return "Sender Generic";
  if(id==="C")return "Receiver Generic";
  if(id==="?")return "Unlabeled boundary";
  return taxonomy.find(row=>row.id===id)?.name??labelFamilies.find(f=>f.id===id)?.name??id.split('.').at(-1)!;
}
export function hierarchy(id:string,taxonomy:Taxonomy=[]):string[] {
  if(id==="P"||id==="C")return [stateName(id,taxonomy)];
  const parts=id.split('.');return parts.map((_,i)=>i?stateName(parts.slice(0,i+1).join('.'),taxonomy):functionRole(id));
}
export function functionStates(m:LabelMessage,resolution:Resolution="family"):string[] {
  if(!m.chunks)return [];
  const ids=specificLabels(m).map(([id])=>resolution==="family"&&id.includes('.')?id.split('.').slice(0,2).join('.'):id);
  return [...new Set(ids)].filter(id=>id==="P"||id==="C"||id.startsWith("P.")||id.startsWith("C.")).sort(compareStates);
}
export const stateKey=(m:LabelMessage,resolution:Resolution="family")=>functionStates(m,resolution).join('|')||null;
export function stateTitle(key:string,taxonomy:Taxonomy=[]):string {
  if(key==="?")return "Unlabeled boundary";
  return ['P','C'].flatMap(root=>{const ids=key.split('|').filter(id=>id===root||id.startsWith(root+'.'));return ids.length?[ids.length===1&&ids[0]===root?stateName(root,taxonomy):`${functionRole(root)} · ${ids.map(id=>shortLabel(id,taxonomy)).join(' + ')}`]:[];}).join(' / ');
}
export function actor(m:LabelMessage,session?:LabelSession):string {
  if(m.segment.startsWith('H-'))return m.segment==='H-template'?"Human input · template":"Human input";
  if(m.segment==='AIUR')return "Agent-supplied input";
  if(m.role==='user')return "User-role input · authorship unavailable";
  if(m.role==='tool')return "Tool result";
  return session?.name??"Agent";
}
export function stateRuns(messages:LabelMessage[],resolution:Resolution,mode:StepMode="changes") {
  const runs:{key:string|null;message:LabelMessage;length:number}[]=[];
  for(const message of messages){const key=stateKey(message,resolution),last=runs.at(-1);if(mode==='changes'&&last&&last.key===key)last.length++;else runs.push({key,message,length:1});}
  return runs;
}
export function stateRows(streams:LabelStream[],resolution:Resolution,taxonomy:Taxonomy):string[] {
  const observed=new Set(streams.flatMap(s=>s.messages.flatMap(m=>functionStates(m,resolution))));
    return [...observed].sort(compareStates);
}
export type StateEdge={from:string;to:string;count:number;streams:Set<string>;examples:{stream:LabelStream;before:LabelMessage;after:LabelMessage}[]};
export function observedEdges(streams:LabelStream[],resolution:Resolution,mode:StepMode):StateEdge[] {
  const edges=new Map<string,StateEdge>();
  for(const stream of streams){const runs=stateRuns(stream.messages,resolution,mode);for(let i=1;i<runs.length;i++){
    const before=runs[i-1],after=runs[i];if(!before.key||!after.key)continue;
    for(const from of before.key.split('|'))for(const to of after.key.split('|')){
      const key=from+'→'+to,item=edges.get(key)??{from,to,count:0,streams:new Set(),examples:[]};
      item.count++;if(!item.streams.has(stream.id))item.examples.push({stream,before:before.message,after:after.message});item.streams.add(stream.id);edges.set(key,item);
    }
  }}
  return [...edges.values()].sort((a,b)=>b.count-a.count||a.from.localeCompare(b.from)||a.to.localeCompare(b.to));
}
export type StatePattern={id:string;states:string[];occurrences:number;examples:{stream:LabelStream;message:LabelMessage}[];streamIds:Set<string>;roots:Set<string>};
export function statePatterns(streams:LabelStream[],resolution:Resolution,mode:StepMode,cycles=false):StatePattern[] {
  const found=new Map<string,StatePattern>();
  const add=(states:string[],stream:LabelStream,message:LabelMessage)=>{
    const id=states.join('→'),item=found.get(id)??{id,states,occurrences:0,examples:[],streamIds:new Set(),roots:new Set()};
    item.occurrences++;if(!item.streamIds.has(stream.id))item.examples.push({stream,message});item.streamIds.add(stream.id);item.roots.add(stream.conversation.id);found.set(id,item);
  };
  for(const stream of streams){const runs=stateRuns(stream.messages,resolution,mode);for(let i=0;i<runs.length;i++){
    if(!runs[i].key)continue;
    if(!cycles){const path=runs.slice(i,i+3);if(path.length===3&&path.every(r=>r.key))add(path.map(r=>r.key!),stream,runs[i].message);}
    else if(runs[i+1]?.key)for(let j=i+2;j<=Math.min(i+5,runs.length-1);j++){
      if(!runs[j].key)break;
      if(runs[j].key===runs[i].key){add(runs.slice(i,j+1).map(r=>r.key!),stream,runs[i].message);break;}
    }
  }}
  return [...found.values()].sort((a,b)=>b.roots.size-a.roots.size||b.streamIds.size-a.streamIds.size||b.occurrences-a.occurrences||a.id.localeCompare(b.id));
}
export function branchContinuations(streams:LabelStream[],resolution:Resolution,mode:StepMode,prefix:string[]) {
  const paths=streams.map(stream=>{const runs=stateRuns(stream.messages,resolution,mode),gap=runs.findIndex(r=>r.key===null);return {stream,runs:gap<0?runs:runs.slice(0,gap),gap:gap>=0||runs.length===0};});
  const matching=paths.filter(p=>prefix.every((state,i)=>p.runs[i]?.key===state));
  const children=new Map<string,{key:string;paths:typeof matching;example:{stream:LabelStream;message:LabelMessage}}>();
  let ended=0,missing=0;
  for(const path of matching){const next=path.runs[prefix.length];if(!next){if(path.gap)missing++;else ended++;continue;}const item=children.get(next.key!)??{key:next.key!,paths:[],example:{stream:path.stream,message:next.message}};item.paths.push(path);children.set(next.key!,item);}
  return {total:matching.length,ended,missing,children:[...children.values()].sort((a,b)=>b.paths.length-a.paths.length||a.key.localeCompare(b.key))};
}
export function phasePrevalence(streams:LabelStream[],bins=5) {
  const ids=new Set(['P','C',...labelFamilies.filter(f=>f.id.includes('.')).map(f=>f.id)]);
  for(const stream of streams)for(const m of stream.messages)for(const id of functionStates(m,'label')){
    const parts=id.split('.');for(let depth=2;depth<=parts.length;depth++)ids.add(parts.slice(0,depth).join('.'));
  }
  const result=Array.from({length:bins},()=>({support:0,values:Object.fromEntries([...ids].map(id=>[id,0])) as Record<string,number>}));
  for(const stream of streams){const binsFor=Array.from({length:bins},()=>[] as LabelMessage[]);stream.messages.forEach((m,i)=>binsFor[phaseBin(i,stream.messages.length,bins)].push(m));binsFor.forEach((messages,i)=>{
    if(!messages.length)return;result[i].support++;
    for(const id of ids)result[i].values[id]+=messages.filter(m=>id==='P'||id==='C'?functionStates(m,'label').includes(id):matchesLabel(m,id)).length/messages.length;
  });}
  for(const bin of result)if(bin.support)for(const id of ids)bin.values[id]/=bin.support;
  return result;
}
export function exchanges(messages:LabelMessage[]) {
  const result:{sender:LabelMessage|null;receivers:LabelMessage[];unknown:LabelMessage[]}[]=[];
  for(const m of messages){const states=functionStates(m),sender=states.some(id=>functionRole(id)==='Sender'),receiver=states.some(id=>functionRole(id)==='Receiver');
    if(sender)result.push({sender:m,receivers:[],unknown:[]});else if(!result.length)result.push({sender:null,receivers:[],unknown:[]});
    if(receiver)result.at(-1)!.receivers.push(m);if(!states.length)result.at(-1)!.unknown.push(m);
  }
  return result;
}
