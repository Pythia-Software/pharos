// The conversation reader's model, without the DOM: message bodies, normalized
// events, token accounting and context samples, tool calls and their results,
// turns, file paths, the Show filter's categories, Find, harness XML blocks,
// message summaries, and Markdown parsing.
// It is a classic script whose top-level declarations are globals: ui.py loads
// it before its inline script, which renders with them, and a shared export
// inlines it (sharedPage in share.go). Node tests load it with vm
// (web/tests/unit/reader). transcript_find.go and work_reader.go mirror parts
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
  const sum=(start,stream=null)=>reports.slice(start).reduce((total,r)=>total+(r&&(!stream||r.stream===stream)?r.total||0:0),0);
  messages.forEach((message,index)=>{
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
      if(!hasRecords){const previous=cumulative.get(stream)||0;total=running>=previous?running-previous:tokenTotal(info.last_token_usage);cumulative.set(stream,running);if(running<previous)label+=' (counter reset; last request only)'}
    }else if(type==='token_count'&&info.last_token_usage){usage=info.last_token_usage;total=hasRecords?null:tokenTotal(usage);label='Last request usage'}
    else if(value.subtype==='task_progress'||value.subtype==='task_notification'){
      scope='task';label='Subagent cumulative usage';const key=canonical(value.tool_use_id||value.task_id||raw.parent_tool_use_id||'');
      const report={total:tokenTotal(usage),compact,label,scope,stream:key||stream,usage,breakdown:tokenBreakdown(usage)};
      const prior=seen.get('task:'+key);if(prior)prior.total=null;
      if(key)seen.set('task:'+key,report);reports[index]=report;return;
    }else if(type==='result'||type==='turn.completed'||type==='cost-state'){
      scope=type==='cost-state'?'session':'run';label=scope==='session'?'Session accounting checkpoint':'Run accounting checkpoint';
      const reported=tokenTotal(usage),aggregate=modelTotal===null?reported:Math.max(reported??0,modelTotal);
      const start=runStarts.get(stream)||0,attributed=sum(start,stream==='main'?null:stream);
      total=aggregate===null?null:Math.max(0,aggregate-attributed);
      runStarts.set(stream,index+1);
      reports[index]={total,compact,label,scope,stream,usage,breakdown:tokenBreakdown(usage),reportedTotal:aggregate,attributed,start,models:value.modelUsage,conflict:aggregate!==null&&aggregate<attributed};return;
    }else total=tokenTotal(usage);
    const context=value.context_token_count??value.estimated_tokens??value.compact_metadata?.pre_tokens;
    if(total===null&&!compact&&running===null&&typeof context!=='number')return;
    const report={total,compact,trigger,label,scope,stream,usage,context,contextEstimated:value.estimated_tokens!==undefined,breakdown:tokenBreakdown(usage),reportedTotal:tokenTotal(usage),checkpoint:tokenTotal(value.thread_token_usage)};
    if(requestID&&scope==='request'){
      const key=stream+':'+requestID,prior=seen.get(key);
      if(prior){report.usage={...prior.usage,...usage};report.breakdown=tokenBreakdown(report.usage);report.total=report.breakdown?.total??null;prior.total=null;prior.superseded=true}
      seen.set(key,report);
    }
    reports[index]=report;
  });
  const requestTotals=new Map();
  for(const report of reports){if(report?.scope!=='request'||report.total===null||report.superseded)continue;const prior=requestTotals.get(report.stream)||0;if(report.checkpoint!==null&&report.checkpoint!==undefined){const gap=Math.max(0,report.checkpoint-prior-report.total);report.total+=gap;if(gap)report.label+=' + '+gap.toLocaleString()+' earlier unattributed tokens'}requestTotals.set(report.stream,prior+report.total)}
  // Reconcile subagent summaries with their retained request records. Keep
  // unallocated excess visible instead of assigning it to an arbitrary tool.
  const tasks=reports.filter(report=>report?.scope==='task'&&report.total!==null).sort((a,b)=>depth(b.stream)-depth(a.stream));
  for(const report of tasks){const attributed=reports.reduce((n,r)=>n+(r&&r!==report&&r.total!==null&&['request','task'].includes(r.scope)&&descends(r.stream,report.stream)?r.total||0:0),0);report.reportedTotal=report.total;report.total=Math.max(0,report.total-attributed);report.attributed=attributed}
  {const latest=new Map();for(const report of reports)if(report?.scope==='thread'&&recordStreams.has(report.stream))latest.set(report.stream,report);for(const report of latest.values()){report.total=Math.max(0,(report.reportedTotal||0)-(requestTotals.get(report.stream)||0));report.label='Thread checkpoint remainder beyond retained request records'}}
  reports.forEach((report,index)=>{
    if(!report)return;
    if(['run','session'].includes(report.scope)&&report.reportedTotal!==null){const attributed=reports.slice(report.start,index).reduce((n,r)=>n+(r&&(report.stream==='main'||r.stream===report.stream)?r.total||0:0),0);report.total=Math.max(0,report.reportedTotal-attributed);report.attributed=attributed;report.conflict=report.reportedTotal<attributed}
  });
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
    if(!report||report.superseded)return;
    const {value}=tokenEnvelope(messages[index]),stream=report.stream||'main';
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
    const base={...message,...(message.authorship?{authorship_text:message.authorship_text??message.text}:{}),native_id:message.native_id||`event-${index}`,raw_text:original};
    let emitted=false;
    const emit=(fields,suffix='')=>{const normalized={...base,...fields,tokenReport:emitted?null:reports[index],native_id:base.native_id+suffix};messageJSONCache.set(normalized,{...messageJSONCache.get(message)});output.push(normalized);emitted=true};
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
  let current=null;for(const message of top){if(message.role==='user'&&message.kind==='message'){if(current)turns.push(current);current={human:message,events:[]}}else{if(!current)current={human:null,events:[]};current.events.push(message)}}if(current)turns.push(current);
  return{childrenByParent,delegationResultsByCall,toolResultsByCall,consumed,turns};
}
// A compact outline for result cards, using the reader's normalized graph.
// Inline delegations stay under their owning turn; paired results are not turns.
function conversationOutline(conversation){
  const graph=conversationGraph(conversationMessages(conversation)),seen=new Set();
  const agents=events=>events.filter(message=>message.kind==='delegation'&&!seen.has(message)).map(message=>{
    seen.add(message);
    const {input}=toolPayload(message),children=[...new Set([message.native_id,message.call_id].filter(Boolean).flatMap(key=>graph.childrenByParent.get(key)||[]))],replies=children.filter(item=>item.kind==='message'),results=graph.delegationResultsByCall.get(message.call_id)||[];
    return {id:message.id||message.native_id||message.call_id,kind:'agent',label:String(input.subagent_type||input.agent_type||input.name||'Sub-agent'),
      prompt:String(input.description||input.prompt||input.task||messageSummary(message)||'Sub-agent'),
      human:{role:'user',text:input.prompt||input.description||input.task||messageSummary(message)},
      response:replies.at(-1)||(results.length?{role:'assistant',text:readableValue(toolPayload(results.at(-1)).content)}:null),tools:children.filter(item=>item.kind==='tool_call').length,children:agents(children)};
  });
  let turnNumber=0;
  return graph.turns.map((turn,index)=>({id:turn.human?.id||turn.human?.native_id||'setup-'+index,kind:'turn',label:turn.human?'Turn '+(++turnNumber):'Session setup',
    prompt:messageSummary(turn.human)||'Session setup',human:turn.human,response:turn.events.filter(message=>message.kind==='message').at(-1)||null,
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
// Antigravity names tool arguments in PascalCase; the reader looks for the conventional names.
const INPUT_ALIASES={CommandLine:'command',AbsolutePath:'file_path',TargetFile:'file_path',DirectoryPath:'path',SearchPath:'path',Url:'url',Query:'query',Prompt:'prompt'};
function conventionalInput(input){if(!Object.keys(INPUT_ALIASES).some(key=>key in input))return input;const result={};for(const [key,value] of Object.entries(input)){const alias=INPUT_ALIASES[key];result[alias&&!(alias in input)&&!(alias in result)?alias:key]=value}return result}
function toolCategory(message){const payload=toolPayload(message),name=payload.name.toLowerCase(),input=payload.input;if(message.kind==='error'||payload.raw?.is_error)return'error';if(message.kind==='reasoning')return'reasoning';if(message.kind==='metadata'||message.kind==='result')return'run';if(message.kind==='context'||message.kind==='attachment')return'context';if(message.kind.includes('delegation')||/^(task|agent)$|spawn_agent/.test(name))return'agent';if(/todo|task(create|update|delete|list|get)|update_plan/.test(name))return'todo';if(/patch|edit|write|replace/.test(name))return'edit';if(/exec|command|shell|terminal|bash/.test(name)||input.cmd||input.command)return'command';if(/grep|search|find|glob|query/.test(name))return'search';if(/browser|web|navigate|click/.test(name)||input.url)return'web';if(/read|open|view|list/.test(name))return'read';return'tool'}
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
function toolFailed(message,results){const category=toolCategory(message);return Boolean(category==='error'||toolPayload(message).raw?.is_error||results.some(item=>item.kind==='error'||toolPayload(item).raw?.is_error||Number(toolPayload(item).raw?.exit_code)>0||(category==='command'&&/^(?:Process exited with code|Exit code:)\s*[1-9]/im.test(readableValue(toolPayload(item).content)))))}

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

// Transcript filter and find
const TRANSCRIPT_CATEGORIES=[['prompts','Prompts','Messages you, or a parent agent, sent'],['response','Final responses','The last reply of each turn'],['replies','Interim replies','Agent messages before the final reply of a turn'],['thinking','Thinking','Reasoning the agent recorded'],['edits','File edits','Edits and writes'],['commands','Commands','Shell commands'],['reads','Reads & searches','File reads, listings, and searches'],['tools','Other tools','Browser, todo, MCP, and other tool calls'],['agents','Sub-agents','Delegated agent runs'],['events','System events','Injected context, attachments, run events, and errors'],['usage','Token usage','Token usage events']];
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
  const value=messageJSON(message);if((value?.payload||value)?.type==='token_usage')return'usage';
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
function transcriptFind(messages,{pattern,shown,output}){
  const structure=transcriptStructure(messages),kinds=new Set();let visible=0,hidden=0;
  for(const message of messages){if(structure.consumed.has(message)&&!output||!pattern.test(findableText(message)))continue;const missing=structure.pathOf(message).filter(key=>!shown.has(key));if(missing.length){hidden++;missing.forEach(key=>kinds.add(key))}else visible++}
  return{shown:visible,hidden,hidden_kinds:TRANSCRIPT_CATEGORIES.map(([key])=>key).filter(key=>kinds.has(key))};
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
function markdownText(text,mask=''){
  if(!mask.includes('1'))return [text];
  const result=[];let start=0;
  while(start<text.length){const typed=mask[start]==='1';let end=start+1;while(end<text.length&&(mask[end]==='1')===typed)end++;
    const part=text.slice(start,end);result.push(typed?markdownElement('mark','human-written',part):part);start=end;
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

// Exact source identities take precedence. Aggregated time rows resolve to the
// nearest retained event; equal distances prefer the earlier recorded event.
function transcriptAnchor(messages,{message='',at='',call=''}={}){
  if(message)return messages.find(item=>item.id===message&&(!call||item.call_id===call))?.id||null;
  const time=typeof at==='string'&&at?Date.parse(at):NaN;if(!Number.isFinite(time))return null;
  let best=null,distance=Infinity,bestTime=Infinity;
  for(const item of messages){const when=Date.parse(item.created_at),delta=Math.abs(when-time);if(!item.id||!Number.isFinite(when))continue;if(delta<distance||delta===distance&&when<bestTime){best=item.id;distance=delta;bestTime=when}}
  return best;
}
