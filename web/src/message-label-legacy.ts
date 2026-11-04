import {EMPTY_QUERY,encodeQuery,type WhereTerm} from '@pythia-software/query-table-core';
import {normalizeLabelChart,normalizeLabelDates} from './label-dashboard-model';
import {analysisVersion} from './dashboard-analysis-model';
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
