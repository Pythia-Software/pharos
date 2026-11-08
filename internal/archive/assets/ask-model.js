// Interaction rules shared by the composer and its unit tests.
// The library chooses suggestions (GET /api/ask/suggestions,
// docs/ask-suggestions.md); keep only well-formed, distinct questions.
function askSuggestionItems(list) {
  const seen=new Set();
  return (Array.isArray(list)?list:[]).flatMap(item=>{
    const text=typeof item?.text==='string'?item.text.trim():'';
    if(typeof item?.id!=='string'||!text||seen.has(text))return [];
    seen.add(text);
    return [{id:item.id,text}];
  }).slice(0,12);
}

// The suggestion carousel shows one card on narrow screens and at least three
// from 600 px. Wider rows, such as full screen Ask, add cards while each stays
// at least 200 px wide. Past three, one question stays off the row, for the
// placeholder and for rotation to bring in. Peeks of the neighbouring cards
// sit on both sides; side is the list padding that puts the first card read
// right after the left peek when the viewport scrolls to the reading block's
// center slot times the stride.
// The row repeats the slate copies times and rests in the copy after lead
// copies, with room on each side for two full rotations and a peek: a timed
// glide plus a click during it never scrolls past the last card.
function askIdeaLayout(width, available) {
  const gap=14,minCard=200;
  if(!(width>0)||!(available>0))return {count:0,card:0,peek:0,stride:0,side:0,lead:0,copies:0};
  const fit=Math.floor((width-2*44+gap)/(minCard+gap));
  const count=Math.max(1,width<600?1:Math.min(available,Math.max(3,Math.min(fit,available-1))));
  const peek=count>1?44:28,card=(width-2*peek-(count-1)*gap)/count,stride=card+gap;
  const lead=Math.max(1,Math.ceil((Math.floor(count/2)+2*count+1)/available));
  const trail=Math.max(1,Math.ceil((Math.ceil(count/2)+2*count)/available));
  return {count,card,peek,stride,side:peek+Math.floor(count/2)*stride,lead,copies:lead+1+trail};
}

