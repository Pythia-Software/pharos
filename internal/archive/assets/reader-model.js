// The conversation reader's model, without the DOM: message bodies, normalized
// events, token accounting and context samples, tool calls and their results,
// turns, file paths, the Show filter's categories, Find, harness XML blocks,
// message summaries, local labels, agent events, and Markdown parsing.
// It is a classic script whose top-level declarations are globals: ui.py loads
// it before its inline script, which renders with them. Node tests load it
// with vm (web/tests/unit/reader). transcript_find.go and work_reader.go mirror parts
// of it for the service; testdata/ holds the fixtures both must agree on.

// Wall-clock ages shared by the reader and the session hierarchy. Accept both
// message ISO timestamps and the hierarchy's epoch milliseconds.
function relativeAge(value,now=Date.now()){
  if(value==null||value===''||typeof value!=='string'&&typeof value!=='number')return'';
  const time=new Date(value).getTime();if(!Number.isFinite(time)||!Number.isFinite(now))return'';
  const minutes=Math.floor(Math.abs(now-time)/60000);if(minutes<1)return'just now';
  const hours=Math.floor(minutes/60),days=Math.floor(hours/24);
  const age=days?`${days}d${hours%24?` ${hours%24}h`:''}`:hours?`${hours}h${minutes%60?` ${minutes%60}m`:''}`:`${minutes}m`;
  return time>now?`in ${age}`:`${age} ago`;
}

// Chat exports have no repository context unless one was identified separately.
function isChatImportSource(kind){return ['claude-ai','chatgpt','chatgpt-export'].includes(kind)}

// Message bodies
function parsedJSON(text){if(typeof text!=='string')return null;const trimmed=text.trim();if(!trimmed||(!trimmed.startsWith('{')&&!trimmed.startsWith('[')))return null;try{return JSON.parse(trimmed)}catch{return null}}
// Keep parsed bodies with their message, rather than repeatedly decoding them
// for normalization, tokens, roots, filters and tool summaries. Comparing the
// source string also invalidates the cache when loadOriginal replaces a preview.
const messageJSONCache=new WeakMap();
function messageJSON(message,raw=false){
  if(!message)return null;
  let cache=messageJSONCache.get(message);if(!cache){cache={};messageJSONCache.set(message,cache)}
  const text=raw?message.raw_text||message.text:message.text,key=raw?'raw':'body',other=raw?'body':'raw';
  if(cache[key]&&cache[key].text===text)return cache[key].value;
  const value=cache[other]&&cache[other].text===text?cache[other].value:parsedJSON(text);
  cache[key]={text,value};return value;
}
function commonEventText(value){if(!value||typeof value!=='object'||Array.isArray(value))return'';const message=value.message,blocks=Array.isArray(message?.content)?message.content:Array.isArray(value.content)?value.content:[],texts=blocks.filter(block=>block&&['text','input_text','output_text'].includes(block.type)&&typeof block.text==='string').map(block=>block.text);if(texts.length)return texts.join('\n\n');if(typeof message==='string')return message;if(typeof message?.content==='string')return message.content;if(typeof value.text==='string')return value.text;if(typeof value.output_text==='string')return value.output_text;if(['agent_message','user_message','message'].includes(value.type)&&typeof value.content==='string')return value.content;return''}
function shortText(value,limit=150){const text=String(value??'').replace(/\s+/g,' ').trim();return text.length>limit?text.slice(0,limit-1)+'…':text}
function displayText(message){const value=messageJSON(message);return commonEventText(value)||(value?eventDescription(value):String(message?.text??''))}
function eventDescription(value){if(Array.isArray(value))return `${value.length} recorded items`;if(!value||typeof value!=='object')return String(value??'');return shortText([value.type||value.kind||'Recorded event',value.subtype,value.description||value.summary||value.status].filter(Boolean).join(' · '),180)}

// Claude exports keep citations beside the prose, not in its Markdown.
// Link only web URLs; document citations retain their source label and quote.
function messageCitations(message){
  const value=messageJSON(message,true);
  const citations=[...(Array.isArray(value?.citations)?value.citations:[])];
  if(value?.display_content?.type==='rich_link'&&value.display_content.link)citations.push({details:value.display_content.link});
  return citations.map(citation=>{
    const details=citation.details||citation;let url='';
    try{const parsed=new URL(details.url);if(['http:','https:'].includes(parsed.protocol))url=parsed.href}catch{}
    return {title:details.title||details.file_name||details.document_title||url||details.type||'Recorded citation',url,quote:details.cited_text||citation.cited_text||''};
  });
}
function attachmentSummary(message){
  const value=messageJSON(message)||{};
  if(value.file_name||value.title)return String(value.file_name||value.title);
  if(Array.isArray(value.file_names))return value.file_names.filter(Boolean).join(', ')||`${value.file_names.length} files attached`;
  if(value.type==='image')return 'Image'+(value.file_uuid?' · '+value.file_uuid:'');
  if(value.file_uuid)return 'Document · '+value.file_uuid;
  return `${value.count||1} ${value.count===1?'image':'images'} attached`;
}
function exportDocumentSections(value){
  if(value?.type!=='export_file')return [];
  if(/\.md(?:own)?$/i.test(value.file_name||'')&&typeof value.extracted_content==='string')return [{title:value.file_name,text:value.extracted_content}];
  if(!String(value.file_name||'').endsWith('.json'))return [];
  const document=parsedJSON(value.extracted_content),sections=[];
  const visit=(item,label)=>{
    if(Array.isArray(item)){item.forEach((entry,index)=>visit(entry,`${label} ${index+1}`));return}
    if(!item||typeof item!=='object')return;
    const title=item.filename||item.file_name||item.path||item.name||item.period||label;
    for(const [key,text] of Object.entries(item)){
      if(typeof text==='string'&&text.trim()&&['content','text','body','description','prompt_template','instructions','prompt'].includes(key))sections.push({title:key==='content'||key==='text'||key==='body'?String(title):`${title} · ${key.replace(/_/g,' ')}`,text});
      else if(text&&typeof text==='object')visit(text,String(title));
    }
  };
  visit(document,value.file_name);return sections;
}

// ChatGPT source context stays evidence: references are not invented tool calls,
// and a saved voice choice is not proof of a retained assistant recording.
function chatGPTMessageContext(message){
  const raw=messageJSON(message,true);
  if(!raw?.author||!raw?.content?.content_type)return message.source_provider==='chatgpt'&&message.raw_clipped?{references:[],directions:[],referenceKinds:[],model:'',clipped:true}:null;
  const metadata=raw.metadata||{},references=[],seen=new Set();
  const link=value=>{if(typeof value!=='string')return null;try{const url=new URL(value);return ['http:','https:'].includes(url.protocol)?url.href:null}catch{return null}};
  const visit=(value,depth=0)=>{
    if(!value||typeof value!=='object'||depth>12)return;
    if(Array.isArray(value)){value.forEach(item=>visit(item,depth+1));return}
    const url=link(value.url||value.href);
    if(url&&!seen.has(url)){seen.add(url);references.push({url,title:String(value.title||value.name||url),snippet:typeof value.snippet==='string'?value.snippet:'',attribution:typeof value.attribution==='string'?value.attribution:''})}
    // Never interpret widget declarations or compressed payloads as code.
    for(const [key,child]of Object.entries(value))if(!['dil','widget','data','payload'].includes(key))visit(child,depth+1);
  };
  visit(metadata.content_references);visit(metadata.search_result_groups);visit(metadata.citations);
  const parts=Array.isArray(raw.content.parts)?raw.content.parts:[],directions=[...new Set(parts.filter(part=>part?.content_type==='audio_transcription').map(part=>part.direction).filter(value=>typeof value==='string'))];
  const referenceKinds=[...new Set((Array.isArray(metadata.content_references)?metadata.content_references:[]).map(value=>value?.type).filter(value=>typeof value==='string'))];
  return {references,directions,referenceKinds,model:typeof metadata.model_slug==='string'?metadata.model_slug:'',clipped:!!message.raw_clipped};
}
function chatGPTConversationContext(conversation){
  if(conversation.provider!=='chatgpt')return null;
  const evidence=(conversation.messages||[]).map(message=>messageJSON(message)).find(value=>value?.source==='chatgpt_export');
  const settings=evidence?.settings||{},facts=[];
  for(const [key,label]of [['default_model_slug','Recorded model label'],['voice','Saved voice'],['voice_name','Saved voice'],['memory_scope','Memory scope']])if(settings[key]!=null&&typeof settings[key]!=='object')facts.push([label,String(settings[key])]);
  for(const [key,label]of [['is_archived','Archived'],['is_starred','Starred'],['is_do_not_remember','Do not remember'],['is_study_mode','Study mode'],['is_read_only','Read only']])if(typeof settings[key]==='boolean')facts.push([label,settings[key]?'Yes':'No']);
  if(evidence?.sharing)facts.push(['Sharing record','Included in export']);
  return {facts,settings};
}

