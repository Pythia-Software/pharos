import React,{useEffect,useRef,useState} from 'react';
import {PageLink} from './nested-pages';
import {actor,familyInfo,functionRole,functionStates,labelHint,messageURL,shortLabel,stateColor,stateFamily,stateName,stateTitle,type LabelConversation,type LabelMessage,type LabelSession,type LabelStream,type Resolution,type StepMode,type Taxonomy,type TrajectoryScope} from './label-trajectories-model';

export const percent=(value:number)=>{
  const amount=value*100;
  return amount>0&&amount<.1?'<0.1%':`${amount>0&&amount<10?Number(amount.toFixed(1)):Math.round(amount)}%`;
};
export type SelectSource=(conversation:LabelConversation,message?:LabelMessage,session?:string)=>void;
export type WorkbenchProps={conversations:LabelConversation[];streams:LabelStream[];scope:TrajectoryScope;nested:boolean;resolution:Resolution;mode:StepMode;selected:LabelConversation;session:LabelSession;messages:LabelMessage[];message:LabelMessage|undefined;select:SelectSource;taxonomy:Taxonomy};

export function LabPlot({label,children,height=440}:{label:string;children:(width:number)=>React.ReactNode;height?:number}) {
  const host=useRef<HTMLDivElement>(null),[width,setWidth]=useState(700);
  useEffect(()=>{const observer=new ResizeObserver(([entry])=>setWidth(Math.max(250,entry.contentRect.width)));observer.observe(host.current!);return()=>observer.disconnect();},[]);
  return <div ref={host} className="lt-plot"><svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label}><title>{label}</title>{children(width)}</svg></div>;
}
export function StateSet({state,taxonomy,compact=false}:{state:string|null;taxonomy:Taxonomy;compact?:boolean}) {
  if(!state)return <span className="lt-missing">Unclassified boundary</span>;
  return <span className={`lt-state-set${compact?' is-compact':''}`}>{['Sender','Receiver'].map(role=>{const ids=state.split('|').filter(id=>functionRole(id)===role);return ids.length?<span key={role} className={`lt-function-group ${role==='Sender'?'is-sender':'is-receiver'}`}><small>{role}</small><span>{ids.map(id=><span key={id} className={`lt-state-name${stateFamily(id)==='C.report'?' is-report':''}`} title={labelHint(id,taxonomy)}><i style={{background:stateColor(id)}} aria-hidden="true"/>{shortLabel(id,taxonomy)}</span>)}</span></span>:null;})}</span>;
}

