// DOM-free interaction rules shared by the composer and its unit tests.
// Interleave subjects so each carousel page offers different ways into the
// archive. {scope} is optional; comparisons deliberately stay library-wide.
const askIdeaCatalog = [
  {topic:'prompting',text:'What instructions am I repeating{scope}?'},
  {topic:'errors',text:'Which errors recur across conversations{scope}?'},
  {topic:'usage',text:'Where did most tokens go this week{scope}?'},
  {topic:'memory',text:'Find past conversations that explain a design decision{scope}.'},
  {topic:'tools',text:'Which commands keep failing{scope}?'},
  {topic:'models',text:'How has my mix of models changed this month?'},
  {topic:'prompting',text:'What did I have to correct more than once{scope}?'},
  {topic:'context',text:'Which tool outputs fill the most context{scope}?'},
  {topic:'delegation',text:'How much token usage comes from subagents{scope}?'},
  {topic:'memory',text:'Find an earlier fix for a recurring test failure{scope}.'},
  {topic:'tools',text:'Which commands account for the most recorded tool time{scope}?'},
  {topic:'cost',text:'How does the API cost equivalent vary by model?'},
  {topic:'prompting',text:'What could I turn into reusable project instructions{scope}?'},
  {topic:'errors',text:'Which files have the most failed edits{scope}?'},
  {topic:'usage',text:'Which repositories use the most tokens?'},
  {topic:'memory',text:'What approaches have I already tried for flaky tests{scope}?'},
  {topic:'context',text:'Which tool outputs get truncated{scope}?'},
  {topic:'activity',text:'When do my agent sessions overlap the most?'},
  {topic:'prompting',text:'Where am I writing the most explanations{scope}?'},
  {topic:'skills',text:'Which skills have recorded invocations or loads{scope}?'},
  {topic:'usage',text:'How has my cache reuse changed this month?'},
  {topic:'memory',text:'Find conversations where I revisited the same decision{scope}.'},
  {topic:'errors',text:'Which test commands fail most often{scope}?'},
  {topic:'models',text:'Which models have the most recorded tool errors?'},
  {topic:'findings',text:'Which recurring problems have findings I could act on{scope}?'},
  {topic:'tools',text:'Which files do agents read repeatedly{scope}?'},
  {topic:'cost',text:'Which conversations have the highest API cost equivalent{scope}?'},
  {topic:'memory',text:'Find my longest conversations{scope}.'},
  {topic:'context',text:'Which conversations have the most compactions{scope}?'},
  {topic:'activity',text:'What changed in my work this week?'},
  {topic:'prompting',text:'Find examples where a clearer prompt helped me change course{scope}.'},
  {topic:'skills',text:'Which skill loads have recorded errors{scope}?'},
  {topic:'usage',text:'How do input and output tokens compare across models?'},
  {topic:'memory',text:'What have I already learned about slow builds{scope}?'},
  {topic:'tools',text:'Which searches am I repeating{scope}?'},
  {topic:'cost',text:'How has my estimated carbon footprint changed this month?'},
  {topic:'findings',text:'Which findings are being watched for improvement{scope}?'},
  {topic:'tools',text:'Which MCP methods do I use most{scope}?'},
  {topic:'activity',text:'How does my activity differ across machines?'},
  {topic:'memory',text:'Find work where I switched approaches after a failure{scope}.'},
  {topic:'tools',text:'Which documentation hosts do agents consult most{scope}?'},
  {topic:'usage',text:'Which days had unusually high token usage this month?'},
];
function askPromptSuggestions({repositories=[],variation=0}={}) {
  const names=[...new Set(repositories.filter(name=>typeof name==='string'&&name.trim()))];
  let eligible=0,scoped=0;
  return askIdeaCatalog.map(idea=>{
    // Keep a mixture of broad and scoped questions, even with many repos.
    const qualify=idea.text.includes('{scope}')&&eligible++%2===1&&names.length>1;
    const repository=qualify?names[(scoped++ + variation)%names.length]:'';
    return idea.text.replace('{scope}',qualify?' in the “'+repository+'” repository':'');
  });
}

