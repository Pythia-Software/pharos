import React,{useMemo} from 'react';
import {ViewDisclosure} from './view-disclosure';
import {labelOccurrenceTree,labelNodeName,labelNodeColorID,labelFocusPath,type LabelOccurrence} from './label-inspection-model';
import {labelHint,stateColor,type LabelMessage,type Taxonomy} from './label-trajectories-model';
import {percent} from './label-trajectories-ui';
export function LabelTrendHierarchy({messages,taxonomy,focus,onFocus,unit='messages'}:{unit?:string;messages:LabelMessage[];taxonomy:Taxonomy;focus:string;onFocus:(id:string)=>void}){
 const tree=useMemo(()=>labelOccurrenceTree(messages),[messages]),path=labelFocusPath(focus);
 const row=(node:LabelOccurrence,depth=0):React.ReactNode=>{
  const content=<div className="lt-trend-label-row" style={{'--lt-tree-depth':depth} as React.CSSProperties}><span><span className={node.children.length?'lt-caret':'lt-caret-placeholder'} aria-hidden="true"/><button type="button" data-view-field="label_focus" aria-pressed={focus===node.id||!focus&&node.id==='all'} title={labelHint(labelNodeColorID(node.id),taxonomy)} onClick={event=>{event.preventDefault();onFocus(node.id==='all'?'':node.id);}}><i style={{background:stateColor(labelNodeColorID(node.id))}}/>{node.id==='all'?'All '+unit:labelNodeName(node.id,taxonomy)}</button></span><span>{node.count.toLocaleString()}</span><span>{percent(node.count/Math.max(1,messages.length))}</span></div>;
  return node.children.length?<ViewDisclosure key={node.id} viewKey={`label-trend-tree:${node.id}`} className="lt-trend-label-branch" defaultOpen={['all','P','C','uncategorized',...path].includes(node.id)} summary={content}>{node.children.map(child=>row(child,depth+1))}</ViewDisclosure>:<div key={node.id}>{content}</div>;
 };
 return <section className="lt-trend-labels" aria-label="Labels in this period"><div className="lt-section-heading"><h3>Labels in this period</h3><span>Unsmoothed occurrences</span></div><div className="lt-trend-label-row lt-trend-label-header"><span>Label · select to explore</span><span>Count</span><span>Share</span></div>{row(tree)}<p className="lt-note">Counts are distinct {unit} per label. Labels can overlap; the denominator stays at {messages.length.toLocaleString()} {unit}.</p></section>;
}
