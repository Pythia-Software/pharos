import React,{useMemo} from 'react';
import {useViewState} from './view-state';
import {ViewDisclosure} from './view-disclosure';
import {actor,compareStates,shortLabel,branchContinuations,functionRole,functionStates,hierarchy,mainSession,messageURL,observedEdges,scopedMessages,stateColor,stateKey,stateName,statePatterns,stateRows,stateRuns,stateTitle,workflowSessions} from './label-trajectories-model';
import {prefixTree,type PrefixNode} from './label-shape-model';
import {tagContinuations,tagPrefixPaths} from './label-discovery-model';
import {ConversationBrowser} from './label-conversation-browser';
import {FunctionRails,LabPlot,SourceExamples,StateSet,percent,type WorkbenchProps} from './label-trajectories-ui';

export function MachineMap(props:WorkbenchProps) {
  const {streams,resolution,mode,taxonomy,select}=props;
  const [focus,setFocus]=useViewState('label_state','');
  const edges=useMemo(()=>observedEdges(streams,resolution,mode),[streams,resolution,mode]);
  const allStates=useMemo(()=>stateRows(streams,resolution,taxonomy),[streams,resolution,taxonomy]);
  const chosen=edges.filter(e=>!focus||e.from===focus||e.to===focus),shown=chosen.slice(0,24);
  const states=allStates.filter(id=>!focus||id===focus||shown.some(e=>e.from===id||e.to===id));
  const groups=['Sender','Receiver'].map(role=>states.filter(id=>functionRole(id)===role));
  const height=Math.max(410,Math.max(...groups.map(g=>g.length))*48+90);
  const [edgeId,setEdgeId]=useViewState('label_transition','');
  const active=edges.find(e=>e.from+'→'+e.to===edgeId);
  return <><label className="lt-local-control">Inspect state<select data-view-field="label_state" value={focus} onChange={e=>setFocus(e.target.value)}><option value="">All observed states</option>{allStates.map(id=><option value={id} key={id}>{hierarchy(id,taxonomy).join(' / ')}</option>)}</select></label>
    <LabPlot label="Directed observed-state map with Sender and Receiver regions" height={height}>{w=>{
      const xs=[w<450?65:110,w-(w<450?65:110)],positions=new Map<string,[number,number]>();groups.forEach((ids,col)=>ids.forEach((id,i)=>positions.set(id,[xs[col],65+i*48])));
      return <><defs><marker id="lt-state-arrow" markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path d="M0 0L7 3.5L0 7" fill="var(--muted)"/></marker></defs>{groups.map((ids,col)=><g key={col}><rect x={col? w/2+5:5} y={15} width={w/2-10} height={height-30} rx="7" fill="var(--lt-soft)"/><text x={xs[col]} y={38} textAnchor="middle" className="lt-svg-group">{col?'Receiver':'Sender'}</text></g>)}{shown.map((edge,i)=>{
        const [x1,y1]=positions.get(edge.from)!,[x2,y2]=positions.get(edge.to)!,same=x1===x2,curve=same?(x1<w/2?Math.min(w/2-12,x1+100):Math.max(w/2+12,x1-100)):(w/2+(i%5-2)*12);
        const d=edge.from===edge.to?`M${x1-10},${y1-4} C${x1-60},${y1-40} ${x1+60},${y1-40} ${x1+10},${y1-4}`:`M${x1},${y1} C${curve},${y1} ${curve},${y2} ${x2},${y2}`;
        const selected=edgeId===edge.from+'→'+edge.to;
        return <a key={edge.from+edge.to} data-view-field="label_transition" href={messageURL(edge.examples[0].stream.conversation,edge.examples[0].after)} aria-label={`${stateTitle(edge.from,taxonomy)} to ${stateTitle(edge.to,taxonomy)}: ${edge.count} observed pairs`} onClick={event=>{event.preventDefault();setEdgeId(edge.from+'→'+edge.to);select(edge.examples[0].stream.conversation,edge.examples[0].after);}}><path d={d} fill="none" stroke={selected?'var(--gold)':'var(--muted)'} strokeWidth={selected?3:Math.min(5,1+Math.log2(edge.count+1)/3)} opacity={selected?1:.26} markerEnd="url(#lt-state-arrow)"/><path d={d} fill="none" stroke="transparent" strokeWidth="14"/><title>{hierarchy(edge.from,taxonomy).join(' / ')} → {hierarchy(edge.to,taxonomy).join(' / ')} · {edge.count} pairs across {edge.streams.size} branches</title></a>;
      })}{states.map(id=>{const [x,y]=positions.get(id)!;const example=streams.flatMap(stream=>{const message=stream.messages.find(m=>functionStates(m,resolution).includes(id));return message?[{stream,message}]:[];})[0];return <a key={id} data-view-field="label_state" href={example?messageURL(example.stream.conversation,example.message):'#'} aria-label={`Inspect state ${hierarchy(id,taxonomy).join(' / ')}`} onClick={event=>{event.preventDefault();setFocus(focus===id?'':id);if(example)select(example.stream.conversation,example.message);}}><rect x={x-(w<450?56:96)} y={y-13} width={w<450?112:192} height="27" rx="4" fill="var(--panel)" stroke={focus===id?'var(--gold)':stateColor(id)} strokeWidth={focus===id?2:1}/><text x={x} y={y+4} textAnchor="middle">{stateName(id,taxonomy).slice(0,w<450?17:29)}</text><title>{hierarchy(id,taxonomy).join(' / ')}</title></a>;})}</>;
    }}</LabPlot><p className="lt-note">Showing {shown.length} of {chosen.length} observed edges by count. Arrow width grows with pair count. Multi-function messages support several edges; missing labels break adjacency. Select a state to isolate its neighborhood.</p>
    {active?<><h3 className="lt-section-title">{stateTitle(active.from,taxonomy)} → {stateTitle(active.to,taxonomy)}</h3><p className="lt-note">{active.count} observed pairs · {active.streams.size} branches</p><SourceExamples examples={active.examples.map(e=>({stream:e.stream,message:e.after}))} select={select}/></>:null}
  </>;
}