async function askReadResponse(response, onProgress) {
  if (!response.ok || !response.headers.get('Content-Type')?.includes('application/x-ndjson')) {
    const result=await response.json();
    if(!response.ok)throw new Error(result.error||'Ask Pharos request failed');
    return result;
  }
  const reader=response.body.getReader(),decoder=new TextDecoder();
  let buffer='';
  try {
    while(true){
      const {value,done}=await reader.read();
      buffer+=decoder.decode(value,{stream:!done});
      if(buffer.length>8*1024*1024)throw new Error('Ask Pharos response is too large');
      let newline;
      while((newline=buffer.indexOf('\n'))>=0){
        const line=buffer.slice(0,newline);buffer=buffer.slice(newline+1);
        if(!line.trim())continue;
        const event=JSON.parse(line);
        if(event.type==='progress')onProgress?.(event.activity);
        if(event.type==='result'&&event.session)return event.session;
      }
      if(done)throw new Error('Ask Pharos connection ended before the answer arrived');
    }
  } finally { reader.releaseLock(); }
}
function askShowActivity({status,activity=[]}) {
  return status==='error'&&activity?.length>0;
}
function askWaitStatus({activity=[],elapsedMs=0,message='',cli=''}) {
  const latest=activity.at(-1),elapsed=Math.floor(elapsedMs/1000);
  const silent=latest?Math.max(0,Math.floor((elapsedMs-latest.elapsedMs)/1000)):elapsed;
  return {
    message:latest?.message||message||'Starting '+cli+'…',
    elapsed:elapsed+'s elapsed'+(silent>=10?' · '+silent+'s since last update':''),
  };
}
function askComposerActions({draft = '', turns = [], pending = false, opening = false, enabled = false, model = ''}) {
  const populated = turns.length > 0 || draft.length > 0;
  return {send: enabled && !!model && !pending && !opening && !!draft.trim(), stop: pending, history: !populated, clear: populated, modelLocked: turns.length > 0};
}
// Carry the visible conversation into a fresh, editable prompt for another model.
function askRetryDraft({turns = [], draft = ''}) {
  const messages=[];
  for(const turn of turns){
    const prompt='You:\n'+turn.prompt;
    const reply=[turn.reply,turn.clarification?.question,
      ...(turn.clarification?.options||[]).map(option=>'- '+option),
      ...(turn.cards||[]).map(card=>[card.title,card.description,card.href].filter(Boolean).join('\n')),
    ].filter(text=>text&&text.trim()).join('\n\n');
    const status=['error','canceled'].includes(turn.status)?' ('+turn.status+')':'';
    messages.push(prompt+(reply?'\n\nPharos'+status+':\n'+reply:''));
  }
  const unsent=draft.trim()?'You (unsent draft):\n'+draft:'';
  const full=[...messages,unsent].filter(Boolean).join('\n\n');
  // Match the server's prompt/draft limit, including labels and omission notices.
  const limit=12000,encoder=new TextEncoder(),bytes=text=>encoder.encode(text).length;
  if(bytes(full)<=limit)return full;
  const notice='[Earlier conversation omitted or shortened to fit.]\n\n';
  const shorten=(text,budget)=>{
    if(bytes(text)<=budget)return text;
    const suffix='\n[Message shortened.]',encoded=encoder.encode(text);
    let end=Math.max(0,budget-bytes(suffix));
    // Do not cut a multibyte code point or introduce a replacement character.
    while(end>0&&(encoded[end]&0xc0)===0x80)end--;
    return new TextDecoder().decode(encoded.subarray(0,end))+suffix;
  };
  let remaining=limit-bytes(notice);
  const kept=[];
  if(unsent){const text=shorten(unsent,remaining);kept.push(text);remaining-=bytes(text)+2;}
  for(let i=messages.length-1;i>=0;i--){
    const text=messages[i];
    if(bytes(text)>remaining){
      // Keep a useful part of the latest turn if even that turn is too large.
      if(i===messages.length-1&&remaining>bytes('\n[Message shortened.]'))kept.unshift(shorten(text,remaining));
      break;
    }
    kept.unshift(text);remaining-=bytes(text)+2;
  }
  return notice+kept.join('\n\n');
}
// A lone card opens itself, unless Pharos left out other destinations the
// agent offered with it: then the agent meant a choice.
function askShouldNavigate({cards = [], omitted = 0, draft = '', enabled, regular, edited, browsing = false}) {
  return enabled && !regular && !edited && !browsing && draft.length === 0 && cards.length === 1 && !omitted;
}
function askOmittedNote(omitted = 0) {
  if (!omitted) return '';
  return omitted === 1 ? 'One suggested view could not be opened, so it is left out.' : `${omitted} suggested views could not be opened, so they are left out.`;
}
function askChatURL(href, id) {
  const url = new URL(href);
  if (!['/', '/home'].includes(url.pathname)) return url.href;
  if (id) url.searchParams.set('ask_chat', id);
  else url.searchParams.delete('ask_chat');
  return url.href;
}
// History links must work independently of the page hosting the overlay.
function askHistoryURL(href, id) {
  const url = new URL(href);
  return askChatURL(['/', '/home'].includes(url.pathname) ? url.href : new URL('/', url).href, id);
}
// Search results are matched against complete chats on the server. Their
// summaries cannot be searched again without dropping later-message matches.
function askHistoryEntries({sessions = [], current = null, query = ''}) {
  const entries = query || !current ? sessions : [current, ...sessions.filter(session => session.id !== current.id)];
  return [...entries].sort((a, b) => b.updatedAt - a.updatedAt);
}
// Keep snapshots/replacements ahead of later removals, even on slow requests.
// A failed write must not prevent the next deletion or clear from running.
let askVisitWrites = Promise.resolve();
function askWriteVisit(write) {
  const result = askVisitWrites.then(write);
  askVisitWrites = result.catch(() => {});
  return result;
}
// Keep a little of the preceding reply for context, then the latest exchange.
// The viewport budget wins when a single exchange is itself very long.
function askThreadHeight({total, latest, previous = 0, gap = 0, available}) {
  const recent = latest + Math.min(previous, 64) + (previous > 0 ? gap : 0);
  return Math.max(0, Math.min(total, recent, available));
}
// Anchor the empty composer a quarter down, then share growth above and below
// its original center. Once an edge reaches the viewport, use the remaining space.
function askDialogLayout({viewportHeight,contentHeight,emptyHeight}) {
  const height=Math.max(0,Math.min(contentHeight,viewportHeight));
  const growth=Math.max(0,height-emptyHeight);
  return {height,top:Math.max(0,Math.min(viewportHeight-height,viewportHeight/4-growth/2))};
}
function askClarificationActions({latest = false, status, pending = false, enabled = false, opening = false, draft = ''}) {
  const active = latest && status === 'clarification' && enabled && !pending && !opening;
  return {active, sendOther: active && !!draft.trim()};
}

