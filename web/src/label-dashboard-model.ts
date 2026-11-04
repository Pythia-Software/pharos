import {chartSeriesStyle} from './chart-colors';
import {chartPeriods,validPeriodCount,shiftPeriod,periodBuckets,periodKey,periodStart,nextPeriod,type ChartPeriod} from './chart-period';
import type {ChartBucket,ChartSeries} from './chart-series';
import {compareStates,functionRole,functionStates,stateColor,type LabelMessage,type LabelStream,type Resolution,type Taxonomy} from './label-trajectories-model';
import {eventTime} from './label-timeline-model';
import type {ChartInspectionRange} from './chart-inspection';
import {labelNodeName,labelNodeColorID,matchesLabelNode} from './label-inspection-model';
export type LabelAudience='all'|'human'|'machine'|'thinking';
export const labelAudiences={
 all:{title:'Message labels over time',unit:'messages',context:'All retained message kinds in the selected workflow scope.'},
 human:{title:'Human words · message labels',unit:'human inputs',context:'Authored human inputs only: typed text, feedback and resent messages. Templates, tool results and agent-supplied inputs are excluded.'},
 machine:{title:'Agent output · message labels',unit:'agent outputs',context:'Agent-authored messages and calls. Retained thinking is separate and can be explored in Agent Thinking.'},
 thinking:{title:'Agent thinking · message labels',unit:'thinking blocks',context:'Retained agent thinking and reasoning blocks only. This measures captured text, not reasoning time or all model activity.'},
} as const;
export const normalizeLabelAudience=(value:string):LabelAudience=>Object.hasOwn(labelAudiences,value)?value as LabelAudience:'all';
export function audienceStreams(streams:LabelStream[],audience:LabelAudience):LabelStream[]{return streams.map(s=>({...s,sourceMessages:s.sourceMessages??s.messages,messages:s.messages.filter(m=>{
 const thinking=['thinking','reasoning'].includes(m.kind),agent=['assistant','agent'].includes(m.role);
 return audience==='all'||(audience==='human'?m.role==='user'&&/^H-(typed|feedback|resent)(\+ctx)?$/.test(m.segment):audience==='thinking'?agent&&thinking:agent&&!thinking);
})})).filter(s=>s.messages.length);}
export type LabelDates={from?:string;to?:string;preset?:'today'|'1d'|'7d'|'30d'|'90d'|'6m'|'1y'|'all';omit?:{from:string;to:string}[]};
export type LabelChart={period:ChartPeriod;hours:number;days:number;smoothing:number;measure:'percent'|'count';resolution:Resolution;scale:'linear'|'logarithmic'};
export const defaultLabelChart:LabelChart={period:'day',hours:6,days:3,smoothing:0,measure:'percent',resolution:'family',scale:'linear'};
export function normalizeLabelChart(value:unknown):LabelChart{
 const v=value&&typeof value==='object'&&!Array.isArray(value)?value as Partial<LabelChart>:{};
 return {period:typeof v.period==='string'&&Object.hasOwn(chartPeriods,v.period)?v.period:'day',hours:validPeriodCount(v.hours)?v.hours:6,days:validPeriodCount(v.days)?v.days:3,smoothing:typeof v.smoothing==='number'&&Number.isInteger(v.smoothing)&&v.smoothing>=2?Math.min(5000,v.smoothing):0,measure:v.measure==='count'?'count':'percent',resolution:v.resolution==='label'?'label':'family',scale:v.scale==='logarithmic'?'logarithmic':'linear'};
}
export function normalizeLabelDates(value:unknown):LabelDates{
 const v=value&&typeof value==='object'&&!Array.isArray(value)?value as LabelDates:{},valid=(date:unknown)=>typeof date==='string'&&Number.isFinite(Date.parse(date))?new Date(date).toISOString():undefined;
 const omit=Array.isArray(v.omit)?v.omit.flatMap(range=>{const from=valid(range?.from),to=valid(range?.to);return from&&to&&from<to?[{from,to}]:[];}).slice(0,100):[];
 return {from:valid(v.from),to:valid(v.to),preset:v.preset&&['today','1d','7d','30d','90d','6m','1y','all'].includes(v.preset)?v.preset:undefined,...(omit.length?{omit}:{})};
}
export function normalizeLabelSelection(value:unknown):{values:string[];excluded:boolean}{const v=value&&typeof value==='object'?value as {values?:unknown;excluded?:unknown}:{};return {values:Array.isArray(v.values)?v.values.filter((id):id is string=>typeof id==='string').slice(0,1000):[],excluded:v.excluded===true};}
export const generationModel=(s:LabelStream,m:LabelMessage)=>m.generation_model||(['assistant','agent'].includes(m.role)?s.session.model:'')||'Not recorded';
const bound=(value:string|undefined,fallback:number)=>{const at=value?Date.parse(value):NaN;return Number.isFinite(at)?at:fallback;};
const omitted=(at:number,dates:LabelDates)=>dates.omit?.some(range=>at>=Date.parse(range.from)&&at<Date.parse(range.to))??false;
export function dashboardStreams(streams:LabelStream[],model:string,dates:LabelDates={}):LabelStream[]{const from=bound(dates.from,-Infinity),to=bound(dates.to,Infinity);return streams.map(s=>({...s,sourceMessages:s.sourceMessages??s.messages,messages:s.messages.filter(m=>(!model||generationModel(s,m)===model)&&(!dates.from&&!dates.to&&!dates.omit?.length||(()=>{const at=eventTime(m);return at!==null&&at>=from&&at<to&&!omitted(at,dates);})()))})).filter(s=>s.messages.length);}
export function labelTrend(streams:LabelStream[],chart:LabelChart,dates:LabelDates,taxonomy:Taxonomy,focus=''){
 const untimed=streams.reduce((n,s)=>n+s.messages.filter(m=>eventTime(m)===null).length,0);
 const events=streams.flatMap(stream=>stream.messages.flatMap(message=>{const at=eventTime(message);return at===null||omitted(at,dates)?[]:[{stream,message,at}];}));
 if(!events.length)return {series:[] as ChartSeries[],buckets:[] as ChartBucket[],timed:0,untimed,clipped:false};
 const first=Math.min(...events.map(e=>e.at)),last=Math.max(...events.map(e=>e.at))+1,from=bound(dates.from,first),to=bound(dates.to,last),count=chart.period==='hours'?chart.hours:chart.period==='days'?chart.days:1;
 if(to<=from)return {series:[],buckets:[],timed:0,untimed:0,clipped:false};
 const smoothing=Number.isFinite(chart.smoothing)?Math.max(1,Math.min(5000,Math.floor(chart.smoothing))):1,seriesFrom=+(shiftPeriod(new Date(from),chart.period,1-smoothing,count)??new Date(from));
 const startKey=periodKey(new Date(from),chart.period,count),inWindow=(at:number)=>at>=from||periodKey(new Date(at),chart.period,count)!==startKey;
 const seriesLabels=(message:LabelMessage)=>labelTrendKeys(message,chart.resolution,focus);
 const frequencies=new Map<string,number>();for(const e of events.filter(e=>e.at>=seriesFrom&&e.at<to&&inWindow(e.at))){for(const id of seriesLabels(e.message))frequencies.set(id,(frequencies.get(id)??0)+1);}
 const ordered=[...frequencies].sort((a,b)=>b[1]-a[1]),ids=ordered.slice(0,12).map(([id])=>id),hidden=new Set(ordered.slice(12).map(([id])=>id));if(hidden.size)ids.push('other');
 const roleRank=(id:string)=>functionRole(labelNodeColorID(id))==='Sender'?0:functionRole(labelNodeColorID(id))==='Receiver'?1:2;ids.sort((a,b)=>roleRank(a)-roleRank(b)||compareStates(a,b));
 const series:ChartSeries[]=ids.map(key=>({key,label:key==='other'?'Other labels':labelNodeName(key,taxonomy),color:key==='other'?'#bdabcc':stateColor(labelNodeColorID(key)),...key==='other'?{}:chartSeriesStyle(chart.resolution==='label'?'label':'family',labelNodeColorID(key))}));
 // Native calendar buckets include idle periods and preserve DST boundaries.
 const raw=periodBuckets(chart.period,new Date(Math.min(first,from)),new Date(to-1),count),lookup=new Map(raw.map(b=>[b.key,b])),denominators=new Map<string,number>();
 for(const e of events){if(e.at>=to||!inWindow(e.at))continue;const key=periodKey(new Date(e.at),chart.period,count),b=lookup.get(key);if(!b)continue;denominators.set(key,(denominators.get(key)??0)+1);const found=new Set(seriesLabels(e.message));for(const id of ids)if(id==='other'?[...found].some(label=>hidden.has(label)):found.has(id))b.values[id]=(b.values[id]??0)+1;}
 const sums=Object.fromEntries(ids.map(id=>[id,0]));let denominator=0;
 const prepared=raw.map((bucket,i)=>{denominator+=denominators.get(bucket.key)??0;for(const id of ids)sums[id]+=bucket.values[id]??0;const previous=raw[i-smoothing];if(previous){denominator-=denominators.get(previous.key)??0;for(const id of ids)sums[id]-=previous.values[id]??0;}const size=Math.min(smoothing,i+1),values=Object.fromEntries(ids.flatMap(id=>chart.measure==='percent'&&!denominator?[]:[[id,chart.measure==='percent'?sums[id]/denominator:sums[id]/size]]));return {...bucket,values,windowSize:size};});
 return {series,buckets:prepared.filter(b=>{const end=new Date(b.date);nextPeriod(end,chart.period,count);return +end>from&&+b.date<to;}),timed:events.filter(e=>e.at>=from&&e.at<to).length,untimed,clipped:raw.length===5000};
}