// Token accounting
// Cached input is part of Codex input; Claude reports it separately. Reasoning is
// part of output. Never add either subset a second time to a reported total.
function tokenTotal(usage){
  return tokenBreakdown(usage)?.total??null;
}
function tokenBreakdown(usage){
  if(!usage||typeof usage!=='object')return null;
  const n=(...values)=>values.find(value=>typeof value==='number'&&Number.isFinite(value)&&value>=0);
  const input=n(usage.input_tokens,usage.prompt_tokens,usage.inputTokens),output=n(usage.output_tokens,usage.completion_tokens,usage.outputTokens);
  const cacheRead=n(usage.cache_read_input_tokens,usage.cacheReadInputTokens,usage.cached_input_tokens,usage.input_tokens_details?.cached_tokens,usage.prompt_tokens_details?.cached_tokens);
  const cacheWrite=n(usage.cache_creation_input_tokens,usage.cacheCreationInputTokens,usage.cache_write_input_tokens,usage.input_tokens_details?.cache_write_tokens,
    usage.cache_creation?Object.values(usage.cache_creation).filter(v=>typeof v==='number'&&v>=0).reduce((a,b)=>a+b,0):undefined);
  const reasoning=n(usage.reasoning_output_tokens,usage.output_tokens_details?.reasoning_tokens,usage.output_tokens_details?.thinking_tokens,usage.completion_tokens_details?.reasoning_tokens,usage.thinking_tokens);
  const explicit=n(usage.total_tokens,usage.totalTokens),separateCache=['cache_read_input_tokens','cache_creation_input_tokens','cacheReadInputTokens','cacheCreationInputTokens','cache_creation'].some(key=>key in usage);
  if([input,output,cacheRead,cacheWrite,explicit].every(v=>v===undefined))return null;
  return {input,output,cacheRead,cacheWrite,reasoning,total:explicit??((input||0)+(output||0)+(separateCache?(cacheRead||0)+(cacheWrite||0):0)),partial:explicit===undefined&&(input===undefined||output===undefined)};
}
function tokenEnvelope(message){
  const raw=messageJSON(message,true)||{},wrappers=[raw];let value=raw;
  for(let depth=0;depth<5;depth++){const next=value.payload||value.event||value.msg||value.data;if(!next||typeof next!=='object'||Array.isArray(next))break;value=next;wrappers.push(value)}
  const record=value.latest_token_usage_record;
  return {raw,value,record,wrappers,type:value.type||raw.type,stream:String(value.agent_id||raw.parent_tool_use_id||message.parent_native_id||'main')};
}
// Whether a Codex session_meta says the rollout continues another thread's
// history rather than starting its own.
function continuesHistory(meta){
  return Boolean(meta?.forked_from_id||(meta?.history_base&&typeof meta.history_base==='object')||meta?.subagent_history_start_ordinal>0);
}
// Harness counters have no categories and may cover only the last request.
// Keep them as evidence; they never enter priced/spent totals or context size.
function conversationTokenEvidence(messages){
  const launches=new Map(messages.filter(m=>m.kind==='delegation'&&m.call_id&&['agent','task'].includes(String(messageJSON(m)?.tool||messageJSON(m)?.name||'').toLowerCase())).map(m=>[String(m.call_id),m]));
  const evidence=[];
  messages.forEach((message,index)=>{
    const launch=launches.get(String(message.call_id||''));if(message.kind!=='delegation_result'||!launch)return;
    const result=messageJSON(message),blocks=result?.content;if(result?.is_error||!Array.isArray(blocks)||!blocks.length)return;
    const last=blocks.at(-1);if(last?.type!=='text'||typeof last.text!=='string')return;
    const match=/^agentId: ([A-Za-z0-9_-]+) (.+)\n<usage>subagent_tokens: ([0-9]+)\ntool_uses: ([0-9]+)\nduration_ms: ([0-9]+)<\/usage>$/.exec(last.text);
    if(!match||match[0]!==last.text)return;
    const agent=match[1],total=Number(match[3]);
    if(match[2]!==`(use SendMessage with to: '${agent}', summary: '<5-10 word recap>' to continue this agent)`||!Number.isSafeInteger(total)||total<=0)return;
    const resume=messageJSON(launch)?.input?.resume,id=result.details?.agentId;
    if(resume&&resume!==agent||id&&id!==agent)return;
    if(blocks.reduce((n,b)=>n+(typeof b?.text==='string'?b.text.split('<usage>subagent_tokens:').length-1:0),0)!==1)return;
    evidence.push({index,stream:String(message.call_id),total,scope:'last-request'});
  });return evidence;
}
function conversationTokenReports(messages){
  const envelopes=messages.map(tokenEnvelope),reports=messages.map(()=>null),seen=new Map(),active=new Map(),cumulative=new Map(),runStarts=new Map(),modelCounters=new Map(),resultIDs=new Set();
  const streamAliases=new Map([['main','main']]);
  for(const message of messages)if(message.kind==='delegation'){const child=String(message.call_id||message.native_id||'');if(child){streamAliases.set(child,child);if(message.native_id)streamAliases.set(String(message.native_id),child)}}
  const streamParents=new Map();
  for(const message of messages)if(message.kind==='delegation'){const child=String(message.call_id||message.native_id||''),rawParent=String(message.parent_native_id||'main'),parent=streamAliases.get(rawParent)||rawParent;if(child&&child!==parent)streamParents.set(child,parent)}
  const canonical=stream=>streamAliases.get(String(stream))||String(stream),descends=(stream,ancestor)=>{const seen=new Set();stream=canonical(stream);while(stream&&!seen.has(stream)){if(stream===ancestor)return true;seen.add(stream);stream=streamParents.get(stream)||''}return false},depth=stream=>{let value=0,seen=new Set;stream=canonical(stream);while(stream&&stream!=='main'&&!seen.has(stream)){seen.add(stream);stream=streamParents.get(stream)||'';value++}return value};
  // New Codex records contain response IDs and exact per-request usage. Legacy
  // token_count snapshots of those same requests must not be charged again.
  const recordStreams=new Set(envelopes.filter(e=>e.raw.type==='token_usage_record'||e.type==='token_usage_record').map(e=>canonical(e.stream)));
  // A transcript that continues another thread's history (a Codex fork, forked
  // subagent or revert page) starts its counters at that thread's usage, which
  // the other thread's transcript counts. inherited holds that start per
  // stream, from its first counter or record.
  const inherits=envelopes.some((e,i)=>messages[i].kind==='metadata'&&messages[i].role==='system'&&e.raw.type==='session_meta'&&continuesHistory(e.value)),inherited=new Map();
  const sum=(start,stream=null)=>reports.slice(start).reduce((total,r)=>total+(r&&(!stream||r.stream===stream)?r.total||0:0),0);
  messages.forEach((message,index)=>{
    if(message.usage_is_estimate===true)return;
    const envelope=envelopes[index],{raw,value,type}=envelope,stream=canonical(envelope.stream),hasRecords=recordStreams.has(stream),info=envelope.record&&!hasRecords?{total_token_usage:envelope.record.thread_token_usage,last_token_usage:envelope.record.usage}:value.info||{};
    const compact=['compacted','compaction','context_compaction','compact_boundary'].includes(type)||raw.type==='compacted'||value.subtype==='compact_boundary'||message.kind==='compaction';
    const trigger=compact?compactionTrigger(raw,value):null;
    if(['result','turn.completed','cost-state'].includes(type)){const id=value.uuid||value.id||raw.uuid;if(id){const key=stream+':'+type+':'+id;if(resultIDs.has(key))return;resultIDs.add(key)}}
    let usage=message.usage||value.message?.usage||value.response?.usage||value.usage||value.metadata?.usage;
    let requestID=value.message?.id||value.response?.id||value.response_id||value.usage_message_id||(['message','response'].includes(type)||value.object==='response'?value.id:null);
    if(type==='message_start')active.set(stream,requestID);
    if(type==='message_delta'){requestID=requestID||active.get(stream)||'unidentified-stream-'+index;active.set(stream,requestID)}
    if(type==='message_stop')active.delete(stream);
    let label='Request usage',scope='request',total=null;
    const models=value.modelUsage&&Object.entries(value.modelUsage).map(([model,usage])=>[model,tokenBreakdown(usage)]).filter(([,usage])=>usage);
    let modelTotal=null;
    if(models?.length){modelTotal=0;for(const [model,usage]of models){const key=stream+':'+(raw.session_id||value.session_id||'')+':'+model,previous=modelCounters.get(key)||0;modelTotal+=usage.total>=previous?usage.total-previous:usage.total;modelCounters.set(key,usage.total)}}
    const running=tokenTotal(info.total_token_usage);
    if(running!==null){
      usage=info.total_token_usage;scope='thread';label='Thread usage checkpoint';
      const last=tokenTotal(info.last_token_usage);
      if(!inherited.has(stream))inherited.set(stream,inherits&&last!==null?Math.max(0,running-last):0);
      if(!hasRecords){const previous=cumulative.get(stream)??inherited.get(stream);total=running>=previous?running-previous:tokenTotal(info.last_token_usage);cumulative.set(stream,running);if(running<previous)label+=' (counter reset; last request only)'}
    }else if(type==='token_count'&&info.last_token_usage){usage=info.last_token_usage;total=hasRecords?null:tokenTotal(usage);label='Last request usage'}
    else if(value.subtype==='task_progress'||value.subtype==='task_notification'){
      scope='task';label='Subagent cumulative usage';const key=canonical(value.tool_use_id||value.task_id||raw.parent_tool_use_id||'');
      const report={total:tokenTotal(usage),compact,label,scope,stream:key||stream,usage,breakdown:tokenBreakdown(usage)};
      const prior=seen.get('task:'+key);if(prior)prior.total=null;
      if(key)seen.set('task:'+key,report);reports[index]=report;return;
    }else if(type==='result'||type==='turn.completed'||type==='cost-state'){
      scope=type==='cost-state'?'session':'run';label=scope==='session'?'Agent usage checkpoint':'Run accounting checkpoint';
      const reported=tokenTotal(usage),aggregate=modelTotal===null?reported:Math.max(reported??0,modelTotal);
      const start=runStarts.get(stream)||0,attributed=sum(start,stream==='main'?null:stream);
      total=aggregate===null?null:Math.max(0,aggregate-attributed);
      runStarts.set(stream,index+1);
      reports[index]={total,compact,label,scope,stream,usage,breakdown:tokenBreakdown(usage),reportedTotal:aggregate,attributed,start,models:value.modelUsage,conflict:aggregate!==null&&aggregate<attributed};return;
    }else total=tokenTotal(usage);
    const context=value.context_token_count??value.estimated_tokens??value.compact_metadata?.pre_tokens;
    if(total===null&&!compact&&running===null&&typeof context!=='number')return;
    const report={total,compact,trigger,label,scope,stream,usage,context,contextEstimated:value.estimated_tokens!==undefined,breakdown:tokenBreakdown(usage),reportedTotal:tokenTotal(usage),checkpoint:tokenTotal(value.thread_token_usage)};
    // Codex writes each response's record before its counter, so a stream's
    // first record reports where its thread usage started. After a counter, a
    // record cannot tell inherited usage from earlier requests no record
    // covers: the counter's start stands and checkpoints fill the rest.
    if(inherits&&!inherited.has(stream)&&type==='token_usage_record'&&report.checkpoint!==null)inherited.set(stream,Math.max(0,report.checkpoint-(total||0)));
    if(requestID&&scope==='request'){
      const key=stream+':'+requestID,prior=seen.get(key);
      if(prior){report.usage={...prior.usage,...usage};report.breakdown=tokenBreakdown(report.usage);report.total=report.breakdown?.total??null;prior.total=null;prior.superseded=true}
      seen.set(key,report);
    }
    reports[index]=report;
  });
  const requestTotals=new Map();
  for(const report of reports){if(report?.scope!=='request'||report.total===null||report.superseded)continue;const prior=requestTotals.get(report.stream)??inherited.get(report.stream)??0;if(report.checkpoint!==null&&report.checkpoint!==undefined){const gap=Math.max(0,report.checkpoint-prior-report.total);report.total+=gap;if(gap)report.label+=' + '+gap.toLocaleString()+' earlier unattributed tokens'}requestTotals.set(report.stream,prior+report.total)}
  // Agent results report the final request. Recover only completed runs with
  // no tool calls, consistent counters, and no retained child accounting.
  const accounted=new Set(),markAccounted=stream=>{while(stream&&!accounted.has(stream)){accounted.add(stream);stream=streamParents.get(stream)}};
  for(const report of reports)if(report?.total>0)markAccounted(report.stream);
  for(let index=messages.length-1;index>=0;index--){
    const message=messages[index],stream=streamAliases.get(String(message.call_id||''));
    if(message.kind!=='delegation_result'||!stream||accounted.has(stream)||message.usage_in_child_conversation)continue;
    const details=messageJSON(message)?.details,usage=details?.usage,breakdown=tokenBreakdown(usage);
    if(details?.status!=='completed'||details.totalToolUseCount!==0||!breakdown||breakdown.total<=0||details.totalTokens!==breakdown.total)continue;
    const categorized=tokenBreakdown({...usage,total_tokens:undefined,totalTokens:undefined})?.total;
    if(categorized!==breakdown.total)continue;
    reports[index]={total:breakdown.total,label:'Subagent result usage',scope:'task',stream,usage,breakdown};markAccounted(stream);
  }
  // Reconcile subagent summaries with their retained request records. Keep
  // unallocated excess visible instead of assigning it to an arbitrary tool.
  const tasks=reports.filter(report=>report?.scope==='task'&&report.total!==null).sort((a,b)=>depth(b.stream)-depth(a.stream));
  for(const report of tasks){const attributed=reports.reduce((n,r)=>n+(r&&r!==report&&r.total!==null&&['request','task'].includes(r.scope)&&descends(r.stream,report.stream)?r.total||0:0),0);report.reportedTotal=report.total;report.total=Math.max(0,report.total-attributed);report.attributed=attributed}
  {const latest=new Map();for(const report of reports)if(report?.scope==='thread'&&recordStreams.has(report.stream))latest.set(report.stream,report);for(const report of latest.values()){report.total=Math.max(0,(report.reportedTotal||0)-(requestTotals.get(report.stream)||0));report.label='Thread checkpoint remainder beyond retained request records'}}
  reports.forEach((report,index)=>{
    if(!report)return;
    if(['run','session'].includes(report.scope)&&report.reportedTotal!==null){const attributed=reports.slice(report.start,index).reduce((n,r)=>n+(r&&(report.stream==='main'||r.stream===report.stream)?r.total||0:0),0);report.total=Math.max(0,report.reportedTotal-attributed);report.attributed=attributed;report.conflict=report.reportedTotal<attributed}
  });
  for(const evidence of conversationTokenEvidence(messages)){
    reports[evidence.index]??={total:null,scope:'observation',stream:evidence.stream,label:'Subagent token evidence'};
    reports[evidence.index].observedMinimum=evidence.total;
  }
  // Stored predictions use the ordinary consumption pathway. They have no
  // input measurement and cannot create an input-context sample.
  for(let index=0;index<messages.length;index++){
    const message=messages[index];
    if(message.usage_is_estimate===true&&!reports[index]){
      const usage=message.usage,total=tokenTotal(usage);
      if(total!==null)reports[index]={total,reportedTotal:total,scope:'request',stream:'main',label:'Estimated output usage',usage,breakdown:tokenBreakdown(usage),isEstimate:true};
    }
  }
  return reports;
}
// Claude records compactMetadata.trigger; the Codex adapter infers
// compaction_trigger from where the compaction falls in its turn.
function compactionTrigger(raw,value){const t=String(raw.compaction_trigger||value.compaction_trigger||value.compactMetadata?.trigger||value.compact_metadata?.trigger||'').toLowerCase();return t==='manual'||t==='auto'?t:null}
// The window a stream's context is measured against. Codex reports it; Claude
// transcripts only name the model, and a 1M window is evident only once
// context has grown past the standard 200K. Unknown windows return null.
function contextWindow(samples){
  const reported=Math.max(0,...samples.map(s=>s.window||0));if(reported)return reported;
  if(!samples.some(s=>/claude|opus|sonnet|haiku|fable/i.test(s.model||'')))return null;
  return samples.some(s=>/\[1m\]/i.test(s.model||'')||s.size>200000)?1000000:200000;
}
// Context occupancy is not consumption. Only request input (including cached
// input) is an occupancy sample; thread/run totals must never become one.
function contextTokenReports(messages,reports){
  const previous=new Map(),exactStreams=new Set(reports.filter(r=>r?.scope==='request'&&r.checkpoint!=null).map(r=>r.stream));
  // Codex reports its window on checkpoints, which exact request records supersede as samples.
  const windows=new Map();
  reports.forEach((report,index)=>{if(!report)return;const {value}=tokenEnvelope(messages[index]),window=value.info?.model_context_window??value.model_context_window,stream=report.stream||'main';if(typeof window==='number'&&window>0)windows.set(stream,Math.max(window,windows.get(stream)||0))});
  reports.forEach((report,index)=>{
    if(!report||report.superseded||report.isEstimate)return;
    const {value}=tokenEnvelope(messages[index]),stream=report.stream||'main';
    // An advisor's request has a context of its own.
    if(value.advisor_of)return;
    let usage=report.scope==='request'?report.usage:value.info?.last_token_usage;
    const b=tokenBreakdown(usage),separate=usage&&['cache_read_input_tokens','cache_creation_input_tokens','cacheReadInputTokens','cacheCreationInputTokens','cache_creation'].some(key=>key in usage);
    let size=typeof report.context==='number'?report.context:b?.input===undefined?null:b.input+(separate?(b.cacheRead||0)+(b.cacheWrite||0):0);
    if(['run','session','task'].includes(report.scope))size=null;
    if(report.compact){previous.delete(stream);report.contextSample={stream,compact:true,trigger:report.trigger,size:null};return}
    if(size===null)return;
    // A legacy checkpoint alongside exact response records is duplicate evidence.
    if(report.scope==='thread'&&exactStreams.has(stream))return;
    const prior=previous.get(stream),before=prior?.size;
    const model=value.message?.model||value.model||messages[index].model;
    report.contextSample={stream,size,before:before??size,delta:before===undefined?null:size-before,estimated:!!report.contextEstimated||!!prior?.estimated,window:windows.get(stream)||null,model:typeof model==='string'?model:null};
    previous.set(stream,{size,estimated:!!report.contextEstimated});
  });
  return reports;
}
// The net context change across samples (with sizes) of one entry: the sum of
// their deltas, or null when they span streams or one is a stream's first
// sample, whose addition is unknown.
function contextDelta(samples){return samples.every(s=>s.delta!==null)&&new Set(samples.map(s=>s.stream)).size===1?samples.reduce((n,s)=>n+s.delta,0):null}
// The context rail's scale: each stream's context window, or, without a known
// window, the largest context observed in the conversation.
function contextScale(samples){
  const streams=[...new Set(samples.map(s=>s.stream))],observed=Math.max(1,...samples.map(s=>s.size||0)),windows=new Map(streams.map(stream=>[stream,contextWindow(samples.filter(s=>s.stream===stream&&!s.compact))]));
  return{streams,windows,scale:sample=>windows.get(sample.stream)||observed};
}
// A rail marker's profile, in percent of its scale: from the context before
// the request to the context after it. Compactions span the rail.
function contextProfile(sample,scale){return sample.compact?{from:100,width:100}:{from:Math.min(100,sample.before/scale(sample)*100),width:Math.min(100,sample.size/scale(sample)*100)}}