// Capture the complete view, including filters, without coupling it to chat routing.
function askPageURI(href) {
  const url = new URL(href);
  for (const key of [...url.searchParams.keys()]) if (key === 'ask_chat' || key.toLowerCase().includes('token')) url.searchParams.delete(key);
  return url.pathname + url.search + url.hash;
}
// A reload keeps this tab's surface. A different page or an explicit link to
// another chat takes precedence over the surface remembered by the tab.
function askRestorePresentation(href, saved) {
  if (!saved || saved.page !== askPageURI(href)) return null;
  const url = new URL(href), id = url.searchParams.get('ask_chat');
  if (id && id !== saved.id) return null;
  if (saved.presentation === 'home' && !['/', '/home'].includes(url.pathname)) return null;
  return ['home', 'modal', 'floating', 'dismissed'].includes(saved.presentation) ? saved.presentation : null;
}
function askShowBrowseHint({pending=false,browsing=false,presentation='home'}) {
  return pending&&!browsing&&['home','modal'].includes(presentation);
}
function askFloatingState(turn) {
  if (!turn) return {message: 'Ask a question', ready: false};
  if (turn.status === 'loading') return {message: askWaitStatus(turn).message, ready: false};
  if (turn.status === 'error') return {message: 'Question failed · Review or retry', ready: true};
  if (turn.status === 'canceled') return {message: 'Question canceled', ready: false};
  return {message: turn.status === 'clarification' ? 'Pharos has a question for you' : 'Your response is ready', ready: true};
}
function askFloatingPosition({x, y, width, height, viewportWidth, viewportHeight}) {
  return {x: Math.max(8, Math.min(x, viewportWidth - width - 8)), y: Math.max(8, Math.min(y, viewportHeight - height - 8))};
}
