import React from 'react';
import {useViewState} from './view-state';
import {type LabelMessage} from './label-trajectories-model';
import {clockRange,eventTime,type TimeRange,type WorkflowClock} from './label-timeline-model';
import {LabPlot} from './label-trajectories-ui';

type SavedClock={workflow?:string;range?:TimeRange};
export function useWorkflowTime(clock:WorkflowClock,workflow:string){
  const [saved,setSaved]=useViewState<SavedClock>('label_time',{});
  return {range:clockRange(clock.range,saved.workflow===workflow?saved.range??null:null),setRange:(range:TimeRange)=>setSaved({workflow,range})};
}
export const elapsed=(ms:number)=>ms<1000?`${Math.round(ms)}ms`:ms<60000?`${Math.round(ms/1000)}s`:ms<3600000?`${Math.round(ms/60000)}m`:`${Number((ms/3600000).toFixed(1))}h`;
const clockText=(at:number)=>new Date(at).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',second:'2-digit'});
export function ClockControls({clock,range,setRange,message}:{clock:WorkflowClock;range:TimeRange;setRange:(range:TimeRange)=>void;message?:LabelMessage}){
  const full=clock.range,span=full[1]-full[0],step=Math.max(1,span/1000),at=message?eventTime(message):null;
  const zoom=(factor:number,center=(range[0]+range[1])/2)=>{
    const width=Math.min(span,Math.max(step*2,(range[1]-range[0])*factor)),start=Math.max(full[0],Math.min(full[1]-width,center-width/2));setRange([start,start+width]);
  };
  return <div className="lt-clock-controls"><div className="lt-clock-actions"><span>{new Date(full[0]).toLocaleDateString()} · {elapsed(range[1]-range[0])} shown</span><button type="button" data-view-field="label_time" onClick={()=>setRange(full)}>Fit workflow</button><button type="button" data-view-field="label_time" onClick={()=>zoom(.5)}>Zoom in</button><button type="button" data-view-field="label_time" onClick={()=>zoom(2)}>Zoom out</button><button type="button" data-view-field="label_time" disabled={at===null} onClick={()=>{if(at!==null){const width=Math.min(span,Math.max(60000,span/12)),start=Math.max(full[0],Math.min(full[1]-width,at-width/2));setRange([start,start+width]);}}}>Around selected message</button></div>
    <LabPlot label="Workflow time overview" height={34}>{w=>{
      const bins=Array(100).fill(0);for(const e of clock.events)bins[Math.min(99,Math.floor((e.at-full[0])/span*100))]++;
      const max=Math.max(1,...bins),x=(at:number)=>(at-full[0])/span*w;
      return <>{bins.map((count,i)=><rect key={i} x={i*w/100} y={28-count/max*24} width={Math.max(1,w/100-1)} height={count/max*24} fill="var(--muted)" opacity=".4"/>)}<rect x={x(range[0])} y="1" width={Math.max(2,x(range[1])-x(range[0]))} height="30" fill="var(--gold)" fillOpacity=".08" stroke="var(--gold)"/></>;
    }}</LabPlot><div className="lt-clock-sliders"><label>Window start · {clockText(range[0])}<input aria-label="Timeline window start" type="range" data-view-field="label_time" min={full[0]} max={full[1]} step={step} value={range[0]} onChange={e=>setRange([Math.min(Number(e.target.value),range[1]-step),range[1]])}/></label><label>Window end · {clockText(range[1])}<input aria-label="Timeline window end" type="range" data-view-field="label_time" min={full[0]} max={full[1]} step={step} value={range[1]} onChange={e=>setRange([range[0],Math.max(Number(e.target.value),range[0]+step)])}/></label></div>
  </div>;
}
export function TimeAxis({range,left,right,height}:{range:TimeRange;left:number;right:number;height:number}){
  return <>{Array.from({length:5},(_,i)=>{const x=left+(right-left)*i/4,at=range[0]+(range[1]-range[0])*i/4;return <g key={i}><line x1={x} x2={x} y1={25} y2={height-20} className="lt-gridline"/><text x={x} y={14} textAnchor={i===0?'start':i===4?'end':'middle'}>{elapsed(at-range[0])}</text></g>;})}<text x={left} y={height-4}>{clockText(range[0])}</text><text x={right} y={height-4} textAnchor="end">{clockText(range[1])}</text></>;
}