// Normalized conversations
function normalizeMessages(messages){
  const output=[],reports=contextTokenReports(messages,conversationTokenReports(messages));
  messages.forEach((message,index)=>{
    const original=message.raw_text||message.text,parsed=messageJSON(message),value=parsed?.payload&&typeof parsed.payload==='object'?parsed.payload:parsed;
    const base={...message,...(message.authorship?{authorship_text:message.authorship_text??message.text}:{}),native_id:message.native_id||message.id||`event-${index}`,raw_text:original};
    let emitted=false;
    // Agent events' ranges index the stored text, so only an entry showing that
    // text keeps them (a block split out of a JSON message does not).
    const emit=(fields,suffix='')=>{const normalized={...base,...fields,tokenReport:emitted?null:reports[index],native_id:base.native_id+suffix};if(normalized.agent_events&&String(normalized.text??'')!==String(message.text??''))delete normalized.agent_events;messageJSONCache.set(normalized,{...messageJSONCache.get(message)});output.push(normalized);emitted=true};
    if(['thinking','reasoning','analysis'].includes(message.kind)){emit({kind:'reasoning'});return}
    const blocks=Array.isArray(value?.message?.content)?value.message.content:Array.isArray(value?.content)?value.content:Array.isArray(value)?value:null;
    if(!['tool_result','delegation_result','error'].includes(message.kind)&&blocks&&blocks.some(block=>['text','input_text','output_text','tool_use','tool_result','thinking'].includes(block?.type))){
      blocks.forEach((block,i)=>{
        if(!block||typeof block!=='object')return;
        const suffix=blocks.length>1?`/block-${i}`:'';
        if(block.type==='tool_use')emit({kind:/^(Task|Agent)$/i.test(block.name)?'delegation':'tool_call',role:'assistant',call_id:block.id||message.call_id,text:JSON.stringify({tool:block.name,input:block.input})},suffix);
        else if(block.type==='tool_result')emit({kind:'tool_result',role:'tool',call_id:block.tool_use_id||message.call_id,text:JSON.stringify({content:block.content,is_error:block.is_error,...(value.tool_use_result?{details:value.tool_use_result}:{})})},suffix);
        else if(['text','input_text','output_text'].includes(block.type))emit({kind:'message',role:value.message?.role||value.role||message.role,text:block.text||''},suffix);
        else if(block.type==='thinking')emit({kind:'reasoning',text:block.thinking||block.text||'Reasoning not retained'},suffix);
        else emit({kind:'metadata',text:JSON.stringify(block)},suffix);
      });return;
    }
    if(['function_call','custom_tool_call'].includes(value?.type)){emit({kind:/spawn_agent|delegate/.test(value.name)?'delegation':'tool_call',role:'assistant',call_id:value.call_id||message.call_id,text:JSON.stringify({tool:value.name,input:parsedJSON(value.arguments)||value.arguments||value.input})});return}
    if(['function_call_output','custom_tool_call_output'].includes(value?.type)){emit({kind:'tool_result',role:'tool',call_id:value.call_id||message.call_id,text:JSON.stringify({output:value.output})});return}
    if(value?.type==='todo_list'&&Array.isArray(value.items)){emit({kind:'tool_call',text:JSON.stringify({tool:'TodoWrite',input:{todos:value.items}})});return}
    if(['agent_message','user_message','message'].includes(value?.type)){emit({kind:'message',role:value.type==='user_message'?'user':value.role||message.role,text:commonEventText(value)||eventDescription(value)});return}
    emit({});
  });return output;
}
function trimTrailingConversationNoise(messages){
  if(messages.length<2)return messages;
  const valueOf=message=>{const parsed=messageJSON(message,true);return parsed?.payload&&typeof parsed.payload==='object'?parsed.payload:parsed},last=valueOf(messages.at(-1)),previous=valueOf(messages.at(-2));
  const aborted=messages.at(-1).kind==='error'&&last?.type==='error'&&typeof last.content==='string'&&last.content.trim().toLowerCase()==='aborted by user';
  const succeeded=previous?.type==='result'&&String(previous.subtype||previous.status||'').toLowerCase()==='success';
  return aborted&&succeeded?messages.slice(0,-1):messages;
}
// Normalizing parses every message, so each conversation does it once.
const normalizedConversations=new WeakMap();
function conversationMessages(conversation){const source=conversation.messages||[];let value=normalizedConversations.get(source);if(!value){value=trimTrailingConversationNoise(normalizeMessages(source));normalizedConversations.set(source,value)}return value}
function messageFailed(message){if(message.kind==='error')return true;if(!['tool_call','tool_result'].includes(message.kind))return false;const payload=toolPayload(message);return Boolean(payload.raw?.is_error||Number(payload.raw?.exit_code)>0||message.kind==='tool_result'&&/^(?:Process exited with code|Exit code:)\s*[1-9]/im.test(readableValue(payload.content)))}
// How the reader groups normalized events: sub-agents' events by parent,
// results paired to their calls (consumed holds the paired results'
// native_ids), and turns of the remaining top-level events, each opened by a
// user's message.
function conversationGraph(messages){
  const childrenByParent=new Map(),delegationResultsByCall=new Map(),toolResultsByCall=new Map(),delegationCalls=new Set(messages.filter(message=>message.kind==='delegation').map(message=>message.call_id).filter(Boolean)),toolCalls=new Set(messages.filter(message=>message.kind==='tool_call').map(message=>message.call_id).filter(Boolean)),consumed=new Set();
  messages.filter(message=>message.parent_native_id).forEach(message=>{if(!childrenByParent.has(message.parent_native_id))childrenByParent.set(message.parent_native_id,[]);childrenByParent.get(message.parent_native_id).push(message);});
  messages.filter(message=>message.call_id&&['delegation_result','tool_result','error'].includes(message.kind)).forEach(message=>{const delegation=delegationCalls.has(message.call_id),regular=(message.kind==='tool_result'||message.kind==='error')&&toolCalls.has(message.call_id),map=delegation?delegationResultsByCall:regular?toolResultsByCall:null;if(map){if(!map.has(message.call_id))map.set(message.call_id,[]);map.get(message.call_id).push(message);consumed.add(message.native_id)}});
  const parentIds=new Set(messages.filter(message=>message.kind==='delegation').flatMap(message=>[message.native_id,message.call_id]).filter(Boolean)),top=messages.filter(message=>(!message.parent_native_id||!parentIds.has(message.parent_native_id))&&!consumed.has(message.native_id)),turns=[];
  // Export summaries and settings are archive metadata, not an extra setup turn.
  // Keep them with the first recorded user turn when one exists.
  const user=top.find(message=>message.role==='user'&&message.kind==='message');
  if(user){const metadata=top.filter(message=>{if(message.kind!=='metadata')return false;const value=messageJSON(message);return value?.type==='claude_ai_conversation'||value?.source==='chatgpt_export'});for(const message of metadata)top.splice(top.indexOf(message),1);top.splice(top.indexOf(user)+1,0,...metadata)}
  let current=null;for(const message of top){if(message.role==='user'&&message.kind==='message'){if(current)turns.push(current);current={human:message,events:[]}}else{if(!current)current={human:null,events:[]};current.events.push(message)}}if(current)turns.push(current);
  return{childrenByParent,delegationResultsByCall,toolResultsByCall,consumed,turns};
}
// Diagnostic sensitivity ranges accompany canonical usage. The input context rail continues to
// describe reported consumption. Sum uncertainty bounds as sensitivity, never
// relabel the sum as a calibrated confidence interval.
function usageEstimateNumber(value){return Number.isFinite(value)&&value>=0?Number(value.toPrecision(2)).toLocaleString():null}
function usageEstimatePresentation(conversation,owned=null){
  const report=conversation?.usage_estimates;
  if(report?.range_kind!=='sensitivity_not_confidence'||!Array.isArray(report.responses))return null;
  const ids=owned?new Set(owned.map(message=>message.id).filter(Boolean)):null;
  const responses=report.responses.filter(response=>(!ids||ids.has(response.message_id))&&['expected','lower','upper'].every(key=>Number.isFinite(response.output_tokens?.[key])&&response.output_tokens[key]>=0));
  if(!responses.length)return null;
  const sum=responses.reduce((total,response)=>{for(const key of ['expected','lower','upper'])total[key]+=response.output_tokens[key];return total},{expected:0,lower:0,upper:0});
  const local=responses.some(response=>response.local_weight>0),unknown=responses.some(response=>!response.model||!response.effort);
  const assumedModels=[...new Set(responses.filter(response=>response.model_source==='observed_conversation_selection').map(response=>response.model).filter(Boolean))];
  return{...sum,label:`~${usageEstimateNumber(sum.expected)} estimated output tokens`,
    detail:`${usageEstimateNumber(sum.lower)}–${usageEstimateNumber(sum.upper)} sensitivity range. ${local?'Local coding calibration with published assumptions':'Published estimation assumptions'} (${report.manifest_revision}).${unknown?' Model or effort unreported; fallback assumptions apply.':''}${assumedModels.length?` Estimation and API pricing assume ${assumedModels.join(', ')} from saved conversation settings; the model used for each response was not recorded.`:''} Output only, for selected-branch responses in conversations without reported usage. Input and cache usage are unavailable; API cost, when priced, covers estimated output only.`,
    assumptions:'/api/estimation/assumptions'};
}
function toolDurationEstimatePresentation(message){
  const estimate=message?.duration_estimate,value=estimate?.seconds;
  if(!value||!['expected','lower','upper'].every(key=>Number.isFinite(value[key])&&value[key]>=0))return null;
  const execution=estimate.execution_seconds,argument=estimate.argument_generation_seconds;
  if(execution&&['observed','estimated'].includes(estimate.execution_status)&&['expected','lower','upper'].every(key=>Number.isFinite(execution[key])&&execution[key]>=0)){
    const recorded=estimate.execution_status==='observed';
    return{label:`${recorded?'Recorded execution:':'Estimated execution: ~'}${recorded?' ':''}${usageEstimateNumber(execution.expected)} s${recorded?'':'*'}`,detail:`${usageEstimateNumber(execution.lower)}–${usageEstimateNumber(execution.upper)} s ${recorded?'native argument-stop to result-observation span':'execution sensitivity range'}. ${argument&&Number.isFinite(argument.expected)?`Argument generation: ${estimate.argument_generation_status==='observed'?'recorded':'estimated'} ${usageEstimateNumber(argument.expected)} s. `:''}Full call-to-result span: ${usageEstimateNumber(value.expected)} s. Execution can include transport and harness time; parallel calls can overlap.`};
  }
  if(estimate.status==='observed')return{label:`Recorded tool span: ${usageEstimateNumber(value.expected)} s`,detail:'Native call start to result observation; this can include transport and harness time.'};
  if(estimate.status!=='estimated')return null;
  return{label:`Estimated call-to-result span: ~${usageEstimateNumber(value.expected)} s*`,detail:`${usageEstimateNumber(value.lower)}–${usageEstimateNumber(value.upper)} s sensitivity range, including argument generation. Published tool assumptions; parallel calls can overlap. Following model work is separate.`};
}
function turnSummaryData(conversation){
  return conversationGraph(conversationMessages(conversation)).turns.map(turn=>({
    human:turn.human,
    response:turn.events.filter(message=>message.kind==='message').at(-1)||null,
    prompt:messageSummary(turn.human)||'Agent setup',
    outcome:messageSummary(turn.events.filter(message=>message.kind==='message').at(-1))||'',
    replies:turn.events.filter(message=>message.kind==='message').length,
    activities:turn.events.filter(message=>message.kind!=='message').length,
    tools:turn.events.filter(message=>message.kind==='tool_call').length,
    subagents:turn.events.filter(message=>message.kind==='delegation').length,
  }));
}
// A compact outline for result cards, using the reader's normalized graph.
// Inline delegations stay under their owning turn; paired results are not turns.
// Each entry lists the messages it owns, so a search match opens its entry.
function conversationOutline(conversation){
  const graph=conversationGraph(conversationMessages(conversation)),seen=new Set();
  const agents=events=>events.filter(message=>message.kind==='delegation'&&!seen.has(message)).map(message=>{
    seen.add(message);
    const {input}=toolPayload(message),children=[...new Set([message.native_id,message.call_id].filter(Boolean).flatMap(key=>graph.childrenByParent.get(key)||[]))],replies=children.filter(item=>item.kind==='message'),results=graph.delegationResultsByCall.get(message.call_id)||[];
    return {id:message.native_id||message.call_id||message.id,kind:'agent',label:String(input.subagent_type||input.agent_type||input.name||'Sub-agent'),
      prompt:String(input.description||input.prompt||input.task||messageSummary(message)||'Sub-agent'),
      human:{role:'user',text:input.prompt||input.description||input.task||messageSummary(message)},
      response:replies.at(-1)||(results.length?{role:'assistant',text:readableValue(toolPayload(results.at(-1)).content)}:null),messages:ownedMessages([message],graph),tools:children.filter(item=>item.kind==='tool_call').length,children:agents(children)};
  });
  let turnNumber=0;
  return graph.turns.map((turn,index)=>({id:turn.human?.id||turn.human?.native_id||'setup-'+index,kind:'turn',label:turn.human?'Turn '+(++turnNumber):'Agent setup',
    prompt:messageSummary(turn.human)||'Agent setup',human:turn.human,response:turn.events.filter(message=>message.kind==='message').at(-1)||null,messages:ownedMessages([turn.human,...turn.events],graph),
    tools:turn.events.filter(message=>message.kind==='tool_call').length,children:agents(turn.events)}));
}
// Publish the result-card model with the model script itself, independently of
// the page shell's rendering and startup work.
window.pharosConversationOutline=conversationOutline;

// Messages a turn or sub-agent owns: its events, their results, and nested
// agents' events. Token totals, search, and reveal all use this one walk.
function ownedMessages(roots,context,visited=new Set()){const owned=[],queue=roots.filter(Boolean);for(let cursor=0;cursor<queue.length;cursor++){const message=queue[cursor];if(visited.has(message))continue;visited.add(message);owned.push(message);if(message.kind==='delegation')for(const key of new Set([message.native_id,message.call_id].filter(Boolean)))queue.push(...(context.childrenByParent.get(key)||[]));if(['tool_call','delegation'].includes(message.kind))queue.push(...(context.toolResultsByCall.get(message.call_id)||[]),...(context.delegationResultsByCall.get(message.call_id)||[]))}return owned}