export function StepReplay({selected,session,messages,message,select,resolution,mode,taxonomy}:WorkbenchProps) {
  const runs=stateRuns(messages,resolution,mode),position=Math.max(0,messages.findIndex(m=>m.id===message?.id));let cursor=0,current=0;
  for(let i=0;i<runs.length;i++){if(position<cursor+runs[i].length){current=i;break;}cursor+=runs[i].length;}
  const lineage=[];let owner:typeof session|undefined=session;const seen=new Set<string>();
  while(owner&&!seen.has(owner.id)){lineage.unshift(owner);seen.add(owner.id);owner=workflowSessions(selected).find(s=>s.id===owner!.parent_id);}
  return <><div className="lt-call-breadcrumb" aria-label="Recorded call ancestry">{lineage.map((s,i)=><React.Fragment key={s.id}>{i?<span>→</span>:null}<button type="button" data-view-field="label_session" onClick={()=>select(selected,scopedMessages(selected,'thinking',s.id)[0],s.id)}>{s.name}</button></React.Fragment>)}</div>
    <div className="lt-replay-controls"><button type="button" data-view-field="label_message" disabled={!current} onClick={()=>select(selected,runs[current-1].message)}>← Previous state</button><span aria-live="polite">State {runs.length?current+1:0} of {runs.length} · message {messages.length?position+1:0} of {messages.length}</span><button type="button" data-view-field="label_message" disabled={current>=runs.length-1} onClick={()=>select(selected,runs[current+1].message)}>Next state →</button></div>
    <div className="lt-replay-states">{[-1,0,1].map(offset=>{const run=runs[current+offset],m=offset===0?message:run?.message;return <section key={offset} className={offset===0?'is-current':''}><p className="lt-eyebrow">{offset<0?'Previous':offset>0?'Next':'Current state'}</p>{run?<><StateSet state={run.key} taxonomy={taxonomy}/><p className="lt-note">{run.length} retained {run.length===1?'message':'messages'} in this state</p><p className="lt-actor-line">Actor: {m?actor(m,session):'unavailable'}</p><blockquote>{m?.text.slice(0,330)||'No retained prose'}</blockquote>{offset!==0?<button type="button" data-view-field="label_message" onClick={()=>select(selected,run.message)}>Inspect this state</button>:null}</>:<p className="lt-note">{offset<0?'Start of captured branch':'End of captured branch'}</p>}</section>;})}</div>
    <p className="lt-note">A state is the complete accepted Sender/Receiver set at the selected resolution. State changes mode compresses identical repeated sets. It does not treat an unclassified boundary as a known state or join independent agents.</p>
  </>;
}

