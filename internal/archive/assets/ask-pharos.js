
window.initPharosAsk = function(root) {
  const product = root.querySelector('.pharos-product');
  const cleanup=[];
  const listen=(target,name,fn)=>{target.addEventListener(name,fn);cleanup.push(()=>target.removeEventListener(name,fn));};
  const find = selector => root.querySelector(selector);

  const ai = find('.pharos-ai');
  const regular = find('.pharos-regular');
  const invite = find('.pharos-invitation');
  const consent = find('.pharos-consent');

  let showInvitation = true, invitationDismissed = false;
  let loadingModels=false,providers=[], accessEnabled=false, disposed=false, draftTimer=null, writeQueue=Promise.resolve();
  const providerModels={},providerCatalog={};
  const report=error=>{searchNotice.hidden=false;searchNotice.textContent=error.message||String(error);};
  const api=async(path,body,signal)=>{const response=await fetch("/api/ask/"+path,{method:body?"POST":"GET",headers:{"Content-Type":"application/json"},...(body?{body:JSON.stringify(body)}:{}),signal});const result=await response.json();if(!response.ok)throw new Error(result.error||"Ask Pharos request failed");return result;};
  const modalReturns = new WeakMap();
  const searchNotice = find('.pharos-search-notice');
  const thread = find('.pharos-thread');
  const threadWrap=find('.pharos-thread-wrap'),threadViewport=find('.pharos-thread-viewport'),latestButton=find('.pharos-latest');
  let threadFollowing=true,threadLayoutFrame=null;
  const turnViews = new Map();
  let turns = [];
  let clarificationDraft='';
  const connectProvider = find('#pharos-connect-provider');
  const question = find('#pharos-question');
  const picker = find('#pharos-model-menu');
  const pickerTrigger = find('.pharos-model-trigger');
  const chatHistory = find('#pharos-chat-history'),historyTrigger=find('.pharos-history-trigger'),historyList=find('.pharos-history-list');
  const choices = {provider:'',model:'',effort:'Default'};
  const modelLabel = () => providerModels[choices.provider]?.[choices.model] || choices.model || 'Choose a model';
  const validSelection = value => value && providers.some(p=>p.id===value.provider) && typeof value.model==='string';
  const copySelection = value => ({provider:value.provider,model:value.model,effort:value.effort});
  let defaultSelection = null;
  async function loadModels(provider) {
    if(providerCatalog[provider]&&Date.now()-providerCatalog[provider].loadedAt<300000)return providerCatalog[provider];
    const result=await api('models?provider='+encodeURIComponent(provider));
    result.loadedAt=Date.now();providerCatalog[provider]=result;providerModels[provider]=Object.fromEntries(result.models.map(m=>[m.id,m.label]));return result;
  }
  const defaultFor=provider=>{const catalog=providerCatalog[provider],models=catalog?.models||[],model=models.find(m=>m.default)||models[0];return catalog?.defaultSelection||{provider,model:model?.id||'',effort:'Default'};};
  function effortChoices(){return providerCatalog[choices.provider]?.models.find(m=>m.id===choices.model)?.efforts||['Default'];}
  async function refreshStatus() {
    const status=await api('status');if(disposed)return;
    providers=status.providers;accessEnabled=status.settings.enabled;showInvitation=status.settings.showInvitation;
    mode=!providers.length?'missing':accessEnabled?'enabled':showInvitation?'available':'declined';
    connectProvider.replaceChildren(...providers.map(p=>{const option=document.createElement('option');option.value=option.textContent=p.id;return option;}));
    if(!accessEnabled||!providers.length){accessEnabled=false;stop();render();return;}
    if(status.settings.selection?.provider&&!providers.some(p=>p.id===status.settings.selection.provider)){regularOverride=true;Object.assign(choices,{provider:status.settings.selection.provider,model:'',effort:'Default'});stop();render();report(new Error(status.settings.selection.provider+' is unavailable. Choose another CLI in Settings → Ask Pharos.'));return;}
    loadingModels=true;render();
    const provider=providers.some(p=>p.id===status.settings.selection?.provider)?status.settings.selection.provider:providers[0].id;
    try {await loadModels(provider);defaultSelection=validSelection(status.settings.selection)?status.settings.selection:null;if(!turns.length&&!activeSessionId)Object.assign(choices,defaultSelection||defaultFor(provider));connectProvider.value=provider;}
    catch(error){report(error);regularOverride=true;}
    loadingModels=false;await loadChatHistory();render();
  }
  const ideas = [
    'Where am I wasting time?', 'What instructions am I repeating?', 'Which commands keep failing?',
    'Which projects use the most tokens?', 'What did I have to correct?', 'Where do agents get stuck?',
    'What changed in my work this week?', 'Which tools take the longest?', 'Find my longest conversations'
  ];
  const suggestions = find('.pharos-suggestions');
  const ideaViewport=find('.pharos-idea-viewport'),ideaRow=find('.pharos-suggestion-list');
  const defaultIdeaCorners=[[0],[2],[1,3],[3],[0,2],[1],[0,2],[1,3],[2]];
  let ideaCorners=defaultIdeaCorners.map(corners=>[...corners]);
  // Choose each ribbon once. Resize, carousel movement and theme changes keep it stable.
  let ideaRibbons=ideaCorners.map(corners=>{
    const phase=Math.floor(Math.random()*360);
    return corners.map((_,index)=>({phase:(phase+index*(110+Math.floor(Math.random()*140)))%360,roundness:Math.random(),reach:Math.random(),depth:Math.random()}));
  });
  let ideaOrder=ideas.map((_,index)=>index);
  let ideaPage = 0, ideaCenter=1,ideaPaused=matchMedia('(prefers-reduced-motion: reduce)').matches,ideaElapsed = 0, ideaLastTick = Date.now();
  let ideaCount=3,ideaStride=0,ideaSize='',ideaMoving=false,ideaScrollEnd=null,ideaMotion=null;
  const ideaCycleMs = 30000;
  let appearance = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  let pickerPage = null;

  const settings = { glow: 20, shimmer: true };
  let mode = 'missing';
  let regularOverride = false;
  let pending = null;
  let ticker = null,pollTimer=null;
  let serial = 0;
  let edgeSize = '';
  let frozenAt = null;
  let sessions=[],activeSessionId=null,activeCreatedAt=0,activeUpdatedAt=0,sessionLoad=0,openingSession=null;
  let historyQuery="",historyOffset=0,historyMore=false,historyLoad=0,historyTimer=null;
  let draftPending=null,cancelQueue=Promise.resolve();
  const activeKey='pharos-ask-active-'+(window.pharosPreviewScope||'local');
  function currentSession(){return {id:activeSessionId,createdAt:activeCreatedAt,updatedAt:activeUpdatedAt,conversation:turns,draft:question.value,clarificationDraft,selection:copySelection(choices)};}
  function sessionRecords(){return activeSessionId?[currentSession(),...sessions.filter(s=>s.id!==activeSessionId)]:sessions;}
  function writeHistory(){try{if(activeSessionId)sessionStorage.setItem(activeKey,activeSessionId);else sessionStorage.removeItem(activeKey);}catch{}}
  function syncChatURL(id, push=false){
    if(disposed)return;
    const href=askChatURL(location.href,id);
    if(href===location.href)return;
    history[push?'pushState':'replaceState'](history.state,'',href);
    window.dispatchEvent(new Event('pharos:uri-changed'));
  }
  function flushDraft(){clearTimeout(draftTimer);draftTimer=null;const payload=draftPending;draftPending=null;if(payload)writeQueue=writeQueue.catch(()=>{}).then(()=>api('session',payload)).catch(report);return writeQueue;}
  function saveChat(touch=true){
    if(disposed)return;
    if(turns.length||question.value.length){if(!activeSessionId){sessionLoad++;activeSessionId=crypto.randomUUID();activeCreatedAt=Date.now();syncChatURL(activeSessionId);}if(touch)activeUpdatedAt=Date.now();sessions=sessionRecords();}
    writeHistory();syncComposerActions();
    clearTimeout(draftTimer);
    if(!activeSessionId||!accessEnabled)return;
    draftPending={id:activeSessionId,draft:question.value,clarificationDraft,selection:copySelection(choices)};
    draftTimer=setTimeout(flushDraft,300);
  }
  function historyDate(timestamp) {
    if(!timestamp)return 'Earlier';
    const date=new Date(timestamp),today=new Date(),yesterday=new Date(today);yesterday.setDate(today.getDate()-1);
    return date.toDateString()===today.toDateString()?'Today':date.toDateString()===yesterday.toDateString()?'Yesterday':date.toLocaleDateString(undefined,{month:'short',day:'numeric',...(date.getFullYear()!==today.getFullYear()?{year:'numeric'}:{})});
  }
  function renderHistory() {
    const entries=askHistoryEntries({sessions,current:activeSessionId?currentSession():null,query:historyQuery});
    historyList.replaceChildren();find('.pharos-history-empty').hidden=entries.length>0;find('.pharos-history-more').hidden=!historyMore;
    for(const session of entries){
      const first=session.conversation[0],last=session.conversation.at(-1),title=first?.prompt||session.draft||'Untitled chat';
      const button=document.createElement('a');button.href=askChatURL(location.href,session.id);button.className='pharos-history-entry cursor-interaction';
      button.setAttribute('aria-label','Open chat: '+title);button.setAttribute('aria-current',String(session.id===activeSessionId));button.dataset.sessionId=session.id;
      const heading=document.createElement('span');heading.className='pharos-history-title';heading.textContent=title;heading.dataset.tooltip=title;
      const meta=document.createElement('span');meta.className='pharos-history-meta';
      const count=session.questionCount??session.conversation.length;
      meta.textContent=[historyDate(session.updatedAt),last?.cli??session.selection.provider,count?count+' question'+(count===1?'':'s'):'Draft',session.id===activeSessionId?'Current':null].filter(Boolean).join(' \u00b7 ');
      button.append(heading,meta);button.addEventListener('click',event=>{if(event.button||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;event.preventDefault();void openSession(session.id,true);});historyList.append(button);
    }
  }
  function closeHistory(focus=false) {
    chatHistory.hidden=true;ai.classList.remove('pharos-history-open');historyTrigger.setAttribute('aria-expanded','false');if(focus)historyTrigger.focus();
  }
  async function refreshHistory(more=false){
    const request=++historyLoad;
    try{const result=await api('sessions?q='+encodeURIComponent(historyQuery)+'&offset='+(more?historyOffset:0));if(request!==historyLoad)return;sessions=more?[...sessions,...result.sessions]:result.sessions;historyOffset=(more?historyOffset:0)+result.sessions.length;historyMore=result.sessions.length===100;if(!chatHistory.hidden){renderHistory();sizeHistory();}}catch(error){report(error);}
  }
  find('.pharos-history-filter').addEventListener('input',event=>{historyQuery=event.target.value;clearTimeout(historyTimer);historyTimer=setTimeout(()=>refreshHistory(),200);});
  find('.pharos-history-more').addEventListener('click',()=>refreshHistory(true));
  function openHistory() {
    if(historyTrigger.hidden||ai.hidden)return;
    closePicker();saveChat(false);renderHistory();chatHistory.hidden=false;historyTrigger.setAttribute('aria-expanded','true');sizeHistory();find('.pharos-history-close').focus();void refreshHistory();
  }
  function sizeHistory() {
    if(chatHistory.hidden)return;
    const anchor=find('.pharos-composer-actions').getBoundingClientRect(),bounds=product.getBoundingClientRect();
    product.style.setProperty('--pharos-history-width',Math.max(0,Math.min(340,anchor.right-bounds.left-16))+'px');
    const height=chatHistory.getBoundingClientRect().height,above=anchor.top>=height+12;
    chatHistory.dataset.placement=above?'above':'below';
    ai.classList.add('pharos-history-open');ai.style.setProperty('--pharos-history-space',above?'0px':height+8+'px');
  }
  function activateSession(session) {
    activeSessionId=session.id;activeCreatedAt=session.createdAt;activeUpdatedAt=session.updatedAt;
    turns=session.conversation.map(turn=>({...turn}));question.value=session.draft||'';clarificationDraft=session.clarificationDraft||'';Object.assign(choices,session.selection);writeHistory();
    threadFollowing=true;
    const latest=turns.at(-1);if(latest?.status==='loading')queueMicrotask(()=>{if(activeSessionId===session.id&&!pending)watchRequest(latest);});
  }
  async function loadSession(id){
    const session=await api('session?id='+encodeURIComponent(id));
    if(providers.some(p=>p.id===session.selection.provider))try{await loadModels(session.selection.provider);}catch(error){report(error);}
    return session;
  }
  async function openSession(id,navigate=false){
    const request=++sessionLoad;
    stop();saveChat(false);flushDraft();closeHistory();closePicker();
    const focused=document.activeElement;
    openingSession=request;syncComposer();
    try {
      await writeQueue;
      const session=await loadSession(id);
      if(disposed||request!==sessionLoad)return;
      activateSession(session);regularOverride=false;syncChatURL(id,navigate);renderThread();render();
    }catch(error){if(!disposed&&request===sessionLoad)report(error);}
    finally{if(openingSession===request){openingSession=null;syncComposer();if(navigate&&!disposed&&request===sessionLoad&&(document.activeElement===focused||document.activeElement===document.body))question.focus({preventScroll:true});}}
  }
  function clearChat(navigate=true){
    sessionLoad++;openingSession=null;
    stop();saveChat(false);flushDraft();closeHistory();closePicker();
    turns=[];activeSessionId=null;activeCreatedAt=0;activeUpdatedAt=0;question.value='';clarificationDraft='';threadFollowing=true;writeHistory();syncChatURL(null,!!navigate);renderThread();render();if(navigate)question.focus({preventScroll:true});
  }
  async function loadChatHistory(){
    const request=++sessionLoad;
    const data=await api('sessions');if(disposed||request!==sessionLoad)return;sessions=data.sessions;
    if(turns.length||question.value.length)return;
    const id=new URL(location.href).searchParams.get('ask_chat')||sessionStorage.getItem(activeKey);
    if(id){
      openingSession=request;syncComposer();
      try{const session=await loadSession(id);if(disposed||request!==sessionLoad||turns.length||question.value.length)return;activateSession(session);syncChatURL(id);renderThread();}
      catch(error){if(!disposed&&request===sessionLoad)report(error);}
      finally{if(openingSession===request){openingSession=null;syncComposer();}}
    }
  }
  function render() {
    const enabled = mode === 'enabled' && !regularOverride;
    ai.hidden = !enabled;
    suggestions.hidden = !enabled || turns.length>0;
    regular.hidden = enabled;
    invite.hidden = mode !== 'available' || !showInvitation || invitationDismissed || consent.open;
    find('.pharos-ai-mode').hidden = mode !== 'enabled';
    find('.pharos-current-settings').textContent = loadingModels?'Loading available models…':choices.provider + ' \u00b7 ' + modelLabel() + ' \u00b7 ' + choices.effort;
    find('.pharos-selected-provider').textContent = choices.provider;
    find('.pharos-selected-model').textContent = modelLabel();
    find('.pharos-selected-effort').textContent = choices.effort;
    const isDefault = defaultSelection && ['provider','model','effort'].every(key=>defaultSelection[key]===choices[key]);
    const defaultButton = find('.pharos-save-default');
    defaultButton.setAttribute('aria-pressed',String(!!isDefault));
    defaultButton.setAttribute('aria-label',isDefault?'This selection is the default':'Make this selection the default');
    defaultButton.dataset.tooltip = isDefault?'Default CLI, model and effort for new questions':'Save this CLI, model and effort as your default';
    defaultButton.querySelector('span').textContent = isDefault?'\u2605':'\u2606';
    product.style.setProperty('--pharos-edge-opacity', String(settings.glow / 25));

    ai.classList.toggle('pharos-motion', settings.shimmer);

    syncComposer();
    if (enabled) autoFit();
    drawEdge();
    renderIdeas();
    sizePicker();
    sizeHistory();
    sizeModal();
  }
  function persist(){writeHistory();}
  async function saveAccessPreference(consent=false){
    try{await api('settings',{enabled:mode==='enabled',showInvitation,consent});accessEnabled=mode==='enabled';window.dispatchEvent(new CustomEvent('pharos:ask-config',{detail:root}));}catch(error){report(error);await refreshStatus();}
  }
  function sizeModal() {
    const dialog=consent.open?consent:null;
    product.classList.toggle('pharos-modal-open',!!dialog);

  }
  function closeModals() {
    for(const dialog of [consent])if(dialog.open)dialog.close();
    sizeModal();
  }
  function openModal(dialog,returnFocus=document.activeElement) {
    closePicker();closeHistory();closeModals();modalReturns.set(dialog,returnFocus);

    dialog.showModal();render();
  }
  function closeModal(dialog,focus=true) {
    if(dialog.open)dialog.close();render();
    const trigger=modalReturns.get(dialog);if(focus&&trigger?.isConnected&&!trigger.hidden)trigger.focus();
  }
  function deferConsent() {
    invitationDismissed=true;closeModal(consent,false);render();persist();find('.home-search-trigger')?.focus();
  }
  function autoFit() {
    question.style.height = 'auto';
    const limit=Math.max(68,Math.round(window.innerHeight*.28));
    question.style.height = Math.min(limit,Math.max(68,question.scrollHeight)) + 'px';
    question.style.overflowY=question.scrollHeight>limit?'auto':'hidden';
    scheduleThreadLayout();
  }
  function syncThreadScroll(){
    const away=threadViewport.scrollHeight-threadViewport.clientHeight-threadViewport.scrollTop>24;
    latestButton.hidden=!away;
    threadWrap.classList.toggle('pharos-earlier',threadViewport.scrollTop>2);
  }
  function scheduleThreadLayout(){
    if(disposed||threadLayoutFrame!==null)return;
    threadLayoutFrame=requestAnimationFrame(()=>{threadLayoutFrame=null;sizeThread();});
  }
  function sizeThread(){
    if(threadWrap.hidden||ai.hidden||!threadViewport.clientWidth||!turns.length)return;
    const lastView=turnViews.get(turns.at(-1).id);lastView?.fitOther?.();
    const last=lastView?.element,previous=turnViews.get(turns.at(-2)?.id)?.get('.pharos-agent-message');
    if(!last)return;
    const content=thread.getBoundingClientRect(),end=last.getBoundingClientRect();
    const padding=thread.scrollHeight-(end.bottom-content.top);
    const gap=previous?end.top-previous.getBoundingClientRect().bottom:0;
    const frame=find('.pharos-ai-frame'),main=root.closest('main');
    const chrome=frame.getBoundingClientRect().height-threadViewport.getBoundingClientRect().height;
    // Measure from the composer's position in the unscrolled page so scrolling
    // the page itself never changes the transcript's size.
    const top=frame.getBoundingClientRect().top+(main?.scrollTop||0);
    const available=Math.max(96,window.innerHeight-Math.max(80,top)-chrome-20);
    const height=askThreadHeight({total:thread.scrollHeight,latest:end.height+padding,previous:previous?.getBoundingClientRect().height||0,gap,available});
    const scrollTop=threadViewport.scrollTop;
    threadViewport.style.maxHeight=available+'px';
    threadViewport.style.height=height+'px';
    threadViewport.scrollTop=threadFollowing?threadViewport.scrollHeight:scrollTop;
    syncThreadScroll();drawEdge();
  }
  threadViewport.addEventListener('scroll',()=>{
    threadFollowing=threadViewport.scrollHeight-threadViewport.clientHeight-threadViewport.scrollTop<=24;
    syncThreadScroll();
  });
  latestButton.addEventListener('click',()=>{threadFollowing=true;scheduleThreadLayout();});
  const threadObserver=new ResizeObserver(scheduleThreadLayout);
  threadObserver.observe(thread);threadObserver.observe(question);threadObserver.observe(find('.pharos-composer-footer'));
  function syncComposerActions() {
    const actions=askComposerActions({draft:question.value,turns,pending:!!pending||openingSession!==null,enabled:accessEnabled,model:choices.model});
    find('.pharos-send').disabled=!actions.send;
    historyTrigger.hidden=!actions.history;
    find('.pharos-history-anchor').hidden=!actions.history;
    find('.pharos-clear-chat').hidden=!actions.clear;
    if(!actions.history&&!chatHistory.hidden)closeHistory();
    turns.forEach(syncClarification);
  }
  function syncComposer() {
    const hasThread=turns.length>0;
    thread.hidden=!hasThread;
    threadWrap.hidden=!hasThread;
    ai.classList.toggle('pharos-has-thread',hasThread);
    suggestions.hidden=ai.hidden||hasThread;
    question.disabled=openingSession!==null;
    question.placeholder=openingSession!==null?'Loading conversation…':hasThread?'Ask a follow-up or refine your question...':'What would you like to find in your work?';
    syncComposerActions();
    ai.classList.toggle('pharos-busy',!!pending);
    pickerTrigger.disabled=hasThread||loadingModels||openingSession!==null;
    pickerTrigger.dataset.tooltip=hasThread?'Model selection is fixed for this chat. Clear to start with another model.':'Choose CLI, model, and effort';
    if(hasThread&&!picker.hidden)closePicker();
    // Only the pending reply is busy, so new user messages remain readable.
    ai.removeAttribute('aria-busy');
    scheduleThreadLayout();
  }
  function appendTurn(turn) {
    const element=find('#pharos-turn-template').content.firstElementChild.cloneNode(true);
    element.dataset.turnId=turn.id;
    const get=selector=>element.querySelector(selector);
    get('.pharos-user-question').textContent=turn.prompt;
    get('.pharos-agent-name').textContent=turn.cli;
    const avatar=get('.pharos-agent-avatar');avatar.dataset.provider=turn.cli;
    avatar.append(find('#pharos-lighthouse-template').content.firstElementChild.cloneNode(true));
    const badge=document.createElement('span');badge.className='pharos-agent-badge';
    badge.append(find('#pharos-logo-'+turn.cli.toLowerCase()).content.firstElementChild.cloneNode(true));avatar.append(badge);
    avatar.setAttribute('aria-label','Pharos with '+turn.cli);avatar.removeAttribute('aria-hidden');avatar.setAttribute('role','img');
    const view={element,get};turnViews.set(turn.id,view);thread.append(element);
    get('.pharos-stop').addEventListener('click',()=>stop());
    get('.pharos-retry').addEventListener('click',()=>startRequest(turn));
    get('.pharos-retry-model').addEventListener('click',retryWithDifferentModel);
    get('.pharos-recovery-search').addEventListener('click',useRegular);
    get('.pharos-recovery-clear').addEventListener('click',()=>clearChat());
    element.querySelectorAll('.pharos-destination').forEach(link=>link.addEventListener('click',event=>{
      event.preventDefault();stop();window.pharosNavigate?.(link.getAttribute('href'));
    }));
    updateTurn(turn);
  }
  function updateTurn(turn) {
    const view=turnViews.get(turn.id);if(!view)return;
    const {element,get}=view,loading=turn.status==='loading';
    element.dataset.status=turn.status;
    get('.pharos-agent-message').setAttribute('aria-busy',String(loading));
    get('.pharos-waiting').hidden=!loading;
    get('.pharos-wait-message').textContent=turn.message;
    get('.pharos-elapsed').textContent=Math.floor(turn.elapsedMs/1000)+'s';
    const response=answerFor(turn),hasResults=response.cards.length>0&&['multiple','single'].includes(turn.status);
    get('.pharos-destinations').hidden=!hasResults;
    get('.pharos-results-label').hidden=!hasResults;get('.pharos-results-label').textContent=turn.reply;
    element.querySelectorAll('.pharos-destination').forEach((link,index)=>{
      const card=response.cards[index];link.hidden=!hasResults||!card;
      if(!card)return;
      link.dataset.destination=card.title;link.href=card.href;
      link.querySelector('.pharos-destination-kind').textContent=card.kind;
      link.querySelector('strong').textContent=card.title;
      link.querySelector('span:last-of-type').textContent=card.description;
    });
    get('.pharos-answer').hidden=!turn.reply||hasResults;get('.pharos-answer').textContent=turn.reply;
    get('.pharos-recovery').hidden=!(turn===turns.at(-1)&&['error','canceled'].includes(turn.status));
    scheduleThreadLayout();drawEdge();
    syncClarification(turn);
  }
  function syncClarification(turn){
    const view=turnViews.get(turn.id);if(!view)return;
    const section=view.get('.pharos-clarification'),clarification=turn.clarification;
    section.hidden=!clarification||turn.status!=='clarification';
    if(section.hidden)return;
    const key=JSON.stringify(clarification);
    if(view.clarificationKey!==key){
      view.clarificationKey=key;
      view.get('.pharos-clarification-question').textContent=clarification.question;
      const options=view.get('.pharos-clarification-options');options.replaceChildren();
      view.clarificationButtons=clarification.options.map((text,index)=>{
        const button=document.createElement('button');button.type='button';button.className='pharos-clarification-choice cursor-interaction';
        const letter=document.createElement('span');letter.className='pharos-option-letter';letter.textContent=String.fromCharCode(65+index);
        const label=document.createElement('span');label.textContent=text;button.append(letter,label);
        button.setAttribute('aria-label',letter.textContent+'. '+text);
        button.addEventListener('click',()=>answerClarification(turn,text));options.append(button);return button;
      });
      const other=view.get('.pharos-clarification-other'),input=other.querySelector('textarea'),send=other.querySelector('button'),label=other.querySelector('label');
      label.querySelector('.pharos-option-letter').textContent=String.fromCharCode(65+clarification.options.length);
      input.id='pharos-other-'+turn.id;label.htmlFor=input.id;
      input.value=turn===turns.at(-1)?clarificationDraft:'';
      send.replaceChildren(find('.pharos-send svg').cloneNode(true));
      view.otherInput=input;view.otherSend=send;
      const fit=()=>{if(other.hidden)return;const before=input.style.height;input.style.height='auto';input.style.height=Math.min(110,Math.max(22,input.scrollHeight))+'px';if(before!==input.style.height)scheduleThreadLayout();};
      view.fitOther=fit;
      input.addEventListener('input',()=>{clarificationDraft=input.value;fit();saveChat();});
      input.addEventListener('keydown',event=>{if(event.key==='Enter'&&!event.shiftKey&&!event.isComposing){event.preventDefault();answerClarification(turn,input.value);}});
      send.addEventListener('click',()=>answerClarification(turn,input.value));
      fit();
    }
    const latest=turn===turns.at(-1);
    const actions=askClarificationActions({latest,status:turn.status,pending:!!pending,enabled:accessEnabled,opening:openingSession!==null,draft:view.otherInput.value});
    const following=turns[turns.indexOf(turn)+1];
    view.clarificationButtons.forEach((button,index)=>{
      button.disabled=!actions.active;button.setAttribute('aria-pressed',String(following?.prompt===clarification.options[index]));
    });
    view.get('.pharos-clarification-other').hidden=!latest;
    view.otherInput.disabled=!actions.active;view.otherSend.disabled=!actions.sendOther;
  }
  function answerClarification(turn,text){
    const actions=askClarificationActions({latest:turn===turns.at(-1),status:turn.status,pending:!!pending,enabled:accessEnabled,opening:openingSession!==null,draft:text});
    if(actions.sendOther){submitQuestion(text,true);question.focus({preventScroll:true});}
  }
  function renderThread(){thread.replaceChildren();turnViews.clear();turns.forEach(appendTurn);syncComposer();}
  function answerFor(turn){return {intro:'',cards:turn.cards||[]};}
  function wrapRegions(art,key,w,h,r,contour) {
    const ns = 'http://www.w3.org/2000/svg';
    const add = (tag,attrs,parent) => {
      const element=document.createElementNS(ns,tag);
      Object.entries(attrs).forEach(([name,value])=>element.setAttribute(name,value));parent.append(element);return element;
    };
    let defs=art.querySelector('defs');
    if(!defs){defs=document.createElementNS(ns,'defs');art.prepend(defs);}
    const boxId=key+'-box',contourId=key+'-contour',outsideId=key+'-outside';
    if(!find('#'+boxId)){
      add('path',{fill:'white',stroke:'none'},add('clipPath',{id:boxId,clipPathUnits:'userSpaceOnUse'},defs));
      add('path',{fill:'white',stroke:'none'},add('clipPath',{id:contourId,clipPathUnits:'userSpaceOnUse'},defs));
      const mask=add('mask',{id:outsideId,maskUnits:'userSpaceOnUse',maskContentUnits:'userSpaceOnUse',style:'mask-type:luminance'},defs);
      add('rect',{fill:'white',stroke:'none'},mask);
      add('path',{fill:'black',stroke:'none'},add('g',{'clip-path':'url(#'+boxId+')'},mask));
    }
    const box=`M ${1+r} 1 H ${w-1-r} Q ${w-1} 1 ${w-1} ${1+r} V ${h-1-r} Q ${w-1} ${h-1} ${w-1-r} ${h-1} H ${1+r} Q 1 ${h-1} 1 ${h-1-r} V ${1+r} Q 1 1 ${1+r} 1 Z`;
    find('#'+boxId+' path').setAttribute('d',box);
    find('#'+contourId+' path').setAttribute('d',contour);
    const outside=find('#'+outsideId);
    Object.entries({x:-16,y:-16,width:w+32,height:h+32}).forEach(([name,value])=>{
      outside.setAttribute(name,value);outside.querySelector('rect').setAttribute(name,value);
    });
    outside.querySelector('path').setAttribute('d',contour);
    return {box,boxClip:'url(#'+boxId+')',contourClip:'url(#'+contourId+')',outsideMask:'url(#'+outsideId+')'};
  }
  function drawEdge() {
    const frame = find('.pharos-ai-frame');
    const {width:w,height:h} = frame.getBoundingClientRect();
    if (!w || !h || edgeSize === w + ':' + h) return;
    edgeSize = w + ':' + h;
    const viewBox = [-16,-16,w+32,h+32].join(' ');
    find('.pharos-edge-art').setAttribute('viewBox',viewBox);
    find('.pharos-ai-surface').setAttribute('viewBox',viewBox);
    const hairline = find('.pharos-edge-hairline');
    const contour = (i,opposite=false) => {
      const p = (x,y) => opposite ? `${w-x} ${h-y}` : `${x} ${y}`;
      const cornerX=Math.min(27,w*.12),cornerY=Math.min(27,h*.22),edgeX=-1.5-i*.65,topY=-1.6-i*1.1;
      const arc=.5522847498,rx=cornerX-edgeX,ry=cornerY-topY;
      // A local quarter ellipse keeps a round corner at any conversation height.
      // Horizontal and vertical handles meet the long edges without a kink.
      return `M ${p(w*.68,1.2+i*.15)}
        C ${p(w*.5,.8-i*.65)}, ${p(w*.32,topY)}, ${p(cornerX,topY)}
        C ${p(cornerX-arc*rx,topY)}, ${p(edgeX,cornerY-arc*ry)}, ${p(edgeX,cornerY)}
        C ${p(edgeX,Math.min(cornerY+24,h*.48))}, ${p(1-i*.15,h*.48)}, ${p(1-i*.15,h*.7)}`;
    };
    // A small phase shift for each adjacent strand, retaining the same palette.
    const defs = find('.pharos-edge-art defs');
    ['cool','warm'].forEach(kind => product.querySelectorAll('.pharos-edge-'+kind+' path').forEach((path,i) => {
      const id = 'pharos-strand-'+kind+'-'+i;
      let gradient = find('#'+id);
      if (!gradient) {
        gradient = document.createElementNS('http://www.w3.org/2000/svg','linearGradient');
        gradient.id = id; gradient.setAttribute('href','#pharos-edge-'+kind);
        gradient.setAttribute('gradientTransform',`translate(${i/36} ${-i/36})`);
        defs.append(gradient);
      }
      path.setAttribute('stroke','url(#'+id+')');
      path.setAttribute('d',contour(i+(kind==='warm'?.35:0)));
    }));
    find('.pharos-edge-warm').setAttribute('transform',`translate(${w} ${h}) rotate(180)`);
    find('[data-contour="cool"]').setAttribute('d',contour(1.5));
    const warmHalo = find('[data-contour="warm"]');
    warmHalo.setAttribute('d',contour(1.5));
    warmHalo.setAttribute('transform',`translate(${w} ${h}) rotate(180)`);
    // The field is the intersection of the ordinary box and ribbon interior.
    // Its border continues to that intersection; decoration is masked outside it.
    const surface = `${contour(0)} V ${h-18} Q 1 ${h-1} 18 ${h-1}
      ${contour(.35,true).replace(/^M/,'L')} V 18 Q ${w-1} 1 ${w-18} 1 Z`;
    const regions=wrapRegions(find('.pharos-edge-art'),'pharos-composer',w,h,17,surface);
    const fill=find('.pharos-ai-surface path');fill.setAttribute('d',surface);fill.setAttribute('clip-path',regions.boxClip);
    hairline.setAttribute('d',regions.box);hairline.setAttribute('clip-path',regions.contourClip);
    product.querySelectorAll('.pharos-edge-halo,.pharos-edge-lines,.pharos-edge-trace').forEach(element=>element.setAttribute('mask',regions.outsideMask));
    product.querySelectorAll('.pharos-edge-trace').forEach(path => path.setAttribute('d',surface));
  }
  function drawIdeas() {
    suggestions.querySelectorAll('.pharos-suggestion').forEach((button,index) => {
      const {width:w,height:h}=button.getBoundingClientRect();
      if (!w || !h) return;
      const art = button.querySelector('svg');
      art.setAttribute('viewBox',[-6,-6,w+12,h+12].join(' '));
      const id=Number(button.dataset.idea),corners=ideaCorners[id],styles=ideaRibbons[id];
      const orient=(kind,x,y)=>[kind===1||kind===2?w-x:x,kind===2||kind===3?h-y:y];
      // The shared fill, border and strand mask use the same contour at every corner.
      // Every join has a horizontal or vertical tangent, including both ends of the arc.
      const corner=(kind,offset=0)=>{
        const p=(x,y)=>orient(kind,x,y).join(' ');
        if(!corners.includes(kind))return kind===1||kind===3?{start:p(1,12),path:`Q ${p(1,1)} ${p(12,1)}`}:{start:p(12,1),path:`Q ${p(1,1)} ${p(1,12)}`};
        const style=styles[corners.indexOf(kind)],arc=.5522847498;
        const cornerX=Math.min(20+style.roundness*10,w*.22),cornerY=Math.min(18+(1-style.roundness)*10,h*.28);
        const reachX=Math.min(w*.46,cornerX+24+style.reach*20),reachY=Math.min(h*.46,cornerY+16+style.reach*12);
        const topY=-1.1-style.depth*1.2-offset,edgeX=-.8-style.depth*.9-offset;
        const rx=cornerX-edgeX,ry=cornerY-topY;
        const points=[
          [reachX,1], [reachX-(reachX-cornerX)*.4,1], [cornerX+(reachX-cornerX)*.4,topY], [cornerX,topY],
          [cornerX-arc*rx,topY], [edgeX,cornerY-arc*ry], [edgeX,cornerY],
          [edgeX,cornerY+(reachY-cornerY)*.4], [1,reachY-(reachY-cornerY)*.4], [1,reachY]
        ];
        if(kind===1||kind===3)points.reverse();
        return {start:p(...points[0]),path:[1,4,7].map(i=>'C '+points.slice(i,i+3).map(point=>p(...point)).join(' ')).join(' '),reachX,reachY};
      };
      const surface=[0,3,2,1].map((kind,index)=>{const part=corner(kind);return `${index?'L':'M'} ${part.start} ${part.path}`;}).join(' ')+' Z';
      const key='pharos-idea-'+index,regions=wrapRegions(art,key,w,h,12,surface);
      const fill=art.querySelector('.pharos-idea-surface');fill.setAttribute('d',surface);fill.setAttribute('clip-path',regions.boxClip);
      const border=art.querySelector('.pharos-idea-border');border.setAttribute('d',regions.box);border.setAttribute('clip-path',regions.contourClip);
      const ribbons=art.querySelector(':scope > g');ribbons.replaceChildren();ribbons.setAttribute('mask',regions.outsideMask);
      const defs=art.querySelector('defs');defs.querySelectorAll('[data-ribbon-paint]').forEach(element=>element.remove());
      const add=(tag,attrs,parent)=>{const element=document.createElementNS('http://www.w3.org/2000/svg',tag);Object.entries(attrs).forEach(([name,value])=>element.setAttribute(name,value));parent.append(element);return element;};
      corners.forEach((kind,ribbon)=>{
        const {reachX,reachY}=corner(kind),style=styles[ribbon],angle=style.phase*Math.PI/180;
        const cx=reachX/2,cy=reachY/2,radius=Math.hypot(reachX,reachY)/2;
        const gradient=(name,start,end)=>add('linearGradient',{id:key+'-'+name+'-'+kind,'data-ribbon-paint':'',gradientUnits:'userSpaceOnUse',x1:start[0],y1:start[1],x2:end[0],y2:end[1]},defs);
        // Color direction varies independently of the taper, so every ribbon fades at its tips.
        const paint=gradient('color',orient(kind,cx-radius*Math.cos(angle),cy-radius*Math.sin(angle)),orient(kind,cx+radius*Math.cos(angle),cy+radius*Math.sin(angle)));
        [['0','var(--pharos-aqua)'],['.48','var(--pharos-violet)'],['1','var(--pharos-gold)']].forEach(([offset,color])=>add('stop',{offset,'stop-color':color},paint));
        const fade=gradient('fade',orient(kind,reachX,1),orient(kind,1,reachY));
        [[0,0],[.2,1],[.8,1],[1,0]].forEach(([offset,opacity])=>add('stop',{offset,'stop-color':'white','stop-opacity':opacity},fade));
        const taperId=key+'-taper-'+kind;
        const taper=add('mask',{id:taperId,'data-ribbon-paint':'',maskUnits:'userSpaceOnUse',x:-6,y:-6,width:w+12,height:h+12},defs);
        add('rect',{x:-6,y:-6,width:w+12,height:h+12,fill:'url(#'+fade.id+')'},taper);
        const group=add('g',{class:'pharos-idea-ribbon','data-corner':kind,mask:'url(#'+taperId+')'},ribbons);
        for(let strand=0;strand<3;strand++){
          const part=corner(kind,strand*1.05);
          add('path',{d:`M ${part.start} ${part.path}`,stroke:'url(#'+paint.id+')'},group);
        }
      });
    });
  }
  const wrapIdea=index=>((index%ideas.length)+ideas.length)%ideas.length;
  const readingIdeas=()=>{const center=ideaStride?wrapIdea(Math.round(ideaViewport.scrollLeft/ideaStride)):ideaCenter;return Array.from({length:ideaCount},(_,index)=>ideaOrder[wrapIdea(center-Math.floor(ideaCount/2)+index)]);};
  function buildIdeas(){
    if(ideaRow.children.length)return;
    for(let slot=0;slot<ideas.length*3;slot++){
      const id=ideaOrder[slot%ideas.length],button=find('#pharos-suggestion-template').content.firstElementChild.cloneNode(true);
      button.dataset.idea=id;button.dataset.slot=slot;button.dataset.corners=ideaCorners[id].join(',');
      button.querySelector('.pharos-idea-text').textContent=ideas[id];
      button.addEventListener('click',()=>{
        if(button.hasAttribute('data-xray')){moveIdeas(Number(button.dataset.slot)-(ideas.length+ideaCenter),true);return;}
        stop();closePicker();question.value=ideas[id];autoFit();drawEdge();saveChat();question.focus();question.setSelectionRange(question.value.length,question.value.length);
      });
      ideaRow.append(button);
    }
  }
  function ideaHeld(){return frozenAt!==null||ideaPaused||suggestions.hidden||document.hidden||pending||question.value.trim()||!picker.hidden||!chatHistory.hidden||consent.open||ideaMoving||ideaViewport.matches(':hover,:focus-within')||matchMedia('(prefers-reduced-motion: reduce)').matches;}
  function updateIdeaClock(){
    const held=ideaHeld();find('.pharos-idea-countdown-label').textContent=held?(ideaPaused||frozenAt!==null||matchMedia('(prefers-reduced-motion: reduce)').matches?'Paused':'Waiting'):'More in '+Math.ceil((ideaCycleMs-ideaElapsed)/1000)+'s';
    find('.pharos-idea-countdown-track i').style.transform=`scaleX(${Math.max(0,1-ideaElapsed/ideaCycleMs)})`;
    const play=find('.pharos-idea-play');play.textContent=ideaPaused?'\u25b6 Play':'\u2161 Pause';play.setAttribute('aria-label',ideaPaused?'Play suggestions':'Pause suggestions');play.setAttribute('aria-pressed',String(ideaPaused));
  }
  function markReading(){
    const at=ideaStride?Math.round(ideaViewport.scrollLeft/ideaStride):ideas.length+ideaCenter;
    const first=at-Math.floor(ideaCount/2),last=at+Math.floor(ideaCount/2);
    [...ideaRow.children].forEach((button,slot)=>{
      const reading=slot>=first&&slot<=last;
      button.toggleAttribute('data-reading',reading);button.toggleAttribute('data-xray',!reading);
      // Only this reading area and the two visible peeks participate in keyboard navigation.
      button.inert=slot<first-1||slot>last+1;
      button.setAttribute('aria-label',reading?ideas[Number(button.dataset.idea)]:'Bring '+ideas[Number(button.dataset.idea)]+' into view');
    });
  }
  function settleIdeas(){
    if(!ideaStride)return;
    clearTimeout(ideaScrollEnd);ideaScrollEnd=null;
    const at=Math.round(ideaViewport.scrollLeft/ideaStride);ideaCenter=wrapIdea(at);ideaPage=Math.floor(ideaCenter/3);
    ideaViewport.scrollLeft=(ideas.length+ideaCenter)*ideaStride;
    ideaMoving=false;ideaMotion=null;ideaViewport.removeAttribute('data-moving');ideaViewport.style.scrollSnapType='';markReading();updateIdeaClock();
  }
  function renderIdeas() {
    if(suggestions.hidden)return;
    buildIdeas();const width=ideaViewport.clientWidth;if(!width)return;
    ideaCount=width>=600?3:1;
    const cardWidth=(width-2*(ideaCount===3?44:28)-(ideaCount-1)*14)/ideaCount;
    const size=width+':'+cardWidth;
    if(size!==ideaSize){
      if(ideaMotion){ideaMotion.cancel();ideaMotion=null;}
      ideaSize=size;ideaMoving=false;ideaViewport.removeAttribute('data-moving');ideaViewport.style.scrollSnapType='';ideaStride=cardWidth+14;
      suggestions.style.setProperty('--pharos-idea-width',cardWidth+'px');suggestions.style.setProperty('--pharos-idea-side',(width-cardWidth)/2+'px');
      ideaViewport.scrollLeft=(ideas.length+ideaCenter)*ideaStride;
    }
    drawIdeas();markReading();updateIdeaClock();
  }
  function runIdeaMotion(){
    const motion=ideaMotion;if(!motion||frozenAt!==null)return;
    motion.started=performance.now();
    const step=()=>{
      if(ideaMotion!==motion||frozenAt!==null)return;
      const elapsed=motion.elapsedMs+performance.now()-motion.started,t=Math.min(1,elapsed/motion.duration),ease=1-Math.pow(1-t,3);
      ideaViewport.scrollLeft=(motion.from+(motion.to-motion.from)*ease)*ideaStride;markReading();
      if(t<1)motion.frame=requestAnimationFrame(step);else settleIdeas();
    };
    motion.frame=requestAnimationFrame(step);
  }
  function pauseIdeaMotion(){
    if(!ideaMotion)return;
    if(frozenAt===null)ideaMotion.elapsedMs=Math.min(ideaMotion.duration,ideaMotion.elapsedMs+performance.now()-ideaMotion.started);
    cancelAnimationFrame(ideaMotion.frame);
  }
  function moveIdeas(offset,user=false){
    if(!ideaStride||!offset)return;
    if(ideaMotion){ideaMotion.cancel();ideaMotion=null;}
    clearTimeout(ideaScrollEnd);ideaScrollEnd=null;
    const from=ideaViewport.scrollLeft,to=from+offset*ideaStride;
    ideaElapsed=0;ideaLastTick=Date.now();
    if(frozenAt!==null||matchMedia('(prefers-reduced-motion: reduce)').matches){ideaViewport.scrollLeft=to;settleIdeas();return;}
    // Match the catalog's slower timed glide and quicker user-requested movement.
    const duration=user?1020:2040;ideaMoving=true;ideaViewport.setAttribute('data-moving','');
    ideaMotion={from:from/ideaStride,to:to/ideaStride,duration,elapsedMs:0,frame:null,started:performance.now(),cancel(){cancelAnimationFrame(this.frame);ideaMoving=false;}};
    ideaViewport.style.scrollSnapType='none';runIdeaMotion();updateIdeaClock();
    if(user)find('.pharos-idea-announcement').textContent='Showing more prompt suggestions.';
  }
  function rotateIdeas() {moveIdeas(ideaCount,false);}
  function advanceIdeas(now=Date.now()) {
    const delta = Math.max(0,now-ideaLastTick);ideaLastTick=now;
    if(!ideaHeld()){ideaElapsed+=delta;if(ideaElapsed>=ideaCycleMs)rotateIdeas();}
    updateIdeaClock();
  }
  const ideaTimer=setInterval(advanceIdeas,250);
  find('.pharos-more-ideas').addEventListener('click',()=>moveIdeas(Math.ceil(ideaCount/2),true));
  find('.pharos-idea-previous').addEventListener('click',()=>moveIdeas(-Math.ceil(ideaCount/2),true));
  find('.pharos-idea-play').addEventListener('click',()=>{advanceIdeas();ideaPaused=!ideaPaused;ideaLastTick=Date.now();updateIdeaClock();});
  find('.pharos-idea-refresh').addEventListener('click',()=>{
    if(ideaMotion){ideaMotion.cancel();ideaMotion=null;}clearTimeout(ideaScrollEnd);ideaScrollEnd=null;ideaMoving=false;ideaViewport.removeAttribute('data-moving');
    ideaOrder=[...ideaOrder.slice(3),...ideaOrder.slice(0,3)].reverse();ideaCenter=1;ideaPage=0;ideaElapsed=0;ideaLastTick=Date.now();ideaSize='';ideaRow.replaceChildren();renderIdeas();
    find('.pharos-idea-announcement').textContent='Prompt suggestions refreshed.';
  });
  ideaViewport.addEventListener('keydown',event=>{if(['ArrowLeft','ArrowRight'].includes(event.key)&&!event.altKey&&!event.metaKey&&!event.ctrlKey&&!event.shiftKey){event.preventDefault();moveIdeas((event.key==='ArrowLeft'?-1:1)*Math.ceil(ideaCount/2),true);}});
  ideaViewport.addEventListener('scroll',()=>{
    markReading();if(ideaMotion||frozenAt!==null)return;
    ideaMoving=true;clearTimeout(ideaScrollEnd);ideaScrollEnd=setTimeout(settleIdeas,150);
  },{passive:true});
  function interruptIdeaSlide(){if(ideaMotion){ideaMotion.cancel();ideaMotion=null;ideaViewport.removeAttribute('data-moving');ideaViewport.style.scrollSnapType='';settleIdeas();}ideaElapsed=0;ideaLastTick=Date.now();}
  ideaViewport.addEventListener('pointerdown',interruptIdeaSlide);
  ideaViewport.addEventListener('wheel',interruptIdeaSlide,{passive:true});
  const ideaObserver=new ResizeObserver(renderIdeas);ideaObserver.observe(ideaViewport);
  function sizePicker() {
    if (picker.hidden) return;
    ai.style.setProperty('--pharos-picker-width',Math.max(0,find('.pharos-ai-inner').clientWidth - 32) + 'px');
    ai.style.setProperty('--pharos-picker-space',picker.getBoundingClientRect().height + 18 + 'px');
  }
  function closePicker(focus = false) {
    picker.hidden = true;
    ai.classList.remove('pharos-picker-open');
    pickerTrigger.setAttribute('aria-expanded','false');
    if (focus) pickerTrigger.focus();
  }
  function showPickerPage(page,focus = true) {
    if(turns.length)return;
    pickerPage = page;
    find('.pharos-picker-root').hidden = !!page;
    find('.pharos-picker-submenu').hidden = !page;
    if (page) {
      const labels = {provider:'CLI',model:'Model',effort:'Effort'};
      find('.pharos-picker-back span').textContent = labels[page];
      const values = page === 'provider' ? providers.map(p=>p.id) : page === 'model' ? Object.keys(providerModels[choices.provider]||{}) : effortChoices();
      const options = find('.pharos-picker-options');
      options.replaceChildren();
      values.forEach(value => {
        const button = document.createElement('button');
        button.type = 'button';button.className = 'pharos-picker-row pharos-picker-choice cursor-interaction';
        button.setAttribute('aria-pressed',String(choices[page] === value));
        const label = document.createElement('span');label.textContent = page === 'model' ? providerModels[choices.provider][value] : value;
        const check = find('#pharos-check-icon').content.firstElementChild.cloneNode(true);
        button.append(label,check);
        button.addEventListener('click',async() => {
          if(turns.length)return;
          try{if(page==='provider'){button.disabled=true;label.textContent='Loading '+value+' models…';await loadModels(value);}}catch(error){button.disabled=false;label.textContent=value;report(error);return;}
          if(disposed||turns.length||openingSession!==null)return;
          stop(); choices[page] = value;
          if (page === 'provider') Object.assign(choices,defaultFor(value));
          if(page==='model'&&!effortChoices().includes(choices.effort))choices.effort='Default';
          render();saveChat();showPickerPage(null,false);
          find('[data-picker="' + page + '"]').focus();
        });
        options.append(button);
      });
    }
    sizePicker();
    if (focus) (page ? find('.pharos-picker-back') : find('[data-picker]')).focus();
  }
  function stop(cancel = true) {
    const turn=pending&&turns.find(turn=>turn.id===pending.turnId);
    if(turn)turn.elapsedMs=Math.max(0,(frozenAt??Date.now())-pending.started);
    if(pending&&cancel){pending.controller?.abort();cancelQueue=api('cancel',{id:activeSessionId}).catch(report);}
    serial++; pending = null;
    clearInterval(ticker);clearTimeout(pollTimer);pollTimer=null;ticker = null;
    if(turn&&cancel){turn.status='canceled';turn.reply='Request canceled. Your question is still in this conversation.';updateTurn(turn);}
    syncComposer();
    if(turn)saveChat(false);
  }
  function changeMode(next) {
    stop();closePicker();closeHistory();closeModals(); mode = next; regularOverride = false;
    render(); persist();
  }
  find('.pharos-try').addEventListener('click', () => openModal(consent));
  find('.pharos-dismiss').addEventListener('click', () => {invitationDismissed=true;render();persist();find('.home-search-trigger')?.focus();});
  find('.pharos-cancel').addEventListener('click', deferConsent);
  consent.addEventListener('cancel',event=>{event.preventDefault();deferConsent();});
  find('.pharos-never').addEventListener('click',()=>{showInvitation=false;invitationDismissed=false;changeMode('declined');saveAccessPreference();find('.home-search-trigger')?.focus();});
  find('.pharos-enable').addEventListener('click',async()=>{
    const button=find('.pharos-enable');button.disabled=true;
    try{
      await api('settings',{enabled:true,showInvitation,consent:true});accessEnabled=true;
      const provider=connectProvider.value;await loadModels(provider);
      if(!turns.length)Object.assign(choices,defaultSelection?.provider===provider?defaultSelection:defaultFor(provider));
      invitationDismissed=false;changeMode('enabled');visitChanged();window.dispatchEvent(new CustomEvent('pharos:ask-config',{detail:root}));await loadChatHistory();question.focus();
    }catch(error){report(error);}finally{button.disabled=false;}
  });

  function useRegular() {stop();closePicker();closeHistory(); regularOverride = true;render(); find('.home-search-trigger')?.focus();}
  function retryWithDifferentModel() {
    const draft=askRetryDraft({turns,draft:question.value});
    clearChat();question.value=draft;autoFit();saveChat();render();openPicker();
  }
  find('.pharos-search-mode').addEventListener('click', useRegular);
  find('.pharos-ai-mode').addEventListener('click', () => {regularOverride = false; stop(); render(); find('#pharos-question').focus();});
  function openPicker() {
    if(pickerTrigger.disabled)return;
    closeHistory();
    picker.hidden = false;ai.classList.add('pharos-picker-open');pickerTrigger.setAttribute('aria-expanded','true');showPickerPage(null);
  }
  pickerTrigger.addEventListener('click',() => {
    if (!picker.hidden) {closePicker();return;}
    openPicker();
  });
  find('.pharos-save-default').addEventListener('click',async()=>{try{await api('settings',{selection:copySelection(choices)});defaultSelection=copySelection(choices);render();window.dispatchEvent(new CustomEvent('pharos:ask-config',{detail:root}));}catch(error){report(error);}});
  product.querySelectorAll('[data-picker]').forEach(button => button.addEventListener('click',() => showPickerPage(button.dataset.picker)));
  find('.pharos-picker-back').addEventListener('click',() => {const previous = pickerPage;showPickerPage(null,false);find('[data-picker="' + previous + '"]').focus();});
  listen(document,'pointerdown',event => {if(!find('.pharos-picker-anchor').contains(event.target))closePicker();});
  find('.pharos-picker-anchor').addEventListener('focusout',event => {if(event.relatedTarget && !find('.pharos-picker-anchor').contains(event.relatedTarget))closePicker();});
  product.addEventListener('keydown',event => {if(event.key === 'Escape' && !picker.hidden){event.preventDefault();closePicker(true);}});
  historyTrigger.addEventListener('click',()=>{if(chatHistory.hidden)openHistory();else closeHistory();});
  find('.pharos-history-close').addEventListener('click',()=>closeHistory(true));
  find('.pharos-clear-chat').addEventListener('click',()=>clearChat());
  listen(document,'pointerdown',event=>{if(!find('.pharos-history-anchor').contains(event.target))closeHistory();});
  find('.pharos-history-anchor').addEventListener('focusout',event=>{if(event.relatedTarget&&!find('.pharos-history-anchor').contains(event.relatedTarget))closeHistory();});
  product.addEventListener('keydown',event=>{if(event.key==='Escape'&&!chatHistory.hidden){event.preventDefault();closeHistory(true);}});
  listen(window,'resize',() => {if(!ai.hidden)autoFit();drawEdge();renderIdeas();sizePicker();sizeHistory();sizeModal();});
  const edgeObserver=new ResizeObserver(() => {if(product.getBoundingClientRect().width && !ai.hidden){autoFit();drawEdge();sizePicker();}});edgeObserver.observe(find('.pharos-ai-frame'));
  async function startRequest(turn,retry=true){
    stop();flushDraft();const request=++serial;
    turn.status='loading';turn.reply='';turn.cards=[];delete turn.clarification;turn.elapsedMs=0;
    turn.message='Starting '+turn.cli+'…';
    const controller=new AbortController();pending={request,turnId:turn.id,started:Date.now(),controller,edited:false};
    turns.forEach(updateTurn);syncComposer();writeHistory();
    ticker=setInterval(()=>tickRequest(request),1000);
    try {
      await writeQueue;await cancelQueue;
      const session=await api('send',{id:activeSessionId,turnID:turn.id,prompt:turn.prompt,draft:question.value,selection:copySelection(choices),retry,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone},controller.signal);
      if(pending?.request!==request)return;
      activeCreatedAt=session.createdAt;activeUpdatedAt=session.updatedAt;const edited=pending.edited;const final=session.conversation.find(t=>t.id===turn.id);Object.assign(turn,final);stop(false);updateTurn(turn);syncComposer();sessions=sessionRecords();
      window.dispatchEvent(new Event('pharos:data-refresh'));
      if(askShouldNavigate({cards:turn.cards,draft:question.value,enabled:mode==='enabled',regular:regularOverride,edited}))window.pharosNavigate?.(turn.cards[0].href);
    }catch(error){
      if(pending?.request!==request)return;
      // A lost response is not permission to repeat a billable request.
      try{const saved=await api('session?id='+encodeURIComponent(activeSessionId));if(pending?.request!==request)return;const actual=saved.conversation.find(t=>t.id===turn.id);if(actual){Object.assign(turn,actual);stop(false);updateTurn(turn);syncComposer();if(turn.status==='loading')watchRequest(turn);return;}}catch{}
      if(pending?.request!==request)return;stop(false);turn.status='error';turn.reply=error.message||String(error);updateTurn(turn);syncComposer();
    }
  }
  function watchRequest(turn){
    const request=++serial,controller=new AbortController();
    pending={request,turnId:turn.id,started:turn.startedAt||Date.now(),controller,edited:true};syncComposer();ticker=setInterval(()=>tickRequest(request),1000);
    const poll=async()=>{try{const saved=await api('session?id='+encodeURIComponent(activeSessionId),undefined,controller.signal);if(pending?.request!==request)return;const actual=saved.conversation.find(t=>t.id===turn.id);if(actual&&actual.status!=='loading'){Object.assign(turn,actual);activeUpdatedAt=saved.updatedAt;stop(false);updateTurn(turn);syncComposer();sessions=sessionRecords();return;}}catch(error){if(pending?.request!==request)return;stop(false);turn.status='error';turn.reply='Could not reconnect to this question. Reopen the chat to check its result.';updateTurn(turn);syncComposer();return;}if(pending?.request===request)pollTimer=setTimeout(poll,800);};
    void poll();
  }
  function tickRequest(request){if(pending?.request!==request)return;const turn=turns.find(t=>t.id===pending.turnId);if(!turn)return;turn.elapsedMs=Date.now()-pending.started;turn.message=(turn.elapsedMs>=10000?'Still waiting for ':'Waiting for ')+turn.cli+'…';updateTurn(turn);}
  function submitQuestion(value,preserveDraft=false){
    if(pending||openingSession!==null||!accessEnabled||!value.trim()||!choices.model)return;
    const prompt=value.trim();closePicker();closeHistory();
    if(!activeSessionId){sessionLoad++;activeSessionId=crypto.randomUUID();activeCreatedAt=Date.now();}
    syncChatURL(activeSessionId);threadFollowing=true;
    const turn={id:crypto.randomUUID(),prompt,cli:choices.provider,model:choices.model,modelLabel:modelLabel(),effort:choices.effort,status:'loading',message:'',reply:'',cards:[],elapsedMs:0};
    turns.push(turn);appendTurn(turn);if(!preserveDraft)question.value='';clarificationDraft='';autoFit();startRequest(turn,false);
  }
  function ask(){submitQuestion(question.value);}
  find('.pharos-send').addEventListener('click', ask);
  ai.addEventListener('submit', event => {event.preventDefault(); ask();});
  find('#pharos-question').addEventListener('keydown', event => {if(event.key === 'Enter' && !event.shiftKey && !event.isComposing){event.preventDefault();if(!pending)ask();}});
  question.addEventListener('input', () => {if(pending)pending.edited=true;question.setCustomValidity('');autoFit();saveChat();});
  function syncTheme(){appearance=document.documentElement.dataset.theme==='dark'?'dark':'light';drawEdge();drawIdeas();}
  const themeObserver=new MutationObserver(syncTheme);themeObserver.observe(document.documentElement,{attributes:true,attributeFilter:['data-theme','style']});
  const configChanged=event=>{if(event.detail!==root)void refreshStatus().then(visitChanged).catch(report);};listen(window,'pharos:ask-config',configChanged);
  const visitChanged=event=>{
    if(!accessEnabled)return;
    const entry=event?.detail?.entry;
    let visits;
    try{
      visits=entry?[entry]:JSON.parse(localStorage.getItem('pharos-history-v1')||'[]').map(entry=>({...entry,title:window.historyTitle?.(entry.url,entry.page)||entry.title}));
    }catch{return;}
    visits=visits.map(entry=>({...entry,title:String(entry.title||'Visited page').slice(0,500)}));
    const input={visits,replaces:event?.detail?.replaced||null,reconcile:!entry};
    void askWriteVisit(()=>api('visits',input)).catch(report);
  };
  listen(window,'pharos:visit',visitChanged);
  const routeChanged=()=>{sessionLoad++;openingSession=null;flushDraft();if(!['/','/home'].includes(location.pathname))stop();else if(accessEnabled){const id=new URL(location.href).searchParams.get('ask_chat');if(id&&id!==activeSessionId)void openSession(id);else if(!id&&activeSessionId)clearChat(false);else{if(id)regularOverride=false;render();}}};
  listen(window,'pharos:route',routeChanged);
  listen(window,'pagehide',()=>{if(draftPending){const payload=draftPending;draftPending=null;void fetch('/api/ask/session',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload),keepalive:true});}});
  render();syncTheme();void refreshStatus().then(visitChanged).catch(report);
  return ()=>{disposed=true;sessionLoad++;stop();clearTimeout(draftTimer);cancelAnimationFrame(threadLayoutFrame);threadObserver.disconnect();clearInterval(ideaTimer);clearTimeout(ideaScrollEnd);ideaMotion?.cancel();edgeObserver.disconnect();ideaObserver.disconnect();clearTimeout(historyTimer);themeObserver.disconnect();cleanup.forEach(fn=>fn());window.removeEventListener('pharos:ask-config',configChanged);window.removeEventListener('pharos:visit',visitChanged);window.removeEventListener('pharos:route',routeChanged);};
};
