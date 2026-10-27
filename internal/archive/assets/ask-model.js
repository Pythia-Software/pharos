// DOM-free interaction rules shared by the composer and its unit tests.
function askComposerActions({draft = '', turns = [], pending = false, enabled = false, model = ''}) {
  const populated = turns.length > 0 || draft.length > 0;
  return {send: enabled && !!model && !pending && !!draft.trim(), history: !populated, clear: populated, modelLocked: turns.length > 0};
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
function askShouldNavigate({cards = [], draft = '', enabled, regular, edited}) {
  return enabled && !regular && !edited && draft.length === 0 && cards.length === 1;
}
function askChatURL(href, id) {
  const url = new URL(href);
  if (!['/', '/home'].includes(url.pathname)) return url.href;
  if (id) url.searchParams.set('ask_chat', id);
  else url.searchParams.delete('ask_chat');
  return url.href;
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
function askClarificationActions({latest = false, status, pending = false, enabled = false, opening = false, draft = ''}) {
  const active = latest && status === 'clarification' && enabled && !pending && !opening;
  return {active, sendOther: active && !!draft.trim()};
}