export function CallResults({selected,scope,select,taxonomy}:WorkbenchProps) {
  const sessions=workflowSessions(selected),children=sessions.filter(s=>s.id!==mainSession(selected)),byId=new Map(selected.messages.map(m=>[m.id,m]));
  if(!children.length)return <p className="lt-note">No recorded subagent branch in this workflow. Choose “With subagents” under Workflow population to explore nested calls.</p>;
  return <div className="lt-call-results">{children.map(s=>{const call=byId.get(s.call_message_id??''),result=byId.get(s.result_message_id??''),parent=sessions.find(p=>p.id===s.parent_id),messages=scopedMessages(selected,scope,s.id),last=messages.at(-1);return <article className="lt-call-result" key={s.id} style={{'--lt-depth':Math.min(s.depth,5)} as React.CSSProperties}>
    <div className="lt-call-owner"><span className="lt-eyebrow">{parent?.name??'Parent unavailable'}</span><strong>↓ {s.name}</strong><small>{s.link_evidence}</small></div><div className="lt-call-columns">
      <div><p className="lt-eyebrow">Call</p>{call?<button type="button" data-view-field="label_message" onClick={()=>select(selected,call)}><StateSet state={stateKey(call)} taxonomy={taxonomy} compact/><span>Inspect recorded delegation →</span></button>:<p className="lt-note">Caller message unavailable; lineage only.</p>}</div>
      <div className="lt-called-branch"><p className="lt-eyebrow">Child machine · local order</p><button type="button" data-view-field="label_session" onClick={()=>select(selected,messages[0],s.id)}><FunctionRails messages={messages} session={s}/><span>{messages.length} retained events · {messages.filter(m=>m.chunks).length} labeled</span><strong>Enter branch →</strong></button>{last?<button className="lt-last-event" type="button" data-view-field="label_message" onClick={()=>select(selected,last)}>Inspect last captured child event</button>:<p className="lt-note">No retained child events.</p>}</div>
      <div><p className="lt-eyebrow">Parent tool result</p>{result?<button type="button" data-view-field="label_message" onClick={()=>select(selected,result)}><StateSet state={stateKey(result)} taxonomy={taxonomy} compact/><span>Inspect recorded result →</span></button>:<p className="lt-note">No matched tool result retained.</p>}</div>
    </div>{result?<p className="lt-note lt-async-note">This result may acknowledge an asynchronous launch. The source does not establish a child completion or return time.</p>:null}
    {sessions.some(child=>child.parent_id===s.id)?<p className="lt-note">This child invokes {sessions.filter(child=>child.parent_id===s.id).length} further recorded branches below.</p>:null}
  </article>;})}</div>;
}

export function BranchingPaths({streams,resolution,mode,taxonomy,select}:WorkbenchProps){
 const [prefix,setPrefix]=useViewState<string[]>('label_prefix',[]),[all,setAll]=useViewState('label_prefix_more',false),[,setPage]=useViewState('label_match_page','0');
 const paths=useMemo(()=>tagPrefixPaths(streams,resolution,mode,prefix),[streams,resolution,mode,prefix]);
 const indices=Array.from({length:prefix.length+1},(_,i)=>i).slice(-4);
 const matches=useMemo(()=>paths.flatMap(p=>{const run=p.runs[Math.max(0,prefix.length-1)];return run?[{stream:p.stream,message:run.message}]:[];}),[paths,prefix.length]);
 const choose=(at:number,id:string)=>{setPrefix([...prefix.slice(0,at),id]);setPage('0');};
 return <><div className="lt-prefix-breadcrumb"><button type="button" data-view-field="label_prefix" onClick={()=>{setPrefix([]);setPage('0');}}>All beginnings</button>{prefix.map((tag,i)=><button key={i} type="button" data-view-field="label_prefix" onClick={()=>setPrefix(prefix.slice(0,i+1))}>{i+1}. {tag==='P'||tag==='C'?functionRole(tag):stateTitle(tag,taxonomy)}</button>)}</div><p className="lt-prefix-summary">{paths.length} branches match · a tag filter includes messages that also have other tags</p>
 <div className="lt-prefix-columns">{indices.map(at=>{
  const continuation=tagContinuations(streams,resolution,mode,prefix.slice(0,at)),ranked=[...continuation.tags].sort((a,b)=>b[1]-a[1]),shown=new Set((all?ranked:ranked.slice(0,8)).flatMap(([id])=>{const parts=id.split('.');return parts.map((_,i)=>parts.slice(0,i+1).join('.'));}));if(prefix[at])shown.add(prefix[at]);
  return <section key={at} className="lt-prefix-column"><h3>Step {at+1}<small>{continuation.paths.length} source branches</small></h3><div className="lt-prefix-options">{[...shown].sort(compareStates).map(id=>{const count=continuation.tags.get(id)??0,ancestors=id.split('.').slice(0,-1).map((_,i)=>id.split('.').slice(0,i+1).join('.'));return <button key={id} type="button" data-view-field="label_prefix" aria-pressed={prefix[at]===id} style={{'--lt-tag-depth':Math.max(0,id.split('.').length-1)} as React.CSSProperties} onClick={()=>choose(at,id)}><span><i style={{background:stateColor(id)}}/>{id==='P'||id==='C'?functionRole(id):shortLabel(id,taxonomy)}{ancestors.length>1?<small>{ancestors.slice(1).map(t=>shortLabel(t,taxonomy)).join(' › ')}</small>:null}</span><strong>{count}</strong></button>;})}</div><p>{continuation.ended} captured ends · {continuation.missing} missing-label boundaries</p></section>;
 })}</div><button type="button" data-view-field="label_prefix_more" onClick={()=>setAll(!all)}>{all?'Show common tags':'Show all tags'}</button><p className="lt-note">Each column is the next captured state within an agent branch. Parent tags include all accepted descendants. Selecting A includes A+B; node counts overlap and are not additive. Columns preserve the selected parent path. Missing labels stop a path, and a captured end is not a completion claim.</p><ConversationBrowser taxonomy={taxonomy} matches={matches} select={select}/></>;
}