// Tool calls
function readableValue(value){if(typeof value==='string'){const parsed=parsedJSON(value);return parsed?readableValue(parsed):value}if(Array.isArray(value))return value.map(readableValue).filter(Boolean).join('\n');if(value&&typeof value==='object')return commonEventText(value)||value.text||value.content&&readableValue(value.content)||value.output&&readableValue(value.output)||JSON.stringify(value,null,2);return String(value??'')}
function toolPayload(message){const parsed=messageJSON(message);if(!parsed||typeof parsed!=='object'||Array.isArray(parsed))return{raw:parsed,input:{value:message?.text??''},name:'',content:message?.text??''};let source=parsed.input??parsed.arguments??parsed;source=parsedJSON(source)||source;const input=conventionalInput(source&&typeof source==='object'&&!Array.isArray(source)?source:{value:source});return{raw:parsed,input,name:String(parsed.tool||parsed.name||''),content:parsed.content??parsed.output??parsed.result??''}}
// MCP tool names have used both server__method and server.method. Match known
// families explicitly; an arbitrary tool with “edit” in its name stays generic.
function mcpIdentity(message){
  const name=toolPayload(message).name,match=name.match(/^mcp__(.+)(?:__|\.)([^.]+)$/i);
  if(!match)return null;
  const server=match[1].toLowerCase(),method=match[2].toLowerCase();
  const family=server==='claude_docs'?'docs':server==='memory'?'memory':server==='gmail'?'gmail':/^(?:remote_)?claude-in-chrome$|^(?:remote-devices__)?claude_browser$|^chrome$/.test(server)?'browser':server==='remote-devices'?'device':server==='pharos'?'pharos':server==='conductor'?'conductor':server==='codex_apps'&&method.startsWith('github_')?'github':server==='codex_apps__github'?'github':'';
  return family?{family,method:family==='github'?method.replace(/^github_/,''):method}:null;
}
// Some Docs receipts append viewer instructions after their JSON. Decode only
// the balanced first value; the rest remains accessible in the raw result.
function mcpJSON(value){
  if(typeof value!=='string')return value;
  const parsed=parsedJSON(value);if(parsed!==null)return parsed;
  const text=value.trimStart();if(!text.startsWith('{')&&!text.startsWith('['))return value;
  let depth=0,quoted=false,escaped=false;
  for(let i=0;i<text.length;i++){const c=text[i];if(quoted){if(escaped)escaped=false;else if(c==='\\')escaped=true;else if(c==='"')quoted=false;continue}if(c==='"')quoted=true;else if(c==='{'||c==='[')depth++;else if(c==='}'||c===']'){if(--depth===0)return parsedJSON(text.slice(0,i+1))??value}}
  return value;
}
function mcpRecords(results){
  const records=[];
  const visit=(value,depth)=>{if(depth>10)return;const decoded=mcpJSON(value);if(decoded!==value){visit(decoded,depth+1);return}if(Array.isArray(value)){value.forEach(item=>visit(item,depth+1));return}if(!value||typeof value!=='object')return;records.push(value);for(const key of ['content','output','result','structuredContent','structured_content','acks','operation_results'])if(value[key]!=null)visit(value[key],depth+1);if(['text','input_text','output_text'].includes(value.type))visit(value.text,depth+1)};
  results.forEach(result=>{visit(toolPayload(result).raw,0);visit(result.mcp_receipts,0)});return records;
}
// Outcome flags belong to MCP envelopes and Docs receipts. A fetched JSON
// file or a web page containing an “error” field is still successful data.
function mcpOutcome(results,family=''){
  if(!results.length)return'missing';
  let failed=results.some(r=>r.kind==='error'),applied=false,unknown=false;
  const visit=(value,depth,envelope=false)=>{
    if(depth>10)return;const decoded=mcpJSON(value);if(decoded!==value){visit(decoded,depth+1,envelope);return}
    if(Array.isArray(value)){value.forEach(item=>visit(item,depth+1));return}if(!value||typeof value!=='object')return;
    const wrapper=envelope||(Array.isArray(value.content)&&value.content.every(block=>block&&typeof block.type==='string'))||'structuredContent' in value||'structured_content' in value||('isError' in value&&Array.isArray(value.content))||('is_error' in value&&'content' in value);
    if(wrapper&&(value.is_error===true||value.isError===true||value.error))failed=true;
    if(family==='docs'){
      if(['deny','denied','reject','rejected','error'].includes(value.verdict)||['failed','rejected'].includes(value.status))failed=true;
      if(value.verdict==='allow'||value.status==='applied')applied=true;
      if(value.status==='unknown'||value.commit_status==='unknown')unknown=true;
      for(const key of ['acks','operation_results'])if(value[key])visit(value[key],depth+1);
    }
    for(const key of ['structuredContent','structured_content'])if(value[key])visit(value[key],depth+1,true);
    if(wrapper)for(const key of ['content','output','result'])if(value[key]!=null)visit(value[key],depth+1);
    if(['text','input_text','output_text'].includes(value.type))visit(value.text,depth+1);
  };
  results.forEach(result=>{visit(toolPayload(result).raw,0,true);visit(result.mcp_receipts,0)});
  return failed?(applied?'partial':'failed'):unknown?'unknown':'recorded';
}
function mcpCategory(identity){
  const {family,method}=identity;
  if(family==='browser')return'web';
  if(family==='device')return method==='device_bash'?'command':'tool';
  if(family==='gmail')return'search';
  if(family==='docs')return ['update','create','batch'].includes(method)?'edit':'read';
  if(family==='memory')return /write|edit|delete/.test(method)?'edit':/search/.test(method)?'search':'read';
  if(family==='conductor')return method==='diffcomment'?'edit':'read';
  return /search|query/.test(method)?'search':'read';
}
function mcpTextSections(results){
  const sections=[];
  const visit=(value,depth=0)=>{if(depth>8||value==null)return;const decoded=mcpJSON(value);if(decoded!==value){visit(decoded,depth+1);return}if(typeof value==='string'){if(value.trim())sections.push({text:value,format:'text'});return}if(Array.isArray(value)){value.forEach(item=>visit(item,depth+1));return}if(typeof value!=='object')return;if(value.structuredContent??value.structured_content){visit(value.structuredContent??value.structured_content,depth+1);return}if(value.type==='text'||value.type==='input_text'||value.type==='output_text'){visit(value.text,depth+1);return}if(value.parsed?.body){sections.push({title:value.parsed.name||value.path,text:value.parsed.body,format:'markdown'});return}for(const key of ['content','output','result','text','body','markdown'])if(value[key]!=null){visit(value[key],depth+1);return}sections.push({text:JSON.stringify(value,null,2),format:'code'})};
  results.forEach(result=>visit(toolPayload(result).raw));return sections;
}
function mcpXMLText(xml){
  return String(xml||'').replace(/<gap\b[^>]*\/>/g,'\n[Excerpt: blocks omitted]\n').replace(/<\/(?:paragraph|list|row|doc)>/g,'\n').replace(/<[^>]*>/g,'').replace(/&(lt|gt|amp|quot|apos);/g,(_,entity)=>({lt:'<',gt:'>',amp:'&',quot:'"',apos:"'"})[entity]).trim();
}
function mcpMarkdownExport(data){
  if(data?.format!=='markdown'||typeof data.bytes_b64!=='string'||data.bytes_b64.length>2_000_000)return'';
  try{const alphabet='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/',source=data.bytes_b64.replace(/\s/g,'');if(!/^[A-Za-z0-9+/]*={0,2}$/.test(source)||source.length%4)return'';let bits=0,buffer=0,encoded='';for(const c of source.replace(/=+$/,'')){buffer=(buffer<<6)|alphabet.indexOf(c);bits+=6;if(bits>=8){bits-=8;encoded+='%'+((buffer>>bits)&255).toString(16).padStart(2,'0')}}return decodeURIComponent(encoded)}catch{return''}
}
// A presentation is source evidence, never a reconstructed document version.
// Unknown operations fall back to the original generic tool view.
function mcpPresentation(message,results=[],artifactLinks={}){
  if(message.kind!=='tool_call')return null;
  const identity=mcpIdentity(message);if(!identity)return null;
  const {family,method}=identity,{input}=toolPayload(message),records=mcpRecords(results),status=mcpOutcome(results,family),sections=[],facts=[];
  const add=(title,text,format='text')=>{if(typeof text==='string'&&text.trim())sections.push({title,text,format})};
  const resultText=()=>sections.push(...mcpTextSections(results));
  let label='',target='',href='',category='tool';
  if(family==='docs'){
    const receipt=records.find(r=>r.verdict||r.frame||r.created)||{},payload=mcpJSON(input.payload)||{},create=input.container?.create;
    const artifactId=input.container?.id||receipt.frame?.slug||receipt.created?.minted,link=artifactLinks[artifactId];
    target=link?.title||create?.name||artifactId||'Claude document';href=link?.href||'';
    const confirmed=status==='recorded'&&records.some(r=>r.verdict==='allow'),verb=(done,request)=>confirmed?done:request;
    category=['update','create','batch'].includes(method)?'edit':'read';
    if(method==='update'&&Array.isArray(payload.ops)){
      if(payload.ops.some(op=>!op||!['replace','insert','append','prepend'].includes(op.op)||typeof op.with?.from?.content!=='string'))return null;
      const notices=records.flatMap(r=>Array.isArray(r.data?.notices)?r.data.notices.filter(Boolean):[]);let additions=0;
      payload.ops.forEach((op,index)=>{
        const content=op.with?.from?.content;
        const added=op.op==='replace'&&op.target?.ids?.length>0&&notices.some(n=>n.code==='pending_ended'&&n.as==='resolved'&&n.opIndex===index&&op.target?.ids?.every(id=>n.ids?.includes(id)));
        if(added)additions++;
        if(['replace','insert','append','prepend'].includes(op.op)&&typeof content==='string')add(added?'Added content':op.op==='replace'?'Replacement content':'New content',content,op.with?.as==='markdown'?'markdown':'text');
        else add('Operation',JSON.stringify(op,null,2),'code');
      });
      label=additions===payload.ops.length&&additions?verb('Added to document','Requested addition to document'):verb('Updated document','Requested document update');
    }else if((method==='batch'||method==='create')&&create){label=verb('Created document','Requested document creation');add('Initial content',create.doc?.markdown,'markdown')}
    else if(method==='batch'&&Array.isArray(input.batch)){
      const operations=input.batch.filter(Boolean),nodes=operations.filter(op=>op.verb==='create'&&op.object==='node'&&op.engine==='prose');
      if(!nodes.length||operations.some(op=>!(op.verb==='create'&&['file','node'].includes(op.object)||op.verb==='update'&&['file','project'].includes(op.ref?.object))))return null;
      for(const op of nodes){const source=op.payload?.source;if(source?.as!=='markdown'||typeof source.from?.content!=='string')return null;const parent=op.payload?.parent?.id;
        const tab=operations.flatMap(item=>Array.isArray(item.payload?.patch)?item.payload.patch:[]).find(patch=>patch?.path?.[0]==='tabs'&&patch.path[1]===parent)?.value?.name;
        add(tab?'New tab · '+tab:'New tab',source.from.content,'markdown');
      }
      label=verb('Added document tab','Requested document tab');
    }
    else if(method==='create'&&input.object==='utterance'&&payload.value?.body){label=verb('Commented on document','Requested document comment');add('Quoted passage',payload.value.parent?.anchor?.text);add('Comment',payload.value.body,'markdown')}
    else if(method==='read'){label=payload.kind==='search'?'Searched document':'Read document';if(payload.text)facts.push(['Search',payload.text]);const data=records.find(r=>r.data?.xml)?.data;if(data)add('Recorded excerpt',mcpXMLText(data.xml));if(data?.complete===false)facts.push(['Coverage','Partial excerpt'])}
    else if(method==='export'){label=verb('Exported document','Requested document export');const data=records.find(r=>r.data?.bytes_b64)?.data;add(data?.name||'Exported Markdown',mcpMarkdownExport(data),'markdown');if(input.format)facts.push(['Format',input.format])}
    else if(method==='guide'){label='Read Claude Docs guide';target=input.topic||input.engine||'Claude Docs';resultText()}
    else return null;
    if(status==='partial')label='Partially updated document';
    if(!sections.length)resultText();
  }else if(family==='memory'&&/^memory_(read|write|edit|delete|list|search)$/.test(method)){
    label=({memory_read:'Read memory',memory_write:'Write memory',memory_edit:'Edit memory',memory_delete:'Delete memory',memory_list:'List memories',memory_search:'Search memories'})[method];target=input.path||input.query||'Memory';category=/write|edit|delete/.test(method)?'edit':'read';
    add('Content',input.content||input.body,'markdown');resultText();
  }else if(family==='browser'){
    label=/^(browser_)?batch$/.test(method)?'Browser actions':method==='javascript_tool'?'Run browser JavaScript':method.replace(/_/g,' ');category='web';target=input.url||input.action_summary||input.action||input.text||'Browser';
    if(Array.isArray(input.actions))input.actions.filter(Boolean).forEach((action,index)=>{const args=action.input||{};add(`${index+1}. ${action.name||'Browser action'}`,args.action_summary||args.url||args.text||args.action||JSON.stringify(args),/javascript/.test(action.name)?'code':'text')});
    else add('Action',input.action_summary||input.url||input.text||input.action,/javascript/.test(method)?'code':'text');resultText();
  }else if(family==='gmail'&&method==='search_threads'){
    label='Search email threads';category='search';target=input.query||'Gmail';const data=records.find(r=>Array.isArray(r.threads));
    if(data){facts.push(['Threads retained',String(data.threads.length)]);if(data.resultCountEstimate!=null)facts.push(['Estimated matches',String(data.resultCountEstimate)]);for(const thread of data.threads)add(thread.subject||thread.id,thread.snippet||thread.body);if(data.nextPageToken)facts.push(['Coverage','More results available in source'])}else resultText();
  }else if(family==='device'&&method==='device_bash'){
    label='Run on device';category='command';target=input.description||input.command||'Device';add('Command',input.command,'code');resultText();
  }else if(family==='conductor'&&['getworkspacediff','getdiffcomments','diffcomment','getterminaloutput'].includes(method)){
    label=({getworkspacediff:'Read workspace diff',getdiffcomments:'Read review comments',diffcomment:'Leave review comments',getterminaloutput:'Read terminal output'})[method];category=method==='diffcomment'?'edit':'read';target=input.file||input.source||'Conductor';
    for(const comment of Array.isArray(input.comments)?input.comments.filter(Boolean):[])add([comment.file,comment.lineNumber].filter(Boolean).join(':'),comment.body,'markdown');resultText();
  }else if(family==='github'&&['fetch_file','fetch','get_pr_info','fetch_pr','fetch_issue','fetch_pr_comments','fetch_issue_comments','search','search_issues','search_prs'].includes(method)){
    label=method==='fetch_file'?'Read GitHub file':/search/.test(method)?'Search GitHub':'Read GitHub';category='read';target=[input.repository_full_name||input.repo_full_name,input.path||input.pr_number||input.issue_number||input.query].filter(Boolean).join(' · ')||input.url||'GitHub';
    const data=records.find(r=>r.display_url)||{};if(/^https:\/\/(github\.com|api\.github\.com|raw\.githubusercontent\.com)\//.test(data.display_url||input.url||''))href=data.display_url||input.url;
    if(method==='fetch_file')add(input.path,data.content,'code');else resultText();
  }else if(family==='pharos'){
    const labels={get_conversation_messages:'Read conversation messages',get_conversation_excerpt:'Read conversation excerpt',get_conversation_overview:'Read conversation overview',get_work_detail:'Read workspace',search_messages:'Search messages',search_conversations:'Search conversations',search_work:'Search workspaces',query_statistics:'Query statistics',archive_status:'Read archive status',site_link:'Resolve Pharos link'};
    label=labels[method];if(!label)return null;category=/search|query/.test(method)?'search':'read';target=input.query||input.conversation_id||input.workspace_id||'Pharos';
    for(const [key,value] of Object.entries(input))if(value!=null&&typeof value!=='object')facts.push([key.replace(/_/g,' '),String(value)]);
    const data=records.find(r=>r.items||r.messages||r.rows||r.groups||r.overview||r.statistics||r.url)||records.at(-1)||{};
    const items=data.items||data.messages;
    for(const item of method!=='query_statistics'&&Array.isArray(items)?items.filter(Boolean):[]){
      const prose=item.snippet||item.text||item.body||item.content;
      if(typeof prose==='string')add(item.title||item.role||'Recorded result',prose,'markdown');
      else{add('Title',item.title);for(const key of ['purpose','outcome','first_input','last_response','description','assignment'])add(key.replace(/_/g,' '),item[key],'markdown')}
    }
    if(method==='get_conversation_overview'){
      target=data.title||target;add('Request',data.request?.text,'markdown');add('Last response',data.last_response?.text,'markdown');add('Workspace outcome',data.workspace_outcome,'markdown');add('Source note',data.note);
    }
    add(data.title||'Summary',data.summary,'markdown');
    if(method==='site_link'&&/^\/(?!\/)/.test(data.url||'')){href=data.url;target=data.title||target}
    const rows=data.rows||(method==='query_statistics'?data.items||data.groups?.map(group=>({...group.keys,...group.values})):null);
    if(Array.isArray(rows)&&rows.length){const columns=[...new Set(rows.flatMap(row=>Object.keys(row)))].filter(key=>rows.some(row=>row[key]==null||typeof row[key]!=='object'));sections.push({title:'Results',columns,rows:rows.map(row=>columns.map(key=>row[key]==null?'Unavailable':String(row[key])))})}
    if(method==='query_statistics'&&data.population_complete===false)facts.push(['Population','Incomplete']);
    if(typeof data.coverage==='string')facts.push(['Coverage',data.coverage]);
    if(!sections.length)resultText();
    if(data.truncated||data.has_more||data.next_cursor||data.next_offset!=null)facts.push(['Coverage','More results available in source']);
    // Retain useful scalar statistics without turning provenance into prose.
    if(!sections.length)for(const [key,value]of Object.entries(data))if(value!=null&&typeof value!=='object')facts.push([key.replace(/_/g,' '),String(value)]);
  }else return null;
  if(!label)return null;
  if(status==='failed'||status==='partial'||status==='unknown'){
    const error=records.find(r=>r.error||r.verdict==='deny'||r.verdict==='reject');
    resultText();
    add('Tool outcome',typeof error?.error==='string'?error.error:error?JSON.stringify(error,null,2):status==='unknown'?'The source did not confirm the outcome.':'The tool reported an error.');
  }
  return{family,label,target,href,category,status,sections,facts};
}

// Antigravity names tool arguments in PascalCase; the reader looks for the conventional names.
const INPUT_ALIASES={CommandLine:'command',AbsolutePath:'file_path',TargetFile:'file_path',DirectoryPath:'path',SearchPath:'path',Url:'url',Query:'query',Prompt:'prompt'};
function conventionalInput(input){if(!Object.keys(INPUT_ALIASES).some(key=>key in input))return input;const result={};for(const [key,value] of Object.entries(input)){const alias=INPUT_ALIASES[key];result[alias&&!(alias in input)&&!(alias in result)?alias:key]=value}return result}
function toolCategory(message){const mcp=message.kind==='tool_call'?mcpIdentity(message):null;if(mcp)return mcpCategory(mcp);const payload=toolPayload(message),name=payload.name.toLowerCase(),input=payload.input;if(message.kind==='error'||payload.raw?.is_error)return'error';if(message.kind==='reasoning')return'reasoning';if(message.kind==='metadata'||message.kind==='result')return'run';if(message.kind==='context'||message.kind==='attachment')return'context';if(message.kind.includes('delegation')||/^(task|agent)$|spawn_agent/.test(name))return'agent';if(/todo|task(create|update|delete|list|get)|update_plan/.test(name))return'todo';if(/patch|edit|write|replace/.test(name))return'edit';if(/exec|command|shell|terminal|bash/.test(name)||input.cmd||input.command)return'command';if(/grep|search|find|glob|query/.test(name))return'search';if(/browser|web|navigate|click/.test(name)||input.url)return'web';if(/read|open|view|list/.test(name))return'read';return'tool'}
function commandKind(message){if(toolCategory(message)!=='command')return toolCategory(message);const input=toolPayload(message).input,cmd=String(input.cmd||input.command||'');if(/(?:^|[\s/;&|])(tsc|typecheck|pyright|mypy)(?=$|[\s;&|])|--noEmit/.test(cmd))return'typecheck';if(/(?:^|[\s/;&|])(pytest|vitest|jest)(?=$|[\s;&|])|\b(?:npm|pnpm|yarn|go|cargo)\s+(?:run\s+)?test\b/.test(cmd))return'test';if(/(?:^|[\s/;&|])git(?=$|[\s;&|])/.test(cmd))return'git';if(/(?:^|[\s/;&|])python(?:3(?:\.\d+)?)?(?=$|[\s;&|])/.test(cmd))return'python';if(/\b(?:eslint|ruff|lint)\b/.test(cmd))return'lint';return'command'}
function todoItems(message,results=[]){const {input}=toolPayload(message);let items=Array.isArray(input.todos)?input.todos:Array.isArray(input.plan)?input.plan:null;if(!items)for(const result of results){const payload=toolPayload(result),value=parsedJSON(readableValue(payload.content))||payload.raw;const candidates=value?.todos||value?.tasks||value?.items||(Array.isArray(value)?value:null);if(Array.isArray(candidates))items=candidates}return(items||[input]).map(item=>({...item,content:item.content||item.text,status:item.status||(item.completed?'completed':'pending')}))}
function describeTodoChanges(messages,context){
  const scopes=new Map();
  for(const message of messages){if(toolCategory(message)!=='todo')continue;const {input,name}=toolPayload(message),results=context.toolResultsByCall.get(message.call_id)||[],scope=message.parent_native_id||'main',previous=scopes.get(scope),items=todoItems(message,results),isList=Array.isArray(input.todos)||Array.isArray(input.plan),title=item=>item.content||item.subject||item.step||('Task '+(item.taskId||item.id||'')),key=item=>String(item.id||item.taskId||title(item)),verb=status=>({completed:'Completed',in_progress:'Started',deleted:'Deleted',pending:'Queued'})[status]||'Updated';
    if(isList){const current=new Map(items.map(item=>[key(item),item])),changes=[];
      if(!previous)message.todoSummary='Set '+items.length+' todo'+(items.length===1?'':'s');
      else{for(const [id,item]of current){const before=previous.get(id);if(!before)changes.push('Added: '+title(item));else if(before.status!==item.status)changes.push(verb(item.status)+': '+title(item));else if(title(before)!==title(item))changes.push('Renamed: '+title(item))}for(const [id,item]of previous)if(!current.has(id))changes.push('Removed: '+title(item));message.todoSummary=changes.length?shortText(changes.join(' · '),180):'No todo changes'}
      if(!results.some(result=>result.kind==='error'||toolPayload(result).raw?.is_error))scopes.set(scope,current);
    }else message.todoSummary=/list|get/i.test(name)?'Read todos':(input.status?verb(input.status)+': ':'')+title(items[0]||input);
  }
}
function diffStats(text){let added=0,removed=0;for(const line of String(text||'').split('\n')){if(line.startsWith('+')&&!line.startsWith('+++'))added++;else if(line.startsWith('-')&&!line.startsWith('---'))removed++}return{added,removed}}
function fileChanges(message,result){
  if(toolCategory(message)!=='edit')return null;
  const {input,name}=toolPayload(message),files=[],linesOf=text=>{const lines=String(text??'').split('\n');if(lines.at(-1)==='')lines.pop();return lines},fallbackPath=input.file_path||input.path||input.filename||'';
  const resultPayload=toolPayload(result||{}).raw,resultDiff=resultPayload?.diff||resultPayload?.details?.diff;
  const patch=String(resultDiff||input.patch||input.diff||(/patch/i.test(name)?input.value||'':'')||'');
  if(patch){
    let path='',lines=[],deletion=false;const flush=()=>{if(!path)return;const diff=lines.join('\n'),stats=diffStats(diff);files.push({path,diff,...stats,unknown:deletion&&!stats.removed})};
    for(const line of patch.split('\n')){const marker=line.match(/^\*\*\* (Update|Add|Delete) File: (.+)$/),git=line.match(/^diff --git a\/(.+) b\/(.+)$/);if(marker||git){flush();path=marker?marker[2]:git[2];deletion=marker?.[1]==='Delete';lines=[line]}else if(path)lines.push(line)}
    flush();if(!files.length)files.push({path:fallbackPath||'Patch',diff:patch,...diffStats(patch)});
  }else if(input.old_string!==undefined||input.new_string!==undefined){
    const before=linesOf(input.old_string),after=linesOf(input.new_string),diff=[`--- ${fallbackPath||'before'}`,`+++ ${fallbackPath||'after'}`,...before.map(line=>'-'+line),...after.map(line=>'+'+line)].join('\n');
    files.push({path:fallbackPath||'Edited file',diff,added:after.length,removed:before.length,unknown:Boolean(input.replace_all),note:input.replace_all?'Replacement excerpt; total changed lines unknown':''});
  }else if(input.content!==undefined){files.push({path:fallbackPath||'Written file',diff:String(input.content),unknown:true,contentOnly:true,note:'Written content; previous contents not retained'})}
  else if(fallbackPath)files.push({path:fallbackPath,diff:'',unknown:true});
  return files.length?files:null;
}
function toolFailed(message,results){const category=toolCategory(message);return Boolean(category==='error'||toolPayload(message).raw?.is_error||(mcpIdentity(message)&&['failed','partial'].includes(mcpOutcome(results,mcpIdentity(message).family)))||results.some(item=>item.kind==='error'||toolPayload(item).raw?.is_error||Number(toolPayload(item).raw?.exit_code)>0||(category==='command'&&/^(?:Process exited with code|Exit code:)\s*[1-9]/im.test(readableValue(toolPayload(item).content)))))}

// The reader uses the same action categories as the timeline ribbon.
function readerTimelineCategory(message){
  const payload=toolPayload(message),name=String(payload.name||'').toLowerCase(),category=toolCategory(message);
  if(['wait_agent','wait','taskoutput'].includes(name))return 'wait';
  if(/sleep|poll/.test(name)||name==='write_stdin')return 'poll';
  if(category==='agent'||/delegate|spawn/.test(name)||name==='task')return 'delegate';
  if(['read','search'].includes(category)||/read|search|glob/.test(name))return 'read';
  if(['edit','write'].includes(category)||/patch|write/.test(name)||name==='edit')return 'write';
  if(name.includes('git'))return 'git';
  if(category==='web'||/web|fetch/.test(name))return 'network';
  return category==='command'||/exec|terminal/.test(name)||name==='bash'?'run':'other';
}

// Activity labels can coincide across timeline categories. Keep their URL
// identities distinct and rebuild the index when a fuller result changes a label.
function transcriptTagId(category,label){return 'tag:'+JSON.stringify([category,label])}
function transcriptTagParts(id){if(typeof id!=='string'||!id.startsWith('tag:'))return null;try{const value=JSON.parse(id.slice(4));return Array.isArray(value)&&value.length===2&&value.every(item=>typeof item==='string')?value:null}catch{return null}}
// Presentation groups refine the activity key without changing its saved tag
// identities or the timeline's action categories. Unknown MCP servers belong
// here too, even when they have no specialized rich tool presentation.
function transcriptActivityPresentation(message){
  const {name,raw}=toolPayload(message),category=toolCategory(message);
  const group=(id,label,icon)=>({id,label,icon});
  const icons={read:'read',search:'search',edit:'edit',command:'command',git:'git',python:'code',typecheck:'typecheck',test:'check',lint:'lint',web:'web',agent:'agent',todo:'todo',reasoning:'thought',context:'context',run:'event',error:'error',tool:'tool'};
  let icon=icons[commandKind(message)]||'tool',keyPath=[];
  const mcp=message.kind==='tool_call'&&name.match(/^mcp__(.+)(?:__|\.)([^.]+)$/i);
  if(mcp){
    let server=mcp[1];const method=mcp[2];
    // Codex Apps records both service.method and service_method names.
    if(server.toLowerCase()==='codex_apps'){
      const service=method.match(/^(github|search_service|plugin_management|chatgpt_space|sites|pets|safety_settings|hotline)_(.+)$/i);
      if(service)server+='__'+service[1];
    }
    const names={'remote-devices':'Remote devices','claude_browser':'Browser','remote-devices__claude_browser':'Browser (remote devices)','claude-in-chrome':'Browser (Chrome)','remote_claude-in-chrome':'Browser (Chrome)','chrome':'Browser (Chrome)','claude_docs':'Docs','gmail':'Gmail','google_sheets':'Google Sheets','google_drive':'Google Drive','codex_apps':'Codex apps','codex_apps__github':'GitHub','codex_apps__search_service':'Search service','codex_apps__plugin_management':'Plugin management','codex_apps__chatgpt_space':'Pages','codex_apps__sites':'Sites','conductor':'Conductor','pharos':'Pharos','memory':'Memory','cua_repl':'Computer','node_repl':'JavaScript'};
    const id=server.toLowerCase(),label=names[id]||server;
    const serverIcon=/browser|chrome/.test(id)?'web':id.endsWith('github')?'git':id==='remote-devices'?'command':/docs|sheets|drive|chatgpt_space/.test(id)?'read':/gmail|search_service/.test(id)?'search':id==='memory'?'context':'tool';
    keyPath=[group('mcp','MCP','tool'),group('mcp:'+id,label,serverIcon)];
  }else if(message.kind==='reasoning')keyPath=[group('thinking','Thinking','thought')];
  else if(message.kind==='attachment'){keyPath=[group('attachments','Attachments','read')];icon='read'}
  else if(message.kind==='context'||raw?.type==='claude_ai_conversation'){
    keyPath=[group('context','Context','context')];
    icon=raw?.type==='claude_ai_conversation'?'chat':/agent_listing/.test(raw?.source||'')?'agent':/tools|mcp/.test(raw?.source||'')?'tool':raw?.source==='date_note'?'history':'context';
  }else if(message.kind!=='tool_call'&&message.kind!=='delegation')keyPath=[group('events','System events','event')];
  else if(/^(toolsearch|tool_search|search_tools|list_tools)$/i.test(name)){keyPath=[group('discovery','Tool discovery','search')];icon='search'}
  else if(/^skill$/i.test(name)){keyPath=[group('skills','Skills','read')];icon='read'}
  else{
    const groups={read:['reads','Reads & searches','read'],search:['reads','Reads & searches','read'],edit:['edits','File edits','edit'],command:['commands','Commands','command'],web:['browser','Browser','web'],todo:['planning','Planning','todo'],agent:['delegation','Delegation','agent'],tool:['tools','Other tools','tool']};
    keyPath=[group(...(groups[category]||['events','System events','event']))];
  }
  return{keyPath,icon};
}
function transcriptTagIndex(messages,structure,describe){
  const entries=new WeakMap(),tags=new Map();
  const add=info=>{let existing=tags.get(info.id);if(!existing){existing={...info,count:0,presentations:new Map()};tags.set(info.id,existing)}existing.count++;const style=existing.presentations.get(info.presentationKey);if(style)style.count++;else existing.presentations.set(info.presentationKey,{keyPath:info.keyPath||[],icon:info.icon||'tool',contentKind:info.contentKind,count:1})};
  const remove=info=>{const existing=tags.get(info.id);if(!existing)return;if(!--existing.count){tags.delete(info.id);return}const style=existing.presentations.get(info.presentationKey);if(style&&!--style.count)existing.presentations.delete(info.presentationKey)};
  const refresh=message=>{const before=entries.get(message),description=describe(message),after=description?.label?{...description,id:transcriptTagId(description.category,description.label),contentKind:structure.pathOf(message)[0],presentationKey:JSON.stringify([structure.pathOf(message)[0],description.keyPath,description.icon])}:null;if(before?.id===after?.id&&before?.presentationKey===after?.presentationKey)return false;if(before)remove(before);if(after){add(after);entries.set(message,after)}else entries.delete(message);return true};
  messages.forEach(message=>{if(!structure.consumed.has(message))refresh(message)});
  return{refresh,tagOf:message=>entries.get(message)?.id||null,info:id=>tags.get(id)||null,all:()=>[...tags.keys()],group:category=>[...tags.values()].filter(tag=>tag.category===category).map(tag=>tag.id)};
}
// Rows and key leaves share the icon for the complete saved activity tag.
// Equal labels can cover different tools; neither surface picks just one.
function transcriptActivityIcon(info){
  const styles=[...(info?.presentations.values()||[])],first=styles[0];
  return first&&styles.every(style=>style.icon===first.icon)?first.icon:info?.category==='run'?'command':'tool';
}
function transcriptActivityKeys(index,labelOf=id=>index.info(id).label,contentKind=null){
  const items=[],groups=new Map();
  for(const id of index.all()){
    const info=index.info(id),styles=[...info.presentations.values()].filter(style=>contentKind===null||style.contentKind===contentKind),first=styles[0];if(!first)continue;
    // One saved tag can cover equal labels from several services. Put it at
    // their common ancestor once, rather than duplicating a filter control.
    const keyPath=[];
    for(const [depth,part]of first.keyPath.entries()){if(!styles.every(style=>style.keyPath[depth]?.id===part.id))break;keyPath.push(part)}
    let children=items,path='';
    for(const part of keyPath){path+='/'+part.id;let parent=groups.get(path);if(!parent){parent={...part,id:'activity:'+path,values:[],children:[]};groups.set(path,parent);children.push(parent)}parent.values.push(id);children=parent.children}
    children.push({id,label:labelOf(id),values:[id],icon:transcriptActivityIcon(info)});
  }
  const order=['context','thinking','attachments','skills','discovery','mcp','reads','edits','commands','browser','planning','delegation','events','tools'];
  items.sort((a,b)=>{const rank=item=>{const position=order.indexOf(item.id.split('/')[1]);return position<0?order.length:position};return rank(a)-rank(b)});
  for(const item of groups.values())item.children.sort((a,b)=>a.label.localeCompare(b.label));
  return items;
}
// Content has one category control, with its recorded activities beneath it.
// Entry IDs are presentation identities only; saved category and tag IDs stay unchanged.
function transcriptContentKeys(index,present,labelOf=id=>index.info(id).label){
  const items=[],entries=[],nodes=new Map(),icons={prompts:'human',response:'chat',replies:'chat',thinking:'thought',edits:'edit',commands:'command',reads:'read',tools:'tool',agents:'agent',events:'event',usage:'summary'};
  const roots={thinking:'thinking',edits:'edits',commands:'commands',reads:'reads',tools:'tools',agents:'delegation',events:'events'};
  for(const [kind,label,title]of TRANSCRIPT_CATEGORIES){
    if(!present.has(kind))continue;
    const activity=transcriptActivityKeys(index,labelOf,kind).flatMap(item=>item.id==='activity:/'+roots[kind]?item.children:[item]);
    const tags=[...new Set(activity.flatMap(item=>item.values))],entry=tag=>'content:'+JSON.stringify([kind,tag]);
    for(const tag of tags.length?tags:[null])entries.push({id:entry(tag),kind,tag});
    const decorate=item=>{const result={...item,id:'content:'+JSON.stringify([kind,item.id]),kind,tags:item.values,values:item.values.map(entry),...(item.children?{children:item.children.map(decorate)}:{})};nodes.set(result.id,result);return result;};
    const item={id:'kind:'+kind,kind,category:true,label,title,icon:icons[kind],tags,values:(tags.length?tags:[null]).map(entry),...(activity.length?{children:activity.map(decorate)}:{})};
    nodes.set(item.id,item);items.push(item);
  }
  return{items,entries,nodes,all:entries.map(entry=>entry.id),tags:index.all()};
}
function transcriptContentShown(model,kinds,hidden){
  return model.entries.filter(entry=>kinds.has(entry.kind)&&(!entry.tag||!transcriptTagHidden(hidden,entry.tag))).map(entry=>entry.id);
}
// Category actions retain dormant child choices when hidden. Showing a category
// restores all of its current children; showing a child also enables its category.
function transcriptContentSelection(model,kinds,hidden,node,action){
  const allKinds=TRANSCRIPT_CATEGORIES.map(([kind])=>kind),next=new Set(kinds);
  const tagsFor=kind=>model.items.find(item=>item.kind===kind)?.tags||[];
  let exclusions=new Set(hidden);
  if(!node||action==='clear')return{categories:action==='hide'?[]:allKinds,hidden:action==='hide'?[...exclusions]:[...transcriptTagSelection(exclusions,model.tags,model.tags)]};
  const shown=new Set(transcriptContentShown(model,kinds,hidden));
  if(action==='toggle')action=node.values.every(value=>shown.has(value))?'hide':'show';
  if(action==='only'){
    next.clear();next.add(node.kind);
    exclusions=transcriptTagSelection(exclusions,tagsFor(node.kind),node.tags);
  }else if(action==='exclude'){
    allKinds.forEach(kind=>next.add(kind));
    if(node.category)next.delete(node.kind);
    else exclusions=transcriptTagSelection(exclusions,model.tags,model.tags.filter(tag=>!node.tags.includes(tag)));
  }else if(action==='show'){
    next.add(node.kind);exclusions=transcriptTagSelection(exclusions,node.tags,node.tags);
  }else if(action==='hide'){
    if(node.category)next.delete(node.kind);
    else exclusions=transcriptTagSelection(exclusions,node.tags,[]);
  }
  return{categories:allKinds.filter(kind=>next.has(kind)),hidden:[...exclusions]};
}
// Unscoped label strings were emitted by earlier readers. A scoped show
// override lets a user show one category while retaining that legacy hide for
// other categories and conversations.
function transcriptTagHidden(hidden,id){const parts=transcriptTagParts(id);return hidden.has(id)||Boolean(parts&&hidden.has(parts[1])&&!hidden.has('show:'+id))}
function transcriptTagSelection(hidden,all,shown){
  const next=new Set(hidden),selected=new Set(shown);
  for(const id of all){const label=transcriptTagParts(id)?.[1],override='show:'+id;if(selected.has(id)){next.delete(id);if(label&&next.has(label))next.add(override);else next.delete(override)}else{next.add(id);next.delete(override)}}
  return next;
}

// Retain calls with missing timestamps; the navigator chooses a real clock or
// message order for the entire session before placing these marks.
function readerNavigatorMarks(model,describe=message=>({name:toolPayload(message).name||'Tool'})){
  return model.messages.filter(message=>['tool_call','delegation'].includes(message.kind)).map(message=>{
    const parsedStart=Date.parse(message.created_at),start=Number.isFinite(parsedStart)?parsedStart:null;
    const results=(message.kind==='delegation'?model.delegationResultsByCall:model.toolResultsByCall).get(message.call_id)||[],end=Date.parse(results.at(-1)?.created_at);
    return{start,end:start!==null&&Number.isFinite(end)?Math.max(start,end):null,
      end_known:start!==null&&Number.isFinite(end)&&end>=start,
      category:readerTimelineCategory(message),
      count:1,error:toolFailed(message,results),message_id:message.id,tool_id:message.call_id||message.native_id,...describe(message)};
  });
}

// File paths
// The directories a conversation's file paths are shown relative to: the
// workspace's, the conversation's and those its events recorded, longest first.
function conversationRoots(conversation,workspaceRoots){
  const roots=[...(Array.isArray(workspaceRoots)?workspaceRoots:[workspaceRoots]),conversation.repo_root,conversation.cwd,conversation.worktree_path,...(conversation.repository_roots||[])];
  const metadata=parsedJSON(conversation.metadata_json)||{};
  roots.push(metadata.cwd,metadata.repo_root,metadata.worktree_path);
  for(const message of conversation.messages||[]){const value=messageJSON(message,true);roots.push(value?.cwd,value?.worktree_path,value?.repo_root,value?.payload?.cwd)}
  return [...new Set(roots.filter(value=>typeof value==='string'&&value.startsWith('/')&&value!=='/').map(value=>value.replace(/\/+$/,'')))].sort((a,b)=>b.length-a.length);
}
// A file chip's parts: the directory and file name relative to the longest
// root holding the path (the path itself when none does). A file chip
// collapses the directory of a path inside a root, or of a relative path.
function filePathParts(path,roots){
  const root=(Array.isArray(roots)?roots:[roots]).filter(value=>typeof value==='string'&&value!=='/').map(value=>value.replace(/\/$/,'')).sort((a,b)=>b.length-a.length).find(value=>path===value||path.startsWith(value+'/'))||'',inside=Boolean(root),absolute=/^(\/|[A-Za-z]:[\\/])/.test(path),relative=inside?path.slice(root.length+1):path,cut=relative.lastIndexOf('/'),directory=cut>=0?relative.slice(0,cut+1):'',filename=cut>=0?relative.slice(cut+1):relative;
  return{root,inside,absolute,directory,filename};
}

// Linked disclosure and content state (reader_expanded, reader_collapsed and
// reader_content). An identity the link leaves out keeps the reader's native
// default, so an absent, [] or {} override agrees with a fresh reader.
function readerDisclosureOpen(id,expanded,collapsed,initial){return expanded.has(id)?true:collapsed.has(id)?false:initial}
function readerDisclosureToggle(id,open,expanded,collapsed){
  const opened=new Set(expanded),closed=new Set(collapsed);
  if(open){opened.add(id);closed.delete(id)}else{closed.add(id);opened.delete(id)}
  return{expanded:[...opened].sort(),collapsed:[...closed].sort()};
}
function readerContentShown(state,key,fallback){return state&&Object.hasOwn(state,key)?state[key]===true:fallback}

// Transcript filter and find
const TRANSCRIPT_CATEGORIES=[['prompts','Prompts','Messages you, or a parent agent, sent'],['response','Final responses','The last reply of each turn'],['replies','Interim replies','Agent messages before the final reply of a turn'],['thinking','Thinking','Reasoning the agent recorded'],['edits','File edits','Edits and writes'],['commands','Commands','Shell commands'],['reads','Reads & searches','File reads, listings, and searches'],['tools','Other tools','Browser, todo, MCP, and other tool calls'],['agents','Sub-agents','Delegated agent runs'],['events','System events','Injected context, attachments, run events, and errors'],['usage','Usage & quota','Token and quota checkpoints and cost bookkeeping']];
const TRANSCRIPT_LEVELS=[['all','All','Every event',TRANSCRIPT_CATEGORIES.map(([key])=>key)],['all-minus-usage','All minus usage','Every event except token usage',TRANSCRIPT_CATEGORIES.map(([key])=>key).filter(key=>key!=='usage')],['thought','Chain of thought','Prompts, replies, and thinking; no tool activity',['prompts','response','replies','thinking']],['primary','Primary only','Prompts and each turn’s final response',['prompts','response']],['custom','Custom','Choose each kind of event',null]];
// Named URL presets stand alone. A category list is required only for Custom;
// older links with just a category list still select Custom.
function transcriptFilterState(params,stored={}){
  const level=params.get('reader_filter_level');
  if(level!=='custom'&&TRANSCRIPT_LEVELS.some(([key])=>key===level))return{level,custom:Array.isArray(stored?.custom)?stored.custom.filter(key=>TRANSCRIPT_CATEGORIES.some(([id])=>id===key)):null};
  const keys=TRANSCRIPT_CATEGORIES.map(([key])=>key);
  if(params.has('reader_filter')){
    try{const custom=JSON.parse(params.get('reader_filter'));if(Array.isArray(custom)&&custom.every(key=>keys.includes(key)))return{level:'custom',custom}}catch{}
  }
  const saved=stored&&typeof stored==='object'?stored:{};
  return{level:TRANSCRIPT_LEVELS.some(([key])=>key===saved.level)?saved.level:'all',custom:Array.isArray(saved.custom)?saved.custom.filter(key=>keys.includes(key)):null};
}
function transcriptShown(state){return new Set(state.level==='custom'?state.custom||[]:TRANSCRIPT_LEVELS.find(([key])=>key===state.level)[3])}
function transcriptCategory(message,context){
  if(message.kind==='delegation')return'agents';if(message.kind==='reasoning')return'thinking';
  if(message.kind==='message')return['user','agent'].includes(message.role)?'prompts':context.finalReplies?.has(message)?'response':'replies';
  const value=messageJSON(message),payload=value?.payload||value;
  if(payload?.type==='token_usage'||message.kind==='metadata'&&(['token_count','token_usage_record','cost-state'].includes(value?.type)||['token_count','token_usage_record','cost-state'].includes(payload?.type)||payload?.type==='system'&&payload?.subtype==='thinking_tokens'))return'usage';
  return{edit:'edits',command:'commands',read:'reads',search:'reads',error:'events',run:'events',context:'events'}[toolCategory(message)]||'tools'
}
// The reader's grouping of normalized events: results paired to their calls,
// each turn's final reply, and each event's category path (its own category,
// then those of the sub-agents holding it). A paired result takes its call's
// path. The service's workspace find (transcript_find.go) mirrors this.
const transcriptStructures=new WeakMap();
function transcriptStructure(messages){
  let structure=transcriptStructures.get(messages);if(structure)return structure;
  const delegationCalls=new Map(),toolCalls=new Map(),delegations=new Map(),consumed=new Map(),finalReplies=new Set(),paths=new Map();
  for(const message of messages){if(!message.call_id)continue;const calls=message.kind==='delegation'?delegationCalls:message.kind==='tool_call'?toolCalls:null;if(calls&&!calls.has(message.call_id))calls.set(message.call_id,message)}
  for(const message of messages){if(message.kind==='delegation')for(const key of [message.native_id,message.call_id])if(key&&!delegations.has(key))delegations.set(key,message);if(!message.call_id||!['delegation_result','tool_result','error'].includes(message.kind))continue;const call=delegationCalls.get(message.call_id)||(message.kind!=='delegation_result'?toolCalls.get(message.call_id):null);if(call)consumed.set(message,call)}
  let events=null;for(const message of messages){if(consumed.has(message)||message.parent_native_id&&delegations.has(message.parent_native_id))continue;if(message.role==='user'&&message.kind==='message'){if(events?.length)finalReplies.add(events.at(-1));events=[]}else if(message.kind==='message'&&message.role!=='agent')(events||=[]).push(message)}if(events?.length)finalReplies.add(events.at(-1));
  const pathOf=(message,seen=new Set())=>{if(paths.has(message))return paths.get(message);if(seen.has(message))return[];seen.add(message);const call=consumed.get(message);let path;if(call)path=pathOf(call,seen);else{const parent=message.parent_native_id&&delegations.get(message.parent_native_id);path=[transcriptCategory(message,{finalReplies}),...(parent?pathOf(parent,seen):[])]}paths.set(message,path);return path};
  structure={consumed,finalReplies,pathOf};transcriptStructures.set(messages,structure);return structure;
}
// The text Find matches in an event: a message's prose, or the string values a
// tool call, result, or event carries (object keys in sorted order).
function jsonStrings(value,out=[]){if(typeof value==='string')out.push(value);else if(Array.isArray(value))value.forEach(item=>jsonStrings(item,out));else if(value&&typeof value==='object')Object.keys(value).sort().forEach(key=>jsonStrings(value[key],out));return out}
function findableText(message){return message.findableText??=message.kind==='message'?displayText(message):['thinking','reasoning','analysis'].includes(message.kind)?String(message.text??''):jsonStrings(messageJSON(message)??String(message.text??'')).join('\n')}
// Counts the events whose findable text matches pattern (a RegExp without the
// g flag): shown ones, and hidden ones with the kinds hiding them. Results
// count only when output is searched.
function transcriptFind(messages,{pattern,shown,output,hiddenTagOf}){
  const structure=transcriptStructure(messages),kinds=new Set(),tags=new Set();let visible=0,hidden=0;
  for(const message of messages){if(structure.consumed.has(message)&&!output||!pattern.test(findableText(message)))continue;const missing=structure.pathOf(message).filter(key=>!shown.has(key)),missingTags=hiddenTagOf?.(message)||[];if(missing.length||missingTags.length){hidden++;missing.forEach(key=>kinds.add(key));missingTags.forEach(tag=>tags.add(tag))}else visible++}
  return{shown:visible,hidden,hidden_kinds:TRANSCRIPT_CATEGORIES.map(([key])=>key).filter(key=>kinds.has(key)),...(hiddenTagOf?{hidden_tags:[...tags]}:{})};
}
function transcriptFindPattern(term,regex,caseSensitive){return new RegExp(regex?term:term.replace(/[.*+?^${}()|[\]\\]/g,'\\$&'),caseSensitive?'u':'iu')}

// Harness XML blocks
// Harnesses inject whole XML blocks (<environment_context>, <system-reminder>, …)
// into message text. A block qualifies only when it starts its own line, its
// tags balance, and its root closes at the end of a line; stray `<` in text is
// tolerated, and fenced code is never scanned. Anything else stays prose.
const xmlTagPattern=/<!--[\s\S]*?-->|<!\[CDATA\[[\s\S]*?\]\]>|<\/([A-Za-z_][\w.:-]*)\s*>|<([A-Za-z_][\w.:-]*)((?:\s+[A-Za-z_][\w.:-]*\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*(\/?)>/g;
function xmlBlockEnd(text,start){
  if(!/<[A-Za-z_][\w.:-]*(?:\s[^<>]*)?>/y.test(text.slice(start,start+400)))return -1;
  const tags=new RegExp(xmlTagPattern.source,'g'),stack=[];tags.lastIndex=start;let match;
  while((match=tags.exec(text))){
    if(!stack.length&&(match.index!==start||!match[2]||match[4]))return -1;
    if(match[1]){if(stack.pop()!==match[1])return -1;if(!stack.length)return tags.lastIndex}
    else if(match[2]&&!match[4])stack.push(match[2]);
  }return -1;
}
function xmlSegments(text){
  const source=String(text??''),segments=[],pushProse=value=>{if(value.trim())segments.push({text:value.replace(/^\s*\n|\s+$/g,'')})};
  let prose=0,lineStart=0,fence=null;
  while(lineStart<source.length){
    const newline=source.indexOf('\n',lineStart),stop=newline<0?source.length:newline,line=source.slice(lineStart,stop),marker=line.match(/^\s*(\x60{3,}|~{3,})/);
    if(fence){if(marker&&marker[1][0]===fence[0]&&marker[1].length>=fence.length)fence=null}
    else if(marker)fence=marker[1];
    else{const start=lineStart+line.search(/\S|$/),end=source[start]==='<'?xmlBlockEnd(source,start):-1;
      if(end>0&&/^[ \t]*(?:\n|$)/.test(source.slice(end,end+200))){const xml=source.slice(start,end);pushProse(source.slice(prose,lineStart));segments.push({text:xml,tag:xml.match(/^<([A-Za-z_][\w.:-]*)/)[1]});const next=source.indexOf('\n',end);prose=lineStart=next<0?source.length:next+1;continue}}
    lineStart=stop+1;
  }
  pushProse(source.slice(prose));return segments;
}
function xmlTree(xml){
  const tags=new RegExp(xmlTagPattern.source,'g'),stack=[{children:[]}];let offset=0,match;
  const text=value=>{if(value)stack.at(-1).children.push(value)};
  while((match=tags.exec(xml))){
    text(xml.slice(offset,match.index));offset=tags.lastIndex;
    if(match[1]){const element=stack.pop();element.close=match[0];stack.at(-1).children.push(element)}
    else if(match[2]&&!match[4])stack.push({open:match[0],close:'',children:[]});
    else if(match[2])stack.at(-1).children.push({open:match[0],close:'',children:[]});
    else text(match[0]);
  }return stack[0].children.find(child=>typeof child!=='string');
}
function dedentText(value){const lines=String(value).replace(/^\s*\n|\s+$/g,'').split('\n'),indent=Math.min(...lines.filter(line=>line.trim()).map(line=>line.search(/\S/)));return lines.map(line=>line.slice(Number.isFinite(indent)?indent:0)).join('\n')}
// A message's one-line summary: its prose, or the tags of its XML blocks.
function proseSummary(text){const segments=xmlSegments(text),prose=segments.filter(segment=>!segment.tag).map(segment=>segment.text).join(' ');return prose.trim()?prose:segments.map(segment=>`<${segment.tag}>`).join(' ')}
function messageSummary(message){return proseSummary(displayText(message))}
function compactMessageSummary(message){
  const inline=new Set(['a','span','strong','em','del','code']),text=item=>typeof item==='string'?item:(item.children||[]).map(text).join('')+(inline.has(item.tag)?'':' ');
  return shortText(text(markdownTree(messageSummary(message))),640);
}

// Authorship offsets refer to UTF-8 bytes in the stored body. Keep that body
// through normalization, then project only an exact display string (including
// JSON escapes). Missing or ambiguous classification never implies human text.
function humanWritingMask(message,text=displayText(message)){
  if(message?.role!=='user')return '';
  const source=String(message.authorship_text??message.text??''),spans=(message.authorship?.spans||[]).filter(span=>span.category==='typed');
  if(!spans.length)return '';
  const bytes=new TextEncoder().encode(source),decoder=new TextDecoder(),mask=Array(source.length).fill('0');
  for(const span of spans){
    if(!Number.isInteger(span.start)||!Number.isInteger(span.end)||span.start<0||span.end>bytes.length||span.end<=span.start)continue;
    const start=decoder.decode(bytes.subarray(0,span.start)).length,end=decoder.decode(bytes.subarray(0,span.end)).length;
    mask.fill('1',start,end);
  }
  const flags=mask.join('');if(source===text)return flags;
  // Text blocks can be unwrapped from a JSON envelope. A unique matching
  // literal preserves attribution even for escaped newlines and emoji.
  const matches=[...source.matchAll(/"(?:\\.|[^"\\])*"/g)].filter(match=>{try{return JSON.parse(match[0])===text}catch{return false}});
  if(matches.length!==1)return '';
  const match=matches[0],body=match[0].slice(1,-1),result=[];
  for(const part of body.matchAll(/\\(?:u[\da-fA-F]{4}|["\\/bfnrt])|[^\\]/g)){
    const at=match.index+1+part.index,typed=flags.slice(at,at+part[0].length).split('').every(flag=>flag==='1');
    result.push((typed?'1':'0').repeat(JSON.parse('"'+part[0]+'"').length));
  }
  return result.join('');
}
function hasHumanWriting(message){return humanWritingMask(message).includes('1')}

// Local labels (labels.go, messageLabelsAttacher): a message's closed label
// set, every label with its ancestors. The reader colors a message by its
// family, a root's child, taken from its likeliest specific label; a message
// with only a root label has none. Hues follow this fixed order.
const LABEL_FAMILIES=[['C.report','Report'],['P.control','Control'],['C.deliberation','Deliberation'],['C.input','Input'],['C.feedback','Feedback'],['P.request','Request'],['P.frame','Frame'],['C.difficulty','Difficulty']];
// The taxonomy's roots are role names; a message labeled only at a root says just this.
const LABEL_ROOTS={P:'Asks or steers',C:'Responds or reports'};
const LABEL_SEGMENTS={'H-typed':'Typed text','H-typed+ctx':'Typed text with quoted context','H-template':'Template or saved prompt','H-resent':'Re-sent text','H-feedback':'UI feedback','H-other':'Human message','AIUR':'Agent text sent as a user message','AR':'Agent reply','DEL':'Delegation','TH':'Thinking'};
function labelFamily(id){const family=String(id).split('.').slice(0,2).join('.');return LABEL_FAMILIES.some(([key])=>key===family)?family:''}
function messageLabelView(message,taxonomy=[]){
  const ids=message?.labels?.ids;if(!Array.isArray(ids)||!ids.length)return null;
  const rows=taxonomy instanceof Map?taxonomy:new Map((taxonomy||[]).map(row=>[row.id,row])),probs=message.labels.probs||{};
  const labels=ids.filter(id=>!ids.some(other=>other.startsWith(id+'.'))).map(id=>{const family=labelFamily(id),familyName=LABEL_FAMILIES.find(([key])=>key===family)?.[1]||'',name=LABEL_ROOTS[id]||rows.get(id)?.name||id;
    return{id,name,family,familyName,title:familyName&&id!==family&&!name.toLowerCase().startsWith(familyName.toLowerCase())?`${familyName} · ${name}`:name,definition:rows.get(id)?.definition||'',prob:typeof probs[id]==='number'?probs[id]:null}})
    .sort((a,b)=>(b.prob??0)-(a.prob??0)||a.id.localeCompare(b.id));
  const segment=message.labels.segment||'';
  return{family:labels.find(label=>label.family)?.family||'',segment,segmentName:LABEL_SEGMENTS[segment]||segment,labels};
}
// Agent events (agent_events.go, agentEventsAttacher): sentences of an agent
// message where something happened (an obstacle, a discovery, a workaround),
// with what it was about (subject) and what the agent did (response). Ranges
// are UTF-16 offsets of the message's stored text (messages.text), so they
// apply only where the reader shows that text unchanged: a reply's card, or
// thinking's own text (Codex keeps the JSON event in raw_text and the prose
// in text). normalizeMessages drops them from blocks it splits out. Each event's sentence is
// marked in its group's hue; subject and response sentences that are not
// themselves events get a lighter mark. Groups follow this fixed order, and
// each has the class event-<suffix>.
const EVENT_GROUPS=[['Friction','friction'],['Reactions and learning','learning'],['Worth a second look','second-look'],['Choices you may not see','choices']];
function eventGroupClass(group){return 'event-'+(EVENT_GROUPS.find(([name])=>name===group)?.[1]||EVENT_GROUPS[0][1])}
// The text events are shown on: thinking's own text (the reader shows it as a
// Reasoning row), otherwise what the message card shows.
function eventText(message){return ['thinking','reasoning','analysis'].includes(message?.kind)?String(message.text??''):displayText(message)}
function messageEventView(message,text=eventText(message)){
  const events=message?.agent_events;if(!Array.isArray(events)||!events.length||text!==String(message.text??''))return null;
  const fits=range=>Array.isArray(range)&&Number.isInteger(range[0])&&Number.isInteger(range[1])&&range[0]>=0&&range[0]<range[1]&&range[1]<=text.length;
  const slice=ranges=>ranges.filter(fits).map(range=>text.slice(range[0],range[1]).trim()).join(' ');
  const view=events.filter(event=>fits(event.sentence)).map(event=>({id:event.id??null,target:event.target?.message_id?event.target:null,kind:event.kind,label:event.label||event.kind,group:event.group||'',definition:event.definition||'',
    probability:typeof event.probability==='number'?event.probability:null,sentence:event.sentence,subject:(event.subject||[]).filter(fits),response:(event.response||[]).filter(fits),
    sentenceText:slice([event.sentence]),subjectText:slice(event.subject||[]),responseText:slice(event.response||[])}));
  return view.length?mergeEventSentences(view,text):null;
}
// Two events on near-identical sentences show as one mark: their sentences
// merge into the span covering both, and the mark's popover lists every
// label with the text once (eventTipView). The rule (nearSameSpan,
// eventSpanGroups) lives in event-spans.js, shared with the timeline and
// fingerprint ribbons. Each event keeps its id (and its own range as
// ownSentence), so a link to either still selects the merged mark.
function mergeEventSentences(view,text){
  const merged=[...view];
  for(const group of eventSpanGroups(view.map(event=>event.sentence))){
    const span=[Math.min(...group.map(index=>view[index].sentence[0])),Math.max(...group.map(index=>view[index].sentence[1]))];
    for(const index of group){const event=view[index];if(span[0]!==event.sentence[0]||span[1]!==event.sentence[1])merged[index]={...event,ownSentence:event.sentence,sentence:span,sentenceText:text.slice(span[0],span[1]).trim()}}
  }
  return merged;
}
// What one mark's popover shows: each label once (the likelier, if a kind
// repeats), then each distinct text once: what was said, what it was about,
// what it did next (followUp picks the clause worth showing) and the earlier
// text it points back at. Text already said is not repeated under another
// heading.
function eventTipView(events,followUp=event=>event.responseText){
  const labels=new Map();for(const event of events){const key=event.group+'|'+event.kind,seen=labels.get(key);if(!seen||(event.probability??0)>(seen.probability??0))labels.set(key,{label:event.label,group:event.group,kind:event.kind,probability:event.probability})}
  const once=values=>[...new Set(values.map(value=>String(value||'').trim()).filter(Boolean))];
  const said=once(events.map(event=>event.sentenceText)),fresh=values=>once(values).filter(value=>!said.includes(value));
  const fields=[['Said',said],['About',fresh(events.map(event=>event.subjectText))],['What it did next',fresh(events.map(followUp))],['Points back at',once(events.map(event=>event.target?.text?'“'+event.target.text+'”':''))]];
  return {labels:[...labels.values()],fields:fields.filter(([,values])=>values.length)};
}
// The distinct sentences a message's events mark, in text order: the events'
// own sentences, and their subject and response sentences (context).
function eventSpans(events){
  const distinct=ranges=>[...new Map(ranges.map(range=>[range[0]+':'+range[1],range])).values()].sort((a,b)=>a[0]-b[0]||a[1]-b[1]);
  return{sentences:distinct(events.map(event=>event.sentence)),context:distinct(events.flatMap(event=>[...event.subject,...event.response]))};
}
// eventMask overlays a message's events on mask (a flag per character).
// Each marked sentence gets its own flag, so Markdown keeps it a mark of its
// own whose class names it (event-at-N, the Nth sentence or context span):
// a flag is a private-use character, 8 per span, the low 3 bits the group
// (7 for context). Context goes first, then each event's sentence, so a
// sentence that is an event keeps its event's hue; of two events on one
// sentence the likelier picks it.
const EVENT_FLAG_BASE=0xE000,EVENT_FLAG_SPANS=800,EVENT_CONTEXT_GROUP=7;
function eventMask(text,events,mask=''){
  if(!events?.length)return mask;
  const flags=Array.from({length:text.length},(_,index)=>mask[index]||'0'),spans=eventSpans(events),index=(list,range)=>list.findIndex(item=>item[0]===range[0]&&item[1]===range[1]);
  const paint=(range,span,group)=>{if(span<0||span>=EVENT_FLAG_SPANS)return;const flag=String.fromCharCode(EVENT_FLAG_BASE+span*8+group);for(let at=range[0];at<range[1]&&at<flags.length;at++)flags[at]=flag};
  for(const range of spans.context)paint(range,index(spans.context,range),EVENT_CONTEXT_GROUP);
  for(const event of [...events].sort((a,b)=>(a.probability??0)-(b.probability??0))){const group=EVENT_GROUPS.findIndex(([name])=>name===event.group);paint(event.sentence,index(spans.sentences,event.sentence),Math.max(0,group))}
  return flags.join('');
}
// The events a highlighted mark belongs to, by the span its class names: the
// events of that sentence, or for a context mark, those whose subject or
// response it is.
function eventsAtMark(events,mark){
  const classes=String(mark?.className??'').split(/\s+/),at=classes.find(name=>/^event-at-\d+$/.test(name));if(!at)return [];
  const spans=eventSpans(events),span=Number(at.slice(9)),same=(a,b)=>a&&b&&a[0]===b[0]&&a[1]===b[1];
  if(classes.includes('event-context')){const range=spans.context[span];return events.filter(event=>[...event.subject,...event.response].some(item=>same(item,range)))}
  const range=spans.sentences[span];return events.filter(event=>same(event.sentence,range));
}
const MARK_CLASSES={'1':'human-written','2':'labeled-text'};
function markClass(flag){
  if(MARK_CLASSES[flag])return MARK_CLASSES[flag];
  const target=flag.charCodeAt(0)-EVENT_TARGET_BASE;if(target>=0&&target<EVENT_TARGET_SPANS*2)return `event-target event-target-at-${target>>1}`+(target&1?' human-written':'');
  const code=flag.charCodeAt(0)-EVENT_FLAG_BASE;if(!(code>=0&&code<EVENT_FLAG_SPANS*8))return '';
  const span=code>>3,group=code&7;return group===EVENT_CONTEXT_GROUP?`event-context event-at-${span}`:`agent-event ${eventGroupClass(EVENT_GROUPS[group]?.[0])} event-at-${span}`;
}
// Two-sided events (agent_event_links.go): an event whose sentence points
// back at earlier text (a quote of the person's words, a prompt sentence it
// shares specific words with) carries a target: the message, UTF-16 offsets
// into its stored text, and those words. The reader pairs both ends: the
// observation (the event's sentence) and its target, numbered in reading
// order. eventLinks lists the pairs whose target is in this conversation.
function eventLinks(messages){
  const present=new Set(messages.map(message=>message.id).filter(Boolean)),links=[],seen=new Set();
  // Events on one (merged) sentence that point at the same text are one
  // pair: one number, one arc, every label. ids lists them all; id is the
  // first, the one the reader writes to reader_event_link.
  const sameTarget=(a,b)=>a.message_id===b.message_id&&(Number.isInteger(a.start)&&Number.isInteger(b.start)&&Number.isInteger(a.end)&&Number.isInteger(b.end)?nearSameSpan([a.start,a.end],[b.start,b.end]):String(a.text??'')===String(b.text??''));
  for(const message of messages){
    const view=messageEventView(message);if(!view)continue;
    for(const event of view)if(event.id&&event.target&&present.has(event.target.message_id)&&!seen.has(event.id)){seen.add(event.id);
      const weak=event.target.method==='overlap'||!event.target.strong;
      const pair=links.find(link=>link.message===message.id&&link.sentence[0]===event.sentence[0]&&link.sentence[1]===event.sentence[1]&&sameTarget(link.target,event.target));
      if(pair){pair.ids.push(event.id);if(!pair.groups.includes(event.group))pair.groups.push(event.group);if(!pair.labels.includes(event.label))pair.labels.push(event.label);pair.weak=pair.weak&&weak;continue}
      links.push({id:event.id,ids:[event.id],n:0,message:message.id,target:event.target,group:event.group,groups:[event.group],label:event.label,labels:[event.label],kind:event.kind,sentence:event.sentence,sentenceText:event.sentenceText,weak})}
  }
  links.forEach((link,index)=>{link.n=index+1});
  return links;
}
// The pair an event id belongs to (any of its merged events' ids).
function eventLinkFor(links,id){return links.find(link=>link.id===id||link.ids?.includes(id))||null}
// A merged pair remains available while any of its event groups is visible.
function eventLinkHighlightKeys(link){return (link.groups||[link.group]).map(group=>'events:'+eventGroupClass(group))}
function eventLinkVisible(link,shown){return !!link&&eventLinkHighlightKeys(link).some(key=>shown.has(key))}
// The spans a message's targets cover in the text the reader shows, each
// with the links pointing at it. Offsets apply when the shown text is the
// stored text there; otherwise (a prompt shown without its harness wrapper)
// the target's words are found in it. A target whose words are not shown is
// left out.
function eventTargetSpans(text,links){
  const spans=new Map();
  for(const link of links){
    const t=link.target,words=String(t.text??'');if(!words)continue;
    let start=Number.isInteger(t.start)&&text.slice(t.start,t.end)===words?t.start:text.indexOf(words);
    if(start<0){const at=text.toLowerCase().indexOf(words.toLowerCase());if(at<0)continue;start=at}
    const key=start+':'+(start+words.length),span=spans.get(key)??{range:[start,start+words.length],links:[]};span.links.push(link.id);spans.set(key,span);
  }
  return [...spans.values()].sort((a,b)=>a.range[0]-b.range[0]||a.range[1]-b.range[1]);
}
// eventTargetMask paints target spans over mask (see eventMask): one flag per
// span, keeping whether the text under it was the person's writing, so a
// target in a prompt stays marked as human-written too.
const EVENT_TARGET_BASE=0xFA00,EVENT_TARGET_SPANS=400;
function eventTargetMask(text,spans,mask=''){
  if(!spans?.length)return mask;
  const flags=Array.from({length:text.length},(_,index)=>mask[index]||'0');
  spans.slice(0,EVENT_TARGET_SPANS).forEach((span,index)=>{for(let at=span.range[0];at<span.range[1]&&at<flags.length;at++)flags[at]=String.fromCharCode(EVENT_TARGET_BASE+index*2+(flags[at]==='1'?1:0))});
  return flags.join('');
}
// The other end of a link: from an observation to its target, and back.
function eventLinkEnd(links,id,from){
  const link=eventLinkFor(links,id);if(!link)return null;
  return from==='target'?{side:'observation',message:link.message,link}:{side:'target',message:link.target.message_id,link};
}
// Highlights: the kinds of marks the reader can show, each switched on or
// off on its own. Each is a boolean view field, so a link keeps the choice,
// with the last choice saved as the fallback for links without it. Human
// writing is shown unless turned off; the classifiers' marks are opt-in.
// Showing a highlight only toggles a class on the panel (show-<id>); the
// marks are always rendered. Where a message's labels and its human writing
// cover the same text, the label's hue wins.
const READER_HIGHLIGHTS=[
  {id:'writing',field:'reader_writing',pref:'pharos-reader-writing',fallback:true,label:'Human writing',title:'Text a person typed or dictated. Pasted, quoted, templated and re-sent text stays plain.'},
  {id:'events',field:'reader_events',pref:'pharos-reader-events',fallback:false,label:'Agent events',title:'Sentences where an agent hit friction, learned something or made a choice you may not see (sentence classifier). Hover a highlighted sentence for its events.'},
  {id:'labels',field:'reader_labels',pref:'pharos-reader-labels',fallback:false,label:'Message labels',title:'Tint each message card by what it does (message classifier). Hover the card for its labels.'}];
// The highlights a conversation offers, in READER_HIGHLIGHTS order: whether
// each has anything to show (available, else why not in title), a status
// note when needed, and a legend of the hues it uses here (label families or
// event groups). Human writing is offered only where there is some; a
// classifier only once it has run on the library, so its row can say why a
// conversation has nothing to show.
function readerHighlightOptions(conversation,messages,taxonomy=[]){
  const options=[];
  for(const option of READER_HIGHLIGHTS){
    if(option.id==='writing'){if(messages.some(hasHumanWriting))options.push({...option,available:true,note:'',legend:[]});continue}
    if(option.id==='events'){const summary=conversation?.agent_events;if(!summary)continue;
      const views=messages.map(message=>messageEventView(message)).filter(Boolean),count=views.reduce((sum,view)=>sum+view.length,0),groups=new Set(views.flat().map(event=>event.group));
      options.push({...option,available:count>0,title:count?option.title:'No agent events found in this conversation',note:summary.pending?'Waiting to be rebuilt for changed transcripts or session evidence; which messages these belong to may change':'',
        legend:[...EVENT_GROUPS.filter(([group])=>groups.has(group)).map(([group])=>({name:group,className:eventGroupClass(group)})),...(views.flat().some(event=>event.target)?[{name:'Earlier text linked to an event',className:'event-target-legend'}]:[])]});continue}
    const summary=conversation?.labels;if(!summary)continue;
    const families=new Set(messages.map(message=>messageLabelView(message,taxonomy)?.family).filter(family=>family!==undefined));
    options.push({...option,available:Boolean(summary.labeled),title:summary.labeled?option.title:'Pharos has not labeled this conversation yet',note:summary.pending?(summary.labeled?'Still labeling this conversation':'Not labeled yet'):'',
      legend:[...LABEL_FAMILIES,['','Other']].filter(([key])=>families.has(key)).map(([key,name])=>({name,family:key}))});
  }
  return options;
}
// Whole-key defaults use the existing linked fields for every key section.
const READER_KEY_PRESETS=[
  {id:'last',label:'Last',note:'Restore your last selection of content, timeline and highlights.'},
  {id:'all',label:'All',note:'Show all content, the full timeline and all text highlighting.',level:'all',highlights:true,timeline:true},
  {id:'content',label:'Content only',note:'Show all content. Hide the timeline and all text highlighting.',level:'all',highlights:false,timeline:false},
  {id:'quiet',label:'Quiet',note:'Show the timeline, text highlighting and all content except usage and quota checkpoints.',level:'all-minus-usage',highlights:true,timeline:true}
];
function readerKeyPresetValues(id){
  const preset=READER_KEY_PRESETS.find(preset=>preset.id===id);if(!preset||id==='last')return null;
  const shown=[...transcriptShown({level:preset.level})];
  return{reader_filter_level:preset.level,reader_filter:shown,reader_hidden_tags:[],
    ...Object.fromEntries(READER_HIGHLIGHTS.map(option=>[option.field,preset.highlights])),reader_highlight_hidden:[],
    timeline_visibility:preset.timeline?{}:{sections:{values:[],excluded:false,allHidden:true}}};
}
function readerKeyLastNote(values){
  const shown=new Set(values.reader_filter||[]),visible=TRANSCRIPT_CATEGORIES.filter(([id])=>shown.has(id)).map(([,label])=>label),hidden=TRANSCRIPT_CATEGORIES.filter(([id])=>!shown.has(id)).map(([,label])=>label);
  const on=READER_HIGHLIGHTS.filter(option=>values[option.field]).map(option=>option.label),off=READER_HIGHLIGHTS.filter(option=>!values[option.field]).map(option=>option.label);
  const parts=[hidden.length?`Content shown: ${visible.join(', ')||'none'}. Hidden: ${hidden.join(', ')}.`:'All content shown.',`Text highlighting shown: ${on.join(', ')||'none'}; hidden: ${off.join(', ')||'none'}.`];
  const tags=values.reader_hidden_tags||[],marks=values.reader_highlight_hidden||[];
  if(tags.length)parts.push(`Activity filters: ${tags.map(id=>{const show=id.startsWith('show:'),tag=show?id.slice(5):id;return (show?'show ':'hide ')+(transcriptTagParts(tag)?.[1]||tag)}).join(', ')}.`);
  if(marks.length){const annotations=marks.filter(id=>id.startsWith('annotation:')).length,keys=marks.filter(id=>!id.startsWith('annotation:'));parts.push(`Highlight exclusions: ${[...keys,...annotations?[annotations+' individual annotation'+(annotations===1?'':'s')]:[]].join(', ')}.`)}
  const timeline=values.timeline_visibility||{},entries=Object.entries(timeline);
  if(!entries.length)parts.push('All timeline elements shown.');
  else if(entries.length===1&&timeline.sections?.allHidden)parts.push('Timeline elements hidden.');
  else parts.push('Timeline filters: '+entries.map(([section,selection])=>`${section}: ${selection.allHidden?'all hidden':selection.excluded?'hidden '+selection.values.join(', '):'only '+selection.values.join(', ')}`).join('; ')+'.');
  return parts.join(' ');
}
function readerKeyPresetID(state,highlights,hidden=[],tags=[],timeline={}){
  if(hidden.length||tags.length)return null;const shown=transcriptShown(state);
  return READER_KEY_PRESETS.find(preset=>{
    const values=readerKeyPresetValues(preset.id);if(!values)return false;
    const expected=new Set(values.reader_filter);
    return shown.size===expected.size&&[...shown].every(key=>expected.has(key))
      &&READER_HIGHLIGHTS.every(option=>Boolean(highlights[option.field])===values[option.field])
      &&JSON.stringify(timeline)===JSON.stringify(values.timeline_visibility);
  })?.id||null;
}
// The ids of the highlights shown: a linked value (true or false) wins, then
// the saved choice, then the option's fallback. One with nothing to show is off.
function readerHighlightsShown(options,linked=()=>null,saved=(pref,fallback)=>fallback){
  return new Set(options.filter(option=>{if(!option.available)return false;const value=linked(option.field);return typeof value==='boolean'?value:saved(option.pref,option.fallback)===true}).map(option=>option.id));
}
// A conversation only edits its available keys; other conversations' exclusions
// survive ordinary selections. Whole-key defaults explicitly reset everything.
function readerHighlightExclusions(hidden,all,selected){
  const available=new Set(all),shown=new Set(selected);
  return [...hidden.filter(key=>!available.has(key)),...all.filter(key=>!shown.has(key))];
}
// An individual annotation keeps its conversation, message, kind and span
// identity in the same URL field as the reader's highlight exclusions.
function readerAnnotationPrefix(conversation){return 'annotation:'+JSON.stringify([conversation]).slice(0,-1)+','}
function readerAnnotationSpan(kind,className,writingIndex){
  if(kind==='writing')return writingIndex;
  const classes=String(className).split(/\s+/),at=classes.find(name=>/^event-(?:target-)?at-\d+$/.test(name));
  return (classes.includes('event-context')?'context:':classes.includes('agent-event')?'sentence:':'target:')+at;
}
function readerAnnotationKey(conversation,message,kind,span){return 'annotation:'+JSON.stringify([conversation,message,kind,span])}
// A sentence can carry several event groups. Its hue and tooltip follow the
// likeliest remaining event when a group is hidden, without changing its text.
function readerEventHighlights(events,shown){
  return events.filter(event=>shown.has('events:'+eventGroupClass(event.group))).sort((a,b)=>(b.probability??0)-(a.probability??0));
}
function markdownText(text,mask=''){
  if(!/[^0]/.test(mask))return [text];
  const result=[];let start=0;
  while(start<text.length){const flag=mask[start]||'0';let end=start+1;while(end<text.length&&(mask[end]||'0')===flag)end++;
    const part=text.slice(start,end),className=markClass(flag);result.push(className?markdownElement('mark',className,part):part);start=end;
  }return result;
}

// Markdown
// Message prose is Markdown, parsed into a tree the page builds with text nodes
// and element properties only (markdownView in ui.py). An element is
// {tag, className, children} plus the properties the page sets on it (href,
// rel, target, start, and language for a code block's data-language); a
// string is text and {icon} one of the page's icons. Embedded HTML therefore
// stays text, and a link keeps its href only for the schemes below.
function markdownElement(tag,className='',content){return{tag,className,children:content===undefined||content===''?[]:[content]}}
function markdownLinkAllowed(href){
  if(!href||/[\x00-\x1f\x7f\\]/.test(href)||href.startsWith('//'))return false;
  if(/^[^/:]+\.[^/:]+:\d+(?::\d+)?(?:#.*)?$/.test(href))return true;
  if(/^[a-z][a-z0-9+.-]*:/i.test(href))return /^(https?:\/\/|mailto:|file:\/\/)/i.test(href);
  return !/[<>()[\]]/.test(href);
}
function markdownLinkKind(href){
  if(!markdownLinkAllowed(href))return null;
  if(href.startsWith('#'))return {kind:'fragment',icon:'link',detail:'Jump within this page'};
  if(href.startsWith('/')||href.startsWith('?')){
    const path=new URL(href,'http://pharos.invalid/').pathname;
    const pages=['/','/home','/cards','/library','/settings','/settings/backups','/settings/libraries','/settings/sources','/settings/sync','/settings/optimization','/settings/mcp','/settings/header','/settings/layout','/settings/preferences','/settings/projects','/settings/models','/settings/ask','/sources','/activity','/analyze','/tools','/health','/mcp','/findings'];
    if(pages.includes(path)||/^\/work\/[^/]+(?:\/conversation\/[^/]+)?$/.test(path)||/^\/conversation\/[^/]+$/.test(path))return {kind:'page',icon:'link',detail:'Open Pharos view'};
  }
  if(/^mailto:/i.test(href))return {kind:'mail',icon:'mail',detail:'Open in your email app'};
  if(/^https?:/i.test(href)){
    try{
      const url=new URL(href),host=url.hostname.toLowerCase();
      if(url.username||url.password)return null;
      const local=host==='localhost'||host.endsWith('.localhost')||host==='[::1]'||host==='0.0.0.0'||/^127(?:\.\d{1,3}){3}$/.test(host);
      return {kind:local?'server':'web',icon:local?'server':'web',host,detail:local?'Checking local server on this Mac…':`Website · ${host} (page availability not checked)`};
    }catch{return null}
  }
  return {kind:'file',icon:'file',detail:'Checking file on this Mac…'};
}
// Only local files and servers are checked on this Mac; Pharos routes,
// fragments, mail and websites never are. Repeated links share a check for a
// minute, and at most four run at once for long transcripts.
function markdownLinkProbed(kind){return kind?.kind==='file'||kind?.kind==='server'}
function markdownLinkChecker(check,{now=Date.now,concurrency=4,size=256,ttl=60000}={}){
  const checks=new Map(),queue=[];let active=0;
  const pump=()=>{
    while(active<concurrency&&queue.length){
      const {href,workspace,resolve}=queue.shift();active++;
      new Promise(done=>done(check(href,workspace))).then(resolve,()=>resolve({status:'unknown',detail:'Could not check availability on this Mac'})).finally(()=>{active--;pump()});
    }
  };
  return (href,workspace)=>{
    const key=JSON.stringify([href,workspace]),cached=checks.get(key);
    if(cached&&cached.until>now())return cached.promise;
    const promise=new Promise(resolve=>queue.push({href,workspace,resolve}));
    if(checks.size>=size)checks.delete(checks.keys().next().value);
    checks.set(key,{promise,until:now()+ttl});pump();return promise;
  };
}
function markdownInline(text,parent,depth=0,mask=''){
  if(depth>8){parent.children.push(...markdownText(text,mask));return}
  const tokens=/(\x60[^\x60\n]+\x60|!?\[[^\]\n]+\]\((?:<[^>\n]+>|[^\s)]+)(?:\s+"[^"]*")?\)|\*\*[^*\n]+\*\*|__[^_\n]+__|~~[^~\n]+~~|\*[^*\n]+\*|_[^_\n]+_)/g;
  let offset=0;
  for(const match of text.matchAll(tokens)){
    parent.children.push(...markdownText(text.slice(offset,match.index),mask.slice(offset,match.index)));const token=match[0],flags=mask.slice(match.index,match.index+token.length);let element;
    if(token.charCodeAt(0)===96){element=markdownElement('code');element.children=markdownText(token.slice(1,-1),flags.slice(1,-1))}
    else if(/^!?\[/.test(token)){
      const link=token.match(/^!?\[([^\]]+)\]\((?:<([^>]+)>|(\S+?))(?:\s+"[^"]*")?\)$/),href=link?.[2]||link?.[3]||'',kind=markdownLinkKind(href),label=link?.[1]||token,labelStart=token.startsWith('!')?2:1;
      element=markdownElement(kind?'a':'span',kind?'markdown-link':'');element.children=markdownText(label,flags.slice(labelStart,labelStart+label.length));
      if(kind){element.href=href;element.link=kind;element.rel='noreferrer noopener';if(['web','server','file'].includes(kind.kind))element.target='_blank'}
    }else{const double=token.startsWith('**')||token.startsWith('__')||token.startsWith('~~'),size=double?2:1;element=markdownElement(token.startsWith('~~')?'del':double?'strong':'em','');markdownInline(token.slice(size,-size),element,depth+1,flags.slice(size,-size))}
    parent.children.push(element);offset=match.index+token.length;
  }parent.children.push(...markdownText(text.slice(offset),mask.slice(offset)));
  parent.children=markdownMarkRuns(parent.children);
}
// A highlight that crosses inline code, emphasis or a link is one mark: an
// inline element whose text one mark covers entirely moves inside the mark
// (mark[code[...]] rather than code[mark[...]]), and neighbours with the same
// mark join it, so "hello `code` world" highlighted whole is one <mark>, not
// three pieces with the code's padding between them. Only an element the
// highlight starts or ends inside keeps its own mark.
function markdownMarkRuns(children){
  const covered=item=>{if(!item||typeof item==='string'||item.icon)return null;if(item.tag==='mark')return item.className;const only=item.children.length===1?item.children[0]:null;return only&&typeof only==='object'&&only.tag==='mark'?only.className:null};
  const result=[];
  for(const item of children){
    const className=covered(item);if(className===null){result.push(item);continue}
    const inner=item.tag==='mark'?item.children:[{...item,children:item.children[0].children}],last=result[result.length-1];
    // Marks in result are this function's own copies, so extending one is safe.
    if(last&&typeof last==='object'&&last.tag==='mark'&&last.className===className)last.children.push(...inner);
    else result.push({...markdownElement('mark',className),children:[...inner]});
  }
  return result;
}
function markdownTree(text,depth=0,mask=''){
  let source=String(text);
  if(source.includes('\r\n')){let at=0;mask=source.split('\r\n').map(part=>{const flags=mask.slice(at,at+part.length);at+=part.length+2;return flags}).join('0');source=source.replace(/\r\n/g,'\n')}
  const root=markdownElement('div','markdown-content'),lines=source.split('\n'),flags=[];let at=0;
  for(const line of lines){flags.push(mask.slice(at,at+line.length));at+=line.length+1}
  const inline=(tag,text,mask='')=>{const el=markdownElement(tag);markdownInline(text,el,0,mask);return el};
  if(depth>12){root.children=markdownText(source,mask);return root}
  let i=0;
  while(i<lines.length){
    const line=lines[i];if(!line.trim()){i++;continue}
    const fence=line.match(/^\s*(\x60{3,}|~{3,})(.*)$/);
    if(fence){const body=[],marks=[];i++;while(i<lines.length&&!lines[i].trim().startsWith(fence[1])){body.push(lines[i]);marks.push(flags[i++])}if(i<lines.length)i++;const pre=markdownElement('pre'),code=markdownElement('code');code.children=markdownText(body.join('\n'),marks.join('0'));if(fence[2].trim())code.language=fence[2].trim();pre.children.push(code);root.children.push(pre);continue}
    const heading=line.match(/^(#{1,6})\s+(.+)$/);if(heading){root.children.push(inline('h'+heading[1].length,heading[2],flags[i].slice(line.length-heading[2].length)));i++;continue}
    if(/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)){root.children.push(markdownElement('hr'));i++;continue}
    if(/^\s*>/.test(line)){const body=[],marks=[];while(i<lines.length&&/^\s*>/.test(lines[i])){const prefix=lines[i].match(/^\s*>\s?/)[0].length;body.push(lines[i].slice(prefix));marks.push(flags[i].slice(prefix));i++}const quote=markdownElement('blockquote');quote.children.push(markdownTree(body.join('\n'),depth+1,marks.join('0')));root.children.push(quote);continue}
    const list=line.match(/^(\s*)([-+*]|\d+[.)])\s+(.*)$/);
    if(list){const ordered=/^\d/.test(list[2]),indent=list[1].length,container=markdownElement(ordered?'ol':'ul');if(ordered)container.start=parseInt(list[2],10);
      while(i<lines.length){const item=lines[i].match(/^(\s*)([-+*]|\d+[.)])\s+(.*)$/);if(!item||item[1].length!==indent||/^\d/.test(item[2])!==ordered)break;const body=[item[3]],marks=[flags[i].slice(lines[i].length-item[3].length)],continuationIndent=indent+item[2].length+1;i++;
        while(i<lines.length&&lines[i].trim()&&/^\s+/.test(lines[i])&&lines[i].search(/\S/)>indent){body.push(lines[i].slice(continuationIndent));marks.push(flags[i].slice(continuationIndent));i++}
        const li=markdownElement('li'),task=body[0].match(/^\[([ xX])\]\s+(.*)$/);if(task){li.children.push(markdownElement('span','markdown-task',{icon:task[1].toLowerCase()==='x'?'todo':'checkbox'}));marks[0]=marks[0].slice(body[0].length-task[2].length);body[0]=task[2]}li.children.push(markdownTree(body.join('\n'),depth+1,marks.join('0')));container.children.push(li);
        if(!lines[i]?.trim()&&lines[i+1]?.match(/^(\s*)([-+*]|\d+[.)])\s+/)?.[1].length===indent)i++;
      }root.children.push(container);continue}
    if(line.includes('|')&&/^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(lines[i+1]||'')){
      const cells=(tag,index)=>{const row=markdownElement('tr'),value=lines[index];let offset=0;for(const cell of value.split('|')){const start=offset;offset+=cell.length+1;if(start===0&&!cell.trim()||offset>value.length&&!cell.trim())continue;const left=cell.length-cell.trimStart().length,text=cell.trim();row.children.push(inline(tag,text,flags[index].slice(start+left,start+left+text.length)))}return row};
      const wrap=markdownElement('div','markdown-table'),table=markdownElement('table'),head=markdownElement('thead');head.children.push(cells('th',i));table.children.push(head);const body=markdownElement('tbody');i+=2;while(i<lines.length&&lines[i].includes('|')&&lines[i].trim())body.children.push(cells('td',i++));table.children.push(body);wrap.children.push(table);root.children.push(wrap);continue}
    const paragraph=[line],marks=[flags[i]];i++;while(i<lines.length&&lines[i].trim()&&!/^\s*(?:#{1,6}\s|>|[-+*]\s|\d+[.)]\s|\x60{3,}|~{3,})/.test(lines[i])){if(lines[i].includes('|')&&/^\s*\|?\s*:?-{3,}/.test(lines[i+1]||''))break;paragraph.push(lines[i]);marks.push(flags[i++])}root.children.push(inline('p',paragraph.join('\n'),marks.join('0')));
  }return root;
}

// File chips' history links (conversationFileLinks, work_files.go): each
// call's files by the message that made the call and the path it named,
// matched exactly, else by the file's path in its project. A file the
// service did not link (a read outside a repository, a session not yet
// derived) opens the Files page by its path, which finds it if it can.
function fileLinkIndex(links){
  const byMessage=new Map();
  for(const link of Array.isArray(links)?links:[]){if(!link?.message_id||!link.url)continue;const list=byMessage.get(link.message_id)||[];list.push(link);byMessage.set(link.message_id,list)}
  return(messageId,path,callId='')=>{
    if(!path)return'';
    const list=(byMessage.get(messageId)||[]).filter(link=>!callId||!link.call_id||link.call_id===callId),clean=String(path).replace(/^\.\//,'');
    const found=list.find(link=>link.path===path)||list.find(link=>link.file&&(link.file===clean||clean.endsWith('/'+link.file)||String(link.path||'').endsWith('/'+clean)));
    return found?found.url:'/files?'+new URLSearchParams({file:path});
  };
}

// Exact source identities take precedence. Aggregated time rows resolve to the
// nearest retained event; equal distances prefer the earlier recorded event.
function transcriptAnchor(messages,{message='',at='',call=''}={}){
  if(message)return messages.find(item=>item.id===message&&(!call||item.call_id===call))?.id||null;
  const time=typeof at==='string'&&at?Date.parse(at):NaN;if(!Number.isFinite(time))return null;
  let best=null,distance=Infinity,bestTime=Infinity;
  for(const item of messages){const when=Date.parse(item.created_at),delta=Math.abs(when-time);if(!item.id||!Number.isFinite(when))continue;if(delta<distance||delta===distance&&when<bestTime){best=item.id;distance=delta;bestTime=when}}
  return best;
}

// Related work (peer_reader.go): top-level work linked to a workspace by
// generic evidence, such as a review that read an implementation's diff and
// whose findings were pasted back. A work page shows it as a strip of chips
// (dashed when uncertain), certain work's sessions in its hierarchy, and
// markers on the messages a hand-off is anchored on.
const PEER_ROLE_LABELS={reviewer:'Reviewer',implementer:'Implementer',related:'Related work'};
function peerRoleLabel(role){return PEER_ROLE_LABELS[role]||PEER_ROLE_LABELS.related}
function peerCount(value,one,many){const n=Number(value)||0;return`${n.toLocaleString()} ${n===1?one:many}`}
// One stored link as a sentence, from a page's point of view: names(id)
// says "this workspace", "it" (the peer) or another title. Links are
// directional: a read link's from side wrote the lines its to side saw; a
// copy link's from side wrote the reply its to side's prompt quoted.
function peerReasonText(reason,names=id=>id){
  const d=reason?.details||{},certain=reason?.confidence==='high'?'':' (uncertain)',from=names(reason?.from_workspace_id),to=names(reason?.to_workspace_id);
  const subject=name=>name.charAt(0).toUpperCase()+name.slice(1),possessive=name=>name==='it'?'its':`${name}’s`;
  if(reason?.kind==='read_written_by'){
    const timing=d.timing==='start'?`, starting as ${from} finished a turn`:d.timing==='return'?`; ${possessive(from)} next prompt followed`:'';
    return`${subject(to)} saw ${peerCount(d.lines,'line','lines')} ${from} wrote, in ${d.basis==='diff'?'a diff':'files it read'}${timing}${certain}`;
  }
  if(reason?.kind==='copied_into')return`A prompt in ${to} quoted ${peerCount(d.words,'word','words')} of a reply in ${from}${certain}`;
  if(reason?.kind==='tl1_candidate')return`Both are TL1 tasks on the same candidate${certain}`;
  return`Related${certain}`;
}
// A peer's chip. page names the workspace shown; titles names other work.
function peerChip(peer,{page='',titles={}}={},now=Date.now()){
  const certain=peer.confidence==='high';
  const names=id=>id===page?'this workspace':id===peer.workspace_id?'it':titles[id]||'other related work';
  const reasons=(peer.evidence||[]).map(reason=>peerReasonText(reason,names));
  if(peer.via)reasons.push(`Linked through ${titles[peer.via]||'other related work'}`);
  return{
    label:[peerRoleLabel(peer.role),peer.title||'Untitled work',peer.provider,relativeAge(peer.started_at,now)].filter(Boolean).join(' · '),
    href:'/work/'+encodeURIComponent(peer.workspace_id),certain,reasons,
    title:[certain?'Related work':'Possibly related work · uncertain',...reasons].join('\n'),
  };
}
// The strip's order: most recently started first, whatever the certainty
// (uncertain chips stay dashed); work with no start time goes last.
function peersByAge(peers){
  const started=peer=>{const time=new Date(peer?.started_at??NaN).getTime();return Number.isFinite(time)?time:-Infinity};
  return[...peers||[]].sort((a,b)=>started(b)-started(a)||0);
}
const HANDOFF_VERBS={'copied_into:in':'Quotes','copied_into:out':'Quoted in','read_written_by:in':'Shows lines from','read_written_by:out':'Read in'};
// The markers a message carries, one per linked message, certain first.
function handoffMarkers(message){
  const markers=new Map();
  for(const direction of['in','out'])for(const h of message?.['handoffs_'+direction]||[]){
    const verb=HANDOFF_VERBS[`${h.kind}:${direction}`];if(!verb||!h.message_id)continue;
    const key=`${direction}:${h.message_id}`,certain=h.confidence==='high',known=markers.get(key);
    if(known&&(known.certain||!certain))continue;
    const params=new URLSearchParams({message:h.message_id});
    markers.set(key,{label:`${verb} ${h.title||'Untitled work'}`,certain,
      workspace:h.workspace_id,conversation:h.conversation_id,message:h.message_id,
      href:`/work/${encodeURIComponent(h.workspace_id)}/conversation/${encodeURIComponent(h.conversation_id)}?${params}`,
      title:certain?'Hand-off between related work':'Possible hand-off between related work · uncertain'});
  }
  return[...markers.values()].sort((a,b)=>Number(b.certain)-Number(a.certain));
}
