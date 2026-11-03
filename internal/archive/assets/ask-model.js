// DOM-free interaction rules shared by the composer and its unit tests.
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
// Chart previews stay in the answer for inspection. A lone ordinary link opens
// itself unless the agent offered a choice or the user has started browsing.
function askShouldNavigate({cards = [], omitted = 0, draft = '', enabled, regular, edited, browsing = false, hasPreview = false}) {
  return enabled && !regular && !edited && !browsing && !hasPreview && draft.length === 0 && cards.length === 1 && !omitted;
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
function askShowBrowseHint({pending=false,browsing=false,presentation='home',hint=true}) {
  return pending&&hint&&!browsing&&['home','modal'].includes(presentation);
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
