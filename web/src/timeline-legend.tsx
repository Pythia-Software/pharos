import React from 'react';
import {compareStates,functionStates,labelFamilies,shortLabel,stateColor,type LabelMessage,type Taxonomy} from './label-trajectories-model';
import {authorshipSegments,callerSources} from './timeline-caller-model';
import {labelIntervals,type ActivityMark} from './label-session-ribbon-model';
import type {TimeRange} from './label-timeline-model';

/** Use the same elapsed attribution and text composition as the drawn ribbons. */
export function timelineLegendWindow(streams:{id?:string;messages:LabelMessage[];marks:ActivityMark[];neighbors?:LabelMessage[];caller?:boolean}[],range:TimeRange){
  const messages=new Map<string,LabelMessage>(),origins=new Set<string>(),tools=new Set<string>(),populated=new Set<string>();let error=false;
  const overlaps=(start:number,end:number)=>start<=range[1]&&end>=range[0];
  for(const stream of streams){
    for(const {message,start,end} of labelIntervals(stream.messages,stream.marks,30*60*1000,stream.neighbors??stream.messages)){
      if(!overlaps(start,end))continue;
      if(stream.id)populated.add(stream.id);
      messages.set(JSON.stringify([message.session_id,message.id]),message);
      if(stream.caller)for(const part of authorshipSegments(message))if(overlaps(start+(end-start)*part.start,start+(end-start)*part.end))origins.add(part.category);
    }
    if(!stream.caller)for(const mark of stream.marks)if(overlaps(mark.start,mark.end_known?Math.max(mark.start,mark.end):mark.start)){if(stream.id)populated.add(stream.id);if(mark.error)error=true;else tools.add(mark.category);}
  }
  return {messages:[...messages.values()],origins:[...origins],tools:[...tools],error,populated:[...populated]};
}

/** Keep only present specific colors, ordered beside their parent mode and role. */
export function timelineModeGroups(messages:LabelMessage[],taxonomy:Taxonomy=[]){
  const labels=[...new Set(messages.flatMap(message=>functionStates(message,'label')))].sort(compareStates);
  return ['P','C'].flatMap(root=>{
    const known=labelFamilies.filter(family=>family.id.startsWith(root+'.')).map(family=>({id:family.id,name:family.name}));
    const extra=[...new Set(labels.filter(id=>id.startsWith(root+'.')).map(id=>id.split('.').slice(0,2).join('.')))].filter(id=>!known.some(family=>family.id===id)).sort(compareStates).map(id=>({id,name:shortLabel(id,taxonomy)}));
    return [...(labels.includes(root)?[{id:root,name:shortLabel(root,taxonomy)}]:[]),...known,...extra].map(family=>({...family,direct:labels.includes(family.id),labels:labels.filter(id=>id.startsWith(family.id+'.')&&family.id!==root)})).filter(family=>family.direct||family.labels.length);
  });
}

export function TimelineLegend({messages,origins,taxonomy,tools,error}:{messages:LabelMessage[];origins:string[];taxonomy:Taxonomy;tools:Record<string,string>;error:boolean}){
  const modes=timelineModeGroups(messages,taxonomy),uncategorized=messages.some(message=>!functionStates(message,'label').length);
  const sources=callerSources.filter(source=>origins.includes(source.id));
  return <div className="lt-timeline-legend">
    {modes.length||uncategorized?<div className="lt-legend-line lt-mode-key"><strong>Message modes</strong><div className="lt-mode-groups">{['P','C'].map(root=>{const families=modes.filter(family=>family.id===root||family.id.startsWith(root+'.'));return families.length?<div className="lt-ontology-role" role="group" aria-label={root==='P'?'Sender':'Receiver'} key={root}><strong>{root==='P'?'Sender':'Receiver'}</strong><div className="lt-ontology-families">{families.map(family=><span className="lt-mode-group" role="group" aria-label={family.name} key={family.id}>
      <span className="lt-mode-parent">{family.direct?<i className="lt-label-swatch" style={{background:stateColor(family.id)}}/>:null}{family.name}</span>
      {family.labels.map(id=><span className="lt-mode-label" key={id}><i className="lt-label-swatch" style={{background:stateColor(id)}}/>{shortLabel(id,taxonomy)}</span>)}
    </span>)}</div></div>:null;})}{uncategorized?<span className="lt-ontology-role lt-mode-label" role="group" aria-label="Coverage"><strong>Coverage</strong><i className="lt-label-swatch" style={{background:stateColor('?')}}/>Uncategorized</span>:null}</div></div>:null}
    {Object.keys(tools).length||error?<div className="lt-legend-line"><strong>Tool actions</strong>{Object.entries(tools).map(([id,name])=><span key={id}><i className="lt-tool-swatch" style={{color:`var(--activity-${id})`}}/>{name}</span>)}{error?<span><i className="lt-tool-swatch" style={{color:'var(--activity-error)'}}/>Error</span>:null}</div>:null}
    {sources.length?<div className="lt-legend-line"><strong>Caller text</strong>{sources.map(source=><span key={source.id} title={source.hint}><i className={`lt-source-swatch is-${source.pattern}`}/>{source.id==='typed'?'Human typed':source.name}</span>)}</div>:null}
  </div>;
}
