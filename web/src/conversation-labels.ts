// Shared production message-label and conversation data.
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
export type LabelMessage = {preview?:string;preview_truncated?:boolean;authorship?:{category:string;bytes:number;text?:string;truncated?:boolean}[];generation_model?:string;session_id?:string;conversation_id?:string;work_id?:string;order?:number|null;call_id?:string|null;id:string;role:string;kind:string;at:string|null;segment:string;labels:[string,number][];text:string;text_source:string;truncated:boolean;chunks:number};
export type LabelTool={preview?:string;preview_truncated?:boolean;id:string;session_id:string;message_id?:string;name:string;category:string;command_category?:string;status:string;start:string|null;end:string|null;duration_ms?:number|null};
export type LabelConversation = {activity?:LabelTool[]|null;sessions?:LabelSession[];main_session?:string;id:string;work_id:string;title:string;repository:string;provider:string;started_at:string|null;messages:LabelMessage[]};
export function specificLabels(message:LabelMessage):[string,number][] {
  return message.labels.filter(([id])=>!message.labels.some(([other])=>other.startsWith(id+".")))
    .sort((a,b)=>b[1]-a[1]||a[0].localeCompare(b[0]));
}
export type LabelSession={id:string;parent_id:string|null;conversation_id:string;work_id:string;name:string;kind:string;depth:number;link_evidence:string;call_message_id:string|null;result_message_id:string|null;labels_pending?:boolean;model?:string;provider?:string};
export type Resolution="family"|"label";
export type Taxonomy={id:string;parent?:string|null;name:string;definition:string}[];
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
export const functionRole=(id:string)=>id==="P"||id.startsWith("P.")?"Sender":id==="C"||id.startsWith("C.")?"Receiver":"Unclassified";
export const stateFamily=(id:string):Family=>labelFamilies.find(f=>f.id===id.split('.').slice(0,2).join('.'))?.id??"root";
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
  if(id==='no-match')return 'transparent';
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
export function functionStates(m:LabelMessage,resolution:Resolution="family"):string[] {
  if(!m.chunks)return [];
  const ids=specificLabels(m).map(([id])=>resolution==="family"&&id.includes('.')?id.split('.').slice(0,2).join('.'):id);
  return [...new Set(ids)].filter(id=>id==="P"||id==="C"||id.startsWith("P.")||id.startsWith("C.")).sort(compareStates);
}