export type MarkEncoding='symbols'|'tiles'|'texture';
export function LabelTextures(){return <svg className="lt-texture-defs" aria-hidden="true"><defs>{['P.request','P.frame','P.control','C.input','C.feedback','C.deliberation','C.report','C.difficulty'].map((id,index)=><pattern key={id} id={'lt-texture-'+id.replace('.','-')} width="5" height="5" patternUnits="userSpaceOnUse" patternTransform={`rotate(${index%3===0?45:index%3===1?0:90})`}><path d="M0 0V5" stroke="var(--ink)" strokeWidth={index%2?1:1.5} opacity=".5"/>{index%2?<circle cx="3" cy="2.5" r=".7" fill="var(--ink)" opacity=".6"/>:null}</pattern>)}</defs></svg>;}
export function LabelMark({id,x,y,size=8,encoding='symbols',selected=false,hint}:{id:string;x:number;y:number;size?:number;encoding?:MarkEncoding;selected?:boolean;hint?:string}) {
  const color=stateColor(id),r=size/2,report=id.startsWith('C.report'),sender=functionRole(id)==='Sender';
  const mark=(fill:string)=>encoding==='symbols'&&id!=='?'?(report?<rect x={x-r} y={y-1.5} width={size} height={3} rx="1" fill={fill}/>:sender?<path d={`M${x},${y-r}l${r},${r}l${-r},${r}l${-r},${-r}Z`} fill={fill}/>:<circle cx={x} cy={y} r={r} fill={fill}/>):<rect x={x-r} y={y-r} width={size} height={size} rx="1" fill={id==='?'?'none':fill} stroke={id==='?'?'var(--muted)':'none'}/>;
  return <g>{hint?<title>{hint}</title>:null}{selected?<rect x={x-r-2} y={y-r-2} width={size+4} height={size+4} rx="2" fill="none" stroke="var(--ink)" strokeWidth="1.2"/>:null}{mark(color)}{encoding==='texture'&&id!=='?'?mark(`url(#lt-texture-${stateFamily(id).replace('.','-')})`):null}</g>;
}
export function MarkLegend({encoding}:{encoding:MarkEncoding}){return <p className="lt-note lt-mark-legend">{encoding==='symbols'?'◆ Sender · ● Receiver · ━ Report · □ unclassified':encoding==='texture'?'Striped tiles retain family color and add texture.':'Solid tiles retain family and sublabel colors.'} Hover a mark for a short label and definition; click to read the original.</p>;}
export function FunctionRails({messages,session}:{messages:LabelMessage[];session?:LabelSession}) {
  return <span className="lt-function-rails" aria-hidden="true"><span className="lt-rail-labels"><small>S</small><small>R</small><small>who</small></span><span className="lt-rail-columns">{messages.map(m=>{const ids=functionStates(m),p=ids.filter(id=>functionRole(id)==='Sender'),c=ids.filter(id=>functionRole(id)==='Receiver');return <span key={m.id} className={`lt-rail-column${!m.chunks?' is-unlabeled':!m.labels.length?' is-no-match':''}`} title={`${actor(m,session)} · ${m.kind} · ${ids.length?stateTitle(ids.join('|')):'No accepted function'}`}>
    <span>{p.map(id=><i key={id} style={{background:stateColor(id)}}/>)}</span><span>{c.map(id=><i key={id} style={{background:stateColor(id)}}/>)}</span><b className={m.segment.startsWith('H-')?'is-human':m.kind==='delegation'?'is-call':m.kind==='delegation_result'?'is-result':''}/>
  </span>;})}</span></span>;
}
export function SourceExamples({examples,select}:{examples:{stream:LabelStream;message:LabelMessage}[];select:SelectSource}) {
  return <div className="lt-source-examples">{examples.slice(0,12).map(({stream,message})=><button type="button" data-view-field="label_message" key={stream.id+message.id} onClick={()=>select(stream.conversation,message,stream.id)}><span><strong>{stream.conversation.title}</strong><small>{stream.session.name} · {stream.conversation.repository}</small></span><span>Inspect source →</span></button>)}{examples.length>12?<p className="lt-note">12 of {examples.length} source branches shown.</p>:null}</div>;
}
export function SourceAnchor({conversation,message,children}:{conversation:LabelConversation;message:LabelMessage;children?:React.ReactNode}) {
  return <PageLink href={messageURL(conversation,message)}>{children??'Open original message in reader ↗'}</PageLink>;
}
export function RoleLegend() {
  const groups=[['Sender','P.'],['Receiver','C.']] as const;
  return <div className="lt-role-legend">{groups.map(([role,prefix])=><div key={role}><strong>{role}<small>{role==='Sender'?'initiate / frame / steer':'respond / evaluate / work'}</small></strong><span>{Object.values(['P.request','P.frame','P.control','C.input','C.feedback','C.deliberation','C.report','C.difficulty']).filter(id=>id.startsWith(prefix)).map(id=><span className={`lt-family${id==='C.report'?' is-report':''}`} key={id}><i style={{background:stateColor(id)}}/>{familyInfo(stateFamily(id)).name}</span>)}</span></div>)}<p>Cool Sender colors · yellow, orange, rose and red Receiver colors · golden Report. Centered diagonal hatching marks captured overlap. One message can carry several functions.</p></div>;
}