/** Include contributing history. A partial first bucket has a gap between
 * preceding complete history buckets and the exact custom start; retain it. */
export function labelDrillURL(params:URLSearchParams,range:ChartInspectionRange,key:string,chart:LabelChart,dates:LabelDates){
 const next=new URLSearchParams(params),originalFrom=bound(dates.from,-Infinity),from=range.rolling?+range.from:Math.max(+range.from,originalFrom),to=Math.min(+range.to,bound(dates.to,Infinity)),omit=[...(dates.omit??[])];
 if(range.rolling&&originalFrom>from){const count=chart.period==='hours'?chart.hours:chart.period==='days'?chart.days:1,start=+periodStart(new Date(originalFrom),chart.period,count);if(start<originalFrom)omit.push({from:new Date(start).toISOString(),to:new Date(originalFrom).toISOString()});}
 next.set('label_lab','distribution');next.set('label_dates',JSON.stringify({from:new Date(from).toISOString(),to:new Date(to).toISOString(),...(omit.length?{omit}:{})}));next.set('label_match_page','0');next.delete('chart_pins');
 next.set('label_focus',key);next.set('label_chart',JSON.stringify({...chart,resolution:'label',smoothing:0}));next.delete('label_series');
 return '/analyze?'+next;
}
export function labelTrendMatches(streams:LabelStream[],chart:LabelChart,series:ChartSeries[],selection:{values:string[];excluded:boolean},focus=''){
 const visible=new Set(series.filter(s=>s.key!=='other').map(s=>s.key));
 return streams.flatMap(stream=>stream.messages.flatMap(message=>{
  if(focus&&!matchesLabelNode(message,focus))return [];
  const keys=labelTrendKeys(message,chart.resolution,focus);
  const match=selection.values.some(key=>key==='other'?keys.some(id=>!visible.has(id)):keys.includes(key));
  return !selection.values.length||(selection.excluded?!match:match)?[{stream,message}]:[];
 }));
}

/** Coverage and parent-only nodes are first-class drill targets. Deep focus
 * uses accepted specific labels even when an older link requested families. */
export function labelTrendKeys(message:LabelMessage,resolution:Resolution,focus=''):string[]{
 if(['?','uncategorized','unlabeled','no-match'].includes(focus)||focus.endsWith('~other'))return matchesLabelNode(message,focus)?[focus]:[];
 const ids=functionStates(message,focus.split('.').length>2?'label':resolution);
 return (ids.length?ids:['?']).filter(id=>!focus||id===focus||id.startsWith(focus+'.'));
}
