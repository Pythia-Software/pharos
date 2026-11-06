import type {AnalysisCatalog} from './dashboard-analysis-model';
import {shortLabel} from './conversation-labels';
const sourceNames:Record<string,string>={human:'Human input',output:'Agent output',thinking:'Agent thinking',supplied:'Supplied input',tool:'Tool',unknown:'Unknown'};
const coverageNames:Record<string,string>={matched:'Matched a label',unlabeled:'Not labeled',no_match:'No accepted match'};
export const messageLabelsCatalog:AnalysisCatalog={
 dataset:'message_labels',time:{first:'occurred_at',last:'occurred_at'},views:['trend_line','trend_bar','compare'],allowNoSplit:false,
 legacyValues:{view:['trend','distribution','scatter','cycle'],unit:['conversation'],split:['none','repository_name','model','provider','source','label_status']},
 legacy:state=>{
  const view=['trend','distribution','scatter','cycle'].includes(state.view??''),unit=state.unit==='conversation',split=['none','repository_name','model','provider','source','label_status'].includes(state.split);
  if(!view&&!unit&&!split)return state;
  return {...state,...view?{view:'trend_line' as const}:{},...unit?{unit:'records'}:{},...split?{split:'family',selection:{values:[],excluded:false}}:{}};
 },
 measures:{
  messages:{label:'Messages',unit:'messages',format:{kind:'number',decimals:0},op:'count_distinct',field:'message_id',additive:false,populationShare:true,note:'Distinct matching messages. A message can carry several labels. Shares divide by all distinct messages in each interval; labels can overlap.'},
  conversations:{label:'Conversations',unit:'conversations',format:{kind:'number',decimals:0},op:'count_distinct',field:'conversation_id',additive:false,note:'Distinct retained conversations with a matching label.'},
  labels:{label:'Distinct labels',unit:'labels',format:{kind:'number',decimals:0},op:'count_distinct',field:'label',additive:false,note:'Different accepted specific labels, including explicit coverage states.'},
  observations:{label:'Label matches',unit:'matches',format:{kind:'number',decimals:0},op:'count',additive:true,note:'One observation per most-specific accepted label, or one coverage observation when no label is accepted. Messages with several labels contribute several matches.'},
 },
 breakdowns:{function_role:{label:'Sender / Receiver',fields:['function_role'],order:['Sender','Receiver','Coverage']},family:{label:'General Label',fields:['family']},label:{label:'Specific Label',fields:['label']}},
 labels:{source:value=>sourceNames[value]??value,label_status:value=>coverageNames[value]??value,label:id=>id==='?'?'Uncategorized':shortLabel(id),family:id=>id==='?'?'Uncategorized':shortLabel(id)},
 records:{label:'Label observation',plural:'Label observations',note:'Stored message classifications. No labeling runs when this page opens.'},
 units:{},
 periods:{day:"day",week:"week",month:"month"},timestamp:'occurred_at',trend:{metric:'messages',split:'family',allSeries:true},scatter:['messages','labels'],
};
