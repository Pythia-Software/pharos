import React,{useId,useMemo,useRef} from 'react';
import {conversationRibbon,fingerprintMessagePositions,parallelEnvelopes,ribbonMessageTitle,ribbonSegment,ribbonWeights,type RibbonToken} from './conversation-fingerprint-model';
import {EventLinkCard,EventLinkOverlay,eventHitTargets,eventLayout,eventTargetFocus,placeEvents,setEventLinkFocus,useBoxSize,type EventLinkFocus} from './event-link-overlay';
import {linkedTarget,type AgentEventLink} from './event-link-layout';
import {nearestMark} from './ribbon-marks';
import {stateColor,shortLabel,mainSession,workflowSessions,type LabelConversation,type Taxonomy} from './conversation-labels';
import {useRibbonInspector,useRibbonPointer,type RibbonHit} from './label-ribbon-inspector';
import './conversation-fingerprint.css';
const emptyTaxonomy:Taxonomy=[];
const percent=(share:number)=>share>0&&share<.001?'<0.1%':`${Number((100*share).toFixed(1))}%`;
function SegmentCard({token,breakdown,taxonomy,first,last,total}:{total:number;token:RibbonToken;breakdown:ReturnType<typeof ribbonSegment>;taxonomy:Taxonomy;first:number;last:number}){
 const name=(id:string)=>id==='all'?'All messages':id==='P'?'Sender':id==='C'?'Receiver':id==='uncategorized'?'Uncategorized':id==='unlabeled'?'Not labeled':id==='no-match'?'Labeled · no match':id.endsWith('~other')?(id==='P~other'||id==='C~other'?'Generic':'Other'):shortLabel(id,taxonomy);
 return <><strong>{ribbonMessageTitle(first,last,total)}</strong><p>{token.parallelism?`Peak overlap: +${token.parallelism} branches`:'No extra overlapping branches'}</p>{breakdown.rows.length>20?<small className="lt-ribbon-taxonomy-cue">Scroll for all labels</small>:null}<div className="lt-ribbon-taxonomy-scroll"><table className="lt-ribbon-taxonomy" aria-label="Label hierarchy for this column"><thead><tr><th scope="col">Message label</th><th scope="col">Share</th></tr></thead><tbody>{breakdown.rows.map(row=><tr key={row.id} className={row.terminal?'is-leaf':'is-parent'}><th scope="row" title={row.id==='all'?undefined:row.id} style={{paddingLeft:row.depth*7}}><span>{row.depth?<i aria-hidden="true" style={{background:stateColor(row.colorID)}}/>:null}{name(row.id)}</span></th><td>{percent(row.share)}</td></tr>)}</tbody></table></div><small>Message weight is split across labels; children add to parents.</small></>;
}
// Agent events (events, with their targets) draw inside the bar: see
// event-link-layout.ts. The bar hit-tests its events and segments together
// through the ribbons' shared pointer model (useRibbonPointer), the same one
// the session timeline uses for tool dots, events and message spans: hover
// shows a card, a click pins it, and the events take one tab stop whose arrow
// keys browse them.
const noEvents:AgentEventLink[]=[];
export function CompactFingerprint({conversation,taxonomy=emptyTaxonomy,stretch=false,events=noEvents,eventHref}:{conversation:LabelConversation;taxonomy?:Taxonomy;stretch?:boolean;events?:AgentEventLink[];eventHref?:(e:AgentEventLink)=>string|null}){
 const tokens=useMemo(()=>conversationRibbon(conversation),[conversation]),breakdowns=useMemo(()=>tokens.map(token=>ribbonSegment(token.messages,taxonomy)),[tokens,taxonomy]),weights=ribbonWeights(tokens),positions=useMemo(()=>fingerprintMessagePositions(tokens),[tokens]),total=weights.reduce((a,b)=>a+b,0)||1,inspector=useRibbonInspector({interactive:true}),clip='ribbon'+useId().replace(/:/g,''),hatch=clip+'hatch';let cursor=0,seen=0;
 const box=useRef<HTMLDivElement>(null),size=useBoxSize(box);
 const segments=tokens.map((token,i)=>{const width=weights[i]/total*320,x=cursor,first=seen+1;cursor+=width;seen+=token.count;return {token,i,width,x,first,last:seen};}),envelopes=parallelEnvelopes(segments);
 const placed=useMemo(()=>placeEvents(events,e=>positions.get(e.message_id)??null),[events,positions]),layout=useMemo(()=>eventLayout(placed,size.width,size.height,3),[placed,size.width,size.height]);
 const targets=useMemo(()=>eventHitTargets(layout),[layout]),byID=useMemo(()=>new Map(placed.map(p=>[p.event.id,p.event])),[placed]);
 const segmentCard=(i:number)=>{const s=segments[i];return <SegmentCard token={s.token} breakdown={breakdowns[i]} taxonomy={taxonomy} first={s.first} last={s.last} total={conversation.messages.length}/>;};
 const eventHit=(list:AgentEventLink[],focus:EventLinkFocus):RibbonHit=>({card:pinned=>pinned?<EventLinkCard events={list} href={eventHref} close={inspector.close}/>:<EventLinkCard events={list}/>,hover:()=>setEventLinkFocus(focus)});
 const unhover=()=>setEventLinkFocus(null);
 const hitAt=(event:React.MouseEvent<Element>):RibbonHit|null=>{
  const rect=box.current!.getBoundingClientRect(),x=event.clientX-rect.left,y=event.clientY-rect.top;
  const target=nearestMark(targets,x,y);
  if(target)return eventHit(target.events.map(id=>byID.get(id)!),eventTargetFocus(target,byID));
  const at=x/rect.width*320,segment=segments.find(s=>at>=s.x&&at<s.x+s.width)??(at>=320?segments[segments.length-1]:undefined);
  return segment?{card:()=>segmentCard(segment.i),hover:unhover}:null;
 };
 const candidates=useMemo(()=>[...placed].sort((a,b)=>a.x-b.x||a.event.id.localeCompare(b.event.id)).map(p=>eventHit([p.event],{event:p.event.id,target:linkedTarget(p.event)?.message_id??null,message:p.event.message_id})),[placed,eventHref]);
 const {onFocus,onBlur,onKeyDown,...pointer}=useRibbonPointer({inspector,hitAt,candidates,unhover});
 return <div ref={box} className={`lt-emblem${events.length?" has-events":""}`} onPointerLeave={inspector.leave}><svg preserveAspectRatio={stretch?'none':'xMidYMid meet'} viewBox="0 0 320 38" role="img" aria-label={`${conversation.messages.length} events, ${workflowSessions(conversation).filter(s=>conversation.messages.some(m=>(m.session_id??mainSession(conversation))===s.id)).length} recorded branches conversation fingerprint`} {...pointer}><title>Colors = deepest accepted message labels, including Report. Stripe heights = message shares. Centered diagonal hatching = extra overlapping branches. Width compresses volume.</title><defs><clipPath id={clip}><rect x="0" y="0" width="320" height="38"/></clipPath><pattern id={hatch} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(40)"><path d="M0 0V6" stroke="#000" strokeOpacity=".65" strokeWidth="1.3"/></pattern></defs><g clipPath={`url(#${clip})`}><rect x="0" y="0" width="320" height="38" fill="var(--lt-soft)"/>{segments.map(({token,i,width,x})=>{
 const h=38;let y=0;
 return <g key={i}>{breakdowns[i].slices.map(slice=>{const height=h*slice.share,top=y;y+=height;return <rect key={slice.id} x={x} y={top} width={width+.2} height={height} fill={stateColor(slice.colorID)}/>;})}</g>;
 })}
 {envelopes.map((envelope,i)=><path key={i} d={envelope.path} fill={`url(#${hatch})`}/>)}
 {segments.map(({token,i,x,width,first,last})=><g key={i} tabIndex={0} role="img" aria-label={`Ribbon segment ${i+1}, ${ribbonMessageTitle(first,last,conversation.messages.length)}, extra overlapping branches ${token.parallelism}`} aria-describedby={inspector.id} onFocus={event=>inspector.show(event.currentTarget,segmentCard(i))} onBlur={inspector.close}><rect className="lt-emblem-hit" x={x} y="0" width={width} height="38" fill="transparent"/></g>)}
 {placed.length?<g tabIndex={0} role="button" aria-label={`${placed.length} agent events. Arrow keys browse events; Enter pins details.`} aria-describedby={inspector.id} onFocus={onFocus} onBlur={onBlur} onKeyDown={onKeyDown}><rect className="lt-emblem-hit" x="0" y="0" width="320" height="38" fill="transparent" pointerEvents="none"/></g>:null}</g></svg>{placed.length?<EventLinkOverlay placed={placed} layout={layout} width={size.width} height={size.height} label={`${placed.length} agent events`}/>:null}{inspector.portal}</div>;
}
