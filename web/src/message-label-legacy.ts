import {EMPTY_QUERY,encodeQuery,type WhereTerm} from '@pythia-software/query-table-core';
import {chartPeriods,validPeriodCount,type ChartPeriod} from './chart-period';
import type {Resolution} from './conversation-labels';
import {analysisVersion} from './dashboard-analysis-model';
type LabelDates={from?:string;to?:string;preset?:'today'|'1d'|'7d'|'30d'|'90d'|'6m'|'1y'|'all';omit?:{from:string;to:string}[]};
type LabelChart={period:ChartPeriod;hours:number;days:number;smoothing:number;measure:'percent'|'count';resolution:Resolution;scale:'linear'|'logarithmic'};
const defaultLabelChart:LabelChart={period:'day',hours:6,days:3,smoothing:0,measure:'percent',resolution:'family',scale:'linear'};
function normalizeLabelChart(value:unknown):LabelChart{
 const v=value&&typeof value==='object'&&!Array.isArray(value)?value as Partial<LabelChart>:{};
 return {period:typeof v.period==='string'&&Object.hasOwn(chartPeriods,v.period)?v.period:'day',hours:validPeriodCount(v.hours)?v.hours:6,days:validPeriodCount(v.days)?v.days:3,smoothing:typeof v.smoothing==='number'&&Number.isInteger(v.smoothing)&&v.smoothing>=2?Math.min(5000,v.smoothing):0,measure:v.measure==='count'?'count':'percent',resolution:v.resolution==='label'?'label':'family',scale:v.scale==='logarithmic'?'logarithmic':'linear'};
}
function normalizeLabelDates(value:unknown):LabelDates{
 const v=value&&typeof value==='object'&&!Array.isArray(value)?value as LabelDates:{},valid=(date:unknown)=>typeof date==='string'&&Number.isFinite(Date.parse(date))?new Date(date).toISOString():undefined;
 const omit=Array.isArray(v.omit)?v.omit.flatMap(range=>{const from=valid(range?.from),to=valid(range?.to);return from&&to&&from<to?[{from,to}]:[];}).slice(0,100):[];
 return {from:valid(v.from),to:valid(v.to),preset:v.preset&&['today','1d','7d','30d','90d','6m','1y','all'].includes(v.preset)?v.preset:undefined,...(omit.length?{omit}:{})};
}

/** Migrate retired experiment URLs into the production dashboard. */
export function messageLabelLegacyURL(params:URLSearchParams):string{
 const parse=(name:string)=>{try{return JSON.parse(params.get(name)??'{}');}catch{return {};}};
 const chart=normalizeLabelChart(parse('label_chart')),dates=normalizeLabelDates(parse('label_dates')),where:WhereTerm[]=[];
 for(const [legacy,field] of [['label_repository','repository_name'],['label_provider','provider'],['label_model','model']])if(params.get(legacy))where.push({field,op:'=',value:params.get(legacy)!});
 const source=params.get('label_audience');if(source&&source!=='all')where.push({field:'source',op:'=',value:source==='machine'?'output':source});
 if(dates.from)where.push({field:'occurred_at',op:'>=',value:dates.from});if(dates.to)where.push({field:'occurred_at',op:'<',value:dates.to});
 for(const range of dates.omit??[])where.push({any:[{field:'occurred_at',op:'<',value:range.from},{field:'occurred_at',op:'>=',value:range.to}]});
 const focus=params.get('label_focus');if(focus){if(['?','uncategorized'].includes(focus))where.push({field:'label_status',op:'!=',value:'matched'});else if(focus==='unlabeled'||focus==='no-match')where.push({field:'label_status',op:'=',value:focus==='no-match'?'no_match':'unlabeled'});else if(focus.endsWith('~other'))where.push({field:'label',op:'=',value:focus.slice(0,-6)});else where.push({any:[{field:'label',op:'=',value:focus},{field:'label',op:'starts_with',value:focus+'.'}]});}
 return '/analyze?'+new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'labels',analyze_data:'message_labels',analyze_window:dates.preset??'all',analyze_filters:encodeQuery({...EMPTY_QUERY,where}),analyze_chart:JSON.stringify({v:analysisVersion,view:'trend_line',metric:'messages',split:chart.resolution==='label'?'label':'family',period:chart.period,hours:chart.hours,days:chart.days,rollingPeriods:chart.smoothing,share:chart.measure==='percent',scale:chart.scale})});
}