export function ReturnLoops({streams,resolution,mode,taxonomy,select}:WorkbenchProps) {
  const patterns=useMemo(()=>statePatterns(streams,resolution,mode,true),[streams,resolution,mode]);
  const [id,setId]=useViewState('label_cycle',''),active=patterns.find(p=>p.id===id)??patterns[0];
  if(!active)return <p className="lt-note">No closed two-to-five-edge path in this population and mode.</p>;
  const states=active.states.slice(0,-1);
  return <><label className="lt-local-control">Observed loop<select data-view-field="label_cycle" value={active.id} onChange={e=>{const p=patterns.find(p=>p.id===e.target.value)!;setId(p.id);select(p.examples[0].stream.conversation,p.examples[0].message);}}>{patterns.map((p,i)=><option key={p.id} value={p.id}>{i+1}. {p.states.map(s=>stateTitle(s,taxonomy)).join(' → ')} · {p.roots.size} workflows</option>)}</select></label>
    <div className="lt-loop-layout"><LabPlot label="Observed closed state path" height={320}>{w=>{
      const cx=w/2,cy=148,r=Math.min(100,w/2-50),pos=states.map((_,i)=>[cx+r*Math.cos(-Math.PI/2+i*2*Math.PI/states.length),cy+r*Math.sin(-Math.PI/2+i*2*Math.PI/states.length)]);
      return <><defs><marker id="lt-loop-arrow" markerWidth="7" markerHeight="7" refX="6" refY="3.5" orient="auto"><path d="M0 0L7 3.5L0 7" fill="var(--muted)"/></marker></defs>{states.map((s,i)=>{const [x,y]=pos[i],[nx,ny]=pos[(i+1)%pos.length],dx=nx-x,dy=ny-y,length=Math.hypot(dx,dy)||1;return <g key={i}><line x1={x+dx/length*20} y1={y+dy/length*20} x2={nx-dx/length*24} y2={ny-dy/length*24} stroke="var(--muted)" strokeWidth="2" markerEnd="url(#lt-loop-arrow)"/><circle cx={x} cy={y} r="19" fill="var(--panel)" stroke={stateColor(s.split('|')[0])} strokeWidth="3"/><text x={x} y={y+4} textAnchor="middle">{i+1}</text></g>;})}<text x={cx} y={cy+4} textAnchor="middle">return</text></>;
    }}</LabPlot><ol className="lt-loop-legend">{states.map((state,i)=><li key={i}><StateSet state={state} taxonomy={taxonomy}/></li>)}<li>Back to state 1</li></ol></div>
    <p className="lt-note">{active.roots.size} workflows · {active.streamIds.size} agent branches · {active.occurrences} occurrences. Showing the first return to the starting state within five edges; no missing-label boundary is crossed.</p><SourceExamples examples={active.examples} select={select}/>
  </>;
}