// While the first suggestions are still being chosen, ask again after 20 s,
// doubling up to every five minutes: a busy library can take longer than any
// fixed number of tries.
function askSuggestionRetryDelay(attempt) {
  return Math.min(300000,20000*2**Math.max(0,attempt));
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
// Elapsed time is measured from the agent's start, so it stays hidden until the
// first activity arrives rather than counting up and then snapping back.
function askWaitStatus({activity=[],elapsedMs=0,message='',cli=''}) {
  const latest=activity.at(-1),elapsed=Math.floor(elapsedMs/1000);
  const silent=latest?Math.max(0,Math.floor((elapsedMs-latest.elapsedMs)/1000)):0;
  return {
    message:latest?.message||message||'Starting '+cli+'…',
    elapsed:latest?elapsed+'s elapsed'+(silent>=10?' · '+silent+'s since last update':''):'',
  };
}

// Walk away from the solved board with legal moves, then reverse the walk.
// Avoid returning the same board twice, including with a deterministic RNG.
function askPuzzlePath(random=Math.random,previous=null) {
  const solved=[1,2,3,4,5,6,7,8,0],boards=[solved],same=(a,b)=>!!b&&a.every((tile,index)=>tile===b[index]);
  let board=solved,priorEmpty=-1;
  const nextBoards=(avoidUndo=true)=>{
    const empty=board.indexOf(0);
    return board.flatMap((tile,index)=>{
      if((avoidUndo&&index===priorEmpty)||Math.abs(index%3-empty%3)+Math.abs(Math.floor(index/3)-Math.floor(empty/3))!==1)return [];
      const next=board.slice();next[empty]=tile;next[index]=0;return [next];
    });
  };
  function advance(next){priorEmpty=board.indexOf(0);board=next;boards.push(next);}
  for(let i=0;i<20;i++){const options=nextBoards();advance(options[Math.floor(random()*options.length)]);}
  if(same(board,previous)||same(board,solved))advance(nextBoards(false).find(next=>!same(next,previous)&&!same(next,solved)));
  return boards.reverse();
}

// One compositor-driven transform animation per tile. JavaScript only runs at
// the end of a solve or reset, and cancels all motion when the turn is hidden.
function askPuzzleMotion(host, {random=Math.random, media=matchMedia('(prefers-reduced-motion: reduce)')}={}) {
  const tiles=[...host.children],step=640,pause=160,initial=1000,rest=2000,reset=400;
  let active=false,animations=[],previous=null;
  const position=(board,tile)=>{
    const index=board.indexOf(tile),home=tile-1;
    return `translate(${(index%3-home%3)*6}px,${(Math.floor(index/3)-Math.floor(home/3))*6}px)`;
  };
  function cancel(){animations.forEach(animation=>animation.cancel());animations=[];tiles.forEach(tile=>tile.style.removeProperty('transform'));}
  function animate(frames,duration,done){
    animations=tiles.map((tile,index)=>tile.animate(frames[index],{duration,fill:'forwards',easing:'linear'}));
    animations[0].onfinish=()=>{if(active&&!media.matches)done();};
  }
  function solve(path,delay){
    const moves=path.length-1,duration=delay+moves*step+(moves-1)*pause+rest;
    const frames=tiles.map((_,index)=>{
      const tile=index+1,first=position(path[0],tile),keyframes=[{transform:first,offset:0}];
      if(delay)keyframes.push({transform:first,offset:delay/duration});
      for(let i=1;i<path.length;i++){
        const start=delay+(i-1)*(step+pause);
        if(i>1)keyframes.push({transform:position(path[i-1],tile),offset:start/duration});
        keyframes.push({transform:position(path[i],tile),offset:(start+step)/duration});
      }
      keyframes.push({transform:position(path.at(-1),tile),offset:1});return keyframes;
    });
    animate(frames,duration,()=>{
      const next=askPuzzlePath(random,previous);previous=next[0];
      const resetFrames=tiles.map((_,index)=>[{transform:position(path.at(-1),index+1)},{transform:position(next[0],index+1)}]);
      cancel();animate(resetFrames,reset,()=>{cancel();solve(next,0);});
    });
  }
  function start(){const path=askPuzzlePath(random,previous);previous=path[0];solve(path,initial);}
  function setWorking(value){const next=!!value;if(next===active)return;active=next;cancel();if(active&&!media.matches)start();}
  function changed(){cancel();if(active&&!media.matches)start();}
  media.addEventListener('change',changed);
  return {setWorking,destroy(){active=false;cancel();media.removeEventListener('change',changed);}};
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
    const reply=[turn.reply,turn.feedback&&'Feedback draft ('+turn.feedback.kind+'):\n'+turn.feedback.comment,turn.clarification?.question,
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
// Chart previews stay in the answer for inspection. A lone ordinary link opens
// itself unless the agent offered a choice or the user has started browsing.
function askShouldNavigate({cards = [], omitted = 0, draft = '', enabled, regular, edited, browsing = false, hasPreview = false}) {
  return enabled && !regular && !edited && !browsing && !hasPreview && draft.length === 0 && cards.length === 1 && !omitted;
}
function askOmittedNote(omitted = 0) {
  if (!omitted) return '';
  return omitted === 1 ? 'One suggested view could not be opened, so it is left out.' : `${omitted} suggested views could not be opened, so they are left out.`;
}
// Home and the full-screen page show the chat inline and name it in ask_chat.
function askChatPage(pathname) { return ['/', '/home', '/ask'].includes(pathname); }
function askChatURL(href, id) {
  const url = new URL(href);
  if (!askChatPage(url.pathname)) return url.href;
  if (id) url.searchParams.set('ask_chat', id);
  else url.searchParams.delete('ask_chat');
  return url.href;
}
// History links must work independently of the page hosting the overlay.
function askHistoryURL(href, id) {
  const url = new URL(href);
  return askChatURL(askChatPage(url.pathname) ? url.href : new URL('/', url).href, id);
}
function askFullscreenURL(href, id) { return askChatURL(new URL('/ask', href).href, id); }
// Leaving full screen returns to the page it was opened from: Home shows the
// chat inline, and any other page reopens it in the dialog.
function askFullscreenExit(href, from, id) {
  let target = new URL('/', href);
  try { const url = new URL(from, href); if (typeof from === 'string' && url.origin === target.origin && url.pathname !== '/ask') target = url; } catch {}
  const home = ['/', '/home'].includes(target.pathname);
  return {href: home ? askChatURL(target.href, id) : target.href, presentation: home ? 'home' : 'modal'};
}
// Search results are matched against complete chats on the server. Their
// summaries cannot be searched again without dropping later-message matches.
function askHistoryEntries({sessions = [], current = null, query = ''}) {
  const entries = query || !current ? sessions : [current, ...sessions.filter(session => session.id !== current.id)];
  return [...entries].sort((a, b) => b.updatedAt - a.updatedAt);
}
// Each CLI and model a chat's turns used, most recent first. Server summaries
// carry the list; the open chat derives it from its turns, and a chat without
// a question yet names the model it will ask.
function askHistoryModels(session, labels = {}) {
  let uses = Array.isArray(session.models) ? session.models : [];
  if (!Array.isArray(session.models)) {
    const seen = new Set();
    for (const turn of [...(session.conversation || [])].reverse()) {
      const key = turn.cli + '\u0000' + turn.model;
      if (!turn.model || seen.has(key)) continue;
      seen.add(key); uses.push({cli: turn.cli, model: turn.model, label: turn.modelLabel});
    }
    if (!uses.length && session.selection?.model) uses = [{cli: session.selection.provider, model: session.selection.model}];
  }
  return uses.map(use => ({cli: use.cli || '', model: use.model || '', label: use.label || labels[use.cli]?.[use.model] || use.model || use.cli || ''}));
}
function askHistoryDay(timestamp) { const day = new Date(timestamp); day.setHours(0, 0, 0, 0); return day; }
// Recent chats group by day, older ones by month.
function askHistoryGroup(timestamp, now = Date.now()) {
  if (!timestamp) return 'Earlier';
  const days = Math.round((askHistoryDay(now) - askHistoryDay(timestamp)) / 86400000), date = new Date(timestamp);
  if (days <= 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return 'Previous 7 days';
  if (days < 30) return 'Previous 30 days';
  return date.toLocaleDateString(undefined, {month: 'long', ...(date.getFullYear() !== new Date(now).getFullYear() ? {year: 'numeric'} : {})});
}
function askHistoryStamp(timestamp, now = Date.now()) {
  if (!timestamp) return '';
  const date = new Date(timestamp), days = Math.round((askHistoryDay(now) - askHistoryDay(timestamp)) / 86400000);
  if (days <= 1) return date.toLocaleTimeString(undefined, {hour: 'numeric', minute: '2-digit'});
  if (days < 7) return date.toLocaleDateString(undefined, {weekday: 'short', month: 'short', day: 'numeric'});
  return date.toLocaleDateString(undefined, {month: 'short', day: 'numeric', ...(date.getFullYear() !== new Date(now).getFullYear() ? {year: 'numeric'} : {})});
}
function askHistoryFullDate(timestamp) {
  return new Date(timestamp).toLocaleString(undefined, {weekday: 'short', month: 'short', day: 'numeric', year: 'numeric', hour: 'numeric', minute: '2-digit'});
}
// One history row: title, turn count, models and when the chat was last active.
function askHistoryRow(session, {now = Date.now(), labels = {}} = {}) {
  const turns = session.questionCount ?? session.conversation?.length ?? 0, models = askHistoryModels(session, labels);
  const started = session.createdAt && session.createdAt !== session.updatedAt ? 'Started ' + askHistoryFullDate(session.createdAt) : '';
  return {
    title: session.conversation?.[0]?.prompt || session.draft || 'Untitled chat',
    turns: turns ? turns + (turns === 1 ? ' turn' : ' turns') : 'Draft',
    models,
    modelSummary: models.map(model => model.label).join(', '),
    group: askHistoryGroup(session.updatedAt, now),
    stamp: askHistoryStamp(session.updatedAt, now),
    when: [session.updatedAt ? (turns ? 'Last active ' : 'Edited ') + askHistoryFullDate(session.updatedAt) : '', started].filter(Boolean).join(' · '),
  };
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
  if (saved.presentation === 'page' && url.pathname !== '/ask') return null;
  return ['home', 'modal', 'floating', 'dismissed', 'page'].includes(saved.presentation) ? saved.presentation : null;
}
function askShowBrowseHint({pending=false,browsing=false,presentation='home',hint=true}) {
  return pending&&hint&&!browsing&&['home','modal','page'].includes(presentation);
}
// Dismissing the browse hint or choosing picture in picture skips the hint for
// the next questions, so only people who never browse keep seeing it.
function askBrowseHintSnooze() { return 5; }
function askStartBrowseHint(snoozed) {
  const left = Math.max(0, Math.floor(Number(snoozed) || 0));
  return {hint: left === 0, snoozed: Math.max(0, left - 1)};
}
function askFloatingState(turn, {navigated = false} = {}) {
  if (!turn) return {message: 'Ask a question', ready: false};
  // The answer is the page now on screen, so the panel invites a follow-up instead of calling for attention.
  if (navigated && turn.status === 'single') return {message: 'Opened · Ask a follow-up', ready: false};
  if (turn.status === 'loading') return {message: askWaitStatus(turn).message, ready: false};
  if (turn.status === 'error') return {message: 'Question failed · Review or retry', ready: true};
  if (turn.status === 'canceled') return {message: 'Question canceled', ready: false};
  return {message: turn.status === 'clarification' ? 'Pharos has a question for you' : 'Your response is ready', ready: true};
}
function askFloatingPosition({x, y, width, height, viewportWidth, viewportHeight}) {
  return {x: Math.max(8, Math.min(x, viewportWidth - width - 8)), y: Math.max(8, Math.min(y, viewportHeight - height - 8))};
}

// A provider's own default wins; otherwise its marked (or first) model.
function askDefaultSelection(provider, catalog) {
  const models = catalog?.models || [], model = models.find(m => m.default) || models[0];
  return catalog?.defaultSelection || {provider, model: model?.id || '', effort: 'Default'};
}
// A saved selection stays usable while its provider exists. Its model is not
// checked against the catalog here; the server validates what it runs.
function askValidSelection(value, providers = []) {
  return !!value && providers.some(p => p.id === value.provider) && typeof value.model === 'string';
}
function askSelectionProvider(saved, providers = []) {
  return providers.some(p => p.id === saved?.provider) ? saved.provider : providers[0]?.id;
}
function askEffortChoices(catalog, model) {
  return catalog?.models.find(m => m.id === model)?.efforts || ['Default'];
}
function askFastAvailable(catalog, model) {
  return !!catalog?.models.find(m => m.id === model)?.fast;
}
function askIsDefaultSelection(selection, defaultSelection) {
  return !!defaultSelection && ['provider', 'model', 'effort'].every(key => defaultSelection[key] === selection[key]) && !!defaultSelection.fast === !!selection.fast;
}
// A new provider starts from its default with Fast off (absent); a new model
// keeps the effort and Fast only if it offers them.
function askPickSelection(selection, page, value, catalogs = {}) {
  const next = {...selection, [page]: value};
  if (page === 'provider') {delete next.fast; Object.assign(next, askDefaultSelection(value, catalogs[value]));}
  if (page === 'model' && !askEffortChoices(catalogs[next.provider], next.model).includes(next.effort)) next.effort = 'Default';
  if (page === 'model' && !askFastAvailable(catalogs[next.provider], next.model) || page === 'fast' && !value) delete next.fast;
  return next;
}
// One visit updates or replaces an entry; without one, the whole browser
// history is sent so the private store can reconcile with it.
function askVisitInput({entry = null, replaced = null, stored = [], titleFor = null} = {}) {
  const visits = (entry ? [entry] : stored.map(visit => ({...visit, title: titleFor?.(visit.url, visit.page) || visit.title})))
    .map(visit => ({...visit, title: String(visit.title || 'Visited page').slice(0, 500)}));
  return {visits, replaces: replaced || null, reconcile: !entry};
}
// Only the latest chat load may apply; starting another or invalidating
// (a new chat, a route change) makes every earlier token stale.
function askLoadSequence() {
  let latest = 0;
  return {next: () => ++latest, invalidate() { latest++; }, current: token => token === latest};
}
// A chat the user opened takes the keyboard unless they moved focus while it loaded.
function askFocusAfterLoad({navigate = false, current = false, before = null, active = null, body = null}) {
  return navigate && current && (active === before || active === body);
}
