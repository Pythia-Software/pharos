import React,{useMemo} from 'react';
import {DashboardSelect} from './dashboard-select';
import {RangeControl} from './usage-chart';
import {useViewState} from './view-state';
import {audienceStreams,dashboardStreams,generationModel,normalizeLabelDates,type LabelAudience,type LabelDates} from './label-dashboard-model';
import type {LabelStream} from './label-trajectories-model';
export function useLabelDashboardFilters(streams:LabelStream[],audience:LabelAudience='all'){
 const [model,setModel]=useViewState('label_model',''),[rawDates,saveDates]=useViewState<LabelDates>('label_dates',{}),dates=normalizeLabelDates(rawDates),[,setPage]=useViewState('label_match_page','0');
 const resetResults=()=>setPage('0'),setDates=(next:LabelDates)=>{saveDates(next);resetResults();};
 const population=useMemo(()=>audienceStreams(streams,audience),[streams,audience]),byModel=useMemo(()=>dashboardStreams(population,audience==='human'?'':model),[population,audience,model]),filtered=useMemo(()=>dashboardStreams(byModel,'',dates),[byModel,JSON.stringify(dates)]),models=[...new Set(population.flatMap(s=>s.messages.map(m=>generationModel(s,m))))].sort();
 const controls=<>{audience!=='human'?<label className="usage-chart-field"><span className="usage-preset-label">Generation model</span><DashboardSelect aria-label="Generation model" data-view-field="label_model" value={model} onChange={e=>{setModel(e.target.value);resetResults();}}><option value="">All models</option>{models.map(id=><option key={id}>{id}</option>)}</DashboardSelect></label>:null}<RangeControl bounds={{from:dates.from?new Date(dates.from):undefined,to:dates.to?new Date(dates.to):undefined,preset:dates.preset==='all'?undefined:dates.preset}} onChange={(from,to,preset)=>setDates({from:from?.toISOString(),to:to?.toISOString(),preset})}/></>;
 return {model,dates,setDates,byModel,filtered,controls,resetResults};
}
